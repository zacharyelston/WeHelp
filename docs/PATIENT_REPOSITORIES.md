# Patient records as authoritative Git collections

Status: **V2 specification for review**, 2026-11-16. Owner decision:
**Git is the authoritative patient record.** This supersedes the initial
V2 notebook draft's Postgres-only content model. It does not change the
running V1 storage model before an explicit, verified practice migration.

Read with [PATIENT_NOTEBOOKS.md](PATIENT_NOTEBOOKS.md),
[V2_PLAN.md](V2_PLAN.md), [ARCHITECTURE.md](ARCHITECTURE.md) and
[SECURITY.md](SECURITY.md). Prior art and Git's transaction limitations
are recorded in [LIT_REVIEW.md](LIT_REVIEW.md).

**Owner, 2026-11-16:** the patient Git collections live in the Data
Science & Security module, not in `wehelpd` ([DATA_MODULE.md](DATA_MODULE.md)).
Under that boundary, appointments, requests and message events stay Back
Office database authorities. The `record` repository holds clinical
relationships, access facts and clinical content only.

Implementation epic #220: #221 collection/adapter, #222 audited publication
and recovery, #223 verified projections and #224 conversion/migration/restore.
Notebook epic #212 and questionnaire/health-data sources depend on that
foundation. These are specs under review, not deployed storage changes.

## Authority and location

A Patient is an aggregate with a stable opaque tenant/patient ID and a
versioned collection of local Git repositories. Clinical record content,
intake answers/scores, accepted readings, notebook definitions/narrative,
drafts and finalized review snapshots are authoritative Git objects.
Postgres clinical tables are derived projections for indexing, bounded
queries and compatibility, not a second writable record authority.

Postgres remains authoritative for staff/device credentials, security
sessions, the append-only audit chain and ledger, publication/workflow
coordination, operational state and non-patient configuration. Git does
not replace audit.Record, audit.Verify, ledger.Post or their pgx transactions.
Patient relationships and record-access scopes are patient-record facts;
their Postgres authorization indexes must be proven current before use.

All patient repos are module-owned, bare and local on the office appliance's
FileVault-protected data volume, included in encrypted backups. No remote,
GitHub/Azure push, sync service, git daemon or direct clone API is enabled.
The patient phone and staff UI access authorized APIs, never repository
filesystem paths or object endpoints. Private Git hosting in a cloud is
not approved by the owner choosing Git authority.

## Collection layout and source format

The layout uses opaque UUIDs, never patient names or contact details:

```text
patient-records/<tenant-id>/<patient-id>/
  manifest.git                      patient publication history
  repos/<opaque-repository-id>.git   component record/history
```

The manifest is itself an append-only Git history with one authoritative
`refs/heads/published`. Its `wehelp.patient.v1` document includes tenant,
patient, schema/object-format versions, parent publication, operation ID
and an immutable map of repository IDs to kinds, access-scope IDs and exact
component commit IDs. It contains no URLs or executable Git submodules.

Initial component kinds:

| Kind | Authoritative content |
|---|---|
| `record` | Clinical relationships/care teams, private status and clinical access facts; operational identity/contact, appointments/requests, communications and orders remain Back Office DB sources |
| `intake` | Exact published form definition used, response/page events, submission, authoritative score/flag results and explicit review events |
| `measurements` | Accepted readings/daily summaries, source/revision/withdrawal and sharing/monitoring facts |
| `notebook` | One provider-owned notebook's definitions, notes, run provenance, saved drafts and immutable reviewed output |

Kinds separate storage; they do not automatically grant a role access to
every record in a repo. For example, admin scheduling access cannot read
clinical access facts in `record`. Notebook repositories retain their initial
author-only policy; sharing needs a separate audience design.

Create component repos lazily, cap at 100 per patient initially and fail
explicitly at the limit; no silent repo merging or unbounded proliferation.
All repo/object identifiers are resolved from the trusted manifest, never
accepted as an arbitrary client path or Git revision expression.

Canonical content is versioned typed JSON, with tenant_id, patient_id,
record/resource ID, revision ID, operation ID and schema version. Clinical
facts append immutable revision/event documents at server-generated paths
such as `records/<kind>/<record-id>/revisions/<revision-id>.json`.
Corrections/withdrawals reference old revisions rather than overwrite them.
Preserve exact questionnaire/scoring versions and measurement revisions.
Existing notebook JSON stays data, never an executable notebook file.

