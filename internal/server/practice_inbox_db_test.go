package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestPracticeInbox is #110: a patient messages the practice; the front
// desk and admins answer on the LAN; providers can't read it; it stays out
// of provider threads; it follows the messages switch.
func TestPracticeInbox(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	ctx := context.Background()
	ada := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	jordan := patientID(t, pool, seed.Patient1Email)
	adaID := patientID(t, pool, seed.ProviderEmail)
	_, sess := pairPhone(t, ada, &client{t: t, h: h, relay: true}, jordan)
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}
	desk := webSignIn(t, h, pool, seed.OfficeEmail, seed.OfficePassword)
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)
	var directBefore int
	_ = pool.QueryRow(ctx, `select count(*) from messages`).Scan(&directBefore)

	// The patient writes to the practice, through the Relay.
	if code, out := phone.do("POST", "/practice-inbox", map[string]string{"body": "Can I move Friday's visit?"}); code != http.StatusCreated || out["from_patient"] != true {
		t.Fatalf("patient writes: %d %v", code, out)
	}
	if code, _ := phone.do("POST", "/practice-inbox", map[string]string{"body": "   "}); code != http.StatusBadRequest {
		t.Errorf("empty body: %d, want 400", code)
	}

	// Providers don't read or answer it.
	if code, _ := ada.do("GET", "/practice-inbox", nil); code != http.StatusForbidden {
		t.Errorf("provider reads the inbox: %d, want 403", code)
	}
	if code, _ := ada.do("POST", "/practice-inbox", map[string]string{"patient_id": jordan, "body": "x"}); code != http.StatusForbidden {
		t.Errorf("provider answers: %d, want 403", code)
	}

	// The front desk sees it, on the LAN only.
	code, out := desk.do("GET", "/practice-inbox", nil)
	convs, _ := out["conversations"].([]any)
	if code != http.StatusOK || len(convs) != 1 || convs[0].(map[string]any)["unread"].(float64) != 1 {
		t.Fatalf("front desk list: %d %v", code, out)
	}
	// Through the Relay a staff cookie isn't accepted at all, and a front-desk
	// bearer token gets a 404 from the inbox.
	if code, _ := (&client{t: t, h: h, relay: true, cookies: desk.cookies, csrf: desk.csrf}).do("GET", "/practice-inbox", nil); code != http.StatusUnauthorized {
		t.Errorf("front desk cookie through the Relay: %d, want 401", code)
	}
	deskToken := login(t, h, pool, seed.OfficeEmail, seed.OfficePassword)
	if code, _ := (&client{t: t, h: h, relay: true, token: deskToken}).do("GET", "/practice-inbox", nil); code != http.StatusNotFound {
		t.Errorf("front desk token through the Relay: %d, want 404", code)
	}
	if code, _ := desk.do("POST", "/practice-inbox", map[string]string{"body": "no patient"}); code != http.StatusBadRequest {
		t.Errorf("reply without patient_id: %d, want 400", code)
	}
	if code, _ := desk.do("POST", "/practice-inbox", map[string]string{"patient_id": adaID, "body": "x"}); code != http.StatusNotFound {
		t.Errorf("reply to a non-patient: %d, want 404", code)
	}
	if code, _ := desk.do("POST", "/practice-inbox", map[string]string{"patient_id": jordan, "body": "Yes, Friday at 2 works."}); code != http.StatusCreated {
		t.Fatalf("front desk replies: %d", code)
	}
	if code, out := desk.do("POST", "/practice-inbox/read", map[string]string{"patient_id": jordan}); code != http.StatusOK || out["marked"].(float64) != 1 {
		t.Errorf("front desk reads: %d %v", code, out)
	}
	if _, out := adm.do("GET", "/practice-inbox", nil); out["conversations"].([]any)[0].(map[string]any)["unread"].(float64) != 0 {
		t.Errorf("admin sees it unread after the front desk read it: %v", out)
	}

	// The patient sees the reply, signed by the person, and only their thread.
	code, out = phone.do("GET", "/practice-inbox", nil)
	msgs, _ := out["messages"].([]any)
	if code != http.StatusOK || len(msgs) != 2 {
		t.Fatalf("patient thread: %d %v", code, out)
	}
	if reply := msgs[1].(map[string]any); reply["from_patient"] != false || reply["author_name"] != seed.OfficeName {
		t.Errorf("reply: %v", reply)
	}
	if code, _ := phone.do("GET", "/practice-inbox/"+adaID, nil); code != http.StatusNotFound {
		t.Errorf("patient reads another thread: %d, want 404", code)
	}
	if code, out := phone.do("POST", "/practice-inbox/read", nil); code != http.StatusOK || out["marked"].(float64) != 1 {
		t.Errorf("patient reads the reply: %d %v", code, out)
	}

	// Separate from provider threads; audited.
	var directAfter int
	_ = pool.QueryRow(ctx, `select count(*) from messages`).Scan(&directAfter)
	if directAfter != directBefore {
		t.Errorf("the inbox wrote %d direct messages", directAfter-directBefore)
	}
	var audited int
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where action = 'practice_message.create'`).Scan(&audited)
	if audited != 2 {
		t.Errorf("%d practice_message.create events, want 2", audited)
	}

	// Off with the messages switch.
	if code, _ := adm.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"messages": false}}); code != http.StatusOK {
		t.Fatalf("switch messages off: %d", code)
	}
	if code, _ := phone.do("POST", "/practice-inbox", map[string]string{"body": "hello?"}); code != http.StatusConflict {
		t.Errorf("inbox with messages off: %d, want 409", code)
	}
}
