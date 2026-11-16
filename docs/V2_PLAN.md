# V2: intake, patient data and notebooks

Status: **specifications for review**, 2026-11-16. The owner added patient
notebooks to V2 alongside questionnaires because intake needs a processing
and review path. V1 release scope and gates do not change.

The owner additionally chose **Git as the authoritative patient record**:
see [PATIENT_REPOSITORIES.md](PATIENT_REPOSITORIES.md). Each Patient is a
local repository collection with notebooks. Postgres clinical indexes are
derived/rebuildable; security audit/identity/workflow remain DB authorities.
There is no independent writable DB clinical source in the migrated mode.

**Where it runs (owner, 2026-11-16):** the Back Office is for scheduling
and daily administration. Clinical records, notebooks and clinical access
decisions go in a separate **Data Science & Security module** on the same
office host, reached only through the Back Office. The boundary is in
[DATA_MODULE.md](DATA_MODULE.md). [V2_CONTRACTS.md](V2_CONTRACTS.md) resolves
actor permissions, kiosk, form arithmetic/schedules and HealthKit retry
semantics. Operational identity/scheduling/communications/orders remain
Back Office DB sources; only clinical projections are rebuilt from Git.
The module skeleton and fenced access authority precede clinical rollout.

## One workflow and its foundations

| Track | Work | Output and boundary |
|---|---|---|
| Intake | Questionnaire epic #201, design [#192](https://github.com/zacharyelston/WeHelp/pull/192), #193–#200 | Versioned forms, assignments, patient/kiosk responses and authoritative stored scores/flags; no notebook dependency for submitting a form |
| Shared patient data | Wearables epic #207, design [#202](https://github.com/zacharyelston/WeHelp/pull/202), #203–#206 | Patient-account-only HealthKit intake, plan/sharing gates and append-only readings/revisions; no vendor cloud or office device ingestion |
| Record foundation | [Patient Git collections](PATIENT_REPOSITORIES.md), epic #220, #221–#224 | Authoritative patient/component manifests, audited publication/recovery, verified projections and migration |
| Processing/review | [Patient notebooks](PATIENT_NOTEBOOKS.md), epic #212, #213–#219 | Typed scoped queries, quality/provenance, notes/tables/charts and immutable provider-reviewed Git snapshots in the module |

This integration reconciles source design PRs #192/#202/#225/#227 without
changing their branches. All stay subject to cross-review. Kiosk transport is
specified; clinical flag escalation remains an owner field-release gate.

```text
Forms and assignments -> responses -> validation/scoring ----+
                                                           |
Patient plan + sharing -> HealthKit -> accepted revisions ---+-> typed datasets
                                                           |       |
Authorized appointments/requests/message activity ----------+       v
                                                    patient notebook
                                                query / inspect / interpret
                                                           |
                                                 draft -> regenerate
                                                           |
                                                  finalize review
                                                           |
                                             immutable internal snapshot
```

Notebook analysis reads the source record; it never becomes an alternative
scoring engine, source-of-truth answer editor or automatic clinical action.

## Delivery order and useful slices

| Foundation task | Owner lane | Prerequisites |
|---|---|---|
| #230 module process/DB isolation, mTLS and bound assertions | Claude | V1 identity/audit foundations |
| #221/#222 Git collection, publication and recovery | Codex | #230 |
| #231 durable access mirror/fences; #226 private/source policy | Claude | #221/#222; #226 integrates #231 |
| #232 booking/link recovery | Claude | #231/#226 |
| #223 verified clinical projections | Codex | #221/#222 |
| #233 operational notebook facts and captured-scope checks | Codex | #230/#231, #213 typed plans |
| #234 outbox, dual audit export and coordinated restore | Codex | #230/#231/#222/#223 |
| #224 migration rehearsal | Codex | #226/#231/#232/#234 and converted existing V1 clinical paths |

These are implementation dependencies, not a requirement to build every
feature before testing the module against synthetic fixtures. No dependency
on a notebook operational adapter is required for app-only form submission.

1. Finish V1's security/identity/audit foundations. V2 authoring stays off
   on field builds until its contracts and review pass.
2. Build module isolation/mTLS/assertions, then Git collection/publication and
   recovery. Add private access policy #226, durable mirror fencing, verified
   clinical projections and booking/link coordination before migration. Convert
   clinical paths and operational privacy gates; rehearse a synthetic practice
   before enabling the module backend.
   No Git mirror/DB dual-master transition is accepted.
3. Questionnaire core: #193 format/evaluator, #194 storage, #196 assignment
   and #197 responses, with #198 app walker. The builder #195 and standard
   instruments #199 complete intake content/authoring; #200 kiosk is a
   separate client/trust integration, not a prerequisite for app intake.
4. Notebook format/catalogue, immutable provider-owned Git storage and bounded
   executor can be developed against synthetic fixtures. Do not mark a
   fixture-only dataset as available in the product.
5. Questionnaire adapter + notebook UI/review: submitted intake -> typed
   query -> missingness/source scores -> provider note -> review snapshot.
   Instrument history uses #199's authoritative scores and exact versions.
6. Health-data #203/#204/#205 intake and #206 basic Readings view progress
   independently. The notebook measurement adapter then supplies the shared
   chart/query layer without making basic readings depend on notebooks.
7. A V2 end-to-end demo verifies app intake and its separate review state,
   repeated instrument scores, optional shared readings, late/revised data,
   immutable review history, revocation and audit failure handling.

## Release acceptance

- Every implementation issue passes the CONTRIBUTING.md reviewer gate from
  `spec-review` to `agent-ready`; a direction decision is not a code review.
- Clinical source truth is published patient Git, not a parallel DB write.
  Every successful command has durable audit intent/publication/completion;
  verified patient query indexes can be rebuilt from Git. Crash recovery and
  coordinated Git/DB backup/restore are required acceptance evidence.
- Form submission/scoring works with notebooks disabled/unavailable; a
  notebook reads only implemented enabled sources. Query limits never
  block ordinary care or present truncated populations as complete totals.
- Per-patient/source authorization and audit are enforced on every read,
  write, run and finalization. Front desk/admin access to patient lists is
  not notebook clinical access; initial notebooks are provider-owned.
- Data values, notes, reports and provenance stay on the office appliance and
  encrypted backups, with browser memory only while displayed. No cloud
  model, remote Git storage of PHI, public report, execution kernel or Kubernetes.
- Empty/missing data, form/scoring versions, units, withdrawals, time zones
  and data cutoffs have visible semantics and negative test evidence.
- A finalized review preserves the exact authorized reviewed output. A
  subsequent source correction produces a new review, never silent changes
  to old snapshots. Viewing a response is not recorded as clinical review.
- Fenced/unknown/expired access mirrors refuse gated operational PHI during
  outages. Booking/link response-loss recovery retains reservations. Test two
  service restarts, replay/assertion binding and restore epoch mismatches.
- Clinical response/escalation policy remains separately approved; notebook
  finalization never resolves instrument flags or generates clinical advice.

The notebook scope replaces earlier “trends later” deferrals for the bounded
score/reading history views in this V2 work. Arbitrary analytics, AI drafting,
cohorts, shared notebooks and external report delivery remain deferred.
