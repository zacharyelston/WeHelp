-- Live video visits per office on the Relay's TURN (#175). Null means
-- automatic: one more than the practice's active providers, at least 4.
-- Support can set a number instead (audited as practice.update).
--
-- Manual rollback: alter table practices drop column turn_max_visits;

alter table practices add column turn_max_visits int check (turn_max_visits between 1 and 200);
