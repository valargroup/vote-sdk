package helper

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"cosmossdk.io/log"
	"golang.org/x/sync/errgroup"
)

// Processor is the background share processing loop. It checks the share queue
// at Poisson-distributed intervals (exponential inter-arrival times) for shares
// whose delay has elapsed, generates Merkle paths and ZKP #3 proofs, and submits
// MsgRevealShare to the chain. The random timing prevents an observer from
// correlating submission patterns with share readiness.
type Processor struct {
	store     *ShareStore
	tree      TreeReader
	prover    ProofGenerator
	submitter *ChainSubmitter
	logger    log.Logger
	// meanInterval is the mean of the exponential distribution for the time
	// between processing cycles. Submissions form a Poisson process.
	meanInterval  time.Duration
	maxConcurrent int
}

// NewProcessor creates a new share processor.
func NewProcessor(
	store *ShareStore,
	tree TreeReader,
	prover ProofGenerator,
	submitter *ChainSubmitter,
	logger log.Logger,
	meanInterval time.Duration,
	maxConcurrent int,
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
		meanInterval:  meanInterval,
		maxConcurrent: maxConcurrent,
	}
}

// Run starts the processing loop. Blocks until ctx is cancelled.
// Wake-up intervals follow an exponential distribution so that share
// submissions form a Poisson process, preventing timing correlation.
func (p *Processor) Run(ctx context.Context) error {
	for {
		delay := p.randomDelay()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			p.processBatch(ctx)
		}
	}
}

// exponentialDelay samples from Exp(1/mean) using crypto/rand for
// unpredictable timing.
func exponentialDelay(mean time.Duration) time.Duration {
	if mean <= 0 {
		return 0
	}
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	u := (float64(binary.LittleEndian.Uint64(buf[:])) + 1.0) / (float64(1<<64) + 1.0)
	delaySecs := -mean.Seconds() * math.Log(u)
	return time.Duration(delaySecs * float64(time.Second))
}

// randomDelay samples from Exp(1/meanInterval) for inter-cycle timing.
func (p *Processor) randomDelay() time.Duration {
	return exponentialDelay(p.meanInterval)
}

// intraShareDelay samples from Exp(2/meanInterval) — half the mean of the
// inter-cycle delay — adding jitter between individual share submissions
// within a batch.
func (p *Processor) intraShareDelay() time.Duration {
	return exponentialDelay(p.meanInterval / 2)
}

// processBatch takes all ready shares and processes them.
func (p *Processor) processBatch(ctx context.Context) {
	ready := p.store.TakeReady()
	if len(ready) == 0 {
		return
	}

	p.logger.Info(
		"processing ready shares",
		"count", len(ready),
		"max_concurrent", p.maxConcurrent,
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(p.maxConcurrent)

	for _, queued := range ready {
		share := queued
		g.Go(func() error {
			select {
			case <-gctx.Done():
				return nil
			default:
			}

			// Skip jitter if less than 60s remain before vote ends — submit immediately.
			if share.VoteEndTime == 0 || time.Until(time.Unix(int64(share.VoteEndTime), 0)) > 60*time.Second {
				delay := p.intraShareDelay()
				timer := time.NewTimer(delay)
				select {
				case <-gctx.Done():
					timer.Stop()
					return nil
				case <-timer.C:
				}
			}

			if err := p.processShare(gctx, share); err != nil {
				p.logger.Warn("share processing failed",
					"round_id", share.Payload.VoteRoundID,
					"share_index", share.Payload.EncShare.ShareIndex,
					"error", err,
				)
				p.store.MarkFailed(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
				return nil
			}

			p.store.MarkSubmitted(share.Payload.VoteRoundID, share.Payload.EncShare.ShareIndex, share.Payload.ProposalID, share.Payload.TreePosition)
			p.logger.Info("share submitted",
				"round_id", share.Payload.VoteRoundID,
				"share_index", share.Payload.EncShare.ShareIndex,
			)
			return nil
		})
	}
	_ = g.Wait()
}

// processShare handles a single share: Merkle path → proof → submit.
func (p *Processor) processShare(ctx context.Context, share QueuedShare) error {
	// Read tree status (leaf count + anchor height) without loading leaf data.
	status, err := p.tree.GetTreeStatus()
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
	merklePath, err := p.tree.MerklePath(share.Payload.TreePosition, uint32(anchorHeight))
	if err != nil {
		return fmt.Errorf("compute merkle path: %w", err)
	}

	// Decode round_id from hex to raw 32 bytes.
	var roundID [32]byte
	roundBytes, err := hex.DecodeString(share.Payload.VoteRoundID)
	if err != nil {
		return fmt.Errorf("decode vote_round_id: %w", err)
	}
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
	c1Bytes, _ := base64.StdEncoding.DecodeString(share.Payload.EncShare.C1)
	c2Bytes, _ := base64.StdEncoding.DecodeString(share.Payload.EncShare.C2)
	var encC1X, encC2X [32]byte
	copy(encC1X[:], c1Bytes)
	copy(encC2X[:], c2Bytes)

	// Generate ZKP #3 proof.
	proof, nullifier, _, err := p.prover.GenerateShareRevealProof(
		merklePath,
		shareComms,
		primaryBlind,
		encC1X,
		encC2X,
		share.Payload.EncShare.ShareIndex,
		share.Payload.ProposalID,
		share.Payload.VoteDecision,
		roundID,
	)
	if err != nil {
		return fmt.Errorf("generate proof: %w", err)
	}

	// Build enc_share: C1 || C2 (64 bytes).
	encShareBytes := make([]byte, 64)
	copy(encShareBytes[:32], c1Bytes)
	copy(encShareBytes[32:], c2Bytes)

	// Submit to chain.
	msg := &MsgRevealShareJSON{
		ShareNullifier:           base64.StdEncoding.EncodeToString(nullifier[:]),
		EncShare:                 base64.StdEncoding.EncodeToString(encShareBytes),
		ProposalID:               share.Payload.ProposalID,
		VoteDecision:             share.Payload.VoteDecision,
		Proof:                    base64.StdEncoding.EncodeToString(proof),
		VoteRoundID:              base64.StdEncoding.EncodeToString(roundBytes),
		VoteCommTreeAnchorHeight: anchorHeight,
	}

	result, err := p.submitter.SubmitRevealShare(msg)
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
