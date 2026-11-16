package server

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestTrainingSeed is #80: the training profile adds a synthetic week to
// Demo Clinic that staff see through the normal API, every write audited,
// the same layout each run, and a second run in the same week writes
// nothing.
func TestTrainingSeed(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	// A Wednesday at noon: Monday and Tuesday are past, Thursday and Friday ahead.
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	r1, err := seed.RunTraining(ctx, pool, io.Discard, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got := r1.WeekOf; !got.Equal(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("week of %v, want Monday 16 Nov", got)
	}
	if len(r1.Staff) != 5 || len(r1.Patients) != 25 {
		t.Fatalf("staff %d patients %d", len(r1.Staff), len(r1.Patients))
	}
	if r1.Appointments < 45 || r1.Appointments > 60 || r1.Requests != 5 || r1.Messages != 16 {
		t.Errorf("created appointments %d requests %d messages %d", r1.Appointments, r1.Requests, r1.Messages)
	}
	for _, p := range r1.Patients {
		if !p.Created || p.Role != "patient" {
			t.Errorf("patient %+v", p)
		}
	}

	var events int64
	if err := pool.QueryRow(ctx, `select count(*) from audit_events where tenant_id = $1`, r1.TenantID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	var statuses map[string]int64
	rows, err := pool.Query(ctx, `select status, count(*) from appointments a join users u on u.id = a.provider_id
		where u.email like 'dr.%@demo.wehelp' and u.email <> $1 group by status`, seed.ProviderEmail)
	if err != nil {
		t.Fatal(err)
	}
	statuses = map[string]int64{}
	for rows.Next() {
		var st string
		var n int64
		_ = rows.Scan(&st, &n)
		statuses[st] = n
	}
	rows.Close()
	if statuses["completed"] == 0 || statuses["scheduled"] == 0 {
		t.Errorf("appointment statuses %v: want past completed and future scheduled", statuses)
	}

	// Same week again: nothing new, no new audit events, same layout.
	r2, err := seed.RunTraining(ctx, pool, io.Discard, now.Add(3*time.Hour), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Appointments != 0 || r2.Requests != 0 || r2.Messages != 0 || countNew(r2.Patients) != 0 {
		t.Errorf("second run created %+v", r2)
	}
	for i := range r1.Patients {
		if r1.Patients[i].Email != r2.Patients[i].Email {
			t.Errorf("patient %d: %s then %s", i, r1.Patients[i].Email, r2.Patients[i].Email)
		}
	}
	var events2 int64
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where tenant_id = $1`, r1.TenantID).Scan(&events2)
	if events2 != events {
		t.Errorf("audit events %d then %d", events, events2)
	}
	if v, err := audit.Verify(ctx, pool, r1.TenantID); err != nil || v.BrokenAt != 0 {
		t.Errorf("audit chain: %+v %v", v, err)
	}

	// The training front desk sees the week and the pending requests.
	desk := webSignIn(t, h, pool, "training.frontdesk@demo.wehelp", seed.TrainingPassword)
	q := url.Values{"from": {r1.WeekOf.Format(time.RFC3339)}, "to": {r1.WeekOf.AddDate(0, 0, 5).Format(time.RFC3339)}}
	code, out := desk.do("GET", "/appointments?"+q.Encode(), nil)
	if code != http.StatusOK {
		t.Fatalf("appointments: %d", code)
	}
	if n := len(out["appointments"].([]any)); n < r1.Appointments {
		t.Errorf("front desk sees %d appointments this week, want ≥ %d", n, r1.Appointments)
	}
	code, out = desk.do("GET", "/appointment-requests?status=pending", nil)
	if code != http.StatusOK {
		t.Fatalf("requests: %d", code)
	}
	if reqs, _ := out["requests"].([]any); len(reqs) != 4 {
		t.Errorf("pending requests = %d, want 4 (%v)", len(reqs), out)
	}

	// A training provider has threads: some unread, some answered.
	mira := webSignIn(t, h, pool, "dr.mira.okafor@demo.wehelp", seed.TrainingPassword)
	code, out = mira.do("GET", "/conversations", nil)
	if code != http.StatusOK {
		t.Fatalf("conversations: %d", code)
	}
	if convs, _ := out["conversations"].([]any); len(convs) == 0 {
		t.Errorf("Dr. Okafor has no conversations: %v", out)
	}
}

func countNew(as []seed.Account) int {
	n := 0
	for _, a := range as {
		if a.Created {
			n++
		}
	}
	return n
}
