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
	for _, ty := range Types {
		for _, f := range ty.Off {
			if !IsFeature(f) {
				t.Errorf("%s turns off unknown feature %q", ty.ID, f)
			}
		}
	}
}

func TestEffective(t *testing.T) {
	nat, _ := Lookup("naturopath")
	if on := Effective(nat, nil); !on["messages"] || !on["store"] || !on["checkins"] {
		t.Errorf("naturopath defaults: %v", on)
	}
	therapy, _ := Lookup("therapy")
	if on := Effective(therapy, nil); on["store"] || on["checkins"] || !on["messages"] {
		t.Errorf("therapy defaults: %v", on)
	}
	if on := Effective(therapy, map[string]bool{"store": true, "messages": false, "bogus": true}); !on["store"] || on["messages"] || on["bogus"] {
		t.Errorf("overrides: %v", on)
	}
	salon, _ := Lookup("salon")
	if on := Effective(salon, map[string]bool{"checkins": true}); on["checkins"] {
		t.Error("a salon turned check-ins on")
	}
}
