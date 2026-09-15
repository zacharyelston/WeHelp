# Architecture

## Goals

- Mobile-first (iOS, SwiftUI) provider↔patient communication: messages, memos,
  check-ins (diet, weigh-ins), appointment scheduling and reminders.
- Sync from Bluetooth health monitors — prefer **HealthKit** on iOS; drop to
  CoreBluetooth only for devices Apple doesn't cover.
- Two deployment shapes from one artifact: **on-prem** (single container +
  Postgres) and **cloud-hosted** (multi-tenant).
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
