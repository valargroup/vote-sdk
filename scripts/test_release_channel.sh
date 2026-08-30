#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHANNEL_SCRIPT="${REPO_ROOT}/scripts/release-channel.sh"
BRANCH_SCRIPT="${REPO_ROOT}/scripts/release-branch.sh"
BRANCH_VALIDATION_SCRIPT="${REPO_ROOT}/scripts/validate-release-branch.sh"
STATE_LABEL_SCRIPT="${REPO_ROOT}/scripts/validate-pr-state-labels.sh"
STATE_LABEL_WORKFLOW="${REPO_ROOT}/.github/workflows/pr-state-compatibility.yml"
METADATA_SCRIPT="${REPO_ROOT}/scripts/release-metadata.sh"
POINTER_SCRIPT="${REPO_ROOT}/scripts/publish-release-pointers.sh"
RENDER_SCRIPT="${REPO_ROOT}/scripts/render-update-chain.sh"
PROMOTION_SCRIPT="${REPO_ROOT}/scripts/validate-release-promotion.sh"
CHANNEL_UPDATE_SCRIPT="${REPO_ROOT}/scripts/validate-release-channel-update.sh"
FETCH_POINTER_SOURCES_SCRIPT="${REPO_ROOT}/scripts/fetch-release-pointer-sources.sh"
VERIFY_SCRIPT="${REPO_ROOT}/scripts/verify_upgrade_release_artifacts.sh"
COSMOVISOR_PACKAGER="${REPO_ROOT}/scripts/package-cosmovisor-archive.sh"
RELEASE_WORKFLOW="${REPO_ROOT}/.github/workflows/release.yml"
PROMOTION_WORKFLOW="${REPO_ROOT}/.github/workflows/promote-release.yml"
UPGRADE_SCRIPT_WORKFLOW="${REPO_ROOT}/.github/workflows/publish-upgrade-scripts.yml"
UPGRADE_RUNBOOK="${REPO_ROOT}/docs/runbooks/software-upgrades.md"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[ "$($CHANNEL_SCRIPT v1.2.3)" = "stable" ] || fail "stable tag classification"
[ "$($CHANNEL_SCRIPT v1.2.3-rc.4)" = "rc" ] || fail "RC tag classification"
[ "$($BRANCH_SCRIPT v1.2.3)" = "v1.2.x" ] || fail "stable release branch"
[ "$($BRANCH_SCRIPT v10.27.3-rc.14)" = "v10.27.x" ] || fail "RC release branch"
for tag in v1.2 v1.2.3-rc v1.2.3-beta.1 v1.2.3.4; do
  if "$CHANNEL_SCRIPT" "$tag" >/dev/null 2>&1; then
    fail "invalid tag accepted: $tag"
  fi
done

EXPECTED_RC_METADATA=$'prerelease=true\nmake_latest=false\nalready_latest=false\npublish_mutable_pointers=false'
[ "$($METADATA_SCRIPT v1.2.3-rc.4 v1.2.3-rc.4)" = "$EXPECTED_RC_METADATA" ] \
  || fail "held RC was not kept as a prerelease"
EXPECTED_HELD_METADATA=$'prerelease=false\nmake_latest=false\nalready_latest=false\npublish_mutable_pointers=false'
[ "$($METADATA_SCRIPT v1.2.3 v1.2.3)" = "$EXPECTED_HELD_METADATA" ] \
  || fail "held stable release metadata"
EXPECTED_STABLE_METADATA=$'prerelease=false\nmake_latest=true\nalready_latest=false\npublish_mutable_pointers=true'
[ "$($METADATA_SCRIPT v1.2.3 v1.2.4)" = "$EXPECTED_STABLE_METADATA" ] \
  || fail "unheld stable release metadata"
EXPECTED_CURRENT_METADATA=$'prerelease=false\nmake_latest=true\nalready_latest=true\npublish_mutable_pointers=true'
[ "$($METADATA_SCRIPT v1.2.3 v1.2.3 v1.2.3)" = "$EXPECTED_CURRENT_METADATA" ] \
  || fail "already promoted held release metadata"

if grep -Fq 'softprops/action-gh-release' "$RELEASE_WORKFLOW"; then
  fail "release creation can mutate Latest outside the channel lock"
