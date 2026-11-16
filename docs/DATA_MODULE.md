# Data Science & Security module: V2 service contract

Status: implementation specification for cross-review, 2026-11-16. The owner
placed structured clinical data/analysis in a separate module. This document
settles the first-release placement and protocol; [V2_CONTRACTS.md](V2_CONTRACTS.md)
defines actor, intake and source contracts. V1 remains unchanged until migration.

## Placement, authority and trust

Use `wehelp-data`: a second Go binary/Compose service on the same office Mac
mini, one practice/appliance. Reuse the existing Postgres server with a separate
`wehelp_data` database, migration owner, restricted runtime role, audit chain
and module-only patient Git volume. No additional host, broker, model runtime,
public listener, Kubernetes or external clinical service.

| Authority | Back Office `wehelpd` | `wehelp-data` |
|---|---|---|
| Identity/security | Staff/patient/device credentials, sessions, enabled roles, admission to Relay/LAN routes | Trusts verified gateway assertions; owns resource authorization, not login |
| Clinical source | None of the new structured clinical source records | Local patient Git: clinical relationships/access facts, forms/assignments/responses/scores, plans/sharing/readings, notebooks/reviews |
| Operational source | Patient directory/contact identity, appointments/requests, communications, visits, store/orders, ledger | Verified clinical projections/workflow and bounded copies explicitly saved in clinical reviews |
| Access | Existing operational permissions plus confirmed module access mirror for gated fields/actions | Current canonical Git clinical grants/private state, source-specific policy, notebook ownership |
| Security evidence | Its DB audit chain and operational coordination | Its DB audit chain and clinical Git publication coordination |

**Ownership is not data classification.** Messages and request reasons can
contain clinical PHI even though the Back Office owns their communication/
scheduling records. Both services protect PHI; the gateway forwards structured
clinical payloads in memory without logging/caching/persisting them. Do not
claim the Back Office contains no clinical information merely because structured
records moved to the module.

The gateway is a trusted identity authority and plaintext forwarding endpoint.
Process/DB/filesystem separation reduces direct storage access; it does not
stop a fully compromised gateway from impersonating an asserted actor or
observing forwarded plaintext. Stronger client-bound encryption/authentication
is a separate future threat model. Per-patient keys are not a V2 feature or a
claim of protection against root/compromised gateway while server-side analysis
and plaintext forwarding remain.

Revoke PUBLIC CONNECT to the module DB, deny cross-service runtime grants,
keep schema owners NOLOGIN during serving and deny mutation of audit tables.
Only the module mounts clinical Git; neither runtime gets Docker socket,
privileged host access or the other's private keys. A host administrator and
the shared Postgres owner remain trusted operators, not isolated adversaries.

## Modes and network

`clinical_backend=legacy` is an unmigrated V1 office. `clinical_backend=module`
is an explicitly migrated office; it never falls back to legacy clinical
tables or ignores private grants because the module is unavailable. Turning
forms/health/notebooks feature switches off hides those features, not the
already-adopted access authority. Module endpoints are private Compose-network
or host-loopback only. TLS 1.3 mutual authentication is required in either case.

The office CA issues distinct server/client certificates with appropriate EKUs
for each direction. Pin allowed service public keys/issuer/purpose separately
from browser staff HTTPS keys. Implement expiry/rotation overlap and refusal
tests; do not accept any certificate merely because the office CA signed it.

Phones still use pinned TLS through Relay to the Back Office. Staff use LAN
HTTPS. Clinical `/api/v1` routes retain their external paths and are forwarded
only after existing authentication/origin/CSRF/body-limit checks. Kiosk is an
office-device LAN exception specified in V2_CONTRACTS; it cannot become a direct
LAN patient-phone path or a public module listener.

## Per-request assertion

Use existing Go JWT support, **EdDSA only**, with separate per-direction signing
keys pinned by receiver. This is an internal request-assertion protocol, not
claimed OAuth DPoP compatibility. Header `typ=wehelp-service-request+jwt` and
`kid`; claims:

