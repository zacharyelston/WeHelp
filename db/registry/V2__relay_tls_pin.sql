-- Relay TLS pin (#36): phones pin the Relay's TLS key as well as the office's.
-- Offices learn it at install and put it in the pairing QR (#37).
--
-- Manual rollback: alter table relays drop column tls_pin;

alter table relays add column tls_pin text not null default '';
