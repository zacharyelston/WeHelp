-- +goose Up
-- +goose StatementBegin
-- Tenants are looked up by name at register/login time.
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
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists refresh_tokens;
drop index if exists tenants_name_key;
-- +goose StatementEnd
