-- Enrolled patient devices (Secure Enclave P-256 keys) and device-bound
-- refresh tokens. Manual rollback: alter table refresh_tokens drop column
-- device_id; drop table devices.

create table devices (
    id           uuid primary key default gen_random_uuid(),
    tenant_id    uuid not null references tenants (id),
    user_id      uuid not null references users (id),
    public_key   bytea not null unique, -- DER SubjectPublicKeyInfo, P-256
    name         text not null default '',
    created_at   timestamptz not null default now(),
    last_seen_at timestamptz,
    revoked_at   timestamptz
);
create index devices_user_idx on devices (user_id);

-- Null = not device-bound (password login before enrollment, staff web UI).
alter table refresh_tokens add column device_id uuid references devices (id);
create index refresh_tokens_device_idx on refresh_tokens (device_id);
