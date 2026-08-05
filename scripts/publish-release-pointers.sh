#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
s3_config="${2:-}"
bucket="${DO_SPACES_BUCKET:-shielded-vote}"
region="${DO_SPACES_REGION:-nyc3}"
do_base="${DO_SPACES_BASE:-https://${bucket}.${region}.digitaloceanspaces.com}"
s3cmd_bin="${S3CMD_BIN:-s3cmd}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

[ -n "$version" ] || { echo "ERROR: release version is required." >&2; exit 1; }
[ -n "$s3_config" ] || { echo "ERROR: s3cmd config path is required." >&2; exit 1; }

channel="$("${repo_root}/scripts/release-channel.sh" "$version")"
if [ "$channel" != "stable" ]; then
  echo "Skipping mutable release pointers for ${version}."
  exit 0
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
printf '%s\n' "$version" > "${tmp_dir}/version.txt"
"${repo_root}/scripts/render-update-chain.sh" \
  "$version" \
  "$do_base" \
  "${do_base%/}/scripts/upgrade/${version}/_chain_upgrade_common.sh" \
  "${do_base%/}/update_chain.sh" \
  > "${tmp_dir}/update_chain.sh"

put() {
  "$s3cmd_bin" -c "$s3_config" put "$1" "s3://${bucket}/$2" --acl-public --force
}

put "${repo_root}/join.sh" "join.sh"
put "${repo_root}/join-full.sh" "join-full.sh"
put "${repo_root}/scripts/_join_common.sh" "scripts/_join_common.sh"
put "${repo_root}/scripts/reset-validator-snapshot.sh" "reset-validator-snapshot.sh"
put "${repo_root}/scripts/remove-validator.sh" "remove-validator.sh"
put "${repo_root}/scripts/remove-pir.sh" "remove-pir.sh"
put "${repo_root}/scripts/svoted-wrapper.sh" "svoted-wrapper.sh"
put "${tmp_dir}/update_chain.sh" "update_chain.sh"
put "${repo_root}/scripts/_chain_upgrade_common.sh" "scripts/_chain_upgrade_common.sh"
put "${repo_root}/scripts/prepare-upgrade-artifacts.sh" "prepare-upgrade-artifacts.sh"
put "${tmp_dir}/version.txt" "version.txt"
