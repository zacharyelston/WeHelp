# Patient notebooks: V2 intake processing and review

Status: **specification for review**, 2026-11-16. The owner placed notebooks
in V2 alongside questionnaires/intake and patient health data. Nothing here
is implemented or required for V1. Work order is in [V2_PLAN.md](V2_PLAN.md).

Subsequent owner decision: **Git is the authoritative patient record**.
[PATIENT_REPOSITORIES.md](PATIENT_REPOSITORIES.md) defines each patient's
local repository collection, publication and migration. Notebook definitions,
narrative and reviewed snapshots are authoritative Git content; Postgres
provides verified rebuildable query projections and audit/workflow state.
This supersedes the first draft's Postgres-only content storage.

Later owner decision (2026-11-16): notebooks run in the **Data Science &
Security module**, not in `wehelpd` ([DATA_MODULE.md](DATA_MODULE.md)). The
staff web reaches them through the Back Office, which forwards the routes
below. The module owns clinical Git/projections/review. Operational facts
remain Back Office DB sources and are read through its scoped batch API.
See [V2_CONTRACTS.md](V2_CONTRACTS.md) for actor/source and client contracts.

## Purpose and references

Questionnaires collect structured intake; the module validates and
scores submitted answers. A patient notebook lets an authorized provider
ask questions of that record, inspect quality/limitations, compare results
over time and retain a reviewed interpretation with its provenance.

