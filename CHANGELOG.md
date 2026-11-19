# Changelog

All notable changes to WeHelp are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed
- The presentation's eleven staff web screenshots are retaken with the new
  sidebar and teal look, on the training week's demo data (alt text and
  speaker notes updated to match).
- The staff web has a menu and a cleaner look:
  - a **sidebar grouped** into Care (patients, appointments, messages, inbox,
    care teams, links), Forms (forms, due forms), Practice (orders, store,
    website) and Admin (settings, staff), showing only groups with something
    in them;
  - a **user menu** for My profile, My devices and Log out;
  - on narrow screens the sidebar becomes a slide-in menu behind a ☰ button;
  - menus close on Escape or a click elsewhere, the current page is marked
    for screen readers, and items show keyboard focus.
- **Brand colours and mark:** a deep teal replaces the default blue, with an
  inline house-and-keyhole mark. The name and mark live in one `BRAND`
  constant in `web/app.js`, so a rename to Keepdom is a one-line change.
- **Denser tables and cards,** so more fits on screen.

### Added
- Pictures in forms on the iPhone (#198). A display item's image loads
  through the pinned connection from the form version's assets. It's checked
  as PNG or JPEG, scaled down if large and kept in memory only. VoiceOver
  reads its description. A picture that can't load, or a video (not played
  in the app yet), shows nothing.

### Fixed
- The form builder no longer says "Not saved: missing bearer token" when the
  staff session has ended (30 minutes idle). It says the user was signed out
  and that the changes are kept on the page, and pauses autosave. A
  **Sign in to save** dialog over the editor signs in again (with the
  authenticator code), takes the new CSRF token, and saves the draft at once,
  without leaving the page or losing the form.

