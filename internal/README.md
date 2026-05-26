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

`Processor.Run()` is deterministic:

1. close out expired rounds and emit alerts for unsubmitted shares,
2. clear witness material for expired processable rows,
3. process all ready shares,
4. wait for the earliest scheduled `submit_at`, a schedule-change notification,
   cancellation, or a 30 second maintenance wake.

The maintenance wake exists only so expiry alerts and closeout still run when
no shares are scheduled. Enqueue and retry scheduling changes signal the
processor through a buffered channel so new immediate shares do not wait for
the maintenance wake.

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
| MissedDeadline (4) - not on-chain at closeout | Terminal, no action needed | N/A |
| ObservedOnChain (5) - accepted on-chain before local submission was recorded | Terminal, no action needed | No |

## Wallet Retry Safety

If the server crashes between receiving the HTTP POST and completing the SQLite
insert, the wallet gets an HTTP error and can retry. `Enqueue` is idempotent:
duplicate payloads return `"duplicate"`, and conflicting payloads for the same
`(round_id, share_index, proposal_id, tree_position)` return `409 Conflict`.

## Known Limitations

- Retry budget: `MarkFailed` allows 5 attempts with exponential backoff (2 s,
  4 s, 8 s, 16 s, 32 s). If the chain is unreachable for longer, shares become
  permanently failed. Attempt counts survive recovery.
- Almost-submitted race: if the chain accepted a share but the server crashed
  before `MarkSubmitted`, recovery will retry it. The chain-side share nullifier
  makes the duplicate reveal idempotent.
