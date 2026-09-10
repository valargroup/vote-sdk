# Helper submission invariants

This document defines the queueing and scheduling behavior that helper changes
must preserve. The helper stores witness material locally until a share reaches
a terminal state or its round is confirmed closed, so scheduling failures can
become share loss rather than ordinary throughput bugs.

## Admission and effective schedule

- `submit_at = 0` is the immediate-submission sentinel. Immediate shares use
  the arrival Unix second as their in-memory schedule bucket.
- A nonzero `submit_at` must be strictly before the round's `vote_end_time`.
- For an active round, a nonzero timestamp in the past is clamped to the arrival
  Unix second and enters the same in-memory bucket as an immediate share. The
  clamped value is the effective `submit_at`; the caller's
  value remains in `original_submit_at` for queue export and diagnosis. The API
  rejects inactive rounds before enqueue.
- A future timestamp is preserved unchanged. No share becomes eligible before
  its effective `submit_at`.
- Exact payload retries remain idempotent after clamping and cannot change an
  existing share's effective schedule.

These rules prevent a caller from claiming arbitrary queue age while preserving
the wallet's future schedule. Normal rescue imports preserve the exported
effective time. `--force-ready` is the explicit trusted operator override.

## Ready-share ordering

The fairness boundary is `round_id`. Voting rounds are created through the
vote-manager-gated chain path, so an ordinary submitter cannot mint round IDs to
obtain additional scheduling weight.

- Only rounds containing at least one currently ready share participate.
- Ready rounds receive one share per rotation before any round receives a
  second share.
- The round-robin cursor continues across bounded `TakeReadyBatch` calls. A new
  ready round is therefore selected within one complete ready-round rotation.
- Within a round, an earlier effective schedule time is selected first.
- Shares with equal effective times are ranked by HMAC-SHA256 under a fresh
  process-local secret. The input is the domain-separated canonical schedule
  key. The rank is cached before sorting and is never persisted, logged, or
  exported. Restart can therefore change equal-time order. The scheduler must
  not use request arrival order or uncommitted witness data as an ordering
  signal.
- Retry backoff supplies a new effective in-memory due time. A retry rejoins its
  round only when that time arrives and receives no special priority. Retry
  targets normally use Unix-second buckets and the same keyed rank for equal
  times. If quantization would erase an intentional positive backoff near
  `vote_end_time`, the retry retains subsecond precision and does not join that
  second's keyed-rank cohort.

The ordering privacy boundary is one round's ready shares within one Unix-second
bucket. The keyed rank only obscures arrival order when that set contains at
least two shares. The helper claims no timing anonymity across seconds or for a
singleton bucket, because it adds no timing delay.

The cursor is process-local. Restart may change which ready round goes first,
but recovery must restore every processable row from its persisted effective
schedule. Cursor state is never required to recover a share.

## State and privacy safety

- A selected row remains durably `Received`. Under the store mutex, its key
  moves from the schedule to a process-local ownership map before it is returned
  to a worker. A key cannot exist in both collections: force-ready imports
  preserve an active owner, and dequeue removes any duplicate schedule entry.
  This is safe because the helper database has an exclusive process lock and
  the processor has one dispatcher.
- A crash discards process-local ownership while leaving the row recoverable as
  `Received`. `Witnessed` is a compatibility-only persisted value: startup
  resets rows from older binaries, and legacy imports write them as `Received`.
- A successfully read row is decoded from permissive driver values. Invalid
  column types, ranges, states, or `share_comms` JSON terminally fail only that
  row. SQL execution and scan errors are never classified as row corruption.
- Any runtime SQL failure retains or restores every unresolved schedule key,
  spends no share attempt, and applies one common retry delay. A recovery scan
  failure aborts store startup instead of silently omitting the row. Database
  damage may pause the queue until operator repair rather than destroying a
  possibly healthy row.
