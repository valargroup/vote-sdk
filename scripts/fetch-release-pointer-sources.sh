#!/usr/bin/env bash
set -euo pipefail

tag="${1:-}"
spaces_base="${2:-}"
destination="${3:-}"
curl_bin="${CURL_BIN:-curl}"

[ -n "$tag" ] || { echo "ERROR: release tag is required." >&2; exit 1; }
[ -n "$spaces_base" ] || { echo "ERROR: Spaces base URL is required." >&2; exit 1; }
[ -n "$destination" ] || { echo "ERROR: destination directory is required." >&2; exit 1; }

spaces_base="${spaces_base%/}"
mkdir -p "$destination"

fetch() {
  "$curl_bin" -fsSL --retry 3 --retry-delay 2 \
    "${spaces_base}/$1" \
    -o "${destination}/$2"
}

fetch "scripts/join/${tag}/join.sh" join.sh
fetch "scripts/join-full/${tag}/join-full.sh" join-full.sh
fetch "scripts/join-common/${tag}/_join_common.sh" _join_common.sh
fetch \
  "scripts/reset-validator-snapshot/${tag}/reset-validator-snapshot.sh" \
  reset-validator-snapshot.sh
fetch "scripts/remove-validator/${tag}/remove-validator.sh" remove-validator.sh
fetch "scripts/remove-pir/${tag}/remove-pir.sh" remove-pir.sh
fetch "scripts/svoted-wrapper/${tag}/svoted-wrapper.sh" svoted-wrapper.sh
fetch "scripts/upgrade/${tag}/update_chain.sh" update_chain.sh
fetch \
  "scripts/upgrade/${tag}/_chain_upgrade_common.sh" \
  _chain_upgrade_common.sh
fetch \
  "scripts/upgrade/${tag}/prepare-upgrade-artifacts.sh" \
  prepare-upgrade-artifacts.sh
