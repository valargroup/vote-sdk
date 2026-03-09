#!/bin/bash
# init_multi.sh — Initialize a 3-validator Shielded-Vote chain on localhost.
#
# Init-only: creates home dirs, genesis, keys, config for all 3 validators.
# Does NOT start processes or register validators — that's handled externally.
#
# Each validator uses a separate home directory and unique port set.
# Usage:
#   bash sdk/scripts/init_multi.sh          # local dev (mise)
#   bash sdk/scripts/init_multi.sh --ci     # CI/remote deployment
#
# --ci mode differences:
#   - Helper server is configured on val1 only (not all 3)
#   - No pkill cleanup (processes managed by systemd)
#   - Summary prints systemd + create-val-tx instructions
set -e

# ---------------------------------------------------------------------------
# Parse flags
# ---------------------------------------------------------------------------
CI_MODE=false
for arg in "$@"; do
    case "$arg" in
        --ci) CI_MODE=true ;;
        *) echo "Unknown argument: $arg"; exit 1 ;;
    esac
done

# Use the Go toolchain from the environment (mise pins 1.24.1 via mise.toml).
if [ "$CI_MODE" = false ]; then
    export GOTOOLCHAIN=auto
fi

CHAIN_ID="svote-1"
BINARY="svoted"
DENOM="usvote"
NUM_VALIDATORS=3

# Home directories.
HOME_VAL1="$HOME/.svoted-val1"
HOME_VAL2="$HOME/.svoted-val2"
HOME_VAL3="$HOME/.svoted-val3"
HOMES=("$HOME_VAL1" "$HOME_VAL2" "$HOME_VAL3")

# Port allocation per validator (all offset from defaults to avoid conflicts
# with other local processes like Cursor IDE which may bind default ports).
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

# Self-delegation amounts. In local mode, val1 gets extra stake so that any 2
# validators hold >2/3 of total power — the chain keeps producing blocks if one
# node goes down (required for the restart test). CI mode uses uniform stakes.
VAL1_SELF_DELEGATION="20000000${DENOM}"
SELF_DELEGATION="10000000${DENOM}"

# Validator genesis balance (covers the 10M self-delegation).
GENESIS_BALANCE="10000000${DENOM}"
# Bootstrap admin balance (enough to fund up to 100 validators at 10M each).
ADMIN_BALANCE="1000000000${DENOM}"

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------
echo "=== Cleaning up previous multi-validator data ==="

# Kill any running svoted processes for these home directories (local dev only;
# CI uses systemd which stops services before running this script).
if [ "$CI_MODE" = false ]; then
    for home in "${HOMES[@]}"; do
        pkill -f "svoted start --home ${home}" 2>/dev/null || true
    done
    sleep 1
fi

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
    # Required for local multi-validator setups where all nodes run on localhost.
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

    # Update [vote] key paths and comet_rpc (section is auto-generated by the template).
    local ea_sk_path="$home/ea.sk"
    local pallas_sk_path="$home/pallas.sk"
    sed -i.bak "s|^ea_sk_path = .*|ea_sk_path = \"$ea_sk_path\"|" "$app_toml"
    sed -i.bak "s|^pallas_sk_path = .*|pallas_sk_path = \"$pallas_sk_path\"|" "$app_toml"
    sed -i.bak "s|^comet_rpc = .*|comet_rpc = \"http://localhost:${rpc_port}\"|" "$app_toml"

    rm -f "${app_toml}.bak"
}

