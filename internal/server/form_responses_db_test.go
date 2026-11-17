package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/forms"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

// formFixture for responses: forms on, the branching form published and
// assigned once to Jordan (open now), Jordan's phone paired.
type responseFixture struct {
	s          *Server
	pool       *pgxpool.Pool
	h          http.Handler
	adm, ada   *client
	phone      *client
	jordan     string
	formID     string
	versionID  string
	occurrence string
}

func newResponseFixture(t *testing.T) responseFixture {
	t.Helper()
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	f := responseFixture{s: s, pool: pool, h: h}
	f.adm = webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)
	f.ada = webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	f.jordan = patientID(t, pool, seed.Patient1Email)
	f.adm.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"forms": true}})
	_, out := f.adm.do("POST", "/forms/import", formFixture(t, "valid/branching.json"))
	f.formID, f.versionID = out["form_id"].(string), out["version_id"].(string)
	f.adm.do("POST", "/form-versions/"+f.versionID+"/publish", nil)
	yesterday := time.Now().In(s.location()).AddDate(0, 0, -1).Format("2006-01-02")
	code, out := f.ada.do("POST", "/assignments", map[string]any{"patient_id": f.jordan, "form_id": f.formID,
		"schedule": map[string]any{"kind": "once", "start_date": yesterday}, "open_before_days": 1, "close_after_days": 3})
	if code != http.StatusCreated {
		t.Fatalf("assign: %d %v", code, out)
	}
	f.occurrence = out["occurrences"].([]any)[0].(map[string]any)["id"].(string)
	staffToken := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	_, sess := pairPhone(t, staffToken, &client{t: t, h: h, relay: true}, f.jordan)
	f.phone = &client{t: t, h: h, token: sess["access_token"].(string), relay: true}
	return f
}

