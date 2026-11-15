# Backups and restore (#41)

The Back Office holds every patient record, so it is backed up every night,
encrypted to a key only the practice owner holds, and copied off the Mac mini.
A restore is tested before go-live and every quarter after that.

## What a backup holds

Each backup is a folder `wehelp-backup-<UTC time>/`:

| File | What | Why |
|---|---|---|
| `db.dump.age` | `pg_dump` of the database | patients, messages, appointments, staff accounts, the audit log |
| `officekeys.tar.age` | the office keys volume | the office identity, the TLS key paired phones pin, the staff-web CA. Without it every phone must be paired again |
| `env.age` | `deploy/.env` | the session signing secret |
| `db-env.age` | `deploy/db.env` | the database passwords (#161). Backups from before #161 don't have it; a restore then makes new ones |
| `manifest.json` | sizes, SHA-256 of each file, migration version, recipients | lets a restore check the files before touching anything |

Each file is encrypted with [age](https://age-encryption.org) to the practice
owner's public key as it is written, so nothing readable ever reaches a disk.
The Mac mini can make backups but can't read them.

## Set up (once, at install)

1. **The practice owner makes a key** on their own computer, not on the
   Mac mini, and keeps it like the keys to the building. A printed copy in
   the practice safe and one in a password manager is a good pair.

   ```bash
   brew install age
   age-keygen -o wehelp-backup-owner.agekey   # keep secret; prints the public key
   ```

2. **On the Mac mini**, put the owner's public key (`age1…`) in
   `deploy/backup-recipients.txt`. Add a second line for a second keyholder
   if the practice wants one.
3. **Choose where the copy goes** in `deploy/backup.conf`. Use a folder on an
   external drive, or an rsync target such as `nas.local:/backups/wehelp`:

   ```bash
   WEHELP_BACKUP_COPY_TO=/Volumes/PNY1TB/Backups/WeHelp
   ```

4. Install `age`, take the first backup by hand, and install the nightly
   job:

   ```bash
   brew install age
   deploy/appliance.sh backup
   deploy/appliance.sh install-backup     # every night at 02:30
   ```

Other settings: `WEHELP_BACKUP_DIR` (local copies, default `~/WeHelpBackups`)
and `WEHELP_BACKUP_KEEP` (30 backups kept in each place).

## When a backup fails

- The staff web shows admins a red **Backups** banner when the last backup
  failed (with the reason) or the last good one is more than 36 hours old.
- The Mac mini shows a notification.
- The job's log is `~/Library/Logs/wehelp-backup.log`.

Common causes:
- the external drive isn't mounted;
- `backup-recipients.txt` has no `age1…` key;
- Docker isn't running.

## Restore

On the machine that will run the Back Office (a replacement Mac mini, or a
spare machine for the quarterly test), with the repo checked out:

```bash
brew install age
deploy/appliance.sh restore /path/to/wehelp-backup-<time> /path/to/wehelp-backup-owner.agekey
```

The restore:
1. checks every file against the manifest, and refuses if any differs;
2. decrypts `.env` first, so a wrong key is refused before anything changes;
3. asks you to type `RESTORE`;
4. keeps the old `.env` as `.env.before-restore-<time>`;
5. replaces the database, office keys, `.env` and `db.env`, setting up the
   database roles before restoring the data (#161);
6. starts the stack, which applies any newer migrations, and waits for it to
   be healthy.

Then:
- sign in to the staff web;
- check the audit log with `docker exec deploy-server-1 wehelpd audit verify`;
- check that a paired phone connects.

### The quarterly restore test (on a spare machine)

Run it as a second stack, so it can't clash with anything on that machine.
**Turn the tunnel off.** A restored office uses the real office's identity,
and would otherwise take over its Relay connection:

```bash
COMPOSE_PROJECT_NAME=wehelprestore WEHELP_DB_PORT=15432 WEHELP_HTTP_PORT=18080 \
WEHELP_HTTPS_PORT=18443 WEHELP_RELAY_TUNNEL=false \
  deploy/appliance.sh restore /path/to/backup /path/to/owner.agekey
docker exec wehelprestore-server-1 wehelpd audit verify
# sign in at https://localhost:18443, then tear it down:
COMPOSE_PROJECT_NAME=wehelprestore docker compose -f deploy/docker-compose.yml \
  -f deploy/docker-compose.appliance.yml down -v
rm deploy/.env   # the restored secret, on a machine that isn't the office's
```

### Last test

2026-11-14: Barnacle's backup was taken from its external-drive copy and
restored on a laptop as a second stack.
- **Data:** the audit chain verified (396 events), and the user,
  appointment and message counts matched Barnacle.
- **Keys:** the office CA was Barnacle's own.
- **Relay:** the tunnel stayed off, and the Relay saw no second connection.
- **Refusals:** a wrong identity and a tampered file were both refused
  before anything changed.
