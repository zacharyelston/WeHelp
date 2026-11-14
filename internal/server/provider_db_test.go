package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/auth"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestProviderPairsOwnDevice is #69: a provider pairs their own iPad from
// the staff UI; it gets a device-bound provider session that reaches the
// schedule and messages through the Relay, with names, but never the staff
// routes.
func TestProviderPairsOwnDevice(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	relay := &client{t: t, h: h, relay: true}
	ada := patientID(t, pool, seed.ProviderEmail) // any account's ID

	code, out := staff.do("POST", "/pairing/invites", map[string]string{"user_id": ada})
	if code != http.StatusCreated || out["role"] != "provider" || out["patient_name"] != seed.ProviderName {
		t.Fatalf("self invite: %d %v", code, out)
	}
	ipad := newPhone(t)
	code, sess := ipad.redeem(relay, inviteCode(t, out))
	if code != http.StatusOK || sess["role"] != "provider" {
		t.Fatalf("redeem: %d %v", code, sess)
	}
	dev := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}
	if code, me := dev.do("GET", "/me", nil); code != http.StatusOK || me["role"] != "provider" || me["display_name"] != seed.ProviderName {
		t.Errorf("me: %d %v", code, me)
	}
	if code, _ := dev.do("GET", "/appointments", nil); code != http.StatusOK {
		t.Errorf("schedule through the Relay: %d", code)
	}
	if code, _ := dev.do("GET", "/messages", nil); code != http.StatusOK {
		t.Errorf("messages through the Relay: %d", code)
	}
	jordan := patientID(t, pool, seed.Patient1Email)
	if code, _ := dev.do("POST", "/messages", map[string]string{"recipient_id": jordan, "kind": "message", "body": "See you Tuesday."}); code != http.StatusCreated {
		t.Errorf("reply through the Relay: %d", code)
	}
	// Links carry names so the app can label patients.
	var links []map[string]any
	if code := dev.doInto("GET", "/links", &links); code != http.StatusOK || len(links) == 0 ||
		links[0]["patient_name"] == "" || links[0]["provider_name"] != seed.ProviderName {
		t.Errorf("links: %d %v", code, links)
	}
	// A provider device never reaches the staff routes.
	if code, _ := dev.do("GET", "/patients", nil); code != http.StatusNotFound {
		t.Errorf("staff route from a provider device: %d", code)
	}
	// The session refreshes with the device key, as for patients.
	body := map[string]any{"refresh_token": sess["refresh_token"]}
	for k, v := range ipad.proof(auth.PurposeRefresh, sess["refresh_token"].(string)) {
		body[k] = v
	}
	if code, _ := relay.do("POST", "/auth/refresh", body); code != http.StatusOK {
		t.Errorf("provider device refresh: %d", code)
	}
}

// A provider can't pair another staff member's device; an admin can.
func TestStaffPairingScope(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	ctx := context.Background()
	var tenant string
	_ = pool.QueryRow(ctx, `select tenant_id::text from users where email = $1`, seed.ProviderEmail).Scan(&tenant)
	hash, _ := auth.HashPassword("other-provider-password")
	var other string
	if err := pool.QueryRow(ctx, `insert into users (tenant_id, email, password_hash, role, display_name)
		values ($1, 'dr.other@demo.wehelp', $2, 'provider', 'Dr. Other') returning id::text`, tenant, hash).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into users (tenant_id, email, password_hash, role, display_name)
		values ($1, 'admin@demo.wehelp', $2, 'admin', 'Office Admin')`, tenant, hash); err != nil {
		t.Fatal(err)
	}
	provider := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	if code, _ := provider.do("POST", "/pairing/invites", map[string]string{"user_id": other}); code != http.StatusNotFound {
		t.Errorf("provider pairing another provider: %d", code)
	}
	admin := &client{t: t, h: h, token: login(t, h, pool, "admin@demo.wehelp", "other-provider-password")}
	if code, out := admin.do("POST", "/pairing/invites", map[string]string{"user_id": other}); code != http.StatusCreated || out["role"] != "provider" {
		t.Errorf("admin pairing a provider: %d %v", code, out)
	}
}
