package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestFormStartingPoints is #258: templates, duplicating a form, and
// copying a built-in into an editable custom form that keeps its
// attribution, each audited as form.create with copied_from; built-ins
// can't be copied where they aren't available.
func TestFormStartingPoints(t *testing.T) {
	f := newResponseFixture(t) // forms on; the branching form published
	s, pool, ctx := f.s, f.pool, context.Background()

	// Templates.
	code, out := f.adm.do("GET", "/form-templates", nil)
	if list, _ := out["templates"].([]any); code != http.StatusOK || len(list) != 3 {
		t.Fatalf("templates: %d %v", code, out)
	}
	if code, _ := f.ada.do("GET", "/form-templates", nil); code != http.StatusForbidden {
		t.Errorf("a provider lists templates: %d, want 403", code)
	}
	code, out = f.adm.do("POST", "/forms", map[string]any{"name": "Our intake", "template": "intake"})
	if code != http.StatusCreated {
		t.Fatalf("create from a template: %d %v", code, out)
	}
	_, ver := f.adm.do("GET", "/form-versions/"+out["version_id"].(string), nil)
	def := ver["definition"].(map[string]any)
	if def["title"] != "Our intake" || len(def["sections"].([]any)[0].(map[string]any)["pages"].([]any)) != 3 {
		t.Errorf("template draft: title %v", def["title"])
	}
	if code, _ := f.adm.do("POST", "/forms", map[string]any{"name": "X", "template": "nope"}); code != http.StatusBadRequest {
		t.Errorf("unknown template: %d, want 400", code)
	}
	if code, _ := f.adm.do("POST", "/forms", map[string]any{"name": "X", "template": "intake", "definition": formFixture(t, "valid/minimal.json")}); code != http.StatusBadRequest {
		t.Errorf("template and definition: %d, want 400", code)
	}

	// Duplicate a custom form: a new draft named "(copy)".
	code, out = f.adm.do("POST", "/forms/"+f.formID+"/copy", nil)
	if code != http.StatusCreated || out["name"] != "Branching intake (synthetic) (copy)" {
		t.Fatalf("duplicate: %d %v", code, out)
	}
	dupVersion := out["version_id"].(string)
	_, ver = f.adm.do("GET", "/form-versions/"+dupVersion, nil)
	if ver["status"] != "draft" || ver["kind"] != "custom" || ver["definition"].(map[string]any)["title"] != "Branching intake (synthetic)" {
		t.Errorf("duplicate draft: %v", ver)
	}
	if code, _ := f.adm.do("POST", "/forms/"+f.formID+"/copy", map[string]any{"version_id": f.versionID, "name": "Short"}); code != http.StatusCreated {
		t.Errorf("copy a named version: %d", code)
	}
	if code, _ := f.ada.do("POST", "/forms/"+f.formID+"/copy", nil); code != http.StatusForbidden {
		t.Errorf("a provider copies: %d, want 403", code)
	}

	// Built-ins: not on a field install; then copied into an editable form.
	s.cfg.FormsInstruments = true
	f.adm.do("GET", "/forms", nil) // syncs them
	var phq9 string
	_ = pool.QueryRow(ctx, `select id::text from forms where instrument_key = 'phq9'`).Scan(&phq9)
	s.cfg.FormsInstruments = false
	if code, _ := f.adm.do("POST", "/forms/"+phq9+"/copy", nil); code != http.StatusConflict {
		t.Errorf("copy a built-in on a field install: %d, want 409", code)
	}
	s.cfg.FormsInstruments = true
	code, out = f.adm.do("POST", "/forms/"+phq9+"/copy", nil)
	if code != http.StatusCreated || out["name"] != "PHQ-9 (copy)" {
		t.Fatalf("copy PHQ-9: %d %v", code, out)
	}
	copyVersion := out["version_id"].(string)
	_, ver = f.adm.do("GET", "/form-versions/"+copyVersion, nil)
	cdef := ver["definition"].(map[string]any)
	if ver["kind"] != "custom" || !strings.HasPrefix(cdef["description"].(string), "Copied from the built-in PHQ-9. Once edited, this is no longer the validated instrument") ||
		!strings.Contains(cdef["copyright"].(string), "Pfizer") || len(cdef["flags"].([]any)) != 1 {
		t.Errorf("PHQ-9 copy: kind %v, description %v", ver["kind"], cdef["description"])
	}
	cdef["title"] = "PHQ-9, our wording"
	raw, _ := json.Marshal(cdef)
	if code, out := f.adm.do("PUT", "/form-versions/"+copyVersion, json.RawMessage(raw)); code != http.StatusOK {
		t.Errorf("edit the copy: %d %v", code, out)
	}

	// Audit: copied_from on each.
	var templ, dup, inst int
	_ = pool.QueryRow(ctx, `select count(*) filter (where detail->'copied_from'->>'template' = 'intake'),
		count(*) filter (where detail->'copied_from'->>'form_id' = $1),
		count(*) filter (where detail->'copied_from'->>'instrument' = 'phq9')
		from audit_events where action = 'form.create'`, f.formID).Scan(&templ, &dup, &inst)
	if templ != 1 || dup != 2 || inst != 1 {
		t.Errorf("copied_from audits: template %d, duplicate %d, instrument %d", templ, dup, inst)
	}
}
