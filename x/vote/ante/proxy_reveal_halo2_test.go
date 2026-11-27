//go:build halo2 && proxyenabled

package ante_test

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/ffi/redpallas"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestHalo2PoolShareThroughAnte proves a pool share with the real ZKP3
// prover and runs it through the ante with the real verifier
// (docs/proxy-delegation/contracts-v1.md §5.4). ZKP3 is unchanged: the vote
// commitment it proves is the DC, Poseidon5(DOMAIN_VC, round, shares_hash,
// 0, d) (§10), so proposal 0 and a delegate index above the eight-option
// bound need no circuit change. share_reveal_inputs.bin holds a single-leaf
// tree at position 0, whose Merkle path does not depend on the leaf, so the
// prover builds the DC's tree from the same inputs.
func TestHalo2PoolShareThroughAnte(t *testing.T) {
	require.False(t, halo2.IsMock, "this test requires the real Halo2 prover and verifier")
	fixture := mustReadFixture(t, "share_reveal_inputs.bin")
	require.Len(t, fixture, 1488, "unexpected fixture size; run: make fixtures")
	var shareComms [16][32]byte
	for i := range shareComms {
		copy(shareComms[i][:], fixture[772+i*32:])
	}
	var primaryBlind, encC1, encC2, roundID [32]byte
	copy(primaryBlind[:], fixture[1284:1316])
	copy(encC1[:], fixture[1316:1348])
	copy(encC2[:], fixture[1348:1380])
	copy(roundID[:], fixture[1392:1424])
	shareIndex := binary.LittleEndian.Uint32(fixture[1380:1384])
	encShare := append([]byte(nil), fixture[1424:1488]...)

	const d = 9 // above the eight-option bound of vote shares
	t.Log("generating a pool share's ZKP3 proof (this takes ~5-15s)...")
	proof, nullifier, treeRoot, err := halo2.GenerateShareRevealProof(fixture[0:772], shareComms, primaryBlind, encC1, encC2, shareIndex, types.PoolProposalID, d, roundID)
	require.NoError(t, err)

	inputs := zkp.VoteShareInputs{
		ShareNullifier:   nullifier[:],
		EncShare:         encShare,
		ProposalId:       types.PoolProposalID,
		VoteDecision:     d,
		VoteRoundId:      roundID[:],
		VoteCommTreeRoot: treeRoot[:],
	}
	require.NoError(t, halo2.VerifyShareRevealProof(proof, inputs))
	for name, mutate := range map[string]func(*zkp.VoteShareInputs){
		"another delegate":    func(in *zkp.VoteShareInputs) { in.VoteDecision = d - 1 },
		"a vote's proposal":   func(in *zkp.VoteShareInputs) { in.ProposalId = 1 },
		"the vote's position": func(in *zkp.VoteShareInputs) { in.ProposalId, in.VoteDecision = 3, 1 },
	} {
		wrong := inputs
		mutate(&wrong)
		require.Error(t, halo2.VerifyShareRevealProof(proof, wrong), name)
	}

	s := new(ValidateTestSuite)
	s.SetT(t)
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)
	round := proxytest.Round(roundID[:], 2)
	round.VoteEndTime = proxyAnteTime + 86_400
	require.NoError(t, s.keeper.SetVoteRound(kv, round))
	require.NoError(t, s.keeper.SetProxyNextDelegateIndex(kv, d+1))
	const anchor = 77
	require.NoError(t, s.keeper.SetCommitmentRootAtHeight(kv, roundID[:], anchor, treeRoot[:]))
	msg := &types.MsgRevealShare{
		ShareNullifier:           nullifier[:],
		EncShare:                 encShare,
		ProposalId:               types.PoolProposalID,
		VoteDecision:             d,
		Proof:                    proof,
		VoteRoundId:              roundID[:],
		VoteCommTreeAnchorHeight: anchor,
	}
	opts := ante.ValidateOpts{SigVerifier: redpallas.NewMockVerifier(), ZKPVerifier: halo2.NewVerifier()}
	require.NoError(t, ante.ValidateVoteTx(s.proxyCtx(), msg, s.keeper, opts))
	require.NoError(t, ante.ValidateVoteTx(s.proxyCtx().WithIsCheckTx(true), msg, s.keeper, opts))

	// The proof binds d: claiming another registered delegate passes the
	// pool check and fails ZKP3.
	msg.VoteDecision = d - 1
	s.requireVoteCode(ante.ValidateVoteTx(s.proxyCtx(), msg, s.keeper, opts), types.ErrInvalidProof)
}
