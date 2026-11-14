# Practice onboarding and training

How a practice is brought onto WeHelp, and the material staff are trained
with.

| Doc | What it covers |
|---|---|
| [RUNBOOK.md](RUNBOOK.md) | The onboarding runbook, practice side first: Registry setup, Back Office install and Relay connection, the provider app, the training week day by day, roles, checklists, go-live criteria, hypercare and rollback |
| [TRAINING.md](TRAINING.md) | Video training: modules, a script per module, and a shot list for every screen in the staff web UI, patient app, provider app and kiosk, marked by what exists today |
| [KIOSK_DEMO.md](KIOSK_DEMO.md) | Interventionaire (the questionnaire and intervention tooling for the post-V1 kiosk): what it does, how it plugs into the Back Office, and the demo script |
| [../LOCAL_AGENT.md](../LOCAL_AGENT.md) | Design note: upgrading the Back Office to a Mac Pro running a local AI agent, with no patient data leaving the box |

**Synthetic data only.** All training, demos and recordings use demo
practices and demo patients on demo hardware. A field Back Office is never
seeded.

## Issues

| Issue | Milestone | What |
|---|---|---|
| #79 | V1-M3 | Decide: practices self-register, or WeHelp support enters them |
| #80 | V1-M7 | Training seed profile, refused on field Back Offices |
| #81 | V1-M7 | `wehelpd doctor`: machine-checked go-live criteria |
| #84 | V1-M7 | Record the training videos |
| #82 | Post-V1 | Kiosk questionnaires from Interventionaire, answers only in the Back Office |
| #83 | Post-V1 | Local AI agent on the Back Office |

Related: demo kit #45, field install #47, external audit #46, kiosk #8,
provider app #69.
