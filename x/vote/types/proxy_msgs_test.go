package types_test

import (
	"bytes"
	"testing"

	errorsmod "cosmossdk.io/errors"
	"github.com/stretchr/testify/require"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// proxyTestRoundID is the round of the proxy delegation message fixtures.
var proxyTestRoundID = bytes.Repeat([]byte{0x01}, 32)

// validProxyDC returns a DC whose byte fields are derived from seed, so DCs
// with seeds at least 8 apart share no value.
func validProxyDC(seed byte) *types.ProxyDelegationAction {
	return &types.ProxyDelegationAction{
		VanNullifier:              bytes.Repeat([]byte{seed}, 32),
		RVpk:                      append([]byte(nil), svtest.DummyPallasPoint...),
		VoteAuthorityNoteNew:      bytes.Repeat([]byte{seed + 1}, 32),
		ProxyDelegationCommitment: bytes.Repeat([]byte{seed + 2}, 32),
		DcSlotNullifier:           bytes.Repeat([]byte{seed + 3}, 32),
		Proof:                     []byte("mock-zkp4-proof"),
		VoteAuthSig:               bytes.Repeat([]byte{seed + 4}, 64),
	}
}

// validProxyBatch returns a valid two-DC batch, anchored to height 42 or,
// with registration, carrying a ZKP1 registration at anchor 0.
func validProxyBatch(registration bool) *types.MsgProxyDelegationBatch {
	msg := &types.MsgProxyDelegationBatch{
		VoteRoundId:              append([]byte(nil), proxyTestRoundID...),
		VoteCommTreeAnchorHeight: 42,
		Dcs:                      []*types.ProxyDelegationAction{validProxyDC(0x10), validProxyDC(0x20)},
		ExpiryHeight:             1_000,
	}
	if registration {
		msg.VoteCommTreeAnchorHeight = 0
		msg.Registration = svtest.ValidDelegation(msg.VoteRoundId, 0x30)
	}
	return msg
}

// validDelegateRegister returns a valid registration of an X account.
func validDelegateRegister() *types.DelegateRegister {
	return &types.DelegateRegister{
		Dk:       bytes.Repeat([]byte{0x41}, 32),
		Provider: types.DelegateProviderX,
		Handle:   "alice_01",
		ProofId:  "1790000000000000000",
		Attestations: []*types.DelegateAttestation{{
			VerifierId: 1,
			NotBefore:  1_790_000_000,
			ExpiresAt:  1_790_000_000 + types.MaxDelegateAttestationSeconds,
			Sig:        bytes.Repeat([]byte{0x43}, 64),
		}},
		DkSig: bytes.Repeat([]byte{0x44}, 64),
	}
}

// validDelegateRetire returns a valid retirement.
func validDelegateRetire() *types.DelegateRetire {
	return &types.DelegateRetire{
		DelegateIndex: 7,
		DkSig:         bytes.Repeat([]byte{0x52}, 64),
	}
}

// validDelegateBallot returns a valid ballot on a two-proposal round.
func validDelegateBallot() *types.DelegateBallot {
	return &types.DelegateBallot{
		VoteRoundId:   append([]byte(nil), proxyTestRoundID...),
		DelegateIndex: 7,
		Options:       []uint32{0, 1},
		DkSig:         bytes.Repeat([]byte{0x61}, 64),
	}
}

// requireInvalidField fails unless err is ErrInvalidField whose message
// contains want, and the registered code 9 in codespace vote surfaces through
// the wrapping (contracts-v1 §0).
func requireInvalidField(t *testing.T, err error, want string) {
	t.Helper()
	require.ErrorIs(t, err, types.ErrInvalidField)
	require.ErrorContains(t, err, want)
	codespace, code, _ := errorsmod.ABCIInfo(err, false)
	require.Equal(t, types.ModuleName, codespace)
	require.Equal(t, uint32(9), code)
}

func TestMsgProxyDelegationBatchValidateBasic(t *testing.T) {
	require.NoError(t, validProxyBatch(false).ValidateBasic())
	require.NoError(t, validProxyBatch(true).ValidateBasic())

	full := validProxyBatch(false)
	full.Dcs = nil
	for i := range types.MaxProxyDelegationDCs {
		full.Dcs = append(full.Dcs, validProxyDC(byte(0x10+8*i)))
	}
	require.NoError(t, full.ValidateBasic(), "eight DCs fill every slot")

	tests := []struct {
		name         string
		registration bool
		mutate       func(*types.MsgProxyDelegationBatch)
		want         string
	}{
		{"short round id", false, func(m *types.MsgProxyDelegationBatch) { m.VoteRoundId = m.VoteRoundId[:31] }, "vote_round_id must be 32 bytes"},
		{"no dcs", false, func(m *types.MsgProxyDelegationBatch) { m.Dcs = nil }, "dcs count must be between 1 and 8, got 0"},
		{"nine dcs", false, func(m *types.MsgProxyDelegationBatch) {
			for i := 2; i < 9; i++ {
				m.Dcs = append(m.Dcs, validProxyDC(byte(0x30+8*i)))
			}
		}, "got 9"},
		{"zero expiry", false, func(m *types.MsgProxyDelegationBatch) { m.ExpiryHeight = 0 }, "expiry_height cannot be zero"},
		{"anchor zero without registration", false, func(m *types.MsgProxyDelegationBatch) { m.VoteCommTreeAnchorHeight = 0 }, "if and only if"},
		{"registration with anchor", true, func(m *types.MsgProxyDelegationBatch) { m.VoteCommTreeAnchorHeight = 42 }, "if and only if"},
		{"invalid registration", true, func(m *types.MsgProxyDelegationBatch) { m.Registration.Rk = nil }, "registration: invalid message field: rk must be 32 bytes"},
		{"registration in another round", true, func(m *types.MsgProxyDelegationBatch) {
			m.Registration = svtest.ValidDelegation(bytes.Repeat([]byte{0x02}, 32), 0x30)
		}, "registration has a different vote_round_id"},
		{"short van nullifier", false, func(m *types.MsgProxyDelegationBatch) { m.Dcs[0].VanNullifier = m.Dcs[0].VanNullifier[:31] }, "dcs[0]: van_nullifier must be 32 bytes"},
		{"identity r_vpk", false, func(m *types.MsgProxyDelegationBatch) { m.Dcs[1].RVpk = make([]byte, 32) }, "dcs[1]: r_vpk must not be the identity point"},
		{"oversized proof", false, func(m *types.MsgProxyDelegationBatch) { m.Dcs[0].Proof = make([]byte, types.MaxProofSize+1) }, "got 15361"},
		{"short signature", false, func(m *types.MsgProxyDelegationBatch) { m.Dcs[0].VoteAuthSig = m.Dcs[0].VoteAuthSig[:63] }, "vote_auth_sig must be 64 bytes"},
		{"repeated van nullifier", false, func(m *types.MsgProxyDelegationBatch) { m.Dcs[1].VanNullifier = m.Dcs[0].VanNullifier }, "dcs[1] repeats a van_nullifier"},
		{"repeated slot nullifier", false, func(m *types.MsgProxyDelegationBatch) { m.Dcs[1].DcSlotNullifier = m.Dcs[0].DcSlotNullifier }, "dcs[1] repeats a dc_slot_nullifier"},
		{"repeated dc", false, func(m *types.MsgProxyDelegationBatch) {
			m.Dcs[1].ProxyDelegationCommitment = m.Dcs[0].ProxyDelegationCommitment
		}, "dcs[1] repeats a proxy_delegation_commitment"},
		{"final van equals a dc", false, func(m *types.MsgProxyDelegationBatch) {
			m.Dcs[1].VoteAuthorityNoteNew = m.Dcs[0].ProxyDelegationCommitment
		}, "the final vote_authority_note_new equals a proxy_delegation_commitment"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := validProxyBatch(tc.registration)
			tc.mutate(msg)
			requireInvalidField(t, msg.ValidateBasic(), tc.want)
		})
	}

	// Only the final successor VAN becomes a leaf, so an intermediate one
	// may equal a DC.
	msg := validProxyBatch(false)
	msg.Dcs[0].VoteAuthorityNoteNew = msg.Dcs[1].ProxyDelegationCommitment
	require.NoError(t, msg.ValidateBasic())
}

