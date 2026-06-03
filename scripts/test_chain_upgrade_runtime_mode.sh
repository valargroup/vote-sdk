#!/usr/bin/env bash
# test_chain_upgrade_runtime_mode.sh — unit tests for runtime-mode helper functions.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMMON="${REPO_ROOT}/scripts/_chain_upgrade_common.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# shellcheck source=scripts/_chain_upgrade_common.sh
source "$COMMON"

echo "=== env parser: last duplicate value wins ==="
ENV_BLOB='PATH=/usr/local/bin SVOTE_UPGRADE_MODE=cosmovisor SVOTE_UPGRADE_MODE=direct DAEMON_HOME=/tmp/a DAEMON_HOME=/tmp/b'
mode=$(svote_upgrade_extract_effective_env_value "$ENV_BLOB" "SVOTE_UPGRADE_MODE")
[ "$mode" = "direct" ] || fail "expected effective mode=direct, got ${mode}"
home=$(svote_upgrade_extract_effective_env_value "$ENV_BLOB" "DAEMON_HOME")
[ "$home" = "/tmp/b" ] || fail "expected effective daemon_home=/tmp/b, got ${home}"

echo "=== env parser: quoted tokens are handled ==="
QUOTED_ENV_BLOB='PATH=/usr/local/bin "SVOTE_UPGRADE_MODE=cosmovisor" "DAEMON_HOME=/tmp/quoted-home"'
quoted_mode=$(svote_upgrade_extract_effective_env_value "$QUOTED_ENV_BLOB" "SVOTE_UPGRADE_MODE")
[ "$quoted_mode" = "cosmovisor" ] || fail "expected quoted mode=cosmovisor, got ${quoted_mode}"
quoted_home=$(svote_upgrade_extract_effective_env_value "$QUOTED_ENV_BLOB" "DAEMON_HOME")
[ "$quoted_home" = "/tmp/quoted-home" ] || fail "expected quoted daemon_home=/tmp/quoted-home, got ${quoted_home}"

echo "=== systemd env setter: replaces existing duplicates ==="
TMP_UNIT="$(mktemp)"
trap 'rm -f "$TMP_UNIT"' EXIT
cat > "$TMP_UNIT" <<'EOF'
[Unit]
Description=svoted

[Service]
Environment="SVOTE_UPGRADE_MODE=direct"
Environment="SVOTE_UPGRADE_MODE=cosmovisor"
Environment="DAEMON_HOME=/tmp/old"
ExecStart=/usr/local/bin/svoted start --home /tmp/old
EOF

svote_upgrade_set_systemd_environment_key "$TMP_UNIT" "SVOTE_UPGRADE_MODE" "cosmovisor"
svote_upgrade_set_systemd_environment_key "$TMP_UNIT" "DAEMON_HOME" "/tmp/new"
wrapper_args='--serve-ui --ui-dist /opt/ui/dist --extra=a&b|c\z'
svote_upgrade_set_systemd_environment_key "$TMP_UNIT" "SVOTE_WRAPPER_SVOTED_START_ARGS" "$wrapper_args"

mode_lines=$(awk '/SVOTE_UPGRADE_MODE=/{print}' "$TMP_UNIT" | wc -l | tr -d '[:space:]')
[ "$mode_lines" = "1" ] || fail "expected one SVOTE_UPGRADE_MODE line, got ${mode_lines}"
grep -q 'Environment="SVOTE_UPGRADE_MODE=cosmovisor"' "$TMP_UNIT" || fail "SVOTE_UPGRADE_MODE was not set to cosmovisor"
grep -q 'Environment="DAEMON_HOME=/tmp/new"' "$TMP_UNIT" || fail "DAEMON_HOME was not replaced"
wrapper_line=$(sed -n 's/^Environment="SVOTE_WRAPPER_SVOTED_START_ARGS=\(.*\)"$/\1/p' "$TMP_UNIT" | head -n 1)
[ "$wrapper_line" = "$wrapper_args" ] || fail "wrapper args were mutated by env setter: ${wrapper_line}"
if grep -q 'SVOTE_UPGRADE_MODE=direct' "$TMP_UNIT"; then
  fail "stale direct-mode env line still present"
fi

echo "=== PASS: chain upgrade runtime-mode tests ==="
