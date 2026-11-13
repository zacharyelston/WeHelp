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

## Push notifications (#9)

| Candidate | Verdict |
|---|---|
| sideshow/apns2 | **adopt** — canonical APNs HTTP/2 lib; slow-moving but the protocol is stable and thin. If it rots, maintained forks exist (kokteyldev/apns2) and a hand-rolled HTTP/2+JWT client is ~100 lines |
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

## Relay: tunnels, site hosting, telehealth (Back Office / Relay split)

Requirement: the cloud Relay routes phone, web and video traffic to each
office's Back Office without being able to read it. See ARCHITECTURE.md,
"Back Office and Relay".

| Candidate | Verdict |
|---|---|
| Tailscale Funnel | **reference** — the closest design: the public edge routes by SNI and the node terminates TLS, so the edge never sees plaintext |
| fatedier/frp (Go) | **evaluate** — self-hosted reverse proxy over an outbound tunnel, with SNI-routed HTTPS vhosts that pass TLS through. Either adopt it, or use it as the reference for a small hand-rolled relay |
| hashicorp/yamux | **adopt** if hand-rolled — multiplexes many streams over the one outbound tunnel connection |
| Cloudflare Tunnel, ngrok (default edges) | **skip** — they terminate TLS at the edge, so the provider sees PHI. ngrok's TLS-passthrough edges are acceptable but third-party |
| caddyserver/certmagic | **adopt** — the Back Office obtains its own certificates. TLS-ALPN-01 works through an SNI-passthrough relay |
| LiveKit (Go SFU) | **evaluate** — supports end-to-end encrypted media with client-held keys, so the SFU forwards ciphertext. LiveKit vs Pion is still an open owner decision |
| pion/webrtc | **evaluate** — lower-level Go WebRTC if LiveKit is too heavy for the Relay |
| SFrame (RFC 9605) | **reference** — the standard for end-to-end media encryption through an SFU |
| HHS cloud-computing guidance (HIPAA) | **reference** — a service that stores or routinely handles ePHI is a business associate even when the data is encrypted. The narrow "conduit" exception covers transmission only. Plan on BAAs for the Relay; designing it to hold no PHI limits exposure, but it does not remove that obligation. This is not legal advice |

## Non-Go prior art worth knowing

OpenEMR (PHP), GNU Health (Python), HAPI FHIR (Java), Medplum (TS) — all
monoliths around EHR/FHIR data models. We deliberately are not one: we're a
communication-and-scheduling layer that maps onto standards, not an EHR.
