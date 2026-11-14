# WeHelp Relay runbook

The Relay (`wehelp-relay`, #4) connects phones to their office without being
able to read or impersonate it. See ARCHITECTURE.md ("Connections", "Relay
internals") and SECURITY.md ("Back Office ↔ Relay").

```
phone ──TLS──► Relay :8443   CONNECT <office-id>.wehelp.invalid:443
                 │ splice (ciphertext only)
                 ▼
          "wehelp-phone" SSH channel ◄── office's outbound SSH to Relay :2222
                                             │
                                             ▼
                              wehelpd: TLS 1.3 with the office key → API
```

- **Offices** dial out over SSH. They authenticate with the office SSH key,
  use their office ID as the SSH user, and pin the Relay's host key from
  their registration. The Relay accepts a key only if it is on the current
  signed office list for this Relay. It opens one channel type,
  `wehelp-phone`, and accepts one from offices, `wehelp-site`, to publish
  the practice website (#97).
- **Phones** connect with TLS 1.3 to the Relay (pinning the Relay TLS key)
  and send `CONNECT <office-id>.wehelp.invalid:443`. Inside, they run TLS
  again, end to end with the office, pinning the office key. The Relay only
  splices bytes.
- **Office list:** the Relay fetches `{registry_url}/v1/office-list` every
  minute and verifies the ed25519 signature with the pinned Registry key.
  - A same-version list is ignored.
  - An older list is rejected and logged.
  - A newer list replaces the current one and disconnects any office no
    longer on it.
  - The last good envelope is cached in `office-list.json` and re-verified
    at start, so a Registry outage changes nothing.
- **Limits:** 64 concurrent phone connections per office. A splice closes 30
  seconds after one side finishes. SSH keepalives run every 30 seconds both
  ways. Offices reconnect with jittered backoff (1 to 30 seconds).
- **Logs:** office IDs, byte counts and durations, plus the key fingerprint
  of refused offices. No device addresses: the HTTP server's own error log,
  which would name client IPs, is discarded.

## Set up (dev laptop, Demo topology)

```bash
make build
bin/wehelp-relay init      # ~/.wehelp/relay: SSH host key, TLS key (prints its pin); never replaces a key
bin/wehelp-registry relay add --name laptop \
  --ssh-address 192.168.1.20:2222 --connect-address 192.168.1.20:8443 \
  --host-key-file ~/.wehelp/relay/ssh_host_ed25519.pub \
  --tls-pin <tls_key_pin from init>                            # prints the relay ID
export WEHELP_RELAY_ID=<relay id>
export WEHELP_RELAY_REGISTRY_KEY="$(bin/wehelp-registry pubkey)"
export WEHELP_REGISTRY_URL=http://127.0.0.1:8090
bin/wehelp-relay serve
```

| Key | Default | Notes |
|---|---|---|
| `relay_dir` | `~/.wehelp/relay` | Host key and TLS key (mode 600), list cache |
| `relay_id` | (required) | From `wehelp-registry relay add`; only offices assigned to it are accepted |
| `relay_registry_key` | (required) | The pinned Registry signing key, `ed25519:…` |
| `registry_url` | `http://localhost:8090` | Use HTTPS outside development |
| `relay_ssh_addr`, `relay_connect_addr` | `:2222`, `:8443` | |
| `relay_list_interval` | `1m` | |
| `relay_apns_key` | (empty = push off) | The app publisher's APNs `.p8` key, mode 600, held only on the Relay (#7) |
| `relay_apns_key_id`, `relay_apns_team` | | Key ID and team ID from the Apple developer account |
| `relay_apns_topic` | `com.zacelston.wehelp` | The app's bundle ID |
| `relay_site_addr` | `:8444` | HTTPS listener for practice websites (#97); empty = off |
| `relay_site_public_url` | (empty) | e.g. `https://192.168.1.20:8444`; told to offices so staff see the full link |
| `relay_site_cert`, `relay_site_key` | (empty = the Relay TLS cert) | A real certificate for the site listener; key mode 600 |

The Back Office opens its tunnel by itself from `wehelpd serve` once it is
installed (#35). The settings are `relay_tunnel` (default on) and
`relay_address_override`, which points the tunnel somewhere other than the
registered Relay SSH address (demos, port forwards).

## Checking it

```bash
# A request through the Relay to the office (curl doesn't check our pins; the app does):
curl --proxy https://192.168.1.20:8443 --proxy-insecure -k https://<office-id>.wehelp.invalid/api/v1/ping
```

On 2026-11-14 in the Demo topology (Relay on the laptop, Barnacle as the
office):
- Barnacle connected as soon as its server started.
- `/api/v1/ping` answered through the Relay. The outer certificate was the
  Relay's, the inner one Barnacle's office certificate.
- `practice suspend` disconnected Barnacle 18 s later.
- `practice reactivate` brought it back at the next list and reconnect
  attempt.

The automated version is `internal/tunnel/e2e_test.go`. It also checks that
none of the request or response plaintext appears in the bytes the Relay
handles.

## Practice websites (#97)

- Staff press **Save and publish** on the Back Office Website tab. The
  office opens a `wehelp-site` channel, writes one tar and reads a JSON
  reply. The Relay stores it under the office ID the SSH connection
  authenticated as, in `{relay_dir}/sites/<office-id>/`, swapping the whole
  directory at once.
- It refuses anything but `.html .css .txt .pdf .png .jpg .jpeg .webp
  .ico`, more than 200 files, 10 MB per file or 25 MB in all, HTML
  containing `<script`, and a second publish within 5 seconds.
- It serves `GET`/`HEAD` of `/s/<office-id>/…` only, for offices on the
  current list, with `Content-Security-Policy: default-src 'none'; style-src
  'self'; img-src 'self'`, `nosniff` and `X-Frame-Options: DENY`. Downloads
  are sent as attachments. Visitor addresses are not logged.
- With no `relay_site_cert`, browsers warn about the Relay's self-signed
  certificate; that is expected in the Demo topology.

```bash
curl -k https://192.168.1.20:8444/s/<office-id>/store.html
```

## Rotating Relay keys

- **Host key:**
  1. Move `ssh_host_ed25519*` aside and run `wehelp-relay init`.
  2. Register the new public key with the Registry. Relay key updates go
     through WeHelp support (#61); in V1-M2, add a new Relay record and
     reassign the practice.
  3. Every office on this Relay reinstalls with a new token so it pins the
     new host key (OFFICE_KEYS.md).
- **TLS key:**
  1. Move `tls.key` and `tls.crt` aside and run `init`.
  2. `wehelp-registry relay set-tls-pin <name> <new pin>`, then reinstall
     each office on this Relay so its pairing identity carries the new pin.
  3. Phones pin the Relay key, so they must be re-paired (#37).
