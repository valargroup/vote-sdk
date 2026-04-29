#!/usr/bin/env bash
#
# reset-join.sh — Reset a secondary validator and join an existing chain.
#
# Designed for CI (sdk-chain-reset.yml): assumes svoted + create-val-tx are
# already installed at /opt/shielded-vote/current/bin/ and on PATH.
#
# 1. Wipes existing chain state
# 2. Downloads genesis from DO Spaces (uploaded by the reset-primary job)
# 3. Discovers the primary's P2P node-id via its REST API
# 4. Imports deterministic validator key from VAL_PRIVKEY + generates Pallas/EA keys
# 5. Configures persistent_peers, broadcast timeout, REST API
# 6. Starts svoted via systemctl
# 7. Waits for sync, verifies funding (pre-funded by CI), registers as validator
#
# Required env:
#   GENESIS_URL       URL to download genesis.json (e.g. https://vote.fra1.digitaloceanspaces.com/genesis.json)
#   PRIMARY_REST_URL  Primary's REST API base URL (e.g. https://vote-chain-primary.example.com)
#   VAL_PRIVKEY       Hex-encoded validator private key (deterministic address for pre-funding)
#
# Optional env:
#   HOME_DIR          svoted home directory (default: /opt/shielded-vote/.svoted)
#   CHAIN_ID          Cosmos chain ID (default: svote-1)
#   MONIKER           Validator moniker (default: valarg-secondary)

set -euo pipefail

GENESIS_URL="${GENESIS_URL:?GENESIS_URL must be set}"
PRIMARY_REST_URL="${PRIMARY_REST_URL:?PRIMARY_REST_URL must be set}"
VAL_PRIVKEY="${VAL_PRIVKEY:?VAL_PRIVKEY must be set}"
HOME_DIR="${HOME_DIR:-/opt/shielded-vote/.svoted}"
CHAIN_ID="${CHAIN_ID:-svote-1}"
MONIKER="${MONIKER:-valarg-secondary}"

echo "=== Reset-join: ${MONIKER} validator ==="
echo "  Genesis:  ${GENESIS_URL}"
echo "  Primary:  ${PRIMARY_REST_URL}"
echo "  Home:     ${HOME_DIR}"
echo ""

# ─── Stop svoted if running ──────────────────────────────────────────────────

if systemctl is-active --quiet svoted 2>/dev/null; then
  echo "Stopping svoted..."
  systemctl stop svoted
fi

# ─── Wipe existing state ─────────────────────────────────────────────────────

