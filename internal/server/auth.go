package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/auth"
)

type ctxKey int

const ctxIdentity ctxKey = iota

// Identity is the authenticated principal resolved from an access token.
type Identity struct {
	UserID   uuid.UUID
	TenantID uuid.UUID
	Role     string
}

func identityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxIdentity).(Identity)
	return id, ok
}

// authenticate requires a valid Bearer access token.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := auth.ParseAccessToken(strings.TrimPrefix(h, "Bearer "), s.jwtSecret)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		uid, _ := uuid.Parse(claims.Subject)
		ctx := context.WithValue(r.Context(), ctxIdentity, Identity{
			UserID:   uid,
			TenantID: claims.TenantID,
			Role:     claims.Role,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func (s *Server) issueTokens(ctx context.Context, tx pgx.Tx, userID, tenantID uuid.UUID, role string) (tokenResponse, error) {
	raw, hash, err := auth.NewRefreshToken()
	if err != nil {
		return tokenResponse{}, err
	}
	_, err = tx.Exec(ctx,
		`insert into refresh_tokens (user_id, tenant_id, token_hash, expires_at) values ($1, $2, $3, now() + $4::interval)`,
		userID, tenantID, hash, s.cfg.RefreshTTL.String())
	if err != nil {
		return tokenResponse{}, err
	}
	access, err := auth.IssueAccessToken(userID, tenantID, role, s.jwtSecret, s.cfg.AccessTTL)
	if err != nil {
		return tokenResponse{}, err
	}
	return tokenResponse{
		AccessToken:  access,
		RefreshToken: raw,
		TokenType:    "Bearer",
		ExpiresIn:    int(s.cfg.AccessTTL.Seconds()),
	}, nil
}

type registerRequest struct {
	Tenant      string `json:"tenant"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
}

// Register creates a tenant (if new) and user. Self-serve registration is
// restricted to provider and patient roles; admins are seeded out of band.
func (s *Server) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Tenant = strings.TrimSpace(req.Tenant)
	req.Email = strings.TrimSpace(req.Email)
	if req.Tenant == "" || !strings.Contains(req.Email, "@") {
		writeErr(w, http.StatusBadRequest, "tenant and valid email required")
		return
	}
	if len(req.Password) < 12 {
		writeErr(w, http.StatusBadRequest, "password must be at least 12 characters")
		return
	}
	if req.Role != "provider" && req.Role != "patient" {
		writeErr(w, http.StatusBadRequest, "role must be provider or patient")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash failed")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	var tenantID uuid.UUID
	err = tx.QueryRow(ctx,
		`insert into tenants (name) values ($1)
		 on conflict (name) do update set name = excluded.name
		 returning id`, req.Tenant).Scan(&tenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tenant failed")
		return
	}

	var userID uuid.UUID
	err = tx.QueryRow(ctx,
		`insert into users (tenant_id, email, password_hash, role, display_name)
		 values ($1, $2, $3, $4, $5) returning id`,
		tenantID, req.Email, hash, req.Role, req.DisplayName).Scan(&userID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeErr(w, http.StatusConflict, "email already registered in this tenant")
			return
		}
		writeErr(w, http.StatusInternalServerError, "user failed")
		return
	}

	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: tenantID, ActorID: &userID,
		Action: "auth.register", ResourceType: "user", ResourceID: userID.String(),
		Detail: json.RawMessage(`{"email":"` + req.Email + `","role":"` + req.Role + `"}`),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}

	resp, err := s.issueTokens(ctx, tx, userID, tenantID, req.Role)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

type loginRequest struct {
	Tenant   string `json:"tenant"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	var userID, tenantID uuid.UUID
	var hash, role string
	err = tx.QueryRow(ctx, `
		select u.id, u.tenant_id, u.password_hash, u.role
		from users u join tenants t on t.id = u.tenant_id
		where t.name = $1 and u.email = $2`, req.Tenant, req.Email).
		Scan(&userID, &tenantID, &hash, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	ok, err := auth.VerifyPassword(req.Password, hash)
	if err != nil || !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: tenantID, ActorID: &userID,
		Action: "auth.login", ResourceType: "user", ResourceID: userID.String(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}

	resp, err := s.issueTokens(ctx, tx, userID, tenantID, role)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Refresh rotates a refresh token and returns a new token pair.
func (s *Server) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	hash, err := auth.HashRefreshToken(req.RefreshToken)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	var tokenID, userID, tenantID uuid.UUID
	var role string
	err = tx.QueryRow(ctx, `
		update refresh_tokens rt set revoked_at = now()
		from users u
		where rt.user_id = u.id
		  and rt.token_hash = $1 and rt.revoked_at is null and rt.expires_at > now()
		returning rt.id, rt.user_id, rt.tenant_id, u.role`, hash).
		Scan(&tokenID, &userID, &tenantID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "refresh failed")
		return
	}

	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: tenantID, ActorID: &userID,
		Action: "auth.refresh", ResourceType: "refresh_token", ResourceID: tokenID.String(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}

	resp, err := s.issueTokens(ctx, tx, userID, tenantID, role)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Me returns the authenticated identity — the smoke test for the middleware.
func (s *Server) Me(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{
		"user_id":   id.UserID.String(),
		"tenant_id": id.TenantID.String(),
		"role":      id.Role,
	})
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
