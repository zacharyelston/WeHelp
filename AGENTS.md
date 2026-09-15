# WeHelp — agent notes

## Where to find work

Work is tracked on GitHub, not in this repo's docs:

- Pick up issues labeled `agent-ready` (fully specified, acceptance criteria
  + verification included). Avoid `needs-spec` issues.
- Milestones define order: lowest open milestone first.
- Task format and workflow rules: `CONTRIBUTING.md` and
  `.github/ISSUE_TEMPLATE/task.md`.
- Architecture invariants (append-only ledger, audit chain, multi-tenancy):
  `docs/ARCHITECTURE.md` — read it before touching `internal/`.

## Commands

- Build server: `make build` → `bin/wehelp`
- Test/vet: `make test`, `make vet`
- Run with local Postgres: `make compose-up` then `make run` (or `wehelp serve`)
- Migrations: `wehelp migrate` (auto-applied on `serve` unless `WEHELP_AUTO_MIGRATE=false`)
- New migration: add `NNNNN_name.sql` to `internal/store/migrations/` (goose format)
- iOS: `make ios` (xcodegen + open Xcode), `make ios-build` (headless compile check)
- Never edit `ios/WeHelp.xcodeproj` directly — it's generated from `ios/project.yml`

## Conventions

- Go: cobra for CLI, viper for config (`WEHELP_*` env vars), chi for HTTP,
  pgx/v5 for Postgres, goose for migrations, slog for logging.
- Config keys live in `internal/config/config.go` defaults — update there, not
  just in docs.
- Financial/audit data: append-only tables only; use `ledger.Post` and
  `audit.Record` inside a caller-managed `pgx.Tx`. Never UPDATE ledger or
  audit tables — DB triggers forbid it.
- Every PHI-touching write must be wrapped in a transaction that also calls
  `audit.Record`.
- Git: ONE-LINE commit subjects only — context goes in CHANGELOG.md, not the
  commit body.
- Git: never commit/push to `main` — every change lands via a feature
  branch + PR.
- Scale budget: the target is an office appliance serving ~500 patients —
  see docs/ARCHITECTURE.md. Prefer boring code; no new infra beyond
  Postgres + object storage without issue-level justification.
- Changelog: every PR-worthy change gets a `CHANGELOG.md` entry (Keep a
  Changelog format).
- Shell: NEVER use heredocs. Write content to a file with file tools first,
  then rename/reference it.
- Swift: iOS 17+, SwiftUI, `@Observable` for state, `async/await` for
  networking. Health data goes through HealthKit, not raw BLE, unless a
  device requires it.
