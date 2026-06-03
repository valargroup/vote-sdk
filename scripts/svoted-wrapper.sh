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
COSMOVISOR_BIN="${COSMOVISOR_BIN:-${SVOTE_INSTALL_DIR:-}/cosmovisor}"
SVOTE_UPGRADE_MODE="${SVOTE_UPGRADE_MODE:-direct}"
case "${SVOTE_UPGRADE_MODE}" in
  legacy) SVOTE_UPGRADE_MODE="direct" ;;
esac
SVOTE_RPC_URL="${SVOTE_RPC_URL:-tcp://localhost:26657}"
JOIN_COMPLETE_FILE="${SVOTE_JOIN_COMPLETE_FILE:-${SVOTE_HOME}/join-complete}"
JOIN_STAKE_USVOTE="${SVOTE_JOIN_STAKE_USVOTE:-10000000}"
case "${JOIN_STAKE_USVOTE}" in
  ''|*[!0-9]*) JOIN_STAKE_USVOTE=10000000 ;;
esac
SYNC_POLL_SECONDS="${SVOTE_WRAPPER_SYNC_POLL_SECONDS:-5}"
BALANCE_POLL_SECONDS="${SVOTE_WRAPPER_BALANCE_POLL_SECONDS:-30}"
POST_TX_SLEEP_SECONDS="${SVOTE_WRAPPER_POST_TX_SLEEP_SECONDS:-6}"
SVOTE_WRAPPER_SVOTED_START_ARGS="${SVOTE_WRAPPER_SVOTED_START_ARGS:-${SVOTED_START_ARGS:-}}"
SVOTED_EXTRA_ARGS=()
if [ -n "${SVOTE_WRAPPER_SVOTED_START_ARGS}" ]; then
  # shellcheck disable=SC2206
  SVOTED_EXTRA_ARGS=( ${SVOTE_WRAPPER_SVOTED_START_ARGS} )
fi

SVOTED_PID=""

# log ...
# Print a UTC timestamped line to stdout.
log() {
  echo "$(date -u +"%Y-%m-%dT%H:%M:%SZ") $*"
}

# child_running
# Return 0 when SVOTED_PID refers to a live, non-zombie process.
child_running() {
  local state
  [ -n "${SVOTED_PID}" ] && kill -0 "${SVOTED_PID}" >/dev/null 2>&1 || return 1
  state=$(ps -p "${SVOTED_PID}" -o stat= 2>/dev/null | awk '{print $1}')
  case "${state}" in
    Z*) return 1 ;;
  esac
  return 0
}

# exit_with_child_status
# wait on SVOTED_PID and exit with its status (0 when pid unset).
exit_with_child_status() {
  local status=0
  if [ -n "${SVOTED_PID}" ]; then
    wait "${SVOTED_PID}" >/dev/null 2>&1
    status=$?
  fi
  log "svoted exited with status ${status}"
  exit "${status}"
}

# stop_child
# Signal handler: forward TERM/INT/HUP to svoted child, then exit_with_child_status.
stop_child() {
  trap - TERM INT HUP
  if child_running; then
    log "forwarding stop signal to svoted pid ${SVOTED_PID}"
    kill "${SVOTED_PID}" >/dev/null 2>&1 || true
  fi
  exit_with_child_status
}

# sleep_checked seconds
# Sleep in 1s steps; exit_with_child_status if the svoted child dies early.
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

# derive_valoper
# Print validator operator address from env or keyring; empty output on failure.
derive_valoper() {
  if [ -n "${VALIDATOR_VALOPER:-}" ]; then
    echo "${VALIDATOR_VALOPER}"
    return 0
  fi

  "${SVOTED_BIN}" keys show validator --bech val -a \
    --keyring-backend test \
    --home "${SVOTE_HOME}" 2>/dev/null
}

