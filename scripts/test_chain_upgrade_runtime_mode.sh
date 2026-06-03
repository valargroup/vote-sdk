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
TMP_MIGRATE="$(mktemp -d)"
trap 'rm -f "$TMP_UNIT"; rm -rf "$TMP_MIGRATE"' EXIT
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

echo "=== migrate patch: deterministic drop-in overrides primary direct config ==="
SERVICE_NAME="svoted"
SERVICE_PATH="${TMP_MIGRATE}/svoted.service"
DAEMON_HOME="${TMP_MIGRATE}/home"
INSTALL_DIR="${TMP_MIGRATE}/bin"
WRAPPER_BIN="${TMP_MIGRATE}/bin/svoted-wrapper.sh"
COSMOVISOR_BIN="${TMP_MIGRATE}/bin/cosmovisor"
SVOTE_WRAPPER_SVOTED_START_ARGS="--serve-ui --ui-dist /opt/ui/dist"
mkdir -p "${DAEMON_HOME}/config" "${TMP_MIGRATE}/svoted.service.d" "${INSTALL_DIR}"
printf 'moniker = "primary-test"\n' > "${DAEMON_HOME}/config/config.toml"
printf '{"chain_id":"upgrade-test-1"}\n' > "${DAEMON_HOME}/config/genesis.json"

cat > "${SERVICE_PATH}" <<EOF
[Unit]
Description=svoted

[Service]
ExecStart=/usr/local/bin/svoted start --home ${DAEMON_HOME}
Environment="SVOTE_UPGRADE_MODE=direct"
EOF

cat > "${TMP_MIGRATE}/svoted.service.d/primary.conf" <<EOF
[Service]
Environment="SVOTE_UPGRADE_MODE=direct"
ExecStart=
ExecStart=/usr/local/bin/svoted start --home ${DAEMON_HOME} --serve-ui --ui-dist /opt/ui/dist
EOF

svote_upgrade_patch_systemd_unit_for_cosmovisor >/dev/null

MIGRATE_DROPIN="${TMP_MIGRATE}/svoted.service.d/z-cosmovisor.conf"
[ -f "$MIGRATE_DROPIN" ] || fail "migrate drop-in was not created"
grep -q "^ExecStart=$" "$MIGRATE_DROPIN" || fail "drop-in missing ExecStart reset"
grep -q "^ExecStart=${WRAPPER_BIN}$" "$MIGRATE_DROPIN" || fail "drop-in missing wrapper ExecStart"
grep -q 'Environment="SVOTE_UPGRADE_MODE=cosmovisor"' "$MIGRATE_DROPIN" || fail "drop-in missing cosmovisor mode env"
grep -q "Environment=\"DAEMON_HOME=${DAEMON_HOME}\"" "$MIGRATE_DROPIN" || fail "drop-in missing daemon home env"
grep -q "Environment=\"SVOTE_HOME=${DAEMON_HOME}\"" "$MIGRATE_DROPIN" || fail "drop-in missing svote home env"
grep -q "Environment=\"MONIKER=primary-test\"" "$MIGRATE_DROPIN" || fail "drop-in missing moniker env"
grep -q "Environment=\"COSMOVISOR_BIN=${COSMOVISOR_BIN}\"" "$MIGRATE_DROPIN" || fail "drop-in missing cosmovisor bin env"
grep -q 'Environment="DAEMON_NAME=svoted"' "$MIGRATE_DROPIN" || fail "drop-in missing daemon name env"
grep -q 'Environment="DAEMON_ALLOW_DOWNLOAD_BINARIES=false"' "$MIGRATE_DROPIN" || fail "drop-in missing no-download env"
grep -q 'Environment="SVOTE_WRAPPER_SVOTED_START_ARGS=--serve-ui --ui-dist /opt/ui/dist"' "$MIGRATE_DROPIN" || fail "drop-in missing wrapper args env"

if grep -q '^ExecStart=' "${TMP_MIGRATE}/svoted.service.d/primary.conf"; then
  fail "primary.conf still contains ExecStart override"
fi
if grep -q 'SVOTE_UPGRADE_MODE=direct' "${TMP_MIGRATE}/svoted.service.d/primary.conf"; then
  fail "primary.conf still contains direct mode env"
fi

first_dropin="$(cat "$MIGRATE_DROPIN")"
svote_upgrade_patch_systemd_unit_for_cosmovisor >/dev/null
second_dropin="$(cat "$MIGRATE_DROPIN")"
[ "$first_dropin" = "$second_dropin" ] || fail "migrate drop-in changed across repeated runs"

echo "=== PASS: chain upgrade runtime-mode tests ==="
