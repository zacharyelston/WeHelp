# WeHelp Registry runbook

The Registry (`wehelp-registry`, #60) is WeHelp's control plane. It registers
practices and Relays, issues install tokens, and signs the list of offices
each Relay may accept. It holds business data only, never patient data. See
ARCHITECTURE.md ("Registry internals") and SECURITY.md ("Registry → Relay and
Back Office").

WeHelp support works in the **Registry console** (#61) at `/console/`. The
subcommands below still run on the Registry host for setup and for Relays.
Each CLI command records who acted (`--actor`, default `cli:<os user>`) in
the Registry's audit log; console actions record `console:<email>`.

## The console (#61)

```bash
bin/wehelp-registry staff add --email you@wehelp.example --name "Your Name" --role registry-admin
# password from the terminal, or WEHELP_CONSOLE_PASSWORD
```

Open `https://<registry>/console/` and sign in. On the first sign-in, scan
the QR code with an authenticator app. After that every sign-in needs a code,
and each code works once. Five wrong attempts lock the account for 15
minutes. Sessions end after 30 idle minutes or 12 hours.

| Role | Can |
|---|---|
| `support` | create and edit practices: name, admin contact, subscription (trial, active, past due, cancelled); add, edit and remove providers and admins (name, email, credentials, licence number); issue install tokens; see Relays, health and the audit log |
| `registry-admin` | also suspend and reactivate practices, move a practice that isn't installed yet to another Relay, and set Relay capacity |

- **Moving an installed office** to another Relay isn't supported yet. The
  office and its paired phones learned their Relay at install and pairing.
- **Health** comes from the Relays themselves. After each list sync (every
  minute) a Relay posts which list version it uses and which offices are
  connected to `/v1/relay-status`. The report is signed with its SSH host key
  (the key the Registry pins), and a report more than 2 minutes old is
  refused. A Relay that hasn't reported for 3 minutes shows as not
  reporting. It holds office IDs only, with no addresses and no patient data.
  The dashboards stack (#55) will add metrics.
- **Browsers:** the console sets a `__Host-` Secure cookie, so it needs HTTPS
  (`registry_tls_cert` and `registry_tls_key`). For a laptop demo,
  `http://localhost:8090/console/` works in Chrome, which treats localhost
  as secure.

## Provider roster (#62)

Providers and admins added to a practice in the console are its roster.
Each installed Back Office pulls it from `POST /v1/roster` every 15 minutes
(`roster_interval` on the Back Office) and when an admin presses **Sync now**
on the Staff screen.

- The request is signed with the office SSH key the Registry pinned at
  install, and must be within 2 minutes of the Registry's clock. The answer
  holds only that practice's entries and is signed with the Registry key the
  office pinned at install. A suspended practice gets 403.
- Removed providers stay on the roster with `removed_at`, so the Back Office
  flags their account for the practice admin to disable. Nothing is disabled
  or deleted automatically.
- Accounts, passwords and TOTP stay in the Back Office. The roster carries
  names, emails, roles, credentials and licence numbers only.

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
