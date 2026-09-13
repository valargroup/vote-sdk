# Helper Server - Share Scheduling Model

The helper server accepts encrypted voting shares from wallets, stores them in
SQLite, waits until each effective `submit_at` time, generates the ZKP 3
share reveal proof, and submits `MsgRevealShare` to the chain.

Timing privacy is owned by the wallet. The helper does not add random
submission delays, random processor wakeups, or intra-batch jitter. If multiple
shares become ready in the same second, the helper processes them together up to
`helper.max_concurrent_proofs_v3`, which defaults to two. The legacy unversioned
and v2 keys are ignored so the binary upgrade moves every validator to the
benchmarked two-worker setting without coordinated local config edits. One
worker drains about 0.58 shares per second, which a wide ballot outruns; each
worker holds roughly 500 MB while proving, so size an explicit v3 override
against the host.

## Client-controlled `submit_at`

`POST /shielded-vote/v1/shares` includes `submit_at` in the share payload.
`ShareStore.Enqueue()` persists an effective value with the payload and
schedules the share for the corresponding Unix second.

- `submit_at = 0` means immediate processing.
- `submit_at > 0` means the share is eligible once that Unix timestamp arrives.
- `submit_at` must be strictly before the round's `vote_end_time`; the API
  separately rejects inactive rounds.
- For an active round, a nonzero past value is clamped to the arrival second so it
  cannot claim queue age. The caller's value is retained as
  `original_submit_at` in queue exports.

The helper accepts same-second collisions without spreading them. Equal-time
shares use a keyed, process-local rank, so connection timing within that second
is not restored as FIFO. This protection requires at least two ready shares
from the same round in one second bucket; a singleton bucket remains correlated
with its arrival time because the helper adds no timing delay.

## Ready-round fairness

Ready shares are scheduled round-robin by `round_id`. Each ready round receives
one processing slot before any ready round receives a second slot. Within a
round, earlier effective `submit_at` values are selected first. Future shares
and retrying shares whose backoff has not elapsed do not participate in the
rotation. Retry targets normally use Unix-second buckets. A near-deadline retry
retains subsecond precision when quantization would erase its positive backoff;
that exceptional retry does not participate in same-time keyed ranking.

The rotation cursor is in memory and continues across bounded processor refills.
It is not persisted: after restart a different ready round may be selected first,
while every share retains its persisted effective schedule.

## Processor wakeups

`Processor.Run()` is deterministic:

1. when the node is caught up and fresh, check committed closure for rounds past
   their local deadline and emit alerts for closed rounds with unsubmitted shares,
2. purge data only for those confirmed closed rounds,
3. process all ready shares,
4. wait for the earliest scheduled `submit_at`, a schedule-change notification,
   cancellation, or a 30 second maintenance wake.

Active rounds and unavailable chain state retain their data even after the local
deadline. The maintenance wake lets closure checks and purging run when no shares
are scheduled. Enqueue and retry scheduling changes signal the processor
through a buffered channel so new immediate shares do not wait for the
maintenance wake.

## Crash Recovery

The helper server is designed for crash-safe operation. Share payloads,
`submit_at`, vote end times, attempt counts, and terminal outcomes are persisted
to SQLite with WAL mode enabled. Worker ownership is process-local: a dequeued
row remains durably Received while its schedule key is held in memory. On
startup, `NewShareStore` calls `recover()`
which:

1. resets legacy Witnessed rows from older binaries back to Received,
2. rebuilds the round cache from the persisted `rounds` table,
3. restores each pending share to its persisted effective `submit_at` schedule.

No fresh random delay is assigned during recovery. A recovered share keeps its
effective schedule; queue exports retain the wallet-provided value separately.
All immediate rows recovered or imported in one operation share one common
second bucket.

### State-by-state behavior

| State at crash | On recovery | Share lost? |
|---|---|---|
| Received (0) - waiting or owned by a worker at crash | Re-enters schedule at persisted `submit_at` | No |
| Witnessed (1) - legacy mid-processing state | Reset to Received, re-enters schedule | No |
| Submitted (2) - on chain | Terminal, no action needed | No |
| Failed (3) - permanent failure | Terminal, no action needed | N/A |

Submitted rows have their witness material cleared as soon as the helper marks
them submitted. Permanently failed rows retain `enc_share_c1`, `enc_share_c2`,
`share_comms`, and `primary_blind` until the helper purges the round after
`vote_end_time`, so local queue exports remain sensitive while a failed round is
still present in `helper.db`. Legacy or imported rows without a known
`vote_end_time` are scrubbed when they become permanently failed because there
is no reliable purge deadline.

If a retained failed row contains malformed `share_comms`, queue export includes
the exact stored bytes as base64 diagnostic data and marks the row corrupt and
nonprocessable. That raw field is witness material and has the same handling
requirements as the rest of the local rescue artifact. Import skips diagnostic
corrupt rows while restoring healthy processable rows.

Malformed `share_comms` is exported this way in any queue state, including a
future `Received` row. Export does not mutate the database state and refuses to
snapshot a round while one of its workers is active. Any SQL execution or scan
failure pauses dequeue with a common retry delay without spending a share
attempt. Only explicit validation of a successfully read row can terminally
classify that row as corrupt. A recovery scan error aborts helper-store startup
instead of silently leaving a persisted share outside the schedule.

## Wallet Retry Safety

If the server crashes between receiving the HTTP POST and completing the SQLite
insert, the wallet gets an HTTP error and can retry. `Enqueue` is idempotent:
duplicate payloads return `"duplicate"`, and conflicting payloads for the same
`(round_id, share_index, proposal_id, tree_position)` return `409 Conflict`.
An authenticated retry that passes payload-consistency and on-chain commitment
validation replaces an unowned, non-submitted row whose stored payload cannot
be decoded and whose state remains independently readable. Active worker
ownership, submitted outcomes, and unreadable states are preserved.

## Known Limitations

- Retry budget: share-specific failures use `MarkFailed`, which allows 5
  attempts with exponential backoff before the terminal attempt (2 s, 4 s,
  8 s, 16 s). Local submit transport errors, non-400 REST errors that do not
  carry a structured chain rejection, round-status check errors, and
  tree/Merkle readiness errors return to pending without spending attempts.
  Generic system retries remain at 10 s. After a share has submitted once at a
  committed height, repeated checks at that same height back off at 10 s, 20 s,
  40 s, 80 s, then 120 s until a newer height is observed. The existing urgent
  retry behavior resumes in the final 30 s of the voting window. Stalled-height
  retry counts are process-local and reset after a restart. Attempt counts
  survive recovery, and failed-row witness material is retained until
  expired-round purge.
- Almost-submitted race: if the chain accepted a share but the server crashed
  before `MarkSubmitted`, recovery will retry it. The chain-side share nullifier
  makes the duplicate reveal idempotent.
