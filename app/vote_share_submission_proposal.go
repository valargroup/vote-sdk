package app

import (
	"fmt"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// MaxVoteShareSubmissionsPerBlock bounds expensive vote share submission proof
// verification in a single block. With normal block times, 256 submissions per
// block still provides substantially more capacity than a voting round needs.
const MaxVoteShareSubmissionsPerBlock = 256

type voteShareSubmissionProposalKey struct {
	roundID   [types.RoundIDLen]byte
	nullifier [32]byte
}

type voteShareSubmissionFilterStats struct {
	kept       int
	duplicates int
	malformed  int
	overLimit  int
}

// filterVoteShareSubmissionTransactions keeps the first well-formed
// transaction for each round-scoped share nullifier, preserves the relative
// order of all retained transactions, and enforces
// MaxVoteShareSubmissionsPerBlock.
func filterVoteShareSubmissionTransactions(txs [][]byte) ([][]byte, voteShareSubmissionFilterStats) {
	filtered := make([][]byte, 0, len(txs))
	seen := make(map[voteShareSubmissionProposalKey]struct{})
	stats := voteShareSubmissionFilterStats{}

	for _, txBytes := range txs {
		key, isSubmission, err := voteShareSubmissionProposalKeyFromTx(txBytes)
		if !isSubmission {
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
		if stats.kept >= MaxVoteShareSubmissionsPerBlock {
			stats.overLimit++
			continue
		}
		seen[key] = struct{}{}
		stats.kept++
		filtered = append(filtered, txBytes)
	}

	return filtered, stats
}

// validateVoteShareSubmissionTransactions enforces the same submission
// invariants as proposal preparation so a proposer cannot bypass deduplication
// or the proof-work cap.
func validateVoteShareSubmissionTransactions(txs [][]byte) error {
	seen := make(map[voteShareSubmissionProposalKey]struct{})
	submissionCount := 0

	for i, txBytes := range txs {
		key, isSubmission, err := voteShareSubmissionProposalKeyFromTx(txBytes)
		if !isSubmission {
			continue
		}
		if err != nil {
			return fmt.Errorf("vote share submission transaction %d is malformed: %w", i, err)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("vote share submission transaction %d duplicates a round-scoped share nullifier", i)
		}

		seen[key] = struct{}{}
		submissionCount++
		if submissionCount > MaxVoteShareSubmissionsPerBlock {
			return fmt.Errorf("proposal contains %d vote share submission transactions; maximum is %d", submissionCount, MaxVoteShareSubmissionsPerBlock)
		}
	}

	return nil
}

// voteShareSubmissionProposalKeyFromTx recognizes the existing MsgRevealShare
// wire type and cheaply extracts the fields used for proposal deduplication.
// Full signature and proof verification remain in the ante handler.
func voteShareSubmissionProposalKeyFromTx(txBytes []byte) (voteShareSubmissionProposalKey, bool, error) {
	if len(txBytes) == 0 || txBytes[0] != voteapi.TagRevealShare {
		return voteShareSubmissionProposalKey{}, false, nil
	}

	_, voteMsg, err := voteapi.DecodeVoteTx(txBytes)
	if err != nil {
		return voteShareSubmissionProposalKey{}, true, err
	}
	submission, ok := voteMsg.(*types.MsgRevealShare)
	if !ok {
		return voteShareSubmissionProposalKey{}, true, fmt.Errorf("expected MsgRevealShare, got %T", voteMsg)
	}
	if err := submission.ValidateBasic(); err != nil {
		return voteShareSubmissionProposalKey{}, true, err
	}

	var key voteShareSubmissionProposalKey
	copy(key.roundID[:], submission.VoteRoundId)
	copy(key.nullifier[:], submission.ShareNullifier)
	return key, true, nil
}
