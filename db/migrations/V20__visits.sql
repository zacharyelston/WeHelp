-- Video visits (#6). Any scheduled appointment can become a video visit
-- when its provider starts one inside the join window. Who joined and left
-- is in the audit log; signaling is held in memory only and never stored.
-- Ending a visit doesn't change the appointment: staff mark its outcome.
--
-- Manual rollback:
--   drop table visits;

create table visits (
    id             uuid primary key default gen_random_uuid(),
    tenant_id      uuid not null references tenants (id),
    appointment_id uuid not null unique references appointments (id),
    turn_token     text not null unique,  -- opaque; in TURN usernames instead of any ID
    status         text not null default 'open' check (status in ('open', 'ended')),
    opened_at      timestamptz not null default now(),
    ended_at       timestamptz,
    ended_by       text check (ended_by in ('provider', 'auto')),
    check ((status = 'ended') = (ended_at is not null))
);
create index visits_open_idx on visits (tenant_id) where status = 'open';
