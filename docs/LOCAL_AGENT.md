# Design note: a local AI agent on the Back Office

**Status:** proposal, post-V1. Spec issue: #83 (`needs-spec`). Lit review:
[LIT_REVIEW.md](LIT_REVIEW.md), "Local model runtimes". Nothing here is
built, and nothing here changes V1.

## Summary

A practice that wants it upgrades its Back Office from a Mac mini to a Mac
Pro and runs a language model **on that machine**. A WeHelp agent uses it
for daily routines and checkups, and drafts patient outreach from scripts
the practice writes. A staff member approves every outreach before it is
sent.

It keeps the rule every other part of WeHelp follows: **patient data lives
only in the Back Office.** The model runs on the Back Office, reads the
Back Office's data through the same store code as everything else, and has
no network path off the box. No prompt, record, embedding or output leaves
it. Every agent action is in the audit chain.

## Goals

- Daily routines that save front-desk time: finding gaps, overdue
  follow-ups and unanswered messages, and drafting the outreach.
- Checkups on the Back Office itself, summarised for the practice admin.
- Inference on the box only. No cloud model, no hosted API, no telemetry
  from the runtime.
- Human approval before anything reaches a patient.
- Every action attributable and in `audit_events`.

## Non-goals

- Clinical advice, diagnosis, triage or treatment suggestions. The agent
  drafts scheduling and follow-up messages from practice-written scripts.
  It never decides what is clinically appropriate.
- Autonomous sending, or any action a staff member could not take.
- Chat with patients. Patients never talk to the model.
- Training or fine-tuning on patient data.

## The upgrade: Mac mini → Mac Pro

The Mac mini is sized for `wehelpd` and Postgres serving ~500 patients. A
useful local model needs much more memory. Apple silicon shares memory
between CPU and GPU, so the model's size sets the machine.

| | Mac mini (V1) | Mac Pro (agent) |
|---|---|---|
| Runs | `wehelpd`, Postgres, Alloy | The same, plus the inference runtime and the agent routines |
| Memory | 16–32 GB | 128 GB or more, so a quantised 30–70B-parameter model fits alongside Postgres with room left |
| Why this box | Small, quiet, cheap | Memory and sustained GPU throughput. A Mac Studio with the same chip and memory is an equivalent, cheaper choice. Decide in #83 |

