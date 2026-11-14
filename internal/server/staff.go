package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/auth"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

// Staff sign-in (#38). Staff use the web UI on the office LAN with a
// password plus TOTP. The browser holds a server-side session in a cookie
// (Secure, HttpOnly, SameSite=Strict) and sends the session's CSRF token in
// a header on every change. None of this is reachable through the Relay.

const (
	staffCookie    = "__Host-wehelp_staff"
	csrfHeader     = "X-CSRF-Token"
	sessionIdle    = 30 * time.Minute
	sessionMax     = 12 * time.Hour
	setupMax       = 10 * time.Minute
	lockAfter      = 5
	lockFor        = 15 * time.Minute
	totpPeriod     = 30
	errStaffSignIn = "invalid credentials"
)

// lanOnly hides a route from the Relay: requests that arrived on a Relay
// channel get a plain 404, decided by the listener (tunnel.ViaRelay is set
// by the tunnel's connection context), never by a header.
func lanOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tunnel.ViaRelay(r.Context()) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type staffUser struct {
	ID, TenantID uuid.UUID
	Role, Name   string
	Hash         *string
	TOTPSecret   *string
	TOTPEnabled  bool
	TOTPLastStep *int64
	FailedLogins int
	LockedUntil  *time.Time
	TenantName   string
}

// checkStaffPassword verifies a staff password, applying the lockout. It
// returns errStaffSignIn for every failure so the response is no oracle.
func checkStaffPassword(ctx context.Context, tx pgx.Tx, tenant, email, password string) (staffUser, error) {
	var u staffUser
	err := tx.QueryRow(ctx, `
		select u.id, u.tenant_id, u.role::text, u.display_name, u.password_hash, u.totp_secret,
		       u.totp_enabled_at is not null, u.totp_last_step, u.failed_logins, u.locked_until, t.name
		from users u join tenants t on t.id = u.tenant_id
		where t.name = $1 and u.email = $2 for update of u`, tenant, email).
		Scan(&u.ID, &u.TenantID, &u.Role, &u.Name, &u.Hash, &u.TOTPSecret, &u.TOTPEnabled, &u.TOTPLastStep,
			&u.FailedLogins, &u.LockedUntil, &u.TenantName)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (u.Hash == nil || u.Role == "patient")) {
		_, _ = auth.VerifyPassword(password, dummyHash)
		return u, errors.New(errStaffSignIn)
	}
	if err != nil {
		return u, err
	}
	if u.LockedUntil != nil && time.Now().Before(*u.LockedUntil) {
		_, _ = auth.VerifyPassword(password, dummyHash)
		return u, errors.New(errStaffSignIn)
	}
	ok, err := auth.VerifyPassword(password, *u.Hash)
	if err != nil || !ok {
		return u, errBadPassword
	}
	return u, nil
}

var errBadPassword = errors.New(errStaffSignIn)

// recordFailure counts a failed sign-in in its own transaction (the
// caller's is rolled back) and locks the account after lockAfter failures.
func (s *Server) recordFailure(ctx context.Context, u staffUser, reason string) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	var failed int
	if err := tx.QueryRow(ctx, `
		update users set failed_logins = failed_logins + 1,
			locked_until = case when failed_logins + 1 >= $2 then now() + $3::interval else locked_until end
		where id = $1 returning failed_logins`, u.ID, lockAfter, lockFor.String()).Scan(&failed); err != nil {
		return
	}
	detail := map[string]any{"reason": reason, "failed_logins": failed}
	if failed >= lockAfter {
		detail["locked_for"] = lockFor.String()
	}
	if audit.Record(ctx, tx, audit.Event{TenantID: u.TenantID, ActorID: &u.ID, Action: "staff.signin_failed",
		ResourceType: "user", ResourceID: u.ID.String(), Detail: mustJSON(detail)}) == nil {
		_ = tx.Commit(ctx)
	}
}

