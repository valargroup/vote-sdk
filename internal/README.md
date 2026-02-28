# Helper Server — Submission Delay Privacy Model

The helper server uses three layers of exponential delay to make share
submissions temporally unlinkable. All layers sample from exponential
distributions via inverse CDF with `crypto/rand`, so delays are
cryptographically unpredictable.

## Layer 1: Per-share readiness delay

**Where:** `ShareStore.Enqueue()` → `cappedExponentialDelay(voteEndTime)`

When a wallet submits a share, it is persisted immediately but not
processed until a random delay elapses. This decouples the time a vote
was cast from the time the share becomes eligible for processing.

- Distribution: Exp(1/`mean_delay`)
- Config: `helper.mean_delay` (seconds), default **43200** (12 hours)
- Cap: delay is clamped so the share is submitted at least 60 seconds
  before `vote_end_time`
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
| `MeanDelay`             | `helper.mean_delay`            | 43200 s  | Layer 1 mean       |
| `ProcessInterval`       | `helper.process_interval`      | 30 s     | Layer 2 & 3 mean   |
| `MaxConcurrentProofs`   | `helper.max_concurrent_proofs` | 2        | Parallelism        |

## Sampling method

All three layers use the same inverse CDF technique:

```
U  = crypto/rand uniform in (0, 1]
delay = -mean * ln(U)
```

This produces an exponentially distributed sample with the given mean.
`crypto/rand` is used instead of `math/rand` so that an adversary who
observes submission times cannot reconstruct the PRNG state and predict
future delays.
