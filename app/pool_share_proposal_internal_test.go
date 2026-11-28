package app

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests cover pool shares, proposal-0 reveals, in the proposer's
// reveal filter and ProcessProposal's check
// (docs/proxy-delegation/contracts-v1.md §5.4): with the gate open
// (-tags proxyenabled) a block
// holding one is valid and they count toward the 256-reveal cap; while
// dormant they are malformed, as today.

// poolShareTx encodes a pool share for delegate d in round roundID whose
// share nullifier encodes nullifierID.
func poolShareTx(t *testing.T, roundID []byte, d uint32, nullifierID uint64) []byte {
	t.Helper()
	nullifier := make([]byte, 32)
	binary.BigEndian.PutUint64(nullifier[24:], nullifierID)
	txBytes, err := voteapi.EncodeVoteTx(&types.MsgRevealShare{
		VoteRoundId:              append([]byte(nil), roundID...),
		ShareNullifier:           nullifier,
		EncShare:                 bytes.Repeat([]byte{0x42}, 64),
		ProposalId:               types.PoolProposalID,
		VoteDecision:             d,
		Proof:                    []byte{0xAA},
		VoteCommTreeAnchorHeight: 1,
	})
	require.NoError(t, err)
	return txBytes
}

// TestRevealFilterPoolShares checks that with the gate open the filter keeps
// pool shares, any d >= 1, deduplicates them by share nullifier with votes,
// drops d = 0 as malformed and counts them toward the cap; with it closed
// every pool share is malformed.
func TestRevealFilterPoolShares(t *testing.T) {
	roundID := bytes.Repeat([]byte{0xE1}, types.RoundIDLen)
	pool := poolShareTx(t, roundID, 9, 1)
	sameNullifierVote := mustInternalVoteShareSubmissionTx(t, roundID, 1)
	zero := poolShareTx(t, roundID, 0, 2)
	txs := [][]byte{pool, sameNullifierVote, zero, poolShareTx(t, roundID, 1<<30, 3)}

	filtered, stats := filterVoteShareSubmissionTransactions(txs)
	if !types.ProxyDelegationEnabled {
		require.Equal(t, [][]byte{sameNullifierVote}, filtered)
		require.Equal(t, voteShareSubmissionFilterStats{kept: 1, malformed: 3}, stats)
		require.ErrorContains(t, validateVoteShareSubmissionTransactions([][]byte{pool}), "proposal_id must be 1..50, got 0")
		return
	}
	require.Equal(t, [][]byte{pool, txs[3]}, filtered)
	require.Equal(t, voteShareSubmissionFilterStats{kept: 2, duplicates: 1, malformed: 1}, stats)
	require.NoError(t, validateVoteShareSubmissionTransactions(filtered))
	require.Error(t, validateVoteShareSubmissionTransactions([][]byte{zero}))
	require.Error(t, validateVoteShareSubmissionTransactions([][]byte{pool, sameNullifierVote}))
}

// TestRevealCapCountsPoolShares checks that pool shares count toward
// MaxVoteShareSubmissionsPerBlock alongside votes: the filter keeps the first
// 256 reveals of either kind and ProcessProposal rejects 257.
func TestRevealCapCountsPoolShares(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	roundID := bytes.Repeat([]byte{0xE2}, types.RoundIDLen)
	txs := make([][]byte, MaxVoteShareSubmissionsPerBlock+1)
	for i := range txs {
		if i%2 == 0 {
			txs[i] = poolShareTx(t, roundID, uint32(i+1), uint64(i+1))
		} else {
			txs[i] = mustInternalVoteShareSubmissionTx(t, roundID, uint64(i+1))
		}
	}
	txs[len(txs)-1] = poolShareTx(t, roundID, 1, uint64(len(txs)))

	filtered, stats := filterVoteShareSubmissionTransactions(txs)
	require.Equal(t, txs[:MaxVoteShareSubmissionsPerBlock], filtered)
	require.Equal(t, voteShareSubmissionFilterStats{kept: MaxVoteShareSubmissionsPerBlock, overLimit: 1}, stats)
	require.NoError(t, validateVoteShareSubmissionTransactions(filtered))
	require.ErrorContains(t, validateVoteShareSubmissionTransactions(txs), "maximum is 256")
}
