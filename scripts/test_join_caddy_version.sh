#!/usr/bin/env bash
# Exercise the installer version gate without running the destructive join flow.
# Variables and the Caddy stub are consumed by the extracted installer code.
# shellcheck disable=SC2034,SC2329
set -euo pipefail

JOIN_SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/join.sh"
# shellcheck disable=SC1090
source <(sed '/^main() {$/,$d' "$JOIN_SCRIPT")
eval "$(sed -n '/^handle_public_url_failure() {$/,/^}$/p' "$JOIN_SCRIPT")"
VERSION_GATE=$(awk '/^# Sanity-check the caddy binary/ { gate=1 } gate { print } gate && /^fi$/ { exit }' "$JOIN_SCRIPT")

fail() { echo "FAIL: $*" >&2; exit 1; }

for version in 'v2.11.7' 'v2.11.7 h1:checksum=' 'v2.11.7+build.1' 'v2.11.10' 'v2.12.0'; do
  caddy_version_supported "$version" || fail "rejected $version"
done
for version in 'v2.6.2' 'v2.10.2' 'v2.11.4' 'v2.11.5' 'v2.11.6' 'v2.11.7-rc.1' 'v2.12.0-beta.1' '(devel)' '' '2.11.7' 'v2.11'; do
  if caddy_version_supported "$version"; then fail "accepted $version"; fi
done

caddy() { printf '%s\n' "$TEST_CADDY_VERSION"; return "${TEST_CADDY_STATUS:-0}"; }

for mode in explicit auto; do
  for version in v2.11.4 v2.11.7; do
    output_file=$(mktemp)
    if (
      DOMAIN_MODE="$mode"
      SVOTE_ALLOW_NO_PUBLIC_URL=0
      VALIDATOR_URL=https://validator.example.org
      TEST_CADDY_VERSION="$version"
      eval "$VERSION_GATE"
      if [ "$version" = v2.11.7 ]; then
        [ "$VALIDATOR_URL" = https://validator.example.org ]
      else
        [ -z "$VALIDATOR_URL" ]
      fi
    ) > "$output_file" 2>&1; then
      [ "$mode/$version" != explicit/v2.11.4 ] || fail 'old explicit proxy accepted'
    else
      [ "$mode/$version" = explicit/v2.11.4 ] || { cat "$output_file"; fail "$mode/$version"; }
    fi
    if [ "$version" = v2.11.4 ]; then
      grep -q 'update-caddy.md' "$output_file" || fail 'missing upgrade guidance'
    else
      grep -q 'Caddy ready:' "$output_file" || fail 'supported binary not ready'
    fi
    rm -f "$output_file"
  done
done

(
  DOMAIN_MODE=explicit
  SVOTE_ALLOW_NO_PUBLIC_URL=1
  VALIDATOR_URL=https://validator.example.org
  TEST_CADDY_VERSION=v2.11.4
  eval "$VERSION_GATE"
  [ -z "$VALIDATOR_URL" ] || fail 'opt-out advertised old proxy'
) >/dev/null

(
  DOMAIN_MODE=auto
  VALIDATOR_URL=https://validator.example.org
  TEST_CADDY_VERSION=v2.11.7
  TEST_CADDY_STATUS=1
  eval "$VERSION_GATE"
  [ -z "$VALIDATOR_URL" ] || fail 'broken binary accepted'
) >/dev/null

(
  VALIDATOR_URL=''
  caddy() { fail 'skip mode invoked Caddy'; }
  eval "$VERSION_GATE"
)

echo 'PASS: Caddy version floor and public URL failure paths'
