package templates

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zacharyelston/wehelp/internal/forms"
)

// TestTemplates: each template parses, evaluates with no answers, and has a
// copy in testdata/forms/valid so the JavaScript evaluator checks it too.
func TestTemplates(t *testing.T) {
	if len(All()) != 3 {
		t.Fatalf("%d templates, want 3", len(All()))
	}
	for _, tpl := range All() {
		def, err := forms.Parse(tpl.Canonical)
		if err != nil {
			t.Fatalf("%s: %v", tpl.Key, err)
		}
		if _, err := forms.Evaluate(def, forms.Answers{}); err != nil {
			t.Errorf("%s: %v", tpl.Key, err)
		}
		if def.Copyright != "" || len(def.Flags) != 0 {
			t.Errorf("%s: a template carries a copyright or flags", tpl.Key)
		}
		raw, _ := files.ReadFile(tpl.Key + ".json")
		copy, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "forms", "valid", "template-"+tpl.Key+".json"))
		if err != nil || string(copy) != string(raw) {
			t.Errorf("testdata/forms/valid/template-%s.json differs from the embedded template (%v)", tpl.Key, err)
		}
	}
	if _, ok := Get("intake"); !ok {
		t.Error("Get(intake)")
	}
	if _, ok := Get("nope"); ok {
		t.Error("Get(nope)")
	}
}
