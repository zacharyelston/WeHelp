# Lit review — prior art per feature

Every `needs-spec` issue starts here: survey what exists before designing.
Verdicts: **adopt** (depend on it), **reference** (steal the design),
**skip** (hand-roll — nothing good exists).

## Messaging / memos / check-ins (#2, #10)

| Candidate | Verdict |
|---|---|
| Open mHealth (IEEE 1752 schemas) | **reference** — align `messages.metadata` schema names to OMH for check-ins (weight, BP, diet); makes FHIR Observation mapping near-free later |
| Medplum (TS, headless EHR) | **reference** — architecture patterns: resource-based API, subscriptions for notifications |
| Go messaging libs | **skip** — none fit; it's CRUD + our audit tx, hand-roll is correct |

## Scheduling + reminders (#7, #8)

| Candidate | Verdict |
|---|---|
| **riverqueue/river** | **adopt** — Postgres-backed job queue, pgx driver, *transactional enqueue* (job inserts inside our existing tx, alongside `audit.Record`), scheduled + periodic + unique jobs, active (v0.44, 2026). Kills the "polling goroutine" plan for reminders — enqueue a scheduled job per reminder instead. MPL-2.0 |
| emersion/go-webdav | **note only** — CalDAV if we ever sync clinic calendars |

## Auth → SSO (#18)

| Candidate | Verdict |
|---|---|
| coreos/go-oidc/v3 | **adopt** — the maintained OIDC client lib for verifying IdP tokens |
| **zitadel/zitadel** | **reference/adopt** — open-source IdP written in Go + Postgres. Option for on-prem installs with no corporate IdP, and a self-host answer to "who is the IdP" |
| keycloak/authentik | reference — non-Go but the standard self-host IdPs; we interoperate via OIDC regardless |

## Ledger / accounting (#12, M4)

| Candidate | Verdict |
|---|---|
| formancehq/ledger | **reference** — production open-source double-entry ledger in Go; study its posting model and commit conventions |
| tigerbeetle | **reference** — safety-first accounting DB (Zig); its correctness docs are the bar for our ledger tests |
| immudb | **reference** — Go immutable DB with Merkle proofs; if our hash chain ever needs to graduate to cryptographic proofs, this is the path |

## Audit trail (#13)

| Candidate | Verdict |
|---|---|
| immudb, google/trillian | **reference** — both prove we chose the right family of design (append-only + chained hashes); ours stays hand-rolled until scale demands otherwise |

## Push notifications (#9, now sent by the Relay: #7)

| Candidate | Verdict |
|---|---|
| sideshow/apns2 | **reference; prefer hand-rolling** (spike S3, docs/spikes/S3.md). It works, but the last release is Oct 2024 and it pins a 2022 `golang.org/x/net` with known vulnerabilities, so using it means dependency overrides. A hand-rolled HTTP/2 POST with an ES256 JWT is ~100 lines with no extra dependencies |
| Firebase/Expo push | skip — third-party PHI routing, no |

## FHIR (#16)

| Candidate | Verdict |
|---|---|
| samply/golang-fhir-models | **dead** — v0.3.2 Dec 2022 |
| google/fhir Go module | **dead** — v0.7.4 Aug 2022; CMSgov/bcda-app removed it over Snyk vuln flags |
| hand-rolled R4 structs | **adopt** — minimal Patient/Observation/Appointment structs + `encoding/json`; the bcda-app precedent. Re-evaluate libs if resource surface grows |

## API contract (#4)

| Candidate | Verdict |
|---|---|
| oapi-codegen/oapi-codegen | **evaluate** — spec-first codegen for chi; alternatively keep hand-written handlers + spec as documentation, generate the Swift client from the spec later |

## V1 secure channel: Back Office ↔ Relay ↔ phone (V1-M1, V1-M2)

Requirement: the Relay connects phones to their office without being able to
read or impersonate it. The office opens only outbound connections. See
ARCHITECTURE.md, "Connections".

