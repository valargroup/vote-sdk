package types

// Event types emitted by the vote module.
const (
	EventTypeSetupVoteRound       = "setup_vote_round"
	EventTypeRegisterDelegation   = "register_delegation"
	EventTypeCreateVoteCommitment = "create_vote_commitment"
	EventTypeRevealVoteShare      = "reveal_vote_share"
	EventTypeCommitmentTreeRoot   = "commitment_tree_root"
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
