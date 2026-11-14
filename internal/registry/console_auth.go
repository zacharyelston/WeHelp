package registry

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/staffauth"
)

// Console sign-in (#61): WeHelp support staff sign in with a password and an
// authenticator code, the same way practice staff do in the Back Office
// (#38). Sessions live server-side behind a __Host- cookie, writes need the
// CSRF header, and 5 failures lock the account for 15 minutes.

const (
	consoleCookie  = "__Host-wehelp_console"
	csrfHeader     = "X-CSRF-Token"
	sessionIdle    = 30 * time.Minute
	sessionMax     = 12 * time.Hour
	setupMax       = 10 * time.Minute
	lockAfter      = 5
	lockFor        = 15 * time.Minute
	errSignIn      = "invalid credentials"
	RoleSupport    = "support"
	RoleAdmin      = "registry-admin"
	minPasswordLen = 12
)

// dummyHash keeps a failed lookup as slow as a wrong password.
var dummyHash, _ = staffauth.HashPassword("not-a-real-password-just-timing")

// AddStaff creates a console account (CLI: wehelp-registry staff add).
func (s *Store) AddStaff(ctx context.Context, actor, email, name, role, password string) (uuid.UUID, error) {
	email, name = strings.TrimSpace(email), strings.TrimSpace(name)
	if err := checkEmail("email", email); err != nil || email == "" {
		return uuid.Nil, errors.Join(ErrInvalidInput, errors.New("a valid email is required"))
	}
	if name == "" {
		return uuid.Nil, errors.Join(ErrInvalidInput, errors.New("name is required"))
	}
	if role != RoleSupport && role != RoleAdmin {
		return uuid.Nil, errors.Join(ErrInvalidInput, errors.New("role must be support or registry-admin"))
	}
	if len(password) < minPasswordLen {
		return uuid.Nil, errors.Join(ErrInvalidInput, errors.New("password must be at least 12 characters"))
	}
	hash, err := staffauth.HashPassword(password)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `insert into console_staff (email, display_name, role, password_hash)
			values ($1, $2, $3, $4) returning id`, email, name, role, hash).Scan(&id)
		if isUnique(err) {
			return errors.Join(ErrDuplicate, errors.New("a staff account with that email"))
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, Event{Actor: actor, Action: "staff.add", ResourceType: "console_staff",
			ResourceID: id.String(), Detail: map[string]string{"role": role}})
	})
	return id, err
}

type consoleStaff struct {
	ID           uuid.UUID
	Email, Name  string
	Role         string
	Hash         string
	TOTPSecret   *string
	TOTPEnabled  bool
	TOTPLastStep *int64
	LockedUntil  *time.Time
	Disabled     bool
}

func (st consoleStaff) actor() string { return "console:" + st.Email }

type consoleSession struct {
	ID, StaffID uuid.UUID
	State, CSRF string
	Email, Name string
	Role        string
}

func (s *Server) recordFailure(ctx context.Context, st consoleStaff, reason string) {
	_ = s.store.withTx(ctx, func(tx pgx.Tx) error {
		var failed int
		if err := tx.QueryRow(ctx, `
			update console_staff set failed_logins = failed_logins + 1,
				locked_until = case when failed_logins + 1 >= $2 then now() + $3::interval else locked_until end
			where id = $1 returning failed_logins`, st.ID, lockAfter, lockFor.String()).Scan(&failed); err != nil {
			return err
		}
		detail := map[string]any{"reason": reason, "failed_logins": failed}
		if failed >= lockAfter {
			detail["locked_for"] = lockFor.String()
		}
		return record(ctx, tx, Event{Actor: st.actor(), Action: "console.signin_failed", ResourceType: "console_staff",
			ResourceID: st.ID.String(), Detail: detail})
	})
}

