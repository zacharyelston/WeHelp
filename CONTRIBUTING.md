# Contributing to WeHelp

WeHelp is built by humans and dev agents together. This file is the
bootstrap: read it first, then pick up work from GitHub.

## Where to look

| You want… | Look at |
|---|---|
| Work to pick up | [Issues](https://github.com/zacharyelston/WeHelp/issues) labeled `agent-ready` or `help wanted`, ordered by milestone |
| Build / test / run commands | [AGENTS.md](AGENTS.md) |
| Design decisions & invariants | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |
| How to write a task | `.github/ISSUE_TEMPLATE/task.md` |

## Workflow

1. **Pick an issue.** Prefer the earliest open milestone. `agent-ready`
   issues have acceptance criteria and a verification section — everything
   needed to complete them without asking. `needs-spec` issues are not
   ready; ask questions in the issue or leave them alone.
2. **Comment / self-assign** so nobody duplicates the work.
3. **Branch per issue:** `git checkout -b <issue#>-short-slug`.
   Never commit or push to `main` — every change, including docs and
   config, lands through a PR. `main` is protected.
4. **Meet the acceptance criteria, then verify** — run the verification
   steps in the issue (at minimum `make build vet test`, plus whatever the
   issue lists).
5. **PR with the template filled in.** Link the issue (`Closes #N`).
   Keep diffs scoped; drive-by refactors get rejected.

## The review gate

Spec maturity is tracked by labels: `needs-spec` → `spec-review` →
`agent-ready`. Nothing becomes `agent-ready` without a reviewer pass.

A reviewer validates, in order:

- **Is the approach still current?** Check every named library/pattern
  against its repo: last release, open-issue health, known vulns, whether
  prominent dependents have dropped it. Flag anything stale.
- **Are dependencies maintained?** Prefer deps with activity in the last
  ~year. Hand-roll before adopting abandonware.
- **Are acceptance criteria testable?** Every box must be verifiable by a
  command, request, or query — not vibes.
- **Is scope tight?** An empty "Out of scope" usually means it isn't.

Findings go in issue comments. The reviewer either fixes the body and flips
to `agent-ready`, or leaves concrete questions and keeps `needs-spec`.

## Standards (the short list — details in the files above)

- Never commit secrets. Config goes through Viper (`WEHELP_*` env vars).
- Ledger and audit tables are append-only. Use `ledger.Post` /
  `audit.Record` inside a transaction — never UPDATE/DELETE those tables.
- Every write touching PHI must also write an `audit_events` row in the
  same DB transaction.
- Every new table carries `tenant_id`. No tenant-less data.
- The Xcode project is generated — edit `ios/project.yml`, never the
  `.xcodeproj`.
- Migrations are goose SQL in `internal/store/migrations/` — numbered,
  forward-only, with a matching `Down`.
- Commits: ONE-LINE subjects only — context belongs in `CHANGELOG.md`
  (Keep a Changelog format), not the commit body.
- Shell: never use heredocs — write files with file tools, then reference
  them.
- PRs: use the template.
