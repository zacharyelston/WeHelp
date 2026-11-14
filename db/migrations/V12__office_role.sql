-- Office role (#106): the front desk sees and books the whole practice's
-- schedule, works orders, the store, the website, patients and pairing,
-- but cannot read message threads or manage staff accounts. admin keeps
-- everything plus setup.
--
-- Manual rollback: Postgres cannot drop an enum value. Move any office
-- accounts to another role, then recreate user_role without it.

alter type user_role add value if not exists 'office';