func (s *Server) startSession(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, st consoleStaff, state string) (consoleSession, error) {
	token := staffauth.RandomToken(32)
	sess := consoleSession{StaffID: st.ID, State: state, CSRF: staffauth.RandomToken(24), Email: st.Email, Name: st.Name, Role: st.Role}
	limit := sessionMax
	if state == "setup" {
		limit = setupMax
	}
	expires := time.Now().Add(limit)
	if err := tx.QueryRow(ctx, `insert into console_sessions (token_hash, staff_id, state, csrf_token, expires_at)
		values ($1, $2, $3, $4, $5) returning id`, staffauth.HashToken(token), st.ID, state, sess.CSRF, expires).Scan(&sess.ID); err != nil {
		return sess, err
	}
	http.SetCookie(w, &http.Cookie{Name: consoleCookie, Value: token, Path: "/", Expires: expires,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return sess, nil
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: consoleCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func (s *Server) lookupSession(ctx context.Context, r *http.Request) (consoleSession, bool) {
	c, err := r.Cookie(consoleCookie)
	if err != nil || c.Value == "" {
		return consoleSession{}, false
	}
	var sess consoleSession
	err = s.store.pool.QueryRow(ctx, `
		update console_sessions cs set last_seen_at = now()
		from console_staff st
		where cs.token_hash = $1 and cs.revoked_at is null and cs.expires_at > now()
		  and cs.last_seen_at > now() - $2::interval and st.id = cs.staff_id and st.disabled_at is null
		returning cs.id, cs.staff_id, cs.state, cs.csrf_token, st.email::text, st.display_name, st.role`,
		staffauth.HashToken(c.Value), sessionIdle.String()).
		Scan(&sess.ID, &sess.StaffID, &sess.State, &sess.CSRF, &sess.Email, &sess.Name, &sess.Role)
	return sess, err == nil
}

type sessionKey struct{}

// signedIn requires an active session (and the CSRF header on writes);
// roleNeeded, if set, must match.
func (s *Server) signedIn(roleNeeded string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess, ok := s.sessionFor(w, r, "active")
			if !ok {
				return
			}
			if roleNeeded != "" && sess.Role != roleNeeded {
				writeError(w, http.StatusForbidden, "needs the "+roleNeeded+" role")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess)))
		})
	}
}

func sessionFrom(ctx context.Context) consoleSession {
	sess, _ := ctx.Value(sessionKey{}).(consoleSession)
	return sess
}

func (s *Server) sessionFor(w http.ResponseWriter, r *http.Request, state string) (consoleSession, bool) {
	sess, ok := s.lookupSession(r.Context(), r)
	if !ok || sess.State != state {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return sess, false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	default:
		got := r.Header.Get(csrfHeader)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) != 1 {
			writeError(w, http.StatusForbidden, "missing or invalid CSRF token")
			return sess, false
		}
	}
	return sess, true
}

