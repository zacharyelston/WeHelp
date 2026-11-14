# WeHelp observability

How the Back Office, the Relay and the Registry report their health, and how
audit events leave the box for long-term retention. Architecture is in
[ARCHITECTURE.md](ARCHITECTURE.md); the security bar is in
[SECURITY.md](SECURITY.md).

## Goals

1. **See load and failures** on each host: CPU, memory, disk, request rates,
   errors, tunnel status, telehealth sessions.
2. **Keep an audit trail off the box:** security and audit events streamed to
   a remote store the hosting group controls, for HIPAA audit-control
   retention.
3. **Fit tiny hosts.** The Back Office and Relay run on Mac minis and laptops.
   Dashboards must never starve the services they watch.
4. **Leave the destination to whoever hosts WeHelp:** their own Grafana,
   our corporate cloud offering, or their existing SIEM.

## The pieces

```
 Back Office host                                Relay host
 ┌─────────────────────────────┐                ┌─────────────────────────────┐
 │ wehelpd ──OTLP──┐           │                │ wehelp-relay ──OTLP──┐      │
 │ stdout JSON ────┤           │                │ stdout JSON ─────────┤      │
 │ audit_events ───┤  Alloy    │                │ host metrics ────────┤Alloy │
 │ host metrics ───┘ (always)  │                │                      │      │
 └──────────┬──────────────────┘                └──────────┬───────────┴──────┘
            │ ops stream (no identifiers)                   │
            │ audit stream (IDs only, TLS, BAA destination) │
            ▼                                               ▼
   dashboards: Grafana + Loki + Prometheus, wherever the host group chooses
   (same host in dev · a separate dashboard box · WeHelp Cloud Dashboards)
            │
            └── optional: syslog (RFC 5424 over TLS) or OTLP to a SIEM
```

- **Alloy (always on, on every host).** One Grafana Alloy collector per
  Back Office, Relay and Registry host, as a Compose sidecar with a memory
  limit. It
  collects container logs, host metrics, OTLP from our binaries, and (on the
  Back Office) the audit stream. It forwards to whatever destinations are
  configured. It's the only telemetry component that must run next to the
  service.
- **Dashboards (optional, anywhere).** Grafana with Loki (logs) and
  Prometheus (metrics), shipped as a separate Compose profile. They can run
  on the same host, on another machine, or not at all. Several hosts can feed
  one dashboard stack. Each Alloy sets `office_id` and `role` labels
  (`backoffice`, `relay` or `registry`).
- **Remote export (optional, per deployment).**
  - **Ops stream:** to a Grafana or OTLP endpoint.
  - **Audit stream:** to syslog (RFC 5424 over TLS) or OTLP.

  Endpoints and credentials live in the host's `.env`, mode 600. Nothing is
  exported unless configured.

## Two streams, two rules

| | Ops stream | Audit stream |
|---|---|---|
| Content | Metrics, application logs, traces | One record per `audit_events` row: seq, time, actor ID, device ID, action, resource type and ID, prev hash, hash |
| Identifiers | None: route templates, status codes, office ID, counts | Opaque UUIDs only. Never names, contact details, message text or tokens |
| Destination | Any operator-chosen Grafana or OTLP endpoint | Only a destination covered by a BAA with the practice, over TLS |
| Default | Local dashboards when the profile is on; no remote | Off until configured |

- **Why split:** ops data helps whoever runs the hardware and should flow
  freely. Audit records point at real patients' records even without
  content, so they go only where a BAA covers them.
- **Labels:** low-cardinality only (`office_id`, `role`, `service`, route
  template, status class). Per-request values (request ID, audit seq) go in
  log lines or structured metadata, never labels, so Loki's index stays small.
- **Audit integrity:** the audit stream carries `prev_hash` and `hash`, so
  the remote copy can be checked with the same chain rule as `audit.Verify`.
  The exporter resumes from the last acknowledged `seq`. The database stays
  the source of truth.

## What each service emits

**Back Office (`wehelpd`):**
- HTTP request rate, latency and status by route template, through OTel
  middleware;
- database pool use;
- River job counts and failures;
- device enrollments and refresh failures (counts);
- audit-chain verify result;
- application logs as JSON on stdout, with no PHI (enforced by #42).

**Relay (`wehelp-relay`):**
- connected offices, tunnel reconnects and phone channels opened (counts);
- bytes relayed per office;
- TURN allocations and bandwidth;
- push requests by category;
- no device IPs.

**Registry (`wehelp-registry`):**
- console sign-ins and failures;
- install tokens issued and redeemed;
- office-list publications and the version each Relay holds;
- roster requests;
- no practice or provider details in labels.

The Registry console shows Relay and office health from these dashboards.

**Hosts:** CPU, load, memory, disk and network, from Alloy's built-in node
exporter.

## Load guard for tiny hosts

Dashboards are the first thing to go when a small host is under pressure:
- Compose resource limits on every observability container. Alloy has a
  hard memory ceiling and its memory-limiter component enabled.
- A **load guard** runs every minute on dev and demo hosts. If 5-minute load
  or memory pressure stays above a threshold for 5 minutes, it stops the
  `dashboards` profile and logs why. Alloy keeps running, so nothing is lost:
  logs and metrics queue or go to the remote destination. Dashboards restart
  only by hand.
- On a field Back Office, local dashboards are off by default. The practice
  views dashboards remotely, or the vendor's stack does.

## Hosting choices (decided by whoever operates WeHelp)

| Option | Dashboards run on | Fits |
|---|---|---|
| **Dev** | The same laptop, profile on | Local development; the load guard is on |
| **Demo** | A separate machine or the laptop, two Alloys feeding one stack | Investor and practitioner demos (laptop Relay + Barnacle Back Office) |
| **Self-hosted** | The practice's or hosting group's own server | Groups with their own IT |
| **WeHelp Cloud Dashboards** | Our multi-tenant cloud Grafana stack | The corporate offering, below |
| **Bring your own** | Their SIEM or Grafana | Syslog or OTLP export only |

## WeHelp Cloud Dashboards (corporate offering, PoC)

A paid option where offices ship telemetry to a Grafana stack we operate.

- **Tenancy:** one tenant per office (Loki and Prometheus tenant = office
  ID). Per-office write credentials are issued with the Relay subscription.
  Grafana organisations give each practice its own view; we see aggregate
  fleet health.
- **What we receive:** the ops stream by default. The audit stream only for
  practices that sign a BAA with us for it.
- **PoC scope:**
  - one cloud Grafana stack, either Grafana Cloud or a small VPS, decided in
    the PoC;
  - Barnacle and the laptop Relay ship to it with per-office credentials;
  - one fleet dashboard and one per-office dashboard;
  - tenant isolation shown: office A can't see office B.
- **Decisions the PoC must inform:** hosted Grafana Cloud vs self-run, cost
  per office, retention periods (ops vs audit), and the BAA position for the
  audit stream.

## Infrastructure justification

ARCHITECTURE.md limits infrastructure beyond Postgres and the Relay.
Observability is justified because HIPAA audit controls need retained,
reviewable audit records, and because V1 runs on small hosts that need load
visibility. Only Alloy is required next to a service. Everything else is
optional, can live elsewhere, and the load guard can switch it off.
