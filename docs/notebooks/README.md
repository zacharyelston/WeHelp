# Synthetic notebook specification fixtures

These are contract examples for [PATIENT_NOTEBOOKS.md](../PATIENT_NOTEBOOKS.md),
not an implemented runtime or deployable analysis. They contain no patient
identity/data and run no source queries. The format issue must add a full
JSON Schema/validator and turn these cases into executable shared vectors.

- [instrument-history.json](instrument-history.json): explicit time window,
  authoritative instrument scores, table and separate-version chart series.
- [reading-history.json](reading-history.json): shared weight readings in
  their canonical unit, current revisions and a gap-preserving chart.
- [patient-manifest.json](patient-manifest.json): structural example of one
  Patient's published record/intake/notebook component map. All UUIDs and
  commit IDs are invented. The storage tests must materialize real Git
  objects and reject this example's nonexistent commits at runtime.

In the Git-authoritative V2 backend these definitions and their finalized
review snapshots are patient notebook repo documents. Query fixtures must
pin a patient-manifest/component map and build verified Postgres projections;
they must not introduce a database-only clinical source or an executable
Git notebook. See [PATIENT_REPOSITORIES.md](../PATIENT_REPOSITORIES.md).

## Golden source/result cases to implement

1. Questionnaire fixture: one submitted version A response with a stored
   total 4, one submitted version B response with total 6, one draft and one
   submitted incomplete/null-score response. Only the submitted rows appear;
   A/B are separate chart series; null stays null. Notebook execution never
   invokes the form scorer or changes its stored output.
2. Completion fixture: four due occurrences, two complete, one expired and
   one still open; completion is 2/4, with those categories shown separately.
   A not-assigned form contributes no occurrence, not a zero answer.
3. Measurement fixture: day one has weight revision 1=80 kg then revision
   2=79 kg; day two is missing; day three is withdrawn. The current view has
   only the 79 kg reading, a visible gap and no withdrawn point. Retained
   provenance names revision 2. No interpolation or double counting.
4. Time fixture: a late-arriving reading has effective_at before the cutoff
   but received_at after run A starts. Run A excludes it; Refresh creates
   run B and may include it. Finalized review A is unchanged. Include a DST
   day boundary and prove inclusive-from/exclusive-to behavior.
5. Adversarial fixture: inject a patient/tenant filter, unknown field/op,
   SQL-shaped resource name, cycle, arithmetic divide by zero, unsupported
   unit and excessive row budget. Unknown scope overrides refuse; literals
   remain bound data; null/error/limit states cannot become a complete total.
6. Access fixture: another linked provider, admin/front desk, unlinked
   author, revoked scope during execution, disabled feature and Relay request
   all refuse. Read-audit failure returns no values. Snapshot retries create
   one finalized row and one explicit response-review event per source.

Numerical/source selection assertions and authorization tests are separate
from chart layout/accessibility checks. No screenshot substitutes for a
golden result or a permission test.
