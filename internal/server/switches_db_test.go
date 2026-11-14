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
)

// TestPracticeSwitches is #109: an admin turns features off and the server
// refuses them, for old apps too; reminders stop; staff keep the catalogue.
func TestPracticeSwitches(t *testing.T) {
	s, pool := testBackOffice(t)
	s.cfg.ReminderOffsets = "1h"
	s.cfg.Timezone = "UTC"
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	ctx := context.Background()
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	jordan := patientID(t, pool, seed.Patient1Email)
	ada := patientID(t, pool, seed.ProviderEmail)
	_, sess := pairPhone(t, staff, &client{t: t, h: h, relay: true}, jordan)
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)
	adaWeb := webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)

	set := func(features map[string]bool) map[string]any {
		t.Helper()
		code, out := adm.do("PUT", "/practice/features", map[string]any{"features": features})
		if code != http.StatusOK {
			t.Fatalf("set %v: %d %v", features, code, out)
		}
		return out["features"].(map[string]any)
	}
	msg := func(c *client, to string) int {
		code, _ := c.do("POST", "/messages", map[string]string{"recipient_id": to, "body": "hello"})
		return code
	}

	if code := msg(phone, ada); code != http.StatusCreated {
		t.Fatalf("message with messages on: %d", code)
	}
	if code, _ := adaWeb.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"store": false}}); code != http.StatusForbidden {
		t.Errorf("provider sets switches: %d, want 403", code)
	}
	if code, _ := adm.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"teleport": false}}); code != http.StatusBadRequest {
		t.Errorf("unknown switch: %d, want 400", code)
	}

	// Messages off: nobody sends, everyone still reads.
	if on := set(map[string]bool{"messages": false}); on["messages"] != false || on["store"] != true {
		t.Fatalf("features after messages off: %v", on)
	}
	if code := msg(phone, ada); code != http.StatusConflict {
		t.Errorf("patient message with messages off: %d, want 409", code)
	}
	if code := msg(staff, jordan); code != http.StatusConflict {
		t.Errorf("provider message with messages off: %d, want 409", code)
	}
	if code, _ := phone.do("GET", "/messages", nil); code != http.StatusOK {
		t.Errorf("reading messages with messages off: %d", code)
	}
	set(map[string]bool{"messages": true})

	// Requests, store, library and website.
	set(map[string]bool{"requests": false, "store": false, "library": false, "website": false})
	if code, _ := phone.do("POST", "/appointment-requests", map[string]any{"provider_id": ada,
		"preferred": []map[string]string{{"date": time.Now().AddDate(0, 0, 3).Format("2006-01-02"), "period": "morning"}}}); code != http.StatusConflict {
		t.Errorf("request with requests off: %d, want 409", code)
	}
	if code, _ := phone.do("GET", "/store/products", nil); code != http.StatusConflict {
		t.Errorf("patient catalogue with store off: %d, want 409", code)
	}
	if code, _ := phone.do("POST", "/orders", map[string]any{"items": []map[string]any{}}); code != http.StatusConflict {
		t.Errorf("order with store off: %d, want 409", code)
	}
	if code, _ := adm.do("GET", "/store/products", nil); code != http.StatusOK {
		t.Errorf("staff catalogue with store off: %d", code)
	}
	if code, _ := phone.do("GET", "/library", nil); code != http.StatusConflict {
		t.Errorf("library with library off: %d, want 409", code)
	}
	if code, _ := adm.do("POST", "/site/publish", nil); code != http.StatusConflict {
		t.Errorf("publish with website off: %d, want 409", code)
	}
	var tenant uuid.UUID
	_ = pool.QueryRow(ctx, `select id from tenants where name = $1`, seed.DemoTenant).Scan(&tenant)
	if text, names := bundleText(t, s, tenant); strings.Contains(strings.Join(names, " "), "store.html") ||
		strings.Contains(strings.Join(names, " "), "library.html") || strings.Contains(text, `href="store.html"`) {
		t.Errorf("website still has the store or library: %v", names)
	}
	if n := auditCount(t, pool, "practice.features", tenant.String()); n != 3 {
		t.Errorf("%d practice.features events, want 3", n)
	}

	// Check-ins: off for a salon, whatever the override.
	if code, out := adm.do("PUT", "/practice", map[string]string{"type": "salon"}); code != http.StatusOK || out["features"].(map[string]any)["checkins"] != false {
		t.Fatalf("salon: %d %v", code, out)
	}
	if code, _ := staff.do("PUT", "/links/"+jordan+"/checkin", map[string]any{"every_days": 7}); code != http.StatusConflict {
		t.Errorf("check-in cadence for a salon: %d, want 409", code)
	}
	adm.do("PUT", "/practice", map[string]string{"type": "naturopath"})

	// Reminders off: the worker skips them.
	var start time.Time
	if err := pool.QueryRow(ctx, `select starts_at from appointments where provider_id = $1 and status = 'scheduled' order by starts_at limit 1`, ada).Scan(&start); err != nil {
		t.Fatal(err)
	}
	set(map[string]bool{"reminders": false})
	if n, err := s.sendDueReminders(ctx, start.Add(-30*time.Minute)); err != nil || n != 0 {
		t.Errorf("reminders off sent %d (%v)", n, err)
	}
	set(map[string]bool{"reminders": true})
	if n, err := s.sendDueReminders(ctx, start.Add(-30*time.Minute)); err != nil || n != 1 {
		t.Errorf("reminders on sent %d (%v), want 1", n, err)
	}
}

