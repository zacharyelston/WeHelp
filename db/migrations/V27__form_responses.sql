-- Form responses (#197). Built in wehelpd with ordinary tables for now
-- (owner decision, 2026-11-16); it moves to the data module (#227) later.
--
-- Answers are PHI and stay in the Back Office. A patient saves a response
-- page by page and submits it once.
--   form_responses: one fill of an occurrence. While in progress only its
--     revision changes. Submitting freezes it with the canonical answers
--     (hidden items dropped), the scores and the flags; after that, and
--     after it expires, the trigger refuses every change.
--   form_answer_events: append-only (trigger plus revoked privileges, like
--     audit_events). One row per page save; the latest per page is the
--     page's answers. Each carries the client's idempotency key and a digest
--     of the body, so a retried save is recognised and a reused key with a
--     different body is refused.
--   form_response_reads: append-only. The first staff open of a response
--     (the "new" badge); not a clinical review.
--
-- Manual rollback:
--   drop table form_response_reads;
--   drop table form_answer_events;
--   drop table form_responses;
--   drop function form_responses_frozen();
--   drop function form_append_only();

create table form_responses (
    id            uuid primary key default gen_random_uuid(),
    tenant_id     uuid not null references tenants (id),
    occurrence_id uuid references form_occurrences (id), -- null only for later kiosk walk-ins
    patient_id    uuid not null references users (id),
    version_id    uuid not null references form_versions (id),
    channel       text not null check (channel in ('app', 'kiosk')),
    device_id     uuid,
    status        text not null default 'in_progress' check (status in ('in_progress', 'submitted', 'expired')),
    revision      int not null default 0,
    started_at    timestamptz not null default now(),
    updated_at    timestamptz not null default now(),
    submitted_at  timestamptz,
    answers       jsonb,                      -- the submitted, canonical answers
    scores        jsonb,                      -- {"id": {"value": "12", "band": "mild"}, ...}
    flags         jsonb not null default '[]',
    check ((status = 'submitted') = (submitted_at is not null))
);
create unique index form_responses_one_open_idx on form_responses (occurrence_id) where status = 'in_progress';
create unique index form_responses_one_submitted_idx on form_responses (occurrence_id) where status = 'submitted';
create index form_responses_patient_idx on form_responses (tenant_id, patient_id, submitted_at desc);

create function form_responses_frozen() returns trigger language plpgsql as $$
begin
    if tg_op = 'DELETE' then
        raise exception 'form responses are never deleted' using errcode = 'restrict_violation';
    end if;
    if old.status <> 'in_progress' then
        raise exception 'form response % is %, and can''t be changed', old.id, old.status
            using errcode = 'restrict_violation';
    end if;
    return new;
end
$$;

create trigger form_responses_frozen
    before update or delete on form_responses
    for each row execute function form_responses_frozen();

create table form_answer_events (
    id              bigserial primary key,
    tenant_id       uuid not null references tenants (id),
    response_id     uuid not null references form_responses (id),
    page_id         text not null,
    answers         jsonb not null,
    revision        int not null,             -- the response's revision after this save
    idempotency_key uuid not null,
    body_digest     text not null,
    device_id       uuid,
    recorded_at     timestamptz not null default now(),
    unique (response_id, idempotency_key),
    unique (response_id, revision)
);
create index form_answer_events_page_idx on form_answer_events (response_id, page_id, id desc);

create table form_response_reads (
    response_id uuid not null references form_responses (id),
    user_id     uuid not null references users (id),
    read_at     timestamptz not null default now(),
    primary key (response_id, user_id)
);

create function form_append_only() returns trigger language plpgsql as $$
begin
    raise exception '% is append-only', tg_table_name using errcode = 'restrict_violation';
end
$$;

create trigger form_answer_events_append_only
    before update or delete on form_answer_events
    for each row execute function form_append_only();
create trigger form_response_reads_append_only
    before update or delete on form_response_reads
    for each row execute function form_append_only();

revoke delete, truncate on form_responses from wehelp_app;
revoke update, delete, truncate on form_answer_events, form_response_reads from wehelp_app;
