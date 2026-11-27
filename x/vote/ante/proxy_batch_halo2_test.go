//go:build halo2 && proxyenabled

package ante_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/ffi/redpallas"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// proxyFixture is a real ZKP #4 proof with its public inputs and the VAN it
// spends, from ffi/zkp/testdata/proxy_delegation_*.bin.
type proxyFixture struct {
	proof    []byte
	inputs   zkp.ProxyDelegationInputs
	spentVan []byte
}

// mustReadProxyFixture parses a ZKP #4 fixture in the layout that
// build_proxy_delegation_fixtures (circuits/tests/generate_fixtures.rs)
// documents.
func mustReadProxyFixture(t *testing.T, name string) proxyFixture {
	t.Helper()
	reader := bytes.NewReader(mustReadFixture(t, name))
	read := func(n int) []byte {
		value := make([]byte, n)
		_, err := io.ReadFull(reader, value)
		require.NoError(t, err, name)
		return value
	}
	require.Equal(t, []byte("SVZKP4D1"), read(8))
	f := proxyFixture{proof: read(int(binary.LittleEndian.Uint32(read(4))))}
	f.inputs.VanNullifier = read(32)
	f.inputs.RVpk = read(32)
	f.inputs.VoteAuthorityNoteNew = read(32)
	f.inputs.ProxyDelegationCommitment = read(32)
	f.inputs.VoteCommTreeRoot = read(32)
	f.inputs.AnchorHeight = binary.LittleEndian.Uint64(read(8))
	f.inputs.VoteRoundId = read(32)
	f.inputs.EaPk = read(32)
	f.inputs.DcSlotNullifier = read(32)
	f.spentVan = read(32)
	require.Zero(t, reader.Len(), "unexpected trailing fixture bytes")
	return f
}

// dc returns f as a 0x09 DC with a placeholder signature.
func (f proxyFixture) dc() *types.ProxyDelegationAction {
	return &types.ProxyDelegationAction{
		VanNullifier:              f.inputs.VanNullifier,
		RVpk:                      f.inputs.RVpk,
		VoteAuthorityNoteNew:      f.inputs.VoteAuthorityNoteNew,
		ProxyDelegationCommitment: f.inputs.ProxyDelegationCommitment,
		DcSlotNullifier:           f.inputs.DcSlotNullifier,
		Proof:                     f.proof,
		VoteAuthSig:               bytes.Repeat([]byte{0x5A}, 64),
	}
}

// realProxyVerifier verifies ZKP #4 with the Halo2 FFI and accepts every
// other proof: there is no ZKP #1 fixture for the fixtures' VAN.
type realProxyVerifier struct{ zkp.MockVerifier }

// VerifyProxyDelegation verifies a real ZKP #4 proof.
func (realProxyVerifier) VerifyProxyDelegation(proof []byte, inputs zkp.ProxyDelegationInputs) error {
	return halo2.VerifyProxyDelegationProof(proof, inputs)
}

// TestHalo2ProxyBatchFixturesThroughAnte runs the real ZKP4 fixtures through
// the 0x09 ante against chain state built to match them (contracts-v1 §5.1
// step 8): an anchored two-DC batch whose second DC proves against the
// first's successor VAN, and a registration batch proving against van_cmx.
// Any change to the root chain, the anchor or the order fails the proofs.
func TestHalo2ProxyBatchFixturesThroughAnte(t *testing.T) {
	require.False(t, halo2.IsMock, "this test requires the real Halo2 verifier")
	anchored := mustReadProxyFixture(t, "proxy_delegation_anchored.bin")
	singleShare := mustReadProxyFixture(t, "proxy_delegation_single_share.bin")
	registration := mustReadProxyFixture(t, "proxy_delegation_registration.bin")
	roundID := anchored.inputs.VoteRoundId

	// setup returns a suite holding the fixtures' proxy round, with root
	// stored at height anchor.
	setup := func(anchor uint64, root []byte) *ValidateTestSuite {
		s := new(ValidateTestSuite)
		s.SetT(t)
		s.SetupTest()
		kv := s.keeper.OpenKVStore(s.ctx)
		round := svtest.ActiveRoundFixture(roundID)
		round.VoteEndTime = proxyAnteTime + 86_400
		round.EaPk = anchored.inputs.EaPk
		round.ProxyDelegation = &types.ProxyDelegationRoundState{Enabled: true, NextDelegateIndexAtCreation: 1}
		require.NoError(t, s.keeper.SetVoteRound(kv, round))
		require.NoError(t, s.keeper.SetCommitmentRootAtHeight(kv, roundID, anchor, root))
		return s
	}
	validate := func(s *ValidateTestSuite, msg *types.MsgProxyDelegationBatch) error {
		m := s.proxyModes()[2]
		return ante.ValidateVoteTx(m.ctx, msg, s.keeper, proxyOpts(m, redpallas.NewMockVerifier(), realProxyVerifier{}))
	}
	anchoredBatch := func() *types.MsgProxyDelegationBatch {
		return &types.MsgProxyDelegationBatch{
			VoteRoundId:              roundID,
			VoteCommTreeAnchorHeight: anchored.inputs.AnchorHeight,
			Dcs:                      []*types.ProxyDelegationAction{anchored.dc(), singleShare.dc()},
			ExpiryHeight:             proxyBatchAnteHeight + 10,
		}
	}

	t.Run("anchored", func(t *testing.T) {
		s := setup(anchored.inputs.AnchorHeight, anchored.inputs.VoteCommTreeRoot)
		require.NoError(t, validate(s, anchoredBatch()))

		reordered := anchoredBatch()
		reordered.Dcs[0], reordered.Dcs[1] = reordered.Dcs[1], reordered.Dcs[0]
		s.requireVoteCode(validate(s, reordered), types.ErrInvalidProof)

		wrongRoot := setup(anchored.inputs.AnchorHeight, singleShare.inputs.VoteCommTreeRoot)
		s.requireVoteCode(validate(wrongRoot, anchoredBatch()), types.ErrInvalidProof)

		// The anchor height is a public input, even with its root stored.
		otherAnchor := setup(anchored.inputs.AnchorHeight+1, anchored.inputs.VoteCommTreeRoot)
		msg := anchoredBatch()
		msg.VoteCommTreeAnchorHeight++
		s.requireVoteCode(validate(otherAnchor, msg), types.ErrInvalidProof)
	})

	t.Run("registration", func(t *testing.T) {
		require.Equal(t, roundID, registration.inputs.VoteRoundId)
		s := setup(1, anchored.inputs.VoteCommTreeRoot)
		msg := &types.MsgProxyDelegationBatch{
			VoteRoundId:  roundID,
			Registration: svtest.ValidDelegation(roundID, 0x61),
			Dcs:          []*types.ProxyDelegationAction{registration.dc()},
			ExpiryHeight: proxyBatchAnteHeight + 10,
		}
		msg.Registration.VanCmx = registration.spentVan
		require.NoError(t, validate(s, msg))

		msg.Registration.VanCmx = anchored.spentVan
		s.requireVoteCode(validate(s, msg), types.ErrInvalidProof)
	})
}
