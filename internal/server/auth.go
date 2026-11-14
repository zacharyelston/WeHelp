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

// issueTokens mints an access token and a refresh token. deviceID binds the
// refresh token to an enrolled device (nil = unbound).
func (s *Server) issueTokens(ctx context.Context, tx pgx.Tx, userID, tenantID uuid.UUID, role string, deviceID *uuid.UUID) (tokenResponse, error) {
	raw, hash, err := auth.NewRefreshToken()
	if err != nil {
		return tokenResponse{}, err
	}
	_, err = tx.Exec(ctx,
		`insert into refresh_tokens (user_id, tenant_id, token_hash, expires_at, device_id) values ($1, $2, $3, now() + $4::interval, $5)`,
		userID, tenantID, hash, s.cfg.RefreshTTL.String(), deviceID)
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
	// Patients have no password: staff add them and they pair a phone in
	// the office (#37).
	if req.Role != "provider" {
		writeErr(w, http.StatusBadRequest, "role must be provider; patients are added by staff and pair their phone in the office")
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

	resp, err := s.issueTokens(ctx, tx, userID, tenantID, req.Role, nil)
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

// dummyHash lets Login spend the same time on accounts with no password.
var dummyHash = func() string {
	h, err := auth.HashPassword("wehelp-no-password-dummy")
	if err != nil {
		panic(err)
	}
	return h
}()

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
	var hash *string
	var role string
	err = tx.QueryRow(ctx, `
		select u.id, u.tenant_id, u.password_hash, u.role
		from users u join tenants t on t.id = u.tenant_id
		where t.name = $1 and u.email = $2`, req.Tenant, req.Email).
		Scan(&userID, &tenantID, &hash, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		_, _ = auth.VerifyPassword(req.Password, dummyHash)
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	if hash == nil {
		// No password (patients): run a dummy check so timing doesn't reveal it.
		_, _ = auth.VerifyPassword(req.Password, dummyHash)
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	ok, err := auth.VerifyPassword(req.Password, *hash)
	if err != nil || !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	// Patients never sign in with a password, even one left from before
	// pairing existed (#37).
	if role == "patient" {
		writeErr(w, http.StatusUnauthorized, "patients sign in by pairing their phone at the practice")
		return
	}

	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: tenantID, ActorID: &userID,
		Action: "auth.login", ResourceType: "user", ResourceID: userID.String(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}

	resp, err := s.issueTokens(ctx, tx, userID, tenantID, role, nil)
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