// checkTOTP accepts a 6-digit code for the current step ±1 that is newer
// than the last accepted one, and returns its step.
func checkTOTP(secret, code string, lastStep *int64, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	step := now.Unix() / totpPeriod
	for _, d := range []int64{-1, 0, 1} {
		st := step + d
		if lastStep != nil && st <= *lastStep {
			continue
		}
		want, err := totp.GenerateCodeCustom(secret, time.Unix(st*totpPeriod, 0), totp.ValidateOpts{
			Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return st, true
		}
	}
	return 0, false
}

type staffSession struct {
	ID, UserID, TenantID uuid.UUID
	State, CSRF          string
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) []byte {
	sum := sha256.Sum256([]byte(t))
	return sum[:]
}

// startSession creates a session and sets the cookie.
func startSession(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, u staffUser, state string) (staffSession, error) {
	token := randomToken(32)
	sess := staffSession{UserID: u.ID, TenantID: u.TenantID, State: state, CSRF: randomToken(24)}
	limit := sessionMax
	if state == "setup" {
		limit = setupMax
	}
	expires := time.Now().Add(limit)
	if err := tx.QueryRow(ctx, `
		insert into staff_sessions (token_hash, user_id, tenant_id, state, csrf_token, expires_at)
		values ($1, $2, $3, $4, $5, $6) returning id`,
		hashToken(token), u.ID, u.TenantID, state, sess.CSRF, expires).Scan(&sess.ID); err != nil {
		return sess, err
	}
	http.SetCookie(w, &http.Cookie{Name: staffCookie, Value: token, Path: "/", Expires: expires,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return sess, nil
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: staffCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// lookupSession resolves the cookie to a live session (idle and absolute
// limits) and slides its idle timer.
func (s *Server) lookupSession(ctx context.Context, r *http.Request) (staffSession, bool) {
	c, err := r.Cookie(staffCookie)
	if err != nil || c.Value == "" {
		return staffSession{}, false
	}
	var sess staffSession
	err = s.pool.QueryRow(ctx, `
		update staff_sessions set last_seen_at = now()
		where token_hash = $1 and revoked_at is null and expires_at > now()
		  and last_seen_at > now() - $2::interval
		returning id, user_id, tenant_id, state, csrf_token`, hashToken(c.Value), sessionIdle.String()).
		Scan(&sess.ID, &sess.UserID, &sess.TenantID, &sess.State, &sess.CSRF)
	return sess, err == nil
}

func csrfOK(r *http.Request, sess staffSession) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	got := r.Header.Get(csrfHeader)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) == 1
}

type staffLoginRequest struct {
	Tenant   string `json:"tenant"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

type staffMe struct {
	Status      string `json:"status"` // active, totp_setup
	UserID      string `json:"user_id"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	Practice    string `json:"practice"`
	CSRFToken   string `json:"csrf_token"`
}

// StaffLogin signs a staff member in with password + TOTP. A staff member
// with no TOTP yet gets a short 'setup' session that can only enroll it.
func (s *Server) StaffLogin(w http.ResponseWriter, r *http.Request) {
	var req staffLoginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
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
	u, err := checkStaffPassword(ctx, tx, strings.TrimSpace(req.Tenant), strings.TrimSpace(req.Email), req.Password)
	if errors.Is(err, errBadPassword) {
		tx.Rollback(ctx)
		s.recordFailure(ctx, u, "password")
	}
	if err != nil {
		writeErr(w, http.StatusUnauthorized, errStaffSignIn)
		return
	}

	state := "active"
	if !u.TOTPEnabled {
		state = "setup"
	} else {
		step, ok := checkTOTP(deref(u.TOTPSecret), req.Code, u.TOTPLastStep, time.Now())
		if !ok {
			tx.Rollback(ctx)
			s.recordFailure(ctx, u, "code")
			writeErr(w, http.StatusUnauthorized, errStaffSignIn)
			return
		}
		if _, err := tx.Exec(ctx, `update users set totp_last_step = $2 where id = $1`, u.ID, step); err != nil {
			writeErr(w, http.StatusInternalServerError, "sign-in failed")
			return
		}
	}
	if _, err := tx.Exec(ctx, `update users set failed_logins = 0, locked_until = null where id = $1`, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "sign-in failed")
		return
	}
	sess, err := startSession(ctx, tx, w, u, state)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "sign-in failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: u.TenantID, ActorID: &u.ID, Action: "staff.signin",
		ResourceType: "staff_session", ResourceID: sess.ID.String(), Detail: mustJSON(map[string]string{"state": state})}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	status := "active"
	if state == "setup" {
		status = "totp_setup"
	}
	writeJSON(w, http.StatusOK, staffMe{Status: status, UserID: u.ID.String(), Role: u.Role,
		DisplayName: u.Name, Practice: u.TenantName, CSRFToken: sess.CSRF})
}

// sessionFor requires a staff session in the given state, with CSRF.
func (s *Server) sessionFor(w http.ResponseWriter, r *http.Request, state string) (staffSession, bool) {
	sess, ok := s.lookupSession(r.Context(), r)
	if !ok || sess.State != state {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return sess, false
	}
	if !csrfOK(r, sess) {
		writeErr(w, http.StatusForbidden, "missing or invalid CSRF token")
		return sess, false
	}
	return sess, true
}

