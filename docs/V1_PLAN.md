# WeHelp V1 plan

How we get from the proof of concept (PoC) to a V1 that passes a security
audit and pen test and runs in a naturopath practice. The target is
[ARCHITECTURE.md](ARCHITECTURE.md), and the security bar is
[SECURITY.md](SECURITY.md).

## Where the PoC stands

The PoC proved the pieces on real hardware: the Go Back Office, Flyway,
Secure Enclave device-bound sessions on a physical iPhone, and Barnacle as an
always-on Back Office. But it is spread across eight open PRs in two repos,
plus a hand-merged branch on Barnacle, with CI that has not run on any PR.

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

### V1-M0: Consolidate (one main, CI on)

Land the PoC into `main` properly so there is one codebase:
- Turn on CI for the fork, and make every PR run the full pipeline.
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

- **Back Office identity:** office SSH key, office TLS key, local CA, all
  generated at install, with a key-rotation procedure.
- **Relay binary:**
  - SSH registry, `CONNECT` listener and splice;
  - SQLite registry;
  - an `wehelp-relay office add` CLI;
  - the import-boundary test (#4, rewritten for SSH).
- **App connectivity:** the app's network layer goes through the Relay with
  pinning. Remove `NSAllowsLocalNetworking`.
- **QR pairing and invite enrollment:** staff UI "Pair phone", invite codes,
  and the app scanner. Patient password login is removed. The app unlocks
  with Face ID or the device passcode.
- **Devices:** list and revoke devices, staff and self (#10).
- **Staff sign-in:** password + TOTP, with the staff UI served over HTTPS on
  the LAN with the local CA. Staff routes are refused on Relay channels.

**Exit:** a fresh iPhone pairs by QR with Barnacle through the laptop Relay,
and a capture on the laptop shows only ciphertext. Revoking the device locks
it out within one access-token lifetime.

### V1-M3: Practice features

- Messages (#14) and appointments (#18) on the secure channel.
- iOS messages inbox and compose (#17), and iOS appointments (a new issue).
- Staff UI: schedule, threads, patients, pairing, devices. No patient web UI
  (#9).
- Reminders as River jobs (#19). Push via the Relay with fixed categories
  (#7, replacing #20).
- The OpenAPI spec covers every V1 route and is validated in CI (#15).

**Exit:** the demo script (below) runs end to end without a developer
touching the database.

### V1-M4: Telehealth

- Visit lifecycle tied to an appointment.
- Signaling through the Back Office, and TURN credentials minted per visit.
- iOS call UI for patients and providers (#6, rewritten for 1:1 WebRTC with
  TURN).
- Audit events for start, join and end. No recording.

**Exit:** two simultaneous 1:1 visits on the Demo topology for 30 minutes,
with acceptable quality.

### V1-M5: Practice website

- Practice profile in the staff UI, `html/template` rendering, publish over
  SSH (#5).
- The Relay serves the site with `apple-app-site-association`.

**Exit:** the demo practice's site loads from the Relay, and its app links
open the app.

### V1-M6: Hardening and audit readiness

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

## Order

```
M0 Consolidate ─► M1 Spikes ─► M2 Secure channel ─┬─► M3 Practice features ─┐
                                                   ├─► M4 Telehealth ────────┼─► M6 Hardening ─► M7 Demo / audit / field
                                                   └─► M5 Website ───────────┘
```

M3, M4 and M5 can run in parallel once M2 lands. The audit-chain fix (#28)
can land any time and should not wait for M6.

## The demo script (V1-M3 onward)

1. Start the Relay on the laptop. Barnacle (Back Office) connects over SSH.
2. Staff signs in to the staff UI (password + TOTP) and adds a demo patient.
3. Staff presses **Pair phone**; the patient scans the QR and is enrolled.
4. Patient requests an appointment; staff confirms; the patient gets a push.
5. Messages are exchanged both ways.
6. At visit time, provider and patient join a video visit from their apps.
7. Show the audit log for everything above, and a Relay capture showing only
   ciphertext.

## After V1 (not planned here)

Patient access to their health records (visit notes, check-ins, HealthKit),
the in-office iPad kiosk (#8), a patient web portal, the ledger API, FHIR,
SSO, cloud-hosted Back Offices, Android, group visits and recording.
