#!/bin/bash
# ceremony.sh — EA Key Ceremony helper for the Zally chain.
#
# Usage:
#   ./ceremony.sh <command> [options]
#
# Commands:
#   register   Phase 1: submit your Pallas public key to the chain
#   deal       Phase 2: encrypt ea_sk for all registered validators and broadcast the deal (dealer only)
#   status     Print the current ceremony state
#   wait       Poll until the ceremony reaches CONFIRMED
#   reset      Reinitialize the ceremony back to REGISTERING
#   run        Fully automated dealer flow: register → confirm on-chain → deal → confirm tx → wait for CONFIRMED
#
# Environment overrides:
#   ZALLY_HOME            Node home dir  (default: ~/.zallyd)
#   ZALLY_CHAIN_ID        Chain ID       (default: zvote-1)
#   ZALLY_NODE_RPC        Tendermint RPC (default: tcp://localhost:26657)
#   ZALLY_REST_API        REST API base  (default: http://localhost:1318)
#   ZALLY_FROM            Key name       (default: validator)
#   ZALLY_KEYRING         Keyring backend (default: test)
#   ZALLY_EA_SK           Path to ea.sk  (default: $HOME_DIR/ea.sk)
#   ZALLY_EA_PK           Path to ea.pk  (default: $HOME_DIR/ea.pk)
#   ZALLY_PAYLOADS        Path to payloads.json (default: /tmp/payloads.json)

set -euo pipefail

# ─── Configuration ────────────────────────────────────────────────────────────

HOME_DIR="${ZALLY_HOME:-$HOME/.zallyd}"
CHAIN_ID="${ZALLY_CHAIN_ID:-zvote-1}"
NODE_RPC="${ZALLY_NODE_RPC:-tcp://localhost:26157}"
REST_API="${ZALLY_REST_API:-http://localhost:1318}"
FROM="${ZALLY_FROM:-validator}"
KEYRING="${ZALLY_KEYRING:-test}"
EA_SK="${ZALLY_EA_SK:-${HOME_DIR}/ea.sk}"
EA_PK="${ZALLY_EA_PK:-${HOME_DIR}/ea.pk}"
PAYLOADS="${ZALLY_PAYLOADS:-/tmp/payloads.json}"

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
  step "Phase 1 — Register Pallas key"
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

cmd_deal() {
  step "Phase 2 — Deal EA key"
  require_cmd jq
  require_cmd xxd

  [ -f "${EA_SK}" ] || die "ea.sk not found at ${EA_SK}"
  [ -f "${EA_PK}" ] || die "ea.pk not found at ${EA_PK}"

  STATUS=$(ceremony_status)
  if [ "${STATUS}" != "CEREMONY_STATUS_REGISTERING" ] && [ "${STATUS}" != "REGISTERING" ] && [ "${STATUS}" != "1" ]; then
    die "Ceremony is in state '${STATUS}', expected REGISTERING. Cannot deal."
  fi

  COUNT=$(validator_count)
  log "Registered validators: ${COUNT}"
  [ "${COUNT}" -eq 0 ] && die "No validators registered yet. Run 'register' on each validator first."

  # 2a — Encrypt ea_sk for each registered validator.
  step "Phase 2a — Encrypting ea_sk for ${COUNT} validator(s)"
  zallyd encrypt-ea-key "${EA_SK}" \
    --node "${REST_API}" \
    --output "${PAYLOADS}"
  log "Payloads written to ${PAYLOADS}"

  # 2b — Broadcast the deal.
  step "Phase 2b — Broadcasting deal transaction"
  EA_PK_HEX=$(xxd -p "${EA_PK}" | tr -d '\n')
  log "ea_pk (hex): ${EA_PK_HEX}"

  submit_tx_checked "Submitting deal-ea-key" \
    vote deal-ea-key "${EA_PK_HEX}" "${PAYLOADS}" \
    --from "${FROM}" \
    --keyring-backend "${KEYRING}" \
    --home "${HOME_DIR}" \
    --chain-id "${CHAIN_ID}" \
    --node "${NODE_RPC}"

  log "Deal accepted. Validators have 30 seconds to auto-ack."
  log "Run './ceremony.sh wait' to monitor until CONFIRMED."
}

