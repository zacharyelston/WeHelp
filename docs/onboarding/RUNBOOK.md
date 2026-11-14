# Practice onboarding runbook

How a whole practice gets onto WeHelp: from the Registry entry to the first
real patient paired. The practice side comes first because nothing else can
start until the practice exists in the Registry and its Back Office is
installed.

Read first: [ARCHITECTURE.md](../ARCHITECTURE.md),
[SECURITY.md](../SECURITY.md), [APPLIANCE.md](../APPLIANCE.md),
[OFFICE_KEYS.md](../OFFICE_KEYS.md), [REGISTRY.md](../REGISTRY.md) and
[RELAY.md](../RELAY.md). Training material is in
[TRAINING.md](TRAINING.md), and the kiosk demo in
[KIOSK_DEMO.md](KIOSK_DEMO.md).

**Two rules hold throughout:**
1. **Training uses synthetic data only.** Demo patients live on a demo Back
   Office registered in a demo Registry, never on the practice's own Back
   Office. Demo and field never share a Registry, a Back Office, a Relay or
   keys (ARCHITECTURE.md, "Test and demo topologies").
2. **No real patient data until go-live,** and go-live comes only after the
   V1 external audit and pen test pass (#46).

## At a glance

| Phase | What | Who leads | Typical length |
|---|---|---|---|
| 0 | Readiness: agreements, hardware, network, people | WeHelp support + practice owner | 1–3 weeks before |
| 1 | Registry: practice, providers, Relay, install token | WeHelp support | 1 hour |
| 2 | Back Office install and Relay connection | WeHelp support, on site or remote with the practice admin | Half a day |
| 3 | WeHelp Admin / provider mobile client | WeHelp support + each provider | 1 hour per provider |
| 4 | Training week at the practice | WeHelp trainer | 5 days |
| 5 | Go-live and hypercare | Practice admin, WeHelp support on call | 1 day + 2 weeks |

## Roles

| Role | Who | Responsible for |
|---|---|---|
| **WeHelp support** | WeHelp staff with Registry access (`support` role; `registry-admin` for suspend and Relay changes) | Registry entries, install tokens, the Back Office install, escalations, the go-live sign-off with the owner |
| **WeHelp trainer** | WeHelp staff (may be the same person as support) | The training week, the demo kit, the training record |
| **Practice owner** | The naturopath who owns the practice (the HIPAA covered entity) | Signing the BAA and the owner documents (risk analysis, policies), holding the backup key, the go-live decision |
| **Practice admin** | Office manager, often the owner in a small practice | Staff accounts, pairing, device revocation, daily checks, first contact for WeHelp support |
| **Providers** | Naturopaths and other clinicians | Their schedule, patient messages, visits, the provider app |
| **Front desk** | Reception staff | Patient records, appointments, pairing patient phones, first-line patient questions |

A small practice can have one person in several roles. Each person still
gets their **own** staff account. Accounts are never shared.

### RACI

R = does it, A = accountable, C = consulted, I = informed.

| Activity | WeHelp support | Trainer | Owner | Admin | Providers | Front desk |
|---|---|---|---|---|---|---|
| BAA and owner documents | C | | A/R | C | | |
| Registry entry | A/R | | C | I | C (licence details) | |
| Back Office install | A/R | | I | C | | |
| Backup key ceremony | R | | A | C | | |
| Staff accounts | C | | I | A/R | I | I |
| Provider app pairing | C | | | A | R | |
| Training week | C | A/R | C | R | R | R |
| Go-live decision | R | C | A | C | C | I |
| Daily checks after go-live | C (escalations) | | I | A/R | | |

## Readiness: what works today

The runbook describes the V1 target. Several steps depend on issues that
haven't landed. Until they do, the step runs in its interim form, shown here.

| Step | V1 form | Today | Waiting on |
|---|---|---|---|
| Registry entry | Registry console | `wehelp-registry` CLI on the Registry host | #61 |
| Who enters the practice | Decision pending | WeHelp support | #79 |
| Provider roster to Back Office | Pulled from the Registry | Staff accounts created by hand | #62 |
| Back Office install | `appliance.sh init-office` | Works (Barnacle installed 2026-11-14) | — |
| Relay connection | Automatic from `wehelpd serve` | Works on the Demo topology | — |
| Phones through the Relay | Pinned, through the Relay | Works (#36); practices entered by hand in Debug builds until QR pairing | #37 |
| Staff sign-in | Password + TOTP, HTTPS on the LAN | Password only, plain HTTP | #38 |
| Patient and provider pairing | QR in the staff UI | Not built (Debug-build sign-in only) | #37 |
| Device list and revoke | Staff UI | Not built | #10 |
| Provider app | Schedule, messages, visits | Not built | #69 |
| Telehealth | 1:1 video | Not built | #6 |
| Encrypted backups | Nightly, `age`, tested restore | Not built | #41 |
| Training data | `seed --profile training` | Default seed: 1 provider, 2 patients | #80 |
| Go-live self-check | `wehelpd doctor` | Manual checklist below | #81 |
| Field go-live | After the external audit | Not allowed yet | #46, #47 |

Until #46 passes, every onboarding is a **pilot on demo data**: phases 0–4
can all run, and phase 5 waits.

---

## Phase 0: Readiness (before anyone travels)

### Agreements and owner documents
- [ ] Business associate agreement (BAA) between WeHelp and the practice signed.
- [ ] Practice owner has a draft HIPAA risk analysis and the policies that V1-M6 prepares with them (SECURITY.md, "Audit and pen-test readiness", item 3).
- [ ] Owner has read the one-page summary of what WeHelp sees: business and workforce records in the Registry; ciphertext only at the Relay; no patient data anywhere outside the Back Office.

### People
- [ ] Practice admin named, with a backup.
- [ ] List of staff who need accounts: name, role (`admin` or `provider`; front desk uses `admin` in V1 until a narrower role exists), work email.
- [ ] List of providers for the Registry: name, credentials (licence number and state), role.
- [ ] Training week dates agreed, with the schedule blocked so staff attend (see Phase 4).

### Hardware
- [ ] Back Office Mac: a Mac mini (Apple silicon, 16 GB or more, 512 GB or more SSD) owned by the practice, new or freshly erased. A Mac Pro if the practice wants the local agent later ([../LOCAL_AGENT.md](../LOCAL_AGENT.md)).
- [ ] UPS for the Mac and the router.
- [ ] External encrypted backup target (until #41 defines the off-box copy).
- [ ] Each provider has an iPhone or iPad on a current iOS (17 or later) that they will use for work.
- [ ] Training kit reserved (Phase 4): a demo Mac mini, the trainer's laptop running the demo Registry and Relay, and 3–4 loaner iPhones labelled **TRAINING**.

### Network
- [ ] Wired Ethernet for the Back Office.
- [ ] Outbound TCP to the production Relay's SSH port, and HTTPS to the Registry. **No inbound ports**, no port forwarding.
- [ ] Office Wi-Fi uses WPA2/WPA3 with a password that isn't printed on the wall. Guest Wi-Fi, if any, is separate.
- [ ] A DHCP reservation for the Back Office, so staff browsers find it at a stable address.

**Exit:** all boxes ticked, or the gap written down with an owner and date.

---

## Phase 1: WeHelp support sets up the practice in the Registry

> **Open question (#79):** whether practices self-register with WeHelp
> approval, or WeHelp support enters them. V1 assumes support enters them.
> This phase is written that way, and changes if #79 decides otherwise.

Today this is the `wehelp-registry` CLI on the Registry host (REGISTRY.md).
Once the Registry console lands (#61), the same steps are console screens.

1. **Pick the Relay.** Production practices go on the production Relay; a
   pilot goes on the demo Relay. List them:
   ```bash
   bin/wehelp-registry relay list
   ```
2. **Create the practice** with its legal name and the Relay:
   ```bash
   bin/wehelp-registry practice add --name "<legal name>" --relay <relay name>
   ```
   Record the practice ID in the onboarding ticket.
3. **Providers and admin contacts.** Enter them in the console when #61 and
   #62 land. Until then, keep the provider list in the onboarding ticket and
   create the staff accounts on the Back Office by hand in Phase 2.
4. **Issue the install token** on the day of the install, not before. It is
   valid for 24 hours, single use, and printed once:
   ```bash
   bin/wehelp-registry token issue <practice-id>
   ```
   Hand it to the installer over a channel the two of you control (in
   person, or a password manager share). Never email it, and never paste it
   into the ticket.
5. **Check** the practice is listed and the audit chain verifies:
   ```bash
   bin/wehelp-registry practice list
   bin/wehelp-registry audit verify
   ```

**Checklist**
- [ ] Practice ID recorded in the ticket.
- [ ] Relay chosen (production or demo) and recorded.
- [ ] Install token issued on install day and handed over out of band.
- [ ] `audit verify` passes.

**Never in the Registry:** patient names, patient counts by condition,
appointment data, or anything from the practice's existing patient system.

---

## Phase 2: Install the Back Office and connect it to its Relay

Follow [APPLIANCE.md](../APPLIANCE.md) step by step. The points below are
the onboarding-specific decisions and checks.

### 2.1 Host setup (APPLIANCE.md steps 1–5)
- [ ] macOS updated, then a dedicated local service user created.
- [ ] **FileVault on.** The recovery key goes to the practice owner, stored
      offline. WeHelp does not keep it.
- [ ] **Automatic login off** (field host). After a power cut, the admin
      signs in once and the boot job does the rest.
- [ ] Never sleep, restart after power failure (`pmset`).
- [ ] Colima, Docker CLI and Compose installed; Colima runs at login.

### 2.2 Stack (APPLIANCE.md step 6)
- [ ] Clone at the **release tag** WeHelp support names. Never a branch.
- [ ] `make appliance-up` reports healthy.
- [ ] `make appliance-install-login` installs the boot job.
- [ ] `nc -z <back-office-ip> 5432` from another machine **fails**
      (Postgres is not on the LAN).
- [ ] **Do not seed.** A field Back Office never gets demo data.

### 2.3 Office identity and Registry install (APPLIANCE.md step 7, OFFICE_KEYS.md)

```bash
read -rs WEHELP_INSTALL_TOKEN && export WEHELP_INSTALL_TOKEN
WEHELP_REGISTRY_URL=https://<registry> deploy/appliance.sh init-office
unset WEHELP_INSTALL_TOKEN
```

- [ ] `init` prints the SSH public key, the key pin and the pairing identity
      (Relay CONNECT address, office ID, key pin). Copy the office ID and
      key pin into the ticket. Neither is secret.
- [ ] The Registry shows the install in its audit log.

### 2.4 Relay connection
- [ ] `wehelpd serve` (inside the stack) opens the tunnel on its own.
- [ ] From outside the practice network, a request through the Relay
      answers (RELAY.md, "Checking it"):
      ```bash
      curl --proxy https://<relay>:8443 --proxy-insecure -k https://<office-id>.wehelp.invalid/api/v1/ping
      ```
- [ ] Suspend test, for pilots on the demo Relay only: `practice suspend`
      drops the office within a minute, and `practice reactivate` brings it
      back.

### 2.5 Staff access on the LAN
- [ ] Install the office's local CA (`ca.crt`) in each staff browser (#38).
- [ ] Create staff accounts: the practice admin first, then providers (from
      the Registry roster once #62 lands), then front desk.
- [ ] Each person sets their own password and enrols TOTP at their own
      desk. WeHelp support never knows a staff password.

### 2.6 Backups (before any real data)
- [ ] Backup key ceremony: the owner generates the `age` key, keeps the
      private key offline (two copies, two places), and WeHelp installs only
      the public key (#41).
- [ ] One backup taken and **restored to a scratch database**, and the
      restore recorded with its date.
- [ ] The `officekeys` volume is in the backup. Losing the office TLS key
      means re-pairing every phone (OFFICE_KEYS.md).

**Exit:** the Back Office is healthy, installed, reachable through its
Relay, has staff accounts, and has a tested backup. It holds no patients.

---

## Phase 3: WeHelp Admin / provider mobile client

The provider app (#69) is how providers reach their schedule, messages and
visits in and out of the office. Providers pair the same way patients do:
QR from the staff UI, with the provider role (#37). Their device key, held
in the Secure Enclave, is their credential on the phone.

For each provider:
1. [ ] Install the provider app (TestFlight during the pilot, App Store
       after).
2. [ ] The practice admin opens the provider's staff account in the staff
       UI and presses **Pair phone**. The QR is valid for 10 minutes.
3. [ ] The provider scans it. The app pins the office key and enrols.
4. [ ] The provider turns on Face ID or a device passcode (required).
5. [ ] **Remote check:** with Wi-Fi off, on cellular, the provider sees
       today's schedule. This proves the Relay path; there is no VPN.
6. [ ] **Revoke check (once, with the admin):** the admin revokes a test
       device in the staff UI (#10), and it is locked out within one access
       token lifetime (15 minutes).

The admin's own phone, if they want one, pairs the same way. Admin
functions (users, pairing, devices, settings) stay in the staff UI on the
LAN and are not in the app.

**Exit:** every provider's device is paired, works on cellular, and appears
in the device list.

---

## Phase 4: Training week at the practice

Training runs on the **training kit**, not the practice's Back Office:

- a demo Mac mini as the Back Office, with `demo_mode` on and the training
  seed loaded (`wehelpd seed --profile training`, #80);
- the trainer's laptop running the demo Registry and Relay (the Demo
  topology);
- 3–4 loaner iPhones labelled **TRAINING**, with the patient app
  (WeHelpMobile) paired to the demo office as demo patients;
- staff browsers trust the demo office's CA only for the week. The trainer
  removes it on Friday.

Staff never pair their personal phones to the demo office. Providers bring
their already-paired work devices only for the provider-app module, and
that module uses a demo provider account on a loaner device.

Each day is about 2 hours of training plus practice, scheduled around the
clinic day. Modules refer to [TRAINING.md](TRAINING.md).

### Day 1 (Monday): Why, privacy, and the staff UI
| Time | Session | Who |
|---|---|---|
| 0:00 | Kickoff: what WeHelp is, what changes at the front desk, the week's plan | All |
| 0:20 | Module 1: privacy rules. Where patient data lives, and what never goes in a message, email or screenshot | All |
| 0:50 | Module 2: staff sign-in (password + TOTP) on each person's own machine | All |
| 1:20 | Module 3 walkthrough: patients list and patient record | Admin, front desk |
| 1:45 | Homework: each person signs in and finds 3 demo patients | All |

### Day 2 (Tuesday): Scheduling and messages
| Time | Session | Who |
|---|---|---|
| 0:00 | Module 4: appointments. Book, confirm a request, cancel, complete, no-show | Front desk, admin |
| 0:40 | Module 5: messages. Inbox, reply, mark read, hand a thread to a provider | All |
| 1:20 | Practice: the trainer, as a demo patient on a loaner iPhone, sends requests and messages; staff work the queue | All |
| 1:50 | Homework: clear the demo inbox by end of day | Front desk |

### Day 3 (Wednesday): The patient app and pairing
| Time | Session | Who |
|---|---|---|
| 0:00 | Module 6: the patient app tour (appointments, messages) on loaner iPhones | All |
| 0:30 | Module 7: **Pair phone**. Each staff member pairs a loaner iPhone to a demo patient, then revokes it | Front desk, admin |
| 1:10 | Module 8: lost phone, new phone, a patient who can't scan; what staff say and do | Front desk, admin |
| 1:40 | Role-play: a demo patient arrives for their first visit, pairs, and books a follow-up | Front desk |

### Day 4 (Thursday): Providers, visits and the kiosk preview
| Time | Session | Who |
|---|---|---|
| 0:00 | Module 9: the provider app. Today's schedule, reply to a patient, remote access on cellular | Providers |
| 0:40 | Module 10: video visits (once #6 lands; otherwise a recorded demo) | Providers, front desk |
| 1:20 | Kiosk preview, post-V1 ([KIOSK_DEMO.md](KIOSK_DEMO.md)) | All, optional |
| 1:45 | Q&A with providers on their own workflow | Providers |

### Day 5 (Friday): Admin, dress rehearsal and sign-off
| Time | Session | Who |
|---|---|---|
| 0:00 | Module 11: admin tasks. Staff accounts, device list, daily checks, reboots, who to call | Admin |
| 0:30 | Module 12: security incidents. Lost device, suspected breach, a staff member leaves | Admin, owner |
| 0:50 | Dress rehearsal: a full demo patient day from the front desk to the provider and back, with no trainer help | All |
| 1:30 | Training record signed; go-live criteria reviewed (below); open issues listed | Owner, admin, trainer |
| 1:50 | Trainer removes the demo CA from staff browsers, collects loaner phones, and resets the demo kit (`seed --reset`) | Trainer |

### Training checklist
- [ ] Every staff member signed in with their own account and TOTP.
- [ ] Every front-desk and admin user paired and revoked a loaner phone.
- [ ] Every provider replied to a demo patient from the provider app.
- [ ] Dress rehearsal finished without trainer help.
- [ ] Training record signed by each attendee (date, modules, trainer). It
      is part of the owner's HIPAA workforce-training evidence.
- [ ] Demo CA removed from staff browsers; loaner phones collected and wiped.

---

## Phase 5: Go-live and hypercare

### Go-live criteria

All must be true. Once #81 lands, `wehelpd doctor` checks the
machine-checkable rows.

**Gate (outside the practice)**
- [ ] V1 external audit and pen test passed, every high or critical finding
      fixed and retested (#46).
- [ ] The practice is on the **production** Registry and Relay. A pilot on
      the demo Relay never goes live.

**Back Office** (`wehelpd doctor`, #81)
- [ ] FileVault on, automatic login off.
- [ ] Running a tagged release.
- [ ] Postgres on loopback only.
- [ ] Office keys present with correct permissions.
- [ ] Installed with the Registry, and the Relay tunnel up.
- [ ] `audit.Verify` passes.
- [ ] Last backup younger than 26 hours, and a restore tested.
- [ ] `demo_mode` off, no seed accounts.
- [ ] Staff UI over HTTPS with the local CA.

**People and process**
- [ ] Every staff member trained (signed record) and has their own account
      with TOTP.
- [ ] Every provider device paired and checked on cellular.
- [ ] The owner holds the FileVault recovery key and the backup private
      key, offline.
- [ ] Owner documents (risk analysis, policies) signed.
- [ ] Escalation path posted at the front desk: practice admin, then WeHelp
      support (phone and hours).
- [ ] Rollback plan agreed (below).

**Sign-off:** the practice owner and WeHelp support sign the go-live
record with the date and the release tag.

### Go-live day
1. Morning: the admin runs `wehelpd doctor` (or the manual list) and posts
   the result to the ticket. It must carry no patient data.
2. Front desk creates the first real patient and pairs their phone at the
   desk. WeHelp support watches remotely from the Registry and Relay health
   views only; they see no patient data.
3. Providers confirm the first real appointment and message on their
   devices.
4. End of day: the admin checks that the nightly backup is scheduled.

### Hypercare (first two weeks)
- Daily 10-minute check-in between the admin and WeHelp support (no
  patient data on the call or in the ticket; IDs only if needed).
- WeHelp support watches Relay and office health and the audit-stream
  verification (OBSERVABILITY.md).
- At day 14: review issues, then close hypercare or extend it by a week.

### Rollback
WeHelp holds no patient data, so rollback is the practice going back to its
previous process for appointments and messages while the Back Office keeps
what it already has.

1. The practice owner decides to roll back.
2. Staff tell paired patients by phone that the app is paused.
3. WeHelp support suspends the practice in the Registry if the problem is
   the connection path; the Back Office keeps running on the LAN.
4. Data stays on the Back Office and in its backups. Nothing is exported to
   WeHelp.
5. Resume: fix, re-run the go-live criteria, reactivate in the Registry.
