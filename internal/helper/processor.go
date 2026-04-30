package helper

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"cosmossdk.io/log"
	"golang.org/x/sync/errgroup"
)

const maintenanceInterval = 30 * time.Second

// Processor is the background share processing loop. It checks the share queue
// when wallet-provided submit_at times arrive, generates Merkle paths and ZKP
// 3 proofs, and submits MsgRevealShare to the chain.
type Processor struct {
	store         *ShareStore
	tree          TreeReader
	prover        ProofGenerator
	submitter     *ChainSubmitter
	logger        log.Logger
	maxConcurrent int
	isRoundActive RoundStatusChecker
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
) *Processor {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	return &Processor{
		store:         store,
		tree:          tree,
		prover:        prover,
		submitter:     submitter,
		logger:        logger,
		maxConcurrent: maxConcurrent,
		isRoundActive: isRoundActive,
	}
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
					p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
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
					p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
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
				p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
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

// processShare handles a single share: Merkle path → proof → submit.
func (p *Processor) processShare(ctx context.Context, share QueuedShare) error {
	// Scope the tree reader to this share's voting round.
	roundBytes, err := hex.DecodeString(share.Payload.VoteRoundID)
	if err != nil {
		return fmt.Errorf("decode vote_round_id: %w", err)
	}
	tree := p.tree.ForRound(roundBytes)

	// Read tree status (leaf count + anchor height) without loading leaf data.
	status, err := tree.GetTreeStatus()
	if err != nil {
		return fmt.Errorf("read tree status: %w", err)
	}
	if status.LeafCount == 0 {
		return fmt.Errorf("commitment tree is empty")
	}
	if share.Payload.TreePosition >= status.LeafCount {
		return fmt.Errorf("tree_position %d out of range (tree has %d leaves)",
			share.Payload.TreePosition, status.LeafCount)
	}
	anchorHeight := status.AnchorHeight

	// Compute Merkle authentication path via the persistent KV-backed tree.
	// O(depth) shard reads — no leaf replay.
	merklePath, err := tree.MerklePath(share.Payload.TreePosition, uint32(anchorHeight))
	if err != nil {
		return fmt.Errorf("compute merkle path: %w", err)
	}

	// Build fixed-size round_id array for the proof generator.
	var roundID [32]byte
	if len(roundBytes) != 32 {
		return fmt.Errorf("vote_round_id must be 32 bytes, got %d", len(roundBytes))
	}
	copy(roundID[:], roundBytes)

	// Decode share_comms.
	var shareComms [16][32]byte
	if len(share.Payload.ShareComms) != 16 {
		return fmt.Errorf("expected 16 share_comms, got %d", len(share.Payload.ShareComms))
	}
	for i, c := range share.Payload.ShareComms {
		cBytes, err := base64.StdEncoding.DecodeString(c)
		if err != nil {
			return fmt.Errorf("decode share_comms[%d]: %w", i, err)
		}
		if len(cBytes) != 32 {
			return fmt.Errorf("share_comms[%d] must be 32 bytes, got %d", i, len(cBytes))
		}
		copy(shareComms[i][:], cBytes)
	}

	// Decode primary_blind.
	var primaryBlind [32]byte
	pbBytes, err := base64.StdEncoding.DecodeString(share.Payload.PrimaryBlind)
	if err != nil {
		return fmt.Errorf("decode primary_blind: %w", err)
	}
	if len(pbBytes) != 32 {
		return fmt.Errorf("primary_blind must be 32 bytes, got %d", len(pbBytes))
	}
	copy(primaryBlind[:], pbBytes)

	// Decode the revealed share's C1/C2 once, reused for both the prover and the message.
	c1Bytes, err := base64.StdEncoding.DecodeString(share.Payload.EncShare.C1)
	if err != nil {
		return fmt.Errorf("decode enc_share.c1: %w", err)
	}
	if len(c1Bytes) != 32 {
		return fmt.Errorf("enc_share.c1 must be 32 bytes, got %d", len(c1Bytes))
	}
	c2Bytes, err := base64.StdEncoding.DecodeString(share.Payload.EncShare.C2)
	if err != nil {
		return fmt.Errorf("decode enc_share.c2: %w", err)
	}
	if len(c2Bytes) != 32 {
		return fmt.Errorf("enc_share.c2 must be 32 bytes, got %d", len(c2Bytes))
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
		return fmt.Errorf("generate proof: %w", err)
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
		return fmt.Errorf("submit: %w", err)
	}
	if result.Code != 0 {
		if IsDuplicateNullifier(result.Code) {
			p.logger.Info("share already revealed by another helper",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
			)
			return nil
		}
		return fmt.Errorf("chain rejected tx (code %d): %s", result.Code, result.Log)
	}

	p.logger.Debug("MsgRevealShare broadcast ok", "tx_hash", result.TxHash)
	return nil
}
