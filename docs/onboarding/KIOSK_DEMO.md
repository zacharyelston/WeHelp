# Kiosk interventions and questionnaires: Interventionaire and the demo

The in-office kiosk iPad (#8) is post-V1. This note covers the questionnaire
and intervention tooling it would run. The design comes from
**Interventionaire**, a separate project that is itself being renewed as an
iOS app. The kiosk copies design ideas from Interventionaire as it renews, so
keep this note in step with it.

This note summarises what Interventionaire does, what the kiosk takes from
it, how the result fits the Back Office without breaking the rule that
patient data lives only there, and the demo script. Integration work is #82.
The Interventionaire repo is not changed by this work.

## What Interventionaire is

A form builder and runner for branching questionnaires (it calls them
*booklets*) and short interventions (pages that inform, show a video, or ask
the patient to rate something). It came from a 2016 product. The backend has
been partly modernised; the authoring client has not.

| Part | Technology | State |
|---|---|---|
| `interventionaire-backend` | AdonisJS 5 (Node, TypeScript), MySQL 8, Docker Compose | Modernisation in progress: v1.3 (models, validation, Jest tests). Raw SQL in most controllers; auth refactor and migrations still on its TODO list |
| `original-source/.../client` | AngularJS 1 SPA, Grunt and Bower | Legacy. The editor and the runner. Not in the Compose file yet |
| `original-source/.../worker` | Grunt-built worker | Legacy |
| Renewal | iOS app | Planned. When it exists, it becomes the main design reference for the kiosk, and this note is updated |

### Content model

```
Booklet (tbl_scripts) ── StyleBook (theme)
  └─ Sections (tbl_sequences)        ordered by Choices between sections
       └─ Pages (tbl_pages)          content + elements
Choice (tbl_choices): from a section, a branch type + expression → next section
Branch (tbl_branches): named branch behaviours (e.g. always, if-expression)
```

- **Page elements** (`client/app/tmpl`): inputs are radio, checkbox, slider,
  keypad and text input; outputs are text, image, video and chart.
- **On-screen keyboard** (`scripts/osk`): built for a touch kiosk with no
  hardware keyboard.
- **Branching:** each choice holds an expression over the answers collected
  so far (*datums*). The runner (`services/booklet-walker.js`) evaluates it
  to pick the next section.
- **Bundles:** a booklet can be exported and imported as a bundle
  (`/api/bundle`, `bundle/download/:id`).
- **Style books and themes:** per-booklet look and feel.
- **Sources:** data sets the branches can read. A source can be `external`,
  in which case the **server fetches a remote URL**.

### Runs

A run of a booklet is a *test* (`tbl_testing`) with a single-use
**test code**. A proctor creates it; the person taking it enters the code.

- `GET /run/available/:testCode`, `GET /run/:testCode`: check and start a run.
- `POST /api/test/page`, `PUT /api/test/:id/page/:ndx`: record each page reached (`tbl_testHistory`).
- Answers are name/value rows per run (`tbl_testDatum`), including a `PID`
  field.
- `GET /api/test/:id/finished`: close the run.
- Demo runs (`demo/:testCode`, `demo-ize`) replay a booklet without keeping
  results.
- The proctor screen lists runs and their progress; the imaging controller
  renders a page or a run as an image.

### What doesn't fit WeHelp as it stands

| Finding | Why it matters here |
|---|---|
| The runner evaluates branch expressions with JavaScript `eval` | Stored content becomes executable code. Unacceptable on a device that handles PHI |
| Answers (`tbl_testDatum`, with a `PID`) are stored in Interventionaire's MySQL | Patient data outside the Back Office and outside `audit.Record` |
| `external` sources fetch remote URLs from the server | An outbound path from wherever answers live |
| EHR practice and patient screens (`ehr-practice`, `ehr-patient-modal`) hold patient names and external IDs | A second patient store |
| Its own users, JWT auth and password reset by email; the run token is still a stub (`'no token'`) | A second identity system, not the WeHelp device-key model |
| Node + MySQL stack | New infrastructure on the Back Office (ARCHITECTURE.md allows Postgres, Relay, Registry, observability sidecar) |

These are findings about the legacy code, not the renewal. They are the
things the kiosk must not copy.

## What the kiosk copies from Interventionaire

| Interventionaire idea | In the WeHelp kiosk |
|---|---|
| Booklet → sections → pages | The same three levels for questionnaires |
| Choices between sections, with branch rules on earlier answers | The same, as declarative rules parsed and checked on import, never executed as code |
| Input elements (radio, checkbox, slider, keypad, text) and output elements (text, image, video, chart) | The same element set, as native SwiftUI views |
| On-screen keyboard for a kiosk without a hardware keyboard | The iPad's own keyboard and number pad, in Guided Access |
| Style books and themes | The practice's branding on the kiosk |
| Single-use run codes and the proctor view | A staff-started kiosk session from the patient record, and a staff view of its progress |
| Demo runs that keep no results | A training mode for the demo kit |
| Bundle export and import | A signed, versioned content bundle (#82) |

## How it plugs into the Back Office

**Recommended: copy Interventionaire's design; the Back Office owns the
runs.** Content flows in one direction, as data. Answers never leave the
Back Office. The diagram shows Interventionaire as the authoring tool. That
holds whether authoring stays in the legacy editor or moves to the renewed
iOS app, as long as it can export a content-only bundle. If it can't,
authoring moves into the WeHelp staff UI (see "Alternatives considered").

```
WeHelp or a staff laptop                     The practice
┌────────────────────────┐   signed JSON     ┌───────────────────────────────┐
│ Interventionaire       │   bundle          │ Back Office (wehelpd+Postgres)│
│ editor: booklets,      │ ────────────────► │ questionnaires (immutable,    │
│ sections, pages,       │   content only,   │   versioned)                  │
│ branches, style books  │   no answers      │ questionnaire_responses       │
│ no patient data        │                   │   + audit.Record              │
└────────────────────────┘                   │ staff review queue            │
                                             └──────────────▲────────────────┘
                                                            │ office LAN, office
                                                            │ device key, TLS
                                                     ┌──────┴───────┐
                                                     │ Kiosk iPad   │
                                                     │ native walker│
                                                     │ (no eval)    │
                                                     └──────────────┘
```

1. **Author** in Interventionaire, with content only. It runs at WeHelp, on
   a staff laptop, or (after the renewal) on an iPad that isn't the kiosk.
   It never runs on the Back Office and holds no answers.
2. **Export** a booklet as a bundle. #82 adds a signed, versioned JSON
   format: pages, elements, sections, choices, and branch rules written in a
   small declarative expression language (comparisons, `and`/`or`/`not`, set
   membership, numeric ranges) that is parsed, not executed.
3. **Import** into the Back Office through the staff UI. Import validates the
   bundle (signature, schema, every branch expression parses, every
   referenced section exists) and stores it as an immutable questionnaire
   version. The import is audited.
4. **Run** on the kiosk:
   - Staff start a session for a patient from the staff UI or the front
     desk, choosing the questionnaire. This settles patient identity at the
     kiosk (#8) without a patient login.
   - The kiosk, enrolled as an office device with its own key, fetches the
     questionnaire version and walks it natively in SwiftUI. Branching runs
     in the same expression evaluator as import validation.
   - Each page's answers are posted to the Back Office as they are given. Each write is
     wrapped in a transaction with `audit.Record`, attributed to the kiosk
     device and the staff member who started the session.
   - At the end, or after an idle timeout, the kiosk wipes everything and
     returns to its attract screen.
5. **Review:** responses wait in a staff review queue, then join the record
   (#8). Structured readings can map to check-ins (#21) and later FHIR
   `QuestionnaireResponse` (#26).

**Not carried over:** `eval` branches, external sources, the EHR screens,
Interventionaire's user accounts and run codes, and its MySQL answer tables.

**Alternatives considered**

| Option | Verdict |
|---|---|
| Run Interventionaire beside `wehelpd` and point the kiosk at it | **Rejected.** Node + MySQL on the Back Office, answers outside the audit chain, `eval` in the runner, a second auth system |
| Rewrite authoring inside the WeHelp staff UI | **Later.** Worth it if the practice authors its own questionnaires often, or if the renewed Interventionaire can't export bundles. The bundle format from #82 is the same either way |
| Use the renewed Interventionaire iOS app as the kiosk itself | **Revisit when the renewal exists.** It would have to enrol as an office device, keep no answers, and save each answer to the Back Office with `audit.Record`. Until then, the kiosk is a WeHelp app that copies its design |
| Adopt a standard format (FHIR Questionnaire, SurveyJS, ResearchKit) and convert Interventionaire into it | **Evaluate in #82**, through a lit-review entry, before the bundle format is fixed |

## Demo script

There are two versions. Use version A until #8 and #82 exist. Every name,
date and answer in both is synthetic.

### Version A: today (Interventionaire prototype, standalone)

**What it shows:** the authoring and the patient experience. It does **not**
show the Back Office integration, and the presenter says so. Once the
renewed Interventionaire iOS app runs, use it here instead of the legacy
web client.

**Setup (not verified for this doc; do a dry run first):**
- Interventionaire backend and MySQL from its Compose file on the demo
  laptop. The legacy client must be built separately (Grunt), as the Compose
  frontend service is commented out.
- An iPad on the **demo** Wi-Fi with the runner open in Safari, in Guided
  Access.
- A demo booklet, "New patient intake (demo)", created beforehand (below).
- Answers stay in the demo laptop's MySQL and are discarded after the demo.
  Use the demo run mode where possible.

**Script (about 12 minutes)**
1. *(Editor, laptop.)* "This is where a practice's questionnaires are
   written. A booklet is made of sections, and sections are made of pages."
   Open "New patient intake (demo)" and show the section map.
2. Open the **Digestion** page. Show a slider ("How often do you have
   bloating?", 0–10) and a checkbox group.
3. Open the choice after **Main concern**. "If the patient picks digestive
   health, they get the digestion section. Otherwise they skip it. Branches
   are rules on earlier answers."
4. Show the style book: the practice's colours and logo (demo practice
   branding).
5. *(iPad.)* Start a demo run. Hand the iPad to a volunteer playing
   **Jordan Lee (demo)**.
6. The volunteer answers: main concern "digestive health", sleep 6/10,
   stress "most days", picks two diet items, uses the on-screen keyboard
   for "anything else".
7. Show that the digestion section appeared because of answer 1. Go back
   and change it to "sleep": the branch changes.
8. Finish. *(Editor.)* Open the proctor view and show the run as complete,
   with each page reached.
9. Close with what changes in WeHelp: "In WeHelp, these answers go only to
   the practice's own server, into a review queue for the front desk. The
   kiosk forgets everything when the patient hands it back."

### Version B: target (once #8 and #82 land)

**Setup:** the training kit (RUNBOOK.md, Phase 4) with the training seed; an
iPad enrolled as the demo office's kiosk device in Guided Access; "New
patient intake (demo)" imported as a questionnaire.

**Script (about 15 minutes)**
1. *(Staff UI.)* Show **Questionnaires**: the imported booklet, its version
   and who imported it. Open the import entry in the audit log.
2. *(Front desk.)* Demo patient **Sam Rivera (demo)** checks in. Front desk
   opens Sam's record and presses **Start kiosk session**, choosing the
   intake questionnaire.
3. *(Kiosk.)* The iPad leaves its attract screen and greets "Sam" by first
   name only. Sam answers. The digestion branch appears.
4. Walk away mid-questionnaire. After the idle timeout, the iPad wipes the
   session and returns to the attract screen. Restart the session from the
   staff UI.
5. Finish. The kiosk shows "Please hand the iPad back to the front desk"
   and wipes itself.
6. *(Staff UI.)* The **Review** queue shows Sam's responses. Front desk
   accepts them into the record. Show the audit trail: session started (by
   staff), each answer saved (kiosk device and staff member), review
   accepted.
7. *(Relay view, optional.)* Show that the kiosk never used the Relay. It
   talks to the Back Office on the office LAN only.
8. *(Provider app.)* The provider opens Sam's appointment and sees the
   accepted intake summary.

### Demo booklet: "New patient intake (demo)"

| Section | Pages and elements | Branch |
|---|---|---|
| Welcome | Text: purpose, how long, "hand back the iPad when done" | always → Main concern |
| Main concern | Radio: digestive health / sleep / stress / energy / skin / other | digestive → Digestion; otherwise → Lifestyle |
| Digestion | Slider: bloating 0–10; checkbox: symptoms; radio: how long | always → Lifestyle |
| Lifestyle | Slider: sleep quality 0–10; radio: stress frequency; checkbox: diet pattern | stress = "most days" → Stress check; otherwise → Goals |
| Stress check | Four short radio items (a demo scale, not a validated instrument) | always → Goals |
| Goals | Text input with the on-screen keyboard: "What would you like to change?" | always → Thank you |
| Thank you | Text: "Please hand the iPad back to the front desk" | end |

The demo booklet is illustrative. A real intake questionnaire, and any
validated scale, is chosen by the practice's providers.
