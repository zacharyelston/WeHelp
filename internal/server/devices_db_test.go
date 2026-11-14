package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/auth"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// pairPhone pairs a new phone for a patient and returns it with its session.
func pairPhone(t *testing.T, staff, phoneSide *client, patientID string) (phone, map[string]any) {
	t.Helper()
	_, out := staff.do("POST", "/pairing/invites", map[string]string{"patient_id": patientID})
	p := newPhone(t)
	code, sess := p.redeem(phoneSide, inviteCode(t, out))
	if code != http.StatusOK {
		t.Fatalf("pair: %d %v", code, sess)
	}
	return p, sess
}

func refreshWith(c *client, p phone, refreshToken string) int {
	body := map[string]any{"refresh_token": refreshToken}
	for k, v := range p.proof(auth.PurposeRefresh, refreshToken) {
		body[k] = v
	}
	code, _ := c.do("POST", "/auth/refresh", body)
	return code
}

// TestDeviceManagement is the #10 verification: two devices, revoke one from
// the other; the revoked one is locked out at once (access and refresh).
func TestDeviceManagement(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	phoneSide := &client{t: t, h: h, relay: true}
	jordan := patientID(t, pool, seed.Patient1Email)

	phoneA, sessA := pairPhone(t, staff, phoneSide, jordan)
	phoneB, sessB := pairPhone(t, staff, phoneSide, jordan)
	a := &client{t: t, h: h, token: sessA["access_token"].(string), relay: true}
	b := &client{t: t, h: h, token: sessB["access_token"].(string), relay: true}

	code, out := a.do("GET", "/devices", nil)
	devices, _ := out["devices"].([]any)
	if code != http.StatusOK || len(devices) != 2 {
		t.Fatalf("list: %d %v", code, out)
	}
	var current int
	for _, d := range devices {
		if d.(map[string]any)["current"] == true {
			current++
			if d.(map[string]any)["id"] != sessA["device_id"] {
				t.Errorf("current device is %v, want %v", d.(map[string]any)["id"], sessA["device_id"])
			}
		}
	}
	if current != 1 {
		t.Errorf("%d devices marked current", current)
	}

	// Revoke B from A ("Sign out other devices").
	if code, _ := a.do("DELETE", "/devices/"+sessB["device_id"].(string), nil); code != http.StatusNoContent {
		t.Fatalf("revoke B: %d", code)
	}
	if code, _ := b.do("GET", "/me", nil); code != http.StatusUnauthorized {
		t.Errorf("revoked device's access token still works: %d", code)
	}
	if code := refreshWith(phoneSide, phoneB, sessB["refresh_token"].(string)); code != http.StatusUnauthorized {
		t.Errorf("revoked device refreshed: %d", code)
	}
	if code, _ := a.do("GET", "/me", nil); code != http.StatusOK {
		t.Errorf("A stopped working: %d", code)
	}
	if code := refreshWith(phoneSide, phoneA, sessA["refresh_token"].(string)); code != http.StatusOK {
		t.Errorf("A refresh: %d", code)
	}
	// Idempotent; someone else's device is not found.
	if code, _ := a.do("DELETE", "/devices/"+sessB["device_id"].(string), nil); code != http.StatusNoContent {
		t.Errorf("second revoke: %d", code)
	}
	sam := patientID(t, pool, seed.Patient2Email)
	_, _ = pool.Exec(t.Context(), `update provider_patients set status = 'active' where patient_id = $1`, sam)
	_, samSess := pairPhone(t, staff, phoneSide, sam)
	if code, _ := a.do("DELETE", "/devices/"+samSess["device_id"].(string), nil); code != http.StatusNotFound {
		t.Errorf("revoke another patient's device: %d", code)
	}

	// Staff list and revoke a patient's devices on the LAN, never through the Relay.
	if code, out := staff.do("GET", "/patients/"+jordan+"/devices", nil); code != http.StatusOK || len(out["devices"].([]any)) != 2 {
		t.Errorf("staff list: %d %v", code, out)
	}
	viaRelay := &client{t: t, h: h, token: staff.token, relay: true}
	if code, _ := viaRelay.do("DELETE", "/patients/"+jordan+"/devices/"+sessA["device_id"].(string), nil); code != http.StatusNotFound {
		t.Errorf("staff revoke via relay: %d", code)
	}
	// A patient (on its phone, through the Relay) can't reach the staff routes.
	if code, _ := a.do("GET", "/patients/"+jordan+"/devices", nil); code != http.StatusNotFound {
		t.Errorf("patient on staff route: %d", code)
	}
	if code, _ := staff.do("DELETE", "/patients/"+jordan+"/devices/"+sessA["device_id"].(string), nil); code != http.StatusNoContent {
		t.Fatalf("staff revoke: %d", code)
	}
	if code, _ := a.do("GET", "/me", nil); code != http.StatusUnauthorized {
		t.Errorf("staff-revoked device still works: %d", code)
	}
	// Staff can't touch a device through the wrong patient.
	if code, _ := staff.do("DELETE", "/patients/"+jordan+"/devices/"+samSess["device_id"].(string), nil); code != http.StatusNotFound {
		t.Errorf("device under the wrong patient: %d", code)
	}

	var events int
	_ = pool.QueryRow(t.Context(), `select count(*) from audit_events where action = 'device.revoke'`).Scan(&events)
	if events != 2 {
		t.Errorf("%d device.revoke audit events, want 2", events)
	}
}

// Patient access tokens without a device are refused, whatever their source.
func TestPatientTokenMustBeDeviceBound(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	jordan, _ := uuid.Parse(patientID(t, pool, seed.Patient1Email))
	var tenant uuid.UUID
	_ = pool.QueryRow(t.Context(), `select tenant_id from users where id = $1`, jordan).Scan(&tenant)
	tok, err := auth.IssueAccessToken(jordan, tenant, "patient", nil, s.jwtSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, h: h, token: tok}
	if code, _ := c.do("GET", "/me", nil); code != http.StatusUnauthorized {
		t.Errorf("unbound patient token: %d", code)
	}
}
