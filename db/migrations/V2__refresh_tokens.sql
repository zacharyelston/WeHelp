-- Refresh tokens + unique tenant names (required for tenant upsert at
-- register/login). Manual rollback: drop refresh_tokens, drop
-- tenants_name_key.

create unique index tenants_name_key on tenants (name);

create table refresh_tokens (
    id         uuid primary key default gen_random_uuid(),
    user_id    uuid not null references users (id),
    tenant_id  uuid not null references tenants (id),
    token_hash bytea not null unique, -- sha256 of the opaque token
    expires_at timestamptz not null,
    revoked_at timestamptz,
    created_at timestamptz not null default now()
);
create index refresh_tokens_user_idx on refresh_tokens (user_id);
