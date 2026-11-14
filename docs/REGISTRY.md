# WeHelp Registry runbook

The Registry (`wehelp-registry`, #60) is WeHelp's control plane. It registers
practices and Relays, issues install tokens, and signs the list of offices
each Relay may accept. It holds business data only, never patient data. See
ARCHITECTURE.md ("Registry internals") and SECURITY.md ("Registry → Relay and
Back Office").

In V1-M2 WeHelp support runs the subcommands below on the Registry host.
Access to that host and its database is the access control until the web
console with password + TOTP lands (#61). Each command records who acted
(`--actor`, default `cli:<os user>`) in the Registry's audit log.

## Set up (dev laptop)

```bash
make registry-db         # Postgres for the Registry on :5433 (compose profile "registry")
make registry-migrate    # Flyway, db/registry
make build               # bin/wehelpd and bin/wehelp-registry
bin/wehelp-registry keygen    # ~/.wehelp/registry/signing.key (mode 600); prints the public key
bin/wehelp-registry serve     # :8090
```

Config (`WEHELP_*` env vars or `wehelp.yaml`; defaults in
`internal/config/config.go`):

| Key | Default | Notes |
|---|---|---|
| `registry_listen_addr` | `:8090` | |
| `registry_database_url` | `postgres://wehelp:wehelp@localhost:5433/wehelp_registry` | Its own database, never the Back Office's |
| `registry_signing_key` | `~/.wehelp/registry/signing.key` | Refused if group or others can read it |
| `registry_tls_cert`, `registry_tls_key` | empty | Empty serves plain HTTP: development only |

## Register a practice and install its office

```bash
bin/wehelp-registry relay add --name laptop \
  --ssh-address 192.168.1.20:2222 --connect-address 192.168.1.20:8443 \
  --host-key-file relay_host_ed25519.pub
bin/wehelp-registry practice add --name "Demo Naturopathy" --relay laptop   # prints the practice ID
bin/wehelp-registry token issue <practice-id>   # prints the token once; valid 24 h, single use
```

Give the token to whoever installs the Back Office. `wehelpd init
--install-token …` (#35) sends it with the office SSH public key to
`POST /v1/install` and stores the office ID, Relay address, Relay host key and
Registry signing key it gets back. Installing again with a new token for the
same practice replaces the office key and keeps the office ID (that is how
the office SSH key is rotated).

```bash
bin/wehelp-registry practice list
bin/wehelp-registry practice suspend <practice-id>      # its office leaves the next list
bin/wehelp-registry practice reactivate <practice-id>
bin/wehelp-registry audit verify
```

## The signed office list

`GET /v1/office-list` returns `{"payload": base64, "signature": base64}`. The
payload is JSON:

```json
{"version": 7, "issued_at": "2026-11-14T03:02:46Z",
 "offices": [{"office_id": "…", "ssh_public_key": "ssh-ed25519 …", "relay_id": "…"}]}
```

A Relay (#4):
1. verifies the ed25519 signature over the decoded payload bytes with the
   Registry key it pinned;
2. rejects any version not greater than its last good list;
3. keeps only the entries whose `relay_id` is its own.

`internal/officelist` does steps 1–2 for both sides. Every version is kept
in `office_lists` (append-only).

## Rotating the signing key

The key signs office lists only. Rotate it if it may have leaked, and on a
schedule before field use with several practices (it then moves to a
hardware-backed key; see LIT_REVIEW.md).

1. Stop `wehelp-registry serve`.
2. Move the old key aside (`mv signing.key signing.key.retired`); keep it
   offline until step 5 is done.
3. `wehelp-registry keygen` and note the new public key.
4. Update the pinned Registry key on every Relay, then start the Registry
   and make any change, so it publishes a list signed by the new key.
   Relays that still pin the old key keep their last good list and reject
   the new one until they are updated, so do this in a maintenance window.
5. Update the Registry key stored on each Back Office (used for the roster,
   #62), then destroy the retired key.

There is no in-band key rollover in V1. If one becomes necessary, the list
would carry the next key signed by the current one.

## Audit

Every change (Relay, practice, token issue, install, suspend, reactivate,
and each list publication) writes an event to `audit_events` in the same
transaction. Events form one hash chain. Each hash covers the previous hash
and every field length-prefixed. Triggers make `audit_events` and
`office_lists` append-only. `wehelp-registry audit verify` recomputes the
chain and names the first broken event.
