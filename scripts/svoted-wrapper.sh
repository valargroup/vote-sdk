#!/usr/bin/env bash
# svoted-wrapper.sh — run svoted and complete one-time validator bonding.

set -u

: "${SVOTE_HOME:?SVOTE_HOME not set}"
: "${MONIKER:?MONIKER not set}"

if [ -n "${SVOTE_INSTALL_DIR:-}" ]; then
  export PATH="${SVOTE_INSTALL_DIR}:${PATH}"
fi

SVOTED_BIN="${SVOTED_BIN:-svoted}"
CREATE_VAL_TX_BIN="${CREATE_VAL_TX_BIN:-create-val-tx}"
SVOTE_RPC_URL="${SVOTE_RPC_URL:-tcp://localhost:26657}"
JOIN_COMPLETE_FILE="${SVOTE_JOIN_COMPLETE_FILE:-${SVOTE_HOME}/join-complete}"
JOIN_STAKE_USVOTE="${SVOTE_JOIN_STAKE_USVOTE:-10000000}"
case "${JOIN_STAKE_USVOTE}" in
  ''|*[!0-9]*) JOIN_STAKE_USVOTE=10000000 ;;
esac
SYNC_POLL_SECONDS="${SVOTE_WRAPPER_SYNC_POLL_SECONDS:-5}"
BALANCE_POLL_SECONDS="${SVOTE_WRAPPER_BALANCE_POLL_SECONDS:-30}"
POST_TX_SLEEP_SECONDS="${SVOTE_WRAPPER_POST_TX_SLEEP_SECONDS:-6}"

SVOTED_PID=""

log() {
  echo "$(date -u +"%Y-%m-%dT%H:%M:%SZ") $*"
}

child_running() {
  local state
  [ -n "${SVOTED_PID}" ] && kill -0 "${SVOTED_PID}" >/dev/null 2>&1 || return 1
  state=$(ps -p "${SVOTED_PID}" -o stat= 2>/dev/null | awk '{print $1}')
  case "${state}" in
    Z*) return 1 ;;
  esac
  return 0
}

exit_with_child_status() {
  local status=0
  if [ -n "${SVOTED_PID}" ]; then
    wait "${SVOTED_PID}" >/dev/null 2>&1
    status=$?
  fi
  log "svoted exited with status ${status}"
  exit "${status}"
}

stop_child() {
  trap - TERM INT HUP
  if child_running; then
    log "forwarding stop signal to svoted pid ${SVOTED_PID}"
    kill "${SVOTED_PID}" >/dev/null 2>&1 || true
  fi
  exit_with_child_status
}

sleep_checked() {
  local seconds="$1"
  local elapsed=0
  while [ "${elapsed}" -lt "${seconds}" ]; do
    if ! child_running; then
      exit_with_child_status
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
}

derive_valoper() {
  if [ -n "${VALIDATOR_VALOPER:-}" ]; then
    echo "${VALIDATOR_VALOPER}"
    return 0
  fi

  "${SVOTED_BIN}" keys show validator --bech val -a \
    --keyring-backend test \
    --home "${SVOTE_HOME}" 2>/dev/null
}

is_synced() {
  local status catching_up height
  status=$("${SVOTED_BIN}" status --home "${SVOTE_HOME}" --node "${SVOTE_RPC_URL}" 2>/dev/null || echo "")
  if [ -z "${status}" ]; then
    return 1
  fi
  catching_up=$(echo "${status}" | jq -r '.sync_info.catching_up' 2>/dev/null || echo "true")
  height=$(echo "${status}" | jq -r '.sync_info.latest_block_height' 2>/dev/null || echo "0")
  catching_up="${catching_up:-true}"
  height="${height:-0}"
  [ "${catching_up}" = "null" ] && catching_up="true"
  [ "${height}" = "null" ] && height="0"
  if [ "${catching_up}" = "false" ] && [ "${height}" != "0" ]; then
    return 0
  fi
  return 1
}

is_bonded() {
  local valoper="$1"
  local out status
  out=$("${SVOTED_BIN}" query staking validator "${valoper}" \
    --home "${SVOTE_HOME}" \
    --node "${SVOTE_RPC_URL}" \
    --output json 2>/dev/null || echo "")
  if [ -z "${out}" ]; then
    return 1
  fi
  status=$(echo "${out}" | jq -r '.validator.status // .status // empty' 2>/dev/null || echo "")
  [ "${status}" = "BOND_STATUS_BONDED" ]
}

balance_usvote() {
  local balances
  balances=$("${SVOTED_BIN}" query bank balances "${VALIDATOR_ADDR}" \
    --home "${SVOTE_HOME}" \
    --node "${SVOTE_RPC_URL}" \
    --output json 2>/dev/null || echo "")
  if [ -z "${balances}" ]; then
    echo "0"
    return 0
  fi
  echo "${balances}" | jq -r '.balances[]? | select(.denom == "usvote") | .amount' 2>/dev/null | head -1
}

mark_join_complete() {
  mkdir -p "$(dirname "${JOIN_COMPLETE_FILE}")"
  : > "${JOIN_COMPLETE_FILE}"
  log "join complete marker written: ${JOIN_COMPLETE_FILE}"
}

trap stop_child TERM INT HUP

log "starting svoted via wrapper (home=${SVOTE_HOME}, moniker=${MONIKER})"
"${SVOTED_BIN}" start --home "${SVOTE_HOME}" &
SVOTED_PID=$!
log "svoted started with pid ${SVOTED_PID}"

VALIDATOR_ADDR="${VALIDATOR_ADDR:-$("${SVOTED_BIN}" keys show validator -a --keyring-backend test --home "${SVOTE_HOME}" 2>/dev/null || echo "")}"
if [ -z "${VALIDATOR_ADDR}" ]; then
  log "validator account address unavailable; skipping join automation"
  exit_with_child_status
fi

VALIDATOR_VALOPER="$(derive_valoper || echo "")"
if [ -z "${VALIDATOR_VALOPER}" ]; then
  log "validator operator address unavailable; skipping join automation"
  exit_with_child_status
fi

log "waiting for local node to finish syncing"
while ! is_synced; do
  sleep_checked "${SYNC_POLL_SECONDS}"
done
log "local node is synced"

if is_bonded "${VALIDATOR_VALOPER}"; then
  mark_join_complete
  log "validator is already bonded; join automation disabled"
  exit_with_child_status
fi

if [ -f "${JOIN_COMPLETE_FILE}" ]; then
  log "join-complete marker exists but validator is not bonded; continuing join automation"
fi

while true; do
  if is_bonded "${VALIDATOR_VALOPER}"; then
    mark_join_complete
    log "validator is bonded; join automation complete"
    exit_with_child_status
  fi

  balance="$(balance_usvote)"
  balance="${balance:-0}"
  case "${balance}" in
    ''|*[!0-9]*) balance=0 ;;
  esac

  if [ "${balance}" -ge "${JOIN_STAKE_USVOTE}" ]; then
    log "balance=${balance} usvote; attempting create-val-tx"
    "${CREATE_VAL_TX_BIN}" \
      --moniker "${MONIKER}" \
      --amount "${JOIN_STAKE_USVOTE}usvote" \
      --home "${SVOTE_HOME}" \
      --rpc-url "${SVOTE_RPC_URL}" || true
    sleep_checked "${POST_TX_SLEEP_SECONDS}"
  else
    log "waiting for validator funding: balance=${balance} usvote, required=${JOIN_STAKE_USVOTE} usvote"
  fi

  sleep_checked "${BALANCE_POLL_SECONDS}"
done
