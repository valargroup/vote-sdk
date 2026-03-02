// Package testutil provides shared test infrastructure for the Zally chain
// integration tests. It includes reusable message constructors, raw tx encoding
// helpers, and a TestApp that wraps ZallyApp for in-process ABCI testing.
package testutil

import (
	"bytes"
	"encoding/binary"
	"time"

	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/x/vote/types"
)

// FpLE returns a 32-byte little-endian encoding of v as a Pallas Fp element.
// Values 0 <= v < 2^64 are always canonical. Use for commitment tree leaves in tests.
func FpLE(v uint64) []byte {
	buf := make([]byte, 32)
	binary.LittleEndian.PutUint64(buf[:8], v)
	return buf
}

// DefaultOptions returns the standard binary vote options (Support/Oppose).
func DefaultOptions() []*types.VoteOption {
	return []*types.VoteOption{
		{Index: 0, Label: "Support"},
		{Index: 1, Label: "Oppose"},
	}
}

// SampleProposals returns two sample proposals for test fixtures.
// Proposal IDs must be 1-indexed per ValidateBasic (expected 1, 2, ...).
func SampleProposals() []*types.Proposal {
	return []*types.Proposal{
		{Id: 1, Title: "Proposal A", Description: "First proposal", Options: DefaultOptions()},
		{Id: 2, Title: "Proposal B", Description: "Second proposal", Options: DefaultOptions()},
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
		NullifierImtRoot:  bytes.Repeat([]byte{0x01}, 32),
		NcRoot:            bytes.Repeat([]byte{0x02}, 32),
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
		NullifierImtRoot:  bytes.Repeat([]byte{0x01}, 32),
		NcRoot:            bytes.Repeat([]byte{0x02}, 32),
		VkZkp1:    bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:    bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:    bytes.Repeat([]byte{0x33}, 64),
		Proposals: SampleProposals(),
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
		NullifierImtRoot:  bytes.Repeat([]byte{0x01}, 32),
		NcRoot:            bytes.Repeat([]byte{0x02}, 32),
		VkZkp1:    bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:    bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:    bytes.Repeat([]byte{0x33}, 64),
		Proposals: SampleProposals(),
	}
}

// ValidDelegation returns a MsgDelegateVote with mock proof data.
// Each call returns unique gov nullifiers derived from the provided seed.
// CmxNew and VanCmx use canonical Fp encodings so the commitment tree FFI accepts them.
// Sighash is set to a dummy 32-byte value; chain only checks length + signature.
func ValidDelegation(roundID []byte, nullifierSeed byte) *types.MsgDelegateVote {
	msg := &types.MsgDelegateVote{
		Rk:                  bytes.Repeat([]byte{0x01}, 32),
		SpendAuthSig:        bytes.Repeat([]byte{0x02}, 64),
		SignedNoteNullifier: bytes.Repeat([]byte{0x03}, 32),
		CmxNew:              FpLE(0x80 + uint64(nullifierSeed)),
		VanCmx:              FpLE(0x90 + uint64(nullifierSeed)),
		GovNullifiers: [][]byte{
			MakeNullifier(nullifierSeed),
			MakeNullifier(nullifierSeed + 1),
		},
		Proof:       []byte("mock-delegation-proof"),
		VoteRoundId: roundID,
		Sighash:     bytes.Repeat([]byte{0x99}, 32), // any 32 bytes; chain checks length + sig only
	}
	return msg
}

// ensureBytes32 returns a new 32-byte slice: copies b into it (pads with zeros if
// shorter, truncates if longer). Used so MsgCastVote sighash inputs survive
// protobuf round-trip unchanged (decoder returns new slices; length must match).
func ensureBytes32(b []byte) []byte {
	out := make([]byte, 32)
	if len(b) > 0 {
		copy(out, b)
	}
	return out
}

// ValidCastVote returns a MsgCastVote with mock data.
// VoteAuthorityNoteNew and VoteCommitment use canonical Fp encodings for the commitment tree.
// RVpkX and RVpkY are 32-byte stubs for condition 4 (Spend Authority).
// All sighash inputs (VoteRoundId, RVpk, VanNullifier, VoteAuthorityNoteNew, VoteCommitment)
// are normalized to 32-byte copies so encode/decode does not change them; Sighash is then set
// to the canonical ComputeCastVoteSighash so ante validation passes.
func ValidCastVote(roundID []byte, anchorHeight uint64, nullifierSeed byte) *types.MsgCastVote {
	msg := &types.MsgCastVote{
		VanNullifier:             ensureBytes32(MakeNullifier(nullifierSeed)),
		RVpkX:                    FpLE(0xA1 + uint64(nullifierSeed)),
		RVpkY:                    FpLE(0xA2 + uint64(nullifierSeed)),
		VoteAuthorityNoteNew:     ensureBytes32(FpLE(0xA0 + uint64(nullifierSeed))),
		VoteCommitment:           ensureBytes32(FpLE(0xB0 + uint64(nullifierSeed))),
		ProposalId:               1, // first proposal in SampleProposals()
		Proof:                    []byte("mock-vote-commitment-proof"),
		VoteRoundId:              ensureBytes32(roundID),
		VoteCommTreeAnchorHeight: anchorHeight,
		VoteAuthSig:              bytes.Repeat([]byte{0xC0 + nullifierSeed}, 64), // RedPallas sig stub
		RVpk:                     ensureBytes32(bytes.Repeat([]byte{0xE0 + nullifierSeed}, 32)),
	}
	msg.Sighash = types.ComputeCastVoteSighash(msg)
	return msg
}

// ValidRevealShare returns a MsgRevealShare with mock data.
// EncShare is a valid ElGamal identity ciphertext (two identity Pallas points)
// that passes keeper validation. The nullifierSeed only affects ShareNullifier.
func ValidRevealShare(roundID []byte, anchorHeight uint64, nullifierSeed byte) *types.MsgRevealShare {
	encShare := elgamal.IdentityCiphertextBytes()
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
