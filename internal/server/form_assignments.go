package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// Form assignments and occurrences (#196). Staff assign a published form to
// a patient, once or on a schedule; each due instance is an occurrence with
// a window. The runner creates occurrences ahead, marks unfinished ones
// missed once they close, and sends the PHI-free form_due push when one
// opens and once more 24 hours before it closes.
//
// Times follow docs/V2_CONTRACTS.md ("Forms"): the practice's IANA zone is
// pinned on the assignment; due dates advance by local calendar days or
// weeks; due, opening and closing times are 09:00 local; opening is
// inclusive and closing exclusive. An occurrence is identified by its
// assignment and local due date, so runs repeat without duplicates.
//
// Who may assign follows appointments: a provider for their linked
// patients, the front desk and admins for the practice. Staff routes are
// office-LAN only; the patient's /me/forms comes through the Relay.

const (
	formsHorizon        = 14 * 24 * time.Hour // occurrences are created this far ahead
	formsMaxOccurrences = 366                 // per assignment
	formsDueHour        = 9                   // 09:00 local
	formsCloseReminder  = 24 * time.Hour      // the second form_due, before closing
)

// formSchedule is an assignment's recurrence.
type formSchedule struct {
	Kind      string  `json:"kind"`            // once, days or weeks
	Every     int     `json:"every"`           // every N days or weeks; 1 for once
	StartDate string  `json:"start_date"`      // YYYY-MM-DD, the first due date
	Count     *int    `json:"count,omitempty"` // stop after this many
	Until     *string `json:"until,omitempty"` // or after this date (inclusive)
}

func parseCivilDate(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", s)
	return t, err == nil
}

// check validates the schedule and returns its start and end dates.
func (sch *formSchedule) check() (start time.Time, until *time.Time, err error) {
	switch sch.Kind {
	case "once":
		sch.Every, sch.Count, sch.Until = 1, nil, nil
	case "days", "weeks":
		if sch.Every < 1 || sch.Every > 365 {
			return start, nil, errors.New("every must be from 1 to 365")
		}
		if sch.Count == nil && sch.Until == nil {
			return start, nil, errors.New("a repeating schedule needs a count or an until date")
		}
		if sch.Count != nil && (*sch.Count < 1 || *sch.Count > formsMaxOccurrences) {
			return start, nil, fmt.Errorf("count must be from 1 to %d", formsMaxOccurrences)
		}
	default:
		return start, nil, errors.New("schedule kind must be once, days or weeks")
	}
	start, ok := parseCivilDate(sch.StartDate)
	if !ok {
		return start, nil, errors.New("start_date must be YYYY-MM-DD")
	}
	if sch.Until != nil {
		u, ok := parseCivilDate(*sch.Until)
		if !ok || u.Before(start) {
			return start, nil, errors.New("until must be a date (YYYY-MM-DD) on or after start_date")
		}
		until = &u
	}
	return start, until, nil
}

// dueDate is the k-th scheduled local date (k from 0).
func (sch formSchedule) dueDate(start time.Time, k int) time.Time {
	step := sch.Every
	if sch.Kind == "weeks" {
		step *= 7
	}
	return start.AddDate(0, 0, k*step)
}

// localAt returns hour:00 on date d in loc. If that wall time happens twice
// (a fall-back change), the earlier instant; if it doesn't exist (spring
// forward), the next valid instant, which is what time.Date gives.
func localAt(d time.Time, hour int, loc *time.Location) time.Time {
	t := time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, loc)
	if earlier := t.Add(-time.Hour); earlier.In(loc).Hour() == hour && earlier.In(loc).Day() == d.Day() {
		return earlier
	}
	return t
}

// window is an occurrence's opening, due and closing times.
func window(due time.Time, openBefore, closeAfter int, loc *time.Location) (opens, dueAt, closes time.Time) {
	return localAt(due.AddDate(0, 0, -openBefore), formsDueHour, loc),
		localAt(due, formsDueHour, loc),
		localAt(due.AddDate(0, 0, closeAfter), formsDueHour, loc)
}

// assignmentRow is what the runner needs of an assignment.
type assignmentRow struct {
	ID, TenantID, PatientID, FormID uuid.UUID
	VersionID                       *uuid.UUID
	Schedule                        formSchedule
	OpenBefore, CloseAfter          int
	Timezone, Channel               string
	CreatedAt                       time.Time
}

type plannedOccurrence struct {
	Due                  time.Time
	Opens, DueAt, Closes time.Time
}

