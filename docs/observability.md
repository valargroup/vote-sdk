# Observability

This document covers error tracking and diagnostic tooling for `svoted`,
including the ABCI consensus handlers, the public vote API, and the
Helper server.

## Prometheus metrics

`svoted` exposes Prometheus text on the REST API listener at:

```
GET /metrics
```

Newly generated `app.toml` files enable Cosmos SDK telemetry and its
Prometheus sink by default. The helper collectors and endpoint are also
available after a binary upgrade when an older `app.toml` still has telemetry
disabled. The historical `?format=prometheus` query remains compatible, but is
not required.

The endpoint gathers the process-wide Prometheus registry. Its response
therefore contains both the direct `svote_*` collectors and metrics emitted
through the Cosmos SDK HashiCorp Prometheus sink.

Prometheus scrape-target labels such as `instance` identify the helper host.
The application does not put hostnames, voting round IDs, share indexes,
proposal IDs, tree positions, transaction hashes, or payload fields into
metric labels.

### Helper APM metrics

| Metric | Meaning |
|--------|---------|
| `svote_helper_share_submission_requests_total{outcome,reason}` | Wallet-facing share requests by bounded final result. |
| `svote_helper_share_submission_in_flight` | Share requests currently executing. |
| `svote_helper_share_submission_duration_seconds{outcome,reason}` | End-to-end latency for `POST /shielded-vote/v1/shares`. |
| `svote_helper_share_submission_stage_duration_seconds{stage,result}` | Ingress latency split across readiness, decoding, validation, round checks, commitment-tree verification, and enqueue. |
| `svote_helper_share_processing_attempts_total{outcome,stage}` | Background processing attempts by bounded final result and stage. |
| `svote_helper_share_processing_in_flight` | Worker attempts currently executing. Compare this with the configured proof concurrency to detect saturation. |
| `svote_helper_share_processing_duration_seconds{outcome,stage}` | End-to-end latency after a queued share is assigned to a worker. |
| `svote_helper_share_processing_stage_duration_seconds{stage,result}` | Worker latency split across round status, pre-proof dedupe, tree reads, payload decoding, proof generation, and chain broadcast. |

Commitment polling between proof slots records `waiting_for_retry` at
`retry_schedule`. A share confirmed after round closure records `confirmed`.

All latency histograms have buckets through 180 seconds, including explicit
10, 15, 20, and 30 second boundaries. To compare p95 ingress stage latency
between helpers over five minutes:

```promql
histogram_quantile(
  0.95,
  sum by (instance, stage, le) (
    rate(svote_helper_share_submission_stage_duration_seconds_bucket[5m])
  )
)
```

To count requests taking longer than 15 seconds:

```promql
sum by (instance) (
  rate(svote_helper_share_submission_duration_seconds_count[5m])
  - ignoring(le) rate(svote_helper_share_submission_duration_seconds_bucket{le="15"}[5m])
)
```

Use the equivalent processing-stage histogram to distinguish proof generation
from local chain broadcast latency:

```promql
histogram_quantile(
  0.95,
  sum by (instance, stage, le) (
    rate(svote_helper_share_processing_stage_duration_seconds_bucket[5m])
  )
)
```

Ingress infrastructure failures and worker saturation are visible with:

```promql
sum by (instance, outcome, reason) (
  rate(svote_helper_share_submission_requests_total{outcome=~"failed|unavailable"}[5m])
)

svote_helper_share_processing_in_flight
```

### Vote transaction verification metrics

The custom vote ante pipeline emits the following metrics for delegation,
single-cast, cast-batch, atomic delegation-and-cast, reveal-share, and tally
transactions:

| Metric | Meaning |
|--------|---------|
| `svote_vote_tx_verification_attempts_total{tx_type,mode,outcome,stage}` | Verification attempts and the stage at which a rejected transaction failed. |
| `svote_vote_tx_verification_in_flight{tx_type,mode}` | Verification attempts currently executing. |
| `svote_vote_tx_verification_duration_seconds{tx_type,mode,outcome}` | End-to-end validation latency. |
| `svote_vote_tx_verification_stage_duration_seconds{tx_type,mode,stage,result}` | Latency for state checks, signatures, state/root lookups, synthetic-root derivation, and individual proofs. |
| `svote_vote_tx_verification_batch_size{tx_type}` | Number of cast votes in cast-batch and atomic delegation-and-cast transactions. |

The bounded `mode` label distinguishes `check_tx`, `recheck_tx`, and
`finalize_block`. Batch signature and proof stages produce one histogram
observation per cast, while the end-to-end histogram produces one observation
per transaction. This makes both per-proof latency and total batch cost visible
without using vote indexes or transaction identifiers as labels.

