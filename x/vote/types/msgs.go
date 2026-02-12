package types

import "fmt"

// ValidateBasic performs stateless validation for MsgCreateVotingSession.
func (msg *MsgCreateVotingSession) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if msg.SnapshotHeight == 0 {
		return fmt.Errorf("%w: snapshot_height cannot be zero", ErrInvalidField)
	}
	if len(msg.SnapshotBlockhash) == 0 {
		return fmt.Errorf("%w: snapshot_blockhash cannot be empty", ErrInvalidField)
	}
	if len(msg.ProposalsHash) == 0 {
		return fmt.Errorf("%w: proposals_hash cannot be empty", ErrInvalidField)
	}
	if msg.VoteEndTime == 0 {
		return fmt.Errorf("%w: vote_end_time cannot be zero", ErrInvalidField)
	}
	if len(msg.NullifierImtRoot) == 0 {
		return fmt.Errorf("%w: nullifier_imt_root cannot be empty", ErrInvalidField)
	}
	if len(msg.NcRoot) == 0 {
		return fmt.Errorf("%w: nc_root cannot be empty", ErrInvalidField)
	}
	if len(msg.EaPk) != 32 {
		return fmt.Errorf("%w: ea_pk must be 32 bytes, got %d", ErrInvalidField, len(msg.EaPk))
	}
	if len(msg.VkZkp1) == 0 {
		return fmt.Errorf("%w: vk_zkp1 cannot be empty", ErrInvalidField)
	}
	if len(msg.VkZkp2) == 0 {
		return fmt.Errorf("%w: vk_zkp2 cannot be empty", ErrInvalidField)
	}
	if len(msg.VkZkp3) == 0 {
		return fmt.Errorf("%w: vk_zkp3 cannot be empty", ErrInvalidField)
	}
	if len(msg.Proposals) == 0 || len(msg.Proposals) > 16 {
		return fmt.Errorf("%w: proposals count must be between 1 and 16, got %d", ErrInvalidField, len(msg.Proposals))
	}
	for i, p := range msg.Proposals {
		if p.Title == "" {
			return fmt.Errorf("%w: proposal %d title cannot be empty", ErrInvalidField, i)
		}
		if p.Id != uint32(i) {
			return fmt.Errorf("%w: proposal id mismatch at index %d: expected %d, got %d", ErrInvalidField, i, i, p.Id)
		}
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgDelegateVote.
func (msg *MsgDelegateVote) ValidateBasic() error {
	if len(msg.Rk) != 32 {
		return fmt.Errorf("%w: rk must be 32 bytes, got %d", ErrInvalidField, len(msg.Rk))
	}
	if len(msg.SpendAuthSig) == 0 {
		return fmt.Errorf("%w: spend_auth_sig cannot be empty", ErrInvalidField)
	}
	if len(msg.SignedNoteNullifier) == 0 {
		return fmt.Errorf("%w: signed_note_nullifier cannot be empty", ErrInvalidField)
	}
	if len(msg.CmxNew) == 0 {
		return fmt.Errorf("%w: cmx_new cannot be empty", ErrInvalidField)
	}
	if len(msg.GovComm) == 0 {
		return fmt.Errorf("%w: gov_comm cannot be empty", ErrInvalidField)
	}
	if len(msg.GovNullifiers) == 0 {
		return fmt.Errorf("%w: gov_nullifiers cannot be empty", ErrInvalidField)
	}
	if len(msg.GovNullifiers) > 4 {
		return fmt.Errorf("%w: gov_nullifiers cannot exceed 4, got %d", ErrInvalidField, len(msg.GovNullifiers))
	}
	for i, n := range msg.GovNullifiers {
		if len(n) == 0 {
			return fmt.Errorf("%w: gov_nullifiers[%d] cannot be empty", ErrInvalidField, i)
		}
	}
	if len(msg.Proof) == 0 {
		return fmt.Errorf("%w: proof cannot be empty", ErrInvalidField)
	}
	if len(msg.VoteRoundId) == 0 {
		return fmt.Errorf("%w: vote_round_id cannot be empty", ErrInvalidField)
	}
	if len(msg.Sighash) != 32 {
		return fmt.Errorf("%w: sighash must be 32 bytes, got %d", ErrInvalidField, len(msg.Sighash))
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgCastVote.
func (msg *MsgCastVote) ValidateBasic() error {
	if len(msg.VanNullifier) == 0 {
		return fmt.Errorf("%w: van_nullifier cannot be empty", ErrInvalidField)
	}
	if len(msg.VoteAuthorityNoteNew) == 0 {
		return fmt.Errorf("%w: vote_authority_note_new cannot be empty", ErrInvalidField)
	}
	if len(msg.VoteCommitment) == 0 {
		return fmt.Errorf("%w: vote_commitment cannot be empty", ErrInvalidField)
	}
	if len(msg.Proof) == 0 {
		return fmt.Errorf("%w: proof cannot be empty", ErrInvalidField)
	}
	if len(msg.VoteRoundId) == 0 {
		return fmt.Errorf("%w: vote_round_id cannot be empty", ErrInvalidField)
	}
	if msg.VoteCommTreeAnchorHeight == 0 {
		return fmt.Errorf("%w: vote_comm_tree_anchor_height cannot be zero", ErrInvalidField)
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgRevealShare.
func (msg *MsgRevealShare) ValidateBasic() error {
	if len(msg.ShareNullifier) == 0 {
		return fmt.Errorf("%w: share_nullifier cannot be empty", ErrInvalidField)
	}
	if msg.VoteAmount == 0 {
		return fmt.Errorf("%w: vote_amount cannot be zero", ErrInvalidField)
	}
	if len(msg.Proof) == 0 {
		return fmt.Errorf("%w: proof cannot be empty", ErrInvalidField)
	}
	if len(msg.VoteRoundId) == 0 {
		return fmt.Errorf("%w: vote_round_id cannot be empty", ErrInvalidField)
	}
	if msg.VoteCommTreeAnchorHeight == 0 {
		return fmt.Errorf("%w: vote_comm_tree_anchor_height cannot be zero", ErrInvalidField)
	}
	return nil
}

// VoteMessage is an interface that all vote module messages implement,
// used by the validation pipeline.
type VoteMessage interface {
	ValidateBasic() error
	GetVoteRoundId() []byte
	GetNullifiers() [][]byte
	GetNullifierType() NullifierType
	// AcceptsTallyingRound returns true if this message type is valid during
	// the TALLYING phase. Only MsgRevealShare returns true.
	AcceptsTallyingRound() bool
}

// --- VoteMessage interface implementations ---

// GetNullifiers returns the nullifiers from a MsgDelegateVote.
func (msg *MsgDelegateVote) GetNullifiers() [][]byte {
	return msg.GovNullifiers
}

// GetNullifierType returns NullifierTypeGov for MsgDelegateVote.
func (msg *MsgDelegateVote) GetNullifierType() NullifierType {
	return NullifierTypeGov
}

// GetNullifiers returns the nullifiers from a MsgCastVote.
func (msg *MsgCastVote) GetNullifiers() [][]byte {
	return [][]byte{msg.VanNullifier}
}

// GetNullifierType returns NullifierTypeVoteAuthorityNote for MsgCastVote.
func (msg *MsgCastVote) GetNullifierType() NullifierType {
	return NullifierTypeVoteAuthorityNote
}

// GetNullifiers returns the nullifiers from a MsgRevealShare.
func (msg *MsgRevealShare) GetNullifiers() [][]byte {
	return [][]byte{msg.ShareNullifier}
}

// GetNullifierType returns NullifierTypeShare for MsgRevealShare.
func (msg *MsgRevealShare) GetNullifierType() NullifierType {
	return NullifierTypeShare
}

// GetNullifiers returns nil for MsgCreateVotingSession (no nullifiers involved).
func (msg *MsgCreateVotingSession) GetNullifiers() [][]byte {
	return nil
}

// GetNullifierType returns 0 for MsgCreateVotingSession (unused; guarded by
// len(nullifiers) > 0 check in the ante handler).
func (msg *MsgCreateVotingSession) GetNullifierType() NullifierType {
	return 0
}

// GetVoteRoundId returns nil for MsgCreateVotingSession (round doesn't exist yet).
func (msg *MsgCreateVotingSession) GetVoteRoundId() []byte {
	return nil
}

// --- AcceptsTallyingRound implementations ---

// AcceptsTallyingRound returns false — delegation requires ACTIVE status.
func (msg *MsgDelegateVote) AcceptsTallyingRound() bool { return false }

// AcceptsTallyingRound returns false — casting votes requires ACTIVE status.
func (msg *MsgCastVote) AcceptsTallyingRound() bool { return false }

// AcceptsTallyingRound returns true — revealing shares is accepted during TALLYING.
func (msg *MsgRevealShare) AcceptsTallyingRound() bool { return true }

// AcceptsTallyingRound returns false — session creation is unrelated to tallying.
func (msg *MsgCreateVotingSession) AcceptsTallyingRound() bool { return false }
