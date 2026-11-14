package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// Appointment requests (#68).
//
//   - A patient asks a linked provider for a visit, with 1-3 preferred
//     days and times of day and an optional reason. At most 3 pending at a
//     time per provider.
//   - The patient may cancel a request while it is pending. To cancel a
//     scheduled appointment they message the practice (out of scope here).
//   - The provider confirms with an exact time, which books a scheduled
//     appointment (the usual overlap rules apply), or declines with an
//     optional note.
//   - Either decision leaves a message in the patient's thread and sends
//     the generic "appointment_update" push. A new request sends the
//     provider's devices "appointment_request". Neither push carries details.

const maxPendingRequests = 3

var requestPeriods = map[string]bool{"morning": true, "afternoon": true, "evening": true, "any": true}

type preferredTime struct {
	Date   string `json:"date"`   // YYYY-MM-DD, office time
	Period string `json:"period"` // morning, afternoon, evening or any
}

type appointmentRequestView struct {
	ID            uuid.UUID       `json:"id"`
	PatientID     uuid.UUID       `json:"patient_id"`
	ProviderID    uuid.UUID       `json:"provider_id"`
	Preferred     []preferredTime `json:"preferred"`
	Reason        string          `json:"reason"`
	Status        string          `json:"status"`
	AppointmentID *uuid.UUID      `json:"appointment_id"`
	Note          string          `json:"note"`
	CreatedAt     time.Time       `json:"created_at"`
	DecidedAt     *time.Time      `json:"decided_at"`
}

const requestColumns = `id, patient_id, provider_id, preferred, reason, status, appointment_id, note, created_at, decided_at`

func scanRequest(row pgx.Row) (appointmentRequestView, error) {
	var v appointmentRequestView
	var pref []byte
	err := row.Scan(&v.ID, &v.PatientID, &v.ProviderID, &pref, &v.Reason, &v.Status, &v.AppointmentID, &v.Note, &v.CreatedAt, &v.DecidedAt)
	if err == nil {
		err = json.Unmarshal(pref, &v.Preferred)
	}
	return v, err
}

// validPreferred checks 1-3 choices, each today or later (office time) and
// within 180 days, with a known period.
func (s *Server) validPreferred(p []preferredTime, now time.Time) error {
	if len(p) < 1 || len(p) > 3 {
		return errors.New("give 1 to 3 preferred times")
	}
	loc := s.location()
	today := now.In(loc).Format(time.DateOnly)
	last := now.In(loc).AddDate(0, 0, 180).Format(time.DateOnly)
	for _, t := range p {
		d, err := time.ParseInLocation(time.DateOnly, t.Date, loc)
		if err != nil || d.Format(time.DateOnly) != t.Date {
			return errors.New("preferred dates must be YYYY-MM-DD")
		}
		if t.Date < today || t.Date > last {
			return errors.New("preferred dates must be from today to 180 days out")
		}
		if !requestPeriods[t.Period] {
			return errors.New("period must be morning, afternoon, evening or any")
		}
	}
	return nil
}

type createRequestBody struct {
	ProviderID uuid.UUID       `json:"provider_id"`
	Preferred  []preferredTime `json:"preferred"`
	Reason     string          `json:"reason"`
}

// CreateAppointmentRequest lets a patient ask a linked provider for a visit.
// Route: POST /api/v1/appointment-requests
func (s *Server) CreateAppointmentRequest(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.Role != "patient" {
		writeErr(w, http.StatusForbidden, "only patients request appointments")
		return
	}
	var req createRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if len(req.Reason) > 500 {
		writeErr(w, http.StatusBadRequest, "reason is limited to 500 characters")
		return
	}
	if err := s.validPreferred(req.Preferred, time.Now()); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	var linked bool
	if err := tx.QueryRow(ctx, `
		select exists(select 1 from provider_patients
			where tenant_id = $1 and provider_id = $2 and patient_id = $3 and status = 'active')`,
		id.TenantID, req.ProviderID, id.UserID).Scan(&linked); err != nil {
		writeErr(w, http.StatusInternalServerError, "link check failed")
		return
	}
	if !linked {
		writeErr(w, http.StatusForbidden, "no active link with that provider")
		return
	}
	// One patient's requests to one provider are counted under a lock, so
	// two at once can't both slip under the limit.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1::text || $2::text, 1))`, id.UserID, req.ProviderID); err != nil {
		writeErr(w, http.StatusInternalServerError, "lock failed")
		return
	}
	var pending int
	if err := tx.QueryRow(ctx, `
		select count(*) from appointment_requests
		where tenant_id = $1 and patient_id = $2 and provider_id = $3 and status = 'pending'`,
		id.TenantID, id.UserID, req.ProviderID).Scan(&pending); err != nil {
		writeErr(w, http.StatusInternalServerError, "count failed")
		return
	}
	if pending >= maxPendingRequests {
		writeErr(w, http.StatusConflict, fmt.Sprintf("you already have %d requests waiting; cancel one first", pending))
		return
	}

	pref, _ := json.Marshal(req.Preferred)
	v, err := scanRequest(tx.QueryRow(ctx, `
		insert into appointment_requests (tenant_id, patient_id, provider_id, preferred, reason)
		values ($1, $2, $3, $4, $5) returning `+requestColumns,
		id.TenantID, id.UserID, req.ProviderID, pref, req.Reason))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}
	if err := s.auditRequest(ctx, tx, id, "appointment_request.create", v.ID, map[string]any{"provider": req.ProviderID}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	s.notify(req.ProviderID, "appointment_request")
	writeJSON(w, http.StatusCreated, v)
}

