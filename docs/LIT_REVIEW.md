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
| **riverqueue/river** | **adopt** — Postgres-backed job queue, pgx driver, *transactional enqueue* (job inserts inside our existing tx, alongside `audit.Record`), scheduled + periodic + unique jobs, active (v0.44, 2026). Kills the "polling goroutine" plan for reminders — enqueue a scheduled job per reminder instead. MPL-2.0. **Not used for #19:** River brings its own schema migrations next to Flyway, and at ~500 patients a one-minute poll with an idempotency key (`reminders_sent`), written in the same transaction as the message and audit event, gives the same guarantees. Revisit if jobs outgrow reminders. |
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
| modernc.org/sqlite | **adopt** for push tokens (#7), pure Go, single file. Not needed for the office list: the Relay caches the signed envelope as one file and re-verifies it on load (#4) |

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
| rsc.io/qr | **adopt** (#37): QR code PNGs for Pair phone, generated by the Back Office. Tiny, pure Go, no dependencies, stable API |
| VisionKit `DataScannerViewController` | **adopt** (#37): the app's QR scanner, built into iOS 16+ with no third-party code |
| pquerna/otp | **adopt**: TOTP for staff sign-in. Small, widely used |
| Apple App Attest | **evaluate** (#11): proves the genuine app at enrollment |
| A local breached-password list (HIBP-style hashes) | **evaluate**: staff password screening without an outbound call |
| Staff invites (#100): admin issues a set-up link, the person sets their own password | **adopt, built in-house**. This is the usual pattern (GitLab, Grafana and Keycloak "required actions"; OWASP ASVS V2.5 and NIST SP 800-63B §6 on initial secrets): a random, single-use, short-lived token, stored hashed, never a password the admin chooses. Our own: 32 random bytes in the URL fragment (kept out of logs and Referer), SHA-256 stored, 24 h, one live invite per account. It leads into the existing TOTP enrollment (#38). A library (Keycloak, Ory Kratos) would be new infrastructure for one form |

## V1 telehealth (V1-M4)

| Candidate | Verdict |
|---|---|
| WebRTC 1:1, DTLS-SRTP end to end | **adopt**: for two parties, peer-to-peer media through TURN is already end-to-end encrypted; no SFU needed |
| pion/turn | **adopt** (confirmed in S2): relay-only 1:1 calls worked, and two calls cost about 6% of one core and 14 MB. Add a quota, a peer `PermissionHandler` and TURN over TLS on 443. See [spikes/S2.md](spikes/S2.md) |
| WebRTC iOS framework (Google's build via a SwiftPM package) | **adopt** (S2): `stasel/WebRTC` 157.0.0, BSD-3, about 12 MB installed and 5.5 MB to download, roughly monthly releases. Pin it, mirror it, and update every quarter. See [spikes/S2.md](spikes/S2.md) |
| LiveKit, SFU + SFrame | **reference**: only needed for group visits, which are post-V1 |

## Practice website, library and store (V1-M5, #5, #97)

Requirement: staff build a small public site in the Back Office (about us,
bios, services, how to get the app and join, a library of podcast links and
PDFs, and a supply catalogue) and the Relay serves it. Patients order
supplies in the app. Orders are patient data, so they stay in the Back
Office and never pass through the public site.

| Candidate | Verdict |
|---|---|
| Go `html/template` + `embed` | **adopt**: renders the pages in the Back Office with contextual escaping; no build step, no dependency |
| `archive/tar` streamed over a new SSH channel type (`wehelp-site`) | **adopt**: the bundle rides the office's existing authenticated connection, so the Relay binds it to the office ID with no extra signing scheme. SSH global requests are capped at one 256 KB packet, too small for PDFs, so a channel is used instead |
| Hugo, Jekyll, other static site generators | **reference**: the right shape (templates plus content to static files), but a second toolchain for a site of five pages |
| Hosted site builders (Squarespace, Wix), WordPress | **skip**: a third party or a dynamic CMS per practice, with logins and plugins to secure |
| Content-Security-Policy `default-src 'none'` with no scripts | **adopt**: the Relay serves every site file with it, and refuses HTML with `<script`, so a site can't run code or call out |
| Shopify, WooCommerce, Stripe Checkout | **skip for V1**: payment is out of scope ("pay at pickup or by invoice"), and an order is patient data that must stay in the Back Office |
| Podcast RSS feeds and embedded players | **skip**: plain external links. Embedding a player means third-party scripts and trackers on the practice site |
| iOS QuickLook (`quickLookPreview`) | **adopt**: shows library PDFs that the app fetched through its pinned tunnel |
| Blog posts in Hugo/Jekyll (front matter with a date, drafts, future posts held back) | **adopt the model** for the practice blog (#121): title, plain-text body, optional image, a publish time, and a draft flag. Rendered by the same `html/template` pages. No Markdown or HTML in posts, so nothing needs sanitising |
| WordPress or Ghost scheduled publishing | **reference**: "future" posts go live by themselves. Ours: a once-a-minute check in the Back Office republishes the static site when a scheduled post comes due |
| RSS/Atom feed for the blog | **skip for now**: easy to add later as one more static file; nothing asks for it yet |

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

## Local model runtimes (post-V1 agent, #83)

Requirement: run a language model on the Back Office (Apple silicon, macOS)
with no network path, no telemetry, a local socket API, and structured
output, so patient data never leaves the box. See
[LOCAL_AGENT.md](LOCAL_AGENT.md). Verdicts are provisional until #83.

| Candidate | Verdict |
|---|---|
| llama.cpp (`llama-server`) | **evaluate first**: C/C++, MIT, Metal acceleration, GGUF quantised models, an HTTP server that can bind to a Unix socket or loopback, and grammar or JSON-schema constrained output for typed tool calls. Few dependencies, so it is easy to pin and audit. Fast-moving: pin a release and update on a schedule |
| MLX / `mlx-lm` (Apple) | **evaluate**: MIT, built for Apple silicon's unified memory, often the fastest on Macs. Python runtime and dependencies to pin; its server is simpler than llama.cpp's |
| Ollama | **reference, likely skip**: convenient wrapper around llama.cpp, but by default it listens on TCP and pulls models from its own registry. Usable only with both locked down, which removes most of its value |
| Apple Foundation Models framework | **evaluate for small tasks**: Apple's on-device model, no network, Swift API. Small model, so suited to classification and short drafts, not long reasoning; needs a Swift helper beside `wehelpd` |
| LM Studio | **skip**: desktop GUI app, closed source |
| vLLM, TGI | **skip**: built for NVIDIA GPU servers, not Apple silicon |
| llamafile | **reference**: single-file packaging of llama.cpp and weights; an option for shipping a pinned runtime |
| Open-weight models (Llama, Qwen, Mistral, Gemma families) | **evaluate in #83**: pick by quality at a size that fits the box, and by licence terms for a commercial clinical setting. Pin by file hash |
| Hosted model APIs | **skip**: sending PHI to a third party is what this design exists to avoid |

## Practice types, message switches and care teams (post-V1, PRACTICE_TYPES.md)

Requirement: serve practices beyond naturopaths (therapists and
psychologists, other allied health, and non-clinical services such as hair
salons). Let a practice or a provider turn messaging off. Let a patient
choose a group of providers who all see their messages, for continuity of
care. Verdicts are provisional until the work issues are specified.

| Candidate | Verdict |
|---|---|
| HIPAA treatment disclosures (45 CFR 164.506) | **reference**: providers may share PHI with each other for treatment without the patient’s authorization. Asking the patient before adding someone to their care team is therefore a product choice, stricter than the rule, and it suits therapy practices |
| HIPAA psychotherapy notes (45 CFR 164.501, 164.508(a)(2)) | **reference**: a therapist’s separate session notes need specific authorization to disclose. Messages are not psychotherapy notes, but therapy practices expect messaging to be off or private by default |
| 42 CFR Part 2 (substance use disorder records) | **reference**: stricter consent for SUD treatment records. A therapy preset must be able to keep every thread private to one provider. Legal review before marketing to SUD programs |
| FHIR R4 `CareTeam` resource | **adopt the shape**: a patient-scoped list of participants with roles and a period. Our `care_team_members` maps onto it for FHIR later (#26) |
| Shared-inbox “message pools” in patient portals (e.g. Epic MyChart) | **reference**: a team or front desk answers a practice inbox, and a clinician can be out of the pool. That is the practice inbox and the provider message switch here |
| Therapy practice software with client portals (e.g. SimplePractice, TherapyNotes) | **reference** to study: client portals, secure messaging that can be turned off, and “not for emergencies” notices |
| Salon booking apps (e.g. Square Appointments, Vagaro, Fresha) | **reference** to study: booking, reminders, a client list, retail and a staff picker. Salons are not HIPAA covered entities; they need the same app with plainer words and no clinical features |
| Feature-flag services (LaunchDarkly and similar) | **skip**: a few practice and provider switches stored in the Back Office are enough; no new infrastructure |

## Questionnaires, intake forms and eCOA (#82, design in [QUESTIONNAIRES.md](QUESTIONNAIRES.md))

Reviewed 2026-11-16, when questionnaires became WeHelp's main feature set
beyond scheduling and messaging. These are custom intake forms and
eCOA-style captures: patient-reported (ePRO) at home on the phone or in the
office on a kiosk, assigned on schedules, using standard instruments with
their scoring.

| Candidate | Verdict |
|---|---|
| Interventionaire (in-house). Legacy: AdonisJS + MySQL + AngularJS. Renewal: `interventionaire-v2`, NestJS + Prisma + React, PostgreSQL, OIDC, MIT | **reference for design, not a dependency or a format.** WeHelp copies the shape: sections and pages, branches between sections, element types, themes, proctored runs. v2's pages are rich-text HTML, with branch rules as expression strings. That suits interventions, but not coded, scored items, so WeHelp has its own item model. v2 never runs in a practice and never receives answers. Importing from v2 can come later as a converter. See [onboarding/KIOSK_DEMO.md](onboarding/KIOSK_DEMO.md) |
| FHIR R4 `Questionnaire` / `QuestionnaireResponse` | **adopt the shape, not the wire format.** WeHelp's form definition maps one to one onto a subset: `item` with `linkId`, `type`, `code`, `answerOption` (with `ordinalValue` for scores), `enableWhen` and `initial`. Export to FHIR comes with #26. FHIR itself isn't stored: it's verbose, and its full expression features need FHIRPath |
| HL7 SDC (Structured Data Capture) IG | **reference** for what to map later: `calculatedExpression` and `variable` (scoring), `itemControl` (slider, drop-down), `entryMode` (sequential, prior-edit). **Rejected as the engine:** SDC expressions are FHIRPath or CQL strings, and evaluating them safely on both Go and Swift is too much. WeHelp uses a small JSON expression tree instead |
| NLM LHC-Forms | **reference**: a JavaScript renderer for FHIR Questionnaires, with a public library of loaded instruments to check our builds against. Not used: the walker is native SwiftUI, and the staff web has no framework |
| SurveyJS | **reference** for the builder's UX and its expression ideas (`visibleIf`, `calculatedValues`). The library is MIT, but the Creator (builder) is commercial. Not used |
| Apple ResearchKit (BSD) | **reference, not a dependency**: its step and navigation-rule model and its consent and active tasks are a good map of what eCOA on iOS needs. It's UIKit-era and much larger than we need, so the walker is SwiftUI |
| CDISC ODM, REDCap data dictionary | **reference** for exporting data to research tools later. Out of scope now |
| FDA PRO guidance, ISPOR ePRO good practice (faithful migration of paper instruments, measurement equivalence) | **adopt the principles for standard instruments.** Wording, answer order and recall period stay as published. One item per screen where the source does so. Timestamps on every answer. No back-filling outside the completion window. Built-in instruments are read-only in the builder |
| 21 CFR Part 11 (electronic records: audit trails, attributable corrections) | **reference; partly met by design.** Submitted answers are append-only and audited by `audit.Record`. Staff corrections with reasons are deferred to a later release |
| PHQ-9, GAD-7 (and their 2-item short forms), Pfizer | **adopt**: no permission is needed to reproduce, translate, display or distribute them. Ship them with LOINC codes and published scoring |
| PROMIS (HealthMeasures) | **later**: free, but under terms of use, and the CAT versions need IRT scoring. Fixed short forms could follow once the terms are reviewed |
| EQ-5D and other licensed instruments | **reject for built-ins**: licence fees and registration. A practice could hold its own licence and build the form itself |
| Open mHealth (IEEE 1752) | already **reference** for check-ins (#21). Scored instruments map to FHIR `Observation`, not OMH |

## Non-Go prior art worth knowing

OpenEMR (PHP), GNU Health (Python), HAPI FHIR (Java), Medplum (TS) — all
monoliths around EHR/FHIR data models. We deliberately are not one: we're a
communication-and-scheduling layer that maps onto standards, not an EHR.
