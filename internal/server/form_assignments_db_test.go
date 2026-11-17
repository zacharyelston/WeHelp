package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestFormSchedulePlan: weekly for 4 weeks gives 4 occurrences at 09:00
// local, across the end of daylight saving time in the office's zone; the
// window opens and closes at 09:00 local too.
func TestFormSchedulePlan(t *testing.T) {
	count := 4
	a := assignmentRow{
		Schedule:   formSchedule{Kind: "weeks", Every: 1, StartDate: "2026-10-25", Count: &count},
		OpenBefore: 2, CloseAfter: 3, Timezone: "America/New_York",
		CreatedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
	}
	plan, err := a.plan(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 4 {
		t.Fatalf("%d occurrences, want 4", len(plan))
	}
	ny, _ := time.LoadLocation("America/New_York")
	wantDue := []string{"2026-10-25", "2026-11-01", "2026-11-08", "2026-11-15"}
	wantUTCHour := []int{13, 14, 14, 14} // EDT before 1 Nov, EST from then
	for i, p := range plan {
		if p.Due.Format("2006-01-02") != wantDue[i] {
			t.Errorf("occurrence %d due %s, want %s", i, p.Due.Format("2006-01-02"), wantDue[i])
		}
		for name, ts := range map[string]time.Time{"opens": p.Opens, "due": p.DueAt, "closes": p.Closes} {
			if l := ts.In(ny); l.Hour() != 9 || l.Minute() != 0 {
				t.Errorf("occurrence %d %s at %s, want 09:00 local", i, name, l)
			}
		}
		if p.DueAt.UTC().Hour() != wantUTCHour[i] {
			t.Errorf("occurrence %d due %s UTC", i, p.DueAt.UTC())
		}
	}
	// The window that spans the change: opens 30 Oct (EDT), closes 4 Nov (EST).
	if got := plan[1].Closes.Sub(plan[1].Opens); got != 5*24*time.Hour+time.Hour {
		t.Errorf("window across the change is %s, want 5 days and an hour", got)
	}

	// Bounds: the horizon stops generation; a repeating schedule must end.
	short, _ := a.plan(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)) // the second opens 30 Oct
	if len(short) != 1 {
		t.Errorf("horizon: %d occurrences, want 1", len(short))
	}
	open := formSchedule{Kind: "days", Every: 1, StartDate: "2026-10-25"}
	if _, _, err := open.check(); err == nil {
		t.Error("an unbounded schedule was accepted")
	}
	until := "2027-12-31"
	daily := assignmentRow{Schedule: formSchedule{Kind: "days", Every: 1, StartDate: "2026-01-01", Until: &until},
		CloseAfter: 1, Timezone: "UTC", CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	if all, _ := daily.plan(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)); len(all) != formsMaxOccurrences {
		t.Errorf("daily for two years: %d occurrences, want the cap %d", len(all), formsMaxOccurrences)
	}
}

