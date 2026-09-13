#!/usr/bin/env bash
# Offline identity and on-chain binding failures must never pass readiness.
set -euo pipefail
source "$(dirname "$0")/_chain_upgrade_common.sh"
REAL_QUERY_FUNCTION=$(declare -f svote_upgrade_query_upgrade_plan)
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
DAEMON_HOME="$root/home"
COSMVISOR_ROOT="$DAEMON_HOME/cosmovisor"
SVOTE_PLATFORM=linux-amd64
PLAN=test-plan
TAG=v1.6.0-rc.6
mkdir -p "$DAEMON_HOME/config" "$COSMVISOR_ROOT/upgrades/$PLAN/bin"
printf '{"chain_id":"upgrade-test-1"}' > "$DAEMON_HOME/config/genesis.json"
binary="$COSMVISOR_ROOT/upgrades/$PLAN/bin/svoted"
printf 'test executable' > "$binary"
SVOTE_PREPARED_ARCHIVE_SHA256=$(printf 'archive' | shasum -a 256 | awk '{print $1}')
TEST_APPLIED_HEIGHT=0
svote_upgrade_query_applied_plan_height() { printf '%s' "$TEST_APPLIED_HEIGHT"; }
TEST_PLAN_JSON=''
svote_upgrade_query_upgrade_plan() { printf '%s' "$TEST_PLAN_JSON"; }
svote_upgrade_query_block_height() { printf '10'; }
fail() { echo "FAIL: $*" >&2; exit 1; }
reject() {
  if ("$@") >"$root/failure.log" 2>&1; then fail "unexpected success: $*"; fi
}
svote_upgrade_write_artifact_identity "$PLAN" "$TAG" "$binary"
svote_upgrade_verify_artifact_identity "$PLAN" "$TAG"
TEST_PLAN_JSON=$(jq -nc --arg name "$PLAN" --arg tag "$TAG" --arg sha "$SVOTE_PREPARED_ARCHIVE_SHA256" \
  '{name:$name,height:"20",info:({tag:$tag,binaries:{"linux/amd64":("https://example.test/release?checksum=sha256:"+$sha)}}|tojson)}')
svote_upgrade_validate_scheduled_plan "$PLAN" 0
svote_upgrade_verify_artifact_identity "$PLAN" "$TAG"
printf 'tampered' >> "$binary"
reject svote_upgrade_verify_artifact_identity "$PLAN" "$TAG"
printf 'test executable' > "$binary"
reject svote_upgrade_verify_artifact_identity "$PLAN" v1.6.0-rc.7
saved_plan="$TEST_PLAN_JSON"
TEST_APPLIED_HEIGHT=12
reject svote_upgrade_validate_scheduled_plan "$PLAN" 1
TEST_APPLIED_HEIGHT=0
TEST_PLAN_JSON=$(printf '%s' "$saved_plan" | jq '.height="10"')
reject svote_upgrade_validate_scheduled_plan "$PLAN" 0
TEST_PLAN_JSON=$(printf '%s' "$saved_plan" | jq '.name="wrong"')
reject svote_upgrade_validate_scheduled_plan "$PLAN" 1
TEST_PLAN_JSON=$(printf '%s' "$saved_plan" | jq '.info="{}"')
reject svote_upgrade_verify_artifact_identity "$PLAN" "$TAG"
TEST_PLAN_JSON=$(printf '%s' "$saved_plan" | jq '.info |= (fromjson | .binaries={"linux/arm64":"https://example.test/other"} | tojson)')
reject svote_upgrade_verify_artifact_identity "$PLAN" "$TAG"
TEST_PLAN_JSON=''
svote_upgrade_validate_scheduled_plan "$PLAN" 1
reject svote_upgrade_validate_scheduled_plan "$PLAN" 0
svote_upgrade_query_upgrade_plan() { return 1; }
reject svote_upgrade_validate_scheduled_plan "$PLAN" 1
reject svote_upgrade_verify_artifact_identity "$PLAN" "$TAG"
echo 'PASS: artifact identity, tampering, tag/platform/plan mismatches, height, and unavailable API'

# A rendered updater must not execute an unrelated adjacent helper, even for --help.
repo=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$root/bundle" "$root/bin"
"$repo/scripts/render-update-chain.sh" "$TAG" https://example.test https://example.test/common https://example.test/updater > "$root/bundle/update_chain.sh"
printf 'touch "%s"\n' "$root/executed-rogue-helper" > "$root/bundle/_chain_upgrade_common.sh"
cat > "$root/bin/curl" <<'CURL'
#!/usr/bin/env bash
set -euo pipefail
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then cp "$SVOTE_TEST_HELPER_SOURCE" "$2"; exit; fi
  shift
done
exit 1
CURL
chmod +x "$root/bin/curl"
export SVOTE_TEST_HELPER_SOURCE="$repo/scripts/_chain_upgrade_common.sh"
PATH="$root/bin:$PATH" bash "$root/bundle/update_chain.sh" --help > "$root/help.log" 2>&1
[ ! -e "$root/executed-rogue-helper" ] || fail 'unverified adjacent helper executed'
SVOTE_TEST_HELPER_SOURCE="$root/bundle/_chain_upgrade_common.sh"
export SVOTE_TEST_HELPER_SOURCE
reject env PATH="$root/bin:$PATH" bash "$root/bundle/update_chain.sh" --help
[ ! -e "$root/executed-rogue-helper" ] || fail 'unverified downloaded helper executed'
echo 'PASS: rendered updater rejects unrelated local and downloaded helpers'

# Query-level failures must not be normalized to "no plan" by the API fallback.
(
  eval "$REAL_QUERY_FUNCTION"
  CHAIN_API=https://example.test
  svote_upgrade_resolve_query_svoted() { return 1; }
  svote_upgrade_validate_chain_api() { :; }
  svote_upgrade_chain_api_get() { printf '%s' "$TEST_RESPONSE"; }
  TEST_RESPONSE='{"plan":null}'
  [ -z "$(svote_upgrade_query_upgrade_plan)" ] || fail 'null plan rejected'
  TEST_RESPONSE='{"error":"upstream unavailable"}'
  reject svote_upgrade_query_upgrade_plan
  TEST_RESPONSE='{}'
  reject svote_upgrade_query_upgrade_plan
  TEST_RESPONSE='not json'
  reject svote_upgrade_query_upgrade_plan
)
echo 'PASS: malformed REST responses cannot masquerade as an absent plan'
