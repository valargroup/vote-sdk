package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/z-cale/zally/x/vote/types"
)

func TestIsVoteTag(t *testing.T) {
	require.True(t, IsVoteTag(TagSetupVoteRound))
	require.True(t, IsVoteTag(TagRegisterDelegation))
	require.True(t, IsVoteTag(TagCreateVoteCommitment))
	require.True(t, IsVoteTag(TagRevealVoteShare))
	require.False(t, IsVoteTag(0x00))
	require.False(t, IsVoteTag(0x05))
	require.False(t, IsVoteTag(0x0a)) // Typical Cosmos Tx first byte
	require.False(t, IsVoteTag(0xff))
}

func TestEncodeDecodeSetupVoteRound(t *testing.T) {
	msg := &types.MsgSetupVoteRound{
		Creator:           "zvote1creator",
		SnapshotHeight:    100,
		SnapshotBlockhash: []byte("blockhash123456789012345678901234"),
		ProposalsHash:     []byte("prophash1234567890123456789012345"),
		VoteEndTime:       1700000000,
		NullifierImtRoot:  []byte("nullroot1234567890123456789012345"),
		NcRoot:            []byte("ncroot12345678901234567890123456"),
	}

	raw, err := EncodeVoteTx(msg)
	require.NoError(t, err)
	require.Equal(t, TagSetupVoteRound, raw[0])

	tag, decoded, err := DecodeVoteTx(raw)
	require.NoError(t, err)
	require.Equal(t, TagSetupVoteRound, tag)

	decodedMsg, ok := decoded.(*types.MsgSetupVoteRound)
	require.True(t, ok)
	require.Equal(t, msg.Creator, decodedMsg.Creator)
	require.Equal(t, msg.SnapshotHeight, decodedMsg.SnapshotHeight)
	require.Equal(t, msg.VoteEndTime, decodedMsg.VoteEndTime)
}

func TestEncodeDecodeRegisterDelegation(t *testing.T) {
	msg := &types.MsgRegisterDelegation{
		Rk:                  make([]byte, 32),
		SpendAuthSig:        []byte("sig"),
		SignedNoteNullifier: []byte("nullifier"),
		CmxNew:              []byte("cmx"),
		GovComm:             []byte("comm"),
		GovNullifiers:       [][]byte{[]byte("nf1"), []byte("nf2")},
		Proof:               []byte("proof"),
		VoteRoundId:         []byte("roundid"),
		Sighash:             make([]byte, 32),
	}

	raw, err := EncodeVoteTx(msg)
	require.NoError(t, err)
	require.Equal(t, TagRegisterDelegation, raw[0])

	tag, decoded, err := DecodeVoteTx(raw)
	require.NoError(t, err)
	require.Equal(t, TagRegisterDelegation, tag)

	decodedMsg, ok := decoded.(*types.MsgRegisterDelegation)
	require.True(t, ok)
	require.Equal(t, msg.Rk, decodedMsg.Rk)
	require.Equal(t, msg.Sighash, decodedMsg.Sighash)
	require.Equal(t, len(msg.GovNullifiers), len(decodedMsg.GovNullifiers))
}

func TestEncodeDecodeCreateVoteCommitment(t *testing.T) {
	msg := &types.MsgCreateVoteCommitment{
		VanNullifier:             []byte("van"),
		VoteAuthorityNoteNew:     []byte("note"),
		VoteCommitment:           []byte("commitment"),
		ProposalId:               1,
		Proof:                    []byte("proof"),
		VoteRoundId:              []byte("roundid"),
		VoteCommTreeAnchorHeight: 50,
	}

	raw, err := EncodeVoteTx(msg)
	require.NoError(t, err)
	require.Equal(t, TagCreateVoteCommitment, raw[0])

	tag, decoded, err := DecodeVoteTx(raw)
	require.NoError(t, err)
	require.Equal(t, TagCreateVoteCommitment, tag)

	decodedMsg, ok := decoded.(*types.MsgCreateVoteCommitment)
	require.True(t, ok)
	require.Equal(t, msg.ProposalId, decodedMsg.ProposalId)
	require.Equal(t, msg.VoteCommTreeAnchorHeight, decodedMsg.VoteCommTreeAnchorHeight)
}

func TestEncodeDecodeRevealVoteShare(t *testing.T) {
	msg := &types.MsgRevealVoteShare{
		ShareNullifier:           []byte("share"),
		VoteAmount:               1000,
		ProposalId:               2,
		VoteDecision:             1,
		Proof:                    []byte("proof"),
		VoteRoundId:              []byte("roundid"),
		VoteCommTreeAnchorHeight: 50,
	}

	raw, err := EncodeVoteTx(msg)
	require.NoError(t, err)
	require.Equal(t, TagRevealVoteShare, raw[0])

	tag, decoded, err := DecodeVoteTx(raw)
	require.NoError(t, err)
	require.Equal(t, TagRevealVoteShare, tag)

	decodedMsg, ok := decoded.(*types.MsgRevealVoteShare)
	require.True(t, ok)
	require.Equal(t, msg.VoteAmount, decodedMsg.VoteAmount)
	require.Equal(t, msg.VoteDecision, decodedMsg.VoteDecision)
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

	_, _, err = DecodeVoteTx([]byte{0x05, 0x00})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid vote tx tag")
}

func TestTagForMessage(t *testing.T) {
	tests := []struct {
		msg  types.VoteMessage
		tag  byte
		name string
	}{
		{&types.MsgSetupVoteRound{}, TagSetupVoteRound, "SetupVoteRound"},
		{&types.MsgRegisterDelegation{}, TagRegisterDelegation, "RegisterDelegation"},
		{&types.MsgCreateVoteCommitment{}, TagCreateVoteCommitment, "CreateVoteCommitment"},
		{&types.MsgRevealVoteShare{}, TagRevealVoteShare, "RevealVoteShare"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag, err := TagForMessage(tt.msg)
			require.NoError(t, err)
			require.Equal(t, tt.tag, tag)
		})
	}
}
