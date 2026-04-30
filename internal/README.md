# Helper Server — Submission Delay Privacy Model

The helper server uses three layers of random delay to make share
submissions temporally unlinkable. Layers 2 and 3 sample from exponential
distributions (Poisson process) via inverse CDF; Layer 1 uses a uniform
distribution to spread shares evenly across the remaining voting window.
All layers use `crypto/rand`, so delays are cryptographically unpredictable.

## Layer 1: Per-share readiness delay

**Where:** `ShareStore.Enqueue()` → `uniformDelay(voteEndTime)`

When a wallet submits a share, it is persisted immediately but not
processed until a random delay elapses. This decouples the time a vote
was cast from the time the share becomes eligible for processing.

- Distribution: Uniform over `[0, remaining_window)` where
  `remaining_window = vote_end_time − now − 60s`
- Floor: delay is raised to at least `min_delay` seconds, preventing
  near-zero samples from making shares trivially linkable
- Cap: delay is clamped so the share is submitted at least 60 seconds
  before `vote_end_time` (cap takes precedence over floor when the
  deadline is imminent)
- On restart, shares get fresh random delays (scheduling is ephemeral)

## Layer 2: Poisson processing cycle

**Where:** `Processor.Run()` → `randomDelay()`

The processor does not wake up at fixed intervals. Instead, the time
between consecutive processing cycles is drawn from an exponential
distribution, making the overall submission pattern a Poisson process.
An observer monitoring chain submissions sees irregularly spaced events
with no periodic structure.

- Distribution: Exp(1/`process_interval`)
- Config: `helper.process_interval` (seconds), default **30**
- Each cycle calls `TakeReady()` and processes any shares whose
  Layer 1 delay has elapsed
- **Urgent-window acceleration:** when any scheduled share belongs to
  a round whose `vote_end_time` is within **5 minutes** of now, the
  mean is divided by **5** (so 6 s instead of 30 s by default). This
  drops `P(wake-up > 60 s)` from ≈13.5 % to ≈4.5×10⁻⁵, making it
  vanishingly unlikely that a last-minute share misses round close.
  Privacy degrades only in the final minutes of an active round —
  preventing vote loss outweighs marginal timing-correlation gains
  when the round is about to end.

## Layer 3: Intra-batch jitter

**Where:** `processBatch()` → `intraShareDelay()`

When multiple shares become ready in the same cycle, each share sleeps
for an additional random duration before proof generation and submission.
This prevents burst patterns where N shares are submitted
near-simultaneously.

- Distribution: Exp(2/`process_interval`) — half the mean of Layer 2
- Not independently configurable; derived from `process_interval`
- **Deadline bypass:** if less than 60 seconds remain before the share's
  `vote_end_time`, the jitter is skipped and the share is submitted
  immediately to avoid missing the deadline

## Configuration summary

| Parameter               | app.toml key                   | Default  | Controls           |
|-------------------------|--------------------------------|----------|--------------------|
| `MinDelay`              | `helper.min_delay`             | 90 s     | Layer 1 floor      |
| `ProcessInterval`       | `helper.process_interval`      | 30 s     | Layer 2 & 3 mean   |
| `MaxConcurrentProofs`   | `helper.max_concurrent_proofs` | 2        | Parallelism        |

## Sampling method

Layers 2 and 3 use inverse CDF exponential sampling:

```
U  = crypto/rand uniform in (0, 1]
delay = -mean * ln(U)
```

This produces an exponentially distributed sample with the given mean.
Exponential inter-arrivals give the Poisson process its memoryless
property — an observer gains no information about the next event from
past observations.

Layer 1 uses uniform sampling over the remaining voting window:

```
U     = crypto/rand uniform in [0, 1)
delay = max(min_delay, U * remaining_window)
```

Uniform spread is preferred over exponential for hold times because
exponential clusters most samples near zero, making shares trivially
linkable to their submission session (see `TestUniformDelayDistribution`).

All layers use `crypto/rand` instead of `math/rand` so that an adversary
who observes submission times cannot reconstruct the PRNG state and
predict future delays.

## Crash Recovery

The helper server is designed for crash-safe operation. Share payloads and
processing state are persisted to SQLite (WAL mode); only scheduling delays
are kept in memory. On startup, `NewShareStore` calls `recover()` which:

1. **Resets in-flight shares** — any share in Witnessed state (taken for
   proof generation but not yet submitted) is rolled back to Received.
2. **Rebuilds the round cache** from the persisted `rounds` table and heals
   shares whose `vote_end_time` was 0 (transient fetch failure at enqueue).
3. **Assigns fresh random delays** to all pending shares. Scheduling is
   intentionally ephemeral — on restart, shares get new uniform delays
   so that an observer cannot predict post-restart timing from pre-crash
   patterns.

### State-by-state behaviour

| State at crash | On recovery | Share lost? |
|---|---|---|
| **Received (0)** — waiting for delay | Fresh random delay, re-enters schedule | No |
| **Witnessed (1)** — mid-processing | Reset to Received, fresh delay, re-processes | No |
| **Submitted (2)** — on chain | Terminal, no action needed | No |
| **Failed (3)** — permanent failure | Terminal, no action needed | N/A |

### Wallet retry safety

If the server crashes between receiving the HTTP POST and completing the
SQLite INSERT, the wallet gets an HTTP error and can retry. `Enqueue` is
idempotent — duplicate payloads return `"duplicate"`, conflicting payloads
for the same `(round_id, share_index, proposal_id, tree_position)` are
rejected with 409 Conflict.

### Known limitations

- **Retry budget**: `MarkFailed` allows 5 attempts with exponential backoff
  (2 s, 4 s, 8 s, 16 s, 32 s ≈ 62 s total). If the chain is unreachable
  for longer, shares become permanently failed. Attempt counts survive
  recovery (not reset), so a share with prior failures has fewer retries
  remaining after restart.
- **Almost-submitted race**: If the chain accepted a share but the server
  crashed before `MarkSubmitted`, the share reverts to Received on recovery
  and is re-processed. The chain rejects the duplicate (nullifier already
  used), consuming retry attempts. The vote itself is safe — it is already
  on chain — but the helper DB may show it as Failed.
- **Synchronous mode**: No explicit `PRAGMA synchronous=FULL` is set.
  SQLite WAL defaults to FULL on most builds, but a power loss (vs process
  crash) could theoretically lose the last committed transaction depending
  on the driver.
