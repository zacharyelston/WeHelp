package instruments

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zacharyelston/wehelp/internal/forms"
)

func wording(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(Wording, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("wording.txt: bad line %q", line)
		}
		out[k] = v
	}
	return out
}

// TestWording: every item, stem, answer option, difficulty question and
// attribution matches the pinned published wording, in order.
func TestWording(t *testing.T) {
	w := wording(t)
	freq := strings.Split(w["FREQ"], "|")
	diff := strings.Split(w["DIFFICULTY"], "|")
	want := map[string][]string{
		"phq9": {"phq9_1", "phq9_2", "phq9_3", "phq9_4", "phq9_5", "phq9_6", "phq9_7", "phq9_8", "phq9_9"},
		"phq2": {"phq9_1", "phq9_2"},
		"gad7": {"gad7_1", "gad7_2", "gad7_3", "gad7_4", "gad7_5", "gad7_6", "gad7_7"},
		"gad2": {"gad7_1", "gad7_2"},
	}
	stems := map[string]string{"phq9": "phq_stem", "phq2": "phq_stem", "gad7": "gad_stem", "gad2": "gad_stem"}
	if len(All()) != 4 {
		t.Fatalf("%d instruments, want 4", len(All()))
	}
	for _, in := range All() {
		def := in.Definition
		if def.Copyright != w["attribution"] {
			t.Errorf("%s: copyright %q", in.Key, def.Copyright)
		}
		var scored []*forms.Item
		var texts []string
		for _, it := range def.Items() {
			texts = append(texts, it.Text)
			if it.Type == forms.TypeChoice && len(it.Options) == 4 && it.Options[0].Score != nil {
				scored = append(scored, it)
			}
		}
		if texts[0] != w[stems[in.Key]] || texts[len(texts)-1] != w["attribution"] {
			t.Errorf("%s: stem or attribution display differs", in.Key)
		}
		if len(scored) != len(want[in.Key]) {
			t.Fatalf("%s: %d scored items, want %d", in.Key, len(scored), len(want[in.Key]))
		}
		for i, it := range scored {
			if it.Text != w[want[in.Key][i]] {
				t.Errorf("%s item %d: %q, published %q", in.Key, i+1, it.Text, w[want[in.Key][i]])
			}
			if !it.Required || it.Code == nil || it.Code.System != "http://loinc.org" {
				t.Errorf("%s item %d: required %v, code %+v", in.Key, i+1, it.Required, it.Code)
			}
			for j, o := range it.Options {
				if o.Text != freq[j] || o.Score == nil || o.Score.RatString() != []string{"0", "1", "2", "3"}[j] {
					t.Errorf("%s item %d option %d: %q score %s", in.Key, i+1, j, o.Text, o.Score.RatString())
				}
			}
		}
		if in.Key == "phq9" || in.Key == "gad7" {
			d := def.Item(in.Key + "_difficulty")
			if d == nil || d.Text != w["difficulty"] || d.Required {
				t.Fatalf("%s: difficulty item %+v", in.Key, d)
			}
			for j, o := range d.Options {
				if o.Text != diff[j] || o.Score != nil {
					t.Errorf("%s difficulty option %d: %q (scored: %v)", in.Key, j, o.Text, o.Score != nil)
				}
			}
		}
	}
}

// TestTestdataCopies: the shared vectors use copies in testdata/forms/valid,
// so the Swift and JavaScript evaluators test the very same definitions.
func TestTestdataCopies(t *testing.T) {
	for _, in := range All() {
		embedded, _ := files.ReadFile(in.Key + ".json")
		copy, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "forms", "valid", "instrument-"+in.Key+".json"))
		if err != nil || string(copy) != string(embedded) {
			t.Errorf("testdata/forms/valid/instrument-%s.json differs from the embedded definition (%v)", in.Key, err)
		}
	}
}

// TestSelfHarmFlag: only PHQ-9 has a flag, on item 9 above zero.
func TestSelfHarmFlag(t *testing.T) {
	for _, in := range All() {
		if in.Key != "phq9" {
			if len(in.Definition.Flags) != 0 {
				t.Errorf("%s has flags", in.Key)
			}
			continue
		}
		answers := forms.Answers{}
		for i := 1; i <= 9; i++ {
			answers["phq9_"+string(rune('0'+i))] = "0"
		}
		res, _ := forms.Evaluate(in.Definition, answers)
		if len(res.Flags) != 0 {
			t.Errorf("all zeros raised %v", res.Flags)
		}
		answers["phq9_9"] = "1"
		res, _ = forms.Evaluate(in.Definition, answers)
		if len(res.Flags) != 1 || res.Flags[0] != "self_harm" {
			t.Errorf("item 9 = 1 raised %v", res.Flags)
		}
	}
}
