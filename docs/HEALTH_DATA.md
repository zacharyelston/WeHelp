# Patient health data from wearables

Status: **design for review** (2026-11-16), V2. Implementation issues are
filed from this document once it's approved. It pairs with the questionnaire
design ([QUESTIONNAIRES.md](QUESTIONNAIRES.md), #192). Monitoring plans
reuse its assignment idea, and measurements share its export path.

## Decisions (owner, 2026-11-16)

- **Patient side only.** A patient shares readings from their own devices
  (Apple Watch, connected scale, BP cuff, glucose monitor, ring) through the
  WeHelp app. **The office collects device data no other way:** no office
  devices, no kiosk measurements, nothing on the provider side of the app,
  and no vendor clouds.
- **A provider can also be a patient.** Their phone may hold a provider
  account at one practice and a patient account at another, or a patient
  account with another provider at their own practice (#89). Health data
  only ever travels through a **patient** account.

The lit review is in [LIT_REVIEW.md](LIT_REVIEW.md), under "Patient health
data from wearables".

The concrete service, privacy, consent, day aggregation and retry contracts
in [DATA_MODULE.md](DATA_MODULE.md) and [V2_CONTRACTS.md](V2_CONTRACTS.md)
are part of this specification. Clinical sources live in module Git; SQL is
a verified projection. External routes remain on the Back Office gateway.

## Invariants

- **HealthKit is the only source.** The app never talks to a device over
  Bluetooth or to a vendor cloud.
- **Data reaches the office only through the patient's app**, over the
  existing pinned connection through the Relay. Nothing goes in a push, a
  log, the Relay or the Registry.
- **Nothing is stored on the phone** except account-scoped opaque HealthKit
  anchors and resumable sync cursors, which hold no values. Readings stay in memory only while they're being sent or
  shown.
- **Minimum necessary.** A practice gets only the types it asked for in a
  monitoring plan **and** that the patient turned on for that practice.
- **Every write is audited** (`audit.Record`), and staff reads are audited
  (`auditRead`). Measurements are append-only.

## Accounts, roles and HealthKit permission

iOS gives HealthKit permission to the **WeHelp app**, not to one account in
it. So the app enforces sharing per account:

| Account on the phone | HealthKit sync |
|---|---|
| Provider, front desk, admin | **Never.** It doesn't read HealthKit or show sharing settings, whatever iOS allows |
| Patient at practice A | Only the types A's active monitoring plan asks for **and** the patient turned on for A |
| Patient at practice B | Only B's plan types the patient turned on for B. Independent of A |

- **Two gates per type.** iOS's permission is necessary but never enough.
  The app also needs the plan and the patient's in-app switch for that
  practice.
- **Each patient account syncs separately**, with its own anchors. Turning
  sharing off for one practice changes nothing for another, and nothing is
  sent for the period it was off.
- **A provider who is a patient at their own practice** has a separate
  patient account there. Their data reaches only the providers they're
  linked to as a patient. They will usually want to be a **private
  patient** (below), so the front desk and admins at their own practice see
  none of it.

## Private patients

Decided 2026-11-16, filed as #226: a patient can be **private** at one
practice. It's an access fact in the patient's Git record (the `record`
repository; see PATIENT_REPOSITORIES.md, #220), and it narrows the clinical
access scopes to linked providers, in addition to source permissions. The
module enforces it; the gateway uses its durably fenced access mirror. It isn't
encryption; see "Later: per-patient keys".

- **Who sees clinical content as staff:** only providers with an active link
  and existing source permissions. Patient self-service and one-response kiosk
  capabilities remain explicitly scoped under V2_CONTRACTS.md. That covers readings, monitoring plans, questionnaire
  responses and message bodies.
- **The front desk and admins still see** the patient's name, contact
  details and appointments, so they can schedule. They can't open their
  clinical content, and searches and lists don't show it.
- **No silent links.** For a private patient, admin booking (#185) doesn't
  create a link. A new provider link needs the patient's approval in the
  app, as for care teams (#111). Without it, booking with an unlinked
  provider is refused.
- **Who sets it:** the patient, in the app (Settings → Privacy, per
  practice), or a linked provider at the patient's request. Only the
  patient can turn it off. Both changes are audited (`patient.private`).
- **Admins can't read around it in the product.** The audit log still
  records every read, and admins can see that a private patient exists.
  There's no "break glass" in this release.
- **One flag for every feature.** Health data, questionnaires and messages
  all check it. Therapy thread membership remains an additional restriction;
  a patient-level flag never widens a private thread or another provider's notebook.

**What a flag doesn't stop:** someone with root on the Mac mini, or a
database restore, can still read the rows. FileVault, backup encryption and
the audit log cover that today, as for every other patient.

### Later: per-patient keys

Per-patient encryption is deferred. The V2 gateway is a trusted plaintext and
identity endpoint; host root and DB owners remain trusted operators. Merely
wrapping repository keys to provider devices does not protect server-side
scoring/analysis or forwarded plaintext from root/gateway compromise. Stronger
client-bound encryption requires a separate recovery, browser-key, analysis
and threat-model design. Do not claim that the private flag provides it.

## Concepts

```
Measurement type (catalogue: code, unit, kind)
Monitoring plan (practice asks a patient for types, for a period)
  └─ Sharing choice (the patient turns each type on or off, for this practice)
Measurement (one reading or one daily summary, append-only)
```

**Measurement types, first set.** LOINC codes are checked at
implementation.

| Type | HealthKit | Kind | Unit (UCUM) |
|---|---|---|---|
| Body weight | `bodyMass` | reading | kg |
| Blood pressure | `bloodPressure` correlation (systolic and diastolic) | reading | mm[Hg] |
| Blood glucose | `bloodGlucose` | reading | mg/dL |
| Oxygen saturation | `oxygenSaturation` | reading | % |
| Body temperature | `bodyTemperature` | reading | Cel |
| Resting heart rate | `restingHeartRate` | daily summary | /min |
| Steps | `stepCount` | daily summary (sum) | /d |
| Sleep | `sleepAnalysis` (asleep stages) | daily summary (minutes asleep) | min |

- **Readings** are sent one by one: one row per HealthKit sample.
- **Daily summaries** are one row per type per local day, computed on the
  phone with quantity statistics for steps/resting HR and category interval
  union for sleep (V2_CONTRACTS.md). Raw heart-rate or step streams are never sent.

**Monitoring plan.** A provider asks a linked patient for types, for a
period, for example "weight and BP daily for 30 days". It has a start, an
end (or open), and an optional note shown to the patient. It works like a
form assignment, without occurrences: there's nothing to fill in. Front desk
and admins can create plans for non-private patients under source permissions;
private patients require an authorized linked provider.

**Sharing choice.** For each type in an active plan, the patient answers
yes or no in the app. The answer is stored as module Git interval events and audited,
and the patient can change it at any time in Settings → Health data, per
practice. A plan with no "yes" sends nothing.

## Module source records and gateway API

**Logical records/projections** (tenant-scoped forward-only Flyway migrations):
Clinical plans/sharing/readings are module Git source; their SQL tables are
verified projections and have no direct source-write handler.

- `measurement_types`: a catalogue seeded by migration (key, LOINC code,
  unit, kind, HealthKit identifier). It isn't edited at runtime.
- `monitoring_plans`: tenant, patient, provider (requested_by), type keys,
  starts_at, ends_at, note, created_at, ended_at.
- `sharing_choices`: append-only. Patient, tenant, type key, `on` or `off`,
  decided_at, device_id. The current choice and effective permitted intervals derive from the events.
- `measurements`: append-only (trigger plus privilege revoke, like
  `audit_events`). It holds:
  - tenant, patient, type key, `kind`;
  - value (or systolic and diastolic), unit;
  - `effective_at`, or local `day`, IANA zone, UTC interval and algorithm version;
  - `source_id`: sample UUID, or patient/type/day/zone/plan/algorithm identity
    for summaries; server-assigned revision and expected-current-revision CAS;
  - `device` (HealthKit source name and model; no serial numbers);
  - `received_at`, device_id;
  - `supersedes_id` for a revised summary, and `withdrawn` for a sample the
    patient deleted in Health.
  - It's unique on (patient, source_id, revision).

**API** (device session for patients; staff routes follow the appointment
rules; everything goes in `api/openapi.yaml`):

| Route | Who |
|---|---|
| `POST /monitoring-plans`, `GET /monitoring-plans?patient_id=`, `POST /monitoring-plans/{id}/end` | staff |
| `GET /me/health-sharing` (active plans, types, my current choices) | patient |
| `PUT /me/health-sharing/{type}` (`on` or `off`) | patient |
| `POST /me/measurements` (a batch of at most 500) | patient. Only types in an active plan with sharing `on`; anything else is refused per item. exact payload duplicates are no-ops; conflicting reading identity or stale summary revision refuses. Per-item durable outcomes control anchor advance |
| `GET /measurements?patient_id=&type=&from=&to=` | staff, with `auditRead` |

- **Who sees measurements:** the patient's linked providers, and the front
  desk and admins of the practice. For a private patient, only the linked
  providers. Revoked links stop access, like messages.
- **Feature switch:** `health_data`, off by default. Hidden for practice
  types without clinical features.
- **Retention:** measurements are part of the record. They aren't deleted
  when sharing is turned off; sending simply stops.

## Patient app

- **Settings → Health data** shows only on patient accounts. Per practice it
  lists the plan's types, each with a switch. Turning one on asks iOS for
  HealthKit read access. Read denial cannot be detected. If there is no accessible data,
  show that state and permission instructions without claiming denial.
- **The sync service runs per patient account:**
  - **When:** on app open, on a pull in Health data, and with HealthKit
    background delivery (`HKObserverQuery`) for plan types. iOS only
    delivers while the phone is unlocked; that's expected.
  - **Readings:** an anchored query per type from the authorized plan/sharing interval or the
    last anchor. Deleted samples are sent as withdrawals.
  - **Summaries:** quantity statistics for steps/resting HR; sleep category
    interval union, with pinned practice-zone days. Recompute affected past
    days on changed/deleted samples and the current day; see V2_CONTRACTS.md.
  - **Sending:** batches of at most 500. The anchor advances only after the
    module reports durable outcomes for every chunk in the anchored page.
    Retain old anchor on any transient/uncertain result; retries deduplicate.
  - **Anchors** are stored in the Keychain, scoped to the account like its
    other items (#89), and removed with the account.
- **Nothing else is stored.** No readings on disk, no caches.
- **The patient sees what's shared:** "Last sent: today 08:14, 3 readings"
  per practice.
- **Provider, front desk and admin accounts** never create the sync
  service.
- The usage strings in `ios/project.yml` say exactly this: data is shared
  with the practices you choose, per type. The unused write permission
  (`NSHealthUpdateUsageDescription`) is removed, because WeHelp only reads
  (overlaps #164).

## Staff web

- **On a patient:**
  - "Monitoring": create or end a plan, and see which types the patient
    turned on;
  - "Readings": the latest value per type, then a table per type over time,
    with the source device;
  - withdrawn and superseded rows hidden by default.
- **Trends and charts** use the bounded V2 notebook adapter #217; the basic
  Readings panel does not depend on notebooks.
- **No alerts on thresholds in this release.** An automatic alert creates a
  clinical duty to respond, so the owner sets that policy first.

## Not in this design

- Office or kiosk devices, and any Bluetooth.
- Vendor cloud APIs.
- Writing to HealthKit.
- Alerts on thresholds.
- Trend charts.
- FHIR export (#26).
- RPM billing reports.
- Android (Health Connect, with #135).

## Open questions

- **ECG and irregular-rhythm notifications** (Apple Watch). They're
  clinically interesting, but they're documents and events, not single
  values. Later.

## Order of work (one PR each)

Filed under epic #207 in V2: #203 (measurements), #204 (plans and
sharing), #205 (app sync) and #206 (Readings view). They stay
`spec-review` until this design is approved.

1. **Measurements in the module:** the type catalogue, `measurements`
   (append-only, idempotent on `source_id`), `POST /me/measurements` and
   `GET /measurements`. This replaces the typed check-ins of #21.
2. **Monitoring plans and sharing choices:** tables, routes, the staff web
   "Monitoring" panel, and the `health_data` switch.
3. **The patient app:** Settings → Health data, and the per-account sync
   service (anchors, summaries, background delivery). This reworks #22.
4. **The staff web "Readings" view.**

Steps 1–3 give the end to end: a provider asks for weight, the patient
turns it on, a scale reading reaches the staff web.
