# WeHelp V1 plan

How we get from the proof of concept (PoC) to a V1 that passes a security
audit and pen test and runs in a naturopath practice. The target is
[ARCHITECTURE.md](ARCHITECTURE.md), and the security bar is
[SECURITY.md](SECURITY.md).

## Where the PoC stands

The PoC proved the pieces on real hardware: the Go Back Office, Flyway,
Secure Enclave device-bound sessions on a physical iPhone, and Barnacle as an
always-on Back Office. But it is spread across eight open PRs in two repos,
plus a hand-merged branch on Barnacle. CI runs on every PR, but `main` is
not protected, so nothing requires it to pass before a merge.

| From the PoC | V1 decision |
|---|---|
| Go server, auth tokens, audit and ledger invariants, Flyway, Compose | **Keep** |
| Device-bound sessions: Secure Enclave key, signed refresh, Keychain (fork PR #3) | **Keep**; becomes the core of patient identity |
| Messages API (alzacai/WeHelp#25), appointments API (alzacai/WeHelp#28), OpenAPI spec (alzacai/WeHelp#27) | **Keep**: review, land, then extend |
| Web client (alzacai/WeHelp#29) | **Keep as the staff UI**; patient login removed (#9) |
| Patient password login, server-URL field, saved-servers screens | **Replace** with QR pairing. Keep only as a Debug-build developer tool |
| Phones talking straight to the Back Office over plain HTTP on the LAN (`NSAllowsLocalNetworking`) | **Replace**: every phone goes through the Relay with pinned TLS; the ATS exception is removed |
| Relay design in fork PR #2 (SNI passthrough, frp or yamux, public certificates) | **Replace** with SSH + `CONNECT` + pinned office key |
| `barnacle-deploy` hand-merged branch | **Retire** once `main` has everything; Barnacle runs `main` |

## Milestones

Each milestone ends with its exit check passing on the Demo topology (dev
laptop as Relay, Barnacle as Back Office).

### V1-M0: Consolidate (one main, CI required)

Issues: #30 CI, #31 consolidation, #12 appliance deploy, #13 AGENTS.md, #14 messages, #15 OpenAPI, #16 sign-in, #18 appointments.

Land the PoC into `main` properly so there is one codebase:
- Protect `main` so CI (Go with the race detector, iOS, Docker) must pass
  before a merge.
- Review and merge, in order:
  1. #1 (device dev);
  2. messages;
  3. appointments;
  4. OpenAPI;
  5. the web client as the staff UI;
  6. #3 (device-bound sessions).

  Rebase each and resolve the conflicts once, properly.
- Bring `AGENTS.md` up to date (#13). Move the appliance Compose setup into
  the repo (#12).
- Barnacle runs `main`; delete `barnacle-deploy`.

**Exit:** `main` builds green in CI, Barnacle runs a tagged `main` build, and
no open PR predates V1-M0.

### V1-M1: Spikes (prove the risky parts, then throw the code away)

Issues: #32 S1, #33 S2, #34 S3.

Time-boxed, a few days each. Each spike ends in a written yes/no and a
lit-review entry.
- **S1 Secure channel:** iOS `ProxyConfiguration` `CONNECT` through a Go
  relay, spliced onto an `x/crypto/ssh` channel, with TLS 1.3 to a
  self-signed Back Office key pinned in a `URLSession` delegate. Confirm ATS
  allows it with no exception.
- **S2 Telehealth:** two iPhones on the same Wi-Fi in a 1:1 WebRTC call with
  relay-only ICE through `pion/turn` on the laptop. Measure quality with two
  calls at once.
- **S3 Push:** an APNs push from the Relay using the team's key, received on
  the device.

**Exit:** all three spikes are yes, or the architecture is updated before
V1-M2 starts.

### V1-M2: Secure channel and identity

Issues: #60 Registry core, #35 office identity and install, #4 Relay, #36 iOS through the Relay, #37 QR pairing, #10 devices, #38 staff sign-in.

- **Registry core:**
  - `wehelp-registry` with Postgres: practices, install tokens, signed
    office lists;
  - a minimal console flow (register practice, issue install token,
    suspend);
  - an import-boundary test.
- **Back Office identity and install:** `wehelpd init` generates the office
  SSH key, office TLS key and local CA, then enrolls with the Registry using
  an install token. Key-rotation procedure.
- **Relay binary:**
  - SSH listener, `CONNECT` listener and splice;
  - pulls and verifies the Registry's signed office list and caches it in
    SQLite;
  - the import-boundary test (#4).
- **App connectivity:** the app's network layer goes through the Relay with
  pinning. Remove `NSAllowsLocalNetworking`.
- **QR pairing and invite enrollment:** staff UI "Pair phone", invite codes,
  and the app scanner. Patient password login is removed. The app unlocks
  with Face ID or the device passcode.
- **Devices:** list and revoke devices, staff and self (#10).
- **Staff sign-in:** password + TOTP, with the staff UI served over HTTPS on
  the LAN with the local CA. Staff routes are refused on Relay channels.

**Exit:**
- Barnacle is registered through the Registry with an install token.
- A fresh iPhone pairs by QR with Barnacle through the laptop Relay, and a
  capture on the laptop shows only ciphertext.
- Revoking the device locks it out within one access-token lifetime.
- Suspending the practice in the Registry disconnects Barnacle from the
  Relay within a minute.

### V1-M3: Practice features

Issues: #14 and #18 extended, #17 iOS messages, #39 iOS appointments, #9 staff-only web, #19 reminders, #7 push, #61 Registry console, #62 provider roster.

- Messages (#14) and appointments (#18) on the secure channel.
- iOS messages inbox and compose (#17), and iOS appointments (a new issue).
- Staff UI: schedule, threads, patients, pairing, devices. No patient web UI
  (#9).
- Reminders as River jobs (#19). Push via the Relay with fixed categories
  (#7, replacing #20).
- **Registry console** (#61): practices, providers, admin
  contacts, Relay assignment, and Relay and office health. **Provider
  roster** (#62): the Back Office pulls its providers from the
  Registry and invites their staff accounts.
- The OpenAPI spec covers every V1 route and is validated in CI (#15).

**Exit:** the demo script (below) runs end to end without a developer
touching the database.

### V1-M4: Telehealth

Issue: #6.

- Visit lifecycle tied to an appointment.
- Signaling through the Back Office, and TURN credentials minted per visit.
- iOS call UI for patients and providers (#6, rewritten for 1:1 WebRTC with
  TURN).
- Audit events for start, join and end. No recording.

**Exit:** two simultaneous 1:1 visits on the Demo topology for 30 minutes,
with acceptable quality.

### V1-M5: Practice website

Issue: #5.

- Practice profile in the staff UI, `html/template` rendering, publish over
  SSH (#5).
- The Relay serves the site with `apple-app-site-association`.

**Exit:** the demo practice's site loads from the Relay, and its app links
open the app.

### V1-M6: Hardening and audit readiness

Issues: #28 audit chain bug, #24 audit verification, #40 audit reads, #41 backups, #42 logging, #43 CI security, #44 checklists, #11 App Attest.

- Fix the audit chain (#28). Run `audit.Verify` nightly with an alert. Audit
  PHI reads, not only writes.
- Encrypted backups with a tested restore. FileVault and logging hygiene,
  with no PHI in logs.
- CI security tooling: `govulncheck`, `staticcheck`, and the race detector.
  Release-build hygiene, with no Debug features in Release.
- ASVS L2 and MASVS L2 checklists, HIPAA technical-safeguard mapping, and
  the App Attest decision (#11).
- Practice-owner documents (risk analysis, policies, BAAs) drafted with the
  owner.

**Exit:** every checklist row is met, not applicable, or accepted in
writing, and an internal pen test of the Demo topology finds no high or
critical issues.

### V1-M7: Demo, audit, field

Issues: #45 demo kit, #46 external audit and pen test, #47 field install.

- **Demo kit:**
  - synthetic demo practices with no real data;
  - a one-command laptop Relay;
  - Barnacle reset to a known state;
  - a written demo script for investors and practitioners.
- **External review:** the security audit and pen test, then fixes and a
  retest for every high or critical finding.
- **Field install** at the practice: the practice's own Mac mini, the
  production Relay, an install runbook, and a go-live check.

**Exit (V1 release):** the external audit and pen test pass, the practice is
live on its own hardware, and demos run only on demo hardware with demo data.

### V1-OBS: Observability (parallel track, from V1-M0 on)

Design: [OBSERVABILITY.md](OBSERVABILITY.md).

Issues: #53 Alloy sidecar, #54 OTel metrics, #55 dashboards and load guard,
#56 audit stream export, #57 Relay telemetry, #63 Registry telemetry,
#58 Cloud Dashboards PoC.

- **Alloy sidecar** on the Back Office; JSON logs and OTel metrics from
  `wehelpd`.
- **Dashboards profile:** Grafana, Loki and Prometheus with resource limits,
  plus the load guard.
- **Audit stream export:** `audit_events` to syslog or OTLP with chain hashes,
  resuming from the last acknowledged seq.
- **Relay telemetry** once the Relay exists (after V1-M2).
- **WeHelp Cloud Dashboards PoC:** per-office tenants, Barnacle and the
  laptop Relay shipping to one cloud stack.

**Exit:** the Demo topology shows live Back Office and Relay dashboards on a
separate machine; the audit stream reaches a remote syslog receiver and
verifies; the load guard has been shown to stop dashboards under load.

The audit stream export is needed for V1 (HIPAA audit controls), so it must
land before V1-M6 exits. The Cloud Dashboards offering is a PoC, not a V1
release requirement.

## Order

```
M0 Consolidate ─► M1 Spikes ─► M2 Secure channel ─┬─► M3 Practice features ─┐
                                                   ├─► M4 Telehealth ────────┼─► M6 Hardening ─► M7 Demo / audit / field
                                                   └─► M5 Website ───────────┘
```

M3, M4 and M5 can run in parallel once M2 lands. The audit-chain fix (#28)
can land any time and should not wait for M6.

## The demo script (V1-M3 onward)

1. Start the Registry and Relay on the laptop. In the Registry console,
   register the demo practice and its provider, and show the practice's
   Relay assignment. Barnacle (Back Office) connects to the Relay over SSH.
2. Staff signs in to the staff UI (password + TOTP) and adds a demo patient.
3. Staff presses **Pair phone**; the patient scans the QR and is enrolled.
4. Patient requests an appointment; staff confirms; the patient gets a push.
5. Messages are exchanged both ways.
6. At visit time, provider and patient join a video visit from their apps.
7. Show the audit log for everything above, and a Relay capture showing only
   ciphertext.

## After V1 (not planned here)

Patient access to their health records (visit notes, check-ins #21,
HealthKit #22), the in-office iPad kiosk (#8), a patient web portal, the
ledger API (#23), message-body encryption (#25), FHIR (#26), SSO (#27),
cloud-hosted Back Offices, Android, group visits and recording. These are
tracked in the **Post-V1** milestone.
