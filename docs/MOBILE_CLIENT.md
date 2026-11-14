# Mobile client contract

What every WeHelp app must do to pair with a practice, reach it, stay signed in and keep patient data safe. The iPhone app (`ios/WeHelp`) is the reference implementation; the Android app (epic #135) implements this document, not the Swift code.

- **Shapes** (fields, types, routes) live in [`api/openapi.yaml`](../api/openapi.yaml).
- **Behaviour** (order of calls, retries, what to store, what to show) lives here.
- **Why** lives in [ARCHITECTURE.md](ARCHITECTURE.md) and [SECURITY.md](SECURITY.md).

"Must" is a requirement; an app that breaks one fails review. "Should" is the default unless an issue says otherwise. Where the iPhone app doesn't yet do what this document says, a note names the issue that tracks it.

## Contents

1. [How an app reaches a practice](#1-how-an-app-reaches-a-practice)
2. [The pairing QR code](#2-the-pairing-qr-code)
3. [Connecting: two pinned TLS hops](#3-connecting-two-pinned-tls-hops)
4. [The device key and its proofs](#4-the-device-key-and-its-proofs)
5. [Pairing](#5-pairing)
6. [Sessions](#6-sessions)
7. [Several practices on one phone](#7-several-practices-on-one-phone)
8. [Errors and what the app shows](#8-errors-and-what-the-app-shows)
9. [Lock and data on the phone](#9-lock-and-data-on-the-phone)
10. [Push](#10-push)
11. [Polling](#11-polling)
12. [Practice words and switches](#12-practice-words-and-switches)
13. [Data formats](#13-data-formats)
14. [Never](#14-never)
15. [Video visits](#15-video-visits)

## 1. How an app reaches a practice

```
app ──TLS 1.3, Relay pin──▶ Relay ──SSH (the office dialled out)──▶ Back Office
 └────────────── TLS 1.3, office pin, end to end ─────────────────────┘
```

The app opens TLS to the Relay, sends an HTTP `CONNECT` for the office, and runs a second TLS session inside that tunnel, end to end with the Back Office. The Relay forwards bytes it cannot read. There is no other path: no direct LAN access, no plain HTTP, no public certificates.

## 2. The pairing QR code

Staff press **Pair phone** in the staff web; the QR code holds JSON (`QRPayload` in `internal/server/pairing.go`):

```json
{"v": 1,
 "relay": "relay.example.com:8443",
 "relay_pin": "<base64, 32 bytes>",
 "office": "<office UUID>",
 "key_pin": "<base64, 32 bytes>",
 "invite": "<22-char base64url>"}
```

An app must accept a payload only if every check passes, and must not touch the network otherwise:

| Field | Rule |
| --- | --- |
| whole text | Trim surrounding whitespace, then parse as JSON |
| `v` | Exactly `1`. Any other version is refused ("update the app") |
| `relay` | `host:port`, split at the **last** colon; host not empty; port 1–65535 |
| `relay_pin`, `key_pin` | Base64 (standard alphabet), trimmed, decoding to exactly 32 bytes |
| `office` | A UUID. Compare and use it lowercased |
| `invite` | 16–64 characters. The Back Office issues 128 random bits as 22 characters of base64url |

The invite is single-use, expires 10 minutes after it was issued, and is bound to one account in the Back Office. The app can't tell any of that from the QR, so it doesn't try: an expired or used invite fails at redemption (§5).

The pins and office ID are not secret. The invite is: don't log it, and drop it once pairing ends either way.

## 3. Connecting: two pinned TLS hops

### The Relay hop

- TCP to `relay`, then TLS **1.3 or later**.
- Accept the Relay only if its leaf certificate's key matches `relay_pin` (below). Ignore the system trust store; there is no "trust anyway".
- Send `CONNECT <office-id>.wehelp.invalid:443 HTTP/1.1` (lowercase office ID).
- The Relay answers 200 and splices the connection, or:

| Relay status | Meaning | App shows |
| --- | --- | --- |
| 404 | Unknown office (malformed host or not on the Relay's list) | Unreachable |
| 502 | Office offline (its tunnel is down) | Unreachable |
| 503 | Office busy (channel limit) | Unreachable; retry later |

### The office hop

- Inside the tunnel, TLS **1.3** with SNI and HTTP `Host` set to `<office-id>.wehelp.invalid`. `.invalid` is a reserved name that never resolves, so nothing can reach it except through the tunnel.
- Accept the office only if its leaf certificate's key matches `key_pin`. Ignore the system trust store. The certificate is self-signed; its name and dates don't matter, the pin does.
- Requests go to `https://<office-id>.wehelp.invalid/api/v1/...`.

### Computing a pin

```
pin = base64( SHA-256( uncompressed X9.63 public key ) )      // 04 || X || Y, 65 bytes for P-256
```

That is the leaf certificate's public key bits, **not** the whole SubjectPublicKeyInfo. On iOS it is `SecKeyCopyExternalRepresentation`; in Go, `ecdsa.PublicKey.Bytes()` (`internal/keyfile.P256Pin`).

> **Android:** OkHttp's `CertificatePinner` and Android's network security config pin the SHA-256 of the whole SPKI, which is a different hash. Using either fails every pin. Take the certificate's `ECPublicKey`, build `04 || X || Y` (each coordinate left-padded to 32 bytes), and hash that. OkHttp also doesn't support a TLS connection to the proxy, so the Relay hop needs a custom socket (#127).

Both checks fail closed. A pin mismatch is reported as a pin mismatch (§8), never retried and never bypassed.

## 4. The device key and its proofs

Each paired account has its own **device key**: P-256 (secp256r1), generated on the phone, used only for signing, never exported.

- **iOS:** `SecureEnclave.P256.Signing.PrivateKey`; the Keychain holds only the Enclave's opaque handle.
- **Android:** Android Keystore, StrongBox when the phone has it, otherwise TEE. A key that is only software-backed is refused (#129).
- Simulators and emulators use a software key in debug builds only.

The public key is sent as **base64 of the DER SubjectPublicKeyInfo** (`publicKey.derRepresentation` on iOS, `PublicKey.getEncoded()` on Android).

A **proof** is a signature over this exact byte string (`auth.DeviceProofMessage`):

```
wehelp-device-v1\n{purpose}\n{token}\n{unix_seconds}
```

- `purpose`: `pair` (redeeming an invite; `token` is the invite), `enroll` (binding a staff login to this device; `token` is the login refresh token) or `refresh` (`token` is the refresh token being presented).
- `unix_seconds`: the phone's current time, in whole seconds. The Back Office refuses timestamps more than **5 minutes** away from its own clock.
- Signature: ECDSA over SHA-256 of the message, **DER-encoded**, then base64 (standard alphabet). On Android, `Signature.getInstance("SHA256withECDSA")` produces DER.
- Sent as `"timestamp": <number>` and `"signature": "<base64>"` next to the other fields of the request.

ECDSA signatures are randomized, so tests check that a signature verifies, not that it equals a fixed value (#141 adds shared vectors).

## 5. Pairing

1. Parse the QR (§2). Generate a new device key for a new local account.
2. `POST /api/v1/pairing/redeem` through the pinned connection with `invite`, `public_key`, `name` (the device's name, shown to staff, e.g. "Jordan's iPhone") and a `pair` proof.
3. On 200 the response has the practice name, the account's display name and role, the device ID and a token pair. Store the refresh token (§6), then call `GET /api/v1/me` for the user ID and role.
4. Save the account (§7) and register for push (§10).

| Result | App shows |
| --- | --- |
| 401 | "This pairing code has expired or was already used. Ask the front desk for a new one." The Back Office gives one answer for unknown, used and expired invites on purpose |
| 409 | "This device is already paired for that account." |
| Pin mismatch, Relay error, network error | The connection error (§8) |

If pairing fails at any step, delete the new key and anything stored for the new account. If redemption succeeded but `GET /me` failed, the invite is already spent: the app should retry `GET /me`, or build the account from the redeem response, rather than throw the new key away (the iPhone app doesn't yet, #144).

Pairing is the only way a patient gets in. There is no patient password, sign-up or "forgot password".

## 6. Sessions

- **Access token:** a bearer JWT, valid 15 minutes by default. Keep it in memory only. Send it as `Authorization: Bearer <token>`.
- **Refresh token:** opaque, valid 30 days by default, **single-use**, bound to this device. Store it encrypted and non-backed-up (§9).

Rules:

1. **Before the first call** with no access token in memory, refresh.
2. **On a 401** from any call, refresh once and retry that call once. Don't loop.
3. **One refresh at a time per account.** A second caller waits for the refresh already running. Two concurrent refreshes with the same token would retire each other.
4. **Keep the old refresh token until the new pair is stored.** If the reply is lost (timeout, Relay blip), present the old token again on the next attempt. The Back Office accepts it once more as long as the token issued in its place was never used, and returns a fresh pair (#136).
5. `expires_in` may be ignored; rule 2 covers expiry.

`POST /api/v1/auth/refresh` takes `refresh_token` and a `refresh` proof. Its 401s carry a `code`:

| `code` | Meaning | App does |
| --- | --- | --- |
| `invalid_token` | Unknown, used or expired token | Sign out of this account (§8) |
| `device_revoked` | The practice revoked this device | Sign out of this account |
| `not_enrolled` | A patient token not bound to a device | Sign out of this account |
| `proof_failed` | The signature didn't verify against the enrolled key | Sign out of this account |
| `proof_timestamp` | The phone's clock is more than 5 minutes off | **Keep the account.** Ask the user to turn on automatic date and time, then retry |
| missing | An older Back Office | Treat as `invalid_token` |
| anything else | A newer Back Office | Treat as `invalid_token` |

Other routes don't carry codes yet; #138 extends them. Until then, branch on status only where this document says to.

**Signing out** of an account from the app: `POST /api/v1/auth/logout` with `refresh_token`. For a device-bound token the Back Office revokes the whole device and all its tokens (it always answers 204). The app then deletes the account, its key and its token locally. The iPhone app has no sign-out button yet; staff revoke devices from the staff web.

## 7. Several practices on one phone

A phone can be paired with several practices, and with one practice as more than one person (a provider who is also a patient). Each pairing is an **account**:

- Identity: (office ID, user ID). Pairing the same person at the same practice again **replaces** the old account and deletes its local secrets. Its device record stays in the Back Office for staff to revoke.
- Each account has its own device key, refresh token and pinned endpoint. Secrets are stored per account and never shared between accounts.
- The account list itself (practice name, display name, role, office endpoint, IDs, paired date) holds no secrets and may live in ordinary app storage.
- Order in the account switcher: admin, then provider, then everyone else; then practice name; then display name, case-insensitively.
- Push tokens are registered with every paired practice (§10).

## 8. Errors and what the app shows

Every failure lands in one of four states. Patient data is never shown from a cache in any of them (§9).

| State | Caused by | App shows | Account |
| --- | --- | --- | --- |
| **Signed out** | A refresh 401 with a sign-out code (§6), or a device key missing from the phone | That this phone was signed out of that practice; pair again at the front desk | Removed, with its key and token |
| **Clock** | Refresh code `proof_timestamp` | Fix the phone's date and time | Kept |
| **Pin mismatch** | Either pin check failed (§3) | Which party (Relay or practice) didn't match; never a way around it | Kept |
| **Unreachable** | Network errors, Relay 404/502/503, timeouts, 5xx | Can't reach the practice; Try again, or switch practice | Kept |

A 403 or 409 from a feature route is not a connection state: show it on the screen that made the call (§12).

Use a request timeout of about 20 seconds. Don't retry automatically in a tight loop; let the user press Try again, or retry on the next screen visit.

## 9. Lock and data on the phone

- **Lock on every trip to the background.** Unlock with the platform's biometric prompt, falling back to the device passcode (iOS `.deviceOwnerAuthentication`; Android `BiometricPrompt` with `DEVICE_CREDENTIAL`).
- **Refuse to run without a device passcode** in release builds, and say why.
- **Hide patient data from the app switcher.** iOS covers the window when the app isn't active; Android sets `FLAG_SECURE` on every screen with patient data.
- **No patient data at rest.** Fetch when a screen is shown, keep it in memory, drop it when the account closes. Nothing with patient data goes to disk, caches, logs, crash reports or the clipboard. Logs carry IDs only.
- **Secrets** (refresh token, device key handle) are readable only after the first unlock, only on this device, and never in backups: iOS Keychain `kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly`; Android Keystore-wrapped encryption with `allowBackup=false` and data extraction rules that exclude them.
- Pause polling while locked (§11).

## 10. Push

- After pairing, and whenever the platform gives a new token, the app sends it to **each** paired practice: `PUT /api/v1/devices/current/push` with the token and environment. Clear it with `DELETE` on the same route when push is turned off.
- The Back Office keeps the token on this device's record; the Relay stores none. A revoked device's token is cleared.
- A notification carries only a fixed category ("New message", "Appointment reminder", "Visit starting", ...) whose text the Relay supplies. No names, times or message text, ever. On tap, the app opens the matching screen and fetches the details through the pinned connection.
- Ask for notification permission after the user is signed in, not on first launch.

The route accepts APNs tokens only today (`environment`: `sandbox` or `production`); #139 makes it platform-neutral and #132 adds FCM.

## 11. Polling

There is no socket for live updates. While the screen is visible and the app is unlocked:

| Screen | Poll every |
| --- | --- |
| Conversation list | 15 seconds |
| An open thread | 10 seconds |

The one exception is a video visit's signal long-poll while the call screen is open (§15).

Stop when the screen goes away, the app goes to the background, or the app locks. Opening a thread marks its incoming messages read, so polling under the lock screen would mark messages read that nobody saw (the iPhone app still does this, #144).

## 12. Practice words and switches

- Call `GET /api/v1/practice` once when an account opens and keep the result in memory. It holds no patient data.
- **Words:** use `type.words` (`patient`, `patients`, `provider`, `providers`) wherever the screen names the people. The API always says patient and provider; the screen says what the practice calls them (a salon says client and stylist).
- **Clinical:** when `type.clinical` is false, hide check-ins and health monitors.
- **Features:** `features` maps each switch to on or off. Hide what is off: `messages`, `requests`, `reminders`, `checkins`, `video`, `store`, `library`, `website`, `careteam`. A route for a switched-off feature answers **409** with the feature's name; an app built before a switch existed still gets a sensible error, but a current app shouldn't offer the action at all.
- **A provider's message mode:** when it is off, a patient's message to that provider gets 409 with a sentence to show as is ("… isn't taking messages in the app. Please message the practice instead."). Outside a provider's office hours the message is accepted and the Back Office adds an automatic reply to the thread, so the next poll shows it.

The iPhone app doesn't follow words or switches yet (#142).

## 13. Data formats

- **Timestamps:** RFC 3339 with an offset (`Z` or `±hh:mm`) and 0 to 9 fractional-second digits. Parse all of these.
- **Dates without time** (appointment request preferences): `YYYY-MM-DD`.
- **IDs:** UUIDs, lowercase on the wire. Compare case-insensitively.
- **Unknown fields:** ignore them. **Unknown enum values:** keep the item and treat the value as "other"; never fail the whole response.
- **Lists:** wrapped in an object (`{"messages": [...], "next_cursor": ...}`), except `GET /api/v1/links`, which returns a bare array.
- **Errors:** `{"error": "...", "code": "..."}`; `error` is for people and may change, `code` is stable but not always present (§6).
- **Requests:** JSON, `Content-Type: application/json`. Send only the fields the spec lists.

## 14. Never

- Never trust the system certificate store, or offer to continue after a pin mismatch.
- Never talk to a Back Office except through its Relay with both pins, even on the office Wi-Fi.
- Never add a patient password, sign-up, or account recovery that skips the front desk.
- Never put patient data in a log, notification, analytics event, crash report, file, cache or backup.
- Never export the device key or share it between accounts.
- Never run two refreshes for one account at once.

## 15. Video visits

A 1:1 video visit between a patient and their provider (#6). Media goes phone to phone, DTLS-SRTP end to end, through the Relay's TURN server. Signaling goes through the Back Office over the two pinned hops, so the Relay can't swap the fingerprints that secure the media. Only when the practice's `video` switch is on (§12).

**Starting and joining**
- Any scheduled appointment can become a video visit. It can be joined from 10 minutes before `starts_at` until 30 minutes after `ends_at`.
- **The provider starts it:** `POST /api/v1/appointments/{id}/visit/start`.
- **The patient** sees "Your provider will start the visit" until the appointment's `visit_status` is `open` (in `GET /appointments`, or after the `visit_starting` push). Then they get **Join video visit**: `POST /api/v1/appointments/{id}/visit/join`. Before that, join returns 409 `not_started`.
- **Codes to handle:** `not_started`, `outside_window`, `not_scheduled` and `ended` (409); `video_unavailable` (503, the practice's Relay connection is down: "The video service isn't reachable right now").
- The answer gives `visit_id`, `role`, `peer_present`, `after`, `ice_servers` and `expires_at`.

**WebRTC**
- `RTCConfiguration`: `iceServers` from the answer, `iceTransportPolicy = relay` (never gather host or reflexive candidates), unified plan, `bundlePolicy = maxBundle`, `rtcpMuxPolicy = require`.
- `RTCCryptoOptions`: enable the SRTP AES-GCM suites.
- Video: front camera, 1280×720 at 30 fps, H.264, degradation preference `balanced`. Audio: echo cancellation on, speaker by default.
- **The provider always makes the offer** and the patient answers. The provider sends a fresh offer with an ICE restart whenever the patient joins or rejoins (`joined`), or asks (`restart`).

**Signals**
- Send: `POST /api/v1/visits/{visit_id}/signals` with `{"type": "offer"|"answer", "sdp"}`, `{"type": "candidate", "candidate": {candidate, sdpMid, sdpMLineIndex}}`, or (patient) `{"type": "restart"}`.
- Receive: `GET /api/v1/visits/{visit_id}/signals?after={seq}` returns at once when there is something newer, otherwise after about 15 seconds, so use a request timeout of at least 20 seconds. Poll again at once with the highest `seq` you got.
  - Only while the call screen is open.
  - The other side's `joined`, `left`, `offer`, `answer`, `candidate`, `restart`, and the Back Office's `ended` and `replaced`.
- `ended: true` or `ended`: the visit is over; show it and close the call.
- `replaced`, or 409 `replaced`: the person joined from another device; stop here.

**Security check:** after the connection is up, compare the remote certificate fingerprint in `getStats` with the `a=fingerprint` in the SDP you received. On a mismatch, hang up and show "This call couldn't be secured."

**Leaving:** `POST /visits/{id}/leave` when the person leaves (the visit stays open to rejoin). The provider may `POST /visits/{id}/end` for both. Ending doesn't change the appointment; staff mark it.

**On the phone:** keep the screen awake during a call. Store nothing about the call. Going to the background drops media; on return, rejoin. No CallKit or background audio in V1.
