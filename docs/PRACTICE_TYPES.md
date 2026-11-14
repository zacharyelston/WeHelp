# Practice types, message switches and care teams (proposal)

Status: proposal, post-V1. Nothing here is built. Prior art:
[LIT_REVIEW.md](LIT_REVIEW.md) → Practice types, message switches and care
teams.

WeHelp V1 serves a naturopath practice. The same product fits any small
practice that wants more contact with its clients between visits, such as
therapists and psychologists, physiotherapists and chiropractors, and even
hair salons. This document proposes three changes that make that possible
without forking the product:

1. **Practice types**: a preset per kind of practice that sets the words
   and the default features.
2. **Switches**: the practice turns features off, and each provider can
   stop taking messages, so no one is forced to answer a noisy inbox.
3. **Care teams**: a patient chooses a group of providers who all see their
   messages, for continuity of care. A shared practice inbox comes with it.

The invariants in [ARCHITECTURE.md](ARCHITECTURE.md) don't change. Patient
data stays in the Back Office. The Relay and Registry never see it. Every
change is audited.

## 1. Practice types

A practice type is a preset, chosen at install and changeable by an admin.
It sets the words the apps and website use, which features start on, and
whether the practice is clinical.

| Type | Words (person · staff) | Starts on | Starts off | Clinical |
|---|---|---|---|---|
| Naturopath (V1) | patient · provider | everything | none | yes |
| Therapy and psychology | client · therapist | requests, reminders, video, website | store, check-ins, health monitors; care team sharing off by default | yes, strict |
| Allied health (physio, chiro, dietitian) | patient · practitioner | everything | none | yes |
| Salon and personal services | client · stylist | requests, reminders, messages, store, website | check-ins, health monitors, video, library, care team | no |

- **Words** are a fixed table per type: patient or client, and provider,
  practitioner, therapist or stylist. The API and the database always say
  patient and provider. The staff web, the app and the website show the
  practice's words. There is no free-text renaming: fixed words keep
  translations and support simple.
- **Clinical: no** hides clinical features (check-ins, health monitors,
  visit notes later). Security stays the same: device keys, TOTP, audit and
  a Back Office in the shop. A salon's client list and messages are still
  personal data, and one code path is safer than a weaker one.
- **Therapy is strict.** Each thread stays private to one therapist unless
  the client turns on care-team sharing. A "not for emergencies" notice and
  the practice's crisis line appear in the app. Messages are not
  psychotherapy notes, and the product says so.
- The Registry records the type for each practice, for pricing and support.
  It never records patient data. The Back Office holds the type itself and
  serves it to the apps with the rest of the practice settings.

## 2. Switches

### Practice switches (admin)

An admin turns whole features on or off in a new **Settings** screen:
messages, video visits, appointment requests, reminders, check-ins, the
store, the library and the website. A switched-off feature disappears from
the app and the staff web. The server also refuses it (409, with a plain
message), so an old app version can't use it.

### Provider message switch (each provider)

Each provider sets their own messaging under My profile:

- **On**: as today.
- **Office hours**: messages arrive at any time. Outside the provider's
  hours the patient sees an automatic reply ("Dr. Shaw replies on
  weekdays, 9 to 5"), and the provider gets no notifications.
- **Off**: patients can't start a new message to that provider. Existing
  threads stay readable. The app points patients to the practice inbox
  instead (below).

This answers "don't force providers to listen to noisy patients" without
cutting the patient off from the practice.

## 3. Care teams and the practice inbox

### Today

A message is one-to-one between a provider and a patient with an active
link. Nobody else can read it. The front desk (`office`, #106) reads no
threads.

### Care team

A **care team** is a group that belongs to one patient: the providers the
patient has allowed to see their messages.

- **Joining.** A provider proposes a colleague, or the patient adds one in
  the app. The colleague joins only when the patient approves in the app,
  the same way links work today (pending, then active). HIPAA would allow
  treatment sharing without asking. We ask anyway: patients trust it more,
  and therapy practices need it.
- **What members see.** One shared thread: the patient and every member,
  each message showing its author. The patient chooses whether a new
  member sees past messages or only new ones. One-to-one threads stay
  private, so a patient can have both.
- **Leaving.** The patient removes a member at any time. From then on that
  member sees nothing of the team thread, past or future.
- **Who is never a member:** the front desk and admins, unless they are
  also providers. Care teams are clinical.
- **Audit.** Joining, approval, removal and every time a member opens the
  thread are audited, so "who read my messages" has an answer. A patient
  can see the member list and when each joined.
- **Therapy preset:** off unless the client turns it on.

This also covers "staff reading other staff's messages to follow
treatment": they read the patient's team thread, with the patient's
consent, never someone's private thread.

### Practice inbox

Patients can also message **the practice**: a thread the front desk and
admins answer. It covers bookings, orders, directions and anything not
clinical. A provider whose messages are off sends patients here. In a
salon, it's the main way clients talk to the shop.

## Data model (sketch)

| Table | Purpose |
|---|---|
| `practice_settings` | One row per tenant: `practice_type`, feature switches, crisis line text |
| `provider_message_settings` | Per provider: `mode` (on, hours, off), `hours` (JSON), `auto_reply` |
| `care_team_members` | `patient_id`, `member_id`, `status` (pending, active, removed), `include_history`, `invited_by`, `approved_at`, `removed_at`. It maps onto FHIR `CareTeam` |
| `messages.thread` | New column: `direct` (today's), `care_team` or `practice`. Team and practice messages have no single recipient: `recipient_id` is null and `thread` plus `patient_id` say who can read them |

Reads are checked in the server, in one function that answers "may this
user read this patient's thread X". Every listing query goes through it,
and DB tests cover each role and thread type.

## Phasing

The iPhone and iPad are away for now, so server and staff web work comes
first. The app follows when the devices are back. Each phase is one issue
and one PR.

1. **Practice type and words** (#108; server, staff web, website): settings
   table, presets, staff web and site words, Registry field. The app picks
   up the words later.
2. **Switches** (#109; server, staff web): practice switches with server
   enforcement, and the provider message switch with office hours and an
   automatic reply.
3. **Practice inbox** (#110; server, staff web): the thread type, the front desk
   answers it.
4. **Care team** (#111; server, staff web, then app): membership with patient
   approval, the shared thread, read audit and DB tests for every role.
   Patient approval needs the app, so the server and staff web land first
   behind a switch, and approval waits for the devices.

## Open questions

- Should a practice be able to mix types (a clinic with a therapist and a
  dietitian)? Proposal: one type per practice, with words per provider
  later if asked.
- Office hours: per provider only, or practice hours as the default?
- When a provider leaves the practice (disabled), do they leave every care
  team automatically? Proposal: yes, removed and audited.
- Are salons sold the same appliance (a Mac mini in the shop), or wait for
  the cloud-hosted Back Office (post-V1)?
- Legal review before selling to therapy practices in each region (HIPAA,
  state mental-health rules, 42 CFR Part 2 for SUD programs).
