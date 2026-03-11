// Package testutil provides shared test infrastructure for the Shielded-Vote chain
// integration tests. It includes reusable message constructors, raw tx encoding
// helpers, and a TestApp that wraps SvoteApp for in-process ABCI testing.
package testutil

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/shielded-vote/crypto/elgamal"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

// TestAuthority is the module authority address used by all keeper tests.
const TestAuthority = "sv1authority"

// TestValAddr generates a deterministic valid bech32 validator operator address from a seed byte.
func TestValAddr(seed byte) string {
	addr := make([]byte, 20)
	addr[0] = seed
	return sdk.ValAddress(addr).String()
}

// TestAccAddr generates a deterministic valid bech32 account address from a seed byte.
func TestAccAddr(seed byte) string {
	addr := make([]byte, 20)
	addr[0] = seed
	return sdk.AccAddress(addr).String()
}

// FpLE returns a 32-byte little-endian encoding of v as a Pallas Fp element.
// Values 0 <= v < 2^64 are always canonical. Use for commitment tree leaves in tests.
func FpLE(v uint64) []byte {
	buf := make([]byte, 32)
	binary.LittleEndian.PutUint64(buf[:8], v)
	return buf
}

// ActiveRoundFixture returns a VoteRound in ACTIVE status with sensible defaults.
// Used to seed keeper state directly in unit tests without going through CreateVotingSession.
func ActiveRoundFixture(roundID []byte) *types.VoteRound {
	return &types.VoteRound{
		VoteRoundId:      roundID,
		VoteEndTime:      2_000_000,
		Creator:          "sv1creator",
		Status:           types.SessionStatus_SESSION_STATUS_ACTIVE,
		NullifierImtRoot: bytes.Repeat([]byte{0x03}, 32),
		NcRoot:           bytes.Repeat([]byte{0x04}, 32),
		EaPk:             bytes.Repeat([]byte{0x05}, 32),
		VkZkp1:           bytes.Repeat([]byte{0x06}, 64),
		VkZkp2:           bytes.Repeat([]byte{0x07}, 64),
		VkZkp3:           bytes.Repeat([]byte{0x08}, 64),
		Proposals:        SampleProposals(),
	}
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
		Creator:           "sv1admin",
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
		Creator:           "sv1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
		VoteEndTime:       uint64(refTime.Add(1 * time.Hour).Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x01}, 32),
		NcRoot:            bytes.Repeat([]byte{0x02}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         SampleProposals(),
	}
}

