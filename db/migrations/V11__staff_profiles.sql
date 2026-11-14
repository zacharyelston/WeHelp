-- Staff profiles (#102): each staff member edits their own credentials,
-- bio and photo, and chooses to show them on the practice website. The
-- website publish turns these into provider bios. Practice content, never
-- patient data.
--
-- Manual rollback:
--   drop table staff_profiles;

create table staff_profiles (
    user_id         uuid primary key references users (id),
    tenant_id       uuid not null references tenants (id),
    credentials     text not null default '' check (length(credentials) <= 120),
    bio             text not null default '' check (length(bio) <= 3000),
    photo           bytea check (octet_length(photo) <= 2097152),
    photo_type      text check (photo_type in ('image/png', 'image/jpeg', 'image/webp')),
    show_on_website boolean not null default false,
    updated_at      timestamptz not null default now(),
    check ((photo is null) = (photo_type is null))
);
