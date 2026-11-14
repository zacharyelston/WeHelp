package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/auth"
)

// Provider accounts from the staff web (#100). A practice admin adds a
// provider and gets a single-use set-up link; the provider opens it on the
// office network, sets their own password and enrolls TOTP (#38). Admins
// never see or choose a provider's password. Accounts are disabled, never
// deleted. Every route here is LAN only. When the Registry roster (#62)
// lands, its entries feed the same invite.

const (
	staffInviteTTL   = 24 * time.Hour
	errInviteInvalid = "this set-up link is invalid, used or expired; ask your practice admin for a new one"
)

// adminOnLAN requires a signed-in admin on the office LAN: 403 for anyone
// else, 404 through the Relay (lanOnly normally stops those first).
func adminOnLAN(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return id, false
	}
	if id.Role != "admin" {
		writeErr(w, http.StatusForbidden, "admins only")
		return id, false
	}
	return id, true
}

type providerAccount struct {
	ID              string     `json:"id"`
	DisplayName     string     `json:"display_name"`
	Email           string     `json:"email"`
	Role            string     `json:"role"`   // provider or office (#106)
	Status          string     `json:"status"` // invited, invite_expired, totp_pending, active, disabled
	CreatedAt       time.Time  `json:"created_at"`
	DisabledAt      *time.Time `json:"disabled_at,omitempty"`
	InviteExpiresAt *time.Time `json:"invite_expires_at,omitempty"`
}

