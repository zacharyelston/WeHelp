package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/forms"
)

// Form responses (#197). A patient fills an occurrence page by page and
// submits once; the Back Office scores it. Answers are PHI: they stay here,
// are append-only (form_answer_events, and a response frozen once
// submitted), every write is audited with ids only, and staff reads are
// read-audited. Logs carry ids only.
//
// Page saves follow docs/V2_CONTRACTS.md ("Forms"): the client sends the
// response revision it last saw (If-Match) and an idempotency key. A stale
// revision is 409 stale_revision; the same key with the same body returns
// the first result; the same key with another body is 409
// idempotency_mismatch. Submit is one idempotent terminal command: saved
// pages, visibility, required answers and the window are checked together.
// docs/MOBILE_CLIENT.md §16 has the app's side.

const (
	maxPageBody = 64 << 10 // one page save
)

type responseRow struct {
	ID, TenantID, PatientID, VersionID uuid.UUID
	OccurrenceID                       *uuid.UUID
	Channel, Status                    string
	Revision                           int
	StartedAt                          time.Time
	SubmittedAt                        *time.Time
	Answers, Scores, Flags             []byte
	OpensAt, ClosesAt                  *time.Time
	OccStatus                          *string
}

func loadResponse(ctx context.Context, q rowQuerier, tenant, rid uuid.UUID, lock bool) (responseRow, error) {
	sql := `select r.id, r.tenant_id, r.patient_id, r.version_id, r.occurrence_id, r.channel, r.status, r.revision,
		       r.started_at, r.submitted_at, r.answers, r.scores, r.flags, o.opens_at, o.closes_at, o.status
		from form_responses r left join form_occurrences o on o.id = r.occurrence_id
		where r.id = $1 and r.tenant_id = $2`
	if lock {
		sql += ` for update of r`
	}
	var x responseRow
	err := q.QueryRow(ctx, sql, rid, tenant).Scan(&x.ID, &x.TenantID, &x.PatientID, &x.VersionID, &x.OccurrenceID, &x.Channel,
		&x.Status, &x.Revision, &x.StartedAt, &x.SubmittedAt, &x.Answers, &x.Scores, &x.Flags, &x.OpensAt, &x.ClosesAt, &x.OccStatus)
	return x, err
}

func loadDefinition(ctx context.Context, q rowQuerier, vid uuid.UUID) (*forms.Definition, error) {
	var raw []byte
	if err := q.QueryRow(ctx, `select definition from form_versions where id = $1`, vid).Scan(&raw); err != nil {
		return nil, err
	}
	return forms.Parse(raw)
}

// windowOpen reports whether the response's occurrence accepts answers now.
func (x responseRow) windowOpen(now time.Time) bool {
	if x.OccurrenceID == nil {
		return false // kiosk walk-ins come later
	}
	return x.OccStatus != nil && *x.OccStatus == "open" && !now.Before(*x.OpensAt) && now.Before(*x.ClosesAt)
}

// practiceToday is today's date in the practice's zone, for "today" bounds.
func (s *Server) practiceToday(now time.Time) string {
	return now.In(s.location()).Format("2006-01-02")
}

type savedPage struct {
	PageID  string          `json:"page_id"`
	Answers json.RawMessage `json:"answers"`
	SavedAt time.Time       `json:"saved_at"`
}

// latestPages returns the latest save of each page, in document order, and
// all their answers merged.
func latestPages(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, rid uuid.UUID, def *forms.Definition) ([]savedPage, forms.Answers, error) {
	rows, err := q.Query(ctx, `select distinct on (page_id) page_id, answers, recorded_at
		from form_answer_events where response_id = $1 order by page_id, id desc`, rid)
	if err != nil {
		return nil, nil, err
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (savedPage, error) {
		var p savedPage
		err := row.Scan(&p.PageID, &p.Answers, &p.SavedAt)
		return p, err
	})
	if err != nil {
		return nil, nil, err
	}
	byPage := map[string]savedPage{}
	for _, p := range got {
		byPage[p.PageID] = p
	}
	pages := []savedPage{}
	merged := forms.Answers{}
	for _, id := range def.PageIDs() {
		p, ok := byPage[id]
		if !ok {
			continue
		}
		pages = append(pages, p)
		a, err := forms.ParseAnswers(p.Answers)
		if err != nil {
			return nil, nil, err
		}
		for k, v := range a {
			merged[k] = v
		}
	}
	return pages, merged, nil
}

