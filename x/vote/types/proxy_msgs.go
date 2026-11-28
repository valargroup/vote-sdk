package types

import (
	"bytes"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

var (
	_ VoteMessage = (*MsgProxyDelegationBatch)(nil)
	_ VoteMessage = (*MsgDelegateOp)(nil)
)

// ValidateBasic performs the stateless checks of
// docs/proxy-delegation/contracts-v1.md §2.5 on a 0x09 batch. Every failure is
// ErrInvalidField, wrapped so its registered code surfaces (§0).
func (msg *MsgProxyDelegationBatch) ValidateBasic() error {
	if msg == nil {
		return errorsmod.Wrap(ErrInvalidField, "proxy delegation batch cannot be nil")
	}
	if err := requireBytes("vote_round_id", msg.VoteRoundId, RoundIDLen); err != nil {
		return err
	}
	if n := len(msg.Dcs); n == 0 || n > MaxProxyDelegationDCs {
		return errorsmod.Wrapf(ErrInvalidField, "dcs count must be between 1 and %d, got %d", MaxProxyDelegationDCs, n)
	}
	if msg.ExpiryHeight == 0 {
		return errorsmod.Wrap(ErrInvalidField, "expiry_height cannot be zero")
	}
	if (msg.Registration != nil) != (msg.VoteCommTreeAnchorHeight == 0) {
		return errorsmod.Wrap(ErrInvalidField, "registration must be set if and only if vote_comm_tree_anchor_height is zero")
	}
	if msg.Registration != nil {
		// MsgDelegateVote's checks wrap with fmt, which hides the code.
		if err := msg.Registration.validateBasic(); err != nil {
			return errorsmod.Wrapf(ErrInvalidField, "registration: %v", err)
		}
		if !bytes.Equal(msg.Registration.VoteRoundId, msg.VoteRoundId) {
			return errorsmod.Wrap(ErrInvalidField, "registration has a different vote_round_id")
		}
	}

	vanNullifiers := make(map[string]struct{}, len(msg.Dcs))
	slotNullifiers := make(map[string]struct{}, len(msg.Dcs))
	// Leaves the batch appends: every DC and the final successor VAN.
	leaves := make(map[string]struct{}, len(msg.Dcs)+1)
	for i, dc := range msg.Dcs {
		if err := dc.validateBasic(); err != nil {
			return errorsmod.Wrapf(err, "dcs[%d]", i)
		}
		if !addUnique(vanNullifiers, dc.VanNullifier) {
			return errorsmod.Wrapf(ErrInvalidField, "dcs[%d] repeats a van_nullifier", i)
		}
		if !addUnique(slotNullifiers, dc.DcSlotNullifier) {
			return errorsmod.Wrapf(ErrInvalidField, "dcs[%d] repeats a dc_slot_nullifier", i)
		}
		if !addUnique(leaves, dc.ProxyDelegationCommitment) {
			return errorsmod.Wrapf(ErrInvalidField, "dcs[%d] repeats a proxy_delegation_commitment", i)
		}
	}
	if !addUnique(leaves, msg.Dcs[len(msg.Dcs)-1].VoteAuthorityNoteNew) {
		return errorsmod.Wrap(ErrInvalidField, "the final vote_authority_note_new equals a proxy_delegation_commitment")
	}
	return nil
}

// validateBasic checks one DC's field sizes and its non-identity r_vpk, as
// MsgCastVote does.
func (dc *ProxyDelegationAction) validateBasic() error {
	if dc == nil {
		return errorsmod.Wrap(ErrInvalidField, "dc cannot be nil")
	}
	for _, field := range [...]struct {
		name  string
		value []byte
	}{
		{"van_nullifier", dc.VanNullifier},
		{"r_vpk", dc.RVpk},
		{"vote_authority_note_new", dc.VoteAuthorityNoteNew},
		{"proxy_delegation_commitment", dc.ProxyDelegationCommitment},
		{"dc_slot_nullifier", dc.DcSlotNullifier},
	} {
		if err := requireBytes(field.name, field.value, 32); err != nil {
			return err
		}
	}
	if bytes.Equal(dc.RVpk, zeroPoint32[:]) {
		return errorsmod.Wrap(ErrInvalidField, "r_vpk must not be the identity point (all zeros)")
	}
	if len(dc.Proof) == 0 || len(dc.Proof) > MaxProofSize {
		return errorsmod.Wrapf(ErrInvalidField, "proof must be 1..%d bytes, got %d", MaxProofSize, len(dc.Proof))
	}
	return requireBytes("vote_auth_sig", dc.VoteAuthSig, 64)
}

// ValidateBasic checks that exactly one of register, retire and ballot is set
// and validates it (contracts-v1 §2.5).
func (msg *MsgDelegateOp) ValidateBasic() error {
	if msg == nil {
		return errorsmod.Wrap(ErrInvalidField, "delegate op cannot be nil")
	}
	var (
		set  int
		name string
		err  error
	)
	if msg.Register != nil {
		set, name, err = set+1, "register", msg.Register.ValidateBasic()
	}
	if msg.Retire != nil {
		set, name, err = set+1, "retire", msg.Retire.ValidateBasic()
	}
	if msg.Ballot != nil {
		set, name, err = set+1, "ballot", msg.Ballot.ValidateBasic()
	}
	if set != 1 {
		return errorsmod.Wrapf(ErrInvalidField, "exactly one of register, retire and ballot must be set, got %d", set)
	}
	if err != nil {
		return errorsmod.Wrap(err, name)
	}
	return nil
}

// ValidateBasic performs the stateless checks of contracts-v1 §2.5 on a
// delegate registration, including the canonical handle and proof_id. It does
// not apply the Ed25519 key rule, which is ErrInvalidDelegateKey (§5.3).
func (op *DelegateRegister) ValidateBasic() error {
	if err := requireBytes("dk", op.GetDk(), 32); err != nil {
		return err
	}
	if err := ValidateDelegateHandle(op.GetProvider(), op.GetHandle()); err != nil {
		return err
	}
	if err := ValidateDelegateProofID(op.GetProvider(), op.GetProofId()); err != nil {
		return err
	}
	attestations := op.GetAttestations()
	if n := len(attestations); n == 0 || n > MaxDelegateAttestations {
		return errorsmod.Wrapf(ErrInvalidField, "attestations count must be between 1 and %d, got %d", MaxDelegateAttestations, n)
	}
	for i, attestation := range attestations {
		if attestation == nil {
			return errorsmod.Wrapf(ErrInvalidField, "attestations[%d] cannot be nil", i)
		}
		if err := requireBytes("sig", attestation.Sig, 64); err != nil {
			return errorsmod.Wrapf(err, "attestations[%d]", i)
		}
		// not_before < expires_at <= not_before + 72 h, without overflow.
		if attestation.NotBefore >= attestation.ExpiresAt ||
			attestation.ExpiresAt-attestation.NotBefore > MaxDelegateAttestationSeconds {
			return errorsmod.Wrapf(ErrInvalidField, "attestations[%d] needs not_before < expires_at <= not_before + %d, got %d and %d",
				i, MaxDelegateAttestationSeconds, attestation.NotBefore, attestation.ExpiresAt)
		}
	}
	return requireBytes("dk_sig", op.GetDkSig(), 64)
}

// ValidateBasic performs the stateless checks of contracts-v1 §2.5 on a
// delegate retirement.
func (op *DelegateRetire) ValidateBasic() error {
	if op.GetDelegateIndex() == 0 {
		return errorsmod.Wrap(ErrInvalidField, "delegate_index must be at least 1")
	}
	return requireBytes("dk_sig", op.GetDkSig(), 64)
}

// ValidateBasic performs the stateless checks of contracts-v1 §2.5 on a
// delegate ballot. Whether options match the round's proposals is checked
// against state (ErrInvalidBallot, §5.3).
func (op *DelegateBallot) ValidateBasic() error {
	if err := requireBytes("vote_round_id", op.GetVoteRoundId(), RoundIDLen); err != nil {
		return err
	}
	if op.GetDelegateIndex() == 0 {
		return errorsmod.Wrap(ErrInvalidField, "delegate_index must be at least 1")
	}
	if n := len(op.GetOptions()); n == 0 || n > MaxProposals {
		return errorsmod.Wrapf(ErrInvalidField, "options count must be between 1 and %d, got %d", MaxProposals, n)
	}
	return requireBytes("dk_sig", op.GetDkSig(), 64)
}

// ValidateBasic performs the stateless checks of contracts-v1 §2.5 on a params
// payload: a bech32 creator, then ValidateProxyDelegationParams. A bad creator
// is ErrInvalidProxyParams, as is every failure but the key rule's (§6).
func (msg *MsgSetProxyDelegationParams) ValidateBasic() error {
	if msg == nil {
		return errorsmod.Wrap(ErrInvalidProxyParams, "params payload cannot be nil")
	}
	if err := requireCreator(msg.Creator, ErrInvalidProxyParams); err != nil {
		return err
	}
	return ValidateProxyDelegationParams(&ProxyDelegationParams{
		Verifiers:         msg.Verifiers,
		VerifierThreshold: msg.VerifierThreshold,
		MaxDelegates:      msg.MaxDelegates,
	})
}

// ValidateBasic checks the pause payload's creator, its only stateless rule.
// A failure is ErrInvalidField (contracts-v1 §6).
func (msg *MsgSetProxyDelegationPause) ValidateBasic() error {
	if msg == nil {
		return errorsmod.Wrap(ErrInvalidField, "pause payload cannot be nil")
	}
	return requireCreator(msg.Creator, ErrInvalidField)
}

// ValidateBasic performs the stateless checks of contracts-v1 §2.5 on a
// status payload. Every failure is ErrInvalidField (§6).
func (msg *MsgSetDelegateStatus) ValidateBasic() error {
	if msg == nil {
		return errorsmod.Wrap(ErrInvalidField, "status payload cannot be nil")
	}
	if err := requireCreator(msg.Creator, ErrInvalidField); err != nil {
		return err
	}
	if n := len(msg.DelegateIndexes); n == 0 || n > MaxDelegateStatusIndexes {
		return errorsmod.Wrapf(ErrInvalidField, "delegate_indexes count must be between 1 and %d, got %d", MaxDelegateStatusIndexes, n)
	}
	seen := make(map[uint32]struct{}, len(msg.DelegateIndexes))
	for i, d := range msg.DelegateIndexes {
		if d == 0 {
			return errorsmod.Wrapf(ErrInvalidField, "delegate_indexes[%d] must be at least 1", i)
		}
		if _, dup := seen[d]; dup {
			return errorsmod.Wrapf(ErrInvalidField, "delegate_indexes[%d] repeats %d", i, d)
		}
		seen[d] = struct{}{}
	}
	switch msg.Action {
	case DelegateStatusActionSuspend, DelegateStatusActionLift, DelegateStatusActionRetire:
	default:
		return errorsmod.Wrapf(ErrInvalidField, "action must be 1, 2 or 3, got %d", msg.Action)
	}
	switch msg.Reason {
	case DelegateStatusReasonImpersonation, DelegateStatusReasonKeyLost, DelegateStatusReasonLegal,
		DelegateStatusReasonVerifierReview, DelegateStatusReasonOther:
		return nil
	default:
		return errorsmod.Wrapf(ErrInvalidField, "reason must be 1, 2, 3, 4 or 99, got %d", msg.Reason)
	}
}

// requireCreator returns codeErr unless creator is a bech32 account address.
func requireCreator(creator string, codeErr *errorsmod.Error) error {
	if _, err := sdk.AccAddressFromBech32(creator); err != nil {
		return errorsmod.Wrapf(codeErr, "creator %q is not a valid bech32 address: %v", creator, err)
	}
	return nil
}

// requireBytes returns ErrInvalidField unless b is exactly n bytes.
func requireBytes(field string, b []byte, n int) error {
	if len(b) != n {
		return errorsmod.Wrapf(ErrInvalidField, "%s must be %d bytes, got %d", field, n, len(b))
	}
	return nil
}

// addUnique adds b to seen and reports whether it was absent.
func addUnique(seen map[string]struct{}, b []byte) bool {
	if _, ok := seen[string(b)]; ok {
		return false
	}
	seen[string(b)] = struct{}{}
	return true
}

// --- VoteMessage interface implementations ---

// GetNullifiers returns nil: a 0x09 batch spends three separately scoped
// nullifier families (gov, VAN and slot), which its own checks handle, as for
// MsgDelegateAndCastVoteBatch.
func (msg *MsgProxyDelegationBatch) GetNullifiers() [][]byte { return nil }

// GetNullifierType is unused; see GetNullifiers.
func (msg *MsgProxyDelegationBatch) GetNullifierType() NullifierType { return 0 }

// AcceptsTallyingRound returns false: DCs need an ACTIVE round.
func (msg *MsgProxyDelegationBatch) AcceptsTallyingRound() bool { return false }

// GetVoteRoundId returns nil. Only a ballot names a round, and it checks the
// round in its own order (contracts-v1 §5.3), not through the generic round
// check.
func (msg *MsgDelegateOp) GetVoteRoundId() []byte { return nil }

// GetNullifiers returns nil: delegate ops spend no nullifiers.
func (msg *MsgDelegateOp) GetNullifiers() [][]byte { return nil }

// GetNullifierType is unused: delegate ops spend no nullifiers.
func (msg *MsgDelegateOp) GetNullifierType() NullifierType { return 0 }

// AcceptsTallyingRound returns false: a ballot needs an ACTIVE round.
func (msg *MsgDelegateOp) AcceptsTallyingRound() bool { return false }
