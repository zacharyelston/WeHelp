package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/turncred"
)

// Video visits (#6). Any scheduled appointment becomes a video visit when its
// provider starts one inside the join window; the patient gets a push and
// joins. Only the two of them, from the app, one device each.
//
// Signaling (offer, answer, ICE candidates) goes through here over each
// phone's pinned connection, so the Relay can't swap the DTLS fingerprints
// that secure the media. It is held in memory only, never stored or logged.
// Media goes phone to phone, end to end encrypted, through the Relay's TURN,
// with credentials this office mints from the key its Relay sent it.
//
// Ending a visit doesn't change the appointment: staff mark its outcome.

const (
	visitEarly      = 10 * time.Minute // joinable this long before the start
	visitLate       = 30 * time.Minute // and this long after the end
	visitMaxSignals = 400
	visitMaxSDP     = 16 << 10
	visitMaxCand    = 2 << 10
)

// visitPollWait is how long a signal poll waits for something new (a var for tests).
var visitPollWait = 25 * time.Second

// TURNKeySource gives the office's TURN key, which the Relay sends when the
// tunnel connects. Without one, visits can't start.
type TURNKeySource interface {
	TURNKey() (turncred.Key, bool)
}

// SetTURNKeys turns video visits on. Before install there is no Relay.
func (s *Server) SetTURNKeys(k TURNKeySource) { s.turnKeys = k }

// ---------------------------------------------------------------------------
// The in-memory signaling hub.
// ---------------------------------------------------------------------------

type visitSignal struct {
	Seq       int64           `json:"seq"`
	From      string          `json:"from"` // provider, patient, or server
	Type      string          `json:"type"` // offer, answer, candidate, restart; joined, left, ended, replaced
	SDP       string          `json:"sdp,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`
	to        string          // a server signal for one role only; "" = both
}

type visitRoom struct {
	seq      int64
	signals  []visitSignal
	devices  map[string]uuid.UUID // role → the device in the call
	replaced map[uuid.UUID]bool   // devices another of the same person's devices took over from
	wake     chan struct{}        // closed and replaced on every change
}

type visitHub struct {
	mu    sync.Mutex
	rooms map[uuid.UUID]*visitRoom
}

func newVisitHub() *visitHub { return &visitHub{rooms: map[uuid.UUID]*visitRoom{}} }

func (h *visitHub) roomLocked(id uuid.UUID) *visitRoom {
	rm := h.rooms[id]
	if rm == nil {
		rm = &visitRoom{devices: map[string]uuid.UUID{}, replaced: map[uuid.UUID]bool{}, wake: make(chan struct{})}
		h.rooms[id] = rm
	}
	return rm
}

func (rm *visitRoom) addLocked(sig visitSignal) {
	rm.seq++
	sig.Seq = rm.seq
	rm.signals = append(rm.signals, sig)
	if len(rm.signals) > visitMaxSignals {
		rm.signals = rm.signals[len(rm.signals)-visitMaxSignals:]
	}
	close(rm.wake)
	rm.wake = make(chan struct{})
}

// join puts device in the call as role, replacing any earlier device of
// that role, and tells the other side. It returns whether the other party is
// present and the sequence to poll after.
func (h *visitHub) join(id uuid.UUID, role string, device uuid.UUID) (peerPresent bool, after int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rm := h.roomLocked(id)
	if old, ok := rm.devices[role]; ok && old != device {
		rm.replaced[old] = true
		rm.addLocked(visitSignal{From: "server", Type: "replaced", to: role}) // wakes its waiting poll
	}
	delete(rm.replaced, device)
	rm.devices[role] = device
	after = rm.seq
	rm.addLocked(visitSignal{From: role, Type: "joined"})
	_, peerPresent = rm.devices[otherRole(role)]
	return peerPresent, after
}

// current reports whether device is the one in the call for role.
func (h *visitHub) current(id uuid.UUID, role string, device uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	rm := h.rooms[id]
	return rm != nil && rm.devices[role] == device
}

// notInCall answers a device that isn't in the call: replaced by another
// of the person's devices, or never joined (or the Back Office restarted).
func (h *visitHub) notInCall(w http.ResponseWriter, id, device uuid.UUID) {
	h.mu.Lock()
	rm := h.rooms[id]
	replaced := rm != nil && rm.replaced[device]
	h.mu.Unlock()
	if replaced {
		writeErrCode(w, http.StatusConflict, "replaced", "this visit moved to another of your devices")
		return
	}
	writeErrCode(w, http.StatusConflict, "not_in_call", "join the visit first")
}

func (h *visitHub) send(id uuid.UUID, sig visitSignal) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.roomLocked(id).addLocked(sig)
}

