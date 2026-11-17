-- Form assignments and their occurrences (#196). Built in wehelpd with
-- ordinary tables for now (owner decision, 2026-11-16); it moves to the data
-- module (#227) with the rest of forms.
--
-- An assignment gives a patient a form once or on a schedule. Each due
-- instance is an occurrence with a window: it opens at opens_at (inclusive)
-- and closes at closes_at (exclusive). Recurrence follows the practice's
-- calendar in the time zone pinned on the assignment: due dates advance by
-- local days or weeks, and every time is 09:00 local (docs/V2_CONTRACTS.md,
-- "Forms"). One occurrence per assignment and due date, so the runner can
-- run again and again without duplicates. At most 366 occurrences per
-- assignment, and recurring schedules must end (a count or a date).
--
-- Who and which form are operational; answers come with responses (#197).
-- Assignments are ended, never deleted.
--
-- Manual rollback:
--   drop table form_occurrences;
--   drop table form_assignments;

create table form_assignments (
    id               uuid primary key default gen_random_uuid(),
    tenant_id        uuid not null references tenants (id),
    patient_id       uuid not null references users (id),
    form_id          uuid not null references forms (id),
    version_id       uuid references form_versions (id), -- null: the latest published, fixed per occurrence
    schedule_kind    text not null check (schedule_kind in ('once', 'days', 'weeks')),
    every            int not null default 1 check (every between 1 and 365),
    start_date       date not null,
    end_count        int check (end_count between 1 and 366),
    end_date         date,
    open_before_days int not null default 0 check (open_before_days between 0 and 365),
    close_after_days int not null default 3 check (close_after_days between 0 and 365),
    timezone         text not null,               -- the practice's IANA zone, pinned
    channel          text not null default 'app' check (channel in ('app', 'kiosk')),
    assigned_by      uuid not null references users (id),
    created_at       timestamptz not null default now(),
    ended_at         timestamptz,
    ended_by         uuid references users (id),
    check (open_before_days + close_after_days > 0),
    check (schedule_kind = 'once' or end_count is not null or end_date is not null),
    check (end_date is null or end_date >= start_date)
);
create index form_assignments_patient_idx on form_assignments (tenant_id, patient_id, created_at desc);
create index form_assignments_active_idx on form_assignments (tenant_id) where ended_at is null;

create table form_occurrences (
    id                uuid primary key default gen_random_uuid(),
    tenant_id         uuid not null references tenants (id),
    assignment_id     uuid not null references form_assignments (id),
    patient_id        uuid not null references users (id),
    form_id           uuid not null references forms (id),
    version_id        uuid not null references form_versions (id),
    due_date          date not null,              -- the scheduled local date: the occurrence's identity
    opens_at          timestamptz not null,
    due_at            timestamptz not null,
    closes_at         timestamptz not null,
    status            text not null default 'open' check (status in ('open', 'completed', 'missed', 'cancelled')),
    reminded_open_at  timestamptz,               -- form_due sent when it opened
    reminded_close_at timestamptz,               -- form_due sent 24 hours before it closes
    created_at        timestamptz not null default now(),
    completed_at      timestamptz,
    unique (assignment_id, due_date),
    check (opens_at < closes_at)
);
create index form_occurrences_patient_idx on form_occurrences (tenant_id, patient_id, due_at);
create index form_occurrences_open_idx on form_occurrences (closes_at) where status = 'open';

revoke delete, truncate on form_assignments, form_occurrences from wehelp_app;
