-- Staff invites (#100): an admin adds a provider in the staff web and gets
-- a single-use set-up link; the provider sets their own password and then
-- enrolls TOTP (#38). Only the SHA-256 of the link's token is stored.
-- Accounts are disabled, never deleted.
--
-- Numbered V10 because #97 adds V9.
--
-- Manual rollback:
--   drop table staff_invites;
--   alter table users drop column disabled_at;

alter table users add column disabled_at timestamptz; -- set: no sign-in, no API access

create table staff_invites (
    id           uuid primary key default gen_random_uuid(),
    tenant_id    uuid not null references tenants (id),
    user_id      uuid not null references users (id),
    token_hash   bytea not null unique,
    created_by   uuid not null references users (id),
    created_at   timestamptz not null default now(),
    expires_at   timestamptz not null,
    used_at      timestamptz,          -- the password was set with it
    cancelled_at timestamptz           -- a newer invite replaced it, or the account was disabled
);
create index staff_invites_user_idx on staff_invites (user_id);