| Candidate | Verdict |
|---|---|
| golang.org/x/crypto/ssh | **adopt**: office ↔ Relay transport. Key auth, host-key pinning and custom channel types are all in the standard Go extended library, with no new protocol to invent or audit |
| iOS 17 `ProxyConfiguration` (HTTP `CONNECT`, Network framework) | **adopt** (confirmed by spike S1, docs/spikes/S1.md): `URLSession` reaches the Relay as a TLS `CONNECT` proxy with a pinned relay key; the inner TLS stays end to end |
| `URLSession` delegate key pinning | **adopt**, with a scoped ATS exception: S1 showed default ATS rejects a pinned self-signed office cert even when the delegate anchors trust. V1 keeps one exception for the reserved `*.wehelp.invalid` office names (TLS 1.3 still required; the pin still fails closed). Network.framework for the inner TLS is the fallback if audit challenges it |
| Tailscale Funnel | **reference**: an edge that routes without terminating TLS. Same principle |
| frp, yamux, SNI-passthrough routing | **superseded** by SSH + `CONNECT` (fewer moving parts; no public DNS or certificates per office) |
| Cloudflare Tunnel, ngrok default edges | **skip**: they terminate TLS at a third party, which would see PHI |
| modernc.org/sqlite | **adopt**: the Relay's registry and push tokens, pure Go, single file |

## Registry: control plane for practices and Relays (V1-M2, V1-M3)

| Candidate | Verdict |
|---|---|
| Tailscale coordination server | **reference**: a control plane that hands out signed peer lists while the data plane never depends on it being up. That's the same split as Registry and Relay |
| The Update Framework (TUF) | **reference**: signed metadata with version numbers to block rollback and freeze attacks. We borrow the rules (signature, monotonic version, cache last good), not the full framework |
| `crypto/ed25519` (Go standard library) | **adopt**: signs office lists; no dependency |
| SSH certificates (Registry as an SSH CA for office keys) | **evaluate later**: would replace key lists with signed certs, but revocation still needs a list, so V1 keeps the signed list |
| Hardware-backed signing (cloud KMS or a YubiHSM) | **evaluate before multi-practice field use**: keeps the signing key off disk |

## V1 identity and staff auth (V1-M2)

| Candidate | Verdict |
|---|---|
| CryptoKit `SecureEnclave.P256` | **adopt** (built in the PoC): the device key is the patient credential |
| `LocalAuthentication` (Face ID or passcode) | **adopt**: unlocks the app; patients have no password in V1 |
| pquerna/otp | **adopt**: TOTP for staff sign-in. Small, widely used |
| Apple App Attest | **evaluate** (#11): proves the genuine app at enrollment |
| A local breached-password list (HIBP-style hashes) | **evaluate**: staff password screening without an outbound call |

## V1 telehealth (V1-M4)

| Candidate | Verdict |
|---|---|
| WebRTC 1:1, DTLS-SRTP end to end | **adopt**: for two parties, peer-to-peer media through TURN is already end-to-end encrypted; no SFU needed |
| pion/turn | **adopt** (confirm in spike S2): TURN server in the Relay binary, in Go |
| WebRTC iOS framework (Google's build via a SwiftPM package) | **evaluate** in S2: the binary dependency size and update cadence must be acceptable |
| LiveKit, SFU + SFrame | **reference**: only needed for group visits, which are post-V1 |

## V1 operations (V1-M6)

| Candidate | Verdict |
|---|---|
| filippo.io/age | **adopt**: encrypted nightly backups to the practice owner's key |
| govulncheck, staticcheck | **adopt**: CI security and correctness checks |
| OWASP ASVS 4.0 L2, MASVS L2 | **adopt** as the audit checklists (SECURITY.md) |

## Observability and audit export (OBSERVABILITY.md)

| Candidate | Verdict |
|---|---|
| Grafana Alloy | **adopt**: the per-host collector. Already used in the owner's hydra-aks and hydra-host projects, including a syslog listener and Loki pipelines with low-cardinality labels and structured metadata |
| Grafana + Loki + Prometheus | **adopt** for dashboards (optional profile). Each is a single container; Loki takes logs and Prometheus metrics |
| Mimir, Tempo | **skip for V1**: Mimir is sized for far more metrics than an office; traces are optional and can be added later through Alloy without app changes |
| go.opentelemetry.io/otel (SDK + otelhttp) | **adopt**: metrics and traces from `wehelpd` and `wehelp-relay` over OTLP to the local Alloy |
| Syslog RFC 5424 over TLS (RFC 5425) | **adopt** for audit export to SIEMs. Confirm in the PoC whether Alloy's `otelcol.exporter.syslog` covers it, or whether the Back Office writes syslog itself |
| Grafana Cloud | **evaluate** in the Cloud Dashboards PoC against a self-run VPS stack: cost per office, tenancy, retention, BAA availability |
| Docker Compose profiles and resource limits | **adopt**: dashboards are an opt-in profile with CPU and memory caps |

## Non-Go prior art worth knowing

OpenEMR (PHP), GNU Health (Python), HAPI FHIR (Java), Medplum (TS) — all
monoliths around EHR/FHIR data models. We deliberately are not one: we're a
communication-and-scheduling layer that maps onto standards, not an EHR.
