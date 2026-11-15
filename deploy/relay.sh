#!/usr/bin/env bash
# WeHelp Relay on an always-on host (#180). See docs/RELAY.md.
#
# One Relay folder, WEHELP_RELAY_HOME (default ~/.wehelp/relay), holds the
# keys, relay.env, list cache, sites and logs. Relative paths in relay.env
# (for example WEHELP_RELAY_APNS_KEY=apns.p8) are inside that folder, so the
# folder moves between hosts as is.
#
#   Mac mini (launchd; Docker on macOS can't carry TURN's UDP):
#     relay.sh build-mac        build bin/wehelp-relay for this Mac in a Go container
#     relay.sh install-launchd  install and start the always-on job (re-run after
#                               editing relay.env or rebuilding)
#     relay.sh uninstall-launchd
#   Linux container host (Docker, host networking):
#     relay.sh up | down        build and start, or stop, deploy/relay/docker-compose.yml
#   Either:
#     relay.sh init             create the Relay's keys (never replaces one); prints the TLS pin
#     relay.sh status           is it running and listening
#     relay.sh logs             follow today's log
set -euo pipefail
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$DEPLOY_DIR/.." && pwd)"
RELAY_HOME="${WEHELP_RELAY_HOME:-$HOME/.wehelp/relay}"
RELAY_ENV="$RELAY_HOME/relay.env"
BINARY="$REPO_DIR/bin/wehelp-relay"
LABEL="com.wehelp.relay"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
GO_IMAGE="golang:1.26.11-alpine" # the toolchain go.mod names (#43)

log() { printf '%s relay: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
need_env() { [[ -f "$RELAY_ENV" ]] || { echo "missing $RELAY_ENV (see docs/RELAY.md)"; exit 1; }; }

compose() {
  WEHELP_RELAY_HOME="$RELAY_HOME" docker compose -f "$DEPLOY_DIR/relay/docker-compose.yml" "$@"
}

# xml_escape keeps relay.env values literal inside the plist.
xml_escape() { sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'; }

# plist_env turns relay.env (KEY=value lines; # comments) into plist entries.
plist_env() {
  printf '\t\t<key>WEHELP_RELAY_DIR</key>\n\t\t<string>%s</string>\n' "$(printf '%s' "$RELAY_HOME" | xml_escape)"
  local line key val
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ "$line" =~ ^[[:space:]]*(#|$) ]] && continue
    key="${line%%=*}"
    val="${line#*=}"
    [[ "$key" == WEHELP_RELAY_DIR ]] && continue
    printf '\t\t<key>%s</key>\n\t\t<string>%s</string>\n' "$key" "$(printf '%s' "$val" | xml_escape)"
  done < "$RELAY_ENV"
}

cmd="${1:-}"
case "$cmd" in
  build-mac)
    [[ "$(uname)" == Darwin ]] || { echo "build-mac is for macOS"; exit 1; }
    arch="$(uname -m)"
    [[ "$arch" == x86_64 ]] && arch=amd64
    mkdir -p "$REPO_DIR/bin"
    docker run --rm -v "$REPO_DIR:/src" -v wehelp-gomod:/go/pkg/mod -w /src \
      -e CGO_ENABLED=0 -e GOOS=darwin -e GOARCH="$arch" "$GO_IMAGE" \
      go build -trimpath -o bin/wehelp-relay ./cmd/wehelp-relay
    log "built $BINARY (darwin/$arch)"
    ;;
  init)
    mkdir -p "$RELAY_HOME"
    chmod 700 "$RELAY_HOME"
    if [[ "$(uname)" == Darwin ]]; then
      WEHELP_RELAY_DIR="$RELAY_HOME" "$BINARY" init
    else
      compose run --rm --no-deps relay init
    fi
    ;;
  install-launchd)
    [[ "$(uname)" == Darwin ]] || { echo "install-launchd is for macOS; use 'relay.sh up' on Linux"; exit 1; }
    need_env
    [[ -x "$BINARY" ]] || { echo "missing $BINARY: run 'relay.sh build-mac'"; exit 1; }
    chmod 700 "$RELAY_HOME"
    chmod 600 "$RELAY_ENV"
    mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"
    envfile="$(mktemp)"
    plist_env > "$envfile"
    (umask 077
     sed -e "s#__BINARY__#$BINARY#g" -e "s#__HOME__#$RELAY_HOME#g" \
         -e "s#__LOG__#$HOME/Library/Logs/wehelp-relay.log#g" \
         -e "/__ENV__/{r $envfile" -e 'd;}' \
         "$DEPLOY_DIR/macos/$LABEL.plist.in" > "$PLIST")
    rm -f "$envfile"
    plutil -lint -s "$PLIST"
    launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
    launchctl bootstrap "gui/$(id -u)" "$PLIST"
    log "installed $PLIST; the Relay runs at login and restarts if it stops"
    log "first run on macOS 15+: approve Local Network access for wehelp-relay on this Mac's screen"
    ;;
  uninstall-launchd)
    launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
    rm -f "$PLIST"
    log "removed $LABEL"
    ;;
  up)
    [[ "$(uname)" == Linux ]] || { echo "up is for a Linux container host; on a Mac use install-launchd"; exit 1; }
    need_env
    chown -R 65532:65532 "$RELAY_HOME" 2>/dev/null || sudo chown -R 65532:65532 "$RELAY_HOME"
    compose up -d --build
    ;;
  down)
    compose down
    ;;
  status)
    if [[ "$(uname)" == Darwin ]]; then
      launchctl print "gui/$(id -u)/$LABEL" 2>/dev/null | grep -E '^\s+(state|pid|last exit code) ' || echo "$LABEL not loaded"
      lsof -nP -a -c wehelp-re -iTCP -sTCP:LISTEN 2>/dev/null | awk 'NR>1 {print "listening", $9}' | sort -u
      lsof -nP -a -c wehelp-re -iUDP 2>/dev/null | awk 'NR>1 {print "udp", $9}' | sort -u | head -3
    else
      compose ps
    fi
    ;;
  logs)
    f="$RELAY_HOME/logs/relay-$(date -u +%Y-%m-%d).log"
    [[ -f "$f" ]] || f="$(ls -t "$RELAY_HOME"/logs/relay-*.log 2>/dev/null | head -1)"
    [[ -n "$f" ]] || { echo "no logs in $RELAY_HOME/logs"; exit 1; }
    tail -n 50 -f "$f"
    ;;
  *)
    sed -n '2,22p' "$0"
    exit 2
    ;;
esac
