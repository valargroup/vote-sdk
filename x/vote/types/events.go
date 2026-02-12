package types

// Event types emitted by the vote module.
const (
	EventTypeCreateVotingSession = "create_voting_session"
	EventTypeDelegateVote        = "delegate_vote"
	EventTypeCastVote            = "cast_vote"
	EventTypeRevealShare         = "reveal_share"
	EventTypeCommitmentTreeRoot  = "commitment_tree_root"
)

// Event attribute keys.
const (
	AttributeKeyRoundID      = "vote_round_id"
	AttributeKeyCreator      = "creator"
	AttributeKeyLeafIndex    = "leaf_index"
	AttributeKeyNullifiers   = "nullifier_count"
	AttributeKeyProposalID   = "proposal_id"
	AttributeKeyVoteDecision = "vote_decision"
	AttributeKeyVoteAmount   = "vote_amount"
	AttributeKeyTreeRoot     = "tree_root"
	AttributeKeyBlockHeight  = "block_height"
)
