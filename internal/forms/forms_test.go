package forms

import (
	"bytes"
	"encoding/json"
	"errors"
	goparser "go/parser"
	"go/token"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const testdata = "../../testdata/forms"

func decodeNumbers(t testing.TB, raw []byte, v any) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		t.Fatal(err)
	}
}

func mustParseFile(t testing.TB, rel string) *Definition {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testdata, rel))
	if err != nil {
		t.Fatal(err)
	}
	def, err := Parse(raw)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return def
}

func TestValidDefinitions(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(testdata, "valid", "*.json"))
	if len(files) == 0 {
		t.Fatal("no valid definitions")
	}
	for _, f := range files {
		mustParseFile(t, filepath.Join("valid", filepath.Base(f)))
	}
}

type invalidCase struct {
	File   string `json:"file"`
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Shared bool   `json:"shared"`
}

func TestInvalidDefinitions(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(testdata, "invalid", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct{ Cases []invalidCase }
	decodeNumbers(t, raw, &manifest)
	listed := map[string]bool{}
	kinds := map[string]bool{}
	for _, c := range manifest.Cases {
		listed[c.File] = true
		t.Run(c.File, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(testdata, "invalid", c.File))
			if err != nil {
				t.Fatal(err)
			}
			_, err = Parse(raw)
			var fe *Error
			if !errors.As(err, &fe) {
				t.Fatalf("want an *Error, got %v", err)
			}
			if fe.Path != c.Path || fe.Kind != c.Kind {
				t.Fatalf("got %s (%s), want path %q kind %q", fe, fe.Kind, c.Path, c.Kind)
			}
			kinds[fe.Kind] = true
		})
	}
	files, _ := filepath.Glob(filepath.Join(testdata, "invalid", "*.json"))
	for _, f := range files {
		if b := filepath.Base(f); b != "cases.json" && !listed[b] {
			t.Errorf("%s isn't listed in cases.json", b)
		}
	}
	for _, k := range []string{KindJSON, KindSchema, KindDuplicate, KindReference, KindOrder, KindType, KindOperator, KindDepth, KindNodes, KindItems, KindRange} {
		if !kinds[k] {
			t.Errorf("no invalid case of kind %q", k)
		}
	}
}

func TestErrorMessageNamesPath(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(testdata, "invalid", "duplicate-option.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Parse(raw)
	want := `sections[0].pages[1].items[3].options: duplicate value "1"`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %s", err, want)
	}
}

type vectorFile struct {
	Description string       `json:"description"`
	Definition  string       `json:"definition"`
	Cases       []vectorCase `json:"cases"`
}

type vectorCase struct {
	Name    string                     `json:"name"`
	Today   string                     `json:"today"`
	Answers Answers                    `json:"answers"`
	Visible map[string]bool            `json:"visible"`
	Next    map[string]string          `json:"next"`
	Path    []string                   `json:"path"`
	Scores  map[string]json.RawMessage `json:"scores"`
	Error   string                     `json:"error"`
	Check   *struct {
		Answers json.RawMessage `json:"answers"`
		Errors  []struct {
			Item string `json:"item"`
			Code string `json:"code"`
		} `json:"errors"`
	} `json:"check"`
}

// canon re-encodes JSON so equal values compare equal as strings.
func canon(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var g any
	decodeNumbers(t, b, &g)
	b, _ = json.Marshal(g)
	return string(b)
}

func TestVectors(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(testdata, "vectors", "*.json"))
	if len(files) == 0 {
		t.Fatal("no vectors")
	}
	ops := map[string]bool{}
	policies := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var vf vectorFile
		decodeNumbers(t, raw, &vf)
		def := mustParseFile(t, vf.Definition)
		collectOps(def, ops, policies)
		for _, c := range vf.Cases {
			t.Run(filepath.Base(f)+"/"+c.Name, func(t *testing.T) { runCase(t, def, c) })
		}
	}
	for _, op := range []string{OpAnswer, OpScoreOf, OpScore, OpNum, OpStr, OpBool, OpEq, OpNe, OpLt, OpLe, OpGt, OpGe, OpIn, OpAnswered, OpAnd, OpOr, OpNot, OpSum, OpAdd, OpSub, OpMul, OpDiv, OpCount, OpRound} {
		if !ops[op] {
			t.Errorf("no vector definition uses operator %q", op)
		}
	}
	for _, p := range []string{MissingNull, MissingZero, MissingProrate} {
		if !policies[p] {
			t.Errorf("no vector definition uses missing policy %q", p)
		}
	}
}