fi
grep -Fq -- '--latest=false --verify-tag' "$RELEASE_WORKFLOW" \
  || fail "new GitHub releases are not created outside Latest"
grep -Fq 'scripts/validate-release-branch.sh' "$RELEASE_WORKFLOW" \
  || fail "release workflow does not validate its maintenance branch"
grep -Fq 'needs: release-branch' "$RELEASE_WORKFLOW" \
  || fail "release metadata is not gated on maintenance branch validation"
github_repo_pattern='GH_REPO: $''{{ github.repository }}'
grep -Fq "$github_repo_pattern" "$RELEASE_WORKFLOW" \
  || fail "release creation has no explicit GitHub repository context"
grep -Fq 'scripts/package-cosmovisor-archive.sh' "$RELEASE_WORKFLOW" \
  || fail "release workflow does not package Cosmovisor archives"
grep -Fq "assets+=(\"\${COSMOVISOR_TARBALL}\" \"\${COSMOVISOR_TARBALL}.sha256\")" \
  "$RELEASE_WORKFLOW" \
  || fail "release workflow does not upload Cosmovisor archives"
grep -Fq 'name: Build voting-config (linux-amd64)' "$RELEASE_WORKFLOW" \
  || fail "release workflow does not build the standalone voting-config verifier"
grep -Fq 'name: Release voting-config (linux-amd64)' "$RELEASE_WORKFLOW" \
  || fail "release workflow does not publish the standalone voting-config verifier"
grep -Fq 'sha256sum --check voting-config-linux-amd64.sha256' "$RELEASE_WORKFLOW" \
  || fail "release workflow does not verify the standalone voting-config checksum"
grep -Fq 'scripts/verify-voting-config-release-asset.sh "${GITHUB_REF_NAME}"' "$RELEASE_WORKFLOW" \
  || fail "release workflow does not verify the uploaded voting-config asset"
grep -Fq 'needs: [release-metadata, distribute, release-voting-config]' "$RELEASE_WORKFLOW" \
  || fail "stable release publication is not gated on the voting-config asset"
grep -Fq 'scripts/verify-voting-config-release-asset.sh "$RELEASE_TAG"' "$PROMOTION_WORKFLOW" \
  || fail "manual promotion does not verify the voting-config release asset"
grep -Fq "shielded-vote-\${RELEASE_TAG}-cosmovisor-v1-\${platform}.tar.gz" \
  "$PROMOTION_WORKFLOW" \
  || fail "promotion does not verify Cosmovisor archives"
pointer_line="$(grep -n 'scripts/publish-release-pointers.sh' "$RELEASE_WORKFLOW" | tail -n 1 | cut -d: -f1)"
verify_line="$(grep -n -- '- name: Verify mutable release pointers' "$RELEASE_WORKFLOW" | cut -d: -f1)"
latest_line="$(grep -n -- '- name: Mark GitHub release latest' "$RELEASE_WORKFLOW" | cut -d: -f1)"
[ "$pointer_line" -lt "$latest_line" ] || fail "GitHub Latest advances before mutable pointers"
[ "$pointer_line" -lt "$verify_line" ] && [ "$verify_line" -lt "$latest_line" ] \
  || fail "stable pointers are not verified before GitHub Latest advances"
[ "$(grep -Fc 'uses: actions/checkout@v4' "$PROMOTION_WORKFLOW")" -eq 1 ] \
  || fail "promotion switches away from the trusted checkout"

grep -Fq 'missing_scripts=()' "$UPGRADE_SCRIPT_WORKFLOW" \
  || fail "upgrade script publication has no complete-revision preflight"
grep -Fq "for script in \"\${missing_scripts[@]}\"" "$UPGRADE_SCRIPT_WORKFLOW" \
  || fail "upgrade script publication does not separate validation from writes"
publish_concurrency_group="$(grep -F 'group: publish-upgrade-scripts-' "$UPGRADE_SCRIPT_WORKFLOW")"
printf '%s\n' "$publish_concurrency_group" | grep -Fq 'vars.DO_SPACES_BUCKET' \
  || fail "upgrade script publication is not serialized by bucket"
printf '%s\n' "$publish_concurrency_group" | grep -Fq 'inputs.script_version' \
  || fail "upgrade script publication is not serialized by revision"
