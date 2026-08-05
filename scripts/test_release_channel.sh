#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHANNEL_SCRIPT="${REPO_ROOT}/scripts/release-channel.sh"
METADATA_SCRIPT="${REPO_ROOT}/scripts/release-metadata.sh"
POINTER_SCRIPT="${REPO_ROOT}/scripts/publish-release-pointers.sh"
RENDER_SCRIPT="${REPO_ROOT}/scripts/render-update-chain.sh"
PROMOTION_SCRIPT="${REPO_ROOT}/scripts/validate-release-promotion.sh"
VERIFY_SCRIPT="${REPO_ROOT}/scripts/verify_upgrade_release_artifacts.sh"
RELEASE_WORKFLOW="${REPO_ROOT}/.github/workflows/release.yml"
PROMOTION_WORKFLOW="${REPO_ROOT}/.github/workflows/promote-release.yml"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[ "$($CHANNEL_SCRIPT v1.2.3)" = "stable" ] || fail "stable tag classification"
[ "$($CHANNEL_SCRIPT v1.2.3-rc.4)" = "rc" ] || fail "RC tag classification"
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

grep -Fq "make_latest: \${{ needs.release-metadata.outputs.already_latest }}" \
  "$RELEASE_WORKFLOW" \
  || fail "release creation can advance Latest before distribution"
pointer_line="$(grep -n 'scripts/publish-release-pointers.sh' "$RELEASE_WORKFLOW" | tail -n 1 | cut -d: -f1)"
latest_line="$(grep -n -- '- name: Mark GitHub release latest' "$RELEASE_WORKFLOW" | cut -d: -f1)"
[ "$pointer_line" -lt "$latest_line" ] || fail "GitHub Latest advances before mutable pointers"
[ "$(grep -Fc 'uses: actions/checkout@v4' "$PROMOTION_WORKFLOW")" -eq 1 ] \
  || fail "promotion switches away from the trusted checkout"

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

: > "$CURL_LOG"
"$VERIFY_SCRIPT" "$VERIFY_TAG" https://objects.example >/dev/null
grep -Fq '/version.txt' "$CURL_LOG" \
  || fail "complete stable verification skipped version.txt"

echo "PASS: release channel tests"
