#!/bin/bash
# test_join_ci.sh — Smoke test for the validator join flow.
#
# Exercises the core join-dev.sh path without DO Spaces downloads:
#   1. init-validator-keys (generates Cosmos key + Pallas + EA keypairs)
#   2. Node syncing from scratch
#   3. create-val-tx with MsgCreateValidatorWithPallasKey
#   4. Validator appearing in the staking module
#
# Expects: zallyd + create-val-tx in PATH, val1 running on default ports
# (API 1318, RPC 26657, P2P 26656).
#
# Usage:
#   mise run test:join
set -euo pipefail

CHAIN_ID="zvote-1"
JOINER_HOME="$HOME/.zallyd-joiner"
VAL1_HOME="$HOME/.zallyd"
MONIKER="joiner"

# ─── Cleanup ──────────────────────────────────────────────────────────────────

echo "=== Validator join smoke test ==="
echo ""

if [ -d "$JOINER_HOME" ]; then
    echo "Cleaning previous joiner data..."
    rm -rf "$JOINER_HOME"
fi

# ─── Step 1: Initialize joiner node ──────────────────────────────────────────

echo "Initializing joiner node..."
zallyd init "$MONIKER" --chain-id "$CHAIN_ID" --home "$JOINER_HOME" > /dev/null 2>&1

# Copy genesis from val1.
cp "$VAL1_HOME/config/genesis.json" "$JOINER_HOME/config/genesis.json"
zallyd genesis validate-genesis --home "$JOINER_HOME" > /dev/null 2>&1

# ─── Step 2: Generate cryptographic keys ──────────────────────────────────────

echo "Generating validator keys (init-validator-keys)..."
zallyd init-validator-keys --home "$JOINER_HOME"

JOINER_ADDR=$(zallyd keys show validator -a --keyring-backend test --home "$JOINER_HOME")
echo "Joiner address: $JOINER_ADDR"

# ─── Step 3: Configure joiner node ───────────────────────────────────────────

echo "Configuring joiner node..."

CONFIG_TOML="$JOINER_HOME/config/config.toml"
APP_TOML="$JOINER_HOME/config/app.toml"

# Ports: offset from defaults to avoid conflicts with val1.
sed -i.bak "s|laddr = \"tcp://0.0.0.0:26656\"|laddr = \"tcp://0.0.0.0:26756\"|" "$CONFIG_TOML"
sed -i.bak "s|laddr = \"tcp://127.0.0.1:26657\"|laddr = \"tcp://127.0.0.1:26757\"|" "$CONFIG_TOML"
sed -i.bak "s|pprof_laddr = \"localhost:6060\"|pprof_laddr = \"localhost:6070\"|" "$CONFIG_TOML"
sed -i.bak 's/^timeout_broadcast_tx_commit = .*/timeout_broadcast_tx_commit = "120s"/' "$CONFIG_TOML"
rm -f "${CONFIG_TOML}.bak"

# Set persistent_peers to val1.
VAL1_NODE_ID=$(zallyd comet show-node-id --home "$VAL1_HOME")
sed -i.bak "s|persistent_peers = \"\"|persistent_peers = \"${VAL1_NODE_ID}@127.0.0.1:26656\"|" "$CONFIG_TOML"
rm -f "${CONFIG_TOML}.bak"

# API + gRPC ports.
sed -i.bak '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "$APP_TOML"
sed -i.bak "s|address = \"tcp://localhost:1317\"|address = \"tcp://0.0.0.0:1419\"|" "$APP_TOML"
sed -i.bak '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/' "$APP_TOML"
sed -i.bak "s|address = \"localhost:9090\"|address = \"localhost:9190\"|" "$APP_TOML"
sed -i.bak "s|address = \"localhost:9091\"|address = \"localhost:9191\"|" "$APP_TOML"

# [vote] key paths.
sed -i.bak "s|^ea_sk_path = .*|ea_sk_path = \"$JOINER_HOME/ea.sk\"|" "$APP_TOML"
sed -i.bak "s|^pallas_sk_path = .*|pallas_sk_path = \"$JOINER_HOME/pallas.sk\"|" "$APP_TOML"
sed -i.bak "s|^comet_rpc = .*|comet_rpc = \"http://localhost:26757\"|" "$APP_TOML"
rm -f "${APP_TOML}.bak"

# Append [helper] section.
cat >> "$APP_TOML" <<HELPERCFG

###############################################################################
###                         Helper Server                                   ###
###############################################################################

[helper]
disable = false
api_token = ""
db_path = ""
mean_delay = 60
process_interval = 5
chain_api_port = 1419
max_concurrent_proofs = 2
HELPERCFG

# ─── Step 4: Start joiner and wait for sync ──────────────────────────────────

