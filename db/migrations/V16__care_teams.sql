-- Care teams (#111): a patient-approved group of providers who share one
-- thread with the patient, for continuity of care. Separate from direct
-- provider threads and the practice inbox. Patient data: stays in the Back
-- Office; joins, approvals, removals and every member's read are audited.
-- See docs/PRACTICE_TYPES.md → Care team.
--
-- Manual rollback:
--   drop table care_team_messages; drop table care_team_members;

create table care_team_members (
    id              uuid primary key default gen_random_uuid(),
    tenant_id       uuid not null references tenants (id),
    patient_id      uuid not null references users (id),
    member_id       uuid not null references users (id),   -- a provider
    status          text not null default 'pending' check (status in ('pending', 'active', 'removed')),
    include_history boolean not null default false,        -- may see messages from before they joined
    invited_by      uuid not null references users (id),
    created_at      timestamptz not null default now(),
    approved_at     timestamptz,                           -- the patient said yes (or added them)
    removed_at      timestamptz,
    check ((status = 'active') = (approved_at is not null and removed_at is null)),
    check ((status = 'removed') = (removed_at is not null))
);
-- At most one pending or active membership per patient and provider.
create unique index care_team_members_live_idx on care_team_members (patient_id, member_id) where status <> 'removed';
create index care_team_members_member_idx on care_team_members (member_id) where status <> 'removed';

create table care_team_messages (
    id         uuid primary key default gen_random_uuid(),
    tenant_id  uuid not null references tenants (id),
    patient_id uuid not null references users (id),   -- whose team
    author_id  uuid not null references users (id),   -- the patient or a member
    body       text not null check (length(body) between 1 and 4000),
    created_at timestamptz not null default now()
);
create index care_team_messages_thread_idx on care_team_messages (tenant_id, patient_id, created_at);
