package forms

import (
	"math"
	"math/big"
)

// Expr is one node of a rule or score expression tree. Op names the
// operator; the other fields hold its operands.
type Expr struct {
	Op   string
	Args []*Expr  // eq ne lt le gt ge in and or not sum add sub mul div round
	Ref  string   // answer, scoreOf, score, answered
	Refs []string // count
	Num  *big.Rat // num
	Str  string   // str
	Bool bool     // bool
}

// Operators.
const (
	OpAnswer   = "answer"
	OpScoreOf  = "scoreOf"
	OpScore    = "score"
	OpNum      = "num"
	OpStr      = "str"
	OpBool     = "bool"
	OpEq       = "eq"
	OpNe       = "ne"
	OpLt       = "lt"
	OpLe       = "le"
	OpGt       = "gt"
	OpGe       = "ge"
	OpIn       = "in"
	OpAnswered = "answered"
	OpAnd      = "and"
	OpOr       = "or"
	OpNot      = "not"
	OpSum      = "sum"
	OpAdd      = "add"
	OpSub      = "sub"
	OpMul      = "mul"
	OpDiv      = "div"
	OpCount    = "count"
	OpRound    = "round"
)

// pos orders items: page index across the form, then index on the page.
type pos struct{ page, item int }

func (a pos) less(b pos) bool {
	return a.page < b.page || (a.page == b.page && a.item < b.item)
}

func (a pos) max(b pos) pos {
	if a.less(b) {
		return b
	}
	return a
}

var (
	noDeps  = pos{-1, -1}
	openEnd = pos{math.MaxInt, math.MaxInt}
)

func (it *Item) pos() pos { return pos{it.page.index, it.index} }

// Static value kinds of expression nodes.
const (
	kNum  = "number"
	kStr  = "string"
	kBool = "boolean"
	kDate = "date"
	kSet  = "multiple-choice answer"
)

type typ struct {
	kind   string
	choice *Item // set when the value is a choice item's answer
	lit    bool  // a literal node
}

// ctx is where an expression appears.
type ctx struct {
	rule  bool      // a when/enableWhen rule (no arithmetic); otherwise a score
	limit pos       // referenced items must come strictly before this
	score *ScoreDef // the score being defined, in a score
	deps  pos       // latest item referenced so far
}

func (p *parser) expressions() error {
	def := p.def
	for i, s := range def.Scores {
		path := join(index("scores", i), "expr")
		c := &ctx{limit: openEnd, score: s, deps: noDeps}
		e, t, err := p.expr(path, p.rawScoreExpr[s], 1, c)
		if err != nil {
			return err
		}
		if t.kind != kNum {
			return errf(path, KindType, "a score must be a number, not a %s", t.kind)
		}
		s.Expr, s.maxDep = e, c.deps
	}
	for i, s := range def.Sections {
		sp := index("sections", i)
		for j, pg := range s.Pages {
			pp := index(join(sp, "pages"), j)
			if raw, ok := p.rawPageWhen[pg]; ok {
				e, err := p.rule(join(pp, "enableWhen"), raw, pos{pg.index, -1})
				if err != nil {
					return err
				}
				pg.EnableWhen = e
			}
			for k, it := range pg.Items {
				if raw, ok := p.rawItemWhen[it]; ok {
					e, err := p.rule(join(index(join(pp, "items"), k), "enableWhen"), raw, it.pos())
					if err != nil {
						return err
					}
					it.EnableWhen = e
				}
			}
		}
		last := s.Pages[len(s.Pages)-1].index
		for j, b := range s.Next {
			bp := index(join(sp, "next"), j)
			if raw, ok := p.rawNextWhen[b]; ok {
				e, err := p.rule(join(bp, "when"), raw, pos{last, math.MaxInt})
				if err != nil {
					return err
				}
				b.When = e
			}
			if b.Section == End {
				continue
			}
			target, ok := def.sections[b.Section]
			if !ok {
				return errf(join(bp, "section"), KindReference, "no section %q", b.Section)
			}
			if target.index <= s.index {
				return errf(join(bp, "section"), KindOrder, "section %q isn't after this one; next may only go forward (or to %q)", b.Section, End)
			}
		}
	}
	// Flags are checked on the whole submitted form, so they may use any
	// item or score.
	for i, fl := range def.Flags {
		e, err := p.rule(join(index("flags", i), "when"), p.rawFlagWhen[fl], openEnd)
		if err != nil {
			return err
		}
		fl.When = e
	}
	return nil
}

