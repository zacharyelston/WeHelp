-- Backup runs (#41). deploy/backup.sh records each nightly run here, so the
-- staff web can warn admins when the last backup failed or is too old.
-- Holds sizes, a checksum and an error message: no patient data.
--
-- Manual rollback:
--   drop table backup_runs;

create table backup_runs (
    id          bigserial primary key,
    started_at  timestamptz not null,
    finished_at timestamptz not null default now(),
    ok          boolean not null,
    bytes       bigint not null default 0,  -- encrypted size
    copied_to   text not null default '',   -- where the off-machine copy went
    manifest    text not null default '',   -- sha256 of the manifest
    error       text not null default ''
);
create index backup_runs_finished_idx on backup_runs (finished_at desc);
