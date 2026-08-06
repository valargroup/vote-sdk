#!/usr/bin/env bash
# test_chain_upgrade_signer_detect.sh — unit tests for signer-process detection helpers.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMMON="${REPO_ROOT}/scripts/_chain_upgrade_common.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# shellcheck source=scripts/_chain_upgrade_common.sh
source "$COMMON"

HOME_PATH="/tmp/svote-upgrade-signer-test-home"

assert_runtime_match() {
  local cmd="$1"
  svote_upgrade_is_signer_runtime_cmd "$cmd" "$HOME_PATH" || fail "expected runtime match: $cmd"
}

assert_runtime_no_match() {
  local cmd="$1"
  if svote_upgrade_is_signer_runtime_cmd "$cmd" "$HOME_PATH"; then
    fail "expected no runtime match: $cmd"
  fi
}

assert_runtime_match_with_inferred_home() {
  local cmd="$1"
  svote_upgrade_is_signer_runtime_cmd "$cmd" "$HOME_PATH" "$HOME_PATH" \
    || fail "expected inferred-home runtime match: $cmd"
}

echo "=== signer detect: upgrade tooling must not match ==="
assert_runtime_no_match "bash ${REPO_ROOT}/scripts/update_chain.sh --mode migrate --home ${HOME_PATH} --plan-name test"
assert_runtime_no_match "bash ${REPO_ROOT}/scripts/_chain_upgrade_common.sh --home ${HOME_PATH}"

echo "=== signer detect: home path substring must not match ==="
assert_runtime_no_match "bash some-script.sh --home ${HOME_PATH}/../.svoted-primary-backup"

echo "=== signer detect: actual svoted/cosmovisor runtimes match ==="
assert_runtime_match "/usr/local/bin/svoted start --home ${HOME_PATH}"
assert_runtime_match "/usr/local/bin/svoted start --home=${HOME_PATH}"
assert_runtime_match "/usr/local/bin/cosmovisor run start --home ${HOME_PATH}"
assert_runtime_match "/root/.svoted/cosmovisor/genesis/bin/svoted start --home ${HOME_PATH}"
assert_runtime_match_with_inferred_home "/usr/local/bin/svoted start"
assert_runtime_no_match "/usr/local/bin/svoted status --home ${HOME_PATH}"

echo "=== signer detect: extract direct ExecStart args ==="
args=$(svote_upgrade_extract_direct_svoted_start_args \
  "/usr/local/bin/svoted start --home ${HOME_PATH} --serve-ui --ui-dist /opt/ui/dist" \
  "$HOME_PATH")
[ "$args" = "--serve-ui --ui-dist /opt/ui/dist" ] || fail "unexpected extracted args: ${args}"

empty_args=$(svote_upgrade_extract_direct_svoted_start_args \
  "/usr/local/bin/svoted-wrapper.sh" \
  "$HOME_PATH")
[ -z "$empty_args" ] || fail "wrapper ExecStart should not yield args: ${empty_args}"

echo "=== PASS: chain upgrade signer detection tests ==="