grep -Fq 'cancel-in-progress: false' "$UPGRADE_SCRIPT_WORKFLOW" \
  || fail "upgrade script publication can cancel an active writer"
common_publish_line="$(grep -n '^            _chain_upgrade_common.sh$' "$UPGRADE_SCRIPT_WORKFLOW" | head -n 1 | cut -d: -f1)"
update_publish_line="$(grep -n '^            update_chain.sh$' "$UPGRADE_SCRIPT_WORKFLOW" | head -n 1 | cut -d: -f1)"
prepare_publish_line="$(grep -n '^            prepare-upgrade-artifacts.sh$' "$UPGRADE_SCRIPT_WORKFLOW" | head -n 1 | cut -d: -f1)"
[ "$common_publish_line" -lt "$update_publish_line" ] \
  && [ "$update_publish_line" -lt "$prepare_publish_line" ] \
  || fail "upgrade scripts are not published in dependency order"
grep -Fq "\"linux/amd64\": \$amd64" "$UPGRADE_RUNBOOK" \
  || fail "upgrade scheduling runbook omits the amd64 Cosmovisor binary"
grep -Fq "\"linux/arm64\": \$arm64" "$UPGRADE_RUNBOOK" \
  || fail "upgrade scheduling runbook omits the arm64 Cosmovisor binary"
grep -Fq "?checksum=sha256:\${AMD64_SHA256}" "$UPGRADE_RUNBOOK" \
  || fail "upgrade scheduling runbook omits the amd64 checksum-pinned URL"
grep -Fq "?checksum=sha256:\${ARM64_SHA256}" "$UPGRADE_RUNBOOK" \
  || fail "upgrade scheduling runbook omits the arm64 checksum-pinned URL"
grep -Fq 'upgrade was not scheduled' "$UPGRADE_RUNBOOK" \
  || fail "upgrade scheduling runbook does not fail closed on invalid metadata"

if grep -q '^concurrency:' "$RELEASE_WORKFLOW" "$PROMOTION_WORKFLOW"; then
  fail "workflow-level release concurrency can cancel queued artifact publication"
fi
[ "$(grep -Fc 'group: release-channel' "$RELEASE_WORKFLOW")" -eq 1 ] \
  || fail "stable channel mutation job does not own one shared lock"
[ "$(grep -Fc 'group: release-channel' "$PROMOTION_WORKFLOW")" -eq 1 ] \
  || fail "promotion job does not own one shared lock"
release_channel_job_line="$(grep -n '^  publish-release-channel:' "$RELEASE_WORKFLOW" | cut -d: -f1)"
release_concurrency_line="$(grep -n '^    concurrency:' "$RELEASE_WORKFLOW" | cut -d: -f1)"
promotion_job_line="$(grep -n '^  promote:' "$PROMOTION_WORKFLOW" | cut -d: -f1)"
promotion_concurrency_line="$(grep -n '^    concurrency:' "$PROMOTION_WORKFLOW" | cut -d: -f1)"
[ "$release_channel_job_line" -lt "$release_concurrency_line" ] \
  || fail "release concurrency is not scoped to the channel mutation job"
[ "$promotion_job_line" -lt "$promotion_concurrency_line" ] \
  || fail "promotion concurrency is not scoped to the promotion job"

promotion_pointer_line="$(grep -n 'scripts/publish-release-pointers.sh' "$PROMOTION_WORKFLOW" | cut -d: -f1)"
promotion_verify_line="$(grep -n -- '- name: Verify mutable release pointers' "$PROMOTION_WORKFLOW" | cut -d: -f1)"
promotion_latest_line="$(grep -n -- '- name: Mark GitHub release latest' "$PROMOTION_WORKFLOW" | cut -d: -f1)"
[ "$promotion_pointer_line" -lt "$promotion_verify_line" ] \
  && [ "$promotion_verify_line" -lt "$promotion_latest_line" ] \
  || fail "promotion aliases are not verified before GitHub Latest advances"
[ "$(grep -Fc 'scripts/fetch-release-pointer-sources.sh' "$RELEASE_WORKFLOW")" -eq 1 ] \
  || fail "stable release channel does not use shared tagged source fetching"
[ "$(grep -Fc 'scripts/fetch-release-pointer-sources.sh' "$PROMOTION_WORKFLOW")" -eq 1 ] \
  || fail "promotion does not use shared tagged source fetching"
