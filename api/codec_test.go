package api

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/z-cale/zally/x/vote/types"
)

func TestIsVoteTag(t *testing.T) {
	require.False(t, IsVoteTag(TagCreateVotingSession))
	require.True(t, IsVoteTag(TagDelegateVote))
	require.True(t, IsVoteTag(TagCastVote))
	require.True(t, IsVoteTag(TagRevealShare))
	require.True(t, IsVoteTag(TagSubmitTally))
	require.False(t, IsVoteTag(0x00))
	require.False(t, IsVoteTag(0x06))
	require.False(t, IsVoteTag(0x0a)) // Typical Cosmos Tx first byte
	require.False(t, IsVoteTag(0xff))
}

func TestEncodeDecodeDelegateVote(t *testing.T) {
	msg := &types.MsgDelegateVote{
		Rk:                  make([]byte, 32),
		SpendAuthSig:        []byte("sig"),
		SignedNoteNullifier: []byte("nullifier"),
		CmxNew:              []byte("cmx"),
		VanCmx:              []byte("comm"),
		GovNullifiers:       [][]byte{[]byte("nf1"), []byte("nf2")},
		Proof:               []byte("proof"),
		VoteRoundId:         []byte("roundid"),
		Sighash:             make([]byte, 32),
	}

	raw, err := EncodeVoteTx(msg)
	require.NoError(t, err)
	require.Equal(t, TagDelegateVote, raw[0])

	tag, decoded, err := DecodeVoteTx(raw)
	require.NoError(t, err)
	require.Equal(t, TagDelegateVote, tag)

	decodedMsg, ok := decoded.(*types.MsgDelegateVote)
	require.True(t, ok)
	require.Equal(t, msg.Rk, decodedMsg.Rk)
	require.Equal(t, msg.Sighash, decodedMsg.Sighash)
	require.Equal(t, len(msg.GovNullifiers), len(decodedMsg.GovNullifiers))
}

func TestEncodeDecodeCastVote(t *testing.T) {
	msg := &types.MsgCastVote{
		VanNullifier:             bytes.Repeat([]byte("van"), 11)[:32], // 32 bytes for Fp
		RVpkX:                    bytes.Repeat([]byte{0x01}, 32),
		RVpkY:                    bytes.Repeat([]byte{0x02}, 32),
		VoteAuthorityNoteNew:     []byte("note"),
		VoteCommitment:           []byte("commitment"),
		ProposalId:               1,
		Proof:                    []byte("proof"),
		VoteRoundId:              []byte("roundid"),
		VoteCommTreeAnchorHeight: 50,
	}

	raw, err := EncodeVoteTx(msg)
	require.NoError(t, err)
	require.Equal(t, TagCastVote, raw[0])

	tag, decoded, err := DecodeVoteTx(raw)
	require.NoError(t, err)
	require.Equal(t, TagCastVote, tag)

	decodedMsg, ok := decoded.(*types.MsgCastVote)
	require.True(t, ok)
	require.Equal(t, msg.ProposalId, decodedMsg.ProposalId)
	require.Equal(t, msg.VoteCommTreeAnchorHeight, decodedMsg.VoteCommTreeAnchorHeight)
}

func TestEncodeDecodeRevealShare(t *testing.T) {
	encShare := make([]byte, 64)
	for i := range encShare {
		encShare[i] = 0xAA
	}
	msg := &types.MsgRevealShare{
		ShareNullifier:           []byte("share"),
		EncShare:                 encShare,
		ProposalId:               2,
		VoteDecision:             1,
		Proof:                    []byte("proof"),
		VoteRoundId:              []byte("roundid"),
		VoteCommTreeAnchorHeight: 50,
	}

	raw, err := EncodeVoteTx(msg)
	require.NoError(t, err)
	require.Equal(t, TagRevealShare, raw[0])

	tag, decoded, err := DecodeVoteTx(raw)
	require.NoError(t, err)
	require.Equal(t, TagRevealShare, tag)

	decodedMsg, ok := decoded.(*types.MsgRevealShare)
	require.True(t, ok)
	require.Equal(t, msg.EncShare, decodedMsg.EncShare)
	require.Equal(t, msg.VoteDecision, decodedMsg.VoteDecision)
}

