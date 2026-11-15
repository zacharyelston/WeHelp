# OWASP ASVS 5.0 Level 2 checklist

WeHelp against the [OWASP Application Security Verification Standard
5.0.0](https://github.com/OWASP/ASVS/tree/master/5.0), Levels 1 and 2 (#44).
Requirement IDs are OWASP's (CC BY-SA 4.0); the short descriptions are ours.
Reviewed 2026-11-15 against `main` at the merge of #158.

Each row: the requirement ID, the requirement in our own words (the official
text is in the standard), a status, and the evidence.

- **Met:** a control exists and was checked in the code, a test or a doc.
- **N/A:** the requirement can't apply to WeHelp (reason given).
- **Accepted:** deliberately not done, with the reasoning.
- **Gap:** not met, or couldn't be confirmed. Every gap has an issue.

Components: Back Office (`wehelpd`, staff web), Relay, Registry (and its
console), iOS app. A row is Met only if it holds for every component it
applies to.

## Summary

| Chapter | Met | N/A | Accepted | Gap |
|---|---|---|---|---|
| V1 Encoding and Sanitization | 17 | 10 | 0 | 0 |
| V2 Validation and Business Logic | 9 | 0 | 0 | 2 |
| V3 Web Frontend Security | 14 | 2 | 0 | 3 |
| V4 API and Web Service | 3 | 6 | 0 | 1 |
| V5 File Handling | 6 | 0 | 0 | 3 |
| V6 Authentication | 15 | 5 | 0 | 15 |
| V7 Session Management | 6 | 3 | 1 | 8 |
| V8 Authorization | 5 | 0 | 0 | 2 |
| V9 Self-contained Tokens | 6 | 1 | 0 | 0 |
| V10 OAuth and OIDC | 0 | 29 | 0 | 0 |
| V11 Cryptography | 8 | 1 | 1 | 4 |
| V12 Secure Communication | 3 | 1 | 0 | 5 |
| V13 Configuration | 8 | 0 | 0 | 5 |
| V14 Data Protection | 6 | 0 | 0 | 3 |
| V15 Secure Coding and Architecture | 6 | 0 | 0 | 7 |
| V16 Security Logging and Error Handling | 6 | 0 | 0 | 10 |
| V17 WebRTC | 2 | 4 | 0 | 1 |
| **Total (253 rows)** | **120** | **62** | **2** | **69** |

The stored XSS in the staff web (V1.2.3) was fixed in #159.

## V1 Encoding and Sanitization

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V1.1.1 | Decode input to canonical form once, before validating | Met | Bodies are decoded once by `encoding/json` into typed structs, and path params by chi. No `QueryUnescape`/`PathUnescape`/`html.UnescapeString` anywhere in `internal/`. The base64 decodes (`internal/server/pairing.go` RedeemInvite, `devices.go`) run before their checks. |
| V1.1.2 | Encode output last, or let the interpreter encode it | Met | The practice site uses `html/template` autoescaping (`internal/site/render.go`). The staff web and Registry console escape at the moment they build HTML (`esc()`/`attr()` in `web/app.js`, `internal/registry/console/app.js`). JSON goes through `writeJSON`. |
| V1.2.1 | Encode HTML/attribute/header output for its exact context | Met | Fixed in #159: `esc()` in the staff web and the Registry console now escapes both quote characters, so it is safe in quoted attributes as well as text; `attr()` is used for attribute values. Checked in a browser with hostile patient names. |
| V1.2.2 | Encode untrusted data in URLs; allow only safe schemes | Met | Practice-site links must be `https` (`internal/site/site.go` Validate `link`), and `html/template` filters `href`. The staff web builds API paths only from server UUIDs. The Relay redirect uses a parsed UUID (`internal/relay/site.go` SiteHandler). |
| V1.2.3 | Encode data placed into JavaScript or JSON | Met | Fixed in #159: no inline handlers remain (`TestStaffWebNoInlineHandlers`). Actions carry arguments as JSON in `data-args`, attribute-escaped, and are dispatched from an allow-list (`runAction`, `web/app.js`). Values are parsed only as JSON. Checked in a browser with names `x');alert(1);//` and `"><img src=x onerror=…>`. |
| V1.2.4 | Parameterized database queries | Met | All SQL is pgx with `$n` placeholders. The dynamic builders only add constant column names and placeholder numbers (`internal/server/appointments.go` ~L400, `appointment_requests.go` ListAppointmentRequests, `messages.go:220`). |
| V1.2.5 | Prevent OS command injection | Met | The only `exec.Command` calls run `flyway` with config-derived argv and no shell (`cmd/wehelp/migrate.go`, `cmd/wehelp-registry/main.go:121`). No request data reaches them. |
| V1.2.6 | Prevent LDAP injection | N/A | No LDAP in WeHelp. |
| V1.2.7 | Prevent XPath injection | N/A | No XML or XPath processing. |
| V1.2.8 | Configure LaTeX safely | N/A | No LaTeX processing. |
| V1.2.9 | Escape regex metacharacters from input | N/A | Every regexp is a compile-time literal (`internal/site/site.go`, `internal/relay/site.go`). None is built from input, and the JS has no `new RegExp`. |
| V1.3.1 | Sanitize rich HTML input with a vetted library | N/A | No WYSIWYG or HTML input. Blog posts and bios are plain text, escaped by `html/template` (`internal/site/site.go` Post comment, `render.go` `paras`). |
| V1.3.2 | Avoid eval and dynamic code execution | Met | No `eval`, `new Function` or string `setTimeout` in `web/*.js` or `internal/registry/console/app.js`. Go has no dynamic evaluation. |
| V1.3.3 | Sanitize data before dangerous contexts (length, charset) | Met | Filenames are reduced to `[a-z0-9._-]`, at most 100 characters (`internal/site/site.go` CleanFilename). Relay site paths are allowlisted by regex (`internal/relay/site.go` validSitePath). Field lengths are capped in `Content.Validate`. |
| V1.3.4 | Sanitize user SVG or refuse it | Met | SVG is not an allowed type in `site.AllowedTypes` or the Relay `SiteTypes`. Uploads are sniffed (`internal/server/site.go` contentMatches; `staff_profile.go` photoTypes). |
| V1.3.5 | Disable or sanitize user-supplied template, Markdown or CSS | Met | No Markdown, BBCode or XSL. CSS uploads are refused (CleanFilename rejects `.css`/`.html`). Themes are an allowlisted ID (`site.go` Theme check). |
| V1.3.6 | Prevent SSRF with allowlists on outbound calls | Met | Outbound URLs come only from config or signed install data: Registry (`internal/office/install.go`, `internal/roster/roster.go`), APNs (`internal/relay/apns.go`), and the office list (`relay/sync.go`). No request supplies a URL to fetch. |
| V1.3.7 | Never build templates from untrusted input | Met | Site templates are embedded files parsed once (`internal/site/render.go` `template.Must`). User data is only template data. |
| V1.3.8 | Prevent JNDI injection | N/A | Go; no JNDI. |
| V1.3.9 | Sanitize data sent to memcache | N/A | No memcache. |
| V1.3.10 | Sanitize format strings | Met | Go `fmt` calls use constant format strings. User data is passed only as arguments, and `go vet` printf checks plus `staticcheck` run in CI (docs/SECURITY.md "As built (#43)"). |
| V1.3.11 | Prevent SMTP or IMAP injection | N/A | WeHelp sends no email (no SMTP code). |
| V1.4.1 | Memory-safe strings and buffers | Met | Go with bounds checking. No `unsafe` or cgo imports in `internal/` or `cmd/`. |
| V1.4.2 | Range checks to prevent integer overflow | Met | Numeric inputs are range-checked: `price_cents` 0 to 1000000 and quantity 1 to 20 (`internal/server/store.go`), `limit` 1 to 200 (`messages.go:177`), and file sizes are capped before allocation. |
| V1.4.3 | Free memory and avoid dangling pointers | N/A | Go is garbage-collected, so use-after-free does not arise in safe code. |
| V1.5.1 | Disable XML external entities | N/A | No XML parsing in WeHelp. |
| V1.5.2 | Safe deserialization of untrusted data | Met | Only `encoding/json` into fixed structs, with `DisallowUnknownFields` on the Registry and some Back Office decoders. No gob or polymorphic decoding. iOS uses Codable. |

## V2 Validation and Business Logic

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V2.1.1 | Document validation rules for each data item | Met | `api/openapi.yaml` schemas give formats, enums, maxLength and ranges for the Back Office API (about 256 constraints). Registry APIs are in its own spec (`internal/registry/openapi_test.go`). |
| V2.1.2 | Document rules for combined fields | Met | `api/openapi.yaml`: appointments need an active provider-patient link, overlaps are refused with 409 (~L1652-1679), and `ends_at` must follow `starts_at`. |
| V2.1.3 | Document business limits per user and globally | Gap | Relay limits (connections, pushes, TURN, publish gap) are in docs/RELAY.md and ARCHITECTURE.md, and the staff lockout in SECURITY.md. Back Office per-user limits (messages, requests, pairing attempts) are not documented. |
| V2.2.1 | Validate input against expected structure and ranges | Met | Server-side checks on every inspected handler: UUID parsing, RFC 3339 times, enums (`validTransition`), lengths (`site.Content.Validate`), ranges (`store.go`), and sniffed file types. |
| V2.2.2 | Enforce validation on the server, not the client | Met | All checks are in Go handlers (`internal/server/*.go`, `internal/registry/*.go`). The SPA's checks (e.g. the password match in `web/staff.js:39`) are convenience only. |
| V2.2.3 | Check combinations of related fields are consistent | Met | `appointments.go:133` (`ends_at` after `starts_at`), HasActiveLink plus checkOverlap before booking, and `validTransition` on status changes. |
| V2.3.1 | Enforce step order in multi-step flows | Met | Staff sign-in: a `setup`-state session can only call the TOTP setup/confirm routes, and API access needs `active` (`internal/server/auth.go` authenticateStaffCookie, `staff.go` sessionFor). Pairing invites are one-time, under `for update` (`pairing.go:342`). |
| V2.3.2 | Implement the documented business limits | Met | Implemented as documented: lockout after 5 failures for 15 min (`staff.go` recordFailure), Relay push rate limit (`internal/relay/push.go`), site publish gap (`relay/site.go` allowPublish), and site size caps. |
| V2.3.3 | Business operations are all-or-nothing transactions | Met | Writes use `pgx.Tx`, with `audit.Record` in the same transaction and rollback on any error (e.g. `staff_profile.go` profileChange, `appointments.go`). |
| V2.3.4 | Lock limited resources against double-booking | Met | `lockProviderSchedule` takes `pg_advisory_xact_lock` before checkOverlap (`appointments.go:34`), also on reschedule and on request confirmation (`appointment_requests.go:291`). |
| V2.4.1 | Anti-automation and rate limits on costly functions | Gap | The Relay caps per office (pushes, connections, TURN, publish), and staff and console logins lock out. The Back Office API (pairing redeem, messages, refresh) and the Registry API have no per-client rate limit. About 17 Back Office JSON decoders read `r.Body` with no `MaxBytesReader` (e.g. `auth.go`, `appointments.go`, `devices.go`, `messages.go`). |

## V3 Web Frontend Security

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V3.2.1 | Stop browsers rendering responses in the wrong context | Met | Library downloads: `Content-Disposition: attachment` plus `nosniff` (`internal/server/site.go` GetLibraryFile). Profile photo: sniffed type plus `nosniff` (`staff_profile.go`). Relay site: `default-src 'none'` CSP, and downloads sent as attachments (`relay/site.go` SiteHandler). APIs return `application/json`. |
| V3.2.2 | Render text with safe DOM functions, not as HTML | Met | Staff web and console build `innerHTML` only from escaped values (`esc`/`attr`, quote-safe since #159), and the staff web CSP forbids inline script, so markup injected through a value can't run (checked: an injected inline handler was blocked, `script-src-attr`). |
| V3.3.1 | Cookies are Secure with a `__Host-`/`__Secure-` prefix | Met | `__Host-wehelp_staff` (`internal/server/staff.go` startSession) and `__Host-wehelp_console` (`internal/registry/console_auth.go:126`) are set with `Secure: true`. The practice site and phones use no cookies. |
| V3.3.2 | SameSite set to fit each cookie's purpose | Met | Both session cookies are `SameSite=Strict` (`staff.go:150`, `console_auth.go:127`). |
| V3.3.3 | Use the `__Host-` prefix unless sharing across hosts | Met | Both cookies use `__Host-` with `Path=/` and no Domain. |
| V3.3.4 | HttpOnly session cookies, token sent only via Set-Cookie | Met | `HttpOnly: true` on both. `/staff/me` and `/console/me` return only the CSRF token, never the session token (`staff.go` staffMe, `console_auth.go` consoleMe). |
| V3.4.1 | HSTS on every response, at least 1 year, with subdomains | Gap | No `Strict-Transport-Security` header anywhere: not in the Back Office staff TLS (`internal/server/server.go` ServeStaffTLS), the Registry, or the Relay site listener (`relay/site.go`). |
| V3.4.2 | CORS origin fixed or checked against an allowlist | Met | No `Access-Control-Allow-*` header is set by any component, so browsers refuse cross-origin reads. |
| V3.4.3 | CSP with object-src/base-uri 'none' and an allowlist | Met | Staff web: `script-src 'self'` with no inline script, plus `object-src 'none'`, `base-uri 'none'`, `form-action 'self'`, `frame-ancestors 'none'` (`staffWebHeaders`, `TestStaffWebHeaders`, #159). Registry console now also has `object-src 'none'` and `base-uri 'none'`. Practice site: `default-src 'none'; base-uri 'none'` (`relay/site.go`). |
| V3.4.4 | `X-Content-Type-Options: nosniff` on every response | Gap | Now set on the staff web static files (#159), the Relay site, the Registry console and two Back Office file endpoints. Still missing on Back Office API JSON (`writeJSON`) and Registry API responses (#160). |
| V3.4.5 | Referrer-Policy to stop leaking URLs to third parties | Met | `no-referrer` on the staff web (#159, `TestStaffWebHeaders`), the Registry console and the Relay site. |
| V3.4.6 | CSP frame-ancestors on every response to block framing | Met | `frame-ancestors 'none'` and `X-Frame-Options: DENY` on the staff web (#159); `frame-ancestors 'none'` on the Registry console and the Relay site. The JSON APIs aren't rendered as pages. |
| V3.5.1 | Anti-CSRF token or non-safelisted header on sensitive requests | Gap | Signed-in changes need `X-CSRF-Token`, compared in constant time (`staff.go` csrfOK; `console_auth.go` ~L187). The unauthenticated `POST /api/v1/staff/login`, `/staff/setup` and console `/login` accept any Content-Type and check neither token nor Origin, which allows login CSRF. |
| V3.5.2 | If relying on preflight, block non-preflighted calls | N/A | WeHelp relies on CSRF tokens (staff web, console) and bearer tokens (phones), not on CORS preflight. |
| V3.5.3 | Sensitive actions use POST/PUT/PATCH/DELETE, not GET | Met | Every state change is a non-GET route (`internal/server/server.go` router; e.g. `POST /messages/{id}/read`, `POST /practice-inbox/read`), and `csrfOK` exempts only GET/HEAD/OPTIONS. |
| V3.5.4 | Separate applications on separate hostnames | Met | The staff web runs on the office LAN host (LAN only, `lanOnly`), the Registry console on the Registry host, and practice sites on the Relay's site listener. Practice sites share one origin under `/s/<id>/`, but they have no scripts or cookies. |
| V3.5.5 | Check origin and syntax of postMessage messages | N/A | No `postMessage` or `message` listeners in any web client. |
| V3.7.1 | Use only supported client-side technologies | Met | Plain HTML, CSS and ES JavaScript. No plugins, Flash or applets. |
| V3.7.2 | Redirect off-site only to allowlisted destinations | Met | Redirects are same-host only: HTTP→HTTPS on the same host (`server.go` healthOrRedirect), `/console`→`/console/`, and the Relay `/s/<uuid>`→`/s/<uuid>/`. No redirect target comes from a parameter. |

## V4 API and Web Service

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V4.1.1 | Correct Content-Type with charset on bodies | Met | Staff web HTML, JS and CSS carry `charset=utf-8` (#159, `TestStaffWebHeaders`); JSON and site types were already right. |
| V4.1.2 | Only browser endpoints auto-redirect HTTP to HTTPS | Gap | The Back Office plain-HTTP listener redirects every path, including `/api/*`, to HTTPS with 308 (`internal/server/server.go` healthOrRedirect). API calls should be refused instead. |
| V4.1.3 | Intermediary-set headers can't be spoofed by clients | Met | No `RealIP` or forwarded-header trust in any component (`server.go` comment, #43; grep finds no X-Forwarded/X-Real-IP use). Relay origin is decided by the listener (`tunnel.ViaRelay`), not a header. |
| V4.2.1 | Find HTTP message boundaries correctly to stop smuggling | Met | Go `net/http` parses framing per RFC on every server. There is no proxy that re-parses HTTP. The Relay splices raw bytes after CONNECT (`internal/relay/relay.go`). |
| V4.3.1 | Limit GraphQL query depth or cost | N/A | No GraphQL. |
| V4.3.2 | Disable GraphQL introspection in production | N/A | No GraphQL. |
| V4.4.1 | WebSockets only over TLS | N/A | No WebSockets. Signaling and APIs are HTTPS requests through the Relay tunnel. |
| V4.4.2 | Check WebSocket Origin at handshake | N/A | No WebSockets. |
| V4.4.3 | Dedicated WebSocket tokens meet session rules | N/A | No WebSockets. |
| V4.4.4 | WebSocket tokens come from the authenticated HTTPS session | N/A | No WebSockets. |

## V5 File Handling

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V5.1.1 | Document allowed file types, sizes and safe handling | Met | `api/openapi.yaml` `/api/v1/site/files` (types, 10 MB, contents must match), profile photo (PNG/JPEG/WebP, 2 MB), and docs/RELAY.md (allowed extensions, 200 files, 10/25 MB, no `<script`, attachment downloads). |
| V5.2.1 | Reject files too large to process safely | Met | `MaxBytesReader` plus `LimitReader` caps: site file 10 MB (`internal/server/site.go` UploadSiteFile), photo 2 MB (`staff_profile.go`), Relay bundle 25 MB and 10 MB per file (`relay/site.go` ReadSiteBundle). |
| V5.2.2 | Check extension and real content type of every file | Gap | The Back Office sniffs content against the extension (`site.go` contentMatches; photo `http.DetectContentType`). The Relay checks only extensions and `<script` in HTML (`relay/site.go` ReadSiteBundle), not file contents. |
| V5.2.3 | Check archive uncompressed size and file count first | Met | The only archive is the uncompressed site tar. The Relay enforces 200 files, 10 MB per file and 25 MB in all while streaming, before storing (`relay/site.go` ReadSiteBundle). |
| V5.3.1 | Uploaded files never executed as server code | Met | Uploads are stored as `bytea` in Postgres (Back Office) or as static files served by `os.ReadFile` plus `ServeContent` (Relay). Nothing interprets them. |
| V5.3.2 | Build file paths from trusted data, not user names | Met | Back Office filenames go through `CleanFilename` and are stored in the DB. Relay paths are validated by `validSitePath` (regex, no `..`) under a parsed office UUID (`relay/site.go` storeSite, SiteHandler). |
| V5.4.1 | Ignore user filenames; set Content-Disposition filename | Gap | The Back Office library download sets a cleaned filename (`site.go` GetLibraryFile). The Relay sends `Content-Disposition: attachment` with no filename (`relay/site.go` SiteHandler). |
| V5.4.2 | Encode or sanitize filenames in headers | Met | Header filenames come only from `CleanFilename` output (`[a-z0-9._-]`), so they cannot inject (`internal/site/site.go`, `internal/server/site.go:485`). |
| V5.4.3 | Antivirus-scan files from untrusted sources | Gap | No malware scanning of staff uploads (`internal/server/site.go`) or of Relay-hosted downloads, and no documented acceptance of that risk. |

## V6 Authentication

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V6.1.1 | Document anti-brute-force controls, their config, and how lockout abuse is avoided | Gap | docs/SECURITY.md "As built (#38)" and docs/REGISTRY.md document only "five failures lock for 15 minutes". Nothing explains rate limiting or how a LAN attacker is stopped from deliberately locking accounts. |
| V6.1.2 | Document a list of context-specific words banned in passwords | Gap | No such list in docs/ (SECURITY.md, ARCHITECTURE.md, REGISTRY.md). |
| V6.1.3 | Document every authentication pathway together, with its strength | Gap | Pathways are spread across SECURITY.md, ARCHITECTURE.md and REGISTRY.md and never listed together. Admin pairing of another staff member's phone (`pairTarget` in internal/server/pairing.go) and the dev-only `/auth/register` are not documented as pathways. |
| V6.2.1 | Passwords at least 8 characters (15 recommended) | Met | A 12-character minimum is enforced everywhere: `StaffSetup` (staff_admin.go), `Register` (auth.go), `wehelpd staff add` (cmd/wehelp/staff.go), Registry `AddStaff` (`minPasswordLen`). |
| V6.2.2 | Users can change their password | Gap | Back Office staff and Registry console: no password-change route or command. The only `password_hash =` write is the one-time `StaffSetup`. |
| V6.2.3 | Password change asks for current and new password | Gap | No password change exists (see V6.2.2). |
| V6.2.4 | Check new passwords against the top 3000 common passwords | Gap | Not implemented. Listed as an "Open security decision" in docs/SECURITY.md, and the "As built" notes say "still an open decision". |
| V6.2.5 | No character-composition rules on passwords | Met | Only length is checked (staff_admin.go `StaffSetup`, auth.go `Register`, cmd/wehelp/staff.go, registry `AddStaff`). |
| V6.2.6 | Password fields are masked input | Met | `type="password"` in web/app.js (login-password), web/staff.js (staff-setup-password, -password2) and internal/registry/console/app.js (password). The CLI uses `term.ReadPassword`. |
| V6.2.7 | Paste and password managers allowed | Met | Fields use `autocomplete="current-password"` or `"new-password"`, and no paste blocking was found in web/ or the console JS. |
| V6.2.8 | Password checked exactly as typed, no truncation or case change | Met | Server handlers pass `req.Password` untrimmed to argon2id (`checkStaffPassword`, `consoleLogin`). Only the tenant and email are trimmed. |
| V6.2.9 | Passwords of 64+ characters allowed | Met | `StaffSetup` allows up to 1024 characters. Login bodies are capped at 8 KiB (`MaxBytesReader`), and argon2id does not truncate. |
| V6.2.10 | No forced periodic password rotation | Met | There is no password-expiry field or check in `users` or `console_staff` handling. |
| V6.2.11 | Use the context-specific word list when setting passwords | Gap | No list exists and no check is made (see V6.1.2). |
| V6.2.12 | Check new passwords against breached-password set | Gap | Not implemented. docs/SECURITY.md "Open security decisions" (local list or k-anonymity lookup) is undecided. |
| V6.3.1 | Brute-force controls implemented as documented | Met | `recordFailure` (staff.go) and Registry `recordFailure` lock after 5 password or TOTP failures for 15 minutes and audit each failure. Covered by `TestStaffLockout`. |
| V6.3.2 | No default accounts (admin, root) present or enabled | Gap | Migrations create no users. But `wehelpd seed` (cmd/wehelp/seed.go, internal/seed) creates admin@demo.wehelp and other accounts with fixed passwords and no TOTP, and has no env guard. A password alone then gives a TOTP-setup session. |
| V6.3.3 | MFA (or combined factors) required for access | Gap | Staff web, the staff bearer API and the Registry console require password+TOTP. Device sessions (patients and paired providers) need only the Secure Enclave key: `DeviceKey.create` sets no `.userPresence` access control, so the Face ID check in ios/WeHelp/AppLock.swift is client-side only. |
| V6.3.4 | No undocumented auth pathways, and equal strength across them | Gap | An admin can issue a pairing QR for another provider or admin account (`pairTarget`, `TestStaffPairingScope`). That gives a session as that person without their password or TOTP, and the docs don't describe it. |
| V6.4.1 | Initial codes random, expiring, single use, not the long-term secret | Met | Set-up links are 32 random bytes, last 24 hours, work once, and only a hash is stored (`issueStaffInvite`, `StaffSetup`). Pairing invites are 128 bits, last 10 minutes, work once (pairing.go). Both lead to a credential the user creates. |
| V6.4.2 | No password hints or secret questions | Met | No such fields in any users or console_staff handler or UI. |
| V6.4.3 | Secure forgotten-password reset that keeps MFA | Gap | No reset process exists for staff or console accounts. `ReissueProviderInvite` refuses accounts that already have a password, so recovery means editing the database by hand. |
| V6.4.4 | Lost MFA factor needs identity proofing like enrollment | Gap | Patients: a lost phone is revoked and re-paired in the office (ARCHITECTURE.md), which works. Staff and console: no TOTP reset or recovery path exists. |
| V6.5.1 | TOTP and one-time codes usable only once | Met | `staffauth.CheckTOTP` refuses steps at or before `totp_last_step`, in both the Back Office and the Registry. Pairing invites (`redeemed_at`) and set-up links (`used_at`) are single use. |
| V6.5.2 | Low-entropy lookup secrets hashed with salted slow hash | N/A | No lookup secrets or recovery codes exist. The bearer secrets that do exist are 128 to 256 bits and stored as SHA-256. |
| V6.5.3 | TOTP seeds and codes from a CSPRNG | Met | `staffauth.NewTOTPKey` uses `totp.Generate`, which defaults to crypto/rand. Invites and set-up tokens use crypto/rand (`pairing.go`, `staffauth.RandomToken`). |
| V6.5.4 | Lookup and out-of-band codes have at least 20 bits of entropy | Met | Pairing invite is 128 bits (`inviteBytes = 16`) and the set-up link is 256 bits. |
| V6.5.5 | Out-of-band codes live 10 minutes or less, TOTP 30 seconds | Gap | Pairing invites expire after 10 minutes (`inviteTTL`). But `CheckTOTP` accepts ±1 step, so a code is valid for about 90 s. Also, set-up links (an out-of-band token) last 24 hours. |
| V6.6.1 | SMS/phone OTP only with validated number and stronger options | N/A | No SMS or voice OTP anywhere in WeHelp. |
| V6.6.2 | Out-of-band codes bound to the request that created them | Met | Each invite is bound to one account and redeemed with a device-key proof over that invite (`RedeemInvite`, purpose "pair"). Set-up links are bound to one user (`staff_invites.user_id`). |
| V6.6.3 | Rate-limit brute force of out-of-band codes | Gap | `/pairing/redeem` (reachable through the Relay) and `/staff/setup` have no rate limiting. The 128 and 256-bit entropy makes guessing infeasible, but the rationale isn't documented. |
| V6.8.1 | Identity cannot be spoofed across multiple IdPs | N/A | There are no external IdPs. SSO is Post-V1 (#27). |
| V6.8.2 | Auth assertion signatures always validated | Met | `auth.ParseAccessToken` (internal/auth/tokens.go) verifies the HMAC before any claim is used. Device proofs are ECDSA-verified (`VerifyDeviceProof`). |
| V6.8.3 | SAML assertions processed once | N/A | No SAML in WeHelp. |
| V6.8.4 | Verify IdP-reported auth strength (acr, amr, auth_time) | N/A | No external IdP or OIDC. |

## V7 Session Management

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V7.1.1 | Document idle and absolute session limits, with NIST deviations justified | Gap | The staff web limits (30-minute idle, 12-hour absolute) are in SECURITY.md. Device sessions (15-minute access, a 720h `refresh_ttl` that resets on every refresh) have no documented absolute limit and no NIST justification. |
| V7.1.2 | Document allowed concurrent sessions and the behavior at the limit | Gap | Not documented. Staff web sessions and devices per account are unlimited ("a person can have several devices", ARCHITECTURE.md). |
| V7.1.3 | Document federated/SSO session coordination | N/A | No federation or SSO in V1 (#27 is Post-V1). |
| V7.2.1 | Session tokens verified by trusted backend | Met | Cookie sessions are looked up server-side (`lookupSession` in staff.go and console_auth.go). JWTs are verified in `authenticate` (auth.go) on the Back Office. |
| V7.2.2 | Dynamic session tokens, not static API keys | Met | Each sign-in or pairing issues a fresh random session or refresh token and a short-lived JWT (`startSession`, `issueTokens`). |
| V7.2.3 | Reference tokens from a CSPRNG with at least 128 bits | Met | Staff and console cookies are 32 bytes from crypto/rand (`staffauth.RandomToken(32)`), and refresh tokens are 32 bytes (`auth.NewRefreshToken`). Only the hashes are stored. |
| V7.2.4 | New token at each authentication, old one terminated | Gap | A new token is always minted, and TOTP confirm revokes the setup session. But `StaffLogin` and `consoleLogin` don't revoke a session already in the incoming cookie, so it stays live until it idles out. |
| V7.3.1 | Inactivity timeout forces re-authentication | Met | Staff web and console: a 30-minute idle limit is enforced in SQL (`last_seen_at > now() - interval`). Phones: AppLock requires Face ID or passcode after every backgrounding, and an unused refresh token dies after 30 days. |
| V7.3.2 | Absolute session lifetime forces re-authentication | Gap | Staff web and console: 12 hours (`sessionMax`). Device sessions: `Refresh` issues a new token with `now() + RefreshTTL` each time, so a phone session never reaches an absolute end. |
| V7.4.1 | Logout or expiry fully invalidates the session | Gap | Staff web and console logout revoke the server-side row. Phone logout revokes the device, and every request checks it. The LAN `/auth/login` bearer path revokes only the refresh token on `Logout`, so the unbound access JWT works for up to 15 more minutes. |
| V7.4.2 | Disabling an account ends all its sessions | Met | `DisableProvider` revokes staff_sessions, refresh tokens and invites. `authenticate` and `authenticateStaffCookie` check `users.disabled_at` on every request, and the Registry `lookupSession` checks `st.disabled_at`. |
| V7.4.3 | Offer to end other sessions after changing an auth factor | N/A | There is no password or TOTP change feature to trigger it (see V6.2.2 and V6.4.4). |
| V7.4.4 | Logout visible on every authenticated page | Accepted | Staff web has "Log out" (web/app.js) and the console "Sign out". The iOS app deliberately has no sign-out or unpair: product decision (2026-11) that only the practice removes a device, in the office or from the staff web (`DELETE /devices/{id}`, revocation in TestDeviceManagement). The app locks with Face ID or the passcode every time it is left (AppLock.swift), and a lost phone is revoked by the practice. |
| V7.4.5 | Admins can end one user's or all users' sessions | Gap | One user: `DisableProvider` and `RevokePatientDevice` work, but admin accounts can't be disabled (`lockProvider` only matches provider or office), and the Registry has no disable command. All users: there's no switch. |
| V7.5.1 | Re-authenticate before changing auth-related attributes | Gap | A signed-in staff session can create a pairing invite that adds a new device credential for itself, or for anyone if the caller is an admin, with no fresh password or TOTP (`CreateInvite`). |
| V7.5.2 | Users can view and end their own active sessions | Gap | Phones: `GET /devices` lists them in the app (DevicesView.swift); ending them is the practice's job by the same decision as V7.4.4 (accepted). Staff web and console: no session list or "end my other sessions" at all. |
| V7.6.1 | RP and IdP session lifetimes behave as documented | N/A | No external IdP. |
| V7.6.2 | Creating a session needs an explicit user action | Met | Sessions start only from a deliberate sign-in (`StaffLogin`, `consoleLogin`, `Login`) or from scanning and redeeming an in-office QR (`RedeemInvite`). |

## V8 Authorization

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V8.1.1 | Document function- and data-level access rules | Met | ARCHITECTURE.md "Staff accounts" (provider, office and admin scopes) and SECURITY.md (roles, LAN-only staff routes, patients only on phones) document function-level and per-patient rules. |
| V8.1.2 | Document field-level read/write rules | Gap | No field-level rules are documented anywhere in docs/. |
| V8.2.1 | Functions restricted to explicitly permitted roles | Met | `staffOnLAN`, `adminOnLAN` and the role checks in handlers (for example `UpdateAppointment` and `CancelOrder`) enforce it. Covered by `TestOfficeRole`, `TestStaffRoutesHiddenFromRelay` and `TestPatientsHaveNoPassword`. |
| V8.2.2 | Object-level checks prevent IDOR/BOLA | Met | Queries filter on `tenant_id` and ownership: `staffPatient` (provider_patients link), `UpdateAppointment` (provider or practiceWide), `CancelOrder` (own order), `teamGate` (care teams). Covered by `TestStaffPairingScope`, `TestCareTeams` and `TestVideoVisit`. |
| V8.2.3 | Field-level checks prevent BOPLA | Gap | Spot checks show explicit request structs (for example `PutStaffProfile` with `DisallowUnknownFields`). But with no documented field rules, this can't be confirmed across all handlers. |
| V8.3.1 | Authorization enforced server-side, not client-side | Met | All checks run in Back Office handlers and middleware. Relay reachability is decided by the listener context (`tunnel.ViaRelay` in `lanOnly`), not a header. |
| V8.4.1 | Cross-tenant isolation in multi-tenant parts | Met | Back Office handlers scope by `tenant_id` from the token. The Registry roster API is keyed to the office key and returns only that practice. The Relay files site bundles under the SSH-authenticated office ID (SECURITY.md). |

## V9 Self-contained Tokens

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V9.1.1 | Verify token signature/MAC before trusting contents | Met | `auth.ParseAccessToken` uses `jwt.ParseWithClaims` with an HMAC key and rejects tokens on error before `authenticate` reads any claim. |
| V9.1.2 | Only allowlisted algorithms, never "none" | Met | The keyfunc accepts only `*jwt.SigningMethodHMAC`, and golang-jwt v5 refuses "none". The family allows HS384 and HS512 as well as the issued HS256, so `jwt.WithValidMethods([]string{"HS256"})` would tighten it. |
| V9.1.3 | Verification keys only from trusted preconfigured sources | Met | The keyfunc always returns the server's own `jwtSecret` (server.go `New`) and ignores the jku, x5u and jwk headers. |
| V9.2.1 | Enforce exp/nbf validity window | Met | golang-jwt v5 checks `exp` and `nbf` by default. `IssueAccessToken` always sets `exp` (15-minute `access_ttl`). |
| V9.2.2 | Accept only the correct token type for the purpose | Met | Only one JWT type (the access token) is ever signed with `jwtSecret`. Refresh tokens, invites and session cookies are opaque random values checked against DB hashes. |
| V9.2.3 | Accept only tokens meant for this service (audience) | Met | There is no `aud` claim. Each Back Office's HMAC secret is used by that service alone, so no other service can mint or accept its tokens. Adding `aud`/`iss` would make this explicit. |
| V9.2.4 | Shared issuer keys must audience-restrict tokens | N/A | The issuer signs for one audience only, itself, with a per-office symmetric key. |

## V10 OAuth and OIDC

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V10.1.1 | OAuth tokens only reach components that need them | N/A | No OAuth/OIDC in WeHelp V1 (no library or code; SSO is #27, Post-V1). |
| V10.1.2 | Bind authorization responses to the initiating session (PKCE, state, nonce) | N/A | No OAuth/OIDC in WeHelp V1. |
| V10.2.1 | OAuth client CSRF protection via PKCE or state | N/A | No OAuth client. |
| V10.2.2 | Defend against authorization server mix-up | N/A | No OAuth client. |
| V10.3.1 | Resource server checks access token audience | N/A | No OAuth resource server. Own-JWT audience is covered in V9.2.3. |
| V10.3.2 | Enforce delegated-authorization claims (scope, sub) | N/A | No OAuth delegated authorization. |
| V10.3.3 | Identify users by non-reassignable claims (iss+sub) | N/A | No OAuth access tokens. |
| V10.3.4 | Verify required auth strength claims in access tokens | N/A | No OAuth/OIDC. |
| V10.4.1 | Exact-match redirect URI allowlist | N/A | WeHelp is not an authorization server. |
| V10.4.2 | Authorization codes usable once | N/A | Not an authorization server. |
| V10.4.3 | Short-lived authorization codes | N/A | Not an authorization server. |
| V10.4.4 | Only required grants per client, no implicit or password grant | N/A | Not an authorization server. |
| V10.4.5 | Mitigate refresh token replay for public clients | N/A | Not an authorization server. Its own refresh tokens are device-proof-bound for phones (devices.go `Refresh`). |
| V10.4.6 | Require PKCE (S256) for code grant | N/A | Not an authorization server. |
| V10.4.7 | Control unauthenticated dynamic client registration | N/A | Not an authorization server. |
| V10.4.8 | Refresh tokens have an absolute expiry | N/A | Not an authorization server. Its own refresh tokens slide (see V7.3.2 Gap). |
| V10.4.9 | Users can revoke refresh tokens via the AS UI | N/A | Not an authorization server. |
| V10.4.10 | Authenticate confidential clients on back-channel | N/A | Not an authorization server. |
| V10.4.11 | Assign clients only required scopes | N/A | Not an authorization server. |
| V10.5.1 | Mitigate ID Token replay with nonce | N/A | No OIDC relying party. |
| V10.5.2 | Identify users by non-reassignable ID Token sub | N/A | No OIDC relying party. |
| V10.5.3 | Reject AS metadata with mismatched issuer | N/A | No OIDC relying party. |
| V10.5.4 | Check ID Token aud equals client_id | N/A | No OIDC relying party. |
| V10.5.5 | Validate back-channel logout tokens | N/A | No OIDC relying party. |
| V10.6.1 | OP allows only safe response modes | N/A | WeHelp is not an OpenID Provider. |
| V10.6.2 | OP prevents forced-logout DoS | N/A | Not an OpenID Provider. |
| V10.7.1 | AS obtains user consent per authorization | N/A | Not an authorization server. |
| V10.7.2 | Consent prompt explains what is granted | N/A | Not an authorization server. |
| V10.7.3 | Users review, modify and revoke consents | N/A | Not an authorization server. |

## V11 Cryptography

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V11.1.1 | Documented key-management policy and lifecycle, keys not overshared | Gap | `docs/OFFICE_KEYS.md` (office keys, rotation), `docs/RELAY.md` and `docs/REGISTRY.md` (Relay/Registry key rotation) cover procedures only. There is no policy with cryptoperiods or a lifecycle standard; OFFICE_KEYS.md cites "the schedule in the security policy", which doesn't exist. |
| V11.1.2 | Maintained inventory of keys, algorithms, certificates and their permitted uses | Gap | OFFICE_KEYS.md lists office keys only. JWT secret, TURN master secret and per-office keys, TOTP secrets, APNs key, Registry signing key, Relay keys, age backup keys and audit hash scheme are not in one inventory. |
| V11.2.1 | Use vetted crypto implementations | Met | Go stdlib `crypto/*` and `golang.org/x/crypto` (ssh, acme), `alexedwards/argon2id`, `golang-jwt/jwt/v5`, `age` for backups (`deploy/backup.sh`), Secure Enclave and libwebrtc on iOS; no hand-rolled primitives found. |
| V11.2.2 | Crypto agility: algorithms and keys replaceable, data re-encryptable | Gap | Some agility: PHC-format argon2id hashes, audit `hash_version` (`internal/audit`), key rotation runbooks. But the office TLS pin has no rollover (OFFICE_KEYS.md "A pin-set rollover is not in V1": rotation forces re-pairing every phone), and JWT HS256 and TURN HMAC-SHA1 are hard-coded. |
| V11.2.3 | All primitives give at least 128-bit security | Met | ed25519 (`internal/keyfile/keyfile.go`), P-256 (`internal/office/office.go:290`, `internal/relay/keys.go:77`), 32-byte HMAC-SHA256 JWT secret (`internal/server/server.go` New, `deploy/appliance.sh` `openssl rand -hex 32`), 32-byte TURN master (`internal/relay/turn.go` EnsureTURNSecret), age X25519. |
| V11.3.1 | No ECB or weak padding schemes | Met | No ECB, `crypto/cipher` or RSA use in our code (grep of internal/ and cmd/); all keys are ECC; backups use age (ChaCha20-Poly1305). |
| V11.3.2 | Only approved ciphers and modes (e.g. AES-GCM) | Gap | Phone paths are TLS 1.3 only (`internal/relay/relay.go:142`, `internal/tunnel/tunnel.go:96`). The TLS 1.2 listeners (staff HTTPS `internal/server/server.go:239`, Relay sites `internal/relay/site.go:301`, TURN TLS `cmd/wehelp-relay/main.go:213,225`, Registry `internal/registry/server.go:54`) set no `CipherSuites`, so Go defaults including ECDHE AES-CBC-SHA suites remain enabled. |
| V11.3.3 | Encrypted data protected from tampering (AEAD or MAC) | Met | TLS 1.3 AEAD on phone paths; age is ChaCha20-Poly1305 plus a SHA-256 manifest checked on restore (`deploy/backup.sh`, docs/BACKUPS.md); SRTP GCM preferred (`ios/WeHelp/Services/VisitCall.swift` cryptoOptions). |
| V11.4.1 | Approved hashes only; no MD5/SHA-1 for crypto | Accepted | SHA-256 everywhere (pins, token hashes `internal/staffauth/staffauth.go` HashToken, audit chain). SHA-1 remains only as HMAC-SHA1 where the protocol mandates it: TURN REST passwords (`internal/turncred/turncred.go` Password, comment "by definition", libwebrtc compatibility) and RFC 6238 TOTP (SECURITY.md "As built (#38)"). |
| V11.4.2 | Passwords stored with tuned memory-hard KDF | Met | argon2id m=19 MiB, t=2, p=1, 16-byte salt (`internal/staffauth/password.go` params), the OWASP minimum; also used by the Registry console (`internal/registry/console_auth.go` via staffauth). |
| V11.4.3 | Integrity/signature hashes collision resistant, at least 256 bits | Met | SHA-256 audit chain (`internal/audit`, SECURITY.md "As built (#28)"), SHA-256 pins (`keyfile.P256Pin`), ed25519 office list and roster signatures (`internal/officelist`, `internal/roster`), SHA-256 backup manifest. |
| V11.4.4 | Password-derived keys use approved stretching KDF | N/A | No keys are derived from passwords; age backup keys come from `age-keygen` (docs/BACKUPS.md), other keys are random. |
| V11.5.1 | Unguessable values from CSPRNG with at least 128 bits | Met | `crypto/rand` for sessions (32 B) and CSRF (24 B) `internal/server/staff.go:136-137`, refresh tokens (32 B) `internal/auth/tokens.go:61`, invites (16 B) `internal/server/pairing.go:41`, visit tokens (18 B) `internal/server/visits.go:330`, install tokens `internal/registry/store.go:276`. `math/rand` only for reconnect jitter (`internal/tunnel/tunnel.go:126`). |
| V11.6.1 | Approved algorithms for key generation and signatures | Met | `ed25519.GenerateKey` / `ecdsa.GenerateKey(P256, crypto/rand)` (`internal/keyfile/keyfile.go:75`, `internal/office/office.go:290`, `internal/relay/keys.go:77`); Secure Enclave P-256 on iOS; ES256 APNs JWT (`internal/relay/apns.go:115`). |

## V12 Secure Communication

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V12.1.1 | Only TLS 1.2/1.3, with 1.3 preferred | Met | Phone-facing CONNECT and inner office TLS require 1.3 (`internal/relay/relay.go:142`, `internal/tunnel/tunnel.go:96`); other listeners set `MinVersion: tls.VersionTLS12` or use Go's 1.2 server default (Registry), and Go negotiates 1.3 first. |
| V12.1.2 | Only recommended cipher suites, strongest preferred | Gap | TLS 1.2 listeners (staff HTTPS, Relay sites, TURN TLS, Registry) leave `CipherSuites` unset, so Go's defaults including AES-CBC-SHA suites are accepted. |
| V12.1.3 | mTLS client certificates validated before use | N/A | No mTLS: offices authenticate to the Relay with SSH keys (`internal/relay/relay.go` authorize) and to the Registry with signed requests (`internal/roster`). |
| V12.2.1 | TLS for all external HTTP services, no plaintext fallback | Gap | The Registry serves plain HTTP whenever `registry_tls_cert`/`key` are unset, with only a warning (`internal/registry/server.go` ListenAndServe), and `Identity.Install` accepts an `http://` Registry URL (`internal/office/install.go:24`), so an install token can travel in clear. Default `registry_url` is `http://localhost:8090` (`internal/config/config.go:130`). |
| V12.2.2 | External services use publicly trusted certificates | Gap | TURN TLS uses Let's Encrypt (`cmd/wehelp-relay/main.go` autocert). The practice website listener falls back to the Relay's self-signed cert when `relay_site_cert` is unset (`internal/relay/site.go`, docs/RELAY.md "browsers warn"). The Relay CONNECT cert is self-signed but pinned by the app (`ios/WeHelp/Services/OfficeTransport.swift`), an acceptable alternative not recorded as an accepted decision. |
| V12.3.1 | Encrypt all inbound/outbound connections incl. DB and management | Gap | Postgres connections use `sslmode=disable` (`internal/config/config.go:116,124`, `deploy/docker-compose.yml:38`); the Registry may run plain HTTP; Back Office port 8080 serves everything in plain HTTP until the office is installed or when `env=dev` (`cmd/wehelp/serve.go` startStaffTLS). Office-Relay is SSH, phones TLS 1.3. |
| V12.3.2 | TLS clients validate server certificates | Met | Go `http.Client`s use default verification (`cmd/wehelp/serve.go` rosterSource, `internal/relay/apns.go:103`); no `InsecureSkipVerify` in code; iOS pins Relay and office keys and fails closed (`ios/WeHelp/Services/OfficeTransport.swift`, `APIClient.swift`); SSH pins the Relay host key (`internal/tunnel/tunnel.go:112` FixedHostKey). |
| V12.3.3 | Encrypted transport between internal HTTP services | Gap | Back Office to Registry roster/install calls and Relay to Registry list sync run over whatever scheme is configured, and HTTP is allowed (see V12.2.1). Payloads are ed25519-signed, but confidentiality isn't enforced. |
| V12.3.4 | Internal TLS trusts only specific CAs or pinned certs | Met | Phones pin office and Relay P-256 keys; staff browsers trust only the office CA (`internal/office/staffcert.go`); Relay pins the Registry ed25519 key for lists, and the office pins the Registry key and Relay host key at install (`internal/office/install.go`). |

## V13 Configuration

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V13.1.1 | All communication needs and external dependencies documented | Met | docs/ARCHITECTURE.md (topology, outbound SSH, APNs), docs/RELAY.md (listeners, ports, firewall, Let's Encrypt), docs/REGISTRY.md, docs/OBSERVABILITY.md (export destinations). No feature lets a user supply a URL the server then fetches. |
| V13.2.1 | Backend component auth uses per-service, non-static credentials | Gap | Office-Relay uses SSH keys, office-Registry uses ed25519-signed requests (`internal/roster` SignRequest), and the Relay signs status reports (`internal/relay/report.go`). But the Back Office and Registry reach Postgres with a static shared password (`wehelp:wehelp`, `deploy/docker-compose.yml`). |
| V13.2.2 | Backend connections use least-privilege accounts | Gap | The app uses the same Postgres user as Flyway migrations (`FLYWAY_USER: wehelp`, `deploy/docker-compose.yml:25,38`), so it owns the schema; triggers stop UPDATEs on audit/ledger, but the owner could drop them. The `distroless/static` image runs as root (`deploy/Dockerfile`, no `USER`). |
| V13.2.3 | No default credentials for service authentication | Gap | Postgres user/password `wehelp`/`wehelp` is hard-coded in `deploy/docker-compose.yml:5-6,54-55` (used by the appliance) and in config defaults (`internal/config/config.go:116,124`). |
| V13.2.4 | Allowlist of external systems the app may contact | Met | Outbound targets are fixed by config or signed data only: Registry URL and Relay from `registration.json` (`internal/office/install.go`), APNs host (`internal/relay/apns.go`), ACME restricted by `autocert.HostWhitelist` (`cmd/wehelp-relay/main.go`). TURN peers are limited to the Relay's own address (`internal/relay/turn.go:100`). |
| V13.2.5 | Server loads data/files only from allowlisted sources | Met | No user-controlled fetches; static assets are embedded (`web/web.go`, `internal/registry/console.go` go:embed); sites are rendered from the office DB (`internal/site`) and served from the Relay's own bundle dir (`internal/relay/site.go` validSitePath). |
| V13.3.1 | Secrets managed in a vault, never in source or builds | Gap | Secrets are mode-600 files and `.env` (`internal/keyfile`, `deploy/appliance.sh:42`), with no secrets manager. The default DB password is in the repo (`deploy/docker-compose.yml`) and demo passwords are compiled into `wehelpd` (`internal/seed/seed.go:36-46`). |
| V13.3.2 | Least-privilege access to secret material | Met | Keys are written 0600 in 0700 dirs and refused if group/other readable (`internal/keyfile/keyfile.go` Write/Read, `CheckPrivate` for TURN secret and TLS keys); the Mac mini holds only the age public key (docs/BACKUPS.md). |
| V13.4.1 | No source-control metadata in deployments | Met | The runtime image is `distroless/static` holding only `/usr/local/bin/wehelpd` (`deploy/Dockerfile`); `COPY . .` is only in the build stage. |
| V13.4.2 | Debug modes off in production | Gap | No pprof/expvar/debug routes and iOS Release is checked (`scripts/check-ios-release.sh`). But Back Office `env` defaults to `dev` (`internal/config/config.go:118`), which opens self-registration (`internal/server/auth.go:135`) and skips the HTTPS redirect (`cmd/wehelp/serve.go:134`); only the appliance compose sets `WEHELP_ENV: appliance`. |
| V13.4.3 | No directory listings | Met | Staff web serves four named embedded files (`internal/server/web.go` serveStatic); the Relay site handler uses `ServeContent` on validated paths (`internal/relay/site.go:277`); the console embed dir has an index.html and no subdirectories. |
| V13.4.4 | HTTP TRACE not supported | Met | chi routes by method, with no TRACE handlers; the Relay CONNECT listener refuses every other method (`internal/relay/relay.go:286`). |
| V13.4.5 | Internal API docs and monitoring endpoints not exposed | Met | OpenAPI files are not served; only `/healthz` and `/readyz` (`internal/server/server.go`, `internal/registry/server.go`), returning status only, by design. |

## V14 Data Protection

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V14.1.1 | Sensitive data identified and classified into protection levels | Met | docs/SECURITY.md "What we protect" (asset table: PHI, keys, credentials, business data) and "treat all patient data as PHI"; AGENTS.md rule that PHI lives only in the Back Office. |
| V14.1.2 | Each protection level has documented protection requirements | Gap | SECURITY.md covers encryption, logging and access for PHI, but no retention or disposal requirement is set for PHI or logs (OBSERVABILITY.md lists retention periods as an open decision). |
| V14.2.1 | No sensitive data in URLs or query strings | Met | Query params are status/date/UUID filters only (`internal/server/messages.go:173`, `appointments.go:211`); tokens are in bodies/headers; provider set-up tokens are in the URL fragment (SECURITY.md #100); invite codes are posted to `/pairing/redeem`. |
| V14.2.2 | Sensitive data not cached in server components | Met | No caching proxy or load balancer: the Relay forwards opaque TLS 1.3 bytes (`internal/relay/relay.go` handleConnect) and the Back Office serves from Postgres directly. |
| V14.2.3 | No sensitive data sent to trackers or third parties | Met | Staff web and console load only same-origin scripts (`web/index.html`, `internal/registry/console/index.html`); no analytics SDKs in iOS; push payloads are fixed categories (SECURITY.md "Push and website"). |
| V14.2.4 | Data controls implemented as documented | Gap | No-PHI logging is enforced by `TestNoPHIInLogs` and backups by `deploy/backup.sh`, but retention is undefined (see V14.1.2), and FileVault and disabled auto-login on the Mac mini are documented only, with no check. |
| V14.3.1 | Client clears authenticated data at session end | Met | Staff web: logout and any 401 set `session = null` and re-render the login screen (`web/app.js` logout, api); cookie cleared server-side (`internal/server/staff.go` clearCookie); nothing in browser storage. iOS uses an ephemeral URLSession and caches no PHI (`ios/WeHelp/Services/OfficeTransport.swift:20`). |
| V14.3.2 | Cache-Control: no-store on sensitive responses | Gap | `no-store` is set only on selected handlers (staff session, pairing, visits, roster, backups, audit verify). PHI responses via `writeJSON` (`internal/server/server.go:279`), such as patients, messages and appointments, carry no Cache-Control. |
| V14.3.3 | Browser storage holds no sensitive data beyond session tokens | Met | Only the `__Host-` HttpOnly session cookie (`internal/server/staff.go:149`); no localStorage/sessionStorage/IndexedDB use in `web/*.js` or the Registry console `app.js`. |

## V15 Secure Coding and Architecture

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V15.1.1 | Documented risk-based remediation time frames for dependencies | Gap | SECURITY.md "Secure development" says dependencies are pinned and scanned, but sets no remediation time frames by severity. |
| V15.1.2 | Maintained SBOM from trusted repositories | Gap | `go.mod`/`go.sum` pin Go modules with checksum verification, and iOS packages are in `ios/project.yml`, but no SBOM is generated or kept. |
| V15.1.3 | Document resource-heavy functions and availability defenses | Gap | No document lists costly operations (argon2 sign-in, site publish, roster sync, audit verify, visit long-poll) or their limits. |
| V15.2.1 | Components within documented remediation time frames | Gap | `govulncheck` v1.9.0 runs on every PR (`.github/workflows/ci.yml:37-38`) and the toolchain is pinned to go1.26.11, but there are no documented time frames to measure against (V15.1.1). |
| V15.2.2 | Defenses against resource-exhaustion based on documented strategy | Gap | Partial controls: account lockout, Relay `MaxChannels`/push rate (`internal/relay/relay.go`), TURN quotas, 60 s handler timeout. But 18 Back Office handlers decode `r.Body` with no `MaxBytesReader` (e.g. `internal/server/auth.go` Register/Login), and there is no documented strategy. |
| V15.2.3 | Production excludes test, sample and dev functionality | Gap | `wehelpd seed` with hard-coded demo passwords ships in the field binary (`cmd/wehelp/seed.go`, `internal/seed/seed.go:36-46`), contrary to SECURITY.md "never present in a field build". Self-registration is gated only by `env != "dev"`, and env defaults to dev. |
| V15.3.1 | Return only needed fields from data objects | Met | Responses use explicit view structs (e.g. `patientView` in `internal/server/patients.go:18`); no `select *` in code. |
| V15.3.2 | Backend HTTP clients don't follow redirects unless intended | Gap | `http.Client`s for Registry install/roster (`cmd/wehelp/init.go:45`, `cmd/wehelp/serve.go:92`), Relay list sync (`internal/relay/relay.go:88`) and APNs (`internal/relay/apns.go:103`) set no `CheckRedirect`, so they follow up to 10 redirects. |
| V15.3.3 | Mass-assignment protection: per-action allowed fields | Met | Each handler decodes into its own request struct holding only the fields that action may set (e.g. `createPatientRequest`, `internal/server/patients.go:67`); Registry uses `DisallowUnknownFields` (`internal/registry/server.go` install). |
| V15.3.4 | Original client IP taken from trusted sources only | Met | chi `RealIP` removed so forwarding headers are never trusted (`internal/server/server.go` Router comment, #43); no IP-based security decisions. The Relay deliberately drops device IPs (`TestRelayLogsNoDeviceAddress`). |
| V15.3.5 | Strict typing and comparisons, no type juggling | Met | Go static typing; JSON into typed structs; `uuid.Parse` and `strconv` for path and query values (`internal/server/visits.go:404`, `internal/registry/console.go` pathID). |
| V15.3.6 | JavaScript resistant to prototype pollution | Met | Vanilla JS with no deep-merge or `Object.assign` of untrusted input; object keys are server UUIDs or fixed `data-*` attribute names (`web/app.js:358-409,1081`, `web/staff.js:432`). |
| V15.3.7 | Defenses against HTTP parameter pollution | Met | Each value is read from one source: chi path params, JSON body, or a specific `URL.Query().Get` (`internal/server/appointments.go:211`); no merged parameter maps. |

## V16 Security Logging and Error Handling

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V16.1.1 | Log inventory: layers, events, formats, storage, access, retention | Gap | SECURITY.md "Logging and audit" and OBSERVABILITY.md describe the audit table and Relay logs (7 days), but Back Office/Registry stdout log storage and retention and audit retention are undecided (OBSERVABILITY.md "Decisions the PoC must inform"). |
| V16.2.1 | Log entries carry when/where/who/what | Gap | `audit_events` records actor, device, tenant, action and time (`internal/audit`). The request log (`internal/server/server.go` requestLogger) has method/path/status/request_id but no actor or device, so refused requests can't be attributed. |
| V16.2.2 | Synchronized clocks; UTC or explicit offset timestamps | Gap | The Back Office and Registry use the default slog handler (no `slog.SetDefault` in `cmd/wehelp` or `cmd/wehelp-registry`), which prints local time with no zone offset. Only the Relay sets a TextHandler with RFC 3339 (`cmd/wehelp-relay/main.go:171`). Time sync isn't documented. |
| V16.2.3 | Logs go only to inventoried destinations | Met | Back Office/Registry to stdout, Relay to mode-600 daily files or stderr (`internal/logfile`), audit to `audit_events`; no other sinks in code (OTLP export not built). |
| V16.2.4 | Logs in a common, machine-correlatable format | Gap | OBSERVABILITY.md says "stdout JSON", but the Back Office and Registry emit the default slog text format, and the Relay emits slog text; nothing outputs JSON. |
| V16.2.5 | Sensitive data logged per its protection level | Met | IDs only, enforced by `TestNoPHIInLogs` (`internal/server/logs_phi_db_test.go:41`, covers tokens, codes, SDP, names) and `TestRelayLogsNoDeviceAddress`; tokens stored and logged only as SHA-256 or fingerprints. |
| V16.3.1 | Log all authentication successes and failures with factor | Gap | Audited: staff sign-in success and failure with reason (`internal/server/staff.go` recordFailure), auth.login, refresh, enroll, pair, console sign-in. Not logged beyond a status line: unknown-account or locked-account sign-ins (`checkStaffPassword` returns before recordFailure), failed refresh/device proof (`internal/server/devices.go:124-147`) and failed pairing redeem (`internal/server/pairing.go:327,345`). |
| V16.3.2 | Log failed authorization attempts | Gap | 403/404 refusals appear only as request-log status lines with no actor (`requestLogger`); no audit or structured event for denied access. |
| V16.3.3 | Log documented security events and control-bypass attempts | Gap | Input-validation rejections, CSRF failures, lanOnly refusals and anti-automation hits aren't logged as security events; only Relay office refusals are (`internal/relay/relay.go` authorize, "relay: office refused"). |
| V16.3.4 | Log unexpected errors and security control failures (e.g. TLS) | Gap | Most handler 500s write a fixed message without logging the cause (e.g. "tx failed", `internal/server/auth.go`); Relay CONNECT and site `ErrorLog` are discarded (`internal/relay/relay.go`, `site.go`), losing TLS handshake failures. Panics are logged by chi Recoverer, and the audit-chain break is logged (`RunAuditVerify`). |
| V16.4.1 | Log encoding prevents log injection | Met | slog key=value handlers quote values containing spaces or control characters; request paths and errors are attributes, and messages are constants (`internal/server/server.go` requestLogger). |
| V16.4.2 | Logs protected from unauthorized access and modification | Gap | `audit_events` is append-only (DB triggers) and hash-chained with verification (`internal/audit`, `wehelpd audit verify`, #24); Relay files are mode 600. But the chain head isn't exported (#56, so a full rewrite is undetectable), and Back Office stdout logs in Docker are editable by the host admin. |
| V16.4.3 | Logs shipped to a separate system | Gap | The Alloy/OTLP/syslog export in OBSERVABILITY.md isn't implemented; no export code in internal/ or deploy/. |
| V16.5.1 | Generic error messages, no internal details | Met | chi `Recoverer` returns a bare 500 (`internal/server/server.go`, `internal/registry/server.go`); handlers return fixed strings or validation messages. One admin-only route echoes the Registry fetch error (`internal/server/roster.go:305`). |
| V16.5.2 | Secure operation when external resources fail | Met | The Relay keeps its last verified office list (`internal/relay/relay.go` loadCache); the tunnel reconnects with backoff (`internal/tunnel/tunnel.go:126`); roster failures are recorded and served stale (`internal/server/roster.go:52`); visits report `video_unavailable`. |
| V16.5.3 | Fail closed on errors, no fail-open | Met | PHI reads fail with 500 if the audit write fails (`internal/server/audit_reads.go`, SECURITY.md #40); writes roll back in one tx with `audit.Record`; pins and DTLS fingerprint mismatch end the connection (`ios/WeHelp/Services/VisitCall.swift` verifyFingerprint). |

## V17 WebRTC

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| V17.1.1 | TURN can't reach reserved/internal IPv4 or IPv6 addresses | Met | `PermissionHandler` allows only `peer.Equal(opt.PublicIP)`, the Relay's own relay address (`internal/relay/turn.go:100`), on UDP, TCP and TLS listeners; IPv6 peers never match the IPv4 public IP. |
| V17.2.1 | DTLS certificate key managed per key policy | N/A | No media server of ours (no SFU/MCU/recorder); DTLS keys are ephemeral libwebrtc keys on each phone, checked against the SDP fingerprint (`ios/WeHelp/Services/VisitCall.swift` verifyFingerprint). |
| V17.2.2 | Media server uses approved DTLS suites and SRTP profiles | N/A | No media server; TURN relays opaque SRTP. Endpoints prefer AES-GCM SRTP and disable AES128_SHA1_32 (`VisitCall.swift` cryptoOptions). |
| V17.2.3 | Media server checks SRTP authentication | N/A | No media server; SRTP is authenticated end to end by libwebrtc on the phones. |
| V17.2.4 | Media server survives malformed SRTP | N/A | No media server; the pion TURN relay doesn't parse SRTP. |
| V17.3.1 | Signaling rate-limited to survive floods | Gap | Signaling (`internal/server/visits.go`) caps message size and keeps the last 400 signals per room (`visitMaxSignals`, line 93), but there is no per-sender rate limit, so a flooding participant can push out legitimate signals. |
| V17.3.2 | Signaling robust to malformed messages | Met | `MaxBytesReader`, SDP and candidate size caps, and JSON validation of candidates (`internal/server/visits.go:459-472`); typed parsing with `strconv` for `after`. |

## Gaps and where they're tracked

| Row | Tracked in |
|---|---|
| V1.2.1 | #159 (fixed) |
| V1.2.3 | #159 (fixed) |
| V2.1.3 | #168 |
| V2.4.1 | #143 |
| V3.2.2 | #159 (fixed) |
| V3.4.1 | #160 |
| V3.4.3 | #160 (fixed) |
| V3.4.4 | #160 |
| V3.4.5 | #160 (fixed) |
| V3.4.6 | #160 (fixed) |
| V3.5.1 | #143 |
| V4.1.1 | #160 (fixed) |
| V4.1.2 | #166 |
| V5.2.2 | #170 |
| V5.4.1 | #170 |
| V5.4.3 | #170 |
| V6.1.1 | #143, #162 |
| V6.1.2 | #162 |
| V6.1.3 | #162 |
| V6.2.2 | #162 |
| V6.2.3 | #162 |
| V6.2.4 | #162 |
| V6.2.11 | #162 |
| V6.2.12 | #162 |
| V6.3.2 | #161 |
| V6.3.3 | #163 |
| V6.3.4 | #163 |
| V6.4.3 | #162 |
| V6.4.4 | #162 |
| V6.5.5 | #162 |
| V6.6.3 | #143 |
| V7.1.1 | #162 |
| V7.1.2 | #162 |
| V7.2.4 | #162 |
| V7.3.2 | #163 |
| V7.4.1 | #162 |
| V7.4.5 | #162 |
| V7.5.1 | #163 |
| V7.5.2 | #162 |
| V8.1.2 | #168 |
| V8.2.3 | #168 |
| V11.1.1 | #168 |
| V11.1.2 | #168 |
| V11.2.2 | #168 |
| V11.3.2 | #166 |
| V12.1.2 | #166 |
| V12.2.1 | #166 |
| V12.2.2 | #166 |
| V12.3.1 | #166 |
| V12.3.3 | #166 |
| V13.2.1 | #161 |
| V13.2.2 | #161 |
| V13.2.3 | #161 |
| V13.3.1 | #161 |
| V13.4.2 | #161 |
| V14.1.2 | #167 |
| V14.2.4 | #167 |
| V14.3.2 | #160 |
| V15.1.1 | #168 |
| V15.1.2 | #165 |
| V15.1.3 | #168 |
| V15.2.1 | #168 |
| V15.2.2 | #143 |
| V15.2.3 | #161 |
| V15.3.2 | #166 |
| V16.1.1 | #167 |
| V16.2.1 | #167 |
| V16.2.2 | #167 |
| V16.2.4 | #167 |
| V16.3.1 | #167 |
| V16.3.2 | #167 |
| V16.3.3 | #167 |
| V16.3.4 | #143, #167 |
| V16.4.2 | #167 |
| V16.4.3 | #167 |
| V17.3.1 | #143 |

### Notes from the review

- V1.2.1, staff web and Registry console: replace `esc()` in attribute values with `attr()` (encodes `"` and `'`), e.g. `console/app.js:213-219` and `web/app.js:730`.
- V1.2.3, staff web (`web/app.js:287,730,829`): drop inline `onclick` handlers that carry names. Use `data-` attributes plus `addEventListener`, or pass IDs only.
- V2.1.3, Back Office: document per-user and global limits (messages, appointment requests, pairing redeem, refresh) in SECURITY.md or openapi.yaml.
- V2.4.1, Back Office and Registry: add a per-client or per-token rate limiter (e.g. on pairing redeem, refresh, messages, `/v1/install`), and wrap every JSON decode in `http.MaxBytesReader`.
- V3.2.2, staff web and Registry console: render text with `textContent` or DOM building, or escape every interpolation, and add a CSP backstop.
- V3.4.1, Back Office staff TLS, Registry, Relay site: send `Strict-Transport-Security: max-age=31536000; includeSubDomains`.
- V3.4.3, staff web and Registry console: add a CSP to the staff web (requires moving inline handlers out), and add `object-src 'none'; base-uri 'none'` to the console CSP.
- V3.4.4, Back Office (staff web and API) and Registry API: add global middleware that sets `X-Content-Type-Options: nosniff`.
- V3.4.5, staff web: send `Referrer-Policy: no-referrer` from `serveStatic` or global middleware.
- V3.4.6, staff web: send `Content-Security-Policy: frame-ancestors 'none'` (and `X-Frame-Options: DENY`) on all Back Office responses.
- V3.5.1, Back Office and Registry login and set-up routes: require `Content-Type: application/json` or check `Origin`/`Sec-Fetch-Site` on `/staff/login`, `/staff/setup` and console `/login`.
- V4.1.1, staff web: serve `text/css; charset=utf-8` and `text/javascript; charset=utf-8` in `internal/server/web.go` serveStatic.
- V4.1.2, Back Office: on the plain-HTTP listener, answer `/api/*` with an error instead of a 308 redirect (`server.go` healthOrRedirect).
- V5.2.2, Relay: sniff each bundle file's contents against its extension in `ReadSiteBundle`, as the Back Office does.
- V5.4.1, Relay: send `Content-Disposition: attachment; filename="<validated base name>"` in SiteHandler.
- V5.4.3, Back Office: scan uploads (e.g. ClamAV) before storing or publishing, or record a formal risk acceptance in SECURITY.md.
- V6.1.1, Back Office and Registry (docs): document the lockout settings, LAN-only exposure, and how deliberate lockout is limited (for example a per-IP limit or lockout scoped by IP).
- V6.1.2, Back Office and Registry (docs): write a context-specific banned-word list (WeHelp, practice name, role names, product codenames).
- V6.1.3, Back Office (docs): add one table to SECURITY.md listing every sign-in pathway (staff web, `/auth/login`, set-up link, QR pairing for self, admin, patient, dev register, console) with its factors.
- V6.2.2, Back Office staff web and Registry console: add a signed-in password-change route.
- V6.2.3, Back Office and Registry: the new change route must ask for the current password and a TOTP code.
- V6.2.4, Back Office and Registry: check new passwords against a bundled top-common-passwords list in `StaffSetup`, `staff add` and `AddStaff`.
- V6.2.11, Back Office and Registry: reject passwords containing the banned context words at set-up and change.
- V6.2.12, Back Office and Registry: settle the open decision and add a breached-password check (a local list keeps the office offline-safe).
- V6.3.2, Back Office: make `wehelpd seed` refuse to run unless `env=dev`, or compile it out of production builds.
- V6.3.3, iOS: create the Secure Enclave key with `SecAccessControl` `.privateKeyUsage` + `.userPresence` so each proof needs Face ID or passcode, or document the rationale.
- V6.3.4, Back Office: drop admin pairing of other staff accounts, or require the target's password+TOTP at redeem, and document it.
- V6.4.3, Back Office and Registry: add an admin-initiated reset (a new single-use set-up link that clears the password and TOTP and re-enrolls) with a runbook.
- V6.4.4, Back Office and Registry: add a TOTP reset that needs admin action plus in-person or set-up-link re-enrollment, audited.
- V6.5.5, Back Office and Registry: accept only the current and previous TOTP step (or document the ±1 window), and shorten set-up links or justify 24 hours.
- V6.6.3, Back Office: add per-source rate limiting on `/pairing/redeem` and `/staff/setup`, or record an accepted-risk rationale based on 128/256-bit entropy.
- V7.1.1, Back Office and phones (docs): document device-session idle and absolute limits and the NIST 800-63B deviation rationale.
- V7.1.2, Back Office and Registry (docs): document the concurrent-session policy for staff web, console and devices.
- V7.2.4, Back Office staff web and Registry console: revoke any session in the incoming cookie when sign-in succeeds.
- V7.3.2, Back Office phones: track the session start (the first refresh token in the chain) and refuse refreshes after an absolute limit.
- V7.4.1, Back Office `/auth/login` bearer path: on `Logout` of an unbound token, also block the outstanding access JWT (for example a per-user `tokens_valid_after`), or drop the unbound bearer path.
- V7.4.5, Back Office and Registry: let admins end sessions for admin accounts and all users, and add a console-staff disable command.
- V7.5.1, Back Office: require a fresh password+TOTP before `CreateInvite` for a staff account.
- V7.5.2, staff web and console: add a session list with "end my other sessions". (Phones: accepted, see V7.4.4.)
- V8.1.2, Back Office (docs): document field-level read and write rules per role (for example what office staff may see on a patient record).
- V8.2.3, Back Office: once V8.1.2 exists, review handlers against it and add tests for response and request field filtering.
| ID | Component | Suggested fix |
|---|---|---|
| V11.1.1 | All (docs) | Write a key-management policy with cryptoperiods, owners and destruction, per NIST SP 800-57. |
| V11.1.2 | All (docs) | Add a crypto inventory listing every key, algorithm and certificate, with its allowed uses. |
| V11.2.2 | iOS, Back Office | Support a pin set (current plus next) so office/Relay key rotation doesn't force re-pairing; record algorithm IDs where they're hard-coded. |
| V11.3.2 | Back Office staff HTTPS, Relay sites/TURN TLS, Registry | Set `CipherSuites` to ECDHE AES-GCM/ChaCha20 only on TLS 1.2 listeners, or require TLS 1.3. |
| V12.1.2 | Back Office staff HTTPS, Relay sites/TURN TLS, Registry | Same fix as V11.3.2. |
| V12.2.1 | Registry, Back Office install | Refuse to start the Registry without TLS outside dev, and reject `http://` Registry URLs in `Install` outside dev. |
| V12.2.2 | Relay (sites) | Require `relay_site_cert` (or autocert) in production; record the pinned-self-signed CONNECT cert as an accepted decision. |
| V12.3.1 | Back Office, Registry | Use TLS (or a unix socket) for Postgres, or record a decision for localhost-only DB; enforce TLS on the Registry. |
| V12.3.3 | Back Office, Relay to Registry | Enforce `https` for Registry calls outside dev. |
| V13.2.1 | Back Office, Registry (Postgres) | Generate a per-install DB password into `.env`, or use peer/cert auth. |
| V13.2.2 | Back Office, Registry, Docker image | Separate the migration (owner) and runtime (DML-only) DB roles; use `distroless/static:nonroot`. |
| V13.2.3 | Deploy (compose, config defaults) | Remove `wehelp`/`wehelp` defaults; have `appliance.sh init-env` generate the DB password. |
| V13.3.1 | All | Keep demo credentials out of field builds and default secrets out of the repo; document file-based secret storage as the accepted approach, or use the macOS Keychain. |
| V13.4.2 | Back Office | Make `env` default to production and require an explicit `dev`. |
| V14.1.2 | Docs | Define retention and disposal for PHI, audit and operational logs. |
| V14.2.4 | Back Office host | Implement the defined retention; add a host check (e.g. `fdesetup status`) for FileVault at install or startup. |
| V14.3.2 | Back Office API | Set `Cache-Control: no-store` in middleware for all `/api/` responses. |
| V15.1.1 | Docs | Document remediation SLAs by severity (e.g. critical 7 days, high 30 days). |
| V15.1.2 | CI | Generate an SBOM (e.g. CycloneDX for Go modules and SwiftPM) on release. |
| V15.1.3 | Docs | Document costly operations and their limits (argon2, publish, long-poll, audit verify). |
| V15.2.1 | CI/process | Track dependency age and vulnerabilities against the SLAs from V15.1.1. |
| V15.2.2 | Back Office | Wrap every JSON body in `http.MaxBytesReader`; add per-actor rate limits for sign-in and costly routes. |
| V15.2.3 | Back Office binary | Move `seed` and demo passwords behind a build tag excluded from field builds. |
| V15.3.2 | Back Office, Relay | Set `CheckRedirect` to return `http.ErrUseLastResponse` on backend HTTP clients. |
| V16.1.1 | Docs | Write a log inventory: each log, its format, storage, access and retention. |
| V16.2.1 | Back Office | Add actor/device ID (not PHI) to request log lines. |
| V16.2.2 | Back Office, Registry | Use `slog.NewJSONHandler` with UTC timestamps; document NTP on hosts. |
| V16.2.4 | Back Office, Registry, Relay | Emit JSON logs, as OBSERVABILITY.md states. |
| V16.3.1 | Back Office | Log unknown-account/locked sign-in attempts and failed refresh, device-proof and pairing attempts (IDs only). |
| V16.3.2 | Back Office, Registry | Log 401/403 denials with actor, route and reason. |
| V16.3.3 | Back Office | Log CSRF failures, lanOnly refusals and validation rejections as security events. |
| V16.3.4 | Back Office, Relay | Log the underlying error on 500s; keep TLS handshake failures with addresses redacted instead of discarding them. |
| V16.4.2 | Back Office | Export the audit chain head off the box (#56); ship app logs to append-only storage. |
| V16.4.3 | All | Implement the Alloy/OTLP or syslog-over-TLS export from OBSERVABILITY.md. |
| V17.3.1 | Back Office (visit signaling) | Add per-participant rate limiting on visit signal posts. |
