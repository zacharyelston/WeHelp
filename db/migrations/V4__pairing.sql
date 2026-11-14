-- QR pairing (#37). Patients have no password: staff issue a single-use
-- invite bound to one patient, and the phone redeems it with a new device key
-- within 10 minutes. That enrolls the device and signs the phone in.
--
-- Manual rollback:
--   drop table pairing_invites;
--   update users set password_hash = '!' where password_hash is null;
--   alter table users alter column password_hash set not null;

-- Patients created by staff (and, from now on, all patients) have no password.
alter table users alter column password_hash drop not null;

create table pairing_invites (
    id          uuid primary key default gen_random_uuid(),
    tenant_id   uuid not null references tenants (id),
    patient_id  uuid not null references users (id),
    created_by  uuid not null references users (id),
    code_hash   bytea not null unique,        -- SHA-256 of the invite code; the code is shown once, in the QR
    expires_at  timestamptz not null,         -- redeem deadline (10 minutes)
    redeemed_at timestamptz,
    device_id   uuid references devices (id), -- the phone that redeemed it
    created_at  timestamptz not null default now()
);
create index pairing_invites_patient_idx on pairing_invites (tenant_id, patient_id);
