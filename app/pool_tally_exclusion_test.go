package app_test

import (
	"bytes"
	"crypto/rand"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestThresholdTallyLeavesPoolsOut runs a threshold round's tally through the
// app with delegate pools stored (docs/proxy-delegation/contracts-v1.md §5.6):
// the injected partial decryption covers every proposal's accumulator and no
// pool, ProcessProposal rejects one that adds a pool entry, and the injected
// tally finalizes the round with results for the proposals only. Nothing
// here depends on the dormancy gate.
func TestThresholdTallyLeavesPoolsOut(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	roundID := bytes.Repeat([]byte{0xC7}, 32)
	eaSk, eaPk := elgamal.KeyGen(rand.Reader)
	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: ta.ValidatorOperAddr(), PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	// With one validator at threshold 1, its share is ea_sk itself.
	accumulators := seedTallyingRoundWithAccumulators(t, ta, roundID, 1, validators, [][]byte{eaPk.Point.ToAffineCompressed()}, eaPk)

	kvStore := ta.VoteKeeper().OpenKVStore(ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height}))
	var pool *elgamal.Ciphertext
	for _, d := range []uint32{3, 9} {
		ct, err := elgamal.Encrypt(eaPk, 1000, rand.Reader)
		require.NoError(t, err)
		ctBytes, err := elgamal.MarshalCiphertext(ct)
		require.NoError(t, err)
		require.NoError(t, ta.VoteKeeper().AddToTally(kvStore, roundID, types.PoolProposalID, d, ctBytes))
		pool = ct
	}
	ta.NextBlock()
	shareBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)
	ta.WriteShareForRound(roundID, shareBytes)

	prepared := ta.CallPrepareProposal().Txs
	require.Len(t, prepared, 1)
	_, decoded, err := voteapi.DecodeCeremonyTx(prepared[0])
	require.NoError(t, err)
	partials := decoded.(*types.MsgSubmitPartialDecryption)
	require.Len(t, partials.Entries, len(accumulators))
	for _, entry := range partials.Entries {
		require.NotEqual(t, types.PoolProposalID, entry.ProposalId)
	}
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, ta.CallProcessProposal(prepared).Status)

	proof, err := elgamal.GeneratePartialDecryptDLEQ(eaSk.Scalar, pool.C1)
	require.NoError(t, err)
	partials.Entries = append(partials.Entries, &types.PartialDecryptionEntry{
		ProposalId:     types.PoolProposalID,
		VoteDecision:   9,
		PartialDecrypt: pool.C1.Mul(eaSk.Scalar).ToAffineCompressed(),
		DleqProof:      proof,
	})
	withPool, err := voteapi.EncodeCeremonyTx(partials, voteapi.TagSubmitPartialDecryption)
	require.NoError(t, err)
	require.Equal(t, abci.ResponseProcessProposal_REJECT, ta.CallProcessProposal([][]byte{withPool}).Status)

	ta.NextBlockWithPrepareProposal() // the partial decryption lands
	ta.NextBlockWithPrepareProposal() // the tally lands

	kvStore = ta.VoteKeeper().OpenKVStore(ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height}))
	round, err := ta.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)
	results, err := ta.VoteKeeper().GetAllTallyResults(kvStore, roundID)
	require.NoError(t, err)
	require.Len(t, results, len(accumulators))
	for _, result := range results {
		require.NotEqual(t, types.PoolProposalID, result.ProposalId)
		require.Equal(t, uint64(42), result.TotalValue)
	}
	stored, err := ta.VoteKeeper().GetPartialDecryption(kvStore, roundID, 1, types.PoolProposalID, 9)
	require.NoError(t, err)
	require.Nil(t, stored)
}