// TestFormAssignments is #196: the switch, who may assign, the patient's
// list and version access, occurrences and reminders that repeat-safe runs
// never duplicate, missed after close, and ending an assignment.
func TestFormAssignments(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	fp := &fakePusher{result: "sent"}
	s.SetPusher(fp)
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)
	ada := webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	jordan := patientID(t, pool, seed.Patient1Email)
	sam := patientID(t, pool, seed.Patient2Email)
	phoneSide := &client{t: t, h: h, relay: true}
	staffToken := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	_, sess := pairPhone(t, staffToken, phoneSide, jordan)
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}
	phone.do("PUT", "/devices/current/push", map[string]string{"token": strings.Repeat("ab", 32), "environment": "sandbox"})

	loc := s.location()
	today := time.Now().In(loc)
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }
	once := func(patient string, formID string, start string) map[string]any {
		return map[string]any{"patient_id": patient, "form_id": formID,
			"schedule": map[string]any{"kind": "once", "start_date": start}, "open_before_days": 1, "close_after_days": 3}
	}

	// Off by default.
	if code, out := ada.do("POST", "/assignments", once(jordan, uuid.NewString(), day(0))); code != http.StatusForbidden || out["code"] != "feature_off" {
		t.Errorf("assign with forms off: %d %v", code, out)
	}
	if code, out := phone.do("GET", "/me/forms", nil); code != http.StatusForbidden || out["code"] != "feature_off" {
		t.Errorf("/me/forms with forms off: %d %v", code, out)
	}
	adm.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"forms": true}})

	// Two published forms.
	publish := func(def json.RawMessage) (string, string) {
		t.Helper()
		_, out := adm.do("POST", "/forms/import", def)
		vid := out["version_id"].(string)
		if code, out := adm.do("POST", "/form-versions/"+vid+"/publish", nil); code != http.StatusOK {
			t.Fatalf("publish: %d %v", code, out)
		}
		return out["form_id"].(string), vid
	}
	_, out := adm.do("POST", "/forms/import", formFixture(t, "valid/branching.json"))
	formID, v1 := out["form_id"].(string), out["version_id"].(string)
	if code, out := ada.do("POST", "/assignments", once(jordan, formID, day(0))); code != http.StatusConflict {
		t.Errorf("assign an unpublished form: %d %v, want 409", code, out)
	}
	adm.do("POST", "/form-versions/"+v1+"/publish", nil)
	otherForm, otherVersion := publish(formFixture(t, "valid/minimal.json"))

	// Who may assign.
	if code, _ := ada.do("POST", "/assignments", once(sam, formID, day(0))); code != http.StatusForbidden {
		t.Errorf("provider assigns to an unlinked patient: %d, want 403", code)
	}
	if code, _ := phone.do("POST", "/assignments", once(jordan, formID, day(0))); code != http.StatusNotFound {
		t.Errorf("assign through the Relay: %d, want 404", code)
	}
	for _, bad := range []map[string]any{
		{"patient_id": jordan, "form_id": formID, "schedule": map[string]any{"kind": "days", "every": 1, "start_date": day(0)}},
		{"patient_id": jordan, "form_id": formID, "schedule": map[string]any{"kind": "once", "start_date": day(0)}, "open_before_days": 0, "close_after_days": 0},
		{"patient_id": jordan, "form_id": formID, "schedule": map[string]any{"kind": "once", "start_date": "tomorrow"}},
		{"patient_id": jordan, "form_id": formID, "schedule": map[string]any{"kind": "once", "start_date": day(0)}, "channel": "email"},
	} {
		if code, out := ada.do("POST", "/assignments", bad); code != http.StatusBadRequest {
			t.Errorf("bad assignment %v: %d %v, want 400", bad, code, out)
		}
	}
	code, out := ada.do("POST", "/assignments", once(jordan, formID, day(-1)))
	if code != http.StatusCreated {
		t.Fatalf("assign: %d %v", code, out)
	}
	assignment := out["id"].(string)
	occs := out["occurrences"].([]any)
	if len(occs) != 1 || occs[0].(map[string]any)["state"] != "open" || out["timezone"] == "" {
		t.Fatalf("assignment occurrences: %v", out)
	}
	occurrence := occs[0].(map[string]any)["id"].(string)
	// Front desk and admins may assign any patient; Sam gets the other form.
	if code, out := adm.do("POST", "/assignments", once(sam, otherForm, day(-1))); code != http.StatusCreated {
		t.Fatalf("admin assigns: %d %v", code, out)
	}

	// The patient's list and versions.
	code, out = phone.do("GET", "/me/forms", nil)
	list, _ := out["forms"].([]any)
	if code != http.StatusOK || len(list) != 1 {
		t.Fatalf("/me/forms: %d %v", code, out)
	}
	f := list[0].(map[string]any)
	if f["occurrence_id"] != occurrence || f["title"] != "Branching intake (synthetic)" || f["state"] != "open" || f["version_id"] != v1 || f["response"] != nil {
		t.Errorf("/me/forms entry: %v", f)
	}
	if code, out := phone.do("GET", "/form-versions/"+v1, nil); code != http.StatusOK || out["definition"] == nil {
		t.Errorf("patient reads their version: %d", code)
	}
	if code, _ := phone.do("GET", "/form-versions/"+otherVersion, nil); code != http.StatusNotFound {
		t.Errorf("patient reads another patient's version: %d, want 404", code)
	}
	if code, _ := phone.do("GET", "/assignments?patient_id="+jordan, nil); code != http.StatusNotFound {
		t.Errorf("patient lists assignments: %d, want 404 (staff route, Relay)", code)
	}
	if code, out := ada.do("GET", "/assignments?patient_id="+jordan, nil); code != http.StatusOK || len(out["assignments"].([]any)) != 1 {
		t.Errorf("staff list: %d %v", code, out)
	}
	if code, _ := ada.do("GET", "/assignments?patient_id="+sam, nil); code != http.StatusForbidden {
		t.Errorf("provider lists an unlinked patient's assignments: %d, want 403", code)
	}

	// Weekly for 4 weeks: occurrences up to two weeks ahead, the rest later.
	code, out = ada.do("POST", "/assignments", map[string]any{"patient_id": jordan, "form_id": formID,
		"schedule": map[string]any{"kind": "weeks", "every": 1, "start_date": day(1), "count": 4}, "open_before_days": 0, "close_after_days": 2})
	if code != http.StatusCreated {
		t.Fatalf("weekly: %d %v", code, out)
	}
	weekly := out["id"].(string)
	if n := len(out["occurrences"].([]any)); n != 2 {
		t.Errorf("weekly: %d occurrences created ahead, want 2 (within 14 days)", n)
	}
	occCount := func(assignment string) int {
		var n int
		_ = pool.QueryRow(ctx, `select count(*) from form_occurrences where assignment_id = $1`, assignment).Scan(&n)
		return n
	}

	// The runner: reminders once when open, never twice.
	now := time.Now()
	st, err := s.runForms(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.Reminded != 2 { // Jordan's and Sam's open occurrences; the weekly one isn't open yet
		t.Errorf("first run reminded %d, want 2", st.Reminded)
	}
	eventually(t, "form_due push", func() bool { return fp.count() == 1 })
	if fp.sent[0].Category != "form_due" {
		t.Errorf("push category %q", fp.sent[0].Category)
	}
	if st, _ := s.runForms(ctx, now); st.Reminded != 0 || st.Created != 0 {
		t.Errorf("second run: %+v, want nothing new", st)
	}
	// 24 hours before close: one more, once.
	closes := mustTime(t, occs[0].(map[string]any)["closes_at"])
	reminded := func() (open, close bool) {
		_ = pool.QueryRow(ctx, `select reminded_open_at is not null, reminded_close_at is not null from form_occurrences where id = $1`, occurrence).Scan(&open, &close)
		return
	}
	if o, c := reminded(); !o || c {
		t.Errorf("after opening: reminded open %v, close %v", o, c)
	}
	if _, err := s.runForms(ctx, closes.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, c := reminded(); c {
		t.Error("close reminder sent more than 24 hours before closing")
	}
	if _, err := s.runForms(ctx, closes.Add(-23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, c := reminded(); !c {
		t.Error("no close reminder 23 hours before closing")
	}
	if st, _ := s.runForms(ctx, closes.Add(-22*time.Hour)); st.Reminded != 0 {
		t.Errorf("reminders sent again: %+v", st)
	}

	// Three weeks on, all four weekly occurrences exist, and the first closed ones are missed.
	later := now.Add(22 * 24 * time.Hour)
	st, err = s.runForms(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if occCount(weekly) != 4 {
		t.Errorf("weekly occurrences: %d, want 4", occCount(weekly))
	}
	if st.Missed < 3 { // Jordan's once, Sam's once, at least the first weekly one
		t.Errorf("missed %d, want at least 3", st.Missed)
	}
	if st, _ := s.runForms(ctx, later); st.Created != 0 || st.Missed != 0 || st.Reminded != 0 {
		t.Errorf("repeat run: %+v, want nothing", st)
	}
	if occCount(weekly) != 4 {
		t.Error("a repeat run duplicated occurrences")
	}
	var status string
	_ = pool.QueryRow(ctx, `select status from form_occurrences where id = $1`, occurrence).Scan(&status)
	if status != "missed" {
		t.Errorf("closed occurrence status %q, want missed", status)
	}
	if code, out := phone.do("GET", "/form-versions/"+v1, nil); code != http.StatusNotFound {
		// Every occurrence of Jordan's on v1 is now missed or not yet open in real time.
		_ = out
		var open int
		_ = pool.QueryRow(ctx, `select count(*) from form_occurrences where patient_id = $1 and status = 'open' and opens_at <= now() and closes_at > now()`, jordan).Scan(&open)
		if open == 0 {
			t.Errorf("patient reads a version with no open occurrence: %d", code)
		}
	}

	// The due view.
	code, out = adm.do("GET", "/form-occurrences/due", nil)
	if code != http.StatusOK || out["due_today"] == nil || out["missed_this_week"] == nil {
		t.Errorf("due view: %d %v", code, out)
	}

	// Ending cancels what hasn't opened.
	adm.do("POST", "/forms/"+otherForm+"/archive", nil)
	code, out = ada.do("POST", "/assignments/"+weekly+"/end", nil)
	if code != http.StatusOK || out["ended_at"] == nil {
		t.Fatalf("end: %d %v", code, out)
	}
	if code, _ := ada.do("POST", "/assignments/"+weekly+"/end", nil); code != http.StatusConflict {
		t.Errorf("end twice: %d, want 409", code)
	}
	var cancelled int
	_ = pool.QueryRow(ctx, `select count(*) from form_occurrences where assignment_id = $1 and status = 'cancelled'`, weekly).Scan(&cancelled)
	if cancelled == 0 {
		t.Error("ending cancelled nothing")
	}
	if tx, err := pool.Begin(ctx); err == nil {
		_, _ = tx.Exec(ctx, `set local role wehelp_app`)
		if _, err := tx.Exec(ctx, `delete from form_occurrences where assignment_id = $1`, assignment); err == nil {
			t.Error("wehelp_app deleted an occurrence")
		}
		_ = tx.Rollback(ctx)
	}

	// Audit: who assigned and ended; occurrences by the system.
	for action, want := range map[string]int{"form_assignment.create": 3, "form_assignment.end": 1} {
		var n int
		_ = pool.QueryRow(ctx, `select count(*) from audit_events where action = $1`, action).Scan(&n)
		if n != want {
			t.Errorf("%d %s events, want %d", n, action, want)
		}
	}
	var system, total int
	_ = pool.QueryRow(ctx, `select count(*) filter (where actor_id is null), count(*) from audit_events where action = 'form_occurrence.create'`).Scan(&system, &total)
	if total == 0 || system != total {
		t.Errorf("form_occurrence.create: %d of %d by the system", system, total)
	}

	// The switch turned off closes the patient's list again.
	adm.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"forms": false}})
	if code, _ := phone.do("GET", "/me/forms", nil); code != http.StatusForbidden {
		t.Errorf("/me/forms with forms off: %d", code)
	}
}

func mustTime(t *testing.T, v any) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, v.(string))
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// TestDemoFormSeed: the demo seed (#196) gives the first demo patient an
// open three-page form, and running it again changes nothing.
func TestDemoFormSeed(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	res, err := seed.RunDemoForms(ctx, pool, io.Discard, time.Now(), s.location())
	if err != nil || !res.Created {
		t.Fatalf("seed: %v %+v", err, res)
	}
	again, err := seed.RunDemoForms(ctx, pool, io.Discard, time.Now(), s.location())
	if err != nil || again.Created {
		t.Fatalf("second seed: %v %+v", err, again)
	}
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	_, sess := pairPhone(t, staff, &client{t: t, h: h, relay: true}, patientID(t, pool, seed.Patient1Email))
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}
	code, out := phone.do("GET", "/me/forms", nil)
	list, _ := out["forms"].([]any)
	if code != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["state"] != "open" {
		t.Fatalf("/me/forms: %d %v", code, out)
	}
	code, out = phone.do("GET", "/form-versions/"+res.VersionID.String(), nil)
	if code != http.StatusOK || out["definition"].(map[string]any)["title"] != "Demo visit check-in (synthetic)" {
		t.Errorf("demo version: %d %v", code, out)
	}
	if st, err := s.runForms(ctx, time.Now()); err != nil || st.Created != 0 {
		t.Errorf("runner after the seed: %+v %v", st, err)
	}
}
