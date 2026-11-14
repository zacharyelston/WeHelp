-- Registry console (#61): WeHelp support staff, practice details and
-- providers, Relay capacity, and Relay health reports. Business and
-- workforce data only; never patient data.
--
-- Manual rollback:
--   drop table relay_status, practice_providers, console_sessions, console_staff;
--   alter table practices drop column contact_name, drop column contact_email,
--     drop column contact_phone, drop column subscription;
--   alter table relays drop column capacity;

create extension if not exists citext;

-- WeHelp support staff. support: day-to-day (practices, providers, install
-- tokens). registry-admin: also suspend, reactivate and Relay changes.
create table console_staff (
    id              uuid primary key default gen_random_uuid(),
    email           citext not null unique,
    display_name    text not null,
    role            text not null check (role in ('support', 'registry-admin')),
    password_hash   text not null,
    totp_secret     text,        -- set at enrolment; active once totp_enabled_at is set
    totp_enabled_at timestamptz,
    totp_last_step  bigint,      -- a code works once
    failed_logins   int not null default 0,
    locked_until    timestamptz,
    disabled_at     timestamptz,
    created_at      timestamptz not null default now()
);

-- Server-side sessions; only the SHA-256 of the cookie is stored.
-- state 'setup' = password accepted, authenticator not yet enrolled.
create table console_sessions (
    id           uuid primary key default gen_random_uuid(),
    token_hash   bytea not null unique,
    staff_id     uuid not null references console_staff (id),
    state        text not null check (state in ('setup', 'active')),
    csrf_token   text not null,
    created_at   timestamptz not null default now(),
    last_seen_at timestamptz not null default now(),
    expires_at   timestamptz not null,
    revoked_at   timestamptz
);

alter table practices
    add column contact_name  text not null default '',
    add column contact_email text not null default '',
    add column contact_phone text not null default '',
    add column subscription  text not null default 'trial'
        check (subscription in ('trial', 'active', 'past_due', 'cancelled'));

-- A practice's providers and admins as WeHelp knows them (#62 sends this
-- roster to the office). Removed providers are kept for the record.
create table practice_providers (
    id             uuid primary key default gen_random_uuid(),
    practice_id    uuid not null references practices (id),
    name           text not null,
    email          citext not null,
    role           text not null check (role in ('provider', 'admin')),
    credentials    text not null default '', -- e.g. "MD", "NP"
    licence_number text not null default '',
    created_at     timestamptz not null default now(),
    removed_at     timestamptz
);
create unique index practice_providers_email_idx on practice_providers (practice_id, email) where removed_at is null;

alter table relays add column capacity int not null default 50 check (capacity > 0);

-- The latest report from each Relay, signed with its SSH host key: which
-- offices are connected and which list it is using. Office IDs only.
create table relay_status (
    relay_id          uuid primary key references relays (id),
    reported_at       timestamptz not null,
    list_version      bigint not null,
    connected_offices jsonb not null default '[]'
);
