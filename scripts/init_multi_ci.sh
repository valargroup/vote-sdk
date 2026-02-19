#!/bin/bash
# init_multi_ci.sh — Initialize a 3-validator Zally chain for CI/remote deployment.
#
# Like init_multi.sh, but does NOT start any processes and does NOT register
# post-genesis validators. Those steps are handled externally:
#   - Validators are started via systemd (zallyd-val1, zallyd-val2, zallyd-val3)
#   - Val2 and val3 are registered after chain start via the create-val-tx binary
#
# Usage:
#   HOME=/opt/zally-chain bash init_multi_ci.sh
set -e

CHAIN_ID="zvote-1"
BINARY="zallyd"
DENOM="stake"

# Home directories (rooted under $HOME set by the caller).
HOME_VAL1="$HOME/.zallyd-val1"
HOME_VAL2="$HOME/.zallyd-val2"
HOME_VAL3="$HOME/.zallyd-val3"
HOMES=("$HOME_VAL1" "$HOME_VAL2" "$HOME_VAL3")

# Port allocation per validator.
#                        Val1    Val2    Val3
# CometBFT P2P:         26156   26256   26356
# CometBFT RPC:         26157   26257   26357
# gRPC:                  9390    9490    9590
# gRPC-web:              9391    9491    9591
# REST API:              1418    1518    1618
# pprof:                 6160    6260    6360
P2P_PORTS=(26156 26256 26356)
RPC_PORTS=(26157 26257 26357)
GRPC_PORTS=(9390 9490 9590)
GRPC_WEB_PORTS=(9391 9491 9591)
API_PORTS=(1418 1518 1618)
PPROF_PORTS=(6160 6260 6360)

SELF_DELEGATION="10000000${DENOM}"
GENESIS_BALANCE="100000000${DENOM}"

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------
echo "=== Cleaning up previous multi-validator data ==="
for home in "${HOMES[@]}"; do
    rm -rf "$home"
done

# ---------------------------------------------------------------------------
# Helper: configure config.toml ports
# ---------------------------------------------------------------------------
configure_config_toml() {
    local home="$1"
    local p2p_port="$2"
    local rpc_port="$3"
    local pprof_port="$4"

    local config_toml="$home/config/config.toml"

    # P2P listen address.
    sed -i.bak "s|laddr = \"tcp://0.0.0.0:26656\"|laddr = \"tcp://0.0.0.0:${p2p_port}\"|" "$config_toml"

    # RPC listen address.
    sed -i.bak "s|laddr = \"tcp://127.0.0.1:26657\"|laddr = \"tcp://127.0.0.1:${rpc_port}\"|" "$config_toml"

    # pprof listen address.
    sed -i.bak "s|pprof_laddr = \"localhost:6060\"|pprof_laddr = \"localhost:${pprof_port}\"|" "$config_toml"

    # Broadcast timeout for long CheckTx (ZKP verification).
    sed -i.bak 's/^timeout_broadcast_tx_commit = .*/timeout_broadcast_tx_commit = "120s"/' "$config_toml"

    # Allow non-routable addresses (127.0.0.1) in the address book.
    sed -i.bak 's/^addr_book_strict = true/addr_book_strict = false/' "$config_toml"

    # Allow multiple peers from the same IP (all validators share 127.0.0.1).
    sed -i.bak 's/^allow_duplicate_ip = false/allow_duplicate_ip = true/' "$config_toml"

    rm -f "${config_toml}.bak"
}

