package forms

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
)

// Answers maps item ids to answer values as decoded from JSON (decode with
// ParseAnswers, or a json.Decoder with UseNumber, so numbers stay exact):
// a string (choice, date, text), a []any of strings (multiple choice), a
// bool, or a json.Number (integer, decimal). A null value is unanswered.
type Answers map[string]any

// ParseAnswers decodes a JSON object of answers, keeping numbers exact.
func ParseAnswers(raw []byte) (Answers, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var a Answers
	if err := d.Decode(&a); err != nil {
		return nil, fmt.Errorf("answers: %w", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("answers: trailing data")
	}
	return a, nil
}

// Reasons a score is null.
const (
	ReasonMissing      = "missing"          // an input is unanswered (or hidden)
	ReasonDivideByZero = "divideByZero"     // a div by zero
	ReasonBelowMin     = "belowMinAnswered" // fewer answers than the missing policy's minimum
	ReasonOverflow     = "overflow"         // a result beyond 128-bit bounds; Scores also returns an error
)

// Score is one computed score. Value is nil when the score is null, and
// Reason then says why. Band is the id of the band Value falls in, or "".
type Score struct {
	Value  *big.Rat
	Band   string
	Reason string
}

// Text is the score's canonical decimal string (rounded half-even to six
// places, no exponent, no trailing zeros), or "" when null.
func (s *Score) Text() string {
	if s.Value == nil {
		return ""
	}
	return formatDecimal(s.Value)
}

// MarshalJSON writes {"value": "12.5" | null, "band": "mild" | null, "reason": "..."}.
func (s *Score) MarshalJSON() ([]byte, error) {
	out := struct {
		Value  *string `json:"value"`
		Band   *string `json:"band"`
		Reason string  `json:"reason,omitempty"`
	}{Reason: s.Reason}
	if s.Value != nil {
		t := s.Text()
		out.Value = &t
	}
	if s.Band != "" {
		out.Band = &s.Band
	}
	return json.Marshal(out)
}

// Result is a whole evaluation of a definition against a set of answers.
type Result struct {
	// Visible is keyed by section, page and item id. A section is visible
	// when it is on the path; a page or item when its section is on the
	// path, its page's and its own enableWhen hold.
	Visible map[string]bool
	// Next is each section's next section id (or End).
	Next map[string]string
	// Path is the sections the walk visits, in order.
	Path []string
	// Scores is keyed by score id.
	Scores map[string]*Score
	// Answers are the effective answers: well-typed answers to visible items.
	Answers Answers
	// Flags are the ids of the definition's flags whose rule holds, in
	// definition order (#199). Staff-side only.
	Flags []string
}

// Evaluate walks the definition in document order. Rules only look back,
// so one pass decides every visibility: an answer counts only once its item
// is known to be visible, and a hidden item's answer is treated as missing
// everywhere after it. Answers that aren't well-typed (wrong JSON type,
// unknown option) count as unanswered; range checks are CheckAnswers' job.
// The error is non-nil only for arithmetic overflow.
func Evaluate(def *Definition, answers Answers) (*Result, error) {
	e := &evaluator{
		def:    def,
		eff:    map[string]value{},
		scores: map[string]*Score{},
	}
	res := &Result{
		Visible: map[string]bool{},
		Next:    map[string]string{},
		Scores:  map[string]*Score{},
		Answers: Answers{},
	}
	target := def.Sections[0].ID
	for _, s := range def.Sections {
		on := s.ID == target
		res.Visible[s.ID] = on
		for _, pg := range s.Pages {
			pv := on && e.test(pg.EnableWhen)
			res.Visible[pg.ID] = pv
			for _, it := range pg.Items {
				iv := pv && e.test(it.EnableWhen)
				res.Visible[it.ID] = iv
				if !iv {
					continue
				}
				if v, ok, _ := answerValue(it, answers[it.ID]); ok {
					e.eff[it.ID] = v
					res.Answers[it.ID] = answers[it.ID]
				}
			}
		}
		next := e.next(s)
		res.Next[s.ID] = next
		if on {
			res.Path = append(res.Path, s.ID)
			target = next
		}
	}
	for _, sd := range def.Scores {
		res.Scores[sd.ID] = e.score(sd)
	}
	res.Flags = []string{}
	for _, fl := range def.Flags {
		if e.test(fl.When) {
			res.Flags = append(res.Flags, fl.ID)
		}
	}
	return res, e.err
}

// Visible reports whether a section, page or item is shown for these answers.
func Visible(def *Definition, answers Answers, id string) bool {
	res, _ := Evaluate(def, answers)
	return res.Visible[id]
}

// NextSection returns the section after sectionID (or End) for these
// answers, or "" if there is no such section.
func NextSection(def *Definition, answers Answers, sectionID string) string {
	res, _ := Evaluate(def, answers)
	return res.Next[sectionID]
}

// Scores computes every score. A score is null (Value nil) with a Reason
// when its inputs are missing or it divides by zero. The error is an
// *Error of KindOverflow when any result exceeds the 128-bit bounds; such a
// score must not be stored.
func Scores(def *Definition, answers Answers) (map[string]*Score, error) {
	res, err := Evaluate(def, answers)
	return res.Scores, err
}

type value struct {
	null   bool
	reason string
	num    *big.Rat
	str    string
	b      bool
	set    []string
}

func null(reason string) value { return value{null: true, reason: reason} }

type evaluator struct {
	def    *Definition
	eff    map[string]value // effective answers so far
	scores map[string]*Score
	err    error
}

func (e *evaluator) next(s *Section) string {
	for _, b := range s.Next {
		if b.When == nil || e.test(b.When) {
			return b.Section
		}
	}
	if s.index+1 < len(e.def.Sections) {
		return e.def.Sections[s.index+1].ID
	}
	return End
}

func (e *evaluator) test(x *Expr) bool {
	if x == nil {
		return true
	}
	v := e.eval(x, nil)
	return !v.null && v.b
}

func (e *evaluator) score(sd *ScoreDef) *Score {
	if s, ok := e.scores[sd.ID]; ok {
		return s
	}
	v := e.eval(sd.Expr, &sd.Missing)
	s := &Score{}
	if v.null {
		s.Reason = v.reason
	} else {
		s.Value = v.num
		for _, b := range sd.Bands {
			if v.num.Cmp(b.Min) >= 0 {
				s.Band = b.ID
			}
		}
	}
	e.scores[sd.ID] = s
	return s
}

// bound checks an arithmetic result; out of bounds it records the error and
// yields a null overflow value.
func (e *evaluator) bound(r *big.Rat) value {
	if !inBounds(r) {
		if e.err == nil {
			e.err = &Error{Kind: KindOverflow, Msg: "a score result exceeds 128-bit numerator/denominator bounds"}
		}
		return null(ReasonOverflow)
	}
	return value{num: r}
}

func (e *evaluator) eval(x *Expr, policy *Missing) value {
	switch x.Op {
	case OpAnswer:
		if v, ok := e.eff[x.Ref]; ok {
			return v
		}
		return null(ReasonMissing)
	case OpScoreOf:
		v, ok := e.eff[x.Ref]
		if !ok {
			return null(ReasonMissing)
		}
		it := e.def.items[x.Ref]
		if !it.Multiple {
			return value{num: optionScore(it, v.str)}
		}
		acc := value{num: new(big.Rat)}
		for _, s := range v.set {
			acc = e.bound(new(big.Rat).Add(acc.num, optionScore(it, s)))
			if acc.null {
				return acc
			}
		}
		return acc
	case OpScore:
		s := e.score(e.def.scores[x.Ref])
		if s.Value == nil {
			return null(s.Reason)
		}
		return value{num: s.Value}
	case OpNum:
		return value{num: x.Num}
	case OpStr:
		return value{str: x.Str}
	case OpBool:
		return value{b: x.Bool}
	case OpEq, OpNe, OpLt, OpLe, OpGt, OpGe:
		a, b := e.eval(x.Args[0], policy), e.eval(x.Args[1], policy)
		if a.null || b.null {
			return value{b: false} // any comparison with a missing value is false
		}
		c := compare(a, b)
		var r bool
		switch x.Op {
		case OpEq:
			r = c == 0
		case OpNe:
			r = c != 0
		case OpLt:
			r = c < 0
		case OpLe:
			r = c <= 0
		case OpGt:
			r = c > 0
		case OpGe:
			r = c >= 0
		}
		return value{b: r}
	case OpIn:
		subj := e.eval(x.Args[0], policy)
		if subj.null {
			return value{b: false}
		}
		for _, lit := range x.Args[1:] {
			switch {
			case subj.num != nil:
				if subj.num.Cmp(lit.Num) == 0 {
					return value{b: true}
				}
			case subj.set != nil:
				for _, s := range subj.set {
					if s == lit.Str {
						return value{b: true}
					}
				}
			default:
				if subj.str == lit.Str {
					return value{b: true}
				}
			}
		}
		return value{b: false}
	case OpAnswered:
		_, ok := e.eff[x.Ref]
		return value{b: ok}
	case OpAnd:
		for _, a := range x.Args {
			if !e.truth(a, policy) {
				return value{b: false}
			}
		}
		return value{b: true}
	case OpOr:
		for _, a := range x.Args {
			if e.truth(a, policy) {
				return value{b: true}
			}
		}
		return value{b: false}
	case OpNot:
		return value{b: !e.truth(x.Args[0], policy)}
	case OpCount:
		n := 0
		for _, id := range x.Refs {
			if _, ok := e.eff[id]; ok {
				n++
			}
		}
		return value{num: new(big.Rat).SetInt64(int64(n))}
	case OpSum:
		return e.sum(x, policy)
	case OpAdd, OpSub, OpMul, OpDiv:
		a, b := e.eval(x.Args[0], policy), e.eval(x.Args[1], policy)
		if a.null {
			return a
		}
		if b.null {
			return b
		}
		switch x.Op {
		case OpAdd:
			return e.bound(new(big.Rat).Add(a.num, b.num))
		case OpSub:
			return e.bound(new(big.Rat).Sub(a.num, b.num))
		case OpMul:
			return e.bound(new(big.Rat).Mul(a.num, b.num))
		default:
			if b.num.Sign() == 0 {
				return null(ReasonDivideByZero)
			}
			return e.bound(new(big.Rat).Quo(a.num, b.num))
		}
	case OpRound:
		a := e.eval(x.Args[0], policy)
		if a.null {
			return a
		}
		return e.bound(roundHalfEven(a.num, int(x.Args[1].Num.Num().Int64())))
	}
	panic("forms: unvalidated operator " + strconv.Quote(x.Op)) // unreachable for a parsed Definition
}

func (e *evaluator) truth(x *Expr, policy *Missing) bool {
	v := e.eval(x, policy)
	return !v.null && v.b
}

// sum adds its operands under the score's missing-answer policy:
//   - null: any null operand makes the sum null (the first one's reason);
//   - zero: null operands count as 0, but fewer than minAnswered non-null
//     operands make it null (belowMinAnswered);
//   - prorate: fewer than minAnswered non-null operands make it null;
//     otherwise sum × operands ÷ answered (when any are missing), then
//     always rounded half-even to places.
//
// An overflow operand always makes the sum an overflow, whatever the policy.
func (e *evaluator) sum(x *Expr, policy *Missing) value {
	vals := make([]value, len(x.Args))
	for i, a := range x.Args {
		vals[i] = e.eval(a, policy)
	}
	p := Missing{Policy: MissingNull}
	if policy != nil {
		p = *policy
	}
	acc := value{num: new(big.Rat)}
	answered := 0
	for _, v := range vals {
		if v.null {
			if p.Policy == MissingNull || v.reason == ReasonOverflow {
				return v
			}
			continue
		}
		answered++
		acc = e.bound(new(big.Rat).Add(acc.num, v.num))
		if acc.null {
			return acc
		}
	}
	if answered < p.MinAnswered {
		return null(ReasonBelowMin)
	}
	if p.Policy != MissingProrate {
		return acc
	}
	if answered == 0 {
		return null(ReasonMissing)
	}
	if answered < len(vals) {
		scaled := e.bound(new(big.Rat).Mul(acc.num, new(big.Rat).SetInt64(int64(len(vals)))))
		if scaled.null {
			return scaled
		}
		acc = e.bound(new(big.Rat).Quo(scaled.num, new(big.Rat).SetInt64(int64(answered))))
		if acc.null {
			return acc
		}
	}
	return e.bound(roundHalfEven(acc.num, p.Places))
}

func optionScore(it *Item, v string) *big.Rat {
	for _, o := range it.Options {
		if o.Value == v {
			return o.Score
		}
	}
	return new(big.Rat) // unreachable: answers are checked against the options
}

func compare(a, b value) int {
	switch {
	case a.num != nil:
		return a.num.Cmp(b.num)
	case a.str != "" || b.str != "":
		switch {
		case a.str < b.str:
			return -1
		case a.str > b.str:
			return 1
		}
		return 0
	default: // booleans
		if a.b == b.b {
			return 0
		}
		return 1
	}
}

// answerValue converts a raw answer for it. ok is false when the answer is
// absent (nil, "", or an empty list) or not well-typed; code then names the
// problem ("" for absent).
func answerValue(it *Item, raw any) (v value, ok bool, code string) {
	if raw == nil {
		return value{}, false, ""
	}
	switch it.Type {
	case TypeDisplay:
		return value{}, false, CodeNotAnswerable
	case TypeChoice:
		if !it.Multiple {
			s, isStr := raw.(string)
			if !isStr {
				return value{}, false, CodeWrongType
			}
			if !hasOption(it, s) {
				return value{}, false, CodeUnknownOption
			}
			return value{str: s}, true, ""
		}
		list, isList := raw.([]any)
		if !isList {
			if ss, isStrs := raw.([]string); isStrs {
				for _, s := range ss {
					list = append(list, s)
				}
				isList = true
			}
		}
		if !isList {
			return value{}, false, CodeWrongType
		}
		if len(list) == 0 {
			return value{}, false, ""
		}
		seen := map[string]bool{}
		set := make([]string, 0, len(list))
		for _, x := range list {
			s, isStr := x.(string)
			if !isStr {
				return value{}, false, CodeWrongType
			}
			if !hasOption(it, s) {
				return value{}, false, CodeUnknownOption
			}
			if seen[s] {
				return value{}, false, CodeDuplicateOption
			}
			seen[s] = true
			set = append(set, s)
		}
		return value{set: set}, true, ""
	case TypeBoolean:
		b, isBool := raw.(bool)
		if !isBool {
			return value{}, false, CodeWrongType
		}
		return value{b: b}, true, ""
	case TypeInteger, TypeDecimal:
		text, isNum := numberText(raw)
		if !isNum {
			return value{}, false, CodeWrongType
		}
		if it.Type == TypeInteger && !intPattern.MatchString(text) {
			return value{}, false, CodeWrongType
		}
		r, good := parseDecimal(text)
		if !good {
			return value{}, false, CodeWrongType
		}
		return value{num: r}, true, ""
	case TypeDate:
		s, isStr := raw.(string)
		if !isStr || !validDate(s) {
			return value{}, false, CodeWrongType
		}
		return value{str: s}, true, ""
	case TypeText:
		s, isStr := raw.(string)
		if !isStr {
			return value{}, false, CodeWrongType
		}
		if s == "" {
			return value{}, false, ""
		}
		return value{str: s}, true, ""
	}
	return value{}, false, CodeWrongType
}

// numberText gives the decimal text of a JSON number answer. A float64 is
// written the way JavaScript's String(n) writes it for the values allowed
// here, so the Go and JavaScript evaluators agree.
func numberText(raw any) (string, bool) {
	switch n := raw.(type) {
	case json.Number:
		return n.String(), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return "", false
		}
		if n == 0 {
			return "0", true
		}
		return strconv.FormatFloat(n, 'f', -1, 64), true
	case int:
		return strconv.Itoa(n), true
	case int64:
		return strconv.FormatInt(n, 10), true
	}
	return "", false
}