func runCase(t *testing.T, def *Definition, c vectorCase) {
	answers := c.Answers
	if answers == nil {
		answers = Answers{}
	}
	res, err := Evaluate(def, answers)
	switch {
	case c.Error == "" && err != nil:
		t.Errorf("unexpected error: %v", err)
	case c.Error != "":
		var fe *Error
		if !errors.As(err, &fe) || fe.Kind != c.Error {
			t.Errorf("error = %v, want kind %q", err, c.Error)
		}
	}
	for id, want := range c.Visible {
		if got := res.Visible[id]; got != want {
			t.Errorf("visible[%s] = %v, want %v", id, got, want)
		}
		if got := Visible(def, answers, id); got != want {
			t.Errorf("Visible(%s) = %v, want %v", id, got, want)
		}
	}
	for id, want := range c.Next {
		if got := NextSection(def, answers, id); got != want {
			t.Errorf("NextSection(%s) = %q, want %q", id, got, want)
		}
	}
	if c.Path != nil && !reflect.DeepEqual(res.Path, c.Path) {
		t.Errorf("path = %v, want %v", res.Path, c.Path)
	}
	if c.Scores != nil {
		scores, _ := Scores(def, answers)
		if len(c.Scores) == 0 && len(scores) != 0 {
			t.Errorf("scores = %v, want none", scores)
		}
		for id, want := range c.Scores {
			s, ok := scores[id]
			if !ok {
				t.Errorf("no score %q", id)
				continue
			}
			var w any
			decodeNumbers(t, want, &w)
			if got, exp := canon(t, s), canon(t, w); got != exp {
				t.Errorf("score %s = %s, want %s", id, got, exp)
			}
		}
	}
	if c.Check != nil {
		today := c.Today
		if today == "" {
			today = "2026-11-16"
		}
		ch := CheckAnswersOn(def, answers, today)
		var wantAnswers any
		decodeNumbers(t, c.Check.Answers, &wantAnswers)
		if got, exp := canon(t, ch.Answers), canon(t, wantAnswers); got != exp {
			t.Errorf("checked answers = %s, want %s", got, exp)
		}
		var got, exp []string
		for _, e := range ch.Errors {
			got = append(got, e.Item+":"+e.Code)
		}
		for _, e := range c.Check.Errors {
			exp = append(exp, e.Item+":"+e.Code)
		}
		if strings.Join(got, ",") != strings.Join(exp, ",") {
			t.Errorf("check errors = %v, want %v", got, exp)
		}
	}
}

func collectOps(def *Definition, ops, policies map[string]bool) {
	var walk func(*Expr)
	walk = func(e *Expr) {
		if e == nil {
			return
		}
		ops[e.Op] = true
		for _, a := range e.Args {
			walk(a)
		}
	}
	for _, s := range def.Sections {
		for _, b := range s.Next {
			walk(b.When)
		}
		for _, p := range s.Pages {
			walk(p.EnableWhen)
			for _, it := range p.Items {
				walk(it.EnableWhen)
			}
		}
	}
	for _, s := range def.Scores {
		walk(s.Expr)
		policies[s.Missing.Policy] = true
	}
}

func TestSizeLimit(t *testing.T) {
	raw := mustRead(t, "valid/minimal.json")
	pad := MaxBytes - len(raw) + 1
	big := append(bytes.TrimRight(raw, "\n}"), []byte(`, "description": "`+strings.Repeat("x", pad)+`"}`)...)
	_, err := Parse(big)
	var fe *Error
	if !errors.As(err, &fe) || fe.Kind != KindSize {
		t.Fatalf("got %v, want a size error", err)
	}
	// Just under the limit is fine.
	ok := append(bytes.TrimRight(raw, "\n}"), []byte(`, "description": "`)...)
	ok = append(ok, []byte(strings.Repeat("x", MaxBytes-len(ok)-2)+`"}`)...)
	if len(ok) != MaxBytes {
		t.Fatalf("built %d bytes", len(ok))
	}
	if _, err := Parse(ok); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
}

