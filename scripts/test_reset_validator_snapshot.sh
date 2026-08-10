#!/usr/bin/env bash
# test_reset_validator_snapshot.sh - unit-style tests for reset-validator-snapshot.sh.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${REPO_ROOT}/scripts/reset-validator-snapshot.sh"
TMPROOT="$(mktemp -d)"
SERVER_PID=""

cleanup() {
  if [ -n "${SERVER_PID}" ]; then
    kill "${SERVER_PID}" >/dev/null 2>&1 || true
    wait "${SERVER_PID}" >/dev/null 2>&1 || true
  fi
  rm -rf "${TMPROOT}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

require_tool() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required for this test"
}

write_fakes() {
  local bin_dir="$1"
  mkdir -p "${bin_dir}"

  cat > "${bin_dir}/uname" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = "-s" ]; then
  printf '%s\n' "${FAKE_UNAME:-Linux}"
else
  /usr/bin/uname "$@"
fi
EOF

  cat > "${bin_dir}/sudo" <<'EOF'
#!/usr/bin/env bash
exec "$@"
EOF

  cat > "${bin_dir}/systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  cat)
    [ "${FAKE_SYSTEMD_MISSING:-0}" = "1" ] && exit 1
    exit 0
    ;;
  stop)
    touch "${TEST_STATE_DIR}/systemctl-stop"
    exit 0
    ;;
  start)
    touch "${TEST_STATE_DIR}/systemctl-start"
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
EOF

  cat > "${bin_dir}/launchctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  bootout)
    touch "${TEST_STATE_DIR}/launchctl-bootout"
    exit 0
    ;;
  bootstrap)
    touch "${TEST_STATE_DIR}/launchctl-bootstrap"
    exit 0
    ;;
  print)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
EOF

  cat > "${bin_dir}/svoted" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  status)
    printf '{"sync_info":{"catching_up":false,"latest_block_height":"42"}}\n'
    ;;
  start)
    trap 'exit 0' TERM INT HUP
    while true; do sleep 1; done
    ;;
  *)
    exit 1
    ;;
esac
EOF

  chmod +x "${bin_dir}/uname" "${bin_dir}/sudo" "${bin_dir}/systemctl" \
    "${bin_dir}/launchctl" "${bin_dir}/svoted"
}

start_http_server() {
  local root="$1"
  local port_file="$2"
  local log_file="$3"

  python3 - "$root" "$port_file" >"${log_file}" 2>&1 <<'PY' &
import http.server
import os
import socketserver
import sys

root, port_file = sys.argv[1], sys.argv[2]
os.chdir(root)

class Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

with socketserver.ThreadingTCPServer(("127.0.0.1", 0), Quiet) as httpd:
    with open(port_file, "w", encoding="utf-8") as f:
        f.write(str(httpd.server_address[1]))
    httpd.serve_forever()
PY
  SERVER_PID="$!"

  for _ in $(seq 1 50); do
    [ -s "${port_file}" ] && return 0
    sleep 0.1
  done
  cat "${log_file}" >&2 || true
  fail "HTTP server did not start"
}

create_home() {
  local home="$1"
  local chain_id="${2:-zvote-1}"
  mkdir -p "${home}/config" "${home}/data/application.db" "${home}/keyring-test"
  printf '{"chain_id":"%s"}\n' "${chain_id}" > "${home}/config/genesis.json"
  printf '{"priv_key":"do-not-touch"}\n' > "${home}/config/priv_validator_key.json"
  printf '{"node_key":"do-not-touch"}\n' > "${home}/config/node_key.json"
  printf '{"height":"100","round":0,"step":0}\n' > "${home}/data/priv_validator_state.json"
  printf 'old-app-state\n' > "${home}/data/application.db/CURRENT"
  printf 'account-key\n' > "${home}/keyring-test/key"
  printf 'pallas-secret\n' > "${home}/pallas.sk"
  printf 'pallas-public\n' > "${home}/pallas.pk"
  printf 'ea-secret\n' > "${home}/ea.sk"
  printf 'ea-public\n' > "${home}/ea.pk"
  printf 'helper-queue\n' > "${home}/helper.db"
  printf 'round-share\n' > "${home}/share.active-round"
  printf 'round-coefficients\n' > "${home}/coeffs.active-round"
}

