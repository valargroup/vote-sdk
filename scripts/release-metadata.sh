#!/usr/bin/env bash
set -euo pipefail

tag="${1:-}"
held_tag="${2:-}"
latest_tag="${3:-}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

channel="$("${repo_root}/scripts/release-channel.sh" "$tag")"
already_latest=false
if [ "$channel" = "stable" ] && [ -n "$latest_tag" ] && [ "$tag" = "$latest_tag" ]; then
  already_latest=true
fi

if [ "$channel" = "rc" ]; then
  prerelease=true
  make_latest=false
  publish_mutable_pointers=false
elif [ -n "$held_tag" ] && [ "$tag" = "$held_tag" ] && [ "$already_latest" = false ]; then
  echo "Holding coordinated release ${tag} from mutable release channels." >&2
  prerelease=false
  make_latest=false
  publish_mutable_pointers=false
else
  prerelease=false
  make_latest=true
  publish_mutable_pointers=true
fi

printf 'prerelease=%s\n' "$prerelease"
printf 'make_latest=%s\n' "$make_latest"
printf 'already_latest=%s\n' "$already_latest"
printf 'publish_mutable_pointers=%s\n' "$publish_mutable_pointers"
