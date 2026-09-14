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
4. **Multi-tenancy from day one.** Every table carries `tenant_id`. On-prem
   deployments run one tenant; cloud runs many. There is no tenant-less data.
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
(db). Auth (next step): email + password → short-lived access token
(refresh token in iOS Keychain); every request resolves `(tenant, user, role)`
before touching data.

## iOS app

Xcode project is **generated** (`ios/project.yml` → `xcodegen generate`);
don't commit `.xcodeproj`. `AppState` (@Observable) holds session state;
`APIClient` is the single network seam. HealthKit entitlement is declared in
`ios/WeHelp/WeHelp.entitlements`.

## Later, deliberately deferred

- **FHIR** mapping layer (`Patient`, `Observation`, `Appointment` map cleanly
  onto `users`, `messages.metadata`, `appointments`).
- E2E encryption for message bodies.
- APNs push for reminders (server → device token table).
- Object storage for attachments/imaging.
