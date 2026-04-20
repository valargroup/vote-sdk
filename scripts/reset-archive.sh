#!/usr/bin/env bash
#
# reset-archive.sh — Reset the archive (non-validator) node and join an
# existing chain.
#
# Designed for CI (sdk-chain-reset.yml): assumes svoted is already installed
# at /opt/shielded-vote/current/bin/ and on PATH.
#
# 1. Wipes existing chain state
# 2. Downloads genesis from DO Spaces (uploaded by the reset-primary job)
# 3. Discovers the primary's P2P node-id via its REST API
# 4. Configures persistent_peers, pruning=nothing, CORS on LCD + RPC
# 5. Starts svoted via systemctl
# 6. Waits for sync
#
# Unlike reset-join.sh: never generates a validator key, never generates
# Pallas/EA keys, never calls create-val-tx. The archive serves queries
# and does not participate in consensus.
#
# Required env:
#   GENESIS_URL       URL to download genesis.json (e.g. https://vote.fra1.digitaloceanspaces.com/genesis.json)
#   PRIMARY_REST_URL  Primary's REST API base URL (e.g. https://vote-chain-primary.example.com)
#
# Optional env:
#   HOME_DIR          svoted home directory (default: /opt/shielded-vote/.svoted)
#   CHAIN_ID          Cosmos chain ID (default: svote-1)
#   MONIKER           Node moniker (default: archive)

set -euo pipefail

GENESIS_URL="${GENESIS_URL:?GENESIS_URL must be set}"
PRIMARY_REST_URL="${PRIMARY_REST_URL:?PRIMARY_REST_URL must be set}"
HOME_DIR="${HOME_DIR:-/opt/shielded-vote/.svoted}"
CHAIN_ID="${CHAIN_ID:-svote-1}"
MONIKER="${MONIKER:-archive}"

echo "=== Reset-archive: non-validator node ==="
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
mkdir -p "${HOME_DIR}"
rm -rf "${HOME_DIR:?}"/*

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

# ─── Configure config.toml ───────────────────────────────────────────────────

CONFIG_TOML="${HOME_DIR}/config/config.toml"
sed -i "s|persistent_peers = \"\"|persistent_peers = \"${PERSISTENT_PEERS}\"|" "${CONFIG_TOML}"

# CORS on Tendermint RPC so the browser-based explorer can query it.
sed -i 's|^cors_allowed_origins = .*|cors_allowed_origins = ["*"]|' "${CONFIG_TOML}"

# ─── Configure app.toml ──────────────────────────────────────────────────────

APP_TOML="${HOME_DIR}/config/app.toml"

# Enable the REST API on all interfaces with CORS for browser access.
sed -i '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "$APP_TOML"
sed -i 's|address = "tcp://localhost:1317"|address = "tcp://0.0.0.0:1317"|' "$APP_TOML"
sed -i '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/' "$APP_TOML"

# Archive pruning — keep every block forever.
sed -i 's|^pruning = ".*"|pruning = "nothing"|' "$APP_TOML"
sed -i 's|^min-retain-blocks = .*|min-retain-blocks = 0|' "$APP_TOML"

# ─── Start svoted ────────────────────────────────────────────────────────────

echo "Starting svoted..."
systemctl daemon-reload
systemctl start svoted

# ─── Wait for sync ───────────────────────────────────────────────────────────

for i in $(seq 1 30); do
  if curl -sf http://localhost:1317/cosmos/base/tendermint/v1beta1/blocks/latest > /dev/null 2>&1; then
    echo "Archive REST API healthy."
    exit 0
  fi
  echo "Waiting for archive REST API... ($i/30)"
  sleep 10
done
echo "ERROR: Archive REST API not healthy after 5 minutes."
exit 1
