package helper

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"cosmossdk.io/log"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

const (
	maintenanceInterval              = 30 * time.Second
	processingReadinessRetryInterval = 10 * time.Second
)

var errAwaitingRetrySlot = errors.New("waiting for scheduled retry attempt")

var errAwaitingCommit = errors.New("broadcast accepted; awaiting committed transaction")

// ErrCheckTxNotReady means BaseApp has not received its first post-restart
// block time yet. Processing should wait without generating a proof.
var ErrCheckTxNotReady = errors.New("local CheckTx block time is not initialized")

type waitingForNewBlockError struct {
	height uint64
}

func (e *waitingForNewBlockError) Error() string {
	return fmt.Sprintf("share already submitted at the latest committed height: %d", e.height)
}

const (
	failureStagePanic            = "panic"
	failureStageRoundStatusCheck = "round_status_check"
	failureStageCommitmentCheck  = "commitment_check"
	failureStageRoundClosed      = "round_closed_unsubmitted_shares"
	failureStageDecodeRoundID    = "decode_round_id"
	failureStageTreeStatus       = "tree_status"
	failureStageMerklePath       = "merkle_path"
	failureStageDecodePayload    = "decode_payload"
	failureStageProofGenerate    = "proof_generate"
	failureStageSubmitHTTP       = "submit_http"
	failureStageSubmitChain      = "submit_chain_reject"
)

const (
	helperShareFailureAlert = "helper_share_failure"
	helperRoundClosedAlert  = "helper_round_closed"
)

type shareFailureAction int

const (
	shareFailureRetry shareFailureAction = iota
	shareFailureFail
)

type shareProcessingError struct {
	action shareFailureAction
	stage  string
	err    error
}

// Error returns the wrapped processing failure message.
func (e *shareProcessingError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

// Unwrap returns the underlying processing error.
func (e *shareProcessingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// retryableShareError marks err as a system retry that should not spend a
// failed-share attempt.
func retryableShareError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &shareProcessingError{action: shareFailureRetry, stage: stage, err: err}
}

// failedShareAttemptError marks err as a share processing failure that should
// use the existing MarkFailed retry budget.
func failedShareAttemptError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &shareProcessingError{action: shareFailureFail, stage: stage, err: err}
}

// classifyShareFailure returns the queue action and Sentry stage for err.
func classifyShareFailure(err error) (shareFailureAction, string) {
	var processingErr *shareProcessingError
	if errors.As(err, &processingErr) {
		return processingErr.action, processingErr.stage
	}
	return shareFailureFail, "process_share"
}

// wrapSubmitError classifies local REST submit failures separately from
// structured chain rejections.
func wrapSubmitError(err error) error {
	var statusErr *submitHTTPStatusError
	if errors.As(err, &statusErr) && statusErr.statusCode == http.StatusBadRequest {
		return failedShareAttemptError(failureStageSubmitHTTP, fmt.Errorf("submit: %w", err))
	}
	return retryableShareError(failureStageSubmitHTTP, fmt.Errorf("submit: %w", err))
}

// wrapProofGenerateError keeps prover failures on the bounded failed-attempt
// path. The real Halo2 wrapper returns deterministic input, deserialization,
// proof generation, or unknown-code errors for unchanged share inputs.
func wrapProofGenerateError(err error) error {
	return failedShareAttemptError(failureStageProofGenerate, fmt.Errorf("generate proof: %w", err))
}

// isCanceledShareError reports whether processing stopped because the caller
// canceled the context.
func isCanceledShareError(err error) bool {
	return errors.Is(err, context.Canceled)
}

// Processor is the background share processing loop. It checks the share queue
// when wallet-provided submit_at times arrive, generates Merkle paths and ZKP
// 3 proofs, and submits MsgRevealShare to the chain.
type Processor struct {
	now               func() time.Time
	store             *ShareStore
	tree              TreeReader
	prover            ProofGenerator
	submitter         *ChainSubmitter
	logger            log.Logger
	metrics           *helperMetrics
	maxConcurrent     int
	isRoundActive     RoundStatusChecker
	isRoundClosed     RoundClosureChecker
	isNodeReady       func() bool
	preProofDedupe    *preProofShareDeduper
	maintenanceEvery  time.Duration
	readinessRetry    time.Duration
	submitHeightMu    sync.Mutex
	submitHeight      uint64
	submitKeys        map[string]struct{}
	stalledRetryCount map[string]uint8
}

type ProcessorOption func(*Processor)

// WithRoundClosureCheck enables cleanup only after committed round closure.
// Without a checker, the processor retains queued data and emits no close alerts.
func WithRoundClosureCheck(isRoundClosed RoundClosureChecker) ProcessorOption {
	return func(p *Processor) { p.isRoundClosed = isRoundClosed }
}

type preProofShareDeduper struct {
	vcHash      VCHashFunc
	shareNFHash ShareNullifierHashFunc
	shareNF     ShareNullifierChecker
}

