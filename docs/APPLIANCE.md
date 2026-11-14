# Back Office appliance runbook (Mac mini)

How to run the WeHelp Back Office as an always-on appliance on a Mac mini in
the practice. Barnacle, the Demo-topology Back Office, runs exactly this.
Architecture is in [ARCHITECTURE.md](ARCHITECTURE.md); security requirements
for a fielded machine are in [SECURITY.md](SECURITY.md).

## What it runs

`deploy/docker-compose.yml` (Postgres, the Flyway `migrate` job, `wehelpd`),
layered with `deploy/docker-compose.appliance.yml`:
- `restart: unless-stopped` on the database and server;
- Postgres bound to `127.0.0.1` only, never the office LAN;
- `deploy/.env` (mode 600, gitignored) holding a persistent
  `WEHELP_JWT_SECRET`, so sessions survive restarts.

`deploy/appliance.sh` (or the `make appliance-*` targets) always passes both
files.

## One-time host setup

1. **Homebrew, Colima, Docker CLI and Compose:**
   `brew install colima docker docker-compose`. Then add
   `"cliPluginsExtraDirs": ["/opt/homebrew/lib/docker/cli-plugins"]` to
   `~/.docker/config.json`, so `docker compose` works.
2. **Colima at login:** `brew services start colima`. It runs as a login
   (launchd) service, so the Mac must log in by itself after a restart.
3. **Power settings** (System Settings, or `sudo pmset`):
   - never sleep: `sudo pmset -a sleep 0`;
   - restart after a power failure: `sudo pmset -a autorestart 1`.
4. **Auto-login:**
   - **Demo/test hosts (Barnacle):** turn on automatic login for the service
     user, so Colima and the boot job start unattended.
   - **Field hosts:** automatic login stays **off** and FileVault is **on**
     (SECURITY.md). After a power cut someone signs in once; the boot job
     then does the rest.
5. **Local Network permission** (macOS 15 and later; only when the Relay is
   on the office LAN, as in the Demo topology): on the Mac mini's own
   screen, approve "Allow local network" for Colima and the WeHelp tunnel
   when macOS asks. Check it under System Settings → Privacy & Security →
   Local Network. Without it, connections to LAN addresses fail with
   `no route to host` or `broken pipe`. A production Relay on the internet
   isn't affected. See docs/spikes/S1.md.
6. **Clone and start:**
   ```
   git clone https://github.com/zacharyelston/WeHelp.git ~/wehelp
   cd ~/wehelp && git checkout <release tag>
   make appliance-up            # creates deploy/.env if missing, builds, starts, waits for /healthz
   make appliance-install-login # installs the login-time boot job
   ```

## After a reboot

The boot job (`~/Library/LaunchAgents/com.wehelp.appliance.plist`) runs
`appliance.sh boot` at login:
1. It waits up to 10 minutes for Docker. After a macOS update, boot load is
   high and Colima can be slow.
2. It runs `docker compose … up -d`. This is needed even with restart
   policies: after an abrupt VM shutdown, Docker can bring a container back
   in the `created` state, which restart policies never start. This happened
   on Barnacle after an OS-update reboot; see #12.
3. It waits up to 5 minutes for `/healthz` and logs `healthy` or
   `UNHEALTHY` to `~/Library/Logs/wehelp-appliance.log`.

**Before a planned reboot or macOS update,** run
`make appliance-stop-for-reboot`. It stops the stack and Colima cleanly, so
the VM saves container state.

## Redeploying a new version

```
cd ~/wehelp && git fetch --tags && git checkout <new tag>
make appliance-up
```

Flyway applies any new migrations before the server starts. Applied
migrations are never edited (checksums are enforced), so a redeploy keeps
existing data.

## Seeding demo data (Demo hosts only)

Postgres listens only on the Mac mini's own loopback, so seed from a dev
machine through an SSH tunnel:

```
ssh -f -N -L 15432:127.0.0.1:5432 <user>@<host>.local
WEHELP_DATABASE_URL='postgres://wehelp:wehelp@127.0.0.1:15432/wehelp?sslmode=disable' bin/wehelpd seed
```

Never seed a field Back Office.

**Pitfall:** on a dev Mac with Homebrew Postgres running, `localhost:5432` is
that Postgres, not the compose database. `make seed` then fails with
`role "wehelp" does not exist`. Point `WEHELP_DATABASE_URL` at the compose
database explicitly, as above (or at the Mac's LAN address in dev).

## Checks

- `make appliance-status`: containers and `/healthz`.
- From another machine, `nc -z <mac-mini-ip> 5432` must **fail** (Postgres is
  not on the LAN).
- `tail ~/Library/Logs/wehelp-appliance.log` after any restart.
