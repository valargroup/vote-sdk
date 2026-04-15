# Observability

This document covers error tracking and diagnostic tooling for `svoted`,
including the ABCI consensus handlers, the public vote API, and the
Helper server.

## Sentry error tracking

Sentry project: **svote-helper** (slug: `svote-helper-vm`) in the
`valar-group` org. Dashboard:
https://valar-group.sentry.io/projects/svote-helper-vm/

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

### CI / deploy

The `sdk-chain-reset` GitHub Actions workflow reads `SENTRY_DSN` from
repository secrets and passes it as `SVOTE_HELPER_SENTRY_DSN` to
`init_multi.sh` during chain reinitialization. Add the secret at:

```
Settings > Secrets and variables > Actions > SENTRY_DSN
```

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
