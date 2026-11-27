package types

import (
	"crypto/ed25519"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEd25519RejectsWrongLengths checks the length guards that run before any
// curve decoding.
func TestEd25519RejectsWrongLengths(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	key := []byte(priv.Public().(ed25519.PublicKey))
	msg := []byte("message")
	sig := ed25519.Sign(priv, msg)

	require.NoError(t, ValidateEd25519Key(key))
	require.NoError(t, VerifyEd25519ZIP215(key, msg, sig))

	for _, bad := range [][]byte{nil, key[:31], slices.Concat(key, []byte{0})} {
		require.ErrorIs(t, ValidateEd25519Key(bad), ErrEd25519KeyLength)
		require.ErrorIs(t, VerifyEd25519ZIP215(bad, msg, sig), ErrEd25519KeyLength)
	}
	for _, bad := range [][]byte{nil, sig[:63], slices.Concat(sig, []byte{0})} {
		require.ErrorIs(t, VerifyEd25519ZIP215(key, msg, bad), ErrEd25519SignatureLength)
	}
}