// WithProcessingReadinessCheck prevents queued shares from being taken while
// the local chain state is catching up or stale.
func WithProcessingReadinessCheck(isNodeReady func() bool) ProcessorOption {
	return func(p *Processor) {
		p.isNodeReady = isNodeReady
	}
}

// WithPreProofShareDeduper enables an optional cheap share-nullifier lookup
// before proof generation and closure handling. Committed shares skip proving,
// and invalid local inputs spend a failed attempt. Unavailable chain lookups
// still allow scheduled proofs in active rounds, but defer closure cleanup.
func WithPreProofShareDeduper(
	vcHash VCHashFunc,
	shareNFHash ShareNullifierHashFunc,
	shareNF ShareNullifierChecker,
) ProcessorOption {
	return func(p *Processor) {
		p.preProofDedupe = newPreProofShareDeduper(vcHash, shareNFHash, shareNF)
	}
}

func newPreProofShareDeduper(
	vcHash VCHashFunc,
	shareNFHash ShareNullifierHashFunc,
	shareNF ShareNullifierChecker,
) *preProofShareDeduper {
	if vcHash == nil || shareNFHash == nil || shareNF == nil {
		return nil
	}
	return &preProofShareDeduper{
		vcHash:      vcHash,
		shareNFHash: shareNFHash,
		shareNF:     shareNF,
	}
}

// NewProcessor creates an idle share processor. maxConcurrent bounds the full
// per-share pipeline and values below one are normalized to one; processing
// does not begin until Run is called.
func NewProcessor(
	store *ShareStore,
	tree TreeReader,
	prover ProofGenerator,
	submitter *ChainSubmitter,
	logger log.Logger,
	maxConcurrent int,
	isRoundActive RoundStatusChecker,
	options ...ProcessorOption,
) *Processor {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	p := &Processor{
		now:               time.Now,
		store:             store,
		tree:              tree,
		prover:            prover,
		submitter:         submitter,
		logger:            logger,
		metrics:           defaultHelperMetrics,
		maxConcurrent:     maxConcurrent,
		isRoundActive:     isRoundActive,
		maintenanceEvery:  maintenanceInterval,
		readinessRetry:    processingReadinessRetryInterval,
		submitKeys:        make(map[string]struct{}),
		stalledRetryCount: make(map[string]uint8),
	}
	for _, option := range options {
		option(p)
	}
	return p
}

// Run processes scheduled shares with at most maxConcurrent in flight. It
// blocks until ctx is cancelled, then waits for all workers to leave every
// dequeued share in a terminal or retryable state and returns ctx.Err.
//
// Run must not be called concurrently on the same Processor.
func (p *Processor) Run(ctx context.Context) error {
	// active counts every dequeued share, including work buffered between the
	// dispatcher and a worker. Matching channel capacities keep both the work
	// set and completion notifications bounded by maxConcurrent.
	jobs := make(chan QueuedShare, p.maxConcurrent)
	completed := make(chan struct{}, p.maxConcurrent)
	var workers sync.WaitGroup
	for range p.maxConcurrent {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for share := range jobs {
				p.processQueuedShare(ctx, share)
				completed <- struct{}{}
			}
		}()
	}

	shutdown := func() {
		close(jobs)
		workers.Wait()
	}

	maintenanceTicker := time.NewTicker(p.maintenanceEvery)
	defer maintenanceTicker.Stop()

	active := 0
	maintenanceDue := true
	readinessPaused := false

	for {
		if err := ctx.Err(); err != nil {
			shutdown()
			return err
		}

		// Cleanup can delete queue rows and witness material. Drain workers
		// before running it so their final state transitions cannot race a purge.
		if maintenanceDue && active == 0 {
			p.cleanupClosedRounds()
			maintenanceDue = false
		}

		// Readiness gates only new work. Shares already handed to workers retain
		// the same completion and retry semantics as the former batch loop.
		if !maintenanceDue && active < p.maxConcurrent {
			nodeReady := p.isNodeReady == nil || p.isNodeReady()
			if !nodeReady {
				if !readinessPaused {
					p.logger.Debug("helper processing paused: local node is catching up or stale")
					readinessPaused = true
				}
			} else {
				readinessPaused = false
				ready := p.store.TakeReadyBatch(p.maxConcurrent - active)
				if len(ready) > 0 {
					p.logger.Debug(
						"dispatching ready shares",
						"count", len(ready),
						"active", active,
						"max_concurrent", p.maxConcurrent,
					)
					for _, share := range ready {
						jobs <- share
						active++
					}
					continue
				}
			}
		}

		// A due-time timer is useful only while capacity is available. At full
		// capacity, the next completion is the event that permits another take.
		var wakeTimer *time.Timer
		var wake <-chan time.Time
		if !maintenanceDue && active < p.maxConcurrent {
			if readinessPaused {
				wakeTimer = time.NewTimer(p.readinessRetry)
				wake = wakeTimer.C
			} else if next, ok := p.store.NextScheduledTime(); ok {
				delay := time.Until(next)
				if delay < 0 {
					delay = 0
				}
				wakeTimer = time.NewTimer(delay)
				wake = wakeTimer.C
			}
		}

		select {
		case <-ctx.Done():
			if wakeTimer != nil {
				wakeTimer.Stop()
			}
			shutdown()
			return ctx.Err()
		case <-completed:
			active--
		case <-p.store.ScheduleChanged():
		case <-wake:
		case <-maintenanceTicker.C:
			maintenanceDue = true
		}
		if wakeTimer != nil {
			wakeTimer.Stop()
		}
	}
}

