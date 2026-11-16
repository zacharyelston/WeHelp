# The Data Science & Security module (V2)

Status: **design for review** (2026-11-16), V2. It sets the boundary that
[V2_PLAN.md](V2_PLAN.md), [PATIENT_REPOSITORIES.md](PATIENT_REPOSITORIES.md),
[PATIENT_NOTEBOOKS.md](PATIENT_NOTEBOOKS.md), the questionnaire design
(#192) and the health data design (#202) build on. V1 doesn't change.

## Decision (owner, 2026-11-16)

**The Back Office is for scheduling and daily administration.** Clinical
data and its analysis go in a separate **Data Science & Security module**.

The defaults below are the drafted answers to the open points: where it
runs, what it owns, and how the two talk. They're for review, not decided.

## What each side owns

| | Back Office (`wehelpd`) | Data module (`wehelp-data`) |
|---|---|---|
| **Purpose** | Scheduling and the daily running of the practice | Clinical records, their analysis, and who may see them |
| **Identity** | Staff accounts, patient accounts, device keys, sessions, the Relay tunnel, the Registry roster | None of its own. It trusts the Back Office's signed identity assertion (below) |
| **Patient data** | Name, contact details and what scheduling needs | Clinical content |
| **Features** | Appointments, requests, availability, reminders, messages, push, video, website, library, store, ledger | Questionnaire forms, assignments, responses and scores; monitoring plans, sharing choices and measurements; notebooks and finalized reviews |
| **Storage** | Its Postgres database, as in V1 | The patient Git collections, plus its own Postgres database for verified indexes and workflow |
| **Access decisions** | Role checks for scheduling and admin work | **The authority for clinical access**: provider–patient links, care teams, private patients (#226), notebook ownership and, later, per-patient keys |
| **Audit** | Its own `audit_events` chain, as in V1 | Its own chain, in the same format |

- **The "security" half** is the access authority. Every clinical read,
  write, notebook run and finalization is decided and audited inside the
  module. The Back Office never decides clinical access.
- **Messages stay in the Back Office.** They're communication, not a
  clinical record. For a private patient, though, the Back Office asks the
  module's access decision before showing a message body (see "Access
  mirror").
- **What changes in PATIENT_REPOSITORIES.md:**
  - the `record` repository holds clinical relationships, access facts and
    clinical content only;
  - appointments, requests and message events stay Back Office database
    authorities, not Git;
  - so the rollout (#224) no longer converts scheduling or messaging paths.

## Where it runs

- **A second binary on the same office Mac mini,** next to `wehelpd`. It's a
  separate Compose service, like `wehelp-relay` and `wehelp-registry` are
  separate binaries.
  - **Why the same host:** the practice still has one appliance, one
    FileVault volume, one backup and one doctor. The scale budget (about
    500 patients) doesn't need a second machine.
  - **Why a separate process:** the Back Office has the most network-facing
    code (the tunnel, the staff web, the store). A bug there shouldn't give
    direct access to clinical files or tables.
- **Its own database and role** in the same Postgres server, for example
  `wehelp_data`. The Back Office's database role has no grants there, and
  the module's role has none on the Back Office database.
- **The patient Git collections** are mounted only in the module's container.
- **Optional.** With V2 clinical features off, the module isn't running and
  V1 behaves exactly as today. Clinical routes answer `feature_unavailable`.

## How they talk

```
 iPhone app ══ pinned TLS via Relay ══► Back Office ──mTLS, loopback──► Data module
 staff web  ── office LAN HTTPS ─────►  (authenticates,                (decides access,
                                         forwards clinical              stores, audits)
                                         routes, stores nothing)
```

- **Phones and the staff web reach only the Back Office.** The pinned
  connection through the Relay, and the office-LAN staff web, don't change.
  Clinical routes (`/me/measurements`, forms, notebooks) keep their paths in
  `api/openapi.yaml`.
- **The Back Office forwards clinical routes.** It authenticates the
  request, then forwards it to the module with an **identity assertion**:
  - the tenant, actor, role, device and request ID;
  - signed by the Back Office with a key the module pins;
  - valid for one request, for a few seconds.

  The module checks the assertion, then makes its own access decision.
- **The Back Office stores nothing clinical.** Forwarded bodies pass
  through memory only: no logs, caches or database rows. The module's
  responses use `Cache-Control: no-store`.
- **The connection is mutual TLS on loopback** (or the Compose network).
  Certificates come from the office CA that already signs the staff HTTPS
  certificate. Only the Back Office's client certificate is accepted.
- **The module calls the Back Office** for scheduling facts it needs, such
  as whether an appointment exists, over the same mTLS link. The module
  never opens connections outside the host. The Back Office stays passive
  toward the Relay.

## Access mirror

Some Back Office screens need a clinical access answer: message bodies for
a private patient, and booking an unlinked provider (#226). Asking the
module for each of these would make messaging fail whenever the module is
down. Instead:

- **Every access change goes through the Back Office,** because phones and
  staff reach the module only through it. When the module accepts a change
  (a link, a revocation, private on or off), its response carries the new
  access facts and an **access version** per patient.
- **The Back Office keeps a mirror** of those facts: links, the private
  flag and the version. It's a projection, not an authority, and it's
  written only from module responses.
- **On start, and after any failed forward,** the Back Office compares
  versions with the module. Until they match, it shows no message bodies
  for patients whose version it can't confirm, and it refuses bookings that
  would create a link.
- **If the module is down,** nothing can change access, so the mirror is
  still current. Scheduling and messaging keep working; clinical panels show
  "unavailable".

## Audit and operations

- **Two audit chains,** one per database, both hash-chained and verified.
  A forwarded request carries one request ID in both chains, so a reviewer
  can follow it across.
- **The audit stream** (OBSERVABILITY.md) exports both chains to the same
  BAA-covered destination.
- **`wehelpd doctor`** adds checks:
  - the module is reachable over mTLS;
  - its audit chain verifies;
  - the access mirror matches;
  - the Git volume is on the FileVault volume and in the backup.
- **Backups** take both databases and the Git collections in one
  consistent snapshot, as PATIENT_REPOSITORIES.md already requires.
- **Logs:** the module follows the V1 logging rule (#167). IDs only, no
  values.

## Later: per-patient keys

The module is where per-patient keys would live (HEALTH_DATA.md, "Later:
per-patient keys"). The Back Office never handles clinical keys or
plaintext beyond forwarding. With keys, even a full compromise of the Back
Office process wouldn't read stored clinical content.

## What changes in the V2 specs

| Spec | Change |
|---|---|
| PATIENT_REPOSITORIES.md, #220–#224 | Runs in the module. `record` narrows to clinical content and access facts; scheduling and messages stay Back Office DB authorities |
| PATIENT_NOTEBOOKS.md, #212–#219 | Runs in the module. "Reuse the Go Back Office" becomes "reuse the module's Go code and the staff web through the Back Office" |
| Questionnaires (#192), #193–#199 | Forms, responses and scoring in the module. Reminders (`form_due`) stay in the Back Office, triggered by the module |
| Health data (#202), #203–#206 | Measurements, plans and sharing in the module. Phones reach them through the Back Office |
| Private patients, #226 | Decided in the module. The Back Office uses the access mirror for message bodies and booking |
| AGENTS.md, ARCHITECTURE.md | The new service and its invariants |

## Open questions

- **The name** of the binary (`wehelp-data` is a placeholder).
- **Links made by booking** (#185): with the module as the access
  authority, admin booking asks the module to create the link. Is that
  acceptable latency for the front desk? It's one loopback call.
- **Kiosk responses** (#200) go to the module through the Back Office like
  the app. The kiosk's own trust design is still separate.
- **A second host later:** the mTLS boundary would allow the module on its
  own machine, but that's not proposed.

## Order of work

1. **The module skeleton:** binary, Compose service, its database and
   Flyway folder, the mTLS link, identity assertions, its audit chain,
   doctor checks and backup coverage. No clinical features.
2. **Access authority and mirror,** with links moved into the module and
   #226 private patients.
3. Then the existing V2 order (V2_PLAN.md), with each feature built in the
   module.
