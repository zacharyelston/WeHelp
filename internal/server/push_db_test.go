package server

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

type fakePusher struct {
	mu     sync.Mutex
	sent   []tunnel.PushRequest
	result string
}

func (f *fakePusher) Push(_ context.Context, r tunnel.PushRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, r)
	return f.result, nil
}

func (f *fakePusher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

const tokA = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

// TestPushOnNewMessage is #7 on the Back Office side: devices register
// tokens; a message pushes a fixed category to the recipient's devices;
// tokens Apple rejects and revoked devices are cleared.
func TestPushOnNewMessage(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	fp := &fakePusher{result: tunnel.PushSent}
	s.SetPusher(fp)
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	relay := &client{t: t, h: h, relay: true}
	jordan := patientID(t, pool, seed.Patient1Email)
	_, sess := pairPhone(t, staff, relay, jordan)
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}

	// Only a paired device registers a token, and it must look like one.
	if code, _ := staff.do("PUT", "/devices/current/push", map[string]string{"token": tokA, "environment": "sandbox"}); code != http.StatusForbidden {
		t.Errorf("staff browser token: %d", code)
	}
	if code, _ := phone.do("PUT", "/devices/current/push", map[string]string{"token": "nope", "environment": "sandbox"}); code != http.StatusBadRequest {
		t.Errorf("bad token: %d", code)
	}
	if code, _ := phone.do("PUT", "/devices/current/push", map[string]string{"token": tokA, "environment": "sandbox"}); code != http.StatusNoContent {
		t.Fatalf("register: %d", code)
	}

	// A message to Jordan pushes "new_message" to that device: no text, no IDs.
	staff.do("POST", "/messages", map[string]string{"recipient_id": jordan, "kind": "message", "body": "Lab results are in"})
	eventually(t, "push", func() bool { return fp.count() == 1 })
	got := fp.sent[0]
	if got.Token != tokA || got.Category != "new_message" || got.Environment != "sandbox" {
		t.Errorf("push %+v", got)
	}

	// Apple says the token is gone: it's cleared, and the next message skips it.
	fp.result = tunnel.PushInvalidToken
	staff.do("POST", "/messages", map[string]string{"recipient_id": jordan, "kind": "message", "body": "again"})
	eventually(t, "second push", func() bool { return fp.count() == 2 })
	eventually(t, "token cleared", func() bool {
		var tok *string
		_ = pool.QueryRow(t.Context(), `select push_token from devices where id = $1`, sess["device_id"]).Scan(&tok)
		return tok == nil
	})
	staff.do("POST", "/messages", map[string]string{"recipient_id": jordan, "kind": "message", "body": "third"})
	time.Sleep(200 * time.Millisecond)
	if fp.count() != 2 {
		t.Errorf("pushed to a cleared token")
	}

	// Revoking a device clears its token too.
	fp.result = tunnel.PushSent
	phone.do("PUT", "/devices/current/push", map[string]string{"token": tokA, "environment": "sandbox"})
	staff.do("DELETE", "/patients/"+jordan+"/devices/"+sess["device_id"].(string), nil)
	var tok *string
	_ = pool.QueryRow(t.Context(), `select push_token from devices where id = $1`, sess["device_id"]).Scan(&tok)
	if tok != nil {
		t.Error("revoked device kept its push token")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
