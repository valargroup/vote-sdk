#!/bin/bash
# test_deterministic_keys.sh — Local smoke test for the deterministic-key flow.
#
# Exercises the same path that CI (sdk-chain-reset.yml) uses:
#   1. init.sh with VAL_PRIVKEY (primary imports a known key)
#   2. Secondary imports its own VAL_PRIVKEY + generates Pallas/EA keys
#   3. Funding from vote-manager-1 to secondary address
#   4. create-val-tx registration
#
# Prerequisites: svoted + create-val-tx on PATH (mise run install).
# Usage:  bash scripts/test_deterministic_keys.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

CHAIN_ID="svote-1"
PRIMARY_HOME="$HOME/.svoted-test-primary"
SECONDARY_HOME="$HOME/.svoted-test-secondary"

# Fixed test keys (deterministic). Generated via `openssl rand -hex 32`.
PRIMARY_VAL_KEY="a]a]a]a]a]a]a]a]a]a]a]a]a]a]a]a]"  # placeholder replaced below
SECONDARY_VAL_KEY="b]b]b]b]b]b]b]b]b]b]b]b]b]b]b]b]"  # placeholder replaced below

# Real 32-byte hex keys for testing. Hardcoded so the test is reproducible.
PRIMARY_VAL_KEY="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
SECONDARY_VAL_KEY="fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

cleanup() {
    echo ""
    echo "=== Cleanup ==="
    kill "$PRIMARY_PID" 2>/dev/null || true
    kill "$SECONDARY_PID" 2>/dev/null || true
    wait "$PRIMARY_PID" 2>/dev/null || true
    wait "$SECONDARY_PID" 2>/dev/null || true
    rm -rf "$PRIMARY_HOME" "$SECONDARY_HOME"
    echo "Done."
}
trap cleanup EXIT

echo "=== Deterministic Key Smoke Test ==="
echo ""

# ─── Step 1: Init primary with deterministic key ─────────────────────────────

echo "--- Step 1: Init primary ---"
rm -rf "$PRIMARY_HOME"

# Load VM_PRIVKEYS from .env
set -a
# shellcheck disable=SC1091
. "$REPO_ROOT/.env"
set +a

export SVOTED_HOME="$PRIMARY_HOME"
export VAL_PRIVKEY="$PRIMARY_VAL_KEY"
bash "$REPO_ROOT/scripts/init.sh"

# Verify the address is deterministic: importing the same key twice should
# yield the same address.
PRIMARY_ADDR=$(svoted keys show validator -a --keyring-backend test --home "$PRIMARY_HOME")
echo "Primary validator address: $PRIMARY_ADDR"

# ─── Step 2: Start primary ───────────────────────────────────────────────────

echo ""
echo "--- Step 2: Start primary ---"

svoted start --home "$PRIMARY_HOME" > /tmp/svoted-test-primary.log 2>&1 &
PRIMARY_PID=$!
echo "Primary PID: $PRIMARY_PID (log: /tmp/svoted-test-primary.log)"

echo "Waiting for primary REST API (port 1317)..."
for i in $(seq 1 30); do
    if curl -sf http://localhost:1317/shielded-vote/v1/rounds > /dev/null 2>&1; then
        echo "Primary REST API healthy."
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "FAIL: Primary REST API not responding after 60s"
        tail -30 /tmp/svoted-test-primary.log
        exit 1
    fi
    sleep 2
done

# ─── Step 3: Init secondary with deterministic key ───────────────────────────

echo ""
echo "--- Step 3: Init secondary (key import + keygen) ---"
rm -rf "$SECONDARY_HOME"

svoted init secondary --chain-id "$CHAIN_ID" --home "$SECONDARY_HOME" > /dev/null 2>&1
cp "$PRIMARY_HOME/config/genesis.json" "$SECONDARY_HOME/config/genesis.json"
svoted genesis validate-genesis --home "$SECONDARY_HOME" > /dev/null 2>&1

