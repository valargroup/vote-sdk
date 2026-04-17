package api

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

func TestIsVoteTag(t *testing.T) {
	require.True(t, IsVoteTag(TagDelegateVote))
	require.True(t, IsVoteTag(TagCastVote))
	require.True(t, IsVoteTag(TagRevealShare))
	require.True(t, IsVoteTag(TagSubmitTally))
	require.False(t, IsVoteTag(0x00))
	require.False(t, IsVoteTag(0x01)) // MsgCreateVotingSession — standard Cosmos Tx
	require.False(t, IsVoteTag(0x06)) // MsgRegisterPallasKey — standard Cosmos Tx
	require.False(t, IsVoteTag(0x0a)) // reserved: collides with Cosmos Tx protobuf
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
		Creator:     "sv1admin",
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
	require.True(t, IsCeremonyTag(TagContributeDKG))
	require.True(t, IsCeremonyTag(TagAckExecutiveAuthorityKey))
	require.True(t, IsCeremonyTag(TagSubmitPartialDecryption))

	// Standard Cosmos Tx ceremony messages are not ceremony tags.
	require.False(t, IsCeremonyTag(0x06)) // MsgRegisterPallasKey
	require.False(t, IsCeremonyTag(0x09)) // MsgCreateValidatorWithPallasKey
	require.False(t, IsCeremonyTag(0x0C)) // MsgUpdateVoteManagers
	require.False(t, IsCeremonyTag(0x00))
	require.False(t, IsCeremonyTag(0x0A)) // reserved: collides with Cosmos Tx protobuf
	require.False(t, IsCeremonyTag(0x01)) // MsgCreateVotingSession — standard Cosmos Tx
}

func TestEncodeDecodeAckExecutiveAuthorityKey(t *testing.T) {
	msg := &types.MsgAckExecutiveAuthorityKey{
		Creator:      "svvaloper1val",
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

func TestEncodeDecodeContributeDKG(t *testing.T) {
	msg := &types.MsgContributeDKG{
		Creator:     "svvaloper1val",
		VoteRoundId: bytes.Repeat([]byte{0xAA}, 32),
	}

	raw, err := EncodeCeremonyTx(msg, TagContributeDKG)
	require.NoError(t, err)
	require.Equal(t, TagContributeDKG, raw[0])

	tag, decoded, err := DecodeCeremonyTx(raw)
	require.NoError(t, err)
	require.Equal(t, TagContributeDKG, tag)

	decodedMsg, ok := decoded.(*types.MsgContributeDKG)
	require.True(t, ok)
	require.Equal(t, msg.Creator, decodedMsg.Creator)
	require.Equal(t, msg.VoteRoundId, decodedMsg.VoteRoundId)
}

func TestEncodeCeremonyTx_RejectsNonCustomTags(t *testing.T) {
	msg := &types.MsgUpdateVoteManagers{
		Creator:   "sv1admin",
		NewVoteManagers: []string{"sv1manager"},
	}

	// MsgUpdateVoteManagers (0x0C) uses the standard Cosmos Tx path — EncodeCeremonyTx must reject it.
	_, err := EncodeCeremonyTx(msg, 0x0C)
	require.Error(t, err)
	require.Contains(t, err.Error(), "only 0x08, 0x0D, 0x0E use custom wire format")
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
		msg  types.VoteMessage
		tag  byte
		name string
	}{
		{&types.MsgDelegateVote{}, TagDelegateVote, "DelegateVote"},
		{&types.MsgCastVote{}, TagCastVote, "CastVote"},
		{&types.MsgRevealShare{}, TagRevealShare, "RevealShare"},
		{&types.MsgSubmitTally{}, TagSubmitTally, "SubmitTally"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag, err := TagForMessage(tt.msg)
			require.NoError(t, err)
			require.Equal(t, tt.tag, tag)
		})
	}
}
