package helper

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"cosmossdk.io/log"
	"golang.org/x/sync/errgroup"
)

const maintenanceInterval = 30 * time.Second

var (
	errCloseoutRetryable     = errors.New("retry closeout")
	errMalformedSharePayload = errors.New("malformed share payload")
)

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

type processShareResult struct {
	state     ShareState
	nullifier []byte
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

// Run starts the processing loop. Blocks until ctx is cancelled. Each cycle
// processes closeouts for completed rounds before handling ready shares.
func (p *Processor) Run(ctx context.Context) error {
	for {
		p.closeoutExpiredRounds(ctx)
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

func (p *Processor) closeoutExpiredRounds(ctx context.Context) {
	now := time.Now()
	roundIDs, err := p.store.ExpiredRoundIDsForCloseout(now)
	if err != nil {
		CaptureErr(err, map[string]string{"stage": "expired_round_closeout_list"})
		return
	}

	closedAt := uint64(now.Unix())
	for _, roundID := range roundIDs {
		if err := p.closeoutRound(ctx, roundID, closedAt); err != nil {
			CaptureErr(err, map[string]string{
				"round_id": roundID,
				"stage":    "expired_round_closeout",
			})
			p.logger.Error("expired round closeout failed", "round_id", roundID, "error", err)
			continue
		}
		summary, err := p.store.RoundCloseoutSummary(roundID)
		if err != nil {
			CaptureErr(err, map[string]string{
				"round_id": roundID,
				"stage":    "expired_round_summary",
			})
			p.logger.Error("expired round summary failed", "round_id", roundID, "error", err)
			continue
		}
		p.alertExpiredUnsubmittedShares(summary)
		if err := p.store.MarkRoundClosed(roundID, closedAt); err != nil {
			CaptureErr(err, map[string]string{
				"round_id": roundID,
				"stage":    "mark_round_closed",
			})
			p.logger.Error("mark round closed failed", "round_id", roundID, "error", err)
			continue
		}
		if p.logger != nil {
			p.logger.Info("closed expired round helper rows",
				"round_id", roundID,
				"total", summary.Total,
				"pending", summary.Pending,
				"failed", summary.Failed,
				"missed_deadline", summary.MissedDeadline,
				"submitted", summary.Submitted,
				"observed_on_chain", summary.ObservedOnChain,
			)
		}
	}
}

func (p *Processor) alertExpiredUnsubmittedShares(summary ExpiredRoundSummary) {
	unsubmitted := summary.Unsubmitted()
	if unsubmitted == 0 {
		return
	}
	err := fmt.Errorf("round closed with unsubmitted shares")
	CaptureErr(err, map[string]string{
		"round_id":               summary.RoundID,
		"stage":                  "round_closed_unsubmitted_shares",
		"total_shares":           strconv.Itoa(summary.Total),
		"pending_shares":         strconv.Itoa(summary.Pending),
		"failed_shares":          strconv.Itoa(summary.Failed),
		"missed_deadline_shares": strconv.Itoa(summary.MissedDeadline),
		"submitted_shares":       strconv.Itoa(summary.Submitted),
		"observed_on_chain_rows": strconv.Itoa(summary.ObservedOnChain),
		"unsubmitted_shares":     strconv.Itoa(unsubmitted),
	})
	p.logger.Error("round closed with unsubmitted shares",
		"round_id", summary.RoundID,
		"total", summary.Total,
		"pending", summary.Pending,
		"failed", summary.Failed,
		"missed_deadline", summary.MissedDeadline,
		"submitted", summary.Submitted,
		"observed_on_chain", summary.ObservedOnChain,
		"unsubmitted", unsubmitted,
	)
}

func (p *Processor) closeoutRound(ctx context.Context, roundID string, closedAt uint64) error {
	shares, err := p.store.ProcessableSharesForRound(roundID)
	if err != nil {
		return err
	}
	for _, share := range shares {
		state, nullifier, err := p.closeoutShareState(ctx, share)
		if err != nil {
			if errors.Is(err, errCloseoutRetryable) {
				return err
			}
			CaptureErr(err, map[string]string{
				"round_id":    share.Payload.VoteRoundID,
				"share_index": strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
				"stage":       "expired_share_closeout",
			})
			p.logger.Warn("expired share closeout classification failed, marking missed deadline",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
				"proposal_id", share.Payload.ProposalID,
				"tree_position", share.Payload.TreePosition,
				"error", fmt.Errorf("close out share_index %d proposal_id %d tree_position %d: %w",
					share.Payload.EncShare.ShareIndex,
					share.Payload.ProposalID,
					share.Payload.TreePosition,
					err,
				),
			)
			state = ShareStateMissedDeadline
			nullifier = nil
			if share.State == ShareStateFailed {
				continue
			}
		}
		if share.State == ShareStateFailed && state != ShareStateObservedOnChain {
			if state == ShareStateFailed {
				if err := p.store.MarkFailedCloseoutChecked(
					share.Payload.VoteRoundID,
					share.Payload.EncShare.ShareIndex,
					share.Payload.ProposalID,
					share.Payload.TreePosition,
					closedAt,
				); err != nil {
					return fmt.Errorf("mark failed closeout checked share_index %d proposal_id %d tree_position %d: %w",
						share.Payload.EncShare.ShareIndex,
						share.Payload.ProposalID,
						share.Payload.TreePosition,
						err,
					)
				}
			}
			continue
		}
		if err := p.store.CloseoutShare(
			share.Payload.VoteRoundID,
			share.Payload.EncShare.ShareIndex,
			share.Payload.ProposalID,
			share.Payload.TreePosition,
			state,
			nullifier,
			closedAt,
		); err != nil {
			return fmt.Errorf("close out share_index %d proposal_id %d tree_position %d: %w",
				share.Payload.EncShare.ShareIndex,
				share.Payload.ProposalID,
				share.Payload.TreePosition,
				err,
			)
		}
	}
	return nil
}

func (p *Processor) closeoutShareState(ctx context.Context, share QueuedShare) (ShareState, []byte, error) {
	if p.preProofDedupe == nil {
		return ShareStateMissedDeadline, nil, nil
	}
	if share.State == ShareStateFailed && len(share.ShareNullifier) > 0 {
		onChain, err := p.preProofDedupe.shareNF(share.Payload.VoteRoundID, share.ShareNullifier)
		if err != nil {
			return 0, nil, fmt.Errorf("%w: check stored share nullifier: %w", errCloseoutRetryable, err)
		}
		if onChain {
			return ShareStateObservedOnChain, share.ShareNullifier, nil
		}
		return ShareStateFailed, nil, nil
	}

	roundBytes, err := hex.DecodeString(share.Payload.VoteRoundID)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: decode vote_round_id: %v", errMalformedSharePayload, err)
	}
	var roundID [32]byte
	if len(roundBytes) != 32 {
		return 0, nil, fmt.Errorf("%w: vote_round_id must be 32 bytes, got %d", errMalformedSharePayload, len(roundBytes))
	}
	copy(roundID[:], roundBytes)

	nullifier, err := p.preProofDedupe.shareNullifier(share, roundID)
	if err != nil {
		if errors.Is(err, errMalformedSharePayload) {
			return 0, nil, err
		}
		return 0, nil, fmt.Errorf("%w: compute share nullifier: %w", errCloseoutRetryable, err)
	}
	onChain, err := p.preProofDedupe.shareNF(share.Payload.VoteRoundID, nullifier[:])
	if err != nil {
		return 0, nil, fmt.Errorf("%w: check share nullifier: %w", errCloseoutRetryable, err)
	}
	if onChain {
		return ShareStateObservedOnChain, nullifier[:], nil
	}
	return ShareStateMissedDeadline, nullifier[:], nil
}

func (p *Processor) shareNullifierForAudit(share QueuedShare) []byte {
	if p.preProofDedupe == nil {
		return nil
	}
	roundBytes, err := hex.DecodeString(share.Payload.VoteRoundID)
	if err != nil || len(roundBytes) != 32 {
		return nil
	}
	var roundID [32]byte
	copy(roundID[:], roundBytes)

	nullifier, err := p.preProofDedupe.shareNullifier(share, roundID)
	if err != nil {
		return nil
	}
	return bytesFromArray32(nullifier)
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
					err := fmt.Errorf("panic in processShare: %v", r)
					spanErr = err
					CaptureErr(err, map[string]string{
						"round_id":    share.Payload.VoteRoundID,
						"share_index": strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
						"stage":       "panic",
					})
					p.logger.Error("panic in share processing",
						"round_id", share.Payload.VoteRoundID,
						"share_index", share.Payload.EncShare.ShareIndex,
						"panic", r,
					)
					p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition, p.shareNullifierForAudit(share))
				}
			}()

			select {
			case <-shareCtx.Done():
				spanErr = shareCtx.Err()
				return nil
			default:
			}

			if p.isRoundActive != nil {
				_, statusSpan := StartTrace(shareCtx, "helper.round_status_check", "helper.round_status_check", nil, nil)
				active, err := p.isRoundActive(share.Payload.VoteRoundID)
				statusSpan.Finish(err)
				if err != nil {
					spanErr = err
					p.logger.Warn("round status check failed, skipping share",
						"round_id", share.Payload.VoteRoundID,
						"share_index", share.Payload.EncShare.ShareIndex,
						"error", err,
					)
					CaptureErr(err, map[string]string{
						"round_id":    share.Payload.VoteRoundID,
						"share_index": strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
						"stage":       "round_status_check",
					})
					p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition, p.shareNullifierForAudit(share))
					return nil
				}
				if !active {
					shareSpan.SetData("outcome", "round_inactive")
					p.logger.Info("round no longer active, skipping share",
						"round_id", share.Payload.VoteRoundID,
						"share_index", share.Payload.EncShare.ShareIndex,
					)
					p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition, p.shareNullifierForAudit(share))
					return nil
				}
			}

			result, err := p.processShare(shareCtx, share)
			if err != nil {
				spanErr = err
				p.logger.Warn("share processing failed",
					"round_id", share.Payload.VoteRoundID,
					"share_index", share.Payload.EncShare.ShareIndex,
					"error", err,
				)
				CaptureErr(err, map[string]string{
					"round_id":    share.Payload.VoteRoundID,
					"share_index": strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
					"stage":       "process_share",
				})
				p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition, p.shareNullifierForAudit(share))
				return nil
			}

			switch result.state {
			case ShareStateObservedOnChain:
				shareSpan.SetData("outcome", "observed_on_chain")
				if err := p.store.MarkObservedOnChain(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition, result.nullifier); err != nil {
					spanErr = err
					p.logger.Error("mark observed on-chain failed",
						"round_id", share.Payload.VoteRoundID,
						"share_index", share.Payload.EncShare.ShareIndex,
						"error", err,
					)
					return nil
				}
				p.logger.Info("share already observed on-chain",
					"round_id", share.Payload.VoteRoundID,
					"share_index", share.Payload.EncShare.ShareIndex,
				)
			default:
				shareSpan.SetData("outcome", "submitted")
				p.store.MarkSubmitted(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
				p.logger.Info("share submitted",
					"round_id", share.Payload.VoteRoundID,
					"share_index", share.Payload.EncShare.ShareIndex,
				)
			}
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

// processShare handles a single share: Merkle path → proof → submit.
func (p *Processor) processShare(ctx context.Context, share QueuedShare) (processShareResult, error) {
	// Scope the tree reader to this share's voting round.
	roundBytes, err := hex.DecodeString(share.Payload.VoteRoundID)
	if err != nil {
		return processShareResult{}, fmt.Errorf("decode vote_round_id: %w", err)
	}
	var roundID [32]byte
	if len(roundBytes) != 32 {
		return processShareResult{}, fmt.Errorf("vote_round_id must be 32 bytes, got %d", len(roundBytes))
	}
	copy(roundID[:], roundBytes)

	if p.preProofDedupe != nil {
		alreadyRevealed, nullifier, err := p.preProofDedupe.shareAlreadyRevealed(ctx, share, roundID)
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
			return processShareResult{
				state:     ShareStateObservedOnChain,
				nullifier: bytesFromArray32(nullifier),
			}, nil
		}
	}

	tree := p.tree.ForRound(roundBytes)

	// Read tree status (leaf count + anchor height) without loading leaf data.
	status, err := tree.GetTreeStatus()
	if err != nil {
		return processShareResult{}, fmt.Errorf("read tree status: %w", err)
	}
	if status.LeafCount == 0 {
		return processShareResult{}, fmt.Errorf("commitment tree is empty")
	}
	if share.Payload.TreePosition >= status.LeafCount {
		return processShareResult{}, fmt.Errorf("tree_position %d out of range (tree has %d leaves)",
			share.Payload.TreePosition, status.LeafCount)
	}
	anchorHeight := status.AnchorHeight

	// Compute Merkle authentication path via the persistent KV-backed tree.
	// O(depth) shard reads — no leaf replay.
	merklePath, err := tree.MerklePath(share.Payload.TreePosition, uint32(anchorHeight))
	if err != nil {
		return processShareResult{}, fmt.Errorf("compute merkle path: %w", err)
	}

	// Decode share_comms.
	var shareComms [16][32]byte
	if len(share.Payload.ShareComms) != 16 {
		return processShareResult{}, fmt.Errorf("expected 16 share_comms, got %d", len(share.Payload.ShareComms))
	}
	for i, c := range share.Payload.ShareComms {
		cBytes, err := base64.StdEncoding.DecodeString(c)
		if err != nil {
			return processShareResult{}, fmt.Errorf("decode share_comms[%d]: %w", i, err)
		}
		if len(cBytes) != 32 {
			return processShareResult{}, fmt.Errorf("share_comms[%d] must be 32 bytes, got %d", i, len(cBytes))
		}
		copy(shareComms[i][:], cBytes)
	}

	// Decode primary_blind.
	var primaryBlind [32]byte
	pbBytes, err := base64.StdEncoding.DecodeString(share.Payload.PrimaryBlind)
	if err != nil {
		return processShareResult{}, fmt.Errorf("decode primary_blind: %w", err)
	}
	if len(pbBytes) != 32 {
		return processShareResult{}, fmt.Errorf("primary_blind must be 32 bytes, got %d", len(pbBytes))
	}
	copy(primaryBlind[:], pbBytes)

	// Decode the revealed share's C1/C2 once, reused for both the prover and the message.
	c1Bytes, err := base64.StdEncoding.DecodeString(share.Payload.EncShare.C1)
	if err != nil {
		return processShareResult{}, fmt.Errorf("decode enc_share.c1: %w", err)
	}
	if len(c1Bytes) != 32 {
		return processShareResult{}, fmt.Errorf("enc_share.c1 must be 32 bytes, got %d", len(c1Bytes))
	}
	c2Bytes, err := base64.StdEncoding.DecodeString(share.Payload.EncShare.C2)
	if err != nil {
		return processShareResult{}, fmt.Errorf("decode enc_share.c2: %w", err)
	}
	if len(c2Bytes) != 32 {
		return processShareResult{}, fmt.Errorf("enc_share.c2 must be 32 bytes, got %d", len(c2Bytes))
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
		"share_index":   share.Payload.EncShare.ShareIndex,
		"proposal_id":   share.Payload.ProposalID,
		"vote_decision": share.Payload.VoteDecision,
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
		return processShareResult{}, fmt.Errorf("generate proof: %w", err)
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
		return processShareResult{}, fmt.Errorf("submit: %w", err)
	}
	if result.Code != 0 {
		if IsDuplicateNullifier(result.Code) {
			p.logger.Info("share already observed on-chain",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
			)
			return processShareResult{
				state:     ShareStateObservedOnChain,
				nullifier: bytesFromArray32(nullifier),
			}, nil
		}
		return processShareResult{}, fmt.Errorf("chain rejected tx (code %d): %s", result.Code, result.Log)
	}

	p.logger.Debug("MsgRevealShare broadcast ok", "tx_hash", result.TxHash)
	return processShareResult{state: ShareStateSubmitted}, nil
}

