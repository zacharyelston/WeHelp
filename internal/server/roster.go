package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/roster"
)

// Provider roster (#62). The Registry is the one source of who works at the
// practice; the Back Office pulls the roster on a schedule and when an admin
// asks. A new provider on it shows on the Staff screen as waiting for an
// invite, and the admin sends the same set-up link as Add staff (#100). A
// provider removed in the Registry is flagged for the admin to disable;
// nothing is disabled or deleted automatically. Accounts and credentials
// never leave the Back Office.

// RosterSource fetches this office's roster from the Registry.
type RosterSource interface {
	Fetch(ctx context.Context) (roster.Roster, error)
}

// SetRosterSource turns on roster sync. Before install there is no source.
func (s *Server) SetRosterSource(src RosterSource) { s.roster = src }

var errNoRoster = errors.New("this office isn't installed with the Registry, so there is no roster to pull")

type rosterSyncResult struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
}

// syncRoster pulls the roster and stores it for tenant. actor is nil for the
// scheduled pull. A failure is recorded for the Staff screen and returned.
func (s *Server) syncRoster(ctx context.Context, tenant uuid.UUID, actor *uuid.UUID) (rosterSyncResult, error) {
	var res rosterSyncResult
	if s.roster == nil {
		return res, errNoRoster
	}
	list, err := s.roster.Fetch(ctx)
	if err != nil {
		if _, e := s.pool.Exec(ctx, `
			insert into roster_sync (tenant_id, attempted_at, error) values ($1, now(), $2)
			on conflict (tenant_id) do update set attempted_at = now(), error = excluded.error`, tenant, err.Error()); e != nil {
			slog.Error("roster: record failure", "err", e)
		}
		return res, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	type prev struct {
		removed bool
		sig     string
	}
	rows, err := tx.Query(ctx, `
		select registry_id, removed_at is not null, name || '|' || email || '|' || role || '|' || credentials || '|' || licence_number
		from roster_entries where tenant_id = $1 for update`, tenant)
	if err != nil {
		return res, err
	}
	old := map[uuid.UUID]prev{}
	for rows.Next() {
		var id uuid.UUID
		var p prev
		if err := rows.Scan(&id, &p.removed, &p.sig); err != nil {
			rows.Close()
			return res, err
		}
		old[id] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	seen := make([]uuid.UUID, 0, len(list.Providers))
	for _, p := range list.Providers {
		id, err := uuid.Parse(p.ID)
		if err != nil || (p.Role != "provider" && p.Role != "admin") || p.Email == "" {
			slog.Warn("roster: skipped an invalid entry", "registry_id", p.ID)
			continue
		}
		seen = append(seen, id)
		o, had := old[id]
		switch {
		case !had && p.RemovedAt == nil:
			res.Added++
		case had && !o.removed && p.RemovedAt != nil:
			res.Removed++
		case had && o.sig != p.Name+"|"+p.Email+"|"+p.Role+"|"+p.Credentials+"|"+p.LicenceNumber:
			res.Changed++
		}
		// The where clause keeps one practice's entry from being claimed by
		// another on a shared Back Office.
		if _, err := tx.Exec(ctx, `
			insert into roster_entries (registry_id, tenant_id, name, email, role, credentials, licence_number, removed_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (registry_id) do update set name = excluded.name, email = excluded.email, role = excluded.role,
				credentials = excluded.credentials, licence_number = excluded.licence_number,
				removed_at = excluded.removed_at, updated_at = now()
			where roster_entries.tenant_id = excluded.tenant_id`,
			id, tenant, p.Name, p.Email, p.Role, p.Credentials, p.LicenceNumber, p.RemovedAt); err != nil {
			return res, err
		}
	}
	// Anyone no longer on the roster at all counts as removed.
	tag, err := tx.Exec(ctx, `
		update roster_entries set removed_at = now(), updated_at = now()
		where tenant_id = $1 and removed_at is null and registry_id <> all($2)`, tenant, seen)
	if err != nil {
		return res, err
	}
	res.Removed += int(tag.RowsAffected())
	if _, err := tx.Exec(ctx, `
		insert into roster_sync (tenant_id, synced_at, attempted_at, error) values ($1, now(), now(), '')
		on conflict (tenant_id) do update set synced_at = now(), attempted_at = now(), error = ''`, tenant); err != nil {
		return res, err
	}
	// The scheduled pull records an event only when something changed.
	if actor != nil || res != (rosterSyncResult{}) {
		if err := audit.Record(ctx, tx, audit.Event{TenantID: tenant, ActorID: actor, Action: "roster.sync",
			ResourceType: "roster", ResourceID: tenant.String(), Detail: mustJSON(res)}); err != nil {
			return res, err
		}
	}
	return res, tx.Commit(ctx)
}

// RunRosterSync pulls the roster every interval until ctx ends. A Back
// Office serves one practice; with any other number of tenants it waits for
// an admin's Sync now instead.
func (s *Server) RunRosterSync(ctx context.Context, interval time.Duration) {
	if s.roster == nil || interval <= 0 {
		return
	}
	t := time.NewTimer(10 * time.Second) // first pull shortly after start
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var tenants []uuid.UUID
		rows, err := s.pool.Query(ctx, `select id from tenants limit 2`)
		if err == nil {
			tenants, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		}
		switch {
		case err != nil:
			slog.Error("roster: tenants", "err", err)
		case len(tenants) != 1:
			slog.Warn("roster: scheduled pull needs exactly one practice on this Back Office", "practices", len(tenants))
		default:
			if res, err := s.syncRoster(ctx, tenants[0], nil); err != nil {
				slog.Warn("roster: pull failed", "err", err)
			} else if res != (rosterSyncResult{}) {
				slog.Info("roster: pulled", "added", res.Added, "removed", res.Removed, "changed", res.Changed)
			}
		}
		t.Reset(interval)
	}
}

type rosterAccount struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Status   string `json:"status"` // invited, invite_expired, totp_pending, active, disabled
	Disabled bool   `json:"disabled"`
}

