#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHANNEL_SCRIPT="${REPO_ROOT}/scripts/release-channel.sh"
METADATA_SCRIPT="${REPO_ROOT}/scripts/release-metadata.sh"
POINTER_SCRIPT="${REPO_ROOT}/scripts/publish-release-pointers.sh"
RENDER_SCRIPT="${REPO_ROOT}/scripts/render-update-chain.sh"
PROMOTION_SCRIPT="${REPO_ROOT}/scripts/validate-release-promotion.sh"

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

EXPECTED_RC_METADATA=$'prerelease=true\nmake_latest=false\npublish_mutable_pointers=false'
[ "$($METADATA_SCRIPT v1.2.3-rc.4 v1.2.3-rc.4)" = "$EXPECTED_RC_METADATA" ] \
  || fail "held RC was not kept as a prerelease"
EXPECTED_HELD_METADATA=$'prerelease=false\nmake_latest=false\npublish_mutable_pointers=false'
[ "$($METADATA_SCRIPT v1.2.3 v1.2.3)" = "$EXPECTED_HELD_METADATA" ] \
  || fail "held stable release metadata"
EXPECTED_STABLE_METADATA=$'prerelease=false\nmake_latest=true\npublish_mutable_pointers=true'
[ "$($METADATA_SCRIPT v1.2.3 v1.2.4)" = "$EXPECTED_STABLE_METADATA" ] \
  || fail "unheld stable release metadata"

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
EOF
chmod +x "${TMPDIR}/s3cmd"

export S3CMD_BIN="${TMPDIR}/s3cmd"
export S3CMD_LOG="${TMPDIR}/uploads.log"
"$POINTER_SCRIPT" v1.2.3-rc.4 "${TMPDIR}/s3cfg"
[ ! -s "$S3CMD_LOG" ] || fail "RC release updated mutable pointers"

"$POINTER_SCRIPT" v1.2.3 "${TMPDIR}/s3cfg"
grep -q 's3://shielded-vote/version.txt' "$S3CMD_LOG" || fail "stable version pointer missing"
grep -q 's3://shielded-vote/update_chain.sh' "$S3CMD_LOG" || fail "stable updater pointer missing"
tail -n 1 "$S3CMD_LOG" | grep -q 's3://shielded-vote/version.txt' \
  || fail "stable version pointer was not published last"

echo "PASS: release channel tests"