func TestMsgDelegateOpValidateBasic(t *testing.T) {
	fullBallot := validDelegateBallot()
	fullBallot.Options = make([]uint32, types.MaxProposals)
	for _, op := range []*types.MsgDelegateOp{
		{Register: validDelegateRegister()},
		{Retire: validDelegateRetire()},
		{Ballot: validDelegateBallot()},
		{Ballot: fullBallot},
	} {
		require.NoError(t, op.ValidateBasic())
	}

	requireInvalidField(t, (&types.MsgDelegateOp{}).ValidateBasic(), "exactly one of register, retire and ballot must be set, got 0")
	requireInvalidField(t, (&types.MsgDelegateOp{Retire: validDelegateRetire(), Ballot: validDelegateBallot()}).ValidateBasic(), "got 2")

	registerTests := []struct {
		name   string
		mutate func(*types.DelegateRegister)
		want   string
	}{
		{"short dk", func(r *types.DelegateRegister) { r.Dk = r.Dk[:31] }, "register: dk must be 32 bytes"},
		{"provider three", func(r *types.DelegateRegister) { r.Provider = 3 }, "provider must be 1 or 2, got 3"},
		{"uppercase handle", func(r *types.DelegateRegister) { r.Handle = "Alice" }, `handle "Alice" is not canonical`},
		{"bad proof id", func(r *types.DelegateRegister) { r.ProofId = "0179" }, `proof_id "0179" has a leading zero`},
		{"no attestations", func(r *types.DelegateRegister) { r.Attestations = nil }, "attestations count must be between 1 and 16, got 0"},
		{"seventeen attestations", func(r *types.DelegateRegister) {
			for len(r.Attestations) < 17 {
				r.Attestations = append(r.Attestations, r.Attestations[0])
			}
		}, "got 17"},
		{"short attestation sig", func(r *types.DelegateRegister) { r.Attestations[0].Sig = r.Attestations[0].Sig[:63] }, "attestations[0]: sig must be 64 bytes"},
		{"empty window", func(r *types.DelegateRegister) { r.Attestations[0].ExpiresAt = r.Attestations[0].NotBefore }, "attestations[0] needs not_before < expires_at"},
		{"window over 72 hours", func(r *types.DelegateRegister) { r.Attestations[0].ExpiresAt++ }, "<= not_before + 259200"},
		{"short dk sig", func(r *types.DelegateRegister) { r.DkSig = nil }, "dk_sig must be 64 bytes"},
	}
	for _, tc := range registerTests {
		t.Run("register/"+tc.name, func(t *testing.T) {
			register := validDelegateRegister()
			tc.mutate(register)
			requireInvalidField(t, (&types.MsgDelegateOp{Register: register}).ValidateBasic(), tc.want)
		})
	}

	retireTests := []struct {
		name   string
		mutate func(*types.DelegateRetire)
		want   string
	}{
		{"index zero", func(r *types.DelegateRetire) { r.DelegateIndex = 0 }, "retire: delegate_index must be at least 1"},
		{"long dk sig", func(r *types.DelegateRetire) { r.DkSig = append(r.DkSig, 0) }, "dk_sig must be 64 bytes"},
	}
	for _, tc := range retireTests {
		t.Run("retire/"+tc.name, func(t *testing.T) {
			retire := validDelegateRetire()
			tc.mutate(retire)
			requireInvalidField(t, (&types.MsgDelegateOp{Retire: retire}).ValidateBasic(), tc.want)
		})
	}

	ballotTests := []struct {
		name   string
		mutate func(*types.DelegateBallot)
		want   string
	}{
		{"no options", func(b *types.DelegateBallot) { b.Options = nil }, "options count must be between 1 and 50, got 0"},
		{"too many options", func(b *types.DelegateBallot) { b.Options = make([]uint32, types.MaxProposals+1) }, "got 51"},
		{"short dk sig", func(b *types.DelegateBallot) { b.DkSig = nil }, "dk_sig must be 64 bytes"},
	}
	for _, tc := range ballotTests {
		t.Run("ballot/"+tc.name, func(t *testing.T) {
			ballot := validDelegateBallot()
			tc.mutate(ballot)
			requireInvalidField(t, (&types.MsgDelegateOp{Ballot: ballot}).ValidateBasic(), tc.want)
		})
	}
}

// TestProxyDelegationMsgsAsVoteMessages pins the VoteMessage answers that keep
// the new Msgs out of the generic round and nullifier checks (contracts-v1
// §5.1, §5.3).
func TestProxyDelegationMsgsAsVoteMessages(t *testing.T) {
	batch := validProxyBatch(true)
	require.Equal(t, batch.VoteRoundId, batch.GetVoteRoundId())
	require.Nil(t, batch.GetNullifiers())
	require.False(t, batch.AcceptsTallyingRound())

	op := &types.MsgDelegateOp{Ballot: validDelegateBallot()}
	require.Nil(t, op.GetVoteRoundId())
	require.Nil(t, op.GetNullifiers())
	require.False(t, op.AcceptsTallyingRound())
}
