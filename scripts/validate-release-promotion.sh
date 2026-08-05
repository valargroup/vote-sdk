#!/usr/bin/env bash
set -euo pipefail

tag="${1:-}"
held_tag="${2:-}"
latest_tag="${3:-}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

"${repo_root}/scripts/validate-release-channel-update.sh" "$tag" "$latest_tag" >/dev/null

if [ -z "$held_tag" ]; then
  echo "ERROR: RELEASE_HOLD_TAG is not set." >&2
  exit 1
fi

if [ "$tag" != "$held_tag" ]; then
  echo "ERROR: requested promotion ${tag} does not match RELEASE_HOLD_TAG ${held_tag}." >&2
  exit 1
fi

printf '%s\n' "$tag"
