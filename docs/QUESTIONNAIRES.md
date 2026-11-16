# Questionnaires: intake forms and eCOA

Status: **design for review** (2026-11-16). Implementation issues are filed
from this document once it's approved. It supersedes the kiosk-only plan in
#82 and [onboarding/KIOSK_DEMO.md](onboarding/KIOSK_DEMO.md): the kiosk is
now one of two places forms are filled.

Questionnaires are WeHelp's main feature set beyond scheduling and
messaging. They cover:
- **custom intake forms** each practice builds for itself;
- **eCOA-style captures:** patient-reported outcomes on standard
  instruments, assigned on schedules, filled at home or in the office.

## Decisions (owner, 2026-11-16)

| Question | Decision |
|---|---|
| What a form definition is | **WeHelp's own versioned JSON, FHIR-shaped.** Typed, coded items; declarative rules; scoring. It maps onto FHIR `Questionnaire` and `QuestionnaireResponse` for export later (#26). Not FHIR as stored, and not Interventionaire v2's HTML pages |
| Where forms are filled, first release | **The patient app** (the paired phone, at home or before a visit) and **the kiosk iPad** (#8) |
| Authoring | **A builder in the staff web.** Publishing freezes a version. JSON import and export, so WeHelp can ship ready-made forms |
| eCOA scope, first release | **Assignments and schedules**, and **standard instruments** (with their scoring) |
| Later | Trend charts; staff corrections with reasons (Part 11 style); FHIR export (#26); staff-entered forms in the web; an Interventionaire v2 converter |

The lit review is in [LIT_REVIEW.md](LIT_REVIEW.md), under "Questionnaires, intake forms and eCOA".

The module/API/state contracts in [DATA_MODULE.md](DATA_MODULE.md) and
[V2_CONTRACTS.md](V2_CONTRACTS.md) are part of this specification.

## Invariants (from ARCHITECTURE.md and SECURITY.md)

- **Answers live only in module-owned clinical Git on the office appliance.** On the phone and the kiosk they
  stay in memory while on screen, never on disk. Nothing goes to the Relay,
  the Registry, logs or push payloads.
- **Every answer write is audited.** Each clinical write uses the audited Git
  prepare/publish/complete protocol in PATIENT_REPOSITORIES.md. Answers are append-only. Reads of responses are audited
  with `auditRead`, like other patient data.
- **Definitions are data, never code.** Rules and scores are a JSON
  expression tree that Go and Swift evaluate the same way. Nothing is
  `eval`ed, and no string expression language is parsed at runtime.
- **Placement.** The approved same-host module owns clinical Git and verified
  Postgres projections. Existing gateway API and fixed push path are reused.

## Concepts

```
Form ─┬─ FormVersion (draft → published, immutable once published)
      │     definition: sections → pages → items, rules, scores
      │
      └─ Assignment (a published version, to one patient, once or on a schedule)
            └─ Occurrence (one due instance: opens, due, closes)
                  └─ Response (one fill: in progress → submitted, or expired)
                        └─ answer events (append-only, per page)
```

- **Form:** a named form owned by the practice, such as "New patient
  intake". Built-in instruments (PHQ-9) are forms too, read-only.
- **Form version:** the definition. Editing makes a new draft. Publishing
  freezes its exact definition/digest; immutable library versions and the
  clinical Git copy cannot be edited in place.
  Responses always point at the exact version that was filled.
- **Assignment:** "Jordan fills PHQ-9 every 2 weeks for 12 weeks, in the
  app." It can be one-off or recurring.
- **Occurrence:** one due instance of an assignment, with an opening time, a
  due time and a closing time (the completion window).
- **Response:** one sitting. It saves page by page, so the patient can stop
  and resume, then submits once. Its channel is `app` or `kiosk`.

## The form definition (`wehelp.form.v1`)

One JSON document per version, validated on import, on save in the builder,
and on publish. The Go validator and evaluator are the reference; the Swift
walker runs the same test vectors.

This is a synthetic format example, not a clinical instrument. Published
standard instruments require their complete definitions and golden vectors.

```json
{
  "format": "wehelp.form.v1",
  "title": "Synthetic one-item example",
  "locale": "en-US",
  "sections": [
    {
      "id": "s1",
      "pages": [
        {
          "id": "p1",
          "items": [
            {
              "id": "q1",
              "type": "choice",
              "required": true,
              "text": "Choose an example value",
              "options": [
                {
                  "value": "0",
                  "text": "Zero",
                  "score": 0
                },
                {
                  "value": "1",
                  "text": "One",
                  "score": 1
                }
              ]
            }
          ]
        }
      ],
      "next": [
        {
          "section": "end"
        }
      ]
    }
  ],
  "scores": [
    {
      "id": "total",
      "text": "Example total",
      "expr": {
        "scoreOf": "q1"
      }
    }
  ]
}
```

**Structure:** form → sections → pages → items. A section is a run of pages
with one way in. Its `next` list picks the following section: the first
entry whose `when` holds wins, and one without `when` is the default. A page
is one screen.

**Item types** (each with `id`, `text`, an optional `code` and `required`):

| Type | Answer | UI hint (`control`) |
|---|---|---|
| `display` | none: text, an image or a video (from the form's own media) | |
| `choice` | one option `value`; `multiple: true` for several | `radio` (default), `dropdown`, `buttons` |
| `boolean` | true or false | `toggle`, `buttons` |
| `integer`, `decimal` | a number, with `min`, `max` and `unit` | `keypad`, `slider` (with `step` and end labels) |
| `date` | a date, with `min` and `max` (`today` allowed) | |
| `text` | short or long text, with `maxLength` | `long` for multi-line |

These match Interventionaire's elements (radio, checkbox, slider, keypad,
text input, and text, image and video outputs). Each one maps to a FHIR
item type.

**Rules (`when`) and scores (`expr`)** form a small JSON expression tree:
- **Values:** `{"answer": "q1"}`, `{"scoreOf": "q1"}` (the chosen option's
  score), `{"score": "total"}` (a computed score), `{"num": 3}`,
  `{"str": "yes"}`, `{"bool": true}`.
- **Tests:** `eq`, `ne`, `lt`, `le`, `gt`, `ge`, `in` (answer among values),
  `answered`, `and`, `or`, `not`.
- **Arithmetic** (scores only): `sum`, `add`, `sub`, `mul`, `div`, `count`
  (answered items), `round`.
- No loops, no functions, no strings built at runtime. Every reference must
  resolve on validation, and rules may only look at items on earlier pages
  or earlier on the same page.
- Expression depth <=32; <=4,096 expression nodes per definition. Exact
  decimal/rational arithmetic and half-even rounding follow V2_CONTRACTS.md.
- **Missing answers:** a score whose inputs are unanswered is `null`, unless
  the instrument's scoring says otherwise (`missing` policy: `null`, `zero`,
  or `prorate` with a minimum answered count).

**Item visibility:** `enableWhen` on an item or a page shows it only when
the rule holds. A hidden item's answer is dropped on submit.

**Limits:** at most 500 items and 200,000 UTF-8 bytes per definition. Media files are
images and short videos stored with the form version, checked by
`internal/filetype` (#170).

**FHIR mapping (for #26):**

| WeHelp | FHIR `Questionnaire` |
|---|---|
| section, page | `item` of type `group` (page groups use SDC `itemControl: page`) |
| item `id`, `type`, `code`, `required` | `linkId`, `type`, `code`, `required` |
| option `value`, `text`, `score` | `answerOption.valueCoding`, plus the `ordinalValue` extension |
| `enableWhen` (simple comparisons) | `enableWhen` and `enableBehavior` |
| `next`, complex rules, `scores` | SDC `enableWhenExpression` and `calculatedExpression`, written out as FHIRPath when exporting (one direction only) |

## Module storage and gateway API

**Logical records/projections** (tenant-scoped forward-only Flyway migrations):
Clinical source records use Git; SQL response/assignment/score tables below
are verified projections, not independently writable authorities. The
patient-free form library may use module DB configuration; exact published
definitions/assets are pinned and retained with clinical records.

- `forms`: id, tenant, name, `kind` (`custom` or `instrument`), `instrument_key`
  for built-ins, created_by, archived_at.
- `form_versions`: id, form_id, version number, `status` (`draft` or
  `published`), `definition` jsonb, published_at, published_by. A trigger
  forbids any change to a published row.
- `form_assignments`: id, tenant, patient_id, form_id, `version_id` (pinned
  at assignment, or `latest` to follow new versions on the next
  occurrence), the schedule, the window, `channel` (`app` or `kiosk`),
  assigned_by, ended_at.
- `form_occurrences`: id, assignment_id, opens_at, due_at, closes_at,
  `status` (`open`, `completed`, `missed`).
- `form_responses`: id, occurrence_id (required; no kiosk walk-ins),
  patient_id, version_id, channel, device_id, started_at, submitted_at,
  `status`, and `scores` jsonb (computed on submit).
- `form_answer_events`: append-only (trigger plus privilege revoke, like
  `audit_events`): response_id, page_id, answers jsonb, recorded_at,
  device_id. The submitted answers are the latest event per page, with
  hidden items dropped.

**Schedules:** `once`, or `every N days` or `every N weeks` from a start
date, ending after a count or on a date. Each occurrence has an open offset
(for example 2 days before due) and a close offset (for example 3 days
after). A background job, like the reminder runner, creates occurrences
ahead of time, marks missed ones, and sends reminders.

**Reminders:** a new fixed push category `form_due` ("You have a form to
fill in from your practice"), with no form name or other PHI. It's sent when
an occurrence opens, and once more before it closes. It uses the existing
Relay push path. The Relay's category list gets the new name.

**Scoring** runs in the module on submit. Submitted answers and authoritative
scores publish atomically in one patient manifest and complete audit before success. The app may also show a score
where the form allows it (`showScore`), using the same evaluator.

**API** (device-session and staff authenticated; every route goes in
`api/openapi.yaml`):

| Route | Who |
|---|---|
| `GET /forms`, `GET /forms/{id}`, `GET /form-versions/{id}` | staff; a patient sees only versions assigned to them |
| `POST /forms`, `PUT /form-versions/{id}` (draft), `POST /form-versions/{id}/publish`, `POST /forms/import`, `GET /form-versions/{id}/export` | admin, LAN only |
| `POST /assignments`, `GET /assignments?patient_id=`, `POST /assignments/{id}/end` | staff (provider for their patients; front desk and admin for the practice) |
| `GET /me/forms` (open and upcoming occurrences) | patient |
| `POST /responses` (start), `PUT /responses/{id}/pages/{pageId}` (save a page), `POST /responses/{id}/submit` | patient (app), kiosk device |
| `GET /responses?patient_id=`, `GET /responses/{id}` | staff, with `auditRead` |

**Who sees what:** V2_CONTRACTS.md's additive privacy matrix applies. Providers
need current link and source permission. Front desk/admin have the existing
non-private source permission, but no private clinical content or form names/
flags/counts. Patients retain own assigned-response APIs; kiosks have one
handoff only. Free-text answers and scores are PHI.

**Feature switch:** `forms`, off until the module migration/release gate passes;
then enabled explicitly per practice. Standard clinical instruments
are hidden for practice types without clinical features (salons; see
PRACTICE_TYPES.md), but salons can still build their own intake forms.

## Patient app

- **A Forms tab** (or a section on Today), listing open and due forms. A
  `form_due` push opens it.
- **The walker:** SwiftUI, one page per screen. It shows progress, Back and
  Next, and checks required answers before Next. Each page is saved to the
  Back Office as it's left, so a patient can stop and resume on the same
  device within the window.
- **Nothing is written to disk.** Answers are in memory until saved. The
  privacy cover and app lock apply as everywhere else.
- **Accessibility:** Dynamic Type, VoiceOver labels from item text, and
  large touch targets for sliders and choices.

## Kiosk (#8)

The office-owned iPad uses the patient walker with a separate kiosk role and
LAN-only device proof. [V2_CONTRACTS.md](V2_CONTRACTS.md) specifies CA/pin/SAN
trust, port, QR expiry, device-bound single-response handoff, private-provider
authorization, response expiry, polling and wipe behavior. These are mandatory
acceptance criteria for #200, not unspecified implementation choices. No
patient-phone LAN path, unassigned walk-ins or date-of-birth lookup. The old
KIOSK_DEMO illustrates a walker only and is not a transport/security contract.

## Staff web

- **Forms (admin):** a list of forms; a builder (sections, pages, items,
  options, rules, scores) with a live preview. The preview uses a small
  JavaScript evaluator held to the same shared test vectors as Go and
  Swift. Also publish, import and export JSON, and archive.
- **Built-in instruments** appear read-only and can only be assigned.
- **On a patient:**
  - assign a form (one-off or a schedule; app or kiosk);
  - see their occurrences (open, done, missed);
  - read submitted responses with scores and bands;
  - "Send to kiosk".
- **Compliance view:** due today, overdue, missed this week, by provider.

## Standard instruments (first set)

PHQ-9, PHQ-2, GAD-7 and GAD-2 (Pfizer; no permission needed). Each is built
exactly as published:
- wording, answer options, recall period and order unchanged;
- LOINC codes for the panel, items and total;
- the published scoring and severity bands;
- PHQ-9 item 9 (thoughts of self-harm) **flagged to staff** on any non-zero
  answer.

The flag shows on the response and in the compliance view. The app shows
the patient a fixed message to contact the practice or emergency services.
How a practice responds is clinical policy, written into the runbook with
the owner.

Every instrument has a test vector: sample answers in, expected scores and
bands out. The test checks it against the published scoring.

## Out of scope for the first release

- Trend charts.
- Staff corrections with reasons.
- FHIR export (#26).
- Staff filling forms in the web.
- Patient file uploads and signatures.
- CAT and IRT instruments (PROMIS CAT).
- Translations beyond the form's own `locale` (one locale per form version).
- An Interventionaire v2 importer.

## Order of work (one PR each)

Filed in the **V2: Questionnaires** milestone, under the epic #201: #193
(format), #194 (forms API), #195 (builder), #196 (assignments), #197
(responses), #198 (patient walker), #199 (instruments) and #200 (kiosk).
They stay `spec-review` (and #200 `needs-spec`) until this design is
approved, so V1 work doesn't pick them up.

1. **Form format:** the `wehelp.form.v1` JSON Schema, a Go validator and
   evaluator (rules, scores, missing policies), and shared test vectors in
   `testdata/forms/`.
2. **Module storage and gateway API for forms:** forms, versions, publish,
   import and export, with immutability and audit.
3. **Staff web builder** with preview.
4. **Assignments and schedules:** occurrences, the background job, the
   `form_due` push category (the Relay too), and the compliance view.
5. **Responses:** start, save page, submit, scoring on submit, staff
   reading with `auditRead`.
6. **Patient app walker** and the Forms tab (Swift evaluator on the shared
   vectors).
7. **Standard instruments:** PHQ-9, PHQ-2, GAD-7 and GAD-2, with vectors and
   the item 9 flag.
8. **Kiosk:** office-device pairing and role, the LAN route, the hand-off,
   the kiosk mode of the walker, Guided Access in the runbook (#8).

Steps 1, 2, 5 and 6 together give a working end to end: a WeHelp-shipped
JSON form, assigned by hand, filled on the phone, read in the staff web.
