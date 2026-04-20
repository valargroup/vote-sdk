#!/bin/bash
#
# test-lifecycle.sh — Drive a full voting lifecycle on the Docker testnet
# and measure DKG ceremony + tally timings with 30 validators.
#
# Prerequisites: docker compose testnet running (make docker-testnet)
#
# Usage:
#   bash docker/test-lifecycle.sh
set -e

API="http://localhost:1318"
CHAIN_ID="svote-1"

# Time in seconds from now until voting closes.
# Must be long enough for: DKG (30 contribute + 30 ack blocks ~60s) + buffer.
VOTE_DURATION=180

log() { echo "[$(date +%H:%M:%S)] $*"; }

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

api_get() {
    curl -sf "$API$1" 2>/dev/null
}

val1_exec() {
    docker exec val1 "$@"
}

wait_for_api() {
    log "Waiting for REST API at $API..."
    for i in $(seq 1 60); do
        if curl -sf "$API/shielded-vote/v1/vote-managers" > /dev/null 2>&1; then
            log "API is ready."
            return 0
        fi
        sleep 2
    done
    log "ERROR: API not available after 120s"
    exit 1
}

# ---------------------------------------------------------------------------
# Phase 0: Verify testnet is running
# ---------------------------------------------------------------------------

log "=== Phase 0: Verify testnet ==="

RUNNING=$(docker ps --filter "name=val" --format "{{.Names}}" | wc -l | tr -d ' ')
log "Running containers: $RUNNING"
if [ "$RUNNING" -lt 2 ]; then
    log "ERROR: Expected at least 2 running validator containers. Is the testnet up?"
    exit 1
fi

wait_for_api

