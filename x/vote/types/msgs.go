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
		if p.Id != uint32(i+1) {
			return fmt.Errorf("%w: proposal id mismatch at index %d: expected %d, got %d", ErrInvalidField, i, i+1, p.Id)
		}
		// Each proposal must have 2-8 vote options.
		if len(p.Options) < 2 || len(p.Options) > 8 {
			return fmt.Errorf("%w: proposal %d must have 2-8 options, got %d", ErrInvalidField, i, len(p.Options))
		}
		for j, opt := range p.Options {
			if opt.Index != uint32(j) {
				return fmt.Errorf("%w: proposal %d option index mismatch at position %d: expected %d, got %d", ErrInvalidField, i, j, j, opt.Index)
			}
			if opt.Label == "" {
				return fmt.Errorf("%w: proposal %d option %d label cannot be empty", ErrInvalidField, i, j)
			}
			if !isASCII(opt.Label) {
				return fmt.Errorf("%w: proposal %d option %d label must contain only ASCII characters", ErrInvalidField, i, j)
			}
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
	if len(msg.VanCmx) == 0 {
		return fmt.Errorf("%w: van_cmx cannot be empty", ErrInvalidField)
	}
	if len(msg.GovNullifiers) == 0 {
		return fmt.Errorf("%w: gov_nullifiers cannot be empty", ErrInvalidField)
	}
	if len(msg.GovNullifiers) > 5 {
		return fmt.Errorf("%w: gov_nullifiers cannot exceed 5, got %d", ErrInvalidField, len(msg.GovNullifiers))
	}
	for i, n := range msg.GovNullifiers {
		if len(n) == 0 {
			return fmt.Errorf("%w: gov_nullifiers[%d] cannot be empty", ErrInvalidField, i)
		}
	}

	// Cheap defense-in-depth: reject duplicate gov_nullifiers within the same message
	// since the circuit does not constrain the 5 governance nullifiers to be distinct.
	seen := make(map[string]struct{}, len(msg.GovNullifiers))
	for i, nf := range msg.GovNullifiers {
		k := string(nf)
		if _, dup := seen[k]; dup {
			return fmt.Errorf("%w: duplicate gov_nullifiers[%d]", ErrInvalidField, i)
		}
		seen[k] = struct{}{}
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
	if len(msg.RVpkX) != 32 {
		return fmt.Errorf("%w: r_vpk_x must be 32 bytes (Pallas Fp), got %d", ErrInvalidField, len(msg.RVpkX))
	}
	if len(msg.RVpkY) != 32 {
		return fmt.Errorf("%w: r_vpk_y must be 32 bytes (Pallas Fp), got %d", ErrInvalidField, len(msg.RVpkY))
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
	if len(msg.VoteAuthSig) == 0 {
		return fmt.Errorf("%w: vote_auth_sig cannot be empty", ErrInvalidField)
	}
	if len(msg.Sighash) != 32 {
		return fmt.Errorf("%w: sighash must be 32 bytes, got %d", ErrInvalidField, len(msg.Sighash))
	}
	if len(msg.RVpk) != 32 {
		return fmt.Errorf("%w: r_vpk must be 32 bytes, got %d", ErrInvalidField, len(msg.RVpk))
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgRevealShare.
func (msg *MsgRevealShare) ValidateBasic() error {
	if len(msg.ShareNullifier) == 0 {
		return fmt.Errorf("%w: share_nullifier cannot be empty", ErrInvalidField)
	}
	if len(msg.EncShare) != 64 {
		return fmt.Errorf("%w: enc_share must be 64 bytes (ElGamal ciphertext), got %d", ErrInvalidField, len(msg.EncShare))
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

// ValidateBasic performs stateless validation for MsgSubmitTally.
func (msg *MsgSubmitTally) ValidateBasic() error {
	if len(msg.VoteRoundId) == 0 {
		return fmt.Errorf("%w: vote_round_id cannot be empty", ErrInvalidField)
	}
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	// Check for duplicate (proposal_id, vote_decision) pairs.
	seen := make(map[[2]uint32]bool, len(msg.Entries))
	for i, e := range msg.Entries {
		key := [2]uint32{e.ProposalId, e.VoteDecision}
		if seen[key] {
			return fmt.Errorf("%w: duplicate entry at index %d: proposal_id=%d vote_decision=%d",
				ErrInvalidField, i, e.ProposalId, e.VoteDecision)
		}
		seen[key] = true
	}
	return nil
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

// AcceptsTallyingRound returns true — revealing shares is accepted during both
// ACTIVE and TALLYING phases. This routes to ValidateRoundForShares.
func (msg *MsgRevealShare) AcceptsTallyingRound() bool { return true }

// AcceptsTallyingRound returns false — session creation is unrelated to tallying.
func (msg *MsgCreateVotingSession) AcceptsTallyingRound() bool { return false }

// --- MsgSubmitTally VoteMessage implementations ---

// GetNullifiers returns nil for MsgSubmitTally (no nullifiers involved).
func (msg *MsgSubmitTally) GetNullifiers() [][]byte { return nil }

// GetNullifierType returns 0 for MsgSubmitTally (unused; no nullifiers).
func (msg *MsgSubmitTally) GetNullifierType() NullifierType { return 0 }

// AcceptsTallyingRound returns true — submitting a tally requires TALLYING status.
func (msg *MsgSubmitTally) AcceptsTallyingRound() bool { return true }

// ValidateBasic performs stateless validation for MsgUnjailValidator.
func (msg *MsgUnjailValidator) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if msg.ValidatorAddress == "" {
		return fmt.Errorf("%w: validator_address cannot be empty", ErrInvalidField)
	}
	return nil
}

// isASCII returns true if every byte in s is in the ASCII range (0x00-0x7F).
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}
