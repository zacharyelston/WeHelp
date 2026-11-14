package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// Care teams (#111). A patient chooses a group of providers who share one
// thread with them, for continuity of care. A provider already caring for
// the patient (an active link, or a team member) proposes a colleague, who
// joins only when the patient approves; a patient can add a provider
// directly. Members are providers, never the front desk or admins. A member
// sees messages from before they joined only if the patient allowed it; a
// removed member sees nothing. Every member's read of the thread is audited
// and the patient can see that log. Separate from direct threads and the
// practice inbox. See docs/PRACTICE_TYPES.md → Care team.

type teamMember struct {
	MemberID       uuid.UUID  `json:"member_id"`
	Name           string     `json:"name"`
	Status         string     `json:"status"` // pending, active, removed
	IncludeHistory bool       `json:"include_history"`
	InvitedBy      string     `json:"invited_by"`
	CreatedAt      time.Time  `json:"created_at"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`
	RemovedAt      *time.Time `json:"removed_at,omitempty"`
}

type teamCandidate struct {
	MemberID uuid.UUID `json:"member_id"`
	Name     string    `json:"name"`
}

type teamRead struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

type careTeamView struct {
	PatientID   uuid.UUID    `json:"patient_id"`
	PatientName string       `json:"patient_name"`
	Members     []teamMember `json:"members"`
	// Reads is who opened the thread and when, newest first: the
	// patient's view only.
	Reads []teamRead `json:"reads,omitempty"`
	// Eligible are the practice's providers not on the team, who can be
	// proposed or added.
	Eligible []teamCandidate `json:"eligible,omitempty"`
}

