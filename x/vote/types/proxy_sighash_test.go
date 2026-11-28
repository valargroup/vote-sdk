package types

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProxyBatchDigestIgnoresExpiryOnly checks that batch_digest is D1 at
// expiry 0, so re-signing with a new expiry keeps the batch identity, and
// that the registration flag, not a stray van_cmx, decides what D1 binds.
func TestProxyBatchDigestIgnoresExpiryOnly(t *testing.T) {
	fill := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	base := ProxyBatchSighashFields{
		VoteRoundID:              fill(1),
		VoteCommTreeAnchorHeight: 10,
		ExpiryHeight:             500,
		DCs: []ProxyDCSighashFields{{
			RVpk: fill(2), VanNullifier: fill(3), VoteAuthorityNoteNew: fill(4),
			ProxyDelegationCommitment: fill(5), DCSlotNullifier: fill(6),
		}},
	}
	compute := func(f ProxyBatchSighashFields) (d1, batchDigest []byte) {
		d1, err := ComputeProxyDelegationBatchSighash("zvote-1", f)
		require.NoError(t, err)
		batchDigest, err = ComputeProxyDelegationBatchDigest("zvote-1", f)
		require.NoError(t, err)
		return d1, batchDigest
	}
	baseD1, baseDigest := compute(base)

	resigned := base
	resigned.ExpiryHeight = 1000
	d1, batchDigest := compute(resigned)
	require.NotEqual(t, baseD1, d1, "expiry must change D1")
	require.Equal(t, baseDigest, batchDigest, "expiry must not change batch_digest")

	atZero := base
	atZero.ExpiryHeight = 0
	d1, _ = compute(atZero)
	require.Equal(t, baseDigest, d1, "batch_digest is D1 at expiry 0")

	stray := base
	stray.RegistrationVanCmx = fill(7)
	d1, _ = compute(stray)
	require.Equal(t, baseD1, d1, "van_cmx without a registration must not be bound")

	registered := stray
	registered.HasRegistration = true
	d1, _ = compute(registered)
	require.NotEqual(t, baseD1, d1, "a registration must change D1")
}
