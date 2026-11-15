-- A runtime role for the server (#161). Migrations run as the owner
-- (wehelp); the server connects as wehelp_app, which can read and write data
-- but can't change the schema, and can't UPDATE or DELETE the append-only
-- audit and ledger tables even if their triggers were disabled.
--
-- The role is created here without a password (NOLOGIN) if it doesn't exist;
-- deploy/appliance.sh gives it LOGIN and a per-install password from
-- deploy/db.env. Roles are cluster-wide, so this is safe to run in every
-- database on a cluster (tests create many).
--
-- Manual rollback:
--   revoke all on all tables in schema public from wehelp_app;
--   revoke all on all sequences in schema public from wehelp_app;
--   revoke usage on schema public from wehelp_app;
--   (on Postgres < 15 only) grant create on schema public to public;
--   alter default privileges in schema public revoke all on tables from wehelp_app;
--   alter default privileges in schema public revoke all on sequences from wehelp_app;

do $$
begin
    if not exists (select from pg_roles where rolname = 'wehelp_app') then
        create role wehelp_app nologin;
    end if;
end
$$;

-- Postgres 15+ already denies CREATE on public to everyone but the owner;
-- older servers grant it to PUBLIC. Make it the same everywhere.
revoke create on schema public from public;
grant usage on schema public to wehelp_app;
grant select, insert, update, delete on all tables in schema public to wehelp_app;
grant usage, select on all sequences in schema public to wehelp_app;

-- Append-only by privilege as well as by trigger.
revoke update, delete, truncate on audit_events, ledger_entries, ledger_transactions from wehelp_app;

-- Tables and sequences that later migrations create (as the owner) get the
-- same grants. Later append-only tables must revoke update and delete again.
alter default privileges in schema public grant select, insert, update, delete on tables to wehelp_app;
alter default privileges in schema public grant usage, select on sequences to wehelp_app;
