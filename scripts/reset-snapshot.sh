#!/usr/bin/env bash
#
# reset-snapshot.sh - Reset the dedicated snapshot node and join it to the
# production chain as a pruned, non-validator follower.
#
# Designed for CI (sdk-chain-reset.yml): assumes svoted is already installed at
# /opt/shielded-vote/current/bin/ and on PATH.
#
# Required env:
#   GENESIS_URL       URL to download genesis.json
#   PRIMARY_REST_URL  Primary's REST API base URL
#
# Optional env:
#   HOME_DIR          svoted home directory (default: /opt/shielded-vote/.svoted)
#   CHAIN_ID          Cosmos chain ID (default: svote-1)
#   MONIKER           Node moniker (default: snapshot)

set -euo pipefail

GENESIS_URL="${GENESIS_URL:?GENESIS_URL must be set}"
PRIMARY_REST_URL="${PRIMARY_REST_URL:?PRIMARY_REST_URL must be set}"
HOME_DIR="${HOME_DIR:-/opt/shielded-vote/.svoted}"
CHAIN_ID="${CHAIN_ID:-svote-1}"
MONIKER="${MONIKER:-snapshot}"

echo "=== Reset-snapshot: pruned snapshot node ==="
echo "  Genesis:  ${GENESIS_URL}"
echo "  Primary:  ${PRIMARY_REST_URL}"
echo "  Home:     ${HOME_DIR}"
echo ""

if systemctl is-active --quiet svoted 2>/dev/null; then
  echo "Stopping svoted..."
  systemctl stop svoted
fi

if systemctl is-active --quiet snapshot.timer 2>/dev/null; then
  echo "Stopping snapshot timer..."
  systemctl stop snapshot.timer
fi

echo "Wiping ${HOME_DIR} contents..."
mkdir -p "${HOME_DIR}"
rm -rf "${HOME_DIR:?}"/*

echo "Initializing node (moniker=${MONIKER}, chain=${CHAIN_ID})..."
svoted init "${MONIKER}" --chain-id "${CHAIN_ID}" --home "${HOME_DIR}" > /dev/null 2>&1

echo "Downloading genesis from ${GENESIS_URL}..."
curl -fsSL -o "${HOME_DIR}/config/genesis.json" "${GENESIS_URL}"
svoted genesis validate-genesis --home "${HOME_DIR}" > /dev/null 2>&1
echo "Genesis validated."

echo "Fetching primary node info from ${PRIMARY_REST_URL}..."
NODE_INFO=$(curl -fsSL "${PRIMARY_REST_URL}/cosmos/base/tendermint/v1beta1/node_info")
NODE_ID=$(echo "$NODE_INFO" | jq -r '.default_node_info.default_node_id // .default_node_info.id // empty')
LISTEN_ADDR=$(echo "$NODE_INFO" | jq -r '.default_node_info.listen_addr // empty')

if [ -z "$NODE_ID" ]; then
  echo "ERROR: Could not fetch node_id from ${PRIMARY_REST_URL}" >&2
  exit 1
fi

PRIMARY_HOST=$(echo "$PRIMARY_REST_URL" | sed -E 's|^https?://||; s|:[0-9]+$||; s|/.*||')
P2P_PORT=$(echo "$LISTEN_ADDR" | sed -E 's|.*:([0-9]+)$|\1|')
P2P_PORT="${P2P_PORT:-26656}"
PERSISTENT_PEERS="${NODE_ID}@${PRIMARY_HOST}:${P2P_PORT}"
echo "Persistent peers: ${PERSISTENT_PEERS}"

CONFIG_TOML="${HOME_DIR}/config/config.toml"
APP_TOML="${HOME_DIR}/config/app.toml"

sed -i "s|persistent_peers = \"\"|persistent_peers = \"${PERSISTENT_PEERS}\"|" "${CONFIG_TOML}"
sed -i '/\[rpc\]/,/\[.*\]/ s|^laddr = .*|laddr = "tcp://127.0.0.1:26657"|' "${CONFIG_TOML}"
sed -i '/\[p2p\]/,/\[.*\]/ s|^laddr = .*|laddr = "tcp://0.0.0.0:26656"|' "${CONFIG_TOML}"
sed -i 's|^indexer = ".*"|indexer = "null"|' "${CONFIG_TOML}"

sed -i '/\[api\]/,/\[.*\]/ s/enable = .*/enable = false/' "${APP_TOML}"
sed -i '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = .*/enabled-unsafe-cors = false/' "${APP_TOML}"
sed -i 's|^pruning = ".*"|pruning = "custom"|' "${APP_TOML}"
sed -i 's|^pruning-keep-recent = ".*"|pruning-keep-recent = "100"|' "${APP_TOML}"
sed -i 's|^pruning-interval = ".*"|pruning-interval = "10"|' "${APP_TOML}"
sed -i 's|^min-retain-blocks = .*|min-retain-blocks = 0|' "${APP_TOML}"
sed -i 's|^discard_abci_responses = .*|discard_abci_responses = true|' "${APP_TOML}"

echo "Starting svoted..."
systemctl daemon-reload
systemctl start svoted

for i in $(seq 1 30); do
  if svoted status --home "${HOME_DIR}" > /dev/null 2>&1; then
    echo "Snapshot node is running."
    systemctl enable --now snapshot.timer
    echo "Snapshot timer enabled."
    exit 0
  fi
  echo "Waiting for snapshot node status... ($i/30)"
  sleep 10
done

echo "ERROR: Snapshot node did not become healthy after 5 minutes." >&2
exit 1
