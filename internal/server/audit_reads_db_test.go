package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestAuditPHIReads is #40: each read of patient data records exactly one
// event (never one per row) naming the actor, the device and the resource;
// a thread read isn't recorded twice; a refused read records nothing; and
// the chain still verifies.
func TestAuditPHIReads(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	relay := &client{t: t, h: h, relay: true}
	ada, jordan := patientID(t, pool, seed.ProviderEmail), patientID(t, pool, seed.Patient1Email)
	_, sess := pairPhone(t, staff, relay, jordan)
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}
	var phoneDevice string
	_ = pool.QueryRow(ctx, `select id::text from devices where user_id = $1 order by created_at desc limit 1`, jordan).Scan(&phoneDevice)

	type event struct {
		Actor, Resource string
		Detail          map[string]string
	}
	since := func() int64 {
		var n int64
		_ = pool.QueryRow(ctx, `select coalesce(max(seq), 0) from audit_events`).Scan(&n)
		return n
	}
	eventsAfter := func(seq int64, action string) []event {
		rows, err := pool.Query(ctx, `select coalesce(actor_id::text, ''), resource_id, detail from audit_events
			where seq > $1 and action = $2 order by seq`, seq, action)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []event
		for rows.Next() {
			var e event
			var raw []byte
			if err := rows.Scan(&e.Actor, &e.Resource, &raw); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal(raw, &e.Detail)
			out = append(out, e)
		}
		return out
	}
	tenant := demoTenantID(t, pool).String()

	for _, c := range []struct {
		who      *client
		path     string
		action   string
		resource string
		actor    string
		device   bool
	}{
		{staff, "/patients", "patients.list", tenant, ada, false},
		{staff, "/patients/" + jordan + "/devices", "patient_devices.list", jordan, ada, false},
		{staff, "/links", "links.list", tenant, ada, false},
		{staff, "/appointments?from=2026-01-01T00:00:00Z", "appointments.list", tenant, ada, false},
		{phone, "/appointments", "appointments.list", tenant, jordan, true},
		{phone, "/appointment-requests", "appointment_requests.list", tenant, jordan, true},
		{phone, "/messages", "messages.list", tenant, jordan, true},
		{phone, "/conversations", "conversations.list", tenant, jordan, true},
		{phone, "/orders", "orders.list", tenant, jordan, true},
		{staff, "/care-teams", "care_team.list", tenant, ada, false},
	} {
		before := since()
		if code, out := c.who.do("GET", c.path, nil); code != http.StatusOK {
			t.Errorf("GET %s: %d %v", c.path, code, out)
			continue
		}
		evs := eventsAfter(before, c.action)
		if len(evs) != 1 {
			t.Errorf("GET %s: %d %s events, want exactly 1", c.path, len(evs), c.action)
			continue
		}
		e := evs[0]
		if e.Actor != c.actor || e.Resource != c.resource {
			t.Errorf("GET %s: actor %s resource %s, want %s %s", c.path, e.Actor, e.Resource, c.actor, c.resource)
		}
		if c.device && e.Detail["device_id"] != phoneDevice {
			t.Errorf("GET %s: device %q, want %s", c.path, e.Detail["device_id"], phoneDevice)
		}
		if !c.device && e.Detail["via"] != "staff_web" {
			t.Errorf("GET %s: detail %v", c.path, e.Detail)
		}
	}
	// Filters are recorded (IDs and dates only).
	before := since()
	staff.do("GET", "/appointments?from=2026-01-01T00:00:00Z", nil)
	if evs := eventsAfter(before, "appointments.list"); len(evs) != 1 || evs[0].Detail["from"] != "2026-01-01T00:00:00Z" {
		t.Errorf("filters not recorded: %v", evs)
	}

	// A thread read is recorded once, as messages.read_thread.
	before = since()
	if code, _ := phone.do("GET", "/messages?with="+ada, nil); code != http.StatusOK {
		t.Fatalf("thread: %d", code)
	}
	if n, m := len(eventsAfter(before, "messages.read_thread")), len(eventsAfter(before, "messages.list")); n != 1 || m != 0 {
		t.Errorf("thread read recorded %d read_thread and %d list events, want 1 and 0", n, m)
	}

	// A refused read records nothing.
	before = since()
	if code, _ := phone.do("GET", "/patients", nil); code == http.StatusOK {
		t.Fatal("patient listed patients")
	}
	if code, _ := staff.do("GET", "/patients/"+uuid.NewString()+"/devices", nil); code == http.StatusOK {
		t.Fatal("devices of an unknown patient")
	}
	if since() != before {
		t.Error("a refused read was recorded")
	}

	if res, err := audit.Verify(ctx, pool, demoTenantID(t, pool)); err != nil || res.BrokenAt != 0 {
		t.Errorf("chain after reads: %+v %v", res, err)
	}

	// If the event can't be written, the data isn't sent.
	broken, err := pgxpool.New(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	broken.Close()
	gated := (&Server{pool: broken}).auditRead("test.read", "practice", "")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"secret":"patient data"}`))
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxIdentity, Identity{UserID: uuid.New(), TenantID: uuid.New(), Role: "provider"}))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("audit failure: %d %s, want 500 without the data", rec.Code, rec.Body.String())
	}
}
