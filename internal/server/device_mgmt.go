package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// Device management (#10). A person can have several devices (a phone plus
// an office iPad, say), each paired in the office (#37). People see and
// revoke their own; staff see and revoke their patients' on the office LAN.
// Revoking a device ends its refresh tokens, and authenticate refuses its
// access tokens at once.

type deviceView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	Status     string     `json:"status"`            // active, revoked
	Current    bool       `json:"current,omitempty"` // the device making this request
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func listDevices(ctx context.Context, q querier, tenantID, userID uuid.UUID, current *uuid.UUID) ([]deviceView, error) {
	rows, err := q.Query(ctx, `
		select id, name, created_at, last_seen_at, revoked_at from devices
		where tenant_id = $1 and user_id = $2
		order by revoked_at is not null, coalesce(last_seen_at, created_at) desc`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (deviceView, error) {
		var d deviceView
		var id uuid.UUID
		if err := row.Scan(&id, &d.Name, &d.CreatedAt, &d.LastSeenAt, &d.RevokedAt); err != nil {
			return d, err
		}
		d.ID, d.Status = id.String(), "active"
		if d.RevokedAt != nil {
			d.Status = "revoked"
		}
		d.Current = current != nil && *current == id
		return d, nil
	})
}

var errNoDevice = errors.New("no such device")

// revokeDevice revokes one device of one user and all its refresh tokens,
// with an audit record, in the caller's transaction. Revoking an already
// revoked device is a no-op that succeeds.
func revokeDevice(ctx context.Context, tx pgx.Tx, tenantID, ownerID, deviceID, actorID uuid.UUID, by string) error {
	var revoked *time.Time
	err := tx.QueryRow(ctx, `select revoked_at from devices where id = $1 and user_id = $2 and tenant_id = $3 for update`,
		deviceID, ownerID, tenantID).Scan(&revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNoDevice
	}
	if err != nil || revoked != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update devices set revoked_at = now(), push_token = null, push_env = null where id = $1`, deviceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update refresh_tokens set revoked_at = now() where device_id = $1 and revoked_at is null`, deviceID); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Event{
		TenantID: tenantID, ActorID: &actorID,
		Action: "device.revoke", ResourceType: "device", ResourceID: deviceID.String(),
		Detail: mustJSON(map[string]string{"user_id": ownerID.String(), "by": by}),
	})
}

// ListMyDevices lists the caller's devices, marking the current one.
func (s *Server) ListMyDevices(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	list, err := listDevices(r.Context(), s.pool, id.TenantID, id.UserID, id.DeviceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": list})
}

// RevokeMyDevice revokes one of the caller's own devices (including the
// current one, which signs this device out).
func (s *Server) RevokeMyDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	s.revokeIn(w, r, id.TenantID, id.UserID, id.UserID, chi.URLParam(r, "deviceID"), "self")
}

// ListPatientDevices lists a patient's devices for staff on the office LAN.
func (s *Server) ListPatientDevices(w http.ResponseWriter, r *http.Request) {
	staff, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	patientID, err := uuid.Parse(chi.URLParam(r, "patientID"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such patient for you")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	if _, err := staffPatient(ctx, tx, staff, patientID); err != nil {
		writeErr(w, http.StatusNotFound, "no such patient for you")
		return
	}
	list, err := listDevices(ctx, tx, staff.TenantID, patientID, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": list})
}

// RevokePatientDevice revokes one of a patient's devices (staff, LAN only),
// for a lost or replaced phone.
func (s *Server) RevokePatientDevice(w http.ResponseWriter, r *http.Request) {
	staff, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	patientID, err := uuid.Parse(chi.URLParam(r, "patientID"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such patient for you")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	if _, err := staffPatient(ctx, tx, staff, patientID); err != nil {
		tx.Rollback(ctx)
		writeErr(w, http.StatusNotFound, "no such patient for you")
		return
	}
	tx.Rollback(ctx)
	s.revokeIn(w, r, staff.TenantID, patientID, staff.UserID, chi.URLParam(r, "deviceID"), "staff")
}

func (s *Server) revokeIn(w http.ResponseWriter, r *http.Request, tenantID, ownerID, actorID uuid.UUID, rawDevice, by string) {
	deviceID, err := uuid.Parse(rawDevice)
	if err != nil {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	if err := revokeDevice(ctx, tx, tenantID, ownerID, deviceID, actorID, by); err != nil {
		if errors.Is(err, errNoDevice) {
			writeErr(w, http.StatusNotFound, "device not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "revoke failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