The primary workflow reference is the updated [notebooks service](https://dev.azure.com/ancerallc/ontology/_git/notebooks?version=GBfeat%2Fagent)
at `15a1d3ea77324a8556cdc8721ef3473911908294`, pulled on 2026-11-16:
typed query specifications, scoped data access, interactive notebook cells,
report dates and draft/regenerate/lock behavior. Roost's examples at
`db573feae45eea99047e7e6d08021f029cc854c0` provide profiling, named evidence
and limitation patterns. See [LIT_REVIEW.md](LIT_REVIEW.md).

We reuse those concepts in the Go module, local patient repos, verified
Postgres query projections and LAN staff
web. We do not install that service, execute its Python, copy its licensed
source, store PHI in GitHub or send prompts to its cloud agent. No new
infrastructure or model dependency is introduced.

Questionnaire source design is [PR #192](https://github.com/zacharyelston/WeHelp/pull/192)
and epic #201; health-data source design is [PR #202](https://github.com/zacharyelston/WeHelp/pull/202)
and epic #207. This integration includes their documents and resolves kiosk
transport in V2_CONTRACTS.md. Clinical flag response policy remains an owner
field-release gate, not a notebook decision.

## First V2 release

- A **Notebooks** tab on one patient's staff record, for a currently linked
  provider. Start with a bundled template or an empty notebook.
- Plain-text notes, typed controls, query cells, bounded scalar calculations,
  tables and line/bar charts. A visual query builder exposes a documented
  JSON plan; there is no SQL/Python/JavaScript editor.
- Datasets: submitted questionnaire answers/scores, assignment completion,
  shared measurements, appointment/request history and authorized message
  activity counts. Message bodies and cross-patient cohorts are excluded.
- Explicit Run/Refresh, visible data time/window, source versions, missing
  data and limitations. Changing controls marks dependent outputs stale;
  a bounded debounced run may refresh them only while the view is open.
- A review draft generated from one successful run; Regenerate creates a
  new draft revision. **Finalize review** creates an immutable internal
  record snapshot. No email, download, external publication or scheduled run.

## Authorization and PHI boundaries

Notebooks initially belong to their creating provider and one patient.
Every list/read/edit/run/draft/finalize request requires an enabled provider,
current patient relationship and notebook ownership. Another linked
provider cannot read it. Front desk/admin patient-list access does not
grant access, and an admin cannot impersonate the author. An account with
a provider role may still use only its own source permissions.

This initial provider-owned policy avoids treating workspace membership as
permission to see a saved narrative that may quote restricted sources.
Shared notebooks need a separate audience/redaction design. This policy
does not silently change the direct questionnaire/measurement API's access
rules; each source issue must state and test its own rules.

Within a notebook, **each resource and field is authorized on every run**
using the source store's current rules. A patient relationship alone never
grants every thread or dataset. Check the feature switch, patient link,
care-team/thread scope and permitted fields at start, and recheck current
committed clinical permissions before releasing results or finalizing a snapshot.
Revocation during a run discards its outputs. Historical run ownership
does not preserve access after relationship revocation.

All definitions, patient-specific query parameters, notes, drafts, snapshots
and provenance are PHI. Their authoritative record is in the office appliance's
patient Git collection mounted only in the module; query/index copies stay in module Postgres, all covered
by coordinated encrypted backups. The browser holds them only while displayed, with
`Cache-Control: no-store`, no localStorage/IndexedDB/service-worker cache,
no third-party scripts/fonts/telemetry and session/account-switch cleanup.
Relay, Registry, public sites, remote Git hosts and dashboards receive none of it.

Record writes use PATIENT_REPOSITORIES.md's audited prepare/stage/publish/
complete protocol; Git cannot participate in pgx.Tx. Reads/runs buffer PHI
until read-audit and run metadata commit and the pinned projection is verified;
audit failure withholds output. No direct projection write is a record edit.
Audit metadata contains IDs, versions, dataset keys and bounded outcomes,
never query literals, narrative, answers, scores, chart values or source
text. Numeric scores are PHI too. Operational logs contain no patient,
actor/device identifiers or data values; use the V1 logging boundary #167.

## Definition format: `wehelp.notebook.v1`

Definitions are JSON data. Built-in templates are reviewed product assets
without patient values; patient-specific definitions are never exported as
templates. Save publishes an immutable revision in the patient notebook repo,
referenced by the patient manifest, not an executable source file.

| Cell kind | Contents and allowed dependencies |
|---|---|
| `note` | Plain text, at most 10,000 code points; author/time attributed by server |
| `input` | Date range, catalogue choice or bounded numeric input; no expressions |
| `query` | Typed resource/filter/select/aggregate plan; input references only |
| `calculation` | Finite scalar arithmetic over named scalar query/calculation outputs |
| `table` | Explicit columns of one query result, with stable field IDs |
| `chart` | Line/bar view of one query result with typed x/y/series fields |

Limits: 256 KiB UTF-8 definition, 50 cells, 20 query cells, unique stable
cell IDs and a directed acyclic dependency graph. Scalar expression trees
have at most 32 nodes/depth 8; only add/subtract/multiply/divide are allowed.
Null propagates; division by zero is null with a visible limitation. There
is no string evaluation, code import, function registry or cell side effect.
Reject unknown keys/operators/types, dangling references and cycles before
reading any source rows. Literal strings are limited to 256 code points;
catalogue IDs, not arbitrary identifiers, select resources and fields.

Each query has `resource`, a typed `window` input reference, `filters`,
`select` OR `measures`/`dimensions`, `order_by` and `limit`.
Filters allow eq/ne/lt/le/gt/ge/in/is_null on fields
the catalogue permits, with at most 20 clauses and 100 values per `in`.
Measures are count/count_distinct/sum/avg/min/max; dimensions permit only
allowlisted categories and day/week/month buckets. No joins, subqueries,
raw predicates, filesystem paths, URLs, extensions or arbitrary SQL names.

The server derives tenant, patient, author and source scope from the route
and authenticated identity. Client plans cannot override these using a
filter or a hidden control. Fixed code maps catalogue identifiers to
parameterized pgx store queries. Read-only query cells cannot modify the
source record, send messages, assign forms or acknowledge clinical flags.

Synthetic examples and contract cases are in
[notebooks/README.md](notebooks/README.md). No complete validator is claimed
by the example; the format task must ship schema, Go validation and vectors.

## Dataset contracts

| Resource | Data and invariants |
|---|---|
| `questionnaire_answers` | Submitted response ID, exact form/version/item ID, type, answer, submitted time/channel; hidden/discarded items excluded; drafts unavailable |
| `questionnaire_scores` | Exact response/form version/instrument key, score ID, authoritative value, unit, band, scoring-version/missing-policy status and unresolved flags |
| `form_completion` | Assignments/occurrences, opens/due/closes, completed/missed/open status; distinguish not assigned, not due, unanswered and expired |
| `measurements` | Current non-withdrawn revision per source reading/day, type, units, effective time/local day, received time, source/revision IDs and coverage |
| `appointments`, `appointment_requests` | Existing authorized history/status/times; patient filter cannot select another patient's rows |
| `message_activity` | Counts/timestamps of only threads the author may read; no message text, free-text metadata or unauthorized count leakage |

Expose resource availability from enabled implemented features. Unsupported
datasets return `dataset_unavailable`, never fabricated empty results. Do
not implement duplicate questionnaire scoring in notebooks: #193/#197/#199
are authoritative. Keep different instrument/form/scoring versions in
separate series by default; cross-version comparison requires a reviewed
explicit mapping, outside this release. Labels alone never establish
equivalence. Free-text answers support display, not numeric aggregation.

Measurement adapters reuse #203/#204 authorization and revision semantics.
Separate raw readings from daily summaries; never sum steps twice across
revisions or treat multiple samples as daily summaries. Convert only
catalogue-supported units; split incompatible units/types. BP components
are distinct series. Missing days/answers remain missing, not zero or
interpolated. Sharing-off stops new intake; it does not erase already
retained authorized records or bypass future access checks.

## Execution, time and provenance

One explicit run resolves relative windows against a server-selected
`as_of` instant in the office IANA time zone. Absolute intervals are
inclusive-from/exclusive-to. Display both effective-data window and
ingestion cutoff. New source rows/revisions committed after the run's
respective source capture cannot appear partway through that source's cells. Backdated data
can change a later Refresh; the old run's timestamp alone cannot recreate
its values. No promise of historical reproducibility from a timestamp/hash.

Each run pins one authoritative clinical manifest/component map and verified
module projection snapshot, notebook version, inputs, catalogue/evaluator
version and time zone. Gather **all** operational query cells into one bounded
gateway batch evaluated in a single repeatable-read read-only DB transaction.
The request/response binds run ID, original actor/patient, ordered cell IDs and
plan digests. Response includes source epoch, snapshot ID/capture time, exact
row versions/digests, completeness and per-cell limits. Reject missing,
duplicate, mixed-run or mismatched-plan results. No separate per-cell snapshot
calls or retained gateway snapshot handle. Apply whole-run deadline/row/byte
caps across the callback, including transport. Callback never waits on module
locks or recursively calls module access endpoints.

Clinical and operational capture times may differ. Display them separately;
there is no cross-service global snapshot or globally instantaneous cutoff.
The final clinical authorization check reads current Git outside the source
snapshot; operational scopes use a bounded gateway authorization-only batch.
Gateway account/device revocation bars new admissions; already-admitted bounded
requests may finish under DATA_MODULE.md. Module grant revocation before the
release/publication check withholds output. Do not promise a cross-DB account
row lock or globally immediate credential revocation. Operational source
permissions linearize at the gateway authorization batch; the resulting
scope proof is bound to this operation/captured-scope digest, expires in at
most 10 seconds and must be valid before module release/publication. A later
gateway permission change bars new checks but may not retract this already
admitted bounded operation. Stronger cross-service instantaneous revocation
would require a separate shared fence protocol. Test both sides of this
boundary rather than promising a cross-DB lock.
Query statement timeout defaults to 2 seconds; whole run deadline 10 seconds;
one active run per provider and two per office. Caps: 10,000 qualifying
source rows inspected per query, 1,000 returned rows per query, 2,000 across
the run and 2 MiB response/snapshot content. Enforce before materialization
or aggregation. Exceeding a source cap fails the affected output; a partial
population is never presented as a complete total/average. Propagate query
failure to dependent cells; unrelated cells may show explicitly partial
interactive results, but incomplete runs cannot be finalized.

The implementation documents index/query-plan evidence and load impact
on a 500-patient synthetic practice. Cancellation releases pool/query
resources. Limiter/cell failure does not block ordinary patient care.
No scheduler, worker service, WebSocket execution kernel or query cache is
introduced. Outputs are request/in-memory only unless explicitly saved as
a review draft/snapshot.

Each output has a stable evidence ID scoped to its run/cell and provenance:
notebook/version, query/input digest, source record/revision identifiers,
source/engine versions, as_of/window/time zone, unit/missing policy, row
count and completeness/limitations. Displayed numbers and labels derived
from data come from computed outputs, never hardcoded template claims.
Hashes use versioned deterministic SHA-256 canonicalization and are
fingerprints, not substitutes for the audit chain or retained source data.

## Storage and API

The patient's authoritative manifest pins a provider-owned notebook repo
and the exact intake/measurement/record commits read by its queries. The
following are logical record/index contracts; clinical content is appended
as typed Git documents through the sole publication adapter, not inserted
as an independent writable DB record. Forward-only migrations create
tenant-scoped indexes/workflow state with composite ownership constraints.
Cross-tenant/patient references fail even when a UUID exists.

- Notebook identity/version documents: tenant, patient, owner, created/archive
  metadata, definition and immutable parent/version/hash. A DB index locates
  the exact published Git revision; `If-Match` conflicts return 409.
- Run coordination: bounded DB status/handles/outcomes without output values;
  successful completion and read audit commit before response. Provenance
  retained as part of a saved clinical artifact is authoritative in Git.
- Review draft documents: patient/owner/run and append-only revisions of
  explicitly saved output/narrative/source manifest provenance.
- Final review documents: immutable snapshot/author, notebook/run/draft and
  exact source versions. New reviews reference, never overwrite, old ones.
- Intake response-review events: append-only documents referencing submitted
  response version and finalized notebook review. One manifest publication
  covers notebook + intake changes, followed by DB audit/index completion.
  A read badge is not a clinical review; record explicit review separately.

Database indexes for these records are derived, source-head-fenced and
rebuildable from published Git. Stale/missing/corrupt projections refuse
or rebuild; they never become an alternative truth or empty record.

All routes are `/api/v1` LAN staff routes, hidden from Relay channels.
Patient phones/kiosks do not author or run notebooks in this release.

| Route | Contract |
|---|---|
| `GET/POST /patients/{patientID}/notebooks` | List owned notebooks or create from validated definition/template |
| `GET /patients/{patientID}/notebooks/{id}` | Current definition, version/ETag and permitted metadata |
| `POST .../{id}/versions` | Append edited definition using If-Match; return new version/ETag |
| `POST .../{id}/runs` | Run pinned version/typed inputs; return bounded outputs/provenance |
| `POST .../{id}/review-drafts` | Explicitly save a successful run's output as a PHI draft; expired/lost handle returns 409 rerun_required, never a silent refresh |
| `POST .../{id}/review-drafts/{draftID}/revisions` | Append authored narrative edits using If-Match; client cannot replace computed output/source provenance |
| `POST .../{id}/review-drafts/{draftID}/regenerate` | New draft revision against latest committed data; visible updated as_of and limitations |
| `POST .../{id}/review-drafts/{draftID}/finalize` | Immutable review with If-Match/idempotency key and explicit response IDs reviewed |
| `GET .../{id}/reviews[/{reviewID}]` | Authorized audited review history/detail; no external download |
| `POST .../{id}/archive` | Audited archive; retains definitions, runs and reviews |

Draft creation carries an opaque, short-lived in-memory run handle, never
client-supplied authoritative output values. Handle scope is author,
patient, notebook/version and input digest; TTL five minutes; lost/restarted
handle returns 409 `rerun_required`. No persisted global result cache.

Use 404 for unauthorized ownership/patient/ID, 403 for disabled features,
409 for stale version/rerun required, 422 for invalid plans/units, 429 for
concurrency and 504 for bounded execution timeout. No field/data values
in errors. All implementing routes/contracts update OpenAPI. Publish
`notebooks` feature/capability via #165; default off until the V2 feature is
installed and enabled by the practice admin. Clinical templates are hidden
when the practice's clinical features are disabled.

## Intake review lifecycle

1. Patient/kiosk submits #197; authoritative scoring and existing flag policy
   run once there. Notebook queries do not resubmit or mutate answers.
2. Provider opens Intake review, selects exact forms/versions and a window,
   then inspects submitted answers, completeness, stored scores/flags and
   optionally authorized readings/history.
3. Provider writes their own interpretation, clearly attributed as authored
   narrative, and explicitly saves a review draft from the successful run.
4. Regenerate keeps edits but creates a new output/input/as_of revision and
   marks any previous review confirmation stale. Definition/input edits require
   regeneration. New source facts do not rewrite or automatically invalidate
   a captured draft: finalization is explicitly a review as of its recorded
   per-source captures, not a guarantee of latest data. Show capture age and
   offer Refresh; reconfirm after any regeneration. Loss of access refuses.
5. Finalize review displays the exact draft version, source dates, completeness
   and unresolved flags. The provider explicitly confirms which submitted
   responses they reviewed, restricted to response/version IDs actually in
   the draft. One patient-manifest publication stores the immutable notebook
   snapshot plus intake response-review events; DB audit/projection completion
   must succeed before the API acknowledges it, once per idempotency key.
6. Reopening a finalized review displays frozen values and attribution; it
   never reruns queries. Every artifact read/finalize rechecks the exact captured
   source scopes, including gateway thread/dataset permissions through an
   authorization-only batch; saved values are not an independent permission
   grant. Unavailable gateway authority returns 503 for artifacts using those
   sources; removed permission refuses. A subsequent run/review references the earlier one
   without changing it. Lost current authorization still refuses access.

Finalization documents review, not diagnosis, treatment, clinical sign-off
on every flag, a legal signature or proof of unchanged future data. It
cannot dismiss a PHQ-9 flag, contact a patient or alter a measurement/form
response. Clinical escalation and acknowledgement stay in #199's reviewed
practice policy. No automated clinical thresholds or AI interpretation.

## Acceptance and verification

Implement in focused issues under epic #212: #213 format/catalogue,
#214 storage/authorization, #215 bounded executor, #216 questionnaire intake
adapter, #217 measurement adapter, #218 review lifecycle and #219 staff
UI/templates. Git storage/publication/projection/migration are prerequisites
from PATIENT_REPOSITORIES.md; these are specs, not completed code.

Required synthetic scenarios:

- Linked author succeeds; other provider, front desk/admin, unrelated
  patient/tenant, disabled author, revoked link/feature and Relay request
  fail at new admission. Gateway disable after admission may finish the bounded
  request; the next read/save/finalize refuses. Clinical grant revocation before
  the module's current-Git check refuses. Operational scope revocation before
  the gateway authorization batch refuses; after proof issuance, completion
  may finish only within its 10-second validity. Expired, wrong-operation or
  wrong-scope-digest proof at CAS/release refuses. Saved definitions/drafts
  cannot extend source privileges.
- Published form versions stay separate; drafts/hidden items never appear;
  missing inputs stay null, stored scores remain authoritative, no extra
  clinical flag is generated or cleared, and first-open differs from review.
- Late/backdated readings, duplicate/revised/withdrawn samples, units and BP
  components, local days/DST, empty data and capped populations behave as
  declared. No silent incomplete aggregate or future-data leakage.
- Invalid resource/field/op, injection-shaped literal, cycle/unknown ref,
  excessive plan/expression, timeout, cancellation and concurrency refuse
  before unbounded materialization. Normal API traffic remains responsive.
- Edit an appointment between two operational cells: both use one gateway
  snapshot or the run fails. Clinical/gateway captures remain separately shown.
  Changed operational values after draft save do not rewrite/reject the captured
  as-of review; removing its captured source permission prevents read/finalize.
- Audit failure returns no PHI. Inject failure at every Git publication/DB
  audit-completion boundary and recover to one explained outcome. Retry
  creates one immutable review; corrupting a DB projection cannot change
  the authoritative snapshot or silently alter its displayed content.
- Draft regenerate changes its revision/as_of while a finalized snapshot
  stays byte-identical after source changes. Expired run handle requires
  rerun; browser-injected result values cannot become authoritative records.
- Synthetic sentinel answers/names/scores never reach ops logs, telemetry,
  browser persistent storage, public URLs or repos. Malicious note/answer
  strings render as inert text under existing CSP.

Run `make build vet test` with DB integration enabled for implementation;
validate OpenAPI/route coverage and staff-web tests. Demo the three bundled
templates: Intake review, Instrument score history, and Shared reading
history. Each demo records exact source versions, missing-data example,
run provenance, draft/regenerate/finalize and permission revocation.

## Deferred

Cloud/local AI query drafting (#83 remains separate), Python/SQL execution,
cross-patient research/cohorts, notebook sharing, report schedules, email,
PDF/download/export, patient-facing notebooks, clinical decision support,
FHIR export, general formula languages and copied source/runtime deployment
from the reference service. A future assistant may draft only validated
typed plans locally, with staff review; it cannot bypass any boundary here.
