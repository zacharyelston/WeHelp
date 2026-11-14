-- Staff sign-in (#38): password + TOTP, server-side cookie sessions with a
-- CSRF token, and a lockout after repeated failures.
--
-- Manual rollback:
--   drop table staff_sessions;
--   alter table users drop column totp_secret, drop column totp_enabled_at,
--     drop column totp_last_step, drop column failed_logins, drop column locked_until;

alter table users
    add column totp_secret     text,        -- base32; set at setup, active once totp_enabled_at is set
    add column totp_enabled_at timestamptz,
    add column totp_last_step  bigint,      -- last accepted 30-second step: a code works once
    add column failed_logins   int not null default 0,
    add column locked_until    timestamptz;

-- One row per staff browser session. Only the SHA-256 of the cookie value is
-- stored. state 'setup' = password accepted, TOTP not yet enrolled (only the
-- TOTP setup routes work); 'active' = fully signed in.
create table staff_sessions (
    id           uuid primary key default gen_random_uuid(),
    token_hash   bytea not null unique,
    user_id      uuid not null references users (id),
    tenant_id    uuid not null references tenants (id),
    state        text not null check (state in ('setup', 'active')),
    csrf_token   text not null,
    created_at   timestamptz not null default now(),
    last_seen_at timestamptz not null default now(),
    expires_at   timestamptz not null,       -- absolute limit
    revoked_at   timestamptz
);
create index staff_sessions_user_idx on staff_sessions (user_id);
