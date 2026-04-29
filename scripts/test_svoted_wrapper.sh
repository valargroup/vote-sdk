#!/usr/bin/env bash
# test_svoted_wrapper.sh — fast unit-style tests for svoted-wrapper.sh.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WRAPPER="${REPO_ROOT}/scripts/svoted-wrapper.sh"
TMPDIR="$(mktemp -d)"

cleanup() {
  rm -rf "${TMPDIR}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

wait_for_file() {
  local file="$1"
  local deadline="$2"
  local i=0
  while [ "${i}" -lt "${deadline}" ]; do
    [ -f "${file}" ] && return 0
    sleep 1
    i=$((i + 1))
  done
  return 1
}

write_fakes() {
  local bin_dir="$1"
  mkdir -p "${bin_dir}"

  cat > "${bin_dir}/svoted" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "$1" in
  start)
    trap 'exit 0' TERM INT HUP
    touch "${STATE_DIR}/svoted-started"
    while true; do sleep 1; done
    ;;
  status)
    printf '{"sync_info":{"catching_up":false,"latest_block_height":"1"}}\n'
    ;;
  keys)
    if printf '%s\n' "$@" | grep -q -- '--bech'; then
      printf 'svvaloper1wrapper\n'
    else
      printf 'sv1wrapper\n'
    fi
    ;;
  query)
    case "$2" in
      staking)
        if [ -f "${STATE_DIR}/bonded" ]; then
          printf '{"validator":{"status":"BOND_STATUS_BONDED"}}\n'
        else
          printf '{"validator":{"status":"BOND_STATUS_UNBONDED"}}\n'
        fi
        ;;
      bank)
        balance=0
        [ -f "${STATE_DIR}/balance" ] && balance="$(cat "${STATE_DIR}/balance")"
        printf '{"balances":[{"denom":"usvote","amount":"%s"}]}\n' "${balance}"
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
  *)
    exit 1
    ;;
esac
EOF

  cat > "${bin_dir}/create-val-tx" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "${STATE_DIR}/create-val-tx.args"
touch "${STATE_DIR}/bonded"
EOF

  chmod +x "${bin_dir}/svoted" "${bin_dir}/create-val-tx"
}

start_wrapper() {
  local state_dir="$1"
  local home_dir="$2"
  local log_file="$3"
  STATE_DIR="${state_dir}" \
  PATH="${TMPDIR}/bin:${PATH}" \
  SVOTE_HOME="${home_dir}" \
  VALIDATOR_ADDR="sv1wrapper" \
  VALIDATOR_VALOPER="svvaloper1wrapper" \
  MONIKER="wrapper-test" \
  SVOTE_INSTALL_DIR="${TMPDIR}/bin" \
  SVOTE_WRAPPER_SYNC_POLL_SECONDS=1 \
  SVOTE_WRAPPER_BALANCE_POLL_SECONDS=1 \
  SVOTE_WRAPPER_POST_TX_SLEEP_SECONDS=1 \
    bash "${WRAPPER}" > "${log_file}" 2>&1 &
  echo "$!"
}

stop_wrapper() {
  local pid="$1"
  kill "${pid}" >/dev/null 2>&1 || true
  wait "${pid}" >/dev/null 2>&1 || true
}

write_fakes "${TMPDIR}/bin"

echo "=== svoted-wrapper: already bonded recreates marker without tx ==="
STATE1="${TMPDIR}/state1"
HOME1="${TMPDIR}/home1"
mkdir -p "${STATE1}" "${HOME1}"
touch "${STATE1}/bonded"
PID1="$(start_wrapper "${STATE1}" "${HOME1}" "${TMPDIR}/case1.log")"
wait_for_file "${HOME1}/join-complete" 5 || {
  cat "${TMPDIR}/case1.log" >&2
  fail "join-complete not written for bonded validator"
}
stop_wrapper "${PID1}"
[ ! -f "${STATE1}/create-val-tx.args" ] || fail "create-val-tx should not run when already bonded"

echo "=== svoted-wrapper: funded unbonded account creates validator ==="
STATE2="${TMPDIR}/state2"
HOME2="${TMPDIR}/home2"
mkdir -p "${STATE2}" "${HOME2}"
echo 10500000 > "${STATE2}/balance"
PID2="$(start_wrapper "${STATE2}" "${HOME2}" "${TMPDIR}/case2.log")"
wait_for_file "${HOME2}/join-complete" 8 || {
  cat "${TMPDIR}/case2.log" >&2
  fail "join-complete not written after funded create-val-tx"
}
stop_wrapper "${PID2}"
[ -f "${STATE2}/create-val-tx.args" ] || fail "create-val-tx was not called"
COUNT="$(wc -l < "${STATE2}/create-val-tx.args" | tr -d '[:space:]')"
[ "${COUNT}" = "1" ] || fail "create-val-tx called ${COUNT} times"

echo "=== PASS: svoted-wrapper tests ==="
