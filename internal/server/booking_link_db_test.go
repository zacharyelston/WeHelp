package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestBookingMakesLink: the front desk or an admin booking a provider with a
// patient they aren't linked to makes the link, audited. A provider still
// books only linked patients, and a revoked link stays revoked.
func TestBookingMakesLink(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	var jordan, deskID, adminID string
	for email, dst := range map[string]*string{seed.Patient1Email: &jordan, seed.OfficeEmail: &deskID, seed.AdminEmail: &adminID} {
		if err := pool.QueryRow(ctx, `select id::text from users where email = $1`, email).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	desk := webSignIn(t, h, pool, seed.OfficeEmail, seed.OfficePassword)
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)

	code, out := adm.do("POST", "/staff/providers", map[string]string{"display_name": "Dr. Kim", "email": "kim@demo.wehelp"})
	if code != http.StatusCreated {
		t.Fatalf("add provider: %d %v", code, out)
	}
	kim := out["provider"].(map[string]any)["id"].(string)
	code, out = adm.do("POST", "/patients", map[string]string{"display_name": "Zed Patient", "email": "zed@demo.wehelp"})
	if code != http.StatusCreated {
		t.Fatalf("add patient: %d %v", code, out)
	}
	zed := out["id"].(string)

	start := time.Now().Add(96 * time.Hour).Truncate(time.Hour)
	book := func(c *client, provider, patient string, at time.Time) (int, map[string]any) {
		body := map[string]any{"provider_id": provider, "patient_id": patient,
			"starts_at": at.Format(time.RFC3339), "ends_at": at.Add(30 * time.Minute).Format(time.RFC3339)}
		return c.do("POST", "/appointments", body)
	}
	linkStatus := func(provider, patient string) string {
		var st string
		_ = pool.QueryRow(ctx, `select status from provider_patients where provider_id = $1 and patient_id = $2`,
			provider, patient).Scan(&st)
		return st
	}

	// No link at all: the admin's booking creates an active one.
	if code, out := book(adm, kim, zed, start); code != http.StatusCreated {
		t.Fatalf("admin books an unlinked pair: %d %v", code, out)
	}
	if st := linkStatus(kim, zed); st != "active" {
		t.Errorf("link after admin booking = %q, want active", st)
	}
	var actor, detail string
	if err := pool.QueryRow(ctx, `select actor_id::text, detail::text from audit_events
		where action = 'link.create' and resource_id = $1`, kim+":"+zed).Scan(&actor, &detail); err != nil {
		t.Fatalf("link.create audit: %v", err)
	}
	if actor != adminID || detail != `{"via": "booking"}` {
		t.Errorf("link.create audit actor %q detail %s", actor, detail)
	}
	// Linked now: a second booking adds no second link event.
	if code, _ := book(desk, kim, zed, start.Add(2*time.Hour)); code != http.StatusCreated {
		t.Errorf("front desk books a linked pair: %d", code)
	}
	var n int
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where action = 'link.create'`).Scan(&n)
	if n != 1 {
		t.Errorf("link.create events = %d, want 1", n)
	}

	// A pending invite: the front desk's booking activates it.
	if _, err := pool.Exec(ctx, `insert into provider_patients (tenant_id, provider_id, patient_id)
		select tenant_id, $1, $2 from users where id = $2`, kim, jordan); err != nil {
		t.Fatal(err)
	}
	if code, out := book(desk, kim, jordan, start.Add(4*time.Hour)); code != http.StatusCreated {
		t.Fatalf("front desk books a pending pair: %d %v", code, out)
	}
	if st := linkStatus(kim, jordan); st != "active" {
		t.Errorf("pending link after booking = %q, want active", st)
	}

	// A revoked link stays revoked.
	if _, err := pool.Exec(ctx, `update provider_patients set status = 'revoked' where provider_id = $1 and patient_id = $2`, kim, zed); err != nil {
		t.Fatal(err)
	}
	if code, _ := book(desk, kim, zed, start.Add(6*time.Hour)); code != http.StatusConflict {
		t.Errorf("booking a revoked pair: %d, want 409", code)
	}
	if st := linkStatus(kim, zed); st != "revoked" {
		t.Errorf("revoked link after booking = %q, want revoked", st)
	}

	// Only a patient of this practice can be linked.
	if code, _ := book(desk, kim, deskID, start.Add(8*time.Hour)); code != http.StatusBadRequest {
		t.Errorf("booking a non-patient: %d, want 400", code)
	}

	// A provider still books only linked patients.
	adaWeb := webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	code, out = adaWeb.do("POST", "/appointments", map[string]any{"patient_id": zed,
		"starts_at": start.Format(time.RFC3339), "ends_at": start.Add(30 * time.Minute).Format(time.RFC3339)})
	if code != http.StatusForbidden {
		t.Errorf("provider books an unlinked patient: %d %v, want 403", code, out)
	}
}