type totpSetupResponse struct {
	Secret     string `json:"secret"`
	OTPAuthURL string `json:"otpauth_url"`
	QRPNG      string `json:"qr_png"`
}

// StaffTOTPSetup creates a new TOTP secret for the signed-in staff member
// (setup session only) and returns it as a QR code for an authenticator app.
func (s *Server) StaffTOTPSetup(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFor(w, r, "setup")
	if !ok {
		return
	}
	ctx := r.Context()
	var email, practice string
	if err := s.pool.QueryRow(ctx, `select u.email::text, t.name from users u join tenants t on t.id = u.tenant_id where u.id = $1`,
		sess.UserID).Scan(&email, &practice); err != nil {
		writeErr(w, http.StatusInternalServerError, "setup failed")
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "WeHelp " + practice, AccountName: email,
		Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "setup failed")
		return
	}
	if _, err := s.pool.Exec(ctx, `update users set totp_secret = $2 where id = $1 and totp_enabled_at is null`,
		sess.UserID, key.Secret()); err != nil {
		writeErr(w, http.StatusInternalServerError, "setup failed")
		return
	}
	png, err := qrPNG(key.URL())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "qr failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, totpSetupResponse{Secret: key.Secret(), OTPAuthURL: key.URL(),
		QRPNG: base64.StdEncoding.EncodeToString(png)})
}

type codeRequest struct {
	Code string `json:"code"`
}

// StaffTOTPConfirm turns TOTP on once the staff member enters a valid code,
// then replaces the setup session with a full one.
func (s *Server) StaffTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFor(w, r, "setup")
	if !ok {
		return
	}
	var req codeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
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
	var u staffUser
	if err := tx.QueryRow(ctx, `
		select u.id, u.tenant_id, u.role::text, u.display_name, u.totp_secret, t.name
		from users u join tenants t on t.id = u.tenant_id where u.id = $1 and u.totp_enabled_at is null for update of u`,
		sess.UserID).Scan(&u.ID, &u.TenantID, &u.Role, &u.Name, &u.TOTPSecret, &u.TenantName); err != nil || u.TOTPSecret == nil {
		writeErr(w, http.StatusConflict, "start setup first")
		return
	}
	step, valid := checkTOTP(*u.TOTPSecret, req.Code, nil, time.Now())
	if !valid {
		writeErr(w, http.StatusUnauthorized, "that code didn't match; check the time on your phone and try again")
		return
	}
	if _, err := tx.Exec(ctx, `update users set totp_enabled_at = now(), totp_last_step = $2 where id = $1`, u.ID, step); err != nil {
		writeErr(w, http.StatusInternalServerError, "confirm failed")
		return
	}
	if _, err := tx.Exec(ctx, `update staff_sessions set revoked_at = now() where id = $1`, sess.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "confirm failed")
		return
	}
	full, err := startSession(ctx, tx, w, u, "active")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "confirm failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: u.TenantID, ActorID: &u.ID, Action: "staff.totp_enroll",
		ResourceType: "user", ResourceID: u.ID.String()}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, staffMe{Status: "active", UserID: u.ID.String(), Role: u.Role,
		DisplayName: u.Name, Practice: u.TenantName, CSRFToken: full.CSRF})
}

// StaffMe returns the signed-in staff member and the CSRF token, so the web
// UI can resume after a reload.
func (s *Server) StaffMe(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.lookupSession(r.Context(), r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	var me staffMe
	if err := s.pool.QueryRow(r.Context(), `
		select u.id::text, u.role::text, u.display_name, t.name from users u join tenants t on t.id = u.tenant_id
		where u.id = $1`, sess.UserID).Scan(&me.UserID, &me.Role, &me.DisplayName, &me.Practice); err != nil {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	me.Status, me.CSRFToken = "active", sess.CSRF
	if sess.State == "setup" {
		me.Status = "totp_setup"
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, me)
}

// StaffLogout ends the session.
func (s *Server) StaffLogout(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.lookupSession(r.Context(), r)
	clearCookie(w)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !csrfOK(r, sess) {
		writeErr(w, http.StatusForbidden, "missing or invalid CSRF token")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `update staff_sessions set revoked_at = now() where id = $1`, sess.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "logout failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: sess.TenantID, ActorID: &sess.UserID, Action: "staff.signout",
		ResourceType: "staff_session", ResourceID: sess.ID.String()}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	_ = tx.Commit(ctx)
	w.WriteHeader(http.StatusNoContent)
}
