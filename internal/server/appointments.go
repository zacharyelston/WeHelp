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

// validTransition reports whether an appointment may move from one status to
// another. Only scheduled appointments change status; the others are final.
// Staying in the same status is not a transition.
func validTransition(from, to string) bool {
	return from == "scheduled" && to != "scheduled" && appointmentStatuses[to]
}

// lockProviderSchedule serialises booking changes for one provider within the
// current transaction, so two concurrent requests cannot both pass the
// overlap check and double-book.
func lockProviderSchedule(ctx context.Context, tx pgx.Tx, providerID uuid.UUID) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1::text, 0))`, providerID)
	return err
}

type appointmentView struct {
	ID           uuid.UUID `json:"id"`
	ProviderID   uuid.UUID `json:"provider_id"`
	PatientID    uuid.UUID `json:"patient_id"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	ProviderName string    `json:"provider_name,omitempty"` // in lists (#106)
	PatientName  string    `json:"patient_name,omitempty"`
	// Video visit state (#6): open or ended, with its times; absent if none.
	VisitStatus   *string    `json:"visit_status,omitempty"`
	VisitOpenedAt *time.Time `json:"visit_opened_at,omitempty"`
	VisitEndedAt  *time.Time `json:"visit_ended_at,omitempty"`
}

type createAppointmentRequest struct {
	PatientID uuid.UUID `json:"patient_id"`
	StartsAt  string    `json:"starts_at"`
	EndsAt    string    `json:"ends_at"`
	// ProviderID is required when the front desk or an admin books (#106);
	// a provider always books for themself.
	ProviderID *uuid.UUID `json:"provider_id"`
}

// bookingProvider returns whose schedule the caller is booking: their own
// for a provider, the named, enabled provider for the front desk or an
// admin (#106). ok is false after an error has been written.
func bookingProvider(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, id Identity, requested *uuid.UUID) (uuid.UUID, bool) {
	switch {
	case id.Role == "provider":
		if requested != nil && *requested != id.UserID {
			writeErr(w, http.StatusForbidden, "providers book only their own schedule")
			return uuid.Nil, false
		}
		return id.UserID, true
	case practiceWide(id.Role):
		if requested == nil {
			writeErr(w, http.StatusBadRequest, "provider_id is required")
			return uuid.Nil, false
		}
		var ok bool
		err := tx.QueryRow(ctx, `select exists (select 1 from users where id = $1 and tenant_id = $2
			and role = 'provider' and disabled_at is null)`, *requested, id.TenantID).Scan(&ok)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "provider check failed")
			return uuid.Nil, false
		}
		if !ok {
			writeErr(w, http.StatusBadRequest, "no such provider in this practice")
			return uuid.Nil, false
		}
		return *requested, true
	}
	writeErr(w, http.StatusForbidden, "only staff can book appointments")
	return uuid.Nil, false
}