make_snapshot_archive() {
  local name="$1"
  local source_dir="${TMPROOT}/${name}-src"
  local tar_file="${SERVER_ROOT}/${name}.tar"
  local archive_file="${SERVER_ROOT}/${name}.tar.lz4"

  rm -rf "${source_dir}"
  mkdir -p "${source_dir}/data/application.db" "${source_dir}/data/cs.wal"
  printf '{"height":"1","round":0,"step":0}\n' > "${source_dir}/data/priv_validator_state.json"
  printf 'snapshot-app-state\n' > "${source_dir}/data/application.db/CURRENT"
  printf 'restored-wal\n' > "${source_dir}/data/cs.wal/wal"
  tar -C "${source_dir}" -cf "${tar_file}" data
  lz4 -q -f "${tar_file}" "${archive_file}"
  sha256_file "${archive_file}"
}

make_unsafe_archive() {
  local name="$1"
  local source_dir="${TMPROOT}/${name}-src"
  local tar_file="${SERVER_ROOT}/${name}.tar"
  local archive_file="${SERVER_ROOT}/${name}.tar.lz4"

  rm -rf "${source_dir}"
  mkdir -p "${source_dir}/config"
  printf 'unsafe\n' > "${source_dir}/config/evil"
  tar -C "${source_dir}" -cf "${tar_file}" config
  lz4 -q -f "${tar_file}" "${archive_file}"
  sha256_file "${archive_file}"
}

make_broken_archive() {
  local name="$1"
  local archive_file="${SERVER_ROOT}/${name}.tar.lz4"

  printf 'not an lz4 tar archive\n' > "${archive_file}"
  sha256_file "${archive_file}"
}

write_metadata() {
  local chain_id="$1"
  local archive_name="$2"
  local checksum="$3"
  cat > "${SERVER_ROOT}/latest.json" <<EOF
{
  "chain_id": "${chain_id}",
  "url": "${BASE_URL}/${archive_name}.tar.lz4",
  "checksum": "${checksum}",
  "height": 42,
  "date": "2026-05-05T17:09:26Z",
  "type": "pruned"
}
EOF
}

run_reset() {
  local home="$1"
  local state_dir="$2"
  local uname_value="${3:-Linux}"
  shift 3 || true

  mkdir -p "${state_dir}"
  HOME="${TMPROOT}/operator-home" \
  PATH="${FAKE_BIN}:${PATH}" \
  TEST_STATE_DIR="${state_dir}" \
  FAKE_UNAME="${uname_value}" \
  SVOTE_HOME="${home}" \
  SVOTE_SNAPSHOT_BASE_URL="${BASE_URL}" \
  SVOTE_POST_RESTART_SYNC_TIMEOUT=3 \
  SVOTE_TMPDIR="${TMPROOT}/tmp" \
  SVOTE_CHAIN_ID='' \
  "$@" \
    bash "${SCRIPT}"
}

assert_untouched_identity() {
  local home="$1"
  [ "$(cat "${home}/config/priv_validator_key.json")" = '{"priv_key":"do-not-touch"}' ] || fail "validator key changed"
  [ "$(cat "${home}/config/node_key.json")" = '{"node_key":"do-not-touch"}' ] || fail "node key changed"
  [ "$(cat "${home}/keyring-test/key")" = "account-key" ] || fail "keyring changed"
  [ "$(cat "${home}/pallas.sk")" = "pallas-secret" ] || fail "pallas key changed"
  [ "$(cat "${home}/pallas.pk")" = "pallas-public" ] || fail "pallas public key changed"
  [ "$(cat "${home}/ea.sk")" = "ea-secret" ] || fail "ea key changed"
  [ "$(cat "${home}/ea.pk")" = "ea-public" ] || fail "ea public key changed"
  [ "$(cat "${home}/helper.db")" = "helper-queue" ] || fail "helper queue changed"
  [ "$(cat "${home}/share.active-round")" = "round-share" ] || fail "round share changed"
  [ "$(cat "${home}/coeffs.active-round")" = "round-coefficients" ] || fail "round coefficients changed"
}

