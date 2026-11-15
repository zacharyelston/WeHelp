# HIPAA Security Rule: technical safeguards

WeHelp against 45 CFR §164.312 (#44). R = required, A = addressable; an
addressable specification may be met by an equivalent measure, which the row
says. Reviewed 2026-11-15 against `main` at the merge of #158. The
administrative and physical safeguards (§164.308, §164.310) are the practice
owner's, with WeHelp's docs as input: see "Practice-owner documents" below.

**Totals:** 8 Met, 4 Gap.

## HIPAA technical safeguards (45 CFR §164.312)

R = required, A = addressable. An addressable specification can be met by an equivalent measure; the row says when it is.

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| 164.312(a)(1) | Limit ePHI access to authorized people and software | Gap | The technical controls are in place:<br>- roles and per-tenant scoping;<br>- staff routes refused on Relay channels (`lanOnly` in `internal/server/staff.go`, `TestStaffRoutesHiddenFromRelay`);<br>- device-bound patient sessions;<br>- disabled accounts refused on every bearer request.<br>The standard isn't fully met while (a)(2)(ii), which is required, is missing. |
| 164.312(a)(2)(i) | Every user has a unique identifier (R) | Met | Each person has a UUID `users` row, with `unique (tenant_id, email)` (`db/migrations/V1__init.sql`). Staff accounts are per person (`wehelpd staff add`, provider set-up links). Each device has its own ID. Every audit event names the actor and the device (`audit.Record`, `TestAuditPHIReads`). |
| 164.312(a)(2)(ii) | Documented way to reach ePHI in an emergency (R) | Gap | No break-glass procedure is written down. The pieces exist (an admin can add or re-enable staff on the host with `wehelpd staff add`; restore is in docs/BACKUPS.md), but nothing says who may use them in an emergency, how, or how the use is audited and reviewed. |
| 164.312(a)(2)(iii) | Sessions end automatically after inactivity (A) | Met | Staff web sessions have a 30-minute idle and 12-hour absolute limit (`sessionIdle`/`sessionMax` in `internal/server/staff.go`, enforced in `lookupSession`). On the phone, which counts as an equivalent measure: access tokens expire after 15 minutes, the app locks with Face ID or the passcode on every trip to the background (`AppLock`, `WeHelpApp.swift`), and iOS auto-lock sends the app to the background. |
| 164.312(a)(2)(iv) | ePHI encrypted at rest where reasonable (A) | Gap | Backups are encrypted with `age` before anything reaches a disk, with the private key held by the owner (`deploy/backup.sh`, docs/BACKUPS.md), and Postgres listens on localhost only. Two problems:<br>- FileVault on the Back Office is an install checklist item only (docs/onboarding/RUNBOOK.md, docs/APPLIANCE.md); `wehelpd` never checks it.<br>- The iOS app stores patient names in UserDefaults (see MASVS-STORAGE-1). |
| 164.312(b) | Record and examine activity in ePHI systems (R) | Met | Every PHI write calls `audit.Record` in the same transaction. Every PHI read route records one event per request, and fails closed if it can't (`TestAuditPHIReads`). Events are hash-chained, and `audit_events` is append-only by trigger (`forbid_mutation`, `V1__init.sql`). `audit.Verify` runs at start-up and daily, and on demand with `wehelpd audit verify` (`TestAuditChain`). The exclusions (signal long-poll, library handouts, your own profile) are justified in docs/SECURITY.md "Logging and audit". |
| 164.312(c)(1) | Protect ePHI from improper change or destruction | Met | The controls:<br>- role checks;<br>- every write audited in the same transaction;<br>- append-only audit and ledger tables;<br>- Postgres on localhost only;<br>- nightly encrypted backups with a SHA-256 manifest and a tested restore (docs/BACKUPS.md "Last test").<br>The Registry and Relay hold no ePHI (import-boundary tests). |
| 164.312(c)(2) | Mechanism proving ePHI wasn't altered or destroyed (A) | Gap | The hash chain authenticates audit events, and the backup manifest authenticates backups. Live PHI rows (messages, appointments, patients) have no integrity check of their own. Without the chain head stored off the box (#56 open), someone who rewrites a whole table and recomputes every hash goes undetected (docs/SECURITY.md). |
| 164.312(d) | Verify people and systems are who they claim (R) | Met | People and systems are all authenticated:<br>- **Staff:** password plus single-use TOTP, with a lockout (`checkStaffPassword`, `TestStaffSignIn`, `TestStaffLockout`).<br>- **Patients:** in-office invite plus a Secure Enclave key proof on pairing and on every refresh (`TestDeviceProof`).<br>- **Office ↔ Relay:** pinned SSH keys.<br>- **Phone → office and Relay:** pinned TLS keys.<br>- **Registry console:** password plus TOTP. |
| 164.312(e)(1) | Guard ePHI sent over networks | Met | Phone ↔ office is TLS 1.3 end to end through the Relay, with both pins (`internal/tunnel/tunnel.go` `MinVersion: tls.VersionTLS13`, `OfficeTransport`). Staff web is HTTPS on the LAN (`ServeStaffTLS`). Video is DTLS-SRTP. Push payloads carry no PHI, and the audit export goes only over TLS to a BAA-covered destination (docs/OBSERVABILITY.md). |
| 164.312(e)(2)(i) | Detect improper changes to ePHI in transit (A) | Met | TLS and DTLS-SRTP authenticate every record (AEAD). Pinned end-to-end TLS means the Relay can't alter or replay requests. DTLS fingerprints travel over pinned signaling and are checked (`DTLSFingerprintTests`). |
| 164.312(e)(2)(ii) | Encrypt ePHI in transit where appropriate (A) | Met | TLS 1.3 for every phone path (`internal/relay/relay.go`, `internal/tunnel/tunnel.go`, iOS `relayTLSOptions` and the ATS minimum). TLS 1.2 or later on the staff LAN listener (`internal/server/server.go:239`). Video is DTLS-SRTP. Media never goes out unencrypted. |

## Practice-owner documents

What the practice owner must provide or sign, which code can't. Per docs/SECURITY.md "Audit and pen-test readiness" item 3, these are owner documents.

- **Risk analysis (45 CFR §164.308(a)(1)(ii)(A)).** WeHelp contributes the threat model, assets, attackers and trust boundaries (docs/SECURITY.md) and the component architecture (docs/ARCHITECTURE.md). The owner adds the practice's own risks, likelihood and impact.
- **Security policies and procedures.** WeHelp contributes the as-built controls (docs/SECURITY.md), the install and fielding checklists (docs/onboarding/RUNBOOK.md, docs/APPLIANCE.md) and the backup and restore runbook (docs/BACKUPS.md). The owner writes and adopts the policies, including the emergency access procedure, which is still a Gap.
- **Workforce training.** WeHelp contributes the staff sign-in, role and lockout behavior to teach from (docs/SECURITY.md "Staff ↔ Back Office") and the patient pairing flow (docs/MOBILE_CLIENT.md). The owner runs and records the training.
- **Business associate agreements** with every vendor that touches ePHI. WeHelp contributes:
  - the data map: the Relay and Registry hold no PHI (docs/ARCHITECTURE.md);
  - the audit export goes only to a BAA-covered destination (docs/OBSERVABILITY.md);
  - the open "Production Relay host and operator" decision, which sets WeHelp's own BAA position (docs/SECURITY.md).
- **Contingency plan** (data backup, disaster recovery, emergency mode operation). WeHelp contributes nightly encrypted backups, the restore procedure, quarterly restore tests with the last test recorded, and the failure banner (docs/BACKUPS.md). The owner holds the `age` private key and the FileVault recovery key (docs/onboarding/RUNBOOK.md).

## Gaps and where they're tracked

| Row | Tracked in |
|---|---|
| 164.312(a)(1) | #169 |
| 164.312(a)(2)(ii) | #169 |
| 164.312(a)(2)(iv) | #169 |
| 164.312(c)(2) | #169 |

### Notes from the review

- **164.312(a)(1)** (Back Office, practice): closes when (a)(2)(ii) closes.
- **164.312(a)(2)(ii)** (Back Office, practice): write a break-glass procedure covering:
- **164.312(a)(2)(iv)** (Back Office, iOS):
- **164.312(c)(2)** (Back Office):
