# WeHelp Home: a family's own base station

Status: **design for discussion** (2026-11-16), Post-V1. Nothing here
changes V1 scope or order. It's work for after V1, and the first prototype
runs on MiniZ, the Linux test host.

## The idea (owner, 2026-11-16)

A home version of WeHelp, for families to run their lives on:
- **Parents and kids each have devices.** Families download the app from the
  store and set up their own **base station**, which handles every outside
  connection.
- **A private family messaging system,** and family video calls.
- **Home scheduling and reminders,** managed in our own apps.
- **A home streaming system for security cameras.**
- **Location sharing**, which is what SquadZ (`~/code/squadz`, a GPS squad
  tracker with join codes and a live map) set out to be.

## Why WeHelp fits

WeHelp's core is a box in the building that holds all the private data, and
phones that reach it through a Relay that only ever sees ciphertext. A
family wants the same promise: **our data stays in our house.** Most of the
platform carries over unchanged:

| WeHelp (clinic) | WeHelp Home | Change |
|---|---|---|
| Back Office (`wehelpd`, Mac mini) | **Base station**: a small always-on box at home | Same binary. MiniZ shows it runs on a cheap Linux box with Docker |
| Relay | The same shared Relay: remote access, push, TURN | None. The base station dials out over SSH, so a family needs no port forwarding or dynamic DNS |
| Registry | Knows that a household exists, its Relay and its subscription. Never who's in it | **Self-service sign-up** replaces support-entered practices |
| Practice | **Household** | A new practice type (PRACTICE_TYPES.md): non-clinical, with its own words and switches |
| Admin, provider, front desk, patient | **Parent, teen, child, caregiver** | Roles and words (below) |
| QR pairing + Secure Enclave device key | A parent scans the base station to set it up; kids' phones pair from a parent's QR | None. No passwords suits kids: nothing to forget or share |
| Messages, content-free push | Private family chat | None |
| Appointments and reminders | Family calendar: pickups, practices, chores | Generalise appointments to events with recurrence and assignees |
| Ledger (append-only, no API yet, #23) | Allowance and chore payouts | The ledger API, used for points or money owed rather than payments |
| Video visits (WebRTC + TURN) | Family calls, and an intercom between rooms | None |
| Questionnaires (V2, #201) | Check-ins ("home safe?"), permission slips | Reuses the forms |
| — | **Location** | New |
| — | **Cameras** | New, and the biggest piece |

**One codebase, two products.** Home is a practice type plus a separate app
target in `ios/project.yml` (and later Android). It's not a fork. A family
app used by children shouldn't share a store listing, privacy label or age
rating with a clinic's patient app. Both share the transport, pairing,
device keys, audit and Relay.

## Invariants (carried over, plus three for home)

- **Household data lives on the base station** and, while it's on screen, on
  family members' phones. That covers messages, calendar, locations and
  camera footage. The Relay, the Registry, dashboards, logs and push
  payloads never hold it.
- **The Relay sees ciphertext only**, as today.
- **Camera footage never leaves the house** except as a live stream to a
  family member's paired device, or in a backup encrypted to the parents'
  key. There's no cloud recording.
- **Location goes to the base station only,** never phone to phone or
  through a third party. It's kept briefly (default: 7 days of history,
  latest position only after that).
- **A child's data is visible to parents by default, and to no one outside
  the household.** Teens can pause location sharing, and parents see that
  it's paused.

## Roles

| Role | Who | Can |
|---|---|---|
| **Parent** | Adults who run the household (the practice admin) | Everything: members, devices, cameras, settings, all calendars, children's locations |
| **Teen** | Older children | Chat, calendar, their own chores. Share location (pausable, and parents see it's paused). View cameras if a parent allows |
| **Child** | Younger children | Chat with household members only, their calendar and chores. Location shared with parents. No cameras |
| **Caregiver** | Grandparents, a babysitter, a co-parent in another home | What a parent grants, for a period (like a care-team member, #111): for example the calendar and cameras on Saturday evening |

- **The age bands are the parents' choice,** not a birthdate rule. The app
  never asks a child for their age.
- **A co-parent in another household** is a caregiver, or later a link
  between two households. Two base stations can't share one child's data in
  this design.

## Cameras

The base station is the camera gateway:
- **It pulls the cameras on the home network**, over RTSP and ONVIF (Profile
  S/T). The cameras themselves never reach the internet: a parent can block
  them at the router, and the design recommends it.
- **It records locally:** motion clips and optional continuous recording, on
  the base station's disk, with a size or age limit.
- **Live view:**
  - **At home:** the app plays WebRTC directly from the base station on the
    LAN.
  - **Away:** WebRTC with ICE. Peer-to-peer when the networks allow it
    (STUN); TURN through the Relay when they don't. TURN streams are capped
    per household, like the visit cap (#175), because a call measured about
    74 MB each way for 5 minutes (RELAY.md). A camera left open could cost
    the Relay that much continuously.
- **Clips and events:** "motion at the front door" goes as a content-free
  push ("Activity at home"). The clip itself is fetched from the base
  station through the tunnel.
- **Prior art to build on, not rewrite** (lit review):
  - **go2rtc** for stream handling (RTSP/ONVIF in, WebRTC out);
  - **Frigate**, optional, for motion and person detection.

  Both run as containers next to `wehelpd`, behind it: only `wehelpd`
  talks to the apps.

**The first question for the spike:** how much remote viewing goes
peer-to-peer and how much needs TURN, on real home networks and mobile
carriers. That decides the Relay's cost model.

## Location (SquadZ)

- **Phones report to the base station** through the existing tunnel:
  - significant-change updates on iOS;
  - more often while a family member has the map open;
  - never continuous GPS in the background.
- **Geofences:** "arrived at school" or "left practice". They're computed
  on the base station, and the push says only "Arrived" with a member's
  first name.
- **Stale detection,** as in SquadZ: positions fade and say how old they
  are.
- **Pause:** each person can pause sharing; a child's pause can be locked
  by a parent. A pause is visible, never hidden.
- **Retention:** 7 days of history by default, then only the latest
  position.

## Setup: self-service

A family has no front desk and no WeHelp support person:
1. **Get a base station:**
   - a box we sell or recommend (a small x86 or ARM mini PC);
   - or the image installed on their own mini PC, Raspberry Pi 5 or NAS (Docker).

   Images for amd64 and arm64.
2. **Claim it:** the base station shows a QR on a small screen, a sticker
   or its local web page. A parent's app scans it, and that does three
   things:
   - **creates the household** in the Registry (self-service sign-up, with
     the subscription through the App Store or Play);
   - **sends the base station its install token;**
   - **pairs the parent as the first member.**
3. **Add the family:** the parent shows a pairing QR for each member, as the
   front desk does today.
4. **Updates install themselves** (signed images and a staged rollout).
   **Backups** go to an encrypted file the parents keep, using the backup
   format WeHelp already has.

**When the base station is off or offline,** nothing works remotely. The
app says "Home is offline" plainly. WeHelp's Unreachable screen already
does this.

## Kids, privacy and the stores

- **COPPA** (US) and the GDPR's rules for children:
  - WeHelp the company never receives a child's data, because it stays on
    the family's base station;
  - the parent sets up every child's device and consents at setup.

  Legal review is still needed before launch.
- **App Store and Google Play rules for apps used by children:**
  - no third-party analytics or advertising;
  - parental gates on links out and purchases.

  Our apps already have no analytics SDKs. Which store category the app
  uses is a launch decision.
- **Cameras and privacy law** vary by place (recording audio, filming
  outside the property). The app shows a notice when a camera is added.

## Not in this design

- Smart-home control (lights, locks, thermostats). That's Home Assistant's
  job; a bridge to it can come later.
- Screen-time controls on kids' devices. That's iOS Screen Time and
  Android Family Link.
- Cloud recording or cloud backup of footage.
- Several households sharing one child (co-parenting across two base
  stations).
- Health data for families. It's possible later through the V2 health-data
  design, but not part of Home.

## Open questions

- **The product name and app name** ("WeHelp Home" is a placeholder).
- **The base station hardware:** one box we sell, a list of recommended
  ones, or image-only at first.
- **Self-hosted Relay:** can a technical family run their own Relay, or is
  the Relay always ours (and the subscription)?
- **Pricing:** the subscription covers the Relay (remote access, push,
  TURN). Is there a free tier for LAN-only use?
- **SquadZ:** retire it and fold its idea in here, or keep it as a separate
  lightweight app for groups that aren't households (teams, trips).

## Order of work (after V1)

1. **Spike H1, cameras on MiniZ:**
   - go2rtc and one camera;
   - live view in the app at home and away through Barnacle's Relay;
   - measure peer-to-peer against TURN, and the bandwidth.
2. **The household practice type:** roles, words, switches, and the Home app
   target.
3. **Self-service households:** Registry sign-up, claiming a base station by
   QR, subscriptions.
4. **The base station image** with automatic updates.
5. **Family calendar and chores:** events with recurrence and assignees, and
   the ledger API for allowance.
6. **Location and geofences.**
7. **Camera recording, events and retention.**
8. **Kids' privacy and store readiness:** consent at setup, visibility
   rules, legal review.
