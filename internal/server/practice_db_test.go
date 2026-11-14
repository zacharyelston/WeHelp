package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestPracticeType is #108: anyone signed in reads the practice type and
// its words; only an admin on the LAN changes it, audited; the website
// uses the words.
func TestPracticeType(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	var tenant uuid.UUID
	if err := pool.QueryRow(ctx, `select id from tenants where name = $1`, seed.DemoTenant).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	ada := webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	desk := webSignIn(t, h, pool, seed.OfficeEmail, seed.OfficePassword)
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)

	words := func(out map[string]any) map[string]any {
		return out["type"].(map[string]any)["words"].(map[string]any)
	}
	code, out := ada.do("GET", "/practice", nil)
	if code != http.StatusOK || out["type"].(map[string]any)["id"] != "naturopath" || words(out)["patient"] != "patient" {
		t.Fatalf("default practice: %d %v", code, out)
	}
	if n := len(out["types"].([]any)); n != 4 {
		t.Errorf("%d types offered", n)
	}

	// Only an admin changes it, on the LAN.
	salon := map[string]string{"type": "salon"}
	if code, _ := ada.do("PUT", "/practice", salon); code != http.StatusForbidden {
		t.Errorf("provider sets the type: %d, want 403", code)
	}
	if code, _ := desk.do("PUT", "/practice", salon); code != http.StatusForbidden {
		t.Errorf("front desk sets the type: %d, want 403", code)
	}
	relay := &client{t: t, h: h, relay: true, cookies: adm.cookies, csrf: adm.csrf}
	if code, _ := relay.do("PUT", "/practice", salon); code != http.StatusNotFound {
		t.Errorf("type set through the Relay: %d, want 404", code)
	}
	if code, _ := adm.do("PUT", "/practice", map[string]string{"type": "dentist"}); code != http.StatusBadRequest {
		t.Errorf("unknown type: %d, want 400", code)
	}
	code, out = adm.do("PUT", "/practice", salon)
	if code != http.StatusOK || words(out)["patient"] != "client" || out["type"].(map[string]any)["clinical"] != false {
		t.Fatalf("admin sets salon: %d %v", code, out)
	}
	if n := auditCount(t, pool, "practice.update", tenant.String()); n != 1 {
		t.Errorf("%d practice.update events", n)
	}
	if code, out := desk.do("GET", "/practice", nil); code != http.StatusOK || words(out)["provider"] != "stylist" {
		t.Errorf("front desk reads salon words: %d %v", code, out)
	}

	// The website uses the words at the next publish.
	text, _ := bundleText(t, s, tenant)
	if !strings.Contains(text, "Clients use the free WeHelp app") || strings.Contains(text, "Patients use the free WeHelp app") {
		t.Error("the website does not use the salon's words")
	}
}