cmd_wait() {
  step "Waiting for ceremony to reach CONFIRMED"
  require_cmd jq

  # The deal phase gives validators 30 s to ack; allow enough headroom.
  TIMEOUT=120
  ELAPSED=0
  INTERVAL=5
  # Track whether we have observed DEALT (state=2) at least once. We only
  # treat a return to REGISTERING as a rollback after the deal was visible
  # on-chain — otherwise an early poll can fire before the deal block commits
  # and falsely look like a reset.
  SAW_DEALT=0

  while true; do
    STATUS=$(ceremony_status)
    COUNT=$(validator_count)
    log "[${ELAPSED}s] state=${STATUS}  validators=${COUNT}"

    case "${STATUS}" in
      CEREMONY_STATUS_CONFIRMED|CONFIRMED|3)
        echo ""
        log "Ceremony CONFIRMED. The chain is ready for voting."
        return 0
        ;;
      CEREMONY_STATUS_DEALT|DEALT|2)
        SAW_DEALT=1
        ;;
      CEREMONY_STATUS_REGISTERING|REGISTERING|1)
        if [ "${SAW_DEALT}" = "1" ]; then
          # Genuine rollback: deal timed out with < 2/3 acks.
          die "Ceremony reset to REGISTERING (deal timed out with insufficient acks). Restart the ceremony."
        fi
        # Otherwise the deal block hasn't committed yet — keep polling.
        ;;
    esac

    if [ "${ELAPSED}" -ge "${TIMEOUT}" ]; then
      die "Timed out after ${TIMEOUT}s waiting for CONFIRMED. Current state: ${STATUS}"
    fi

    sleep "${INTERVAL}"
    ELAPSED=$((ELAPSED + INTERVAL))
  done
}

cmd_reset() {
  step "Resetting ceremony to REGISTERING"

  submit_tx_checked "Submitting reinitialize-election-authority" \
    vote reinitialize-election-authority \
    --from "${FROM}" \
    --keyring-backend "${KEYRING}" \
    --home "${HOME_DIR}" \
    --chain-id "${CHAIN_ID}" \
    --node "${NODE_RPC}"

  log "Waiting for block commit (~6s)..."
  sleep 6

  STATUS=$(ceremony_status)
  log "Ceremony state: ${STATUS}"
}

