package types

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProxyBatchDigestIgnoresExpiryOnly checks that batch_digest is D1 at
// expiry 0, so re-signing with a new expiry keeps the batch identity, and
// that a registration changes D1.
func TestProxyBatchDigestIgnoresExpiryOnly(t *testing.T) {
	fill := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	batch := func(expiry uint64, registration *MsgDelegateVote) *MsgProxyDelegationBatch {
		return &MsgProxyDelegationBatch{
			VoteRoundId:              fill(1),
			VoteCommTreeAnchorHeight: 10,
			ExpiryHeight:             expiry,
			Registration:             registration,
			Dcs: []*ProxyDelegationAction{{
				RVpk: fill(2), VanNullifier: fill(3), VoteAuthorityNoteNew: fill(4),
				ProxyDelegationCommitment: fill(5), DcSlotNullifier: fill(6),
			}},
		}
	}
	compute := func(msg *MsgProxyDelegationBatch) (d1, batchDigest []byte) {
		d1, err := ComputeProxyDelegationBatchSighash("zvote-1", msg)
		require.NoError(t, err)
		batchDigest, err = ComputeProxyDelegationBatchDigest("zvote-1", msg)
		require.NoError(t, err)
		return d1, batchDigest
	}
	baseD1, baseDigest := compute(batch(500, nil))

	d1, batchDigest := compute(batch(1000, nil))
	require.NotEqual(t, baseD1, d1, "expiry must change D1")
	require.Equal(t, baseDigest, batchDigest, "expiry must not change batch_digest")

	d1, _ = compute(batch(0, nil))
	require.Equal(t, baseDigest, d1, "batch_digest is D1 at expiry 0")

	d1, _ = compute(batch(500, &MsgDelegateVote{VanCmx: fill(7)}))
	require.NotEqual(t, baseD1, d1, "a registration must change D1")
}
