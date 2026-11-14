package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/roster"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// fakeRoster stands in for the Registry.
type fakeRoster struct {
	r   roster.Roster
	err error
}

func (f *fakeRoster) Fetch(context.Context) (roster.Roster, error) { return f.r, f.err }

func rosterEntry(out map[string]any, email string) map[string]any {
	for _, e := range out["entries"].([]any) {
		if m := e.(map[string]any); m["email"] == email {
			return m
		}
	}
	return nil
}

// TestRosterSync is #62's Back Office half: the roster from the Registry
// shows new providers waiting for an invite, the invite is the same set-up
// link as Add staff, and a provider removed in the Registry is flagged for
// the admin to disable, never disabled or deleted by the sync.
func TestRosterSync(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)
	ada := webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)

	// Before install there is no source.
	if code, _ := adm.do("POST", "/staff/roster/sync", nil); code != http.StatusConflict {
		t.Errorf("sync before install: %d, want 409", code)
	}
	src := &fakeRoster{}
	s.SetRosterSource(src)
	now := time.Now()
	src.r = roster.Roster{IssuedAt: now, Providers: []roster.Provider{
		{ID: "11111111-1111-1111-1111-111111111111", Name: "Dr. Sam Lee", Email: "sam.lee@demo.wehelp", Role: "provider", Credentials: "ND"},
		{ID: "22222222-2222-2222-2222-222222222222", Name: "Ada", Email: seed.ProviderEmail, Role: "provider"},
		{ID: "33333333-3333-3333-3333-333333333333", Name: "Office Manager", Email: "manager@demo.wehelp", Role: "admin"},
	}}

	// Only admins, and only on the LAN.
	if code, _ := ada.do("GET", "/staff/roster", nil); code != http.StatusForbidden {
		t.Errorf("provider reads roster: %d, want 403", code)
	}
	relay := &client{t: t, h: h, relay: true, cookies: adm.cookies, csrf: adm.csrf}
	for _, p := range []string{"/staff/roster", "/staff/roster/sync", "/staff/roster/11111111-1111-1111-1111-111111111111/invite"} {
		method := "POST"
		if p == "/staff/roster" {
			method = "GET"
		}
		if code, _ := relay.do(method, p, nil); code != http.StatusNotFound {
			t.Errorf("%s through the Relay: %d, want 404", p, code)
		}
	}

	code, out := adm.do("POST", "/staff/roster/sync", nil)
	if code != http.StatusOK {
		t.Fatalf("sync: %d %v", code, out)
	}
	if res := out["result"].(map[string]any); res["added"] != float64(3) {
		t.Errorf("result %v", res)
	}
	sam := rosterEntry(out, "sam.lee@demo.wehelp")
	if sam["status"] != "needs_invite" || sam["credentials"] != "ND" {
		t.Errorf("sam = %v", sam)
	}
	if e := rosterEntry(out, seed.ProviderEmail); e["status"] != "linked" || e["account"].(map[string]any)["status"] != "active" {
		t.Errorf("existing account not linked: %v", e)
	}
	if e := rosterEntry(out, "manager@demo.wehelp"); e["status"] != "add_admin" {
		t.Errorf("admin entry %v", e)
	}
	if out["need_action"] != float64(1) {
		t.Errorf("need_action %v", out["need_action"])
	}

	// Invite Sam: the same set-up link as Add staff, audited via roster.
	if code, _ := adm.do("POST", "/staff/roster/33333333-3333-3333-3333-333333333333/invite", nil); code != http.StatusConflict {
		t.Errorf("invite an admin: %d, want 409", code)
	}
	code, inv := adm.do("POST", "/staff/roster/11111111-1111-1111-1111-111111111111/invite", nil)
	if code != http.StatusCreated {
		t.Fatalf("invite: %d %v", code, inv)
	}
	token := setupToken(t, inv)
	samID := inv["provider"].(map[string]any)["id"].(string)
	if auditCount(t, pool, "staff.invite", samID) != 1 {
		t.Error("invite not audited")
	}
	if code, _ := adm.do("POST", "/staff/roster/11111111-1111-1111-1111-111111111111/invite", nil); code != http.StatusConflict {
		t.Errorf("second invite: %d, want 409", code)
	}
	sam2 := &client{t: t, h: h}
	if code, out := sam2.do("POST", "/staff/setup", map[string]string{"token": token, "password": "a long sam password"}); code != http.StatusOK {
		t.Fatalf("setup from roster invite: %d %v", code, out)
	}
	_, out = adm.do("GET", "/staff/roster", nil)
	if e := rosterEntry(out, "sam.lee@demo.wehelp"); e["status"] != "linked" {
		t.Errorf("after invite: %v", e)
	}

	// A failed pull is shown and changes nothing.
	src.err = errors.New("registry unreachable")
	if code, _ := adm.do("POST", "/staff/roster/sync", nil); code != http.StatusBadGateway {
		t.Errorf("failed sync: %d, want 502", code)
	}
	_, out = adm.do("GET", "/staff/roster", nil)
	if out["error"] != "registry unreachable" || len(out["entries"].([]any)) != 3 {
		t.Errorf("after failure: error %v, %d entries", out["error"], len(out["entries"].([]any)))
	}
	src.err = nil

	// Sam is removed in the Registry: flagged, still enabled.
	removed := now
	src.r.Providers[0].RemovedAt = &removed
	src.r.Providers = src.r.Providers[:1] // Ada and the manager drop off the roster entirely
	if _, err := s.syncRoster(ctx, demoTenantID(t, pool), nil); err != nil {
		t.Fatal(err)
	}
	_, out = adm.do("GET", "/staff/roster", nil)
	if e := rosterEntry(out, "sam.lee@demo.wehelp"); e["status"] != "disable" {
		t.Errorf("removed provider: %v", e)
	}
	if e := rosterEntry(out, seed.ProviderEmail); e["status"] != "disable" {
		t.Errorf("provider dropped from the roster: %v", e)
	}
	var disabled bool
	if err := pool.QueryRow(ctx, `select disabled_at is not null from users where id = $1`, samID).Scan(&disabled); err != nil || disabled {
		t.Errorf("sync disabled the account: %v %v", disabled, err)
	}
	if out["need_action"] != float64(2) {
		t.Errorf("need_action %v", out["need_action"])
	}
	if code, _ := adm.do("POST", "/staff/providers/"+samID+"/disable", nil); code != http.StatusOK {
		t.Fatalf("disable: %d", code)
	}
	_, out = adm.do("GET", "/staff/roster", nil)
	if e := rosterEntry(out, "sam.lee@demo.wehelp"); e["status"] != "removed" {
		t.Errorf("after disable: %v", e)
	}
	var syncs int
	if err := pool.QueryRow(ctx, `select count(*) from audit_events where action = 'roster.sync'`).Scan(&syncs); err != nil || syncs != 2 {
		t.Errorf("roster.sync events = %d, want 2 (one by the admin, one with changes)", syncs)
	}
	// An unchanged scheduled pull records nothing.
	if _, err := s.syncRoster(ctx, demoTenantID(t, pool), nil); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from audit_events where action = 'roster.sync'`).Scan(&syncs); err != nil || syncs != 2 {
		t.Errorf("unchanged pull recorded an event: %d", syncs)
	}
}

// demoTenantID is the seeded demo practice.
func demoTenantID(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `select tenant_id from users where email = $1`, seed.AdminEmail).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