[ "$(grep -Fc -- '--mutable-only' "$RELEASE_WORKFLOW")" -eq 1 ] \
  || fail "stable release channel does not verify mutable pointers before Latest"
[ "$(grep -Fc -- '--mutable-only' "$PROMOTION_WORKFLOW")" -eq 1 ] \
  || fail "promotion does not verify mutable pointers before Latest"

[ "$($CHANNEL_UPDATE_SCRIPT v1.2.3 v1.2.2)" = "v1.2.3" ] \
  || fail "newer stable channel update validation"
[ "$($CHANNEL_UPDATE_SCRIPT v1.2.3 v1.2.3)" = "v1.2.3" ] \
  || fail "idempotent stable channel update validation"
[ "$($CHANNEL_UPDATE_SCRIPT v2.0.0 v1.99.99)" = "v2.0.0" ] \
  || fail "new major stable channel update validation"
if "$CHANNEL_UPDATE_SCRIPT" v1.2.3 v1.2.4 >/dev/null 2>&1; then
  fail "stable channel update replaced a newer Latest release"
fi
if "$CHANNEL_UPDATE_SCRIPT" v1.2.3-rc.4 v1.2.3 >/dev/null 2>&1; then
  fail "RC release accepted for stable channel update"
fi

[ "$($PROMOTION_SCRIPT v1.2.3 v1.2.3 v1.2.2)" = "v1.2.3" ] \
  || fail "held stable release promotion validation"
[ "$($PROMOTION_SCRIPT v1.2.3 v1.2.3 v1.2.3)" = "v1.2.3" ] \
  || fail "idempotent promotion validation"
[ "$($PROMOTION_SCRIPT v2.0.0 v2.0.0 v1.99.99)" = "v2.0.0" ] \
  || fail "new major release promotion validation"
if "$PROMOTION_SCRIPT" v1.2.3 v1.2.3 v1.2.4 >/dev/null 2>&1; then
  fail "promotion replaced a newer Latest release"
fi
if "$PROMOTION_SCRIPT" v2.0.0 v2.0.0 v10.0.0 >/dev/null 2>&1; then
  fail "promotion replaced a newer multi-digit major release"
fi
if "$PROMOTION_SCRIPT" v1.2.3-rc.4 v1.2.3-rc.4 >/dev/null 2>&1; then
  fail "RC release promotion accepted"
fi
if "$PROMOTION_SCRIPT" v1.2.3 v1.2.4 >/dev/null 2>&1; then
  fail "promotion tag did not match hold"
fi
if "$PROMOTION_SCRIPT" v1.2.3 "" >/dev/null 2>&1; then
  fail "promotion accepted without a hold"
fi

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

RELEASE_REPO="${TMPDIR}/release-repo"
git init -q -b main "$RELEASE_REPO"
git -C "$RELEASE_REPO" config user.name "Release test"
git -C "$RELEASE_REPO" config user.email "release-test@example.invalid"
git -C "$RELEASE_REPO" -c commit.gpgSign=false commit -q --allow-empty -m base
RELEASE_BASE_SHA="$(git -C "$RELEASE_REPO" rev-parse HEAD)"
git -C "$RELEASE_REPO" branch v1.2.x "$RELEASE_BASE_SHA"
git -C "$RELEASE_REPO" -c commit.gpgSign=false commit -q --allow-empty -m main-only
RELEASE_MAIN_SHA="$(git -C "$RELEASE_REPO" rev-parse HEAD)"

"$BRANCH_VALIDATION_SCRIPT" v1.2.3 "$RELEASE_BASE_SHA" "$RELEASE_REPO" >/dev/null \
  || fail "release branch rejected a reachable stable commit"
"$BRANCH_VALIDATION_SCRIPT" v1.2.4-rc.2 "$RELEASE_BASE_SHA" "$RELEASE_REPO" >/dev/null \
  || fail "release branch rejected a reachable RC commit"
if "$BRANCH_VALIDATION_SCRIPT" v1.2.3 "$RELEASE_MAIN_SHA" "$RELEASE_REPO" \
  >/dev/null 2>&1; then
  fail "release branch accepted a main-only commit"
