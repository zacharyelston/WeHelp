package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

// Practice inbox (#110). A patient messages "the practice": bookings,
// orders, directions, anything not clinical, or a provider whose messages
// are off. The front desk (office) and admins answer, on the office LAN.
// Providers don't read it, and it is separate from direct provider
// threads. Off when the practice turns messages off (#109).

type practiceMessage struct {
	ID          uuid.UUID  `json:"id"`
	PatientID   uuid.UUID  `json:"patient_id"`
	AuthorID    uuid.UUID  `json:"author_id"`
	AuthorName  string     `json:"author_name"`
	FromPatient bool       `json:"from_patient"`
	Body        string     `json:"body"`
	ReadAt      *time.Time `json:"read_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

type inboxConversation struct {
	PatientID       uuid.UUID `json:"patient_id"`
	PatientName     string    `json:"patient_name"`
	LastBody        string    `json:"last_body"`
	LastAt          time.Time `json:"last_at"`
	LastFromPatient bool      `json:"last_from_patient"`
	Unread          int       `json:"unread"` // from the patient, not yet read by the practice
}

// inboxSide resolves who is using the inbox: a patient (their own thread,
// any channel) or the front desk or an admin (any thread, LAN only).
// Providers get 403. ok is false after an error has been written.
func inboxSide(w http.ResponseWriter, r *http.Request) (id Identity, staff, ok bool) {
	id, ok = identityFrom(r.Context())
	switch {
	case !ok:
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
	case id.Role == "patient":
		return id, false, true
	case !practiceWide(id.Role):
		writeErr(w, http.StatusForbidden, "the practice inbox is answered by the front desk")
	case tunnel.ViaRelay(r.Context()):
		writeErr(w, http.StatusNotFound, "not found")
	default:
		return id, true, true
	}
	return id, false, false
}

func loadThread(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, tenant, patient uuid.UUID) ([]practiceMessage, error) {
	rows, err := q.Query(ctx, `
		select m.id, m.patient_id, m.author_id, u.display_name, m.from_patient, m.body, m.read_at, m.created_at
		from practice_messages m join users u on u.id = m.author_id
		where m.tenant_id = $1 and m.patient_id = $2 order by m.created_at, m.id`, tenant, patient)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (practiceMessage, error) {
		var m practiceMessage
		err := row.Scan(&m.ID, &m.PatientID, &m.AuthorID, &m.AuthorName, &m.FromPatient, &m.Body, &m.ReadAt, &m.CreatedAt)
		return m, err
	})
	if list == nil {
		list = []practiceMessage{}
	}
	return list, err
}

// GetPracticeInbox returns a patient's own thread, or for the front desk
// the list of conversations, newest first.
// Route: GET /api/v1/practice-inbox
func (s *Server) GetPracticeInbox(w http.ResponseWriter, r *http.Request) {
	id, staff, ok := inboxSide(w, r)
	if !ok || featureOff(w, r.Context(), s.pool, id.TenantID, "messages") {
		return
	}
	ctx := r.Context()
	if !staff {
		list, err := loadThread(ctx, s.pool, id.TenantID, id.UserID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "load failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"messages": list})
		return
	}
	rows, err := s.pool.Query(ctx, `
		select distinct on (m.patient_id) m.patient_id, u.display_name, m.body, m.created_at, m.from_patient,
		       (select count(*) from practice_messages x where x.tenant_id = $1 and x.patient_id = m.patient_id
		          and x.from_patient and x.read_at is null)
		from practice_messages m join users u on u.id = m.patient_id
		where m.tenant_id = $1
		order by m.patient_id, m.created_at desc`, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (inboxConversation, error) {
		var c inboxConversation
		err := row.Scan(&c.PatientID, &c.PatientName, &c.LastBody, &c.LastAt, &c.LastFromPatient, &c.Unread)
		return c, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastAt.After(list[j].LastAt) }) // newest first
	if list == nil {
		list = []inboxConversation{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": list})
}

// GetPracticeInboxThread returns one patient's thread. Front desk, LAN.
// Route: GET /api/v1/practice-inbox/{patientID}
func (s *Server) GetPracticeInboxThread(w http.ResponseWriter, r *http.Request) {
	id, staff, ok := inboxSide(w, r)
	if !ok || featureOff(w, r.Context(), s.pool, id.TenantID, "messages") {
		return
	}
	patient, err := uuid.Parse(chi.URLParam(r, "patientID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid patient id")
		return
	}
	if !staff && patient != id.UserID {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	list, err := loadThread(r.Context(), s.pool, id.TenantID, patient)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": list})
}

type inboxPost struct {
	PatientID *uuid.UUID `json:"patient_id"` // the front desk says whose thread
	Body      string     `json:"body"`
}

// PostPracticeInbox adds a message: a patient writes to the practice, or
// the front desk replies in a patient's thread. Audited.
// Route: POST /api/v1/practice-inbox
func (s *Server) PostPracticeInbox(w http.ResponseWriter, r *http.Request) {
	id, staff, ok := inboxSide(w, r)
	if !ok || featureOff(w, r.Context(), s.pool, id.TenantID, "messages") {
		return
	}
	var req inboxPost
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Body == "" || utf8.RuneCountInString(req.Body) > 4000 {
		writeErr(w, http.StatusBadRequest, "body required, at most 4000 characters")
		return
	}
	patient := id.UserID
	if staff {
		if req.PatientID == nil {
			writeErr(w, http.StatusBadRequest, "patient_id is required")
			return
		}
		patient = *req.PatientID
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	var isPatient bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from users where id = $1 and tenant_id = $2 and role = 'patient')`,
		patient, id.TenantID).Scan(&isPatient); err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if !isPatient {
		writeErr(w, http.StatusNotFound, "no such patient in this practice")
		return
	}
	var m practiceMessage
	if err := tx.QueryRow(ctx, `
		insert into practice_messages (tenant_id, patient_id, author_id, from_patient, body)
		values ($1, $2, $3, $4, $5) returning id, created_at`, id.TenantID, patient, id.UserID, !staff, req.Body).
		Scan(&m.ID, &m.CreatedAt); err != nil {
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: "practice_message.create",
		ResourceType: "practice_message", ResourceID: m.ID.String(),
		Detail: mustJSON(map[string]any{"patient": patient.String(), "from_patient": !staff})}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	var notifyIDs []uuid.UUID
	if staff {
		notifyIDs = []uuid.UUID{patient}
	} else {
		rows, err := tx.Query(ctx, `select id from users where tenant_id = $1 and role in ('office', 'admin') and disabled_at is null`, id.TenantID)
		if err == nil {
			notifyIDs, _ = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	for _, u := range notifyIDs {
		s.notify(u, "new_message") // generic, no content (#7)
	}
	m.PatientID, m.AuthorID, m.FromPatient, m.Body = patient, id.UserID, !staff, req.Body
	writeJSON(w, http.StatusCreated, m)
}

type inboxRead struct {
	PatientID *uuid.UUID `json:"patient_id"`
}

// ReadPracticeInbox marks the other side's messages read: a patient reads
// the practice's replies; the front desk reads a patient's messages.
// Route: POST /api/v1/practice-inbox/read
func (s *Server) ReadPracticeInbox(w http.ResponseWriter, r *http.Request) {
	id, staff, ok := inboxSide(w, r)
	if !ok {
		return
	}
	var req inboxRead
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	patient := id.UserID
	if staff {
		if req.PatientID == nil {
			writeErr(w, http.StatusBadRequest, "patient_id is required")
			return
		}
		patient = *req.PatientID
	}
	tag, err := s.pool.Exec(r.Context(), `
		update practice_messages set read_at = now()
		where tenant_id = $1 and patient_id = $2 and from_patient = $3 and read_at is null`, id.TenantID, patient, staff)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"marked": tag.RowsAffected()})
}