type providerInvite struct {
	Provider  providerAccount `json:"provider"`
	SetupURL  string          `json:"setup_url"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// loadProviders lists the practice's provider accounts (one, if id is set).
func loadProviders(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, tenantID uuid.UUID, id *uuid.UUID) ([]providerAccount, error) {
	rows, err := q.Query(ctx, `
		select u.id::text, u.display_name, u.email::text, u.role::text, u.password_hash is not null,
		       u.totp_enabled_at is not null, u.disabled_at, u.created_at, i.expires_at
		from users u
		left join lateral (
			select expires_at from staff_invites
			where user_id = u.id and used_at is null and cancelled_at is null
			order by created_at desc limit 1) i on true
		where u.tenant_id = $1 and u.role in ('provider', 'office') and ($2::uuid is null or u.id = $2)
		order by u.display_name, u.email`, tenantID, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (providerAccount, error) {
		var p providerAccount
		var hasPassword, hasTOTP bool
		if err := row.Scan(&p.ID, &p.DisplayName, &p.Email, &p.Role, &hasPassword, &hasTOTP, &p.DisabledAt,
			&p.CreatedAt, &p.InviteExpiresAt); err != nil {
			return p, err
		}
		switch {
		case p.DisabledAt != nil:
			p.Status = "disabled"
		case hasTOTP:
			p.Status = "active"
		case hasPassword:
			p.Status = "totp_pending"
		case p.InviteExpiresAt != nil && p.InviteExpiresAt.After(time.Now()):
			p.Status = "invited"
		default:
			p.Status = "invite_expired"
		}
		if hasPassword {
			p.InviteExpiresAt = nil
		}
		return p, nil
	})
}

// ListProviders returns the practice's provider and front-desk accounts.
// Admins, and the front desk read-only so it can book for any provider
// (#106).
func (s *Server) ListProviders(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	if !practiceWide(id.Role) {
		writeErr(w, http.StatusForbidden, "admins and the front desk only")
		return
	}
	list, err := loadProviders(r.Context(), s.pool, id.TenantID, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"providers": list})
}

// issueStaffInvite cancels any open invite for the account and makes a new
// one. It returns the token, which is shown once and never stored.
func issueStaffInvite(ctx context.Context, tx pgx.Tx, tenantID, userID, by uuid.UUID) (string, time.Time, uuid.UUID, error) {
	if _, err := tx.Exec(ctx, `
		update staff_invites set cancelled_at = now()
		where user_id = $1 and used_at is null and cancelled_at is null`, userID); err != nil {
		return "", time.Time{}, uuid.Nil, err
	}
	token := randomToken(32)
	expires := time.Now().Add(staffInviteTTL)
	var inviteID uuid.UUID
	err := tx.QueryRow(ctx, `
		insert into staff_invites (tenant_id, user_id, token_hash, created_by, expires_at)
		values ($1, $2, $3, $4, $5) returning id`, tenantID, userID, hashToken(token), by, expires).Scan(&inviteID)
	return token, expires, inviteID, err
}

// setupURL is the link the provider opens. The token is in the fragment, so
// the browser never sends it in a request line, a Referer or a log.
func setupURL(r *http.Request, token string) string {
	return "https://" + r.Host + "/#setup=" + token
}

type createProviderRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        string `json:"role"` // provider (default) or office (#106)
}

// CreateProvider adds a provider account with no password and returns a
// set-up link for them. Admins only.
func (s *Server) CreateProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := adminOnLAN(w, r)
	if !ok {
		return
	}
	var req createProviderRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.DisplayName, req.Email = strings.TrimSpace(req.DisplayName), strings.TrimSpace(req.Email)
	if req.Role == "" {
		req.Role = "provider"
	}
	if req.Role != "provider" && req.Role != "office" {
		writeErr(w, http.StatusBadRequest, "role must be provider or office")
		return
	}
	if req.DisplayName == "" || len(req.DisplayName) > 200 || !strings.Contains(req.Email, "@") || len(req.Email) > 254 {
		writeErr(w, http.StatusBadRequest, "display_name and a valid email are required")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	var userID uuid.UUID
	err = tx.QueryRow(ctx, `
		insert into users (tenant_id, email, password_hash, role, display_name)
		values ($1, $2, null, $4, $3) returning id`, id.TenantID, req.Email, req.DisplayName, req.Role).Scan(&userID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeErr(w, http.StatusConflict, "that email already has an account in this practice")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "create failed")
		return
	}
	token, expires, inviteID, err := issueStaffInvite(ctx, tx, id.TenantID, userID, id.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "invite failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: "staff.invite",
		ResourceType: "user", ResourceID: userID.String(),
		Detail: mustJSON(map[string]string{"role": req.Role, "email": req.Email, "invite_id": inviteID.String(), "via": "web"})}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	list, err := loadProviders(ctx, tx, id.TenantID, &userID)
	if err != nil || len(list) != 1 {
		writeErr(w, http.StatusInternalServerError, "create failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, providerInvite{Provider: list[0], SetupURL: setupURL(r, token), ExpiresAt: expires})
}

type lockedProvider struct {
	ID          uuid.UUID
	HasPassword bool
	Disabled    bool
}

// lockProvider locks a provider account in the admin's practice.
func lockProvider(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, raw string) (lockedProvider, error) {
	var p lockedProvider
	uid, err := uuid.Parse(raw)
	if err != nil {
		return p, pgx.ErrNoRows
	}
	err = tx.QueryRow(ctx, `
		select id, password_hash is not null, disabled_at is not null from users
		where id = $1 and tenant_id = $2 and role in ('provider', 'office') for update`, uid, tenantID).
		Scan(&p.ID, &p.HasPassword, &p.Disabled)
	return p, err
}

// changeResult is what one admin change to a provider did: the audit
// action, or an HTTP error status and message.
type changeResult struct {
	Action   string
	Detail   map[string]string
	SetupURL string // a new set-up link, shown once
	Status   int
	Err      string
}

func changeErr(status int, msg string) changeResult { return changeResult{Status: status, Err: msg} }

// providerChange runs one admin change to a provider account in a
// transaction with its audit event, and answers with the account.
func (s *Server) providerChange(w http.ResponseWriter, r *http.Request,
	change func(ctx context.Context, tx pgx.Tx, id Identity, p lockedProvider) changeResult) {
	id, ok := adminOnLAN(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	p, err := lockProvider(ctx, tx, id.TenantID, chi.URLParam(r, "userID"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "provider not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	res := change(ctx, tx, id, p)
	if res.Status != 0 {
		writeErr(w, res.Status, res.Err)
		return
	}
	ev := audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: res.Action, ResourceType: "user", ResourceID: p.ID.String()}
	if res.Detail != nil {
		ev.Detail = mustJSON(res.Detail)
	}
	if err := audit.Record(ctx, tx, ev); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	list, err := loadProviders(ctx, tx, id.TenantID, &p.ID)
	if err != nil || len(list) != 1 {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	if res.SetupURL != "" {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, providerInvite{Provider: list[0], SetupURL: res.SetupURL, ExpiresAt: *list[0].InviteExpiresAt})
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

// ReissueProviderInvite replaces a provider's set-up link, for one that
// expired or was lost. Only for an account that hasn't set a password.
func (s *Server) ReissueProviderInvite(w http.ResponseWriter, r *http.Request) {
	s.providerChange(w, r, func(ctx context.Context, tx pgx.Tx, id Identity, p lockedProvider) changeResult {
		if p.Disabled {
			return changeErr(http.StatusConflict, "enable this provider first")
		}
		if p.HasPassword {
			return changeErr(http.StatusConflict, "this provider has already set up their account")
		}
		token, _, inviteID, err := issueStaffInvite(ctx, tx, id.TenantID, p.ID, id.UserID)
		if err != nil {
			return changeErr(http.StatusInternalServerError, "invite failed")
		}
		return changeResult{Action: "staff.invite", SetupURL: setupURL(r, token),
			Detail: map[string]string{"invite_id": inviteID.String(), "reissue": "true", "via": "web"}}
	})
}

// DisableProvider stops a provider signing in or using the API: it ends
// their staff web sessions, revokes refresh tokens and cancels open
// invites. Paired devices stay paired, so enabling restores them.
func (s *Server) DisableProvider(w http.ResponseWriter, r *http.Request) {
	s.providerChange(w, r, func(ctx context.Context, tx pgx.Tx, _ Identity, p lockedProvider) changeResult {
		if p.Disabled {
			return changeErr(http.StatusConflict, "already disabled")
		}
		for _, q := range []string{
			`update users set disabled_at = now() where id = $1`,
			`update staff_sessions set revoked_at = now() where user_id = $1 and revoked_at is null`,
			`update refresh_tokens set revoked_at = now() where user_id = $1 and revoked_at is null`,
			`update staff_invites set cancelled_at = now() where user_id = $1 and used_at is null and cancelled_at is null`,
			`update care_team_members set status = 'removed', removed_at = now() where member_id = $1 and status <> 'removed'`, // #111
		} {
			if _, err := tx.Exec(ctx, q, p.ID); err != nil {
				return changeErr(http.StatusInternalServerError, "disable failed")
			}
		}
		return changeResult{Action: "staff.disable"}
	})
}

// EnableProvider lets a disabled provider sign in again. One who never set
// up their account needs a new set-up link.
func (s *Server) EnableProvider(w http.ResponseWriter, r *http.Request) {
	s.providerChange(w, r, func(ctx context.Context, tx pgx.Tx, _ Identity, p lockedProvider) changeResult {
		if !p.Disabled {
			return changeErr(http.StatusConflict, "not disabled")
		}
		if _, err := tx.Exec(ctx, `update users set disabled_at = null, failed_logins = 0, locked_until = null where id = $1`, p.ID); err != nil {
			return changeErr(http.StatusInternalServerError, "enable failed")
		}
		return changeResult{Action: "staff.enable"}
	})
}

type staffSetupRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// StaffSetup redeems a set-up link: the provider sets their password and
// gets a 'setup' session that can only enroll TOTP, as at a first sign-in
// (#38). The link works once.
func (s *Server) StaffSetup(w http.ResponseWriter, r *http.Request) {
	var req staffSetupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(req.Password) < 12 || len(req.Password) > 1024 {
		writeErr(w, http.StatusBadRequest, "password must be at least 12 characters")
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeErr(w, http.StatusBadRequest, errInviteInvalid)
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	var inviteID uuid.UUID
	var u staffUser
	err = tx.QueryRow(ctx, `
		select i.id, u.id, u.tenant_id, u.role::text, u.display_name, t.name
		from staff_invites i join users u on u.id = i.user_id join tenants t on t.id = u.tenant_id
		where i.token_hash = $1 and i.used_at is null and i.cancelled_at is null and i.expires_at > now()
		  and u.disabled_at is null and u.password_hash is null
		for update of i, u`, hashToken(token)).
		Scan(&inviteID, &u.ID, &u.TenantID, &u.Role, &u.Name, &u.TenantName)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusBadRequest, errInviteInvalid)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "set-up failed")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "set-up failed")
		return
	}
	if _, err := tx.Exec(ctx, `update users set password_hash = $2, failed_logins = 0, locked_until = null where id = $1`,
		u.ID, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "set-up failed")
		return
	}
	if _, err := tx.Exec(ctx, `update staff_invites set used_at = now() where id = $1`, inviteID); err != nil {
		writeErr(w, http.StatusInternalServerError, "set-up failed")
		return
	}
	sess, err := startSession(ctx, tx, w, u, "setup")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "set-up failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: u.TenantID, ActorID: &u.ID, Action: "staff.setup",
		ResourceType: "user", ResourceID: u.ID.String(),
		Detail: mustJSON(map[string]string{"invite_id": inviteID.String()})}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, staffMe{Status: "totp_setup", UserID: u.ID.String(), Role: u.Role,
		DisplayName: u.Name, Practice: u.TenantName, CSRFToken: sess.CSRF})
}