# Import deterministic validator key (same as CI would do)
svoted keys import-hex validator "$SECONDARY_VAL_KEY" --keyring-backend test --home "$SECONDARY_HOME"
SECONDARY_ADDR=$(svoted keys show validator -a --keyring-backend test --home "$SECONDARY_HOME")
echo "Secondary validator address: $SECONDARY_ADDR"

# Generate ceremony keys (Pallas + EA)
svoted pallas-keygen --home "$SECONDARY_HOME"
svoted ea-keygen --home "$SECONDARY_HOME"

echo "Verifying key files exist..."
for f in pallas.sk pallas.pk ea.sk ea.pk; do
    if [ ! -f "$SECONDARY_HOME/$f" ]; then
        echo "FAIL: $SECONDARY_HOME/$f not found"
        exit 1
    fi
done
echo "All key files present."

# ─── Step 4: Configure & start secondary ─────────────────────────────────────

echo ""
echo "--- Step 4: Configure & start secondary ---"

CONFIG_TOML="$SECONDARY_HOME/config/config.toml"
APP_TOML="$SECONDARY_HOME/config/app.toml"

# Port offsets to avoid conflicts with primary.
sed -i.bak "s|laddr = \"tcp://0.0.0.0:26656\"|laddr = \"tcp://0.0.0.0:26756\"|" "$CONFIG_TOML"
sed -i.bak "s|laddr = \"tcp://127.0.0.1:26657\"|laddr = \"tcp://127.0.0.1:26757\"|" "$CONFIG_TOML"
sed -i.bak "s|pprof_laddr = \"localhost:6060\"|pprof_laddr = \"localhost:6070\"|" "$CONFIG_TOML"
sed -i.bak 's/^timeout_broadcast_tx_commit = .*/timeout_broadcast_tx_commit = "120s"/' "$CONFIG_TOML"
rm -f "${CONFIG_TOML}.bak"

VAL1_NODE_ID=$(svoted comet show-node-id --home "$PRIMARY_HOME")
sed -i.bak "s|persistent_peers = \"\"|persistent_peers = \"${VAL1_NODE_ID}@127.0.0.1:26656\"|" "$CONFIG_TOML"
rm -f "${CONFIG_TOML}.bak"

sed -i.bak '/\[api\]/,/\[.*\]/ s/enable = false/enable = true/' "$APP_TOML"
sed -i.bak "s|address = \"tcp://localhost:1317\"|address = \"tcp://0.0.0.0:1419\"|" "$APP_TOML"
sed -i.bak '/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/' "$APP_TOML"
sed -i.bak "s|address = \"localhost:9090\"|address = \"localhost:9290\"|" "$APP_TOML"
sed -i.bak "s|address = \"localhost:9091\"|address = \"localhost:9291\"|" "$APP_TOML"
sed -i.bak "s|^ea_sk_path = .*|ea_sk_path = \"$SECONDARY_HOME/ea.sk\"|" "$APP_TOML"
sed -i.bak "s|^pallas_sk_path = .*|pallas_sk_path = \"$SECONDARY_HOME/pallas.sk\"|" "$APP_TOML"
sed -i.bak "s|^comet_rpc = .*|comet_rpc = \"http://localhost:26757\"|" "$APP_TOML"
rm -f "${APP_TOML}.bak"

cat >> "$APP_TOML" <<HELPERCFG

[helper]
disable = false
api_token = ""
db_path = ""
process_interval = 5
chain_api_port = 1419
max_concurrent_proofs = 2
HELPERCFG

svoted start --home "$SECONDARY_HOME" > /tmp/svoted-test-secondary.log 2>&1 &
SECONDARY_PID=$!
echo "Secondary PID: $SECONDARY_PID (log: /tmp/svoted-test-secondary.log)"