// processQueuedShare performs one dequeued share attempt and records exactly
// one resulting queue transition. Cancellation and transient infrastructure
// failures return the share for retry; deterministic failures spend an attempt.
// Panics are recovered and classified as failed attempts.
func (p *Processor) processQueuedShare(ctx context.Context, share QueuedShare) {
	observation := p.metrics.beginShareProcessing()
	metricOutcome := "panic"
	metricStage := failureStagePanic
	defer func() {
		observation.finish(metricOutcome, metricStage)
	}()

	shareCtx, shareSpan := StartTrace(ctx, "helper.process_share", "helper.process_share", map[string]string{
		"round_id":    share.Payload.VoteRoundID,
		"share_index": strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
	}, map[string]interface{}{
		"proposal_id":   share.Payload.ProposalID,
		"tree_position": share.Payload.TreePosition,
		"submit_at":     share.Payload.SubmitAt,
	})
	var spanErr error
	defer func() {
		shareSpan.Finish(spanErr)
	}()
	defer func() {
		p.store.requeueInFlightIfOwned(
			share.Payload.VoteRoundID,
			share.Payload.EncShare.ShareIndex,
			share.Payload.ProposalID,
			share.Payload.TreePosition,
			share.attemptID,
		)
	}()
	defer func() {
		if r := recover(); r != nil {
			metricOutcome = "panic"
			metricStage = failureStagePanic
			err := failedShareAttemptError(failureStagePanic, fmt.Errorf("panic in processShare: %v", r))
			spanErr = err
			captureShareProcessingFailure(share, failureStagePanic, err)
			p.logger.Error("panic in share processing",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
				"panic", r,
			)
			p.markShareFailure(share, err)
		}
	}()

	select {
	case <-shareCtx.Done():
		metricOutcome = "canceled"
		metricStage = "context"
		spanErr = shareCtx.Err()
		p.store.MarkRetry(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
		return
	default:
	}

	active := true
	if p.isRoundActive != nil {
		_, statusSpan := StartTrace(shareCtx, "helper.round_status_check", "helper.round_status_check", nil, nil)
		stageStart := time.Now()
		var err error
		active, err = p.isRoundActive(share.Payload.VoteRoundID)
		p.metrics.observeProcessingStage("round_status", stageStart, err)
		if errors.Is(err, ErrCheckTxNotReady) {
			metricOutcome = "waiting_for_readiness"
			metricStage = "round_status"
			statusSpan.Finish(nil)
			shareSpan.SetData("outcome", "check_tx_not_ready")
			p.logger.Debug("waiting for post-restart CheckTx block time",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
			)
			p.store.MarkRetry(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
			return
		}
		statusSpan.Finish(err)
		if err != nil {
			err = retryableShareError(failureStageRoundStatusCheck, err)
			metricOutcome = "retry"
			metricStage = failureStageRoundStatusCheck
			spanErr = err
			p.logger.Warn("round status check failed, skipping share",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
				"error", err,
			)
			captureShareProcessingFailure(share, failureStageRoundStatusCheck, err)
			p.markShareFailure(share, err)
			return
		}
	}

	// Commitment is independent of proof timing and round activity. Use the
	// same lookup and error classification as closed-round reconciliation.
	stageStart := time.Now()
	committed, err := p.shareAlreadyRevealed(shareCtx, share)
	p.metrics.observeProcessingStage("preproof_dedupe", stageStart, err)
	if err != nil {
		action, _ := classifyShareFailure(err)
		if !active || action == shareFailureFail {
			metricOutcome = "failed"
			if action == shareFailureRetry {
				metricOutcome = "retry"
			}
			metricStage = failureStageCommitmentCheck
			spanErr = err
			p.logger.Warn("share commitment check failed", "round_id", share.Payload.VoteRoundID, "error", err)
			if action == shareFailureFail {
				captureShareProcessingFailure(share, failureStageCommitmentCheck, err)
			}
			p.markShareFailure(share, err)
			return
		}
		// Lookup availability must not prevent an otherwise eligible reveal.
		// The persisted schedule still bounds proof work on this fallback path.
		p.logger.Warn("share commitment lookup unavailable, continuing with scheduled proof",
			"round_id", share.Payload.VoteRoundID, "error", err)
	}
	if committed {
		metricOutcome = "confirmed"
		metricStage = "confirmation"
		shareSpan.SetData("outcome", "submitted")
		p.markShareSubmitted(share)
		return
	}
	if !active {
		metricOutcome = "inactive"
		metricStage = "round_status"
		shareSpan.SetData("outcome", "round_inactive")
		p.logger.Info("round no longer active, skipping share",
			"round_id", share.Payload.VoteRoundID,
			"share_index", share.Payload.EncShare.ShareIndex,
		)
		p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
		return
	}

	if err := p.processShare(shareCtx, share); err != nil {
		spanErr = err
		var waitingErr *waitingForNewBlockError
		if errors.As(err, &waitingErr) {
			metricOutcome = "waiting_for_block"
			metricStage = "block_height"
			shareSpan.SetData("outcome", "waiting_for_new_block")
			spanErr = nil
			retryCount := p.nextStalledRetryCount(share, waitingErr.height)
			shareSpan.SetData("stalled_retry_count", retryCount)
			p.store.MarkStalledRetry(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition, retryCount)
			return
		}
		if errors.Is(err, errAwaitingCommit) || errors.Is(err, errAwaitingRetrySlot) {
			metricOutcome = "awaiting_commit"
			metricStage = "chain_broadcast"
			if errors.Is(err, errAwaitingRetrySlot) {
				metricOutcome = "waiting_for_retry"
				metricStage = "retry_schedule"
			}
			shareSpan.SetData("outcome", "awaiting_commit")
			spanErr = nil
			// Waiting does not spend failed attempts. The persisted retry state
			// bounds proof work while committed-state checks continue.
			p.store.MarkRetry(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
			return
		}
		if isCanceledShareError(err) {
			metricOutcome = "canceled"
			metricStage = "context"
			p.logger.Warn("share processing canceled",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
				"error", err,
			)
			p.store.MarkRetry(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
			return
		}
		action, stage := classifyShareFailure(err)
		metricOutcome = "failed"
		if action == shareFailureRetry {
			metricOutcome = "retry"
		}
		metricStage = stage
		p.logger.Warn("share processing failed",
			"round_id", share.Payload.VoteRoundID,
			"share_index", share.Payload.EncShare.ShareIndex,
			"error", err,
		)
		captureShareProcessingFailure(share, stage, err)
		p.markShareFailure(share, err)
		return
	}

	metricOutcome = "confirmed"
	metricStage = "confirmation"
	shareSpan.SetData("outcome", "submitted")
	p.markShareSubmitted(share)
}

func (p *Processor) markShareSubmitted(share QueuedShare) {
	p.store.MarkSubmitted(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
	p.clearSubmitHeight(share)
	p.logger.Info("share submitted",
		"round_id", share.Payload.VoteRoundID,
		"share_index", share.Payload.EncShare.ShareIndex,
	)
}

// cleanupClosedRounds uses wall time only to select candidates. A fresh node
// and positive committed closure are required before alerts or deletion.
func (p *Processor) cleanupClosedRounds() {
	if p.isRoundClosed == nil || p.isNodeReady == nil || !p.isNodeReady() {
		return
	}
	now := time.Now()
	roundIDs, err := p.store.ExpiredRoundIDs(now)
	if err != nil {
		CaptureErr(err, map[string]string{"stage": "expired_round_candidates"})
		return
	}
	closed := make(map[string]bool, len(roundIDs))
	for _, roundID := range roundIDs {
		isClosed, err := p.isRoundClosed(roundID)
		if err != nil {
			p.logger.Warn("retaining shares: round closure unavailable", "round_id", roundID, "error", err)
			continue
		}
		if isClosed {
			closed[roundID] = true
		}
	}
	summaries, err := p.store.ExpiredRoundSummaries(now)
	if err != nil {
		CaptureErr(err, map[string]string{"stage": "expired_round_summary"})
		return
	}
	for _, summary := range summaries {
		if !closed[summary.RoundID] {
			continue
		}
		if err := p.reconcileClosedRoundSummary(&summary, now); err != nil {
			delete(closed, summary.RoundID)
			p.logger.Warn("retaining shares: commitment reconciliation unavailable", "round_id", summary.RoundID, "error", err)
			continue
		}
		unsubmitted := summary.Unsubmitted()
		if unsubmitted == 0 {
			continue
		}
		err := fmt.Errorf("round closed with unsubmitted shares")
		CaptureErrWithGrouping(err, map[string]string{
			"alert":              helperRoundClosedAlert,
			"round_id":           summary.RoundID,
			"stage":              failureStageRoundClosed,
			"total_shares":       strconv.Itoa(summary.Total),
			"pending_shares":     strconv.Itoa(summary.Pending),
			"failed_shares":      strconv.Itoa(summary.Failed),
			"submitted_shares":   strconv.Itoa(summary.Submitted),
			"unsubmitted_shares": strconv.Itoa(unsubmitted),
		}, helperRoundClosedAlert, summary.RoundID, failureStageRoundClosed)
		p.logger.Error("round closed with unsubmitted shares",
			"round_id", summary.RoundID,
			"total", summary.Total,
			"pending", summary.Pending,
			"failed", summary.Failed,
			"submitted", summary.Submitted,
			"unsubmitted", unsubmitted,
		)
	}
	confirmed := make([]string, 0, len(closed))
	for _, roundID := range roundIDs {
		if closed[roundID] {
			confirmed = append(confirmed, roundID)
		}
	}
	p.store.PurgeRounds(confirmed)
}

// reconcileClosedRoundSummary accounts for commits missed by queue polling,
// including failed rows. The rows are purged after reporting, so only the
// summary needs updating. Invalid local rows remain counted as unsubmitted.
// Unavailable chain lookups defer this round's cleanup.
func (p *Processor) reconcileClosedRoundSummary(summary *ExpiredRoundSummary, now time.Time) error {
	if p.preProofDedupe == nil || summary.Unsubmitted() == 0 {
		return nil
	}
	shares, err := p.store.unsubmittedSharesBeforeClose(summary.RoundID, now)
	if err != nil {
		return err
	}
	for _, share := range shares {
		committed, err := p.shareAlreadyRevealed(context.Background(), share)
		if err != nil {
			if action, _ := classifyShareFailure(err); action == shareFailureRetry {
				return err
			}
			p.logger.Warn("invalid share remains unsubmitted at round closure",
				"round_id", summary.RoundID, "share_index", share.Payload.EncShare.ShareIndex, "error", err)
			continue
		}
		if !committed {
			continue
		}
		if share.State == ShareStateFailed {
			summary.Failed--
		} else {
			summary.Pending--
		}
		summary.Submitted++
	}
	return nil
}

// captureShareProcessingFailure groups repeated attempts from the local helper
// by round, failure stage, and queue action while preserving the share index as
// diagnostic context. A new round, stage, or action creates a separate issue.
func captureShareProcessingFailure(share QueuedShare, stage string, err error) {
	action, _ := classifyShareFailure(err)
	actionTag := "failed"
	if action == shareFailureRetry {
		actionTag = "retry"
	}
	CaptureErrWithGrouping(err, map[string]string{
		"alert":          helperShareFailureAlert,
		"failure_action": actionTag,
		"round_id":       share.Payload.VoteRoundID,
		"share_index":    strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
		"stage":          stage,
	}, helperShareFailureFingerprint(share.Payload.VoteRoundID, stage, actionTag)...)
}

func helperShareFailureFingerprint(roundID, stage, action string) []string {
	return []string{helperShareFailureAlert, roundID, stage, action}
}

// markShareFailure records err using the queue action carried by its
// shareProcessingError wrapper.
func (p *Processor) markShareFailure(share QueuedShare, err error) {
	action, _ := classifyShareFailure(err)
	if action == shareFailureRetry {
		p.store.MarkRetry(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
		return
	}
	p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
}

// processShare builds the Merkle path and reserves a due attempt before proving
// and submitting. The caller checks commitment and round activity first.
func (p *Processor) processShare(ctx context.Context, share QueuedShare) error {
	// Scope the tree reader to this share's voting round.
	stageStart := time.Now()
	roundID, err := decodeShareRoundID(share.Payload.VoteRoundID)
	p.metrics.observeProcessingStage("payload_decode", stageStart, err)
	if err != nil {
		return err
	}
	roundBytes := roundID[:]

	retry, err := decodeRetryState(share.retryState, share.VoteEndTime)
	if err != nil {
		return retryableShareError("retry_schedule", err)
	}
	// Empty retry state can belong to a pre-upgrade or imported row. Its old
	// attempt count must consume slots when we first reserve the new schedule.
	previousAttempts := 0
	if retry == nil {
		previousAttempts = max(0, share.Attempts)
		if previousAttempts >= 5 {
			return retryableShareError(failureStageSubmitChain, errAwaitingRetrySlot)
		}
	}
	if retry != nil {
		if slot, _ := retry.due(p.now(), share.VoteEndTime); slot < 0 {
			return retryableShareError(failureStageSubmitChain, errAwaitingRetrySlot)
		}
	}

	tree := p.tree.ForRound(roundBytes)

	// Read tree status (leaf count + anchor height) without loading leaf data.
	stageStart = time.Now()
	status, err := tree.GetTreeStatus()
	if err != nil {
		p.metrics.observeProcessingStage("tree_status", stageStart, err)
		return retryableShareError(failureStageTreeStatus, fmt.Errorf("read tree status: %w", err))
	}
	if status.LeafCount == 0 {
		err := fmt.Errorf("commitment tree is empty")
		p.metrics.observeProcessingStage("tree_status", stageStart, err)
		return retryableShareError(failureStageTreeStatus, err)
	}
	if share.Payload.TreePosition >= status.LeafCount {
		err := fmt.Errorf("tree_position %d out of range (tree has %d leaves)", share.Payload.TreePosition, status.LeafCount)
		p.metrics.observeProcessingStage("tree_status", stageStart, err)
		return retryableShareError(
			failureStageTreeStatus,
			err,
		)
	}
	p.metrics.observeProcessingStage("tree_status", stageStart, nil)
	anchorHeight := status.AnchorHeight
	blockHeight := tree.LatestBlockHeight()
	if blockHeight == 0 || p.submittedAtHeight(share, blockHeight) {
		return retryableShareError(
			failureStageSubmitChain,
			&waitingForNewBlockError{height: blockHeight},
		)
	}

	// Compute Merkle authentication path via the persistent KV-backed tree.
	// O(depth) shard reads — no leaf replay.
	stageStart = time.Now()
	merklePath, err := tree.MerklePath(share.Payload.TreePosition, uint32(anchorHeight))
	p.metrics.observeProcessingStage("merkle_path", stageStart, err)
	if err != nil {
		return retryableShareError(failureStageMerklePath, fmt.Errorf("compute merkle path: %w", err))
	}

	// Decode share_comms.
	stageStart = time.Now()
	var shareComms [types.VoteCommitmentShareCount][32]byte
	if len(share.Payload.ShareComms) != types.VoteCommitmentShareCount {
		err := fmt.Errorf("expected %d share_comms, got %d", types.VoteCommitmentShareCount, len(share.Payload.ShareComms))
		p.metrics.observeProcessingStage("payload_decode", stageStart, err)
		return failedShareAttemptError(failureStageDecodePayload, err)
	}
	for i, c := range share.Payload.ShareComms {
		cBytes, err := base64.StdEncoding.DecodeString(c)
		if err != nil {
			p.metrics.observeProcessingStage("payload_decode", stageStart, err)
			return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("decode share_comms[%d]: %w", i, err))
		}
		if len(cBytes) != 32 {
			err := fmt.Errorf("share_comms[%d] must be 32 bytes, got %d", i, len(cBytes))
			p.metrics.observeProcessingStage("payload_decode", stageStart, err)
			return failedShareAttemptError(failureStageDecodePayload, err)
		}
		copy(shareComms[i][:], cBytes)
	}

	// Decode primary_blind.
	var primaryBlind [32]byte
	pbBytes, err := base64.StdEncoding.DecodeString(share.Payload.PrimaryBlind)
	if err != nil {
		p.metrics.observeProcessingStage("payload_decode", stageStart, err)
		return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("decode primary_blind: %w", err))
	}
	if len(pbBytes) != 32 {
		err := fmt.Errorf("primary_blind must be 32 bytes, got %d", len(pbBytes))
		p.metrics.observeProcessingStage("payload_decode", stageStart, err)
		return failedShareAttemptError(failureStageDecodePayload, err)
	}
	copy(primaryBlind[:], pbBytes)

	// Decode the revealed share's C1/C2 once, reused for both the prover and the message.
	c1Bytes, err := base64.StdEncoding.DecodeString(share.Payload.EncShare.C1)
	if err != nil {
		p.metrics.observeProcessingStage("payload_decode", stageStart, err)
		return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("decode enc_share.c1: %w", err))
	}
	if len(c1Bytes) != 32 {
		err := fmt.Errorf("enc_share.c1 must be 32 bytes, got %d", len(c1Bytes))
		p.metrics.observeProcessingStage("payload_decode", stageStart, err)
		return failedShareAttemptError(failureStageDecodePayload, err)
	}
	c2Bytes, err := base64.StdEncoding.DecodeString(share.Payload.EncShare.C2)
	if err != nil {
		p.metrics.observeProcessingStage("payload_decode", stageStart, err)
		return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("decode enc_share.c2: %w", err))
	}
	if len(c2Bytes) != 32 {
		err := fmt.Errorf("enc_share.c2 must be 32 bytes, got %d", len(c2Bytes))
		p.metrics.observeProcessingStage("payload_decode", stageStart, err)
		return failedShareAttemptError(failureStageDecodePayload, err)
	}
	var encC1, encC2 [32]byte
	copy(encC1[:], c1Bytes)
	copy(encC2[:], c2Bytes)
	p.metrics.observeProcessingStage("payload_decode", stageStart, nil)

	// Reserve the proof attempt durably after cheap validation. Crashes and
	// ambiguous submissions consume the slot rather than repeating proof work.
	now := p.now()
	if retry == nil {
		info, err := p.store.getRoundInfo(share.Payload.VoteRoundID)
		if err != nil {
			return retryableShareError("retry_schedule", fmt.Errorf("read round window: %w", err))
		}
		now = p.now()
		initial := newRetryState(now, info.CreatedAtTime, share.VoteEndTime)
		retry = &initial
	}
	slot, _ := retry.due(now, share.VoteEndTime)
	if slot < 0 {
		return retryableShareError(failureStageSubmitChain, errAwaitingRetrySlot)
	}
	if blockHeight <= retry.LastHeight {
		return retryableShareError(failureStageSubmitChain, &waitingForNewBlockError{height: blockHeight})
	}
	retry.NextSlot = min(slot+1+previousAttempts, len(retryTimes(retry.FirstAttempt, retry.FinalAttempt)))
	retry.LastAttempt = now
	retry.LastHeight = blockHeight
	if err := p.store.reserveProofAttempt(share, *retry); err != nil {
		return retryableShareError("retry_schedule", err)
	}

	// Generate ZKP #3 proof.
	proofStart := time.Now()
	_, span := StartTrace(ctx, "zkp.prove", "helper.generate_share_reveal_proof", map[string]string{
		"round_id":    share.Payload.VoteRoundID,
		"share_index": strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
	}, map[string]interface{}{
		"share_index": share.Payload.EncShare.ShareIndex,
		"proposal_id": share.Payload.ProposalID,
	})
	proof, nullifier, _, err := p.prover.GenerateShareRevealProof(
		merklePath,
		shareComms,
		primaryBlind,
		encC1,
		encC2,
		share.Payload.EncShare.ShareIndex,
		share.Payload.ProposalID,
		share.Payload.VoteDecision,
		roundID,
	)
	proofDuration := time.Since(proofStart)
	p.metrics.observeProcessingStage("proof_generation", proofStart, err)
	span.SetData("duration_ms", proofDuration.Milliseconds())
	span.SetData("proof_bytes", len(proof))
	span.Finish(err)
	if err != nil {
		return wrapProofGenerateError(err)
	}
	p.logger.Info("proof generated",
		"round_id", share.Payload.VoteRoundID,
		"share_index", share.Payload.EncShare.ShareIndex,
		"duration", proofDuration,
	)

	// Build enc_share: C1 || C2 (64 bytes).
	encShareBytes := make([]byte, 64)
	copy(encShareBytes[:32], c1Bytes)
	copy(encShareBytes[32:], c2Bytes)

	msg := &MsgRevealShareJSON{
		ShareNullifier:           base64.StdEncoding.EncodeToString(nullifier[:]),
		EncShare:                 base64.StdEncoding.EncodeToString(encShareBytes),
		ProposalID:               share.Payload.ProposalID,
		VoteDecision:             share.Payload.VoteDecision,
		Proof:                    base64.StdEncoding.EncodeToString(proof),
		VoteRoundID:              base64.StdEncoding.EncodeToString(roundBytes),
		VoteCommTreeAnchorHeight: anchorHeight,
	}

	// Proof generation may cross a block boundary, so claim the height again
	// immediately before the outbound request.
	blockHeight = tree.LatestBlockHeight()
	if blockHeight == 0 || !p.claimSubmitHeight(share, blockHeight) {
		return retryableShareError(
			failureStageSubmitChain,
			&waitingForNewBlockError{height: blockHeight},
		)
	}

	// Submit to chain.
	stageStart = time.Now()
	result, err := p.submitter.SubmitRevealShareContext(ctx, msg)
	if err != nil {
		p.metrics.observeProcessingStage("chain_broadcast", stageStart, err)
		return wrapSubmitError(err)
	}
	if result.Code != 0 {
		if IsDuplicateNullifier(result.Code) {
			p.metrics.observeProcessingStage("chain_broadcast", stageStart, nil)
			p.logger.Info("share already revealed by another helper",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
			)
			return nil
		}
		err := fmt.Errorf("chain rejected tx (code %d): %s", result.Code, result.Log)
		p.metrics.observeProcessingStage("chain_broadcast", stageStart, err)
		return failedShareAttemptError(failureStageSubmitChain, err)
	}

	if result.TxHash == "" {
		err := fmt.Errorf("chain accepted broadcast without a transaction hash")
		p.metrics.observeProcessingStage("chain_broadcast", stageStart, err)
		return failedShareAttemptError(failureStageSubmitChain, err)
	}
	p.metrics.observeProcessingStage("chain_broadcast", stageStart, nil)

	// CheckTx acceptance only places the transaction in the mempool. Preserve
	// the witness until a later pass observes its nullifier in committed state.
	// That pass confirms commitment in processQueuedShare before scrubbing.
	p.logger.Debug("MsgRevealShare broadcast accepted; awaiting committed nullifier",
		"tx_hash", result.TxHash)
	return retryableShareError(
		failureStageSubmitChain,
		fmt.Errorf("%w %s", errAwaitingCommit, result.TxHash),
	)
}

// submittedAtHeight reports whether this process already sent the share at
// height. Restarts clear this cache, but the post-restart CheckTx readiness gate
// prevents processing until a newer block commits.
func (p *Processor) submittedAtHeight(share QueuedShare, height uint64) bool {
	p.submitHeightMu.Lock()
	defer p.submitHeightMu.Unlock()

	if !p.advanceSubmitHeightLocked(height) {
		return true
	}
	_, submitted := p.submitKeys[shareScheduleKey(share)]
	return submitted
}

// claimSubmitHeight records the outbound submission height unless the same
// share was already submitted at that height.
func (p *Processor) claimSubmitHeight(share QueuedShare, height uint64) bool {
	p.submitHeightMu.Lock()
	defer p.submitHeightMu.Unlock()

	if !p.advanceSubmitHeightLocked(height) {
		return false
	}
	key := shareScheduleKey(share)
	if _, submitted := p.submitKeys[key]; submitted {
		return false
	}
	p.submitKeys[key] = struct{}{}
	delete(p.stalledRetryCount, key)
	return true
}

// nextStalledRetryCount increments the bounded retry streak for a share at an
// unchanged committed height. A newer height resets every stalled streak.
func (p *Processor) nextStalledRetryCount(share QueuedShare, height uint64) uint8 {
	p.submitHeightMu.Lock()
	defer p.submitHeightMu.Unlock()

	p.advanceSubmitHeightLocked(height)
	key := shareScheduleKey(share)
	retryCount := p.stalledRetryCount[key]
	if retryCount < shareStalledRetryMaxCount {
		retryCount++
	}
	p.stalledRetryCount[key] = retryCount
	return retryCount
}

func (p *Processor) clearSubmitHeight(share QueuedShare) {
	p.submitHeightMu.Lock()
	defer p.submitHeightMu.Unlock()
	key := shareScheduleKey(share)
	delete(p.submitKeys, key)
	delete(p.stalledRetryCount, key)
}

// advanceSubmitHeightLocked rotates the cache when height increases and
// rejects stale observations so concurrent work cannot rotate it backwards.
func (p *Processor) advanceSubmitHeightLocked(height uint64) bool {
	if height < p.submitHeight {
		return false
	}
	if height > p.submitHeight {
		clear(p.submitKeys)
		clear(p.stalledRetryCount)
		p.submitHeight = height
	}
	return true
}

func shareScheduleKey(share QueuedShare) string {
	return schedKey(
		share.Payload.VoteRoundID,
		share.Payload.EncShare.ShareIndex,
		share.Payload.ProposalID,
		share.Payload.TreePosition,
	)
}

// shareAlreadyRevealed checks commitment without requiring an active round or
// a due proof slot. Production helpers always have a deduper configured.
func (p *Processor) shareAlreadyRevealed(ctx context.Context, share QueuedShare) (bool, error) {
	if p.preProofDedupe == nil {
		return false, nil
	}
	roundID, err := decodeShareRoundID(share.Payload.VoteRoundID)
	if err != nil {
		return false, err
	}
	return p.preProofDedupe.shareAlreadyRevealed(ctx, share, roundID)
}

func decodeShareRoundID(value string) ([32]byte, error) {
	var roundID [32]byte
	raw, err := hex.DecodeString(value)
	if err != nil {
		return roundID, failedShareAttemptError(failureStageDecodeRoundID, fmt.Errorf("decode vote_round_id: %w", err))
	}
	if len(raw) != len(roundID) {
		return roundID, failedShareAttemptError(failureStageDecodeRoundID, fmt.Errorf("vote_round_id must be 32 bytes, got %d", len(raw)))
	}
	copy(roundID[:], raw)
	return roundID, nil
}

// shareAlreadyRevealed computes the queued share's nullifier and checks whether
// the chain has already recorded it for the share's voting round. Only chain
// lookup errors are retryable. Decode and hash errors are local row failures.
func (d *preProofShareDeduper) shareAlreadyRevealed(ctx context.Context, share QueuedShare, roundID [32]byte) (bool, error) {
	_, span := StartTrace(ctx, "helper.dedupe", "helper.preproof_share_nullifier_check", map[string]string{
		"round_id":    share.Payload.VoteRoundID,
		"share_index": strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
	}, map[string]interface{}{
		"proposal_id": share.Payload.ProposalID,
	})

	var spanErr error
	var already bool
	defer func() {
		span.SetData("already_revealed", already)
		span.Finish(spanErr)
	}()

	sharesHash, err := decodeBase64Array32(share.Payload.SharesHash, "shares_hash")
	if err != nil {
		spanErr = err
		return false, err
	}
	primaryBlind, err := decodeBase64Array32(share.Payload.PrimaryBlind, "primary_blind")
	if err != nil {
		spanErr = err
		return false, err
	}
	voteCommitment, err := d.vcHash(roundID, sharesHash, share.Payload.ProposalID, share.Payload.VoteDecision)
	if err != nil {
		spanErr = err
		return false, fmt.Errorf("compute vote commitment: %w", err)
	}
	nullifier, err := d.shareNFHash(voteCommitment, share.Payload.EncShare.ShareIndex, primaryBlind)
	if err != nil {
		spanErr = err
		return false, fmt.Errorf("compute share nullifier: %w", err)
	}
	already, err = d.shareNF(share.Payload.VoteRoundID, nullifier[:])
	if err != nil {
		spanErr = err
		return false, retryableShareError(failureStageCommitmentCheck, fmt.Errorf("check share nullifier: %w", err))
	}
	return already, nil
}

func decodeBase64Array32(value string, field string) ([32]byte, error) {
	var out [32]byte
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return out, fmt.Errorf("decode %s: %w", field, err)
	}
	if len(decoded) != 32 {
		return out, fmt.Errorf("%s must be 32 bytes, got %d", field, len(decoded))
	}
	copy(out[:], decoded)
	return out, nil
}
