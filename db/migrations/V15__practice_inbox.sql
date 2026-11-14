-- Practice inbox (#110): a patient messages "the practice", and the front
-- desk (office) and admins answer. A table of its own, so the direct
-- provider threads in messages are untouched and nothing leaks between
-- them. Providers do not read it. Patient data: stays in the Back Office,
-- every write audited.
--
-- Manual rollback:
--   drop table practice_messages;

create table practice_messages (
    id           uuid primary key default gen_random_uuid(),
    tenant_id    uuid not null references tenants (id),
    patient_id   uuid not null references users (id),   -- whose thread
    author_id    uuid not null references users (id),   -- the patient, or the staff member who replied
    from_patient boolean not null,
    body         text not null check (length(body) between 1 and 4000),
    read_at      timestamptz,                            -- by the other side
    created_at   timestamptz not null default now()
);
create index practice_messages_thread_idx on practice_messages (tenant_id, patient_id, created_at);
create index practice_messages_unread_idx on practice_messages (tenant_id, from_patient) where read_at is null;
