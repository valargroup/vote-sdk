// Package testutil provides shared test infrastructure for the Zally chain
// integration tests. It includes reusable message constructors, raw tx encoding
// helpers, and a TestApp that wraps ZallyApp for in-process ABCI testing.
package testutil

import (
	"bytes"
	"encoding/binary"
	"time"

	"github.com/z-cale/zally/x/vote/types"
)

// FpLE returns a 32-byte little-endian encoding of v as a Pallas Fp element.
// Values 0 <= v < 2^64 are always canonical. Use for commitment tree leaves in tests.
func FpLE(v uint64) []byte {
	buf := make([]byte, 32)
	binary.LittleEndian.PutUint64(buf[:8], v)
	return buf
}

// SampleProposals returns two sample proposals for test fixtures.
// Proposal IDs must be 1-indexed per ValidateBasic (expected 1, 2, ...).
func SampleProposals() []*types.Proposal {
	return []*types.Proposal{
		{Id: 1, Title: "Proposal A", Description: "First proposal"},
		{Id: 2, Title: "Proposal B", Description: "Second proposal"},
	}
}

// ValidCreateVotingSession returns a MsgCreateVotingSession with all fields populated.
// The VoteEndTime is set 1 hour in the future from the reference time.
func ValidCreateVotingSession() *types.MsgCreateVotingSession {
	return &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
		VoteEndTime:       uint64(time.Now().Add(1 * time.Hour).Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0xCC}, 32),
		NcRoot:            bytes.Repeat([]byte{0xDD}, 32),
		EaPk:              bytes.Repeat([]byte{0xEE}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         SampleProposals(),
	}
}

// ValidCreateVotingSessionAt returns a MsgCreateVotingSession with VoteEndTime set relative
// to the given reference time. Use this when the block time is deterministic.
func ValidCreateVotingSessionAt(refTime time.Time) *types.MsgCreateVotingSession {
	return &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
		VoteEndTime:       uint64(refTime.Add(1 * time.Hour).Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0xCC}, 32),
		NcRoot:            bytes.Repeat([]byte{0xDD}, 32),
		EaPk:              bytes.Repeat([]byte{0xEE}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         SampleProposals(),
	}
}

// ExpiredCreateVotingSessionAt returns a MsgCreateVotingSession with VoteEndTime in the past
// relative to the given reference time.
func ExpiredCreateVotingSessionAt(refTime time.Time) *types.MsgCreateVotingSession {
	return &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
		VoteEndTime:       uint64(refTime.Add(-1 * time.Hour).Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0xCC}, 32),
		NcRoot:            bytes.Repeat([]byte{0xDD}, 32),
		EaPk:              bytes.Repeat([]byte{0xEE}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         SampleProposals(),
	}
}

// ValidDelegation returns a MsgDelegateVote with mock proof data.
// Each call returns unique gov nullifiers derived from the provided seed.
// CmxNew and VanCmx use canonical Fp encodings so the commitment tree FFI accepts them.
// Sighash is set to the canonical ComputeDelegationSighash so ante validation passes.
func ValidDelegation(roundID []byte, nullifierSeed byte) *types.MsgDelegateVote {
	msg := &types.MsgDelegateVote{
		Rk:                  bytes.Repeat([]byte{0x01}, 32),
		SpendAuthSig:        bytes.Repeat([]byte{0x02}, 64),
		SignedNoteNullifier: bytes.Repeat([]byte{0x03}, 32),
		CmxNew:              FpLE(0x80 + uint64(nullifierSeed)),
		EncMemo:             bytes.Repeat([]byte{0x05}, 64),
		VanCmx:              FpLE(0x90 + uint64(nullifierSeed)),
		GovNullifiers: [][]byte{
			MakeNullifier(nullifierSeed),
			MakeNullifier(nullifierSeed + 1),
		},
		Proof:       []byte("mock-delegation-proof"),
		VoteRoundId: roundID,
		Sighash:     nil, // set below from canonical computation
	}
	msg.Sighash = types.ComputeDelegationSighash(msg)
	return msg
}

// ValidCastVote returns a MsgCastVote with mock data.
// VoteAuthorityNoteNew and VoteCommitment use canonical Fp encodings for the commitment tree.
// RVpkX and RVpkY are 32-byte stubs for condition 4 (Spend Authority).
func ValidCastVote(roundID []byte, anchorHeight uint64, nullifierSeed byte) *types.MsgCastVote {
	return &types.MsgCastVote{
		VanNullifier:             MakeNullifier(nullifierSeed),
		RVpkX:                    FpLE(0xA1 + uint64(nullifierSeed)),
		RVpkY:                    FpLE(0xA2 + uint64(nullifierSeed)),
		VoteAuthorityNoteNew:     FpLE(0xA0 + uint64(nullifierSeed)),
		VoteCommitment:           FpLE(0xB0 + uint64(nullifierSeed)),
		ProposalId:               1, // first proposal in SampleProposals()
		Proof:                    []byte("mock-vote-commitment-proof"),
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
	}
}

// ValidRevealShare returns a MsgRevealShare with mock data.
// EncShare is a deterministic 64-byte stub derived from nullifierSeed.
func ValidRevealShare(roundID []byte, anchorHeight uint64, nullifierSeed byte) *types.MsgRevealShare {
	encShare := make([]byte, 64)
	for i := range encShare {
		encShare[i] = nullifierSeed
	}
	return &types.MsgRevealShare{
		ShareNullifier:           MakeNullifier(nullifierSeed),
		EncShare:                 encShare,
		ProposalId:               1, // first proposal in SampleProposals()
		VoteDecision:             1, // "yes"
		Proof:                    []byte("mock-reveal-share-proof"),
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
	}
}

// ValidRevealShareReal returns a MsgRevealShare with a real ElGamal ciphertext
// as the EncShare. Use this when testing end-to-end encryption/decryption.
func ValidRevealShareReal(roundID []byte, anchorHeight uint64, nullifierSeed byte,
	proposalID uint32, decision uint32, encShare []byte,
) *types.MsgRevealShare {
	return &types.MsgRevealShare{
		ShareNullifier:           MakeNullifier(nullifierSeed),
		EncShare:                 encShare,
		ProposalId:               proposalID,
		VoteDecision:             decision,
		Proof:                    []byte("mock-reveal-share-proof"),
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: anchorHeight,
	}
}

// ValidSubmitTally returns a MsgSubmitTally for the given round ID and creator.
// By default it includes a single entry matching the default ValidRevealShare
// fixture (proposal_id=1, vote_decision=1, total_value=1000).
func ValidSubmitTally(roundID []byte, creator string) *types.MsgSubmitTally {
	return &types.MsgSubmitTally{
		VoteRoundId: roundID,
		Creator:     creator,
		Entries: []*types.TallyEntry{
			{
				ProposalId:   1,
				VoteDecision: 1,
				TotalValue:   1000,
			},
		},
	}
}

// ValidSubmitTallyWithEntries returns a MsgSubmitTally with custom entries.
func ValidSubmitTallyWithEntries(roundID []byte, creator string, entries []*types.TallyEntry) *types.MsgSubmitTally {
	return &types.MsgSubmitTally{
		VoteRoundId: roundID,
		Creator:     creator,
		Entries:     entries,
	}
}

// MakeNullifier creates a deterministic 32-byte nullifier from a seed byte.
func MakeNullifier(seed byte) []byte {
	return bytes.Repeat([]byte{seed}, 32)
}