**Migration keeps the office identity**, so phones don't re-pair and the
Registry sees nothing change:
1. Take and verify an encrypted backup on the Mac mini (#41), including the
   `officekeys` volume.
2. Set up the Mac Pro exactly as a field host (APPLIANCE.md: FileVault on,
   automatic login off, release tag).
3. Stop the Mac mini's stack (`make appliance-stop-for-reboot`) so it drops
   its Relay tunnel.
4. Restore the database and the `officekeys` volume onto the Mac Pro. The
   office SSH key, TLS key (the phones' pin) and local CA come with it.
5. Start the stack. It reconnects to the same Relay with the same key.
   Check pairing, staff sign-in and `wehelpd doctor` (#81).
6. Erase the Mac mini (or keep it powered off and encrypted for a week as a
   fallback, then erase it). Two Back Offices with the same office key must
   never run at once.

The agent is turned on only after the migrated Back Office passes its
checks, and only by the practice admin (`agent_enabled`, default false).

## Architecture

```
Back Office (Mac Pro)
┌──────────────────────────────────────────────────────────────┐
│ wehelpd                                                      │
│  ├─ River periodic jobs: agent routines (nightly, morning)   │
│  │     │ typed tool calls (Go functions, read-only + drafts)  │
│  │     ▼                                                      │
│  │   agent runner ──── Unix socket ───► inference runtime     │
│  │     │  prompt templates (versioned,    (llama.cpp or MLX;  │
│  │     │  hashed)                          model files on     │
│  │     ▼                                   FileVault disk;    │
│  │   drafts + findings (Postgres)          no network)        │
│  │     │                                                      │
│  ├─ staff UI: Agent queue (approve / edit / reject)          │
│  └─ existing send paths: messages, PHI-free push via Relay   │
│ Postgres: agent_runs, agent_drafts, audit_events             │
└──────────────────────────────────────────────────────────────┘
          no outbound path from the runtime; Relay and Registry unchanged
```

- **Inside `wehelpd`,** as River periodic jobs. No new service to run, back
  up or secure beyond the runtime itself. This follows the "no new
  infrastructure without justification" rule; #83 is the justification for
  the runtime.
- **The inference runtime** is a separate local process (the lit review
  shortlists llama.cpp's server and MLX). It listens on a Unix socket owned
  by the WeHelp service user, or on loopback with a per-boot secret if the
  runtime can't do sockets. It has no other listener. The host firewall
  blocks it from making outbound connections.
- **Model files** are downloaded once by WeHelp support during setup, from a
  source named in #83, checked against a pinned SHA-256, and stored on the
  FileVault disk. Updates follow the same path, never an automatic pull.
- **The agent runner** builds prompts from versioned templates, calls the
  runtime, and accepts only structured output (JSON matching a schema,
  enforced with constrained decoding where the runtime supports it).
  Anything that doesn't parse is discarded and logged as a failed step.

## What the agent may read

Through typed Go functions in `internal/store`, never raw SQL and never a
general query tool. Each function returns the **minimum necessary** fields
for its routine.

| Routine | Reads |
|---|---|
| Unanswered messages | Thread IDs, last-message time, assigned staff, the last patient message's text (needed to draft a reply prompt for staff) |
| Appointment follow-up | Appointments in a window (status, provider, time), patient first name, the practice's follow-up interval per visit type |
| Unconfirmed requests | Pending appointment requests and their age |
| No-shows | Yesterday's no-shows, patient first name, next-available slots |
| Back Office checkup | `wehelpd doctor` results, backup age, audit verify result, disk use, failed jobs. No PHI |

It never reads: staff credentials, device keys, office keys, the audit
table itself (except the verify result), or anything outside its tenant.

Every read of patient data is audited as a read by the agent (this depends
on #40, audit PHI reads).

## What the agent may do

| Action | Allowed | How |
|---|---|---|
| Create a draft outreach message | Yes | Row in `agent_drafts`, status `pending`, linked to a practice-written script |
| Create a staff to-do ("call Jordan about…") | Yes | Row in `agent_drafts`, kind `task` |
| Write the daily checkup summary for the admin | Yes | Row in `agent_runs` |
| Send a message or push | **No** | Only a staff approval sends, through the existing paths |
| Book, cancel or change an appointment | **No** | |
| Change any patient record, device, account or setting | **No** | |
| Read or write files, run commands, open network connections | **No** | No such tools exist |

The agent's identity is a service actor, `agent:<routine>`, with no
password, no session and no API route. It exists only inside River jobs.

## Outreach: scripts, drafts and approval

1. **Scripts.** The practice writes outreach scripts in the staff UI: the
   trigger (for example, "no follow-up booked 6 weeks after a first
   visit"), the tone, what the message may say, and what it must never say.
   A provider approves each script. Scripts are versioned, and changes are
   audited.
2. **Drafts.** A routine finds the patients a script applies to and asks
   the model to personalise the script for each (first name, provider name,
   offered times). The output is a draft. Its fields are checked: length,
   no content outside the script's allowed placeholders, no clinical terms
   from a deny-list, no links.
3. **Queue.** Staff see drafts in an **Agent queue** in the staff UI, each
   marked "Drafted by the WeHelp assistant", with the script it came from
   and why the patient was picked.
4. **Approve, edit or reject.** Only a staff member with the right role can
   approve. Approve sends it as a normal message from the practice, through
   the existing messages path, and a PHI-free push ("New message") through
   the Relay. Edit then approve records both versions. Reject records a
   reason.
5. **Expiry.** Drafts not acted on within 72 hours expire. Nothing is ever
   sent by default.

Patients receive a normal practice message. Whether outreach messages carry
an "AI-assisted" note is a practice-policy question settled in #83.

## Audit

Every agent action is written with `audit.Record` in the same transaction
as the change it describes, so the agent appears in the same hash chain as
staff.

| Event | Actor | Detail (IDs only) |
|---|---|---|
| `agent.run.start` / `agent.run.end` | `agent:<routine>` | Run ID, routine, model ID and file hash, template hash, counts, outcome |
| `agent.read` | `agent:<routine>` | Run ID, which tool, the record IDs read |
| `agent.draft.create` | `agent:<routine>` | Draft ID, script ID and version, patient ID |
| `agent.draft.approve` / `edit` / `reject` / `expire` | Staff user (or system for expiry) | Draft ID, reason on reject |
| `message.create` (on approve) | Staff user | As for any staff message, linked to the draft ID |
| `agent.script.*` | Staff user | Script ID and version |
| `agent.config.*` | Staff admin | Enable/disable, schedule changes |

Prompts and model outputs are stored in Postgres with the run (they are
PHI, so they stay in the Back Office and in its encrypted backups). They
are not written to application logs. Logs carry run IDs, timings and
counts only. The audit stream export (#56) carries these events like any
others: IDs only.

## Threat model additions

These extend [SECURITY.md](SECURITY.md) and are written into it by #83.

**New assets**

| Asset | Worst case |
|---|---|
| Model files | A tampered model that writes harmful or misleading drafts |
| Prompt templates and outreach scripts | Altered wording sent to patients |
| Stored prompts and outputs | PHI, protected like patient records |

**New adversaries and attacks**

| Threat | Control |
|---|---|
| **Prompt injection** through patient-written text ("ignore your instructions and…") | Patient text is passed as quoted data, never as instructions. The agent has no tool that sends, changes or deletes, so a successful injection can at worst produce a strange draft, which a human sees before anything is sent. Injection tests are part of #83 |
| **Data exfiltration** by the model | No network path: socket-only runtime, outbound firewall rule for the runtime, no tools that fetch. Outputs stay in Postgres |
| **Supply chain:** a malicious model or runtime | Pinned checksums for model files and runtime binaries, a licence and provenance review, updates only by WeHelp support through the same check |
| **Resource exhaustion** starving `wehelpd` | Routines run off-hours; the runtime runs at lower priority with a memory cap; the observability load guard (#55) stops the agent first |
| **Over-reach:** the agent reads more than it needs | Typed, per-routine read functions; every read audited (#40) |
| **Staff over-trust** in drafts | Drafts are labelled; scripts are provider-approved; approval is per message, no "approve all" in V1 of the feature |
| **A compromised Back Office host** | Out of scope, as for V1. The agent adds no new remote entry point |

**Unchanged:** the Relay, the Registry and the phones. Nothing about the
agent is visible to them except, once approved, ordinary messages and
pushes.

**HIPAA notes.** Inference on the covered entity's own hardware is a use of
PHI within the practice, for treatment and operations. No model vendor
receives PHI, so no new BAA is needed for the model. WeHelp support's
access during setup is the same as for any Back Office install. The
practice's risk analysis gains a section for the agent.

## Open questions (for #83)

1. Runtime: llama.cpp server or MLX (lit review).
2. Model family and size, and its licence terms for commercial clinical
   settings.
3. Mac Pro or Mac Studio.
4. Whether outreach messages say they were AI-assisted.
5. The exact deny-list and checks applied to drafts.
6. Whether any routine may run during clinic hours.