fi
if "$BRANCH_VALIDATION_SCRIPT" v2.0.0 "$RELEASE_BASE_SHA" "$RELEASE_REPO" \
  >/dev/null 2>&1; then
  fail "release branch accepted a missing maintenance branch"
fi

"$STATE_LABEL_SCRIPT" V:state/compatible >/dev/null \
  || fail "compatible state label was rejected"
"$STATE_LABEL_SCRIPT" V:state/breaking >/dev/null \
  || fail "breaking state label was rejected on main"
"$STATE_LABEL_SCRIPT" V:state/compatible A:backport/v1.4.x >/dev/null \
  || fail "compatible backport labels were rejected"
if "$STATE_LABEL_SCRIPT" >/dev/null 2>&1; then
  fail "missing state label was accepted"
fi
if "$STATE_LABEL_SCRIPT" V:state/compatible V:state/breaking >/dev/null 2>&1; then
  fail "conflicting state labels were accepted"
fi
if "$STATE_LABEL_SCRIPT" V:state/breaking A:backport/v1.4.x >/dev/null 2>&1; then
  fail "state-breaking backport was accepted"
fi
if PR_BASE_REF=v1.4.x "$STATE_LABEL_SCRIPT" V:state/breaking >/dev/null 2>&1; then
  fail "state-breaking maintenance PR was accepted"
fi
if PR_BASE_REF=v1.4.x "$STATE_LABEL_SCRIPT" \
  V:state/compatible A:backport/v1.4.x >/dev/null 2>&1; then
  fail "backport target label was accepted on a maintenance PR"
fi
grep -Eq 'types: \[[^]]*edited' "$STATE_LABEL_WORKFLOW" \
  || fail "state label validation does not run after a PR base change"

RUNBOOK_BIN="${TMPDIR}/runbook-bin"
RUNBOOK_EXAMPLE="${TMPDIR}/schedule-upgrade-example.sh"
RUNBOOK_SVOTED_CALLED="${TMPDIR}/runbook-svoted-called"
mkdir -p "$RUNBOOK_BIN"
awk '
  $0 == "TAG=v1.2.3" { capture = 1 }
  capture && $0 == "```" { exit }
  capture { print }
' "$UPGRADE_RUNBOOK" \
  | sed \
      -e 's/<name>/test-upgrade/g' \
      -e 's/<height>/123/g' \
      -e 's/<vote-manager-key>/test-key/g' \
  > "$RUNBOOK_EXAMPLE"
cat > "${RUNBOOK_BIN}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${RUNBOOK_CURL_FAIL:-false}" = "true" ]; then
  exit 22
fi
printf '%s\n' "${RUNBOOK_CHECKSUM_BODY:-not-a-checksum}"
EOF
cat > "${RUNBOOK_BIN}/svoted" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
: > "$RUNBOOK_SVOTED_CALLED"
EOF
chmod +x "${RUNBOOK_BIN}/curl" "${RUNBOOK_BIN}/svoted"
export RUNBOOK_SVOTED_CALLED

if RUNBOOK_CURL_FAIL=true PATH="${RUNBOOK_BIN}:$PATH" bash "$RUNBOOK_EXAMPLE" >/dev/null 2>&1; then
  fail "upgrade scheduling example accepted a failed checksum download"
fi
[ ! -e "$RUNBOOK_SVOTED_CALLED" ] \
  || fail "upgrade scheduling example submitted after a failed checksum download"
if RUNBOOK_CHECKSUM_BODY=not-a-checksum PATH="${RUNBOOK_BIN}:$PATH" bash "$RUNBOOK_EXAMPLE" >/dev/null 2>&1; then
  fail "upgrade scheduling example accepted a malformed checksum"
fi
[ ! -e "$RUNBOOK_SVOTED_CALLED" ] \
  || fail "upgrade scheduling example submitted with a malformed checksum"
RUNBOOK_CHECKSUM_BODY='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  artifact' \
  PATH="${RUNBOOK_BIN}:$PATH" bash "$RUNBOOK_EXAMPLE" >/dev/null
[ -e "$RUNBOOK_SVOTED_CALLED" ] \
  || fail "upgrade scheduling example did not submit with valid checksums"