func (p *parser) rule(path string, raw any, limit pos) (*Expr, error) {
	e, t, err := p.expr(path, raw, 1, &ctx{rule: true, limit: limit, deps: noDeps})
	if err != nil {
		return nil, err
	}
	if t.kind != kBool {
		return nil, errf(path, KindType, "a rule must be true or false, not a %s", t.kind)
	}
	return e, nil
}

// itemRef resolves an item id used by an expression and checks it is
// answerable and comes before the expression.
func (p *parser) itemRef(path string, v any, c *ctx) (*Item, error) {
	id, ok := v.(string)
	if !ok {
		return nil, errf(path, KindSchema, "must be an item id")
	}
	it, ok := p.def.items[id]
	if !ok {
		return nil, errf(path, KindReference, "no item %q", id)
	}
	if it.Type == TypeDisplay {
		return nil, errf(path, KindType, "item %q is a display item and has no answer", id)
	}
	if !it.pos().less(c.limit) {
		return nil, errf(path, KindOrder, "item %q isn't earlier than this rule", id)
	}
	c.deps = c.deps.max(it.pos())
	return it, nil
}

func (p *parser) expr(path string, raw any, depth int, c *ctx) (*Expr, typ, error) {
	if depth > MaxDepth {
		return nil, typ{}, errf(path, KindDepth, "expression is more than %d deep", MaxDepth)
	}
	p.nodes++
	if p.nodes > MaxNodes {
		return nil, typ{}, errf(path, KindNodes, "more than %d expression nodes in the definition", MaxNodes)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, typ{}, errf(path, KindSchema, "an expression must be an object")
	}
	if len(m) != 1 {
		return nil, typ{}, errf(path, KindOperator, "an expression must have exactly one operator")
	}
	var op string
	var arg any
	for k, v := range m {
		op, arg = k, v
	}
	ap := join(path, op)
	e := &Expr{Op: op}

	// args decodes an operand list of at least min (and at most max, if >= 0) expressions.
	args := func(min, max int) ([]typ, error) {
		list, ok := arg.([]any)
		if !ok {
			return nil, errf(ap, KindSchema, "must be an array of expressions")
		}
		if len(list) < min || (max >= 0 && len(list) > max) {
			if min == max {
				return nil, errf(ap, KindOperator, "%s takes %d operands", op, min)
			}
			return nil, errf(ap, KindOperator, "%s takes at least %d operands", op, min)
		}
		var ts []typ
		for i, a := range list {
			ce, ct, err := p.expr(index(ap, i), a, depth+1, c)
			if err != nil {
				return nil, err
			}
			e.Args = append(e.Args, ce)
			ts = append(ts, ct)
		}
		return ts, nil
	}
	arith := func() error {
		if c.rule {
			return errf(ap, KindOperator, "arithmetic (%s) is only allowed in scores", op)
		}
		return nil
	}
	needKind := func(i int, t typ, kinds ...string) error {
		for _, k := range kinds {
			if t.kind == k {
				return nil
			}
		}
		return errf(index(ap, i), KindType, "%s can't take a %s here", op, t.kind)
	}

	switch op {
	case OpAnswer:
		it, err := p.itemRef(ap, arg, c)
		if err != nil {
			return nil, typ{}, err
		}
		e.Ref = it.ID
		return e, answerType(it), nil

	case OpScoreOf:
		it, err := p.itemRef(ap, arg, c)
		if err != nil {
			return nil, typ{}, err
		}
		if it.Type != TypeChoice {
			return nil, typ{}, errf(ap, KindType, "item %q isn't a choice item", it.ID)
		}
		for _, o := range it.Options {
			if o.Score == nil {
				return nil, typ{}, errf(ap, KindType, "item %q has options without a score", it.ID)
			}
		}
		e.Ref = it.ID
		return e, typ{kind: kNum}, nil

	case OpScore:
		id, ok := arg.(string)
		if !ok {
			return nil, typ{}, errf(ap, KindSchema, "must be a score id")
		}
		s, ok := p.def.scores[id]
		if !ok {
			return nil, typ{}, errf(ap, KindReference, "no score %q", id)
		}
		if c.score != nil {
			if s.index >= c.score.index {
				return nil, typ{}, errf(ap, KindOrder, "score %q isn't defined before this one (scores may only use earlier scores)", id)
			}
		} else if !s.maxDep.less(c.limit) {
			return nil, typ{}, errf(ap, KindOrder, "score %q depends on items that aren't earlier than this rule", id)
		}
		c.deps = c.deps.max(s.maxDep)
		e.Ref = id
		return e, typ{kind: kNum}, nil

	case OpNum:
		r, err := decimalValue(ap, arg)
		if err != nil {
			return nil, typ{}, err
		}
		e.Num = r
		return e, typ{kind: kNum, lit: true}, nil

	case OpStr:
		s, ok := arg.(string)
		if !ok {
			return nil, typ{}, errf(ap, KindSchema, "must be a string")
		}
		e.Str = s
		return e, typ{kind: kStr, lit: true}, nil

	case OpBool:
		b, ok := arg.(bool)
		if !ok {
			return nil, typ{}, errf(ap, KindSchema, "must be true or false")
		}
		e.Bool = b
		return e, typ{kind: kBool, lit: true}, nil

	case OpEq, OpNe, OpLt, OpLe, OpGt, OpGe:
		ts, err := args(2, 2)
		if err != nil {
			return nil, typ{}, err
		}
		ordered := op != OpEq && op != OpNe
		if err := p.comparable(ap, e.Args, ts, ordered); err != nil {
			return nil, typ{}, err
		}
		return e, typ{kind: kBool}, nil

	case OpIn:
		ts, err := args(2, -1)
		if err != nil {
			return nil, typ{}, err
		}
		subj := ts[0]
		var want string
		switch subj.kind {
		case kStr, kSet:
			want = kStr
		case kNum:
			want = kNum
		default:
			return nil, typ{}, errf(index(ap, 0), KindType, "in can't test a %s", subj.kind)
		}
		for i := 1; i < len(ts); i++ {
			if !ts[i].lit || ts[i].kind != want {
				return nil, typ{}, errf(index(ap, i), KindType, "in takes %s literals after its first operand", want)
			}
			if subj.choice != nil && !hasOption(subj.choice, e.Args[i].Str) {
				return nil, typ{}, errf(index(ap, i), KindReference, "item %q has no option %q", subj.choice.ID, e.Args[i].Str)
			}
		}
		return e, typ{kind: kBool}, nil

	case OpAnswered:
		it, err := p.itemRef(ap, arg, c)
		if err != nil {
			return nil, typ{}, err
		}
		e.Ref = it.ID
		return e, typ{kind: kBool}, nil

	case OpAnd, OpOr:
		ts, err := args(1, -1)
		if err != nil {
			return nil, typ{}, err
		}
		for i, t := range ts {
			if err := needKind(i, t, kBool); err != nil {
				return nil, typ{}, err
			}
		}
		return e, typ{kind: kBool}, nil

	case OpNot:
		ce, ct, err := p.expr(ap, arg, depth+1, c)
		if err != nil {
			return nil, typ{}, err
		}
		if ct.kind != kBool {
			return nil, typ{}, errf(ap, KindType, "not can't take a %s", ct.kind)
		}
		e.Args = []*Expr{ce}
		return e, typ{kind: kBool}, nil

	case OpSum, OpAdd, OpSub, OpMul, OpDiv:
		if err := arith(); err != nil {
			return nil, typ{}, err
		}
		min, max := 2, 2
		if op == OpSum {
			min, max = 1, -1
		}
		ts, err := args(min, max)
		if err != nil {
			return nil, typ{}, err
		}
		for i, t := range ts {
			if err := needKind(i, t, kNum); err != nil {
				return nil, typ{}, err
			}
		}
		return e, typ{kind: kNum}, nil

	case OpRound:
		if err := arith(); err != nil {
			return nil, typ{}, err
		}
		ts, err := args(2, 2)
		if err != nil {
			return nil, typ{}, err
		}
		if err := needKind(0, ts[0], kNum); err != nil {
			return nil, typ{}, err
		}
		pl := e.Args[1]
		if pl.Op != OpNum || !pl.Num.IsInt() || pl.Num.Sign() < 0 || pl.Num.Cmp(big.NewRat(6, 1)) > 0 {
			return nil, typ{}, errf(index(ap, 1), KindRange, "round places must be a num literal from 0 to 6")
		}
		return e, typ{kind: kNum}, nil

	case OpCount:
		if err := arith(); err != nil {
			return nil, typ{}, err
		}
		list, ok := arg.([]any)
		if !ok || len(list) == 0 {
			return nil, typ{}, errf(ap, KindSchema, "must be a non-empty array of item ids")
		}
		for i, v := range list {
			it, err := p.itemRef(index(ap, i), v, c)
			if err != nil {
				return nil, typ{}, err
			}
			e.Refs = append(e.Refs, it.ID)
		}
		return e, typ{kind: kNum}, nil
	}
	return nil, typ{}, errf(path, KindOperator, "unknown operator %q", op)
}