- fixed issuer/audience (`backoffice`/`data` in the appropriate direction),
  installation epoch, tenant, actor account/role, device or browser-session ID,
  request/operation IDs, and origin (`lan_staff`, `relay_device`, `lan_kiosk`,
  `office_service`);
- `iat`, `nbf`, `exp` (maximum 30 seconds, clock skew maximum 5 seconds), unique
  128-bit `jti`, uppercase method, exact normalized escaped path, SHA-256 of
  canonical query parameters and SHA-256 of exact forwarded body bytes;
- an explicit allowed purpose (`clinical_request`, `operational_facts`,
  `source_authorization`, `access_fence`, `access_snapshot`, `booking_receipt`,
  `reminder_delivery`), never a free scope. Receiver maps each purpose to a
  fixed direction, method and endpoint allowlist; a signed purpose is not a
  general permission to call another route.

Receiver validates certificate, key/type/algorithm, issuer/audience/epoch,
times, purpose, method/target/body binding, tenant and origin/role eligibility.
Reject duplicate/ambiguous query parameters and traversal before signing.
Strip client-supplied assertion/identity headers and generate them from the
authenticated server context. Bound request bodies before hashing/buffering.

Durably consume `jti` with unique constraint before execution; retain nonce
until expiry plus skew. A retry uses a new assertion/jti and the same operation
idempotency key/digest. A replay cannot create a second clinical effect; a
completed matching operation returns its original result. Clock uncertainty,
nonce-store failure, wrong epoch/audience or unknown key fails closed.

Requests admitted interactively have a 30-second maximum execution budget
(notebook runs retain their shorter 10-second deadline). Recovery of a durable
operation is a new authenticated idempotent request, not reuse of expired
assertions. Module-owned scheduled clinical work uses a fixed local service
actor/command allowlist with audit; it cannot issue user assertions or bypass
gateway access-change fences.

Durable receipts are distinct from request assertions. Use EdDSA JWS/JWT
`typ=wehelp-service-proof+jwt`, pinned per-direction key, fixed issuer/audience,
epoch/tenant/patient, proof kind, operation/run ID, fence generation where
applicable, exact payload SHA-256 and issue/expiry times. The signed payload
contains versioned typed facts; reject duplicate JSON keys, unknown fields,
ambiguous encodings and wrong proof kind. Bind the exact bytes/digest rather
than reserializing arbitrary JSON for signature verification. Never accept a
receipt as a user/request credential. Fence tickets and provisional-booking
receipts live <=30 seconds and are reissued only for the same reconciled
durable operation; access snapshots live <=5 minutes; notebook source-scope
proofs live <=10 seconds. Expired proofs require refresh, not silent reuse.
Replay of a bound durable receipt can recover only that same idempotent
operation; it cannot activate a new grant, clear a newer fence or authorize
another run. Clinical grant/head and phase checks still apply.

Canonical request target: require an absolute escaped path without dot,
encoded slash/backslash or duplicate slash segments. Query keys are unique,
decoded exactly once as UTF-8, sorted by key bytes, then percent-encoded using
RFC 3986 unreserved characters with uppercase hex and `%20` for space. Reject
invalid percent/UTF-8 encoding; never reinterpret `+` as space at one side
only. Hash the empty byte string for absent query/body. Implement shared
request-binding vectors before proxy routes; matching signatures over two
different interpretations are a contract failure.

Admission is the credential-revocation boundary: disabling a staff/device
account bars new gateway admissions immediately. An already admitted bounded
operation may finish; no globally instantaneous revocation across two DBs is
claimed. Module-owned clinical access is rechecked at publication/result release
against current Git; patient-head CAS/fences prevent a revoked clinical grant
from authorizing a later publication. The module cannot lock gateway DB rows.

## Access mirror with durable fencing

Back Office stores only signed minimal access facts: tenant/patient, current
module installation epoch, access revision + source manifest, linked provider
IDs, private flag, expiring-grant boundaries and signature. No answers, scores,
plan notes or notebook narrative. States are `unknown`, `fenced`, `confirmed`.

