# Changelog

All notable changes to WeHelp are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- Contribution rules: one-line commit subjects, notes live in this
  changelog, no heredocs in shell commands (write files instead).
- Auth: register/login/refresh endpoints with argon2id password hashing,
  short-lived HMAC JWT access tokens, and rotating opaque refresh tokens
  stored as SHA-256 hashes (PR #17, issue #1).
- `refresh_tokens` table and unique `tenants.name` (migration 00002).
- `GET /api/v1/me` behind Bearer middleware resolving
  (tenant, user, role) into request context.
- GitHub project board (WeHelp Roadmap, project #5), 5 milestones,
  16 seeded issues, `agent-ready`/`needs-spec` label taxonomy.
- CONTRIBUTING.md, issue/PR templates, AGENTS.md agent bootstrap notes.

### Fixed
- `audit.Record` genesis event: first event in a tenant chain now links to
  an empty bytea instead of violating the `NOT NULL` `prev_hash` column.

## [0.1.0] — 2026-09-14

### Added
- Initial monorepo scaffold: Go backend (cobra/viper/chi/pgx), SwiftUI iOS
  app generated via xcodegen, docker-compose deploy, CI, AGPL-3.0.
- Core schema (migration 00001): tenants, users, provider_patients,
  appointments, messages, double-entry ledger, hash-chained audit_events —
  ledger and audit tables immutable via DB triggers.
