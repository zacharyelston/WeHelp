package practice

import (
	"strings"
	"testing"
)

func TestPresets(t *testing.T) {
	seen := map[string]bool{}
	for _, ty := range Types {
		if seen[ty.ID] || ty.ID == "" || ty.Label == "" {
			t.Errorf("bad or duplicate preset %+v", ty)
		}
		seen[ty.ID] = true
		w := ty.Words
		for _, s := range []string{w.Patient, w.Patients, w.Provider, w.Providers} {
			if s == "" || s != strings.ToLower(s) {
				t.Errorf("%s: words must be lower case and set: %+v", ty.ID, w)
			}
		}
		if got, ok := Lookup(ty.ID); !ok || got.ID != ty.ID {
			t.Errorf("lookup %s", ty.ID)
		}
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("unknown type found")
	}
	if Default.ID != "naturopath" {
		t.Errorf("default %s", Default.ID)
	}
	if salon, _ := Lookup("salon"); salon.Clinical {
		t.Error("salons are not clinical")
	}
}
