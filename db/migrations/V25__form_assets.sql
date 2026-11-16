-- Images on form display items (#195). Each belongs to one form version and
-- is named in the definition by asset ("media": {"type": "image", "asset":
-- "map.png"}). Practice content, never patient data. Only PNG and JPEG, at
-- most 10 MiB each, 20 per version and 64 MiB per version, checked by the
-- server (docs/V2_CONTRACTS.md, "Forms"). Video stays out for now.
--
-- A published version's assets are as immutable as its definition: the
-- trigger refuses inserts, updates and deletes on them. A new draft gets
-- copies of the previous version's assets.
--
-- Manual rollback:
--   drop table form_assets;
--   drop function form_assets_immutable();

create table form_assets (
    id              uuid primary key default gen_random_uuid(),
    form_version_id uuid not null references form_versions (id),
    name            text not null check (name ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$'),
    content_type    text not null check (content_type in ('image/png', 'image/jpeg')),
    data            bytea not null check (octet_length(data) between 1 and 10485760),
    sha256          text not null,
    created_by      uuid references users (id),
    created_at      timestamptz not null default now(),
    unique (form_version_id, name)
);

create function form_assets_immutable() returns trigger language plpgsql as $$
declare
    vid uuid;
begin
    if tg_op = 'INSERT' then
        vid := new.form_version_id;
    else
        vid := old.form_version_id;
    end if;
    if exists (select 1 from form_versions where id = vid and status = 'published') then
        raise exception 'form version % is published and its images can''t be changed', vid
            using errcode = 'restrict_violation';
    end if;
    if tg_op = 'DELETE' then
        return old;
    end if;
    return new;
end
$$;

create trigger form_assets_immutable
    before insert or update or delete on form_assets
    for each row execute function form_assets_immutable();

revoke truncate on form_assets from wehelp_app;
