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
	store      *ShareStore
	tree       TreeReader
	merklePath MerklePathFunc
	prover     ProofGenerator
	submitter  *ChainSubmitter
	logger     log.Logger
	interval   time.Duration
	// maxConcurrent bounds the number of shares processed in parallel.
	maxConcurrent int
}

// NewProcessor creates a new share processor.
func NewProcessor(
	store *ShareStore,
	tree TreeReader,
	merklePath MerklePathFunc,
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
		store:      store,
		tree:       tree,
		merklePath: merklePath,
		prover:     prover,
		submitter:  submitter,
		logger:     logger,
		interval:   interval,
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
	// Read all commitment leaves from the keeper and find anchor height.
	leaves, anchorHeight, err := p.tree.GetAllLeaves()
	if err != nil {
		return fmt.Errorf("read tree leaves: %w", err)
	}
	if len(leaves) == 0 {
		return fmt.Errorf("commitment tree is empty")
	}
	if share.Payload.TreePosition >= uint64(len(leaves)) {
		return fmt.Errorf("tree_position %d out of range (tree has %d leaves)",
			share.Payload.TreePosition, len(leaves))
	}

	// Compute Merkle authentication path.
	merklePath, err := p.merklePath(leaves, share.Payload.TreePosition)
	if err != nil {
		return fmt.Errorf("compute merkle path: %w", err)
	}

	// Decode all_enc_shares into 10 × 32-byte array.
	var allEncShares [10][32]byte
	if len(share.Payload.AllEncShares) != 5 {
		return fmt.Errorf("expected 5 all_enc_shares, got %d", len(share.Payload.AllEncShares))
	}
	for i, es := range share.Payload.AllEncShares {
		c1Bytes, err := base64.StdEncoding.DecodeString(es.C1)
		if err != nil {
			return fmt.Errorf("decode all_enc_shares[%d].c1: %w", i, err)
		}
		c2Bytes, err := base64.StdEncoding.DecodeString(es.C2)
		if err != nil {
			return fmt.Errorf("decode all_enc_shares[%d].c2: %w", i, err)
		}
		if len(c1Bytes) != 32 || len(c2Bytes) != 32 {
			return fmt.Errorf("all_enc_shares[%d] c1/c2 must be 32 bytes", i)
		}
		copy(allEncShares[i*2][:], c1Bytes)
		copy(allEncShares[i*2+1][:], c2Bytes)
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

	// Decode shares_hash.
	var sharesHash [32]byte
	shBytes, err := base64.StdEncoding.DecodeString(share.Payload.SharesHash)
	if err != nil {
		return fmt.Errorf("decode shares_hash: %w", err)
	}
	if len(shBytes) != 32 {
		return fmt.Errorf("shares_hash must be 32 bytes, got %d", len(shBytes))
	}
	copy(sharesHash[:], shBytes)

	// Generate ZKP #3 proof.
	proof, nullifier, _, err := p.prover.GenerateShareRevealProof(
		merklePath,
		allEncShares,
		share.Payload.EncShare.ShareIndex,
		share.Payload.ProposalID,
		share.Payload.VoteDecision,
		roundID,
		sharesHash,
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
