# Built-in instruments

The standard questionnaires WeHelp ships ready to assign (#199): PHQ-9, PHQ-2, GAD-7 and GAD-2. This is the "Standard instruments" section of the questionnaire design. It moves into `docs/QUESTIONNAIRES.md` when that design (#192) is merged.

## What's built in

| Instrument | Measures | Items | Total | Bands | LOINC panel / total |
| --- | --- | --- | --- | --- | --- |
| PHQ-9 | Depression | 9, each 0–3, plus the unscored difficulty question | 0–27 | 0–4 minimal, 5–9 mild, 10–14 moderate, 15–19 moderately severe, 20–27 severe | 44249-1 / 44261-6 |
| PHQ-2 | Depression screen | PHQ-9 items 1–2 | 0–6 | 3 or more: positive screen | 55757-9 / 55758-7 |
| GAD-7 | Anxiety | 7, each 0–3, plus the unscored difficulty question | 0–21 | 0–4 minimal, 5–9 mild, 10–14 moderate, 15–21 severe | 69737-5 / 70274-6 |
| GAD-2 | Anxiety screen | GAD-7 items 1–2 | 0–6 | 3 or more: positive screen | none confirmed / none confirmed |

- **Definitions:** `internal/forms/instruments/*.json` (`wehelp.form.v1`), embedded in the binary. Copies in `testdata/forms/valid/instrument-*.json` feed the shared vectors, and a test keeps them identical.
- **Fidelity:** wording, answer options, order and recall period are as published. `internal/forms/instruments/wording.txt` pins the published wording, and `TestWording` fails if a definition drifts from it.
- **Item codes:** PHQ-9 items 44250-9, 44255-8, 44259-0, 44254-1, 44251-7, 44258-2, 44252-5, 44253-3, 44260-8; difficulty 69722-7. GAD-7 items 69725-0, 68509-9, 69733-4, 69734-2, 69735-9, 69689-8, 69736-7. Answers: LA6568-5, LA6569-3, LA6570-1, LA6571-9 (frequency); LA6572-7, LA6573-5, LA6575-0, LA6574-3 (difficulty).
- **Where the codes come from:** checked on 2026-11-16 against the HL7 US Core STU7 PHQ-9 Questionnaire example, a PACIO GAD-7 Questionnaire, and an HL7 PHQ-2 example. loinc.org refused automated access, so the LOINC release number isn't recorded; confirm against the current release before a field rollout. GAD-2 panel and total codes, and a GAD-7 difficulty code, couldn't be confirmed, so none are given.
- **Missing answers:** every scored item is required. A total with any item missing is blank (no proration). A prorating rule would need the instrument manual's guidance and the owner's approval.
- **Licence:** "No permission required to reproduce, translate, display or distribute." Each definition carries the attribution, "Developed by Drs. Robert L. Spitzer, Janet B.W. Williams, Kurt Kroenke and colleagues, with an educational grant from Pfizer Inc.", in `copyright` and as the last display item. AUDIT isn't included: it needs WHO permission for commercial use.

## How practices get them

- They are synced into each practice as read-only `kind = instrument` forms by `internal/instrumentsync`. The sync runs from the forms runner and the forms list.
- The sync is idempotent. The first sync is audited as `form.create` with no actor. A changed definition publishes a new version and never changes an old one.
- They're hidden, and can't be assigned, unless:
  - the build is a demo build, or the config sets `forms_instruments: true`;
  - the practice's `forms` switch is on;
  - the practice type is clinical (salons never get them).
- The demo seed (`wehelpd seed`) also assigns Jordan a PHQ-9, so a demo can show the flag on a synthetic answer.

## The self-harm flag

PHQ-9 item 9 ("Thoughts that you would be better off dead or of hurting yourself in some way") above "Not at all" raises the flag `self_harm`. Flags are part of `wehelp.form.v1`: a definition-level `flags` list of `{id, text, when}`.

- **Staff only:** on submit, flags are stored on the response and shown to staff in red, in the response, the patient's Answers list and the Due forms view ("Flagged this week").
- **Not shown to the patient:** the submit answer the app gets carries no flags, and the app shows nothing about them.

## What the owner must decide before a field install

`forms_instruments` stays off on field installs until the owner has decided and written down (the runbook, with #199):

1. **Who monitors the flag, and when:** which role sees "Flagged this week", in which hours, and what happens to a PHQ-9 sent outside them (evenings, weekends, holidays).
2. **What patients are told:** the exact wording before a PHQ-9 is assigned or opened, and after it's sent, including whether and how they are told that answers aren't read in real time. TODO(owner): no patient-facing text about item 9 or crisis help exists in the app or in pushes; none was invented.
3. **The escalation steps:** what the person who sees a flag does, in order, and how it's recorded.
4. **Confirming the instruments:** the LOINC release, the wording against the published PDFs, and that no proration is wanted.

Until then, the instruments exist only in demo builds.