Each published component also carries a versioned canonical current-record
catalogue mapping resource/record IDs to exact revision/object IDs. Updating
that catalogue changes the next Git tree; prior Git trees and immutable
revision documents remain intact. It supplies the complete expected record
membership for projection verification, not merely a timestamp/head marker.

Use SHA-256 Git object format for new patient repos, recorded explicitly
in the manifest; validate supported Git versions before enabling the backend.
Separate versioned SHA-256 canonical payload fingerprints and audit hashes
remain explicit. An object hash proves bytes, not actor authorization or
clinical truth. Commit metadata uses opaque actor/operation IDs and UTC,
never names, email addresses, narrative, tokens or PHI-bearing subjects.

## One publication point across repos

Git cannot atomically commit several repos together. Therefore component
refs are never the patient aggregate's publication authority: **only the
manifest's expected-old-object ref update publishes the new collection**.
Readers pin one manifest commit and load exactly its component commits.
One operation can atomically publish intake + notebook review changes by
advancing one manifest after both components are prepared.

There is one bounded module operation coordinator per patient, plus durable
patient/access conflict reservations. Back Office provider/day scheduling
reservations belong to the separate booking coordinator in DATA_MODULE.md. Existing `pg_advisory_xact_lock` helpers release
at a DB commit and cannot span this protocol. Each
publication has an idempotency key and expected old manifest ID. Competing
updates return 409/rebase at the application level; there is no automatic
text merge of clinical facts. A committed manifest must descend from the
previous published manifest; no history rewrite, force-reset or rebase API.

## Audited Git/Postgres publication protocol

Git filesystem writes cannot participate in pgx.Tx. The previous invariant
that all PHI lives in database transactions is replaced **for the migrated
V2 Git backend only** by this explicit recoverable protocol. V1 database
writes and append-only financial/security tables keep their existing rules.

1. **Validate in memory.** Authenticate/authorize, apply schema/size limits,
   check expected head and identify ordered patient/business conflict keys. Compute bounded
   canonical mutation hashes without writing PHI to disk or projecting it.
2. **Prepare, in a DB transaction.** Persist an operation ID, actor/device,
   expected manifest and candidate digests, durable exclusive conflict-key
   reservations/fencing generation, and audit `patient_record.prepare`.
   Commit before any patient repo/staging filesystem write. Failure means no
   Git write. The outbox contains IDs/hashes/state, never clinical payload.
3. **Stage immutable objects.** Through the sole record adapter, write
   component commits and candidate manifest under private staging refs,
   preserving published ancestry. Verify object existence/content/durability.
   Staging is attributable to the committed prepare and invisible to APIs.
4. **Record publish intent.** Commit exact old/new manifest and component IDs
   plus `patient_record.publish_intent` in Postgres/audit. Recheck permissions
   and live head under coordinated actor/patient reservations. If anything changed,
   refuse/recover without publishing; never trust stale authorization indexes.
5. **Publish Git.** Begin the module completion DB transaction. Recheck current
   canonical clinical grants under patient reservations; require the gateway
   fence ticket for access changes. Gateway credentials are admitted under
   DATA_MODULE.md's bounded request assertion; the module cannot lock staff
   rows in the other database. Verify durable
   operation ownership/fencing and reservations, then compare-and-swap
   `refs/heads/published` from expected old
   to candidate manifest using Git plumbing. Flush required object/ref writes
   before treating the publication as durable; a successful CAS alone is not
   a storage-durability proof. Source truth is now the manifest.
6. **Complete, in that DB transaction.** Apply derived projections tagged with
   that exact manifest, advance the indexed-head fence, append
   `patient_record.publish_complete` and complete the operation/idempotency
   record and release conflict reservations. Return success/release PHI or trigger external notifications only
   after this transaction commits. No parallel direct-to-projection writes.

This is a recoverable workflow, not claimed ACID across Git/Postgres. At no
stage does the service create PHI files without a durable audited intent.
A publication cannot become a completed client-visible clinical action
without durable audit completion and correct indexes.

## Recovery and failures

