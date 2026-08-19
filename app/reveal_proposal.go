package app

import (
	"fmt"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// MaxRevealSharesPerBlock bounds expensive reveal proof verification in a
// single block. With normal block times, 256 reveals per block still provides
// substantially more capacity than a voting round needs over its active window.
const MaxRevealSharesPerBlock = 256

type revealProposalKey struct {
	roundID   [types.RoundIDLen]byte
	nullifier [32]byte
}

type revealFilterStats struct {
	kept       int
	duplicates int
	malformed  int
	overLimit  int
}

// filterRevealTransactions keeps the first well-formed transaction for each
// round-scoped share nullifier, preserves the relative order of all retained
// transactions, and enforces MaxRevealSharesPerBlock.
func filterRevealTransactions(txs [][]byte) ([][]byte, revealFilterStats) {
	filtered := make([][]byte, 0, len(txs))
	seen := make(map[revealProposalKey]struct{})
	stats := revealFilterStats{}

	for _, txBytes := range txs {
		if len(txBytes) > 0 && txBytes[0] == voteapi.TagRevealShare && stats.kept >= MaxRevealSharesPerBlock {
			stats.overLimit++
			continue
		}
		key, isReveal, err := revealProposalKeyFromTx(txBytes)
		if !isReveal {
			filtered = append(filtered, txBytes)
			continue
		}
		if err != nil {
			stats.malformed++
			continue
		}
		if _, exists := seen[key]; exists {
			stats.duplicates++
			continue
		}
		seen[key] = struct{}{}
		stats.kept++
		filtered = append(filtered, txBytes)
	}

	return filtered, stats
}

// validateRevealTransactions enforces the same reveal invariants as proposal
// preparation so a proposer cannot bypass deduplication or the proof-work cap.
func validateRevealTransactions(txs [][]byte) error {
	seen := make(map[revealProposalKey]struct{})
	revealCount := 0

	for i, txBytes := range txs {
		key, isReveal, err := revealProposalKeyFromTx(txBytes)
		if !isReveal {
			continue
		}
		if err != nil {
			return fmt.Errorf("reveal transaction %d is malformed: %w", i, err)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("reveal transaction %d duplicates a round-scoped share nullifier", i)
		}

		seen[key] = struct{}{}
		revealCount++
		if revealCount > MaxRevealSharesPerBlock {
			return fmt.Errorf("proposal contains %d reveal transactions; maximum is %d", revealCount, MaxRevealSharesPerBlock)
		}
	}

	return nil
}

// revealProposalKeyFromTx cheaply extracts the fields used for proposal
// deduplication. Full signature and proof verification remain in the ante
// handler and is deliberately not repeated here.
func revealProposalKeyFromTx(txBytes []byte) (revealProposalKey, bool, error) {
	if len(txBytes) == 0 || txBytes[0] != voteapi.TagRevealShare {
		return revealProposalKey{}, false, nil
	}

	_, voteMsg, err := voteapi.DecodeVoteTx(txBytes)
	if err != nil {
		return revealProposalKey{}, true, err
	}
	reveal, ok := voteMsg.(*types.MsgRevealShare)
	if !ok {
		return revealProposalKey{}, true, fmt.Errorf("expected MsgRevealShare, got %T", voteMsg)
	}
	if err := reveal.ValidateBasic(); err != nil {
		return revealProposalKey{}, true, err
	}

	var key revealProposalKey
	copy(key.roundID[:], reveal.VoteRoundId)
	copy(key.nullifier[:], reveal.ShareNullifier)
	return key, true, nil
}