# ---------------------------------------------------------------------------
# Helper: configure app.toml ports and vote module keys
# ---------------------------------------------------------------------------
configure_app_toml() {
    local home="$1"
    local api_port="$2"
    local grpc_port="$3"
    local grpc_web_port="$4"
    local rpc_port="$5"

    local app_toml="$home/config/app.toml"

    # Enable the REST API and set port.
    sed -i.bak '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "$app_toml"
    sed -i.bak "s|address = \"tcp://localhost:1317\"|address = \"tcp://0.0.0.0:${api_port}\"|" "$app_toml"

    # Enable CORS for cross-origin access (e.g. Vercel deployments).
    sed -i.bak '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/' "$app_toml"

    # gRPC server port.
    sed -i.bak "s|address = \"localhost:9090\"|address = \"localhost:${grpc_port}\"|" "$app_toml"

    # gRPC-web port.
    sed -i.bak "s|address = \"localhost:9091\"|address = \"localhost:${grpc_web_port}\"|" "$app_toml"

    rm -f "${app_toml}.bak"

    # Append vote module configuration.
    local ea_sk_path="$home/ea.sk"
    local pallas_sk_path="$home/pallas.sk"
    cat >> "$app_toml" <<VOTECFG

###############################################################################
###                          Vote Module                                    ###
###############################################################################

[vote]

# Path to the Election Authority secret key file (32 bytes).
# Used by PrepareProposal to decrypt tallies and auto-inject MsgSubmitTally.
ea_sk_path = "$ea_sk_path"

# Path to the Pallas secret key file (32 bytes).
# Used by PrepareProposal to ECIES-decrypt the EA key share during ceremony
# and auto-inject MsgAckExecutiveAuthorityKey.
pallas_sk_path = "$pallas_sk_path"

# CometBFT RPC endpoint for the vote REST API to broadcast transactions.
comet_rpc = "http://localhost:${rpc_port}"
VOTECFG
}

# ---------------------------------------------------------------------------
# Helper: append helper server config (val1 only)
# ---------------------------------------------------------------------------
configure_helper() {
    local home="$1"
    local api_port="$2"

    local app_toml="$home/config/app.toml"
    cat >> "$app_toml" <<HELPERCFG

###############################################################################
###                         Helper Server                                   ###
###############################################################################

[helper]

# Set to true to disable the helper server.
disable = false

# Optional auth token for POST /api/v1/shares (sent via X-Helper-Token header).
# Empty disables token auth.
api_token = ""

# Path to the SQLite database file. Empty = default ($home/helper.db).
db_path = ""

# Mean of the exponential delay distribution (seconds).
# Shares are delayed by Exp(1/mean) for temporal unlinkability, capped at vote end time.
# Use a short value for testing; production default is 43200 (12 hours).
mean_delay = 60

# How often to check for shares ready to submit (seconds).
process_interval = 5

# Port of the chain's REST API (used for MsgRevealShare submission).
chain_api_port = ${api_port}

# Maximum concurrent proof generation goroutines.
max_concurrent_proofs = 2
HELPERCFG
}

# ---------------------------------------------------------------------------
# Helper: set persistent_peers in config.toml
# ---------------------------------------------------------------------------
set_persistent_peers() {
    local home="$1"
    local peers="$2"
    local config_toml="$home/config/config.toml"

    sed -i.bak "s|persistent_peers = \"\"|persistent_peers = \"${peers}\"|" "$config_toml"
    rm -f "${config_toml}.bak"
}

# ---------------------------------------------------------------------------
# Step 1: Initialize Validator 1 (genesis validator)
# ---------------------------------------------------------------------------
echo ""
echo "=== Initializing Validator 1 (genesis) ==="

$BINARY init val1 --chain-id "$CHAIN_ID" --home "$HOME_VAL1"

# Create validator key.
$BINARY keys add validator --keyring-backend test --home "$HOME_VAL1"
VAL1_ADDR=$($BINARY keys show validator -a --keyring-backend test --home "$HOME_VAL1")
echo "Val1 address: $VAL1_ADDR"

# Initialize keys for validators 2 and 3 (separate home dirs, but we need
# their addresses now to add as genesis accounts).
echo ""
echo "=== Pre-initializing keys for Validators 2 and 3 ==="

for i in 2 3; do
    idx=$((i - 1))
    home="${HOMES[$idx]}"

    # Init the node (creates config + priv_validator_key.json).
    $BINARY init "val${i}" --chain-id "$CHAIN_ID" --home "$home"

    # Create a validator account key.
    $BINARY keys add validator --keyring-backend test --home "$home"
    ADDR=$($BINARY keys show validator -a --keyring-backend test --home "$home")
    echo "Val${i} address: $ADDR"

    # Save the address for create-val-tx to read.
    echo "$ADDR" > "$home/validator_address.txt"
done

# Save val1 address too.
echo "$VAL1_ADDR" > "$HOME_VAL1/validator_address.txt"

