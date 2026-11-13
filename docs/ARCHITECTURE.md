# Architecture

## Goals

- Mobile-first (iOS, SwiftUI) provider↔patient communication: messages, memos,
  check-ins (diet, weigh-ins), appointment scheduling and reminders.
- Sync from Bluetooth health monitors — prefer **HealthKit** on iOS; drop to
  CoreBluetooth only for devices Apple doesn't cover.
- Two deployment shapes from one artifact: **on-prem** (single container +
  Postgres) and **cloud-hosted** (multi-tenant).
- Patient-facing connectivity (phones, web, telehealth, push) goes through a
  separate **Relay** that holds no patient data. See "Back Office and Relay".
- An internal credit system for resource accounting that survives an audit.
- Document-standard-agnostic now, FHIR-mappable later.

## Scale budget & deployment shape

The reference deployment is an **appliance PC in a provider's office** —
a Mac mini or small x86 box on gigabit fiber or a 5G business line,
serving one practice:

- **≤ ~500 active patients**, tens of staff. Peak load is tens of
  requests/second, not thousands. Do not design for hospital-network scale.
- One Go binary + one Postgres instance. No service mesh, no shards, no
  stream processors — a job queue is a Postgres table (River), never Kafka.
- Must idle quietly for years between OS updates. Recovery = restore the
  DB backup and start the binary.
- The cloud-hosted tier runs the *same artifact* with many tenants; scale
  is a cloud ops problem, never a code-architecture problem.

Design consequence: prefer boring, inspectable code over clever scale
machinery. A feature needing infrastructure beyond Postgres + object
storage requires explicit justification in its issue. We are not competing
with Salesforce; we're a tight, correct tool providers can run and audit
themselves.

## Non-negotiables

1. **PHI handling.** Every access to patient data must be attributable. All
   writes that touch PHI go through `audit.Record` inside the same DB
   transaction as the write itself. Encryption in transit (TLS) and at rest
   (disk / Postgres TDE or volume encryption) is a deployment requirement.
2. **Money-like data is append-only.** `ledger_transactions` +
   `ledger_entries` are double-entry and immutable (DB trigger). Balances are
   always computed — never stored. See `internal/ledger`.
3. **Audit is tamper-evident.** `audit_events` rows are hash-chained per
   tenant (`prev_hash` → `hash`, SHA-256) and immutable by trigger.
   `audit.Verify` re-checks a chain. See `internal/audit`.
4. **Multi-tenancy from day one — but cheap.** Every table carries
   `tenant_id`. On-prem deployments run one tenant; the hosted tier runs
   many. This is a cheap isolation column, not a scale play — no tenant
   routing infrastructure.
5. **No cloud-locked dependencies.** Postgres + S3-compatible object storage
   only, so the same artifact runs in a clinic closet or in our cloud.

## Back Office and Relay: two systems, one boundary

WeHelp is two separately deployed systems. The rule between them: **the part
that connects phones and the web does not hold patient data.**

```
 website visitors ──HTTPS──► Relay: static practice site (no PHI, no logins)

 patient app (iPhone) ─TLS─► Relay (cloud, multi-office, paid tier)
                               │  routes by hostname, never decrypts
                               ▼
                        outbound tunnel opened by the office
                               │
 office staff (LAN) ───────► Back Office (Mac mini in the office: wehelpd + Postgres)
                             all PHI, scheduling, audit, ledger
```

### Patient access is mobile-only (for now)

The mobile app is the only place a patient sees their data: appointments,
office visit notes, messages, telehealth. A phone is a personal device that
can hold a key we enroll on it, which a shared browser cannot. There is **no
patient web portal**. The web is an attack surface we are choosing to avoid
for now, and reversing that choice needs its own design.

- **Device enrollment.** At registration the app generates a key pair in the
  Secure Enclave (the private key never leaves the phone). It registers the
  public key with the Back Office, through the Relay tunnel. Sessions are then
  bound to that device key, not only to a password. This builds on the
  Keychain token work in #5. App Attest can additionally prove that the
  request comes from the genuine WeHelp app.
- **Telehealth** runs in the app. End-to-end media keys live on the
  participants' devices.
- **Staff web UI** (the embedded SPA) is for office staff on the LAN only.
  Patient-role accounts get no web UI.

### Back Office (`wehelpd`, this repo today)

- Runs in the office and is managed by the office through its web UI on the
  office network. It holds all PHI: patients, messages, appointments,
  check-ins, audit chain, ledger.
- It **accepts no inbound connections from the internet.** It reaches the
  Relay by opening an outbound tunnel, so the office router needs no port
  forwarding.
- It terminates TLS for patient traffic itself. The certificate for the
  office's public hostname lives on the Back Office (ACME TLS-ALPN-01 works
  through an SNI-passthrough relay), never on the Relay.
- Patient login, tokens, and every API call are handled here, as today.
- The staff web UI and staff/admin API routes are served on the LAN only by
  default. Exposing them through the Relay is an explicit per-office setting.
  Through the tunnel, only the patient-app API is reachable.

### Relay (planned; separate binary and deployment)