echo "Wiping ${HOME_DIR} contents..."
rm -rf "${HOME_DIR:?}"/*
mkdir -p "${HOME_DIR}"

# ─── Initialize node ─────────────────────────────────────────────────────────

echo "Initializing node (moniker=${MONIKER}, chain=${CHAIN_ID})..."
svoted init "${MONIKER}" --chain-id "${CHAIN_ID}" --home "${HOME_DIR}" > /dev/null 2>&1

# ─── Download genesis ─────────────────────────────────────────────────────────

echo "Downloading genesis from ${GENESIS_URL}..."
curl -fsSL -o "${HOME_DIR}/config/genesis.json" "${GENESIS_URL}"
svoted genesis validate-genesis --home "${HOME_DIR}" > /dev/null 2>&1
echo "Genesis validated."

# ─── Discover primary peer ────────────────────────────────────────────────────

echo "Fetching primary node info from ${PRIMARY_REST_URL}..."
NODE_INFO=$(curl -fsSL "${PRIMARY_REST_URL}/cosmos/base/tendermint/v1beta1/node_info")
NODE_ID=$(echo "$NODE_INFO" | jq -r '.default_node_info.default_node_id // .default_node_info.id // empty')
LISTEN_ADDR=$(echo "$NODE_INFO" | jq -r '.default_node_info.listen_addr // empty')

if [ -z "$NODE_ID" ]; then
  echo "ERROR: Could not fetch node_id from ${PRIMARY_REST_URL}"
  exit 1
fi

PRIMARY_HOST=$(echo "$PRIMARY_REST_URL" | sed -E 's|^https?://||; s|:[0-9]+$||; s|/.*||')
P2P_PORT=$(echo "$LISTEN_ADDR" | sed -E 's|.*:([0-9]+)$|\1|')
P2P_PORT="${P2P_PORT:-26656}"
PERSISTENT_PEERS="${NODE_ID}@${PRIMARY_HOST}:${P2P_PORT}"
echo "Persistent peers: ${PERSISTENT_PEERS}"

# ─── Import validator key + generate ceremony keys ───────────────────────────

echo "Importing validator key..."
svoted keys import-hex validator "${VAL_PRIVKEY}" --keyring-backend test --home "${HOME_DIR}"
VALIDATOR_ADDR=$(svoted keys show validator -a --keyring-backend test --home "${HOME_DIR}")
echo "Validator address: ${VALIDATOR_ADDR}"

echo "Generating Pallas + EA keypairs..."
svoted pallas-keygen --home "${HOME_DIR}"
svoted ea-keygen --home "${HOME_DIR}"

# ─── Configure config.toml ───────────────────────────────────────────────────

CONFIG_TOML="${HOME_DIR}/config/config.toml"
sed -i "s|persistent_peers = \"\"|persistent_peers = \"${PERSISTENT_PEERS}\"|" "${CONFIG_TOML}"

# ─── Configure app.toml ──────────────────────────────────────────────────────

APP_TOML="${HOME_DIR}/config/app.toml"
sed -i '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "${APP_TOML}"
sed -i '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/' "${APP_TOML}"
sed -i "s|address = \"tcp://localhost:1317\"|address = \"tcp://0.0.0.0:1317\"|" "${APP_TOML}"
sed -i "s|\\\$HOME/.svoted|${HOME_DIR}|g" "${APP_TOML}"

# ─── Start svoted ─────────────────────────────────────────────────────────────

echo "Starting svoted..."
systemctl daemon-reload
systemctl start svoted

# ─── Wait for sync ────────────────────────────────────────────────────────────

echo "Waiting for node to sync..."
sleep 5
while true; do
  STATUS=$(svoted status --home "${HOME_DIR}" 2>/dev/null || echo "")
  if [ -z "$STATUS" ]; then
    sleep 2
    continue
  fi

  CATCHING_UP=$(echo "$STATUS" | jq -r '.sync_info.catching_up' 2>/dev/null || echo "true")
  HEIGHT=$(echo "$STATUS" | jq -r '.sync_info.latest_block_height' 2>/dev/null || echo "0")
  echo "  height: ${HEIGHT}, catching_up: ${CATCHING_UP}"

  if [ "$CATCHING_UP" = "false" ]; then
    echo "Node is synced."
    break
  fi
  sleep 5
done

# ─── Register as validator ────────────────────────────────────────────────────
# Assumes the account has already been funded externally (e.g. by CI).

echo "Verifying account ${VALIDATOR_ADDR} is funded..."
for i in $(seq 1 60); do
  BALANCE=$(svoted query bank balances "${VALIDATOR_ADDR}" --home "${HOME_DIR}" --output json 2>/dev/null \
    | jq -r '.balances[] | select(.denom == "usvote") | .amount' 2>/dev/null || echo "")
  if [ -n "$BALANCE" ] && [ "$BALANCE" != "0" ]; then
    echo "Account funded (${BALANCE} usvote)."
    break
  fi
  if [ "$i" -eq 60 ]; then
    echo "ERROR: Account ${VALIDATOR_ADDR} not funded after 5 minutes." >&2
    exit 1
  fi
  sleep 5
done

echo "Registering as validator..."
if ! create-val-tx --moniker "${MONIKER}" --amount 10000000usvote --home "${HOME_DIR}" --rpc-url tcp://localhost:26657; then
  echo "ERROR: create-val-tx failed." >&2
  exit 1
fi

sleep 6
IS_VALIDATOR=$(svoted query staking validators --home "${HOME_DIR}" --output json 2>/dev/null \
  | jq -r ".validators[] | select(.description.moniker == \"${MONIKER}\") | .operator_address" 2>/dev/null || echo "")

if [ -z "${IS_VALIDATOR}" ]; then
  echo "ERROR: Validator '${MONIKER}' not found in the validator set after registration." >&2
  exit 1
fi

echo "Validator registered: ${IS_VALIDATOR}"
echo ""
echo "=== Reset-join complete ==="