On startup and before serving an affected patient, reconcile nonterminal
operations with actual manifest heads. Unknown/unexplained heads, missing
objects, wrong ancestry or conflicting same-id/different-digest operations
fail closed and alert with IDs only.

| Failure point | Recovery |
|---|---|
| Before prepare commit | No objects/source write; safe to retry |
| After prepare, before Git publish | Published manifest is unchanged. Resume validated prepared work or append an abort outcome; staging never appears in normal reads |
| CAS conflict | Another manifest won. Append conflict/abort, release locks, require a new validated operation; no forced overwrite |
| Git publish succeeded, DB completion failed | Keep the Git publication; rebuild/reapply its projection idempotently and append completion audit. Do not roll back/ref-reset the patient history |
| Client lost completion response | Same idempotency key returns the completed publication; no duplicate clinical fact/review |
| Disk/fsync failure, corruption or lost intent | No successful response. Quarantine/fence affected patient writes and repair/restore with recorded evidence |

Staging-ref cleanup is an audited bounded maintenance operation. Published
objects/manifests and finalized reviews are never pruned by automatic GC.
An orphan/unpublished object is still PHI on disk, protected by the same
access/backup rules; retention/disposal policy must address it explicitly.

Git fsck checks object structure/hash integrity. Independently retained
audit anchors (#56) bind publication IDs/manifests; they are still needed
to detect rewriting local history after compromise. Git alone is not a
tamper-proof audit or signature system.

## Projections, queries and authorization

Every patient query projection includes source manifest/component IDs,
schema/projector version and progress state. Before use, verify it matches
the requested authoritative manifest. A stale, absent or corrupt projection
is rebuilt or returns `record_recovering`; never accept absence as an empty
clinical record. Scheduling availability remains a Back Office DB decision.

Matching head/version tags is necessary but **does not prove index content**.
For the first release, before querying a resource, derive its complete
current candidate record set/values from the pinned Git catalogue and typed
revision objects, stream-compare canonical values and membership with the
Postgres projection, and only then evaluate filters/aggregates. Verify IDs,
patient/tenant, field values, source revisions, counts and absence of extra
or missing rows. Reject unchanged-tag content corruption. Verification
itself has a 10,000-record/32 MiB input and request deadline budget; too-large
or unprovable resources return an explicit bounded error, never unchecked
query results. No persisted "verified=true" flag substitutes for this check.

Evaluate access-control facts directly from the currently published Git
record (plus verified gateway identity admission), not an unverified SQL
permission row. Do not reconstruct the Back Office directory or schedule from
clinical Git: they are independent operational DB authorities. Validate root
clinical collection membership during migration/restore independently of the
derived clinical index.
Faster authenticated projection proofs/caches require a separately reviewed
scheme; they are not assumed by this spec.

Rebuild uses published Git content only and deterministic source vectors.
Discarding/rebuilding patient projections must yield the same canonical
current facts/counts/permissions; it cannot discard security audit/ledger
or operator workflow state. Those database authorities need their own backup.

Notebook SQL runs only over verified projections through typed source store
adapters, not Git execution or arbitrary filesystem queries. Pin one patient
manifest plus consistent module projection snapshot for each run; record both
in provenance. Operational sources arrive as one separately captured gateway
batch per run (PATIENT_NOTEBOOKS.md). Recheck current published access facts
before returning/finalizing, even when source data is pinned historically.
An old Git commit must never resurrect a revoked access grant.

Durable module reservations are tenant/patient/access keys owned by operation
ID and fencing generation. They survive DB commits and restart; elapsed time
alone cannot release them. Reconcile actual Git publication/audit/projection
before terminal release or ownership transfer. Back Office booking holds its
own provider/day reservations while coordinating with the module; no clinical
Git projection is used as the source of appointment availability. Test both
sets independently and at their cross-service handoff (DATA_MODULE.md).

## Git adapter and deployment boundaries

The only clinical Git writer is the module record adapter. No browser/client
receives raw git, shell, clone, push, ref or object APIs. Use fixed executable
and argument vectors, allowlisted operations and server-derived paths;
never shell interpolation or client revision/path expressions.

Disable network protocols/remotes, hooks, external filters/diff/merge/credential
helpers, submodules, object alternates and inherited/system/user Git config.
No symlink/absolute/traversal path, executable payload, Git config or
`.gitmodules` content is imported. Disable automatic GC/background
maintenance; implement reviewed maintenance separately. Bound command
time/output/object/collection size and process concurrency; redact raw Git
stderr and paths before logging. Treat repo content as inert PHI, not code.

Config defaults in internal/config/config.go will add backend selection,
private record root and budgets. Field startup/doctor verifies directory
ownership/modes (0700; files 0600), supported Git/object format, writable
durable volume, encrypted-backup coverage, recovery state and projection
freshness. This adapter runs only within the separate local module specified in DATA_MODULE.md.
Pin/review the packaged Git binary and advisories before shipping; the
developer's installed version is not a claim of a safe field dependency.

## Migration, restore and rollout

Do not put a Git mirror next to a writable Postgres patient record and call
both authoritative. Enable `git` only at an explicit practice migration:

1. Take/verify encrypted backups of both DB authorities/keys. Quiesce clinical writes
   and background patient writers; record the DB source watermark/schema.
2. Export clinical records/access facts at that boundary, keeping stable IDs
   and current source relationships. Do not move operational records into Git.
   Build initial patient collections and
   audited baseline manifests through the publication adapter.
3. Record honestly that legacy rows provide a baseline current snapshot;
   do not invent historical revisions or pretend old audit detail contains
   all past clinical values. Preserve the original audit/ledger authorities.
4. Rebuild patient query projections from Git and compare canonical values,
   counts, ownership, source/scoring versions and role visibility against
   the export. Verify fsck, every manifest component and audit binding.
5. Activate the backend gate for the whole practice only after every
   clinical command/read path is converted and verification passes. Operational
   identity/contact, appointments/requests, communications and orders stay in
   the Back Office DB; privacy-gated paths adopt the confirmed access mirror. Older
   binaries/DB-only writers refuse a migrated installation. No silent
   fallback to stale pre-migration tables.

After the first Git-native write, a rollback must replay all new published
records into a verified compatible target or restore/roll forward; never
restart the old V1 snapshot and lose new care data. Rehearse on synthetic
500-patient data and measure downtime/disk/index rebuild costs before a
practice migration. This spec authorizes design/tests, not a real migration.

Encrypted backups include both DB authorities, all patient repos/staging intents,
manifest heads, keys, format/projector versions and a consistent recovery
boundary. A quiesced/coordinated snapshot is acceptable at this appliance
scale. Restore verifies Git objects + manifest references, audit anchors,
operation reconciliation and reconstructed projections before serving.
An old DB plus newer Git may require replay; missing published objects is
a failed restore, not an empty dataset. No unencrypted Git backup remote.

Retention/correction policy recognizes that Git retains old PHI: a logical
withdrawal is not erasure. No purge/history-rewrite/export function is added
until its retention, audit and backup implications are separately reviewed.

## Required acceptance evidence

- Two-patient/two-tenant fixtures prove collection isolation, stable IDs,
  published manifest resolution, immutable revisions and role-specific API
  visibility even when several resources share a component repo.
- Concurrent publications/CAS conflicts, process termination and injected
  DB/Git/fsync failures at every phase converge to one auditable outcome;
  no success, notification, PHI response or mixed-head read escapes early.
- Rebuild patient projections from Git only and compare golden facts and
  access scopes; deliberately stale indexes refuse clinical reads. Disabling
  a relationship mid-run prevents result/finalization at the canonical recheck.
  Gateway credential disable follows bounded admission in DATA_MODULE.md.
- Modify/remove/add clinical and permission projection
  rows while leaving all source-head/version tags unchanged. Verification
  must reject/rebuild before a clinical query or authorization uses them.
- Notebook finalize + intake-review events publish through one manifest;
  retry yields one immutable review, old review remains byte-identical.
- Corrupt/missing objects, changed history versus audit anchors, malicious
  repo config/path/hook/filter/import and fake object IDs fail closed without
  execution/network access or raw PHI logs.
- Full synthetic V1-to-Git migration, new V2 writes, backup/restore/recovery
  and measured 500-patient load prove no data loss and no dual-writer mode.

Each implementation uses forward-only migrations, OpenAPI/capability updates,
CHANGELOG and `make build vet test` with DB integration plus actual local Git
fault fixtures. V1 operation and its database-only invariants remain unchanged
until this design passes review and migration is explicitly performed.