expect_stage_failure() {
  local name="$1"
  local home="$2"
  local state_dir="${TMPROOT}/state-${name}"
  shift 2

  set +e
  run_reset "${home}" "${state_dir}" Linux "$@" > "${TMPROOT}/${name}.log" 2>&1
  status=$?
  set -e
  [ "${status}" -ne 0 ] || {
    cat "${TMPROOT}/${name}.log" >&2
    fail "${name} unexpectedly succeeded"
  }
  [ ! -f "${state_dir}/systemctl-stop" ] || fail "${name} stopped service before failing"
  [ "$(cat "${home}/data/application.db/CURRENT")" = "old-app-state" ] || fail "${name} modified app data"
  if [ -f "${home}/data/priv_validator_state.json" ]; then
    [ "$(cat "${home}/data/priv_validator_state.json")" = '{"height":"100","round":0,"step":0}' ] || fail "${name} modified validator state"
  fi
  assert_untouched_identity "${home}"
}

require_tool python3
require_tool jq
require_tool lz4
require_tool tar
require_tool curl

FAKE_BIN="${TMPROOT}/bin"
SERVER_ROOT="${TMPROOT}/server"
mkdir -p "${SERVER_ROOT}" "${TMPROOT}/operator-home/Library/LaunchAgents" "${TMPROOT}/tmp"
write_fakes "${FAKE_BIN}"
touch "${TMPROOT}/operator-home/Library/LaunchAgents/com.shielded-vote.validator.plist"
start_http_server "${SERVER_ROOT}" "${TMPROOT}/server.port" "${TMPROOT}/server.log"
BASE_URL="http://127.0.0.1:$(cat "${TMPROOT}/server.port")"

echo "=== reset-validator-snapshot: linux success preserves validator state ==="
GOOD_SUM="$(make_snapshot_archive good)"
write_metadata "zvote-1" "good" "${GOOD_SUM}"
HOME1="${TMPROOT}/home-success-linux"
STATE1="${TMPROOT}/state-success-linux"
create_home "${HOME1}"
run_reset "${HOME1}" "${STATE1}" Linux > "${TMPROOT}/success-linux.log" 2>&1 || {
  cat "${TMPROOT}/success-linux.log" >&2
  fail "linux success case failed"
}
[ -f "${STATE1}/systemctl-stop" ] || fail "systemctl stop was not called"
[ -f "${STATE1}/systemctl-start" ] || fail "systemctl start was not called"
[ "$(cat "${HOME1}/data/application.db/CURRENT")" = "snapshot-app-state" ] || fail "snapshot app data not restored"
[ "$(cat "${HOME1}/data/priv_validator_state.json")" = '{"height":"100","round":0,"step":0}' ] || fail "local validator state not preserved"
[ ! -e "${HOME1}/data/cs.wal" ] || fail "consensus WAL was not removed"
assert_untouched_identity "${HOME1}"

echo "=== reset-validator-snapshot: explicit testnet override matches local genesis ==="
HOME_TESTNET="${TMPROOT}/home-success-testnet"
STATE_TESTNET="${TMPROOT}/state-success-testnet"
create_home "${HOME_TESTNET}" "svote-1"
write_metadata "svote-1" "good" "${GOOD_SUM}"
run_reset "${HOME_TESTNET}" "${STATE_TESTNET}" Linux env SVOTE_CHAIN_ID=svote-1 > "${TMPROOT}/success-testnet.log" 2>&1 || {
  cat "${TMPROOT}/success-testnet.log" >&2
  fail "testnet override success case failed"
}
[ -f "${STATE_TESTNET}/systemctl-stop" ] || fail "testnet override did not stop service"
[ -f "${STATE_TESTNET}/systemctl-start" ] || fail "testnet override did not start service"
assert_untouched_identity "${HOME_TESTNET}"

