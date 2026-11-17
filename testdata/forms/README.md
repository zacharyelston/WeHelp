# wehelp.form.v1 shared test vectors

Every evaluator of `wehelp.form.v1` runs these files and must produce the
same results:
- Go: `internal/forms`, the reference. `go test ./internal/forms/` runs them all.
- JavaScript: `web/forms-eval.js`, for the builder preview. Run
  `node --test web/forms-eval.test.js` or `make test-js`.
- Swift: the patient walker (#198).

The format is described in `internal/forms/doc.go` and
`schemas/wehelp.form.v1.json`.

## `valid/`

Definitions that must parse. The vectors refer to them.
`depth-32.json` is exactly at the expression depth limit.

## `invalid/`

Each file has exactly one problem. `invalid/cases.json` lists it:

```json
{ "file": "dangling-answer.json", "path": "sections[0].pages[1].items[0].enableWhen.eq[0].answer", "kind": "reference", "shared": true }
```

- `path` is where the validator reports the problem.
- `kind` is one of `json`, `schema`, `duplicate`, `reference`, `order`,
  `type`, `operator`, `depth`, `nodes`, `items` and `range`.
- `shared: true` cases cover ids, references, ordering and cycles, types,
  operators, number literals and caps. The JavaScript (and Swift) validators
  must refuse these with the same path and kind. The other cases are
  field-level checks that only the Go validator makes, since the server
  validates every save and publish.

`gen-invalid.js` regenerates the folder. Run it from the repo root with
`node testdata/forms/gen-invalid.js testdata/forms/invalid`. Edit the
generator, not the output.

## `vectors/`

One file per definition:

```json
{
  "definition": "valid/branching.json",
  "cases": [
    {
      "name": "fever branch skips the general section",
      "today": "2026-11-16",
      "answers": { "consent": true, "symptoms": ["fever"], "temp": 38.5 },
      "visible": { "s_general": false, "temp": true },
      "next": { "s_intake": "s_fever" },
      "path": ["s_intake", "s_fever", "s_notes"],
      "scores": { "temp_f": { "value": "101.3", "band": "fever" } },
      "error": "overflow",
      "check": {
        "answers": { "consent": true, "symptoms": ["fever"], "temp": 38.5 },
        "errors": [{ "item": "age", "code": "required" }]
      }
    }
  ]
}
```

Every key except `name` and `answers` is optional. A key that is present is
checked:
- **`visible`:** the listed ids only (sections, pages and items).
- **`next`:** `NextSection` for each listed section (`"end"` finishes the form).
- **`path`:** the exact list of sections the walk visits.
- **`scores`:** the listed scores exactly.
  - `value` is a canonical decimal string (rounded half-even to six places,
    no exponent, no trailing zeros), or `null`.
  - `band` is a band id or `null`.
  - `reason` is present only when `value` is null: `missing`,
    `divideByZero`, `belowMinAnswered` or `overflow`.
  - `{}` means the form has no scores.
- **`flags`:** the ids of the raised staff flags, in definition order (`[]` for none).
- **`error`:** the evaluation error kind. Only `overflow` exists, and its
  score must not be stored. When `error` is absent, there must be no error.
- **`check`:** the result of `CheckAnswersOn(definition, answers, today)`.
  `today` defaults to `2026-11-16`.
  - `answers` is the canonical set to store.
  - `errors` lists `item` and `code` pairs in order: document order, then
    unknown ids alphabetically, then overflowed scores.
  - The codes are `unknown_item`, `not_answerable`, `wrong_type`,
    `unknown_option`, `duplicate_option`, `out_of_range`, `too_long`,
    `required` and `overflow`.

Keep numbers in these files within 15 significant digits, so JavaScript's
`JSON.parse` reads them exactly.
