# Block Time Configuration

`svoted` overrides CometBFT v0.38 defaults at startup via `initCometBFTConfig()`
in `cmd/svoted/cmd/commands.go` to reduce end-to-end block time. The approach
follows [Osmosis](https://github.com/osmosis-labs/osmosis), which applies the
same class of overrides on their mainnet (v31, CometBFT v0.38).

## Parameter overrides

| Parameter | svoted | Osmosis mainnet | CometBFT default | Purpose |
|---|---|---|---|---|
| `timeout_propose` | **1.8s** | 1.4s | 3s | Max wait for a block proposal before prevoting nil |
| `timeout_commit` | **800ms** | 400ms | 1s | Idle delay after commit before starting next height |
| `peer_gossip_sleep_duration` | **50ms** | 50ms | 100ms | Sleep between gossip rounds (faster vote propagation) |
| `p2p.flush_throttle_timeout` | **80ms** | 80ms | 100ms | P2P message flush interval |

### Why 1.8s instead of Osmosis's 1.4s

Osmosis's `PrepareProposal` is essentially mempool tx selection (<1ms). Ours
performs cryptographic work during the DKG ceremony (ECIES encryption/decryption,
Feldman share verification) and tally (Lagrange interpolation, BSGS discrete log).
The extra 400ms headroom over Osmosis accounts for this, while benchmarks confirm
the actual peak is well under 120ms (see below).

## Observed block time

With the current `800ms` `timeout_commit`, blocks that contain transactions can
still commit at roughly **1.2s** cadence. The previous `400ms` `timeout_commit`
setting produced **~0.91s average** blocks in a 30-validator Docker testnet,
compared to ~4-6s with CometBFT defaults.

## Benchmarks

Safety of these parameters was confirmed through two rounds of measurement:

### 1. In-process ABCI benchmarks (`app/abci_bench_test.go`)

Unit-test-style benchmarks using `TestApp` (in-memory DB, no network). Measures
pure compute cost of each ABCI handler.

```
go test -count=1 -run TestABCILatencies -v -timeout 10m ./app/...
```

**Results with n=30, t=15:**

| Operation | Latency | % of 1.8s budget |
|---|---|---|
| DKG contribute (29 ECIES encryptions) | 22ms | 1.2% |
| DKG ack (29 ECIES decryptions + Feldman verify) | 82ms | 4.6% |
| Tally decryption (Lagrange + BSGS, cold) | 121ms | 6.7% |
| Tally decryption (BSGS warm) | 6ms | 0.3% |
| EndBlocker (Poseidon tree root) | 2-8ms | <0.5% |
| Partial decrypt | 2-5ms | <0.3% |

### 2. 30-validator Docker testnet (`docker/test-lifecycle.sh`)

Full CometBFT consensus with 30 containers, real disk I/O, network gossip.
Measures production-realistic wall-clock timings.

```
make docker-testnet
bash docker/test-lifecycle.sh
```

**Results:**

| Metric | Value |
|---|---|
| Validators | 30 bonded |
| Average block time (`400ms` `timeout_commit` baseline) | 0.91s |
| DKG ceremony wall-clock | 56s (~60 blocks) |
| DKG contribute per block | 11-17ms |
| DKG ack per block (peak) | **116ms** (val20), median ~80ms |
| Partial decrypt per block | 1-3ms |
| Tally (empty round) | 4s (~4 blocks) |
| `timeout_propose` hits | 2-3 per validator over 663 blocks (0.4%, caused by scheduling, not compute) |
| `timeout_prevote` hits | 0 |
| `timeout_precommit` hits | 0 |
| Round retries (round > 0) | 0 |

The heaviest single-block operation (DKG ack at 116ms) uses **6.4%** of the
1.8s `timeout_propose` budget, leaving a **15x safety margin**.

## How the timeouts interact with the consensus pipeline

```
Propose ──────────► Prevote ──────────► Precommit ──────────► Commit
  │                   │                    │                    │
  │ timeout_propose   │ timeout_prevote    │ timeout_precommit  │ timeout_commit
  │ (1.8s)            │ (1s default)       │ (1s default)       │ (800ms)
  │                   │                    │                    │
  ▼                   ▼                    ▼                    ▼
PrepareProposal    Internal CometBFT    Internal CometBFT    BeginBlock
ProcessProposal    voting               voting               DeliverTx
                                                              EndBlock
                                                              Commit
```

- `timeout_propose` gates how long validators wait for a proposal (which
  includes `PrepareProposal` compute + network delivery). This is the primary
  parameter that must accommodate our DKG/tally crypto work.
- `timeout_commit` is an idle delay after the block is committed. Setting it
  to 800ms keeps active block production faster than CometBFT defaults while
  targeting a roughly 1.2s cadence when there are transactions.
- `timeout_prevote` and `timeout_precommit` are left at the 1s CometBFT
  default. They are not performance-critical for our workload.

## References

- [Osmosis consensus config overrides](https://github.com/osmosis-labs/osmosis/blob/main/cmd/osmosisd/cmd/root.go) (`recommendedConfigTomlValues`)
- [CometBFT v0.38 configuration docs](https://docs.cometbft.com/v0.38/core/configuration)
- [CometBFT ADR-115: Predictable block times](https://github.com/cometbft/cometbft/blob/main/docs/references/architecture/adr-115-predictable-block-times.md)
