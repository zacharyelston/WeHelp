# Working together: Claude and Codex

Two coding agents, Claude (Claude Code) and Codex, work on WeHelp at the
same time. Each picks up issues, but they share one repository, one `main`
and one owner. This file is how they avoid stepping on each other. Read it
together with [CONTRIBUTING.md](../CONTRIBUTING.md) and [AGENTS.md](../AGENTS.md); where it's
stricter, it wins.

## The short version

1. **Work only on issues in your lane** (`agent:claude` or `agent:codex`).
2. **Claim before you start:** add `in-progress` and comment. Never take an
   issue that's already `in-progress`.
3. **Branch** `claude/<issue>-slug` or `codex/<issue>-slug`. One issue, one
   branch, one PR.
4. **The other agent reviews.** Open the PR with `review:codex` or
   `review:claude`. The owner merges.
5. **Hand off in writing.** When you stop, leave a handoff comment on the
   issue, even if the work is done.

GitHub is the only shared state: labels, issue comments and PRs. There's
no shared board file to edit. The board is a set of queries (below).

## Lanes

Lanes split the work by area, so the two agents rarely touch the same
files. Each open V1 issue, and each V2 issue in review, has one lane label.

| | **Claude** (`agent:claude`) | **Codex** (`agent:codex`) |
|---|---|---|
| **Focus** | Clients, the Relay, transport, demo and field operations, V2 intake and health data | Server hardening, audit, observability, V2 patient records and notebooks |
| **Mostly touches** | `ios/`, `internal/relay/`, `internal/tunnel/`, `deploy/`, `scripts/`, the staff web for its features | `internal/server/` hardening, `internal/audit/`, `internal/registry/`, telemetry config, docs/SECURITY.md and OBSERVABILITY.md |
| **V2 designs** | Questionnaires (#192), health data (#202), private patients (#226), the data module boundary (#227) | Git patient records (#220), notebooks (#212), V2_PLAN.md (#225) |

- **An issue outside your lane:** don't pick it up. If you need it, comment
  on it and ask the owner to move the label.
- **Issues without a lane label** (Post-V1, `needs-spec`) aren't for agents.
- **The owner moves lanes,** not the agents.

## Claiming and handing off

**Claim** (before writing code or a spec):
```
gh issue edit N -R zacharyelston/WeHelp --add-label in-progress
gh issue comment N -R zacharyelston/WeHelp --body "Claimed by <agent>. Branch <branch>. Plan: <one line>."
```
If the label was added by someone else in the meantime (check the issue's
timeline), back off.

**Handoff** (when you stop, finish or get blocked), as an issue comment:
```
Handoff (<agent>, <date>)
State: done, PR #M | in progress | blocked on <what>
Branch: <branch> (pushed: yes/no)
Done: <bullets>
Left: <bullets>
Verified: <commands run and results>
Touches shared files: <migrations, openapi.yaml, router, project.yml>
```
Then remove `in-progress` unless a PR is open for it. An open PR keeps the
claim.

The other agent, or a fresh session of the same agent, must be able to
continue from the handoff alone.

## Reviewing each other

- **Code PRs:** the author adds `review:<other agent>`. The reviewer:
  - reads the issue's acceptance criteria and runs its verification;
  - posts findings as a PR review (comments, not pushes);
  - replaces the label with `reviewed` when there's nothing blocking.

  The author fixes findings on their own branch. **Only the owner merges.**
- **Specs:** the spec-review gate in CONTRIBUTING.md is done by the
  **other** agent. A spec written in the Codex lane is reviewed by Claude,
  and the other way round. The reviewer may flip a V1 issue to
  `agent-ready`. **V2 issues need the owner's approval as well**, and stay
  `spec-review` until then.
- **Never push to the other agent's branch.** Suggest the change in the
  review. If it's large, say so and let the owner decide.

## Shared files and collisions

Some files are touched by almost every PR. Rules for them:

| File | Rule |
|---|---|
| `db/migrations/V{N}__*.sql` | Take the next free number when you create it. **Before asking for merge, merge `main` in and renumber yours if the number is taken.** Never renumber a migration that's on `main`. The same applies to `db/registry/` |
| `CHANGELOG.md` | Add your entry at the top of the right heading under `[Unreleased]`. On a conflict, keep both entries |
| `api/openapi.yaml` | Change only your routes. On a conflict, keep both and run `make test` (`TestOpenAPICoversRoutes`) |
| `internal/server/server.go` (router) | Add routes in one block for your feature. Don't reorder others |
| `ios/project.yml` | Claude lane only. A Codex change that needs it asks in the issue |
| `docs/LIT_REVIEW.md` | Add a new section rather than editing another lane's section |
| `AGENTS.md`, `CONTRIBUTING.md`, this file | Owner's files. Propose changes in a PR that changes nothing else |

**Bring in `main` before review,** not only before merge: `git fetch origin
&& git merge origin/main`, then rerun `make build vet test`. Merge, don't
rebase: **never force-push** a shared branch.

## Shared environments

- **Barnacle (the demo Mac mini), the test phones and the Relay host** are
  touched only when the owner asks in that session. Deploying, seeding or
  installing on them isn't part of any issue's default verification.
- **CI** is shared: don't rerun another agent's failed jobs. Comment on its
  PR instead.

## The board (queries)

```
# My lane, ready to pick up, not claimed (oldest milestone first)
gh issue list -R zacharyelston/WeHelp -l agent:claude -l agent-ready --search "-label:in-progress"

# What each agent is doing now
gh issue list -R zacharyelston/WeHelp -l in-progress

# PRs waiting for my review
gh pr list -R zacharyelston/WeHelp -l review:claude

# Specs I should review (written in the other lane)
gh issue list -R zacharyelston/WeHelp -l agent:codex -l spec-review
```
Use `agent:codex` and `review:codex` for Codex.

## Rules that stay as they are

AGENTS.md and CONTRIBUTING.md still apply to both agents:
- Never commit to `main`.
- One-line commit subjects.
- No heredocs.
- No PHI outside the Back Office.
- Don't pick up V2 work unless the owner asks for it.
- Every route change updates `api/openapi.yaml`.
- Every PR-worthy change gets a CHANGELOG entry.