// send makes a request with headers (If-Match, Idempotency-Key) and a raw body.
func (c *client) send(method, path string, headers map[string]string, body []byte) (int, map[string]any, http.Header) {
	c.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if c.relay {
		req = req.WithContext(tunnel.WithViaRelay(req.Context()))
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

func (f responseFixture) savePage(t *testing.T, rid, page string, rev int, key string, answers map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"answers": answers})
	code, out, _ := f.phone.send("PUT", "/responses/"+rid+"/pages/"+page,
		map[string]string{"If-Match": `"` + strconv.Itoa(rev) + `"`, "Idempotency-Key": key}, b)
	return code, out
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// TestFormResponses is #197: start or resume, page saves with revisions and
// idempotency keys, validation, the latest save per page, hidden answers
// dropped, required answers on submit, server scoring, one outcome per
// submit, the closed window, other patients, append-only storage, audit
// and staff reads with the new badge.
func TestFormResponses(t *testing.T) {
	f := newResponseFixture(t)
	ctx := context.Background()
	pool := f.pool

	// Start, and resume the same response.
	code, out, hdr := f.phone.send("POST", "/responses", nil, []byte(`{"occurrence_id":"`+f.occurrence+`"}`))
	if code != http.StatusCreated || out["revision"] != float64(0) || hdr.Get("ETag") != `"0"` || out["status"] != "in_progress" {
		t.Fatalf("start: %d %v %v", code, out, hdr)
	}
	rid := out["id"].(string)
	if code, out, _ := f.phone.send("POST", "/responses", nil, []byte(`{"occurrence_id":"`+f.occurrence+`"}`)); code != http.StatusOK || out["id"] != rid {
		t.Errorf("resume: %d %v", code, out)
	}
	if code, _, _ := f.phone.send("POST", "/responses", nil, []byte(`{"occurrence_id":"`+uuid.NewString()+`"}`)); code != http.StatusNotFound {
		t.Errorf("start an unknown occurrence: %d, want 404", code)
	}

	// Page 1, then retries and refusals.
	k1 := uuid.NewString()
	intro := map[string]any{"consent": true, "age": 40, "smoker": "no"}
	if code, out := f.savePage(t, rid, "p_intro", 0, k1, intro); code != http.StatusOK || out["revision"] != float64(1) {
		t.Fatalf("save page 1: %d %v", code, out)
	}
	if code, out := f.savePage(t, rid, "p_intro", 0, k1, intro); code != http.StatusOK || out["revision"] != float64(1) {
		t.Errorf("retry with the same key: %d %v", code, out)
	}
	if code, out := f.savePage(t, rid, "p_intro", 0, k1, map[string]any{"consent": false}); code != http.StatusConflict || out["code"] != "idempotency_mismatch" {
		t.Errorf("same key, other body: %d %v", code, out)
	}
	if code, out := f.savePage(t, rid, "p_intro", 0, uuid.NewString(), intro); code != http.StatusConflict || out["code"] != "stale_revision" || out["revision"] != float64(1) {
		t.Errorf("stale revision: %d %v", code, out)
	}
	if code, out, _ := f.phone.send("PUT", "/responses/"+rid+"/pages/p_intro", map[string]string{"Idempotency-Key": uuid.NewString()}, []byte(`{"answers":{}}`)); code != http.StatusBadRequest || out["code"] != "if_match_required" {
		t.Errorf("no If-Match: %d %v", code, out)
	}
	if code, out, _ := f.phone.send("PUT", "/responses/"+rid+"/pages/p_intro", map[string]string{"If-Match": "1"}, []byte(`{"answers":{}}`)); code != http.StatusBadRequest || out["code"] != "idempotency_key_required" {
		t.Errorf("no key: %d %v", code, out)
	}
	if code, out := f.savePage(t, rid, "p_nope", 1, uuid.NewString(), map[string]any{}); code != http.StatusNotFound || out["code"] != "unknown_page" {
		t.Errorf("unknown page: %d %v", code, out)
	}
	code, out = f.savePage(t, rid, "p_intro", 1, uuid.NewString(), map[string]any{"age": 200, "temp": 37})
	if code != http.StatusUnprocessableEntity || out["code"] != "invalid_answers" || len(out["errors"].([]any)) != 2 {
		t.Errorf("invalid answers: %d %v", code, out)
	}
	big := map[string]any{"notes": strings.Repeat("x", 70<<10)}
	if code, out := f.savePage(t, rid, "p_notes", 1, uuid.NewString(), big); code != http.StatusRequestEntityTooLarge || out["code"] != "body_too_large" {
		t.Errorf("a 70 KB page: %d %v", code, out)
	}

	// Submit with a required answer missing: 422 with the item and its page.
	code, out, _ = f.phone.send("POST", "/responses/"+rid+"/submit", map[string]string{"Idempotency-Key": uuid.NewString()}, nil)
	if code != http.StatusUnprocessableEntity || out["code"] != "invalid_answers" {
		t.Fatalf("submit with symptoms missing: %d %v", code, out)
	}
	if e := out["errors"].([]any)[0].(map[string]any); e["item"] != "symptoms" || e["page_id"] != "p_symptoms" || e["code"] != "required" {
		t.Errorf("submit error: %v", e)
	}

	// Page 2, then change page 1: age 16 hides the smoker question.
	if code, out := f.savePage(t, rid, "p_symptoms", 1, uuid.NewString(), map[string]any{"symptoms": []string{"cough", "pain"}, "pain_level": 5}); code != http.StatusOK || out["revision"] != float64(2) {
		t.Fatalf("save page 2: %d %v", code, out)
	}
	if code, out := f.savePage(t, rid, "p_intro", 2, uuid.NewString(), map[string]any{"consent": true, "age": 16, "smoker": "yes"}); code != http.StatusOK || out["revision"] != float64(3) {
		t.Fatalf("change page 1: %d %v", code, out)
	}
	_, out = f.phone.do("GET", "/me/forms", nil)
	if r := out["forms"].([]any)[0].(map[string]any)["response"].(map[string]any); r["id"] != rid || r["revision"] != float64(3) {
		t.Errorf("/me/forms resume pointer: %v", r)
	}
	_, out, _ = f.phone.send("POST", "/responses", nil, []byte(`{"occurrence_id":"`+f.occurrence+`"}`))
	if a := out["answers"].(map[string]any); a["age"] != float64(16) || a["pain_level"] != float64(5) || len(out["pages"].([]any)) != 2 ||
		out["pages"].([]any)[0].(map[string]any)["page_id"] != "p_intro" {
		t.Errorf("resume answers: %v", out)
	}
	if code, out, _ := f.phone.send("POST", "/responses/"+rid+"/submit", map[string]string{"Idempotency-Key": uuid.NewString(), "If-Match": `"2"`}, nil); code != http.StatusConflict || out["code"] != "stale_revision" {
		t.Errorf("submit with a stale If-Match: %d %v", code, out)
	}

	// Submit: scored by the server; hidden answers dropped.
	code, out, _ = f.phone.send("POST", "/responses/"+rid+"/submit", map[string]string{"Idempotency-Key": uuid.NewString(), "If-Match": `"3"`}, nil)
	if code != http.StatusOK || out["status"] != "submitted" || len(out["scores"].(map[string]any)) != 0 {
		t.Fatalf("submit: %d %v", code, out)
	}
	var answersJSON, scoresJSON []byte
	var occStatus string
	_ = pool.QueryRow(ctx, `select r.answers, r.scores, o.status from form_responses r join form_occurrences o on o.id = r.occurrence_id where r.id = $1`, rid).
		Scan(&answersJSON, &scoresJSON, &occStatus)
	stored, _ := forms.ParseAnswers(answersJSON)
	if _, hidden := stored["smoker"]; hidden || stored["age"] == nil || occStatus != "completed" {
		t.Errorf("stored answers %s, occurrence %s", answersJSON, occStatus)
	}
	var raw []byte
	_ = pool.QueryRow(ctx, `select definition from form_versions where id = $1`, f.versionID).Scan(&raw)
	def, _ := forms.Parse(raw)
	want, _ := forms.Scores(def, stored)
	wantJSON, _ := json.Marshal(want)
	if !sameJSON(scoresJSON, wantJSON) {
		t.Errorf("stored scores %s, forms.Scores gives %s", scoresJSON, wantJSON)
	}
	if code, out, _ := f.phone.send("POST", "/responses/"+rid+"/submit", map[string]string{"Idempotency-Key": uuid.NewString()}, nil); code != http.StatusOK || out["status"] != "submitted" {
		t.Errorf("submit again: %d %v, want the same 200", code, out)
	}
	if code, out := f.savePage(t, rid, "p_notes", 3, uuid.NewString(), map[string]any{"notes": "late"}); code != http.StatusConflict || out["code"] != "already_submitted" {
		t.Errorf("save after submit: %d %v", code, out)
	}
	if code, out, _ := f.phone.send("POST", "/responses", nil, []byte(`{"occurrence_id":"`+f.occurrence+`"}`)); code != http.StatusConflict || out["code"] != "already_submitted" {
		t.Errorf("start a completed form: %d %v", code, out)
	}

	// Append-only and frozen, even for the owner.
	if _, err := pool.Exec(ctx, `update form_answer_events set answers = '{}' where response_id = $1`, rid); err == nil {
		t.Error("UPDATE on form_answer_events succeeded")
	}
	if _, err := pool.Exec(ctx, `delete from form_answer_events where response_id = $1`, rid); err == nil {
		t.Error("DELETE on form_answer_events succeeded")
	}
	if _, err := pool.Exec(ctx, `update form_responses set scores = '{}' where id = $1`, rid); err == nil {
		t.Error("UPDATE on a submitted response succeeded")
	}

	// Another patient can't touch it.
	adminToken := &client{t: t, h: f.h, token: login(t, f.h, pool, seed.AdminEmail, seed.AdminPassword)}
	_, sess := pairPhone(t, adminToken, &client{t: t, h: f.h, relay: true}, patientID(t, pool, seed.Patient2Email))
	sam := &client{t: t, h: f.h, token: sess["access_token"].(string), relay: true}
	if code, _, _ := sam.send("POST", "/responses", nil, []byte(`{"occurrence_id":"`+f.occurrence+`"}`)); code != http.StatusNotFound {
		t.Errorf("another patient starts it: %d, want 404", code)
	}
	if code, _, _ := sam.send("POST", "/responses/"+rid+"/submit", map[string]string{"Idempotency-Key": uuid.NewString()}, nil); code != http.StatusNotFound {
		t.Errorf("another patient submits it: %d, want 404", code)
	}

	// Staff read it, with the new badge until the first open.
	code, out = f.ada.do("GET", "/responses?patient_id="+f.jordan, nil)
	list, _ := out["responses"].([]any)
	if code != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["new"] != true {
		t.Fatalf("staff list: %d %v", code, out)
	}
	code, out = f.ada.do("GET", "/responses/"+rid, nil)
	if code != http.StatusOK || out["new"] != true {
		t.Fatalf("staff read: %d %v", code, out)
	}
	items := map[string]map[string]any{}
	for _, it := range out["items"].([]any) {
		items[it.(map[string]any)["item_id"].(string)] = it.(map[string]any)
	}
	if items["age"]["text"] != "Your age" || items["age"]["display"] != "16 years" || items["symptoms"]["display"] != "Cough, Pain" || items["smoker"] != nil {
		t.Errorf("answers with item text: %v", items)
	}
	if _, out = f.ada.do("GET", "/responses?patient_id="+f.jordan, nil); out["responses"].([]any)[0].(map[string]any)["new"] != false {
		t.Errorf("still new after opening: %v", out)
	}
	if code, _ := f.phone.do("GET", "/responses/"+rid, nil); code != http.StatusNotFound {
		t.Errorf("patient reads the staff view: %d, want 404 (LAN only)", code)
	}

	// Audit: writes with ids only, and the staff reads.
	for action, want := range map[string]int{"form_response.start": 1, "form_response.save_page": 3, "form_response.submit": 1,
		"form_responses.list": 2, "form_response.read": 1} {
		var n int
		_ = pool.QueryRow(ctx, `select count(*) from audit_events where action = $1`, action).Scan(&n)
		if n != want {
			t.Errorf("%d %s events, want %d", n, action, want)
		}
	}
	var leaked int
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where action like 'form_response%' and (detail::text like '%cough%' or detail::text like '%"16"%' or detail::text like '%value%')`).Scan(&leaked)
	if leaked != 0 {
		t.Errorf("%d form_response audit events carry answers or scores", leaked)
	}
}

// TestFormResponseWindow: after the window closes, saves and submit are
// 409 window_closed, and the runner expires the response.
func TestFormResponseWindow(t *testing.T) {
	f := newResponseFixture(t)
	ctx := context.Background()
	_, out, _ := f.phone.send("POST", "/responses", nil, []byte(`{"occurrence_id":"`+f.occurrence+`"}`))
	rid := out["id"].(string)
	if code, _ := f.savePage(t, rid, "p_intro", 0, uuid.NewString(), map[string]any{"consent": true, "age": 30}); code != http.StatusOK {
		t.Fatal("save")
	}
	if _, err := f.pool.Exec(ctx, `update form_occurrences set closes_at = now() - interval '1 minute' where id = $1`, f.occurrence); err != nil {
		t.Fatal(err)
	}
	if code, out := f.savePage(t, rid, "p_symptoms", 1, uuid.NewString(), map[string]any{"symptoms": []string{"none"}}); code != http.StatusConflict || out["code"] != "window_closed" {
		t.Errorf("save after close: %d %v", code, out)
	}
	if code, out, _ := f.phone.send("POST", "/responses/"+rid+"/submit", map[string]string{"Idempotency-Key": uuid.NewString()}, nil); code != http.StatusConflict || out["code"] != "window_closed" {
		t.Errorf("submit after close: %d %v", code, out)
	}
	if code, out, _ := f.phone.send("POST", "/responses", nil, []byte(`{"occurrence_id":"`+f.occurrence+`"}`)); code != http.StatusConflict || out["code"] != "window_closed" {
		t.Errorf("start after close: %d %v", code, out)
	}
	if _, err := f.s.runForms(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var rs, os string
	_ = f.pool.QueryRow(ctx, `select r.status, o.status from form_responses r join form_occurrences o on o.id = r.occurrence_id where r.id = $1`, rid).Scan(&rs, &os)
	if rs != "expired" || os != "missed" {
		t.Errorf("after the runner: response %s, occurrence %s", rs, os)
	}
	// Forms off: the patient routes are refused.
	f.adm.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"forms": false}})
	if code, out, _ := f.phone.send("POST", "/responses", nil, []byte(`{"occurrence_id":"`+f.occurrence+`"}`)); code != http.StatusForbidden || out["code"] != "feature_off" {
		t.Errorf("start with forms off: %d %v", code, out)
	}
}

// TestFormResponseNumbersExact: numeric answers keep the exact JSON text the
// app sent (no float64 round trip) on resume and in the staff view, and the
// revision is in the body of start, resume and page saves (the app reads the
// body, not the ETag).
func TestFormResponseNumbersExact(t *testing.T) {
	f := newResponseFixture(t)
	code, out, _ := f.phone.send("POST", "/responses", nil, []byte(`{"occurrence_id":"`+f.occurrence+`"}`))
	if code != http.StatusCreated || out["revision"] != float64(0) {
		t.Fatalf("start: %d %v", code, out)
	}
	rid := out["id"].(string)
	body := []byte(`{"answers":{"temp":38.50}}`)
	code, out, _ = f.phone.send("PUT", "/responses/"+rid+"/pages/p_fever", map[string]string{"If-Match": `"0"`, "Idempotency-Key": uuid.NewString()}, body)
	if code != http.StatusOK || out["revision"] != float64(1) {
		t.Fatalf("save: %d %v", code, out)
	}
	req := httptest.NewRequest("POST", "/api/v1/responses", strings.NewReader(`{"occurrence_id":"`+f.occurrence+`"}`))
	req.Header.Set("Authorization", "Bearer "+f.phone.token)
	req = req.WithContext(tunnel.WithViaRelay(req.Context()))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	raw := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(raw, `"temp":38.50`) || !strings.Contains(raw, `"revision":1`) {
		t.Errorf("resume body: %d %s", rec.Code, raw)
	}
}