// plan lists the occurrences the assignment has by horizon: those whose
// window opens by then and that hadn't already closed when it was assigned.
func (a assignmentRow) plan(horizon time.Time) ([]plannedOccurrence, error) {
	loc, err := time.LoadLocation(a.Timezone)
	if err != nil {
		return nil, err
	}
	start, until, err := a.Schedule.check()
	if err != nil {
		return nil, err
	}
	var out []plannedOccurrence
	for k := 0; k < formsMaxOccurrences; k++ {
		if a.Schedule.Kind == "once" && k > 0 {
			break
		}
		if a.Schedule.Count != nil && k >= *a.Schedule.Count {
			break
		}
		due := a.Schedule.dueDate(start, k)
		if until != nil && due.After(*until) {
			break
		}
		opens, dueAt, closes := window(due, a.OpenBefore, a.CloseAfter, loc)
		if opens.After(horizon) {
			break
		}
		if !closes.After(a.CreatedAt) {
			continue // already over when it was assigned
		}
		out = append(out, plannedOccurrence{due, opens, dueAt, closes})
	}
	return out, nil
}

const assignmentColumns = `a.id, a.tenant_id, a.patient_id, a.form_id, a.version_id, a.schedule_kind, a.every,
	a.start_date::text, a.end_count, a.end_date::text, a.open_before_days, a.close_after_days, a.timezone, a.channel, a.created_at`

func scanAssignment(row pgx.Row) (assignmentRow, error) {
	var a assignmentRow
	err := row.Scan(&a.ID, &a.TenantID, &a.PatientID, &a.FormID, &a.VersionID, &a.Schedule.Kind, &a.Schedule.Every,
		&a.Schedule.StartDate, &a.Schedule.Count, &a.Schedule.Until, &a.OpenBefore, &a.CloseAfter, &a.Timezone, &a.Channel, &a.CreatedAt)
	return a, err
}

