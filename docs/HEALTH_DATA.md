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

## Invariants

- **HealthKit is the only source.** The app never talks to a device over
  Bluetooth or to a vendor cloud.
- **Data reaches the office only through the patient's app**, over the
  existing pinned connection through the Relay. Nothing goes in a push, a
  log, the Relay or the Registry.
- **Nothing is stored on the phone** except HealthKit's sync anchors, which
  hold no values. Readings stay in memory only while they're being sent or
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
  linked to as a patient. Colleagues see nothing beyond what the existing
  link and care-team rules allow. Whether such a patient can be marked
  private to one provider is an open question below.

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
  phone with HealthKit statistics queries. Raw heart-rate or step streams
  are never sent.

**Monitoring plan.** A provider asks a linked patient for types, for a
period, for example "weight and BP daily for 30 days". It has a start, an
end (or open), and an optional note shown to the patient. It works like a
form assignment, without occurrences: there's nothing to fill in. The front
desk and admins can create plans for the practice, as with appointments.

**Sharing choice.** For each type in an active plan, the patient answers
yes or no in the app. The answer is stored in the Back Office and audited,
and the patient can change it at any time in Settings → Health data, per
practice. A plan with no "yes" sends nothing.

## Back Office

**Tables** (Flyway, forward-only):
- `measurement_types`: a catalogue seeded by migration (key, LOINC code,
  unit, kind, HealthKit identifier). It isn't edited at runtime.
- `monitoring_plans`: tenant, patient, provider (requested_by), type keys,
  starts_at, ends_at, note, created_at, ended_at.
- `sharing_choices`: append-only. Patient, tenant, type key, `on` or `off`,
  decided_at, device_id. The current choice is the latest row.
- `measurements`: append-only (trigger plus privilege revoke, like
  `audit_events`). It holds:
  - tenant, patient, type key, `kind`;
  - value (or systolic and diastolic), unit;
  - `effective_at`, or `day` and `tz_offset` for summaries;
  - `source_id`: the HealthKit sample UUID, or `type:day` for summaries;
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
| `POST /me/measurements` (a batch of at most 500) | patient. Only types in an active plan with sharing `on`; anything else is refused per item. Duplicate `source_id`s are accepted as no-ops |
| `GET /measurements?patient_id=&type=&from=&to=` | staff, with `auditRead` |

- **Who sees measurements:** the patient's linked providers, and the front
  desk and admins of the practice. Revoked links stop access, like
  messages.
- **Feature switch:** `health_data`, off by default. Hidden for practice
  types without clinical features.
- **Retention:** measurements are part of the record. They aren't deleted
  when sharing is turned off; sending simply stops.

## Patient app

- **Settings → Health data** shows only on patient accounts. Per practice it
  lists the plan's types, each with a switch. Turning one on asks iOS for
  that HealthKit read permission if it isn't already granted. If iOS
  permission is missing, it says so and links to the Health app's settings.
- **The sync service runs per patient account:**
  - **When:** on app open, on a pull in Health data, and with HealthKit
    background delivery (`HKObserverQuery`) for plan types. iOS only
    delivers while the phone is unlocked; that's expected.
  - **Readings:** an anchored query per type from the plan's start or the
    last anchor. Deleted samples are sent as withdrawals.
  - **Summaries:** a statistics query per type per local day, from the
    last sent day to today. Today's value is re-sent as it grows (a new
    revision).
  - **Sending:** batches of at most 500. The anchor advances only after the
    Back Office accepts the batch.
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
- **Trends and charts** come later, with questionnaire trends (one charting
  piece for both).
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

- **A provider who is a patient at their own practice:** should a patient
  be markable as **private to one provider**, so the front desk and admins
  don't see their readings? The same question applies to messages; see the
  therapy preset in PRACTICE_TYPES.md.
- **ECG and irregular-rhythm notifications** (Apple Watch). They're
  clinically interesting, but they're documents and events, not single
  values. Later.

## Order of work (one PR each)

Filed under epic #207 in V2: #203 (measurements), #204 (plans and
sharing), #205 (app sync) and #206 (Readings view). They stay
`spec-review` until this design is approved.

1. **Measurements in the Back Office:** the type catalogue, `measurements`
   (append-only, idempotent on `source_id`), `POST /me/measurements` and
   `GET /measurements`. This replaces the typed check-ins of #21.
2. **Monitoring plans and sharing choices:** tables, routes, the staff web
   "Monitoring" panel, and the `health_data` switch.
3. **The patient app:** Settings → Health data, and the per-account sync
   service (anchors, summaries, background delivery). This reworks #22.
4. **The staff web "Readings" view.**

Steps 1–3 give the end to end: a provider asks for weight, the patient
turns it on, a scale reading reaches the staff web.
