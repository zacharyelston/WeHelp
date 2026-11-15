#!/usr/bin/env bash
# Release hygiene for the iOS app (#43). Run on a Release build:
#
#   scripts/check-ios-release.sh path/to/WeHelp.app [path/to/WeHelp.xcodeproj]
#
# Fails if the app could reach anything but the pinned Relay/office path, or
# carries demo accounts, demo servers or debug-only code:
#   - Info.plist: no NSAllowsArbitraryLoads (or in web content/media), no
#     NSAllowsLocalNetworking, exactly one ATS exception domain
#     (wehelp.invalid, TLS 1.3), no file sharing;
#   - the app binaries: no demo accounts or passwords, no LAN or localhost
#     addresses, no Debug dylib, and none of the strings only DEBUG builds
#     contain;
#   - with the project given: the Release configuration doesn't define DEBUG
#     and doesn't enable testability.
set -euo pipefail

app="${1:?usage: check-ios-release.sh WeHelp.app [WeHelp.xcodeproj]}"
project="${2:-}"
plist="$app/Info.plist"
bin="$app/$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$plist")"
fail=0
bad() { echo "RELEASE CHECK FAILED: $*"; fail=1; }

json="$(plutil -convert json -o - "$plist")"
has() { python3 -c "import json,sys; d=json.loads(sys.argv[1]); print(eval(sys.argv[2]))" "$json" "$1"; }

ats='d.get("NSAppTransportSecurity", {})'
for key in NSAllowsArbitraryLoads NSAllowsArbitraryLoadsInWebContent NSAllowsArbitraryLoadsForMedia NSAllowsLocalNetworking; do
  [[ "$(has "$ats.get('$key', False)")" == "False" ]] || bad "Info.plist sets $key"
done
[[ "$(has "sorted($ats.get('NSExceptionDomains', {}).keys())")" == "['wehelp.invalid']" ]] ||
  bad "ATS exception domains are $(has "sorted($ats.get('NSExceptionDomains', {}).keys())"), want only wehelp.invalid"
[[ "$(has "$ats.get('NSExceptionDomains', {}).get('wehelp.invalid', {}).get('NSExceptionMinimumTLSVersion')")" == "TLSv1.3" ]] ||
  bad "wehelp.invalid doesn't require TLS 1.3"
for key in UIFileSharingEnabled LSSupportsOpeningDocumentsInPlace; do
  [[ "$(has "d.get('$key', False)")" == "False" ]] || bad "Info.plist sets $key"
done

# Debug builds put the app's code in <name>.debug.dylib; Release never has one.
for d in "$app"/*.debug.dylib; do
  [[ -e "$d" ]] && bad "$(basename "$d") is a Debug build"
done
# Strings that must never ship, from the executable and any dylib beside it.
# "sandbox" exists only in DEBUG builds (Push.environment; Release says
# "production").
strings_file="$(mktemp)"
for f in "$bin" "$app"/*.dylib; do [[ -f "$f" ]] && strings -a "$f"; done > "$strings_file" || true
for s in 'wehelp-demo' '@demo.wehelp' 'Demo Clinic' '192.168.' '127.0.0.1' 'localhost:'; do
  if grep -qF -- "$s" "$strings_file"; then bad "the binary contains \"$s\""; fi
done
grep -qw 'sandbox' "$strings_file" && bad "the binary contains the DEBUG-only push environment \"sandbox\""
rm -f "$strings_file"

if [[ -n "$project" ]]; then
  settings="$(xcodebuild -project "$project" -scheme WeHelp -configuration Release -showBuildSettings 2>/dev/null)"
  grep -E '^ *SWIFT_ACTIVE_COMPILATION_CONDITIONS = ' <<<"$settings" | grep -qw DEBUG && bad "Release defines DEBUG"
  grep -E '^ *ENABLE_TESTABILITY = YES' <<<"$settings" >/dev/null && bad "Release enables testability"
fi

if (( fail )); then exit 1; fi
echo "release check ok: $app"
