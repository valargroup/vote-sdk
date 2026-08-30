package types

import (
	"bytes"
	"fmt"
	"strings"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

const MaxUpgradeInfoBytes = 4096

// MaxCastVoteBatchSize bounds one atomic vote batch independently of the
// number of proposals in a round.
const MaxCastVoteBatchSize = 15

// zeroPoint32 is the compressed encoding of the Pallas identity (point at
// infinity). Used by ValidateBasic as a cheap stateless guard against the
// identity-point signature bypass.
var zeroPoint32 [32]byte

// ValidateBasic performs stateless validation for MsgCreateVotingSession.
func (msg *MsgCreateVotingSession) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if msg.SnapshotHeight == 0 {
		return fmt.Errorf("%w: snapshot_height cannot be zero", ErrInvalidField)
	}
	if len(msg.SnapshotBlockhash) != 32 {
		return fmt.Errorf("%w: snapshot_blockhash must be 32 bytes, got %d", ErrInvalidField, len(msg.SnapshotBlockhash))
	}
	if len(msg.ProposalsHash) != 32 {
		return fmt.Errorf("%w: proposals_hash must be 32 bytes, got %d", ErrInvalidField, len(msg.ProposalsHash))
	}
	if msg.VoteEndTime == 0 {
		return fmt.Errorf("%w: vote_end_time cannot be zero", ErrInvalidField)
	}
	if len(msg.NullifierImtRoot) != 32 {
		return fmt.Errorf("%w: nullifier_imt_root must be 32 bytes, got %d", ErrInvalidField, len(msg.NullifierImtRoot))
	}
	if len(msg.NcRoot) != 32 {
		return fmt.Errorf("%w: nc_root must be 32 bytes, got %d", ErrInvalidField, len(msg.NcRoot))
	}
	if len(msg.Proposals) == 0 || len(msg.Proposals) > MaxProposals {
		return fmt.Errorf("%w: proposals count must be between 1 and %d, got %d", ErrInvalidField, MaxProposals, len(msg.Proposals))
	}
	for i, p := range msg.Proposals {
		if p == nil {
			return fmt.Errorf("%w: proposal %d cannot be nil", ErrInvalidField, i)
		}
		if p.Title == "" {
			return fmt.Errorf("%w: proposal %d title cannot be empty", ErrInvalidField, i)
		}
		if p.Id != uint32(i+1) {
			return fmt.Errorf("%w: proposal id mismatch at index %d: expected %d, got %d", ErrInvalidField, i, i+1, p.Id)
		}
		if err := ValidateProposalOptions(i, p.Options); err != nil {
			return err
		}
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgDelegateVote.
func (msg *MsgDelegateVote) ValidateBasic() error {
	if len(msg.Rk) != 32 {
		return fmt.Errorf("%w: rk must be 32 bytes, got %d", ErrInvalidField, len(msg.Rk))
	}
	if bytes.Equal(msg.Rk, zeroPoint32[:]) {
		return fmt.Errorf("%w: rk must not be the identity point (all zeros)", ErrInvalidField)
	}
	if len(msg.SpendAuthSig) != 64 {
		return fmt.Errorf("%w: spend_auth_sig must be 64 bytes, got %d", ErrInvalidField, len(msg.SpendAuthSig))
	}
	if len(msg.SignedNoteNullifier) != 32 {
		return fmt.Errorf("%w: signed_note_nullifier must be 32 bytes, got %d", ErrInvalidField, len(msg.SignedNoteNullifier))
	}
	if len(msg.CmxNew) != 32 {
		return fmt.Errorf("%w: cmx_new must be 32 bytes, got %d", ErrInvalidField, len(msg.CmxNew))
	}
	if len(msg.VanCmx) != 32 {
		return fmt.Errorf("%w: van_cmx must be 32 bytes, got %d", ErrInvalidField, len(msg.VanCmx))
	}
	if len(msg.GovNullifiers) == 0 {
		return fmt.Errorf("%w: gov_nullifiers cannot be empty", ErrInvalidField)
	}
	if len(msg.GovNullifiers) > 5 {
		return fmt.Errorf("%w: gov_nullifiers cannot exceed 5, got %d", ErrInvalidField, len(msg.GovNullifiers))
	}
	for i, n := range msg.GovNullifiers {
		if len(n) != 32 {
			return fmt.Errorf("%w: gov_nullifiers[%d] must be 32 bytes, got %d", ErrInvalidField, i, len(n))
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
	if len(msg.Proof) == 0 || len(msg.Proof) > MaxProofSize {
		return fmt.Errorf("%w: proof must be 1..%d bytes, got %d", ErrInvalidField, MaxProofSize, len(msg.Proof))
	}
	if len(msg.VoteRoundId) != RoundIDLen {
		return fmt.Errorf("%w: vote_round_id must be exactly %d bytes, got %d", ErrInvalidField, RoundIDLen, len(msg.VoteRoundId))
	}
	if err := validateDelegationTX1Effects(msg); err != nil {
		return err
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgCastVote.
func (msg *MsgCastVote) ValidateBasic() error {
	if len(msg.VanNullifier) != 32 {
		return fmt.Errorf("%w: van_nullifier must be 32 bytes, got %d", ErrInvalidField, len(msg.VanNullifier))
	}
	if len(msg.VoteAuthorityNoteNew) != 32 {
		return fmt.Errorf("%w: vote_authority_note_new must be 32 bytes, got %d", ErrInvalidField, len(msg.VoteAuthorityNoteNew))
	}
	if len(msg.VoteCommitment) != 32 {
		return fmt.Errorf("%w: vote_commitment must be 32 bytes, got %d", ErrInvalidField, len(msg.VoteCommitment))
	}
	if msg.ProposalId < MinProposalID || msg.ProposalId > MaxProposals {
		return fmt.Errorf("%w: proposal_id must be %d..%d, got %d", ErrInvalidField, MinProposalID, MaxProposals, msg.ProposalId)
	}
	if len(msg.Proof) == 0 || len(msg.Proof) > MaxProofSize {
		return fmt.Errorf("%w: proof must be 1..%d bytes, got %d", ErrInvalidField, MaxProofSize, len(msg.Proof))
	}
	if len(msg.VoteRoundId) != RoundIDLen {
		return fmt.Errorf("%w: vote_round_id must be exactly %d bytes, got %d", ErrInvalidField, RoundIDLen, len(msg.VoteRoundId))
	}
	if msg.VoteCommTreeAnchorHeight == 0 {
		return fmt.Errorf("%w: vote_comm_tree_anchor_height cannot be zero", ErrInvalidField)
	}
	if len(msg.VoteAuthSig) != 64 {
		return fmt.Errorf("%w: vote_auth_sig must be 64 bytes, got %d", ErrInvalidField, len(msg.VoteAuthSig))
	}
	if len(msg.RVpk) != 32 {
		return fmt.Errorf("%w: r_vpk must be 32 bytes, got %d", ErrInvalidField, len(msg.RVpk))
	}
	if bytes.Equal(msg.RVpk, zeroPoint32[:]) {
		return fmt.Errorf("%w: r_vpk must not be the identity point (all zeros)", ErrInvalidField)
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgCastVoteBatch.
func (msg *MsgCastVoteBatch) ValidateBasic() error {
	if msg == nil {
		return fmt.Errorf("%w: batch cannot be nil", ErrInvalidField)
	}
	if len(msg.Votes) == 0 || len(msg.Votes) > MaxCastVoteBatchSize {
		return fmt.Errorf("%w: votes count must be between 1 and %d, got %d", ErrInvalidField, MaxCastVoteBatchSize, len(msg.Votes))
	}

	first := msg.Votes[0]
	if first == nil {
		return fmt.Errorf("%w: votes[0] cannot be nil", ErrInvalidField)
	}

	seenProposals := make(map[uint32]struct{}, len(msg.Votes))
	seenNullifiers := make(map[string]struct{}, len(msg.Votes))
	for i, vote := range msg.Votes {
		if vote == nil {
			return fmt.Errorf("%w: votes[%d] cannot be nil", ErrInvalidField, i)
		}
		if err := vote.ValidateBasic(); err != nil {
			return fmt.Errorf("votes[%d]: %w", i, err)
		}
		if !bytes.Equal(vote.VoteRoundId, first.VoteRoundId) {
			return fmt.Errorf("%w: votes[%d] has a different vote_round_id", ErrInvalidField, i)
		}
		if vote.VoteCommTreeAnchorHeight != first.VoteCommTreeAnchorHeight {
			return fmt.Errorf("%w: votes[%d] has a different vote_comm_tree_anchor_height", ErrInvalidField, i)
		}
		if _, exists := seenProposals[vote.ProposalId]; exists {
			return fmt.Errorf("%w: duplicate proposal_id %d at votes[%d]", ErrInvalidField, vote.ProposalId, i)
		}
		seenProposals[vote.ProposalId] = struct{}{}

		nullifierKey := string(vote.VanNullifier)
		if _, exists := seenNullifiers[nullifierKey]; exists {
			return fmt.Errorf("%w: duplicate van_nullifier at votes[%d]", ErrInvalidField, i)
		}
		seenNullifiers[nullifierKey] = struct{}{}
	}

	return nil
}

// ValidateBasic performs stateless validation for MsgRevealShare.
func (msg *MsgRevealShare) ValidateBasic() error {
	if len(msg.ShareNullifier) != 32 {
		return fmt.Errorf("%w: share_nullifier must be 32 bytes, got %d", ErrInvalidField, len(msg.ShareNullifier))
	}
	if len(msg.EncShare) != 64 {
		return fmt.Errorf("%w: enc_share must be 64 bytes (ElGamal ciphertext), got %d", ErrInvalidField, len(msg.EncShare))
	}
	if msg.ProposalId < MinProposalID || msg.ProposalId > MaxProposals {
		return fmt.Errorf("%w: proposal_id must be %d..%d, got %d", ErrInvalidField, MinProposalID, MaxProposals, msg.ProposalId)
	}
	if err := ValidateVoteChoiceUpperBound(msg.VoteDecision); err != nil {
		return err
	}
	if len(msg.Proof) == 0 || len(msg.Proof) > MaxProofSize {
		return fmt.Errorf("%w: proof must be 1..%d bytes, got %d", ErrInvalidField, MaxProofSize, len(msg.Proof))
	}
	if len(msg.VoteRoundId) != RoundIDLen {
		return fmt.Errorf("%w: vote_round_id must be exactly %d bytes, got %d", ErrInvalidField, RoundIDLen, len(msg.VoteRoundId))
	}
	if msg.VoteCommTreeAnchorHeight == 0 {
		return fmt.Errorf("%w: vote_comm_tree_anchor_height cannot be zero", ErrInvalidField)
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgRotatePallasKey.
func (msg *MsgRotatePallasKey) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if len(msg.NewPallasPk) != 32 {
		return fmt.Errorf("%w: new_pallas_pk must be 32 bytes, got %d", ErrInvalidField, len(msg.NewPallasPk))
	}
	if bytes.Equal(msg.NewPallasPk, zeroPoint32[:]) {
		return fmt.Errorf("%w: new_pallas_pk must not be the identity point (all zeros)", ErrInvalidField)
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
	if len(msg.VoteRoundId) != RoundIDLen {
		return fmt.Errorf("%w: vote_round_id must be exactly %d bytes, got %d", ErrInvalidField, RoundIDLen, len(msg.VoteRoundId))
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

// ValidateBasic performs stateless validation for MsgScheduleUpgrade.
func (msg *MsgScheduleUpgrade) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if strings.TrimSpace(msg.Name) == "" {
		return fmt.Errorf("%w: name cannot be empty", ErrInvalidField)
	}
	if msg.Height <= 0 {
		return fmt.Errorf("%w: height must be greater than 0", ErrInvalidField)
	}
	if len(msg.Info) > MaxUpgradeInfoBytes {
		return fmt.Errorf("%w: info cannot exceed %d bytes, got %d", ErrInvalidField, MaxUpgradeInfoBytes, len(msg.Info))
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgSetEndorser.
func (msg *MsgSetEndorser) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return fmt.Errorf("%w: creator %q is not a valid bech32 address: %v", ErrInvalidField, msg.Creator, err)
	}
	if err := ValidateEndorserID(msg.EndorserId); err != nil {
		return err
	}
	if msg.Address != "" {
		if _, err := sdk.AccAddressFromBech32(msg.Address); err != nil {
			return fmt.Errorf("%w: address %q is not a valid bech32 address: %v", ErrInvalidField, msg.Address, err)
		}
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgUpdateVoteManagers.
func (msg *MsgUpdateVoteManagers) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return fmt.Errorf("%w: creator %q is not a valid bech32 address: %v", ErrInvalidField, msg.Creator, err)
	}
	if _, _, err := ValidateAndNormalizeVoteManagerPolicy(msg.NewVoteManagers, msg.NewThreshold); err != nil {
		return err
	}
	if NormalizeMinCeremonyValidators(msg.NewMinCeremonyValidators) < 1 {
		return fmt.Errorf("%w: new_min_ceremony_validators must be at least 1", ErrInvalidField)
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgAuthorizedSend.
func (msg *MsgAuthorizedSend) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return fmt.Errorf("%w: creator %q is not a valid bech32 address: %v", ErrInvalidField, msg.Creator, err)
	}
	if _, err := sdk.AccAddressFromBech32(msg.ToAddress); err != nil {
		return fmt.Errorf("%w: invalid to_address: %v", ErrInvalidField, err)
	}
	amt, ok := sdkmath.NewIntFromString(msg.Amount)
	if !ok || !amt.IsPositive() {
		return fmt.Errorf("%w: amount must be a positive integer string", ErrInvalidField)
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgCancelUpgrade.
func (msg *MsgCancelUpgrade) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgProposeCoordinatorAction.
func (msg *MsgProposeCoordinatorAction) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return fmt.Errorf("%w: creator %q is not a valid bech32 address: %v", ErrInvalidField, msg.Creator, err)
	}
	if msg.Payload == nil || msg.Payload.TypeUrl == "" || len(msg.Payload.Value) == 0 {
		return fmt.Errorf("%w: payload cannot be empty", ErrInvalidCoordinatorAction)
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgApproveCoordinatorAction.
func (msg *MsgApproveCoordinatorAction) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return fmt.Errorf("%w: creator %q is not a valid bech32 address: %v", ErrInvalidField, msg.Creator, err)
	}
	if msg.ActionId == 0 {
		return fmt.Errorf("%w: action_id cannot be zero", ErrInvalidCoordinatorAction)
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgEndorseRound.
func (msg *MsgEndorseRound) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return fmt.Errorf("%w: creator %q is not a valid bech32 address: %v", ErrInvalidField, msg.Creator, err)
	}
	if err := ValidateEndorserID(msg.EndorserId); err != nil {
		return err
	}
	if len(msg.VoteRoundId) != RoundIDLen {
		return fmt.Errorf("%w: vote_round_id must be exactly %d bytes, got %d", ErrInvalidField, RoundIDLen, len(msg.VoteRoundId))
	}
	return nil
}

// ValidateBasic performs stateless validation for MsgClearRoundEndorsement.
func (msg *MsgClearRoundEndorsement) ValidateBasic() error {
	if msg.Creator == "" {
		return fmt.Errorf("%w: creator cannot be empty", ErrInvalidField)
	}
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return fmt.Errorf("%w: creator %q is not a valid bech32 address: %v", ErrInvalidField, msg.Creator, err)
	}
	if err := ValidateEndorserID(msg.EndorserId); err != nil {
		return err
	}
	if len(msg.VoteRoundId) != RoundIDLen {
		return fmt.Errorf("%w: vote_round_id must be exactly %d bytes, got %d", ErrInvalidField, RoundIDLen, len(msg.VoteRoundId))
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

// GetVoteRoundId returns the shared voting round for a batch.
func (msg *MsgCastVoteBatch) GetVoteRoundId() []byte {
	if msg == nil || len(msg.Votes) == 0 || msg.Votes[0] == nil {
		return nil
	}
	return msg.Votes[0].VoteRoundId
}

// GetNullifiers returns all VAN nullifiers in action order.
func (msg *MsgCastVoteBatch) GetNullifiers() [][]byte {
	if msg == nil {
		return nil
	}
	nullifiers := make([][]byte, 0, len(msg.Votes))
	for _, vote := range msg.Votes {
		if vote != nil {
			nullifiers = append(nullifiers, vote.VanNullifier)
		}
	}
	return nullifiers
}

// GetNullifierType returns NullifierTypeVoteAuthorityNote for a vote batch.
func (msg *MsgCastVoteBatch) GetNullifierType() NullifierType {
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

// AcceptsTallyingRound returns false — casting a vote batch requires ACTIVE status.
func (msg *MsgCastVoteBatch) AcceptsTallyingRound() bool { return false }

// AcceptsTallyingRound returns false — shares must land before the vote window
// closes. Accepting shares during TALLYING would corrupt the tally accumulator
// after partial decryptions have been committed.
func (msg *MsgRevealShare) AcceptsTallyingRound() bool { return false }

// AcceptsTallyingRound returns false — session creation is unrelated to tallying.
func (msg *MsgCreateVotingSession) AcceptsTallyingRound() bool { return false }

// --- MsgSubmitTally VoteMessage implementations ---

// GetNullifiers returns nil for MsgSubmitTally (no nullifiers involved).
func (msg *MsgSubmitTally) GetNullifiers() [][]byte { return nil }

// GetNullifierType returns 0 for MsgSubmitTally (unused; no nullifiers).
func (msg *MsgSubmitTally) GetNullifierType() NullifierType { return 0 }

// AcceptsTallyingRound returns true — submitting a tally requires TALLYING status.
func (msg *MsgSubmitTally) AcceptsTallyingRound() bool { return true }

// isASCII returns true if every byte in s is in the ASCII range (0x00-0x7F).
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}
