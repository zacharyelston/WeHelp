package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestInstruments is #199: the built-ins stay off on a field install until
// forms_instruments is set; then the sync is idempotent and audited,
// hidden for non-clinical practices, read-only, and a new definition
// publishes a new version without touching the old. A PHQ-9 submitted with
// item 9 = 2 stores the self_harm flag, shown to staff (list, detail, due
// view) and never to the patient.
func TestInstruments(t *testing.T) {
	f := newResponseFixture(t) // forms on, Jordan paired
	s, pool, ctx := f.s, f.pool, context.Background()
	countInstruments := func() int {
		_, out := f.adm.do("GET", "/forms", nil)
		n := 0
		for _, x := range out["forms"].([]any) {
			if x.(map[string]any)["kind"] == "instrument" {
				n++
			}
		}
		return n
	}

	// A field install: off.
	if n := countInstruments(); n != 0 {
		t.Fatalf("field install lists %d instruments", n)
	}
	var stored int
	_ = pool.QueryRow(ctx, `select count(*) from forms where kind = 'instrument'`).Scan(&stored)
	if stored != 0 {
		t.Fatalf("%d instrument forms synced on a field install", stored)
	}

	// The owner has approved: on.
	s.cfg.FormsInstruments = true
	if n := countInstruments(); n != 4 {
		t.Fatalf("%d instruments listed, want 4", n)
	}
	if n := countInstruments(); n != 4 {
		t.Fatal("second list changed the count")
	}
	if n, err := s.syncAllInstruments(ctx); err != nil || n != 0 {
		t.Errorf("repeat sync published %d (%v), want 0", n, err)
	}
	var versions, created int
	_ = pool.QueryRow(ctx, `select count(*) from form_versions v join forms f on f.id = v.form_id where f.kind = 'instrument'`).Scan(&versions)
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where action = 'form.create' and actor_id is null and detail ? 'instrument'`).Scan(&created)
	if versions != 4 || created != 4 {
		t.Errorf("after repeat syncs: %d versions, %d audited creates; want 4 and 4", versions, created)
	}

	// Read-only.
	var phq9Form, phq9Version string
	_ = pool.QueryRow(ctx, `select f.id::text, v.id::text from forms f join form_versions v on v.form_id = f.id
		where f.instrument_key = 'phq9' order by v.version desc limit 1`).Scan(&phq9Form, &phq9Version)
	if code, _ := f.adm.do("POST", "/forms/"+phq9Form+"/draft", nil); code != http.StatusConflict {
		t.Errorf("draft of a built-in: %d, want 409", code)
	}

	// A changed definition publishes a new version; the old ones stay.
	var oldDigest string
	_ = pool.QueryRow(ctx, `select digest from form_versions where id = $1`, phq9Version).Scan(&oldDigest)
	if _, err := pool.Exec(ctx, `insert into form_versions (form_id, version, status, definition, digest, published_at)
		select form_id, 2, 'published', definition, 'an older definition', now() from form_versions where id = $1`, phq9Version); err != nil {
		t.Fatal(err)
	}
	if n, err := s.syncAllInstruments(ctx); err != nil || n != 1 {
		t.Fatalf("sync after a change: %d %v, want 1", n, err)
	}
	var v3 int
	_ = pool.QueryRow(ctx, `select max(version) from form_versions where form_id = $1`, phq9Form).Scan(&v3)
	var stillOld string
	_ = pool.QueryRow(ctx, `select digest from form_versions where id = $1`, phq9Version).Scan(&stillOld)
	if v3 != 3 || stillOld != oldDigest {
		t.Errorf("after the change: latest v%d, v1 digest kept: %v", v3, stillOld == oldDigest)
	}

	// Hidden for a practice type without clinical features.
	f.adm.do("PUT", "/practice", map[string]string{"type": "salon"})
	if n := countInstruments(); n != 0 {
		t.Errorf("a salon lists %d instruments", n)
	}
	if code, _ := f.ada.do("POST", "/assignments", map[string]any{"patient_id": f.jordan, "form_id": phq9Form,
		"schedule": map[string]any{"kind": "once", "start_date": time.Now().Format("2006-01-02")}}); code != http.StatusConflict {
		t.Errorf("assign a built-in at a salon: %d, want 409", code)
	}
	f.adm.do("PUT", "/practice", map[string]string{"type": "naturopath"})

	// Jordan fills a PHQ-9 with item 9 = 2.
	yesterday := time.Now().In(s.location()).AddDate(0, 0, -1).Format("2006-01-02")
	code, out := f.ada.do("POST", "/assignments", map[string]any{"patient_id": f.jordan, "form_id": phq9Form,
		"schedule": map[string]any{"kind": "once", "start_date": yesterday}, "open_before_days": 1, "close_after_days": 3})
	if code != http.StatusCreated {
		t.Fatalf("assign PHQ-9: %d %v", code, out)
	}
	occ := out["occurrences"].([]any)[0].(map[string]any)["id"].(string)
	_, out, _ = f.phone.send("POST", "/responses", nil, []byte(`{"occurrence_id":"`+occ+`"}`))
	rid := out["id"].(string)
	answers := map[string]any{"phq9_difficulty": "somewhat"}
	for i := 1; i <= 8; i++ {
		answers["phq9_"+string(rune('0'+i))] = "1"
	}
	answers["phq9_9"] = "2"
	if code, out := f.savePage(t, rid, "phq9_p1", 0, uuid.NewString(), answers); code != http.StatusOK {
		t.Fatalf("save PHQ-9: %d %v", code, out)
	}
	code, out, _ = f.phone.send("POST", "/responses/"+rid+"/submit", map[string]string{"Idempotency-Key": uuid.NewString()}, nil)
	if code != http.StatusOK {
		t.Fatalf("submit PHQ-9: %d %v", code, out)
	}
	if _, leaked := out["flags"]; leaked {
		t.Errorf("the patient's submit answer carries flags: %v", out)
	}
	if b, _ := json.Marshal(out); strings.Contains(string(b), "self_harm") || strings.Contains(string(b), "phq9_total") {
		t.Errorf("the patient sees the flag or the total: %s", b)
	}
	var flags, scores string
	_ = pool.QueryRow(ctx, `select flags::text, scores::text from form_responses where id = $1`, rid).Scan(&flags, &scores)
	if !strings.Contains(flags, `"self_harm"`) || !strings.Contains(scores, `"10"`) || !strings.Contains(scores, `"moderate"`) {
		t.Errorf("stored flags %s, scores %s", flags, scores)
	}

	// Staff see it: the detail, the list and the due view.
	_, out = f.ada.do("GET", "/responses/"+rid, nil)
	if fl, _ := out["flags"].([]any); len(fl) != 1 || fl[0].(map[string]any)["id"] != "self_harm" {
		t.Errorf("staff detail flags: %v", out["flags"])
	}
	_, out = f.ada.do("GET", "/responses?patient_id="+f.jordan, nil)
	if fl, _ := out["responses"].([]any)[0].(map[string]any)["flags"].([]any); len(fl) != 1 {
		t.Errorf("staff list flags: %v", out["responses"])
	}
	_, out = f.adm.do("GET", "/form-occurrences/due", nil)
	if fl, _ := out["flagged_this_week"].([]any); len(fl) != 1 || fl[0].(map[string]any)["response_id"] != rid {
		t.Errorf("due view flagged: %v", out)
	}

	// Item 9 = 0 raising no flag is in the shared vectors (instrument-phq9.json).
}

// TestDemoSeedPHQ9: the demo seed (#199) gives Jordan a PHQ-9 too, and a
// re-run on a host seeded before #199 adds just that.
func TestDemoSeedPHQ9(t *testing.T) {
	s, pool := testBackOffice(t)
	ctx := context.Background()
	res, err := seed.RunDemoForms(ctx, pool, ioDiscard{}, time.Now(), s.location())
	if err != nil || res.PHQ9OccurrenceID == uuid.Nil {
		t.Fatalf("seed: %v %+v", err, res)
	}
	again, err := seed.RunDemoForms(ctx, pool, ioDiscard{}, time.Now(), s.location())
	if err != nil || again.Created {
		t.Fatalf("second seed: %v %+v", err, again)
	}
	var open int
	_ = pool.QueryRow(ctx, `select count(*) from form_occurrences where status = 'open'`).Scan(&open)
	if open != 2 {
		t.Errorf("%d open occurrences, want 2 (check-in and PHQ-9)", open)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
