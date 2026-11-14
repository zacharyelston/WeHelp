package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/turncred"
)

type fakeTURNKeys struct{ key *turncred.Key }

func (f *fakeTURNKeys) TURNKey() (turncred.Key, bool) {
	if f.key == nil {
		return turncred.Key{}, false
	}
	return *f.key, true
}

// providerDevice pairs a provider's own iPad and returns its client.
func providerDevice(t *testing.T, staff, relay *client, userID string) *client {
	t.Helper()
	_, out := staff.do("POST", "/pairing/invites", map[string]string{"user_id": userID})
	code, sess := newPhone(t).redeem(relay, inviteCode(t, out))
	if code != http.StatusOK {
		t.Fatalf("pair provider device: %d %v", code, sess)
	}
	return &client{t: t, h: staff.h, token: sess["access_token"].(string), relay: true}
}

func auditCountAction(t *testing.T, pool *pgxpool.Pool, action, resource string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from audit_events where action = $1 and resource_id = $2`, action, resource).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func poll(c *client, vid string, after int64) (int, []map[string]any, bool) {
	code, out := c.do("GET", fmt.Sprintf("/visits/%s/signals?after=%d", vid, after), nil)
	var sigs []map[string]any
	if raw, ok := out["signals"].([]any); ok {
		for _, s := range raw {
			sigs = append(sigs, s.(map[string]any))
		}
	}
	ended, _ := out["ended"].(bool)
	return code, sigs, ended
}

func lastSeq(sigs []map[string]any, after int64) int64 {
	for _, s := range sigs {
		if n := int64(s["seq"].(float64)); n > after {
			after = n
		}
	}
	return after
}

// TestVideoVisit is #6's Back Office half: the provider starts the visit
// from the app, the patient is held until then, signaling passes between
// the two phones (offers from the provider, answers from the patient), a
// second device replaces the first, and ending leaves the appointment for
// staff. Front desk, admins, browsers and other people are refused.
func TestVideoVisit(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	visitPollWait = 300 * time.Millisecond
	t.Cleanup(func() { visitPollWait = 15 * time.Second })
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	relay := &client{t: t, h: h, relay: true}
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	ada, jordan := patientID(t, pool, seed.ProviderEmail), patientID(t, pool, seed.Patient1Email)
	ipad := providerDevice(t, staff, relay, ada)
	_, sess := pairPhone(t, staff, relay, jordan)
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}

	// An appointment starting in 5 minutes, inside the join window.
	if _, err := pool.Exec(ctx, `update appointments set status = 'cancelled' where status = 'scheduled'`); err != nil {
		t.Fatal(err)
	}
	starts := time.Now().Add(5 * time.Minute).Truncate(time.Second)
	code, appt := staff.do("POST", "/appointments", map[string]string{"patient_id": jordan,
		"starts_at": starts.Format(time.RFC3339), "ends_at": starts.Add(30 * time.Minute).Format(time.RFC3339)})
	if code != http.StatusCreated {
		t.Fatalf("book: %d %v", code, appt)
	}
	apptID := appt["id"].(string)
	start, join := "/appointments/"+apptID+"/visit/start", "/appointments/"+apptID+"/visit/join"

	// No TURN key from the Relay yet: the video service is unavailable.
	if code, out := ipad.do("POST", start, nil); code != http.StatusServiceUnavailable || out["code"] != "video_unavailable" {
		t.Fatalf("start without a TURN key: %d %v", code, out)
	}
	key := turncred.Key{KeyID: "k1", Secret: "s1", URLs: []string{"turn:192.0.2.1:3478?transport=udp"}}
	s.SetTURNKeys(&fakeTURNKeys{key: &key})

	// The patient waits for the provider; others can't start or join.
	if code, out := phone.do("POST", join, nil); code != http.StatusConflict || out["code"] != "not_started" {
		t.Errorf("patient joins before start: %d %v", code, out)
	}
	if code, _ := phone.do("POST", start, nil); code != http.StatusForbidden {
		t.Errorf("patient starts: %d, want 403", code)
	}
	if code, _ := staff.do("POST", start, nil); code != http.StatusForbidden {
		t.Errorf("provider from a browser session: %d, want 403", code)
	}
	admin := &client{t: t, h: h, token: login(t, h, pool, seed.AdminEmail, seed.AdminPassword)}
	if code, _ := admin.do("POST", join, nil); code != http.StatusForbidden {
		t.Errorf("admin joins: %d, want 403", code)
	}
	_, other := pairPhone(t, staff, relay, patientID(t, pool, seed.Patient2Email))
	sam := &client{t: t, h: h, token: other["access_token"].(string), relay: true}
	if code, _ := sam.do("POST", join, nil); code != http.StatusNotFound {
		t.Errorf("another patient joins: %d, want 404", code)
	}

	// The provider starts it from the iPad.
	code, pv := ipad.do("POST", start, nil)
	if code != http.StatusOK || pv["role"] != "provider" || pv["peer_present"] != false {
		t.Fatalf("start: %d %v", code, pv)
	}
	vid := pv["visit_id"].(string)
	ice := pv["ice_servers"].([]any)[0].(map[string]any)
	u, err := turncred.Parse(ice["username"].(string))
	var token string
	_ = pool.QueryRow(ctx, `select turn_token from visits where id = $1`, vid).Scan(&token)
	if err != nil || u.KeyID != "k1" || u.Token != token || !u.Expires.Equal(starts.Add(60*time.Minute)) ||
		ice["credential"] != turncred.Password("s1", ice["username"].(string)) {
		t.Errorf("TURN credentials %v (parsed %+v, %v)", ice, u, err)
	}
	if auditCountAction(t, pool, "visit.open", vid) != 1 || auditCountAction(t, pool, "visit.join", vid) != 1 {
		t.Error("open and join not audited")
	}
	var list struct {
		Appointments []map[string]any `json:"appointments"`
	}
	phone.doInto("GET", "/appointments", &list)
	shown := false
	for _, a := range list.Appointments {
		shown = shown || (a["id"] == apptID && a["visit_status"] == "open")
	}
	if !shown {
		t.Errorf("patient's schedule doesn't show the open visit: %v", list.Appointments)
	}

	// The patient joins; the provider hears about it and offers.
	code, jv := phone.do("POST", join, nil)
	if code != http.StatusOK || jv["role"] != "patient" || jv["peer_present"] != true || jv["visit_id"] != vid {
		t.Fatalf("join: %d %v", code, jv)
	}
	pAfter, jAfter := int64(pv["after"].(float64)), int64(jv["after"].(float64))
	_, sigs, _ := poll(ipad, vid, pAfter)
	if len(sigs) != 1 || sigs[0]["type"] != "joined" || sigs[0]["from"] != "patient" {
		t.Fatalf("provider sees %v", sigs)
	}
	pAfter = lastSeq(sigs, pAfter)
	if code, _ := phone.do("POST", "/visits/"+vid+"/signals", map[string]string{"type": "offer", "sdp": "v=0"}); code != http.StatusBadRequest {
		t.Errorf("patient offers: %d, want 400", code)
	}
	if code, _ := ipad.do("POST", "/visits/"+vid+"/signals", map[string]string{"type": "offer", "sdp": "v=0 offer"}); code != http.StatusNoContent {
		t.Fatalf("offer: %d", code)
	}
	_, sigs, _ = poll(phone, vid, jAfter)
	if len(sigs) != 1 || sigs[0]["type"] != "offer" || sigs[0]["sdp"] != "v=0 offer" {
		t.Fatalf("patient sees %v", sigs)
	}
	jAfter = lastSeq(sigs, jAfter)
	if code, _ := phone.do("POST", "/visits/"+vid+"/signals", map[string]string{"type": "answer", "sdp": "v=0 answer"}); code != http.StatusNoContent {
		t.Fatalf("answer: %d", code)
	}
	if code, _ := phone.do("POST", "/visits/"+vid+"/signals", map[string]any{"type": "candidate", "candidate": "not json object {"}); code != http.StatusBadRequest {
		t.Errorf("bad candidate: %d", code)
	}
	_, sigs, _ = poll(ipad, vid, pAfter)
	if len(sigs) != 1 || sigs[0]["type"] != "answer" {
		t.Fatalf("provider sees %v", sigs)
	}
	pAfter = lastSeq(sigs, pAfter)

	// A long poll wakes when a candidate arrives, and times out empty.
	got := make(chan []map[string]any, 1)
	go func() {
		c := &client{t: t, h: h, token: phone.token, relay: true}
		_, s, _ := poll(c, vid, jAfter)
		got <- s
	}()
	time.Sleep(50 * time.Millisecond)
	cand := map[string]any{"candidate": "candidate:1 1 udp 1 192.0.2.1 49160 typ relay", "sdpMid": "0", "sdpMLineIndex": 0}
	if code, _ := ipad.do("POST", "/visits/"+vid+"/signals", map[string]any{"type": "candidate", "candidate": cand}); code != http.StatusNoContent {
		t.Fatalf("candidate: %d", code)
	}
	select {
	case s := <-got:
		if len(s) != 1 || s[0]["type"] != "candidate" {
			t.Errorf("woken poll got %v", s)
		}
		jAfter = lastSeq(s, jAfter)
	case <-time.After(2 * time.Second):
		t.Fatal("long poll didn't wake")
	}
	if code, s, ended := poll(phone, vid, jAfter); code != http.StatusOK || len(s) != 0 || ended {
		t.Errorf("idle poll: %d %v %v", code, s, ended)
	}

	// A second iPad takes over the provider's place; the first is told.
	// A poll waiting on the first iPad wakes with "replaced"; after that it
	// gets the replaced code.
	waiting := make(chan []map[string]any, 1)
	go func() {
		c := &client{t: t, h: h, token: ipad.token, relay: true}
		_, s, _ := poll(c, vid, pAfter)
		waiting <- s
	}()
	time.Sleep(50 * time.Millisecond)
	ipad2 := providerDevice(t, staff, relay, ada)
	if code, _ := ipad2.do("POST", join, nil); code != http.StatusOK {
		t.Fatalf("provider rejoins from another device: %d", code)
	}
	select {
	case s := <-waiting:
		if len(s) == 0 || s[0]["type"] != "replaced" {
			t.Errorf("first iPad's waiting poll got %v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first iPad's poll didn't wake")
	}
	if code, out := ipad.do("GET", "/visits/"+vid+"/signals?after=0", nil); code != http.StatusConflict || out["code"] != "replaced" {
		t.Errorf("replaced device polls: %d %v", code, out)
	}
	if code, out := ipad.do("POST", "/visits/"+vid+"/signals", map[string]string{"type": "offer", "sdp": "x"}); code != http.StatusConflict || out["code"] != "replaced" {
		t.Errorf("replaced device signals: %d %v", code, out)
	}

	// The patient leaves and the provider is told; then the provider ends it.
	if code, _ := phone.do("POST", "/visits/"+vid+"/leave", nil); code != http.StatusNoContent {
		t.Fatalf("leave: %d", code)
	}
	if auditCountAction(t, pool, "visit.leave", vid) != 1 {
		t.Error("leave not audited")
	}
	if code, _ := phone.do("POST", "/visits/"+vid+"/end", nil); code != http.StatusForbidden {
		t.Errorf("patient ends: %d, want 403", code)
	}
	if code, _ := ipad2.do("POST", "/visits/"+vid+"/end", nil); code != http.StatusNoContent {
		t.Fatalf("end: %d", code)
	}
	if code, _, ended := poll(phone, vid, 0); code != http.StatusOK || !ended {
		t.Errorf("poll after end: %d ended=%v", code, ended)
	}
	if code, out := phone.do("POST", join, nil); code != http.StatusConflict || out["code"] != "ended" {
		t.Errorf("join after end: %d %v", code, out)
	}
	var status, endedBy string
	_ = pool.QueryRow(ctx, `select a.status, v.ended_by from visits v join appointments a on a.id = v.appointment_id where v.id = $1`, vid).Scan(&status, &endedBy)
	if status != "scheduled" || endedBy != "provider" || auditCountAction(t, pool, "visit.end", vid) != 1 {
		t.Errorf("after end: appointment %s, ended by %s", status, endedBy)
	}

	// Outside the join window, and with video turned off.
	far := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	_, later := staff.do("POST", "/appointments", map[string]string{"patient_id": jordan,
		"starts_at": far.Format(time.RFC3339), "ends_at": far.Add(30 * time.Minute).Format(time.RFC3339)})
	if code, out := ipad2.do("POST", "/appointments/"+later["id"].(string)+"/visit/start", nil); code != http.StatusConflict || out["code"] != "outside_window" {
		t.Errorf("start days early: %d %v", code, out)
	}
	if _, err := pool.Exec(ctx, `update appointments set starts_at = now() + interval '1 minute', ends_at = now() + interval '31 minutes' where id = $1`, later["id"]); err != nil {
		t.Fatal(err)
	}
	web := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)
	if code, out := web.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"video": false}}); code != http.StatusOK {
		t.Fatalf("turn video off: %d %v", code, out)
	}
	if code, _ := ipad2.do("POST", "/appointments/"+later["id"].(string)+"/visit/start", nil); code != http.StatusConflict {
		t.Errorf("start with video off: %d, want 409", code)
	}
	web.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"video": true}})

	// A visit nobody ends closes itself once its window passes.
	if code, out := ipad2.do("POST", "/appointments/"+later["id"].(string)+"/visit/start", nil); code != http.StatusOK {
		t.Fatalf("start second visit: %d %v", code, out)
	} else {
		v2 := out["visit_id"].(string)
		s.closeExpiredVisits(ctx, time.Now().Add(2*time.Hour))
		_ = pool.QueryRow(ctx, `select ended_by from visits where id = $1`, v2).Scan(&endedBy)
		if endedBy != "auto" || auditCountAction(t, pool, "visit.end", v2) != 1 {
			t.Errorf("auto close: ended_by %q", endedBy)
		}
	}
}