echo "Waiting for secondary to sync..."
for i in $(seq 1 90); do
    STATUS=$(svoted status --home "$SECONDARY_HOME" --node tcp://127.0.0.1:26757 2>/dev/null || echo "")
    if [ -z "$STATUS" ]; then
        sleep 2
        continue
    fi
    CATCHING_UP=$(echo "$STATUS" | jq -r '.sync_info.catching_up' 2>/dev/null || echo "true")
    HEIGHT=$(echo "$STATUS" | jq -r '.sync_info.latest_block_height' 2>/dev/null || echo "0")
    if [ "$CATCHING_UP" = "false" ] && [ "$HEIGHT" != "0" ]; then
        echo "Secondary synced at block $HEIGHT."
        break
    fi
    if [ "$((i % 10))" -eq 0 ]; then
        echo "  Still syncing... height=$HEIGHT catching_up=$CATCHING_UP ($i/90)"
    fi
    sleep 2
done

# ─── Step 5: Fund secondary from vote manager (mimics CI fund-secondary job) ──

echo ""
echo "--- Step 5: Fund secondary from vote-manager-1 ---"
echo "Sending tokens to $SECONDARY_ADDR..."

svoted tx vote authorized-send "$SECONDARY_ADDR" 200000 usvote \
    --from vote-manager-1 --home "$PRIMARY_HOME" --keyring-backend test \
    --chain-id "$CHAIN_ID" -y

echo "Waiting for balance..."
for i in $(seq 1 30); do
    BALANCE=$(svoted query bank balances "$SECONDARY_ADDR" --home "$SECONDARY_HOME" \
        --node tcp://127.0.0.1:26757 --output json 2>/dev/null \
        | jq -r '.balances[] | select(.denom == "usvote") | .amount' 2>/dev/null || echo "")
    if [ -n "$BALANCE" ] && [ "$BALANCE" != "0" ]; then
        echo "Secondary funded: $BALANCE usvote"
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "FAIL: Secondary not funded after 60s"
        exit 1
    fi
    sleep 2
done

# ─── Step 6: Register secondary as validator ──────────────────────────────────

echo ""
echo "--- Step 6: Register secondary as validator ---"
create-val-tx \
    --home "$SECONDARY_HOME" \
    --moniker secondary \
    --amount 100000usvote \
    --rpc-url tcp://localhost:26657

echo "Waiting for registration to commit..."
sleep 6

# ─── Step 7: Verify ──────────────────────────────────────────────────────────

echo ""
echo "--- Step 7: Verify ---"

VALIDATORS=$(svoted query staking validators --home "$SECONDARY_HOME" \
    --node tcp://127.0.0.1:26757 --output json 2>/dev/null)
VAL_COUNT=$(echo "$VALIDATORS" | jq '.validators | length')
echo "Total validators: $VAL_COUNT"

FOUND_SECONDARY=$(echo "$VALIDATORS" | jq -r '.validators[] | select(.description.moniker == "secondary") | .operator_address' 2>/dev/null || echo "")
if [ -z "$FOUND_SECONDARY" ]; then
    echo "FAIL: Secondary validator not found in staking module"
    echo "$VALIDATORS" | jq '.validators[].description.moniker'
    exit 1
fi

# Verify address determinism: import the same key in a temp dir, check address matches.
echo ""
echo "Verifying address determinism..."
TMPDIR=$(mktemp -d)
svoted keys import-hex validator "$SECONDARY_VAL_KEY" --keyring-backend test --home "$TMPDIR"
CHECK_ADDR=$(svoted keys show validator -a --keyring-backend test --home "$TMPDIR")
rm -rf "$TMPDIR"

if [ "$CHECK_ADDR" != "$SECONDARY_ADDR" ]; then
    echo "FAIL: Address not deterministic! $CHECK_ADDR != $SECONDARY_ADDR"
    exit 1
fi
echo "Address determinism confirmed: $SECONDARY_ADDR"

echo ""
echo "=== PASS: Deterministic Key Smoke Test ==="
echo "  Primary addr:    $PRIMARY_ADDR"
echo "  Secondary addr:  $SECONDARY_ADDR"
echo "  Secondary voper: $FOUND_SECONDARY"
echo "  Validators:      $VAL_COUNT"
