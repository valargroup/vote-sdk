#!/bin/bash
# init-archive.sh — Initialize an archive (non-validator) svoted node.
#
# Must run AFTER scripts/init_multi.sh (requires val1's genesis to exist).
# Creates $HOME/.svoted-archive configured to peer with val1 via p2p on
# 127.0.0.1:26156 and serve LCD/RPC for the block explorer.
#
# Usage:
#   bash scripts/init-archive.sh
#
# Env: $HOME must be the prefix where .svoted-val1 lives (same convention
# as init_multi.sh — local dev uses the real $HOME, CI exports
# HOME=/opt/shielded-vote).
set -e

BINARY="svoted"
CHAIN_ID="svote-1"

HOME_VAL1="$HOME/.svoted-val1"
HOME_ARCHIVE="$HOME/.svoted-archive"

# Archive port allocation (+100 pattern matching init_multi.sh).
#                        Val1    Val2    Val3    Archive
# CometBFT P2P:         26156   26256   26356   26456
# CometBFT RPC:         26157   26257   26357   26457
# gRPC:                  9390    9490    9590    9690
# gRPC-web:              9391    9491    9591    9691
# REST API:              1418    1518    1618    1718
# pprof:                 6160    6260    6360    6460
P2P_PORT=26456
RPC_PORT=26457
API_PORT=1718
GRPC_PORT=9690
GRPC_WEB_PORT=9691
PPROF_PORT=6460

# ---------------------------------------------------------------------------
# Pre-flight: val1's genesis must exist.
# ---------------------------------------------------------------------------
if [ ! -f "$HOME_VAL1/config/genesis.json" ]; then
    echo "ERROR: $HOME_VAL1/config/genesis.json not found."
    echo "       Run scripts/init_multi.sh (or --ci) before init-archive.sh."
    exit 1
fi

# ---------------------------------------------------------------------------
# Cleanup — always start from a clean archive home.
# ---------------------------------------------------------------------------
echo "=== Cleaning up previous archive data ==="
rm -rf "$HOME_ARCHIVE"

# ---------------------------------------------------------------------------
# Initialize the archive node
# ---------------------------------------------------------------------------
echo ""
echo "=== Initializing archive node ==="
$BINARY init archive --chain-id "$CHAIN_ID" --home "$HOME_ARCHIVE"

# Generate a Pallas keypair. Archive doesn't use it — the archive never runs
# PrepareProposal. Matches the validator init pattern, and keeps
# pallas_sk_path pointing at a readable file so any future strict-validation
# on this path won't break the archive.
$BINARY pallas-keygen --home "$HOME_ARCHIVE"

# Copy val1's finalized genesis — THIS is what makes the archive part of the
# same chain as val1/2/3. Same genesis hash = same chain.
cp "$HOME_VAL1/config/genesis.json" "$HOME_ARCHIVE/config/genesis.json"

# ---------------------------------------------------------------------------
# Configure config.toml (ports, relaxed addr_book, persistent_peers)
# ---------------------------------------------------------------------------
CONFIG_TOML="$HOME_ARCHIVE/config/config.toml"

sed -i.bak "s|laddr = \"tcp://0.0.0.0:26656\"|laddr = \"tcp://0.0.0.0:${P2P_PORT}\"|" "$CONFIG_TOML"
sed -i.bak "s|laddr = \"tcp://127.0.0.1:26657\"|laddr = \"tcp://127.0.0.1:${RPC_PORT}\"|" "$CONFIG_TOML"
sed -i.bak "s|pprof_laddr = \"localhost:6060\"|pprof_laddr = \"localhost:${PPROF_PORT}\"|" "$CONFIG_TOML"

# All validators run on 127.0.0.1 — same relaxed addr_book as init_multi.sh.
sed -i.bak 's/^addr_book_strict = true/addr_book_strict = false/' "$CONFIG_TOML"
sed -i.bak 's/^allow_duplicate_ip = false/allow_duplicate_ip = true/' "$CONFIG_TOML"

# Enable CORS on Tendermint RPC so the browser-based explorer can query it.
# (The LCD's CORS is handled separately via enabled-unsafe-cors in app.toml.)
sed -i.bak 's|^cors_allowed_origins = .*|cors_allowed_origins = ["*"]|' "$CONFIG_TOML"

# Peer with val1 at its p2p port (26156).
VAL1_NODE_ID=$($BINARY comet show-node-id --home "$HOME_VAL1")
VAL1_PEER="${VAL1_NODE_ID}@127.0.0.1:26156"
sed -i.bak "s|persistent_peers = \"\"|persistent_peers = \"${VAL1_PEER}\"|" "$CONFIG_TOML"
echo "Archive peer: $VAL1_PEER"

rm -f "${CONFIG_TOML}.bak"

# ---------------------------------------------------------------------------
# Configure app.toml (API, CORS, ports, vote-module paths, pruning=nothing)
# ---------------------------------------------------------------------------
APP_TOML="$HOME_ARCHIVE/config/app.toml"

# Enable REST API, bind 0.0.0.0 so Caddy can reach it, CORS on for browser.
sed -i.bak '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "$APP_TOML"
sed -i.bak "s|address = \"tcp://localhost:1317\"|address = \"tcp://0.0.0.0:${API_PORT}\"|" "$APP_TOML"
sed -i.bak '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/' "$APP_TOML"

# gRPC / gRPC-web ports (not exposed publicly, just avoiding collision).
sed -i.bak "s|address = \"localhost:9090\"|address = \"localhost:${GRPC_PORT}\"|" "$APP_TOML"
sed -i.bak "s|address = \"localhost:9091\"|address = \"localhost:${GRPC_WEB_PORT}\"|" "$APP_TOML"

# Vote-module key paths. Archive doesn't use these; setting them to local
# paths satisfies app.toml's startup validation.
EA_SK_PATH="$HOME_ARCHIVE/ea.sk"
PALLAS_SK_PATH="$HOME_ARCHIVE/pallas.sk"
sed -i.bak "s|^ea_sk_path = .*|ea_sk_path = \"$EA_SK_PATH\"|" "$APP_TOML"
sed -i.bak "s|^pallas_sk_path = .*|pallas_sk_path = \"$PALLAS_SK_PATH\"|" "$APP_TOML"
sed -i.bak "s|^comet_rpc = .*|comet_rpc = \"http://localhost:${RPC_PORT}\"|" "$APP_TOML"

# Full-history pruning: keep every block forever. min-retain-blocks=0
# disables the retention cap entirely.
sed -i.bak 's|^pruning = ".*"|pruning = "nothing"|' "$APP_TOML"
sed -i.bak 's|^min-retain-blocks = .*|min-retain-blocks = 0|' "$APP_TOML"

rm -f "${APP_TOML}.bak"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "=========================================="
echo "=== Archive Node Initialized OK        ==="
echo "=========================================="
echo "  Home:    $HOME_ARCHIVE"
echo "  Chain:   $CHAIN_ID (genesis copied from val1)"
echo "  RPC:     http://127.0.0.1:${RPC_PORT}"
echo "  API:     http://127.0.0.1:${API_PORT}"
echo "  P2P:     ${P2P_PORT} (peer: $VAL1_PEER)"
echo "  Pruning: nothing (full history retained)"
echo ""
echo "Start with: sudo systemctl start svoted-archive"
