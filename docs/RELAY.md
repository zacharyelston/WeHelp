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

## Logs (#42)

The Relay writes daily log files, `{relay_dir}/logs/relay-YYYY-MM-DD.log`
(mode 600), and deletes files older than `relay_log_days` (default 7). Set
`relay_log_dir` to another directory, or to `-` for stderr when a journal
(journald) keeps them, with its own retention set to 7 days. Lines carry
office IDs, categories and byte counts, never device addresses or patient
data. Startup lines (listeners, the TLS pin) still go to stderr.

## Video-visit TURN (#6)

Video visits send media phone to phone, DTLS-SRTP end to end, forced through
the Relay's TURN server, which forwards ciphertext only. TURN is off until
`relay_turn_public_ip` is set.

| Key | Default | Notes |
|---|---|---|
| `relay_turn_public_ip` | empty (off) | the IPv4 address phones reach; relay addresses use it too |
| `relay_turn_addr` | `:3478` | UDP and TCP |
| `relay_turn_ports` | `49160-49999` | relay port range, UDP |
| `relay_turn_max_visits` | `4` | live visits per office when the office list gives none (#175); the list's `max_visits` wins |
| `relay_turn_max_total_visits` | `0` (no cap) | live visits across every office on this Relay, to protect its bandwidth |
| `relay_turn_secret_file` | `{relay_dir}/turn.secret` | master secret, created on first run, mode 600, never sent anywhere |
| `relay_turn_tls_addr` | empty | TURN over TLS, normally `:443`, for networks that block UDP |
| `relay_turn_hostname` | empty | public DNS name: the Relay gets a Let's Encrypt certificate itself (TLS-ALPN-01 on the TLS port, so no port 80), cached in `relay_acme_dir` |
| `relay_turn_tls_cert`, `relay_turn_tls_key` | empty | instead of the hostname, a certificate from certbot |

- **Credentials:** when an office's tunnel connects, the Relay sends it a
  TURN key derived from the master secret. The office mints per-visit
  credentials from it and never asks the Relay for anything. The Relay
  accepts a credential only while its office is on the signed list, before
  it expires, and if it was minted no more than 12 hours ahead.
- **Limits:** 8 allocations per visit credential, and per office the
  `max_visits` from the signed list (one more than its providers, at least
  4; see [REGISTRY.md](REGISTRY.md#the-signed-office-list)), or
  `relay_turn_max_visits` for a list without it. `relay_turn_max_total_visits`
  caps the whole Relay. The office gets its cap with its TURN key, and again
  whenever a new list changes it, so it can answer `video_busy` before TURN
  refuses. TURN still enforces both caps itself. Peers may only be this Relay's own relay address,
  so TURN can't reach the internet or the Relay's network. Allocations
  live 5 minutes unless refreshed, and permissions 2 minutes.
- **TURN over TLS** needs a publicly trusted certificate, because libwebrtc
  can't pin the Relay's self-signed key. A Relay without a public name (the
  laptop demo) runs UDP and TCP only.
- **Firewall:** open UDP and TCP 3478, UDP 49160-49999, and TCP 443 for TLS.
- **Bandwidth, not CPU, is the cost:** allow about 5 Mbit/s each way per
  concurrent 720p visit. When both phones are in the office, the media goes
  office → Relay → office. A visit costs a few percent of a core (S2).
- **Rotating the master secret:** stop the Relay, delete `turn.secret`, and
  start it. Each office gets its new key on its next connect, so restart
  the offices' tunnels too, or wait for them to reconnect.
- **Logs:** a line a minute with live allocation and visit counts while any
  are live. No addresses or usernames.

```bash
WEHELP_RELAY_TURN_PUBLIC_IP=192.168.1.20 bin/wehelp-relay serve
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
