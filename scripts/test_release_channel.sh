#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHANNEL_SCRIPT="${REPO_ROOT}/scripts/release-channel.sh"
POINTER_SCRIPT="${REPO_ROOT}/scripts/publish-release-pointers.sh"
RENDER_SCRIPT="${REPO_ROOT}/scripts/render-update-chain.sh"

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

echo "PASS: release channel tests"
