-- Practice settings (#108): the practice type, which sets the words the
-- apps and website use and whether the practice is clinical. One row per
-- tenant; a tenant without a row is the default type (naturopath). The
-- feature switches of #109 will join this table.
--
-- Manual rollback:
--   drop table practice_settings;

create table practice_settings (
    tenant_id     uuid primary key references tenants (id),
    practice_type text not null default 'naturopath'
                  check (practice_type in ('naturopath', 'therapy', 'allied_health', 'salon')),
    updated_at    timestamptz not null default now(),
    updated_by    uuid references users (id)
);
