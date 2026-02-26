#!/bin/bash
# test_validator_restart.sh — Kill val2, restart it, verify it rejoins consensus.
#
# Expects: 3 validators running from multi:start-chain, chain producing blocks.
# Val2 ports: RPC 26257, P2P 26256, API 1518
#
# Usage:
#   mise run test:restart
set -euo pipefail

VAL2_HOME="$HOME/.zallyd-val2"
VAL2_RPC_PORT=26257
VAL1_RPC_PORT=26157

echo "=== Validator restart test ==="
echo ""

# ─── Step 1: Find and kill val2 ────────────────────────────────────────────

VAL2_PID=$(pgrep -f 'zallyd start --home.*val2' || true)
if [ -z "$VAL2_PID" ]; then
    echo "FAIL: val2 is not running"
    exit 1
fi
echo "Killing val2 (PID $VAL2_PID)..."
kill "$VAL2_PID"

# ─── Step 2: Wait for val2 RPC to go down ──────────────────────────────────

echo "Waiting for val2 RPC to stop..."
for i in $(seq 1 10); do
    if ! curl -sf --max-time 1 "http://127.0.0.1:$VAL2_RPC_PORT/status" > /dev/null 2>&1; then
        echo "  val2 RPC down after ${i}s"
        break
    fi
    if [ "$i" -eq 10 ]; then
        echo "FAIL: val2 RPC still responding after 10s"
        exit 1
    fi
    sleep 1
done

# ─── Step 3: Verify chain keeps producing blocks without val2 ───────────────
# Val1 has 20M stake, val2+val3 have 10M each (total 40M). With val2 down,
# val1+val3 hold 30M = 75% > 2/3 — enough for CometBFT to keep committing.

echo "Checking chain keeps producing blocks without val2..."
HEIGHT_BEFORE=$(curl -sf "http://127.0.0.1:$VAL1_RPC_PORT/status" \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['result']['sync_info']['latest_block_height'])" 2>/dev/null || echo "0")
if [ "$HEIGHT_BEFORE" = "0" ]; then
    echo "FAIL: val1 not responding"
    exit 1
fi
echo "  val1 at block $HEIGHT_BEFORE"

echo "Waiting 10s for blocks to advance..."
sleep 10

HEIGHT_AFTER=$(curl -sf "http://127.0.0.1:$VAL1_RPC_PORT/status" \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['result']['sync_info']['latest_block_height'])" 2>/dev/null || echo "0")
if [ "$HEIGHT_AFTER" -le "$HEIGHT_BEFORE" ]; then
    echo "FAIL: chain stalled while val2 was down (before=$HEIGHT_BEFORE, after=$HEIGHT_AFTER)"
    exit 1
fi
echo "  Chain advanced: $HEIGHT_BEFORE → $HEIGHT_AFTER (val2 down, 2/3 consensus OK)"

# ─── Step 4: Restart val2 ──────────────────────────────────────────────────

echo "Restarting val2..."
ZALLY_PIR_URL=${ZALLY_PIR_URL:-http://localhost:3000} nohup zallyd start --home "$VAL2_HOME" >> sdk/multi-val2.log 2>&1 &
NEW_PID=$!
echo "  val2 restarted (PID $NEW_PID)"

# ─── Step 5: Wait for val2 RPC to come back ────────────────────────────────

echo "Waiting for val2 RPC to come back..."
for i in $(seq 1 60); do
    if curl -sf --max-time 1 "http://127.0.0.1:$VAL2_RPC_PORT/status" > /dev/null 2>&1; then
        echo "  val2 RPC up after ${i}s"
        break
    fi
    if [ "$i" -eq 60 ]; then
        echo "FAIL: val2 RPC not responding after 60s"
        exit 1
    fi
    sleep 1
done

# ─── Step 6: Wait for val2 to catch up ─────────────────────────────────────

echo "Waiting for val2 to catch up..."
for i in $(seq 1 60); do
    STATUS=$(curl -sf "http://127.0.0.1:$VAL2_RPC_PORT/status" 2>/dev/null || echo "")
    if [ -n "$STATUS" ]; then
        CATCHING_UP=$(echo "$STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin)['result']['sync_info']['catching_up'])" 2>/dev/null || echo "True")
        if [ "$CATCHING_UP" = "False" ]; then
            HEIGHT=$(echo "$STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin)['result']['sync_info']['latest_block_height'])" 2>/dev/null || echo "?")
            echo "  val2 caught up at block $HEIGHT"
            break
        fi
    fi
    if [ "$i" -eq 60 ]; then
        echo "FAIL: val2 still catching up after 60s"
        exit 1
    fi
    if [ "$((i % 10))" -eq 0 ]; then
        echo "  Still catching up... ($i/60)"
    fi
    sleep 1
done

# ─── Summary ────────────────────────────────────────────────────────────────

echo ""
echo "=== Status after restart ==="
for i in 1 2 3; do
    rpc_port=$((26057 + i * 100))
    STATUS=$(curl -sf "http://127.0.0.1:$rpc_port/status" 2>/dev/null || echo "")
    if [ -n "$STATUS" ]; then
        HEIGHT=$(echo "$STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin)['result']['sync_info']['latest_block_height'])" 2>/dev/null || echo "?")
        printf "  val%s: block %s\n" "$i" "$HEIGHT"
    else
        printf "  val%s: UNREACHABLE\n" "$i"
    fi
done

echo ""
echo "=== PASS: Validator restart test ==="
