#!/usr/bin/env bash
# join-loop.sh — Background loop: register with admin, wait for funding, bond, exit when bonded.
#
# Environment (set by join.sh via systemd EnvironmentFile or launchd):
#   SVOTE_HOME          — validator data dir (~/.svoted)
#   VALIDATOR_ADDR      — operator account bech32
#   MONIKER             — validator moniker
#   VALIDATOR_URL       — public REST URL (https://...), may be empty
#   SVOTE_ADMIN_URL     — base URL of svoted admin (same host as /api/register-validator)
#   SVOTE_INSTALL_DIR   — directory containing create-val-tx (on PATH for ExecStart)

set -euo pipefail

: "${SVOTE_HOME:?SVOTE_HOME not set}"
: "${VALIDATOR_ADDR:?VALIDATOR_ADDR not set}"
: "${MONIKER:?MONIKER not set}"
: "${SVOTE_ADMIN_URL:?SVOTE_ADMIN_URL not set}"

VALIDATOR_URL="${VALIDATOR_URL:-}"
if [ -n "${SVOTE_INSTALL_DIR:-}" ]; then
  export PATH="${SVOTE_INSTALL_DIR}:${PATH}"
fi

cleanup_launchd_join_service() {
  if [ "$(uname -s)" != "Darwin" ]; then
    return 0
  fi

  local join_label="com.shielded-vote.join"
  local home_dir="${HOME:-}"
  if [ -z "${home_dir}" ]; then
    home_dir="$(cd ~ && pwd)"
  fi
  local join_plist="${home_dir}/Library/LaunchAgents/${join_label}.plist"

  echo "$(date -u +"%Y-%m-%dT%H:%M:%SZ") removing launchd join service ${join_label}"
  (
    sleep 1
    rm -f "${join_plist}"
    launchctl bootout "gui/$(id -u)/${join_label}" >/dev/null 2>&1 || true
  ) >/dev/null 2>&1 &
}

exit_bonded() {
  echo "$(date -u +"%Y-%m-%dT%H:%M:%SZ") $1"
  cleanup_launchd_join_service
  exit 0
}

echo "$(date -u +"%Y-%m-%dT%H:%M:%SZ") join-loop starting (moniker=${MONIKER})"

while true; do
  if [ -n "${VALIDATOR_URL}" ]; then
    ts=$(date +%s)
    payload=$(printf '%s' "{\"operator_address\":\"${VALIDATOR_ADDR}\",\"url\":\"${VALIDATOR_URL}\",\"moniker\":\"${MONIKER}\",\"timestamp\":${ts}}")
    if sig_json=$(svoted sign-arbitrary "${payload}" --from validator --keyring-backend test --home "${SVOTE_HOME}" 2>/dev/null); then
      sig=$(echo "${sig_json}" | jq -r '.signature')
      pub_key=$(echo "${sig_json}" | jq -r '.pub_key')
      body=$(jq -nc \
        --arg oa "${VALIDATOR_ADDR}" \
        --arg u "${VALIDATOR_URL}" \
        --arg m "${MONIKER}" \
        --argjson ts "${ts}" \
        --arg s "${sig}" \
        --arg pk "${pub_key}" \
        '{operator_address:$oa,url:$u,moniker:$m,timestamp:$ts,signature:$s,pub_key:$pk}')
      admin_base="${SVOTE_ADMIN_URL%/}"
      if resp=$(curl -fsSL -X POST "${admin_base}/api/register-validator" \
        -H "Content-Type: application/json" \
        -d "${body}" 2>/dev/null); then
        status=$(echo "${resp}" | jq -r '.status // empty')
        if [ "${status}" = "bonded" ]; then
          exit_bonded "register-validator returned bonded - exiting 0"
        fi
      fi
    fi
  fi

  bond_status=$(svoted query staking validators --home "${SVOTE_HOME}" --output json 2>/dev/null \
    | jq -r --arg moniker "${MONIKER}" '.validators[] | select(.description.moniker == $moniker) | .status' 2>/dev/null | head -1 || echo "")

  if [ "${bond_status}" = "BOND_STATUS_BONDED" ]; then
    exit_bonded "validator is bonded - exiting 0"
  else
    balance=$(svoted query bank balances "${VALIDATOR_ADDR}" --home "${SVOTE_HOME}" --output json 2>/dev/null \
      | jq -r '.balances[] | select(.denom == "usvote") | .amount' 2>/dev/null || echo "")
    if [ -n "${balance}" ] && [ "${balance}" != "0" ]; then
      echo "$(date -u +"%Y-%m-%dT%H:%M:%SZ") balance=${balance} usvote — attempting create-val-tx"
      create-val-tx --moniker "${MONIKER}" --amount 10000000usvote --home "${SVOTE_HOME}" --rpc-url tcp://localhost:26657 || true
      sleep 6
    fi
  fi

  sleep 30
done
