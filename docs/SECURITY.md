# WeHelp V1 security model

V1 must pass an independent security audit and penetration test before it
holds real patient data. This document is what the auditors and pen testers
work from. Architecture is in [ARCHITECTURE.md](ARCHITECTURE.md).

**Bar:** HIPAA Security Rule technical safeguards, OWASP ASVS 4.0 Level 2
(Back Office and Relay), and OWASP MASVS Level 2 (iOS app). We treat all
patient data as PHI whether or not the practice is a HIPAA covered entity.

## What we protect

| Asset | Lives on | Worst case if lost |
|---|---|---|
| Patient records, messages, appointments | Back Office Postgres | Disclosure of PHI |
| Telehealth audio and video | In flight between two apps | Eavesdropping on a visit |
| Office TLS key | Back Office | A fake office that phones would trust |
| Office SSH key | Back Office | Impersonating the office to the Relay |
| Device keys | iPhone Secure Enclave | Impersonating one patient (key is not extractable) |
| Staff credentials (password + TOTP) | Staff, hashed in Back Office | Staff-level access from the LAN |
| Audit chain | Back Office Postgres | Undetected tampering |
| Backups | Encrypted, off the Mac mini | Same as patient records |

## Who we defend against

1. **Network attackers** on the practice Wi-Fi, the internet path or a hostile
   network the patient is on.
2. **A compromised or curious Relay** (including its operator, us). The
   Relay is untrusted for patient data by design.
3. **Someone with a patient's unlocked phone**, or a lost or stolen phone.
4. **A malicious or careless staff member** inside the practice.
5. **An attacker holding a stolen Mac mini** or a stolen backup.

Out of scope for V1: a fully compromised Back Office host OS, a nation-state
attacker, and jailbroken devices with root on the patient's own phone.

## Trust boundaries and controls

### Phone ↔ Back Office (through the Relay)

- TLS 1.3, end to end, terminated only on the Back Office.
- The app pins the office TLS public key (SPKI SHA-256) from the QR pairing.
  It fails closed on mismatch and has no "trust anyway" option.
- The Relay sees an office ID, timing and byte counts. It cannot decrypt,
  alter or replay any request.
- **Sessions:**
  - access tokens live 15 minutes in memory;
  - refresh tokens are single-use and bound to the device key (an ECDSA
    proof on every refresh);
  - patient sessions must be device-bound.
- **Enrollment** needs a single-use, 10-minute invite code from an in-office
  QR, plus proof of the new device key. Invites are bound to one patient and
  logged.

### Back Office ↔ Relay

- SSH with an office ed25519 key against a registry of allowed keys. The Back
  Office pins the Relay host key.
- The Relay can only open phone channels to an office, and the Back Office
  accepts only that channel type.
- Relay requests from the Back Office (push, TURN credentials, publish site)
  carry opaque IDs and fixed categories. The Relay validates them and the
  Back Office never sends free text.

### Staff ↔ Back Office (LAN)

- HTTPS only, using the office local CA, installed on staff machines at
  setup.
- Password (minimum 12 characters, checked against a breached-password list)
  plus TOTP. Sessions expire, cookies are `Secure; HttpOnly; SameSite=Strict`,
  and there is CSRF protection.
- Staff routes are refused on Relay channels: the listener decides reachability,
  not a header.
- Admin actions (users, pairing, device revoke, settings) are audited.

### Telehealth

- Signaling goes only over the pinned connections, so DTLS fingerprints are
  authentic.
- Media is DTLS-SRTP end to end, relay-only ICE through the Relay's TURN, with
  short-lived TURN credentials per visit.
- No recording. A visit is audited as started, joined and ended, with no
  content.

### Push and website

- Push payloads come from a fixed category list, with no names, times or text.
- The website is static, rendered only from the Practice profile, and has no
  forms, scripts from third parties or cookies.

## Data protection at rest

- The Mac mini runs FileVault, with automatic login disabled once fielded.
  Barnacle uses auto-login for testing only.
- Postgres listens on localhost only.
- Backups are nightly, encrypted with `age` to a key held by the practice
  owner, and copied off the Mac mini. A restore is tested before go-live and
  quarterly after that.
- On the phone:
  - the device key is in the Secure Enclave;
  - the refresh token is in the Keychain with
    `AfterFirstUnlockThisDeviceOnly`;
  - no PHI is cached on disk in V1 (the app fetches on view);
  - screens with PHI are hidden in the app switcher.

## Logging and audit

- `audit_events` records every PHI read and write, with actor, device and
  action. It is hash-chained, and `audit.Verify` runs nightly and alerts on
  failure.
- Application logs carry no PHI: IDs only, never names, message text or
  tokens.
- Relay logs carry office ID and byte counts, kept for 7 days, with no device
  IPs.

## Secure development

- CI runs on every PR:
  - `go build`, `go vet`, `go test` with the race detector;
  - `govulncheck`;
  - `staticcheck`;
  - the iOS build;
  - OpenAPI validation;
  - the Relay import-boundary test.
- Dependencies are pinned and reviewed. New dependencies need a lit-review
  entry.
- Secrets never go in the repo. Demo credentials are seed data only and are
  never present in a field build.
- Release builds are produced from tagged commits. Debug-only features (demo
  servers, demo passwords) are compiled out of Release.

## Audit and pen-test readiness

Before the audit:
1. **Threat model:** this document, reviewed and current.
2. **Control mapping:** an ASVS L2 and MASVS L2 checklist with each
   requirement marked met, not applicable, or accepted with reasoning
   (`docs/security/asvs-l2.md`, `docs/security/masvs-l2.md`).
3. **HIPAA mapping:** the technical safeguards (access control, audit
   controls, integrity, authentication, transmission security) mapped to
   controls above. The administrative and physical safeguards (risk
   analysis, policies, workforce training, device and media controls, BAAs)
   are prepared with the practice owner. They are owner documents, not code.
4. **Pen-test scope:**
   - the Relay (all listeners);
   - the Back Office (Relay channel and LAN);
   - the iOS app (static and dynamic);
   - pairing and enrollment;
   - telehealth signaling and TURN;
   - the staff web UI.

   The pen test runs against the Demo topology with synthetic data.
5. **Findings:** fixed or formally accepted before field deployment, and a
   retest done for every high or critical finding.

## Open security decisions

- **App Attest:** whether enrollment also requires Apple App Attest.
- **Breached-password list:** a local copy versus k-anonymity lookups (a
  local copy keeps the Back Office offline-safe).
- **Production Relay host and operator:** these determine the BAA position.