// generateOccurrences creates the assignment's missing occurrences up to
// horizon and returns how many it created. Each is audited with no actor
// (the system). The version is the assignment's pinned one, or the form's
// latest published version at creation.
func generateOccurrences(ctx context.Context, tx pgx.Tx, a assignmentRow, horizon time.Time) (int, error) {
	planned, err := a.plan(horizon)
	if err != nil || len(planned) == 0 {
		return 0, err
	}
	versionID := a.VersionID
	if versionID == nil {
		var v uuid.UUID
		err := tx.QueryRow(ctx, `select id from form_versions where form_id = $1 and status = 'published'
			order by version desc limit 1`, a.FormID).Scan(&v)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil // nothing published (yet): try again next run
		}
		if err != nil {
			return 0, err
		}
		versionID = &v
	}
	n := 0
	for _, p := range planned {
		var oid uuid.UUID
		err := tx.QueryRow(ctx, `insert into form_occurrences
			(tenant_id, assignment_id, patient_id, form_id, version_id, due_date, opens_at, due_at, closes_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			on conflict (assignment_id, due_date) do nothing returning id`,
			a.TenantID, a.ID, a.PatientID, a.FormID, *versionID, p.Due, p.Opens, p.DueAt, p.Closes).Scan(&oid)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // already there
		}
		if err != nil {
			return n, err
		}
		detail, _ := json.Marshal(map[string]any{"assignment_id": a.ID, "due_date": p.Due.Format("2006-01-02"), "version_id": *versionID})
		if err := audit.Record(ctx, tx, audit.Event{TenantID: a.TenantID, Action: "form_occurrence.create",
			ResourceType: "form_occurrence", ResourceID: oid.String(), Detail: detail}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// --- views --------------------------------------------------------------------

type occurrenceView struct {
	ID           uuid.UUID  `json:"id"`
	AssignmentID uuid.UUID  `json:"assignment_id"`
	VersionID    uuid.UUID  `json:"version_id"`
	Version      int        `json:"version"`
	DueDate      string     `json:"due_date"`
	OpensAt      time.Time  `json:"opens_at"`
	DueAt        time.Time  `json:"due_at"`
	ClosesAt     time.Time  `json:"closes_at"`
	Status       string     `json:"status"` // open, completed, missed, cancelled
	State        string     `json:"state"`  // upcoming, open, completed, missed, cancelled (open split by time)
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// occurrenceState splits status open into upcoming and open by the window.
func occurrenceState(status string, opens, closes, now time.Time) string {
	if status != "open" {
		return status
	}
	switch {
	case now.Before(opens):
		return "upcoming"
	case now.Before(closes):
		return "open"
	}
	return "missed" // closed; the runner will mark it
}

type assignmentView struct {
	ID             uuid.UUID        `json:"id"`
	PatientID      uuid.UUID        `json:"patient_id"`
	FormID         uuid.UUID        `json:"form_id"`
	FormName       string           `json:"form_name"`
	VersionID      *uuid.UUID       `json:"version_id"` // null: the latest published version
	Schedule       formSchedule     `json:"schedule"`
	OpenBeforeDays int              `json:"open_before_days"`
	CloseAfterDays int              `json:"close_after_days"`
	Timezone       string           `json:"timezone"`
	Channel        string           `json:"channel"`
	AssignedBy     uuid.UUID        `json:"assigned_by"`
	AssignedByName string           `json:"assigned_by_name"`
	CreatedAt      time.Time        `json:"created_at"`
	EndedAt        *time.Time       `json:"ended_at,omitempty"`
	Occurrences    []occurrenceView `json:"occurrences"`
}

func loadAssignments(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, tenant uuid.UUID, patient *uuid.UUID, assignment *uuid.UUID, now time.Time) ([]assignmentView, error) {
	rows, err := q.Query(ctx, `
		select a.id, a.patient_id, a.form_id, f.name, a.version_id, a.schedule_kind, a.every, a.start_date::text,
		       a.end_count, a.end_date::text, a.open_before_days, a.close_after_days, a.timezone, a.channel,
		       a.assigned_by, u.display_name, a.created_at, a.ended_at
		from form_assignments a join forms f on f.id = a.form_id join users u on u.id = a.assigned_by
		where a.tenant_id = $1 and ($2::uuid is null or a.patient_id = $2) and ($3::uuid is null or a.id = $3)
		order by a.created_at desc`, tenant, patient, assignment)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (assignmentView, error) {
		var a assignmentView
		err := row.Scan(&a.ID, &a.PatientID, &a.FormID, &a.FormName, &a.VersionID, &a.Schedule.Kind, &a.Schedule.Every,
			&a.Schedule.StartDate, &a.Schedule.Count, &a.Schedule.Until, &a.OpenBeforeDays, &a.CloseAfterDays,
			&a.Timezone, &a.Channel, &a.AssignedBy, &a.AssignedByName, &a.CreatedAt, &a.EndedAt)
		a.Occurrences = []occurrenceView{}
		return a, err
	})
	if err != nil || len(list) == 0 {
		return list, err
	}
	byID := map[uuid.UUID]*assignmentView{}
	ids := make([]uuid.UUID, len(list))
	for i := range list {
		byID[list[i].ID] = &list[i]
		ids[i] = list[i].ID
	}
	orows, err := q.Query(ctx, `
		select o.id, o.assignment_id, o.version_id, v.version, o.due_date::text, o.opens_at, o.due_at, o.closes_at, o.status, o.completed_at
		from form_occurrences o join form_versions v on v.id = o.version_id
		where o.assignment_id = any($1) order by o.due_at desc`, ids)
	if err != nil {
		return nil, err
	}
	occs, err := pgx.CollectRows(orows, func(row pgx.CollectableRow) (occurrenceView, error) {
		var o occurrenceView
		err := row.Scan(&o.ID, &o.AssignmentID, &o.VersionID, &o.Version, &o.DueDate, &o.OpensAt, &o.DueAt, &o.ClosesAt, &o.Status, &o.CompletedAt)
		o.State = occurrenceState(o.Status, o.OpensAt, o.ClosesAt, now)
		return o, err
	})
	if err != nil {
		return nil, err
	}
	for _, o := range occs {
		a := byID[o.AssignmentID]
		a.Occurrences = append(a.Occurrences, o)
	}
	return list, nil
}

// --- staff routes -----------------------------------------------------------

type createAssignmentRequest struct {
	PatientID      uuid.UUID    `json:"patient_id"`
	FormID         uuid.UUID    `json:"form_id"`
	VersionID      *uuid.UUID   `json:"version_id"`
	Schedule       formSchedule `json:"schedule"`
	OpenBeforeDays *int         `json:"open_before_days"`
	CloseAfterDays *int         `json:"close_after_days"`
	Channel        string       `json:"channel"`
}

// mayFormPatient reports whether staff member id may assign forms to, or
// read the assignments of, patient: a provider needs an active link; the
// front desk and admins cover the practice. It writes 404 for someone who
// isn't a patient of the practice.
func mayFormPatient(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, id Identity, patient uuid.UUID) bool {
	var ok bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from users where id = $1 and tenant_id = $2 and role = 'patient' and disabled_at is null)`,
		patient, id.TenantID).Scan(&ok); err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return false
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "patient not found")
		return false
	}
	if practiceWide(id.Role) {
		return true
	}
	linked, err := HasActiveLink(ctx, tx, id.TenantID, id.UserID, patient)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "link check failed")
		return false
	}
	if !linked {
		writeErr(w, http.StatusForbidden, "no active link with that patient")
		return false
	}
	return true
}

// CreateAssignment assigns a published form to a patient. Staff, LAN;
// audited as form_assignment.create. Its first occurrences are created at
// once.
// Route: POST /api/v1/assignments
func (s *Server) CreateAssignment(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, false)
	if !ok {
		return
	}
	var req createAssignmentRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if _, _, err := req.Schedule.check(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	openBefore, closeAfter := 0, 3
	if req.OpenBeforeDays != nil {
		openBefore = *req.OpenBeforeDays
	}
	if req.CloseAfterDays != nil {
		closeAfter = *req.CloseAfterDays
	}
	if openBefore < 0 || openBefore > 365 || closeAfter < 0 || closeAfter > 365 || openBefore+closeAfter == 0 {
		writeErr(w, http.StatusBadRequest, "the window must be open for at least a day: open_before_days and close_after_days from 0 to 365, not both 0")
		return
	}
	if req.Channel == "" {
		req.Channel = "app"
	}
	if req.Channel != "app" && req.Channel != "kiosk" {
		writeErr(w, http.StatusBadRequest, "channel must be app or kiosk")
		return
	}
	now := time.Now()
	s.formTx(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !mayFormPatient(ctx, tx, w, id, req.PatientID) {
			return nil, errHandled
		}
		var kind string
		var archived *time.Time
		var published int
		err := tx.QueryRow(ctx, `select f.kind, f.archived_at,
			(select count(*) from form_versions v where v.form_id = f.id and v.status = 'published' and ($3::uuid is null or v.id = $3))
			from forms f where f.id = $1 and f.tenant_id = $2`, req.FormID, id.TenantID, req.VersionID).Scan(&kind, &archived, &published)
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "form not found")
			return nil, errHandled
		}
		if err != nil {
			return nil, err
		}
		if archived != nil {
			writeErr(w, http.StatusConflict, "this form is archived")
			return nil, errHandled
		}
		if kind == "instrument" {
			if ok, err := s.instrumentsAvailable(ctx, id.TenantID); err != nil {
				return nil, err
			} else if !ok {
				writeErr(w, http.StatusConflict, "built-in instruments aren't available on this practice")
				return nil, errHandled
			}
		}
		if published == 0 {
			if req.VersionID != nil {
				writeErr(w, http.StatusBadRequest, "version_id must be a published version of this form")
			} else {
				writeErr(w, http.StatusConflict, "this form has no published version yet")
			}
			return nil, errHandled
		}
		a := assignmentRow{TenantID: id.TenantID, PatientID: req.PatientID, FormID: req.FormID, VersionID: req.VersionID,
			Schedule: req.Schedule, OpenBefore: openBefore, CloseAfter: closeAfter, Timezone: s.location().String(), Channel: req.Channel}
		var until any
		if req.Schedule.Until != nil {
			until = *req.Schedule.Until
		}
		if err := tx.QueryRow(ctx, `insert into form_assignments
			(tenant_id, patient_id, form_id, version_id, schedule_kind, every, start_date, end_count, end_date,
			 open_before_days, close_after_days, timezone, channel, assigned_by)
			values ($1, $2, $3, $4, $5, $6, $7::date, $8, $9::date, $10, $11, $12, $13, $14) returning id, created_at`,
			a.TenantID, a.PatientID, a.FormID, a.VersionID, a.Schedule.Kind, a.Schedule.Every, a.Schedule.StartDate,
			a.Schedule.Count, until, a.OpenBefore, a.CloseAfter, a.Timezone, a.Channel, id.UserID).Scan(&a.ID, &a.CreatedAt); err != nil {
			return nil, err
		}
		if err := formAudit(ctx, tx, id, "form_assignment.create", "form_assignment", a.ID,
			map[string]any{"patient_id": a.PatientID, "form_id": a.FormID, "schedule": a.Schedule.Kind}); err != nil {
			return nil, err
		}
		if _, err := generateOccurrences(ctx, tx, a, now.Add(formsHorizon)); err != nil {
			return nil, err
		}
		list, err := loadAssignments(ctx, tx, id.TenantID, nil, &a.ID, now)
		if err != nil || len(list) != 1 {
			return nil, fmt.Errorf("reload assignment: %v", err)
		}
		return list[0], nil
	})
}

// ListAssignments returns a patient's assignments with their occurrences.
// Staff, LAN; read-audited.
// Route: GET /api/v1/assignments?patient_id=
func (s *Server) ListAssignments(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, false)
	if !ok {
		return
	}
	patient, err := uuid.Parse(r.URL.Query().Get("patient_id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "patient_id is required")
		return
	}
	s.formTx(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !mayFormPatient(ctx, tx, w, id, patient) {
			return nil, errHandled
		}
		list, err := loadAssignments(ctx, tx, id.TenantID, &patient, nil, time.Now())
		if err != nil {
			return nil, err
		}
		return map[string]any{"assignments": list}, nil
	})
}

// EndAssignment stops an assignment: no new occurrences, and those not yet
// open are cancelled. Open ones stay open. Staff, LAN; audited as
// form_assignment.end.
// Route: POST /api/v1/assignments/{assignmentID}/end
func (s *Server) EndAssignment(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, false)
	if !ok {
		return
	}
	aid, ok := uuidParam(w, r, "assignmentID", "assignment")
	if !ok {
		return
	}
	s.formTx(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var patient uuid.UUID
		var ended *time.Time
		err := tx.QueryRow(ctx, `select patient_id, ended_at from form_assignments where id = $1 and tenant_id = $2 for update`,
			aid, id.TenantID).Scan(&patient, &ended)
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "assignment not found")
			return nil, errHandled
		}
		if err != nil {
			return nil, err
		}
		if !mayFormPatient(ctx, tx, w, id, patient) {
			return nil, errHandled
		}
		if ended != nil {
			writeErr(w, http.StatusConflict, "this assignment has already ended")
			return nil, errHandled
		}
		if _, err := tx.Exec(ctx, `update form_assignments set ended_at = now(), ended_by = $2 where id = $1`, aid, id.UserID); err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `update form_occurrences set status = 'cancelled' where assignment_id = $1 and status = 'open' and opens_at > now()`, aid)
		if err != nil {
			return nil, err
		}
		if err := formAudit(ctx, tx, id, "form_assignment.end", "form_assignment", aid,
			map[string]any{"patient_id": patient, "cancelled": tag.RowsAffected()}); err != nil {
			return nil, err
		}
		list, err := loadAssignments(ctx, tx, id.TenantID, nil, &aid, time.Now())
		if err != nil || len(list) != 1 {
			return nil, fmt.Errorf("reload assignment: %v", err)
		}
		return list[0], nil
	})
}

type dueOccurrence struct {
	ID          uuid.UUID     `json:"occurrence_id"`
	PatientID   uuid.UUID     `json:"patient_id"`
	PatientName string        `json:"patient_name"`
	FormName    string        `json:"form_name"`
	DueAt       time.Time     `json:"due_at"`
	ClosesAt    time.Time     `json:"closes_at"`
	Status      string        `json:"status"`
	Providers   []providerRef `json:"providers"`
}

type providerRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// DueForms is the compliance view: due today, overdue (due before today and
// still open) and missed in the last 7 days, each with the patient's linked
// providers. A provider sees their linked patients; the front desk and
// admins the practice. Staff, LAN; read-audited.
// Route: GET /api/v1/form-occurrences/due
func (s *Server) DueForms(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, false)
	if !ok {
		return
	}
	now := time.Now()
	loc := s.location()
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)
	tomorrow := today.AddDate(0, 0, 1)
	var provider *uuid.UUID
	if !practiceWide(id.Role) {
		provider = &id.UserID
	}
	rows, err := s.pool.Query(r.Context(), `
		select o.id, o.patient_id, p.display_name, f.name, o.due_at, o.closes_at, o.status,
		       coalesce((select json_agg(json_build_object('id', pr.id, 'name', pr.display_name) order by pr.display_name)
		                 from provider_patients pp join users pr on pr.id = pp.provider_id
		                 where pp.patient_id = o.patient_id and pp.tenant_id = o.tenant_id and pp.status = 'active'), '[]')
		from form_occurrences o join users p on p.id = o.patient_id join forms f on f.id = o.form_id
		where o.tenant_id = $1
		  and ($2::uuid is null or exists (select 1 from provider_patients pp where pp.provider_id = $2
		       and pp.patient_id = o.patient_id and pp.status = 'active'))
		  and ((o.status = 'open' and o.due_at < $3 and o.closes_at > $4)
		    or (o.status = 'missed' and o.closes_at >= $4 - interval '7 days'))
		order by o.due_at`, id.TenantID, provider, tomorrow, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dueOccurrence, error) {
		var d dueOccurrence
		var provs []byte
		err := row.Scan(&d.ID, &d.PatientID, &d.PatientName, &d.FormName, &d.DueAt, &d.ClosesAt, &d.Status, &provs)
		if err == nil {
			err = json.Unmarshal(provs, &d.Providers)
		}
		return d, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	out := map[string][]dueOccurrence{"due_today": {}, "overdue": {}, "missed_this_week": {}}
	for _, d := range list {
		switch {
		case d.Status == "missed":
			out["missed_this_week"] = append(out["missed_this_week"], d)
		case d.DueAt.Before(today):
			out["overdue"] = append(out["overdue"], d)
		default:
			out["due_today"] = append(out["due_today"], d)
		}
	}
	// Responses with a staff flag in the last 7 days (#199), newest first.
	frows, err := s.pool.Query(r.Context(), `
		select r.id, r.patient_id, p.display_name, f.name, r.submitted_at, r.flags,
		       coalesce((select json_agg(json_build_object('id', pr.id, 'name', pr.display_name) order by pr.display_name)
		                 from provider_patients pp join users pr on pr.id = pp.provider_id
		                 where pp.patient_id = r.patient_id and pp.tenant_id = r.tenant_id and pp.status = 'active'), '[]')
		from form_responses r join users p on p.id = r.patient_id
		join form_versions v on v.id = r.version_id join forms f on f.id = v.form_id
		where r.tenant_id = $1 and r.status = 'submitted' and jsonb_array_length(r.flags) > 0
		  and r.submitted_at >= $3::timestamptz - interval '7 days'
		  and ($2::uuid is null or exists (select 1 from provider_patients pp where pp.provider_id = $2
		       and pp.patient_id = r.patient_id and pp.status = 'active'))
		order by r.submitted_at desc`, id.TenantID, provider, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	flagged, err := pgx.CollectRows(frows, func(row pgx.CollectableRow) (flaggedResponse, error) {
		var x flaggedResponse
		var provs []byte
		err := row.Scan(&x.ResponseID, &x.PatientID, &x.PatientName, &x.FormName, &x.SubmittedAt, &x.Flags, &provs)
		if err == nil {
			err = json.Unmarshal(provs, &x.Providers)
		}
		return x, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if flagged == nil {
		flagged = []flaggedResponse{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"due_today": out["due_today"], "overdue": out["overdue"],
		"missed_this_week": out["missed_this_week"], "flagged_this_week": flagged})
}

// flaggedResponse is a submitted response that raised a staff flag (#199).
type flaggedResponse struct {
	ResponseID  uuid.UUID       `json:"response_id"`
	PatientID   uuid.UUID       `json:"patient_id"`
	PatientName string          `json:"patient_name"`
	FormName    string          `json:"form_name"`
	SubmittedAt time.Time       `json:"submitted_at"`
	Flags       json.RawMessage `json:"flags"`
	Providers   []providerRef   `json:"providers"`
}

// --- the patient ------------------------------------------------------------

type myForm struct {
	OccurrenceID uuid.UUID       `json:"occurrence_id"`
	AssignmentID uuid.UUID       `json:"assignment_id"`
	FormID       uuid.UUID       `json:"form_id"`
	VersionID    uuid.UUID       `json:"version_id"`
	Title        string          `json:"title"`
	State        string          `json:"state"` // open or upcoming
	OpensAt      time.Time       `json:"opens_at"`
	DueAt        time.Time       `json:"due_at"`
	ClosesAt     time.Time       `json:"closes_at"`
	Response     *myFormResponse `json:"response"`
}

// myFormResponse points at an in-progress response to resume (#197).
type myFormResponse struct {
	ID       uuid.UUID `json:"id"`
	Status   string    `json:"status"`
	Revision int       `json:"revision"`
}

// patientFormsOn checks the caller is a patient and the forms switch is on.
func (s *Server) patientFormsOn(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return id, false
	}
	if id.Role != "patient" {
		writeErr(w, http.StatusForbidden, "patients only")
		return id, false
	}
	return id, s.formsSwitchOn(w, r.Context(), id.TenantID)
}

// formsSwitchOn writes 403 feature_off when the practice has forms off.
func (s *Server) formsSwitchOn(w http.ResponseWriter, ctx context.Context, tenant uuid.UUID) bool {
	_, on, err := practiceConfig(ctx, s.pool, tenant)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "settings lookup failed")
		return false
	}
	if !on["forms"] {
		writeErrCode(w, http.StatusForbidden, "feature_off", "Forms are turned off for this practice")
		return false
	}
	return true
}

// MyForms lists the patient's open and upcoming form occurrences, soonest
// due first. Patient, any channel.
// Route: GET /api/v1/me/forms
func (s *Server) MyForms(w http.ResponseWriter, r *http.Request) {
	id, ok := s.patientFormsOn(w, r)
	if !ok {
		return
	}
	now := time.Now()
	rows, err := s.pool.Query(r.Context(), `
		select o.id, o.assignment_id, o.form_id, o.version_id, coalesce(v.definition->>'title', f.name),
		       o.opens_at, o.due_at, o.closes_at
		from form_occurrences o join form_versions v on v.id = o.version_id join forms f on f.id = o.form_id
		join form_assignments a on a.id = o.assignment_id
		where o.tenant_id = $1 and o.patient_id = $2 and o.status = 'open' and a.channel = 'app'
		  and o.closes_at > $3 and o.opens_at <= $4
		order by o.due_at, o.id`, id.TenantID, id.UserID, now, now.Add(formsHorizon))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (myForm, error) {
		var f myForm
		err := row.Scan(&f.OccurrenceID, &f.AssignmentID, &f.FormID, &f.VersionID, &f.Title, &f.OpensAt, &f.DueAt, &f.ClosesAt)
		f.State = occurrenceState("open", f.OpensAt, f.ClosesAt, now)
		return f, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if list == nil {
		list = []myForm{}
	}
	if err := s.attachResponses(r.Context(), list); err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"forms": list})
}

// patientMayReadVersion reports whether the patient has an open occurrence
// on this version.
func patientMayReadVersion(ctx context.Context, q rowQuerier, id Identity, vid uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `select exists(select 1 from form_occurrences where version_id = $1 and patient_id = $2
		and tenant_id = $3 and status = 'open' and opens_at <= now() and closes_at > now())`, vid, id.UserID, id.TenantID).Scan(&ok)
	return ok, err
}

// --- the runner -------------------------------------------------------------

// RunFormOccurrences runs the forms runner every interval until ctx ends.
func (s *Server) RunFormOccurrences(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		slog.Info("form occurrences off")
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if st, err := s.runForms(ctx, time.Now()); err != nil && ctx.Err() == nil {
			slog.Warn("forms: run failed", "err", err)
		} else if st.Created+st.Missed+st.Reminded > 0 {
			slog.Info("forms: run", "created", st.Created, "missed", st.Missed, "reminded", st.Reminded)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type formsRunStats struct{ Created, Missed, Reminded int }

// runForms is one idempotent run at now: create occurrences up to the
// horizon, mark closed unfinished ones missed, and send form_due (at most
// once per occurrence for opening and once for closing).
func (s *Server) runForms(ctx context.Context, now time.Time) (formsRunStats, error) {
	var st formsRunStats
	if _, err := s.syncAllInstruments(ctx); err != nil { // #199
		slog.Warn("instruments: sync failed", "err", err)
	}
	rows, err := s.pool.Query(ctx, `select `+assignmentColumns+` from form_assignments a where a.ended_at is null`)
	if err != nil {
		return st, err
	}
	assignments, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (assignmentRow, error) { return scanAssignment(row) })
	if err != nil {
		return st, err
	}
	for _, a := range assignments {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return st, err
		}
		n, err := generateOccurrences(ctx, tx, a, now.Add(formsHorizon))
		if err == nil {
			err = tx.Commit(ctx)
		}
		tx.Rollback(ctx)
		if err != nil {
			slog.Warn("forms: generating occurrences failed", "assignment_id", a.ID, "err", err)
			continue
		}
		st.Created += n
	}
	missed, err := s.markMissed(ctx, now)
	st.Missed = missed
	if err != nil {
		return st, err
	}
	if _, err := s.expireResponses(ctx, now); err != nil { // #197
		return st, err
	}
	sent, err := s.sendFormDue(ctx, now)
	st.Reminded = sent
	return st, err
}

// markMissed marks open occurrences whose window has closed; runForms then
// expires their in-progress responses (#197).
func (s *Server) markMissed(ctx context.Context, now time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `update form_occurrences set status = 'missed' where status = 'open' and closes_at <= $1`, now)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// sendFormDue claims and sends the reminders that are due. Claiming sets
// reminded_*_at first, so a reminder is never sent twice even across
// restarts. When the opening reminder goes out after the closing one is
// already due (a short window, or the Back Office was down), it counts as
// both, so the patient gets one push, not two.
func (s *Server) sendFormDue(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.pool.Query(ctx, `
		update form_occurrences o set reminded_open_at = $1,
		       reminded_close_at = case when o.closes_at - $2::interval <= $1 then $1 else o.reminded_close_at end
		from form_assignments a
		where a.id = o.assignment_id and o.status = 'open' and o.reminded_open_at is null
		  and o.opens_at <= $1 and o.closes_at > $1
		returning o.tenant_id, o.patient_id, a.channel`, now, formsCloseReminder.String())
	if err != nil {
		return 0, err
	}
	type claim struct {
		Tenant, Patient uuid.UUID
		Channel         string
	}
	collect := func(rows pgx.Rows) ([]claim, error) {
		return pgx.CollectRows(rows, func(row pgx.CollectableRow) (claim, error) {
			var c claim
			err := row.Scan(&c.Tenant, &c.Patient, &c.Channel)
			return c, err
		})
	}
	claims, err := collect(rows)
	if err != nil {
		return 0, err
	}
	rows, err = s.pool.Query(ctx, `
		update form_occurrences o set reminded_close_at = $1
		from form_assignments a
		where a.id = o.assignment_id and o.status = 'open' and o.reminded_open_at is not null and o.reminded_close_at is null
		  and o.closes_at - $2::interval <= $1 and o.closes_at - $2::interval > o.opens_at and o.closes_at > $1
		returning o.tenant_id, o.patient_id, a.channel`, now, formsCloseReminder.String())
	if err != nil {
		return 0, err
	}
	more, err := collect(rows)
	if err != nil {
		return 0, err
	}
	sent := 0
	switches := map[uuid.UUID]bool{}
	for _, c := range append(claims, more...) {
		on, cached := switches[c.Tenant]
		if !cached {
			_, all, err := practiceConfig(ctx, s.pool, c.Tenant)
			if err != nil {
				return sent, err
			}
			on = all["forms"]
			switches[c.Tenant] = on
		}
		if !on || c.Channel != "app" {
			continue // claimed, not sent: no burst when forms are turned on later
		}
		s.notify(c.Patient, "form_due")
		sent++
	}
	return sent, nil
}

// formVersionReader admits staff on the office LAN, and a patient for a
// version on one of their open occurrences (404 for any other version).
func (s *Server) formVersionReader(w http.ResponseWriter, r *http.Request, vid uuid.UUID) (Identity, bool) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return id, false
	}
	if id.Role != "patient" {
		return s.formsOn(w, r, false)
	}
	if !s.formsSwitchOn(w, r.Context(), id.TenantID) {
		return id, false
	}
	may, err := patientMayReadVersion(r.Context(), s.pool, id, vid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return id, false
	}
	if !may {
		writeErr(w, http.StatusNotFound, "form version not found")
		return id, false
	}
	return id, true
}

// attachResponses adds each occurrence's in-progress response, for resume (#197).
func (s *Server) attachResponses(ctx context.Context, list []myForm) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(list))
	for i, f := range list {
		ids[i] = f.OccurrenceID
	}
	rows, err := s.pool.Query(ctx, `select occurrence_id, id, revision from form_responses
		where occurrence_id = any($1) and status = 'in_progress'`, ids)
	if err != nil {
		return err
	}
	type found struct {
		Occurrence uuid.UUID
		Response   myFormResponse
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (found, error) {
		var f found
		f.Response.Status = "in_progress"
		err := row.Scan(&f.Occurrence, &f.Response.ID, &f.Response.Revision)
		return f, err
	})
	if err != nil {
		return err
	}
	by := map[uuid.UUID]*myFormResponse{}
	for i := range got {
		by[got[i].Occurrence] = &got[i].Response
	}
	for i := range list {
		list[i].Response = by[list[i].OccurrenceID]
	}
	return nil
}
