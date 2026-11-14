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

// TestReminders is #19: appointment reminders at each offset, once; check-in
// reminders on the provider's cadence; nothing twice across ticks.
func TestReminders(t *testing.T) {
	s, pool := testBackOffice(t)
	s.cfg.ReminderOffsets = "1h, 24h"
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

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	// The seed books a visit ~2 days out; keep this test's count to its own.
	if _, err := pool.Exec(ctx, `update appointments set status = 'cancelled'`); err != nil {
		t.Fatal(err)
	}
	book := func(start time.Time) string {
		t.Helper()
		code, out := staff.do("POST", "/appointments", map[string]string{
			"patient_id": jordan, "starts_at": start.Format(time.RFC3339), "ends_at": start.Add(30 * time.Minute).Format(time.RFC3339)})
		if code != http.StatusCreated {
			t.Fatalf("book: %d %v", code, out)
		}
		return out["id"].(string)
	}
	tick := func(at time.Time, want int) {
		t.Helper()
		n, err := s.sendDueReminders(ctx, at)
		if err != nil || n != want {
			t.Fatalf("tick at %s: sent %d (%v), want %d", at.Sub(now).Round(time.Minute), n, err, want)
		}
	}
	reminders := func() int {
		var n int
		_ = pool.QueryRow(ctx, `select count(*) from messages where kind = 'reminder'`).Scan(&n)
		return n
	}

	// In 30 minutes: the 1h reminder only. In 20 hours: the 24h one.
	// Cancelled: none.
	book(now.Add(30 * time.Minute))
	later := book(now.Add(20 * time.Hour))
	cancelled := book(now.Add(3 * time.Hour))
	staff.do("PATCH", "/appointments/"+cancelled, map[string]string{"status": "cancelled"})
	tick(now, 2)
	tick(now, 0) // restart or next tick: no duplicates
	tick(now.Add(time.Minute), 0)
	if reminders() != 2 {
		t.Fatalf("%d reminders", reminders())
	}
	var body, meta string
	_ = pool.QueryRow(ctx, `select body, metadata::text from messages where kind = 'reminder' and recipient_id = $1 and sender_id = $2
		order by created_at limit 1`, jordan, ada).Scan(&body, &meta)
	if !strings.Contains(body, seed.ProviderName) || !strings.Contains(meta, `"reminder": "appointment"`) {
		t.Errorf("reminder %q %s", body, meta)
	}
	eventually(t, "pushes", func() bool { return fp.count() == 2 })
	for _, p := range fp.sent {
		if p.Category != "appointment_reminder" {
			t.Errorf("push category %s", p.Category)
		}
	}

	// Rescheduled: reminders follow the new time.
	staff.do("PATCH", "/appointments/"+later, map[string]string{
		"starts_at": now.Add(40 * time.Hour).Format(time.RFC3339), "ends_at": now.Add(41 * time.Hour).Format(time.RFC3339)})
	tick(now.Add(2*time.Hour), 0)
	tick(now.Add(17*time.Hour), 1) // 23h before the new time
	tick(now.Add(39*time.Hour+30*time.Minute), 1)
	tick(now.Add(39*time.Hour+45*time.Minute), 0)

	// Check-in cadence: providers only, 1-90 days.
	if code, _ := phone.do("PUT", "/links/"+jordan+"/checkin", map[string]any{"every_days": 7}); code != http.StatusForbidden {
		t.Errorf("patient set cadence: %d", code)
	}
	if code, _ := staff.do("PUT", "/links/"+jordan+"/checkin", map[string]any{"every_days": 0}); code != http.StatusBadRequest {
		t.Errorf("0 days: %d", code)
	}
	if code, _ := staff.do("PUT", "/links/"+uuid.NewString()+"/checkin", map[string]any{"every_days": 7}); code != http.StatusNotFound {
		t.Errorf("unlinked: %d", code)
	}
	if code, _ := staff.do("PUT", "/links/"+jordan+"/checkin", map[string]any{"every_days": 7}); code != http.StatusOK {
		t.Fatalf("set cadence: %d", code)
	}
	var since time.Time
	_ = pool.QueryRow(ctx, `select checkin_since from provider_patients where provider_id = $1 and patient_id = $2`, ada, jordan).Scan(&since)
	since = since.UTC() // office time in this test
	day := func(at time.Time) time.Time { // the next daytime hour, office time
		for at.Hour() < checkinDayStart || at.Hour() >= checkinDayEnd {
			at = at.Add(time.Hour)
		}
		return at
	}
	week := 7 * 24 * time.Hour
	tick(day(since.Add(6*24*time.Hour)), 0)
	night := since.Add(week) // due, but wait for the day, office time
	for night.Hour() >= checkinDayStart && night.Hour() < checkinDayEnd {
		night = night.Add(time.Hour)
	}
	tick(night, 0)
	tick(day(since.Add(week)), 1)
	tick(day(since.Add(week)).Add(time.Hour), 0)

	// Jordan checked in during the last period: no reminder.
	if _, err := pool.Exec(ctx, `insert into messages (tenant_id, sender_id, recipient_id, kind, body, created_at)
		select tenant_id, $1, $2, 'checkin', 'feeling good', $3 from users where id = $1`,
		jordan, ada, since.Add(2*week-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	tick(day(since.Add(2*week)), 0)
	tick(day(since.Add(3*week)), 1)

	// Off again; the link shows the cadence while it's on.
	staff.do("PUT", "/links/"+jordan+"/checkin", map[string]any{"every_days": nil})
	tick(day(since.Add(5*week)), 0)
	var every *int
	_ = pool.QueryRow(ctx, `select checkin_every_days from provider_patients where provider_id = $1 and patient_id = $2`, ada, jordan).Scan(&every)
	if every != nil {
		t.Errorf("cadence still %d", *every)
	}

	// Every reminder is audited as a system action.
	var audited int
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where action = 'reminder.create' and actor_id is null`).Scan(&audited)
	if audited != reminders() {
		t.Errorf("%d audit events for %d reminders", audited, reminders())
	}
}