The Relay is the paid "Web & Tele Hosting" service. Each office registers with
a **subscription key**, which entitles it to:

1. **Traffic routing.** The patient app connects to the office's public
   hostname. The Relay reads only the TLS SNI, looks up the office, and
   forwards the still-encrypted bytes down that office's tunnel. It cannot
   read requests, responses, credentials or tokens.
2. **Website hosting.** Each office gets a static practice website: about,
   providers, hours, location and contact details. It has links to download
   the app and deep links into it ("Register", "My appointments", "Visit
   notes"), which open the app or the App Store. The site has no logins, no
   forms and no patient data.
   - **How it is made:** the office fills in a "Practice profile" page in the
     Back Office staff UI. The Back Office renders the site from a few built-in
     templates (Go `html/template`) and publishes the static files to the
     Relay. Offices never write HTML or run a site builder.
   - **How it is served:** the Relay serves each office's files by hostname,
     like any shared static host. No per-office server-side code runs.
3. **Telehealth routing.** The Relay runs signaling and media forwarding for
   video visits. Who meets whom and when stays in the Back Office, which mints
   short-lived room tokens. Media is end-to-end encrypted between participants
   with keys exchanged through the Back Office, so the media server forwards
   ciphertext. Nothing is recorded on the Relay.
4. **Push.** APNs keys belong to the app publisher, so pushes are sent from the
   Relay. The Back Office sends an opaque request (device token plus a generic
   category); payloads never contain PHI ("You have a new message", not its
   text).

What the Relay is allowed to store: office records (ID, subscription key hash,
hostnames, plan, tunnel status, billing), published website bundles, and push
device tokens mapped to an office. Nothing else. Connection logs are kept
minimal and short-lived, because knowing that a device talks to a given clinic
is itself sensitive.

### Enforcing the boundary

- The Relay is its own binary (`cmd/wehelp-relay`) with its own small
  database. It must not import `internal/store`, `internal/audit`,
  `internal/ledger`, `internal/seed` or the PHI handlers in `internal/server`.
  A test fails the build if it does.
- The Relay never holds TLS private keys for office hostnames. Traffic to
  them is passthrough-only.
- Relay APIs for the Back Office (publish site, mint room, send push) take
  opaque identifiers, never patient names, contact details or message
  content.
- On the LAN, phones may talk to the Back Office directly. The iOS app's
  configurable server URL already supports both cases.

This section is the issue-level justification for the Relay as infrastructure
beyond Postgres and object storage (see "Scale budget"). Prior art is in
docs/LIT_REVIEW.md, under "Relay".

## Data model (migration 00001)

- `tenants`, `users` (role: provider | patient | admin)
- `provider_patients` — relationship + consent status
- `appointments` — scheduling; reminders ride on `messages` of kind `reminder`
- `messages` — one transport for `message | memo | checkin | reminder`;
  `metadata` jsonb holds kind-specific payloads (weigh-in value, diet log)
- `ledger_accounts`, `ledger_transactions`, `ledger_entries`
- `audit_events`

## API shape

`/api/v1/...` behind chi. Health endpoints: `/healthz` (process), `/readyz`
(db). Auth is live: email + password → short-lived access token + rotating
refresh token (#17). Local credentials are **interim** — the target is OIDC
SSO (Google, Microsoft, Sign in with Apple; #18), keeping local login as an
on-prem fallback. Every request resolves `(tenant, user, role)` before
touching data.

## iOS app

Xcode project is **generated** (`ios/project.yml` → `xcodegen generate`);
don't commit `.xcodeproj`. `AppState` (@Observable) holds session state;
`APIClient` is the single network seam. HealthKit entitlement is declared in
`ios/WeHelp/WeHelp.entitlements`.

## Later, deliberately deferred

- **SSO** via OIDC — Google, Microsoft, Sign in with Apple (#18). Apple is
  mandatory once any third-party login ships (App Store rule).
- **FHIR** mapping layer (`Patient`, `Observation`, `Appointment` map cleanly
  onto `users`, `messages.metadata`, `appointments`).
- E2E encryption for message bodies.
- APNs push for reminders (server → device token table).
- Object storage for attachments/imaging.
- **In-office iPad kiosk for patient data entry** (needs spec). The office
  will run an iPad in kiosk mode so patients can enter their own data
  (intake forms, check-in, history) at the practice. It differs from the
  patient's phone: it is a shared, office-owned device on the office LAN
  that talks to the Back Office directly, never through the Relay. Before
  any code, the spec must settle:
  - **Device trust:** enroll the iPad as an office device (its own device
    key and role) rather than as any patient's device.
  - **Patient identity:** how a patient is matched or verified at the
    kiosk without a personal login (staff hand-off, code, date of birth).
  - **Session isolation:** wipe all entered data from the iPad between
    patients, with an idle timeout.
  - **Lockdown:** Guided Access or Single App Mode, and whether MDM is
    needed.
  - **Staff review:** whether entries go straight into the record or into
    a staff review queue.
  - **Audit:** how audit attribution works for patient-entered data on a
    shared device.