BONDED=$(api_get "/cosmos/staking/v1beta1/validators?pagination.limit=100" | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(sum(1 for v in d['validators'] if v['status']=='BOND_STATUS_BONDED'))")
log "Bonded validators: $BONDED"

# ---------------------------------------------------------------------------
# Phase 1: Create voting session
# ---------------------------------------------------------------------------

log ""
log "=== Phase 1: Create voting session ==="

VM_ADDR=$(api_get "/shielded-vote/v1/vote-managers" | python3 -c "import sys,json; print(json.load(sys.stdin)['vote_manager_addresses'][0])")
log "Vote manager: $VM_ADDR"

VOTE_END_TIME=$(python3 -c "import time; print(int(time.time()) + $VOTE_DURATION)")
log "Vote end time: $VOTE_END_TIME ($(python3 -c "import datetime; print(datetime.datetime.fromtimestamp($VOTE_END_TIME).strftime('%H:%M:%S'))"))"

# Field element values must be canonical Pallas Fp (LE < modulus).
# Small values (a few bytes then zeros) are always valid.
HASH_A="0100000000000000000000000000000000000000000000000000000000000000"
HASH_B="0200000000000000000000000000000000000000000000000000000000000000"
HASH_C="0300000000000000000000000000000000000000000000000000000000000000"
HASH_D="0400000000000000000000000000000000000000000000000000000000000000"

SESSION_JSON=$(cat <<ENDJSON
{
  "snapshot_height": 10,
  "snapshot_blockhash": "$HASH_A",
  "proposals_hash": "$HASH_B",
  "vote_end_time": $VOTE_END_TIME,
  "nullifier_imt_root": "$HASH_C",
  "nc_root": "$HASH_D",
  "description": "Benchmark test round",
  "proposals": [
    {
      "id": 1,
      "title": "Benchmark Proposal A",
      "options": [
        {"index": 0, "label": "Support"},
        {"index": 1, "label": "Oppose"}
      ]
    }
  ]
}
ENDJSON
)

val1_exec bash -c "cat > /tmp/session.json << 'EOF'
$SESSION_JSON
EOF"

log "Submitting MsgCreateVotingSession..."
CREATE_START=$(date +%s)

val1_exec svoted tx vote create-voting-session /tmp/session.json \
    --from vote-manager-1 \
    --keyring-backend test \
    --home /root/.svoted \
    --chain-id "$CHAIN_ID" \
    --node tcp://localhost:26657 \
    --yes \
    --output json 2>/dev/null | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(f'  tx_hash: {d.get(\"txhash\", \"n/a\")}')
    print(f'  code:    {d.get(\"code\", \"n/a\")}')
except:
    print('  (tx submitted)')
" || log "  (broadcast submitted, checking chain...)"

sleep 3

# ---------------------------------------------------------------------------
# Phase 2: Monitor DKG ceremony
# ---------------------------------------------------------------------------

log ""
log "=== Phase 2: DKG Ceremony ==="

# Find the round.
# Status codes: 1=ACTIVE, 2=TALLYING, 3=FINALIZED, 4=PENDING
# vote_round_id is base64; convert to hex for the /round/{id} endpoint.
log "Polling for new round..."
ROUND_ID=""
for i in $(seq 1 30); do
    ROUNDS_JSON=$(api_get "/shielded-vote/v1/rounds")
    if [ -n "$ROUNDS_JSON" ]; then
        ROUND_ID=$(echo "$ROUNDS_JSON" | python3 -c "
import sys, json, base64
d = json.load(sys.stdin)
rounds = d.get('rounds', [])
for r in rounds:
    status = r.get('status', 0)
    # status 4=PENDING, 1=ACTIVE
    if status in (4, 1) or str(status) in ('4','1'):
        rid_b64 = r.get('vote_round_id', '')
        rid_hex = base64.b64decode(rid_b64).hex()
        print(rid_hex)
        break
" 2>/dev/null)
    fi
    if [ -n "$ROUND_ID" ]; then
        break
    fi
    sleep 2
done

if [ -z "$ROUND_ID" ]; then
    log "ERROR: No round found after 60s"
    log "Checking all rounds:"
    api_get "/shielded-vote/v1/rounds" | python3 -m json.tool 2>/dev/null || true
    exit 1
fi

log "Round ID: $ROUND_ID"

# Status codes: 1=ACTIVE, 2=TALLYING, 3=FINALIZED, 4=PENDING
# Ceremony codes: 1=REGISTERING, 2=DEALT, 3=CONFIRMED
STATUS_NAMES=("UNSPECIFIED" "ACTIVE" "TALLYING" "FINALIZED" "PENDING")
CEREMONY_NAMES=("UNSPECIFIED" "REGISTERING" "DEALT" "CONFIRMED")

DKG_START=$(date +%s)
LAST_STATUS=""
LAST_CEREMONY=""

log "Monitoring DKG ceremony progress..."
while true; do
    ROUND_JSON=$(api_get "/shielded-vote/v1/round/$ROUND_ID" 2>/dev/null)
    if [ -z "$ROUND_JSON" ]; then
        sleep 1
        continue
    fi

    eval "$(echo "$ROUND_JSON" | python3 -c "
import sys, json
d = json.load(sys.stdin)
r = d.get('round', d)
print(f'STATUS={r.get(\"status\", 0)}')
print(f'CEREMONY={r.get(\"ceremony_status\", 0)}')
print(f'ACKS={len(r.get(\"ceremony_acks\", []))}')
print(f'CONTRIBS={len(r.get(\"dkg_contributions\", []))}')
" 2>/dev/null)"

    STATUS_NAME="${STATUS_NAMES[$STATUS]:-$STATUS}"
    CEREMONY_NAME="${CEREMONY_NAMES[$CEREMONY]:-$CEREMONY}"

    if [ "$STATUS" != "$LAST_STATUS" ] || [ "$CEREMONY" != "$LAST_CEREMONY" ]; then
        ELAPSED=$(($(date +%s) - DKG_START))
        log "  [${ELAPSED}s] status=$STATUS_NAME ceremony=$CEREMONY_NAME contribs=$CONTRIBS acks=$ACKS"
        LAST_STATUS="$STATUS"
        LAST_CEREMONY="$CEREMONY"
    fi

    # status=1 means ACTIVE (DKG complete)
    if [ "$STATUS" = "1" ]; then
        DKG_END=$(date +%s)
        DKG_DURATION=$((DKG_END - DKG_START))
        log ""
        log "DKG ceremony complete! Duration: ${DKG_DURATION}s"
        break
    fi

    ELAPSED=$(($(date +%s) - DKG_START))
    if [ "$ELAPSED" -gt 300 ]; then
        log "ERROR: DKG ceremony timed out after 300s"
        log "Final state: status=$STATUS_NAME ceremony=$CEREMONY_NAME contribs=$CONTRIBS acks=$ACKS"
        exit 1
    fi

    sleep 2
done

# ---------------------------------------------------------------------------
# Phase 3: Wait for voting to end (skip actual votes -- ZKP proofs required)
# ---------------------------------------------------------------------------

log ""
log "=== Phase 3: Waiting for vote_end_time ==="

NOW=$(date +%s)
REMAINING=$((VOTE_END_TIME - NOW))
if [ "$REMAINING" -gt 0 ]; then
    log "Voting period ends in ${REMAINING}s. Waiting..."
    # Poll every 10s to show progress
    while [ "$(date +%s)" -lt "$VOTE_END_TIME" ]; do
        REMAINING=$((VOTE_END_TIME - $(date +%s)))
        if [ "$REMAINING" -le 0 ]; then break; fi
        log "  ${REMAINING}s remaining..."
        SLEEP_TIME=$((REMAINING < 10 ? REMAINING : 10))
        sleep "$SLEEP_TIME"
    done
fi
log "Voting period ended."

# ---------------------------------------------------------------------------
# Phase 4: Monitor tally
# ---------------------------------------------------------------------------

log ""
log "=== Phase 4: Tally ==="

TALLY_START=$(date +%s)
LAST_STATUS=""

log "Monitoring ACTIVE -> TALLYING -> FINALIZED..."
while true; do
    ROUND_JSON=$(api_get "/shielded-vote/v1/round/$ROUND_ID" 2>/dev/null)
    if [ -z "$ROUND_JSON" ]; then
        sleep 1
        continue
    fi

    STATUS=$(echo "$ROUND_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('round',d); print(r.get('status',0))" 2>/dev/null)
    STATUS_NAME="${STATUS_NAMES[$STATUS]:-$STATUS}"

    if [ "$STATUS" != "$LAST_STATUS" ]; then
        ELAPSED=$(($(date +%s) - TALLY_START))
        log "  [${ELAPSED}s] status=$STATUS_NAME"
        LAST_STATUS="$STATUS"
    fi

    # status=3 means FINALIZED
    if [ "$STATUS" = "3" ]; then
        TALLY_END=$(date +%s)
        TALLY_DURATION=$((TALLY_END - TALLY_START))
        log ""
        log "Round finalized! Tally duration: ${TALLY_DURATION}s"
        break
    fi

    ELAPSED=$(($(date +%s) - TALLY_START))
    if [ "$ELAPSED" -gt 120 ]; then
        log "ERROR: Tally timed out after 120s"
        log "Final state: status=$STATUS_NAME"
        exit 1
    fi

    sleep 2
done

# ---------------------------------------------------------------------------
# Phase 5: Query results and PrepareProposal timings
# ---------------------------------------------------------------------------

log ""
log "=== Phase 5: Results ==="

RESULTS=$(api_get "/shielded-vote/v1/tally-results/$ROUND_ID")
log "Tally results:"
echo "$RESULTS" | python3 -m json.tool 2>/dev/null || echo "$RESULTS"

log ""
log "=== PrepareProposal timings from val1 logs ==="
docker logs val1 2>&1 | grep "PrepareProposal:" | grep -v "duration_ms=0" | tail -30

log ""
log "=== PrepareProposal timings from val15 logs ==="
docker logs val15 2>&1 | grep "PrepareProposal:" | grep -v "duration_ms=0" | tail -30

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

TOTAL_END=$(date +%s)
TOTAL_DURATION=$((TOTAL_END - CREATE_START))

log ""
log "========================================="
log "=== 30-Validator Lifecycle Summary ==="
log "========================================="
log ""
log "  Validators bonded:       $BONDED"
log "  DKG ceremony duration:   ${DKG_DURATION}s"
log "  Tally duration:          ${TALLY_DURATION}s"
log "  Total lifecycle:         ${TOTAL_DURATION}s"
log ""
log "  Block time config:"
log "    timeout_propose:   1.8s"
log "    timeout_commit:    400ms"
log ""

# Count non-zero PrepareProposal timings
DKG_CONTRIBUTE_COUNT=$(docker logs val1 2>&1 | grep "dkg-contribute injector done" | grep -v "duration_ms=0" | wc -l | tr -d ' ')
ACK_COUNT=$(docker logs val1 2>&1 | grep "ack injector done" | grep -v "duration_ms=0" | wc -l | tr -d ' ')
PD_COUNT=$(docker logs val1 2>&1 | grep "partial-decrypt injector done" | grep -v "duration_ms=0" | wc -l | tr -d ' ')
TALLY_COUNT=$(docker logs val1 2>&1 | grep "tally injector done" | grep -v "duration_ms=0" | wc -l | tr -d ' ')

log "  PrepareProposal activity (val1):"
log "    DKG contribute blocks: $DKG_CONTRIBUTE_COUNT"
log "    Ack blocks:            $ACK_COUNT"
log "    Partial decrypt blocks: $PD_COUNT"
log "    Tally blocks:          $TALLY_COUNT"
log ""
log "Done."
