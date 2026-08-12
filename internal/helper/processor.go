package helper

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"cosmossdk.io/log"
	"golang.org/x/sync/errgroup"
)

const maintenanceInterval = 30 * time.Second

var errAwaitingCommit = errors.New("broadcast accepted; awaiting committed transaction")

const (
	failureStagePanic            = "panic"
	failureStageRoundStatusCheck = "round_status_check"
	failureStageDecodeRoundID    = "decode_round_id"
	failureStageTreeStatus       = "tree_status"
	failureStageMerklePath       = "merkle_path"
	failureStageDecodePayload    = "decode_payload"
	failureStageProofGenerate    = "proof_generate"
	failureStageSubmitHTTP       = "submit_http"
	failureStageSubmitChain      = "submit_chain_reject"
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
	store          *ShareStore
	tree           TreeReader
	prover         ProofGenerator
	submitter      *ChainSubmitter
	logger         log.Logger
	maxConcurrent  int
	isRoundActive  RoundStatusChecker
	preProofDedupe *preProofShareDeduper
}

type ProcessorOption func(*Processor)

type preProofShareDeduper struct {
	vcHash      VCHashFunc
	shareNFHash ShareNullifierHashFunc
	shareNF     ShareNullifierChecker
}

// WithPreProofShareDeduper enables an optional cheap share-nullifier lookup
// before proof generation. When the pre-proof check reports an existing reveal,
// the processor skips proof generation; when the check fails, processing falls
// through to the normal proof and submit path.
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

// NewProcessor creates a new share processor.
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
		store:         store,
		tree:          tree,
		prover:        prover,
		submitter:     submitter,
		logger:        logger,
		maxConcurrent: maxConcurrent,
		isRoundActive: isRoundActive,
	}
	for _, option := range options {
		option(p)
	}
	return p
}

// Run starts the processing loop. Blocks until ctx is cancelled.
// Each cycle processes ready shares and purges share data for rounds whose
// voting window has ended.
func (p *Processor) Run(ctx context.Context) error {
	for {
		p.alertExpiredUnsubmittedShares()
		p.store.PurgeExpiredRounds()
		p.processBatch(ctx)
		if err := p.waitForSchedule(ctx); err != nil {
			return err
		}
	}
}

