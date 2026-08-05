#!/usr/bin/env bash
set -euo pipefail

tag="${1:-}"
held_tag="${2:-}"
latest_tag="${3:-}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

is_newer_stable_tag() {
  local lhs="${1#v}"
  local rhs="${2#v}"
  local -a lhs_parts rhs_parts
  local index lhs_value rhs_value

  IFS=. read -r -a lhs_parts <<<"$lhs"
  IFS=. read -r -a rhs_parts <<<"$rhs"
  for index in 0 1 2; do
    lhs_value=$((10#${lhs_parts[$index]}))
    rhs_value=$((10#${rhs_parts[$index]}))
    if (( lhs_value > rhs_value )); then
      return 0
    fi
    if (( lhs_value < rhs_value )); then
      return 1
    fi
  done
  return 1
}

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

if [ -n "$latest_tag" ]; then
  if ! latest_channel="$("${repo_root}/scripts/release-channel.sh" "$latest_tag")" \
    || [ "$latest_channel" != "stable" ]; then
    echo "ERROR: current Latest is not a stable vN.N.N release: ${latest_tag}." >&2
    exit 1
  fi
  if is_newer_stable_tag "$latest_tag" "$tag"; then
    echo "ERROR: refusing to replace newer Latest ${latest_tag} with ${tag}." >&2
    exit 1
  fi
fi

printf '%s\n' "$tag"
