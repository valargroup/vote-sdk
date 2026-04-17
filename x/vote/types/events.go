package types

// Event types emitted by the vote module.
const (
	EventTypeCreateVotingSession          = "create_voting_session"
	EventTypeDelegateVote                 = "delegate_vote"
	EventTypeCastVote                     = "cast_vote"
	EventTypeRevealShare                  = "reveal_share"
	EventTypeCommitmentTreeRoot           = "commitment_tree_root"
	EventTypeRoundStatusChange            = "round_status_change"
	EventTypeSubmitTally                  = "submit_tally"
	EventTypeRegisterPallasKey            = "register_pallas_key"
	EventTypeRotatePallasKey              = "rotate_pallas_key"
	EventTypeContributeDKG               = "contribute_dkg"
	EventTypeAckExecutiveAuthorityKey     = "ack_executive_authority_key"
	EventTypeCeremonyStatusChange             = "ceremony_status_change"
	EventTypeUpdateVoteManagers                     = "update_vote_managers"
	EventTypeSubmitPartialDecryption          = "submit_partial_decryption"
	EventTypeAuthorizedSend                   = "authorized_send"
	EventTypeTallyTimeout                     = "tally_timeout"
)

// Event attribute keys.
const (
	AttributeKeyRoundID            = "vote_round_id"
	AttributeKeyCreator            = "creator"
	AttributeKeyLeafIndex          = "leaf_index"
	AttributeKeyNullifiers         = "nullifier_count"
	AttributeKeyProposalID         = "proposal_id"
	AttributeKeyVoteDecision       = "vote_decision"
	AttributeKeyShareNullifier     = "share_nullifier"
	AttributeKeyTreeRoot           = "tree_root"
	AttributeKeyBlockHeight        = "block_height"
	AttributeKeyOldStatus          = "old_status"
	AttributeKeyNewStatus          = "new_status"
	AttributeKeyFinalizedEntries   = "finalized_entries"
	AttributeKeyValidatorAddress   = "validator_address"
	AttributeKeyCeremonyStatus     = "ceremony_status"
	AttributeKeyEAPK               = "ea_pk"
	AttributeKeyVoteManagers             = "vote_managers"
	AttributeKeyValidatorIndex     = "validator_index"
	AttributeKeyEntryCount         = "entry_count"
	AttributeKeySender             = "sender"
	AttributeKeyRecipient          = "recipient"
	AttributeKeyAmount             = "amount"
	AttributeKeyOldPallasPk        = "old_pallas_pk"
	AttributeKeyNewPallasPk        = "new_pallas_pk"
)
