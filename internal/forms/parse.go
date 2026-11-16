package forms

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf8"
)

// Format is the only definition format this package reads.
const Format = "wehelp.form.v1"

// Limits on one definition (docs/V2_CONTRACTS.md, "Forms").
const (
	MaxBytes      = 200000 // UTF-8 bytes of the definition JSON
	MaxItems      = 500    // items across all pages, display items included
	MaxDepth      = 32     // nodes on the longest path of one expression tree
	MaxNodes      = 4096   // expression nodes across the whole definition
	MaxTextLength = 10000  // the largest maxLength a text item may declare
	// DefaultMaxLength applies to a text item that declares no maxLength.
	DefaultMaxLength = 4000
)

// End is the reserved next-section target that finishes the form.
const End = "end"

// Error kinds. Validation and evaluation errors carry one of these, so tests
// (Go, JavaScript, Swift) can compare kinds without comparing wording.
const (
	KindJSON      = "json"      // not JSON, or trailing data
	KindSize      = "size"      // definition over MaxBytes
	KindSchema    = "schema"    // missing, unknown or wrongly typed field
	KindDuplicate = "duplicate" // repeated id or option value
	KindReference = "reference" // id that doesn't resolve, or wrong kind of target
	KindOrder     = "order"     // rule or score looks forward, or a cycle
	KindType      = "type"      // expression operand of the wrong type
	KindOperator  = "operator"  // unknown operator, wrong arity, arithmetic in a rule
	KindDepth     = "depth"     // expression deeper than MaxDepth
	KindNodes     = "nodes"     // more than MaxNodes expression nodes
	KindItems     = "items"     // more than MaxItems items
	KindRange     = "range"     // number format, min > max, bad bounds
	KindOverflow  = "overflow"  // evaluation: a result beyond 128-bit bounds
)

// Error is a validation error. Path names the offending field, for example
// `sections[0].pages[1].items[2].options`.
type Error struct {
	Path string
	Kind string
	Msg  string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return e.Path + ": " + e.Msg
}

