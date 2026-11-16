# V2 implementation contracts and failure cases

Status: specified for cross-review, 2026-11-16. These contracts apply to the
migrated V2 mode, not today's V1. Read [DATA_MODULE.md](DATA_MODULE.md) for
service authority, assertions, access fencing and booking coordination;
[PATIENT_REPOSITORIES.md](PATIENT_REPOSITORIES.md) for clinical Git writes;
and the source documents for schemas/UI. This document resolves their
boundary choices. Issue acceptance criteria must include the applicable cases.

## Actor and field authorization

Privacy is an **additional restriction on an existing permission**, never a
replacement grant. Use `basePermission && privateRestriction`, with patient
self and explicitly scoped kiosk operations evaluated in their own policies.
Do not implement a universal “linked provider or deny” middleware.

| Actor | Non-private patient | Private patient |
|---|---|---|
| Patient's own paired account | Own assigned forms, page save/submit, health-sharing/ingest and privacy/link-approval APIs; only explicitly implemented own reads | Same self-service permissions; no staff APIs or provider notebook access |
| Enabled linked provider | Existing source/field/thread permissions | Same permissions, with active link required for clinical content |
| Unlinked provider | Existing non-clinical permissions only | No clinical content; new link remains pending patient approval |
| Front desk/admin | Existing explicitly granted source permissions | Name/contact/basic appointment times/status/provider and private marker only; no reasons, clinical notes, message previews, scores, form names/flags or clinical counts |
| Kiosk device | One valid staff handoff's response only | Same narrow capability, issued only by an authorized linked provider |
| Internal service | Fixed purpose, original actor and bounded patient/resource scope | No general act-as/admin bypass; same private/source checks |

A linked provider cannot read another provider's notebook or an otherwise
restricted thread. The patient-private flag does not replace therapy thread
membership. Searches, badges, exports, lists, errors and callback datasets
apply the same field restrictions; filtering only a detail endpoint is insufficient.
Use 404 for inaccessible resource IDs, 403 for a known disallowed operation,
409 for access-version/booking conflicts, and 503 for unavailable access authority.
Never infer an empty clinical history from unavailable authority.

The patient can enable/disable private status; an enabled linked provider can
enable it at the patient's request, recording the requester/attestation, but
cannot disable it. Show currently linked providers when enabling: the flag
does not revoke those links. New links require explicit patient approval when
private. Existing grant revocation and private toggles use the durable fence
protocol even if no clinical feature tab is enabled. Admin cannot turn it off.
The product provides no break-glass bypass in this release.

## Forms: state, arithmetic and clinical release

The module owns the immutable published form library and clinical response
commands. Library templates contain no patient values and may be module DB
configuration; an occurrence pins an exact immutable definition/version/digest,
copied into clinical Git with the response. A later publish never changes an
open occurrence. Media is module-local, validated by file type/size, immutable
and content-addressed; allow JPEG/PNG images and MP4 video, maximum 10 MiB per
asset, 64 MiB and 20 assets per version, 20 megapixels per decoded image.
Serve same-origin with no autoplay/external embeds. Pinned assets are retained and encrypted-backed-up for
the life of referring records. No remote media URLs or executable content.

Every table has tenant_id. Clinical assignments, occurrence events, page
revisions, answers, submission, scores/flags and review events are Git source;
their SQL tables are rebuildable projections. A library trigger alone does
not make a clinical SQL write authoritative. Source scoring occurs once on
successful submit, in the same manifest publication as submitted answers.

- Definition maximum: 200,000 UTF-8 bytes excluding separately bounded media,
  500 items; each expression depth <=32 and <=4,096 nodes across the definition.
  Reference/type/graph validation rejects unknown operators, cycles and
  forward dependencies. Go, Swift and builder share positive/negative vectors.
- Numeric literals/answers accept finite base-10 values with <=12 integer and
  <=6 fractional digits, no exponent/NaN/infinity. Parse exact decimal/rational
  values (Go JSON UseNumber), not platform floating-point comparisons. Arithmetic
  is exact until explicit rounding; `round` takes `args: [value, places]`, uses half-even with
  places 0..6. Bound intermediates to 128 numerator/denominator bits; overflow
  yields a validation/scoring error, never a platform-dependent score.
  A published prorate policy specifies denominator, minimum answered count and
  final rounding. Division by zero/missing produces null with a reason, not zero.
- Hidden answers are removed from the submitted canonical answer set; missing
  required visible answers reject the whole submit. Definitions and scores
  reference only existing items. Server result is authoritative; app previews
  cannot submit scores, and instrument versions ship approved golden vectors.
