#!/bin/bash
#
# entrypoint.sh — Docker entrypoint for the 30-validator local testnet.
#
# Environment variables (set by docker-compose):
#   VALIDATOR_INDEX   1-based index (1 = genesis validator)
#   NUM_VALIDATORS    total number of validators (default 30)
#   CHAIN_ID          chain ID (default svote-1)
#
# Shared volume at /shared is used for genesis distribution and key exchange.
set -e

VALIDATOR_INDEX="${VALIDATOR_INDEX:?VALIDATOR_INDEX must be set}"
NUM_VALIDATORS="${NUM_VALIDATORS:-30}"
CHAIN_ID="${CHAIN_ID:-svote-1}"
DENOM="usvote"
HOME_DIR="/root/.svoted"
MONIKER="val${VALIDATOR_INDEX}"
SELF_DELEGATION="10000000${DENOM}"
ADMIN_BALANCE="1000000000${DENOM}"

# ---------------------------------------------------------------------------
# Phase 1: Initialize node and publish address
# ---------------------------------------------------------------------------

svoted init "$MONIKER" --chain-id "$CHAIN_ID" --home "$HOME_DIR" 2>/dev/null

svoted keys add validator --keyring-backend test --home "$HOME_DIR" 2>/dev/null
VAL_ADDR=$(svoted keys show validator -a --keyring-backend test --home "$HOME_DIR")

svoted pallas-keygen --home "$HOME_DIR"

# Configure vote module key paths.
APP_TOML="$HOME_DIR/config/app.toml"
sed -i "s|^ea_sk_path = .*|ea_sk_path = \"$HOME_DIR/ea.sk\"|" "$APP_TOML"
sed -i "s|^pallas_sk_path = .*|pallas_sk_path = \"$HOME_DIR/pallas.sk\"|" "$APP_TOML"
sed -i "s|^comet_rpc = .*|comet_rpc = \"http://localhost:26657\"|" "$APP_TOML"

# Enable REST API on all interfaces.
sed -i '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "$APP_TOML"
sed -i '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/' "$APP_TOML"
sed -i 's|address = "tcp://localhost:1317"|address = "tcp://0.0.0.0:1317"|' "$APP_TOML"

# Bind RPC to all interfaces so other containers can reach it.
CONFIG_TOML="$HOME_DIR/config/config.toml"
sed -i 's|laddr = "tcp://127.0.0.1:26657"|laddr = "tcp://0.0.0.0:26657"|' "$CONFIG_TOML"

# Allow peers from any address (Docker networking).
sed -i 's/^addr_book_strict = true/addr_book_strict = false/' "$CONFIG_TOML"
sed -i 's/^allow_duplicate_ip = false/allow_duplicate_ip = true/' "$CONFIG_TOML"

# Publish our address for genesis account creation.
mkdir -p /shared/addrs
echo "$VAL_ADDR" > "/shared/addrs/val${VALIDATOR_INDEX}.addr"

# ---------------------------------------------------------------------------
# Phase 2: Genesis creation (val1 only) or genesis copy (val2+)
# ---------------------------------------------------------------------------

