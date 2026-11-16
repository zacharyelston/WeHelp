#!/usr/bin/env bash
# Encrypted Back Office backups and restore (#41). Run through appliance.sh:
#
#   appliance.sh backup                       one backup now (the nightly job runs this)
#   appliance.sh restore <backup-dir> <identity-file>
#                                             replace this stack's data with a backup
#   appliance.sh install-backup               install the nightly job, 02:30 (macOS launchd, Linux systemd timer)
#
# A backup is a directory wehelp-backup-<UTC stamp>/ holding three files, each
# encrypted with age to the practice owner's public key(s) as it is written,
# so nothing readable ever touches disk:
#   db.dump.age          pg_dump (custom format) of the wehelp database
#   officekeys.tar.age   the office keys volume (identity, TLS key phones pin, CA)
#   env.age              deploy/.env (the session signing secret)
# and manifest.json (sizes, SHA-256 of each encrypted file, migration
# version, recipients). Only the holder of the matching age identity can
# restore. See docs/BACKUPS.md.
#
# Settings, from the environment or deploy/backup.conf (KEY=VALUE lines):
#   WEHELP_BACKUP_RECIPIENTS  age public keys, one per line (default deploy/backup-recipients.txt)
#   WEHELP_BACKUP_DIR         where backups are written (default ~/WeHelpBackups)
#   WEHELP_BACKUP_COPY_TO     off-machine copy: a mounted directory (e.g. an
#                             external drive) or an rsync target host:path
#   WEHELP_BACKUP_KEEP        backups kept in each place (default 30)
set -euo pipefail
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$DEPLOY_DIR/.." && pwd)"
ENV_FILE="$DEPLOY_DIR/.env"
[[ -f "$DEPLOY_DIR/backup.conf" ]] && set -a && source "$DEPLOY_DIR/backup.conf" && set +a
PROJECT="${COMPOSE_PROJECT_NAME:-$(basename "$DEPLOY_DIR")}"
RECIPIENTS="${WEHELP_BACKUP_RECIPIENTS:-$DEPLOY_DIR/backup-recipients.txt}"
BACKUP_DIR="${WEHELP_BACKUP_DIR:-$HOME/WeHelpBackups}"
COPY_TO="${WEHELP_BACKUP_COPY_TO:-}"
KEEP="${WEHELP_BACKUP_KEEP:-30}"
HEALTH_URL="${WEHELP_HEALTH_URL:-http://127.0.0.1:${WEHELP_HTTP_PORT:-8080}/healthz}"

