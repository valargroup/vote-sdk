#!/usr/bin/env bash
set -euo pipefail

script_version="${1:-}"
do_base="${2:-}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_script="${repo_root}/scripts/disable-cosmovisor-backups.sh"
common_script="${repo_root}/scripts/_chain_upgrade_common.sh"

[ -n "$script_version" ] || { echo "ERROR: script version is required." >&2; exit 1; }
[ -n "$do_base" ] || { echo "ERROR: Spaces base URL is required." >&2; exit 1; }
printf '%s\n' "$script_version" | grep -Eq '^v[1-9][0-9]*$' \
  || { echo "ERROR: script version must match vN, for example v1." >&2; exit 1; }

if command -v sha256sum >/dev/null 2>&1; then
  common_sha256="$(sha256sum "$common_script" | awk '{print $1}')"
else
  common_sha256="$(shasum -a 256 "$common_script" | awk '{print $1}')"
fi
common_url="${do_base%/}/scripts/disable-cosmovisor-backups/${script_version}/_chain_upgrade_common.sh"

sed \
  -e "s|__COMMON_URL__|${common_url}|g" \
  -e "s|__COMMON_SHA256__|${common_sha256}|g" \
  "$source_script"