// ValidCreateVotingSessionWithEndTime returns a MsgCreateVotingSession with an explicit
// VoteEndTime. Use when you need a specific end time (e.g. 10 seconds from block time).
func ValidCreateVotingSessionWithEndTime(endTime time.Time) *types.MsgCreateVotingSession {
	return &types.MsgCreateVotingSession{
		Creator:           "sv1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
		VoteEndTime:       uint64(endTime.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x01}, 32),
		NcRoot:            bytes.Repeat([]byte{0x02}, 32),
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
		Creator:           "sv1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
		VoteEndTime:       uint64(refTime.Add(-1 * time.Hour).Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x01}, 32),
		NcRoot:            bytes.Repeat([]byte{0x02}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         SampleProposals(),
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
// shorter, truncates if longer). Used so MsgCastVote fields survive protobuf
// round-trip unchanged (decoder returns new slices; length must match).
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
// Sighash is computed on-chain by the ante handler from the message fields.
func ValidCastVote(roundID []byte, anchorHeight uint64, nullifierSeed byte) *types.MsgCastVote {
	return &types.MsgCastVote{
		VanNullifier:             ensureBytes32(MakeNullifier(nullifierSeed)),
		VoteAuthorityNoteNew:     ensureBytes32(FpLE(0xA0 + uint64(nullifierSeed))),
		VoteCommitment:           ensureBytes32(FpLE(0xB0 + uint64(nullifierSeed))),
		ProposalId:               1, // first proposal in SampleProposals()
		Proof:                    []byte("mock-vote-commitment-proof"),
		VoteRoundId:              ensureBytes32(roundID),
		VoteCommTreeAnchorHeight: anchorHeight,
		VoteAuthSig:              bytes.Repeat([]byte{0xC0 + nullifierSeed}, 64), // RedPallas sig stub
		RVpk:                     ensureBytes32(bytes.Repeat([]byte{0xE0 + nullifierSeed}, 32)),
	}
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

// makeNullifierFromUint64 creates a deterministic 32-byte nullifier from a
// uint64. This supports large stress batches without the 256-value collision
// limit of MakeNullifier(seed byte).
func makeNullifierFromUint64(v uint64) []byte {
	out := make([]byte, 32)
	binary.LittleEndian.PutUint64(out[0:8], v)
	binary.LittleEndian.PutUint64(out[8:16], v^0x9e3779b97f4a7c15)
	binary.LittleEndian.PutUint64(out[16:24], v^0x243f6a8885a308d3)
	binary.LittleEndian.PutUint64(out[24:32], v^0xb7e151628aed2a6b)
	return out
}

// ValidDelegationN returns n deterministic MsgDelegateVote messages with unique
// gov nullifiers and VAN commitments. The seed controls reproducible generation.
func ValidDelegationN(roundID []byte, n int, seed uint64) []*types.MsgDelegateVote {
	if n <= 0 {
		return nil
	}
	out := make([]*types.MsgDelegateVote, 0, n)
	for i := range n {
		base := seed + uint64(i)*4
		msg := &types.MsgDelegateVote{
			Rk:                  bytes.Repeat([]byte{0x01}, 32),
			SpendAuthSig:        bytes.Repeat([]byte{0x02}, 64),
			SignedNoteNullifier: makeNullifierFromUint64(base + 1),
			CmxNew:              FpLE(base + 2),
			VanCmx:              FpLE(base + 3),
			GovNullifiers: [][]byte{
				makeNullifierFromUint64(base + 10),
				makeNullifierFromUint64(base + 11),
			},
			Proof:       []byte("mock-delegation-proof"),
			VoteRoundId: ensureBytes32(roundID),
			Sighash:     bytes.Repeat([]byte{0x99}, 32),
		}
		out = append(out, msg)
	}
	return out
}

// ValidCastVoteN returns n deterministic MsgCastVote messages with unique VAN
// nullifiers and commitment leaves. The seed controls reproducible generation.
func ValidCastVoteN(roundID []byte, anchorHeight uint64, n int, seed uint64) []*types.MsgCastVote {
	if n <= 0 {
		return nil
	}
	out := make([]*types.MsgCastVote, 0, n)
	for i := range n {
		base := seed + uint64(i)*5
		sigByte := byte((base & 0xff))
		rvpkByte := byte(((base + 77) & 0xff))
		msg := &types.MsgCastVote{
			VanNullifier:             makeNullifierFromUint64(base + 20),
			VoteAuthorityNoteNew:     FpLE(base + 21),
			VoteCommitment:           FpLE(base + 22),
			ProposalId:               1,
			Proof:                    []byte("mock-vote-commitment-proof"),
			VoteRoundId:              ensureBytes32(roundID),
			VoteCommTreeAnchorHeight: anchorHeight,
			VoteAuthSig:              bytes.Repeat([]byte{sigByte}, 64),
			RVpk:                     bytes.Repeat([]byte{rvpkByte}, 32),
		}
		out = append(out, msg)
	}
	return out
}

// ShuffleWithSeed returns a deterministically shuffled copy of in.
func ShuffleWithSeed[T any](in []T, seed uint64) []T {
	out := make([]T, len(in))
	copy(out, in)
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	r.Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	return out
}

// NullifierConflictSet contains conflicting/fresh tx fixtures for both
// delegation (gov nullifiers) and cast-vote (VAN nullifiers) race tests.
type NullifierConflictSet struct {
	GovWinner *types.MsgDelegateVote
	GovLoser  *types.MsgDelegateVote
	GovFresh  *types.MsgDelegateVote

	VanWinner *types.MsgCastVote
	VanLoser  *types.MsgCastVote
	VanFresh  *types.MsgCastVote
}

// BuildConflictingNullifierSet builds deterministic tx fixtures where two txs
// intentionally conflict on the same nullifier and one tx is fresh.
func BuildConflictingNullifierSet(roundID []byte, anchorHeight uint64, seed uint64) NullifierConflictSet {
	govWinner := ValidDelegationN(roundID, 1, seed+100)[0]
	govLoser := ValidDelegationN(roundID, 1, seed+200)[0]
	// Force conflict on both gov nullifiers.
	govLoser.GovNullifiers = [][]byte{
		append([]byte(nil), govWinner.GovNullifiers[0]...),
		append([]byte(nil), govWinner.GovNullifiers[1]...),
	}
	govFresh := ValidDelegationN(roundID, 1, seed+300)[0]

	vanWinner := ValidCastVoteN(roundID, anchorHeight, 1, seed+400)[0]
	vanLoser := ValidCastVoteN(roundID, anchorHeight, 1, seed+500)[0]
	// Force conflict on VAN nullifier.
	vanLoser.VanNullifier = append([]byte(nil), vanWinner.VanNullifier...)
	vanFresh := ValidCastVoteN(roundID, anchorHeight, 1, seed+600)[0]

	return NullifierConflictSet{
		GovWinner: govWinner,
		GovLoser:  govLoser,
		GovFresh:  govFresh,
		VanWinner: vanWinner,
		VanLoser:  vanLoser,
		VanFresh:  vanFresh,
	}
}
