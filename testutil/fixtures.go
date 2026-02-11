// Package testutil provides shared test infrastructure for the Zally chain
// integration tests. It includes reusable message constructors, raw tx encoding
// helpers, and a TestApp that wraps ZallyApp for in-process ABCI testing.
package testutil

import (
	"bytes"
	"time"

	"github.com/z-cale/zally/x/vote/types"
)

// ValidSetupRound returns a MsgSetupVoteRound with all fields populated.
// The VoteEndTime is set 1 hour in the future from the reference time.
func ValidSetupRound() *types.MsgSetupVoteRound {
	return &types.MsgSetupVoteRound{
		Creator:           "zvote1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
		VoteEndTime:       uint64(time.Now().Add(1 * time.Hour).Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0xCC}, 32),
		NcRoot:            bytes.Repeat([]byte{0xDD}, 32),
	}
}

// ValidSetupRoundAt returns a MsgSetupVoteRound with VoteEndTime set relative
// to the given reference time. Use this when the block time is deterministic.
func ValidSetupRoundAt(refTime time.Time) *types.MsgSetupVoteRound {
	return &types.MsgSetupVoteRound{
		Creator:           "zvote1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
		VoteEndTime:       uint64(refTime.Add(1 * time.Hour).Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0xCC}, 32),
		NcRoot:            bytes.Repeat([]byte{0xDD}, 32),
	}
}

// ExpiredSetupRoundAt returns a MsgSetupVoteRound with VoteEndTime in the past
// relative to the given reference time.
func ExpiredSetupRoundAt(refTime time.Time) *types.MsgSetupVoteRound {
	return &types.MsgSetupVoteRound{
		Creator:           "zvote1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
		VoteEndTime:       uint64(refTime.Add(-1 * time.Hour).Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0xCC}, 32),
		NcRoot:            bytes.Repeat([]byte{0xDD}, 32),
	}
}

// ValidDelegation returns a MsgRegisterDelegation with mock proof data.
// Each call returns unique gov nullifiers derived from the provided seed.
func ValidDelegation(roundID []byte, nullifierSeed byte) *types.MsgRegisterDelegation {
	return &types.MsgRegisterDelegation{
		Rk:                  bytes.Repeat([]byte{0x01}, 32),
		SpendAuthSig:        bytes.Repeat([]byte{0x02}, 64),
		SignedNoteNullifier: bytes.Repeat([]byte{0x03}, 32),
		CmxNew:              bytes.Repeat([]byte{nullifierSeed + 0x80}, 32),
		EncMemo:             bytes.Repeat([]byte{0x05}, 64),
		GovComm:             bytes.Repeat([]byte{nullifierSeed + 0x90}, 32),
		GovNullifiers: [][]byte{
			MakeNullifier(nullifierSeed),
			MakeNullifier(nullifierSeed + 1),
		},
		Proof:       []byte("mock-delegation-proof"),
		VoteRoundId: roundID,
	}
}

// ValidVoteCommitment returns a MsgCreateVoteCommitment with mock data.
func ValidVoteCommitment(roundID []byte, anchorHeight uint64, nullifierSeed byte) *types.MsgCreateVoteCommitment {
	return &types.MsgCreateVoteCommitment{
		VanNullifier:             MakeNullifier(nullifierSeed),
		VoteAuthorityNoteNew:     bytes.Repeat([]byte{nullifierSeed + 0xA0}, 32),
		VoteCommitment:           bytes.Repeat([]byte{nullifierSeed + 0xB0}, 32),
		ProposalId:               1,
		Proof:                    []byte("mock-vote-commitment-proof"),
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
	}
}

// ValidRevealShare returns a MsgRevealVoteShare with mock data.
func ValidRevealShare(roundID []byte, anchorHeight uint64, nullifierSeed byte) *types.MsgRevealVoteShare {
	return &types.MsgRevealVoteShare{
		ShareNullifier:           MakeNullifier(nullifierSeed),
		VoteAmount:               1000,
		ProposalId:               1,
		VoteDecision:             1, // "yes"
		Proof:                    []byte("mock-reveal-share-proof"),
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
	}
}

// MakeNullifier creates a deterministic 32-byte nullifier from a seed byte.
func MakeNullifier(seed byte) []byte {
	return bytes.Repeat([]byte{seed}, 32)
}
