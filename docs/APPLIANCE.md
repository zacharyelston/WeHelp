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
7. **Office identity and Registry install** (#35): get an install token from
   WeHelp support, then run `deploy/appliance.sh init-office` with
   `WEHELP_INSTALL_TOKEN` and `WEHELP_REGISTRY_URL` set. The keys live on
   the `officekeys` volume. See docs/OFFICE_KEYS.md, which also covers key
   rotation. Back up the volume with the database: losing the TLS key means
   re-pairing every phone.
8. **Staff web UI over HTTPS** (#38):
   1. Set `WEHELP_STAFF_HOSTS` in `deploy/.env` to the names and LAN
      addresses staff will type, for example
      `barnacle.local,192.168.1.55`. Then `make appliance-up`.
   2. Staff use `https://<host>:8443`; plain `:8080` redirects there.
   3. Run `deploy/appliance.sh export-ca > wehelp-office-ca.crt` and install
      that certificate on each staff machine (macOS: open it, then in
      Keychain Access set it to Always Trust).
   4. Create each staff account on this machine:
      ```
      cd deploy && docker compose -f docker-compose.yml -f docker-compose.appliance.yml \
        run --rm --no-deps server staff add --tenant "<practice>" --email <email> --name "<name>" --role provider|admin
      ```
      It asks for the password. Each person sets up their authenticator app
      at their first sign-in.
   5. Set up nightly encrypted backups to the practice owner's key, copied
      off the machine, and test a restore: [BACKUPS.md](BACKUPS.md).

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

If Docker isn't up a minute after login, the boot job steps in once.
After a reboot, Lima's `vz.pid` can name a process ID that macOS has given
to something else, and Colima then refuses to start ("vz driver is running
but host agent is not"). The boot job removes that stale file, along with
`ha.sock`, and restarts Colima. This happened on Barnacle after a macOS
update (2026-11-14) and was fixed in #123.

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

## Demo hosts and field installs (#161)

A **demo host** (like Barnacle) is for showing WeHelp, with fixed logins
anyone can use. A **field install** is a practice's Back Office and never has
them. The difference is one line in `deploy/.env`:

```
WEHELP_DEMO=1
```

With it, `appliance.sh up` builds the image with the `demo` tag. That adds:
- the seed command, which creates the Demo Clinic practice, its staff and
  patients, all with fixed passwords;
- the demo logins, listed on the staff web's sign-in page.

Then seed it (idempotent):

```
deploy/appliance.sh seed
```

Without the line, the image has neither, and `appliance.sh seed` refuses.
CI checks the field image for demo logins and the seed command. Never set
`WEHELP_DEMO=1` on a practice's Back Office.

## Database passwords and roles (#161)

`appliance.sh up` creates `deploy/db.env` (mode 600) once, with two random
passwords:
- `wehelp` owns the schema and runs migrations;
- `wehelp_app` is what the server connects as. It can read and write data,
  but can't change the schema or alter audit and ledger rows.

Nobody types these. Every `up` and `boot` sets the database to match
`db.env`. Backups include it (`db-env.age`).

The tracked compose files never name these passwords. On each run,
`appliance.sh` (through `deploy/compose.sh`) writes them from `db.env` into
two generated files, also mode 600 and gitignored:
- `db-owner.env` for the database and migrations;
- `db-server.env`, the server's `wehelp_app` connection URL.

So the server never holds the owner password. Don't edit the generated
files; they are rewritten on every run. Without `db.env` the stack refuses
to start instead of falling back to the dev password. This also keeps the
compose files clean for secret scanners: GitGuardian flagged the earlier
`${WEHELP_DB_PASSWORD…}` lines on #174.

The server image runs as a non-root user (uid 65532). `up` gives the
office-keys volume to that user.

**Upgrading an install from before #161:** just run `appliance.sh up`. It
does all of this in place, and the old shared password stops working. To roll
back to an older version, first set the old password again:

```
docker compose -f deploy/docker-compose.yml exec db psql -U wehelp -c "alter role wehelp password 'wehelp'"
```

**Pitfall:** on a dev Mac with Homebrew Postgres running, `localhost:5432` is
that Postgres, not the compose database. `make seed` then fails with
`role "wehelp" does not exist`. Point `WEHELP_DATABASE_URL` at the compose
database explicitly, or use `appliance.sh seed`.

## Checks

- `make appliance-status`: containers and `/healthz`.
- From another machine, `nc -z <mac-mini-ip> 5432` must **fail** (Postgres is
  not on the LAN).
- `tail ~/Library/Logs/wehelp-appliance.log` after any restart.
