#!/usr/bin/env bash
set -euo pipefail

tag="${1:-}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"${script_dir}/release-channel.sh" "$tag" >/dev/null
if [[ "$tag" =~ ^v([0-9]+)\.([0-9]+)\.[0-9]+(-rc\.[0-9]+)?$ ]]; then
  printf 'v%s.%s.x\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
  exit 0
fi

echo "ERROR: could not derive a maintenance branch from ${tag:-<empty>}" >&2
exit 1
