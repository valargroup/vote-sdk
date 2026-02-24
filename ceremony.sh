#!/bin/bash
# ceremony.sh — EA Key Ceremony helper for the Zally chain.
#
# Usage:
#   ./ceremony.sh <command> [options]
#
# Commands:
#   register   Register your Pallas public key to the global registry
#   status     Print the current ceremony state and registered validators
#
# The EA key ceremony is now automatic per voting round. When a round is
# created, eligible validators are snapshotted and the block proposer
# handles dealing and acking via PrepareProposal. The only manual step
# is the one-time Pallas key registration.
#
# Environment overrides:
#   ZALLY_HOME            Node home dir  (default: ~/.zallyd)
#   ZALLY_CHAIN_ID        Chain ID       (default: zvote-1)
#   ZALLY_NODE_RPC        Tendermint RPC (default: tcp://localhost:26657)
#   ZALLY_REST_API        REST API base  (default: http://localhost:1318)
#   ZALLY_FROM            Key name       (default: validator)
#   ZALLY_KEYRING         Keyring backend (default: test)

set -euo pipefail

# ─── Configuration ────────────────────────────────────────────────────────────

HOME_DIR="${ZALLY_HOME:-$HOME/.zallyd}"
CHAIN_ID="${ZALLY_CHAIN_ID:-zvote-1}"
NODE_RPC="${ZALLY_NODE_RPC:-tcp://localhost:26157}"
REST_API="${ZALLY_REST_API:-http://localhost:1318}"
FROM="${ZALLY_FROM:-validator}"
KEYRING="${ZALLY_KEYRING:-test}"

# ─── Helpers ──────────────────────────────────────────────────────────────────

log()  { echo "  $*"; }
step() { echo ""; echo "=== $* ==="; }
die()  { echo ""; echo "ERROR: $*" >&2; exit 1; }

require_cmd() {
  command -v "$1" > /dev/null 2>&1 || die "$1 is required but not found in PATH."
}

ceremony_json() {
  curl -fsSL "${REST_API}/zally/v1/ceremony" 2>/dev/null || echo "{}"
}

ceremony_status() {
  ceremony_json | jq -r '.ceremony.status // "UNKNOWN"'
}

validator_count() {
  ceremony_json | jq '.ceremony.validators | length // 0'
}

# submit_tx_checked <description> <zallyd tx sub-command and flags...>
# Runs the tx with --output json, logs the txhash, and dies on non-zero code.
# Do NOT pass --yes here; it is appended automatically.
submit_tx_checked() {
  local desc="$1"; shift
  log "${desc}..."
  local result code txhash raw_log
  result=$(zallyd tx "$@" --output json --yes 2>&1) || true
  code=$(echo "${result}"   | jq -r '.code    // 1'  2>/dev/null || echo "1")
  txhash=$(echo "${result}" | jq -r '.txhash  // ""' 2>/dev/null || echo "")
  [ -n "${txhash}" ] && log "TxHash: ${txhash}"
  if [ "${code}" != "0" ]; then
    raw_log=$(echo "${result}" | jq -r '.raw_log // ""' 2>/dev/null || echo "${result}")
    die "Transaction failed (code=${code}): ${raw_log}"
  fi
  log "Transaction accepted (code=0)."
}

# ─── Commands ─────────────────────────────────────────────────────────────────

cmd_status() {
  step "Ceremony status"
  require_cmd jq

  STATUS=$(ceremony_status)
  COUNT=$(validator_count)
  log "State:      ${STATUS}"
  log "Validators: ${COUNT} registered"

  echo ""
  zallyd q vote ceremony-state \
    --home "${HOME_DIR}" \
    --node "${NODE_RPC}" 2>/dev/null || true
}

cmd_register() {
  step "Register Pallas key"
  require_cmd jq

  submit_tx_checked "Submitting register-pallas-key" \
    vote register-pallas-key \
    --from "${FROM}" \
    --keyring-backend "${KEYRING}" \
    --home "${HOME_DIR}" \
    --chain-id "${CHAIN_ID}" \
    --node "${NODE_RPC}"

  log "Waiting for block commit (~6s)..."
  sleep 6

  COUNT=$(validator_count)
  log "Registered validators: ${COUNT}"
  log "Registration complete."
}

# ─── Dispatch ────────────────────────────────────────────────────────────────

usage() {
  grep '^#' "$0" | grep -v '^#!/' | sed 's/^# \{0,1\}//'
  exit 1
}

require_cmd zallyd
require_cmd curl

COMMAND="${1:-}"
shift || true

case "${COMMAND}" in
  register) cmd_register "$@" ;;
  status)   cmd_status   "$@" ;;
  *)        usage ;;
esac