func TestItemLimitExactly(t *testing.T) {
	var items []string
	for i := 0; i < MaxItems; i++ {
		items = append(items, `{"id":"x`+strconv.Itoa(i)+`","type":"display","text":"."}`)
	}
	raw := `{"format":"wehelp.form.v1","title":"t","sections":[{"id":"s","pages":[{"id":"p","items":[` + strings.Join(items, ",") + `]}]}]}`
	if _, err := Parse([]byte(raw)); err != nil {
		t.Fatalf("%d items: %v", MaxItems, err)
	}
}

func TestDepthLimitExactly(t *testing.T) {
	mustParseFile(t, "valid/depth-32.json")
}

func mustRead(t *testing.T, rel string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testdata, rel))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRoundHalfEven(t *testing.T) {
	for _, c := range []struct {
		in     string
		places int
		want   string
	}{
		{"0.5", 0, "0"}, {"1.5", 0, "2"}, {"2.5", 0, "2"}, {"-0.5", 0, "0"}, {"-1.5", 0, "-2"},
		{"-2.5", 0, "-2"}, {"2.4999999", 0, "2"}, {"1.005", 2, "1"}, {"1.015", 2, "1.02"},
		{"-1.015", 2, "-1.02"}, {"0.0000005", 6, "0"}, {"0.0000015", 6, "0.000002"}, {"123", 0, "123"},
	} {
		r, _ := new(big.Rat).SetString(c.in)
		if got := formatDecimal(roundHalfEven(r, c.places)); got != c.want {
			t.Errorf("round(%s, %d) = %s, want %s", c.in, c.places, got, c.want)
		}
	}
}

func TestValidDate(t *testing.T) {
	for s, want := range map[string]bool{
		"2024-02-29": true, "2023-02-29": false, "1900-02-29": false, "2000-02-29": true,
		"2026-04-31": false, "2026-12-31": true, "2026-1-01": false, "0000-01-01": false, "2026-11-16T00:00": false,
	} {
		if got := validDate(s); got != want {
			t.Errorf("validDate(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestCheckAnswersDefaultToday(t *testing.T) {
	def := mustParseFile(t, "valid/minimal.json")
	if ch := CheckAnswers(def, Answers{"q1": "0"}); len(ch.Errors) != 0 {
		t.Fatal(ch.Errors)
	}
}

// The package must stay usable from the Back Office, the data module and as
// the reference for the Swift walker: standard library only, and nothing
// that talks to a database or the network.
func TestNoServerDependencies(t *testing.T) {
	allowed := map[string]bool{
		"bytes": true, "encoding/json": true, "errors": true, "fmt": true, "io": true, "math": true,
		"math/big": true, "regexp": true, "sort": true, "strconv": true, "strings": true,
		"time": true, "unicode/utf8": true,
	}
	files, _ := filepath.Glob("*.go")
	var bad []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := goparser.ParseFile(token.NewFileSet(), f, nil, goparser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !allowed[p] {
				bad = append(bad, f+": "+p)
			}
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("imports outside the allowed standard library set: %v", bad)
	}
}

func FuzzParse(f *testing.F) {
	for _, dir := range []string{"valid", "invalid"} {
		files, _ := filepath.Glob(filepath.Join(testdata, dir, "*.json"))
		for _, file := range files {
			if raw, err := os.ReadFile(file); err == nil && len(raw) < 20000 {
				f.Add(raw)
			}
		}
	}
	answerSets := []Answers{
		{},
		{"q1": "1", "n": json.Number("2.5"), "i": json.Number("0"), "m": []any{"x", "z"}, "consent": true, "age": json.Number("40")},
		{"q1": json.Number("1"), "x": json.Number("999999999999"), "y": json.Number("0"), "symptoms": []any{"fever", 3}},
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		def, err := Parse(data)
		if err != nil {
			var fe *Error
			if !errors.As(err, &fe) {
				t.Fatalf("Parse returned a %T, not an *Error", err)
			}
			return
		}
		// Use the input itself as answers too, when it decodes as an object.
		sets := answerSets
		if a, err := ParseAnswers(data); err == nil {
			sets = append(sets, a)
		}
		for _, item := range def.allItems {
			sets = append(sets, Answers{item.ID: json.Number("1")}, Answers{item.ID: "1"}, Answers{item.ID: []any{"1"}})
		}
		for _, a := range sets {
			if _, err := Evaluate(def, a); err != nil {
				var fe *Error
				if !errors.As(err, &fe) || fe.Kind != KindOverflow {
					t.Fatalf("Evaluate: %v", err)
				}
			}
			CheckAnswersOn(def, a, "2026-11-16")
		}
	})
}
