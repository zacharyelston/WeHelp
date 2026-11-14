-- Appointment requests (#68): a patient asks for a visit with preferred
-- times; the provider confirms (creating a scheduled appointment) or
-- declines. The patient may cancel while it is pending.
--
-- Manual rollback:
--   drop table appointment_requests;

create table appointment_requests (
    id             uuid primary key default gen_random_uuid(),
    tenant_id      uuid not null references tenants (id),
    patient_id     uuid not null references users (id),
    provider_id    uuid not null references users (id),
    -- 1-3 of {"date": "2026-11-20", "period": "morning|afternoon|evening|any"}
    preferred      jsonb not null,
    reason         text not null default '' check (length(reason) <= 500),
    status         text not null default 'pending'
                   check (status in ('pending', 'confirmed', 'declined', 'cancelled')),
    appointment_id uuid references appointments (id),
    note           text not null default '' check (length(note) <= 500), -- the provider's, on decline
    created_at     timestamptz not null default now(),
    decided_at     timestamptz,
    check ((status = 'confirmed') = (appointment_id is not null))
);
create index appointment_requests_provider_idx on appointment_requests (tenant_id, provider_id, status, created_at);
create index appointment_requests_patient_idx on appointment_requests (tenant_id, patient_id, created_at desc);
