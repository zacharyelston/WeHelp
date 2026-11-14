-- WeHelp Registry schema (#60). Separate database from the Back Office:
-- business and workforce data only, never patient data.
--
-- Manual rollback (Flyway Community has no undo):
--   drop table audit_events, office_lists, install_tokens, offices,
--     practices, relays cascade;
--   drop function forbid_mutation();

create table relays (
    id              uuid primary key default gen_random_uuid(),
    name            text not null unique,
    ssh_address     text not null,          -- host:port offices dial
    connect_address text not null,          -- host:port phones dial (HTTP CONNECT)
    ssh_host_key    text not null,          -- authorized_keys line, pinned by offices
    created_at      timestamptz not null default now()
);

create table practices (
    id         uuid primary key default gen_random_uuid(),
    name       text not null,
    status     text not null default 'active' check (status in ('active', 'suspended')),
    relay_id   uuid not null references relays (id),
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

-- One office (Back Office appliance) per practice in V1. Reinstalling with a
-- new install token replaces the SSH key and keeps the office ID.
create table offices (
    id             uuid primary key default gen_random_uuid(),
    practice_id    uuid not null unique references practices (id),
    ssh_public_key text not null unique,    -- authorized_keys line (ssh-ed25519)
    installed_at   timestamptz not null default now()
);

-- Only the SHA-256 of a token is stored; the token is shown once.
create table install_tokens (
    id          uuid primary key default gen_random_uuid(),
    practice_id uuid not null references practices (id),
    token_hash  bytea not null unique,
    expires_at  timestamptz not null,
    used_at     timestamptz,
    created_by  text not null,
    created_at  timestamptz not null default now()
);

-- Every published, signed office list. The payload bytes are exactly what
-- was signed; Relays verify them against the Registry's ed25519 key.
create table office_lists (
    version    bigint primary key check (version > 0),
    payload    bytea not null,
    signature  bytea not null,
    created_at timestamptz not null default now()
);

-- The Registry's own hash-chained audit log (one chain).
create table audit_events (
    seq           bigserial primary key,
    actor         text not null,            -- e.g. "cli:alice", "install-token:<id>", "system"
    action        text not null,
    resource_type text not null,
    resource_id   text not null default '',
    detail        jsonb not null default '{}',
    prev_hash     bytea not null,
    hash          bytea not null,
    created_at    timestamptz not null default now()
);

create or replace function forbid_mutation() returns trigger as $$
begin
    raise exception '% is append-only; updates and deletes are forbidden', TG_TABLE_NAME;
end;
$$ language plpgsql;

create trigger office_lists_immutable
    before update or delete on office_lists
    for each row execute function forbid_mutation();

create trigger audit_events_immutable
    before update or delete on audit_events
    for each row execute function forbid_mutation();
