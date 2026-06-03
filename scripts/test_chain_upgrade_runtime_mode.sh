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

echo "=== systemd autodetect: infers --home from ExecStart ==="
TMP_AUTODETECT_UNIT="$(mktemp)"
trap 'rm -f "$TMP_UNIT" "$TMP_AUTODETECT_UNIT"; rm -rf "$TMP_MIGRATE"' EXIT
cat > "$TMP_AUTODETECT_UNIT" <<'EOF'
[Unit]
Description=svoted

[Service]
User=root
ExecStart=/usr/local/bin/svoted start --home /tmp/primary-home --serve-ui --ui-dist /opt/ui/dist
EOF
SERVICE_PATH="$TMP_AUTODETECT_UNIT"
SERVICE_NAME="svoted"
SVOTE_HOME="/tmp/default-home"
DAEMON_HOME="/tmp/default-home"
INSTALL_DIR="/tmp/default-install"
WRAPPER_BIN="/tmp/default-install/svoted-wrapper.sh"
COSMOVISOR_BIN="/tmp/default-install/cosmovisor"
svote_upgrade_autodetect_from_systemd_unit 0 0 >/dev/null
[ "$DAEMON_HOME" = "/tmp/primary-home" ] || fail "autodetect failed to infer home from ExecStart: ${DAEMON_HOME}"
[ "$SVOTE_HOME" = "/tmp/primary-home" ] || fail "autodetect failed to set SVOTE_HOME from ExecStart: ${SVOTE_HOME}"
[ "$INSTALL_DIR" = "/usr/local/bin" ] || fail "autodetect failed to infer install dir from ExecStart wrapper: ${INSTALL_DIR}"
[ "$WRAPPER_BIN" = "/tmp/default-install/svoted-wrapper.sh" ] || fail "autodetect must not replace WRAPPER_BIN from direct ExecStart: ${WRAPPER_BIN}"

echo "=== migrate patch: rewrites main unit for cosmovisor and removes drop-ins ==="
SERVICE_NAME="svoted"
SERVICE_PATH="${TMP_MIGRATE}/svoted.service"
DAEMON_HOME="${TMP_MIGRATE}/home"
INSTALL_DIR="${TMP_MIGRATE}/bin"
WRAPPER_BIN="${TMP_MIGRATE}/bin/svoted-wrapper.sh"
COSMOVISOR_BIN="${TMP_MIGRATE}/bin/cosmovisor"
SVOTE_WRAPPER_SVOTED_START_ARGS=""
mkdir -p "${DAEMON_HOME}/config" "${TMP_MIGRATE}/svoted.service.d" "${INSTALL_DIR}"
printf '#!/usr/bin/env bash\n' > "${COSMOVISOR_BIN}"
chmod +x "${COSMOVISOR_BIN}"
printf 'moniker = "primary-test"\n' > "${DAEMON_HOME}/config/config.toml"
printf '{"chain_id":"upgrade-test-1"}\n' > "${DAEMON_HOME}/config/genesis.json"

cat > "${SERVICE_PATH}" <<EOF
[Unit]
Description=svoted

[Service]
User=root
ExecStart=/usr/local/bin/svoted start --home ${DAEMON_HOME} --serve-ui --ui-dist /opt/ui/dist
Environment="SVOTE_UPGRADE_MODE=direct"
[Install]
WantedBy=multi-user.target
EOF

cat > "${TMP_MIGRATE}/svoted.service.d/primary.conf" <<EOF
[Service]
ExecStart=
ExecStart=/usr/local/bin/svoted start --home ${DAEMON_HOME} --serve-ui --ui-dist /opt/ui/dist
EOF

cat > "${TMP_MIGRATE}/svoted.service.d/zz-rogue.conf" <<EOF
[Service]
ExecStart=/usr/local/bin/svoted
EOF

cat > "${TMP_MIGRATE}/svoted.service.d/99-cosmovisor-migrate.conf" <<EOF
[Service]
Environment="SVOTE_UPGRADE_MODE=cosmovisor"
Environment="DAEMON_HOME=${DAEMON_HOME}"
EOF

svote_upgrade_patch_systemd_unit_for_cosmovisor >/dev/null

grep -q '^Description=svoted$' "${SERVICE_PATH}" || fail "service description not preserved"
grep -q '^User=root$' "${SERVICE_PATH}" || fail "service user not preserved"
grep -q "^ExecStart=${COSMOVISOR_BIN} run start --home ${DAEMON_HOME} --serve-ui --ui-dist /opt/ui/dist$" "${SERVICE_PATH}" || fail "service ExecStart is not cosmovisor run start"
grep -q 'Environment="SVOTE_UPGRADE_MODE=cosmovisor"' "${SERVICE_PATH}" || fail "service missing cosmovisor mode env"
grep -q "Environment=\"DAEMON_HOME=${DAEMON_HOME}\"" "${SERVICE_PATH}" || fail "service missing daemon home env"
grep -q 'Environment="DAEMON_NAME=svoted"' "${SERVICE_PATH}" || fail "service missing daemon name env"
grep -q "Environment=\"COSMOVISOR_BIN=${COSMOVISOR_BIN}\"" "${SERVICE_PATH}" || fail "service missing cosmovisor env"
grep -q '^Restart=on-failure$' "${SERVICE_PATH}" || fail "service missing restart policy"

[ ! -f "${TMP_MIGRATE}/svoted.service.d/primary.conf" ] || fail "primary.conf should be removed"
[ ! -f "${TMP_MIGRATE}/svoted.service.d/zz-rogue.conf" ] || fail "zz-rogue.conf should be removed"
[ ! -f "${TMP_MIGRATE}/svoted.service.d/99-cosmovisor-migrate.conf" ] || fail "legacy drop-in should be removed"
ls "${TMP_MIGRATE}/svoted.service.d/primary.conf.bak.pre-migrate."* >/dev/null 2>&1 || fail "primary.conf backup missing"
ls "${TMP_MIGRATE}/svoted.service.d/zz-rogue.conf.bak.pre-migrate."* >/dev/null 2>&1 || fail "zz-rogue.conf backup missing"
ls "${TMP_MIGRATE}/svoted.service.d/99-cosmovisor-migrate.conf.bak.pre-migrate."* >/dev/null 2>&1 || fail "legacy drop-in backup missing"

first_unit="$(cat "$SERVICE_PATH")"
svote_upgrade_patch_systemd_unit_for_cosmovisor >/dev/null
second_unit="$(cat "$SERVICE_PATH")"
[ "$first_unit" = "$second_unit" ] || fail "rewritten service changed across repeated runs"
[ ! -f "${TMP_MIGRATE}/svoted.service.d/primary.conf" ] || fail "primary.conf reappeared after second migrate"

echo "=== PASS: chain upgrade runtime-mode tests ==="
