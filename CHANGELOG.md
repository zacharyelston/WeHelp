# Changelog

All notable changes to WeHelp are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- Spike S2, telehealth (#33): the answer is yes. Two iOS simulators held 1:1
  WebRTC calls with relay-only ICE through pion/turn. Media was DTLS-SRTP end
  to end, and the DTLS fingerprints matched each peer's SDP. Two calls at
  once used about 6% of one core on the server. The write-up and
  measurements are in docs/spikes/S2.md. The throwaway code in `spikes/s2/`
  is a TURN and signaling server in Go and a SwiftUI app using
  `stasel/WebRTC`. The lit review verdicts for pion/turn and the iOS WebRTC
  framework are updated.

### Changed
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
