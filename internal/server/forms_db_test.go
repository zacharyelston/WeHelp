package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/seed"
)

func formFixture(t *testing.T, rel string) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "forms", rel))
	if err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(b)
}

// TestFormsLibrary is #194: the forms switch, admin-only changes on the
// LAN, drafts validated by internal/forms, immutable published versions
// (in the handler and at the trigger), version numbering, canonical
// import/export, built-ins refusing changes, archive, and audit.
func TestFormsLibrary(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)
	desk := webSignIn(t, h, pool, seed.OfficeEmail, seed.OfficePassword)
	var tenant uuid.UUID
	_ = pool.QueryRow(ctx, `select id from tenants where name = $1`, seed.DemoTenant).Scan(&tenant)

	// Off by default: every route is 403, for admins too.
	if code, out := adm.do("GET", "/forms", nil); code != http.StatusForbidden {
		t.Fatalf("forms off, list: %d %v", code, out)
	}
	if code, _ := adm.do("POST", "/forms", map[string]any{"name": "Intake"}); code != http.StatusForbidden {
		t.Errorf("forms off, create: %d, want 403", code)
	}
	if code, out := adm.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"forms": true}}); code != http.StatusOK {
		t.Fatalf("turn forms on: %d %v", code, out)
	}

	// Who may do what.
	if code, _ := desk.do("POST", "/forms", map[string]any{"name": "Intake"}); code != http.StatusForbidden {
		t.Errorf("front desk create: %d, want 403", code)
	}
	if code, _ := desk.do("GET", "/forms", nil); code != http.StatusOK {
		t.Errorf("front desk list: %d, want 200", code)
	}
	relay := &client{t: t, h: h, relay: true, cookies: adm.cookies, csrf: adm.csrf}
	if code, _ := relay.do("GET", "/forms", nil); code != http.StatusNotFound {
		t.Errorf("list through the Relay: %d, want 404", code)
	}
	if code, _ := relay.do("POST", "/forms", map[string]any{"name": "Intake"}); code != http.StatusNotFound {
		t.Errorf("create through the Relay: %d, want 404", code)
	}

	// Create: a starter draft without a definition, or the given one.
	code, out := adm.do("POST", "/forms", map[string]any{"name": "Starter"})
	if code != http.StatusCreated || out["version"] != float64(1) {
		t.Fatalf("create starter: %d %v", code, out)
	}
	code, out = adm.do("POST", "/forms", map[string]any{"name": "Intake", "definition": formFixture(t, "valid/minimal.json")})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	formID, v1 := out["form_id"].(string), out["version_id"].(string)
	if code, out := adm.do("POST", "/forms", map[string]any{"name": "Bad", "definition": formFixture(t, "invalid/dangling-answer.json")}); code != http.StatusUnprocessableEntity || out["kind"] != "reference" {
		t.Errorf("create invalid: %d %v", code, out)
	}
	if code, _ := adm.do("POST", "/forms", map[string]any{"name": ""}); code != http.StatusBadRequest {
		t.Errorf("create without a name: %d, want 400", code)
	}

	// Save: validated, 422 with the path.
	code, out = adm.do("PUT", "/form-versions/"+v1, formFixture(t, "invalid/duplicate-option.json"))
	if code != http.StatusUnprocessableEntity || out["path"] != "sections[0].pages[1].items[3].options" || out["kind"] != "duplicate" || out["code"] != "invalid_definition" {
		t.Errorf("save invalid: %d %v", code, out)
	}
	if code, out := adm.do("PUT", "/form-versions/"+v1, formFixture(t, "valid/branching.json")); code != http.StatusOK || out["version"] != float64(1) {
		t.Fatalf("save: %d %v", code, out)
	}
	if code, _ := desk.do("PUT", "/form-versions/"+v1, formFixture(t, "valid/branching.json")); code != http.StatusForbidden {
		t.Errorf("front desk save: %d, want 403", code)
	}
	code, out = desk.do("GET", "/form-versions/"+v1, nil)
	if code != http.StatusOK || out["status"] != "draft" || out["definition"].(map[string]any)["title"] != "Branching intake (synthetic)" {
		t.Errorf("read version: %d %v", code, out)
	}

	// Publish freezes it.
	if code, out := adm.do("POST", "/form-versions/"+v1+"/publish", nil); code != http.StatusOK {
		t.Fatalf("publish: %d %v", code, out)
	}
	if code, _ := adm.do("PUT", "/form-versions/"+v1, formFixture(t, "valid/minimal.json")); code != http.StatusConflict {
		t.Errorf("save a published version: %d, want 409", code)
	}
	if code, _ := adm.do("POST", "/form-versions/"+v1+"/publish", nil); code != http.StatusConflict {
		t.Errorf("publish twice: %d, want 409", code)
	}
	if _, err := pool.Exec(ctx, `update form_versions set definition = '{}' where id = $1`, v1); err == nil || !strings.Contains(err.Error(), "published") {
		t.Errorf("UPDATE of a published row: %v, want the trigger to refuse", err)
	}
	if _, err := pool.Exec(ctx, `delete from form_versions where id = $1`, v1); err == nil {
		t.Error("DELETE of a published row succeeded")
	}

	// New drafts get the next number; one draft at a time.
	code, out = adm.do("POST", "/forms/"+formID+"/draft", nil)
	if code != http.StatusCreated || out["version"] != float64(2) {
		t.Fatalf("new draft: %d %v", code, out)
	}
	v2 := out["id"].(string)
	if code, _ := adm.do("POST", "/forms/"+formID+"/draft", nil); code != http.StatusConflict {
		t.Errorf("second draft: %d, want 409", code)
	}
	if _, err := pool.Exec(ctx, `insert into form_versions (form_id, version, definition, digest) select form_id, 9, definition, digest from form_versions where id = $1`, v2); err == nil {
		t.Error("the database allowed a second draft")
	}
	code, out = adm.do("GET", "/forms/"+formID, nil)
	if vs, _ := out["versions"].([]any); code != http.StatusOK || len(vs) != 2 || out["form"].(map[string]any)["published"] != float64(1) {
		t.Errorf("form detail: %d %v", code, out)
	}

	// Export, import, export: the same bytes.
	exp1 := exportBytes(t, adm, v1)
	if !bytes.HasSuffix(exp1, []byte("}\n")) || bytes.Contains(exp1, []byte("\n  ")) {
		t.Errorf("export isn't canonical: %.80q", exp1)
	}
	code, out = adm.do("POST", "/forms/import", json.RawMessage(exp1))
	if code != http.StatusCreated {
		t.Fatalf("import: %d %v", code, out)
	}
	imported := out["version_id"].(string)
	if exp2 := exportBytes(t, adm, imported); !bytes.Equal(exp1, exp2) {
		t.Errorf("import/export round trip differs:\n%s\n%s", exp1, exp2)
	}
	if code, _ := adm.do("POST", "/forms/import?form_id="+formID, json.RawMessage(exp1)); code != http.StatusConflict {
		t.Errorf("import onto a form with a draft: %d, want 409", code)
	}
	if code, _ := adm.do("POST", "/form-versions/"+v2+"/publish", nil); code != http.StatusOK {
		t.Fatal("publish v2")
	}
	if code, out := adm.do("POST", "/forms/import?form_id="+formID, json.RawMessage(exp1)); code != http.StatusCreated || out["version"] != float64(3) {
		t.Errorf("import as a new draft: %d %v", code, out)
	}
	if code, out := adm.do("POST", "/forms/import", formFixture(t, "invalid/score-cycle.json")); code != http.StatusUnprocessableEntity || out["kind"] != "order" {
		t.Errorf("import invalid: %d %v", code, out)
	}

	// Built-in instruments refuse every change.
	var instForm, instVersion uuid.UUID
	if err := pool.QueryRow(ctx, `insert into forms (tenant_id, name, kind, instrument_key) values ($1, 'Synthetic instrument', 'instrument', 'synthetic') returning id`, tenant).Scan(&instForm); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `insert into form_versions (form_id, version, definition, digest) values ($1, 1, $2, 'x') returning id`, instForm, string(formFixture(t, "valid/minimal.json"))).Scan(&instVersion); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{
		{"PUT", "/form-versions/" + instVersion.String()},
		{"POST", "/form-versions/" + instVersion.String() + "/publish"},
		{"POST", "/forms/" + instForm.String() + "/draft"},
		{"POST", "/forms/" + instForm.String() + "/archive"},
		{"POST", "/forms/import?form_id=" + instForm.String()},
	} {
		if code, out := adm.do(c.method, c.path, formFixture(t, "valid/minimal.json")); code != http.StatusConflict {
			t.Errorf("instrument %s %s: %d %v, want 409", c.method, c.path, code, out)
		}
	}
	if code, _ := desk.do("GET", "/form-versions/"+instVersion.String(), nil); code != http.StatusOK {
		t.Errorf("read an instrument: %d", code)
	}

	// Archive.
	if code, _ := adm.do("POST", "/forms/"+formID+"/archive", nil); code != http.StatusOK {
		t.Errorf("archive: %d", code)
	}
	if code, _ := adm.do("POST", "/forms/"+formID+"/archive", nil); code != http.StatusConflict {
		t.Errorf("archive twice: %d, want 409", code)
	}
	if code, _ := adm.do("GET", "/forms/"+uuid.NewString(), nil); code != http.StatusNotFound {
		t.Errorf("unknown form: %d, want 404", code)
	}
	code, out = desk.do("GET", "/forms", nil)
	if list, _ := out["forms"].([]any); code != http.StatusOK || len(list) != 4 {
		t.Errorf("list: %d, %d forms, want 4", code, len(list))
	}

	// Audit: ids and version numbers, never the definition.
	for action, want := range map[string]int{"form.create": 2, "form_version.save": 1, "form_version.publish": 2,
		"form_version.draft": 1, "form.import": 2, "form.archive": 1} {
		var n int
		_ = pool.QueryRow(ctx, `select count(*) from audit_events where action = $1 and tenant_id = $2`, action, tenant).Scan(&n)
		if n != want {
			t.Errorf("%d %s events, want %d", n, action, want)
		}
	}
	var leaked int
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where action like 'form%' and detail::text like '%sections%'`).Scan(&leaked)
	if leaked != 0 {
		t.Errorf("%d form audit events carry the definition", leaked)
	}

	// Switching forms off again closes every route.
	if code, _ := adm.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"forms": false}}); code != http.StatusOK {
		t.Fatal("turn forms off")
	}
	if code, _ := adm.do("GET", "/form-versions/"+v1+"/export", nil); code != http.StatusForbidden {
		t.Errorf("export with forms off: %d, want 403", code)
	}
}

func exportBytes(t *testing.T, c *client, versionID string) []byte {
	t.Helper()
	code, _ := c.do("GET", "/form-versions/"+versionID+"/export", nil)
	if code != http.StatusOK {
		t.Fatalf("export %s: %d", versionID, code)
	}
	if cd := c.last.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("export Content-Disposition %q", cd)
	}
	b, err := io.ReadAll(c.last.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
