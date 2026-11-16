-- Forms and their versions (#194). A form is practice content (an intake
-- form, or a built-in instrument such as PHQ-9); a version is one
-- wehelp.form.v1 definition (internal/forms). No patient data: assignments
-- and responses come later.
--
-- Placement: in wehelpd for now (owner, 2026-11-16), in its own tables and
-- route group so it can move to the data module (#227) later. The runtime
-- role is wehelp_app (V23).
--
-- A draft is editable; publishing freezes it. The trigger forbids any
-- UPDATE or DELETE of a published version, and the partial unique index
-- allows at most one draft per form. Versions are never deleted.
--
-- Manual rollback:
--   drop table form_versions;
--   drop table forms;
--   drop function form_versions_immutable();

create table forms (
    id             uuid primary key default gen_random_uuid(),
    tenant_id      uuid not null references tenants (id),
    name           text not null check (length(name) between 1 and 200),
    kind           text not null default 'custom' check (kind in ('custom', 'instrument')),
    instrument_key text,                       -- built-ins only, e.g. 'phq9'
    created_by     uuid references users (id), -- null for built-ins
    created_at     timestamptz not null default now(),
    archived_at    timestamptz,
    check ((kind = 'instrument') = (instrument_key is not null))
);
create index forms_tenant_idx on forms (tenant_id, created_at);
create unique index forms_instrument_idx on forms (tenant_id, instrument_key) where instrument_key is not null;

create table form_versions (
    id           uuid primary key default gen_random_uuid(),
    form_id      uuid not null references forms (id),
    version      int not null check (version >= 1),
    status       text not null default 'draft' check (status in ('draft', 'published')),
    definition   jsonb not null,
    digest       text not null,               -- sha256 of the canonical definition JSON
    created_by   uuid references users (id),
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),
    published_at timestamptz,
    published_by uuid references users (id),
    unique (form_id, version),
    check ((status = 'published') = (published_at is not null))
);
create unique index form_versions_one_draft_idx on form_versions (form_id) where status = 'draft';

create function form_versions_immutable() returns trigger language plpgsql as $$
begin
    if old.status = 'published' then
        raise exception 'form version % is published and can''t be changed', old.id
            using errcode = 'restrict_violation';
    end if;
    if tg_op = 'DELETE' then
        return old;
    end if;
    return new;
end
$$;

create trigger form_versions_immutable
    before update or delete on form_versions
    for each row execute function form_versions_immutable();

-- Forms are archived and versions published, never deleted.
revoke delete, truncate on forms, form_versions from wehelp_app;
