package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/turncred"
)

// TestVisitCap is #175's office half: at the Relay's cap of live visits, a
// new visit gets 503 video_busy (audited, nothing opened) instead of a call
// that fails inside TURN; joining a visit already live still works; and a
// room nobody has polled for a minute stops counting.
func TestVisitCap(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	relay := &client{t: t, h: h, relay: true}
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	ada := patientID(t, pool, seed.ProviderEmail)
	jordan := patientID(t, pool, seed.Patient1Email)
	ipad := providerDevice(t, staff, relay, ada)
	_, sess := pairPhone(t, staff, relay, jordan)
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}

	if _, err := pool.Exec(ctx, `update appointments set status = 'cancelled' where status = 'scheduled'`); err != nil {
		t.Fatal(err)
	}
	book := func(patient string, starts time.Time, length time.Duration) string {
		code, appt := staff.do("POST", "/appointments", map[string]string{"patient_id": patient,
			"starts_at": starts.Format(time.RFC3339), "ends_at": starts.Add(length).Format(time.RFC3339)})
		if code != http.StatusCreated {
			t.Fatalf("book: %d %v", code, appt)
		}
		return appt["id"].(string)
	}
	// Two back-to-back appointments, both inside their join windows now.
	now := time.Now().Truncate(time.Second)
	first := book(jordan, now.Add(time.Minute), 7*time.Minute)
	second := book(jordan, now.Add(9*time.Minute), 10*time.Minute)

	key := turncred.Key{KeyID: "k1", Secret: "s1", URLs: []string{"turn:192.0.2.1:3478?transport=udp"}, MaxVisits: 1}
	s.SetTURNKeys(&fakeTURNKeys{key: &key})

	code, pv := ipad.do("POST", "/appointments/"+first+"/visit/start", nil)
	if code != http.StatusOK {
		t.Fatalf("first visit: %d %v", code, pv)
	}
	vid := pv["visit_id"].(string)

	// At the cap: the second visit is refused with a code the app can show.
	code, out := ipad.do("POST", "/appointments/"+second+"/visit/start", nil)
	if code != http.StatusServiceUnavailable || out["code"] != "video_busy" {
		t.Fatalf("second visit at the cap: %d %v", code, out)
	}
	var opened int
	_ = pool.QueryRow(ctx, `select count(*) from visits v join appointments a on a.id = v.appointment_id where a.id = $1`,
		second).Scan(&opened)
	if opened != 0 || auditCountAction(t, pool, "visit.turn_busy", second) != 1 {
		t.Errorf("busy refusal: %d visits opened, audit %d", opened, auditCountAction(t, pool, "visit.turn_busy", second))
	}

	// The patient joining the live visit isn't a new visit.
	if code, jv := phone.do("POST", "/appointments/"+first+"/visit/join", nil); code != http.StatusOK {
		t.Fatalf("patient joins the live visit: %d %v", code, jv)
	}

	// Phones that vanished without leaving: after a minute with no poll the
	// room stops counting, and the second visit can start.
	s.visits.mu.Lock()
	s.visits.rooms[uuid.MustParse(vid)].seen = time.Now().Add(-2 * visitLiveGrace)
	s.visits.mu.Unlock()
	if code, out := ipad.do("POST", "/appointments/"+second+"/visit/start", nil); code != http.StatusOK {
		t.Fatalf("second visit after the first went quiet: %d %v", code, out)
	}

	if n := s.visits.liveExcept(uuid.Nil, time.Now()); n != 1 {
		t.Errorf("live visits = %d, want 1 (the quiet one doesn't count)", n)
	}
}
