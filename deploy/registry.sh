#!/usr/bin/env bash
# WeHelp Registry on an always-on host (#182). See docs/REGISTRY.md.
#
# Docker Compose project "wehelp-registry" (deploy/registry/docker-compose.yml):
# Postgres, Flyway and `wehelp-registry serve`. Plain TCP, so it runs in
# Colima on a Mac mini as well as on a Linux container host.
#
#   registry.sh up          create the DB password and signing key once, build,
#                           migrate, start
#   registry.sh down        stop and remove the containers (volumes kept)
#   registry.sh status      containers and /healthz
#   registry.sh logs        follow the server log
#   registry.sh cli ARGS…   run a wehelp-registry subcommand in the image, e.g.
#                           cli keygen | cli pubkey | cli audit verify
#                           (pipe input works: stdin is passed through)
#
# Settings for interpolation (optional), in deploy/registry/.env:
#   WEHELP_REGISTRY_PORT (8090), WEHELP_REGISTRY_BIND (0.0.0.0),
#   WEHELP_REGISTRY_DB_PORT (5433, always on 127.0.0.1).
set -euo pipefail
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REG_DIR="$DEPLOY_DIR/registry"
DB_ENV="$REG_DIR/db.env"
PORT=""
[[ -f "$REG_DIR/.env" ]] && PORT="$(sed -n 's/^WEHELP_REGISTRY_PORT=//p' "$REG_DIR/.env" | tail -1)"
PORT="${PORT:-${WEHELP_REGISTRY_PORT:-8090}}"

# render_registry_env_files (#161 pattern: no tracked file names a password).
source "$DEPLOY_DIR/compose.sh"

log() { printf '%s registry: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

# init_db_env creates deploy/registry/db.env (mode 600) once, with a random
# password for the Registry database owner. Nobody types it.
init_db_env() {
  if [[ -f "$DB_ENV" ]]; then
    chmod 600 "$DB_ENV"
    return
  fi
  (umask 077; printf 'WEHELP_REGISTRY_DB_PASSWORD=%s\n' "$(openssl rand -hex 24)" > "$DB_ENV")
  log "created $DB_ENV (mode 600); back it up with the registry volumes"
}

compose() {
  init_db_env
  render_registry_env_files "$REG_DIR"
  local envs=()
  [[ -f "$REG_DIR/.env" ]] && envs+=(--env-file "$REG_DIR/.env")
  docker compose ${envs[@]+"${envs[@]}"} -f "$REG_DIR/docker-compose.yml" "$@"
}

cmd="${1:-}"
case "$cmd" in
  up)
    compose up -d --build --wait db
    compose run --rm migrate
    compose build registry
    # The first start creates the signing key (keygen never replaces one) and
    # prints the public key that Relays and offices pin.
    if ! compose run --rm --no-deps -T registry pubkey >/dev/null 2>&1; then
      log "no signing key yet; creating one"
      compose run --rm --no-deps -T registry keygen
    fi
    compose up -d registry
    for _ in $(seq 1 30); do
      curl -fsS -m 3 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && { log "healthy on :$PORT"; exit 0; }
      sleep 2
    done
    log "not healthy on :$PORT"
    compose logs --tail 20 registry
    exit 1
    ;;
  down)
    compose down
    ;;
  status)
    compose ps
    curl -fsS -m 3 "http://127.0.0.1:$PORT/healthz" && echo || echo "healthz: no answer on :$PORT"
    ;;
  logs)
    compose logs -f --tail 50 registry
    ;;
  cli)
    shift
    flags=(--rm --no-deps)
    [[ -t 0 ]] || flags+=(-T)
    actor=()
    [[ " $* " == *" --actor"* ]] || actor=(--actor "cli:$(id -un)@$(hostname -s)")
    compose run "${flags[@]}" registry ${actor[@]+"${actor[@]}"} "$@"
    ;;
  *)
    sed -n '2,19p' "$0"
    exit 2
    ;;
esac
