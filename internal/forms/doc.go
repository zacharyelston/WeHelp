// Package forms reads, validates and evaluates wehelp.form.v1 form
// definitions: custom intake forms and standard instruments (docs/
// QUESTIONNAIRES.md, "The form definition"; docs/V2_CONTRACTS.md, "Forms").
//
// The package is pure: no database, no HTTP, no server types, no clock
// except CheckAnswers' default "today". It can live in the Back Office or the
// data module, and it is the reference the JavaScript builder preview
// (web/forms-eval.js) and the Swift walker are held to through the shared
// vectors in testdata/forms/.
//
// # Definitions are data
//
// Rules and scores are a closed JSON expression tree. Nothing is eval'ed and
// no string expression language is parsed. Parse rejects unknown operators
// and fields, dangling references, forward references (a rule may only use
// items on earlier pages, or earlier on its own page; a section's next rules
// may use items up to the end of that section; a score may only use earlier
// scores; next may only go forward), so the evaluation graph has no cycles.
//
// Limits: MaxBytes (200,000) of JSON, MaxItems (500) items, expression trees
// at most MaxDepth (32) nodes deep and MaxNodes (4,096) nodes in total.
//
// # Expressions
//
//	values      {"answer": id} {"scoreOf": id} {"score": id}
//	            {"num": 3} {"str": "yes"} {"bool": true}
//	tests       {"eq"|"ne"|"lt"|"le"|"gt"|"ge": [a, b]}
//	            {"in": [subject, literal, ...]}  (a multiple choice matches if any chosen value is listed)
//	            {"answered": id} {"and": [...]} {"or": [...]} {"not": x}
//	arithmetic  {"sum": [...]} {"add"|"sub"|"mul"|"div": [a, b]}
//	(scores)    {"count": [id, ...]} {"round": [x, {"num": places}]}
//
// Operands are typed statically: numbers, strings, booleans, dates (a str
// literal compared with a date answer must be YYYY-MM-DD) and multiple-choice
// answers (usable only in "in" and "answered"). A string literal compared
// with a choice answer must be one of its option values.
//
// # Evaluation
//
// Evaluate walks sections in document order. The first section is on the
// path; each section on the path picks the next with its first next entry
// whose when holds (an entry without when is the default; with none, the
// following section, or "end"). A page is visible when its section is on
// the path and its enableWhen holds; an item when its page is visible and
// its enableWhen holds. Only well-typed answers to visible items count:
// a hidden item's answer is missing everywhere, and is dropped on submit.
//
// A comparison or "in" with a missing operand is false; "not" of it is
// true. and/or short-circuit left to right.
//
// Numbers are exact rationals (math/big), never floats. Numeric literals and
// answers have at most 12 integer and 6 fractional digits and no exponent.
// Every arithmetic result (each step of a sum, add, sub, mul, div, round,
// a multiple choice's scoreOf, and each step of proration) must keep its
// normalised numerator and denominator within 128 bits; otherwise the score
// is null with reason "overflow" and Scores returns a KindOverflow error, so
// it is never stored. Division by zero is null with reason "divideByZero".
// round is half-even. A score's reported value is its exact value rounded
// half-even to six places (Score.Text); bands compare the exact value.
//
// A score's missing policy governs the sum nodes in its expression:
// "null" (default: any missing operand makes the sum null), "zero" (missing
// operands count as 0, with an optional minAnswered) or "prorate"
// (minAnswered and places required: sum × operands ÷ answered, rounded
// half-even to places). Elsewhere a missing operand makes the result null.
//
// # FHIR mapping (for export, #26; there is no exporter yet)
//
//	WeHelp                                FHIR R4 Questionnaire
//	section, page                         item of type group (page groups carry the SDC itemControl "page")
//	item id, type, code, required         item linkId, type, code, required. Types map one to one
//	                                      (choice, boolean, integer, decimal, date, display), and text
//	                                      maps to string, or to text when control is "long"
//	choice multiple                       item.repeats = true
//	option value, text, score             answerOption.valueCoding (code, display), ordinalValue extension
//	integer/decimal min, max              minValue / maxValue extensions; unit → questionnaire-unit
//	text maxLength                        item.maxLength
//	enableWhen (simple comparisons,       enableWhen + enableBehavior (all for and, any for or)
//	  and/or of them)
//	next, complex rules, scores           SDC enableWhenExpression and calculatedExpression, written
//	                                      out as FHIRPath on export (one direction only)
//
// Answers map onto QuestionnaireResponse.item.answer by linkId. FHIR is never
// the stored format.
package forms
