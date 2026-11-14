package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

// TestAppointmentRequests is #68: a patient requests with preferred times,
// may cancel while pending; the provider confirms (booking the visit) or
// declines; the patient hears back in their thread and by push.
func TestAppointmentRequests(t *testing.T) {
	s, pool := testBackOffice(t)
	s.cfg.Timezone = "UTC"
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	fp := &fakePusher{result: tunnel.PushSent}
	s.SetPusher(fp)
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	relay := &client{t: t, h: h, relay: true}
	jordan := patientID(t, pool, seed.Patient1Email)
	ada := patientID(t, pool, seed.ProviderEmail)
	_, sess := pairPhone(t, staff, relay, jordan)
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}
	phone.do("PUT", "/devices/current/push", map[string]string{"token": strings.Repeat("ab", 32), "environment": "sandbox"})

	// The seed books a visit ~2 days out; keep this test's slots free.
	if _, err := pool.Exec(context.Background(), `update appointments set status = 'cancelled'`); err != nil {
		t.Fatal(err)
	}
	day := func(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format(time.DateOnly) }
	ask := func(c *client, provider string, pref ...map[string]string) (int, map[string]any) {
		t.Helper()
		return c.do("POST", "/appointment-requests", map[string]any{"provider_id": provider, "preferred": pref, "reason": "Follow-up on blood pressure"})
	}
	morning := map[string]string{"date": day(2), "period": "morning"}

	// Who and what may be asked.
	if code, _ := ask(staff, ada, morning); code != http.StatusForbidden {
		t.Errorf("provider requesting: %d", code)
	}
	for _, bad := range [][]map[string]string{
		{},
		{{"date": day(-1), "period": "morning"}},
		{{"date": day(200), "period": "morning"}},
		{{"date": "next tuesday", "period": "morning"}},
		{{"date": day(1), "period": "midnight"}},
		{morning, morning, morning, morning},
	} {
		if code, out := ask(phone, ada, bad...); code != http.StatusBadRequest {
			t.Errorf("%v: %d %v", bad, code, out)
		}
	}
	if code, _ := ask(phone, uuid.NewString(), morning); code != http.StatusForbidden {
		t.Errorf("unlinked provider: %d", code)
	}

	// Three may wait at once.
	var ids []string
	for i := 0; i < 3; i++ {
		code, out := ask(phone, ada, morning, map[string]string{"date": day(3), "period": "any"})
		if code != http.StatusCreated || out["status"] != "pending" {
			t.Fatalf("request %d: %d %v", i, code, out)
		}
		ids = append(ids, out["id"].(string))
	}
	if code, _ := ask(phone, ada, morning); code != http.StatusConflict {
		t.Errorf("4th pending: %d", code)
	}

	// Only the patient cancels, and only while pending.
	if code, _ := staff.do("POST", "/appointment-requests/"+ids[0]+"/cancel", nil); code != http.StatusForbidden {
		t.Errorf("provider cancel: %d", code)
	}
	if code, out := phone.do("POST", "/appointment-requests/"+ids[0]+"/cancel", nil); code != http.StatusOK || out["status"] != "cancelled" {
		t.Fatalf("cancel: %d %v", code, out)
	}
	if code, _ := phone.do("POST", "/appointment-requests/"+ids[0]+"/cancel", nil); code != http.StatusConflict {
		t.Errorf("cancel twice: %d", code)
	}
	if code, _ := phone.do("POST", "/appointment-requests/"+uuid.NewString()+"/cancel", nil); code != http.StatusNotFound {
		t.Errorf("cancel unknown: %d", code)
	}
	var pending struct {
		Requests []map[string]any `json:"requests"`
	}
	if staff.doInto("GET", "/appointment-requests?status=pending", &pending); len(pending.Requests) != 2 {
		t.Fatalf("provider sees %d pending", len(pending.Requests))
	}

	// Confirm books the visit at the provider's time.
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	when := map[string]string{"starts_at": start.Format(time.RFC3339), "ends_at": start.Add(30 * time.Minute).Format(time.RFC3339)}
	if code, _ := phone.do("POST", "/appointment-requests/"+ids[1]+"/confirm", when); code != http.StatusForbidden {
		t.Errorf("patient confirm: %d", code)
	}
	past := map[string]string{"starts_at": start.Add(-72 * time.Hour).Format(time.RFC3339), "ends_at": start.Add(-71 * time.Hour).Format(time.RFC3339)}
	if code, _ := staff.do("POST", "/appointment-requests/"+ids[1]+"/confirm", past); code != http.StatusBadRequest {
		t.Errorf("confirm in the past: %d", code)
	}
	code, out := staff.do("POST", "/appointment-requests/"+ids[1]+"/confirm", when)
	if code != http.StatusOK || out["status"] != "confirmed" || out["appointment_id"] == nil {
		t.Fatalf("confirm: %d %v", code, out)
	}
	var apptStatus string
	_ = pool.QueryRow(context.Background(), `select status from appointments where id = $1`, out["appointment_id"]).Scan(&apptStatus)
	if apptStatus != "scheduled" {
		t.Errorf("booked appointment is %q", apptStatus)
	}
	if code, _ := staff.do("POST", "/appointment-requests/"+ids[1]+"/decline", map[string]string{}); code != http.StatusConflict {
		t.Errorf("decline after confirm: %d", code)
	}
	// Same slot again: the overlap check refuses, and the request stays pending.
	if code, _ := staff.do("POST", "/appointment-requests/"+ids[2]+"/confirm", when); code != http.StatusConflict {
		t.Errorf("overlapping confirm: %d", code)
	}

	// Decline with a note.
	code, out = staff.do("POST", "/appointment-requests/"+ids[2]+"/decline", map[string]string{"note": "Please call the front desk."})
	if code != http.StatusOK || out["status"] != "declined" || out["note"] != "Please call the front desk." {
		t.Fatalf("decline: %d %v", code, out)
	}

	// The patient sees the outcomes, in the thread and by push.
	var mine struct {
		Requests []map[string]any `json:"requests"`
	}
	phone.doInto("GET", "/appointment-requests", &mine)
	got := map[string]string{}
	for _, r := range mine.Requests {
		got[r["id"].(string)] = r["status"].(string)
	}
	if got[ids[0]] != "cancelled" || got[ids[1]] != "confirmed" || got[ids[2]] != "declined" {
		t.Errorf("patient sees %v", got)
	}
	var bodies []string
	rows, _ := pool.Query(context.Background(), `select body from messages where sender_id = $1 and recipient_id = $2
		and metadata ? 'appointment_request' order by created_at`, ada, jordan)
	for rows.Next() {
		var b string
		_ = rows.Scan(&b)
		bodies = append(bodies, b)
	}
	rows.Close()
	if len(bodies) != 2 || !strings.Contains(bodies[0], "confirmed") || !strings.Contains(bodies[1], "front desk") {
		t.Errorf("thread messages %q", bodies)
	}
	eventually(t, "pushes", func() bool { return fp.count() == 2 })
	for _, p := range fp.sent {
		if p.Category != "appointment_update" {
			t.Errorf("push %s", p.Category)
		}
	}

	// Every step is audited.
	var actions []string
	rows, _ = pool.Query(context.Background(), `select action from audit_events where resource_type = 'appointment_request' order by seq`)
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	want := "appointment_request.create appointment_request.create appointment_request.create appointment_request.cancel appointment_request.confirm appointment_request.decline"
	if strings.Join(actions, " ") != want {
		t.Errorf("audit %v", actions)
	}
}