### Added
- Answer lists and a grid editor in the form builder (#259), builder only.
  A grid saves as ordinary choice questions with identical answers, so the
  format, scoring, the server and the app are unchanged.
  - **Answer lists:**
    - built-in lists when adding or editing a choice question: Never /
      Sometimes / Often / Always, None / Mild / Moderate / Severe and 0–10
      (all with scores), and Yes / No / Not sure;
    - "Reverse scores";
    - "Copy options from" another question.
  - **The grid:** consecutive single-choice questions with identical answers,
    the same required setting and control, and no rule open as one grid
    block.
    - Editing the shared answers changes every row.
    - Rows have their own text, id and optional LOINC code, and can be added,
      reordered, removed, or pasted one per line.
    - "Score = sum of these rows" adds the score in one step.
    - "Add item" offers a grid, and built-ins such as PHQ-9 show their
      items as a read-only grid.
  - **Fixed:** `web/forms.js` and `web/app.js` both defined a global
    `addItem`, so app.js's replaced the builder's. The builder's is now
    `addFormItem`, and a test fails if the staff web scripts ever define
    the same global twice.
- Starting points in the form builder (#258).
  - **Duplicate a form:** `POST /forms/{id}/copy` makes a new custom form,
    named "(copy)", with a draft from any version and its images.
  - **Copy a built-in to edit:** a copy of a built-in such as PHQ-9 is a
    custom, editable form.
    - It keeps the copyright, and its description says that once edited it's
      no longer the validated instrument. The builder shows that note too.
    - Built-ins can only be copied where they're available (#199).
  - **Starter templates:** new patient intake, follow-up visit and symptom
    checklist, under New form (`GET /form-templates`; `POST /forms`
    with `template`). They're synthetic and practice-neutral, in
    `internal/forms/templates`, and checked by Go and JavaScript.
  - **Duplicate a page or a question:** the copy gets fresh ids, and rules
    inside a copied page point at the copied questions; rules elsewhere are
    unchanged.
  - **Audit:** every copy is `form.create` with a `copied_from` detail.
- The form builder keeps your work (#260):
  - **Autosave:** a draft saves itself 2 seconds after the last change,
    through the existing save route. A status line shows "Unsaved
    changes", "Saving…", "Saved" or "Not saved: <reason>".
  - A refused save (422) keeps the local copy and outlines the problem at
    its path, as Save does.
  - Saving updates only the status and the error box, so typing elsewhere
    isn't interrupted.
  - **Undo and redo:** buttons, plus Cmd/Ctrl+Z and Shift+Cmd/Ctrl+Z (or
    Ctrl+Y) outside text fields, which keep their own undo. Up to 100
    steps.
  - **Leaving:** closing the tab, Back to forms, switching versions or
    switching app tabs with unsaved changes asks first.
  - Everything stays in page memory: nothing goes to localStorage or
    IndexedDB.
- The iOS form evaluator understands staff flags (#199), so it passes the
  PHQ-9, PHQ-2, GAD-7 and GAD-2 vectors. The app still never shows flags to
  the patient.
- Built-in instruments (#199): PHQ-9, PHQ-2, GAD-7 and GAD-2, with the
  PHQ-9 self-harm flag. `docs/INSTRUMENTS.md` has the details.
  - **The format:** `wehelp.form.v1` gains a definition-level `flags` list
    (`{id, text, when}`) and a `copyright` field. They're added to
    `internal/forms`, the JSON Schema and `web/forms-eval.js`.
    `Evaluate` returns the raised flags.
    - Shared vectors cover each instrument: all zeros, all maximum, each
      band boundary, a missing item, and PHQ-9 item 9 at 0 (no flag) and
      1–3 (flag). There are also three new negative cases for flags.
  - **Definitions:** `internal/forms/instruments`, embedded. They carry
    LOINC codes checked against HL7 examples, the published bands, the
    unscored difficulty question and the Pfizer attribution. `TestWording`
    pins every item to the published wording.
  - **Sync:** `internal/instrumentsync` puts the built-ins into each
    practice as read-only `kind = instrument` forms. It's idempotent and
    audited; a changed definition publishes a new version.
  - **Field gate:** the instruments exist only in demo builds, or on a
    field install whose new `forms_instruments` config key is on. That key
    waits for the owner's decisions on monitoring, patient wording and
    escalation. They're also only for clinical practice types with forms
    on.
  - **Flags:** stored on the response at submit and shown in red to staff,
    in the response, the Answers list and a new "Flagged this week" list
    in the Due forms view (`flagged_this_week`). The patient never sees a
    flag, and no patient-facing crisis text was added.
  - **Demo seed:** it also gives Jordan a PHQ-9. Re-running it on an older
    demo host adds just that.
- iOS Forms client, second part (#198), on the routes from #196 and #197:
  - Today lists the patient's open and upcoming forms from `/me/forms` when
    the practice's `forms` switch is on. The list refreshes on push, on
    return to the app and on pull to refresh; upcoming forms are greyed out
    with their opening time.
  - Opening a form fetches its published version, starts or resumes the
    response (`POST /responses`) and continues at the first page still
    missing a required answer.
  - Page saves send `If-Match` and an `Idempotency-Key`, reused on retry.
    A stale revision refetches and saves once more.
  - The Back Office's refusals show on the right question; on submit the
    walker jumps to the page the server names. A closed form says so and
    stops.
  - Answers go out and come back as exact JSON number text.
  - Tapping a `form_due` push opens Today. Locking the app or switching
    practice account closes the walker and drops its answers.
  - `APIClient` reads `error` and `code` from any error body, and has
    `authorizedRaw` for callers that read the body themselves.
- Form responses (#197), in `wehelpd` with ordinary tables (V27) per the
  owner's decision. The answers are PHI and stay in the Back Office.
  - **Patient routes:**
    - `POST /responses` starts a response to one of the patient's open
      forms, or returns the one in progress with its saved pages (resume).
    - `PUT /responses/{id}/pages/{pageId}` saves one page. It needs the
      response revision (`If-Match`) and an `Idempotency-Key`, and is
      limited to 64 KB. Answers are checked by the new `forms.CheckPage`.
      A stale revision or a reused key with another body is 409.
    - `POST /responses/{id}/submit` rebuilds the answers from each page's
      latest save, drops hidden items, checks required answers on visible
      pages (422 with each item and its page), scores the form on the
      server and completes the occurrence. Submitting again returns the
      same result. After the window closes, every patient route is 409
      `window_closed`.
  - **Storage:** `form_answer_events` and `form_response_reads` are
    append-only, by trigger and by `wehelp_app` privileges. A response is
    frozen once it's submitted or expired. The runner expires in-progress
    responses when their window closes.
  - **Staff routes (LAN):** `GET /responses?patient_id=` and
    `GET /responses/{id}`, both read-audited, return answers next to their
    question text, scores with bands, flags, and a "new" badge until the
    first staff open.
  - **Audit:** `form_response.start`, `form_response.save_page` (page id
    only) and `form_response.submit` (score ids and counts, never values).
    A test checks that answers never reach the logs.
  - **Staff web:** the patient's Forms panel lists sent forms with scores
    and the new badge, and opens each one.
  - `docs/MOBILE_CLIENT.md` §16 has the exact requests, responses and error
    codes for the app.
- Form assignments (#196), in `wehelpd` with ordinary tables (V26) per the
  owner's decision.
  - **Assigning:** staff give a published form to a patient, once or every
    N days or weeks. A repeating schedule needs a count or an end date.
    - A provider assigns to their linked patients; the front desk and admins
      to anyone in the practice.
    - The version can be pinned, or each occurrence takes the latest
      published version.
    - The channel is the app or the kiosk (kiosk ones are stored, but
      nothing delivers them yet).
  - **Occurrences:** each due instance has a window that opens and closes
    at 09:00 practice time, in the zone pinned on the assignment.
    - Dates follow the local calendar across daylight-saving changes.
    - There's one occurrence per assignment and due date, and at most 366
      per assignment.
  - **Routes:**
    - staff, LAN: `POST /assignments`, `GET /assignments?patient_id=`,
      `POST /assignments/{id}/end` (cancels what hasn't opened) and
      `GET /form-occurrences/due` (due today, overdue, missed this week,
      with providers);
    - patient: `GET /me/forms`, the open and upcoming forms;
    - patients may now read `GET /form-versions/{id}` and its images for a
      version on one of their open occurrences.
  - **Runner:** `wehelpd serve` runs it every `forms_interval` (default 5m).
    Repeated runs never duplicate anything. Each run:
    - creates occurrences two weeks ahead;
    - marks closed, unfinished ones missed;
    - sends the `form_due` push when one opens and 24 hours before it
      closes, each at most once.
  - **Relay:** the new `form_due` category has fixed text and never names
    the form.
  - **Audit:** `form_assignment.create` and `form_assignment.end`;
    `form_occurrence.create` with no actor (the system); read audits for
    the staff lists. Forms routes now answer 403 with code `feature_off`
    while the switch is off.
  - **Staff web:** a Forms button on each patient (their assignments and
    occurrences, Assign form, End) and a Due forms tab for staff.
  - **Demo seed:** `wehelpd seed` now also turns forms on and gives the
    first demo patient a synthetic three-page branching form with a score,
    open for a week (`seed.RunDemoForms`).
  - `docs/MOBILE_CLIENT.md` §16 has the app contract.
- iOS Forms client, first part (#198):
  - `ios/WeHelp/Forms/`, a Swift `wehelp.form.v1` evaluator, ported from
    `web/forms-eval.js` with Go's exact number handling. It covers:
    - a JSON reader that keeps number text as written;
    - exact rationals within 128 bits;
    - the compiler and validator;
    - visibility, branching, scores and missing policies;
    - `checkAnswers`.
  - `FormEvaluatorTests` runs every shared vector and the 39 shared invalid
    definitions in `testdata/forms/`, which are bundled into the test target.
  - The walker (`FormWalkerView`, `FormSession`): one page per screen with
    progress, Back and Next, and inline errors.
    - Next checks the page's required, range and type problems.
    - Submit appears only on a complete last page.
    - Afterwards it shows "Thank you" and any `showScore` results.
    - Controls: radio list, buttons, checkboxes, dropdown, yes/no, a number
      keypad with a Done key, a slider with end labels, a date picker, and
      short and long text with a counter.
  - Answers stay in memory. A `FormSink` protocol takes page saves and the
    submit; the Back Office implementation follows #196 and #197.
  - Until then, Today shows a bundled sample form in demo builds only. Debug
    builds open it directly with the `-sampleForm` launch argument.
- A form builder in the staff web (#195): a Forms tab for admins, shown
  while the `forms` switch is on. The code is in `web/forms.js`.
  - **Forms list:** shows custom and built-in forms with their latest and
    published versions. Admins create, import JSON and archive from there.
  - **The builder,** on a draft:
    - add, reorder and remove sections, pages, items and options;
    - edit every item type's fields;
    - build rules for `enableWhen` and section branching from pick lists
      (item, operator, value). A rule the builder can't show appears as
      read-only JSON.
    - build scores as a sum of chosen items' scores, with a missing-answer
      policy and bands.
  - **Validation:** the builder checks the draft with `web/forms-eval.js` as
    you edit. Server errors (422) come back with their path, and the part
    of the form it names is outlined.
  - **Live preview:** walks the draft page by page as a patient would, with
    visibility, branching, required checks and live scores.
  - **Publishing:** asks for confirmation first. "New version" starts a
    draft from a published version, and "Export JSON" downloads it.
    Built-in instruments open read-only, with the preview.
  - **Images on display items:** PNG or JPEG, uploaded to the draft and
    checked by content and decoded size. Each image is at most 10 MB and
    20 megapixels, and a version holds at most 20 images and 64 MB.
    - They're stored in the new V25 `form_assets` table.
    - A published version's images are immutable, enforced by a trigger.
    - A new draft gets copies.
    - Publishing needs every named image uploaded.
    - Upload and delete are audited.
  - CI now runs the staff web JavaScript tests (`node --test web/*.test.js`)
    in the go job: the shared evaluator vectors, plus Forms tab and builder
    tests that load the real scripts with a fake DOM.
- The practice's form library in `wehelpd` (#194). Following the owner's
  2026-11-16 decision, forms live in the Back Office for now. They have their
  own V24 migration, their own route group and one file
  (`internal/server/forms.go`), so they can move to the data module later.
  - **Storage:** `forms` (custom or built-in instrument) and
    `form_versions`.
    - A trigger refuses any UPDATE or DELETE of a published version.
    - A partial unique index allows only one draft per form.
    - `wehelp_app` can't delete either table.
  - **Admin routes (LAN):** `POST /forms`, `PUT /form-versions/{id}`,
    `POST /form-versions/{id}/publish`, `POST /forms/{id}/draft`,
    `POST /forms/import`, `GET /form-versions/{id}/export` and
    `POST /forms/{id}/archive`.
  - **Staff routes (LAN):** `GET /forms`, `GET /forms/{id}` and
    `GET /form-versions/{id}`.
  - **Saving:** every save goes through `forms.Parse`, and an invalid
    definition gets a 422 with the error's path and kind.
  - **Export:** canonical JSON (`forms.Canonical`), so import then export
    gives the same bytes. Each version stores its SHA-256 digest.
  - **Built-ins:** they refuse every change with a 409.
  - **Audit:** `form.create`, `form_version.save`, `form_version.publish`,
    `form_version.draft`, `form.import` and `form.archive`. Events carry ids
    and version numbers only.
  - **Switch:** a new `forms` switch starts off for every practice type
    (`practice.DefaultOff`). While it's off, every form route answers 403.
    An admin turns it on in Settings.
- The `wehelp.form.v1` form format (#193), the first questionnaire step.
  - **`internal/forms`:** `Parse` validates a definition and names the
    path of the first problem. It checks:
    - the schema and the fields allowed for each item type;
    - unique ids and option values;
    - that references resolve;
    - that rules only look back, `next` only goes forward, and scores only
      use earlier scores, so there are no cycles;
    - the static types of expression operands;
    - the V2_CONTRACTS limits: 200,000 bytes, 500 items, expression
      depth 32, 4,096 nodes.
  - **Evaluator:** `Evaluate`, `Visible`, `NextSection` and `Scores`.
    - Arithmetic uses exact rationals bounded to 128 bits; overflow is an
      error, never a score.
    - `round` is half-even.
    - Division by zero gives null with a reason.
    - Missing-answer policies are `null`, `zero` and `prorate`.
  - **`CheckAnswers`:** reports wrong types, out-of-range values, unknown
    options, over-long text and missing required items on visible pages.
    It drops answers to hidden items.
  - The package has no database, HTTP or server dependencies (a test
    enforces this), so it can live in the Back Office or the proposed data
    module. Its package doc carries the FHIR mapping.
  - **`schemas/wehelp.form.v1.json`:** a JSON Schema kept in step with the
    validator by a test.
  - **`testdata/forms/`:** valid and invalid definitions plus evaluator
    vectors (visibility, next section, path, scores and bands, answer
    checks). Go runs all of them.
  - **`web/forms-eval.js`:** the same evaluator for the builder preview, in
    plain JavaScript with no dependencies and no `eval`. It runs the same
    vectors and the shared invalid cases. Run it with
    `node --test web/forms-eval.test.js` or `make test-js`; it's opt-in,
    since Node isn't in CI.
- `deploy/appliance.sh` works fully on a Linux (Docker Engine) Back Office.
  `install-backup` installs a systemd service and timer
  (`deploy/linux/wehelp-backup.*`) that back up nightly at 02:30, like the
  launchd job. `demo-setup` finds the LAN address with `ip route` or
  `hostname -I`. Doctor's `filevault` check passes when Docker's data is on
  LUKS (dm-crypt), and `auto_login` checks GDM and getty, instead of
  reporting SKIP. See `docs/APPLIANCE.md` ("Linux host").
- A Linux Back Office test host, MiniZ (Ubuntu 24.04, Docker Engine). It's
  documented in `docs/APPLIANCE.md` ("Linux host"), with what's still
  macOS-only, and listed in the ARCHITECTURE.md test topologies.
- The Registry runs on an always-on host (#182): `deploy/registry.sh`
  (`up`, `down`, `status`, `logs`, `cli …`) drives its own Compose project,
  `deploy/registry/` (Postgres, Flyway and a distroless nonroot
  `wehelp-registry` image). The DB password is generated per install into
  gitignored `deploy/registry/db.env`, and `deploy/compose.sh` renders the
  env files the services load, so no tracked file names it. Postgres listens
  on 127.0.0.1 only. The first `up` creates the signing key on a named
  volume. The demo topology now runs a fresh Registry on Barnacle on :8090,
  with the Relay and the office re-registered (new key and IDs; phones
  re-pair).
- The Relay logs each video visit's TURN usage when its last allocation
  goes: `relay: TURN visit ended` with the office, the duration, bytes in
  and out, average kbit/s, and the allocation count. Counts only, no
  addresses or credentials. TURN's client sockets are wrapped to count bytes
  by the phone's address. `docs/RELAY.md` also records the first measured
  visit on Barnacle: about 2.2 Mbit/s each way at the Relay.

- `wehelpd doctor` and `appliance.sh doctor` (#81): the go-live checks a
  machine can check.
  - **Host:** FileVault, automatic login, Postgres on loopback only.
  - **Back Office:** office keys and modes, the Registry install, the Relay
    tunnel, the audit chain, a good backup under 26 hours old, a field
    build with no seed accounts, and staff HTTPS from the office CA.
  - One PASS, FAIL or SKIP line per check, or `--json`. It exits non-zero
    on any FAIL, with no patient data. The quarterly restore test stays
    manual (SKIP).
  - `/readyz` now also reports the Relay tunnel (`relay`: connected, down
    or off).
- A training seed profile (#80): `wehelpd seed --profile training`
  (`appliance.sh seed training` on a demo host) adds a synthetic week at a
  naturopath office to Demo Clinic. That's 3 providers, a front desk and an
  admin, 25 fictional patients with links, this week's schedule
  (completed, no-show, cancelled, scheduled), appointment requests and
  message threads. The layout comes from a fixed seed. It's idempotent
  within a week, and every write is audited. It runs only where `seed`
  already may: a demo build with `WEHELP_ENV=dev|demo` (#161), never the
  field image. There's no reset: audit events can't be deleted.

### Fixed
- iOS: a push that arrives while the app is open refreshes the messages
  list, the open conversation and appointments at once. Before, the banner
  arrived instantly but the screen waited for its 10–15 s timer. Coming
  back to the app (for example by tapping the push) refreshes them too.
- A front-desk or admin booking links the provider and patient. Booking
  needed an active link, but a patient the front desk or an admin added was
  linked to no one, and a provider's email invite stays pending (the app
  can't accept it), so such a patient could never be booked. Now the
  booking makes the link, or activates a pending invite, in the same
  transaction, audited as `link.create` (`via: booking`). A revoked link
  stays revoked (409). Providers still book only their linked patients.
- iOS: the "Can't reach" screen always offers pairing a practice, not only
  with two or more. A phone with one practice whose Relay moved was stuck on
  that screen with only "Try again".

### Changed
- Record the owner-approved remaining V1 direction and prior art: support-entered
  registration, the shared provider app, field identity/privacy/transport
  hardening, truthful audit-export guarantees, Mac host telemetry and demo
  reset semantics. GitHub specifications now separate acceptance evidence,
  failure cases and release gates; #143 is split into #208–#210, and the
  corporate Cloud Dashboards PoC (#58) is deferred to Post-V1. These are
  specifications, not claims that the controls are implemented.
- Demo practices don't lock the app. A demo build of the office (build tag
  `demo`, as on Barnacle) answers `/me` with `demo: true`. The app then
  skips its Face ID or passcode lock for that practice, so testers can use
  borrowed phones. Switching to a real practice shows the lock again. A
  practice's Back Office is never a demo build, so it always locks.
- `appliance.sh init-office` passes the install token to the container by
  name, so it never appears in a command line (#182).
- The dev `registry` Compose profile publishes Postgres on 127.0.0.1:5433
  only (#182).
- The cap on live video visits per office now fits the practice instead of
  a fixed 4 (#175). The Registry puts `max_visits` in each office's signed
  list entry: one more than the practice's active providers, at least 4, or
  a number support sets in the console. The Relay enforces it per office
  (`relay_turn_max_visits` is only the fallback for an older list), can cap
  its total with `relay_turn_max_total_visits`, and sends the office its cap
  with the TURN key, again whenever it changes. At the cap, joining a new
  visit answers 503 `video_busy` with `Retry-After`, audited as
  `visit.turn_busy`, and the app says "Video is busy right now" instead of a
  call that fails inside TURN. A visit nobody has polled for a minute stops
  counting. Registry migration V4 adds `practices.turn_max_visits`.
- The ecosystem diagram shows the practice side of the Relay: the Back Office
  as a mini PC appliance, and the front desk as a laptop using the staff web
  over the office Wi-Fi, where the pairing QR code is shown. The
  presentation's "How it works" slide matches.
- The readme opens with a hero diagram of the WeHelp ecosystem
  (`presentation/img/ecosystem.svg`): the app and the Back Office tied
  together by QR pairing, the Relay, the Registry and the practice website
  and blog. The presentation has a Blog slide after the website (23 slides).
- The presentation covers the new features: the front desk's day, the
  practice inbox, care teams, practice types, and switches with provider
  office hours, with an updated status slide (22 slides).
- The readme and the presentation lead with who we are: built by providers,
  for providers, from 30 years in healthcare services technology and patient
  services, with licensed providers on the board of directors. The deck has a
  new "Who we are" slide after the cover, and its page numbers now count
  themselves.
- The readme opens with screenshots (staff web, iPhone app, practice website)
  and links to the overview presentation, published from `presentation/` to
  GitHub Pages (https://zacharyelston.github.io/WeHelp/) by
  `.github/workflows/pages.yml`. The presentation is a static copy of the
  WeHelp overview deck: arrow keys or swipe to move, and it prints as one
  slide per page.
- Proposal for other kinds of practice (docs/PRACTICE_TYPES.md, epic #112):
  presets with their own words for therapy, allied health and salons; switches
  so a practice or a provider can turn messaging off; a practice inbox; and
  care teams, where a patient approves the providers who share their thread.
- AGENTS.md and CONTRIBUTING.md brought up to date (#13):
  - issues and PRs live in zacharyelston/WeHelp;
  - V1 milestones and the docs to read first;
  - required CI checks and `-race`;
  - the V1 rules: patient data only in the Back Office, no patient
    passwords, and the OpenAPI spec kept in step with the router;
  - Flyway migrations (the last goose reference is removed).
- WeHelp Registry added to the V1 architecture: a fourth service, the
  corporate control plane and support arm.
  - WeHelp support registers practices and their providers in a Registry
    console, issues single-use install tokens, and assigns each practice to
    a Relay.
  - Relays accept only offices on an ed25519-signed, versioned office list
    pulled from the Registry. They cache the last good list and reject
    rollbacks; suspending a practice disconnects it within a minute.
  - Back Offices enroll with an install token and pull their provider roster
    from the Registry.
  - The Registry holds no patient data and has an import-boundary test.

  Services are now Back Office, Relay, Registry and two mobile clients
  (patient app and provider app, #69), plus dashboards for the first three.
  The in-office Kiosk iPad (#8) is a post-V1 client that talks to the Back
  Office over the office LAN. Security, plan, observability and lit review updated.
- Observability designed (docs/OBSERVABILITY.md):
  - a Grafana Alloy sidecar on every Back Office and Relay;
  - optional Grafana, Loki and Prometheus dashboards that can run anywhere,
    behind a load guard that stops them on small hosts under pressure;
  - an ops stream with no identifiers, and a separate audit stream (the
    `audit_events` chain, IDs only) sent only to BAA-covered destinations
    over syslog or OTLP;
  - WeHelp Cloud Dashboards specified as a PoC corporate offering.

  Plan track V1-OBS added to docs/V1_PLAN.md.
- V1 architecture defined (docs/ARCHITECTURE.md rewritten, plus
  docs/SECURITY.md and docs/V1_PLAN.md). In V1:
  - A local Back Office holds all patient data.
  - The Back Office connects out to a Relay over SSH with an office key.
  - Phones reach their office through the Relay with HTTP `CONNECT` and
    TLS end to end to a Back Office key pinned by in-office QR pairing.
  - Patients have no passwords; device keys are their credential.
  - The staff UI is LAN-only with TOTP.
  - Telehealth is 1:1 WebRTC with media through TURN on the Relay.
  - Push and the practice website go through the Relay with no PHI.

  The bar is HIPAA technical safeguards plus OWASP ASVS L2 and MASVS L2,
  with an external audit and pen test before field use. V1_PLAN.md sets
  milestones V1-M0 to V1-M7 and lists what is kept, replaced and deferred
  from the PoC. The lit review covers SSH, `CONNECT`, TURN, TOTP and
  backups.
- CI and `make test` run Go tests with the race detector (`-race`). `main`
  is protected: the `go`, `ios` and `docker` CI jobs must pass and branches
  must be up to date before a PR merges (#30).
- iOS signing: automatic signing with team `WZYFJ8TSWD`, bundle ID
  `com.zacelston.wehelp` (`com.wehelp.app` is not registered to this team).
  The HealthKit entitlement is kept; it provisions on this paid team.
- Migrations moved from embedded goose to **Flyway**: `db/migrations/
  V{n}__name.sql`, `flyway_schema_history` + checksum enforcement. The
  compose `migrate` service runs before `server`; `make db-migrate` is the
  containerized path and `wehelpd migrate` wraps a local flyway CLI.
  `serve`/`seed` no longer auto-migrate; `WEHELP_AUTO_MIGRATE` is gone.
  Existing goose-managed dev DBs need `flyway baseline` or a fresh volume.
- Server binary renamed `wehelp` → `wehelpd`: viper found the binary itself
  when it sat at the container's CWD and tried to parse it as YAML.

### Added
- The Relay runs on an always-on host (#180). The Demo Relay moved from the
  dev laptop to Barnacle.
  - **`deploy/relay.sh`:** a launchd job on a Mac mini, built in a Go
    container, so the Mac needs no Go. On Linux it runs
    `deploy/relay/docker-compose.yml` with host networking, the way the
    Ubuntu Mac mini will. Docker on macOS can't carry TURN's UDP.
  - **The Relay folder** holds its keys, `relay.env`, cache, sites and logs.
    Relative paths in `relay.env` are inside it, so the folder moves between
    hosts as it is.
  - **`wehelp-registry relay update`:** changes a Relay's name or addresses
    (audited), for moving it with its keys.
- Demo hosts and field installs are separate (#161).
  - **Demo hosts:** `WEHELP_DEMO=1` in `deploy/.env` builds the image with
    the `demo` tag. That brings the seed command (`appliance.sh seed`) and
    the fixed demo logins on the sign-in page (`/demo.js`), so demos stay
    easy. `appliance.sh demo-setup` does it in one step: it fills in
    missing demo defaults in `.env` (`WEHELP_DEMO`, `WEHELP_STAFF_HOSTS`,
    `WEHELP_TIMEZONE`), then runs `up` and `seed`.
  - **Field installs:** neither the seed command nor the demo logins.
  - **Seed:** refuses unless `env` is `dev` or `demo`.
  - **`env`:** now defaults to production.
  - **Database:** `appliance.sh` generates per-install passwords in
    `deploy/db.env` (mode 600, not in the server's environment) and moves
    an existing install over in place. The server connects as `wehelp_app`
    (migration `V23`): data only, no schema changes, and no UPDATE or DELETE
    on the audit and ledger tables.
  - **Compose files:** never name the passwords. Each run writes them from
    `db.env` into generated per-service env files (`db-owner.env`,
    `db-server.env`), so secret scanners stay quiet. Without `db.env` the
    stack won't start.
  - **Container:** runs as non-root (uid 65532).
  - **Backups:** include `db-env.age`, and a restore sets up the roles
    first.
  - **CI:** checks the field image has no demo logins or seed command and
    runs as non-root.
- Security checklists (#44) in `docs/security/`:
  - OWASP ASVS 5.0 Levels 1 and 2: 253 rows. 113 met, 62 not applicable,
    2 accepted, 76 gaps.
  - OWASP MASVS v2 for the iOS app: 24 controls. 9 met, 1 not applicable,
    2 accepted, 12 gaps.
  - HIPAA §164.312 technical safeguards: 8 met, 4 gaps, plus the
    practice-owner documents.

  Each row cites the code, test or doc behind it, and every gap is tracked
  (#159–#170, #143, #11). The review found a stored XSS in the staff web
  (#159, P0).
- CI security checks (#43).
  - `govulncheck` and `staticcheck` at pinned versions, and the Relay and
    Registry import-boundary tests as a named step.
  - A Release iOS build checked by `scripts/check-ios-release.sh`: App
    Transport Security, file sharing, Debug leftovers, and demo or LAN
    strings.
- Encrypted backups with a tested restore (#41). Runbook:
  `docs/BACKUPS.md`.
  - **Backup:** `appliance.sh backup` streams `pg_dump`, the office keys
    volume and `.env` straight into `age`, encrypted to the practice
    owner's public key, with a SHA-256 manifest. It keeps 30, copies to
    `WEHELP_BACKUP_COPY_TO` (a mounted drive or rsync target), and records
    each run in `backup_runs` (migration `V22`).
  - **Nightly:** `appliance.sh install-backup` runs it at 02:30.
  - **Restore:** `appliance.sh restore` checks the manifest, proves the key,
    replaces the database, keys and `.env`, and waits for health.
  - **Alerts:** the staff web warns admins when the last backup failed or
    the last good one is over 36 hours old.
  - **A second stack:** the appliance compose file takes
    `WEHELP_DB_PORT`, `WEHELP_HTTP_PORT`, `WEHELP_HTTPS_PORT` and
    `WEHELP_RELAY_TUNNEL`, so a restore test can run beside another stack.
  - **Tested:** Barnacle's backup was restored on a laptop from its
    external-drive copy. The chain verified, counts and office CA matched,
    and the restored copy never connected to the Relay.
- Logging hygiene (#42).
  - **Back Office:** the seed command no longer logs emails, only user IDs.
    `TestNoPHIInLogs` runs a full flow (seeding, pairing, messages,
    appointments, requests, inbox, orders, a video visit, refusals) and
    fails if any name, email, text, SDP, token or code is logged.
  - **Relay:** logs go to daily files under `{relay_dir}/logs`, kept
    `relay_log_days` (7). `relay_log_dir: -` keeps stderr.
    `TestRelayLogsNoDeviceAddress` checks phone addresses never appear.
- Reads of patient data are audited (#40). Each read of patients, a
  patient's devices, links, appointments and requests, messages and
  conversations, the practice inbox, care teams, orders or a pairing invite
  records one event per request, never per row.
  - **The event:** actor, device (or `staff_web`), the patient or practice,
    and the request's ID, date and status filters. No names or text.
  - **Fails closed:** the response waits for the event. If it can't be
    written, the request fails with a 500 and the data isn't sent.
  - **No double counting:** thread reads and care team messages were
    already audited, and are still recorded once.
- Audit chain checks (#24).
  - **Command line:** `wehelpd audit verify [--tenant name-or-id]` checks
    every practice's chain and exits 1 on a break.
  - **Staff web:** `GET /api/v1/staff/audit/verify` lets an admin check
    their own practice on the LAN, and is recorded as `audit.verify`.
  - **Daily check:** the Back Office checks every chain daily and logs any
    break.
  - **Older forks:** before #28, two events written at the same moment
    could link to the same parent. `Verify` accepts and counts these forks
    among version-1 events, and still catches deletions. On Barnacle, 395
    events verified: 261 by link only and 3 forked.
- Video visits in the iOS app (#6, step 4).
  - **Starting:** appointments inside the join window show **Start video
    visit** to the provider, and **Join video visit** to the patient once it
    is open. Until then the patient sees "Your provider will start the video
    visit". The Schedule checks every 15 seconds while a window is open.
  - **Call screen:** the other person full screen, your camera in a corner;
    mute, camera, flip, and leave or end. Works on iPad in every orientation,
    with ⌘D and ⌘E shortcuts.
  - **WebRTC** (`stasel/WebRTC` 157.0.0, pinned) with relay-only ICE and
    AES-GCM SRTP preferred. The provider offers and re-offers with an ICE
    restart. The call is ended if the other phone's DTLS certificate doesn't
    match the fingerprint in its SDP.
  - Third-party licences screen in Settings. First iOS unit-test target
    (`WeHelpTests`).
  - The signal long-poll now waits 15 seconds, under the app's 20-second
    request timeout.
- Video visits in the Back Office (#6, step 3).
  - **Starting:** any scheduled appointment can become a video visit. The
    provider starts it from the app (`POST
    /appointments/{id}/visit/start`), from 10 minutes before the start to 30
    minutes after the end. The patient gets the `visit_starting` push and
    joins (`.../visit/join`, 409 `not_started` until then).
  - **Who:** only the appointment's provider and patient, from the app (no
    browser sessions, front desk or admins), one device each. Joining from
    another device replaces the first.
  - **Signaling:** `/visits/{id}/signals`, a long-poll held in memory only,
    never stored or logged. The provider makes the offers and the patient
    answers.
  - **TURN credentials** are minted from the key the Relay sent at connect.
    Without one, joining answers 503 `video_unavailable`.
  - **Ending:** the provider ends it for both, or it closes itself after the
    window. Ending leaves the appointment for staff to mark. The staff web
    shows each visit as in progress or how long it lasted.
  - **Audit:** `visit.open`, `visit.join`, `visit.leave`, `visit.end`.
    Migration `V20`, the `video` switch, and MOBILE_CLIENT.md §15.
- Video-visit TURN in the Relay (#6, step 2): `pion/turn` on UDP and TCP
  3478, and TURN over TLS (normally 443) with a Let's Encrypt certificate
  the Relay fetches itself, or certbot files. Off until
  `relay_turn_public_ip` is set.
  - When an office's tunnel connects, the Relay sends it a TURN key
    derived from a master secret that never leaves the Relay. The office
    mints per-visit credentials itself (`internal/turncred`); it never asks
    the Relay for anything.
  - The Relay accepts a credential only while its office is on the signed
    list, before it expires, and if minted no more than 12 hours ahead.
    Limits: 8 allocations per visit and 4 live visits per office. Peers are
    limited to the Relay's own relay address, so TURN is not an open proxy.
    Allocations live 5 minutes and permissions 2 minutes unless refreshed.
  - A counts-only log line a minute while visits are live. Runbook in
    `docs/RELAY.md`.
- Provider roster (#62): the Back Office pulls its practice's providers
  from the Registry.
  - `POST /v1/roster` on the Registry. The office signs the request with
    its office SSH key; the Registry answers with only that practice's
    entries, signed with the Registry key the office pinned at install.
    Suspended practices get none. Shared types in `internal/roster`.
  - The Back Office pulls it every `roster_interval` (15 minutes) and on
    **Sync now** in the Staff screen's new "From the Registry" card. A new
    provider shows as needing an invite, and **Invite** sends the same
    set-up link as Add staff (#100). Admins on the roster are added with
    `wehelpd staff add`.
  - A provider removed in the Registry, or dropped from the roster, is
    flagged for the admin to disable. The sync never disables or deletes
    an account. Accounts and credentials stay in the Back Office.
  - Audit: `roster.sync` (on Sync now, and on scheduled pulls that changed
    something) and `staff.invite` with `via: roster`. Migration `V19`.
- Registry console (#61) at `/console/` for WeHelp support.
  - Sign-in is a password plus an authenticator code, with the same rules as
    the Back Office staff UI (#38): codes work once, 5 failures lock the
    account for 15 minutes, `__Host-` cookie sessions, and a CSRF header on
    writes. Accounts come from `wehelp-registry staff add`.
  - Roles: `support` runs practices (admin contact, subscription status),
    providers and admins (credentials, licence number) and install tokens.
    `registry-admin` can also suspend, reactivate, move a practice that
    isn't installed yet to another Relay, and set Relay capacity.
  - Health: each Relay posts a status report after every list sync, signed
    with its SSH host key: its list version and connected office IDs. The
    console shows Relays up or down and each office connected or not.
  - Every console action goes into the Registry's hash chain as
    `console:<email>`. The console has a strict CSP and no inline script.
    Migration `db/registry/V3`.
  - Shared sign-in pieces (TOTP, tokens, QR, argon2id) moved to
    `internal/staffauth`, so the Registry doesn't import the Back Office's
    `auth` package.
- `docs/MOBILE_CLIENT.md` (#140): the contract every WeHelp app follows,
  written down for the Android app. It covers the pairing QR code and its
  checks, the two pinned TLS hops and how a pin is computed, the device key
  and its proof message, pairing, session and refresh rules with the new
  error codes, several practices on one phone, error states, lock and what
  may stay on the phone, push, polling, practice words and switches, and
  data formats. `docs/SECURITY.md` said office pins were an SPKI SHA-256;
  they are the SHA-256 of the uncompressed X9.63 key, as the code does.
- Practice blog (#121).
  - The Website tab has a **Blog** card. Staff write timely or seasonal
    posts: a title, the text, an optional uploaded image, a go-live time,
    and a draft flag. They can also edit and delete posts.
  - The website gets a Blog page (newest first), a page per post, and the
    latest three posts on the home page. The Blog link appears only once
    there are posts. Drafts and future posts stay off the site.
  - Once the site has been published, the Back Office republishes it by
    itself within a minute of a scheduled post coming due. That publish is
    audited with no actor and marked `scheduled`.
  - Posts are plain text, escaped by the templates, and practice content
    only. Audited as `site_post.create`, `site_post.update` and
    `site_post.delete`.
  - Routes under `/api/v1/site/posts` (staff, LAN only). Migration
    `V17__site_posts.sql`.
- Care teams (#111, docs/PRACTICE_TYPES.md phase 4).
  - A patient chooses a group of providers who share one thread with them,
    for continuity of care.
  - A provider already caring for the patient proposes a colleague, who
    joins only when the patient approves. The patient can also add a
    provider directly.
  - Members are providers, never the front desk or admins.
  - A new member sees earlier messages only if the patient allows it. A
    removed member sees nothing, past or future.
  - Every member's read of the thread is audited (`care_team.read`), and
    the patient sees who read it and when.
  - Disabling a provider removes them from every team.
  - A new **Care teams** switch: off by default for therapy, never on for
    a practice that isn't clinical.
  - Providers get a **Care teams** tab in the staff web. Patient approval
    in the app waits for the test devices; the API is ready.
  - Routes under `/api/v1/care-teams`. Migration `V16__care_teams.sql`.
- Practice inbox (#110, docs/PRACTICE_TYPES.md phase 3).
  - Patients message "the practice", and the front desk and admins answer
    in a new staff web **Inbox** tab. Each reply is signed with the staff
    member's name.
  - Providers don't see it, and it is a table of its own
    (`practice_messages`), separate from provider threads.
  - It follows the messages switch. A provider with messages off now
    points patients to the practice inbox.
  - The front desk no longer sees the always-empty Messages tab.
  - Routes: `GET`/`POST /api/v1/practice-inbox`,
    `GET /api/v1/practice-inbox/{patientID}` and
    `POST /api/v1/practice-inbox/read`. Patients use them on any channel;
    the front desk only on the office LAN. Audited as
    `practice_message.create`; pushes are generic. Migration
    `V15__practice_inbox.sql`.
  - The app screen follows when the test devices are back.
- Practice and provider switches (#109, docs/PRACTICE_TYPES.md phase 2).
  - Settings has a **Features** card. An admin turns off messages,
    appointment requests, reminders, check-ins, video, the store, the
    library or the website.
  - Each practice type has defaults: therapy starts without the store and
    check-ins; a salon without check-ins, video and the library.
  - A switched-off feature disappears from the staff web, and the server
    refuses it with 409, so older app versions can't use it either. Staff
    keep the catalogue while the store is off. The reminder worker skips
    reminders and check-ins that are off. The website drops its Store and
    Library pages when those are off. Nothing is deleted.
  - Providers choose how they take messages, under **My profile** →
    Messages:
    - on, as before;
    - office hours: messages arrive any time, but outside the hours the
      patient gets an automatic reply (at most once every 12 hours) and
      the provider gets no notification;
    - off: patients get a 409 asking them to call the practice. Staff can
      always write to patients.
  - New routes: `PUT /api/v1/practice/features` (admins, audited as
    `practice.features`) and `GET`/`PUT /api/v1/staff/profile/messages`
    (audited as `staff.message_settings`). Automatic replies are audited
    as `message.auto_reply`. Migration `V14__switches.sql`.
- Practice types (#108, docs/PRACTICE_TYPES.md phase 1).
  - An admin picks the practice type on a new **Settings** screen:
    naturopathic, therapy and psychology, allied health, or salon and
    personal services.
  - The type sets the words: patient or client; provider, practitioner,
    therapist or stylist. The staff web and the website use them (the
    website at its next publish). The API and database still say patient
    and provider.
  - A salon is not clinical, so the staff web hides check-in reminders.
    Security is the same for every type.
  - New routes: `GET /api/v1/practice` for anyone signed in (the app will
    read the words, and it holds no patient data), and `PUT` for admins on
    the LAN, audited as `practice.update`. Migration
    `V13__practice_settings.sql`.
  - Not yet: the app using the words, and the Registry recording the type
    (waits for the Registry console, #99).
- Office role for the front desk (#106).
  - A third staff role, `office`, sees every patient and every provider's
    schedule and appointment requests. The front desk books, changes and
    confirms visits for any provider (`provider_id` on
    `POST /appointments`), and works orders, the store, the website,
    patients and pairing. It cannot read message threads or manage staff.
  - `admin` keeps everything plus setup. Its Providers screen becomes
    **Staff** and invites providers or front-desk accounts.
  - Appointment lists carry provider and patient names. The staff web
    shows a Provider column and picker for the front desk and admins.
  - The seed adds `frontdesk@demo.wehelp` / `wehelp-demo-office`, and the
    sign-in page lists it. `wehelpd staff add --role office` also works.
    Office accounts can't pair their own phone yet: the app has no
    front-desk view. Migration `V12__office_role.sql`.
- Website themes (#104).
  - The Website tab has a **Look** card with six colour schemes: sage
    green, ocean blue, sunset coral, plum, warm clay and midnight (dark).
  - Each theme colours the header, footer, a banner on the home page,
    buttons, cards and photo frames. Visitors in dark mode get a dark
    version with the theme's accent. The choice is saved with the site
    content and applied at the next publish; the default is sage.
  - Fixed: page content no longer sits flush against the header.
- Staff profiles for the website bios (#102).
  - Every staff member has **My profile** in the staff web: name as shown,
    credentials, bio and a photo (PNG, JPEG or WebP, at most 2 MB, checked
    by content). They choose whether the practice website shows them.
  - Publishing the website turns those profiles into provider bios with
    their photos. A bio typed in the Website tab with the same name is
    replaced; other typed bios stay. The Website tab lists which bios come
    from profiles. Disabled accounts never appear.
  - New routes `/api/v1/staff/profile` and `/api/v1/staff/profile/photo`
    cover your own profile only. They are LAN only and audited as
    `staff.profile_update`. Migration `V11__staff_profiles.sql` adds them.
- Admins register providers from the staff web (#100).
  - A **Providers** button (admins only) lists the practice's provider
    accounts. An admin adds one by name and email and gets a set-up link.
  - The link works once, for 24 hours, on the office network. Its token is
    in the URL fragment, and only its SHA-256 is stored. The provider opens
    it, chooses their own password and enrolls TOTP as at a first sign-in.
    Admins never see or set the password. A new link replaces the old one.
  - Admins can disable a provider and enable them again. Disabling ends web
    sessions, revokes refresh tokens and refuses sign-in and API calls.
    Nothing is deleted, and paired devices stay paired.
  - New routes: `/api/v1/staff/setup` and `/api/v1/staff/providers…`. They
    are LAN only, return 403 for providers, and are audited (`staff.invite`,
    `staff.setup`, `staff.disable`, `staff.enable`). Migration
    `V10__staff_invites.sql` adds them.
  - This is the Back Office half of #62: Registry roster entries will feed
    the same invite. `wehelpd staff add` still works on the host.
  - The demo seed adds an admin, `admin@demo.wehelp`. The staff sign-in page
    lists it under Dr. Ada's demo login.

- Practice website with a library and a supply store (#97, most of #5).
  - Staff build the site on a new staff web **Website** tab: about us,
    hours and contact, provider bios, services, "Get the app and join" (the
    download and the in-office QR pairing steps), podcast links, and
    uploaded PDFs and images (10 MB each; contents must match the type).
  - **Save and publish** renders six static pages with `html/template` and
    streams them as a tar over a new `wehelp-site` SSH channel on the
    office's Relay connection. The Relay files the site under the office ID
    that connection authenticated as, and serves it read-only at
    `https://<relay>:8444/s/<office-id>/` (`relay_site_addr`,
    `relay_site_public_url`, optional `relay_site_cert`/`relay_site_key`).
  - The Relay accepts only `.html .css .txt .pdf .png .jpg .jpeg .webp
    .ico`, at most 200 files, 10 MB each and 25 MB in all, refuses HTML
    with `<script`, and serves every file with a `default-src 'none'` CSP
    and `nosniff`. A suspended office's site goes away with its listing.
  - The **Store** tab keeps the catalogue (the seed and a button load ten
    demo vitamin, supplement and herbal products). The public site shows
    it and says "Order in the WeHelp app"; payment is at pickup or by
    invoice.
  - Patients order in the app's new **Store** tab (quantities, a note, My
    orders, cancel while placed), through the pinned tunnel only. Staff work
    orders on the staff web **Orders** tab (ready, fulfilled, cancelled),
    on the office LAN only. Every change is audited, and staff changes
    send the new fixed push category `order_update` ("Your supply order has
    an update.").
  - The app's **Library** (Store tab and Today) lists podcast links and
    opens downloads in Quick Look, fetched through the pinned tunnel.
  - Migration V9 adds `site_profiles`, `site_files`, `products`, `orders`
    and `order_items`. Still open on #5: `apple-app-site-association` and
    Universal Links.
- Appointment requests (#68, #39).
  - Patients tap **Request** on the Schedule tab and pick a provider, 1–3
    preferred days with a time of day, and an optional reason. Up to 3 can
    wait per provider, and a patient can cancel one while it waits.
  - Providers see waiting requests on the staff web Appointments tab. They
    confirm at an exact time, which books the visit with the usual overlap
    check, or decline with an optional note. Their iPad lists what's
    waiting.
  - The patient gets a message in their thread and a push. Two new fixed
    Relay categories carry no details: `appointment_update` ("Your
    appointment request has an update.") and `appointment_request` ("A
    patient has requested an appointment.").
  - Every step is audited. Migration V8 adds `appointment_requests`.
- Reminders (#19), sent by the Back Office as reminder messages from the
  provider, with a push through the Relay.
  - Appointment reminders at each `reminder_offsets` offset (default 24h
    and 1h). Only the closest one that has come due is sent, so late
    bookings and restarts don't send a burst. Rescheduling moves them;
    cancelled appointments and revoked links get none.
  - Check-in reminders on a cadence the provider sets per patient
    (`PUT /api/v1/links/{id}/checkin`, 1–90 days, or off), shown as a
    "Check-in reminders" choice on the web Links tab. They're skipped if
    the patient already checked in during the last period, and wait for
    08:00–20:00 office time (`WEHELP_TIMEZONE`).
  - A worker in `wehelpd serve` checks every `reminder_interval` (1m; 0
    turns it off) and stops with the server. Each reminder is recorded
    in `reminders_sent` under a key naming it, in the same transaction as
    the message and its audit event, so nothing is sent twice.
  - A minute's poll replaces the River jobs the plan named: same
    guarantees at this scale, with no second migration system next to
    Flyway.
- Staff web: the Appointments and Links tabs show people's names instead
  of ID prefixes. The booking form now defaults to tomorrow at the next
  hour in local time (it showed UTC), and to 30 minutes long (its end
  time used to equal its start).
- The Relay writes a new office list to its cache before using it, so the
  cache is never behind the list in use. This also fixes a flaky
  `TestListRollbackAndForgeryRejected`.
- iPad: the app turns to landscape and upside down (iPhone stays
  portrait), for staff iPads docked with a Bluetooth keyboard. Cmd-Return
  sends a message.
- Push notifications through the Relay (#7).
  - The Relay sends Apple pushes with a hand-rolled HTTP/2 + ES256 client;
    the APNs key never leaves the Relay. Only three categories exist
    (`new_message`, `appointment_reminder`, `visit_starting`), and the
    Relay supplies their fixed, generic text. Each office is limited to
    120 pushes a minute, and logs show the office, category and result,
    never the token.
  - The Back Office asks over its SSH tunnel (`wehelp-push`, through
    `tunnel.Pusher`). A new message pushes to every active device of the
    recipient. Tokens Apple rejects are cleared, and so are tokens of
    revoked devices.
  - `PUT` and `DELETE /api/v1/devices/current/push` (device sessions only)
    register a device's token; migration V6 stores it. The Relay keeps no
    tokens.
  - iOS asks for notification permission after unlock and registers its
    APNs token with every paired practice. Alerts show while the app is
    open too.
  - Relay config: `relay_apns_key`, `relay_apns_key_id`,
    `relay_apns_team`, `relay_apns_topic`.
- Message threads with names (#67).
  - `GET /api/v1/messages?with=<user>` returns the whole conversation with
    one linked user, in both directions, newest first and paginated. It's
    404 if the users aren't linked, and every thread read is audited
    (`messages.read_thread`).
  - `GET /api/v1/conversations` lists everyone you're linked with, with
    their name, the latest message, who sent it, and the unread count.
  - iOS Messages is now a conversation list that opens a chat-style
    thread with a reply box. Your own sent messages show, and opening a
    thread marks their messages read. The staff web Messages tab works
    the same way.
- Several practices per device (#89).
  - The app keeps an **account** for each practice it's paired with:
    practice, name and role. Each has **its own Secure Enclave key, refresh
    token and session** (Keychain items scoped by account), so nothing is
    shared between practices.
  - Settings → **Accounts** lists them, admin first, then provider, then
    patient. Tap one to switch the whole app, or use **Pair another
    practice**. Re-pairing the same person at the same practice replaces
    their entry.
  - If a practice revokes the device, only that account is removed. There's
    still no unpair in the app.
  - A device paired before this update moves to the new storage
    automatically, with no re-pairing.
  - Removed: the Debug-only saved-servers and password screens and their
    `WEHELP_DEV_*` settings. Password login has been office-LAN only since
    #38; devices join by QR.
- Provider devices (#69): one WeHelp app, with the role set by pairing.
  - On the staff web UI, **My devices** shows a staff member's phones and
    iPads, with **Pair my phone or iPad** and Revoke.
  - `POST /pairing/invites` takes `user_id` for any account: a provider for
    linked patients or themselves; an admin for anyone in the practice.
    Redeem issues a device-bound session with the account's own role.
  - The app opens on a provider **Today** view (today's visits with patient
    names, what's coming up, and unread messages) alongside Messages,
    Schedule and Settings.
  - `/links` now carries `provider_name` and `patient_name`, and `/me`
    carries `display_name`, so messages and visits show names instead of
    "Patient" or "Care team".
  - **The app never unpairs itself** (owner direction). Settings' Sign out
    is replaced by **Lock now**, and the Devices screen is read-only. Only
    the practice removes a device, from the staff page; the app clears
    itself when the server refuses a revoked device.
  - A paired device that can't reach its practice at launch shows "Can't
    reach <practice>" with **Try again** and keeps the pairing. It used to
    drop to the pair screen.
- Staff sign-in and the LAN-only staff surface (#38).
  - Staff web UI sign-in is password + TOTP. The first sign-in walks through
    authenticator setup (QR code and key). Codes work once, and five failures
    lock the account for 15 minutes.
  - Sessions are server-side, in a `__Host-wehelp_staff` cookie (Secure,
    HttpOnly, SameSite=Strict) with a CSRF token on every change. The web UI
    no longer keeps a token in `localStorage`.
  - New routes: `/api/v1/staff/{login,totp/setup,totp/confirm,me,logout}`.
    Migration V5 adds the TOTP columns, lockout and `staff_sessions`.
  - HTTPS for staff on `:8443`, with a certificate issued by the office's
    local CA for `staff_hosts`. Outside dev, plain HTTP only serves
    `/healthz` and `/readyz` and redirects everything else.
    `appliance.sh export-ca` prints the CA for staff machines.
  - Staff routes (patients, pairing invites, patient devices), password
    login and registration, and the web UI all return 404 through the
    Relay. The listener decides this, not a header.
  - Bearer `/auth/login` also needs a TOTP code for staff.
  - Self-registration is dev-only; staff accounts are created with
    `wehelpd staff add` on the Back Office.
- Device management (#10).
  - `GET /api/v1/devices` lists your own devices, marking the current one,
    and `DELETE /api/v1/devices/{id}` revokes one.
  - Staff on the office LAN can list and revoke a linked patient's devices
    with `GET` and `DELETE /api/v1/patients/{id}/devices[/{deviceID}]`.
  - A revoke also ends that device's refresh tokens and is audited
    (`device.revoke`).
  - Access tokens now carry the device ID (`did`), and every request checks
    it, so a revoked device is locked out at once rather than after up to 15
    minutes. Patient tokens without a device are refused.
  - Staff web UI: each patient's device count opens their devices, with
    Revoke.
  - iOS: Settings → Devices lists this account's devices (read-only since
    #69: only the practice revokes). If the server ends this device's
    session, the app clears it.
- QR pairing; patients have no password (#37).
  - Staff web UI: a new **Patients** tab to add patients (no password) and
    **Pair phone**, which shows a QR code with the office's pairing identity
    and a single-use invite (10 minutes, bound to one patient, only its hash
    stored). It reports when the phone has paired.
  - API: `POST/GET /api/v1/patients`, `POST /api/v1/pairing/invites`,
    `GET /api/v1/pairing/invites/{id}`, and `POST /api/v1/pairing/redeem`.
    Redeem takes a proof from the phone's new key, enrolls the device and
    returns the device-bound session. Every step is audited. Staff routes are
    refused through the Relay.
  - Patient password login and patient self-registration are refused.
    Migration V4 makes `users.password_hash` nullable, and the seed creates
    patients with no password.
  - iOS:
    - signed-out phones open on **Pair with your practice**, with a VisionKit
      QR scanner and a paste fallback;
    - the app locks with Face ID, Touch ID or the passcode whenever it goes
      to the background;
    - the saved-practices and password screens are Debug-only, for staff
      demos.
  - First Back Office database tests (`internal/server/pairing_db_test.go`).
    They apply all migrations and the seed to a fresh database and cover:
    pairing, single use, expiry, the wrong key, unlinked patients, refusals
    through the Relay, and no patient passwords.
- Practice onboarding plan and training material (`docs/onboarding/`):
  - an onboarding runbook, practice side first: Registry setup, Back Office
    install and Relay connection, the provider app, a day-by-day training
    week on a separate demo kit, roles and RACI, checklists, go-live
    criteria, hypercare and rollback;
  - a video training outline: 13 modules with scripts and a shot list per
    screen, each marked as existing today or waiting on its issue;
  - Interventionaire (questionnaire and intervention tooling, being
    renewed as an iOS app): what it does, which of its design ideas the
    post-V1 kiosk copies, how content bundles plug into the Back Office,
    and the demo script.
- Design note `docs/LOCAL_AGENT.md`: a Mac Pro Back Office running a local
  AI agent for daily routines and approved outreach, with inference and all
  patient data on the box and every agent action audited. Lit-review
  entries for local model runtimes and questionnaire formats.
- Issues: self-registration decision (#79), training seed (#80),
  `wehelpd doctor` (#81), kiosk questionnaires (#82), local agent (#83) and
  training videos (#84).
- The iOS app reaches its office only through the Relay (#36).
  - Every request is an HTTP `CONNECT` to the Relay (`ProxyConfiguration`).
    It pins the Relay's TLS key on the outer hop and the office's key on the
    inner, end-to-end TLS 1.3. Both checks fail closed, with an error that
    says whether the Relay or the office didn't match.
  - Saved practices are now office endpoints: Relay address, Relay pin,
    office ID and office pin. The editor can paste the `pairing` JSON that
    `wehelpd init` prints.
  - `NSAllowsLocalNetworking` and the plain-HTTP, direct-LAN path are
    removed from Debug and Release. The only ATS exception is
    `*.wehelp.invalid`, with TLS 1.3 required.
  - Debug demo practices come from `WEHELP_DEV_SERVERS`
    (`Label|relay|relay pin|office ID|office pin`), compiled out of Release.
  - The Registry now stores each Relay's TLS pin: migration V2,
    `relay add --tls-pin` and `relay set-tls-pin`. Install returns it, and
    the office's pairing identity includes `relay_pin` for the QR code
    (#37).
- WeHelp Relay (#4): the new `wehelp-relay` binary and the office tunnel.
  - Offices connect out over SSH with their office key. The Relay accepts
    only keys on the Registry's signed office list for its own relay ID, and
    offices pin the Relay host key.
  - Phones send TLS `CONNECT <office-id>.wehelp.invalid:443`. The Relay
    splices that onto a `wehelp-phone` SSH channel and sees only the
    phone↔office TLS.
  - The office list is fetched every minute and verified against the pinned
    Registry key. Older or forged lists are rejected. The last good list is
    cached and re-verified at start. Offices dropped from the list are
    disconnected.
  - `wehelpd serve` opens the tunnel by itself once the office is installed.
    It reconnects with jittered backoff and terminates TLS 1.3 with the
    office key. Requests that arrive this way are marked
    (`tunnel.ViaRelay`) so staff routes can refuse them (#38).
  - Logs carry office IDs and byte counts only.
  - Import-boundary test for the Relay, and an in-process end-to-end test
    that includes a no-plaintext check.
  - Runbook: docs/RELAY.md.
- Back Office identity (#35): `wehelpd init`.
  - It creates the office SSH key (ed25519), the office TLS key (P-256) with
    a self-signed certificate for `<office-id>.wehelp.invalid`, and a local
    CA for the staff UI.
  - Everything lives in `office_dir`, mode 700, with keys at mode 600. It is
    never overwritten, and it is safe to rerun.
  - With `--install-token` (or `WEHELP_INSTALL_TOKEN`) it installs with the
    Registry and stores the office ID, Relay addresses and host key, and the
    Registry signing key.
  - It prints the pairing identity for the QR payload: Relay address, office
    ID, and the key pin (SHA-256 of the X9.63 public key, the SPKI key bits).
  - Appliance: an `officekeys` volume and `appliance.sh init-office`.
  - Rotation procedures for each key: docs/OFFICE_KEYS.md.
- WeHelp Registry core (#60): a new `wehelp-registry` binary with its own
  Postgres database and Flyway migrations (`db/registry`).
  - Practices, Relays, offices, install tokens, and every published office
    list.
  - `POST /v1/install`: a single-use, 24-hour install token plus the office
    SSH key returns the office ID, Relay address and host key, and the
    Registry signing key. Reinstalling keeps the office ID and replaces the
    key.
  - `GET /v1/office-list`: an ed25519-signed list with a monotonic version,
    republished in the same transaction as every change. Suspended practices
    drop off.
  - CLI for V1-M2 support work: `relay add`, `practice add`, `suspend`,
    `reactivate`, `token issue`, `keygen` and `audit verify`.
  - Its own hash-chained, append-only audit log, with length-prefixed fields
    and jsonb-safe verification.
  - `internal/officelist` (sign and verify, rollback check) and
    `internal/keyfile` (mode-600 PEM keys that are never overwritten) are
    shared with the Relay and office work.
  - Tests: an import-boundary test, `TestOpenAPICoversRoutes` for
    `api/registry.yaml`, and database integration tests that CI now runs
    against a Postgres service.
  - Runbook: docs/REGISTRY.md.
- Spike S2, telehealth (#33): the answer is yes. Two iOS simulators held 1:1
  WebRTC calls with relay-only ICE through pion/turn. Media was DTLS-SRTP end
  to end, and the DTLS fingerprints matched each peer's SDP. Two calls at
  once used about 6% of one core on the server. The write-up and
  measurements are in docs/spikes/S2.md. The throwaway code in `spikes/s2/`
  is a TURN and signaling server in Go and a SwiftUI app using
  `stasel/WebRTC`. The lit review verdicts for pion/turn and the iOS WebRTC
  framework are updated.
- Spike S1 (#32), written up in docs/spikes/S1.md, with throwaway code in
  `spikes/s1/`. A phone reaches its Back Office through a relay with HTTP
  `CONNECT` and an SSH channel, using TLS 1.3 end to end to a pinned office
  key. The relay's recordings held no plaintext.
  - Finding: default ATS rejects the pinned self-signed office certificate,
    so V1 needs one scoped exception for `*.wehelp.invalid`.
  - Finding: macOS Local Network privacy needs a one-time approval on
    Barnacle for the Demo topology (added to docs/APPLIANCE.md).
- Spike S3 (#34), written up in docs/spikes/S3.md, with throwaway code in
  `spikes/s3/`. The Relay-side sender pushed through the APNs sandbox with
  the team's token key, limited to three fixed PHI-free categories (others
  are refused), and the iOS app received all three.
  - Finding: the key is scoped to `com.zacelston.wehelp` and sandbox only,
    and Apple enforces both.
  - Finding: `sideshow/apns2` pins a vulnerable 2022 `x/net`, so V1 should
    hand-roll the client.
  - Finding: the local Go 1.26.0 toolchain has standard-library
    vulnerabilities; pin a patched toolchain (#43).
- Back Office appliance setup for an always-on Mac mini (#12), with the
  runbook in docs/APPLIANCE.md:
  - `deploy/docker-compose.appliance.yml`: restart policies, Postgres on
    127.0.0.1 only, and a required mode-600 `deploy/.env` holding a
    persistent JWT secret;
  - `deploy/appliance.sh` and `make appliance-*` targets: `up` creates the
    secret if missing and waits for health; `stop-for-reboot` stops the
    stack and Colima cleanly; `install-login` installs a launchd boot job;
  - the boot job waits for Docker, runs `compose up -d`, then checks
    `/healthz` and logs the result. This fixes the server container being
    left in `created` after an abrupt reboot, which restart policies alone
    don't handle.
- iOS Messages tab (#17): inbox from `GET /api/v1/messages`, newest first,
  with kind badges, unread dots and cursor pagination. Opening a message
  marks it read. Compose sends kind `message` to the patient's active linked
  provider (from `GET /api/v1/links`). Pull to refresh plus a 60-second poll
  while the tab is open, and loading, empty and error states. Messages stay
  in memory only. Signed-in screens are covered when the app is inactive, so
  the app-switcher snapshot shows no PHI. `APIClient` gains query
  parameters and RFC 3339 date decoding.
- iOS Schedule tab (#39, list only): upcoming appointments (soonest first)
  and past ones (most recent first) from `GET /api/v1/appointments`, with
  status badges for scheduled, cancelled, completed and missed (`no_show`).
  Pull to refresh, plus loading, empty and error states; nothing is cached
  on disk. Requesting an appointment from the app waits on a patient
  request endpoint in the API.
- Messages API (#14): send, paginated inbox (`?unread`, `?limit`,
  opaque `?cursor`), and mark-read. Sending requires an active
  provider-patient link; creates and read-receipts are audit-chained in the
  same transaction.
- Appointments API (#18): POST/GET/PATCH /api/v1/appointments.
  Provider-only create requires an active link with the patient and rejects
  overlapping scheduled appointments (409); concurrent bookings for one
  provider are serialised with a transaction-scoped advisory lock. List is
  scoped to the caller (provider or patient) with optional `?from`/`?to`
  bounds on `starts_at`. PATCH supports reschedule (re-checks overlap) and
  status transitions only from `scheduled` to `cancelled`, `completed` or
  `no_show` (409 otherwise). All mutations are audited, including the status
  change.
- Staff web UI: a vanilla-JS SPA (`web/`) embedded in the Go binary via
  `embed.FS` and served at `/` by the chi router. No framework, no build
  step, no Node.js. Messaging, appointments (create, list, cancel,
  complete) and links. Unknown non-API paths fall back to `index.html`.
  Patient accounts get a "use the WeHelp app" notice and no data (#9).
  Error text is HTML-escaped. Not yet done: HttpOnly sessions, refresh,
  TOTP and security headers (#38).
- OpenAPI spec (#15): `api/openapi.yaml` is the contract between the Back
  Office, the iOS app and agents. It covers health, auth (including device
  enrol, signed refresh and logout), me, links, messages and appointments.
  CI checks that the spec is valid OpenAPI, and `TestOpenAPICoversRoutes`
  fails if any route registered on the server is missing from the spec.
  CONTRIBUTING.md requires route changes to update the spec in the same PR.
- iOS sign-in with device-bound sessions (#5). There is a sign-in screen
  (practice, email, password, server). After a password login the app creates
  a P-256 key in the Secure Enclave (a software key on the simulator) and
  enrolls it. The refresh token is kept in the Keychain
  (`AfterFirstUnlockThisDeviceOnly`) and the access token in memory only.
  `APIClient` attaches the Bearer token and refreshes once on a 401, with
  single-flight refresh. Launch resumes the session using the device key.
  Sign-out revokes the device on the server and wipes the token and key.
  Settings shows the role from `/api/v1/me`.
- iOS saved connections: each successful sign-in saves its server, practice
  and email (never the password) in UserDefaults. When signed out, the app
  opens on a **Servers** list:
  - "+" opens a New server screen (address with Test connection, practice,
    optional email).
  - Tapping a server opens its own sign-in screen.
  - Each row has an ⓘ button, plus a long-press menu and swipe actions, to
    edit or delete it. The editor has a Delete server button, and each
    sign-in screen has an Edit button. Deletes ask for confirmation.
  - The Debug "Demo" entry can be edited or deleted too, and a deleted demo
    entry does not come back at the next launch.
  - An optional **Label** ("Barnacle") is shown in place of the practice
    name. It is local only; the practice name must still match the server.
  - Debug builds can define several demo servers as `Label|URL` pairs in
    `WEHELP_DEV_SERVERS`, set in the gitignored `Local.xcconfig` (see
    `Local.xcconfig.example`). Each uses the demo login. A "Restore demo
    servers" button brings back any you deleted. Release builds include no
    demo servers or demo credentials.
- Server: `POST /api/v1/auth/enroll` and `/auth/logout`, plus migration
  `V3__devices.sql` (a `devices` table and `refresh_tokens.device_id`).
  - Device-bound refresh tokens need an ECDSA-P256 proof over
    `wehelp-device-v1\n{purpose}\n{refresh_token}\n{unix_ts}` (±5 min).
    Single-use rotation makes a captured proof useless.
  - Patient accounts can only refresh device-bound tokens, which keeps
    patients mobile-only per docs/ARCHITECTURE.md. Staff tokens are
    unaffected.
  - Enroll, refresh and logout are audited.
- iOS: run on a physical device against a local backend. The server URL can
  now be set in Settings (persisted in UserDefaults; it is not a secret),
  with "Save and test connection" calling `/api/v1/ping` and a reset to the
  build default. The build default comes from `ios/Config/*.xcconfig`:
  Debug uses `http://localhost:8080` (simulator) unless a gitignored
  `ios/Config/Local.xcconfig` overrides it with the Mac's LAN address (see
  `Local.xcconfig.example`). Release has no default. This is the first step
  toward clinic-manifest onboarding.
- iOS: `NSAppTransportSecurity` → `NSAllowsLocalNetworking` so dev builds
  can use plain HTTP to LAN hosts only (no `NSAllowsArbitraryLoads`), plus an
  `NSLocalNetworkUsageDescription` for the local-network prompt.
- Seed data + demo script (issue #14): `wehelpd seed` / `make seed` creates a
  demo tenant ("Demo Clinic"), a provider, two patients, a provider-patient
  link (one active, one pending), a short message thread, and an
  appointment — so devs and agents can exercise the API instantly. Applies
  migrations first, so it works against a fresh `make compose-up` stack.
  Idempotent: every entity uses check-then-insert, so re-runs perform no
  writes and append no duplicate audit events. Reuses `auth.HashPassword`
  and `audit.Record` and mirrors the validated SQL in `internal/server`
  rather than reaching around the service layer. Demo credentials are
  printed to stdout; README quickstart updated with login examples.
- Scale budget + deployment shape (docs/ARCHITECTURE.md): the reference
  target is an office appliance PC serving ~500 patients — one Go binary +
  Postgres, no distributed machinery. Cloud tier is the same artifact.
- Lit review (`docs/LIT_REVIEW.md`): prior-art survey per feature, now the
  mandatory first step of spec review. Outcomes: adopt River for
  reminder/job scheduling (transactional enqueue fits our audit model),
  go-oidc for SSO with Zitadel as a candidate self-hosted IdP, apns2 for
  push; reference Open mHealth schemas for check-ins, Formance/TigerBeetle
  for ledger design, immudb/Trillian for audit.
- Spec-review gate: issues flow `needs-spec` → `spec-review` →
  `agent-ready`; reviewer validates approach currency, dependency health,
  testable criteria, and scope. CODEOWNERS requests owner review on all PRs.
- Issue #16 corrected on review: both Go FHIR libraries are stale
  (samply v0.3.2 2022, google/fhir Go v0.7.4 2022); recommendation is now
  hand-rolled minimal R4 structs, matching CMSgov/bcda-app's approach.
- Provider-patient links: invite by email (provider only), patient accepts,
  either side revokes; `GET /api/v1/links` lists the caller's relationships.
  All transitions audited (issue #3). Path param on accept/revoke is the
  *other* party's user ID.
- Rule: all changes land via feature branches + PRs; `main` is protected
  against direct pushes.
- Direction: auth is interim local credentials; the target is OIDC SSO —
  Google, Microsoft, Sign in with Apple (issue #18).
- Contribution rules: one-line commit subjects, notes live in this
  changelog, no heredocs in shell commands (write files instead).
- Auth: register/login/refresh endpoints with argon2id password hashing,
  short-lived HMAC JWT access tokens, and rotating opaque refresh tokens
  stored as SHA-256 hashes (PR #17, issue #1).
- `refresh_tokens` table and unique `tenants.name` (migration 00002).
- `GET /api/v1/me` behind Bearer middleware resolving
  (tenant, user, role) into request context.
- GitHub project board (WeHelp Roadmap, project #5), 5 milestones,
  16 seeded issues, `agent-ready`/`needs-spec` label taxonomy.
- CONTRIBUTING.md, issue/PR templates, AGENTS.md agent bootstrap notes.

### Fixed
- Stored XSS in the staff web (#159).
  - **The bug:** patient names went into inline `onclick` handlers with
    HTML escaping only. The browser decodes `&#39;` back to a quote before
    running the handler, so a name like `x');alert(1);//` ran script in
    another staff member's session.
  - **No inline handlers now:** all 43 are replaced by `data-act` with JSON
    arguments, dispatched from an allow-list.
  - **Quote-safe escaping:** `esc()` escapes both quote characters in the
    staff web and the Registry console.
  - **Headers:** the staff web sends a CSP with no inline script, plus
    `nosniff`, `no-referrer`, no framing and charsets (part of #160). The
    console CSP adds `object-src` and `base-uri`.
  - **Tests:** `TestStaffWebNoInlineHandlers` and `TestStaffWebHeaders`.
  - **Also:** My devices did nothing when opened from another screen; fixed.
- Go toolchain pinned to 1.26.11 in `go.mod` and the Docker image (#43).
  `govulncheck` found 40 standard-library vulnerabilities reachable under
  1.26.0, and 2 in `golang.org/x/crypto` v0.55.0 (now v0.59.0).
- Removed chi's `RealIP` middleware (#43). It rewrote the client address
  from client-supplied headers; nothing used it.
- The Back Office audit chain verifies (#28). `audit.Verify` reported a
  break at any event with a multi-key detail.
  - **Cause:** `Record` hashed the caller's JSON bytes, but jsonb stores them
    rewritten.
  - **Fix:** events are now hash version 2: every field is length-prefixed
    (so bytes can't shift between fields) and the detail is hashed as
    `jsonb::text`, as the Registry already does.
  - **Locking:** a per-tenant advisory lock now covers a tenant's first
    event too, so concurrent writers can't fork a new chain.
  - **Older events:** they stay version 1. `Verify` checks their links,
    reports how many it could check only that way, and refuses a version-1
    event after a version-2 one. It now returns a result: events checked,
    first broken event, link-only count.
  - Migration `V21` adds `hash_version`.
- A failed session refresh no longer deletes a patient's pairing (#136).
  Refresh tokens are single-use, so a lost reply (a Relay blip or a
  timeout) left the phone holding a retired token, and the next launch's
  401 made the iPhone app remove the practice and its device key. Now a
  device-bound token whose replacement was never used may be presented
  again; the Back Office retires the unused replacement and issues a new
  pair (migration V18 adds `refresh_tokens.replaced_by`). Logout, device
  revocation and staff deactivation still end it. Refresh errors carry a
  `code` (`invalid_token`, `not_enrolled`, `device_revoked`, `proof_failed`,
  `proof_timestamp`); on `proof_timestamp` the app keeps the account and
  asks the patient to fix the phone's date and time.
- Patients can't send memos or reminders (#137). Those are staff kinds, and
  a patient's memo got past the practice's message switch and the
  provider's message mode. Patients get 403 for them now.
- Appliance boot after a reboot (#123). Lima's `vz.pid` can survive a
  reboot and name a process ID macOS has reused, and Colima then won't
  start. If Docker isn't up a minute after login, the boot job clears that
  stale state and restarts Colima. Seen on Barnacle after a macOS update;
  the fix was checked on Barnacle against the same state.
- Website publish (#97): when the Relay refused a publish (for example
  "publishing too often") before the whole bundle was sent, the Back Office
  reported EOF instead of the reason. It now reads the reply after a failed
  write. This also fixes the flaky `TestPublishSiteThroughRelay`.
- CI: Dockerfile and workflow pinned to Go 1.25 while go.mod requires 1.26 —
  bumped both to 1.26.
- `audit.Record` genesis event: first event in a tenant chain now links to
  an empty bytea instead of violating the `NOT NULL` `prev_hash` column.

## [0.1.0] — 2026-09-14

### Added
- Initial monorepo scaffold: Go backend (cobra/viper/chi/pgx), SwiftUI iOS
  app generated via xcodegen, docker-compose deploy, CI, AGPL-3.0.
- Core schema (migration 00001): tenants, users, provider_patients,
  appointments, messages, double-entry ledger, hash-chained audit_events —
  ledger and audit tables immutable via DB triggers.
