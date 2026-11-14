# WeHelp V1 architecture

WeHelp V1 connects a practice with its patients. The V1 practice is a
naturopath office. Patients use an iPhone app for appointments, messages and
telehealth visits. The practice runs its own server (the Back Office), a
Relay connects phones to it, and the WeHelp Registry, our corporate support
arm, decides which practices exist and which Relay serves them.

This document is the source of truth for V1. Security goals and the audit bar
are in [SECURITY.md](SECURITY.md); build order and scope are in
[V1_PLAN.md](V1_PLAN.md).

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
| **Provider app** (Mobile, #69) | The provider's own iPhone or iPad, in or out of the office | The same, with the provider role | The same |
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

### Registry → Relay: signed office list

- The Registry publishes an **office list**: office ID, office SSH public
  key, status (active or suspended) and assigned Relay, with a version
  number. It is signed with the Registry's ed25519 signing key.
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

These keys stay on the Back Office. FileVault and the backup encryption
protect them at rest.

### Patient onboarding: in-office QR pairing

1. Staff create or select the patient in the staff UI and press **Pair
   phone**.
2. The Back Office shows a QR code containing: the Relay address, the office
   ID, the SHA-256 of the office TLS public key, and a **single-use invite
   code** that expires in 10 minutes.
3. The patient scans it in the app. The app generates a Secure Enclave
   P-256 key, connects through the Relay, verifies the pinned office key,
   and enrolls by redeeming the invite with a signature from the new key.
4. From then on the device key is the patient's credential. Every session
   refresh is signed by it (device-bound sessions, already built). The app
   is unlocked with Face ID or the device passcode; patients have no
   password in V1.

Lost or replaced phone: staff revoke the device and pair the new phone the
same way. Providers pair their devices the same way, with the provider role,
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

Staff sign in to the staff UI with a password plus TOTP. Roles: `admin`
(office settings, users, pairing) and `provider` (patients, schedule,
messages, visits). Provider accounts are invited from the Registry roster;
the Back Office holds the accounts and credentials.

## V1 features

| Feature | Where it lives | Notes |
|---|---|---|
| Appointments | Back Office | Patients see and request; staff and providers manage |
| Messages | Back Office | Patient ↔ practice threads; no attachments in V1 |
| Reminders | Back Office → Relay push | Scheduled jobs (River); push payloads carry no PHI |
| Telehealth video | App ↔ app, media via Relay TURN | 1:1 visits; see below |
| Practice website | Relay (static files) | Published by the Back Office; no logins, forms or PHI |
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
- The Back Office mints short-lived TURN credentials per visit through the
  Relay. Nothing is recorded anywhere.

### Push

APNs keys belong to the app publisher, so the Relay sends pushes. The Back
Office asks the Relay to notify a device token with one of a fixed set of
categories ("New message", "Appointment reminder", "Visit starting"). The
Relay rejects anything else. The app fetches the details through its pinned
connection.

### Practice website

Staff fill in a Practice profile (name, providers, hours, address, phone,
logo). The Back Office renders a few built-in templates (`html/template`) and
publishes the static files to the Relay over SSH. The Relay serves them by
hostname with `apple-app-site-association`, so "Book or manage
appointments" links open the app.

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

- **One Go binary** (`wehelp-relay`) and a small SQLite file for the cached
  signed office list and push tokens. No Postgres, and no import of any Back
  Office data package: a build-failing test enforces this.
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
| **Field** (the naturopath practice) | WeHelp production Registry | Production Relay | Practice Mac mini | Real patients, only after the V1 audit and pen test pass |

Demo and field never share a Registry, a Back Office, a Relay or keys.
