# Observability

This document covers error tracking and diagnostic tooling for `svoted`,
including the ABCI consensus handlers, the public vote API, and the
Helper server.

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

Only unexpected infrastructure errors are reported. Expected conditions
(bad client input, duplicate nullifiers, inactive rounds) are **not** sent
to Sentry.

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
| API handler (`/shielded-vote/v1/shares`) | Internal `Enqueue` errors (500s) |
| API handler (`/shielded-vote/v1/share-status`) | Nullifier check failures (500s) |
| HTTP panic recovery | Any panic in a helper HTTP handler |
| Processor panic recovery | Any panic during share processing |

### Share pipeline observability

The helper share pipeline has distinct stages. Dashboard widgets should not
treat HTTP request counts as durable queue counts.

| Metric | Sentry signal | Meaning |
|--------|---------------|---------|
| Shares Received | HTTP server span for `POST /shielded-vote/v1/shares` | Every request that reaches the share endpoint. Includes invalid requests, unauthorized requests, conflicts, idempotent duplicates, and newly queued shares. |
| Shares Enqueued | `helper.enqueue` span with description `helper.share_enqueued` | A new unique share payload was inserted into the SQLite queue (`EnqueueInserted`) in `ShareStateReceived`. Duplicates and conflicts are excluded. |
| Shares Processed | `helper.process_share` span | A ready queued share was taken by the background processor after its `submit_at` time and processing began. |
| Share Reveals Submitted | HTTP client span for `POST /shielded-vote/v1/reveal-share` | The helper submitted a `MsgRevealShare` to the chain REST API. |
| Shares Confirmed | Share nullifier observed on-chain | Final confirmation that the reveal was accepted on-chain. The `/share-status/{roundId}/{nullifier}` endpoint reports this as `confirmed`. |

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

Upgraded helpers also include round-level accounting totals. These are
helper-local SQLite counters, not chain or consensus state. `accepted_total`
counts new rows accepted into that helper's queue. `active_total` is derived
from the current non-terminal rows in the helper DB. `complete_total` counts
rows that reached a final local outcome. For shares accepted after this upgrade,
the completion reason is split across `completed_by_broadcast_total`,
`completed_by_duplicate_total`, `completed_by_preproof_dedupe_total`, and
`failed_total`. Operators can use
`accepted_total == active_total + complete_total` as the main per-helper
integrity check. Shares that were already submitted before the upgrade only
contribute to `complete_total`; the helper did not record whether those
submissions were broadcast, duplicate, or pre-proof dedupe completions. Older
helpers omit these fields until upgraded.

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

### Release tracking

Each Sentry event is tagged with the binary version (set via ldflags at
build time). This correlates errors to specific deployments and makes
regressions visible in the Sentry releases dashboard.

### Panic recovery

- **HTTP handlers** -- all helper routes are wrapped with the `sentryhttp`
  middleware, which recovers panics and reports them to Sentry before
  returning a 500 response.
- **Processor goroutines** -- each share processing goroutine has a
  `recover()` guard that captures panics to Sentry and marks the share as
  failed, preventing a single bad share from crashing the processor loop.

## Proof generation logging

The processor logs the wall-clock duration of every ZKP #3 proof generation
at `INFO` level:

```
INF proof generated round_id=<hex> share_index=<n> duration=<time>
```

This is useful for spotting degraded prover performance or hardware issues
without requiring a metrics stack.
