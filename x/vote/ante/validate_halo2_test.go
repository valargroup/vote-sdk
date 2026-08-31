//go:build halo2

package ante_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/ffi/redpallas"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// repoRoot returns the absolute path to the repository root by walking up
// from this test file's location (x/vote/ante/).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	// thisFile = .../x/vote/ante/validate_halo2_test.go → go up 4 levels
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// mustReadFixture reads a binary fixture from ffi/zkp/testdata/.
func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(repoRoot(t), "ffi", "zkp", "testdata", name)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read fixture %s", path)
	return data
}

type voteMaxProposalFixture struct {
	proof                []byte
	vanNullifier         []byte
	rVpk                 []byte
	voteAuthorityNoteNew []byte
	voteCommitment       []byte
	voteCommTreeRoot     []byte
	anchorHeight         uint64
	proposalID           uint32
	voteRoundID          []byte
	eaPk                 []byte
}

func mustReadVoteMaxProposalFixture(t *testing.T) voteMaxProposalFixture {
	t.Helper()
	reader := bytes.NewReader(mustReadFixture(t, "vote_max_proposal.bin"))

	magic := make([]byte, 8)
	_, err := io.ReadFull(reader, magic)
	require.NoError(t, err)
	require.Equal(t, []byte("SVZKP2M1"), magic)

	var proofLen uint32
	require.NoError(t, binary.Read(reader, binary.LittleEndian, &proofLen))
	require.LessOrEqual(t, int(proofLen), types.MaxProofSize)
	proof := make([]byte, proofLen)
	_, err = io.ReadFull(reader, proof)
	require.NoError(t, err)

	read32 := func(name string) []byte {
		value := make([]byte, 32)
		_, readErr := io.ReadFull(reader, value)
		require.NoError(t, readErr, name)
		return value
	}

	fixture := voteMaxProposalFixture{
		proof:                proof,
		vanNullifier:         read32("van_nullifier"),
		rVpk:                 read32("r_vpk"),
		voteAuthorityNoteNew: read32("vote_authority_note_new"),
		voteCommitment:       read32("vote_commitment"),
		voteCommTreeRoot:     read32("vote_comm_tree_root"),
	}
	require.NoError(t, binary.Read(reader, binary.LittleEndian, &fixture.anchorHeight))
	require.NoError(t, binary.Read(reader, binary.LittleEndian, &fixture.proposalID))
	fixture.voteRoundID = read32("voting_round_id")
	fixture.eaPk = read32("ea_pk")
	require.Zero(t, reader.Len(), "unexpected trailing fixture bytes")
	return fixture
}

// toyAsDelegationVerifier uses the toy circuit verifier for delegation so that
// the full ante pipeline can be tested with the only real proof fixture we have
// (toy_valid_proof.bin). The real delegation circuit expects a different proof
// format; once a delegation proof fixture exists, tests can switch to
// halo2.NewVerifier() and that fixture.
type toyAsDelegationVerifier struct{}

func (toyAsDelegationVerifier) VerifyDelegation(proof []byte, inputs zkp.DelegationInputs) error {
	return halo2.VerifyToyProof(proof, inputs.VanCmx)
}

func (toyAsDelegationVerifier) VerifyVoteCommitment(proof []byte, _ zkp.VoteCommitmentInputs) error {
	return nil
}

func (toyAsDelegationVerifier) VerifyVoteShare(proof []byte, _ zkp.VoteShareInputs) error {
	return nil
}

// TestHalo2DelegationValidProof runs the full ante validation pipeline with a
// real Halo2 toy proof. The MsgDelegateVote.Proof carries the real proof
// bytes and VanCmx carries the 32-byte public input (toy circuit convention).
func TestHalo2DelegationValidProof(t *testing.T) {
	proof := mustReadFixture(t, "toy_valid_proof.bin")
	publicInput := mustReadFixture(t, "toy_valid_input.bin")

	// Build a MsgDelegateVote with the real proof.
	// VanCmx carries the toy circuit public input; Rk is a non-zero dummy
	// (not used by the toy circuit, but must pass ValidateBasic's identity check).
	msg := &types.MsgDelegateVote{
		Rk:                  append([]byte(nil), svtest.DummyPallasPoint...),
		SpendAuthSig:        make([]byte, 64),
		SignedNoteNullifier: make([]byte, 32),
		CmxNew:              make([]byte, 32),
		VanCmx:              publicInput, // 32-byte toy circuit public input
		GovNullifiers: [][]byte{
			make([]byte, 32),
		},
		Proof:       proof,
		VoteRoundId: testRoundID,
	}
	svtest.SetDelegationTX1(msg)

	// Use toy-as-delegation verifier so the toy proof fixture passes; mock the
	// signature verifier (RedPallas is not under test here).
	opts := ante.ValidateOpts{
		SigVerifier: redpallas.NewMockVerifier(),
		ZKPVerifier: toyAsDelegationVerifier{},
	}

	// Create a test suite for the keeper/context setup, then run through
	// the full ValidateVoteTx pipeline.
	s := new(ValidateTestSuite)
	s.SetT(t)
	s.SetupTest()
	s.setupActiveRound()

	err := ante.ValidateVoteTx(s.ctx, msg, s.keeper, opts)
	require.NoError(t, err, "valid Halo2 toy proof should pass the ante handler")
}

