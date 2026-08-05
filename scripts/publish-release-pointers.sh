#!/usr/bin/env bash
# Usage: publish-release-pointers.sh <version> <s3_config> [pointer_source_dir]
# pointer_source_dir contains immutable tag-scoped scripts for trusted promotion.
set -euo pipefail

version="${1:-}"
s3_config="${2:-}"
pointer_source_dir="${3:-}"
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

if [ -n "$pointer_source_dir" ]; then
  pointer_source_dir="${pointer_source_dir%/}"
  join_script="${pointer_source_dir}/join.sh"
  join_full_script="${pointer_source_dir}/join-full.sh"
  join_common_script="${pointer_source_dir}/_join_common.sh"
  reset_validator_script="${pointer_source_dir}/reset-validator-snapshot.sh"
  remove_validator_script="${pointer_source_dir}/remove-validator.sh"
  remove_pir_script="${pointer_source_dir}/remove-pir.sh"
  wrapper_script="${pointer_source_dir}/svoted-wrapper.sh"
  tagged_update_script="${pointer_source_dir}/update_chain.sh"
  chain_common_script="${pointer_source_dir}/_chain_upgrade_common.sh"
  prepare_upgrade_script="${pointer_source_dir}/prepare-upgrade-artifacts.sh"
else
  join_script="${repo_root}/join.sh"
  join_full_script="${repo_root}/join-full.sh"
  join_common_script="${repo_root}/scripts/_join_common.sh"
  reset_validator_script="${repo_root}/scripts/reset-validator-snapshot.sh"
  remove_validator_script="${repo_root}/scripts/remove-validator.sh"
  remove_pir_script="${repo_root}/scripts/remove-pir.sh"
  wrapper_script="${repo_root}/scripts/svoted-wrapper.sh"
  tagged_update_script=""
  chain_common_script="${repo_root}/scripts/_chain_upgrade_common.sh"
  prepare_upgrade_script="${repo_root}/scripts/prepare-upgrade-artifacts.sh"
fi

for source_script in \
  "$join_script" \
  "$join_full_script" \
  "$join_common_script" \
  "$reset_validator_script" \
  "$remove_validator_script" \
  "$remove_pir_script" \
  "$wrapper_script" \
  "$chain_common_script" \
  "$prepare_upgrade_script"
do
  [ -f "$source_script" ] \
    || { echo "ERROR: release pointer source is missing: ${source_script}" >&2; exit 1; }
  bash -n "$source_script"
done
if [ -n "$tagged_update_script" ]; then
  [ -f "$tagged_update_script" ] \
    || { echo "ERROR: release pointer source is missing: ${tagged_update_script}" >&2; exit 1; }
  bash -n "$tagged_update_script"
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
printf '%s\n' "$version" > "${tmp_dir}/version.txt"
if [ -n "$tagged_update_script" ]; then
  grep -Fq "readonly UPDATE_DEFAULT_RELEASE_TAG='${version}'" "$tagged_update_script" \
    || { echo "ERROR: tagged updater does not target ${version}." >&2; exit 1; }
  sed \
    "s|^readonly UPDATE_DEFAULT_UPDATER_URL=.*|readonly UPDATE_DEFAULT_UPDATER_URL='${do_base%/}/update_chain.sh'|" \
    "$tagged_update_script" \
    > "${tmp_dir}/update_chain.sh"
  grep -Fq "readonly UPDATE_DEFAULT_UPDATER_URL='${do_base%/}/update_chain.sh'" \
    "${tmp_dir}/update_chain.sh" \
    || { echo "ERROR: tagged updater has no mutable updater URL field." >&2; exit 1; }
else
  "${repo_root}/scripts/render-update-chain.sh" \
    "$version" \
    "$do_base" \
    "${do_base%/}/scripts/upgrade/${version}/_chain_upgrade_common.sh" \
    "${do_base%/}/update_chain.sh" \
    > "${tmp_dir}/update_chain.sh"
fi

if [ -n "$pointer_source_dir" ]; then
  sed \
    "s|^SVOTE_JOIN_COMMON_VERSION=.*|SVOTE_JOIN_COMMON_VERSION=\"\${SVOTE_JOIN_COMMON_VERSION:-}\"|" \
    "$join_full_script" \
    > "${tmp_dir}/join-full.sh"
  grep -Fq "SVOTE_JOIN_COMMON_VERSION=\"\${SVOTE_JOIN_COMMON_VERSION:-}\"" \
    "${tmp_dir}/join-full.sh" \
    || { echo "ERROR: tagged join-full script has no common version field." >&2; exit 1; }
  join_full_script="${tmp_dir}/join-full.sh"
fi

put() {
  "$s3cmd_bin" -c "$s3_config" put "$1" "s3://${bucket}/$2" --acl-public --force
}

put "$join_script" "join.sh"
put "$join_full_script" "join-full.sh"
put "$join_common_script" "scripts/_join_common.sh"
put "$reset_validator_script" "reset-validator-snapshot.sh"
put "$remove_validator_script" "remove-validator.sh"
put "$remove_pir_script" "remove-pir.sh"
put "$wrapper_script" "svoted-wrapper.sh"
put "${tmp_dir}/update_chain.sh" "update_chain.sh"
put "$chain_common_script" "scripts/_chain_upgrade_common.sh"
put "$prepare_upgrade_script" "prepare-upgrade-artifacts.sh"
put "${tmp_dir}/version.txt" "version.txt"