# derive_chain_id
# Print chain ID from SVOTE_CHAIN_ID or genesis.json; empty output when unavailable.
derive_chain_id() {
  if [ -n "${SVOTE_CHAIN_ID:-}" ]; then
    echo "${SVOTE_CHAIN_ID}"
    return 0
  fi

  jq -r '.chain_id // empty' "${SVOTE_HOME}/config/genesis.json" 2>/dev/null
}

# is_synced
# Return 0 when local RPC reports catching_up=false and height > 0.
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

# is_bonded valoper
# Return 0 when staking query reports BOND_STATUS_BONDED for valoper.
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

# balance_usvote
# Print validator usvote balance from bank query; print 0 when query fails or denom absent.
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

# mark_join_complete
# Create/truncate JOIN_COMPLETE_FILE marker after successful bonding path.
mark_join_complete() {
  mkdir -p "$(dirname "${JOIN_COMPLETE_FILE}")"
  : > "${JOIN_COMPLETE_FILE}"
  log "join complete marker written: ${JOIN_COMPLETE_FILE}"
}

trap stop_child TERM INT HUP

# start_svoted_process
# Background svoted via cosmovisor or direct start; exit 1 in cosmovisor mode when binaries missing.
start_svoted_process() {
  local genesis_bin="${SVOTE_HOME}/cosmovisor/genesis/bin/svoted"
  if [ "${SVOTE_UPGRADE_MODE}" = "cosmovisor" ]; then
    if [ ! -x "${COSMOVISOR_BIN}" ]; then
      log "ERROR: cosmovisor binary missing at ${COSMOVISOR_BIN} (SVOTE_UPGRADE_MODE=cosmovisor)"
      exit 1
    fi
    if [ ! -x "${genesis_bin}" ]; then
      log "ERROR: cosmovisor genesis binary missing at ${genesis_bin}"
      exit 1
    fi
    export DAEMON_HOME="${SVOTE_HOME}"
    export DAEMON_NAME="svoted"
    export DAEMON_ALLOW_DOWNLOAD_BINARIES="${DAEMON_ALLOW_DOWNLOAD_BINARIES:-false}"
    log "starting svoted via cosmovisor (home=${SVOTE_HOME}, extra_args=${SVOTE_WRAPPER_SVOTED_START_ARGS:-<none>})"
    if [ "${#SVOTED_EXTRA_ARGS[@]}" -gt 0 ]; then
      "${COSMOVISOR_BIN}" run start --home "${SVOTE_HOME}" "${SVOTED_EXTRA_ARGS[@]}" &
    else
      "${COSMOVISOR_BIN}" run start --home "${SVOTE_HOME}" &
    fi
    return
  fi
  log "starting svoted directly (home=${SVOTE_HOME}, extra_args=${SVOTE_WRAPPER_SVOTED_START_ARGS:-<none>})"
  if [ "${#SVOTED_EXTRA_ARGS[@]}" -gt 0 ]; then
    "${SVOTED_BIN}" start --home "${SVOTE_HOME}" "${SVOTED_EXTRA_ARGS[@]}" &
  else
    "${SVOTED_BIN}" start --home "${SVOTE_HOME}" &
  fi
}

log "starting svoted via wrapper (home=${SVOTE_HOME}, moniker=${MONIKER}, upgrade_mode=${SVOTE_UPGRADE_MODE})"
start_svoted_process
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

SVOTE_CHAIN_ID="$(derive_chain_id || echo "")"
if [ -z "${SVOTE_CHAIN_ID}" ]; then
  log "chain ID unavailable; skipping join automation"
  exit_with_child_status
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
      --chain-id "${SVOTE_CHAIN_ID}" \
      --rpc-url "${SVOTE_RPC_URL}" || true
    sleep_checked "${POST_TX_SLEEP_SECONDS}"
  else
    log "waiting for validator funding: balance=${balance} usvote, required=${JOIN_STAKE_USVOTE} usvote"
  fi

  sleep_checked "${BALANCE_POLL_SECONDS}"
done
