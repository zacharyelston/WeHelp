#!/usr/bin/env bash
# WeHelp Back Office appliance helper (macOS Mac mini with Colima, or any
# Docker host). See docs/APPLIANCE.md.
#
#   appliance.sh init-env         create deploy/.env with a random secret (if missing)
#   appliance.sh up               build and start the stack
#   appliance.sh down             stop and remove the containers (data volume kept)
#   appliance.sh boot             login-time job: wait for Docker, start, check health
#   appliance.sh stop-for-reboot  stop the stack and Colima cleanly before a reboot
#   appliance.sh install-login    install the login-time job (macOS launchd)
#   appliance.sh status           show containers and /healthz
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
  local limit="${1:-600}" waited=0
  until docker info >/dev/null 2>&1; do
    if (( waited >= limit )); then
      log "Docker not ready after ${limit}s"
      return 1
    fi
    sleep 5
    waited=$((waited + 5))
  done
  log "Docker ready after ${waited}s"
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
  status)
    compose ps
    curl -s -m 3 -o /dev/null -w "healthz %{http_code}\n" "$HEALTH_URL" || true
    ;;
  *)
    sed -n '2,12p' "$0"
    exit 2
    ;;
esac