type teamMessage struct {
	ID          uuid.UUID `json:"id"`
	AuthorID    uuid.UUID `json:"author_id"`
	AuthorName  string    `json:"author_name"`
	FromPatient bool      `json:"from_patient"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
}

// teamAccess is what the caller may do with a patient's care team:
// "patient" (their own), "member" (active), "pending" (invited, not yet
// approved), "carer" (a provider with an active link), or "" (none).
func teamAccess(ctx context.Context, q rowQuerier, id Identity, patient uuid.UUID) (string, error) {
	if id.Role == "patient" {
		if id.UserID == patient {
			return "patient", nil
		}
		return "", nil
	}
	if id.Role != "provider" {
		return "", nil
	}
	var status string
	err := q.QueryRow(ctx, `select status from care_team_members where tenant_id = $1 and patient_id = $2 and member_id = $3
		and status <> 'removed'`, id.TenantID, patient, id.UserID).Scan(&status)
	switch {
	case err == nil && status == "active":
		return "member", nil
	case err == nil:
		return "pending", nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	var linked bool
	if err := q.QueryRow(ctx, `select exists (select 1 from provider_patients where tenant_id = $1 and provider_id = $2
		and patient_id = $3 and status = 'active')`, id.TenantID, id.UserID, patient).Scan(&linked); err != nil {
		return "", err
	}
	if linked {
		return "carer", nil
	}
	return "", nil
}

// teamGate resolves the patient in the URL and the caller's access, after
// the practice switches. It writes the error and returns ok=false when the
// caller has no access in allowed.
func (s *Server) teamGate(w http.ResponseWriter, r *http.Request, allowed ...string) (Identity, uuid.UUID, string, bool) {
	id, _ := identityFrom(r.Context())
	ctx := r.Context()
	if featureOff(w, ctx, s.pool, id.TenantID, "messages") || featureOff(w, ctx, s.pool, id.TenantID, "careteam") {
		return id, uuid.Nil, "", false
	}
	patient, err := uuid.Parse(chi.URLParam(r, "patientID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid patient id")
		return id, patient, "", false
	}
	access, err := teamAccess(ctx, s.pool, id, patient)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "access check failed")
		return id, patient, "", false
	}
	for _, a := range allowed {
		if a == access {
			return id, patient, access, true
		}
	}
	writeErr(w, http.StatusNotFound, "care team not found")
	return id, patient, access, false
}

func loadTeam(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenant, patient uuid.UUID, withReads bool) (careTeamView, error) {
	v := careTeamView{PatientID: patient, Members: []teamMember{}}
	if err := q.QueryRow(ctx, `select display_name from users where id = $1 and tenant_id = $2 and role = 'patient'`, patient, tenant).
		Scan(&v.PatientName); err != nil {
		return v, err
	}
	rows, err := q.Query(ctx, `
		select m.member_id, u.display_name, m.status, m.include_history, i.display_name, m.created_at, m.approved_at, m.removed_at
		from care_team_members m join users u on u.id = m.member_id join users i on i.id = m.invited_by
		where m.tenant_id = $1 and m.patient_id = $2
		order by m.status = 'removed', m.created_at`, tenant, patient)
	if err != nil {
		return v, err
	}
	if v.Members, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (teamMember, error) {
		var m teamMember
		err := row.Scan(&m.MemberID, &m.Name, &m.Status, &m.IncludeHistory, &m.InvitedBy, &m.CreatedAt, &m.ApprovedAt, &m.RemovedAt)
		return m, err
	}); err != nil {
		return v, err
	}
	if v.Members == nil {
		v.Members = []teamMember{}
	}
	if withReads {
		rows, err := q.Query(ctx, `
			select u.display_name, a.created_at from audit_events a join users u on u.id = a.actor_id
			where a.tenant_id = $1 and a.action = 'care_team.read' and a.resource_id = $2
			order by a.created_at desc limit 100`, tenant, patient.String())
		if err != nil {
			return v, err
		}
		if v.Reads, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (teamRead, error) {
			var t teamRead
			err := row.Scan(&t.Name, &t.At)
			return t, err
		}); err != nil {
			return v, err
		}
	}
	return v, nil
}

type teamSummary struct {
	PatientID     uuid.UUID  `json:"patient_id"`
	PatientName   string     `json:"patient_name"`
	MyStatus      string     `json:"my_status"` // active, pending, or none (a linked patient without me on the team)
	ActiveMembers int        `json:"active_members"`
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
}

// ListCareTeams lists, for a provider, the care teams they are on or
// invited to, and their linked patients they could start one for. A
// patient gets their own team.
// Route: GET /api/v1/care-teams
func (s *Server) ListCareTeams(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	ctx := r.Context()
	if featureOff(w, ctx, s.pool, id.TenantID, "messages") || featureOff(w, ctx, s.pool, id.TenantID, "careteam") {
		return
	}
	if id.Role == "patient" {
		v, err := loadTeam(ctx, s.pool, id.TenantID, id.UserID, true)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "load failed")
			return
		}
		writeJSON(w, http.StatusOK, v)
		return
	}
	if id.Role != "provider" {
		writeErr(w, http.StatusForbidden, "care teams are for providers and their patients")
		return
	}
	rows, err := s.pool.Query(ctx, `
		with mine as (
			select patient_id, status from care_team_members
			where tenant_id = $1 and member_id = $2 and status <> 'removed'
			union
			select patient_id, 'none' from provider_patients
			where tenant_id = $1 and provider_id = $2 and status = 'active'
			  and patient_id not in (select patient_id from care_team_members where member_id = $2 and status <> 'removed'))
		select m.patient_id, u.display_name, m.status,
		       (select count(*) from care_team_members x where x.patient_id = m.patient_id and x.status = 'active'),
		       (select max(created_at) from care_team_messages c where c.patient_id = m.patient_id)
		from mine m join users u on u.id = m.patient_id
		order by u.display_name`, id.TenantID, id.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (teamSummary, error) {
		var t teamSummary
		err := row.Scan(&t.PatientID, &t.PatientName, &t.MyStatus, &t.ActiveMembers, &t.LastMessageAt)
		return t, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if list == nil {
		list = []teamSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": list})
}

// GetCareTeam returns a patient's care team. The patient also sees who has
// read the thread.
// Route: GET /api/v1/care-teams/{patientID}
func (s *Server) GetCareTeam(w http.ResponseWriter, r *http.Request) {
	id, patient, access, ok := s.teamGate(w, r, "patient", "member", "pending", "carer")
	if !ok {
		return
	}
	v, err := loadTeam(r.Context(), s.pool, id.TenantID, patient, access == "patient")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		select id, display_name from users
		where tenant_id = $1 and role = 'provider' and disabled_at is null
		  and id not in (select member_id from care_team_members where patient_id = $2 and status <> 'removed')
		order by display_name`, id.TenantID, patient)
	if err == nil {
		v.Eligible, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (teamCandidate, error) {
			var c teamCandidate
			err := row.Scan(&c.MemberID, &c.Name)
			return c, err
		})
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type addMemberRequest struct {
	MemberID       uuid.UUID `json:"member_id"`
	IncludeHistory bool      `json:"include_history"`
}

