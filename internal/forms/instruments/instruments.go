// Package instruments holds the built-in standard instruments (#199):
// PHQ-9, PHQ-2, GAD-7 and GAD-2, as embedded wehelp.form.v1 definitions.
// They are synced into each practice as read-only kind=instrument forms
// (internal/server/instruments.go).
//
// # Fidelity
//
// Wording, answer options, order and recall period are as published on the
// Pfizer screener forms; wording.txt pins them and TestWording checks every
// definition against it. Option scores are 0 to 3; the total is their sum.
// Bands are the published ones (PHQ-9: 0-4 minimal, 5-9 mild, 10-14
// moderate, 15-19 moderately severe, 20-27 severe; GAD-7: 0-4, 5-9, 10-14,
// 15-21; PHQ-2 and GAD-2: 3 or more is a positive screen). The PHQ-9 and
// GAD-7 difficulty question is included, unscored, as published.
//
// Missing answers: every scored item is required, and a total with any item
// missing is null (the default missing policy). No proration is applied:
// the published scoring sums all items; a practice that wants a prorating
// rule must take it from the instrument's manual and the owner's approval.
//
// # Licence and attribution
//
// "No permission required to reproduce, translate, display or distribute."
// Each definition carries the attribution line in copyright and as a
// display item, as the published forms do.
//
// # LOINC
//
// Codes were checked on 2026-11-16 against the HL7 US Core STU7 PHQ-9
// Questionnaire example (panel 44249-1, items 44250-9 ... 44260-8, total
// 44261-6, difficulty 69722-7, answers LA6568-5 ... LA6571-9 and
// LA6572-7 ... LA6575-0), a PACIO GAD-7 Questionnaire (panel 69737-5, items
// 69725-0, 68509-9, 69733-4, 69734-2, 69735-9, 69689-8, 69736-7, total
// 70274-6) and an HL7 PHQ-2 example (panel 55757-9, total 55758-7).
// loinc.org itself refused automated access, so the LOINC release number
// isn't recorded; confirm against the current release before a field
// rollout. GAD-2 has no panel or total code here, and the GAD-7 difficulty
// question no code: none could be confirmed, and none is guessed.
//
// # The self-harm flag
//
// PHQ-9 item 9 above "Not at all" raises the staff-side flag self_harm.
// Nothing is shown to the patient: wording for patients, who monitors the
// flag and when, and the escalation steps are the owner's to decide
// (TODO(owner), see #199). Until then the instruments stay off on field
// installs.
package instruments

import (
	"embed"
	"fmt"
	"sort"

	"github.com/zacharyelston/wehelp/internal/forms"
)

//go:embed *.json
var files embed.FS

// Wording is the pinned published wording (see wording.txt).
//
//go:embed wording.txt
var Wording string

// Instrument is one built-in definition.
type Instrument struct {
	Key        string // phq9, phq2, gad7, gad2: the form's instrument_key
	Definition *forms.Definition
	Canonical  []byte // canonical JSON, as stored
	Digest     string
}

var all []Instrument

func init() {
	entries, err := files.ReadDir(".")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		raw, err := files.ReadFile(e.Name())
		if err != nil {
			panic(err)
		}
		def, err := forms.Parse(raw)
		if err != nil {
			panic(fmt.Sprintf("instruments: %s: %v", e.Name(), err))
		}
		canon, err := forms.Canonical(raw)
		if err != nil {
			panic(err)
		}
		key := e.Name()[:len(e.Name())-len(".json")]
		all = append(all, Instrument{Key: key, Definition: def, Canonical: canon, Digest: forms.Digest(canon)})
	}
	sort.Slice(all, func(i, j int) bool { return order(all[i].Key) < order(all[j].Key) })
}

func order(key string) int {
	for i, k := range []string{"phq9", "phq2", "gad7", "gad2"} {
		if k == key {
			return i
		}
	}
	return 99
}

// All returns the built-ins: PHQ-9, PHQ-2, GAD-7, GAD-2.
func All() []Instrument { return append([]Instrument(nil), all...) }