# Add genesis accounts for all 3 validators.
$BINARY genesis add-genesis-account "$VAL1_ADDR" "$GENESIS_BALANCE" \
    --keyring-backend test --home "$HOME_VAL1"

for i in 2 3; do
    idx=$((i - 1))
    home="${HOMES[$idx]}"
    ADDR=$(cat "$home/validator_address.txt")
    $BINARY genesis add-genesis-account "$ADDR" "$GENESIS_BALANCE" \
        --keyring-backend test --home "$HOME_VAL1"
done

# Create genesis transaction for val1 (self-delegation).
$BINARY genesis gentx validator "$SELF_DELEGATION" \
    --chain-id "$CHAIN_ID" \
    --keyring-backend test \
    --home "$HOME_VAL1"

# Collect genesis transactions and validate.
$BINARY genesis collect-gentxs --home "$HOME_VAL1"
$BINARY genesis validate-genesis --home "$HOME_VAL1"

# Generate EA and Pallas keypairs for val1.
$BINARY ea-keygen --home "$HOME_VAL1"
$BINARY pallas-keygen --home "$HOME_VAL1"

# Configure ports and vote module for val1.
configure_config_toml "$HOME_VAL1" "${P2P_PORTS[0]}" "${RPC_PORTS[0]}" "${PPROF_PORTS[0]}"
configure_app_toml "$HOME_VAL1" "${API_PORTS[0]}" "${GRPC_PORTS[0]}" "${GRPC_WEB_PORTS[0]}" "${RPC_PORTS[0]}"

# Enable helper server on val1 only.
configure_helper "$HOME_VAL1" "${API_PORTS[0]}"

# ---------------------------------------------------------------------------
# Step 2: Configure Validators 2 and 3 (copy genesis, set peers)
# ---------------------------------------------------------------------------
echo ""
echo "=== Configuring Validators 2 and 3 ==="

# Get val1's node ID for persistent_peers.
VAL1_NODE_ID=$($BINARY comet show-node-id --home "$HOME_VAL1")
VAL1_PEER="${VAL1_NODE_ID}@127.0.0.1:${P2P_PORTS[0]}"
echo "Val1 peer address: $VAL1_PEER"

for i in 2 3; do
    idx=$((i - 1))
    home="${HOMES[$idx]}"
    echo "--- Validator ${i} ---"

    # Copy the finalized genesis.json from val1.
    cp "$HOME_VAL1/config/genesis.json" "$home/config/genesis.json"

    # Generate Pallas keypair.
    $BINARY pallas-keygen --home "$home"

    # Generate EA keypair (needed for PrepareProposal auto-ack/tally).
    $BINARY ea-keygen --home "$home"

    # Configure ports.
    configure_config_toml "$home" "${P2P_PORTS[$idx]}" "${RPC_PORTS[$idx]}" "${PPROF_PORTS[$idx]}"
    configure_app_toml "$home" "${API_PORTS[$idx]}" "${GRPC_PORTS[$idx]}" "${GRPC_WEB_PORTS[$idx]}" "${RPC_PORTS[$idx]}"

    # Set persistent_peers to val1.
    set_persistent_peers "$home" "$VAL1_PEER"
done

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "============================================="
echo "=== Multi-Validator Chain Initialized OK  ==="
echo "============================================="
echo ""
echo "Validators (start via systemd: zallyd-val1/2/3):"
for i in 1 2 3; do
    idx=$((i - 1))
    echo "  Val${i}:"
    echo "    Home:  ${HOMES[$idx]}"
    echo "    RPC:   http://127.0.0.1:${RPC_PORTS[$idx]}"
    echo "    API:   http://localhost:${API_PORTS[$idx]}"
    echo "    P2P:   ${P2P_PORTS[$idx]}"
done
echo ""
echo "After chain start, register val2 and val3:"
echo "  create-val-tx --home $HOME_VAL2 --moniker val2 --amount $SELF_DELEGATION --rpc-url tcp://localhost:${RPC_PORTS[0]}"
echo "  create-val-tx --home $HOME_VAL3 --moniker val3 --amount $SELF_DELEGATION --rpc-url tcp://localhost:${RPC_PORTS[0]}"
