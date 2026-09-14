-- +goose Up
-- +goose StatementBegin
create extension if not exists citext;
create extension if not exists pgcrypto;

create table tenants (
    id         uuid primary key default gen_random_uuid(),
    name       text not null,
    created_at timestamptz not null default now()
);

create type user_role as enum ('provider', 'patient', 'admin');

create table users (
    id            uuid primary key default gen_random_uuid(),
    tenant_id     uuid not null references tenants (id),
    email         citext not null,
    password_hash text not null,
    role          user_role not null,
    display_name  text not null default '',
    created_at    timestamptz not null default now(),
    unique (tenant_id, email)
);

-- Provider <-> patient relationship with consent state.
create table provider_patients (
    tenant_id   uuid not null references tenants (id),
    provider_id uuid not null references users (id),
    patient_id  uuid not null references users (id),
    status      text not null default 'pending'
                check (status in ('pending', 'active', 'revoked')),
    created_at  timestamptz not null default now(),
    primary key (provider_id, patient_id)
);

create table appointments (
    id          uuid primary key default gen_random_uuid(),
    tenant_id   uuid not null references tenants (id),
    provider_id uuid not null references users (id),
    patient_id  uuid not null references users (id),
    starts_at   timestamptz not null,
    ends_at     timestamptz not null,
    status      text not null default 'scheduled'
                check (status in ('scheduled', 'cancelled', 'completed', 'no_show')),
    created_at  timestamptz not null default now(),
    check (ends_at > starts_at)
);
create index appointments_patient_idx on appointments (tenant_id, patient_id, starts_at);
create index appointments_provider_idx on appointments (tenant_id, provider_id, starts_at);

-- Messages, memos, check-ins, and reminders share one transport table.
-- `metadata` carries kind-specific payloads (e.g. a check-in's scale reading).
create table messages (
    id           uuid primary key default gen_random_uuid(),
    tenant_id    uuid not null references tenants (id),
    sender_id    uuid not null references users (id),
    recipient_id uuid not null references users (id),
    kind         text not null default 'message'
                 check (kind in ('message', 'memo', 'checkin', 'reminder')),
    body         text not null,
    metadata     jsonb not null default '{}',
    read_at      timestamptz,
    created_at   timestamptz not null default now()
);
create index messages_inbox_idx on messages (tenant_id, recipient_id, created_at desc);

-- Internal credit system: double-entry, append-only.
create table ledger_accounts (
    id         uuid primary key default gen_random_uuid(),
    tenant_id  uuid not null references tenants (id),
    owner_id   uuid references users (id), -- null = system account
    name       text not null,
    type       text not null
               check (type in ('asset', 'liability', 'equity', 'revenue', 'expense')),
    currency   char(3) not null default 'CRD',
    created_at timestamptz not null default now()
);

create table ledger_transactions (
    id         uuid primary key default gen_random_uuid(),
    tenant_id  uuid not null references tenants (id),
    memo       text not null default '',
    created_at timestamptz not null default now()
);

create table ledger_entries (
    id             bigserial primary key,
    transaction_id uuid not null references ledger_transactions (id),
    account_id     uuid not null references ledger_accounts (id),
    direction      text not null check (direction in ('debit', 'credit')),
    amount         bigint not null check (amount > 0),
    created_at     timestamptz not null default now()
);
create index ledger_entries_account_idx on ledger_entries (account_id, id);

-- Tamper-evident audit trail: every event links to the previous event's hash.
create table audit_events (
    seq           bigserial primary key,
    tenant_id     uuid not null references tenants (id),
    actor_id      uuid references users (id),
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

create trigger ledger_entries_immutable
    before update or delete on ledger_entries
    for each row execute function forbid_mutation();

create trigger ledger_transactions_immutable
    before update or delete on ledger_transactions
    for each row execute function forbid_mutation();

create trigger audit_events_immutable
    before update or delete on audit_events
    for each row execute function forbid_mutation();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop trigger if exists audit_events_immutable on audit_events;
drop trigger if exists ledger_transactions_immutable on ledger_transactions;
drop trigger if exists ledger_entries_immutable on ledger_entries;
drop function if exists forbid_mutation();
drop table if exists audit_events;
drop table if exists ledger_entries;
drop table if exists ledger_transactions;
drop table if exists ledger_accounts;
drop table if exists messages;
drop table if exists appointments;
drop table if exists provider_patients;
drop table if exists users;
drop type if exists user_role;
drop table if exists tenants;
drop extension if exists pgcrypto;
drop extension if exists citext;
-- +goose StatementEnd