func setETag(w http.ResponseWriter, revision int) {
	w.Header().Set("ETag", `"`+strconv.Itoa(revision)+`"`)
}

// parseIfMatch reads If-Match: "3", W/"3" or 3.
func parseIfMatch(h string) (int, bool) {
	h = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(h), "W/"))
	n, err := strconv.Atoi(strings.Trim(h, `"`))
	return n, err == nil && n >= 0
}

// respTx runs fn in a transaction and writes its status and body.
func (s *Server) respTx(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, tx pgx.Tx) (int, any, error)) {
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	status, body, err := fn(ctx, tx)
	if errors.Is(err, errHandled) {
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, status, body)
}

// --- the patient --------------------------------------------------------------

type responseView struct {
	ID           uuid.UUID     `json:"id"`
	OccurrenceID *uuid.UUID    `json:"occurrence_id"`
	VersionID    uuid.UUID     `json:"version_id"`
	Status       string        `json:"status"`
	Revision     int           `json:"revision"`
	StartedAt    time.Time     `json:"started_at"`
	ClosesAt     *time.Time    `json:"closes_at"`
	Pages        []savedPage   `json:"pages"`
	Answers      forms.Answers `json:"answers"`
}

// StartResponse starts a response to one of the patient's open
// occurrences, or returns the one in progress. Patient, any channel;
// audited as form_response.start.
// Route: POST /api/v1/responses
func (s *Server) StartResponse(w http.ResponseWriter, r *http.Request) {
	id, ok := s.patientFormsOn(w, r)
	if !ok {
		return
	}
	var req struct {
		OccurrenceID uuid.UUID `json:"occurrence_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.OccurrenceID == uuid.Nil {
		writeErr(w, http.StatusBadRequest, "send {\"occurrence_id\": \"…\"}")
		return
	}
	now := time.Now()
	s.respTx(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var vid uuid.UUID
		var status, channel string
		var opens, closes time.Time
		err := tx.QueryRow(ctx, `select o.version_id, o.status, o.opens_at, o.closes_at, a.channel
			from form_occurrences o join form_assignments a on a.id = o.assignment_id
			where o.id = $1 and o.tenant_id = $2 and o.patient_id = $3 for update of o`,
			req.OccurrenceID, id.TenantID, id.UserID).Scan(&vid, &status, &opens, &closes, &channel)
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "form not found")
			return 0, nil, errHandled
		}
		if err != nil {
			return 0, nil, err
		}
		switch {
		case status == "completed":
			writeErrCode(w, http.StatusConflict, "already_submitted", "This form has already been sent.")
			return 0, nil, errHandled
		case status != "open" || !now.Before(closes):
			writeErrCode(w, http.StatusConflict, "window_closed", "This form is closed.")
			return 0, nil, errHandled
		case now.Before(opens):
			writeErrCode(w, http.StatusConflict, "not_open", "This form isn't open yet.")
			return 0, nil, errHandled
		}
		created := false
		var rid uuid.UUID
		err = tx.QueryRow(ctx, `select id from form_responses where occurrence_id = $1 and status = 'in_progress'`, req.OccurrenceID).Scan(&rid)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.QueryRow(ctx, `insert into form_responses (tenant_id, occurrence_id, patient_id, version_id, channel, device_id)
				values ($1, $2, $3, $4, $5, $6) returning id`, id.TenantID, req.OccurrenceID, id.UserID, vid, channel, id.DeviceID).Scan(&rid); err != nil {
				return 0, nil, err
			}
			if err := formAudit(ctx, tx, id, "form_response.start", "form_response", rid, map[string]any{"occurrence_id": req.OccurrenceID}); err != nil {
				return 0, nil, err
			}
			created = true
		} else if err != nil {
			return 0, nil, err
		}
		view, err := s.responseView(ctx, tx, id.TenantID, rid)
		if err != nil {
			return 0, nil, err
		}
		setETag(w, view.Revision)
		if created {
			return http.StatusCreated, view, nil
		}
		return http.StatusOK, view, nil
	})
}

func (s *Server) responseView(ctx context.Context, tx pgx.Tx, tenant, rid uuid.UUID) (responseView, error) {
	x, err := loadResponse(ctx, tx, tenant, rid, false)
	if err != nil {
		return responseView{}, err
	}
	def, err := loadDefinition(ctx, tx, x.VersionID)
	if err != nil {
		return responseView{}, err
	}
	pages, merged, err := latestPages(ctx, tx, rid, def)
	if err != nil {
		return responseView{}, err
	}
	return responseView{ID: x.ID, OccurrenceID: x.OccurrenceID, VersionID: x.VersionID, Status: x.Status, Revision: x.Revision,
		StartedAt: x.StartedAt, ClosesAt: x.ClosesAt, Pages: pages, Answers: merged}, nil
}

// patientResponse loads and locks one of the patient's responses, writing
// 404 for anyone else's.
func patientResponse(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, id Identity, rid uuid.UUID) (responseRow, bool, error) {
	x, err := loadResponse(ctx, tx, id.TenantID, rid, true)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && x.PatientID != id.UserID) {
		writeErr(w, http.StatusNotFound, "response not found")
		return x, false, nil
	}
	return x, err == nil, err
}

// SaveResponsePage saves one page's answers. Patient, any channel; audited
// as form_response.save_page (the page id only).
// Route: PUT /api/v1/responses/{responseID}/pages/{pageID}
func (s *Server) SaveResponsePage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.patientFormsOn(w, r)
	if !ok {
		return
	}
	rid, ok := uuidParam(w, r, "responseID", "response")
	if !ok {
		return
	}
	pageID := chi.URLParam(r, "pageID")
	expected, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		writeErrCode(w, http.StatusBadRequest, "if_match_required", "If-Match with the response revision is required")
		return
	}
	key, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key (a UUID) is required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPageBody+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(body) > maxPageBody {
		writeErrCode(w, http.StatusRequestEntityTooLarge, "body_too_large", "a page save is limited to 64 KB")
		return
	}
	var req struct {
		Answers json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Answers) == 0 {
		writeErr(w, http.StatusBadRequest, "send {\"answers\": {…}}")
		return
	}
	answers, err := forms.ParseAnswers(req.Answers)
	if err != nil || answers == nil {
		writeErr(w, http.StatusBadRequest, "answers must be an object")
		return
	}
	canonBody, _ := forms.Canonical(req.Answers)
	sum := sha256.Sum256(append([]byte(pageID+"\n"), canonBody...))
	digest := hex.EncodeToString(sum[:])
	now := time.Now()
	s.respTx(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		x, ok, err := patientResponse(ctx, tx, w, id, rid)
		if !ok {
			if err == nil {
				err = errHandled
			}
			return 0, nil, err
		}
		// A retry of a save that already went through returns its result.
		var prevDigest string
		var prevRevision int
		var prevAt time.Time
		err = tx.QueryRow(ctx, `select body_digest, revision, recorded_at from form_answer_events where response_id = $1 and idempotency_key = $2`,
			rid, key).Scan(&prevDigest, &prevRevision, &prevAt)
		if err == nil {
			if prevDigest != digest {
				writeErrCode(w, http.StatusConflict, "idempotency_mismatch", "this Idempotency-Key was used for a different save")
				return 0, nil, errHandled
			}
			setETag(w, x.Revision)
			return http.StatusOK, map[string]any{"id": rid, "page_id": pageID, "revision": prevRevision, "saved_at": prevAt}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, err
		}
		switch {
		case x.Status == "submitted":
			writeErrCode(w, http.StatusConflict, "already_submitted", "This form has already been sent.")
			return 0, nil, errHandled
		case x.Status == "expired" || !x.windowOpen(now):
			writeErrCode(w, http.StatusConflict, "window_closed", "This form is closed.")
			return 0, nil, errHandled
		case expected != x.Revision:
			writeJSON(w, http.StatusConflict, map[string]any{"error": "the response changed since you loaded it", "code": "stale_revision", "revision": x.Revision})
			return 0, nil, errHandled
		}
		def, err := loadDefinition(ctx, tx, x.VersionID)
		if err != nil {
			return 0, nil, err
		}
		kept, problems, found := forms.CheckPage(def, pageID, answers, s.practiceToday(now))
		if !found {
			writeErrCode(w, http.StatusNotFound, "unknown_page", "no page "+pageID+" on this form")
			return 0, nil, errHandled
		}
		if len(problems) > 0 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "some answers can't be saved", "code": "invalid_answers", "errors": problems})
			return 0, nil, errHandled
		}
		stored, _ := json.Marshal(kept)
		rev := x.Revision + 1
		var at time.Time
		if err := tx.QueryRow(ctx, `insert into form_answer_events (tenant_id, response_id, page_id, answers, revision, idempotency_key, body_digest, device_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8) returning recorded_at`,
			id.TenantID, rid, pageID, stored, rev, key, digest, id.DeviceID).Scan(&at); err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `update form_responses set revision = $2, updated_at = now() where id = $1`, rid, rev); err != nil {
			return 0, nil, err
		}
		if err := formAudit(ctx, tx, id, "form_response.save_page", "form_response", rid, map[string]any{"page_id": pageID}); err != nil {
			return 0, nil, err
		}
		setETag(w, rev)
		return http.StatusOK, map[string]any{"id": rid, "page_id": pageID, "revision": rev, "saved_at": at}, nil
	})
}

// patientScores keeps the scores the form shows to the patient.
func patientScores(def *forms.Definition, raw []byte) map[string]json.RawMessage {
	all := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &all)
	out := map[string]json.RawMessage{}
	for _, sd := range def.Scores {
		if v, ok := all[sd.ID]; ok && sd.ShowScore {
			out[sd.ID] = v
		}
	}
	return out
}

type submitProblem struct {
	forms.AnswerError
	PageID string `json:"page_id"`
}

// SubmitResponse submits a response: it rebuilds the answers from the
// latest save of each page, drops hidden items, checks required answers on
// visible pages, scores it and completes the occurrence. Submitting again
// returns the same result. Patient, any channel; audited as
// form_response.submit (score ids and counts, never values).
// Route: POST /api/v1/responses/{responseID}/submit
func (s *Server) SubmitResponse(w http.ResponseWriter, r *http.Request) {
	id, ok := s.patientFormsOn(w, r)
	if !ok {
		return
	}
	rid, ok := uuidParam(w, r, "responseID", "response")
	if !ok {
		return
	}
	if _, err := uuid.Parse(r.Header.Get("Idempotency-Key")); err != nil {
		writeErrCode(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key (a UUID) is required")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	now := time.Now()
	s.respTx(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		x, ok, err := patientResponse(ctx, tx, w, id, rid)
		if !ok {
			if err == nil {
				err = errHandled
			}
			return 0, nil, err
		}
		def, err := loadDefinition(ctx, tx, x.VersionID)
		if err != nil {
			return 0, nil, err
		}
		if x.Status == "submitted" { // one outcome, however often it's sent
			setETag(w, x.Revision)
			return http.StatusOK, map[string]any{"id": rid, "status": "submitted", "submitted_at": x.SubmittedAt, "scores": patientScores(def, x.Scores)}, nil
		}
		if x.Status == "expired" || !x.windowOpen(now) {
			writeErrCode(w, http.StatusConflict, "window_closed", "This form is closed.")
			return 0, nil, errHandled
		}
		if ifMatch != "" {
			if n, ok := parseIfMatch(ifMatch); !ok || n != x.Revision {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "the response changed since you loaded it", "code": "stale_revision", "revision": x.Revision})
				return 0, nil, errHandled
			}
		}
		_, merged, err := latestPages(ctx, tx, rid, def)
		if err != nil {
			return 0, nil, err
		}
		checked := forms.CheckAnswersOn(def, merged, s.practiceToday(now))
		var problems []submitProblem
		overflow := false
		for _, e := range checked.Errors {
			if e.Code == forms.CodeOverflow {
				overflow = true
				continue
			}
			problems = append(problems, submitProblem{e, def.PageOf(e.Item)})
		}
		if len(problems) > 0 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "some answers are missing or can't be accepted", "code": "invalid_answers", "errors": problems})
			return 0, nil, errHandled
		}
		if overflow {
			writeErrCode(w, http.StatusUnprocessableEntity, "score_overflow", "a score can't be computed exactly; please contact the practice")
			return 0, nil, errHandled
		}
		answers, _ := json.Marshal(checked.Answers)
		scores, _ := json.Marshal(checked.Result.Scores)
		var at time.Time
		if err := tx.QueryRow(ctx, `update form_responses set status = 'submitted', submitted_at = now(), updated_at = now(),
			answers = $2, scores = $3, flags = $4 where id = $1 returning submitted_at`, rid, answers, scores, flagsJSON(def, checked.Result.Flags)).Scan(&at); err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `update form_occurrences set status = 'completed', completed_at = $2 where id = $1 and status = 'open'`,
			x.OccurrenceID, at); err != nil {
			return 0, nil, err
		}
		ids := []string{}
		for _, sd := range def.Scores {
			ids = append(ids, sd.ID)
		}
		if err := formAudit(ctx, tx, id, "form_response.submit", "form_response", rid,
			map[string]any{"occurrence_id": x.OccurrenceID, "score_ids": ids, "answered": len(checked.Answers)}); err != nil {
			return 0, nil, err
		}
		setETag(w, x.Revision)
		return http.StatusOK, map[string]any{"id": rid, "status": "submitted", "submitted_at": at, "scores": patientScores(def, scores)}, nil
	})
}

// --- staff --------------------------------------------------------------------

type responseSummary struct {
	ID          uuid.UUID                  `json:"id"`
	FormName    string                     `json:"form_name"`
	VersionID   uuid.UUID                  `json:"version_id"`
	Version     int                        `json:"version"`
	Status      string                     `json:"status"`
	SubmittedAt *time.Time                 `json:"submitted_at"`
	New         bool                       `json:"new"`
	Scores      map[string]json.RawMessage `json:"scores"`
	Flags       json.RawMessage            `json:"flags"` // [{id, text}], staff-side (#199)
}

// ListResponses lists a patient's submitted responses, newest first, with
// scores and whether any staff member has opened each yet. Staff, LAN;
// read-audited.
// Route: GET /api/v1/responses?patient_id=
func (s *Server) ListResponses(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, false)
	if !ok {
		return
	}
	patient, err := uuid.Parse(r.URL.Query().Get("patient_id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "patient_id is required")
		return
	}
	s.respTx(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		if !mayFormPatient(ctx, tx, w, id, patient) {
			return 0, nil, errHandled
		}
		rows, err := tx.Query(ctx, `select r.id, f.name, r.version_id, v.version, r.status, r.submitted_at,
			       not exists (select 1 from form_response_reads rr where rr.response_id = r.id), coalesce(r.scores, '{}'), r.flags
			from form_responses r join form_versions v on v.id = r.version_id join forms f on f.id = v.form_id
			where r.tenant_id = $1 and r.patient_id = $2 and r.status = 'submitted'
			order by r.submitted_at desc`, id.TenantID, patient)
		if err != nil {
			return 0, nil, err
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (responseSummary, error) {
			var x responseSummary
			var scores []byte
			err := row.Scan(&x.ID, &x.FormName, &x.VersionID, &x.Version, &x.Status, &x.SubmittedAt, &x.New, &scores, &x.Flags)
			if err == nil {
				err = json.Unmarshal(scores, &x.Scores)
			}
			return x, err
		})
		if err != nil {
			return 0, nil, err
		}
		if list == nil {
			list = []responseSummary{}
		}
		return http.StatusOK, map[string]any{"responses": list}, nil
	})
}

type answeredItem struct {
	ItemID string `json:"item_id"`
	PageID string `json:"page_id"`
	Type   string `json:"type"`
	Text   string `json:"text"`
	Answer any    `json:"answer"`
	// Display is the answer as people read it: option texts for choices,
	// yes or no, the number with its unit.
	Display string `json:"display"`
}

type scoreDetail struct {
	ID       string  `json:"id"`
	Text     string  `json:"text"`
	Value    *string `json:"value"`
	Band     *string `json:"band"`
	BandText string  `json:"band_text,omitempty"`
	Reason   string  `json:"reason,omitempty"`
}

// GetResponse returns a submitted response with each answer next to its
// item's text, the scores with their bands, and the flags. The first staff
// open is recorded (the "new" badge). Staff, LAN; read-audited.
// Route: GET /api/v1/responses/{responseID}
func (s *Server) GetResponse(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, false)
	if !ok {
		return
	}
	rid, ok := uuidParam(w, r, "responseID", "response")
	if !ok {
		return
	}
	s.respTx(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		x, err := loadResponse(ctx, tx, id.TenantID, rid, false)
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "response not found")
			return 0, nil, errHandled
		}
		if err != nil {
			return 0, nil, err
		}
		if !mayFormPatient(ctx, tx, w, id, x.PatientID) {
			return 0, nil, errHandled
		}
		if x.Status != "submitted" {
			writeErr(w, http.StatusNotFound, "response not found (not submitted)")
			return 0, nil, errHandled
		}
		def, err := loadDefinition(ctx, tx, x.VersionID)
		if err != nil {
			return 0, nil, err
		}
		var formName string
		var version int
		if err := tx.QueryRow(ctx, `select f.name, v.version from form_versions v join forms f on f.id = v.form_id where v.id = $1`,
			x.VersionID).Scan(&formName, &version); err != nil {
			return 0, nil, err
		}
		var firstRead *time.Time
		_ = tx.QueryRow(ctx, `select min(read_at) from form_response_reads where response_id = $1`, rid).Scan(&firstRead)
		if _, err := tx.Exec(ctx, `insert into form_response_reads (response_id, user_id) values ($1, $2) on conflict do nothing`, rid, id.UserID); err != nil {
			return 0, nil, err
		}
		answers, err := forms.ParseAnswers(x.Answers)
		if err != nil {
			return 0, nil, err
		}
		items := []answeredItem{}
		for _, it := range def.Items() {
			v, ok := answers[it.ID]
			if !ok {
				continue
			}
			items = append(items, answeredItem{ItemID: it.ID, PageID: def.PageOf(it.ID), Type: it.Type, Text: it.Text, Answer: v, Display: displayAnswer(it, v)})
		}
		var raw map[string]struct {
			Value  *string `json:"value"`
			Band   *string `json:"band"`
			Reason string  `json:"reason"`
		}
		_ = json.Unmarshal(x.Scores, &raw)
		scores := []scoreDetail{}
		for _, sd := range def.Scores {
			v := raw[sd.ID]
			d := scoreDetail{ID: sd.ID, Text: sd.Text, Value: v.Value, Band: v.Band, Reason: v.Reason}
			for _, b := range sd.Bands {
				if v.Band != nil && b.ID == *v.Band {
					d.BandText = b.Text
				}
			}
			scores = append(scores, d)
		}
		flags := json.RawMessage(x.Flags)
		return http.StatusOK, map[string]any{
			"id": x.ID, "patient_id": x.PatientID, "occurrence_id": x.OccurrenceID, "form_name": formName,
			"title": def.Title, "version_id": x.VersionID, "version": version, "status": x.Status, "channel": x.Channel,
			"started_at": x.StartedAt, "submitted_at": x.SubmittedAt, "first_read_at": firstRead, "new": firstRead == nil,
			"items": items, "scores": scores, "flags": flags,
		}, nil
	})
}

// displayAnswer writes an answer for people to read.
func displayAnswer(it *forms.Item, v any) string {
	switch it.Type {
	case forms.TypeChoice:
		var vals []string
		switch x := v.(type) {
		case string:
			vals = []string{x}
		case []any:
			for _, e := range x {
				if s, ok := e.(string); ok {
					vals = append(vals, s)
				}
			}
		}
		var out []string
		for _, val := range vals {
			text := val
			for _, o := range it.Options {
				if o.Value == val {
					text = o.Text
				}
			}
			out = append(out, text)
		}
		return strings.Join(out, ", ")
	case forms.TypeBoolean:
		if b, ok := v.(bool); ok && b {
			return "Yes"
		}
		return "No"
	case forms.TypeInteger, forms.TypeDecimal:
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(v)
		s := strings.TrimSpace(buf.String())
		if it.Unit != "" {
			s += " " + it.Unit
		}
		return s
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// expireResponses marks in-progress responses expired once their
// occurrence has closed. The runner calls it with markMissed.
func (s *Server) expireResponses(ctx context.Context, now time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `update form_responses r set status = 'expired', updated_at = now()
		from form_occurrences o where o.id = r.occurrence_id and r.status = 'in_progress' and o.closes_at <= $1`, now)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// flagID is a raised flag as stored on the response and shown to staff.
type flagID struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// flagsJSON writes the raised flags with their staff text (#199). Never
// sent to the patient.
func flagsJSON(def *forms.Definition, raised []string) []byte {
	out := []flagID{}
	for _, id := range raised {
		for _, fl := range def.Flags {
			if fl.ID == id {
				out = append(out, flagID{fl.ID, fl.Text})
			}
		}
	}
	b, _ := json.Marshal(out)
	return b
}
