# Staff training: video session outline

The modules, a script for each, and a shot list for every screen staff
will see: the staff web UI, the patient app (WeHelpMobile), the provider
app, and the kiosk. The training week (RUNBOOK.md, Phase 4) uses the same
modules live; the videos are the reference copy staff can rewatch.
Recording is tracked in #84.

## Rules for every recording

- **Synthetic demo data only,** on a demo Back Office with `demo_mode` on
  and the training seed (#80), on the Demo topology. Never a field Back
  Office, never a real patient, never a real staff member's account.
- Demo names are obviously fictional and marked "(demo)" where the UI
  allows: Dr. Ada Shaw (demo), Jordan Lee (demo), Sam Rivera (demo), and the
  wider cast from #80.
- Recording devices: Do Not Disturb on, no personal notifications, no
  personal accounts visible, clock set to the seeded demo week.
- No real TOTP secrets, invite codes or QR codes that stay valid after the
  recording. Pair and revoke on camera so any code shown is already dead.
- Each video has captions or a transcript.

## Status key

| Mark | Meaning |
|---|---|
| **Exists** | Built on `main` today and can be recorded now |
| **Partial** | A version exists but will change before V1. Record it last, or as a draft |
| **Not yet** | Not built. The issue that builds it is named. Use a mock-up or skip |
| **Post-V1** | Planned after V1 |

## Modules

| # | Module | Audience | Length | Screens ready? |
|---|---|---|---|---|
| 1 | Privacy rules: where patient data lives | All staff | 6 min | No screens (slides) |
| 2 | Signing in to the staff UI | All staff | 4 min | Partial (#38) |
| 3 | Patients and the patient record | Front desk, admin | 6 min | Not yet |
| 4 | Appointments | Front desk, admin, providers | 8 min | Partial (#68) |
| 5 | Messages | All staff | 7 min | Partial (#67) |
| 6 | The patient app tour | All staff | 6 min | Partial (#37, #39) |
| 7 | Pairing a patient's phone | Front desk, admin | 6 min | Not yet (#37) |
| 8 | Lost phone, new phone, can't scan | Front desk, admin | 5 min | Not yet (#10) |
| 9 | The provider app | Providers | 8 min | Not yet (#69) |
| 10 | Video visits | Providers, front desk | 8 min | Not yet (#6) |
| 11 | Admin tasks: accounts, devices, daily checks | Admin | 8 min | Partial |
| 12 | Security incidents | Admin, owner | 6 min | No screens (slides) |
| 13 | The kiosk | Front desk | 6 min | Post-V1 (#8, #82) |

---

## Module scripts

Scripts are what the narrator says, with **[SHOT id]** markers that point
at the shot lists below. Keep each to the stated length.

### Module 1: Privacy rules (slides)
1. "WeHelp keeps your patients' information in one place: the practice's
   own server, here in the office. We call it the Back Office."
2. "Patients reach it from their own phones through a connection that
   WeHelp can't read. WeHelp support can see that your office is online.
   They can never see a patient."
3. "So the rules are simple. Patient details go in WeHelp, not in email,
   text messages, screenshots or sticky notes. If WeHelp support asks you
   for help, give them a screen name or an error, never a patient's name."
4. "Every time someone opens or changes a patient's information, WeHelp
   records who did it. That record can't be edited, by anyone."
5. "If a phone is lost or someone leaves, tell the practice admin the same
   day. Module 12 covers what happens next."

### Module 2: Signing in to the staff UI
1. "The staff UI only works inside the office, on the office network. It
   won't open from home. That's on purpose." **[W-01]**
2. "Sign in with your own email and password. Never share an account."
   **[W-02]**
3. "Then enter the six-digit code from your authenticator app." **[W-03]**
4. "If your browser warns about the certificate, stop and call the
   practice admin. Don't click through." **[W-04]**
5. "Sign out when you leave the desk." **[W-05]**

### Module 3: Patients and the patient record
1. "The Patients list is the front desk's home." **[W-10]**
2. "Search by name or date of birth. Open a patient." **[W-11]**
3. "The record shows contact details, upcoming appointments, message
   threads and paired devices." **[W-12]**
4. "To add a new patient, press New patient and fill in the required
   fields." **[W-13]**

### Module 4: Appointments
1. "The schedule shows the day by provider." **[W-20]**
2. "To book, choose the patient, provider, time and visit type." **[W-21]**
3. "Patients can ask for an appointment from the app. Requests wait here
   until you confirm or offer another time." **[W-22]** **[P-21]**
4. "Cancel, complete, or mark a no-show from the appointment." **[W-23]**
5. "When you confirm, the patient's phone gets a notification that says
   only 'Appointment update'. The details open inside the app." **[P-40]**

### Module 5: Messages
1. "The inbox shows every patient thread, unread first." **[W-30]**
2. "Open a thread, reply, and mark it read." **[W-31]**
3. "To hand a thread to a provider, assign it." **[W-32]**
4. "From the patient's side, it looks like this." **[P-30]** **[P-31]**
5. "Messages are for scheduling and follow-up. If something sounds urgent,
   follow the practice's urgent-care policy; don't wait on a reply."

### Module 6: The patient app tour
1. "This is what your patients see." **[P-01]**
2. "Today shows their next appointment." **[P-10]**
3. "Schedule lists upcoming visits." **[P-20]**
4. "Messages is their thread with the practice." **[P-30]**
5. "The app locks with Face ID or the phone's passcode. Patients have no
   password to forget." **[P-02]**
6. "If the app switcher is open, patient screens are blurred." **[P-03]**

### Module 7: Pairing a patient's phone
1. "Pairing happens at the desk, with the patient in front of you."
2. "Open the patient and press Pair phone. A QR code appears; it works for
   10 minutes, once." **[W-40]**
3. "The patient opens the WeHelp app and scans it." **[P-50]**
4. "The app confirms it's talking to this practice, and asks the patient to
   turn on Face ID." **[P-51]** **[P-52]**
5. "The device now appears on the patient's record." **[W-41]**

### Module 8: Lost phone, new phone, can't scan
1. "Lost or replaced phone: open the patient, find the device, Revoke. It's
   locked out within 15 minutes." **[W-42]**
2. "Then pair the new phone the same way as Module 7."
3. "Patient can't scan (camera broken, no smartphone): they can still be
   seen and booked; they just don't use the app. Never pair a phone that
   isn't theirs."
4. "Revoked phones show this screen." **[P-53]**

### Module 9: The provider app
1. "Providers have their own app, separate from the patient app." **[R-01]**
2. "Today's schedule, in the office or at home." **[R-10]**
3. "Open a patient's message and reply." **[R-20]** **[R-21]**
4. "It works on cellular with no VPN." **[R-11]**
5. "Admin work (accounts, pairing, devices) stays in the staff UI."

### Module 10: Video visits
1. "At visit time, the provider starts the visit from the appointment."
   **[R-30]**
2. "The patient gets a 'Visit starting' notification and joins." **[P-60]**
   **[P-61]**
3. "Video goes straight between the two phones, encrypted. Nothing is
   recorded." **[R-31]**
4. "End the visit." **[R-32]**

### Module 11: Admin tasks
1. "Staff accounts: invite, reset, disable on the day someone leaves."
   **[W-50]** **[W-51]**
2. "Devices: every paired phone in the practice, and revoke." **[W-52]**
3. "Daily check: the health page, or `wehelpd doctor`, should be all
   green." **[W-53]** **[T-01]**
4. "Before a macOS update, stop WeHelp cleanly." **[T-02]**
5. "Audit log: who did what, and when." **[W-54]**

### Module 12: Security incidents (slides)
1. Lost or stolen staff device: revoke, reset that person's password and
   TOTP, tell the owner.
2. Suspected breach: don't power off the Back Office. Call WeHelp support
   and the owner. The owner leads the HIPAA breach assessment.
3. A staff member leaves: disable the account and revoke their devices the
   same day.
4. Someone calls claiming to be WeHelp and asks for a code or a patient
   name: hang up and call WeHelp support on the number posted at the desk.

### Module 13: The kiosk (post-V1)
Follows [KIOSK_DEMO.md](KIOSK_DEMO.md), version B, as a 6-minute
walkthrough. **[K-01]** to **[K-06]**.

---

## Shot lists

One row per screen. "Demo data" is what must be on screen (from the
training seed, #80).

### Staff web UI (LAN only)

| Shot | Screen | Status | Demo data | Notes |
|---|---|---|---|---|
| W-01 | Staff UI from outside the office (connection refused) | Not yet (#38; the Relay-channel marker `tunnel.ViaRelay` exists) | — | Optional; can be a slide |
| W-02 | Sign-in: email and password | Partial: today asks for practice, email, password, over HTTP | `front.desk@demo.wehelp` | Re-record once #38 lands |
| W-03 | TOTP code entry | Not yet (#38) | — | |
| W-04 | Browser certificate warning (what to stop at) | Not yet (#38) | — | Show it, then the trusted state |
| W-05 | Header with role and Log out | Exists | front desk role | |
| W-06 | "This page is for practice staff" notice for patients | Exists | — | Used in Module 6 voice-over |
| W-10 | Patients list | Not yet (V1-M3 staff UI) | 25 training patients | |
| W-11 | Patient search | Not yet | search "Rivera" | |
| W-12 | Patient record | Not yet | Sam Rivera (demo) | Must show the devices section |
| W-13 | New patient form | Not yet | a new fictional patient | |
| W-20 | Schedule by provider (day) | Partial: Appointments tab lists appointments, with Schedule, Cancel, Complete | Monday of the demo week | |
| W-21 | Book an appointment | Partial: Schedule form exists | Jordan Lee (demo) with Dr. Ada Shaw (demo) | |
| W-22 | Appointment requests queue | Not yet (#68) | 3 pending requests | |
| W-23 | Cancel / complete / no-show | Partial: cancel and complete exist; no-show doesn't | | |
| W-30 | Messages inbox | Partial: Messages tab lists messages with Mark read | 4 unread threads | Threads and display names: #67 |
| W-31 | Thread and reply | Partial: Send Message form, no threads (#67) | Jordan Lee thread | |
| W-32 | Assign thread to a provider | Not yet (#67) | | |
| W-33 | Links tab: patient–provider links (invite, accept, revoke) | Exists | | May be folded into the patient record in V1 |
| W-40 | Pair phone QR | Not yet (#37) | Sam Rivera (demo) | Pair on camera so the code is used |
| W-41 | Devices on the patient record | Not yet (#10) | | |
| W-42 | Revoke device | Not yet (#10) | | |
| W-50 | Staff accounts list | Not yet (#38, #62) | 5 training staff | |
| W-51 | Invite / disable staff | Not yet (#38, #62) | | |
| W-52 | Practice device list | Not yet (#10) | | |
| W-53 | Health page | Not yet (#81 for the checks) | | Or use T-01 |
| W-54 | Audit log view | Not yet (CLI verify only, #24) | | |

### Patient app (WeHelpMobile, iPhone)

| Shot | Screen | Status | Demo data | Notes |
|---|---|---|---|---|
| P-01 | App icon and launch | Exists | — | |
| P-02 | Face ID / passcode unlock | Not yet (#37) | — | |
| P-03 | App switcher with PHI hidden | Not yet (V1-M6) | — | |
| P-04 | Sign-in with password and server list | Exists, **Debug only**, being removed (#37) | — | **Do not record.** Patients will never see it |
| P-10 | Today | Exists | Jordan Lee (demo) | |
| P-20 | Schedule (upcoming) | Exists | 2 upcoming visits | |
| P-21 | Request an appointment | Not yet (#39, #68). Today the app says to message the care team | | |
| P-30 | Messages | Exists | Jordan Lee thread | Threads: #67 |
| P-31 | Compose / reply | Partial | | |
| P-40 | Notification "Appointment update" (no details) | Not yet (#7) | | Lock screen shot |
| P-50 | Scan QR to pair | Not yet (#37) | | |
| P-51 | "Connected to Demo Naturopathy" pin confirmation | Not yet (#37; the pins are checked since #36) | | |
| P-52 | Turn on Face ID | Not yet (#37) | | |
| P-53 | Device revoked / not connected | Not yet (#10) | | |
| P-54 | Settings and sign-out | Exists; wording changes with #37 | | Re-record after #37 |
| P-60 | Notification "Visit starting" | Not yet (#7, #6) | | |
| P-61 | In-call screen (patient) | Not yet (#6) | | Two demo phones |

### Provider app

| Shot | Screen | Status | Demo data | Notes |
|---|---|---|---|---|
| R-01 | App icon and launch (separate app or role switch, decided in #69) | Not yet (#69) | | |
| R-02 | Pairing with the provider role | Not yet (#37, #69) | Dr. Ada Shaw (demo) | |
| R-10 | Today's schedule | Not yet (#69) | Monday of the demo week | |
| R-11 | Same screen with Wi-Fi off (cellular indicator visible) | Not yet (#69) | | Proves the Relay path |
| R-20 | Patient messages | Not yet (#69) | | |
| R-21 | Reply | Not yet (#69) | | |
| R-30 | Start visit from an appointment | Not yet (#6, #69) | | |
| R-31 | In-call screen (provider) | Not yet (#6) | | |
| R-32 | End visit | Not yet (#6) | | |

### Kiosk iPad

| Shot | Screen | Status | Demo data | Notes |
|---|---|---|---|---|
| K-01 | Attract screen (Guided Access) | Post-V1 (#8) | | |
| K-02 | Staff starts a kiosk session from the patient record | Post-V1 (#8, #82) | Sam Rivera (demo) | |
| K-03 | Questionnaire page: radio, slider, checkbox | Post-V1 (#82). The Interventionaire runner can stand in, labelled "prototype" | "New patient intake (demo)" | |
| K-04 | Branch appears after an answer | Post-V1 (#82) | Digestion section | |
| K-05 | Idle timeout wipes the session | Post-V1 (#8) | | |
| K-06 | Staff review queue | Post-V1 (#8) | | |

### Terminal (admin only)

| Shot | Screen | Status | Notes |
|---|---|---|---|
| T-01 | `wehelpd doctor` all PASS | Not yet (#81) | On a demo host, demo checks show FAIL by design; explain or mask |
| T-02 | `make appliance-stop-for-reboot` | Exists | |
| T-03 | `make appliance-status` | Exists | |

## Recording order

Record what exists now as drafts (W-05, W-20, W-21, W-30, W-33, P-10, P-20,
P-30, T-02, T-03), so the format and voice are settled. Re-record each
module once all its screens reach **Exists**. Modules 1 and 12 are slides
and can be final now.