- An authenticated wallet retry whose payload passed witness and on-chain
  commitment validation may replace an unowned row whose payload fails
  deterministic decoding only when its state independently decodes as
  non-submitted. Replacement resets the row to `Received` with a new attempt
  budget. Active ownership, `Submitted`, and unreadable state take precedence
  and are never replaced automatically.
- A failed completion transition removes process-local ownership and restores a
  bounded retry. A committed terminal transition, confirmed row absence, or
  successful worker ownership transfer is required before a schedule key stays
  removed.
- A bounded take never returns the same row twice and never consumes capacity
  for a future or stale row.
- Cancellation and retry release process-local ownership and reschedule the
  still-`Received` row without spending a deterministic-failure attempt.
- Every worker return has an idempotent fallback tied to that specific dequeue
  attempt. It requeues only ownership from the same attempt; normal completion
  or ownership transferred to a later retry makes the fallback a no-op.
- Active ownership has no wall-clock expiry. Vote end alone does not prove that
  a worker has stopped, so cleanup must not clear ownership and risk dispatching
  the same share twice.
- Submitted witness material is scrubbed only after committed-state deduplication
  confirms the reveal. Closed-round cleanup and rescue export reject active
  rounds even if a future caller bypasses the processor's worker drain.
- Scheduling may interleave public rounds. It must not create voter-level flows
  or expose hidden share fields through deterministic tie ordering.

## Required regression tests

The following tests name the executable contract for scheduling changes:

- `TestTakeReadyBatch_OldestFirstWithinRound`
- `TestTakeReadyBatch_RoundRobinAcrossCalls`
- `TestTakeReadyBatch_RoundRobinWithinLargeBatch`
- `TestTakeReadyBatch_FreshRoundWithinOneRotation`
- `TestTakeReadyBatch_ExcludesFutureRoundUntilDue`
- `TestEnqueue_ClampsPastSubmitAtToArrival`
- `TestRecovery_PreservesClampedAndOriginalSubmitAt`
- `TestRecovery_ScanFailureDoesNotSilentlyStrandShare`
- `TestTakeReadyBatch_CorruptOldestFailsOnlyThatShare`
- `TestTakeReadyBatch_ValueTypeCorruptionIsTerminal`
- `TestTakeReadyBatch_StateValueCorruptionIsTerminal`
- `TestTakeReadyBatch_StoreFailureAppliesGlobalBackoff`
- `TestTakeReadyBatch_MissingSchemaRetainsReceivedShare`
- `TestTakeReadyBatch_UsesProcessLocalOwnership`
- `TestTakeReadyBatch_DropsScheduleEntryForInFlightShare`
- `TestEnqueue_ReplacesUnownedCorruptRow`
- `TestEnqueue_ReplacesTerminalizedCorruptRow`
- `TestEnqueue_DoesNotReplaceActiveCorruptRow`
- `TestEnqueue_DoesNotResurrectSubmittedCorruptRow`
- `TestEnqueue_DoesNotReplaceCorruptRowWithUnreadableState`
- `TestEnqueue_CorruptRowReplacementFailurePreservesRow`
- `TestMarkSubmitted_WriteLockRestoresScheduleAndBacksOff`
- `TestWorkerCompletionWithoutOwnerIsIgnored`
- `TestRequeueInFlightIfOwned`
- `TestRequeueInFlightIfOwned_ExpiredRoundUsesBackoff`
- `TestProcessor_ProcessBatch_PanicResolvesOwnershipOnce`
- `TestImportQueueForceReadyPreservesInFlightOwnership`
- `TestExportQueueRejectsActiveRound`
- `TestEnqueue_ImmediateAndClampedShareScheduleBucket`
- `TestTakeReadyBatch_EqualSecondUsesOpaqueRank`
- `TestRecovery_ImmediateRowsShareOneOpaqueBucket`
- `TestRetrySchedulesUseCommonSecondBucket`
- `TestExportQueue_IncludesUnclassifiedCorruptDiagnostic`

Changes to these invariants require the matching tests and a privacy review of
any new ordering signal before merge.