func (p *Processor) waitForSchedule(ctx context.Context) error {
	delay := maintenanceInterval
	if next, ok := p.store.NextScheduledTime(); ok {
		until := time.Until(next)
		if until <= 0 {
			return nil
		}
		if until < delay {
			delay = until
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	case <-p.store.ScheduleChanged():
		return nil
	}
}

func (p *Processor) alertExpiredUnsubmittedShares() {
	summaries, err := p.store.ExpiredRoundSummaries(time.Now())
	if err != nil {
		CaptureErr(err, map[string]string{"stage": "expired_round_summary"})
		return
	}
	for _, summary := range summaries {
		unsubmitted := summary.Unsubmitted()
		if unsubmitted == 0 {
			continue
		}
		err := fmt.Errorf("round closed with unsubmitted shares")
		CaptureErr(err, map[string]string{
			"round_id":           summary.RoundID,
			"stage":              "round_closed_unsubmitted_shares",
			"total_shares":       strconv.Itoa(summary.Total),
			"pending_shares":     strconv.Itoa(summary.Pending),
			"failed_shares":      strconv.Itoa(summary.Failed),
			"submitted_shares":   strconv.Itoa(summary.Submitted),
			"unsubmitted_shares": strconv.Itoa(unsubmitted),
		})
		p.logger.Error("round closed with unsubmitted shares",
			"round_id", summary.RoundID,
			"total", summary.Total,
			"pending", summary.Pending,
			"failed", summary.Failed,
			"submitted", summary.Submitted,
			"unsubmitted", unsubmitted,
		)
	}
}

// processBatch takes all ready shares and processes them.
func (p *Processor) processBatch(ctx context.Context) {
	ready := p.store.TakeReady()
	if len(ready) == 0 {
		return
	}

	ctx, batchSpan := StartTrace(ctx, "helper.wakeup", "helper.process_ready_shares", nil, map[string]interface{}{
		"ready_count":    len(ready),
		"max_concurrent": p.maxConcurrent,
	})
	p.logger.Info(
		"processing ready shares",
		"count", len(ready),
		"max_concurrent", p.maxConcurrent,
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(p.maxConcurrent)

	for _, queued := range ready {
		share := queued
		g.Go(func() (retErr error) {
			shareCtx, shareSpan := StartTrace(gctx, "helper.process_share", "helper.process_share", map[string]string{
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
				if r := recover(); r != nil {
					err := failedShareAttemptError(failureStagePanic, fmt.Errorf("panic in processShare: %v", r))
					spanErr = err
					CaptureErr(err, map[string]string{
						"round_id":    share.Payload.VoteRoundID,
						"share_index": strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
						"stage":       failureStagePanic,
					})
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
				spanErr = shareCtx.Err()
				p.store.MarkRetry(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
				return nil
			default:
			}

			if p.isRoundActive != nil {
				_, statusSpan := StartTrace(shareCtx, "helper.round_status_check", "helper.round_status_check", nil, nil)
				active, err := p.isRoundActive(share.Payload.VoteRoundID)
				statusSpan.Finish(err)
				if err != nil {
					err = retryableShareError(failureStageRoundStatusCheck, err)
					spanErr = err
					p.logger.Warn("round status check failed, skipping share",
						"round_id", share.Payload.VoteRoundID,
						"share_index", share.Payload.EncShare.ShareIndex,
						"error", err,
					)
					CaptureErr(err, map[string]string{
						"round_id":    share.Payload.VoteRoundID,
						"share_index": strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
						"stage":       failureStageRoundStatusCheck,
					})
					p.markShareFailure(share, err)
					return nil
				}
				if !active {
					shareSpan.SetData("outcome", "round_inactive")
					p.logger.Info("round no longer active, skipping share",
						"round_id", share.Payload.VoteRoundID,
						"share_index", share.Payload.EncShare.ShareIndex,
					)
					p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
					return nil
				}
			}

			if err := p.processShare(shareCtx, share); err != nil {
				spanErr = err
				if errors.Is(err, errAwaitingCommit) {
					shareSpan.SetData("outcome", "awaiting_commit")
					spanErr = nil
					// Bound accepted-but-unconfirmed broadcasts so an unavailable
					// committed-state check cannot trigger proof generation forever.
					p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
					return nil
				}
				if isCanceledShareError(err) {
					p.logger.Warn("share processing canceled",
						"round_id", share.Payload.VoteRoundID,
						"share_index", share.Payload.EncShare.ShareIndex,
						"error", err,
					)
					p.store.MarkRetry(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
					return nil
				}
				_, stage := classifyShareFailure(err)
				p.logger.Warn("share processing failed",
					"round_id", share.Payload.VoteRoundID,
					"share_index", share.Payload.EncShare.ShareIndex,
					"error", err,
				)
				CaptureErr(err, map[string]string{
					"round_id":    share.Payload.VoteRoundID,
					"share_index": strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
					"stage":       stage,
				})
				p.markShareFailure(share, err)
				return nil
			}

			shareSpan.SetData("outcome", "submitted")
			p.store.MarkSubmitted(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
			p.logger.Info("share submitted",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
			)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		batchSpan.Finish(err)
		p.logger.Error("share processing batch had errors", "error", err)
		return
	}
	batchSpan.Finish(nil)
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

// processShare handles a single share: Merkle path → proof → submit.
func (p *Processor) processShare(ctx context.Context, share QueuedShare) error {
	// Scope the tree reader to this share's voting round.
	roundBytes, err := hex.DecodeString(share.Payload.VoteRoundID)
	if err != nil {
		return failedShareAttemptError(failureStageDecodeRoundID, fmt.Errorf("decode vote_round_id: %w", err))
	}
	var roundID [32]byte
	if len(roundBytes) != 32 {
		return failedShareAttemptError(failureStageDecodeRoundID, fmt.Errorf("vote_round_id must be 32 bytes, got %d", len(roundBytes)))
	}
	copy(roundID[:], roundBytes)

	if p.preProofDedupe != nil {
		alreadyRevealed, err := p.preProofDedupe.shareAlreadyRevealed(ctx, share, roundID)
		if err != nil {
			p.logger.Warn("pre-proof share nullifier check failed, continuing with proof",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
				"error", err,
			)
		} else if alreadyRevealed {
			p.logger.Info("share already revealed before proof generation",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
			)
			return nil
		}
	}

	tree := p.tree.ForRound(roundBytes)

	// Read tree status (leaf count + anchor height) without loading leaf data.
	status, err := tree.GetTreeStatus()
	if err != nil {
		return retryableShareError(failureStageTreeStatus, fmt.Errorf("read tree status: %w", err))
	}
	if status.LeafCount == 0 {
		return retryableShareError(failureStageTreeStatus, fmt.Errorf("commitment tree is empty"))
	}
	if share.Payload.TreePosition >= status.LeafCount {
		return retryableShareError(
			failureStageTreeStatus,
			fmt.Errorf("tree_position %d out of range (tree has %d leaves)", share.Payload.TreePosition, status.LeafCount),
		)
	}
	anchorHeight := status.AnchorHeight

	// Compute Merkle authentication path via the persistent KV-backed tree.
	// O(depth) shard reads — no leaf replay.
	merklePath, err := tree.MerklePath(share.Payload.TreePosition, uint32(anchorHeight))
	if err != nil {
		return retryableShareError(failureStageMerklePath, fmt.Errorf("compute merkle path: %w", err))
	}

	// Decode share_comms.
	var shareComms [16][32]byte
	if len(share.Payload.ShareComms) != 16 {
		return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("expected 16 share_comms, got %d", len(share.Payload.ShareComms)))
	}
	for i, c := range share.Payload.ShareComms {
		cBytes, err := base64.StdEncoding.DecodeString(c)
		if err != nil {
			return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("decode share_comms[%d]: %w", i, err))
		}
		if len(cBytes) != 32 {
			return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("share_comms[%d] must be 32 bytes, got %d", i, len(cBytes)))
		}
		copy(shareComms[i][:], cBytes)
	}

	// Decode primary_blind.
	var primaryBlind [32]byte
	pbBytes, err := base64.StdEncoding.DecodeString(share.Payload.PrimaryBlind)
	if err != nil {
		return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("decode primary_blind: %w", err))
	}
	if len(pbBytes) != 32 {
		return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("primary_blind must be 32 bytes, got %d", len(pbBytes)))
	}
	copy(primaryBlind[:], pbBytes)

	// Decode the revealed share's C1/C2 once, reused for both the prover and the message.
	c1Bytes, err := base64.StdEncoding.DecodeString(share.Payload.EncShare.C1)
	if err != nil {
		return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("decode enc_share.c1: %w", err))
	}
	if len(c1Bytes) != 32 {
		return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("enc_share.c1 must be 32 bytes, got %d", len(c1Bytes)))
	}
	c2Bytes, err := base64.StdEncoding.DecodeString(share.Payload.EncShare.C2)
	if err != nil {
		return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("decode enc_share.c2: %w", err))
	}
	if len(c2Bytes) != 32 {
		return failedShareAttemptError(failureStageDecodePayload, fmt.Errorf("enc_share.c2 must be 32 bytes, got %d", len(c2Bytes)))
	}
	var encC1, encC2 [32]byte
	copy(encC1[:], c1Bytes)
	copy(encC2[:], c2Bytes)

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

	// Submit to chain.
	result, err := p.submitter.SubmitRevealShareContext(ctx, msg)
	if err != nil {
		return wrapSubmitError(err)
	}
	if result.Code != 0 {
		if IsDuplicateNullifier(result.Code) {
			p.logger.Info("share already revealed by another helper",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
			)
			return nil
		}
		return failedShareAttemptError(failureStageSubmitChain, fmt.Errorf("chain rejected tx (code %d): %s", result.Code, result.Log))
	}

	if result.TxHash == "" {
		return failedShareAttemptError(failureStageSubmitChain, fmt.Errorf("chain accepted broadcast without a transaction hash"))
	}

	// CheckTx acceptance only places the transaction in the mempool. Preserve
	// the witness until a later pass observes its nullifier in committed state.
	// That pass returns nil from the pre-proof dedupe above and only then allows
	// processBatch to call MarkSubmitted and scrub the witness.
	p.logger.Debug("MsgRevealShare broadcast accepted; awaiting committed nullifier",
		"tx_hash", result.TxHash)
	return retryableShareError(
		failureStageSubmitChain,
		fmt.Errorf("%w %s", errAwaitingCommit, result.TxHash),
	)
}

// shareAlreadyRevealed computes the queued share's nullifier and checks whether
// the chain has already recorded it for the share's voting round.
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
		return false, fmt.Errorf("check share nullifier: %w", err)
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