// ListAppointmentRequests returns the caller's requests: a patient's own, or
// those addressed to a provider. ?status= filters; newest first, 100 at most.
// Route: GET /api/v1/appointment-requests
func (s *Server) ListAppointmentRequests(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	q := `select ` + requestColumns + ` from appointment_requests
		where tenant_id = $1 and (patient_id = $2 or provider_id = $2)`
	args := []any{id.TenantID, id.UserID}
	if st := r.URL.Query().Get("status"); st != "" {
		q += ` and status = $3`
		args = append(args, st)
	}
	q += ` order by created_at desc limit 100`
	rows, err := s.pool.Query(r.Context(), q, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (appointmentRequestView, error) { return scanRequest(row) })
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "scan failed")
		return
	}
	if list == nil {
		list = []appointmentRequestView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": list})
}

// pendingRequest locks a pending request the caller may act on, in role:
// "patient" for their own, "provider" for one addressed to them.
func pendingRequest(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, r *http.Request, id Identity, role string) (appointmentRequestView, bool) {
	reqID, err := uuid.Parse(chi.URLParam(r, "requestID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request id")
		return appointmentRequestView{}, false
	}
	v, err := scanRequest(tx.QueryRow(ctx, `select `+requestColumns+` from appointment_requests
		where id = $1 and tenant_id = $2 for update`, reqID, id.TenantID))
	mine := err == nil && ((role == "patient" && v.PatientID == id.UserID) || (role == "provider" && v.ProviderID == id.UserID))
	switch {
	case errors.Is(err, pgx.ErrNoRows) || (err == nil && !mine):
		writeErr(w, http.StatusNotFound, "request not found")
		return v, false
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "query failed")
		return v, false
	case v.Status != "pending":
		writeErr(w, http.StatusConflict, "this request was already "+v.Status)
		return v, false
	}
	return v, true
}

// CancelAppointmentRequest lets a patient withdraw a pending request.
// Route: POST /api/v1/appointment-requests/{requestID}/cancel
func (s *Server) CancelAppointmentRequest(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.Role != "patient" {
		writeErr(w, http.StatusForbidden, "only the patient cancels a request")
		return
	}
	s.decide(w, r, id, "patient", func(ctx context.Context, tx pgx.Tx, v *appointmentRequestView) (string, int, error) {
		_, err := tx.Exec(ctx, `update appointment_requests set status = 'cancelled', decided_at = now() where id = $1`, v.ID)
		v.Status = "cancelled"
		return "", 0, err
	})
}

type confirmRequestBody struct {
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}

