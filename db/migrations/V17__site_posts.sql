-- Practice blog (#121): timely and seasonal posts on the practice website.
-- Practice content only, never patient data. A post goes on the site when it
-- isn't a draft and publish_at has passed; the Back Office republishes the
-- site by itself when a scheduled post comes due.
--
-- Manual rollback:
--   drop table site_posts;

create table site_posts (
    id          uuid primary key default gen_random_uuid(),
    tenant_id   uuid not null references tenants (id),
    title       text not null check (length(title) between 1 and 200),
    body        text not null check (length(body) between 1 and 20000),
    image_file  text not null default '',          -- an uploaded site image's filename, or ''
    author_id   uuid not null references users (id),
    draft       boolean not null default false,
    publish_at  timestamptz not null default now(),
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now()
);
create index site_posts_due_idx on site_posts (tenant_id, publish_at desc) where not draft;