if [ "$VALIDATOR_INDEX" -eq 1 ]; then
    echo "[$MONIKER] Waiting for all $NUM_VALIDATORS validator addresses..."
    while true; do
        COUNT=$(ls /shared/addrs/*.addr 2>/dev/null | wc -l)
        if [ "$COUNT" -ge "$NUM_VALIDATORS" ]; then
            break
        fi
        sleep 0.5
    done
    echo "[$MONIKER] All $NUM_VALIDATORS addresses collected."

    # Generate a throwaway vote-manager key for this ephemeral testnet.
    VM_PRIVKEY=$(cat /dev/urandom | head -c 32 | od -An -tx1 | tr -d ' \n')
    svoted keys import-hex vote-manager-1 "$VM_PRIVKEY" --keyring-backend test --home "$HOME_DIR" 2>/dev/null
    VM_ADDR=$(svoted keys show vote-manager-1 -a --keyring-backend test --home "$HOME_DIR")

    # Add genesis accounts for all validators.
    for i in $(seq 1 "$NUM_VALIDATORS"); do
        ADDR=$(cat "/shared/addrs/val${i}.addr")
        svoted genesis add-genesis-account "$ADDR" "$SELF_DELEGATION" \
            --keyring-backend test --home "$HOME_DIR"
    done

    # Add vote-manager account.
    svoted genesis add-genesis-account "$VM_ADDR" "$ADMIN_BALANCE" \
        --keyring-backend test --home "$HOME_DIR"

    # Genesis transaction (self-delegation for val1).
    svoted genesis gentx validator "$SELF_DELEGATION" \
        --chain-id "$CHAIN_ID" \
        --keyring-backend test \
        --home "$HOME_DIR"

    svoted genesis collect-gentxs --home "$HOME_DIR"

    # Patch genesis: set vote_manager_addresses (single vote manager here), disable slashing.
    GENESIS="$HOME_DIR/config/genesis.json"
    jq --arg admin "$VM_ADDR" '
      .app_state.vote.vote_manager_addresses = [$admin]
      | .app_state.slashing.params.slash_fraction_double_sign = "0.000000000000000000"
      | .app_state.slashing.params.slash_fraction_downtime = "0.000000000000000000"' \
      "$GENESIS" > "${GENESIS}.tmp" && mv "${GENESIS}.tmp" "$GENESIS"

    svoted genesis validate-genesis --home "$HOME_DIR"

    # Publish genesis and node ID.
    cp "$GENESIS" /shared/genesis.json
    NODE_ID=$(svoted comet show-node-id --home "$HOME_DIR")
    echo "$NODE_ID" > /shared/val1_node_id

    touch /shared/genesis-ready
    echo "[$MONIKER] Genesis created. Node ID: $NODE_ID"

else
    echo "[$MONIKER] Waiting for genesis..."
    while [ ! -f /shared/genesis-ready ]; do
        sleep 0.5
    done

    cp /shared/genesis.json "$HOME_DIR/config/genesis.json"

    # Set persistent_peers to val1.
    VAL1_NODE_ID=$(cat /shared/val1_node_id)
    VAL1_PEER="${VAL1_NODE_ID}@val1:26656"
    sed -i "s|persistent_peers = \"\"|persistent_peers = \"${VAL1_PEER}\"|" "$CONFIG_TOML"

    echo "[$MONIKER] Genesis copied. Peering with val1: $VAL1_PEER"
fi

# ---------------------------------------------------------------------------
# Phase 3: Start the node
# ---------------------------------------------------------------------------

if [ "$VALIDATOR_INDEX" -eq 1 ]; then
    # Val1 starts immediately as the genesis validator.
    echo "[$MONIKER] Starting genesis validator..."
    exec svoted start --home "$HOME_DIR"
else
    # Joiners start the node in background, wait for sync, then register.
    echo "[$MONIKER] Starting node and registering as validator..."
    svoted start --home "$HOME_DIR" &
    NODE_PID=$!

    # Wait for the node to sync (RPC becomes available).
    echo "[$MONIKER] Waiting for node to sync..."
    for attempt in $(seq 1 120); do
        if curl -sf http://localhost:26657/status > /dev/null 2>&1; then
            break
        fi
        sleep 2
    done

    # Wait a few extra blocks for the chain to be stable.
    sleep 10

    # Register as validator via create-val-tx.
    echo "[$MONIKER] Registering as validator..."
    for attempt in $(seq 1 5); do
        if create-val-tx \
            --home "$HOME_DIR" \
            --moniker "$MONIKER" \
            --amount "$SELF_DELEGATION" \
            --rpc-url "tcp://val1:26657" \
            --chain-id "$CHAIN_ID"; then
            echo "[$MONIKER] Validator registration submitted."
            break
        fi
        echo "[$MONIKER] Registration attempt $attempt failed, retrying in 10s..."
        sleep 10
    done

    # Signal that this validator has registered.
    touch "/shared/val${VALIDATOR_INDEX}-registered"

    # Keep the node running in foreground.
    wait $NODE_PID
fi