func TestEncodeDecodeSubmitTally(t *testing.T) {
	msg := &types.MsgSubmitTally{
		VoteRoundId: []byte("roundid12345678901234567890123456"),
		Creator:     "zvote1admin",
		Entries: []*types.TallyEntry{
			{ProposalId: 0, VoteDecision: 1, TotalValue: 500},
			{ProposalId: 1, VoteDecision: 0, TotalValue: 200},
		},
	}

	raw, err := EncodeVoteTx(msg)
	require.NoError(t, err)
	require.Equal(t, TagSubmitTally, raw[0])

	tag, decoded, err := DecodeVoteTx(raw)
	require.NoError(t, err)
	require.Equal(t, TagSubmitTally, tag)

	decodedMsg, ok := decoded.(*types.MsgSubmitTally)
	require.True(t, ok)
	require.Equal(t, msg.VoteRoundId, decodedMsg.VoteRoundId)
	require.Equal(t, msg.Creator, decodedMsg.Creator)
	require.Len(t, decodedMsg.Entries, 2)
	require.Equal(t, uint32(0), decodedMsg.Entries[0].ProposalId)
	require.Equal(t, uint32(1), decodedMsg.Entries[0].VoteDecision)
	require.Equal(t, uint64(500), decodedMsg.Entries[0].TotalValue)
	require.Equal(t, uint32(1), decodedMsg.Entries[1].ProposalId)
	require.Equal(t, uint32(0), decodedMsg.Entries[1].VoteDecision)
	require.Equal(t, uint64(200), decodedMsg.Entries[1].TotalValue)
}

func TestIsCeremonyTag(t *testing.T) {
	// Only MsgAckExecutiveAuthorityKey (0x08) uses the custom wire format.
	require.True(t, IsCeremonyTag(TagAckExecutiveAuthorityKey))

	// All other ceremony tags now use standard Cosmos SDK transactions.
	require.False(t, IsCeremonyTag(TagRegisterPallasKey))
	require.False(t, IsCeremonyTag(TagDealExecutiveAuthorityKey))
	require.False(t, IsCeremonyTag(TagCreateValidatorWithPallasKey))
	require.False(t, IsCeremonyTag(TagSetVoteManager))
	require.False(t, IsCeremonyTag(0x00))
	require.False(t, IsCeremonyTag(0x0A)) // reserved: collides with Cosmos Tx protobuf
	require.False(t, IsCeremonyTag(0x01)) // vote tag, not ceremony
}

func TestEncodeDecodeAckExecutiveAuthorityKey(t *testing.T) {
	msg := &types.MsgAckExecutiveAuthorityKey{
		Creator:      "zvotevaloper1val",
		AckSignature: []byte("signature"),
	}

	raw, err := EncodeCeremonyTx(msg, TagAckExecutiveAuthorityKey)
	require.NoError(t, err)
	require.Equal(t, TagAckExecutiveAuthorityKey, raw[0])

	tag, decoded, err := DecodeCeremonyTx(raw)
	require.NoError(t, err)
	require.Equal(t, TagAckExecutiveAuthorityKey, tag)

	decodedMsg, ok := decoded.(*types.MsgAckExecutiveAuthorityKey)
	require.True(t, ok)
	require.Equal(t, msg.Creator, decodedMsg.Creator)
	require.Equal(t, msg.AckSignature, decodedMsg.AckSignature)
}

func TestEncodeCeremonyTx_RejectsNonAckTags(t *testing.T) {
	msg := &types.MsgSetVoteManager{
		Creator:    "zvote1admin",
		NewManager: "zvote1manager",
	}

	// Non-ack ceremony tags should be rejected since they now use standard Cosmos txs.
	_, err := EncodeCeremonyTx(msg, TagSetVoteManager)
	require.Error(t, err)
	require.Contains(t, err.Error(), "only 0x08 uses custom wire format")
}

func TestDecodeVoteTx_TooShort(t *testing.T) {
	_, _, err := DecodeVoteTx(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "too short")

	_, _, err = DecodeVoteTx([]byte{0x01})
	require.Error(t, err)
	require.Contains(t, err.Error(), "too short")
}

func TestDecodeVoteTx_InvalidTag(t *testing.T) {
	_, _, err := DecodeVoteTx([]byte{0x00, 0x00})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid vote tx tag")

	_, _, err = DecodeVoteTx([]byte{0x06, 0x00})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid vote tx tag")
}

func TestTagForMessage(t *testing.T) {
	tests := []struct {
		msg       types.VoteMessage
		tag       byte
		name      string
		expectErr bool
	}{
		{&types.MsgCreateVotingSession{}, TagCreateVotingSession, "CreateVotingSession", true},
		{&types.MsgDelegateVote{}, TagDelegateVote, "DelegateVote", false},
		{&types.MsgCastVote{}, TagCastVote, "CastVote", false},
		{&types.MsgRevealShare{}, TagRevealShare, "RevealShare", false},
		{&types.MsgSubmitTally{}, TagSubmitTally, "SubmitTally", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag, err := TagForMessage(tt.msg)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.tag, tag)
		})
	}
}