func (h *visitHub) leave(id uuid.UUID, role string, device uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rm := h.rooms[id]
	if rm == nil || rm.devices[role] != device {
		return
	}
	delete(rm.devices, role)
	rm.addLocked(visitSignal{From: role, Type: "left"})
}

// end tells everyone the visit ended and forgets it.
func (h *visitHub) end(id uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if rm := h.rooms[id]; rm != nil {
		rm.addLocked(visitSignal{From: "server", Type: "ended"})
		delete(h.rooms, id)
	}
}

// since returns the signals for role after seq, or a channel that closes on
// the next change. ok is false once the room is gone.
func (h *visitHub) since(id uuid.UUID, role string, after int64) (out []visitSignal, wake <-chan struct{}, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rm := h.rooms[id]
	if rm == nil {
		return nil, nil, false
	}
	for _, s := range rm.signals {
		if s.Seq > after && s.From != role && (s.to == "" || s.to == role) {
			out = append(out, s)
		}
	}
	return out, rm.wake, true
}

func otherRole(role string) string {
	if role == "provider" {
		return "patient"
	}
	return "provider"
}

// ---------------------------------------------------------------------------
// Handlers.
// ---------------------------------------------------------------------------

type visitAppt struct {
	ID, ProviderID, PatientID uuid.UUID
	StartsAt, EndsAt          time.Time
	Status                    string
	VisitID                   *uuid.UUID
	VisitStatus, TurnToken    *string
}

func (a visitAppt) window() (time.Time, time.Time) {
	return a.StartsAt.Add(-visitEarly), a.EndsAt.Add(visitLate)
}

func (a visitAppt) role(user uuid.UUID) string {
	switch user {
	case a.ProviderID:
		return "provider"
	case a.PatientID:
		return "patient"
	}
	return ""
}

func loadVisitAppt(ctx context.Context, q pgx.Tx, tenant, apptID uuid.UUID, lock bool) (visitAppt, error) {
	var a visitAppt
	sql := `
		select a.id, a.provider_id, a.patient_id, a.starts_at, a.ends_at, a.status, v.id, v.status, v.turn_token
		from appointments a left join visits v on v.appointment_id = a.id
		where a.id = $1 and a.tenant_id = $2`
	if lock {
		sql += ` for update of a`
	}
	err := q.QueryRow(ctx, sql, apptID, tenant).Scan(&a.ID, &a.ProviderID, &a.PatientID, &a.StartsAt, &a.EndsAt,
		&a.Status, &a.VisitID, &a.VisitStatus, &a.TurnToken)
	return a, err
}

// visitCaller is a signed-in participant on a phone, with video turned on.
func (s *Server) visitCaller(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, _ := identityFrom(r.Context())
	if id.DeviceID == nil {
		writeErr(w, http.StatusForbidden, "video visits are joined from the WeHelp app")
		return id, false
	}
	if id.Role != "provider" && id.Role != "patient" {
		writeErr(w, http.StatusForbidden, "only the appointment's provider and patient can join")
		return id, false
	}
	if featureOff(w, r.Context(), s.pool, id.TenantID, "video") {
		return id, false
	}
	return id, true
}

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

type visitJoin struct {
	VisitID     uuid.UUID   `json:"visit_id"`
	Role        string      `json:"role"`
	PeerPresent bool        `json:"peer_present"`
	After       int64       `json:"after"` // poll signals after this sequence
	ICEServers  []iceServer `json:"ice_servers"`
	ExpiresAt   time.Time   `json:"expires_at"` // end of the join window
}

// StartVisit is the provider starting (or rejoining) the visit.
func (s *Server) StartVisit(w http.ResponseWriter, r *http.Request) { s.enterVisit(w, r, true) }

// JoinVisit is the patient joining an open visit, or the provider rejoining.
func (s *Server) JoinVisit(w http.ResponseWriter, r *http.Request) { s.enterVisit(w, r, false) }

