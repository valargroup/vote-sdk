#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
do_base="${2:-}"
common_url="${3:-}"
updater_url="${4:-}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

[ -n "$version" ] || { echo "ERROR: release version is required." >&2; exit 1; }
[ -n "$do_base" ] || { echo "ERROR: Spaces base URL is required." >&2; exit 1; }
[ -n "$common_url" ] || { echo "ERROR: common library URL is required." >&2; exit 1; }
[ -n "$updater_url" ] || { echo "ERROR: updater URL is required." >&2; exit 1; }

sed \
  -e "s|__RELEASE_TAG__|${version}|g" \
  -e "s|__GITHUB_REPO__|valargroup/vote-sdk|g" \
  -e "s|__DO_BASE__|${do_base}|g" \
  -e "s|__COMMON_URL__|${common_url}|g" \
  -e "s|__UPDATER_URL__|${updater_url}|g" \
  "${repo_root}/scripts/update_chain.sh.template"