DB_ENV="$DEPLOY_DIR/db.env"
source "$DEPLOY_DIR/compose.sh" # compose()
log() { printf '%s backup: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
size() { stat -f %z "$1" 2>/dev/null || stat -c %s "$1"; }

# record writes one row to backup_runs (no patient data), so the staff web can
# warn admins. If the database is down, the missing good run shows later.
record() {
  local ok="$1" bytes="$2" copied="$3" manifest="$4" err="$5"
  printf "insert into backup_runs (started_at, ok, bytes, copied_to, manifest, error) values (:'started', :'ok', :'bytes', :'copied', :'manifest', :'err');\n" |
    compose exec -T db psql -q -U wehelp -d wehelp -v ON_ERROR_STOP=1 \
      -v started="$STARTED" -v ok="$ok" -v bytes="$bytes" -v copied="$copied" -v manifest="$manifest" -v err="$err" \
      >/dev/null 2>&1 || log "could not record this run in backup_runs"
}

fail() {
  log "BACKUP FAILED: $1"
  record false 0 "" "" "$1"
  # A visible alert on the Mac mini, besides the staff web banner.
  command -v osascript >/dev/null 2>&1 &&
    osascript -e "display notification \"$1\" with title \"WeHelp backup failed\"" >/dev/null 2>&1 || true
  exit 1
}

# prune keeps the newest $KEEP backups in a directory.
prune() {
  local dir="$1"
  [[ -d "$dir" ]] || return 0
  ls -1d "$dir"/wehelp-backup-* 2>/dev/null | sort -r | tail -n +"$((KEEP + 1))" | while read -r old; do
    rm -rf "$old"
    log "removed old backup $(basename "$old")"
  done
}

backup() {
  STARTED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  command -v age >/dev/null 2>&1 || fail "age is not installed (brew install age)"
  grep -q '^age1' "$RECIPIENTS" 2>/dev/null || fail "no age public keys in $RECIPIENTS"
  [[ -f "$ENV_FILE" ]] || fail "missing $ENV_FILE"
  compose exec -T db pg_isready -U wehelp >/dev/null 2>&1 || fail "the database isn't running"

  local stamp name tmp final
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  name="wehelp-backup-$stamp"
  mkdir -p "$BACKUP_DIR" && chmod 700 "$BACKUP_DIR"
  tmp="$BACKUP_DIR/.$name.partial"
  final="$BACKUP_DIR/$name"
  rm -rf "$tmp" && mkdir -m 700 "$tmp"
  trap 'rm -rf "$tmp"' EXIT

  compose exec -T db pg_dump -U wehelp -d wehelp -Fc | age -R "$RECIPIENTS" -o "$tmp/db.dump.age" ||
    fail "pg_dump or encryption failed"
  docker run --rm -v "${PROJECT}_officekeys:/k:ro" alpine:3 tar -C /k -cf - . | age -R "$RECIPIENTS" -o "$tmp/officekeys.tar.age" ||
    fail "reading the office keys volume failed"
  age -R "$RECIPIENTS" -o "$tmp/env.age" "$ENV_FILE" || fail "encrypting .env failed"
  if [[ -f "$DB_ENV" ]]; then
    age -R "$RECIPIENTS" -o "$tmp/db-env.age" "$DB_ENV" || fail "encrypting db.env failed"
  fi

  local migration commit f total=0 files=""
  migration="$(compose exec -T db psql -U wehelp -d wehelp -Atc "select max(version::int) from flyway_schema_history where success" 2>/dev/null || echo "")"
  commit="$(git -C "$REPO_DIR" rev-parse --short HEAD 2>/dev/null || echo "")"
  for f in db.dump.age officekeys.tar.age env.age db-env.age; do
    [[ -f "$tmp/$f" ]] || continue # db-env.age: installs from #161 on
    local n
    n="$(size "$tmp/$f")"
    (( n > 150 )) || fail "$f is empty"
    head -c 40 "$tmp/$f" | grep -q 'age-encryption.org/v1' || fail "$f is not age-encrypted"
    total=$((total + n))
    files="$files${files:+,}{\"name\":\"$f\",\"bytes\":$n,\"sha256\":\"$(sha "$tmp/$f")\"}"
  done
  printf '{"created_at":"%s","host":"%s","migration":"%s","commit":"%s","recipients":[%s],"files":[%s]}\n' \
    "$STARTED" "$(hostname -s)" "$migration" "$commit" \
    "$(grep '^age1' "$RECIPIENTS" | sed 's/.*/"&"/' | paste -sd, -)" "$files" > "$tmp/manifest.json"
  mv "$tmp" "$final"
  trap - EXIT
  log "wrote $final ($total bytes, migration $migration)"

  local copied=""
  if [[ -n "$COPY_TO" ]]; then
    if [[ "$COPY_TO" == *:* ]]; then
      rsync -a "$final" "$COPY_TO/" || fail "copy to $COPY_TO failed; the backup is still in $BACKUP_DIR"
    else
      [[ -d "$COPY_TO" ]] || fail "copy target $COPY_TO isn't there (drive not mounted?); the backup is still in $BACKUP_DIR"
      rsync -a "$final" "$COPY_TO/" || fail "copy to $COPY_TO failed; the backup is still in $BACKUP_DIR"
      prune "$COPY_TO"
    fi
    copied="$COPY_TO"
    log "copied to $COPY_TO"
  else
    log "WARNING: no WEHELP_BACKUP_COPY_TO, so this backup is only on this machine"
  fi
  prune "$BACKUP_DIR"
  record true "$total" "$copied" "$(sha "$final/manifest.json")" ""
}

wait_for_db() {
  local waited=0
  until compose exec -T db pg_isready -U wehelp >/dev/null 2>&1; do
    (( waited >= 120 )) && { log "database not ready after 120s"; return 1; }
    sleep 2
    waited=$((waited + 2))
  done
}

restore() {
  local src="${1:-}" identity="${2:-}"
  [[ -d "$src" && -f "$src/manifest.json" ]] || { echo "usage: appliance.sh restore <backup-dir> <age-identity-file>"; exit 2; }
  [[ -f "$identity" ]] || { echo "age identity file not found: $identity"; exit 2; }
  command -v age >/dev/null 2>&1 || { echo "age is not installed (brew install age)"; exit 1; }

  # Check every file against the manifest before touching anything.
  local f want got
  for f in db.dump.age officekeys.tar.age env.age db-env.age; do
    [[ -f "$src/$f" ]] || { [[ "$f" == db-env.age ]] && continue; }
    want="$(tr ',' '\n' < "$src/manifest.json" | grep -A2 "\"name\":\"$f\"" | sed -n 's/.*"sha256":"\([0-9a-f]*\)".*/\1/p' | head -1)"
    got="$(sha "$src/$f")"
    [[ -n "$want" && "$want" == "$got" ]] || { log "RESTORE REFUSED: $f doesn't match the manifest"; exit 1; }
  done
  log "manifest checks out: $(cat "$src/manifest.json" | sed -n 's/.*"created_at":"\([^"]*\)".*/\1/p') from $(sed -n 's/.*"host":"\([^"]*\)".*/\1/p' "$src/manifest.json")"
  # Decrypting .env first proves the identity before anything is replaced.
  local envtmp
  envtmp="$(mktemp)"
  chmod 600 "$envtmp"
  age -d -i "$identity" -o "$envtmp" "$src/env.age" || { rm -f "$envtmp"; log "RESTORE REFUSED: this identity can't decrypt the backup"; exit 1; }

  if [[ "${WEHELP_RESTORE_YES:-}" != "1" ]]; then
    printf 'This replaces the database, office keys and .env of stack "%s". Type RESTORE to continue: ' "$PROJECT"
    local answer
    read -r answer
    [[ "$answer" == "RESTORE" ]] || { rm -f "$envtmp"; echo "cancelled"; exit 1; }
  fi

  local stamp
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  [[ -f "$ENV_FILE" ]] && cp -p "$ENV_FILE" "$ENV_FILE.before-restore-$stamp"
  mv "$envtmp" "$ENV_FILE"
  chmod 600 "$ENV_FILE"
  # Database passwords (#161): the backup's, or new ones for an older backup.
  [[ -f "$DB_ENV" ]] && cp -p "$DB_ENV" "$DB_ENV.before-restore-$stamp"
  if [[ -f "$src/db-env.age" ]]; then
    (umask 077; age -d -i "$identity" -o "$DB_ENV" "$src/db-env.age")
  else
    rm -f "$DB_ENV"
  fi

  compose stop server >/dev/null 2>&1 || true
  # Passwords and the runtime role first: db-roles creates db.env if needed,
  # and the dump grants to wehelp_app, so it must exist before pg_restore.
  /bin/bash "$DEPLOY_DIR/appliance.sh" db-roles
  compose up --no-start --build # creates the stack's volumes, so Compose owns them
  wait_for_db
  compose exec -T db psql -q -U wehelp -d postgres -v ON_ERROR_STOP=1 \
    -c "drop database if exists wehelp with (force)" -c "create database wehelp owner wehelp"
  age -d -i "$identity" "$src/db.dump.age" | compose exec -T db pg_restore -U wehelp -d wehelp --no-owner --exit-on-error
  log "database restored"
  age -d -i "$identity" "$src/officekeys.tar.age" |
    docker run --rm -i -v "${PROJECT}_officekeys:/k" alpine:3 sh -c 'find /k -mindepth 1 -delete && tar -C /k -xf - && chown -R 65532:65532 /k' # the server runs as nonroot (#161)
  log "office keys restored"

  compose up -d --build
  local waited=0
  until [[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$HEALTH_URL")" == 200 ]]; do
    (( waited >= 180 )) && { log "UNHEALTHY after restore: $HEALTH_URL"; exit 1; }
    sleep 5
    waited=$((waited + 5))
  done
  log "restore complete; healthy at $HEALTH_URL"
}

