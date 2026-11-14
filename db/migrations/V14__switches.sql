-- Practice and provider switches (#109).
--
-- practice_settings.features: the admin's overrides of the practice
-- type's default switches, {"messages": false, ...}. See internal/practice.
--
-- provider_message_settings: how each provider takes messages. on: as
-- before; hours: messages arrive, but outside the hours the patient gets
-- the automatic reply and the provider no notification; off: patients can't
-- send them messages.
--
-- Manual rollback:
--   drop table provider_message_settings;
--   alter table practice_settings drop column features;

alter table practice_settings add column features jsonb not null default '{}';

create table provider_message_settings (
    user_id      uuid primary key references users (id),
    tenant_id    uuid not null references tenants (id),
    mode         text not null default 'on' check (mode in ('on', 'hours', 'off')),
    days         int[] not null default '{1,2,3,4,5}',      -- 0 = Sunday, office time
    start_minute int not null default 540 check (start_minute between 0 and 1439),
    end_minute   int not null default 1020 check (end_minute between 1 and 1440),
    auto_reply   text not null default '' check (length(auto_reply) <= 500),
    updated_at   timestamptz not null default now(),
    check (end_minute > start_minute)
);