echo "Starting joiner node..."
zallyd start --home "$JOINER_HOME" > joiner.log 2>&1 &
JOINER_PID=$!
echo "Joiner PID: $JOINER_PID"

# Ensure cleanup on exit.
cleanup() {
    echo "Stopping joiner (PID $JOINER_PID)..."
    kill "$JOINER_PID" 2>/dev/null || true
    wait "$JOINER_PID" 2>/dev/null || true
    rm -rf "$JOINER_HOME"
}
trap cleanup EXIT

echo "Waiting for joiner to sync..."
for i in $(seq 1 90); do
    STATUS=$(zallyd status --home "$JOINER_HOME" --node tcp://127.0.0.1:26757 2>/dev/null || echo "")
    if [ -z "$STATUS" ]; then
        sleep 2
        continue
    fi
    CATCHING_UP=$(echo "$STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin)['sync_info']['catching_up'])" 2>/dev/null || echo "True")
    HEIGHT=$(echo "$STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin)['sync_info']['latest_block_height'])" 2>/dev/null || echo "0")
    if [ "$CATCHING_UP" = "False" ] && [ "$HEIGHT" != "0" ]; then
        echo "Joiner synced at block $HEIGHT"
        break
    fi
    if [ "$((i % 10))" -eq 0 ]; then
        echo "  Still syncing... height=$HEIGHT catching_up=$CATCHING_UP ($i/90)"
    fi
    sleep 2
done

# Verify sync completed.
FINAL_STATUS=$(zallyd status --home "$JOINER_HOME" --node tcp://127.0.0.1:26757 2>/dev/null || echo "")
if [ -z "$FINAL_STATUS" ]; then
    echo "FAIL: Joiner node not responding after sync wait"
    exit 1
fi
FINAL_CATCHING=$(echo "$FINAL_STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin)['sync_info']['catching_up'])" 2>/dev/null || echo "True")
if [ "$FINAL_CATCHING" != "False" ]; then
    echo "FAIL: Joiner still catching up after 180s"
    exit 1
fi

# ─── Step 5: Fund the joiner account ─────────────────────────────────────────

echo "Funding joiner account..."
zallyd tx bank send validator "$JOINER_ADDR" 200000stake \
    --home "$VAL1_HOME" --keyring-backend test --chain-id "$CHAIN_ID" -y \
    > /dev/null 2>&1

# Wait for the transfer to commit.
echo "Waiting for balance..."
for i in $(seq 1 30); do
    BALANCE=$(zallyd query bank balances "$JOINER_ADDR" --home "$JOINER_HOME" --node tcp://127.0.0.1:26757 --output json 2>/dev/null \
        | python3 -c "import sys,json; balances=json.load(sys.stdin).get('balances',[]); print(next((b['amount'] for b in balances if b['denom']=='stake'), '0'))" 2>/dev/null || echo "0")
    if [ "$BALANCE" != "0" ] && [ -n "$BALANCE" ]; then
        echo "  Joiner funded: $BALANCE stake"
        break
    fi
    sleep 2
done

if [ "$BALANCE" = "0" ] || [ -z "$BALANCE" ]; then
    echo "FAIL: Joiner account not funded after 60s"
    exit 1
fi

# ─── Step 6: Register as validator ────────────────────────────────────────────

echo "Registering joiner as validator (create-val-tx)..."
create-val-tx \
    --home "$JOINER_HOME" \
    --moniker "$MONIKER" \
    --amount 100000stake \
    --rpc-url tcp://localhost:26657

# Wait for the registration tx to commit.
echo "Waiting for validator registration to commit..."
sleep 6

# ─── Step 7: Verify ──────────────────────────────────────────────────────────

echo "Verifying validator registration..."
VALIDATORS=$(zallyd query staking validators --home "$JOINER_HOME" --node tcp://127.0.0.1:26757 --output json 2>/dev/null)
FOUND=$(echo "$VALIDATORS" | python3 -c "
import sys, json
data = json.load(sys.stdin)
validators = data.get('validators', [])
for v in validators:
    if v.get('description', {}).get('moniker') == 'joiner':
        print(v.get('operator_address', ''))
        break
" 2>/dev/null || echo "")

if [ -z "$FOUND" ]; then
    echo "FAIL: Validator 'joiner' not found in staking module"
    echo "Validators:"
    echo "$VALIDATORS" | python3 -c "import sys,json; [print(f'  {v[\"description\"][\"moniker\"]}') for v in json.load(sys.stdin).get('validators',[])]" 2>/dev/null || true
    exit 1
fi

echo ""
echo "=== PASS: Validator join smoke test ==="
echo "  Moniker:  $MONIKER"
echo "  Valoper:  $FOUND"
echo "  Address:  $JOINER_ADDR"
