-- Push (#7): each device's APNs token, registered by the app through its
-- pinned connection. The Back Office sends (token, category) to its Relay,
-- which supplies fixed, generic text. Not PHI; cleared when the device is
-- revoked or Apple reports the token invalid.
--
-- Manual rollback: alter table devices drop column push_token, drop column push_env;

alter table devices
    add column push_token text,
    add column push_env   text check (push_env in ('sandbox', 'production'));
