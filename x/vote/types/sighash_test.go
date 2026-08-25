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
