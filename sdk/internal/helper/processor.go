package helper

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"cosmossdk.io/log"
	"golang.org/x/sync/errgroup"
)

// Processor is the background share processing loop. It periodically checks the
// share queue for shares whose delay has elapsed, generates Merkle paths and ZKP #3
// proofs, and submits MsgRevealShare to the chain.
type Processor struct {
	store         *ShareStore
	tree          TreeReader
	prover        ProofGenerator
	submitter     *ChainSubmitter
	logger        log.Logger
	interval      time.Duration
	// maxConcurrent bounds the number of shares processed in parallel.
	maxConcurrent int
}

// NewProcessor creates a new share processor.
func NewProcessor(
	store *ShareStore,
	tree TreeReader,
	prover ProofGenerator,
	submitter *ChainSubmitter,
	logger log.Logger,
	interval time.Duration,
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
		interval:      interval,
		maxConcurrent: maxConcurrent,
	}
}

// Run starts the processing loop. Blocks until ctx is cancelled.
func (p *Processor) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.processBatch(ctx)
		}
	}
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

	// Decode the revealed share's C1/C2 for the prover.
	c1Decoded, _ := base64.StdEncoding.DecodeString(share.Payload.EncShare.C1)
	c2Decoded, _ := base64.StdEncoding.DecodeString(share.Payload.EncShare.C2)
	var encC1X, encC2X [32]byte
	copy(encC1X[:], c1Decoded)
	copy(encC2X[:], c2Decoded)

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
	c1Bytes, _ := base64.StdEncoding.DecodeString(share.Payload.EncShare.C1)
	c2Bytes, _ := base64.StdEncoding.DecodeString(share.Payload.EncShare.C2)
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
		return fmt.Errorf("chain rejected tx (code %d): %s", result.Code, result.Log)
	}

	p.logger.Debug("MsgRevealShare broadcast ok", "tx_hash", result.TxHash)
	return nil
}