func errf(path, kind, format string, args ...any) *Error {
	return &Error{Path: path, Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// Coding is a code from a terminology, such as LOINC.
type Coding struct {
	System  string
	Code    string
	Display string
}

// Definition is a parsed, validated wehelp.form.v1 form definition. Treat it
// as read-only.
type Definition struct {
	Title       string
	Locale      string
	Description string
	Code        *Coding
	Sections    []*Section
	Scores      []*ScoreDef

	sections map[string]*Section
	pages    map[string]*Page
	items    map[string]*Item
	scores   map[string]*ScoreDef
	allItems []*Item // document order
}

// Section is a run of pages with one way in. Next picks the section after it.
type Section struct {
	ID    string
	Title string
	Pages []*Page
	Next  []*Branch

	index int
}

// Branch is one entry of a section's next list. A nil When is the default.
type Branch struct {
	When    *Expr
	Section string // a later section's id, or End
}

// Page is one screen.
type Page struct {
	ID         string
	Title      string
	EnableWhen *Expr
	Items      []*Item

	section *Section
	index   int // global page index, document order
}

// Item types.
const (
	TypeDisplay = "display"
	TypeChoice  = "choice"
	TypeBoolean = "boolean"
	TypeInteger = "integer"
	TypeDecimal = "decimal"
	TypeDate    = "date"
	TypeText    = "text"
)

// Item is one question or display block.
type Item struct {
	ID         string
	Type       string
	Text       string
	Code       *Coding
	Required   bool
	EnableWhen *Expr
	Control    string

	// choice
	Multiple bool
	Options  []*Option
	// integer, decimal (Min/Max/Step) and date (MinDate/MaxDate: a date or "today")
	Min, Max, Step     *big.Rat
	MinDate, MaxDate   string
	Unit               string
	MinLabel, MaxLabel string
	// text
	MaxLength int
	// display
	Media *Media

	page  *Page
	index int // within its page
	seq   int // document order across the form
}

// Option is one answer option of a choice item.
type Option struct {
	Value string
	Text  string
	Code  *Coding
	Score *big.Rat // nil when the option has no score
}

// Media is an image or video shipped with the form version, named by asset.
type Media struct {
	Kind  string // "image" or "video"
	Asset string
	Alt   string
}

// ScoreDef is a computed score.
type ScoreDef struct {
	ID        string
	Text      string
	Code      *Coding
	Expr      *Expr
	Missing   Missing
	Bands     []*Band
	ShowScore bool

	index  int
	maxDep pos // the latest item the score depends on, transitively
}

// Missing-answer policies for the sum nodes of a score.
const (
	MissingNull    = "null"
	MissingZero    = "zero"
	MissingProrate = "prorate"
)

// Missing is a score's missing-answer policy. It governs every sum node in
// the score's expression.
type Missing struct {
	Policy      string // MissingNull (default), MissingZero or MissingProrate
	MinAnswered int    // 0 = no minimum (null, zero); >= 1 for prorate
	Places      int    // prorate: final half-even rounding
}

// Band is a labelled range of a score: from Min (inclusive) up to the next
// band's Min.
type Band struct {
	Min  *big.Rat
	ID   string
	Text string
}

var (
	idPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
	assetPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	localePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
)

// Parse decodes and validates a wehelp.form.v1 definition. It returns the
// first problem found, as an *Error.
func Parse(raw []byte) (*Definition, error) {
	if len(raw) > MaxBytes {
		return nil, errf("", KindSize, "definition is %d bytes, over the %d limit", len(raw), MaxBytes)
	}
	if !utf8.Valid(raw) {
		return nil, errf("", KindJSON, "definition is not valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var doc any
	if err := d.Decode(&doc); err != nil {
		return nil, errf("", KindJSON, "invalid JSON: %v", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errf("", KindJSON, "invalid JSON: trailing data")
	}
	p := &parser{def: &Definition{
		sections: map[string]*Section{},
		pages:    map[string]*Page{},
		items:    map[string]*Item{},
		scores:   map[string]*ScoreDef{},
	}}
	if err := p.document(doc); err != nil {
		return nil, err
	}
	return p.def, nil
}

type parser struct {
	def   *Definition
	ids   map[string]string // id -> path where it was declared
	nodes int

	// raw expressions, parsed once every id is known
	rawPageWhen  map[*Page]any
	rawItemWhen  map[*Item]any
	rawNextWhen  map[*Branch]any
	rawScoreExpr map[*ScoreDef]any
}

// --- generic JSON helpers ---------------------------------------------------

type obj struct {
	path string
	m    map[string]any
}

func object(path string, v any, allowed ...string) (*obj, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errf(path, KindSchema, "must be an object")
	}
	known := map[string]bool{}
	for _, k := range allowed {
		known[k] = true
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !known[k] {
			return nil, errf(join(path, k), KindSchema, "unknown field")
		}
	}
	return &obj{path: path, m: m}, nil
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func index(path string, i int) string { return path + "[" + strconv.Itoa(i) + "]" }

func (o *obj) has(k string) bool { _, ok := o.m[k]; return ok }

func (o *obj) str(k string, required bool) (string, error) {
	v, ok := o.m[k]
	if !ok {
		if required {
			return "", errf(join(o.path, k), KindSchema, "required")
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", errf(join(o.path, k), KindSchema, "must be a string")
	}
	if required && s == "" {
		return "", errf(join(o.path, k), KindSchema, "must not be empty")
	}
	return s, nil
}

func (o *obj) boolean(k string) (bool, error) {
	v, ok := o.m[k]
	if !ok {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, errf(join(o.path, k), KindSchema, "must be true or false")
	}
	return b, nil
}

func (o *obj) array(k string, required bool) ([]any, error) {
	v, ok := o.m[k]
	if !ok {
		if required {
			return nil, errf(join(o.path, k), KindSchema, "required")
		}
		return nil, nil
	}
	a, ok := v.([]any)
	if !ok {
		return nil, errf(join(o.path, k), KindSchema, "must be an array")
	}
	if required && len(a) == 0 {
		return nil, errf(join(o.path, k), KindSchema, "must not be empty")
	}
	return a, nil
}

// number reads a JSON number field as an exact decimal.
func (o *obj) number(k string) (*big.Rat, error) {
	v, ok := o.m[k]
	if !ok {
		return nil, nil
	}
	return decimalValue(join(o.path, k), v)
}

func decimalValue(path string, v any) (*big.Rat, error) {
	n, ok := v.(json.Number)
	if !ok {
		return nil, errf(path, KindSchema, "must be a number")
	}
	r, ok := parseDecimal(n.String())
	if !ok {
		return nil, errf(path, KindRange, "number %s must have at most 12 integer and 6 fractional digits, and no exponent", n)
	}
	return r, nil
}

func (o *obj) integer(k string, lo, hi int) (int, bool, error) {
	v, ok := o.m[k]
	if !ok {
		return 0, false, nil
	}
	n, ok := v.(json.Number)
	if !ok || !intPattern.MatchString(n.String()) {
		return 0, false, errf(join(o.path, k), KindSchema, "must be an integer")
	}
	i, err := strconv.Atoi(n.String())
	if err != nil || i < lo || i > hi {
		return 0, false, errf(join(o.path, k), KindRange, "must be between %d and %d", lo, hi)
	}
	return i, true, nil
}

func (o *obj) coding(k string) (*Coding, error) {
	v, ok := o.m[k]
	if !ok {
		return nil, nil
	}
	c, err := object(join(o.path, k), v, "system", "code", "display")
	if err != nil {
		return nil, err
	}
	out := &Coding{}
	if out.System, err = c.str("system", true); err != nil {
		return nil, err
	}
	if out.Code, err = c.str("code", true); err != nil {
		return nil, err
	}
	if out.Display, err = c.str("display", false); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *parser) declare(path, id string) error {
	if !idPattern.MatchString(id) {
		return errf(path, KindSchema, "id %q must start with a letter and use only letters, digits, '_', '.' and '-' (at most 64)", id)
	}
	if id == End {
		return errf(path, KindSchema, "id %q is reserved", End)
	}
	if prev, ok := p.ids[id]; ok {
		return errf(path, KindDuplicate, "duplicate id %q (first at %s)", id, prev)
	}
	p.ids[id] = path
	return nil
}

// --- the document -----------------------------------------------------------

func (p *parser) document(doc any) error {
	o, err := object("", doc, "format", "title", "locale", "description", "code", "sections", "scores")
	if err != nil {
		if e, ok := err.(*Error); ok && e.Path == "" {
			e.Msg = "definition must be a JSON object"
		}
		return err
	}
	format, err := o.str("format", true)
	if err != nil {
		return err
	}
	if format != Format {
		return errf("format", KindSchema, "must be %q", Format)
	}
	def := p.def
	if def.Title, err = o.str("title", true); err != nil {
		return err
	}
	if def.Locale, err = o.str("locale", false); err != nil {
		return err
	}
	if def.Locale != "" && !localePattern.MatchString(def.Locale) {
		return errf("locale", KindSchema, "must be a language tag such as en-US")
	}
	if def.Description, err = o.str("description", false); err != nil {
		return err
	}
	if def.Code, err = o.coding("code"); err != nil {
		return err
	}
	p.ids = map[string]string{}
	p.rawPageWhen = map[*Page]any{}
	p.rawItemWhen = map[*Item]any{}
	p.rawNextWhen = map[*Branch]any{}
	p.rawScoreExpr = map[*ScoreDef]any{}

	sections, err := o.array("sections", true)
	if err != nil {
		return err
	}
	pageIndex := 0
	for i, sv := range sections {
		s, err := p.section(index("sections", i), sv, i, &pageIndex)
		if err != nil {
			return err
		}
		def.Sections = append(def.Sections, s)
	}
	if len(def.allItems) > MaxItems {
		return errf("sections", KindItems, "%d items, over the %d limit", len(def.allItems), MaxItems)
	}
	scores, err := o.array("scores", false)
	if err != nil {
		return err
	}
	for i, sv := range scores {
		s, err := p.scoreDef(index("scores", i), sv, i)
		if err != nil {
			return err
		}
		def.Scores = append(def.Scores, s)
	}
	return p.expressions()
}

func (p *parser) section(path string, v any, i int, pageIndex *int) (*Section, error) {
	o, err := object(path, v, "id", "title", "pages", "next")
	if err != nil {
		return nil, err
	}
	s := &Section{index: i}
	if s.ID, err = o.str("id", true); err != nil {
		return nil, err
	}
	if err := p.declare(join(path, "id"), s.ID); err != nil {
		return nil, err
	}
	p.def.sections[s.ID] = s
	if s.Title, err = o.str("title", false); err != nil {
		return nil, err
	}
	pages, err := o.array("pages", true)
	if err != nil {
		return nil, err
	}
	for j, pv := range pages {
		pg, err := p.page(index(join(path, "pages"), j), pv, s, *pageIndex)
		if err != nil {
			return nil, err
		}
		*pageIndex++
		s.Pages = append(s.Pages, pg)
	}
	next, err := o.array("next", false)
	if err != nil {
		return nil, err
	}
	for j, nv := range next {
		bp := index(join(path, "next"), j)
		bo, err := object(bp, nv, "when", "section")
		if err != nil {
			return nil, err
		}
		b := &Branch{}
		if b.Section, err = bo.str("section", true); err != nil {
			return nil, err
		}
		if bo.has("when") {
			p.rawNextWhen[b] = bo.m["when"]
		} else if j != len(next)-1 {
			return nil, errf(bp, KindSchema, "an entry without when is the default and must come last")
		}
		s.Next = append(s.Next, b)
	}
	return s, nil
}

func (p *parser) page(path string, v any, s *Section, gi int) (*Page, error) {
	o, err := object(path, v, "id", "title", "enableWhen", "items")
	if err != nil {
		return nil, err
	}
	pg := &Page{section: s, index: gi}
	if pg.ID, err = o.str("id", true); err != nil {
		return nil, err
	}
	if err := p.declare(join(path, "id"), pg.ID); err != nil {
		return nil, err
	}
	p.def.pages[pg.ID] = pg
	if pg.Title, err = o.str("title", false); err != nil {
		return nil, err
	}
	if o.has("enableWhen") {
		p.rawPageWhen[pg] = o.m["enableWhen"]
	}
	items, err := o.array("items", true)
	if err != nil {
		return nil, err
	}
	for k, iv := range items {
		if len(p.def.allItems) >= MaxItems {
			return nil, errf(index(join(path, "items"), k), KindItems, "more than %d items", MaxItems)
		}
		it, err := p.item(index(join(path, "items"), k), iv, pg, k)
		if err != nil {
			return nil, err
		}
		pg.Items = append(pg.Items, it)
	}
	return pg, nil
}

// Fields every item type may carry.
var commonItemFields = []string{"id", "type", "text", "code", "required", "enableWhen", "control"}

var itemFields = map[string][]string{
	TypeDisplay: {"media"},
	TypeChoice:  {"multiple", "options"},
	TypeBoolean: {},
	TypeInteger: {"min", "max", "step", "unit", "minLabel", "maxLabel"},
	TypeDecimal: {"min", "max", "step", "unit", "minLabel", "maxLabel"},
	TypeDate:    {"min", "max"},
	TypeText:    {"maxLength"},
}

var controls = map[string][]string{
	TypeDisplay: {},
	TypeChoice:  {"radio", "dropdown", "buttons"},
	TypeBoolean: {"toggle", "buttons"},
	TypeInteger: {"keypad", "slider"},
	TypeDecimal: {"keypad", "slider"},
	TypeDate:    {},
	TypeText:    {"long"},
}

func (p *parser) item(path string, v any, pg *Page, k int) (*Item, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errf(path, KindSchema, "must be an object")
	}
	typ, ok := m["type"].(string)
	if !ok {
		if _, present := m["type"]; !present {
			return nil, errf(join(path, "type"), KindSchema, "required")
		}
		return nil, errf(join(path, "type"), KindSchema, "must be a string")
	}
	extra, known := itemFields[typ]
	if !known {
		return nil, errf(join(path, "type"), KindSchema, "unknown item type %q", typ)
	}
	o, err := object(path, v, append(append([]string{}, commonItemFields...), extra...)...)
	if err != nil {
		return nil, err
	}
	it := &Item{Type: typ, page: pg, index: k, seq: len(p.def.allItems)}
	if it.ID, err = o.str("id", true); err != nil {
		return nil, err
	}
	if err := p.declare(join(path, "id"), it.ID); err != nil {
		return nil, err
	}
	p.def.items[it.ID] = it
	p.def.allItems = append(p.def.allItems, it)
	if it.Text, err = o.str("text", typ != TypeDisplay); err != nil {
		return nil, err
	}
	if it.Code, err = o.coding("code"); err != nil {
		return nil, err
	}
	if it.Required, err = o.boolean("required"); err != nil {
		return nil, err
	}
	if it.Required && typ == TypeDisplay {
		return nil, errf(join(path, "required"), KindSchema, "a display item can't be required")
	}
	if o.has("enableWhen") {
		p.rawItemWhen[it] = o.m["enableWhen"]
	}
	if it.Control, err = o.str("control", false); err != nil {
		return nil, err
	}
	if it.Control != "" && !contains(controls[typ], it.Control) {
		return nil, errf(join(path, "control"), KindSchema, "control %q isn't allowed on a %s item", it.Control, typ)
	}

	switch typ {
	case TypeDisplay:
		if o.has("media") {
			mo, err := object(join(path, "media"), o.m["media"], "type", "asset", "alt")
			if err != nil {
				return nil, err
			}
			md := &Media{}
			if md.Kind, err = mo.str("type", true); err != nil {
				return nil, err
			}
			if md.Kind != "image" && md.Kind != "video" {
				return nil, errf(join(path, "media.type"), KindSchema, "must be image or video")
			}
			if md.Asset, err = mo.str("asset", true); err != nil {
				return nil, err
			}
			if !assetPattern.MatchString(md.Asset) {
				return nil, errf(join(path, "media.asset"), KindSchema, "must name an asset of this form version, not a URL or path")
			}
			if md.Alt, err = mo.str("alt", false); err != nil {
				return nil, err
			}
			it.Media = md
		}
		if it.Text == "" && it.Media == nil {
			return nil, errf(join(path, "text"), KindSchema, "a display item needs text or media")
		}
	case TypeChoice:
		if it.Multiple, err = o.boolean("multiple"); err != nil {
			return nil, err
		}
		opts, err := o.array("options", true)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for j, ov := range opts {
			op := index(join(path, "options"), j)
			oo, err := object(op, ov, "value", "text", "code", "score")
			if err != nil {
				return nil, err
			}
			opt := &Option{}
			if opt.Value, err = oo.str("value", true); err != nil {
				return nil, err
			}
			if utf8.RuneCountInString(opt.Value) > 64 {
				return nil, errf(join(op, "value"), KindSchema, "at most 64 characters")
			}
			if seen[opt.Value] {
				return nil, errf(join(path, "options"), KindDuplicate, "duplicate value %q", opt.Value)
			}
			seen[opt.Value] = true
			if opt.Text, err = oo.str("text", true); err != nil {
				return nil, err
			}
			if opt.Code, err = oo.coding("code"); err != nil {
				return nil, err
			}
			if opt.Score, err = oo.number("score"); err != nil {
				return nil, err
			}
			it.Options = append(it.Options, opt)
		}
	case TypeInteger, TypeDecimal:
		for _, f := range []struct {
			key string
			dst **big.Rat
		}{{"min", &it.Min}, {"max", &it.Max}, {"step", &it.Step}} {
			r, err := o.number(f.key)
			if err != nil {
				return nil, err
			}
			if r != nil && typ == TypeInteger && !r.IsInt() {
				return nil, errf(join(path, f.key), KindRange, "must be a whole number on an integer item")
			}
			*f.dst = r
		}
		if it.Min != nil && it.Max != nil && it.Min.Cmp(it.Max) > 0 {
			return nil, errf(join(path, "min"), KindRange, "min is greater than max")
		}
		if it.Step != nil && it.Step.Sign() <= 0 {
			return nil, errf(join(path, "step"), KindRange, "must be greater than zero")
		}
		if it.Control == "slider" && (it.Min == nil || it.Max == nil) {
			return nil, errf(join(path, "control"), KindSchema, "a slider needs min and max")
		}
		if it.Unit, err = o.str("unit", false); err != nil {
			return nil, err
		}
		if it.MinLabel, err = o.str("minLabel", false); err != nil {
			return nil, err
		}
		if it.MaxLabel, err = o.str("maxLabel", false); err != nil {
			return nil, err
		}
	case TypeDate:
		for _, f := range []struct {
			key string
			dst *string
		}{{"min", &it.MinDate}, {"max", &it.MaxDate}} {
			s, err := o.str(f.key, false)
			if err != nil {
				return nil, err
			}
			if o.has(f.key) && s != "today" && !validDate(s) {
				return nil, errf(join(path, f.key), KindRange, "must be a date (YYYY-MM-DD) or \"today\"")
			}
			*f.dst = s
		}
		if it.MinDate != "" && it.MaxDate != "" && it.MinDate != "today" && it.MaxDate != "today" && it.MinDate > it.MaxDate {
			return nil, errf(join(path, "min"), KindRange, "min is after max")
		}
	case TypeText:
		n, ok, err := o.integer("maxLength", 1, MaxTextLength)
		if err != nil {
			return nil, err
		}
		it.MaxLength = DefaultMaxLength
		if ok {
			it.MaxLength = n
		}
	}
	return it, nil
}

func (p *parser) scoreDef(path string, v any, i int) (*ScoreDef, error) {
	o, err := object(path, v, "id", "text", "code", "expr", "missing", "bands", "showScore")
	if err != nil {
		return nil, err
	}
	s := &ScoreDef{index: i, Missing: Missing{Policy: MissingNull}}
	if s.ID, err = o.str("id", true); err != nil {
		return nil, err
	}
	if err := p.declare(join(path, "id"), s.ID); err != nil {
		return nil, err
	}
	if s.Text, err = o.str("text", false); err != nil {
		return nil, err
	}
	if s.Code, err = o.coding("code"); err != nil {
		return nil, err
	}
	if s.ShowScore, err = o.boolean("showScore"); err != nil {
		return nil, err
	}
	if !o.has("expr") {
		return nil, errf(join(path, "expr"), KindSchema, "required")
	}
	p.rawScoreExpr[s] = o.m["expr"]
	if o.has("missing") {
		mp := join(path, "missing")
		mo, err := object(mp, o.m["missing"], "policy", "minAnswered", "places")
		if err != nil {
			return nil, err
		}
		if s.Missing.Policy, err = mo.str("policy", true); err != nil {
			return nil, err
		}
		switch s.Missing.Policy {
		case MissingNull, MissingZero, MissingProrate:
		default:
			return nil, errf(join(mp, "policy"), KindSchema, "must be null, zero or prorate")
		}
		minA, hasMin, err := mo.integer("minAnswered", 1, MaxItems)
		if err != nil {
			return nil, err
		}
		places, hasPlaces, err := mo.integer("places", 0, 6)
		if err != nil {
			return nil, err
		}
		switch s.Missing.Policy {
		case MissingProrate:
			if !hasMin {
				return nil, errf(join(mp, "minAnswered"), KindSchema, "required for prorate")
			}
			if !hasPlaces {
				return nil, errf(join(mp, "places"), KindSchema, "required for prorate (the final rounding)")
			}
		case MissingNull:
			if hasMin || hasPlaces {
				return nil, errf(mp, KindSchema, "the null policy takes no minAnswered or places")
			}
		case MissingZero:
			if hasPlaces {
				return nil, errf(join(mp, "places"), KindSchema, "only prorate takes places")
			}
		}
		s.Missing.MinAnswered, s.Missing.Places = minA, places
	}
	bands, err := o.array("bands", false)
	if err != nil {
		return nil, err
	}
	bandIDs := map[string]bool{}
	for j, bv := range bands {
		bp := index(join(path, "bands"), j)
		bo, err := object(bp, bv, "min", "id", "text")
		if err != nil {
			return nil, err
		}
		b := &Band{}
		if !bo.has("min") {
			return nil, errf(join(bp, "min"), KindSchema, "required")
		}
		if b.Min, err = bo.number("min"); err != nil {
			return nil, err
		}
		if b.ID, err = bo.str("id", true); err != nil {
			return nil, err
		}
		if !idPattern.MatchString(b.ID) {
			return nil, errf(join(bp, "id"), KindSchema, "band id %q must start with a letter and use only letters, digits, '_', '.' and '-'", b.ID)
		}
		if bandIDs[b.ID] {
			return nil, errf(join(bp, "id"), KindDuplicate, "duplicate band id %q", b.ID)
		}
		bandIDs[b.ID] = true
		if b.Text, err = bo.str("text", false); err != nil {
			return nil, err
		}
		if j > 0 && s.Bands[j-1].Min.Cmp(b.Min) >= 0 {
			return nil, errf(join(bp, "min"), KindRange, "bands must be in increasing order of min")
		}
		s.Bands = append(s.Bands, b)
	}
	p.def.scores[s.ID] = s
	return s, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// validDate reports whether s is a real calendar date in YYYY-MM-DD form.
// It deliberately avoids time.Parse so the JavaScript and Swift evaluators
// can apply exactly the same rule.
func validDate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range s {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	y, _ := strconv.Atoi(s[0:4])
	m, _ := strconv.Atoi(s[5:7])
	d, _ := strconv.Atoi(s[8:10])
	if y < 1 || m < 1 || m > 12 || d < 1 {
		return false
	}
	days := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[m-1]
	if m == 2 && (y%4 == 0 && (y%100 != 0 || y%400 == 0)) {
		days = 29
	}
	return d <= days
}