- Page writes require expected response revision (`If-Match`) and idempotency
  key/digest. Conflicting stale page saves return 409; same-key/different-body
  refuses. Submission is one idempotent terminal command: saved page revisions,
  visibility, completion window and authorization are revalidated together.
  Expired/submitted responses cannot accept new pages; an old handoff cannot
  reopen them. First-open/read and explicit clinical review are separate events.
- Recurrence uses the practice IANA zone pinned to the assignment. Due dates
  advance by local calendar days/weeks, not fixed 24-hour seconds. Due local
  time is 09:00; opening/closing offsets are calendar days at 09:00. Ambiguous
  instants choose the earlier offset; nonexistent times choose the next valid
  instant. Occurrence identity is assignment ID + scheduled local due date;
  uniqueness survives restarts/DST. Opening is inclusive, closing exclusive.
  Reject nonpositive windows and unbounded recurrence; generate at most 90
  future days per pass and cap assignments to 366 occurrences.
- `form_due` events are durable after Git completion. Send once on opening and
  once 24 hours before close only if that instant is strictly after opening
  and still before close. Terminal occurrences suppress pending reminders.
  Worker restarts do not create new event IDs. Transport can duplicate delivery.

Custom nonclinical intake can be developed without choosing emergency policy.
Clinical instruments/flags remain field-disabled until the practice approves
who monitors them, response hours, patient-facing expectations and escalation
procedures, with an acknowledged test workflow. This is a named owner release
gate, not permission to invent a clinical response rule. Submit or notebook
finalize never acknowledges/resolves a flag or sends clinical advice.

## Kiosk transport and handoff

The first kiosk is an office-owned iPad using the same walker, with a separate
Secure Enclave device key and `kiosk` role. It is not a patient's phone and
cannot use patient account pairing or ordinary staff routes.

Admin enrolls it on office Wi-Fi using a one-use, 10-minute kiosk QR. The QR
contains no PHI: office ID, explicit LAN host/port, CA/key pin and enrollment
token only. Use LAN HTTPS port 8443, TLS 1.3, stable office staff TLS key and a
certificate with matching DNS/IP SAN. The operator manually installs/trusts
the office CA on the managed iPad; pairing additionally checks the QR pin.
No discovery, trust-all TLS, ATS exception or module public listener. A doctor
check validates SAN/CA expiry, key persistence and route-origin separation.

Staff chooses patient, occurrence and device. The server issues one response
handoff, single-use to start, valid 10 minutes, bound to device/staff/patient/
occurrence/response. A private patient's handoff requires an active linked
provider, not admin/front desk. No walk-in identity lookup or unassigned response
in the first release. Kiosk polls its own handoff with <=15-second long polls;
the 20-second client timeout remains unchanged. Only after claiming a handoff
does the patient see a limited identity confirmation; the attract screen has no name.

Writes carry the response capability and device proof; the gateway asserts
`lan_kiosk` origin and the module rechecks the current handoff/source scope.
No private toggle, general record read or HealthKit sync. One live response per
device; replacing/cancelling handoff revokes the old capability. After start,
capability expires at the earliest of occurrence close, cancellation or 30
minutes; staff must explicitly reissue to resume saved pages after expiry.

Submit, cancel, app background and 3-minute inactivity clear all displayed
PHI/in-memory answers; warn at 2.5 minutes. Network failure clears the walker
and permits resume only through a still-valid explicit handoff; no disk cache.
Use Guided Access runbook, no app switcher previews/analytics. Record kiosk,
patient and issuing staff IDs in audit, never answers. Staff revocation/private
change during a response is checked before next save/submit and result release.

## HealthKit consent, summaries and retries

HealthKit read denial is intentionally indistinguishable from no accessible
samples. UI says “No accessible data; check Health permissions” and offers
instructions; never asserts that the app detected a denied read permission.
OS permission does not replace per-practice plan and patient sharing choices.
Provider/front-desk/admin sessions never create a HealthKit reader.

Sharing is an append-only interval history per patient/practice/type. Accept
new readings only when a plan and sharing are currently active AND the
effective sample instant belongs to an authorized plan/sharing interval. On
reenabling, start a new interval; do not backfill a previous off period. A
daily summary requires its complete day interval to be covered by both gates;
partial first/last days remain unavailable rather than disclose off-period data.
Withdrawals of a previously accepted owned source are allowed while sharing
is off. Ending a plan/offline disable stops new intake, not retained access
or correction/withdrawal history. Distinguish permanent policy rejection from
transient service/authority failures.