FAKE_SVOTED="${TMPDIR}/svoted"
printf '#!/usr/bin/env bash\necho svoted\n' > "$FAKE_SVOTED"
chmod +x "$FAKE_SVOTED"
COSMOVISOR_OUTPUT="${TMPDIR}/cosmovisor"
COSMOVISOR_ARCHIVE="$($COSMOVISOR_PACKAGER \
  v1.2.3 \
  linux-amd64 \
  "$FAKE_SVOTED" \
  "$COSMOVISOR_OUTPUT")"
[ "$(basename "$COSMOVISOR_ARCHIVE")" = \
  "shielded-vote-v1.2.3-cosmovisor-v1-linux-amd64.tar.gz" ] \
  || fail "Cosmovisor archive name does not match the upgrade loader"
[ "$(tar tzf "$COSMOVISOR_ARCHIVE")" = "bin/svoted" ] \
  || fail "Cosmovisor archive has the wrong layout"
(cd "$COSMOVISOR_OUTPUT" && sha256sum --check "$(basename "$COSMOVISOR_ARCHIVE").sha256" >/dev/null) \
  || fail "Cosmovisor archive checksum did not verify"
COSMOVISOR_EXTRACT="${TMPDIR}/cosmovisor-extract"
mkdir -p "$COSMOVISOR_EXTRACT"
tar xzf "$COSMOVISOR_ARCHIVE" -C "$COSMOVISOR_EXTRACT"
[ -x "$COSMOVISOR_EXTRACT/bin/svoted" ] \
  || fail "Cosmovisor archive lost the executable bit"
if "$COSMOVISOR_PACKAGER" v1.2.3 darwin-arm64 "$FAKE_SVOTED" "$COSMOVISOR_OUTPUT" \
  >/dev/null 2>&1; then
  fail "Cosmovisor packager accepted an unsupported platform"
fi

cat > "${TMPDIR}/fetch-curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

output_file=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      output_file="$2"
      shift 2
      ;;
    --retry|--retry-delay)
      shift 2
      ;;
    -*)
      shift
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done
printf '%s\n' "$url" >> "$FETCH_CURL_LOG"
printf '#!/usr/bin/env bash\n' > "$output_file"
EOF
chmod +x "${TMPDIR}/fetch-curl"
export CURL_BIN="${TMPDIR}/fetch-curl"
export FETCH_CURL_LOG="${TMPDIR}/fetch-curl.log"
FETCHED_POINTER_SOURCES="${TMPDIR}/fetched-pointer-sources"
"$FETCH_POINTER_SOURCES_SCRIPT" \
  v1.2.3 \
  https://objects.example/ \
  "$FETCHED_POINTER_SOURCES"
[ "$(find "$FETCHED_POINTER_SOURCES" -type f | wc -l | tr -d '[:space:]')" = "10" ] \
  || fail "shared pointer source fetch did not download every source"
grep -Fq 'https://objects.example/scripts/join-full/v1.2.3/join-full.sh' "$FETCH_CURL_LOG" \
  || fail "shared pointer source fetch used the wrong tag path"

RC_UPDATE_URL="https://objects.example/scripts/upgrade/v1.2.3-rc.4/update_chain.sh"
RENDERED_UPDATE_CHAIN="${TMPDIR}/rendered-update-chain"
"$RENDER_SCRIPT" \
  v1.2.3-rc.4 \
  https://objects.example \
  https://objects.example/scripts/upgrade/v1.2.3-rc.4/_chain_upgrade_common.sh \
  "$RC_UPDATE_URL" \
  > "$RENDERED_UPDATE_CHAIN"
grep -Fq "readonly UPDATE_DEFAULT_UPDATER_URL='${RC_UPDATE_URL}'" "$RENDERED_UPDATE_CHAIN" \
  || fail "RC updater URL is not tag-scoped"
grep -Fq "echo \"  curl -fsSL \${VERIFY_UPDATER_URL}" "$RENDERED_UPDATE_CHAIN" \
  || fail "verify command does not use the rendered updater URL"

cat > "${TMPDIR}/s3cmd" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$S3CMD_LOG"
if [ -n "${S3CMD_CAPTURE_DIR:-}" ] && [ "${3:-}" = "put" ]; then
  case "${5:-}" in
    */join-full.sh)
      cp "$4" "${S3CMD_CAPTURE_DIR}/join-full.sh"
      ;;
    */update_chain.sh)
      cp "$4" "${S3CMD_CAPTURE_DIR}/update_chain.sh"
      ;;
  esac
