package app

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestTallyInjectorIgnoresPools checks the proposer's tally path with
// delegate pools stored (docs/proxy-delegation/contracts-v1.md §5.6): a round
// whose only accumulators are pools counts as a zero-vote round, and
// decryptRoundTalliesThreshold decrypts the proposals' accumulators without
// asking for the pools' partial decryptions or emitting entries for them.
func TestTallyInjectorIgnoresPools(t *testing.T) {
	roundID := bytes.Repeat([]byte{0x9C}, 32)
	k, ctx := newTallyThresholdTestKV(t)
	kvStore := k.OpenKVStore(ctx)
	eaSk, eaPk := elgamal.KeyGen(rand.Reader)
	round := &types.VoteRound{VoteRoundId: roundID, Threshold: 1, Proposals: multiProposals}
	require.NoError(t, k.SetVoteRound(kvStore, round))

	add := func(proposalID, decision uint32, value uint64) *elgamal.Ciphertext {
		ct, err := elgamal.Encrypt(eaPk, value, rand.Reader)
		require.NoError(t, err)
		ctBytes, err := elgamal.MarshalCiphertext(ct)
		require.NoError(t, err)
		require.NoError(t, k.AddToTally(kvStore, roundID, proposalID, decision, ctBytes))
		return ct
	}
	add(types.PoolProposalID, 1, 7)
	add(types.PoolProposalID, 9, 3)
	has, err := roundHasAccumulators(kvStore, k, round)
	require.NoError(t, err)
	require.False(t, has, "pools alone are a zero-vote round")

	direct := add(2, 1, 11)
	has, err = roundHasAccumulators(kvStore, k, round)
	require.NoError(t, err)
	require.True(t, has)
	require.NoError(t, k.SetPartialDecryptions(kvStore, roundID, 1, []*types.PartialDecryptionEntry{{
		ProposalId:     2,
		VoteDecision:   1,
		PartialDecrypt: direct.C1.Mul(eaSk.Scalar).ToAffineCompressed(),
	}}))

	entries, err := decryptRoundTalliesThreshold(kvStore, k, round, bsgsSmall)
	require.NoError(t, err)
	require.Equal(t, []*types.TallyEntry{{ProposalId: 2, VoteDecision: 1, TotalValue: 11}}, entries)
}
