# Docker Testnet

A Docker Compose setup for running a multi-validator testnet on a single
machine. Used for measuring block times, DKG ceremony latency, and
end-to-end voting lifecycle with realistic CometBFT consensus.

## Prerequisites

- Docker Desktop (or Docker Engine + Compose plugin)
- ~8 GB RAM available for containers (30 validators)

## Quick start

```bash
# Build image and start 30-validator testnet (~2 min first build, cached after)
make docker-testnet

# Tear down (stops containers, removes volumes)
make docker-testnet-down
```

Custom validator count:

```bash
DOCKER_TESTNET_VALIDATORS=10 make docker-testnet
```

## What happens on startup

1. All 30 containers start in parallel, each initializes a node and publishes
   its address to a shared Docker volume.
2. **val1** (genesis validator) waits for all addresses, builds genesis with
   pre-funded accounts, and starts producing blocks.
3. **val2-val30** copy genesis, peer with val1, sync, and register as
   validators via `create-val-tx`.
4. Within ~10-15 seconds all 30 validators are bonded and producing blocks.

## Ports

Only val1 exposes ports to the host. All inter-validator traffic stays on
the internal Docker network.

| Port | Service |
|---|---|
| `localhost:1318` | REST API (Cosmos SDK + custom vote endpoints) |
| `localhost:26157` | CometBFT RPC |

## Monitoring

```bash
# Follow all logs
docker compose -f docker/docker-compose.yml logs -f

# Follow a specific validator
docker compose -f docker/docker-compose.yml logs -f val15

# Check block height
docker exec val1 curl -sf http://localhost:26657/status | \
  python3 -c "import sys,json; d=json.load(sys.stdin)['result']['sync_info']; print(f\"height={d['latest_block_height']}\")"

# Count bonded validators
curl -sf 'http://localhost:1318/cosmos/staking/v1beta1/validators?pagination.limit=100' | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(sum(1 for v in d['validators'] if v['status']=='BOND_STATUS_BONDED'))"

# Check vote-manager set (any-of-N)
curl -sf http://localhost:1318/shielded-vote/v1/vote-managers | python3 -m json.tool
```

## Running the lifecycle test

The lifecycle test creates a voting round, monitors the DKG ceremony across
all 30 validators, waits for voting to close, and reports tally timings.

```bash
# Testnet must be running with all validators bonded
bash docker/test-lifecycle.sh
```

The script:
1. Creates a `MsgCreateVotingSession` via val1's CLI
2. Polls the round through PENDING → DKG (REGISTERING → DEALT → CONFIRMED) → ACTIVE
3. Waits for `vote_end_time` (3 min from creation)
4. Monitors TALLYING → FINALIZED
5. Prints per-phase timings and PrepareProposal durations from validator logs

### Extracting PrepareProposal timings

After the test (or any DKG ceremony), extract the per-validator timings:

```bash
# Heaviest PrepareProposal blocks across all validators (sorted by duration)
for i in $(seq 1 30); do
  docker logs "val$i" 2>&1 | sed 's/\x1b\[[0-9;]*m//g' | \
    grep "duration_ms=" | grep -E "duration_ms=[1-9][0-9]*" | \
    sed "s/^/[val$i] /"
done | sort -t= -k2 -n -r | head -20

# Per-validator peak ack times (the heaviest per-block operation)
for i in $(seq 1 30); do
  MS=$(docker logs "val$i" 2>&1 | sed 's/\x1b\[[0-9;]*m//g' | \
    grep "ack injector done" | grep -oE 'duration_ms=[0-9]+' | \
    grep -oE '[0-9]+' | sort -n | tail -1)
  [ -n "$MS" ] && [ "$MS" -gt 0 ] && echo "  val$i: ${MS}ms"
done
```

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                  Docker Compose                      │
│                                                      │
│  val1 (genesis) ◄──► val2  ◄──► val3  ... val30     │
│    │                                                 │
│    │ ports: 26657→26157, 1317→1318                   │
└────┼─────────────────────────────────────────────────┘
     │
     ▼
   Host (curl, test-lifecycle.sh)
```

- Star topology: all validators peer with val1 via `persistent_peers`
- Shared Docker volume (`/shared`) for genesis distribution during init
- Each container runs `docker/entrypoint.sh` which handles genesis creation
  (val1) or joining (val2+)

## Files

| File | Purpose |
|---|---|
| `docker/Dockerfile` | 3-stage build: Rust circuits → Go binaries → Debian runtime |
| `docker/entrypoint.sh` | Per-container init: genesis (val1) or join + register (val2+) |
| `docker/generate-compose.sh` | Generates `docker-compose.yml` for N validators |
| `docker/test-lifecycle.sh` | Drives full voting lifecycle and reports timings |

## Benchmark results

See [blocktimes.md](blocktimes.md) for the full timing data from a 30-validator
run, including per-phase PrepareProposal latencies and Osmosis comparison.
