-- Audit hash version (#28). Version 1 hashed the caller's detail bytes, which
-- jsonb rewrites, so audit.Verify couldn't recompute those hashes. Version 2
-- length-prefixes every field and hashes the detail as jsonb::text. Existing
-- events stay version 1 (checked by their links); new ones are version 2.
-- Adding a column fires no row trigger, so audit_events stays append-only.
--
-- Manual rollback:
--   alter table audit_events drop column hash_version;

alter table audit_events add column hash_version smallint not null default 1
    check (hash_version in (1, 2));
alter table audit_events alter column hash_version set default 2;