fi
EOF
chmod +x "${TMPDIR}/s3cmd"

export S3CMD_BIN="${TMPDIR}/s3cmd"
export S3CMD_LOG="${TMPDIR}/uploads.log"
export S3CMD_CAPTURE_DIR="${TMPDIR}/captured"
mkdir -p "$S3CMD_CAPTURE_DIR"
"$POINTER_SCRIPT" v1.2.3-rc.4 "${TMPDIR}/s3cfg"
[ ! -s "$S3CMD_LOG" ] || fail "RC release updated mutable pointers"

"$POINTER_SCRIPT" v1.2.3 "${TMPDIR}/s3cfg"
grep -q 's3://shielded-vote/version.txt' "$S3CMD_LOG" || fail "stable version pointer missing"
grep -q 's3://shielded-vote/update_chain.sh' "$S3CMD_LOG" || fail "stable updater pointer missing"
tail -n 1 "$S3CMD_LOG" | grep -q 's3://shielded-vote/version.txt' \
  || fail "stable version pointer was not published last"

POINTER_SOURCES="${TMPDIR}/pointer-sources"
mkdir -p "$POINTER_SOURCES"
cp "${REPO_ROOT}/join.sh" "${POINTER_SOURCES}/join.sh"
sed "s|^SVOTE_JOIN_COMMON_VERSION=.*|SVOTE_JOIN_COMMON_VERSION=\"\${SVOTE_JOIN_COMMON_VERSION:-v1.2.3}\"|" \
  "${REPO_ROOT}/join-full.sh" > "${POINTER_SOURCES}/join-full.sh"
cp "${REPO_ROOT}/scripts/_join_common.sh" "${POINTER_SOURCES}/_join_common.sh"
cp "${REPO_ROOT}/scripts/reset-validator-snapshot.sh" \
  "${POINTER_SOURCES}/reset-validator-snapshot.sh"
cp "${REPO_ROOT}/scripts/remove-validator.sh" "${POINTER_SOURCES}/remove-validator.sh"
cp "${REPO_ROOT}/scripts/remove-pir.sh" "${POINTER_SOURCES}/remove-pir.sh"
cp "${REPO_ROOT}/scripts/svoted-wrapper.sh" "${POINTER_SOURCES}/svoted-wrapper.sh"
cp "${REPO_ROOT}/scripts/_chain_upgrade_common.sh" \
  "${POINTER_SOURCES}/_chain_upgrade_common.sh"
cp "${REPO_ROOT}/scripts/prepare-upgrade-artifacts.sh" \
  "${POINTER_SOURCES}/prepare-upgrade-artifacts.sh"
"$RENDER_SCRIPT" \
  v1.2.3 \
  https://objects.example \
  https://objects.example/scripts/upgrade/v1.2.3/_chain_upgrade_common.sh \
  https://objects.example/scripts/upgrade/v1.2.3/update_chain.sh \
  > "${POINTER_SOURCES}/update_chain.sh"

: > "$S3CMD_LOG"
"$POINTER_SCRIPT" v1.2.3 "${TMPDIR}/s3cfg" "$POINTER_SOURCES"
grep -Fq "${POINTER_SOURCES}/join.sh s3://shielded-vote/join.sh" "$S3CMD_LOG" \
  || fail "tag-scoped join source was not published"
grep -Fq "readonly UPDATE_DEFAULT_RELEASE_TAG='v1.2.3'" \
  "${S3CMD_CAPTURE_DIR}/update_chain.sh" \
  || fail "promoted updater lost its release tag"
grep -Fq "readonly UPDATE_DEFAULT_UPDATER_URL='https://shielded-vote.nyc3.digitaloceanspaces.com/update_chain.sh'" \
  "${S3CMD_CAPTURE_DIR}/update_chain.sh" \
  || fail "promoted updater did not use the mutable updater URL"
grep -Fq "SVOTE_JOIN_COMMON_VERSION=\"\${SVOTE_JOIN_COMMON_VERSION:-}\"" \
  "${S3CMD_CAPTURE_DIR}/join-full.sh" \
  || fail "promoted join-full pointer remained pinned to the tag-scoped common script"