func (s *Server) enterVisit(w http.ResponseWriter, r *http.Request, start bool) {
	id, ok := s.visitCaller(w, r)
	if !ok {
		return
	}
	apptID, err := uuid.Parse(chi.URLParam(r, "appointmentID"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "appointment not found")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	a, err := loadVisitAppt(ctx, tx, id.TenantID, apptID, start)
	role := a.role(id.UserID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && role == "") {
		writeErr(w, http.StatusNotFound, "appointment not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if start && role != "provider" {
		writeErr(w, http.StatusForbidden, "only the appointment's provider starts the visit")
		return
	}
	if a.Status != "scheduled" {
		writeErrCode(w, http.StatusConflict, "not_scheduled", "this appointment is "+a.Status)
		return
	}
	from, until := a.window()
	if now := time.Now(); now.Before(from) || now.After(until) {
		writeErrCode(w, http.StatusConflict, "outside_window",
			"the visit can be joined from "+from.Format(time.RFC3339)+" until "+until.Format(time.RFC3339))
		return
	}
	if a.VisitStatus != nil && *a.VisitStatus == "ended" {
		writeErrCode(w, http.StatusConflict, "ended", "this visit has ended")
		return
	}
	if a.VisitID == nil && !start {
		writeErrCode(w, http.StatusConflict, "not_started", "your provider will start the visit")
		return
	}
	var key turncred.Key
	if s.turnKeys != nil {
		key, ok = s.turnKeys.TURNKey()
	}
	if s.turnKeys == nil || !ok {
		writeErrCode(w, http.StatusServiceUnavailable, "video_unavailable", "the video service isn't reachable right now")
		return
	}

	opened := false
	if a.VisitID == nil {
		token := randomToken(18)
		var vid uuid.UUID
		if err := tx.QueryRow(ctx, `
			insert into visits (tenant_id, appointment_id, turn_token) values ($1, $2, $3) returning id`,
			id.TenantID, a.ID, token).Scan(&vid); err != nil {
			writeErr(w, http.StatusInternalServerError, "start failed")
			return
		}
		a.VisitID, a.TurnToken, opened = &vid, &token, true
		if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: "visit.open",
			ResourceType: "visit", ResourceID: vid.String(),
			Detail: mustJSON(map[string]string{"appointment_id": a.ID.String()})}); err != nil {
			writeErr(w, http.StatusInternalServerError, "audit failed")
			return
		}
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: "visit.join",
		ResourceType: "visit", ResourceID: a.VisitID.String(),
		Detail: mustJSON(map[string]string{"role": role, "device_id": id.DeviceID.String()})}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}

	present, after := s.visits.join(*a.VisitID, role, *id.DeviceID)
	if role == "provider" && (opened || !present) {
		s.notify(a.PatientID, "visit_starting")
	}
	user, pass := turncred.Mint(key, *a.TurnToken, until)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, visitJoin{VisitID: *a.VisitID, Role: role, PeerPresent: present, After: after,
		ICEServers: []iceServer{{URLs: key.URLs, Username: user, Credential: pass}}, ExpiresAt: until})
}

// visitMember checks the caller is the device in the call for this visit.
func (s *Server) visitMember(w http.ResponseWriter, r *http.Request) (Identity, uuid.UUID, visitAppt, string, bool) {
	id, ok := s.visitCaller(w, r)
	if !ok {
		return id, uuid.Nil, visitAppt{}, "", false
	}
	vid, err := uuid.Parse(chi.URLParam(r, "visitID"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "visit not found")
		return id, vid, visitAppt{}, "", false
	}
	var a visitAppt
	var status string
	err = s.pool.QueryRow(r.Context(), `
		select a.id, a.provider_id, a.patient_id, a.starts_at, a.ends_at, a.status, v.status
		from visits v join appointments a on a.id = v.appointment_id
		where v.id = $1 and v.tenant_id = $2`, vid, id.TenantID).
		Scan(&a.ID, &a.ProviderID, &a.PatientID, &a.StartsAt, &a.EndsAt, &a.Status, &status)
	role := a.role(id.UserID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && role == "") {
		writeErr(w, http.StatusNotFound, "visit not found")
		return id, vid, a, "", false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return id, vid, a, "", false
	}
	a.VisitStatus = &status
	return id, vid, a, role, true
}

// PollVisitSignals long-polls for signals from the other side.
func (s *Server) PollVisitSignals(w http.ResponseWriter, r *http.Request) {
	id, vid, a, role, ok := s.visitMember(w, r)
	if !ok {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	w.Header().Set("Cache-Control", "no-store")
	if *a.VisitStatus == "ended" {
		writeJSON(w, http.StatusOK, map[string]any{"signals": []visitSignal{}, "ended": true})
		return
	}
	if !s.visits.current(vid, role, *id.DeviceID) {
		s.visits.notInCall(w, vid, *id.DeviceID)
		return
	}
	timer := time.NewTimer(visitPollWait)
	defer timer.Stop()
	for {
		sigs, wake, live := s.visits.since(vid, role, after)
		if !live {
			writeJSON(w, http.StatusOK, map[string]any{"signals": []visitSignal{}, "ended": true})
			return
		}
		if len(sigs) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"signals": sigs, "ended": false})
			return
		}
		select {
		case <-wake:
		case <-timer.C:
			writeJSON(w, http.StatusOK, map[string]any{"signals": []visitSignal{}, "ended": false})
			return
		case <-r.Context().Done():
			return
		}
	}
}

type visitSignalIn struct {
	Type      string          `json:"type"`
	SDP       string          `json:"sdp"`
	Candidate json.RawMessage `json:"candidate"`
}