To compare p95 proof-verification latency by transaction type and validator:

```promql
histogram_quantile(
  0.95,
  sum by (instance, tx_type, stage, le) (
    rate(svote_vote_tx_verification_stage_duration_seconds_bucket{
      stage=~"delegation_proof|cast_proof"
    }[5m])
  )
)
```

To find full verification attempts that take longer than 15 seconds:

```promql
sum by (instance, tx_type, mode) (
  rate(svote_vote_tx_verification_duration_seconds_count{
    mode=~"check_tx|finalize_block"
  }[5m])
  - ignoring(le) rate(svote_vote_tx_verification_duration_seconds_bucket{
    mode=~"check_tx|finalize_block",
    le="15"
  }[5m])
)
```

## Sentry error tracking

Sentry project: **svote-helper** (slug: `svote-helper-vm`) in the
`valar-group` org. Dashboard:
https://valar-group.sentry.io/dashboard/3836839/

The Helper server supports optional [Sentry](https://sentry.io) integration
for capturing infrastructure errors. When disabled (the default), the Sentry
SDK is never initialized and adds zero overhead.

### Configuration

The Sentry DSN can be provided in three ways (highest priority first):

1. **`app.toml`** -- set `sentry_dsn` under the `[helper]` section:

   ```toml
   [helper]
   sentry_dsn = "https://...@sentry.io/..."
   ```

2. **Init-time environment variable** -- set `SVOTE_HELPER_SENTRY_DSN` before
   running `scripts/init.sh` or `scripts/init_multi.sh`. The value is baked
   into `app.toml` during chain initialization:

   ```bash
   SVOTE_HELPER_SENTRY_DSN="https://...@sentry.io/..." bash scripts/init.sh
   ```

3. **Runtime environment variable** -- set `SENTRY_DSN` when starting the
   binary. This is useful for injecting the secret via Docker, systemd, or
   CI without touching config files:

   ```bash
   SENTRY_DSN="https://...@sentry.io/..." svoted start
   ```

If `app.toml` has a non-empty `sentry_dsn`, it takes precedence over the
`SENTRY_DSN` environment variable.

Set `SENTRY_ENVIRONMENT` to `staging` or `production` on managed fleets. The
binary defaults to `production` only for local/backward-compatible starts where
no explicit environment is available.

Sentry error events remain enabled at 100% in both managed environments.
Performance tracing is disabled in `staging`. In `production`, the helper
share-processing transaction and the high-volume `share-status`, `round`, and
`vote-managers` polling routes are sampled at 10%; write routes and other
transactions remain fully traced. Child spans inherit their root transaction's
sampling decision. Use Prometheus metrics, rather than sampled Sentry spans, for
exact request, processing, outcome, and latency totals.

### CI / deploy

Both the `sdk-chain-deploy` and `sdk-chain-reset` workflows read
`SENTRY_DSN` from the selected GitHub Environment secret and append it to
`/etc/default/svoted` on each host. They also write
`SENTRY_ENVIRONMENT` from the workflow's `target_environment`. The
`svoted.service` systemd unit loads this file via `EnvironmentFile=`, and the
Go binary picks up `SENTRY_DSN` at runtime as a fallback when `app.toml` has no
`sentry_dsn`.

The same workflows also write optional primary-only operational secrets, such
as `CONFIG_PR_GITHUB_TOKEN`, to the primary's `/etc/default/svoted` under the
runtime environment variable name expected by `svoted`.

Inventory the Sentry project with:

```bash
sentry-cli projects list --org valar-group
```

The chain fleet uses project `svote-helper-vm`. Add `SENTRY_DSN` as a GitHub
Environment secret under both `staging` and `production`. The host-side
`SENTRY_ENVIRONMENT` is derived from the selected workflow environment.

### What gets captured

Known caller errors remain visible through HTTP responses and bounded counters,
but are not captured as Sentry error events. Helper-owned and unexpected errors
remain alertable. When tracing is enabled, rejected requests can still appear as
scrubbed HTTP performance transactions.

#### ABCI consensus handlers

| Source | Errors captured |
|--------|----------------|
| PrepareProposal — tally | KV iteration failures, accumulator check errors, partial decryption count errors, threshold decryption failures (Lagrange / BSGS), tx encoding errors |
| PrepareProposal — DKG contribution | Round lookup failures, threshold computation errors, Shamir split / Feldman commit failures, coefficient write errors, Pallas PK unmarshal errors, ECIES encryption failures, tx encoding errors |
| PrepareProposal — ceremony ack | Round lookup failures, share recovery errors (ECIES decryption, Feldman verification), share disk write errors, tx encoding errors |
| PrepareProposal — partial decrypt | KV iteration failures, existing submission check errors, tally read errors, ciphertext unmarshal errors, off-curve D_i, DLEQ proof generation failures, tx encoding errors |
| ProcessProposal | Every block REJECT — invalid DKG contribution, invalid ack, invalid partial decrypt, or invalid tally |

#### Ante handler (vote tx validation)

| Source | Errors captured |
|--------|----------------|
| `ValidateVoteTx` | `ErrInvalidProof` (ZKP verification failure) and `ErrInvalidSignature` (RedPallas signature failure) only |

Parameter validation errors (`ValidateBasic`, `ErrRoundNotActive`,
`ErrDuplicateNullifier`, `ErrInvalidAnchorHeight`, etc.) are **not**
captured — they represent expected invalid client input.

#### Public vote API

| Source | Errors captured |
|--------|----------------|
| `broadcastVoteTx` — encode | 500: tx encoding failure (internal bug) |
| `broadcastVoteTx` — broadcast | 502: CometBFT broadcast RPC error (node unreachable or failing) |

422 CheckTx rejections are **not** captured — they represent expected
invalid votes.

#### Helper server

| Source | Errors captured |
|--------|----------------|
| Processor (`processShare`) | Proof generation failures, tree read errors, chain submission errors |
| Processor (round check) | Round status check failures (KV store errors) |
| API handler (`/shielded-vote/v1/shares`) | Validator, round-reader, commitment-tree, and internal `Enqueue` failures (500s/503s) |
| API handler (`/shielded-vote/v1/share-status`) | Nullifier check failures (500s) |
| HTTP panic recovery | Any panic in a helper HTTP handler |
| Processor panic recovery | Any panic during share processing |

At ingress, malformed JSON, invalid encodings or ranges, noncanonical fields,
invalid curve points, inconsistent share commitments, proposals or options not
present in the authenticated round, unknown or inactive rounds, invalid
schedules, and duplicate conflicts are caller errors. They return a 4xx
response and increment a fixed-path `helper.share_submission` counter without
creating an incident event. Exact idempotent duplicates for an active round
remain 200. If a configured validator, round reader, or commitment reader is
unavailable, the helper returns 503 instead of accepting an unchecked share.
The API does not impose processing headroom. It accepts immediate submissions
while the round is active and scheduled submissions strictly before round end.

### Share pipeline observability

The helper share pipeline has distinct stages. Dashboard widgets should not
treat HTTP request counts as durable queue counts. In production, Sentry counts
for the sampled helper processing and polling transactions are estimates; use
the corresponding Prometheus counters for exact operational totals. Staging
does not send performance spans.

| Metric | Sentry signal | Meaning |
|--------|---------------|---------|
| Shares Received | HTTP server span for `POST /shielded-vote/v1/shares` | Every request that reaches the share endpoint. Includes invalid requests, unauthorized requests, conflicts, idempotent duplicates, and newly queued shares. |
| Shares Enqueued | `helper.enqueue` span with description `helper.share_enqueued` | A new unique share payload was inserted into the SQLite queue (`EnqueueInserted`) in `ShareStateReceived`. Duplicates and conflicts are excluded. |
| Shares Processed | `helper.process_share` span | A ready queued share was taken by the background processor after its `submit_at` time and processing began. |
| Share Reveals Submitted | HTTP client span for `POST /shielded-vote/v1/reveal-share` | The helper submitted a `MsgRevealShare` to the chain REST API. |
| Shares Confirmed | Share nullifier observed on-chain | Final confirmation that the reveal was accepted on-chain. The `/share-status/{roundId}/{nullifier}` endpoint reports this as `confirmed`. |
| Share Submission Outcomes | `helper.share_submission.<outcome>.<reason>` counter | Bounded ingress outcomes such as queued, duplicate, invalid JSON, inactive round, or internal failure. No share identifiers or payload fields are attached. |

For an individual share, the intended lifecycle is:

```
Received request -> Enqueued -> Processed -> Submitted -> Confirmed
```

`Shares Received` can be greater than `Shares Enqueued` because endpoint hits
include duplicate, conflicting, unauthorized, and invalid submissions. Within a
fixed dashboard time window, `Enqueued`, `Processed`, and `Submitted` may also
cross window boundaries: a share can be enqueued before the window and processed
inside it, or enqueued inside the window with a future `submit_at`.

### Public queue summaries

Helpers expose a public coarse queue histogram at:

```
GET /shielded-vote/v1/queue-summary/{round_id}
```

The endpoint is enabled by `[helper].expose_queue_summary = true`, which is the
default. Operators that do not want to expose even coarse round-level queue
counts can set it to `false`.

The response is round-level only. It does not include proposal IDs, vote
decisions, share indices, nullifiers, tree positions, exact submit times, or
payload material. The helper chooses the bucket size from the vote duration:

| Vote duration | Bucket size |
|---------------|-------------|
| 21 days or more | 6 hours |
| 7 days or more | 3 hours |
| 1 day or more | 1 hour |
| 1 hour or more | 15 minutes |
| Less than 1 hour | 1 minute |

Each bucket reports `submitted`, `pending_future`, `overdue_pending`,
`processing`, `failed`, and `total`. `overdue_pending` means a share is still
waiting in the helper DB even though its `submit_at` time has passed.

The response also reports top-level `ready`, `not_yet_due`, and `processing`
queue depth. These fields use the helper's effective in-memory schedule, so a
share delayed by retry backoff is `not_yet_due` even when its original
`submit_at` has passed. `processing` comes from process-local worker ownership;
the durable row remains `Received` until its outcome is recorded. Histogram
placement remains based on the persisted
effective submit time and does not move during retries. Queue rescue exports
retain a clamped caller value separately as `original_submit_at`.
Retry due times use Unix-second buckets, so multiple retries for one round can
share the same opaque scheduling rank boundary.

`last_minute_start` marks the final 40% of the round, capped at six hours.
Older shares target their final retry around the earlier of this boundary or
48 hours after their first attempt, with jitter clipped at that cap. Shares
first attempted inside this window can retry through its remainder, leaving a
separate safety margin before the voting deadline.

The benchmark-only authenticated `/shielded-vote/v1/queue-status` response
exposes the same three fields per round. Its existing `pending` field remains
the aggregate of all nonterminal shares for compatibility; normally,
`pending = ready + not_yet_due + processing`.

The admin UI has a monitor route at `/queue-monitor` that reads `vote_servers[]`
from `/api/voting-config`, queries each helper's queue summary, and overlays the
bucket histograms across the vote period. It also highlights unavailable
helpers, stale summaries, the current time, and the final-minute window.

### Tags

Every captured error includes contextual tags where available:

- `handler` -- which handler produced the error (`PrepareProposal`, `ProcessProposal`, `ante`, `broadcastVoteTx`)
- `stage` -- processing stage within the handler (e.g. `threshold_tally_decryption`, `ack_dkg_round`, `encode_pd_tx`)
- `tag` -- injected tx type for ProcessProposal rejections (`dkg_contribution`, `ack`, `partial_decrypt`, `tally`)
- `msg_type` -- Go type of the vote message (ante and API errors)
- `round_id` -- the voting round identifier (hex)
- `share_index` -- the share index within the round (helper only)
- `alert` -- `helper_share_failure` for a failed processing attempt or
  `helper_round_closed` for the close-time unsubmitted-share summary
- `failure_action` -- `retry` when the helper retains the share for another
  attempt, or `failed` when the attempt uses the bounded failed-share budget

Share-processing alerts use a stable Sentry fingerprint containing the alert
class, local helper identity, round, processing stage, and queue action.
Round-close summaries group by alert class, local helper identity, and round.
The share index stays diagnostic context, so retries and multiple shares group
together. These alerts come from the locally running helper process; they do
not probe or monitor other helper servers.

### Release tracking

Each Sentry event is tagged with the binary version (set via ldflags at
build time). This correlates errors to specific deployments and makes
regressions visible in the Sentry releases dashboard.

### Panic recovery

- **HTTP handlers** -- all helper routes are wrapped with the `sentryhttp`
  middleware, which recovers panics and reports them to Sentry before
  returning a 500 response. It can also create sampled transactions for 4xx
  responses. Ordinary 4xx responses do not create Sentry error events. The
  Sentry hooks remove helper request bodies and the `X-Helper-Token` header from
  both error and transaction events.
- **Processor goroutines** -- each share processing goroutine has a
  `recover()` guard that captures panics to Sentry and marks the share as
  failed, preventing a single bad share from crashing the processor loop.

## Proof generation logging

Successful helper construction logs the effective proof worker width at
`INFO` level, regardless of timing diagnostics:

```
INF helper constructed proof_concurrency=<n>
```

The processor logs the wall-clock duration of every ZKP #3 proof generation
at `INFO` level:

```
INF proof generated round_id=<hex> share_index=<n> duration=<time>
```

This is useful for spotting degraded prover performance or hardware issues
without requiring a metrics stack.
