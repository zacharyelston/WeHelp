package forms

import (
	"fmt"
	"sort"
	"time"
	"unicode/utf8"
)

// Answer problem codes reported by CheckAnswers.
const (
	CodeUnknownItem     = "unknown_item"     // no item with that id
	CodeNotAnswerable   = "not_answerable"   // a display item was answered
	CodeWrongType       = "wrong_type"       // wrong JSON type or format
	CodeUnknownOption   = "unknown_option"   // a choice value that isn't an option
	CodeDuplicateOption = "duplicate_option" // the same option twice in a multiple choice
	CodeOutOfRange      = "out_of_range"     // below min or above max
	CodeTooLong         = "too_long"         // text over maxLength characters
	CodeRequired        = "required"         // a required visible item is unanswered
	CodeOverflow        = "overflow"         // a score overflowed (Item is the score id)
)

// AnswerError is one problem with a set of answers.
type AnswerError struct {
	Item    string `json:"item"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e AnswerError) Error() string { return e.Item + ": " + e.Message }

// Checked is the outcome of CheckAnswers.
type Checked struct {
	// Answers is the canonical answer set to store: well-formed answers to
	// visible items only. Answers to hidden items are dropped.
	Answers Answers
	// Errors lists every problem, in document order (unknown ids last).
	// A submit with any error must be refused as a whole.
	Errors []AnswerError
	// Result is the evaluation of Answers (visibility, path and scores).
	Result *Result
}

// CheckAnswers checks answers against the definition using today's local
// date for "today" bounds. Callers that know the practice's time zone should
// use CheckAnswersOn with the practice's local date.
func CheckAnswers(def *Definition, answers Answers) *Checked {
	return CheckAnswersOn(def, answers, time.Now().Format("2006-01-02"))
}

// CheckAnswersOn checks answers against the definition. It reports wrong
// types, values out of range, unknown options, text over its maxLength and
// required items missing on visible pages; it drops answers for hidden items.
// today (YYYY-MM-DD) resolves date bounds written as "today".
func CheckAnswersOn(def *Definition, answers Answers, today string) *Checked {
	var errs []AnswerError
	bad := map[string]bool{}
	valid := Answers{}
	ids := make([]string, 0, len(answers))
	for id := range answers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		raw := answers[id]
		it, ok := def.items[id]
		if !ok {
			errs = append(errs, AnswerError{id, CodeUnknownItem, "no such item"})
			bad[id] = true
			continue
		}
		v, present, code := answerValue(it, raw)
		if code != "" {
			errs = append(errs, AnswerError{id, code, problem(code, it)})
			bad[id] = true
			continue
		}
		if !present {
			continue
		}
		if code, msg := rangeProblem(it, v, today); code != "" {
			errs = append(errs, AnswerError{id, code, msg})
			bad[id] = true
			continue
		}
		valid[id] = raw
	}
	res, err := Evaluate(def, valid)
	// Errors on answers to hidden items don't matter: those answers are dropped.
	kept := errs[:0]
	for _, e := range errs {
		if _, known := def.items[e.Item]; !known || res.Visible[e.Item] {
			kept = append(kept, e)
		}
	}
	errs = kept
	for _, it := range def.allItems {
		if it.Required && res.Visible[it.ID] && !bad[it.ID] {
			if _, ok := res.Answers[it.ID]; !ok {
				errs = append(errs, AnswerError{it.ID, CodeRequired, "an answer is required"})
			}
		}
	}
	sort.SliceStable(errs, func(i, j int) bool {
		a, aok := def.items[errs[i].Item]
		b, bok := def.items[errs[j].Item]
		switch {
		case aok && bok:
			return a.seq < b.seq
		case aok != bok:
			return aok
		}
		return errs[i].Item < errs[j].Item
	})
	if err != nil {
		for _, sd := range def.Scores {
			if s := res.Scores[sd.ID]; s.Reason == ReasonOverflow {
				errs = append(errs, AnswerError{sd.ID, CodeOverflow, "the score can't be computed exactly within bounds"})
			}
		}
	}
	return &Checked{Answers: res.Answers, Errors: errs, Result: res}
}

// CheckPage checks the answers saved for one page (#197): each must belong
// to an answerable item on that page and be well formed and in range.
// Required items and visibility aren't checked here; submit checks them
// over the whole form. It returns the answers to keep (nulls, "" and empty
// lists dropped) and the problems in document order. ok is false when the
// page doesn't exist.
func CheckPage(def *Definition, pageID string, answers Answers, today string) (kept Answers, errs []AnswerError, ok bool) {
	pg, ok := def.pages[pageID]
	if !ok {
		return nil, nil, false
	}
	kept = Answers{}
	ids := make([]string, 0, len(answers))
	for id := range answers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		raw := answers[id]
		it, known := def.items[id]
		if !known || it.page != pg {
			errs = append(errs, AnswerError{id, CodeUnknownItem, "not an item on page " + pageID})
			continue
		}
		v, present, code := answerValue(it, raw)
		if code != "" {
			errs = append(errs, AnswerError{id, code, problem(code, it)})
			continue
		}
		if !present {
			continue
		}
		if code, msg := rangeProblem(it, v, today); code != "" {
			errs = append(errs, AnswerError{id, code, msg})
			continue
		}
		kept[id] = raw
	}
	sort.SliceStable(errs, func(i, j int) bool {
		a, aok := def.items[errs[i].Item]
		b, bok := def.items[errs[j].Item]
		switch {
		case aok && bok:
			return a.seq < b.seq
		case aok != bok:
			return aok
		}
		return errs[i].Item < errs[j].Item
	})
	return kept, errs, true
}

// PageOf returns the id of the page an item is on, or "".
func (d *Definition) PageOf(itemID string) string {
	if it, ok := d.items[itemID]; ok {
		return it.page.ID
	}
	return ""
}

// PageIDs lists every page id in document order.
func (d *Definition) PageIDs() []string {
	var out []string
	for _, s := range d.Sections {
		for _, p := range s.Pages {
			out = append(out, p.ID)
		}
	}
	return out
}

// Item returns the item with this id, or nil.
func (d *Definition) Item(id string) *Item { return d.items[id] }

// Items lists every item in document order.
func (d *Definition) Items() []*Item { return d.allItems }

func problem(code string, it *Item) string {
	switch code {
	case CodeNotAnswerable:
		return "a display item takes no answer"
	case CodeUnknownOption:
		return "not one of the item's options"
	case CodeDuplicateOption:
		return "an option is chosen twice"
	}
	switch it.Type {
	case TypeChoice:
		if it.Multiple {
			return "must be a list of option values"
		}
		return "must be an option value"
	case TypeBoolean:
		return "must be true or false"
	case TypeInteger:
		return "must be a whole number with at most 12 digits"
	case TypeDecimal:
		return "must be a number with at most 12 integer and 6 fractional digits"
	case TypeDate:
		return "must be a date (YYYY-MM-DD)"
	}
	return "must be a string"
}

func rangeProblem(it *Item, v value, today string) (string, string) {
	switch it.Type {
	case TypeInteger, TypeDecimal:
		if it.Min != nil && v.num.Cmp(it.Min) < 0 {
			return CodeOutOfRange, fmt.Sprintf("below the minimum %s", formatDecimal(it.Min))
		}
		if it.Max != nil && v.num.Cmp(it.Max) > 0 {
			return CodeOutOfRange, fmt.Sprintf("above the maximum %s", formatDecimal(it.Max))
		}
	case TypeDate:
		min, max := it.MinDate, it.MaxDate
		if min == "today" {
			min = today
		}
		if max == "today" {
			max = today
		}
		if min != "" && v.str < min {
			return CodeOutOfRange, "before " + min
		}
		if max != "" && v.str > max {
			return CodeOutOfRange, "after " + max
		}
	case TypeText:
		if utf8.RuneCountInString(v.str) > it.MaxLength {
			return CodeTooLong, fmt.Sprintf("longer than %d characters", it.MaxLength)
		}
	}
	return "", ""
}