// SendVisitSignal passes one signal to the other side. The provider makes
// offers and the patient answers, so the two never offer at once.
func (s *Server) SendVisitSignal(w http.ResponseWriter, r *http.Request) {
	id, vid, a, role, ok := s.visitMember(w, r)
	if !ok {
		return
	}
	if *a.VisitStatus == "ended" {
		writeErrCode(w, http.StatusConflict, "ended", "this visit has ended")
		return
	}
	if !s.visits.current(vid, role, *id.DeviceID) {
		s.visits.notInCall(w, vid, *id.DeviceID)
		return
	}
	var in visitSignalIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, visitMaxSDP+4096)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	switch {
	case in.Type == "offer" && role == "provider", in.Type == "answer" && role == "patient":
		if in.SDP == "" || len(in.SDP) > visitMaxSDP {
			writeErr(w, http.StatusBadRequest, "sdp is required (at most 16 KB)")
			return
		}
		in.Candidate = nil
	case in.Type == "candidate":
		var obj map[string]any
		if len(in.Candidate) == 0 || len(in.Candidate) > visitMaxCand || json.Unmarshal(in.Candidate, &obj) != nil {
			writeErr(w, http.StatusBadRequest, "candidate must be a JSON object (at most 2 KB)")
			return
		}
		in.SDP = ""
	case in.Type == "restart" && role == "patient": // ask the provider for a fresh offer
		in.SDP, in.Candidate = "", nil
	default:
		writeErr(w, http.StatusBadRequest, "the provider sends offers, the patient answers; both send candidates")
		return
	}
	s.visits.send(vid, visitSignal{From: role, Type: in.Type, SDP: in.SDP, Candidate: in.Candidate})
	w.WriteHeader(http.StatusNoContent)
}

// LeaveVisit takes the caller's device out of the call. The visit stays
// open for them to rejoin.
func (s *Server) LeaveVisit(w http.ResponseWriter, r *http.Request) {
	id, vid, a, role, ok := s.visitMember(w, r)
	if !ok {
		return
	}
	if *a.VisitStatus == "open" && s.visits.current(vid, role, *id.DeviceID) {
		ctx := r.Context()
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "tx failed")
			return
		}
		defer tx.Rollback(ctx)
		if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID,
			Action: "visit.leave", ResourceType: "visit", ResourceID: vid.String(),
			Detail: mustJSON(map[string]string{"role": role, "device_id": id.DeviceID.String()})}); err != nil {
			writeErr(w, http.StatusInternalServerError, "audit failed")
			return
		}
		if err := tx.Commit(ctx); err != nil {
			writeErr(w, http.StatusInternalServerError, "commit failed")
			return
		}
		s.visits.leave(vid, role, *id.DeviceID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// EndVisit is the provider ending the visit for both. The appointment is
// left for staff to mark.
func (s *Server) EndVisit(w http.ResponseWriter, r *http.Request) {
	id, vid, _, role, ok := s.visitMember(w, r)
	if !ok {
		return
	}
	if role != "provider" {
		writeErr(w, http.StatusForbidden, "only the provider ends the visit; leave it instead")
		return
	}
	if err := s.endVisit(r.Context(), id.TenantID, vid, &id.UserID, "provider"); err != nil {
		writeErr(w, http.StatusInternalServerError, "end failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// endVisit marks an open visit ended with its audit event, then tells both
// phones. Ending one that already ended does nothing.
func (s *Server) endVisit(ctx context.Context, tenant, vid uuid.UUID, actor *uuid.UUID, by string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		update visits set status = 'ended', ended_at = now(), ended_by = $3
		where id = $1 and tenant_id = $2 and status = 'open'`, vid, tenant, by)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: tenant, ActorID: actor, Action: "visit.end",
		ResourceType: "visit", ResourceID: vid.String(), Detail: mustJSON(map[string]string{"by": by})}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.visits.end(vid)
	return nil
}

// closeExpiredVisits ends visits whose join window has passed.
func (s *Server) closeExpiredVisits(ctx context.Context, now time.Time) {
	rows, err := s.pool.Query(ctx, `
		select v.id, v.tenant_id from visits v join appointments a on a.id = v.appointment_id
		where v.status = 'open' and a.ends_at < $1`, now.Add(-visitLate))
	if err != nil {
		slog.Error("visits: expiry lookup", "err", err)
		return
	}
	type open struct{ id, tenant uuid.UUID }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (open, error) {
		var o open
		err := row.Scan(&o.id, &o.tenant)
		return o, err
	})
	if err != nil {
		return
	}
	for _, o := range list {
		if err := s.endVisit(ctx, o.tenant, o.id, nil, "auto"); err != nil {
			slog.Error("visits: auto end", "visit", o.id, "err", err)
		}
	}
}

// RunVisitCloser ends visits nobody ended, once their window passes.
func (s *Server) RunVisitCloser(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.closeExpiredVisits(ctx, now)
		}
	}
}
