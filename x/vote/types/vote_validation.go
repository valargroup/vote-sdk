package types

import "fmt"

// ValidateProposalOptions checks the SDK/chain policy for proposal options.
// Proposal indexes are zero-based here because this is used while validating
// the ordered proposal list in MsgCreateVotingSession.
func ValidateProposalOptions(proposalIndex int, options []*VoteOption) error {
	if len(options) < MinVoteOptions || len(options) > MaxVoteOptions {
		return fmt.Errorf("%w: proposal %d must have %d-%d options, got %d",
			ErrInvalidField, proposalIndex, MinVoteOptions, MaxVoteOptions, len(options))
	}

	for j, opt := range options {
		if opt == nil {
			return fmt.Errorf("%w: proposal %d option %d cannot be nil", ErrInvalidField, proposalIndex, j)
		}
		if opt.Index != uint32(j) {
			return fmt.Errorf("%w: proposal %d option index mismatch at position %d: expected %d, got %d",
				ErrInvalidField, proposalIndex, j, j, opt.Index)
		}
		if opt.Label == "" {
			return fmt.Errorf("%w: proposal %d option %d label cannot be empty", ErrInvalidField, proposalIndex, j)
		}
		if !isASCII(opt.Label) {
			return fmt.Errorf("%w: proposal %d option %d label must contain only ASCII characters",
				ErrInvalidField, proposalIndex, j)
		}
	}

	return nil
}

// ValidateVoteChoice checks that voteDecision selects an existing option for a
// proposal. Proposal IDs are one-based here because this is used after a round
// has been created.
func ValidateVoteChoice(proposalID, voteDecision uint32, options []*VoteOption) error {
	if len(options) < MinVoteOptions || len(options) > MaxVoteOptions {
		return fmt.Errorf("%w: proposal %d has invalid option count %d; expected %d-%d",
			ErrInvalidField, proposalID, len(options), MinVoteOptions, MaxVoteOptions)
	}
	if int(voteDecision) >= len(options) {
		return fmt.Errorf("%w: vote_decision %d out of range [0, %d] for proposal %d",
			ErrInvalidField, voteDecision, len(options)-1, proposalID)
	}
	return nil
}

// ValidateVoteChoiceUpperBound checks only the global SDK/chain vote option
// cap. Use ValidateVoteChoice when the proposal's actual option list is known.
func ValidateVoteChoiceUpperBound(voteDecision uint32) error {
	if voteDecision >= MaxVoteOptions {
		return fmt.Errorf("%w: vote_decision must be 0..%d, got %d",
			ErrInvalidField, MaxVoteOptions-1, voteDecision)
	}
	return nil
}
