-- Reminders (#19): appointment reminders and check-in cadences, sent as
-- kind='reminder' messages by the Back Office's reminder worker.
--
-- Manual rollback:
--   drop table reminders_sent;
--   alter table provider_patients drop column checkin_every_days,
--                                 drop column checkin_since;

-- Check-in cadence per provider-patient link, set by the provider. Null = off.
-- checkin_since anchors the periods: the first reminder is one period after.
alter table provider_patients
    add column checkin_every_days int check (checkin_every_days between 1 and 90),
    add column checkin_since      timestamptz;

-- One row per reminder sent. The key names the reminder (appointment, start
-- time and offset, or link and period), so a restart or a second worker
-- never sends the same one twice.
create table reminders_sent (
    key        text primary key,
    tenant_id  uuid not null references tenants (id),
    message_id uuid not null references messages (id),
    created_at timestamptz not null default now()
);
