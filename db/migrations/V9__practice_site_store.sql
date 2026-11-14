-- Practice website, library and supply store (#97).
--
-- site_profiles and site_files hold the public website the Back Office
-- renders and publishes to the Relay. They are practice content, never
-- patient data. products is the supply catalogue shown on the site and in
-- the app. orders and order_items are patient data: they stay here, are
-- placed only through the app's pinned tunnel, and every change is audited.
--
-- Manual rollback:
--   drop table order_items; drop table orders; drop table products;
--   drop table site_files; drop table site_profiles;

create table site_profiles (
    tenant_id     uuid primary key references tenants (id),
    -- about, bios, services, hours, contact, join text, podcasts: see
    -- internal/site.Content. Validated by the server.
    content       jsonb not null default '{}',
    updated_at    timestamptz not null default now(),
    updated_by    uuid references users (id),
    published_at  timestamptz,
    published_url text not null default ''
);

create table site_files (
    id           uuid primary key default gen_random_uuid(),
    tenant_id    uuid not null references tenants (id),
    kind         text not null check (kind in ('library', 'image')),
    title        text not null check (length(title) between 1 and 200),
    -- Published as files/<filename>; lower case, no path.
    filename     text not null check (filename ~ '^[a-z0-9][a-z0-9._-]{0,99}$'),
    content_type text not null,
    data         bytea not null check (octet_length(data) <= 10485760),
    created_at   timestamptz not null default now(),
    unique (tenant_id, filename)
);

create table products (
    id          uuid primary key default gen_random_uuid(),
    tenant_id   uuid not null references tenants (id),
    sku         text not null check (length(sku) between 1 and 40),
    name        text not null check (length(name) between 1 and 120),
    description text not null default '' check (length(description) <= 1000),
    category    text not null default '' check (length(category) <= 60),
    unit        text not null default '' check (length(unit) <= 60), -- "60 capsules", "100 g"
    price_cents integer not null check (price_cents between 0 and 1000000),
    active      boolean not null default true,
    sort        integer not null default 0,
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now(),
    unique (tenant_id, sku)
);

create table orders (
    id          uuid primary key default gen_random_uuid(),
    tenant_id   uuid not null references tenants (id),
    patient_id  uuid not null references users (id),
    status      text not null default 'placed'
                check (status in ('placed', 'ready', 'fulfilled', 'cancelled')),
    note        text not null default '' check (length(note) <= 500),       -- the patient's
    staff_note  text not null default '' check (length(staff_note) <= 500), -- shown to the patient
    total_cents integer not null check (total_cents >= 0),
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now(),
    closed_at   timestamptz,
    check ((status in ('fulfilled', 'cancelled')) = (closed_at is not null))
);
create index orders_status_idx on orders (tenant_id, status, created_at desc);
create index orders_patient_idx on orders (tenant_id, patient_id, created_at desc);

create table order_items (
    tenant_id        uuid not null references tenants (id),
    order_id         uuid not null references orders (id),
    product_id       uuid not null references products (id),
    -- Copied at order time so later catalogue edits don't change the order.
    name             text not null,
    unit             text not null default '',
    unit_price_cents integer not null check (unit_price_cents >= 0),
    quantity         integer not null check (quantity between 1 and 20),
    primary key (order_id, product_id)
);