// linkForBooking makes the provider–patient link active for a booking by
// the front desk or an admin: it creates it, or activates a pending invite,
// with a link.create audit event in the booking's transaction. A revoked
// link stays revoked: the patient or provider ended it. It returns 0, or
// an HTTP status and message.
func linkForBooking(ctx context.Context, tx pgx.Tx, id Identity, providerID, patientID uuid.UUID) (int, string) {
	var isPatient bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from users where id = $1 and tenant_id = $2
		and role = 'patient' and disabled_at is null)`, patientID, id.TenantID).Scan(&isPatient); err != nil {
		return http.StatusInternalServerError, "patient check failed"
	}
	if !isPatient {
		return http.StatusBadRequest, "no such patient in this practice"
	}
	tag, err := tx.Exec(ctx, `
		insert into provider_patients (tenant_id, provider_id, patient_id, status)
		values ($1, $2, $3, 'active')
		on conflict (provider_id, patient_id) do update set status = 'active'
		where provider_patients.status = 'pending' and provider_patients.tenant_id = excluded.tenant_id`,
		id.TenantID, providerID, patientID)
	if err != nil {
		return http.StatusInternalServerError, "link failed"
	}
	if tag.RowsAffected() == 0 {
		return http.StatusConflict, "this patient's link with that provider was revoked; the provider can invite them again"
	}
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: "link.create", ResourceType: "provider_patient",
		ResourceID: providerID.String() + ":" + patientID.String(),
		Detail:     mustJSON(map[string]string{"via": "booking"}),
	}); err != nil {
		return http.StatusInternalServerError, "audit failed"
	}
	return 0, ""
}

type updateAppointmentRequest struct {
	StartsAt *string `json:"starts_at"`
	EndsAt   *string `json:"ends_at"`
	Status   *string `json:"status"`
}

// CreateAppointment schedules a new appointment. A provider books their
// own patients (an active link is required); the front desk or an admin
// books any provider, and the booking makes the link if there is none.
// Rejects overlapping appointments for the same provider.
// Route: POST /api/v1/appointments
func (s *Server) CreateAppointment(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if !isStaffRole(id.Role) {
		writeErr(w, http.StatusForbidden, "only staff can create appointments")
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

	providerID, ok := bookingProvider(ctx, tx, w, id, req.ProviderID)
	if !ok {
		return
	}
	linked, err := HasActiveLink(ctx, tx, id.TenantID, providerID, req.PatientID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "link check failed")
		return
	}
	if !linked && practiceWide(id.Role) {
		// The front desk or an admin booking a provider with a patient is
		// how the practice assigns them: the booking makes the link.
		status, msg := linkForBooking(ctx, tx, id, providerID, req.PatientID)
		if status != 0 {
			writeErr(w, status, msg)
			return
		}
		linked = true
	}
	if !linked {
		writeErr(w, http.StatusForbidden, "no active link between that provider and patient")
		return
	}

	if err := lockProviderSchedule(ctx, tx, providerID); err != nil {
		writeErr(w, http.StatusInternalServerError, "lock failed")
		return
	}
	overlap, err := checkOverlap(ctx, tx, id.TenantID, providerID, startsAt, endsAt, uuid.Nil)
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
		id.TenantID, providerID, req.PatientID, startsAt, endsAt).
		Scan(&appt.ID, &appt.ProviderID, &appt.PatientID, &appt.StartsAt, &appt.EndsAt, &appt.Status, &appt.CreatedAt)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}

	detail, _ := json.Marshal(map[string]string{
		"patient_id":  req.PatientID.String(),
		"provider_id": providerID.String(),
		"starts_at":   startsAt.Format(time.RFC3339),
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

	// The front desk and admins see the whole practice (#106), optionally
	// one provider's day with ?provider_id=.
	base := `
		select a.id, a.provider_id, a.patient_id, a.starts_at, a.ends_at, a.status, a.created_at,
		       pr.display_name, pa.display_name, v.status, v.opened_at, v.ended_at
		from appointments a
		join users pr on pr.id = a.provider_id
		join users pa on pa.id = a.patient_id
		left join visits v on v.appointment_id = a.id
		where a.tenant_id = $1`
	args := []any{id.TenantID}
	if practiceWide(id.Role) {
		if v := q.Get("provider_id"); v != "" {
			pid, err := uuid.Parse(v)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "provider_id must be a UUID")
				return
			}
			base += ` and a.provider_id = $2`
			args = append(args, pid)
		}
	} else {
		base += ` and (a.provider_id = $2 or a.patient_id = $2)`
		args = append(args, id.UserID)
	}
	if v := q.Get("from"); v != "" {
		from, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "from must be RFC3339")
			return
		}
		base += fmt.Sprintf(` and a.starts_at >= $%d`, len(args)+1)
		args = append(args, from)
	}
	if v := q.Get("to"); v != "" {
		to, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "to must be RFC3339")
			return
		}
		base += fmt.Sprintf(` and a.starts_at < $%d`, len(args)+1)
		args = append(args, to)
	}
	base += ` order by a.starts_at asc`

	rows, err := s.pool.Query(r.Context(), base, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	appts := []appointmentView{}
	for rows.Next() {
		var a appointmentView
		if err := rows.Scan(&a.ID, &a.ProviderID, &a.PatientID, &a.StartsAt, &a.EndsAt, &a.Status, &a.CreatedAt, &a.ProviderName, &a.PatientName,
			&a.VisitStatus, &a.VisitOpenedAt, &a.VisitEndedAt); err != nil {
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
	if id.Role != "provider" && !practiceWide(id.Role) {
		writeErr(w, http.StatusForbidden, "only staff can update appointments")
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
	if appt.ProviderID != id.UserID && !practiceWide(id.Role) {
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
	statusChange := req.Status != nil && *req.Status != appt.Status
	if statusChange && !validTransition(appt.Status, *req.Status) {
		writeErr(w, http.StatusConflict, fmt.Sprintf("cannot change status from %s to %s", appt.Status, *req.Status))
		return
	}
	if rescheduling && appt.Status != "scheduled" {
		writeErr(w, http.StatusConflict, "only scheduled appointments can be rescheduled")
		return
	}
	if !rescheduling && !statusChange {
		writeJSON(w, http.StatusOK, appt) // nothing changed; no write, no audit
		return
	}
	if rescheduling {
		if err := lockProviderSchedule(ctx, tx, appt.ProviderID); err != nil {
			writeErr(w, http.StatusInternalServerError, "lock failed")
			return
		}
		overlap, err := checkOverlap(ctx, tx, id.TenantID, appt.ProviderID, newStarts, newEnds, apptID)
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
	if statusChange {
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
	if statusChange {
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