# ---------------------------------------------------------------------------
# Helper: append helper server config
# ---------------------------------------------------------------------------
configure_helper() {
    local home="$1"
    local api_port="$2"
    local pulse_url="${3:-}"
    local helper_url="${4:-}"

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

# Path to the SQLite database file. Empty = default (\$home/helper.db).
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

# Heartbeat pulse URL. Empty disables the heartbeat (local dev default).
pulse_url = "${pulse_url}"

# This server's public URL. Empty disables the heartbeat (local dev default).
helper_url = "${helper_url}"
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
# Detect public IP for heartbeat URLs (CI mode only)
# ---------------------------------------------------------------------------
PULSE_URL=""
DASHED_IP=""
if [ "$CI_MODE" = true ]; then
    PUBLIC_IP=$(curl -4s --connect-timeout 5 ifconfig.me || true)
    if [ -n "$PUBLIC_IP" ]; then
        DASHED_IP=$(echo "$PUBLIC_IP" | tr '.' '-')
        PULSE_URL="${VOTING_CONFIG_URL:-https://shielded-vote.vercel.app}"
    fi
fi

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

# Import the deterministic bootstrap admin key (matches E2E test constant).
# In dev mode this account is also set as the vote-manager for convenience.
VM_PRIVKEY="b7e910eded435dd4e19c581b9a0b8e65104dcc4ebca8a1d55aa5c803e72ba2ee"
$BINARY keys import-hex manager "$VM_PRIVKEY" --keyring-backend test --home "$HOME_VAL1"
MANAGER_ADDR=$($BINARY keys show manager -a --keyring-backend test --home "$HOME_VAL1")
echo "Manager address:   $MANAGER_ADDR"

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

    # Save the address for later use (create-val-tx in CI, Go helper locally).
    echo "$ADDR" > "$home/validator_address.txt"
done

# Save val1 address too.
echo "$VAL1_ADDR" > "$HOME_VAL1/validator_address.txt"

# Add genesis accounts for all 3 validators and the bootstrap admin.
$BINARY genesis add-genesis-account "$VAL1_ADDR" "$VAL1_SELF_DELEGATION" \
    --keyring-backend test --home "$HOME_VAL1"
$BINARY genesis add-genesis-account "$MANAGER_ADDR" "$ADMIN_BALANCE" \
    --keyring-backend test --home "$HOME_VAL1"

for i in 2 3; do
    idx=$((i - 1))
    home="${HOMES[$idx]}"
    ADDR=$(cat "$home/validator_address.txt")
    $BINARY genesis add-genesis-account "$ADDR" "$GENESIS_BALANCE" \
        --keyring-backend test --home "$HOME_VAL1"
done

# Create genesis transaction for val1 (self-delegation).
$BINARY genesis gentx validator "$VAL1_SELF_DELEGATION" \
    --chain-id "$CHAIN_ID" \
    --keyring-backend test \
    --home "$HOME_VAL1"

# Collect genesis transactions and validate.
$BINARY genesis collect-gentxs --home "$HOME_VAL1"

# Patch slashing genesis: zero out slash fractions (no token burn).
GENESIS="$HOME_VAL1/config/genesis.json"
jq '.app_state.slashing.params.slash_fraction_double_sign = "0.000000000000000000"
  | .app_state.slashing.params.slash_fraction_downtime = "0.000000000000000000"' \
  "$GENESIS" > "${GENESIS}.tmp" && mv "${GENESIS}.tmp" "$GENESIS"

$BINARY genesis validate-genesis --home "$HOME_VAL1"

# Generate Pallas keypair for val1 (EA key is generated per-round by auto-deal).
$BINARY pallas-keygen --home "$HOME_VAL1"

# Configure ports.
configure_config_toml "$HOME_VAL1" "${P2P_PORTS[0]}" "${RPC_PORTS[0]}" "${PPROF_PORTS[0]}"
configure_app_toml "$HOME_VAL1" "${API_PORTS[0]}" "${GRPC_PORTS[0]}" "${GRPC_WEB_PORTS[0]}" "${RPC_PORTS[0]}"

# Helper server on all validators — each needs its own to accept shares when
# the iOS app distributes encrypted shares across per-validator URLs.
if [ -n "$DASHED_IP" ]; then
    configure_helper "$HOME_VAL1" "${API_PORTS[0]}" "$PULSE_URL" "https://val1.${DASHED_IP}.sslip.io"
else
    configure_helper "$HOME_VAL1" "${API_PORTS[0]}"
fi

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

    # Generate Pallas keypair (EA key is generated per-round by auto-deal).
    $BINARY pallas-keygen --home "$home"

    # Configure ports.
    configure_config_toml "$home" "${P2P_PORTS[$idx]}" "${RPC_PORTS[$idx]}" "${PPROF_PORTS[$idx]}"
    configure_app_toml "$home" "${API_PORTS[$idx]}" "${GRPC_PORTS[$idx]}" "${GRPC_WEB_PORTS[$idx]}" "${RPC_PORTS[$idx]}"

    if [ -n "$DASHED_IP" ]; then
        configure_helper "$home" "${API_PORTS[$idx]}" "$PULSE_URL" "https://val${i}.${DASHED_IP}.sslip.io"
    else
        configure_helper "$home" "${API_PORTS[$idx]}"
    fi

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

if [ "$CI_MODE" = true ]; then
    echo "Validators (start via systemd: svoted-val1/2/3):"
else
    echo "Validators:"
fi

for i in 1 2 3; do
    idx=$((i - 1))
    echo "  Val${i}:"
    echo "    Home:  ${HOMES[$idx]}"
    echo "    RPC:   http://127.0.0.1:${RPC_PORTS[$idx]}"
    echo "    API:   http://localhost:${API_PORTS[$idx]}"
    echo "    P2P:   ${P2P_PORTS[$idx]}"
done
echo ""

if [ "$CI_MODE" = true ]; then
    echo "After chain start, register val2 and val3:"
    echo "  create-val-tx --home $HOME_VAL2 --moniker val2 --amount $SELF_DELEGATION --rpc-url tcp://localhost:${RPC_PORTS[0]}"
    echo "  create-val-tx --home $HOME_VAL3 --moniker val3 --amount $SELF_DELEGATION --rpc-url tcp://localhost:${RPC_PORTS[0]}"
else
    echo "Start with: mise run multi:start"
fi

# ---------------------------------------------------------------------------
# Edge Config: register per-validator domains (CI mode only)
# ---------------------------------------------------------------------------
# Register each validator's sslip.io subdomain as a vote_server in Vercel Edge
# Config so the iOS app discovers them via service discovery. Only vote_servers
# are populated — PIR runs on the main domain and is configured separately.
#
# Requires: VERCEL_API_TOKEN, EDGE_CONFIG_ID (skipped silently if unset)
# Optional: VOTING_CONFIG_URL (default: https://shielded-vote.vercel.app)

if [ "$CI_MODE" = true ] && [ -n "$VERCEL_API_TOKEN" ] && [ -n "$EDGE_CONFIG_ID" ]; then
    echo ""
    echo "=== Registering validator domains in Edge Config ==="

    if ! command -v jq > /dev/null 2>&1; then
        echo "Warning: jq not found, skipping domain registration."
    else
        # Reuse the public IP detected earlier for heartbeat URLs.
        if [ -z "$DASHED_IP" ]; then
            echo "Warning: Could not detect public IP, skipping domain registration."
        else
            BASE_DOMAIN="${DASHED_IP}.sslip.io"

            # Fetch current voting-config from the public endpoint.
            CONFIG_URL="${VOTING_CONFIG_URL:-https://shielded-vote.vercel.app}"
            CURRENT_CONFIG=$(curl -s "${CONFIG_URL}/api/voting-config" 2>/dev/null)
            if ! echo "$CURRENT_CONFIG" | jq -e '.vote_servers' > /dev/null 2>&1; then
                CURRENT_CONFIG='{"version":1,"vote_servers":[],"pir_servers":[]}'
            fi

            # Upsert each validator's subdomain. Both URL and operator_address are
            # unique keys — evict any existing entry matching either, then append.
            UPDATED_CONFIG="$CURRENT_CONFIG"
            CHANGED=false
            for i in $(seq 1 $NUM_VALIDATORS); do
                idx=$((i - 1))
                URL="https://val${i}.${BASE_DOMAIN}"
                LABEL="val${i}"
                ADDR=$(cat "${HOMES[$idx]}/validator_address.txt" 2>/dev/null || echo "")

                # Remove any entry with matching URL or operator_address.
                UPDATED_CONFIG=$(echo "$UPDATED_CONFIG" | jq \
                    --arg url "$URL" --arg addr "$ADDR" \
                    '.vote_servers |= [.[] | select(.url != $url and .operator_address != $addr)]')

                # Append fresh entry.
                UPDATED_CONFIG=$(echo "$UPDATED_CONFIG" | jq \
                    --arg url "$URL" \
                    --arg label "$LABEL" \
                    --arg addr "$ADDR" \
                    '.vote_servers += [{"url": $url, "label": $label, "operator_address": $addr}]')
                echo "  ${LABEL}: ${URL} (${ADDR})"
                CHANGED=true
            done

            if [ "$CHANGED" = true ]; then
                # Build approved-servers from the same entries.
                APPROVED_SERVERS=$(echo "$UPDATED_CONFIG" | jq '.vote_servers')

                PATCH_BODY=$(jq -n \
                    --argjson config "$UPDATED_CONFIG" \
                    --argjson approved "$APPROVED_SERVERS" \
                    '{items: [
                        {operation: "upsert", key: "voting-config", value: $config},
                        {operation: "upsert", key: "approved-servers", value: $approved}
                    ]}')

                HTTP_STATUS=$(curl -s -o /tmp/edge-config-resp.txt -w "%{http_code}" \
                    -X PATCH \
                    "https://api.vercel.com/v1/edge-config/${EDGE_CONFIG_ID}/items" \
                    -H "Authorization: Bearer ${VERCEL_API_TOKEN}" \
                    -H "Content-Type: application/json" \
                    -d "$PATCH_BODY")

                if [ "$HTTP_STATUS" = "200" ]; then
                    echo "  Edge Config updated successfully (voting-config + approved-servers)."
                else
                    echo "  Warning: Edge Config update failed (HTTP ${HTTP_STATUS})."
                    cat /tmp/edge-config-resp.txt 2>/dev/null
                    echo ""
                fi
                rm -f /tmp/edge-config-resp.txt
            else
                echo "  All domains already registered, no changes needed."
            fi

            # Patch pulse_url and helper_url into each validator's app.toml
            # so the heartbeat goroutine activates on next start.
            for i in $(seq 1 $NUM_VALIDATORS); do
                idx=$((i - 1))
                VAL_URL="https://val${i}.${BASE_DOMAIN}"
                VAL_TOML="${HOMES[$idx]}/config/app.toml"
                sed -i.bak "s|^pulse_url = \"\"$|pulse_url = \"${CONFIG_URL}\"|" "$VAL_TOML"
                sed -i.bak "s|^helper_url = \"\"$|helper_url = \"${VAL_URL}\"|" "$VAL_TOML"
                rm -f "${VAL_TOML}.bak"
                echo "  val${i}: pulse_url=${CONFIG_URL} helper_url=${VAL_URL}"
            done
        fi
    fi
elif [ "$CI_MODE" = true ]; then
    echo ""
    echo "Note: Set VERCEL_API_TOKEN and EDGE_CONFIG_ID to auto-register validator domains in Edge Config."
fi
