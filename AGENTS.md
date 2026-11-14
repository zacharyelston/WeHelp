# WeHelp — agent notes

## Where to find work

Work is tracked in **zacharyelston/WeHelp** issues
(`gh issue list -R zacharyelston/WeHelp`); PRs go there too. The older
backlog in alzacai/WeHelp was copied over and is referenced as
`alzacai/WeHelp#N`.

- Pick up issues labeled `agent-ready` (fully specified, acceptance criteria
  + verification included). Avoid `needs-spec` issues.
- Milestones define order: `V1-M0` … `V1-M7`, plus the parallel `V1-OBS`
  track. `Post-V1` is deliberately deferred.
- Task format and workflow rules: `CONTRIBUTING.md` and
  `.github/ISSUE_TEMPLATE/task.md`.
- Read before designing or touching `internal/`:
  - `docs/ARCHITECTURE.md`: the services (Back Office, Relay, Registry,
    patient and provider apps, dashboards) and the invariants;
  - `docs/SECURITY.md`: the threat model and the audit bar (HIPAA, OWASP
    ASVS L2, MASVS L2);
  - `docs/V1_PLAN.md`: scope and order;
  - `docs/OBSERVABILITY.md`: telemetry and audit export;
  - `docs/LIT_REVIEW.md`: add an entry before designing anything new.

## Commands

- Build server: `make build` → `bin/wehelpd`
- Test/vet: `make test` (runs with `-race`), `make vet`. CI runs go, ios,
  docker and openapi on every PR, and all four are required to merge.
- Run with local Postgres: `make compose-up` then `make run` (or `wehelpd serve`)
- Migrations: Flyway. `make db-migrate` (containerized) or `wehelpd migrate`
  (needs flyway CLI on PATH). The compose `migrate` service runs before
  `server` starts, so `make compose-up` self-migrates.
- New migration: add `V{N}__name.sql` to `db/migrations/` — Flyway naming,
  forward-only, never edit an applied migration (checksums are enforced).
  Community Flyway has no undo; put manual rollback SQL in a comment.
- iOS: `make ios` (xcodegen + open Xcode), `make ios-build` (headless compile check)
- Never edit `ios/WeHelp.xcodeproj` directly — it's generated from `ios/project.yml`

## Conventions

- Go: cobra for CLI, viper for config (`WEHELP_*` env vars), chi for HTTP,
  pgx/v5 for Postgres, Flyway for migrations, slog for logging.
- Config keys live in `internal/config/config.go` defaults — update there, not
  just in docs.
- Financial/audit data: append-only tables only; use `ledger.Post` and
  `audit.Record` inside a caller-managed `pgx.Tx`. Never UPDATE ledger or
  audit tables — DB triggers forbid it.
- Every PHI-touching write must be wrapped in a transaction that also calls
  `audit.Record`.
- **Patient data lives only in the Back Office** (and on the patient's phone
  while displayed). Never add PHI to the Relay, the Registry, dashboards,
  logs or push payloads. Logs carry IDs only.
- API routes: every route change updates `api/openapi.yaml` in the same PR.
  `TestOpenAPICoversRoutes` fails if the spec and the router differ.
- Patients have no passwords in V1: the Secure Enclave device key is their
  credential. Don't add patient password flows or direct LAN phone access.
- Git: ONE-LINE commit subjects only — context goes in CHANGELOG.md, not the
  commit body.
- Git: never commit/push to `main` — every change lands via a feature
  branch + PR.
- Scale budget: the Back Office is an office appliance serving ~500
  patients (see docs/ARCHITECTURE.md). Prefer boring code. No new
  infrastructure beyond what ARCHITECTURE.md lists (Postgres, the Relay, the
  Registry, the observability sidecar) without issue-level justification.
- Changelog: every PR-worthy change gets a `CHANGELOG.md` entry (Keep a
  Changelog format).
- Shell: NEVER use heredocs. Write content to a file with file tools first,
  then rename/reference it.
- Swift: iOS 17+, SwiftUI, `@Observable` for state, `async/await` for
  networking. Health data goes through HealthKit, not raw BLE, unless a
  device requires it.
