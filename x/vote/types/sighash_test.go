package types

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func frozenBatchVote(fill byte, proposalID uint32) *MsgCastVote {
	return &MsgCastVote{
		RVpk:                     bytes.Repeat([]byte{fill}, 32),
		VanNullifier:             bytes.Repeat([]byte{fill + 1}, 32),
		VoteAuthorityNoteNew:     bytes.Repeat([]byte{fill + 2}, 32),
		VoteCommitment:           bytes.Repeat([]byte{fill + 3}, 32),
		ProposalId:               proposalID,
		VoteRoundId:              bytes.Repeat([]byte{1}, 32),
		VoteCommTreeAnchorHeight: 0x0102030405060708,
	}
}

func TestComputeCastVoteBatchSighashFrozenVector(t *testing.T) {
	batch := &MsgCastVoteBatch{Votes: []*MsgCastVote{
		frozenBatchVote(2, 1),
		frozenBatchVote(6, 15),
	}}

	got := hex.EncodeToString(ComputeCastVoteBatchSighash(batch))
	const want = "7381e034bee32634f6983f851d0aeaea39110725be97fa3b43973e358b7ce3db"
	if got != want {
		t.Fatalf("batch digest mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestComputeCastVoteBatchSighashBindsWholeOrderedBatch(t *testing.T) {
	first := frozenBatchVote(2, 1)
	second := frozenBatchVote(6, 15)
	batch := &MsgCastVoteBatch{Votes: []*MsgCastVote{first, second}}
	base := ComputeCastVoteBatchSighash(batch)

	reordered := &MsgCastVoteBatch{Votes: []*MsgCastVote{second, first}}
	if bytes.Equal(base, ComputeCastVoteBatchSighash(reordered)) {
		t.Fatal("reordering actions must change the digest")
	}
	truncated := &MsgCastVoteBatch{Votes: []*MsgCastVote{first}}
	if bytes.Equal(base, ComputeCastVoteBatchSighash(truncated)) {
		t.Fatal("truncating actions must change the digest")
	}
	grafted := &MsgCastVoteBatch{Votes: []*MsgCastVote{first, second, frozenBatchVote(10, 7)}}
	if bytes.Equal(base, ComputeCastVoteBatchSighash(grafted)) {
		t.Fatal("grafting an action must change the digest")
	}

	first.Proof = []byte("replacement valid proof bytes")
	first.VoteAuthSig = bytes.Repeat([]byte{0xff}, 64)
	if !bytes.Equal(base, ComputeCastVoteBatchSighash(batch)) {
		t.Fatal("proof and signature bytes are not effecting fields")
	}
}

func frozenDelegateAndCastVoteBatch() *MsgDelegateAndCastVoteBatch {
	first := frozenBatchVote(2, 1)
	second := frozenBatchVote(6, 2)
	first.VoteCommTreeAnchorHeight = 0
	second.VoteCommTreeAnchorHeight = 0
	return &MsgDelegateAndCastVoteBatch{
		Delegation: &MsgDelegateVote{
			VoteRoundId: bytes.Repeat([]byte{1}, 32),
			VanCmx:      bytes.Repeat([]byte{9}, 32),
		},
		Batch: &MsgCastVoteBatch{Votes: []*MsgCastVote{first, second}},
	}
}

func TestComputeDelegateAndCastVoteBatchSighashFrozenVector(t *testing.T) {
	got := hex.EncodeToString(ComputeDelegateAndCastVoteBatchSighash(frozenDelegateAndCastVoteBatch()))
	const want = "1b884143da3d43cd2834a1f347c60d76b2d9a5b0ba5da6f91a4b2b09511f6e23"
	if got != want {
		t.Fatalf("composite digest mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestComputeDelegateAndCastVoteBatchSighashBindsWholeTransaction(t *testing.T) {
	base := ComputeDelegateAndCastVoteBatchSighash(frozenDelegateAndCastVoteBatch())
	tests := []struct {
		name string
		edit func(*MsgDelegateAndCastVoteBatch)
	}{
		{"round", func(msg *MsgDelegateAndCastVoteBatch) { msg.Delegation.VoteRoundId[0] ^= 1 }},
		{"delegation VAN", func(msg *MsgDelegateAndCastVoteBatch) { msg.Delegation.VanCmx[0] ^= 1 }},
		{"order", func(msg *MsgDelegateAndCastVoteBatch) {
			msg.Batch.Votes[0], msg.Batch.Votes[1] = msg.Batch.Votes[1], msg.Batch.Votes[0]
		}},
		{"length", func(msg *MsgDelegateAndCastVoteBatch) { msg.Batch.Votes = msg.Batch.Votes[:1] }},
		{"r_vpk", func(msg *MsgDelegateAndCastVoteBatch) { msg.Batch.Votes[0].RVpk[0] ^= 1 }},
		{"VAN nullifier", func(msg *MsgDelegateAndCastVoteBatch) { msg.Batch.Votes[0].VanNullifier[0] ^= 1 }},
		{"successor VAN", func(msg *MsgDelegateAndCastVoteBatch) { msg.Batch.Votes[0].VoteAuthorityNoteNew[0] ^= 1 }},
		{"vote commitment", func(msg *MsgDelegateAndCastVoteBatch) { msg.Batch.Votes[0].VoteCommitment[0] ^= 1 }},
		{"proposal", func(msg *MsgDelegateAndCastVoteBatch) { msg.Batch.Votes[0].ProposalId++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			msg := frozenDelegateAndCastVoteBatch()
			test.edit(msg)
			if bytes.Equal(base, ComputeDelegateAndCastVoteBatchSighash(msg)) {
				t.Fatalf("changing %s must change the digest", test.name)
			}
		})
	}
}