echo "=== reset-validator-snapshot: rejects chain override that disagrees with genesis ==="
HOME_OVERRIDE_MISMATCH="${TMPROOT}/home-override-mismatch"
create_home "${HOME_OVERRIDE_MISMATCH}"
write_metadata "zvote-1" "good" "${GOOD_SUM}"
expect_stage_failure "override-mismatch" "${HOME_OVERRIDE_MISMATCH}" env SVOTE_CHAIN_ID=svote-1

echo "=== reset-validator-snapshot: launchd success path ==="
write_metadata "zvote-1" "good" "${GOOD_SUM}"
HOME2="${TMPROOT}/home-success-darwin"
STATE2="${TMPROOT}/state-success-darwin"
create_home "${HOME2}"
run_reset "${HOME2}" "${STATE2}" Darwin > "${TMPROOT}/success-darwin.log" 2>&1 || {
  cat "${TMPROOT}/success-darwin.log" >&2
  fail "darwin success case failed"
}
[ -f "${STATE2}/launchctl-bootout" ] || fail "launchctl bootout was not called"
[ -f "${STATE2}/launchctl-bootstrap" ] || fail "launchctl bootstrap was not called"
[ "$(cat "${HOME2}/data/priv_validator_state.json")" = '{"height":"100","round":0,"step":0}' ] || fail "darwin validator state not preserved"

echo "=== reset-validator-snapshot: rejects wrong chain id ==="
HOME3="${TMPROOT}/home-wrong-chain"
create_home "${HOME3}"
write_metadata "wrong-chain" "good" "${GOOD_SUM}"
expect_stage_failure "wrong-chain" "${HOME3}"

echo "=== reset-validator-snapshot: rejects bad checksum ==="
HOME4="${TMPROOT}/home-bad-checksum"
create_home "${HOME4}"
write_metadata "zvote-1" "good" "0000000000000000000000000000000000000000000000000000000000000000"
expect_stage_failure "bad-checksum" "${HOME4}"

echo "=== reset-validator-snapshot: rejects unsafe archive paths ==="
HOME5="${TMPROOT}/home-unsafe-path"
create_home "${HOME5}"
UNSAFE_SUM="$(make_unsafe_archive unsafe)"
write_metadata "zvote-1" "unsafe" "${UNSAFE_SUM}"
expect_stage_failure "unsafe-path" "${HOME5}"

echo "=== reset-validator-snapshot: rejects missing local validator state ==="
HOME6="${TMPROOT}/home-missing-state"
create_home "${HOME6}"
rm -f "${HOME6}/data/priv_validator_state.json"
write_metadata "zvote-1" "good" "${GOOD_SUM}"
expect_stage_failure "missing-state" "${HOME6}"

echo "=== reset-validator-snapshot: rejects invalid metadata ==="
HOME7="${TMPROOT}/home-invalid-metadata"
create_home "${HOME7}"
printf 'not json\n' > "${SERVER_ROOT}/latest.json"
expect_stage_failure "invalid-metadata" "${HOME7}"

echo "=== reset-validator-snapshot: rejects broken archive ==="
HOME8="${TMPROOT}/home-broken-archive"
create_home "${HOME8}"
BROKEN_SUM="$(make_broken_archive broken)"
write_metadata "zvote-1" "broken" "${BROKEN_SUM}"
expect_stage_failure "broken-archive" "${HOME8}"

echo "=== reset-validator-snapshot: rejects missing service control ==="
HOME9="${TMPROOT}/home-missing-service"
STATE9="${TMPROOT}/state-missing-service"
create_home "${HOME9}"
write_metadata "zvote-1" "good" "${GOOD_SUM}"
set +e
run_reset "${HOME9}" "${STATE9}" Linux env FAKE_SYSTEMD_MISSING=1 > "${TMPROOT}/missing-service.log" 2>&1
status=$?
set -e
[ "${status}" -ne 0 ] || fail "missing service unexpectedly succeeded"
[ "$(cat "${HOME9}/data/application.db/CURRENT")" = "old-app-state" ] || fail "missing service modified app data"
[ ! -f "${STATE9}/systemctl-stop" ] || fail "missing service called stop"

echo "=== PASS: reset-validator-snapshot tests ==="