: > "$S3CMD_LOG"
mkdir -p "${TMPDIR}/missing-pointer-sources"
if "$POINTER_SCRIPT" v1.2.3 "${TMPDIR}/s3cfg" "${TMPDIR}/missing-pointer-sources" \
  >/dev/null 2>&1; then
  fail "missing tag-scoped pointer sources were accepted"
fi
[ ! -s "$S3CMD_LOG" ] || fail "pointers changed before source validation"

cat > "${TMPDIR}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

output_file=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      output_file="$2"
      shift 2
      ;;
    --retry|--retry-delay)
      shift 2
      ;;
    -*)
      shift
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done

printf '%s\n' "$url" >> "$CURL_LOG"
case "$url" in
  */version.txt)
    body="$VERIFY_TAG"
    ;;
  *.tar.gz.sha256)
    body='c7c5c1d70c5dec4416ab6158afd0b223ef40c29b1dc1f97ed9428b94d4cadb1c  artifact'
    ;;
  *.tar.gz)
    body='artifact'
    ;;
  */scripts/upgrade/*/update_chain.sh)
    body="#!/usr/bin/env bash
readonly UPDATE_DEFAULT_RELEASE_TAG='${VERIFY_TAG}'"
    ;;
  */update_chain.sh)
    body="#!/usr/bin/env bash
readonly UPDATE_DEFAULT_RELEASE_TAG='${VERIFY_TAG}'"
    ;;
  *)
    body='#!/usr/bin/env bash'
    ;;
esac

if [ -n "$output_file" ]; then
  printf '%s' "$body" > "$output_file"
else
  printf '%s' "$body"
fi
EOF
chmod +x "${TMPDIR}/curl"

export CURL_BIN="${TMPDIR}/curl"
export CURL_LOG="${TMPDIR}/curl.log"
export VERIFY_TAG=v1.2.3

"$VERIFY_SCRIPT" --tag-scoped-only "$VERIFY_TAG" https://objects.example >/dev/null
if grep -Fq '/version.txt' "$CURL_LOG"; then
  fail "tag-scoped verification checked mutable stable pointers"
fi
for key in \
  "scripts/join-full/${VERIFY_TAG}/join-full.sh" \
  "scripts/join-common/${VERIFY_TAG}/_join_common.sh" \
  "scripts/join/${VERIFY_TAG}/join.sh" \
  "scripts/reset-validator-snapshot/${VERIFY_TAG}/reset-validator-snapshot.sh" \
  "scripts/remove-validator/${VERIFY_TAG}/remove-validator.sh" \
  "scripts/remove-pir/${VERIFY_TAG}/remove-pir.sh" \
  "scripts/svoted-wrapper/${VERIFY_TAG}/svoted-wrapper.sh" \
  "scripts/upgrade/${VERIFY_TAG}/update_chain.sh" \
  "scripts/upgrade/${VERIFY_TAG}/_chain_upgrade_common.sh" \
  "scripts/upgrade/${VERIFY_TAG}/prepare-upgrade-artifacts.sh"
do
  grep -Fqx "https://objects.example/${key}" "$CURL_LOG" \
    || fail "tag-scoped verification skipped ${key}"
done

: > "$CURL_LOG"
"$VERIFY_SCRIPT" --mutable-only "$VERIFY_TAG" https://objects.example >/dev/null
grep -Fq '/version.txt' "$CURL_LOG" \
  || fail "mutable verification skipped version.txt"
grep -Fq '/join-full.sh' "$CURL_LOG" \
  || fail "mutable verification skipped join-full.sh"
if grep -Fq "/scripts/upgrade/${VERIFY_TAG}/" "$CURL_LOG"; then
  fail "mutable verification fetched tag-scoped scripts"
fi
if grep -Fq '.tar.gz' "$CURL_LOG"; then
  fail "mutable verification downloaded release tarballs"
fi

: > "$CURL_LOG"
"$VERIFY_SCRIPT" "$VERIFY_TAG" https://objects.example >/dev/null
grep -Fq '/version.txt' "$CURL_LOG" \
  || fail "complete stable verification skipped version.txt"
grep -Fq '/join-full.sh' "$CURL_LOG" \
  || fail "complete stable verification skipped join-full.sh"
grep -Fq '/remove-pir.sh' "$CURL_LOG" \
  || fail "complete stable verification skipped remove-pir.sh"

echo "PASS: release channel tests"
