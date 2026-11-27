//go:build halo2 && proxyenabled

package ante_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/ffi/redpallas"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// stuffingBlockBytes is app.MaxBlockBytes, the consensus block size limit.
const stuffingBlockBytes = 5 << 20

// replayVerifier pays the real Halo2 cost of every proof the ante asks for:
// each call verifies a fixture proof, and call failAt verifies a corrupted
// one, so a tx fails after failAt-1 valid verifications as a copied batch
// with a corrupted last proof would.
type replayVerifier struct {
	zkp.MockVerifier
	t      *testing.T
	zkp4   proxyFixture
	zkp2   voteMaxProposalFixture
	failAt int
	calls  int
}

// VerifyProxyDelegation verifies the ZKP4 fixture, or a corrupted copy on
// call failAt.
func (v *replayVerifier) VerifyProxyDelegation([]byte, zkp.ProxyDelegationInputs) error {
	v.calls++
	proof := v.zkp4.proof
	if v.calls == v.failAt {
		proof = corrupted(proof)
	}
	err := halo2.VerifyProxyDelegationProof(proof, v.zkp4.inputs)
	require.Equal(v.t, v.calls == v.failAt, err != nil, "ZKP4 call %d: %v", v.calls, err)
	return err
}

// VerifyVoteCommitment verifies the ZKP2 fixture, or a corrupted copy on
// call failAt.
func (v *replayVerifier) VerifyVoteCommitment([]byte, zkp.VoteCommitmentInputs) error {
	v.calls++
	f := v.zkp2
	proof := f.proof
	if v.calls == v.failAt {
		proof = corrupted(proof)
	}
	err := halo2.VerifyVoteProof(proof, zkp.VoteCommitmentInputs{
		VanNullifier: f.vanNullifier, RVpk: f.rVpk, VoteAuthorityNoteNew: f.voteAuthorityNoteNew,
		VoteCommitment: f.voteCommitment, ProposalId: f.proposalID, VoteRoundId: f.voteRoundID,
		AnchorHeight: f.anchorHeight, VoteCommTreeRoot: f.voteCommTreeRoot, EaPk: f.eaPk,
	})
	require.Equal(v.t, v.calls == v.failAt, err != nil, "ZKP2 call %d: %v", v.calls, err)
	return err
}

// corrupted returns proof with one byte in its middle flipped.
func corrupted(proof []byte) []byte {
	out := append([]byte(nil), proof...)
	out[len(out)/2] ^= 1
	return out
}

// TestProposalStuffingCost measures the FinalizeBlock ante work of a
// proposal stuffed with late-failing txs: copies of an unspent 8-DC 0x09
// batch whose last proof is corrupted, one-DC 0x09 batches with a bad
// proof, copies of a 50-vote 0x06 batch corrupted like the first, and
// single 0x03 casts with a bad proof. Every
// proof is a real fixture verified with Halo2; signatures use the mock
// verifier. It logs each tx's size and ante time and the block time at
// MaxBlockBytes. It runs only with SVOTE_STUFFING_COST=1.
func TestProposalStuffingCost(t *testing.T) {
	if os.Getenv("SVOTE_STUFFING_COST") == "" {
		t.Skip("set SVOTE_STUFFING_COST=1 to measure")
	}
	require.False(t, halo2.IsMock, "this test requires the real Halo2 verifier")
	require.NoError(t, halo2.WarmVerifierCaches())
	zkp4 := mustReadProxyFixture(t, "proxy_delegation_anchored.bin")
	zkp2 := mustReadVoteMaxProposalFixture(t)

	// measure runs msg through the FinalizeBlock ante reps times, failing at
	// proof failAt, and logs its cost per tx and per stuffed block.
	measure := func(name string, setup func() *ValidateTestSuite, msg types.VoteMessage, failAt, reps int) {
		raw, err := voteapi.EncodeVoteTx(msg)
		require.NoError(t, err)
		var total time.Duration
		for range reps {
			s := setup()
			verifier := &replayVerifier{t: t, zkp4: zkp4, zkp2: zkp2, failAt: failAt}
			m := s.proxyModes()[2]
			start := time.Now()
			err := ante.ValidateVoteTx(m.ctx, msg, s.keeper, proxyOpts(m, redpallas.NewMockVerifier(), verifier))
			total += time.Since(start)
			require.True(t, errors.Is(err, types.ErrInvalidProof), "%s: %v", name, err)
			require.Equal(t, failAt, verifier.calls)
		}
		perTx := total / time.Duration(reps)
		perBlock := stuffingBlockBytes / len(raw)
		t.Logf("%s: %d bytes, %d valid proofs, %v per tx; %d per block, %v per block",
			name, len(raw), failAt-1, perTx.Round(time.Millisecond), perBlock,
			(perTx * time.Duration(perBlock)).Round(time.Millisecond))
	}

	proxySuite := func() *ValidateTestSuite {
		s := new(ValidateTestSuite)
		s.SetT(t)
		s.SetupTest()
		s.setupProxyBatchRound(func(*types.VoteRound) {})
		return s
	}
	batch := proxyAnteBatch(types.MaxProxyDelegationDCs, false)
	for _, dc := range batch.Dcs {
		dc.Proof = zkp4.proof
	}
	measure("0x09 with 8 DCs", proxySuite, batch, types.MaxProxyDelegationDCs, 20)
	// A forged one-DC batch needs no copied proofs and fresh nullifiers do,
	// so deduplicating by nullifier would not stop it.
	forged := proxyAnteBatch(1, false)
	forged.Dcs[0].Proof = corrupted(zkp4.proof)
	measure("0x09 with 1 bad DC", proxySuite, forged, 1, 100)

	castSuite := func() *ValidateTestSuite {
		s := new(ValidateTestSuite)
		s.SetT(t)
		s.SetupTest()
		s.setupProxyBatchRound(func(r *types.VoteRound) {
			r.Proposals = nil
			for id := uint32(1); id <= types.MaxProposals; id++ {
				r.Proposals = append(r.Proposals, &types.Proposal{Id: id, Title: "P", Options: svtest.DefaultOptions()})
			}
		})
		return s
	}
	votes := svtest.ValidCastVoteN(proxyAnteRound, proxyBatchAnteAnchor, types.MaxCastVoteBatchSize, 0x70)
	for i, vote := range votes {
		vote.ProposalId = uint32(i + 1)
		vote.Proof = zkp2.proof
	}
	measure("0x06 with 50 votes", castSuite, &types.MsgCastVoteBatch{Votes: votes}, types.MaxCastVoteBatchSize, 5)
	measure("0x03 with a bad proof", castSuite, votes[0], 1, 100)
}
