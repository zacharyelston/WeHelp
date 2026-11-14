-- A lost refresh reply must not cost a patient their pairing (#136).
-- Refresh rotates the token; replaced_by points from the old token to the
-- one issued in its place. If the reply never reached the phone, the phone
-- presents the old token again: a device-bound token may be redeemed again
-- while its successor is unused and live (the device key must still sign
-- the request). Logout, device revocation and staff deactivation revoke the
-- live successor, which ends that retry too.
--
-- Manual rollback: alter table refresh_tokens drop column replaced_by;

alter table refresh_tokens
    add column replaced_by uuid references refresh_tokens (id);