install_backup() {
  case "$(uname)" in
    Darwin) install_backup_launchd ;;
    Linux) install_backup_systemd ;;
    *) echo "install-backup is for macOS (launchd) or Linux (systemd)"; exit 1 ;;
  esac
}

# install_backup_systemd: a system service + timer (nightly 02:30) that runs
# the backup as the user who installed it, with that user's HOME. A system
# timer runs without anyone logged in, unlike a user timer. Needs sudo.
install_backup_systemd() {
  command -v systemctl >/dev/null 2>&1 || { echo "install-backup on Linux needs systemd"; exit 1; }
  local user="${SUDO_USER:-$(id -un)}" unit_dir="/etc/systemd/system" tmp sudo=""
  [[ "$user" == root ]] && { echo "run install-backup as the service user (with sudo access), not as root"; exit 1; }
  [[ "$(id -u)" -eq 0 ]] || sudo="sudo"
  tmp="$(mktemp)"
  sed -e "s#__SCRIPT__#$DEPLOY_DIR/appliance.sh#g" \
      -e "s#__REPO__#$REPO_DIR#g" \
      -e "s#__USER__#$user#g" \
      "$DEPLOY_DIR/linux/wehelp-backup.service.in" > "$tmp"
  $sudo install -m 644 "$tmp" "$unit_dir/wehelp-backup.service"
  rm -f "$tmp"
  $sudo install -m 644 "$DEPLOY_DIR/linux/wehelp-backup.timer" "$unit_dir/wehelp-backup.timer"
  $sudo systemctl daemon-reload
  $sudo systemctl enable --now wehelp-backup.timer >/dev/null
  log "installed wehelp-backup.timer (nightly at 02:30 as $user; log: journalctl -u wehelp-backup)"
}

install_backup_launchd() {
  local label="com.wehelp.backup" plist="$HOME/Library/LaunchAgents/com.wehelp.backup.plist"
  mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"
  sed -e "s#__SCRIPT__#$DEPLOY_DIR/appliance.sh#g" \
      -e "s#__REPO__#$REPO_DIR#g" \
      -e "s#__LOG__#$HOME/Library/Logs/wehelp-backup.log#g" \
      "$DEPLOY_DIR/macos/$label.plist.in" > "$plist"
  launchctl bootout "gui/$(id -u)/$label" 2>/dev/null || true
  launchctl bootstrap "gui/$(id -u)" "$plist"
  log "installed $plist (nightly at 02:30; log: ~/Library/Logs/wehelp-backup.log)"
}

case "${1:-}" in
  backup) backup ;;
  restore) shift; restore "$@" ;;
  install-backup) install_backup ;;
  *) sed -n '2,8p' "$0"; exit 2 ;;
esac
