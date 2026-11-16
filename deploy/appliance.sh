#!/usr/bin/env bash
# WeHelp Back Office appliance helper (macOS Mac mini with Colima, or any
# Docker host). See docs/APPLIANCE.md.
#
#   appliance.sh init-env         create deploy/.env with a random secret (if missing)
#   appliance.sh init-office      create the office keys; with WEHELP_INSTALL_TOKEN set,
#                                 also install with the Registry (WEHELP_REGISTRY_URL)
#   appliance.sh export-ca        print the office CA certificate (install on staff machines)
#   appliance.sh up               build and start the stack
#   appliance.sh down             stop and remove the containers (data volume kept)
#   appliance.sh boot             login-time job: wait for Docker, start, check health
#   appliance.sh stop-for-reboot  stop the stack and Colima cleanly before a reboot
#   appliance.sh install-login    install the login-time job (macOS launchd)
#   appliance.sh status           show containers and /healthz
#   appliance.sh doctor [--json]  go-live checks: host and Back Office, PASS/FAIL/SKIP (#81)
#   appliance.sh demo-setup       demo hosts only: demo defaults in .env, then up and seed
#   appliance.sh seed [training]  demo hosts only (WEHELP_DEMO=1): add the demo practice and logins,
#                                 or with "training" also a synthetic week for staff training
#   appliance.sh db-roles         set the database role passwords from deploy/db.env
#   appliance.sh backup           encrypted backup now (nightly job: install-backup)
#   appliance.sh restore DIR ID   restore a backup with an age identity (docs/BACKUPS.md)
#   appliance.sh install-backup   install the nightly backup job (macOS launchd, 02:30)
set -euo pipefail

# launchd starts jobs with a minimal PATH.
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$DEPLOY_DIR/.." && pwd)"
ENV_FILE="$DEPLOY_DIR/.env"
HEALTH_URL="${WEHELP_HEALTH_URL:-http://127.0.0.1:8080/healthz}"
LABEL="com.wehelp.appliance"

DB_ENV="$DEPLOY_DIR/db.env"
PROJECT="${COMPOSE_PROJECT_NAME:-$(basename "$DEPLOY_DIR")}"

# compose(): Docker Compose with the database passwords from db.env (#161).
source "$DEPLOY_DIR/compose.sh"