// TestProviderMessageModes is #109: a provider turns messages off, or takes
// them in office hours with an automatic reply at most every 12 hours.
func TestProviderMessageModes(t *testing.T) {
	s, pool := testBackOffice(t)
	s.cfg.Timezone = "UTC"
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	jordan := patientID(t, pool, seed.Patient1Email)
	ada := patientID(t, pool, seed.ProviderEmail)
	_, sess := pairPhone(t, staff, &client{t: t, h: h, relay: true}, jordan)
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}
	adaWeb := webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	mode := func(body map[string]any) int {
		code, _ := adaWeb.do("PUT", "/staff/profile/messages", body)
		return code
	}
	send := func() (int, map[string]any) {
		return phone.do("POST", "/messages", map[string]string{"recipient_id": ada, "body": "Question about my plan"})
	}
	autoReplies := func() int {
		var n int
		_ = pool.QueryRow(context.Background(), `select count(*) from messages where sender_id = $1 and recipient_id = $2
			and metadata->>'auto_reply' = 'true'`, ada, jordan).Scan(&n)
		return n
	}

	if code, out := adaWeb.do("GET", "/staff/profile/messages", nil); code != http.StatusOK || out["mode"] != "on" {
		t.Fatalf("default mode: %d %v", code, out)
	}
	for _, bad := range []map[string]any{
		{"mode": "maybe", "days": []int{1}, "start_minute": 540, "end_minute": 1020},
		{"mode": "hours", "days": []int{}, "start_minute": 540, "end_minute": 1020},
		{"mode": "hours", "days": []int{1, 1}, "start_minute": 540, "end_minute": 1020},
		{"mode": "hours", "days": []int{1}, "start_minute": 1020, "end_minute": 540},
	} {
		if code := mode(bad); code != http.StatusBadRequest {
			t.Errorf("bad settings %v: %d, want 400", bad, code)
		}
	}
	if code, _ := (&client{t: t, h: h, relay: true, cookies: adaWeb.cookies, csrf: adaWeb.csrf}).do("PUT", "/staff/profile/messages",
		map[string]any{"mode": "off"}); code != http.StatusNotFound {
		t.Errorf("settings through the Relay: %d, want 404", code)
	}

	// Off: the patient is refused; Ada can still write.
	if code := mode(map[string]any{"mode": "off", "days": []int{1, 2, 3, 4, 5}, "start_minute": 540, "end_minute": 1020}); code != http.StatusOK {
		t.Fatalf("set off: %d", code)
	}
	if code, out := send(); code != http.StatusConflict || !strings.Contains(out["error"].(string), seed.ProviderName) {
		t.Errorf("message to a provider with messages off: %d %v", code, out)
	}
	if code, _ := staff.do("POST", "/messages", map[string]string{"recipient_id": jordan, "body": "Checking in"}); code != http.StatusCreated {
		t.Errorf("provider writes with messages off: %d", code)
	}

	// Office hours on a day that isn't today: an automatic reply, once.
	tomorrow := int(time.Now().UTC().Add(24*time.Hour).Weekday())
	if code := mode(map[string]any{"mode": "hours", "days": []int{tomorrow}, "start_minute": 540, "end_minute": 1020, "auto_reply": ""}); code != http.StatusOK {
		t.Fatalf("set hours: %d", code)
	}
	if code, _ := send(); code != http.StatusCreated {
		t.Fatalf("message outside hours: %d", code)
	}
	if n := autoReplies(); n != 1 {
		t.Errorf("%d automatic replies, want 1", n)
	}
	var body string
	_ = pool.QueryRow(context.Background(), `select body from messages where metadata->>'auto_reply' = 'true'`).Scan(&body)
	if !strings.Contains(body, seed.ProviderName) || !strings.Contains(body, "9:00 to 17:00") {
		t.Errorf("automatic reply text: %q", body)
	}
	if code, _ := send(); code != http.StatusCreated || autoReplies() != 1 {
		t.Errorf("second message: %d, %d automatic replies, want 1", code, autoReplies())
	}
	if n := auditCount(t, pool, "staff.message_settings", ada); n != 2 {
		t.Errorf("%d staff.message_settings events, want 2", n)
	}

	// Every day, all day: in hours, no reply.
	if code := mode(map[string]any{"mode": "hours", "days": []int{0, 1, 2, 3, 4, 5, 6}, "start_minute": 0, "end_minute": 1440}); code != http.StatusOK {
		t.Fatalf("set all hours: %d", code)
	}
	if _, err := pool.Exec(context.Background(), `delete from messages where metadata->>'auto_reply' = 'true'`); err != nil {
		t.Fatal(err)
	}
	if code, _ := send(); code != http.StatusCreated || autoReplies() != 0 {
		t.Errorf("in hours: %d, %d automatic replies, want 0", code, autoReplies())
	}
}
