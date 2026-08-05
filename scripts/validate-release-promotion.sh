#!/usr/bin/env bash
set -euo pipefail

tag="${1:-}"
held_tag="${2:-}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

channel="$("${repo_root}/scripts/release-channel.sh" "$tag")"
if [ "$channel" != "stable" ]; then
  echo "ERROR: only a stable vN.N.N release can be promoted: ${tag:-<empty>}" >&2
  exit 1
fi

if [ -z "$held_tag" ]; then
  echo "ERROR: RELEASE_HOLD_TAG is not set." >&2
  exit 1
fi

if [ "$tag" != "$held_tag" ]; then
  echo "ERROR: requested promotion ${tag} does not match RELEASE_HOLD_TAG ${held_tag}." >&2
  exit 1
fi

printf '%s\n' "$tag"