cmd_run() {
  step "EA Key Ceremony — fully automated dealer flow"
  require_cmd jq
  require_cmd xxd

  [ -f "${EA_SK}" ] || die "ea.sk not found at ${EA_SK}"
  [ -f "${EA_PK}" ] || die "ea.pk not found at ${EA_PK}"

  # Resolve our own validator operator address (valoper bech32) — that is what
  # the ceremony module stores in .ceremony.validators[].validator_address.
  MY_ADDR=$(zallyd keys show "${FROM}" -a \
    --bech val \
    --keyring-backend "${KEYRING}" \
    --home "${HOME_DIR}")
  log "Dealer address: ${MY_ADDR}"

  # Capture the account address (not valoper) for sequence polling.
  MY_ACCT=$(zallyd keys show "${FROM}" -a \
    --keyring-backend "${KEYRING}" \
    --home "${HOME_DIR}")

  # ── Step 1/4: Register Pallas key ────────────────────────────────────────
  step "Step 1/4 — Register Pallas key"
  # Snapshot sequence before submitting so we can detect when it increments.
  PRE_REG_SEQ=$(zallyd q auth account "${MY_ACCT}" \
    --node "${NODE_RPC}" --output json 2>/dev/null \
    | jq -r '.account.value.sequence // 0' 2>/dev/null || echo "0")
  submit_tx_checked "Submitting register-pallas-key" \
    vote register-pallas-key \
    --from "${FROM}" \
    --keyring-backend "${KEYRING}" \
    --home "${HOME_DIR}" \
    --chain-id "${CHAIN_ID}" \
    --node "${NODE_RPC}"

  # ── Step 2/4: Confirm our key is visible on-chain ────────────────────────
  step "Step 2/4 — Confirming registration on-chain"
  TIMEOUT=60
  ELAPSED=0
  INTERVAL=3
  while true; do
    # Accept either .address or .validator_address field names.
    FOUND=$(ceremony_json | jq -r \
      --arg addr "${MY_ADDR}" \
      '(.ceremony.validators // [])
       | map(.address // .validator_address)
       | map(select(. == $addr))
       | length' \
      2>/dev/null || echo "0")
    if [ "${FOUND}" = "1" ]; then
      COUNT=$(validator_count)
      log "Our key is on-chain. Total registered: ${COUNT}"
      # Poll until the node's account sequence has incremented past PRE_REG_SEQ,
      # ensuring deal-ea-key is constructed with the correct sequence number.
      SEQ_TIMEOUT=30
      SEQ_ELAPSED=0
      while true; do
        CURRENT_SEQ=$(zallyd q auth account "${MY_ACCT}" \
          --node "${NODE_RPC}" --output json 2>/dev/null \
          | jq -r '.account.value.sequence // 0' 2>/dev/null || echo "0")
        if [ "${CURRENT_SEQ}" -gt "${PRE_REG_SEQ}" ]; then
          log "Account sequence updated: ${CURRENT_SEQ}"
          break
        fi
        if [ "${SEQ_ELAPSED}" -ge "${SEQ_TIMEOUT}" ]; then
          log "WARN: sequence did not update after ${SEQ_TIMEOUT}s, proceeding anyway"
          break
        fi
        sleep 2
        SEQ_ELAPSED=$((SEQ_ELAPSED + 2))
      done
      break
    fi
    if [ "${ELAPSED}" -ge "${TIMEOUT}" ]; then
      die "Timed out after ${TIMEOUT}s waiting for our registration to appear on-chain."
    fi
    log "[${ELAPSED}s] Not visible yet, retrying in ${INTERVAL}s..."
    sleep "${INTERVAL}"
    ELAPSED=$((ELAPSED + INTERVAL))
  done

  # ── Step 3/4: Encrypt and deal ────────────────────────────────────────────
  step "Step 3/4 — Deal EA key"
  COUNT=$(validator_count)
  log "Encrypting ea_sk for ${COUNT} validator(s)..."
  zallyd encrypt-ea-key "${EA_SK}" \
    --node "${REST_API}" \
    --output "${PAYLOADS}"
  log "Payloads written to ${PAYLOADS}"

  EA_PK_HEX=$(xxd -p "${EA_PK}" | tr -d '\n')
  log "ea_pk (hex): ${EA_PK_HEX}"

  submit_tx_checked "Submitting deal-ea-key" \
    vote deal-ea-key "${EA_PK_HEX}" "${PAYLOADS}" \
    --from "${FROM}" \
    --keyring-backend "${KEYRING}" \
    --home "${HOME_DIR}" \
    --chain-id "${CHAIN_ID}" \
    --node "${NODE_RPC}"

  log "Deal on-chain. Validators have 30 seconds to auto-ack."

  # ── Step 4/4: Wait for CONFIRMED ─────────────────────────────────────────
  step "Step 4/4 — Waiting for CONFIRMED"
  cmd_wait

  echo ""
  echo "============================================="
  echo "       EA Key Ceremony Complete"
  echo "============================================="
  echo ""
  log "Chain ID: ${CHAIN_ID}"
  log "State:    CONFIRMED"
  log "The chain is ready to accept votes."
  echo ""
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
  deal)     cmd_deal     "$@" ;;
  status)   cmd_status   "$@" ;;
  wait)     cmd_wait     "$@" ;;
  reset)    cmd_reset    "$@" ;;
  run)      cmd_run      "$@" ;;
  *)        usage ;;
esac
