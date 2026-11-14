package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestCareTeams is #111: the patient approves who joins; members share one
// thread; history only if allowed; removal takes the past too; reads are
// audited and visible to the patient; the front desk and outsiders see
// nothing; disabling a provider removes them; the switch turns it off.
func TestCareTeams(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	ctx := context.Background()
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)
	adaTok := login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	ada := &client{t: t, h: h, token: adaTok}
	adaID := patientID(t, pool, seed.ProviderEmail)
	jordan := patientID(t, pool, seed.Patient1Email)
	_, sess := pairPhone(t, ada, &client{t: t, h: h, relay: true}, jordan)
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}

	// Two more providers, with Ada's password for the test.
	provider := func(name, email string) (*client, string) {
		t.Helper()
		code, out := adm.do("POST", "/staff/providers", map[string]string{"display_name": name, "email": email})
		if code != http.StatusCreated {
			t.Fatalf("add %s: %d %v", name, code, out)
		}
		id := out["provider"].(map[string]any)["id"].(string)
		if _, err := pool.Exec(ctx, `update users set password_hash = (select password_hash from users where email = $1) where id = $2`,
			seed.ProviderEmail, id); err != nil {
			t.Fatal(err)
		}
		return &client{t: t, h: h, token: login(t, h, pool, email, seed.ProviderPassword)}, id
	}
	kim, kimID := provider("Dr. Kim", "kim@demo.wehelp")
	lee, leeID := provider("Dr. Lee", "lee@demo.wehelp")
	desk := &client{t: t, h: h, token: login(t, h, pool, seed.OfficeEmail, seed.OfficePassword)}
	team := "/care-teams/" + jordan
	var directBefore int
	_ = pool.QueryRow(ctx, `select count(*) from messages`).Scan(&directBefore)

	// Ada is linked, so she sees Jordan as a team she isn't on yet.
	code, out := ada.do("GET", "/care-teams", nil)
	if code != http.StatusOK || len(out["teams"].([]any)) != 1 || out["teams"].([]any)[0].(map[string]any)["my_status"] != "none" {
		t.Fatalf("Ada's teams: %d %v", code, out)
	}

	// The patient adds Ada directly; Ada proposes Kim, who waits.
	if code, out := phone.do("POST", team+"/members", map[string]any{"member_id": adaID}); code != http.StatusCreated {
		t.Fatalf("patient adds Ada: %d %v", code, out)
	}
	if code, _ := ada.do("POST", team+"/members", map[string]any{"member_id": kimID}); code != http.StatusCreated {
		t.Fatalf("Ada proposes Kim: %d", code)
	}
	if code, _ := ada.do("POST", team+"/members", map[string]any{"member_id": kimID}); code != http.StatusConflict {
		t.Errorf("propose Kim twice: %d, want 409", code)
	}
	deskID := patientID(t, pool, seed.OfficeEmail)
	if code, _ := ada.do("POST", team+"/members", map[string]any{"member_id": deskID}); code != http.StatusBadRequest {
		t.Errorf("propose the front desk: %d, want 400", code)
	}
	if code, _ := kim.do("GET", team+"/messages", nil); code != http.StatusNotFound {
		t.Errorf("pending member reads: %d, want 404", code)
	}

	// Messages before Kim joins.
	for _, c := range []*client{phone, ada} {
		if code, _ := c.do("POST", team+"/messages", map[string]string{"body": "before Kim"}); code != http.StatusCreated {
			t.Fatalf("post: %d", code)
		}
	}
	// Only the patient approves.
	if code, _ := ada.do("POST", team+"/members/"+kimID+"/approve", nil); code != http.StatusNotFound {
		t.Errorf("a provider approves: %d, want 404", code)
	}
	if code, out := phone.do("POST", team+"/members/"+kimID+"/approve", nil); code != http.StatusOK {
		t.Fatalf("patient approves Kim: %d %v", code, out)
	}
	count := func(c *client) int {
		t.Helper()
		code, out := c.do("GET", team+"/messages", nil)
		if code != http.StatusOK {
			return -code
		}
		return len(out["messages"].([]any))
	}
	if n := count(kim); n != 0 {
		t.Errorf("Kim sees %d messages from before she joined, want 0", n)
	}
	if code, _ := kim.do("POST", team+"/messages", map[string]string{"body": "Kim here"}); code != http.StatusCreated {
		t.Fatalf("Kim posts: %d", code)
	}
	if n := count(kim); n != 1 {
		t.Errorf("Kim sees %d, want 1", n)
	}
	if n := count(ada); n != 3 {
		t.Errorf("Ada sees %d, want 3", n)
	}
	if n := count(phone); n != 3 {
		t.Errorf("patient sees %d, want 3", n)
	}

	// Outsiders: an unlinked provider, the front desk.
	if code, _ := lee.do("GET", team, nil); code != http.StatusNotFound {
		t.Errorf("outsider reads the team: %d, want 404", code)
	}
	if code, _ := lee.do("POST", team+"/messages", map[string]string{"body": "x"}); code != http.StatusNotFound {
		t.Errorf("outsider posts: %d, want 404", code)
	}
	if code, _ := desk.do("GET", "/care-teams", nil); code != http.StatusForbidden {
		t.Errorf("front desk lists teams: %d, want 403", code)
	}
	if code, _ := desk.do("GET", team+"/messages", nil); code != http.StatusNotFound {
		t.Errorf("front desk reads: %d, want 404", code)
	}

	// The patient sees who read the thread.
	code, out = phone.do("GET", "/care-teams", nil)
	readers := map[string]bool{}
	for _, r := range out["reads"].([]any) {
		readers[r.(map[string]any)["name"].(string)] = true
	}
	if code != http.StatusOK || !readers["Dr. Kim"] || !readers[seed.ProviderName] {
		t.Errorf("patient's read log: %d %v", code, out["reads"])
	}

	// Removal: only the patient removes others, and the past goes too.
	if code, _ := ada.do("DELETE", team+"/members/"+kimID, nil); code != http.StatusForbidden {
		t.Errorf("Ada removes Kim: %d, want 403", code)
	}
	if code, _ := phone.do("DELETE", team+"/members/"+kimID, nil); code != http.StatusOK {
		t.Fatalf("patient removes Kim: %d", code)
	}
	if n := count(kim); n != -http.StatusNotFound {
		t.Errorf("removed member reads: %d, want 404", n)
	}

	// History when the patient allows it.
	if code, _ := phone.do("POST", team+"/members", map[string]any{"member_id": leeID, "include_history": true}); code != http.StatusCreated {
		t.Fatalf("patient adds Lee with history: %d", code)
	}
	if n := count(lee); n != 3 {
		t.Errorf("Lee with history sees %d, want 3", n)
	}

	// Disabling Ada removes her from the team.
	if code, _ := adm.do("POST", "/staff/providers/"+adaID+"/disable", nil); code != http.StatusOK {
		t.Fatalf("disable Ada: %d", code)
	}
	var adaStatus string
	_ = pool.QueryRow(ctx, `select status from care_team_members where member_id = $1 order by created_at desc limit 1`, adaID).Scan(&adaStatus)
	if adaStatus != "removed" {
		t.Errorf("disabled provider's membership: %s, want removed", adaStatus)
	}

	// Audit, and nothing in direct threads.
	var events int
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where action in ('care_team.add', 'care_team.propose', 'care_team.approve', 'care_team.remove')`).Scan(&events)
	if events != 5 {
		t.Errorf("%d membership audit events, want 5", events)
	}
	var directAfter int
	_ = pool.QueryRow(ctx, `select count(*) from messages`).Scan(&directAfter)
	if directAfter != directBefore {
		t.Errorf("care teams wrote %d direct messages", directAfter-directBefore)
	}

	// The switch: therapy starts with care teams off.
	if code, out := adm.do("PUT", "/practice", map[string]string{"type": "therapy"}); code != http.StatusOK || out["features"].(map[string]any)["careteam"] != false {
		t.Fatalf("therapy: %d %v", code, out)
	}
	if code, _ := phone.do("GET", team+"/messages", nil); code != http.StatusConflict {
		t.Errorf("care teams off: %d, want 409", code)
	}
}