// ConfirmAppointmentRequest books the visit at the time the provider picks.
// Route: POST /api/v1/appointment-requests/{requestID}/confirm
func (s *Server) ConfirmAppointmentRequest(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.Role != "provider" {
		writeErr(w, http.StatusForbidden, "only providers confirm requests")
		return
	}
	var body confirmRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	start, err1 := time.Parse(time.RFC3339Nano, body.StartsAt)
	end, err2 := time.Parse(time.RFC3339Nano, body.EndsAt)
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusBadRequest, "starts_at and ends_at must be RFC3339")
		return
	}
	if !end.After(start) || !start.After(time.Now()) {
		writeErr(w, http.StatusBadRequest, "the visit must be in the future and end after it starts")
		return
	}
	s.decide(w, r, id, "provider", func(ctx context.Context, tx pgx.Tx, v *appointmentRequestView) (string, int, error) {
		linked, err := HasActiveLink(ctx, tx, id.TenantID, id.UserID, v.PatientID)
		if err != nil {
			return "", 0, err
		}
		if !linked {
			return "", http.StatusForbidden, errors.New("no active link with that patient")
		}
		if err := lockProviderSchedule(ctx, tx, id.UserID); err != nil {
			return "", 0, err
		}
		overlap, err := checkOverlap(ctx, tx, id.TenantID, id.UserID, start, end, uuid.Nil)
		if err != nil {
			return "", 0, err
		}
		if overlap {
			return "", http.StatusConflict, errors.New("overlapping appointment exists")
		}
		var apptID uuid.UUID
		if err := tx.QueryRow(ctx, `
			insert into appointments (tenant_id, provider_id, patient_id, starts_at, ends_at)
			values ($1, $2, $3, $4, $5) returning id`,
			id.TenantID, id.UserID, v.PatientID, start, end).Scan(&apptID); err != nil {
			return "", 0, err
		}
		detail, _ := json.Marshal(map[string]string{"patient_id": v.PatientID.String(), "starts_at": start.Format(time.RFC3339), "request": v.ID.String()})
		if err := audit.Record(ctx, tx, audit.Event{
			TenantID: id.TenantID, ActorID: &id.UserID,
			Action: "appointment.create", ResourceType: "appointment", ResourceID: apptID.String(), Detail: detail,
		}); err != nil {
			return "", 0, err
		}
		if _, err := tx.Exec(ctx, `update appointment_requests set status = 'confirmed', appointment_id = $2, decided_at = now() where id = $1`, v.ID, apptID); err != nil {
			return "", 0, err
		}
		v.Status, v.AppointmentID = "confirmed", &apptID
		return fmt.Sprintf("Your appointment request is confirmed: %s.", start.In(s.location()).Format("Mon, Jan 2 at 3:04 PM")), 0, nil
	})
}

type declineRequestBody struct {
	Note string `json:"note"`
}

// DeclineAppointmentRequest turns a request down, with an optional note.
// Route: POST /api/v1/appointment-requests/{requestID}/decline
func (s *Server) DeclineAppointmentRequest(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.Role != "provider" {
		writeErr(w, http.StatusForbidden, "only providers decline requests")
		return
	}
	var body declineRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	body.Note = strings.TrimSpace(body.Note)
	if len(body.Note) > 500 {
		writeErr(w, http.StatusBadRequest, "note is limited to 500 characters")
		return
	}
	s.decide(w, r, id, "provider", func(ctx context.Context, tx pgx.Tx, v *appointmentRequestView) (string, int, error) {
		if _, err := tx.Exec(ctx, `update appointment_requests set status = 'declined', note = $2, decided_at = now() where id = $1`, v.ID, body.Note); err != nil {
			return "", 0, err
		}
		v.Status, v.Note = "declined", body.Note
		msg := "Your appointment request was declined."
		if body.Note != "" {
			msg += " " + body.Note
		}
		return msg, 0, nil
	})
}

// decide runs one transition on a pending request in a transaction with its
// audit event. If change returns a message, it goes to the patient's thread
// from the provider, and the patient's devices get "appointment_update".
func (s *Server) decide(w http.ResponseWriter, r *http.Request, id Identity, role string,
	change func(ctx context.Context, tx pgx.Tx, v *appointmentRequestView) (msg string, status int, err error)) {
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	v, ok := pendingRequest(ctx, tx, w, r, id, role)
	if !ok {
		return
	}
	msg, status, err := change(ctx, tx, &v)
	if err != nil {
		if status == 0 {
			writeErr(w, http.StatusInternalServerError, "update failed")
		} else {
			writeErr(w, status, err.Error())
		}
		return
	}
	detail := map[string]any{"status": v.Status}
	if v.AppointmentID != nil {
		detail["appointment"] = v.AppointmentID
	}
	if msg != "" {
		var msgID uuid.UUID
		meta, _ := json.Marshal(map[string]any{"appointment_request": v.ID, "status": v.Status})
		if err := tx.QueryRow(ctx, `
			insert into messages (tenant_id, sender_id, recipient_id, kind, body, metadata)
			values ($1, $2, $3, 'message', $4, $5) returning id`,
			id.TenantID, v.ProviderID, v.PatientID, msg, meta).Scan(&msgID); err != nil {
			writeErr(w, http.StatusInternalServerError, "message failed")
			return
		}
		detail["message"] = msgID
	}
	action := map[string]string{"confirmed": "confirm", "declined": "decline", "cancelled": "cancel"}[v.Status]
	if err := s.auditRequest(ctx, tx, id, "appointment_request."+action, v.ID, detail); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	if msg != "" {
		s.notify(v.PatientID, "appointment_update")
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) auditRequest(ctx context.Context, tx pgx.Tx, id Identity, action string, reqID uuid.UUID, detail map[string]any) error {
	d, _ := json.Marshal(detail)
	return audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: action, ResourceType: "appointment_request", ResourceID: reqID.String(), Detail: d,
	})
}
