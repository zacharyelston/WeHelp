package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// Reminders (#19). A worker in wehelpd serve wakes every reminder_interval
// and sends kind='reminder' messages from the provider to the patient:
//
//   - before each scheduled appointment, at each reminder_offsets offset
//     (only the closest one that has come due, so a late booking or a
//     restart never sends a burst);
//   - every checkin_every_days for a link where the provider set a cadence,
//     unless the patient already checked in during the last period, and
//     only during the day in the office's time zone.
//
// Each reminder is recorded in reminders_sent under a key naming it, in the
// same transaction as the message and its audit event, so restarts never
// send duplicates. Push carries only the fixed category (#7).

// Check-in reminders wait for the day, office time.
const (
	checkinDayStart = 8  // 08:00
	checkinDayEnd   = 20 // 20:00
)

// reminderOffsets parses reminder_offsets ("24h,1h"), largest first.
func (s *Server) reminderOffsets() []time.Duration {
	var out []time.Duration
	for _, f := range strings.Split(s.cfg.ReminderOffsets, ",") {
		if d, err := time.ParseDuration(strings.TrimSpace(f)); err == nil && d > 0 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// location is the office's time zone for reminder text and the check-in day.
func (s *Server) location() *time.Location {
	if s.cfg.Timezone != "" {
		if loc, err := time.LoadLocation(s.cfg.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

// RunReminders sends due reminders every interval until ctx ends.
func (s *Server) RunReminders(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		slog.Info("reminders off")
		return
	}
	slog.Info("reminders on", "interval", interval, "offsets", s.cfg.ReminderOffsets, "timezone", s.location().String())
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if n, err := s.sendDueReminders(ctx, time.Now()); err != nil && ctx.Err() == nil {
			slog.Warn("reminders: tick failed", "err", err)
		} else if n > 0 {
			slog.Info("reminders sent", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// dueReminder is one reminder ready to send.
type dueReminder struct {
	key                      string
	tenantID                 uuid.UUID
	providerID, patientID    uuid.UUID
	body, kind, pushCategory string
	meta                     map[string]any
}

// sendDueReminders sends every reminder due at now and returns how many.
func (s *Server) sendDueReminders(ctx context.Context, now time.Time) (int, error) {
	appts, err := s.dueAppointmentReminders(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("appointments: %w", err)
	}
	checkins, err := s.dueCheckinReminders(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("check-ins: %w", err)
	}
	sent := 0
	for _, d := range append(appts, checkins...) {
		ok, err := s.sendReminder(ctx, d)
		if err != nil {
			return sent, err
		}
		if ok {
			sent++
			s.notify(d.patientID, d.pushCategory)
		}
	}
	return sent, nil
}

func (s *Server) dueAppointmentReminders(ctx context.Context, now time.Time) ([]dueReminder, error) {
	offsets := s.reminderOffsets()
	if len(offsets) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		select a.id, a.tenant_id, a.provider_id, a.patient_id, a.starts_at, pr.display_name
		from appointments a
		join users pr on pr.id = a.provider_id
		join provider_patients pp on pp.provider_id = a.provider_id and pp.patient_id = a.patient_id
		where a.status = 'scheduled' and pp.status = 'active'
		  and a.starts_at > $1 and a.starts_at <= $2`,
		now, now.Add(offsets[0]))
	if err != nil {
		return nil, err
	}
	type appt struct {
		ID, Tenant, Provider, Patient uuid.UUID
		StartsAt                      time.Time
		ProviderName                  string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (appt, error) {
		var a appt
		err := r.Scan(&a.ID, &a.Tenant, &a.Provider, &a.Patient, &a.StartsAt, &a.ProviderName)
		return a, err
	})
	if err != nil {
		return nil, err
	}
	loc := s.location()
	var out []dueReminder
	for _, a := range list {
		// The closest offset that has come due (offsets are largest first).
		var due time.Duration
		for _, o := range offsets {
			if !now.Before(a.StartsAt.Add(-o)) {
				due = o
			}
		}
		if due == 0 {
			continue
		}
		with := a.ProviderName
		if with == "" {
			with = "your provider"
		}
		out = append(out, dueReminder{
			key:      fmt.Sprintf("appt:%s:%d:%s", a.ID, a.StartsAt.Unix(), due),
			tenantID: a.Tenant, providerID: a.Provider, patientID: a.Patient,
			kind:         "appointment",
			pushCategory: "appointment_reminder",
			body: fmt.Sprintf("Reminder: you have an appointment with %s on %s.",
				with, a.StartsAt.In(loc).Format("Mon, Jan 2 at 3:04 PM")),
			meta: map[string]any{"reminder": "appointment", "appointment_id": a.ID, "starts_at": a.StartsAt, "offset": due.String()},
		})
	}
	return out, nil
}

func (s *Server) dueCheckinReminders(ctx context.Context, now time.Time) ([]dueReminder, error) {
	if h := now.In(s.location()).Hour(); h < checkinDayStart || h >= checkinDayEnd {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		select pp.tenant_id, pp.provider_id, pp.patient_id, pp.checkin_every_days, pp.checkin_since, pr.display_name
		from provider_patients pp
		join users pr on pr.id = pp.provider_id
		where pp.status = 'active' and pp.checkin_every_days is not null and pp.checkin_since is not null`)
	if err != nil {
		return nil, err
	}
	type link struct {
		Tenant, Provider, Patient uuid.UUID
		Every                     int
		Since                     time.Time
		ProviderName              string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (link, error) {
		var l link
		err := r.Scan(&l.Tenant, &l.Provider, &l.Patient, &l.Every, &l.Since, &l.ProviderName)
		return l, err
	})
	if err != nil {
		return nil, err
	}
	var out []dueReminder
	for _, l := range list {
		every := time.Duration(l.Every) * 24 * time.Hour
		n := int64(now.Sub(l.Since) / every)
		if n < 1 {
			continue
		}
		// Already checked in during the last period: nothing to remind.
		var recent bool
		periodStart := l.Since.Add(time.Duration(n) * every)
		if err := s.pool.QueryRow(ctx, `
			select exists(select 1 from messages
				where tenant_id = $1 and sender_id = $2 and recipient_id = $3
				  and kind = 'checkin' and created_at >= $4)`,
			l.Tenant, l.Patient, l.Provider, periodStart.Add(-every)).Scan(&recent); err != nil {
			return nil, err
		}
		if recent {
			continue
		}
		with := l.ProviderName
		if with == "" {
			with = "your provider"
		}
		out = append(out, dueReminder{
			key:      fmt.Sprintf("checkin:%s:%s:%d:%d", l.Provider, l.Patient, l.Since.Unix(), n),
			tenantID: l.Tenant, providerID: l.Provider, patientID: l.Patient,
			kind:         "checkin",
			pushCategory: "new_message",
			body:         fmt.Sprintf("Time for your check-in with %s. Reply here to let them know how you're doing.", with),
			meta:         map[string]any{"reminder": "checkin", "every_days": l.Every},
		})
	}
	return out, nil
}

// sendReminder writes the message, its audit event and its reminders_sent
// row in one transaction. It returns false if the reminder was already sent.
func (s *Server) sendReminder(ctx context.Context, d dueReminder) (bool, error) {
	var sent bool
	if err := s.pool.QueryRow(ctx, `select exists(select 1 from reminders_sent where key = $1)`, d.key).Scan(&sent); err != nil || sent {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	meta, _ := json.Marshal(d.meta)
	var msgID uuid.UUID
	if err := tx.QueryRow(ctx, `
		insert into messages (tenant_id, sender_id, recipient_id, kind, body, metadata)
		values ($1, $2, $3, 'reminder', $4, $5) returning id`,
		d.tenantID, d.providerID, d.patientID, d.body, meta).Scan(&msgID); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `
		insert into reminders_sent (key, tenant_id, message_id) values ($1, $2, $3)
		on conflict (key) do nothing`, d.key, d.tenantID, msgID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil // sent before; the rollback drops the new message
	}
	detail, _ := json.Marshal(map[string]string{"reminder": d.kind, "recipient": d.patientID.String()})
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: d.tenantID, // a system action: no actor
		Action:   "reminder.create", ResourceType: "message", ResourceID: msgID.String(),
		Detail: detail,
	}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

type checkinCadenceRequest struct {
	EveryDays *int `json:"every_days"` // null turns check-in reminders off
}

// SetCheckinCadence lets a provider set how often a linked patient is
// reminded to check in (#19). Setting it restarts the periods from now.
// Route: PUT /api/v1/links/{userID}/checkin
func (s *Server) SetCheckinCadence(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.Role != "provider" {
		writeErr(w, http.StatusForbidden, "only providers set check-in reminders")
		return
	}
	patient, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var req checkinCadenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.EveryDays != nil && (*req.EveryDays < 1 || *req.EveryDays > 90) {
		writeErr(w, http.StatusBadRequest, "every_days must be 1-90, or null to turn off")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		update provider_patients
		set checkin_every_days = $4, checkin_since = case when $4::int is null then null else now() end
		where tenant_id = $1 and provider_id = $2 and patient_id = $3 and status = 'active'`,
		id.TenantID, id.UserID, patient, req.EveryDays)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "no active link with that patient")
		return
	}
	detail, _ := json.Marshal(map[string]any{"every_days": req.EveryDays})
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: "link.checkin_cadence", ResourceType: "link", ResourceID: patient.String(),
		Detail: detail,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"every_days": req.EveryDays})
}
