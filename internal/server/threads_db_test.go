package server

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestThreadsAndConversations is #67: a patient sees their own sent
// messages in the thread, both sides see names, only linked users are
// reachable, and thread reads are audited.
func TestThreadsAndConversations(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	relay := &client{t: t, h: h, relay: true}
	jordan := patientID(t, pool, seed.Patient1Email)
	ada := patientID(t, pool, seed.ProviderEmail)
	sam := patientID(t, pool, seed.Patient2Email)
	_, sess := pairPhone(t, staff, relay, jordan)
	patient := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}

	// Jordan writes; their own message is in the thread with Dr. Ada.
	if code, _ := patient.do("POST", "/messages", map[string]string{"recipient_id": ada, "kind": "message", "body": "Can I move Tuesday?"}); code != http.StatusCreated {
		t.Fatalf("send: %d", code)
	}
	code, out := patient.do("GET", "/messages?with="+ada, nil)
	msgs, _ := out["messages"].([]any)
	if code != http.StatusOK || len(msgs) == 0 {
		t.Fatalf("thread: %d %v", code, out)
	}
	newest := msgs[0].(map[string]any)
	if newest["body"] != "Can I move Tuesday?" || newest["sender_id"] != jordan {
		t.Errorf("newest in thread: %v", newest)
	}
	var fromAda, fromJordan int
	for _, m := range msgs {
		switch m.(map[string]any)["sender_id"] {
		case ada:
			fromAda++
		case jordan:
			fromJordan++
		}
	}
	if fromAda == 0 || fromJordan == 0 {
		t.Errorf("thread isn't both directions: %d from Ada, %d from Jordan", fromAda, fromJordan)
	}

	// Conversations: names, last message, unread.
	code, out = patient.do("GET", "/conversations", nil)
	convs, _ := out["conversations"].([]any)
	if code != http.StatusOK || len(convs) != 1 {
		t.Fatalf("patient conversations: %d %v", code, out)
	}
	c := convs[0].(map[string]any)
	if c["name"] != seed.ProviderName || c["last_body"] != "Can I move Tuesday?" || c["last_from_me"] != true {
		t.Errorf("patient conversation: %v", c)
	}
	code, out = staff.do("GET", "/conversations", nil)
	convs, _ = out["conversations"].([]any)
	if code != http.StatusOK || len(convs) < 1 {
		t.Fatalf("provider conversations: %d %v", code, out)
	}
	first := convs[0].(map[string]any)
	if first["name"] != seed.Patient1Name || first["unread_count"].(float64) < 1 {
		t.Errorf("provider's newest conversation: %v", first)
	}

	// Only linked users: Jordan can't read a thread with Sam.
	if code, _ := patient.do("GET", "/messages?with="+sam, nil); code != http.StatusNotFound {
		t.Errorf("thread with an unlinked user: %d", code)
	}
	if code, _ := patient.do("GET", "/messages?with=nope", nil); code != http.StatusBadRequest {
		t.Errorf("bad with: %d", code)
	}

	// Pagination over the thread.
	for i := 0; i < 5; i++ {
		patient.do("POST", "/messages", map[string]string{"recipient_id": ada, "kind": "message", "body": fmt.Sprint("note ", i)})
	}
	code, out = patient.do("GET", "/messages?with="+ada+"&limit=3", nil)
	if code != http.StatusOK || len(out["messages"].([]any)) != 3 || out["next_cursor"] == nil {
		t.Fatalf("page 1: %d %v", code, out)
	}
	code, out = patient.do("GET", "/messages?with="+ada+"&limit=3&cursor="+out["next_cursor"].(string), nil)
	if code != http.StatusOK || len(out["messages"].([]any)) != 3 {
		t.Errorf("page 2: %d %v", code, out)
	}

	var audited int
	_ = pool.QueryRow(t.Context(), `select count(*) from audit_events where action = 'messages.read_thread'`).Scan(&audited)
	if audited < 3 {
		t.Errorf("%d thread reads audited", audited)
	}
}