Every access-changing command (private toggle, link/care-team approval/revoke,
clinical audience change and approved maintenance) follows this sequence:

1. Gateway transaction durably sets that patient's mirror `fenced`, records
   operation/body digest/expected access version and audits before forwarding.
   New gated body reads/bookings are refused from this point. Reads admitted
   before fencing may finish; no promise to retract already released data.
2. Gateway issues a signed fence ticket bound to that operation/patient/digest
   and expected version. Module refuses access publication without a valid
   durable fence ticket. Internal recovery/maintenance cannot bypass it.
3. Module executes Git prepare/publish/complete and returns a signed complete
   access snapshot plus operation outcome. A lost response is an unknown result,
   never a reason to restore the old confirmed state.
4. Gateway verifies epoch/version/source/payload/operation, persists the whole
   mirror snapshot and outcome in one audited transaction, then marks confirmed.
   Only after this commit is the client operation acknowledged. Local mirror
   write failure keeps the durable fence. Replays are idempotent.

Serialize access changes per patient with a durable gateway operation owner
and monotonically increasing fence generation. Tickets, module outcomes and
mirror completion bind that generation; completion/renewal uses DB CAS and
cannot clear another operation's fence. Background renewal updates confirmed
state only when no mutation is pending. A valid old snapshot cannot undo a
newer private toggle/revocation. Patient grant mutations share the module's
patient reservation/CAS coordinator with ordinary clinical publications.

On gateway startup, set all access mirrors unknown until module handshake and
reconciliation. Pending operations remain fenced across restarts/timeouts.
Module reinstall/restore requires a coordinated global maintenance fence and
fresh epoch reconciliation, not automatic acceptance of a lower version.

A confirmed unfenced snapshot is renewed while healthy, every 30 seconds, with
a signed maximum offline age of 5 minutes and explicit grant expirations.
During module outage, existing operational actions may use it only until that
deadline, with current gateway credentials and base resource checks. Unknown,
fenced, expired, wrong-epoch or clock-uncertain state refuses gated content and
link-creating actions (`503 clinical_access_unavailable`). Names/contact/basic
schedule remain available. No assertion that module-down makes a cache current.

Module clinical access commands cannot publish outside this fenced path; no
background task silently alters grants. All expiry conditions are checked by
the gateway as well as the module. Future independently changing authority
would require a different invalidation/lease protocol before it can ship.

## Booking and provider-link coordination

The old `linkForBooking` DB transaction cannot span module Git. Availability,
appointment rows and provider/day durable reservations stay Back Office
authorities. A proposed clinical link stays non-authorizing until finalized.

| Stage | Durable state and permitted outcome |
|---|---|
| Prepare | Gateway reserves old/new provider/day conflict keys, writes booking intent + audit and fences affected access before proposing a new link |
| Validate access | Module validates current private/link rules and expected access revision; returns idempotent pending proposal. Private + unlinked returns 409 and creates no active link |
| Persist provisional appointment | Gateway records `pending_access` appointment/intent + audit; hidden from normal schedule but conflicts count as reserved. It returns no success yet |
| Activate link | Module requires signed proof of the exact durable provisional booking, rechecks current grant/private state and publishes activation through Git. No provisional booking means no active grant |
| Complete | Gateway persists current signed access mirror + scheduled appointment/outcome + audit and releases reservation, then acknowledges |

No clinical access arises from a pending link/proposal. If private/revocation
changes win before activation, refuse activation and durably cancel the
provisional booking; never reactivate a revoked grant on retry. If activation
commits and the response/completion is lost, recovery normally completes the
same durable booking, rather than silently deleting an authorized link. A
later patient revocation wins over completion and the provisional appointment
is cancelled without resurrecting access. Cancellation of an already completed
appointment does not automatically revoke a previously valid provider link.

