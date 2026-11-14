package server

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/turncred"
)

// lockedBuffer collects log output from handlers on other goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestNoPHIInLogs is #42: a run through seeding, pairing, messages,
// appointments, requests, the practice inbox, orders and a video visit,
// including refused and failing requests, logs IDs only. It fails if any
// patient or staff name, email, message or note text, SDP, token or code
// appears in the log output.
func TestNoPHIInLogs(t *testing.T) {
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s, pool := testBackOffice(t) // seeds the demo practice
	h := s.Router()
	ctx := context.Background()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	s.SetTURNKeys(&fakeTURNKeys{key: &turncred.Key{KeyID: "k1", Secret: "turn-secret-xyz", URLs: []string{"turn:192.0.2.1:3478"}}})
	visitPollWait = 100 * time.Millisecond
	t.Cleanup(func() { visitPollWait = 15 * time.Second })

	const (
		msgText   = "PHI-MARKER my knee still hurts after the fall"
		reason    = "PHI-MARKER-REASON rash on left arm"
		note      = "PHI-MARKER-NOTE leave at back door"
		inboxText = "PHI-MARKER-INBOX please call about results"
		sdp       = "v=0 PHI-MARKER-SDP o=- 1 2 IN IP4 198.51.100.7"
	)
	token := login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	staff := &client{t: t, h: h, token: token}
	relay := &client{t: t, h: h, relay: true}
	ada, jordan := patientID(t, pool, seed.ProviderEmail), patientID(t, pool, seed.Patient1Email)

	_, inv := staff.do("POST", "/pairing/invites", map[string]string{"patient_id": jordan})
	code := inviteCode(t, inv)
	_, sess := newPhone(t).redeem(relay, code)
	phoneToken := sess["access_token"].(string)
	phone := &client{t: t, h: h, token: phoneToken, relay: true}

	phone.do("POST", "/messages", map[string]string{"recipient_id": ada, "kind": "message", "body": msgText})
	staff.do("GET", "/messages?with="+jordan, nil)
	staff.do("GET", "/conversations", nil)
	phone.do("POST", "/appointment-requests", map[string]any{"provider_id": ada, "reason": reason,
		"preferred": []map[string]string{{"date": time.Now().Add(48 * time.Hour).Format("2006-01-02"), "period": "morning"}}})
	phone.do("POST", "/orders", map[string]any{"items": []any{}, "note": note})
	phone.do("POST", "/practice-inbox", map[string]string{"body": inboxText})
	frontDesk := webSignIn(t, h, pool, seed.OfficeEmail, seed.OfficePassword)
	frontDesk.do("GET", "/practice-inbox", nil)
	frontDesk.do("POST", "/practice-inbox", map[string]any{"patient_id": jordan, "body": inboxText})

	// A video visit with SDP in the signals.
	ipad := providerDevice(t, staff, relay, ada)
	starts := time.Now().Add(5 * time.Minute).Truncate(time.Second)
	_, appt := staff.do("POST", "/appointments", map[string]string{"patient_id": jordan,
		"starts_at": starts.Format(time.RFC3339), "ends_at": starts.Add(30 * time.Minute).Format(time.RFC3339)})
	if id, ok := appt["id"].(string); ok {
		_, v := ipad.do("POST", "/appointments/"+id+"/visit/start", nil)
		if vid, ok := v["visit_id"].(string); ok {
			phone.do("POST", "/appointments/"+id+"/visit/join", nil)
			ipad.do("POST", "/visits/"+vid+"/signals", map[string]string{"type": "offer", "sdp": sdp})
			phone.do("GET", "/visits/"+vid+"/signals?after=0", nil)
		}
	}

	// Refusals and failures log too: a bad login, a forbidden route, junk.
	relay.do("POST", "/auth/login", map[string]string{"tenant": seed.DemoTenant, "email": seed.Patient2Email, "password": "wrong " + seed.Patient2Name})
	phone.do("GET", "/patients", nil)
	phone.do("POST", "/messages", map[string]string{"recipient_id": "not-a-uuid", "kind": "message", "body": msgText})
	_ = ctx

	out := logs.String()
	if !strings.Contains(out, "request") {
		t.Fatal("no log output captured; the guard would pass vacuously")
	}
	for _, phi := range []string{
		seed.Patient1Name, seed.Patient2Name, seed.ProviderName,
		seed.Patient1Email, seed.Patient2Email, seed.ProviderEmail, seed.AdminEmail, seed.OfficeEmail,
		"PHI-MARKER", "198.51.100.7",
		token, phoneToken, sess["refresh_token"].(string), code,
		"turn-secret-xyz",
	} {
		if phi != "" && strings.Contains(out, phi) {
			i := strings.Index(out, phi)
			lo, hi := max(0, i-120), min(len(out), i+len(phi)+40)
			t.Errorf("log output contains %q: …%s…", phi, out[lo:hi])
		}
	}
}