type rosterEntryView struct {
	RegistryID    string         `json:"registry_id"`
	Name          string         `json:"name"`
	Email         string         `json:"email"`
	Role          string         `json:"role"`
	Credentials   string         `json:"credentials"`
	LicenceNumber string         `json:"licence_number"`
	RemovedAt     *time.Time     `json:"removed_at,omitempty"`
	Account       *rosterAccount `json:"account,omitempty"`
	// Status: needs_invite (a provider with no account), add_admin (an admin
	// with no account: added on the Back Office host), linked, disable
	// (removed in the Registry but the account still works: the flag), or
	// removed.
	Status string `json:"status"`
}

type rosterView struct {
	Installed  bool              `json:"installed"`
	SyncedAt   *time.Time        `json:"synced_at"`
	AttemptAt  *time.Time        `json:"attempted_at"`
	Error      string            `json:"error"`
	Entries    []rosterEntryView `json:"entries"`
	Result     *rosterSyncResult `json:"result,omitempty"`
	NeedAction int               `json:"need_action"` // invites to send plus accounts to disable
}

func (s *Server) loadRoster(ctx context.Context, tenant uuid.UUID) (rosterView, error) {
	v := rosterView{Installed: s.roster != nil, Entries: []rosterEntryView{}}
	err := s.pool.QueryRow(ctx, `select synced_at, attempted_at, error from roster_sync where tenant_id = $1`, tenant).
		Scan(&v.SyncedAt, &v.AttemptAt, &v.Error)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return v, err
	}
	rows, err := s.pool.Query(ctx, `
		select e.registry_id::text, e.name, e.email::text, e.role, e.credentials, e.licence_number, e.removed_at,
		       u.id::text, u.role::text, u.disabled_at is not null, u.password_hash is not null, u.totp_enabled_at is not null,
		       (select max(expires_at) from staff_invites i where i.user_id = u.id and i.used_at is null and i.cancelled_at is null)
		from roster_entries e
		left join users u on u.tenant_id = e.tenant_id and u.email = e.email and u.role <> 'patient'
		where e.tenant_id = $1
		order by e.removed_at is not null, e.role desc, e.name`, tenant)
	if err != nil {
		return v, err
	}
	v.Entries, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (rosterEntryView, error) {
		var e rosterEntryView
		var uid, urole *string
		var disabled, hasPW, hasTOTP *bool
		var invite *time.Time
		if err := row.Scan(&e.RegistryID, &e.Name, &e.Email, &e.Role, &e.Credentials, &e.LicenceNumber, &e.RemovedAt,
			&uid, &urole, &disabled, &hasPW, &hasTOTP, &invite); err != nil {
			return e, err
		}
		if uid != nil {
			a := &rosterAccount{ID: *uid, Role: *urole, Disabled: *disabled}
			switch {
			case *disabled:
				a.Status = "disabled"
			case *hasTOTP:
				a.Status = "active"
			case *hasPW:
				a.Status = "totp_pending"
			case invite != nil && invite.After(time.Now()):
				a.Status = "invited"
			default:
				a.Status = "invite_expired"
			}
			e.Account = a
		}
		switch {
		case e.RemovedAt != nil && e.Account != nil && !e.Account.Disabled:
			e.Status = "disable"
		case e.RemovedAt != nil:
			e.Status = "removed"
		case e.Account != nil:
			e.Status = "linked"
		case e.Role == "admin":
			e.Status = "add_admin"
		default:
			e.Status = "needs_invite"
		}
		return e, nil
	})
	for _, e := range v.Entries {
		if e.Status == "needs_invite" || e.Status == "disable" {
			v.NeedAction++
		}
	}
	return v, err
}

