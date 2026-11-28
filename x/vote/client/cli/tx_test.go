package cli

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseProxyVerifier checks the --verifier format, <id>:<pubkey-hex>.
func TestParseProxyVerifier(t *testing.T) {
	pubkey := bytes.Repeat([]byte{0xab}, 32)
	verifier, err := parseProxyVerifier("7:" + hex.EncodeToString(pubkey))
	require.NoError(t, err)
	require.Equal(t, uint32(7), verifier.VerifierId)
	require.Equal(t, pubkey, verifier.Pubkey)

	for _, bad := range []string{"", "7", "x:ab", "-1:ab", "4294967296:ab", "7:zz"} {
		_, err := parseProxyVerifier(bad)
		require.Error(t, err, "%q", bad)
	}
}
