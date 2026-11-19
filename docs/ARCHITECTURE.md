# WeHelp V1 architecture

WeHelp V1 connects a practice with its patients. The V1 practice is a
naturopath office. Patients use an iPhone app for appointments, messages and
telehealth visits. The practice runs its own server (the Back Office), a
Relay connects phones to it, and the WeHelp Registry, our corporate support
arm, decides which practices exist and which Relay serves them.

This document is the source of truth for V1. Security goals and the audit bar
are in [SECURITY.md](SECURITY.md); build order and scope are in
[V1_PLAN.md](V1_PLAN.md).

## Approved remaining V1 work (2026-11-16)

The following direction is approved; the linked issue specifications still
need their reviewer pass and implementation evidence. Existing behavior is
described separately below, so this list does not claim the controls ship.

- Support enters practices in the Registry (#79); no public registration
  workflow. The provider uses the existing role-based WeHelp app (#69).
- Practice sites use company-owned office hostnames and public TLS, with
  Universal Links for navigation only (#5). Links never establish pairing
  trust. Custom domains and public patient enrollment remain out of scope.
- New field enrollment requires App Attest (#11). Staff pairing also needs
  the account holder's TOTP; device credentials require user presence and a
  non-sliding renewal deadline (#163). Patients stay passwordless.
- Staff/console recovery is local and operator-assisted (#162). Phone account
  preferences retain identifiers/routing trust only, not patient names (#164).
  Version discovery and an emergency minimum app build are explicit (#165).
- Planned pin rollover and compromise recovery have separate procedures
  (#168). Field transport policy is #166; security versus operational logging
  is #167. Emergency access, FileVault freshness and live-row integrity
  evidence are #169.
- Required audit retention distinguishes privacy-safe event projections,
  independent source-chain anchors and full source verification (#56).
  Operational telemetry cannot carry patient/actor/device identifiers.
- A demo reset recreates only the positively identified demo database;
  office keys/enrollment survive and all phones re-pair (#190). The current
  demo runs its always-on Relay and Registry on Barnacle (#180/#182).
- The corporate cloud dashboard PoC (#58) is Post-V1. Required V1 telemetry,
  audit retention, independent review and field acceptance remain in scope.

Patient-record notebook exploration is separate from V1 release scope.

## The services

```
                       WeHelp Registry (corporate)
                       practices · providers · subscriptions
                       admin console for WeHelp support
                          │ signed office list        ▲ install token,
                          ▼ (pulled by each Relay)    │ office public key
                    TLS end to end, pinned office key │
 iPhone app ═════════════════════════════════════════════► Back Office
 (patients,  ──CONNECT──► Relay ◄──── outbound SSH ──────── (in the practice)
  providers)              │  routes by office ID            wehelpd + Postgres
                          │  sees ciphertext only           ALL patient data
                          │  TURN for video media           staff web UI
 website visitors ─HTTPS─►│  static practice site           (office LAN only)
                          │  sends PHI-free pushes
```

| Service | Runs on | Holds | Never holds |
|---|---|---|---|
| **Back Office** (`wehelpd`) | A Mac mini in the practice | All patient data, staff accounts, the audit chain | Inbound internet ports |
| **Relay** (`wehelp-relay`) | A server we operate (V1 demos: the dev laptop) | A cached, signed office list from the Registry; published websites; push tokens | Patient data, office TLS keys, plaintext of any patient connection |
| **Registry** (`wehelp-registry`) | WeHelp's corporate server (V1 demos: the dev laptop) | Practices, their providers and admin contacts, subscriptions, office public keys, Relay assignments | Patient data of any kind |
| **Patient app** (Mobile) | The patient's own iPhone | A Secure Enclave device key and the pinned office identity | Long-lived passwords; patient data at rest |
| **Provider app** (Mobile, #69) | The provider's own iPhone or iPad, in or out of the office | The same, with the provider role. **Decision (2026-11-14): one WeHelp app** that opens on the provider view when the device is paired to a provider or admin account; no separate listing | The same |
| **Kiosk iPad** (post-V1, #8) | A shared, office-owned iPad on the office Wi-Fi | An office device key; talks to the Back Office directly, never through the Relay | Anything from one patient once the next begins |
| **Dashboards** | Anywhere (see [OBSERVABILITY.md](OBSERVABILITY.md)) | Telemetry from the Back Office, Relay and Registry | Patient data |

**The rule:** the parts that connect phones or run the business never hold or
read patient data. Patient data lives only in the Back Office and, while
displayed, on the patient's own phone. Every V1 design choice below follows
from it.

## Connections

### Back Office → Relay: SSH

- The Back Office opens one outbound SSH connection to the Relay
  (`golang.org/x/crypto/ssh`) and keeps it up, reconnecting with backoff.
- It authenticates with an **office SSH key** (ed25519, generated at install
  and never leaving the Back Office). The Relay accepts only offices on the
  Registry's signed office list, so being registered and active in the
  Registry is how an office subscribes.
- The Back Office pins the Relay's SSH host key and refuses to connect if it
  changes.
- For each phone connection, the Relay opens an SSH channel to that office.
  It never opens anything else.
- The Back Office sends Relay requests (publish website, send push, issue
  TURN credentials) as typed SSH requests that carry opaque identifiers only.
- The practice router needs no port forwarding: all traffic leaves the
  practice as one outbound connection.

### iPhone → Back Office: through the Relay, end to end

1. The app connects to the Relay and asks it, with an HTTP `CONNECT`, for
   office `<office-id>` (iOS 17 `ProxyConfiguration`). That outer hop is TLS
   to the Relay's own certificate.
2. The Relay looks up the office and splices the connection onto an SSH
   channel to that Back Office.
3. Inside, the app runs TLS 1.3 **directly with the Back Office** and
   accepts only the Back Office public key it pinned at pairing. The Relay
   relays ciphertext. It cannot read, alter or impersonate the office.

Every phone takes this path, including on the office Wi-Fi. There is one path
to secure, test and audit.

The app checks two pins, each the base64 SHA-256 of a P-256 public key in
X9.63 form: the Relay's (outer TLS) and the office's (inner TLS). Both come
from pairing: the office's pairing identity carries the Relay address, Relay
pin, office ID and office pin. A mismatch fails closed with an error that
names which side failed. iOS needs one scoped ATS exception for
`*.wehelp.invalid` (TLS 1.3 still required), because default ATS rejects a
pinned self-signed certificate (spike S1). There is no plain-HTTP or
direct-LAN path in Debug or Release builds (#36).

Everything an app must do on this path (the QR format, pins, device-key
proofs, sessions, error handling, what may stay on the phone) is written
down for every client in [MOBILE_CLIENT.md](MOBILE_CLIENT.md).

### Registry → Relay: signed office list

- The Registry publishes an **office list**: office ID, office SSH public
  key and assigned Relay, with a version number. Only offices of active
  practices are listed: being on the list is the permission to connect. It is
  signed with the Registry's ed25519 signing key, and a new version is
  published in the same transaction as every change that affects it. Format
  and runbook: [REGISTRY.md](REGISTRY.md).
- Each Relay pulls the list over HTTPS every minute, verifies the signature
  against the Registry key it pinned at install, and rejects any list with an
  older version. It keeps the last good list, so a Registry outage doesn't
  stop practices working.
- Suspending an office in the Registry disconnects it from its Relay within
  one pull interval.
- A compromised Relay can't add an office, and a forged list can't be
  injected without the Registry's signing key.

### Back Office ↔ Registry: install and roster

- **Install:** WeHelp support registers the practice in the Registry console
  and gets a single-use **install token**. On the Mac mini, `wehelpd init`
  generates the office keys and sends the token plus the office SSH public
  key to the Registry over HTTPS. The Registry returns the office ID, the
  assigned Relay's address and SSH host key, and the Registry's signing key.
- **Roster:** the Back Office fetches its practice's provider roster from the
  Registry, authenticated with the office key. It uses the roster to invite
  provider staff accounts. Staff accounts and their credentials live only in
  the Back Office.
- The Back Office never sends patient data to the Registry; there is no API
  for it.

### Staff → Back Office: office LAN only

The staff web UI (embedded SPA) and staff API routes are served by the Back
Office on the office LAN over HTTPS, using the office's local certificate
authority (see SECURITY.md). They are **not reachable through the Relay**,
and the Back Office rejects staff routes that arrive on a Relay channel.

## Identity and onboarding

### Office identity

At install, the Back Office generates:
- the **office SSH key** (to authenticate to the Relay);
- the **office TLS key** (for patient connections, pinned by phones);
- a **local CA** (for the staff UI on the LAN).

`wehelpd init` creates them and, with an install token, installs the office
with the Registry. Files, the key pin format and rotation:
[OFFICE_KEYS.md](OFFICE_KEYS.md).

These keys stay on the Back Office. FileVault and the backup encryption
protect them at rest.

### Patient onboarding: in-office QR pairing

1. Staff create or select the patient in the staff UI and press **Pair
   phone**.
2. The Back Office shows a QR code containing: the Relay address and pin,
   the office ID and pin, and a **single-use invite code** that expires in 10
   minutes. The invite is 128 random bits, bound to that one patient in the
   Back Office, which stores only its hash. Nothing about it goes to the
   Relay or the Registry.
3. The patient scans it in the app. The app generates a Secure Enclave
   P-256 key, connects through the Relay, verifies both pinned keys, and
   redeems the invite with a signature from the new key. That enrolls the
   device and signs the phone in.

Issuing invites (and adding patients) is a staff action on the office LAN:
the Back Office refuses those routes when they arrive through the Relay.
Patient password login and patient self-registration are refused. Cutting
access: one patient by revoking their device (#10); a whole practice by
suspending it in the Registry, which drops it from the Relay within a minute.
4. From then on the device key is the patient's credential. Every session
   refresh is signed by it (device-bound sessions, already built). The app
   is unlocked with Face ID or the device passcode; patients have no
   password in V1.

Lost or replaced phone: staff revoke the device and pair the new phone the
same way. A person can have several devices (a phone plus an office iPad,
say), each paired in the office and listed in the staff UI and in the app's
Settings, where people can revoke their own ("Sign out other devices").
Access tokens carry the device ID and every request checks it, so a revoked
device is locked out immediately, not when its token expires (#10). Providers pair their devices the same way, with the provider role,
in the provider app. Because every phone goes through the Relay, providers
reach their schedule, messages and visits remotely with no VPN and nothing
opened at the practice.

### Practices and providers: the Registry

WeHelp support registers each practice in the Registry console: legal name,
address, admin contacts, subscription, and the practice's **providers**
(name, credentials such as licence number, role). These are business and
workforce records, not patient data. Practice admins can view their own
practice's record; changes go through WeHelp support in V1.

### Staff accounts

Staff sign in to the staff UI with a password plus TOTP. Three fixed roles
(#106), not configurable groups:
- `provider`: their own patients, schedule, messages and visits.
- `office` (front desk): every patient and every provider's schedule and
  appointment requests (book, change, confirm), orders, the store, the
  website, adding patients and pairing their phones. No message threads
  (minimum necessary) and no staff management.
- `admin`: everything `office` has, plus setup: staff accounts and office
  settings.

 Provider accounts are invited from the Registry roster;
the Back Office holds the accounts and credentials.

As built (#38):
- Accounts are created on the Back Office host with `wehelpd staff add`.
  API self-registration is development-only.
- Admins add providers in the staff web (#100). Each new provider gets a
  single-use set-up link that works for 24 hours on the LAN and is stored
  only as a hash. The provider chooses their own password and enrolls TOTP
  from it. Admins disable and re-enable accounts; nothing is deleted.
- The Registry roster (#62) feeds the same invite. The Back Office pulls
  its practice's providers every 15 minutes (`roster_interval`) and on
  Sync now. It signs the request with the office SSH key and checks the
  answer against the Registry key pinned at install. A new provider waits
  on the Staff screen for the admin to invite them. One removed in the
  Registry is flagged for the admin to disable; the sync never disables or
  deletes an account. Admins on the roster are added with `wehelpd staff
  add`, as at install.
- The first sign-in enrolls TOTP. After that every sign-in needs the
  password and a code that hasn't been used before. Five failures lock the
  account for 15 minutes.
- Sessions are server-side, in a `__Host-` cookie (Secure, HttpOnly,
  SameSite=Strict), with a 30-minute idle and 12-hour absolute limit. Every
  change carries a CSRF token.
- The staff UI is HTTPS on `:8443` with a certificate from the office's
  local CA. Outside dev, plain HTTP only answers health checks and redirects.
- Staff routes, password sign-in and the web UI itself return 404 when they
  arrive on a Relay channel. The listener decides this, not a header.

## V1 features

| Feature | Where it lives | Notes |
|---|---|---|
| Appointments | Back Office | Patients see and request; staff and providers manage |
| Messages | Back Office | Patient ↔ practice threads; no attachments in V1 |
| Reminders | Back Office → Relay push | A one-minute polling worker; `reminders_sent` keys make each reminder send once; push payloads carry no PHI |
| Telehealth video | App ↔ app, media via Relay TURN | 1:1 visits; see below |
| Practice website | Relay (static files) | Published by the Back Office; no logins, forms or PHI. Library of podcast links and PDFs |
| Supply store | Back Office (orders), site (catalogue only) | Patients order in the app; staff fulfil on the LAN; no payments (#97) |
| Staff web UI | Back Office, LAN only | Schedule, messages, patients, pairing, devices |

**Not in V1:** patient access to their own health records (visit notes,
check-ins, HealthKit sync), a patient web portal, the in-office iPad kiosk,
the ledger API, FHIR, SSO, and cloud-hosted Back Offices.

### Telehealth

- 1:1 WebRTC video between the provider's app and the patient's app.
- **Signaling** (offer, answer, ICE) goes through the Back Office over each
  party's pinned connection, so the DTLS fingerprints that secure the media
  cannot be swapped by the Relay.
- **Media** is DTLS-SRTP, end to end between the two apps. It is forced
  through the Relay's TURN server (`iceTransportPolicy = relay`), so device
  IP addresses are not exposed and the path is predictable. TURN forwards
  ciphertext only.
- The Back Office mints short-lived TURN credentials per visit itself, from
  a key the Relay sends it when the tunnel connects. It never asks the Relay
  for anything. Nothing is recorded anywhere.
- As built (#6):
  - **Lifecycle:** any scheduled appointment becomes a visit when its
    provider starts one in the app, from 10 minutes before the start to 30
    minutes after the end. The patient gets a push and can join only after
    that.
  - **Who:** only the two of them, from the app, one device each.
  - **Signaling:** a long-poll held in the Back Office's memory, never
    stored or logged.
  - **Ending:** the provider ends the visit, or it closes itself at the end
    of the window. Ending doesn't change the appointment; staff mark it.
  - **Audit:** open, join, leave and end.
  - The client contract is in MOBILE_CLIENT.md §15.

### Push

APNs keys belong to the app publisher, so the Relay sends pushes. The Back
Office asks the Relay to notify a device token with one of a fixed set of
categories ("New message", "Appointment reminder", "Visit starting",
"Appointment request update", "New appointment request", "Supply order
update"). The
Relay rejects anything else. The app fetches the details through its pinned
connection.

As built (#7):
- The app registers its APNs token with each practice it's paired with,
  and the Back Office keeps it on the device record. **The Relay stores no
  tokens.**
- Each push is an SSH global request (`wehelp-push`) carrying the token,
  the app's APNs environment and the category. The Relay checks the
  category, supplies the fixed text itself, limits each office to 120
  pushes a minute, and logs only the office ID, category and result.
- Tokens are cleared when a device is revoked or when Apple reports them
  invalid.
- APNs is hand-rolled (HTTP/2 + ES256 JWT), per spike S3.

### Practice website, library and supply store

Staff fill in the Website tab: about us, hours and contact, provider bios,
services, how to get the app and join (download, then in-office QR
pairing), podcast links, and uploaded PDFs and images. The Back Office
renders six static pages with built-in templates (`html/template`) and
streams them as one tar over a `wehelp-site` channel on its SSH connection
to the Relay, which files them under the office ID that connection
authenticated as and serves them read-only at `/s/<office-id>/`. The
Relay allows only a few static file types, refuses HTML with scripts and
serves everything with a `default-src 'none'` CSP.

Provider bios come mainly from staff profiles (#102). Each staff member
edits their own name as shown, credentials, bio and photo under My profile,
and opts in to the website. Publishing turns those profiles into bios,
replacing any typed bio with the same name. Disabled accounts never appear.

The site also shows the practice's **supply catalogue**, but takes no
orders: an order is patient data. Patients order in the app through the
pinned tunnel; staff fulfil orders on the office LAN; payment is at pickup
or by invoice (#97).

Still to do (#5): serving each site by hostname with
`apple-app-site-association`, so "Book or manage appointments" links open
the app.

## Back Office internals

- **One Go binary** (`wehelpd`) plus **Postgres**, run with Docker Compose
  on the Mac mini. Migrations are Flyway (`db/migrations`). Jobs are River
  (a Postgres table). No other infrastructure.
- **Scale budget:** about 500 active patients and tens of staff, so tens of
  requests per second at peak. Prefer boring, inspectable code.
- **Invariants:**
  1. Every write that touches patient data runs in a transaction that also
     calls `audit.Record`.
  2. `audit_events` is hash-chained per tenant and immutable by trigger, and
     `audit.Verify` must pass.
  3. Ledger tables are append-only (kept from the PoC; no V1 API).
  4. Every table has `tenant_id`. A Back Office serves one practice, so one
     tenant, but the column stays.
  5. Configuration is via Viper (`WEHELP_*`). Secrets come from files with
     mode 600, never from the repo.
- **API:** `/api/v1`, described by an OpenAPI spec that CI validates. Every
  request resolves (tenant, user, role, device) before touching data.

## Relay internals

- **One Go binary** (`wehelp-relay`). The last good signed office list is
  cached as the signed envelope in one file (`office-list.json`) and
  re-verified on load. Push tokens (#7) will add a small SQLite file. No
  Postgres, and no import of any Back Office data package: a build-failing
  test enforces this. Runbook: [RELAY.md](RELAY.md).
- **Listeners:** CONNECT proxy (TLS), SSH (offices), TURN (UDP and TCP),
  HTTPS (websites).
- **Admin view:** Relay health (connected offices, tunnels, TURN load) is
  shown in the Registry console from the Relay's telemetry. Relays have no
  separate admin login: what they accept is controlled only through the
  signed office list.
- **Logs** hold connection metadata only (office ID, byte counts, timing),
  for at most 7 days. Device IP addresses are not logged.

## Registry internals

- **One Go binary** (`wehelp-registry`) plus Postgres, run by WeHelp. It
  holds business and workforce data only, and imports no Back Office data
  package; a build-failing test enforces this, as for the Relay.
- **Registry console:** a web UI for WeHelp support staff (password + TOTP,
  roles `support` and `registry-admin`). It covers:
  - registering and suspending practices;
  - managing providers and admin contacts;
  - issuing install tokens;
  - assigning Relays;
  - viewing Relay and office health.
- **Signing key:** ed25519, used only to sign office lists. In V1 it is a
  mode-600 file with a documented rotation procedure. Before field use with
  multiple practices, it moves to a hardware-backed key.
- **Audit:** every console action and every list publication is recorded in
  the Registry's own hash-chained audit log.
- **APIs:**
  - install (token-authenticated, single use);
  - office list (public, signed);
  - roster (office-key-authenticated);
  - console API (staff sessions).

## Service ownership

| Data | Owner | Copies |
|---|---|---|
| Patients, messages, appointments, visit records | Back Office | The patient's phone, while displayed |
| Staff accounts and credentials | Back Office | None |
| Practice, providers, subscription | Registry | Provider roster pulled by that practice's Back Office |
| Which offices may connect, and to which Relay | Registry | Signed office list cached by each Relay |
| Device keys | The phone (private key); Back Office (public key) | None |

## Observability

Each Back Office and Relay runs a Grafana Alloy collector as a sidecar.
Dashboards (Grafana, Loki, Prometheus) are an optional profile that can run
on the same host, elsewhere, or in WeHelp Cloud Dashboards. Two streams leave
a host: an **ops stream** with no identifiers, and an **audit stream** (the
`audit_events` chain, IDs only) sent only to a BAA-covered destination. A load
guard stops dashboards that starve a small host. Details are in
[OBSERVABILITY.md](OBSERVABILITY.md).

## Test and demo topologies

| Topology | Registry | Relay | Back Office | Data |
|---|---|---|---|---|
| **Dev** | Developer laptop | Developer laptop | Developer laptop (`make compose-up`) | Seed data |
| **Demo** (investors, practitioners) | Developer laptop | Developer laptop | Barnacle (Mac mini) | Synthetic demo practices only |
| **Linux test** (from 2026-11-16) | Barnacle | Barnacle | MiniZ (Ubuntu 24.04, Docker Engine) | Synthetic demo data only. No disk encryption; wiped and rebuilt after V1 |
| **Field** (the naturopath practice) | WeHelp production Registry | Production Relay | Practice Mac mini | Real patients, only after the V1 audit and pen test pass |

Demo and field never share a Registry, a Back Office, a Relay or keys.