// GetRoster shows the roster as last pulled, with each entry's account.
func (s *Server) GetRoster(w http.ResponseWriter, r *http.Request) {
	id, ok := adminOnLAN(w, r)
	if !ok {
		return
	}
	v, err := s.loadRoster(r.Context(), id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "roster failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}

// SyncRoster pulls the roster now.
func (s *Server) SyncRoster(w http.ResponseWriter, r *http.Request) {
	id, ok := adminOnLAN(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.syncRoster(ctx, id.TenantID, &id.UserID)
	if errors.Is(err, errNoRoster) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		slog.Warn("roster: sync now failed", "err", err)
		writeErr(w, http.StatusBadGateway, "couldn't get the roster from the Registry: "+err.Error())
		return
	}
	v, err := s.loadRoster(r.Context(), id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "roster failed")
		return
	}
	v.Result = &res
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}

// InviteFromRoster creates the account for a roster provider and returns
// their set-up link, exactly as Add staff does. Admins on the roster are
// added on the Back Office host (wehelpd staff add), as at install.
func (s *Server) InviteFromRoster(w http.ResponseWriter, r *http.Request) {
	id, ok := adminOnLAN(w, r)
	if !ok {
		return
	}
	regID, err := uuid.Parse(chi.URLParam(r, "registryID"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not on the roster")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	var name, email, role string
	var removed bool
	err = tx.QueryRow(ctx, `
		select name, email::text, role, removed_at is not null from roster_entries
		where registry_id = $1 and tenant_id = $2 for update`, regID, id.TenantID).Scan(&name, &email, &role, &removed)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "not on the roster")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	switch {
	case removed:
		writeErr(w, http.StatusConflict, "removed from the roster in the Registry")
		return
	case role != "provider":
		writeErr(w, http.StatusConflict, "admins are added on the Back Office host with wehelpd staff add")
		return
	}
	userID, token, expires, err := inviteStaff(ctx, tx, id, name, email, "provider",
		map[string]string{"via": "roster", "registry_id": regID.String()})
	if errors.Is(err, errEmailTaken) {
		writeErr(w, http.StatusConflict, "that email already has an account in this practice")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "invite failed")
		return
	}
	list, err := loadProviders(ctx, tx, id.TenantID, &userID)
	if err != nil || len(list) != 1 {
		writeErr(w, http.StatusInternalServerError, "invite failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, providerInvite{Provider: list[0], SetupURL: setupURL(r, token), ExpiresAt: expires})
}
