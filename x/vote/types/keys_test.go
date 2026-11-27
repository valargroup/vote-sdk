package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProxyStoreKeysRejectMalformedInputs checks that the proxy delegation
// store keys refuse a dk or round id of the wrong length, which would
// otherwise make one key a prefix of another.
func TestProxyStoreKeysRejectMalformedInputs(t *testing.T) {
	for _, n := range []int{0, 31, 33} {
		_, err := ProxyUsedDelegateKeyKey(make([]byte, n))
		require.ErrorIs(t, err, ErrInvalidField, "dk length %d", n)

		_, err = DelegateBallotKey(make([]byte, n), 1)
		require.ErrorIs(t, err, ErrInvalidRoundIDLen, "round id length %d", n)
	}
}
