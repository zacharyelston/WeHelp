package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestOfficeRole is #106: the front desk sees and books the whole practice's
// schedule and requests, but reads no message threads and manages no staff;
// providers still see only their own.
func TestOfficeRole(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	var ada, jordan, sam string
	for email, dst := range map[string]*string{seed.ProviderEmail: &ada, seed.Patient1Email: &jordan, seed.Patient2Email: &sam} {
		if err := pool.QueryRow(ctx, `select id::text from users where email = $1`, email).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	desk := webSignIn(t, h, pool, seed.OfficeEmail, seed.OfficePassword)
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)

	// A second provider, whose schedule Ada must not see.
	code, out := adm.do("POST", "/staff/providers", map[string]string{"display_name": "Dr. Kim", "email": "kim@demo.wehelp"})
	if code != http.StatusCreated {
		t.Fatalf("add provider: %d %v", code, out)
	}
	kim := out["provider"].(map[string]any)["id"].(string)
	if _, err := pool.Exec(ctx, `insert into provider_patients (tenant_id, provider_id, patient_id, status)
		select tenant_id, $1, $2, 'active' from users where id = $2`, kim, sam); err != nil {
		t.Fatal(err)
	}

	start := time.Now().Add(72 * time.Hour).Truncate(time.Hour)
	book := func(c *client, provider, patient string, at time.Time) (int, map[string]any) {
		body := map[string]any{"patient_id": patient, "starts_at": at.Format(time.RFC3339), "ends_at": at.Add(30 * time.Minute).Format(time.RFC3339)}
		if provider != "" {
			body["provider_id"] = provider
		}
		return c.do("POST", "/appointments", body)
	}

	// The front desk books for either provider and needs to say which.
	// Booking an unlinked pair makes the link: TestBookingMakesLink.
	if code, _ := book(desk, "", jordan, start); code != http.StatusBadRequest {
		t.Errorf("office booking without provider_id: %d, want 400", code)
	}
	code, out = book(desk, ada, jordan, start)
	if code != http.StatusCreated || out["provider_id"] != ada {
		t.Fatalf("office books for Ada: %d %v", code, out)
	}
	adaAppt := out["id"].(string)
	if code, _ := book(desk, kim, sam, start); code != http.StatusCreated {
		t.Fatalf("office books for Kim: %d", code)
	}
	if code, _ := book(desk, ada, jordan, start.Add(10*time.Minute)); code != http.StatusConflict {
		t.Errorf("office double-books Ada: %d, want 409", code)
	}
	var actor string
	_ = pool.QueryRow(ctx, `select actor_id::text from audit_events where action = 'appointment.create' and resource_id = $1`, adaAppt).Scan(&actor)
	var deskID string
	_ = pool.QueryRow(ctx, `select id::text from users where email = $1`, seed.OfficeEmail).Scan(&deskID)
	if actor != deskID {
		t.Errorf("audit actor %q, want the front desk %q", actor, deskID)
	}

	// The front desk sees the whole practice with names; Ada only her own.
	providersOf := func(c *client) map[string]bool {
		code, out := c.do("GET", "/appointments", nil)
		if code != http.StatusOK {
			t.Fatalf("list appointments: %d %v", code, out)
		}
		seen := map[string]bool{}
		for _, a := range out["appointments"].([]any) {
			a := a.(map[string]any)
			seen[a["provider_id"].(string)] = true
			if a["provider_name"] == "" || a["patient_name"] == "" {
				t.Errorf("appointment without names: %v", a)
			}
		}
		return seen
	}
	if seen := providersOf(desk); !seen[ada] || !seen[kim] {
		t.Errorf("front desk sees providers %v, want Ada and Kim", seen)
	}
	adaWeb := webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	if seen := providersOf(adaWeb); seen[kim] || !seen[ada] {
		t.Errorf("Ada sees providers %v, want only herself", seen)
	}
	if code, _ := adaWeb.do("PATCH", "/appointments/"+adaAppt, map[string]string{"status": "completed"}); code != http.StatusOK {
		t.Errorf("Ada completes her own visit: %d", code)
	}

	// The front desk changes any visit.
	code, out = book(desk, kim, sam, start.Add(2*time.Hour))
	if code != http.StatusCreated {
		t.Fatalf("book: %d", code)
	}
	if code, _ := desk.do("PATCH", "/appointments/"+out["id"].(string), map[string]string{"status": "cancelled"}); code != http.StatusOK {
		t.Errorf("office cancels Kim's visit: %d", code)
	}

	// Requests: the front desk sees and confirms one addressed to Ada.
	var reqID string
	if err := pool.QueryRow(ctx, `insert into appointment_requests (tenant_id, patient_id, provider_id, preferred, reason)
		select tenant_id, $1, $2, '[{"date":"2030-01-02","period":"morning"}]', 'Follow-up' from users where id = $1 returning id::text`,
		jordan, ada).Scan(&reqID); err != nil {
		t.Fatal(err)
	}
	code, out = desk.do("GET", "/appointment-requests?status=pending", nil)
	if code != http.StatusOK || len(out["requests"].([]any)) != 1 {
		t.Fatalf("office lists requests: %d %v", code, out)
	}
	at := start.Add(24 * time.Hour)
	code, out = desk.do("POST", "/appointment-requests/"+reqID+"/confirm", map[string]string{
		"starts_at": at.Format(time.RFC3339), "ends_at": at.Add(30 * time.Minute).Format(time.RFC3339)})
	if code != http.StatusOK || out["status"] != "confirmed" {
		t.Fatalf("office confirms Ada's request: %d %v", code, out)
	}
	var bookedFor string
	_ = pool.QueryRow(ctx, `select a.provider_id::text from appointment_requests r join appointments a on a.id = r.appointment_id where r.id = $1`, reqID).Scan(&bookedFor)
	if bookedFor != ada {
		t.Errorf("confirmed visit booked for %q, want Ada", bookedFor)
	}

	// Patients: the whole practice.
	code, out = desk.do("GET", "/patients", nil)
	if code != http.StatusOK || len(out["patients"].([]any)) != 2 {
		t.Errorf("office lists patients: %d %v", code, out)
	}

	// No message threads, and no staff management.
	if code, out := desk.do("GET", "/messages?with="+jordan, nil); code == http.StatusOK && len(out["messages"].([]any)) > 0 {
		t.Errorf("front desk read Ada's thread with Jordan: %v", out)
	}
	if code, out := desk.do("GET", "/conversations", nil); code == http.StatusOK {
		if list, _ := out["conversations"].([]any); len(list) > 0 {
			t.Errorf("front desk sees conversations: %v", list)
		}
	}
	if code, _ := desk.do("GET", "/staff/providers", nil); code != http.StatusOK {
		t.Errorf("office reads the staff list: %d", code)
	}
	if code, _ := desk.do("POST", "/staff/providers", map[string]string{"display_name": "X", "email": "x@demo.wehelp"}); code != http.StatusForbidden {
		t.Errorf("office adds staff: %d, want 403", code)
	}
	if code, _ := desk.do("POST", "/staff/providers/"+kim+"/disable", nil); code != http.StatusForbidden {
		t.Errorf("office disables a provider: %d, want 403", code)
	}
	if code, _ := adaWeb.do("GET", "/staff/providers", nil); code != http.StatusForbidden {
		t.Errorf("provider reads the staff list: %d, want 403", code)
	}

	// Admins invite front-desk accounts too.
	code, out = adm.do("POST", "/staff/providers", map[string]string{"display_name": "Reception", "email": "reception@demo.wehelp", "role": "office"})
	if code != http.StatusCreated || out["provider"].(map[string]any)["role"] != "office" {
		t.Errorf("admin adds a front-desk account: %d %v", code, out)
	}
	if code, _ := adm.do("POST", "/staff/providers", map[string]string{"display_name": "Y", "email": "y@demo.wehelp", "role": "admin"}); code != http.StatusBadRequest {
		t.Errorf("admin invites an admin: %d, want 400", code)
	}
}