type consoleMe struct {
	Status    string `json:"status"` // active or totp_setup
	Email     string `json:"email"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	CSRFToken string `json:"csrf_token"`
}

func (s *Server) consoleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct{ Email, Password, Code string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := r.Context()
	var st consoleStaff
	var sess consoleSession
	var failure string
	err := s.store.withTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select id, email::text, display_name, role, password_hash, totp_secret,
			totp_enabled_at is not null, totp_last_step, locked_until, disabled_at is not null
			from console_staff where email = $1 for update`, strings.TrimSpace(req.Email)).
			Scan(&st.ID, &st.Email, &st.Name, &st.Role, &st.Hash, &st.TOTPSecret, &st.TOTPEnabled, &st.TOTPLastStep,
				&st.LockedUntil, &st.Disabled)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (st.Disabled || (st.LockedUntil != nil && time.Now().Before(*st.LockedUntil)))) {
			_, _ = staffauth.VerifyPassword(req.Password, dummyHash)
			return errors.New(errSignIn)
		}
		if err != nil {
			return err
		}
		if ok, err := staffauth.VerifyPassword(req.Password, st.Hash); err != nil || !ok {
			failure = "password"
			return errors.New(errSignIn)
		}
		state := "active"
		if !st.TOTPEnabled {
			state = "setup"
		} else {
			step, ok := staffauth.CheckTOTP(deref(st.TOTPSecret), req.Code, st.TOTPLastStep, time.Now())
			if !ok {
				failure = "code"
				return errors.New(errSignIn)
			}
			if _, err := tx.Exec(ctx, `update console_staff set totp_last_step = $2 where id = $1`, st.ID, step); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update console_staff set failed_logins = 0, locked_until = null where id = $1`, st.ID); err != nil {
			return err
		}
		if sess, err = s.startSession(ctx, tx, w, st, state); err != nil {
			return err
		}
		return record(ctx, tx, Event{Actor: st.actor(), Action: "console.signin", ResourceType: "console_session",
			ResourceID: sess.ID.String(), Detail: map[string]string{"state": state}})
	})
	if err != nil {
		if failure != "" {
			s.recordFailure(ctx, st, failure)
		}
		writeError(w, http.StatusUnauthorized, errSignIn)
		return
	}
	writeJSON(w, http.StatusOK, meFrom(sess))
}

func meFrom(sess consoleSession) consoleMe {
	status := "active"
	if sess.State == "setup" {
		status = "totp_setup"
	}
	return consoleMe{Status: status, Email: sess.Email, Name: sess.Name, Role: sess.Role, CSRFToken: sess.CSRF}
}

func (s *Server) consoleMe(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.lookupSession(r.Context(), r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	writeJSON(w, http.StatusOK, meFrom(sess))
}

func (s *Server) consoleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFor(w, r, "setup")
	if !ok {
		return
	}
	key, err := staffauth.NewTOTPKey("WeHelp Registry", sess.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "setup failed")
		return
	}
	if _, err := s.store.pool.Exec(r.Context(), `update console_staff set totp_secret = $2 where id = $1 and totp_enabled_at is null`,
		sess.StaffID, key.Secret()); err != nil {
		writeError(w, http.StatusInternalServerError, "setup failed")
		return
	}
	png, err := staffauth.QRPNG(key.URL())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "setup failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"secret": key.Secret(), "otpauth_url": key.URL(),
		"qr_png": base64.StdEncoding.EncodeToString(png)})
}

// consoleTOTPConfirm turns the authenticator on with a first code and
// replaces the setup session with a full one.
func (s *Server) consoleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFor(w, r, "setup")
	if !ok {
		return
	}
	var req struct{ Code string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := r.Context()
	var next consoleSession
	err := s.store.withTx(ctx, func(tx pgx.Tx) error {
		var st consoleStaff
		if err := tx.QueryRow(ctx, `select id, email::text, display_name, role, totp_secret from console_staff
			where id = $1 and totp_enabled_at is null for update`, sess.StaffID).
			Scan(&st.ID, &st.Email, &st.Name, &st.Role, &st.TOTPSecret); err != nil {
			return err
		}
		step, ok := staffauth.CheckTOTP(deref(st.TOTPSecret), req.Code, nil, time.Now())
		if !ok {
			return errors.New("code")
		}
		if _, err := tx.Exec(ctx, `update console_staff set totp_enabled_at = now(), totp_last_step = $2 where id = $1`, st.ID, step); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update console_sessions set revoked_at = now() where id = $1`, sess.ID); err != nil {
			return err
		}
		var err error
		if next, err = s.startSession(ctx, tx, w, st, "active"); err != nil {
			return err
		}
		return record(ctx, tx, Event{Actor: st.actor(), Action: "console.totp_enabled", ResourceType: "console_staff",
			ResourceID: st.ID.String()})
	})
	if err != nil {
		writeError(w, http.StatusUnauthorized, "that code didn't match; try the next one")
		return
	}
	writeJSON(w, http.StatusOK, meFrom(next))
}

func (s *Server) consoleLogout(w http.ResponseWriter, r *http.Request) {
	if sess, ok := s.lookupSession(r.Context(), r); ok {
		_, _ = s.store.pool.Exec(r.Context(), `update console_sessions set revoked_at = now() where id = $1`, sess.ID)
		_ = s.store.RecordAs(r.Context(), Event{Actor: "console:" + sess.Email, Action: "console.signout",
			ResourceType: "console_session", ResourceID: sess.ID.String()})
	}
	clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