| Data | Deterministic construction |
|---|---|
| Weight, BP, glucose and other reading types | HealthKit sample UUID/correlation identity, canonical catalogue unit, source metadata and effective instant; exact duplicate is no-op, conflicting same identity refuses |
| Steps | HealthKit quantity statistics cumulative sum per practice-zone day, not a sum of individually overlapping device streams |
| Resting heart rate | HealthKit quantity statistics discrete average per practice-zone day; no observations means null |
| Sleep | HealthKit category samples, select asleep stages only, clip to day, union overlapping intervals across sources/stages, sum duration; exclude awake/in-bed, never double count overlap |

The practice IANA zone is pinned in each monitoring-plan version. Summaries
store local date, IANA zone, UTC interval and algorithm version; an offset alone
is insufficient. A zone change starts a new plan interval, never relabels
previous days. HealthKit statistics collection queries apply only to quantity
types; sleep must use category queries and the explicit interval algorithm.

Anchors are per patient account/type in Keychain. Process one anchored page
into batches <=500. Persist its next anchor only when **every** chunk has a
durable per-item accepted/duplicate/permanent-policy-rejected result. Network,
DB, audit, module outage and uncertain commit are retryable: retain old anchor,
use same item identities/digests, and retry safely. A mixed batch response
lists per-item status/reason without reflecting other patients. Account
removal clears anchors; reinstall/replay cannot duplicate accepted sources.

Summary source identity includes patient, type, local day, zone, plan interval
and algorithm version. Server assigns monotonic source revision, accepts
`expected_current_revision` plus payload digest. Exact same payload is no-op;
a different stale revision returns 409 + current revision. The app re-queries
HealthKit before retry, never blindly overwrites with an old buffered summary.
Changed/deleted samples invalidate their affected days, including past days;
recompute those days and the current day. Initial sync scans the active
interval; anchor loss replays it. HealthKit deletion events may provide UUID
without timestamp: resolve previously accepted UUIDs through a bounded own
source metadata lookup; an unresolved summary deletion triggers rescan of the
active interval. No raw values are persisted on the phone for this purpose.
Cap a plan interval to 90 days per sync pass with resumable cursors; do not
drop older invalidations or advance past unprocessed work.

Readings, summaries and withdrawals append Git revisions; no deleting source
rows to make a chart look current. No hardcoded clinical threshold alerts.
Missing/withdrawn days are null and charts do not interpolate them as zero.

## Required protocol vectors

Developers must implement the matching tests in the linked issues; these are
expected outcomes, not assertions that runtime code already exists.

| Case | Required result |
|---|---|
| Private patient saves own assigned form; linked provider reads someone else's notebook | Patient succeeds; provider notebook read refuses |
| Private enabled during a run/handoff | Recheck source scopes; withhold newly unauthorized output/save; no cached clinical preview |
| Access mutation response lost, then module stops | Gateway remains durably fenced; no old-rights fallback |
| Module restore has older epoch/version | Maintenance fence, reconcile before clinical access; never silently trust old mirror |
| Booking persists provisional row, activation succeeds, gateway crashes | Slot remains reserved; recovery yields the same booking or a later-revocation cancellation; no duplicate grant |
| Same JWT is replayed; same operation retried with fresh JWT | Replay refuses; matching idempotent operation returns one durable outcome |
| Operational source changes between notebook source captures | Per-source provenance displays separate captures; no global snapshot claim |
| Shared HealthKit day spans an off interval | No whole-day summary sent; retained prior authorized facts remain |
| Second of three anchored chunks fails transiently | Old anchor retained; retries deduplicate first chunk; no data lost |
| Sleep intervals 22:00–02:00 and 01:00–03:00 overlap | Union is 5 hours before day clipping, not 6; DST uses instants |
| Reinstall submits old daily summary against newer revision | Exact duplicate no-op or stale 409/requery; newer data not silently replaced |
| Visible required answer missing; score division by zero | Submit rejects missing answer atomically; evaluator null/reason for zero division |
| Repeated scheduler pass across DST | One occurrence per scheduled local date, same pinned form version |
| Kiosk background/network loss/idle then next patient | No previous answers/name on screen or disk; old capability cannot start another response |

For implementation PRs: update OpenAPI, capability/config defaults and
CHANGELOG; forward-only tenant-scoped migrations; `make build vet test` and
DB/Git/two-process fault tests applicable to the task. iOS compile plus walker
vectors for client tasks. Clinical policy, field migration and owner merge
approval remain explicit release gates after code verification.