log() { printf '%s appliance: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

init_env() {
  if [[ -f "$ENV_FILE" ]]; then
    chmod 600 "$ENV_FILE"
    log "kept existing $ENV_FILE"
    return
  fi
  umask 077
  printf 'WEHELP_JWT_SECRET=%s\n' "$(openssl rand -hex 32)" > "$ENV_FILE"
  log "created $ENV_FILE (mode 600)"
}

# env_default adds KEY=VALUE to .env when KEY isn't set there yet.
env_default() {
  grep -q "^$1=" "$ENV_FILE" && return 0
  printf '%s=%s\n' "$1" "$2" >> "$ENV_FILE"
  log "set $1=$2 in $ENV_FILE"
}

# init_db_env creates deploy/db.env (mode 600) once, with a random password
# for the owner role (wehelp, used by migrations) and one for the server's
# runtime role (wehelp_app) (#161). Nobody types them.
init_db_env() {
  if [[ -f "$DB_ENV" ]]; then
    chmod 600 "$DB_ENV"
    return
  fi
  (umask 077; printf 'WEHELP_DB_PASSWORD=%s\nWEHELP_DB_APP_PASSWORD=%s\n' "$(openssl rand -hex 24)" "$(openssl rand -hex 24)" > "$DB_ENV")
  log "created $DB_ENV (mode 600)"
}

# db_roles makes the running database match db.env: the owner's password
# (an install from before #161 still has the old shared one), and wehelp_app
# with login. Idempotent. Passwords go over stdin, never on a command line.
db_roles() {
  local owner app waited=0
  owner="$(sed -n 's/^WEHELP_DB_PASSWORD=//p' "$DB_ENV")"
  app="$(sed -n 's/^WEHELP_DB_APP_PASSWORD=//p' "$DB_ENV")"
  [[ -n "$owner" && -n "$app" ]] || { log "$DB_ENV is missing a password"; return 1; }
  compose up -d db >/dev/null 2>&1
  until compose exec -T db pg_isready -U wehelp >/dev/null 2>&1; do
    (( waited >= 120 )) && { log "database not ready after 120s"; return 1; }
    sleep 2
    waited=$((waited + 2))
  done
  printf '%s\n' "\\set owner '$owner'" "\\set app '$app'" \
    "alter role wehelp password :'owner';" \
    "select format('create role wehelp_app login password %L', :'app') where not exists (select from pg_roles where rolname = 'wehelp_app') \\gexec" \
    "alter role wehelp_app login password :'app';" |
    compose exec -T db psql -q -U wehelp -d postgres -v ON_ERROR_STOP=1 >/dev/null
  log "database roles ready (wehelp, wehelp_app)"
}

# own_volumes gives the office keys volume to the image's nonroot user
# (uid 65532, #161); installs from before #161 wrote it as root.
own_volumes() {
  compose up --no-start --build >/dev/null 2>&1 || compose up --no-start --build
  docker run --rm -v "${PROJECT}_officekeys:/k" alpine:3 chown -R 65532:65532 /k
}

# prepare is everything a start needs, in order. Idempotent.
prepare() {
  init_env
  init_db_env
  own_volumes
  db_roles
}

wait_for_docker() {
  local limit="${1:-600}" waited=0 recovered=0
  until docker info >/dev/null 2>&1; do
    if (( waited >= limit )); then
      log "Docker not ready after ${limit}s"
      return 1
    fi
    # Give Colima's own login service a minute, then step in once.
    if (( waited >= 60 && recovered == 0 )); then
      recover_colima
      recovered=1
    fi
    sleep 5
    waited=$((waited + 5))
  done
  log "Docker ready after ${waited}s"
}

# recover_colima starts Colima when it isn't running, first clearing Lima
# state left over from before a reboot. Lima keeps the VM's process ID in
# vz.pid; after a reboot macOS can hand that ID to an unrelated process, and
# Lima then refuses to start ("vz driver is running but host agent is not").
# Seen on Barnacle after a macOS update on 2026-11-14. A live vz.pid points
# at `limactl hostagent`, so anything else means the file is stale.
recover_colima() {
  command -v colima >/dev/null 2>&1 || return 0
  colima status >/dev/null 2>&1 && return 0
  local dir="$HOME/.colima/_lima/colima" pid=""
  if [[ -f "$dir/vz.pid" ]]; then
    pid="$(cat "$dir/vz.pid" 2>/dev/null || true)"
    if [[ -z "$pid" ]] || ! ps -p "$pid" -o args= 2>/dev/null | grep -q "limactl"; then
      log "removing stale Lima state (vz.pid ${pid:-empty} is not a Lima process)"
      rm -f "$dir/vz.pid" "$dir/ha.sock"
    fi
  fi
  log "Colima is not running; starting it"
  if brew services list 2>/dev/null | grep -q '^colima '; then
    brew services restart colima >/dev/null 2>&1 || log "brew services restart colima failed"
  else
    colima start >/dev/null 2>&1 || log "colima start failed"
  fi
}

wait_for_health() {
  local limit="${1:-300}" waited=0
  until [[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$HEALTH_URL")" == 200 ]]; do
    if (( waited >= limit )); then
      log "UNHEALTHY: $HEALTH_URL not 200 after ${limit}s"
      return 1
    fi
    sleep 5
    waited=$((waited + 5))
  done
  log "healthy: $HEALTH_URL after ${waited}s"
}

cmd="${1:-}"
case "$cmd" in
  init-env)
    init_env
    ;;
  init-office)
    # Runs `wehelpd init` in the server image against the officekeys volume.
    # The token comes from the environment (see docs/OFFICE_KEYS.md: read -rs).
    init_env
    init_db_env
    own_volumes
    # -e with a name only: Compose takes the value from this environment, so
    # the token never appears in a command line (ps) (#182).
    export WEHELP_INSTALL_TOKEN="${WEHELP_INSTALL_TOKEN:-}"
    compose run --rm --no-deps \
      -e WEHELP_INSTALL_TOKEN \
      -e WEHELP_REGISTRY_URL="${WEHELP_REGISTRY_URL:-http://localhost:8090}" \
      server init
    ;;
  export-ca)
    # Staff browsers trust the staff HTTPS page through this CA (#38).
    # The server image is distroless (no shell), so read the volume with alpine.
    docker run --rm -v "${PROJECT}_officekeys:/k:ro" alpine:3 cat /k/ca.crt
    ;;
  up)
    prepare
    compose up -d --build
    wait_for_health 180
    ;;
  down)
    compose down
    ;;
  boot)
    # `up -d` is idempotent. It also starts containers that Docker restored in
    # the "created" state after an abrupt VM shutdown, which restart policies
    # alone do not (seen on Barnacle after an OS-update reboot).
    log "boot: starting"
    wait_for_docker 600
    [[ -f "$ENV_FILE" ]] || { log "missing $ENV_FILE; run 'appliance.sh init-env'"; exit 1; }
    prepare
    compose up -d
    wait_for_health 300
    ;;
  stop-for-reboot)
    compose stop
    if command -v colima >/dev/null 2>&1; then
      colima stop
    fi
    log "stopped cleanly; safe to reboot or update macOS"
    ;;
  install-login)
    [[ "$(uname)" == "Darwin" ]] || { echo "install-login is for macOS (launchd)"; exit 1; }
    plist="$HOME/Library/LaunchAgents/$LABEL.plist"
    mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"
    sed -e "s#__SCRIPT__#$DEPLOY_DIR/appliance.sh#g" \
        -e "s#__REPO__#$REPO_DIR#g" \
        -e "s#__LOG__#$HOME/Library/Logs/wehelp-appliance.log#g" \
        "$DEPLOY_DIR/macos/$LABEL.plist.in" > "$plist"
    launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
    launchctl bootstrap "gui/$(id -u)" "$plist"
    log "installed $plist (runs 'appliance.sh boot' at login; log: ~/Library/Logs/wehelp-appliance.log)"
    ;;
  backup|restore|install-backup)
    # Encrypted backups and restore (#41): deploy/backup.sh.
    exec /bin/bash "$DEPLOY_DIR/backup.sh" "$@"
    ;;
  db-roles)
    # Used by restore (deploy/backup.sh) before pg_restore.
    init_db_env
    db_roles
    ;;
  demo-setup)
    # One step for a demo host: fill in demo defaults in .env (only keys that
    # are missing; existing values are kept), build with the demo tag, start,
    # and seed the demo practice. Never run this on a practice's Back Office.
    init_env
    env_default WEHELP_DEMO 1
    host="$(hostname -s | tr '[:upper:]' '[:lower:]')"
    ip="$(ipconfig getifaddr en0 2>/dev/null || true)"
    env_default WEHELP_STAFF_HOSTS "$host.local,localhost${ip:+,$ip}"
    tz="$(readlink /etc/localtime 2>/dev/null | sed -n 's#.*/zoneinfo/##p')"
    [[ -n "$tz" ]] && env_default WEHELP_TIMEZONE "$tz"
    /bin/bash "$0" up
    /bin/bash "$0" seed
    log "demo host ready: https://$host.local:${WEHELP_HTTPS_PORT:-8443} (demo logins are on the sign-in page)"
    ;;
  seed)
    # Demo hosts only (WEHELP_DEMO=1 in .env): fixed demo logins, no real data.
    grep -q '^WEHELP_DEMO=1' "$ENV_FILE" 2>/dev/null ||
      { echo "seed is for demo hosts: set WEHELP_DEMO=1 in $ENV_FILE, then appliance.sh up"; exit 1; }
    # "seed training" adds the synthetic training week (#80).
    compose run --rm --no-deps -e WEHELP_ENV=demo server seed --profile "${2:-default}"
    ;;
  doctor)
    # Host checks the container can't see, as "name|STATUS|detail" lines,
    # then the Back Office's own checks in one report (#81).
    host=""
    add() { host+="$1|$2|$3"$'\n'; }
    if [[ "$(uname)" == Darwin ]]; then
      if fdesetup isactive >/dev/null 2>&1; then add filevault PASS "on"
      else add filevault FAIL "FileVault is off: turn it on in System Settings"; fi
      if defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser >/dev/null 2>&1; then
        add auto_login FAIL "automatic login is on: turn it off in System Settings > Users & Groups"
      else add auto_login PASS "off"; fi
    else
      add filevault SKIP "not macOS: check full-disk encryption by hand"
      add auto_login SKIP "not macOS: check automatic login by hand"
    fi
    db_id="$(compose ps -q db 2>/dev/null || true)"
    if [[ -z "$db_id" ]]; then
      add postgres_loopback FAIL "the db container isn't running"
    else
      # One "ip=<HostIp>" per binding; an empty HostIp means every interface.
      binds="$(docker inspect "$db_id" --format '{{range $p, $b := .HostConfig.PortBindings}}{{range $b}}ip={{.HostIp}} {{end}}{{end}}')"
      bad="$(printf '%s\n' $binds | grep -vx 'ip=127.0.0.1' | sed 's/^ip=$/ip=all-interfaces/; s/^ip=//' || true)"
      if [[ -z "${binds// /}" ]]; then add postgres_loopback PASS "not published"
      elif [[ -z "$bad" ]]; then add postgres_loopback PASS "127.0.0.1 only"
      else add postgres_loopback FAIL "published on ${bad//$'\n'/ } (want 127.0.0.1 only)"; fi
    fi
    shift
    compose exec -T -e WEHELP_DOCTOR_HOST="$host" server wehelpd doctor "$@"
    ;;
  status)
    compose ps
    curl -s -m 3 -o /dev/null -w "healthz %{http_code}\n" "$HEALTH_URL" || true
    ;;
  *)
    sed -n '2,22p' "$0"
    exit 2
    ;;
esac
