package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// Patients are created by staff and have no password (#37): they join by
// pairing a phone in the office.

type patientView struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email"`
	LinkStatus  string     `json:"link_status,omitempty"`
	Devices     int        `json:"devices"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
}

// ListPatients lists the caller's patients (admins: all in the tenant) with
// how many paired phones each has. Staff only.
func (s *Server) ListPatients(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if id.Role != "provider" && id.Role != "admin" {
		writeErr(w, http.StatusForbidden, "staff only")
		return
	}
	q := `
		select u.id::text, u.display_name, u.email::text, coalesce(pp.status, ''),
		       count(d.id) filter (where d.revoked_at is null), max(d.last_seen_at)
		from users u
		left join provider_patients pp on pp.patient_id = u.id and pp.provider_id = $2
		left join devices d on d.user_id = u.id
		where u.tenant_id = $1 and u.role = 'patient'`
	if id.Role != "admin" {
		q += ` and pp.status is not null and pp.status <> 'revoked'`
	}
	q += ` group by u.id, pp.status order by u.display_name, u.email`
	rows, err := s.pool.Query(r.Context(), q, id.TenantID, id.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list failed")
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (patientView, error) {
		var p patientView
		err := row.Scan(&p.ID, &p.DisplayName, &p.Email, &p.LinkStatus, &p.Devices, &p.LastSeenAt)
		return p, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"patients": list})
}

type createPatientRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

// CreatePatient adds a patient with no password and, for a provider, an
// active link to them. Staff on the office LAN only.
func (s *Server) CreatePatient(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	var req createPatientRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.DisplayName, req.Email = strings.TrimSpace(req.DisplayName), strings.TrimSpace(req.Email)
	if req.DisplayName == "" || !strings.Contains(req.Email, "@") {
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
	var p patientView
	err = tx.QueryRow(ctx, `
		insert into users (tenant_id, email, password_hash, role, display_name)
		values ($1, $2, null, 'patient', $3) returning id::text, display_name, email::text`,
		id.TenantID, req.Email, req.DisplayName).Scan(&p.ID, &p.DisplayName, &p.Email)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeErr(w, http.StatusConflict, "email already registered in this practice")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "create failed")
		return
	}
	if id.Role == "provider" {
		if _, err := tx.Exec(ctx, `insert into provider_patients (tenant_id, provider_id, patient_id, status) values ($1, $2, $3, 'active')`,
			id.TenantID, id.UserID, p.ID); err != nil {
			writeErr(w, http.StatusInternalServerError, "link failed")
			return
		}
		p.LinkStatus = "active"
	}
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: "patient.create", ResourceType: "user", ResourceID: p.ID,
		Detail: mustJSON(map[string]string{"role": "patient"}),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusCreated, p)
}