func answerType(it *Item) typ {
	switch it.Type {
	case TypeChoice:
		if it.Multiple {
			return typ{kind: kSet, choice: it}
		}
		return typ{kind: kStr, choice: it}
	case TypeBoolean:
		return typ{kind: kBool}
	case TypeInteger, TypeDecimal:
		return typ{kind: kNum}
	case TypeDate:
		return typ{kind: kDate}
	default:
		return typ{kind: kStr}
	}
}

func hasOption(it *Item, v string) bool {
	for _, o := range it.Options {
		if o.Value == v {
			return true
		}
	}
	return false
}

// comparable checks the two operands of a comparison. A string literal
// compared with a date must itself be a date; one compared with a choice
// answer must be one of its option values.
func (p *parser) comparable(ap string, args []*Expr, ts []typ, ordered bool) error {
	for i, t := range ts {
		if t.kind == kSet {
			return errf(index(ap, i), KindType, "compare a multiple-choice answer with in, not %s", args[0].Op)
		}
	}
	// A string literal next to a date is a date.
	for i := range ts {
		other := ts[1-i]
		if ts[i].lit && ts[i].kind == kStr && other.kind == kDate {
			if !validDate(args[i].Str) {
				return errf(index(ap, i), KindType, "%q isn't a date (YYYY-MM-DD)", args[i].Str)
			}
			ts[i].kind = kDate
		}
	}
	a, b := ts[0], ts[1]
	if a.kind != b.kind {
		return errf(index(ap, 1), KindType, "can't compare a %s with a %s", a.kind, b.kind)
	}
	if ordered && a.kind != kNum && a.kind != kDate {
		return errf(ap, KindType, "only numbers and dates can be ordered, not a %s", a.kind)
	}
	for i := range ts {
		other := ts[1-i]
		if other.choice != nil && ts[i].lit && !hasOption(other.choice, args[i].Str) {
			return errf(index(ap, i), KindReference, "item %q has no option %q", other.choice.ID, args[i].Str)
		}
	}
	return nil
}
