package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/auth"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

type ctxKey int

const ctxIdentity ctxKey = iota

// Identity is the authenticated principal resolved from an access token.
type Identity struct {
	UserID   uuid.UUID
	TenantID uuid.UUID
	Role     string
	DeviceID *uuid.UUID // the enrolled device, for device-bound sessions
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
			s.authenticateStaffCookie(next, w, r)
			return
		}
		claims, err := auth.ParseAccessToken(strings.TrimPrefix(h, "Bearer "), s.jwtSecret)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		uid, _ := uuid.Parse(claims.Subject)
		// Device-bound sessions stop working the moment the device is revoked
		// (#10), not when the access token expires. Patient sessions are always
		// device-bound.
		if claims.DeviceID == nil && claims.Role == "patient" {
			writeErr(w, http.StatusUnauthorized, "patient sessions must be device-bound")
			return
		}
		if claims.DeviceID != nil {
			var active bool
			err := s.pool.QueryRow(r.Context(),
				`select revoked_at is null from devices where id = $1 and user_id = $2`, claims.DeviceID, uid).Scan(&active)
			if err != nil || !active {
				writeErr(w, http.StatusUnauthorized, "device revoked")
				return
			}
		}
		ctx := context.WithValue(r.Context(), ctxIdentity, Identity{
			UserID:   uid,
			TenantID: claims.TenantID,
			Role:     claims.Role,
			DeviceID: claims.DeviceID,
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
	access, err := auth.IssueAccessToken(userID, tenantID, role, deviceID, s.jwtSecret, s.cfg.AccessTTL)
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
	Code     string `json:"code"` // TOTP, required for staff (#38)
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

	// Same checks as staff web sign-in: lockout, no patients (they pair
	// their phone, #37), and TOTP for every staff account (#38).
	u, err := checkStaffPassword(ctx, tx, req.Tenant, req.Email, req.Password)
	if errors.Is(err, errBadPassword) {
		tx.Rollback(ctx)
		s.recordFailure(ctx, u, "password")
	}
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !u.TOTPEnabled {
		writeErr(w, http.StatusUnauthorized, "set up two-step sign-in in the staff web UI first")
		return
	}
	step, ok := checkTOTP(deref(u.TOTPSecret), req.Code, u.TOTPLastStep, time.Now())
	if !ok {
		tx.Rollback(ctx)
		s.recordFailure(ctx, u, "code")
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if _, err := tx.Exec(ctx, `update users set totp_last_step = $2, failed_logins = 0, locked_until = null where id = $1`,
		u.ID, step); err != nil {
		writeErr(w, http.StatusInternalServerError, "login failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: u.TenantID, ActorID: &u.ID,
		Action: "auth.login", ResourceType: "user", ResourceID: u.ID.String(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}

	resp, err := s.issueTokens(ctx, tx, u.ID, u.TenantID, u.Role, nil)
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

// authenticateStaffCookie authenticates a staff browser session (#38): an
// active session cookie, on the office LAN, with the CSRF header on any
// change.
func (s *Server) authenticateStaffCookie(next http.Handler, w http.ResponseWriter, r *http.Request) {
	if tunnel.ViaRelay(r.Context()) {
		writeErr(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	sess, ok := s.lookupSession(r.Context(), r)
	if !ok || sess.State != "active" {
		writeErr(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	if !csrfOK(r, sess) {
		writeErr(w, http.StatusForbidden, "missing or invalid CSRF token")
		return
	}
	var role string
	if err := s.pool.QueryRow(r.Context(), `select role::text from users where id = $1`, sess.UserID).Scan(&role); err != nil {
		writeErr(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	ctx := context.WithValue(r.Context(), ctxIdentity, Identity{UserID: sess.UserID, TenantID: sess.TenantID, Role: role})
	next.ServeHTTP(w, r.WithContext(ctx))
}

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
