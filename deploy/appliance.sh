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

compose() {
  docker compose -f "$DEPLOY_DIR/docker-compose.yml" -f "$DEPLOY_DIR/docker-compose.appliance.yml" "$@"
}

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
    compose run --rm --no-deps \
      -e WEHELP_INSTALL_TOKEN="${WEHELP_INSTALL_TOKEN:-}" \
      -e WEHELP_REGISTRY_URL="${WEHELP_REGISTRY_URL:-http://localhost:8090}" \
      server init
    ;;
  export-ca)
    # Staff browsers trust the staff HTTPS page through this CA (#38).
    # The server image is distroless (no shell), so read the volume with alpine.
    docker run --rm -v "${COMPOSE_PROJECT_NAME:-$(basename "$DEPLOY_DIR")}_officekeys:/k:ro" alpine:3 cat /k/ca.crt
    ;;
  up)
    init_env
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
  status)
    compose ps
    curl -s -m 3 -o /dev/null -w "healthz %{http_code}\n" "$HEALTH_URL" || true
    ;;
  *)
    sed -n '2,18p' "$0"
    exit 2
    ;;
esac
