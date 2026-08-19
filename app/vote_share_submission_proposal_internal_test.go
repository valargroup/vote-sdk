package app

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

func TestFilterVoteShareSubmissionTransactionsClassifiesBeforeLimit(t *testing.T) {
	roundID := bytes.Repeat([]byte{0xF6}, types.RoundIDLen)
	txs := make([][]byte, MaxVoteShareSubmissionsPerBlock)
	for i := range txs {
		txs[i] = mustInternalVoteShareSubmissionTx(t, roundID, uint64(i+1))
	}

	txs = append(txs,
		[]byte{voteapi.TagRevealShare},
		txs[0],
		mustInternalVoteShareSubmissionTx(t, roundID, MaxVoteShareSubmissionsPerBlock+1),
	)

	filtered, stats := filterVoteShareSubmissionTransactions(txs)
	require.Len(t, filtered, MaxVoteShareSubmissionsPerBlock)
	require.Equal(t, MaxVoteShareSubmissionsPerBlock, stats.kept)
	require.Equal(t, 1, stats.malformed)
	require.Equal(t, 1, stats.duplicates)
	require.Equal(t, 1, stats.overLimit)
}

func mustInternalVoteShareSubmissionTx(t *testing.T, roundID []byte, nullifierID uint64) []byte {
	t.Helper()
	nullifier := make([]byte, 32)
	binary.BigEndian.PutUint64(nullifier[24:], nullifierID)
	txBytes, err := voteapi.EncodeVoteTx(&types.MsgRevealShare{
		VoteRoundId:              append([]byte(nil), roundID...),
		ShareNullifier:           nullifier,
		EncShare:                 bytes.Repeat([]byte{0x42}, 64),
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    []byte{0xAA},
		VoteCommTreeAnchorHeight: 1,
	})
	require.NoError(t, err)
	return txBytes
}