Timeout/lease expiry does not release a slot or fence until the other side's
durable outcome is reconciled. Idempotency key + digest binds every phase;
client retries return one outcome. Non-link-changing bookings may use a current
confirmed mirror during its offline window; new-link bookings cannot succeed
offline. Test privacy-on/revoke racing each phase, both service restarts and
all response-loss/DB failure boundaries. Loopback latency is a measurement,
not the correctness question; target ordinary healthy booking p95 <500 ms on
the synthetic appliance and retain a bounded retry/error UI if exceeded.

## Cross-service reads and events

Module reads operational appointment/request/message-activity facts only through
one allowlisted mTLS gateway endpoint, not cross-DB grants. It forwards the
original scoped actor/patient purpose and bounded typed plan. Gateway applies
its own field/thread permissions plus the mirror and audits its read. No message
bodies enter notebook datasets. Metadata may contain PHI; both sides keep it
out of logs and persistent caches. Callback endpoints never synchronously call
back into the module or require a lock held by the originating request.

One batch returns a consistent gateway DB snapshot with source epoch, capture
time, record IDs/versions/digests and limits. Clinical Git inputs pin one module
manifest. **These are consistent per-source snapshots, not one distributed
snapshot.** Notebook provenance displays both capture times/versions; a
finalized review explicitly retains the exact authorized output. It is not
source authority for an appointment or message. Module-owned source datasets
remain reconstructible from clinical Git; operational data is not fabricated
when the gateway is unavailable.

PATIENT_NOTEBOOKS.md defines the concrete one-batch-per-run envelope and
authorization-only rechecks for captured scopes. A value-changing operational
write does not invalidate an explicitly dated saved review; a permission
change can prevent its read/finalize. No callback claims to prove that all
source values are still latest when a module Git commit publishes.

The module's audited Git completion writes a durable `form_due` outbox event
with opaque event/patient IDs and fixed category only. Gateway polls/accepts
idempotently into its durable queue and acknowledges only after that commit.
APNs delivery may retry/duplicate; no exactly-once claim. Tokens and Relay/APNs
credentials remain only in the gateway. No form names, scores or clinical
details enter a push, event log or operational metric.

## Audit, operations and migration

Both DB audit chains use the same hash rules but distinct source kind/epoch/
chain IDs and cursors under #56. Each side audits its own decision/read/write;
shared operation/request IDs correlate them without raw data. Backups capture
both DB authorities, clinical Git and keys at one quiesced/reconciled boundary.
Restore verifies both chains, objects, publication intents, mirror epochs and
projection content before reopening. Doctor distinguishes ordinary module
unavailable from a required failed migrated clinical authority.

Clinical migration moves links/care-team/private facts, intake/readings and
notebooks into the module. Patient identity, scheduling, communications,
store/order/ledger histories remain Back Office sources. Update #224's command
inventory accordingly; do not migrate scheduling/message bodies into clinical
Git. Preserve stable IDs and original audit chains. Fence legacy writers and
mirrors before activation; no double authority or rollback to stale V1 data.

## Implementation order and acceptance

1. Module bootstrap, runtime DB boundary, private volume, mTLS/assertions/replay.
2. Git collection/publication/recovery (#221/#222), then canonical access
   policy/private patients and durable gateway mirror fencing. Adapters/UI
   may progress against fixtures, not enabled in field builds.
3. Verified clinical projections (#223), booking/link coordinator, operational
   facts and event outbox. Migrate/rehearse only after these pass.
4. Questionnaire, health and notebook slices per [V2_PLAN.md](V2_PLAN.md).

Each boundary is a scoped task with DB/Git integration and two-process fault
fixtures. Required cases: wrong-origin/assertion/replay; private patient own
flows; mirror write failure/timeout/restore; unchanged-tag content corruption;
booking conflict/response loss; no callback deadlock; bounded outage behavior;
cross-source provenance; no PHI leakage; coordinated restore. Developer-ready
means those choices/tests are specified, not that they have passed in code.
Cross-review and the owner's merge/release gates remain mandatory.