// shareAlreadyRevealed computes the queued share's nullifier and checks whether
// the chain has already recorded it for the share's voting round.
func (d *preProofShareDeduper) shareAlreadyRevealed(ctx context.Context, share QueuedShare, roundID [32]byte) (bool, [32]byte, error) {
	_, span := StartTrace(ctx, "helper.dedupe", "helper.preproof_share_nullifier_check", map[string]string{
		"round_id":    share.Payload.VoteRoundID,
		"share_index": strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
	}, map[string]interface{}{
		"proposal_id":   share.Payload.ProposalID,
		"vote_decision": share.Payload.VoteDecision,
	})

	var spanErr error
	var already bool
	var nullifier [32]byte
	defer func() {
		span.SetData("already_revealed", already)
		span.Finish(spanErr)
	}()

	nullifier, err := d.shareNullifier(share, roundID)
	if err != nil {
		spanErr = err
		return false, [32]byte{}, err
	}
	already, err = d.shareNF(share.Payload.VoteRoundID, nullifier[:])
	if err != nil {
		spanErr = err
		return false, [32]byte{}, fmt.Errorf("check share nullifier: %w", err)
	}
	return already, nullifier, nil
}

func (d *preProofShareDeduper) shareNullifier(share QueuedShare, roundID [32]byte) ([32]byte, error) {
	sharesHash, err := decodeBase64Array32(share.Payload.SharesHash, "shares_hash")
	if err != nil {
		return [32]byte{}, err
	}
	primaryBlind, err := decodeBase64Array32(share.Payload.PrimaryBlind, "primary_blind")
	if err != nil {
		return [32]byte{}, err
	}
	voteCommitment, err := d.vcHash(roundID, sharesHash, share.Payload.ProposalID, share.Payload.VoteDecision)
	if err != nil {
		return [32]byte{}, fmt.Errorf("compute vote commitment: %w", err)
	}
	nullifier, err := d.shareNFHash(voteCommitment, share.Payload.EncShare.ShareIndex, primaryBlind)
	if err != nil {
		return [32]byte{}, fmt.Errorf("compute share nullifier: %w", err)
	}
	return nullifier, nil
}

func decodeBase64Array32(value string, field string) ([32]byte, error) {
	var out [32]byte
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return out, fmt.Errorf("%w: decode %s: %v", errMalformedSharePayload, field, err)
	}
	if len(decoded) != 32 {
		return out, fmt.Errorf("%w: %s must be 32 bytes, got %d", errMalformedSharePayload, field, len(decoded))
	}
	copy(out[:], decoded)
	return out, nil
}

func bytesFromArray32(value [32]byte) []byte {
	out := make([]byte, 32)
	copy(out, value[:])
	return out
}
