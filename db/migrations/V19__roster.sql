-- Provider roster (#62): the providers and admins the Registry lists for this
-- practice, as last pulled. Business data only. Accounts and credentials stay
-- in users; an entry links to an account by email. A provider removed in the
-- Registry keeps their entry with removed_at set, so the admin sees a flag
-- and disables the account themselves; nothing is deleted automatically.
--
-- Manual rollback:
--   drop table roster_sync, roster_entries;

create table roster_entries (
    registry_id    uuid primary key,          -- practice_providers.id in the Registry
    tenant_id      uuid not null references tenants (id),
    name           text not null,
    email          citext not null,
    role           text not null check (role in ('provider', 'admin')),
    credentials    text not null default '',
    licence_number text not null default '',
    removed_at     timestamptz,               -- removed in the Registry
    first_seen_at  timestamptz not null default now(),
    updated_at     timestamptz not null default now()
);
create index roster_entries_tenant_idx on roster_entries (tenant_id, email);

-- The last pull per practice: when it last worked, and the last error.
create table roster_sync (
    tenant_id    uuid primary key references tenants (id),
    synced_at    timestamptz,
    attempted_at timestamptz not null,
    error        text not null default ''
);
