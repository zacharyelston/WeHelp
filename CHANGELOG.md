# Changelog

All notable changes to WeHelp are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed
- The ecosystem diagram shows the practice side of the Relay: the Back Office
  as a mini PC appliance, and the front desk as a laptop using the staff web
  over the office Wi-Fi, where the pairing QR code is shown.
- The readme opens with a hero diagram of the WeHelp ecosystem
  (`presentation/img/ecosystem.svg`): the app and the Back Office tied
  together by QR pairing, the Relay, the Registry and the practice website
  and blog. The presentation has a Blog slide after the website (23 slides).
- The presentation covers the new features: the front desk's day, the
  practice inbox, care teams, practice types, and switches with provider
  office hours, with an updated status slide (22 slides).
- The readme and the presentation lead with who we are: built by providers,
  for providers, from 30 years in healthcare services technology and patient
  services, with licensed providers on the board of directors. The deck has a
  new "Who we are" slide after the cover, and its page numbers now count
  themselves.
- The readme opens with screenshots (staff web, iPhone app, practice website)
  and links to the overview presentation, published from `presentation/` to
  GitHub Pages (https://zacharyelston.github.io/WeHelp/) by
  `.github/workflows/pages.yml`. The presentation is a static copy of the
  WeHelp overview deck: arrow keys or swipe to move, and it prints as one
  slide per page.
- Proposal for other kinds of practice (docs/PRACTICE_TYPES.md, epic #112):
  presets with their own words for therapy, allied health and salons; switches
  so a practice or a provider can turn messaging off; a practice inbox; and
  care teams, where a patient approves the providers who share their thread.
- AGENTS.md and CONTRIBUTING.md brought up to date (#13):
  - issues and PRs live in zacharyelston/WeHelp;
  - V1 milestones and the docs to read first;
  - required CI checks and `-race`;
  - the V1 rules: patient data only in the Back Office, no patient
    passwords, and the OpenAPI spec kept in step with the router;
  - Flyway migrations (the last goose reference is removed).
- WeHelp Registry added to the V1 architecture: a fourth service, the
  corporate control plane and support arm.
  - WeHelp support registers practices and their providers in a Registry
    console, issues single-use install tokens, and assigns each practice to
    a Relay.
  - Relays accept only offices on an ed25519-signed, versioned office list
    pulled from the Registry. They cache the last good list and reject
    rollbacks; suspending a practice disconnects it within a minute.
  - Back Offices enroll with an install token and pull their provider roster
    from the Registry.
  - The Registry holds no patient data and has an import-boundary test.

  Services are now Back Office, Relay, Registry and two mobile clients
  (patient app and provider app, #69), plus dashboards for the first three.
  The in-office Kiosk iPad (#8) is a post-V1 client that talks to the Back
  Office over the office LAN. Security, plan, observability and lit review updated.
- Observability designed (docs/OBSERVABILITY.md):
  - a Grafana Alloy sidecar on every Back Office and Relay;
  - optional Grafana, Loki and Prometheus dashboards that can run anywhere,
    behind a load guard that stops them on small hosts under pressure;
  - an ops stream with no identifiers, and a separate audit stream (the
    `audit_events` chain, IDs only) sent only to BAA-covered destinations
    over syslog or OTLP;
  - WeHelp Cloud Dashboards specified as a PoC corporate offering.

  Plan track V1-OBS added to docs/V1_PLAN.md.
- V1 architecture defined (docs/ARCHITECTURE.md rewritten, plus
  docs/SECURITY.md and docs/V1_PLAN.md). In V1:
  - A local Back Office holds all patient data.
  - The Back Office connects out to a Relay over SSH with an office key.
  - Phones reach their office through the Relay with HTTP `CONNECT` and
    TLS end to end to a Back Office key pinned by in-office QR pairing.
  - Patients have no passwords; device keys are their credential.
  - The staff UI is LAN-only with TOTP.
  - Telehealth is 1:1 WebRTC with media through TURN on the Relay.
  - Push and the practice website go through the Relay with no PHI.

  The bar is HIPAA technical safeguards plus OWASP ASVS L2 and MASVS L2,
  with an external audit and pen test before field use. V1_PLAN.md sets
  milestones V1-M0 to V1-M7 and lists what is kept, replaced and deferred
  from the PoC. The lit review covers SSH, `CONNECT`, TURN, TOTP and
  backups.
- CI and `make test` run Go tests with the race detector (`-race`). `main`
  is protected: the `go`, `ios` and `docker` CI jobs must pass and branches
  must be up to date before a PR merges (#30).
- iOS signing: automatic signing with team `WZYFJ8TSWD`, bundle ID
  `com.zacelston.wehelp` (`com.wehelp.app` is not registered to this team).
  The HealthKit entitlement is kept; it provisions on this paid team.
- Migrations moved from embedded goose to **Flyway**: `db/migrations/
  V{n}__name.sql`, `flyway_schema_history` + checksum enforcement. The
  compose `migrate` service runs before `server`; `make db-migrate` is the
  containerized path and `wehelpd migrate` wraps a local flyway CLI.
  `serve`/`seed` no longer auto-migrate; `WEHELP_AUTO_MIGRATE` is gone.
  Existing goose-managed dev DBs need `flyway baseline` or a fresh volume.
- Server binary renamed `wehelp` → `wehelpd`: viper found the binary itself
  when it sat at the container's CWD and tried to parse it as YAML.

### Added
- Practice blog (#121).
  - The Website tab has a **Blog** card. Staff write timely or seasonal
    posts: a title, the text, an optional uploaded image, a go-live time,
    and a draft flag. They can also edit and delete posts.
  - The website gets a Blog page (newest first), a page per post, and the
    latest three posts on the home page. The Blog link appears only once
    there are posts. Drafts and future posts stay off the site.
  - Once the site has been published, the Back Office republishes it by
    itself within a minute of a scheduled post coming due. That publish is
    audited with no actor and marked `scheduled`.
  - Posts are plain text, escaped by the templates, and practice content
    only. Audited as `site_post.create`, `site_post.update` and
    `site_post.delete`.
  - Routes under `/api/v1/site/posts` (staff, LAN only). Migration
    `V17__site_posts.sql`.
- Care teams (#111, docs/PRACTICE_TYPES.md phase 4).
  - A patient chooses a group of providers who share one thread with them,
    for continuity of care.
  - A provider already caring for the patient proposes a colleague, who
    joins only when the patient approves. The patient can also add a
    provider directly.
  - Members are providers, never the front desk or admins.
  - A new member sees earlier messages only if the patient allows it. A
    removed member sees nothing, past or future.
  - Every member's read of the thread is audited (`care_team.read`), and
    the patient sees who read it and when.
  - Disabling a provider removes them from every team.
  - A new **Care teams** switch: off by default for therapy, never on for
    a practice that isn't clinical.
  - Providers get a **Care teams** tab in the staff web. Patient approval
    in the app waits for the test devices; the API is ready.
  - Routes under `/api/v1/care-teams`. Migration `V16__care_teams.sql`.
- Practice inbox (#110, docs/PRACTICE_TYPES.md phase 3).
  - Patients message "the practice", and the front desk and admins answer
    in a new staff web **Inbox** tab. Each reply is signed with the staff
    member's name.
  - Providers don't see it, and it is a table of its own
    (`practice_messages`), separate from provider threads.
  - It follows the messages switch. A provider with messages off now
    points patients to the practice inbox.
  - The front desk no longer sees the always-empty Messages tab.
  - Routes: `GET`/`POST /api/v1/practice-inbox`,
    `GET /api/v1/practice-inbox/{patientID}` and
    `POST /api/v1/practice-inbox/read`. Patients use them on any channel;
    the front desk only on the office LAN. Audited as
    `practice_message.create`; pushes are generic. Migration
    `V15__practice_inbox.sql`.
  - The app screen follows when the test devices are back.
- Practice and provider switches (#109, docs/PRACTICE_TYPES.md phase 2).
  - Settings has a **Features** card. An admin turns off messages,
    appointment requests, reminders, check-ins, video, the store, the
    library or the website.
  - Each practice type has defaults: therapy starts without the store and
    check-ins; a salon without check-ins, video and the library.
  - A switched-off feature disappears from the staff web, and the server
    refuses it with 409, so older app versions can't use it either. Staff
    keep the catalogue while the store is off. The reminder worker skips
    reminders and check-ins that are off. The website drops its Store and
    Library pages when those are off. Nothing is deleted.
  - Providers choose how they take messages, under **My profile** →
    Messages:
    - on, as before;
    - office hours: messages arrive any time, but outside the hours the
      patient gets an automatic reply (at most once every 12 hours) and
      the provider gets no notification;
    - off: patients get a 409 asking them to call the practice. Staff can
      always write to patients.
  - New routes: `PUT /api/v1/practice/features` (admins, audited as
    `practice.features`) and `GET`/`PUT /api/v1/staff/profile/messages`
    (audited as `staff.message_settings`). Automatic replies are audited
    as `message.auto_reply`. Migration `V14__switches.sql`.
- Practice types (#108, docs/PRACTICE_TYPES.md phase 1).
  - An admin picks the practice type on a new **Settings** screen:
    naturopathic, therapy and psychology, allied health, or salon and
    personal services.
  - The type sets the words: patient or client; provider, practitioner,
    therapist or stylist. The staff web and the website use them (the
    website at its next publish). The API and database still say patient
    and provider.
  - A salon is not clinical, so the staff web hides check-in reminders.
    Security is the same for every type.
  - New routes: `GET /api/v1/practice` for anyone signed in (the app will
    read the words, and it holds no patient data), and `PUT` for admins on
    the LAN, audited as `practice.update`. Migration
    `V13__practice_settings.sql`.
  - Not yet: the app using the words, and the Registry recording the type
    (waits for the Registry console, #99).
- Office role for the front desk (#106).
  - A third staff role, `office`, sees every patient and every provider's
    schedule and appointment requests. The front desk books, changes and
    confirms visits for any provider (`provider_id` on
    `POST /appointments`), and works orders, the store, the website,
    patients and pairing. It cannot read message threads or manage staff.
  - `admin` keeps everything plus setup. Its Providers screen becomes
    **Staff** and invites providers or front-desk accounts.
  - Appointment lists carry provider and patient names. The staff web
    shows a Provider column and picker for the front desk and admins.
  - The seed adds `frontdesk@demo.wehelp` / `wehelp-demo-office`, and the
    sign-in page lists it. `wehelpd staff add --role office` also works.
    Office accounts can't pair their own phone yet: the app has no
    front-desk view. Migration `V12__office_role.sql`.
- Website themes (#104).
  - The Website tab has a **Look** card with six colour schemes: sage
    green, ocean blue, sunset coral, plum, warm clay and midnight (dark).
  - Each theme colours the header, footer, a banner on the home page,
    buttons, cards and photo frames. Visitors in dark mode get a dark
    version with the theme's accent. The choice is saved with the site
    content and applied at the next publish; the default is sage.
  - Fixed: page content no longer sits flush against the header.
- Staff profiles for the website bios (#102).
  - Every staff member has **My profile** in the staff web: name as shown,
    credentials, bio and a photo (PNG, JPEG or WebP, at most 2 MB, checked
    by content). They choose whether the practice website shows them.
  - Publishing the website turns those profiles into provider bios with
    their photos. A bio typed in the Website tab with the same name is
    replaced; other typed bios stay. The Website tab lists which bios come
    from profiles. Disabled accounts never appear.
  - New routes `/api/v1/staff/profile` and `/api/v1/staff/profile/photo`
    cover your own profile only. They are LAN only and audited as
    `staff.profile_update`. Migration `V11__staff_profiles.sql` adds them.
- Admins register providers from the staff web (#100).
  - A **Providers** button (admins only) lists the practice's provider
    accounts. An admin adds one by name and email and gets a set-up link.
  - The link works once, for 24 hours, on the office network. Its token is
    in the URL fragment, and only its SHA-256 is stored. The provider opens
    it, chooses their own password and enrolls TOTP as at a first sign-in.
    Admins never see or set the password. A new link replaces the old one.
  - Admins can disable a provider and enable them again. Disabling ends web
    sessions, revokes refresh tokens and refuses sign-in and API calls.
    Nothing is deleted, and paired devices stay paired.
  - New routes: `/api/v1/staff/setup` and `/api/v1/staff/providers…`. They
    are LAN only, return 403 for providers, and are audited (`staff.invite`,
    `staff.setup`, `staff.disable`, `staff.enable`). Migration
    `V10__staff_invites.sql` adds them.
  - This is the Back Office half of #62: Registry roster entries will feed
    the same invite. `wehelpd staff add` still works on the host.
  - The demo seed adds an admin, `admin@demo.wehelp`. The staff sign-in page
    lists it under Dr. Ada's demo login.

- Practice website with a library and a supply store (#97, most of #5).
  - Staff build the site on a new staff web **Website** tab: about us,
    hours and contact, provider bios, services, "Get the app and join" (the
    download and the in-office QR pairing steps), podcast links, and
    uploaded PDFs and images (10 MB each; contents must match the type).
  - **Save and publish** renders six static pages with `html/template` and
    streams them as a tar over a new `wehelp-site` SSH channel on the
    office's Relay connection. The Relay files the site under the office ID
    that connection authenticated as, and serves it read-only at
    `https://<relay>:8444/s/<office-id>/` (`relay_site_addr`,
    `relay_site_public_url`, optional `relay_site_cert`/`relay_site_key`).
  - The Relay accepts only `.html .css .txt .pdf .png .jpg .jpeg .webp
    .ico`, at most 200 files, 10 MB each and 25 MB in all, refuses HTML
    with `<script`, and serves every file with a `default-src 'none'` CSP
    and `nosniff`. A suspended office's site goes away with its listing.
  - The **Store** tab keeps the catalogue (the seed and a button load ten
    demo vitamin, supplement and herbal products). The public site shows
    it and says "Order in the WeHelp app"; payment is at pickup or by
    invoice.
  - Patients order in the app's new **Store** tab (quantities, a note, My
    orders, cancel while placed), through the pinned tunnel only. Staff work
    orders on the staff web **Orders** tab (ready, fulfilled, cancelled),
    on the office LAN only. Every change is audited, and staff changes
    send the new fixed push category `order_update` ("Your supply order has
    an update.").
  - The app's **Library** (Store tab and Today) lists podcast links and
    opens downloads in Quick Look, fetched through the pinned tunnel.
  - Migration V9 adds `site_profiles`, `site_files`, `products`, `orders`
    and `order_items`. Still open on #5: `apple-app-site-association` and
    Universal Links.
- Appointment requests (#68, #39).
  - Patients tap **Request** on the Schedule tab and pick a provider, 1–3
    preferred days with a time of day, and an optional reason. Up to 3 can
    wait per provider, and a patient can cancel one while it waits.
  - Providers see waiting requests on the staff web Appointments tab. They
    confirm at an exact time, which books the visit with the usual overlap
    check, or decline with an optional note. Their iPad lists what's
    waiting.
  - The patient gets a message in their thread and a push. Two new fixed
    Relay categories carry no details: `appointment_update` ("Your
    appointment request has an update.") and `appointment_request` ("A
    patient has requested an appointment.").
  - Every step is audited. Migration V8 adds `appointment_requests`.
- Reminders (#19), sent by the Back Office as reminder messages from the
  provider, with a push through the Relay.
  - Appointment reminders at each `reminder_offsets` offset (default 24h
    and 1h). Only the closest one that has come due is sent, so late
    bookings and restarts don't send a burst. Rescheduling moves them;
    cancelled appointments and revoked links get none.
  - Check-in reminders on a cadence the provider sets per patient
    (`PUT /api/v1/links/{id}/checkin`, 1–90 days, or off), shown as a
    "Check-in reminders" choice on the web Links tab. They're skipped if
    the patient already checked in during the last period, and wait for
    08:00–20:00 office time (`WEHELP_TIMEZONE`).
  - A worker in `wehelpd serve` checks every `reminder_interval` (1m; 0
    turns it off) and stops with the server. Each reminder is recorded
    in `reminders_sent` under a key naming it, in the same transaction as
    the message and its audit event, so nothing is sent twice.
  - A minute's poll replaces the River jobs the plan named: same
    guarantees at this scale, with no second migration system next to
    Flyway.
- Staff web: the Appointments and Links tabs show people's names instead
  of ID prefixes. The booking form now defaults to tomorrow at the next
  hour in local time (it showed UTC), and to 30 minutes long (its end
  time used to equal its start).
- The Relay writes a new office list to its cache before using it, so the
  cache is never behind the list in use. This also fixes a flaky
  `TestListRollbackAndForgeryRejected`.
- iPad: the app turns to landscape and upside down (iPhone stays
  portrait), for staff iPads docked with a Bluetooth keyboard. Cmd-Return
  sends a message.
- Push notifications through the Relay (#7).
  - The Relay sends Apple pushes with a hand-rolled HTTP/2 + ES256 client;
    the APNs key never leaves the Relay. Only three categories exist
    (`new_message`, `appointment_reminder`, `visit_starting`), and the
    Relay supplies their fixed, generic text. Each office is limited to
    120 pushes a minute, and logs show the office, category and result,
    never the token.
  - The Back Office asks over its SSH tunnel (`wehelp-push`, through
    `tunnel.Pusher`). A new message pushes to every active device of the
    recipient. Tokens Apple rejects are cleared, and so are tokens of
    revoked devices.
  - `PUT` and `DELETE /api/v1/devices/current/push` (device sessions only)
    register a device's token; migration V6 stores it. The Relay keeps no
    tokens.
  - iOS asks for notification permission after unlock and registers its
    APNs token with every paired practice. Alerts show while the app is
    open too.
  - Relay config: `relay_apns_key`, `relay_apns_key_id`,
    `relay_apns_team`, `relay_apns_topic`.
- Message threads with names (#67).
  - `GET /api/v1/messages?with=<user>` returns the whole conversation with
    one linked user, in both directions, newest first and paginated. It's
    404 if the users aren't linked, and every thread read is audited
    (`messages.read_thread`).
  - `GET /api/v1/conversations` lists everyone you're linked with, with
    their name, the latest message, who sent it, and the unread count.
  - iOS Messages is now a conversation list that opens a chat-style
    thread with a reply box. Your own sent messages show, and opening a
    thread marks their messages read. The staff web Messages tab works
    the same way.
- Several practices per device (#89).
  - The app keeps an **account** for each practice it's paired with:
    practice, name and role. Each has **its own Secure Enclave key, refresh
    token and session** (Keychain items scoped by account), so nothing is
    shared between practices.
  - Settings → **Accounts** lists them, admin first, then provider, then
    patient. Tap one to switch the whole app, or use **Pair another
    practice**. Re-pairing the same person at the same practice replaces
    their entry.
  - If a practice revokes the device, only that account is removed. There's
    still no unpair in the app.
  - A device paired before this update moves to the new storage
    automatically, with no re-pairing.
  - Removed: the Debug-only saved-servers and password screens and their
    `WEHELP_DEV_*` settings. Password login has been office-LAN only since
    #38; devices join by QR.
- Provider devices (#69): one WeHelp app, with the role set by pairing.
  - On the staff web UI, **My devices** shows a staff member's phones and
    iPads, with **Pair my phone or iPad** and Revoke.
  - `POST /pairing/invites` takes `user_id` for any account: a provider for
    linked patients or themselves; an admin for anyone in the practice.
    Redeem issues a device-bound session with the account's own role.
  - The app opens on a provider **Today** view (today's visits with patient
    names, what's coming up, and unread messages) alongside Messages,
    Schedule and Settings.
  - `/links` now carries `provider_name` and `patient_name`, and `/me`
    carries `display_name`, so messages and visits show names instead of
    "Patient" or "Care team".
  - **The app never unpairs itself** (owner direction). Settings' Sign out
    is replaced by **Lock now**, and the Devices screen is read-only. Only
    the practice removes a device, from the staff page; the app clears
    itself when the server refuses a revoked device.
  - A paired device that can't reach its practice at launch shows "Can't
    reach <practice>" with **Try again** and keeps the pairing. It used to
    drop to the pair screen.
- Staff sign-in and the LAN-only staff surface (#38).
  - Staff web UI sign-in is password + TOTP. The first sign-in walks through
    authenticator setup (QR code and key). Codes work once, and five failures
    lock the account for 15 minutes.
  - Sessions are server-side, in a `__Host-wehelp_staff` cookie (Secure,
    HttpOnly, SameSite=Strict) with a CSRF token on every change. The web UI
    no longer keeps a token in `localStorage`.
  - New routes: `/api/v1/staff/{login,totp/setup,totp/confirm,me,logout}`.
    Migration V5 adds the TOTP columns, lockout and `staff_sessions`.
  - HTTPS for staff on `:8443`, with a certificate issued by the office's
    local CA for `staff_hosts`. Outside dev, plain HTTP only serves
    `/healthz` and `/readyz` and redirects everything else.
    `appliance.sh export-ca` prints the CA for staff machines.
  - Staff routes (patients, pairing invites, patient devices), password
    login and registration, and the web UI all return 404 through the
    Relay. The listener decides this, not a header.
  - Bearer `/auth/login` also needs a TOTP code for staff.
  - Self-registration is dev-only; staff accounts are created with
    `wehelpd staff add` on the Back Office.
- Device management (#10).
  - `GET /api/v1/devices` lists your own devices, marking the current one,
    and `DELETE /api/v1/devices/{id}` revokes one.
  - Staff on the office LAN can list and revoke a linked patient's devices
    with `GET` and `DELETE /api/v1/patients/{id}/devices[/{deviceID}]`.
  - A revoke also ends that device's refresh tokens and is audited
    (`device.revoke`).
  - Access tokens now carry the device ID (`did`), and every request checks
    it, so a revoked device is locked out at once rather than after up to 15
    minutes. Patient tokens without a device are refused.
  - Staff web UI: each patient's device count opens their devices, with
    Revoke.
  - iOS: Settings → Devices lists this account's devices (read-only since
    #69: only the practice revokes). If the server ends this device's
    session, the app clears it.
- QR pairing; patients have no password (#37).
  - Staff web UI: a new **Patients** tab to add patients (no password) and
    **Pair phone**, which shows a QR code with the office's pairing identity
    and a single-use invite (10 minutes, bound to one patient, only its hash
    stored). It reports when the phone has paired.
  - API: `POST/GET /api/v1/patients`, `POST /api/v1/pairing/invites`,
    `GET /api/v1/pairing/invites/{id}`, and `POST /api/v1/pairing/redeem`.
    Redeem takes a proof from the phone's new key, enrolls the device and
    returns the device-bound session. Every step is audited. Staff routes are
    refused through the Relay.
  - Patient password login and patient self-registration are refused.
    Migration V4 makes `users.password_hash` nullable, and the seed creates
    patients with no password.
  - iOS:
    - signed-out phones open on **Pair with your practice**, with a VisionKit
      QR scanner and a paste fallback;
    - the app locks with Face ID, Touch ID or the passcode whenever it goes
      to the background;
    - the saved-practices and password screens are Debug-only, for staff
      demos.
  - First Back Office database tests (`internal/server/pairing_db_test.go`).
    They apply all migrations and the seed to a fresh database and cover:
    pairing, single use, expiry, the wrong key, unlinked patients, refusals
    through the Relay, and no patient passwords.
- Practice onboarding plan and training material (`docs/onboarding/`):
  - an onboarding runbook, practice side first: Registry setup, Back Office
    install and Relay connection, the provider app, a day-by-day training
    week on a separate demo kit, roles and RACI, checklists, go-live
    criteria, hypercare and rollback;
  - a video training outline: 13 modules with scripts and a shot list per
    screen, each marked as existing today or waiting on its issue;
  - Interventionaire (questionnaire and intervention tooling, being
    renewed as an iOS app): what it does, which of its design ideas the
    post-V1 kiosk copies, how content bundles plug into the Back Office,
    and the demo script.
- Design note `docs/LOCAL_AGENT.md`: a Mac Pro Back Office running a local
  AI agent for daily routines and approved outreach, with inference and all
  patient data on the box and every agent action audited. Lit-review
  entries for local model runtimes and questionnaire formats.
- Issues: self-registration decision (#79), training seed (#80),
  `wehelpd doctor` (#81), kiosk questionnaires (#82), local agent (#83) and
  training videos (#84).
- The iOS app reaches its office only through the Relay (#36).
  - Every request is an HTTP `CONNECT` to the Relay (`ProxyConfiguration`).
    It pins the Relay's TLS key on the outer hop and the office's key on the
    inner, end-to-end TLS 1.3. Both checks fail closed, with an error that
    says whether the Relay or the office didn't match.
  - Saved practices are now office endpoints: Relay address, Relay pin,
    office ID and office pin. The editor can paste the `pairing` JSON that
    `wehelpd init` prints.
  - `NSAllowsLocalNetworking` and the plain-HTTP, direct-LAN path are
    removed from Debug and Release. The only ATS exception is
    `*.wehelp.invalid`, with TLS 1.3 required.
  - Debug demo practices come from `WEHELP_DEV_SERVERS`
    (`Label|relay|relay pin|office ID|office pin`), compiled out of Release.
  - The Registry now stores each Relay's TLS pin: migration V2,
    `relay add --tls-pin` and `relay set-tls-pin`. Install returns it, and
    the office's pairing identity includes `relay_pin` for the QR code
    (#37).
- WeHelp Relay (#4): the new `wehelp-relay` binary and the office tunnel.
  - Offices connect out over SSH with their office key. The Relay accepts
    only keys on the Registry's signed office list for its own relay ID, and
    offices pin the Relay host key.
  - Phones send TLS `CONNECT <office-id>.wehelp.invalid:443`. The Relay
    splices that onto a `wehelp-phone` SSH channel and sees only the
    phone↔office TLS.
  - The office list is fetched every minute and verified against the pinned
    Registry key. Older or forged lists are rejected. The last good list is
    cached and re-verified at start. Offices dropped from the list are
    disconnected.
  - `wehelpd serve` opens the tunnel by itself once the office is installed.
    It reconnects with jittered backoff and terminates TLS 1.3 with the
    office key. Requests that arrive this way are marked
    (`tunnel.ViaRelay`) so staff routes can refuse them (#38).
  - Logs carry office IDs and byte counts only.
  - Import-boundary test for the Relay, and an in-process end-to-end test
    that includes a no-plaintext check.
  - Runbook: docs/RELAY.md.
- Back Office identity (#35): `wehelpd init`.
  - It creates the office SSH key (ed25519), the office TLS key (P-256) with
    a self-signed certificate for `<office-id>.wehelp.invalid`, and a local
    CA for the staff UI.
  - Everything lives in `office_dir`, mode 700, with keys at mode 600. It is
    never overwritten, and it is safe to rerun.
  - With `--install-token` (or `WEHELP_INSTALL_TOKEN`) it installs with the
    Registry and stores the office ID, Relay addresses and host key, and the
    Registry signing key.
  - It prints the pairing identity for the QR payload: Relay address, office
    ID, and the key pin (SHA-256 of the X9.63 public key, the SPKI key bits).
  - Appliance: an `officekeys` volume and `appliance.sh init-office`.
  - Rotation procedures for each key: docs/OFFICE_KEYS.md.
- WeHelp Registry core (#60): a new `wehelp-registry` binary with its own
  Postgres database and Flyway migrations (`db/registry`).
  - Practices, Relays, offices, install tokens, and every published office
    list.
  - `POST /v1/install`: a single-use, 24-hour install token plus the office
    SSH key returns the office ID, Relay address and host key, and the
    Registry signing key. Reinstalling keeps the office ID and replaces the
    key.
  - `GET /v1/office-list`: an ed25519-signed list with a monotonic version,
    republished in the same transaction as every change. Suspended practices
    drop off.
  - CLI for V1-M2 support work: `relay add`, `practice add`, `suspend`,
    `reactivate`, `token issue`, `keygen` and `audit verify`.
  - Its own hash-chained, append-only audit log, with length-prefixed fields
    and jsonb-safe verification.
  - `internal/officelist` (sign and verify, rollback check) and
    `internal/keyfile` (mode-600 PEM keys that are never overwritten) are
    shared with the Relay and office work.
  - Tests: an import-boundary test, `TestOpenAPICoversRoutes` for
    `api/registry.yaml`, and database integration tests that CI now runs
    against a Postgres service.
  - Runbook: docs/REGISTRY.md.
- Spike S2, telehealth (#33): the answer is yes. Two iOS simulators held 1:1
  WebRTC calls with relay-only ICE through pion/turn. Media was DTLS-SRTP end
  to end, and the DTLS fingerprints matched each peer's SDP. Two calls at
  once used about 6% of one core on the server. The write-up and
  measurements are in docs/spikes/S2.md. The throwaway code in `spikes/s2/`
  is a TURN and signaling server in Go and a SwiftUI app using
  `stasel/WebRTC`. The lit review verdicts for pion/turn and the iOS WebRTC
  framework are updated.
- Spike S1 (#32), written up in docs/spikes/S1.md, with throwaway code in
  `spikes/s1/`. A phone reaches its Back Office through a relay with HTTP
  `CONNECT` and an SSH channel, using TLS 1.3 end to end to a pinned office
  key. The relay's recordings held no plaintext.
  - Finding: default ATS rejects the pinned self-signed office certificate,
    so V1 needs one scoped exception for `*.wehelp.invalid`.
  - Finding: macOS Local Network privacy needs a one-time approval on
    Barnacle for the Demo topology (added to docs/APPLIANCE.md).
- Spike S3 (#34), written up in docs/spikes/S3.md, with throwaway code in
  `spikes/s3/`. The Relay-side sender pushed through the APNs sandbox with
  the team's token key, limited to three fixed PHI-free categories (others
  are refused), and the iOS app received all three.
  - Finding: the key is scoped to `com.zacelston.wehelp` and sandbox only,
    and Apple enforces both.
  - Finding: `sideshow/apns2` pins a vulnerable 2022 `x/net`, so V1 should
    hand-roll the client.
  - Finding: the local Go 1.26.0 toolchain has standard-library
    vulnerabilities; pin a patched toolchain (#43).
- Back Office appliance setup for an always-on Mac mini (#12), with the
  runbook in docs/APPLIANCE.md:
  - `deploy/docker-compose.appliance.yml`: restart policies, Postgres on
    127.0.0.1 only, and a required mode-600 `deploy/.env` holding a
    persistent JWT secret;
  - `deploy/appliance.sh` and `make appliance-*` targets: `up` creates the
    secret if missing and waits for health; `stop-for-reboot` stops the
    stack and Colima cleanly; `install-login` installs a launchd boot job;
  - the boot job waits for Docker, runs `compose up -d`, then checks
    `/healthz` and logs the result. This fixes the server container being
    left in `created` after an abrupt reboot, which restart policies alone
    don't handle.
- iOS Messages tab (#17): inbox from `GET /api/v1/messages`, newest first,
  with kind badges, unread dots and cursor pagination. Opening a message
  marks it read. Compose sends kind `message` to the patient's active linked
  provider (from `GET /api/v1/links`). Pull to refresh plus a 60-second poll
  while the tab is open, and loading, empty and error states. Messages stay
  in memory only. Signed-in screens are covered when the app is inactive, so
  the app-switcher snapshot shows no PHI. `APIClient` gains query
  parameters and RFC 3339 date decoding.
- iOS Schedule tab (#39, list only): upcoming appointments (soonest first)
  and past ones (most recent first) from `GET /api/v1/appointments`, with
  status badges for scheduled, cancelled, completed and missed (`no_show`).
  Pull to refresh, plus loading, empty and error states; nothing is cached
  on disk. Requesting an appointment from the app waits on a patient
  request endpoint in the API.
- Messages API (#14): send, paginated inbox (`?unread`, `?limit`,
  opaque `?cursor`), and mark-read. Sending requires an active
  provider-patient link; creates and read-receipts are audit-chained in the
  same transaction.
- Appointments API (#18): POST/GET/PATCH /api/v1/appointments.
  Provider-only create requires an active link with the patient and rejects
  overlapping scheduled appointments (409); concurrent bookings for one
  provider are serialised with a transaction-scoped advisory lock. List is
  scoped to the caller (provider or patient) with optional `?from`/`?to`
  bounds on `starts_at`. PATCH supports reschedule (re-checks overlap) and
  status transitions only from `scheduled` to `cancelled`, `completed` or
  `no_show` (409 otherwise). All mutations are audited, including the status
  change.
- Staff web UI: a vanilla-JS SPA (`web/`) embedded in the Go binary via
  `embed.FS` and served at `/` by the chi router. No framework, no build
  step, no Node.js. Messaging, appointments (create, list, cancel,
  complete) and links. Unknown non-API paths fall back to `index.html`.
  Patient accounts get a "use the WeHelp app" notice and no data (#9).
  Error text is HTML-escaped. Not yet done: HttpOnly sessions, refresh,
  TOTP and security headers (#38).
- OpenAPI spec (#15): `api/openapi.yaml` is the contract between the Back
  Office, the iOS app and agents. It covers health, auth (including device
  enrol, signed refresh and logout), me, links, messages and appointments.
  CI checks that the spec is valid OpenAPI, and `TestOpenAPICoversRoutes`
  fails if any route registered on the server is missing from the spec.
  CONTRIBUTING.md requires route changes to update the spec in the same PR.
- iOS sign-in with device-bound sessions (#5). There is a sign-in screen
  (practice, email, password, server). After a password login the app creates
  a P-256 key in the Secure Enclave (a software key on the simulator) and
  enrolls it. The refresh token is kept in the Keychain
  (`AfterFirstUnlockThisDeviceOnly`) and the access token in memory only.
  `APIClient` attaches the Bearer token and refreshes once on a 401, with
  single-flight refresh. Launch resumes the session using the device key.
  Sign-out revokes the device on the server and wipes the token and key.
  Settings shows the role from `/api/v1/me`.
- iOS saved connections: each successful sign-in saves its server, practice
  and email (never the password) in UserDefaults. When signed out, the app
  opens on a **Servers** list:
  - "+" opens a New server screen (address with Test connection, practice,
    optional email).
  - Tapping a server opens its own sign-in screen.
  - Each row has an ⓘ button, plus a long-press menu and swipe actions, to
    edit or delete it. The editor has a Delete server button, and each
    sign-in screen has an Edit button. Deletes ask for confirmation.
  - The Debug "Demo" entry can be edited or deleted too, and a deleted demo
    entry does not come back at the next launch.
  - An optional **Label** ("Barnacle") is shown in place of the practice
    name. It is local only; the practice name must still match the server.
  - Debug builds can define several demo servers as `Label|URL` pairs in
    `WEHELP_DEV_SERVERS`, set in the gitignored `Local.xcconfig` (see
    `Local.xcconfig.example`). Each uses the demo login. A "Restore demo
    servers" button brings back any you deleted. Release builds include no
    demo servers or demo credentials.
- Server: `POST /api/v1/auth/enroll` and `/auth/logout`, plus migration
  `V3__devices.sql` (a `devices` table and `refresh_tokens.device_id`).
  - Device-bound refresh tokens need an ECDSA-P256 proof over
    `wehelp-device-v1\n{purpose}\n{refresh_token}\n{unix_ts}` (±5 min).
    Single-use rotation makes a captured proof useless.
  - Patient accounts can only refresh device-bound tokens, which keeps
    patients mobile-only per docs/ARCHITECTURE.md. Staff tokens are
    unaffected.
  - Enroll, refresh and logout are audited.
- iOS: run on a physical device against a local backend. The server URL can
  now be set in Settings (persisted in UserDefaults; it is not a secret),
  with "Save and test connection" calling `/api/v1/ping` and a reset to the
  build default. The build default comes from `ios/Config/*.xcconfig`:
  Debug uses `http://localhost:8080` (simulator) unless a gitignored
  `ios/Config/Local.xcconfig` overrides it with the Mac's LAN address (see
  `Local.xcconfig.example`). Release has no default. This is the first step
  toward clinic-manifest onboarding.
- iOS: `NSAppTransportSecurity` → `NSAllowsLocalNetworking` so dev builds
  can use plain HTTP to LAN hosts only (no `NSAllowsArbitraryLoads`), plus an
  `NSLocalNetworkUsageDescription` for the local-network prompt.
- Seed data + demo script (issue #14): `wehelpd seed` / `make seed` creates a
  demo tenant ("Demo Clinic"), a provider, two patients, a provider-patient
  link (one active, one pending), a short message thread, and an
  appointment — so devs and agents can exercise the API instantly. Applies
  migrations first, so it works against a fresh `make compose-up` stack.
  Idempotent: every entity uses check-then-insert, so re-runs perform no
  writes and append no duplicate audit events. Reuses `auth.HashPassword`
  and `audit.Record` and mirrors the validated SQL in `internal/server`
  rather than reaching around the service layer. Demo credentials are
  printed to stdout; README quickstart updated with login examples.
- Scale budget + deployment shape (docs/ARCHITECTURE.md): the reference
  target is an office appliance PC serving ~500 patients — one Go binary +
  Postgres, no distributed machinery. Cloud tier is the same artifact.
- Lit review (`docs/LIT_REVIEW.md`): prior-art survey per feature, now the
  mandatory first step of spec review. Outcomes: adopt River for
  reminder/job scheduling (transactional enqueue fits our audit model),
  go-oidc for SSO with Zitadel as a candidate self-hosted IdP, apns2 for
  push; reference Open mHealth schemas for check-ins, Formance/TigerBeetle
  for ledger design, immudb/Trillian for audit.
- Spec-review gate: issues flow `needs-spec` → `spec-review` →
  `agent-ready`; reviewer validates approach currency, dependency health,
  testable criteria, and scope. CODEOWNERS requests owner review on all PRs.
- Issue #16 corrected on review: both Go FHIR libraries are stale
  (samply v0.3.2 2022, google/fhir Go v0.7.4 2022); recommendation is now
  hand-rolled minimal R4 structs, matching CMSgov/bcda-app's approach.
- Provider-patient links: invite by email (provider only), patient accepts,
  either side revokes; `GET /api/v1/links` lists the caller's relationships.
  All transitions audited (issue #3). Path param on accept/revoke is the
  *other* party's user ID.
- Rule: all changes land via feature branches + PRs; `main` is protected
  against direct pushes.
- Direction: auth is interim local credentials; the target is OIDC SSO —
  Google, Microsoft, Sign in with Apple (issue #18).
- Contribution rules: one-line commit subjects, notes live in this
  changelog, no heredocs in shell commands (write files instead).
- Auth: register/login/refresh endpoints with argon2id password hashing,
  short-lived HMAC JWT access tokens, and rotating opaque refresh tokens
  stored as SHA-256 hashes (PR #17, issue #1).
- `refresh_tokens` table and unique `tenants.name` (migration 00002).
- `GET /api/v1/me` behind Bearer middleware resolving
  (tenant, user, role) into request context.
- GitHub project board (WeHelp Roadmap, project #5), 5 milestones,
  16 seeded issues, `agent-ready`/`needs-spec` label taxonomy.
- CONTRIBUTING.md, issue/PR templates, AGENTS.md agent bootstrap notes.

### Fixed
- Patients can't send memos or reminders (#137). Those are staff kinds, and
  a patient's memo got past the practice's message switch and the
  provider's message mode. Patients get 403 for them now.
- Appliance boot after a reboot (#123). Lima's `vz.pid` can survive a
  reboot and name a process ID macOS has reused, and Colima then won't
  start. If Docker isn't up a minute after login, the boot job clears that
  stale state and restarts Colima. Seen on Barnacle after a macOS update;
  the fix was checked on Barnacle against the same state.
- Website publish (#97): when the Relay refused a publish (for example
  "publishing too often") before the whole bundle was sent, the Back Office
  reported EOF instead of the reason. It now reads the reply after a failed
  write. This also fixes the flaky `TestPublishSiteThroughRelay`.
- CI: Dockerfile and workflow pinned to Go 1.25 while go.mod requires 1.26 —
  bumped both to 1.26.
- `audit.Record` genesis event: first event in a tenant chain now links to
  an empty bytea instead of violating the `NOT NULL` `prev_hash` column.

## [0.1.0] — 2026-09-14

### Added
- Initial monorepo scaffold: Go backend (cobra/viper/chi/pgx), SwiftUI iOS
  app generated via xcodegen, docker-compose deploy, CI, AGPL-3.0.
- Core schema (migration 00001): tenants, users, provider_patients,
  appointments, messages, double-entry ledger, hash-chained audit_events —
  ledger and audit tables immutable via DB triggers.
