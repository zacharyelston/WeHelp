package forms

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// TestSchemaMatchesValidator keeps schemas/wehelp.form.v1.json in step with
// the validator: the same item types, fields per type, controls, operators
// and missing policies.
func TestSchemaMatchesValidator(t *testing.T) {
	raw, err := os.ReadFile("../../schemas/wehelp.form.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
			OneOf      []struct {
				Required []string `json:"required"`
			} `json:"oneOf"`
		} `json:"$defs"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	sorted := func(s []string) []string { s = append([]string{}, s...); sort.Strings(s); return s }
	keys := func(m map[string]json.RawMessage) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		return sorted(out)
	}

	if got, want := keys(schema.Properties), sorted([]string{"format", "title", "locale", "description", "copyright", "code", "sections", "scores", "flags"}); !reflect.DeepEqual(got, want) {
		t.Errorf("top-level fields: schema %v, validator %v", got, want)
	}
	for typ, extra := range itemFields {
		def, ok := schema.Defs[typ+"Item"]
		if !ok {
			t.Errorf("schema has no %sItem", typ)
			continue
		}
		want := sorted(append(append([]string{}, commonItemFields...), extra...))
		if got := keys(def.Properties); !reflect.DeepEqual(got, want) {
			t.Errorf("%s fields: schema %v, validator %v", typ, got, want)
		}
		var control struct {
			Enum []string `json:"enum"`
		}
		_ = json.Unmarshal(def.Properties["control"], &control)
		if !reflect.DeepEqual(sorted(control.Enum), sorted(controls[typ])) && !(len(control.Enum) == 0 && len(controls[typ]) == 0) {
			t.Errorf("%s controls: schema %v, validator %v", typ, control.Enum, controls[typ])
		}
	}
	var ops []string
	for _, alt := range schema.Defs["expr"].OneOf {
		ops = append(ops, alt.Required...)
	}
	want := sorted([]string{OpAnswer, OpScoreOf, OpScore, OpNum, OpStr, OpBool, OpEq, OpNe, OpLt, OpLe, OpGt, OpGe, OpIn, OpAnswered, OpAnd, OpOr, OpNot, OpSum, OpAdd, OpSub, OpMul, OpDiv, OpCount, OpRound})
	if got := sorted(ops); !reflect.DeepEqual(got, want) {
		t.Errorf("operators: schema %v, validator %v", got, want)
	}
	var missing struct {
		Properties struct {
			Missing struct {
				Properties struct {
					Policy struct {
						Enum []string `json:"enum"`
					} `json:"policy"`
				} `json:"properties"`
			} `json:"missing"`
		} `json:"properties"`
	}
	scoreRaw, _ := json.Marshal(map[string]any{"properties": schema.Defs["score"].Properties})
	_ = json.Unmarshal(scoreRaw, &missing)
	if got := sorted(missing.Properties.Missing.Properties.Policy.Enum); !reflect.DeepEqual(got, sorted([]string{MissingNull, MissingZero, MissingProrate})) {
		t.Errorf("missing policies: schema %v", got)
	}
}