// AddCareTeamMember adds a provider: at once when the patient adds them,
// pending the patient's approval when a provider proposes them.
// Route: POST /api/v1/care-teams/{patientID}/members
func (s *Server) AddCareTeamMember(w http.ResponseWriter, r *http.Request) {
	id, patient, access, ok := s.teamGate(w, r, "patient", "member", "carer")
	if !ok {
		return
	}
	var req addMemberRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	var provider bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from users where id = $1 and tenant_id = $2 and role = 'provider'
		and disabled_at is null)`, req.MemberID, id.TenantID).Scan(&provider); err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if !provider {
		writeErr(w, http.StatusBadRequest, "care team members are providers of this practice")
		return
	}
	status, action := "pending", "care_team.propose"
	var approved *time.Time
	if access == "patient" {
		now := time.Now()
		status, action, approved = "active", "care_team.add", &now
	}
	_, err = tx.Exec(ctx, `
		insert into care_team_members (tenant_id, patient_id, member_id, status, include_history, invited_by, approved_at)
		values ($1, $2, $3, $4, $5, $6, $7)`, id.TenantID, patient, req.MemberID, status, req.IncludeHistory, id.UserID, approved)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeErr(w, http.StatusConflict, "already on this care team or waiting for approval")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "add failed")
		return
	}
	if err := s.auditTeam(ctx, tx, id, patient, action, req.MemberID); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	v, err := loadTeam(ctx, tx, id.TenantID, patient, access == "patient")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	if status == "pending" {
		s.notify(patient, "new_message") // the app asks the patient to approve (generic push)
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) auditTeam(ctx context.Context, tx pgx.Tx, id Identity, patient uuid.UUID, action string, member uuid.UUID) error {
	return audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: action,
		ResourceType: "care_team", ResourceID: patient.String(), Detail: mustJSON(map[string]string{"member": member.String()})})
}

// changeMember runs one membership change in a transaction with its audit
// event and answers with the team.
func (s *Server) changeMember(w http.ResponseWriter, r *http.Request, id Identity, patient uuid.UUID, access, action, query string) {
	member, err := uuid.Parse(chi.URLParam(r, "memberID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid member id")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, query, id.TenantID, patient, member)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "no such member in that state")
		return
	}
	if err := s.auditTeam(ctx, tx, id, patient, action, member); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	v, err := loadTeam(ctx, tx, id.TenantID, patient, access == "patient")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ApproveCareTeamMember: the patient lets a proposed provider join.
// Route: POST /api/v1/care-teams/{patientID}/members/{memberID}/approve
func (s *Server) ApproveCareTeamMember(w http.ResponseWriter, r *http.Request) {
	id, patient, access, ok := s.teamGate(w, r, "patient")
	if !ok {
		return
	}
	s.changeMember(w, r, id, patient, access, "care_team.approve", `
		update care_team_members set status = 'active', approved_at = now()
		where tenant_id = $1 and patient_id = $2 and member_id = $3 and status = 'pending'`)
}

// DeclineCareTeamMember: the patient turns a proposed provider down.
// Route: POST /api/v1/care-teams/{patientID}/members/{memberID}/decline
func (s *Server) DeclineCareTeamMember(w http.ResponseWriter, r *http.Request) {
	id, patient, access, ok := s.teamGate(w, r, "patient")
	if !ok {
		return
	}
	s.changeMember(w, r, id, patient, access, "care_team.decline", `
		update care_team_members set status = 'removed', removed_at = now()
		where tenant_id = $1 and patient_id = $2 and member_id = $3 and status = 'pending'`)
}

// RemoveCareTeamMember: the patient removes a member, or a member leaves.
// A removed member sees nothing of the thread, past or future.
// Route: DELETE /api/v1/care-teams/{patientID}/members/{memberID}
func (s *Server) RemoveCareTeamMember(w http.ResponseWriter, r *http.Request) {
	id, patient, access, ok := s.teamGate(w, r, "patient", "member", "pending")
	if !ok {
		return
	}
	if access != "patient" && chi.URLParam(r, "memberID") != id.UserID.String() {
		writeErr(w, http.StatusForbidden, "only the patient removes others; you can leave")
		return
	}
	s.changeMember(w, r, id, patient, access, "care_team.remove", `
		update care_team_members set status = 'removed', removed_at = now()
		where tenant_id = $1 and patient_id = $2 and member_id = $3 and status <> 'removed'`)
}

// GetCareTeamMessages returns the shared thread, oldest first. A member's
// read is audited; a member sees messages from before they joined only if
// the patient allowed it.
// Route: GET /api/v1/care-teams/{patientID}/messages
func (s *Server) GetCareTeamMessages(w http.ResponseWriter, r *http.Request) {
	id, patient, access, ok := s.teamGate(w, r, "patient", "member")
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
	since := time.Time{}
	if access == "member" {
		var history bool
		var approved time.Time
		if err := tx.QueryRow(ctx, `select include_history, approved_at from care_team_members
			where patient_id = $1 and member_id = $2 and status = 'active'`, patient, id.UserID).Scan(&history, &approved); err != nil {
			writeErr(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		if !history {
			since = approved
		}
		if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: "care_team.read",
			ResourceType: "care_team", ResourceID: patient.String()}); err != nil {
			writeErr(w, http.StatusInternalServerError, "audit failed")
			return
		}
	}
	rows, err := tx.Query(ctx, `
		select m.id, m.author_id, u.display_name, m.author_id = m.patient_id, m.body, m.created_at
		from care_team_messages m join users u on u.id = m.author_id
		where m.tenant_id = $1 and m.patient_id = $2 and m.created_at >= $3
		order by m.created_at, m.id`, id.TenantID, patient, since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (teamMessage, error) {
		var m teamMessage
		err := row.Scan(&m.ID, &m.AuthorID, &m.AuthorName, &m.FromPatient, &m.Body, &m.CreatedAt)
		return m, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if list == nil {
		list = []teamMessage{}
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": list})
}

type teamPost struct {
	Body string `json:"body"`
}

// PostCareTeamMessage adds to the shared thread: the patient or an active
// member. Audited; everyone else on the team gets a generic push.
// Route: POST /api/v1/care-teams/{patientID}/messages
func (s *Server) PostCareTeamMessage(w http.ResponseWriter, r *http.Request) {
	id, patient, _, ok := s.teamGate(w, r, "patient", "member")
	if !ok {
		return
	}
	var req teamPost
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Body == "" || utf8.RuneCountInString(req.Body) > 4000 {
		writeErr(w, http.StatusBadRequest, "body required, at most 4000 characters")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	var m teamMessage
	if err := tx.QueryRow(ctx, `insert into care_team_messages (tenant_id, patient_id, author_id, body) values ($1, $2, $3, $4)
		returning id, created_at`, id.TenantID, patient, id.UserID, req.Body).Scan(&m.ID, &m.CreatedAt); err != nil {
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: "care_team_message.create",
		ResourceType: "care_team_message", ResourceID: m.ID.String(),
		Detail: mustJSON(map[string]string{"patient": patient.String()})}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	rows, err := tx.Query(ctx, `select member_id from care_team_members where patient_id = $1 and status = 'active'
		union select $1::uuid`, patient)
	var others []uuid.UUID
	if err == nil {
		all, _ := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		for _, u := range all {
			if u != id.UserID {
				others = append(others, u)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	for _, u := range others {
		s.notify(u, "new_message")
	}
	m.AuthorID, m.FromPatient, m.Body = id.UserID, id.UserID == patient, req.Body
	writeJSON(w, http.StatusCreated, m)
}
