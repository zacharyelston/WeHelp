package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
)

var appointmentStatuses = map[string]bool{
	"scheduled": true,
	"cancelled": true,
	"completed": true,
	"no_show":   true,
}

type appointmentView struct {
	ID         uuid.UUID `json:"id"`
	ProviderID uuid.UUID `json:"provider_id"`
	PatientID  uuid.UUID `json:"patient_id"`
	StartsAt   time.Time `json:"starts_at"`
	EndsAt     time.Time `json:"ends_at"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

type createAppointmentRequest struct {
	PatientID uuid.UUID `json:"patient_id"`
	StartsAt  string    `json:"starts_at"`
	EndsAt    string    `json:"ends_at"`
}

type updateAppointmentRequest struct {
	StartsAt *string `json:"starts_at"`
	EndsAt   *string `json:"ends_at"`
	Status   *string `json:"status"`
}

// CreateAppointment schedules a new appointment (provider only).
// Requires an active provider-patient link and rejects overlapping
// appointments for the same provider.
// Route: POST /api/v1/appointments
func (s *Server) CreateAppointment(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.Role != "provider" {
		writeErr(w, http.StatusForbidden, "only providers can create appointments")
		return
	}

	var req createAppointmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.PatientID == id.UserID {
		writeErr(w, http.StatusBadRequest, "cannot book appointment with yourself")
		return
	}
	startsAt, err := time.Parse(time.RFC3339Nano, req.StartsAt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "starts_at must be RFC3339")
		return
	}
	endsAt, err := time.Parse(time.RFC3339Nano, req.EndsAt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ends_at must be RFC3339")
		return
	}
	if !endsAt.After(startsAt) {
		writeErr(w, http.StatusBadRequest, "ends_at must be after starts_at")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	linked, err := HasActiveLink(ctx, tx, id.TenantID, id.UserID, req.PatientID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "link check failed")
		return
	}
	if !linked {
		writeErr(w, http.StatusForbidden, "no active link with that patient")
		return
	}

	overlap, err := checkOverlap(ctx, tx, id.TenantID, id.UserID, startsAt, endsAt, uuid.Nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "overlap check failed")
		return
	}
	if overlap {
		writeErr(w, http.StatusConflict, "overlapping appointment exists")
		return
	}

	var appt appointmentView
	err = tx.QueryRow(ctx, `
		insert into appointments (tenant_id, provider_id, patient_id, starts_at, ends_at)
		values ($1, $2, $3, $4, $5)
		returning id, provider_id, patient_id, starts_at, ends_at, status, created_at`,
		id.TenantID, id.UserID, req.PatientID, startsAt, endsAt).
		Scan(&appt.ID, &appt.ProviderID, &appt.PatientID, &appt.StartsAt, &appt.EndsAt, &appt.Status, &appt.CreatedAt)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}

	detail, _ := json.Marshal(map[string]string{
		"patient_id": req.PatientID.String(),
		"starts_at":  startsAt.Format(time.RFC3339),
	})
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: "appointment.create", ResourceType: "appointment", ResourceID: appt.ID.String(),
		Detail: detail,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusCreated, appt)
}

// ListAppointments returns appointments scoped to the caller (provider or
// patient), optionally filtered by ?from= and ?to= on starts_at.
// Route: GET /api/v1/appointments
func (s *Server) ListAppointments(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	q := r.URL.Query()

	base := `
		select id, provider_id, patient_id, starts_at, ends_at, status, created_at
		from appointments
		where tenant_id = $1 and (provider_id = $2 or patient_id = $2)`
	args := []any{id.TenantID, id.UserID}
	if v := q.Get("from"); v != "" {
		from, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "from must be RFC3339")
			return
		}
		base += fmt.Sprintf(` and starts_at >= $%d`, len(args)+1)
		args = append(args, from)
	}
	if v := q.Get("to"); v != "" {
		to, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "to must be RFC3339")
			return
		}
		base += fmt.Sprintf(` and starts_at < $%d`, len(args)+1)
		args = append(args, to)
	}
	base += ` order by starts_at asc`

	rows, err := s.pool.Query(r.Context(), base, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	appts := []appointmentView{}
	for rows.Next() {
		var a appointmentView
		if err := rows.Scan(&a.ID, &a.ProviderID, &a.PatientID, &a.StartsAt, &a.EndsAt, &a.Status, &a.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan failed")
			return
		}
		appts = append(appts, a)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "rows failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"appointments": appts})
}

// UpdateAppointment lets the provider reschedule and/or transition status.
// Rescheduling re-checks for overlaps (excluding the current appointment).
// Route: PATCH /api/v1/appointments/{appointmentID}
func (s *Server) UpdateAppointment(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.Role != "provider" {
		writeErr(w, http.StatusForbidden, "only providers can update appointments")
		return
	}
	apptID, err := uuid.Parse(chi.URLParam(r, "appointmentID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid appointment id")
		return
	}

	var req updateAppointmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.StartsAt == nil && req.EndsAt == nil && req.Status == nil {
		writeErr(w, http.StatusBadRequest, "nothing to update")
		return
	}
	if req.Status != nil && !appointmentStatuses[*req.Status] {
		writeErr(w, http.StatusBadRequest, "invalid status")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	// Load current appointment, verify caller owns it.
	var appt appointmentView
	err = tx.QueryRow(ctx, `
		select id, provider_id, patient_id, starts_at, ends_at, status, created_at
		from appointments where id = $1 and tenant_id = $2`,
		apptID, id.TenantID).Scan(
		&appt.ID, &appt.ProviderID, &appt.PatientID, &appt.StartsAt, &appt.EndsAt, &appt.Status, &appt.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "appointment not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	if appt.ProviderID != id.UserID {
		writeErr(w, http.StatusForbidden, "not the provider for this appointment")
		return
	}

	newStarts := appt.StartsAt
	newEnds := appt.EndsAt
	if req.StartsAt != nil {
		newStarts, err = time.Parse(time.RFC3339Nano, *req.StartsAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "starts_at must be RFC3339")
			return
		}
	}
	if req.EndsAt != nil {
		newEnds, err = time.Parse(time.RFC3339Nano, *req.EndsAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "ends_at must be RFC3339")
			return
		}
	}
	if !newEnds.After(newStarts) {
		writeErr(w, http.StatusBadRequest, "ends_at must be after starts_at")
		return
	}

	rescheduling := !newStarts.Equal(appt.StartsAt) || !newEnds.Equal(appt.EndsAt)
	if rescheduling {
		overlap, err := checkOverlap(ctx, tx, id.TenantID, id.UserID, newStarts, newEnds, apptID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "overlap check failed")
			return
		}
		if overlap {
			writeErr(w, http.StatusConflict, "reschedule would overlap an existing appointment")
			return
		}
	}

	// Build the UPDATE dynamically — only set columns that changed.
	sets := []string{}
	args := []any{}
	if rescheduling {
		sets = append(sets, "starts_at", "ends_at")
		args = append(args, newStarts, newEnds)
	}
	if req.Status != nil && *req.Status != appt.Status {
		sets = append(sets, "status")
		args = append(args, *req.Status)
	}
	query := `update appointments set `
	for i, col := range sets {
		if i > 0 {
			query += ", "
		}
		query += col + fmt.Sprintf(` = $%d`, i+1)
	}
	args = append(args, apptID, id.TenantID)
	query += fmt.Sprintf(` where id = $%d and tenant_id = $%d`, len(args)-1, len(args))

	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "appointment not found")
		return
	}

	// Read back the updated row.
	err = tx.QueryRow(ctx, `
		select id, provider_id, patient_id, starts_at, ends_at, status, created_at
		from appointments where id = $1 and tenant_id = $2`,
		apptID, id.TenantID).Scan(
		&appt.ID, &appt.ProviderID, &appt.PatientID, &appt.StartsAt, &appt.EndsAt, &appt.Status, &appt.CreatedAt)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "readback failed")
		return
	}

	actions := []string{}
	if rescheduling {
		actions = append(actions, "reschedule")
	}
	if req.Status != nil && *req.Status != appt.Status {
		actions = append(actions, "status:"+*req.Status)
	}
	detail, _ := json.Marshal(map[string]any{"changes": actions})
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: "appointment.update", ResourceType: "appointment", ResourceID: apptID.String(),
		Detail: detail,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, appt)
}

// checkOverlap reports whether the provider has a scheduled appointment
// overlapping [startsAt, endsAt), excluding the appointment with excludeID
// (uuid.Nil excludes nothing — used for new appointments).
func checkOverlap(ctx context.Context, tx pgx.Tx, tenantID, providerID uuid.UUID, startsAt, endsAt time.Time, excludeID uuid.UUID) (bool, error) {
	var overlap bool
	err := tx.QueryRow(ctx, `
		select exists(
			select 1 from appointments
			where tenant_id = $1 and provider_id = $2 and status = 'scheduled'
			  and starts_at < $3 and ends_at > $4
			  and ($5::uuid = '00000000-0000-0000-0000-000000000000' or id <> $5))`,
		tenantID, providerID, endsAt, startsAt, excludeID).Scan(&overlap)
	return overlap, err
}