// TestHalo2DelegationWrongInput verifies that a real Halo2 proof fails when
// paired with the wrong public input (i.e. the full pipeline returns
// ErrInvalidProof).
func TestHalo2DelegationWrongInput(t *testing.T) {
	proof := mustReadFixture(t, "toy_valid_proof.bin")
	wrongInput := mustReadFixture(t, "toy_wrong_input.bin")

	msg := &types.MsgDelegateVote{
		Rk:                  append([]byte(nil), svtest.DummyPallasPoint...),
		SpendAuthSig:        make([]byte, 64),
		SignedNoteNullifier: make([]byte, 32),
		CmxNew:              make([]byte, 32),
		VanCmx:              wrongInput, // wrong public input
		GovNullifiers: [][]byte{
			make([]byte, 32),
		},
		Proof:       proof,
		VoteRoundId: testRoundID,
	}
	svtest.SetDelegationTX1(msg)

	opts := ante.ValidateOpts{
		SigVerifier: redpallas.NewMockVerifier(),
		ZKPVerifier: toyAsDelegationVerifier{},
	}

	s := new(ValidateTestSuite)
	s.SetT(t)
	s.SetupTest()
	s.setupActiveRound()

	err := ante.ValidateVoteTx(s.ctx, msg, s.keeper, opts)
	require.Error(t, err, "wrong public input should fail verification")
	require.ErrorIs(t, err, types.ErrInvalidProof, "should wrap ErrInvalidProof")
}

func TestHalo2VoteMaxProposalThroughAnte(t *testing.T) {
	require.False(t, halo2.IsMock, "this test requires the real Halo2 verifier")
	fixture := mustReadVoteMaxProposalFixture(t)
	require.Equal(t, uint32(types.MaxProposals), fixture.proposalID)

	inputs := zkp.VoteCommitmentInputs{
		VanNullifier:         fixture.vanNullifier,
		RVpk:                 fixture.rVpk,
		VoteAuthorityNoteNew: fixture.voteAuthorityNoteNew,
		VoteCommitment:       fixture.voteCommitment,
		ProposalId:           fixture.proposalID,
		VoteRoundId:          fixture.voteRoundID,
		AnchorHeight:         fixture.anchorHeight,
		VoteCommTreeRoot:     fixture.voteCommTreeRoot,
		EaPk:                 fixture.eaPk,
	}
	require.NoError(t, halo2.VerifyVoteProof(fixture.proof, inputs))

	wrongProposal := inputs
	wrongProposal.ProposalId--
	require.Error(t, halo2.VerifyVoteProof(fixture.proof, wrongProposal))

	proposals := make([]*types.Proposal, types.MaxProposals)
	for i := range proposals {
		proposals[i] = &types.Proposal{
			Id:          uint32(i + 1),
			Title:       "Proposal",
			Description: "Boundary proof test",
			Options:     svtest.DefaultOptions(),
		}
	}
	round := &types.VoteRound{
		VoteRoundId:       fixture.voteRoundID,
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
		VoteEndTime:       activeEndTime,
		NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
		NcRoot:            bytes.Repeat([]byte{0x04}, 32),
		Creator:           "sv1testcreator",
		Status:            types.SessionStatus_SESSION_STATUS_ACTIVE,
		EaPk:              fixture.eaPk,
		Proposals:         proposals,
	}

	s := new(ValidateTestSuite)
	s.SetT(t)
	s.SetupTest()
	kvStore := s.keeper.OpenKVStore(s.ctx)
	require.NoError(t, s.keeper.SetVoteRound(kvStore, round))
	require.NoError(t, s.keeper.SetCommitmentRootAtHeight(
		kvStore,
		fixture.voteRoundID,
		fixture.anchorHeight,
		fixture.voteCommTreeRoot,
	))

	msg := &types.MsgCastVote{
		VanNullifier:             fixture.vanNullifier,
		RVpk:                     fixture.rVpk,
		VoteAuthSig:              make([]byte, 64),
		VoteAuthorityNoteNew:     fixture.voteAuthorityNoteNew,
		VoteCommitment:           fixture.voteCommitment,
		ProposalId:               fixture.proposalID,
		Proof:                    fixture.proof,
		VoteRoundId:              fixture.voteRoundID,
		VoteCommTreeAnchorHeight: fixture.anchorHeight,
	}
	opts := ante.ValidateOpts{
		SigVerifier: redpallas.NewMockVerifier(),
		ZKPVerifier: halo2.NewVerifier(),
	}

	msg.ProposalId--
	err := ante.ValidateVoteTx(s.ctx, msg, s.keeper, opts)
	require.ErrorIs(t, err, types.ErrInvalidProof)

	msg.ProposalId++
	require.NoError(t, ante.ValidateVoteTx(s.ctx, msg, s.keeper, opts))
}
