# Helper Server - Share Scheduling Model

The helper server accepts encrypted voting shares from wallets, stores them in
SQLite, waits until each wallet-provided `submit_at` time, generates the ZKP 3
share reveal proof, and submits `MsgRevealShare` to the chain.

Timing privacy is owned by the wallet. The helper does not add random
submission delays, random processor wakeups, or intra-batch jitter. If multiple
shares become ready in the same second, the helper processes them together up to
`helper.max_concurrent_proofs`.

## Client-controlled `submit_at`

`POST /shielded-vote/v1/shares` includes `submit_at` in the share payload.
`ShareStore.Enqueue()` persists that value with the payload and schedules the
share for the corresponding Unix second.

- `submit_at = 0` means immediate processing.
- `submit_at > 0` means the share is eligible once that Unix timestamp arrives.
- `submit_at` must not be greater than the round's `vote_end_time`.

The helper accepts same-second collisions without spreading them. This preserves
the wallet's intended schedule exactly.

## Processor wakeups

`Processor.Run()` starts a processing loop and a separate confirmation loop.
The processing loop is deterministic:

1. emit alerts for expired rounds with unsubmitted shares,
2. purge expired round data,
3. process all ready shares,
4. wait for the earliest scheduled `submit_at`, a schedule-change notification,
   cancellation, or a 30 second maintenance wake.

The maintenance wake exists only so expiry alerts and purging still run when no
shares are scheduled. Enqueue and retry scheduling changes signal the processor
through a buffered channel so new immediate shares do not wait for the
maintenance wake.

After the chain REST API accepts a reveal broadcast, the helper records the row
as Confirming and keeps processing other ready shares. The confirmation loop
polls committed share-nullifier state and only marks the row Submitted once the
nullifier is visible on-chain. If the nullifier is not visible before the
confirmation timeout, the row is retried through the normal backoff path.

## Crash Recovery

The helper server is designed for crash-safe operation. Share payloads,
`submit_at`, vote end times, attempt counts, and processing state are persisted
to SQLite with WAL mode enabled. On startup, `NewShareStore` calls `recover()`
which:

1. resets in-flight shares from Witnessed back to Received,
2. rebuilds the round cache from the persisted `rounds` table,
3. restores each pending share to its persisted `submit_at` schedule.

No fresh random delay is assigned during recovery. A recovered share keeps the
same schedule the wallet provided.

### State-by-state behavior

| State at crash | On recovery | Share lost? |
|---|---|---|
| Received (0) - waiting for `submit_at` | Re-enters schedule at persisted `submit_at` | No |
| Witnessed (1) - mid-processing | Reset to Received, re-enters schedule | No |
| Submitted (2) - on chain | Terminal, no action needed | No |
| Failed (3) - permanent failure | Terminal, no action needed | N/A |
| Confirming (4) - broadcast accepted, waiting for committed state | Stays Confirming; confirmation polling resumes | No |

Submitted rows have their witness material cleared as soon as the helper marks
them submitted. Permanently failed rows retain `enc_share_c1`, `enc_share_c2`,
`share_comms`, and `primary_blind` until the helper purges the round after
`vote_end_time`, so local queue exports remain sensitive while a failed round is
still present in `helper.db`. Legacy or imported rows without a known
`vote_end_time` are scrubbed when they become permanently failed because there
is no reliable purge deadline.

## Wallet Retry Safety

If the server crashes between receiving the HTTP POST and completing the SQLite
insert, the wallet gets an HTTP error and can retry. `Enqueue` is idempotent:
duplicate payloads return `"duplicate"`, and conflicting payloads for the same
`(round_id, share_index, proposal_id, tree_position)` return `409 Conflict`.

## Known Limitations

- Retry budget: `MarkFailed` allows 5 attempts with exponential backoff (2 s,
  4 s, 8 s, 16 s, 32 s). If the chain is unreachable for longer, shares become
  permanently failed. Attempt counts survive recovery, and failed-row witness
  material is retained until expired-round purge.
- Confirmation timeout: if a broadcast is accepted but the share nullifier is
  not observed in committed chain state before the timeout, the helper retries
  the share. The chain-side share nullifier makes any duplicate reveal
  idempotent.
