package cli

import (
	"bytes"
	"encoding/hex"
	"strings"
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

	for _, bad := range []string{"7", "x:ab", "7:zz"} {
		_, err := parseProxyVerifier(bad)
		require.Error(t, err, "%q", bad)
	}
}

// TestCreateVotingSessionProxyFlag checks that --proxy-delegation sets the
// session's proxy flag and that a msg file setting the flag itself is
// refused.
func TestCreateVotingSessionProxyFlag(t *testing.T) {
	file := `{
		"snapshot_height": 1000,
		"snapshot_blockhash": "aabb",
		"proposals_hash": "ccdd",
		"vote_end_time": 1893456000,
		"nullifier_imt_root": "eeff",
		"nc_root": "0011",
		"title": "Round",
		"proposals": [{"id": 1, "title": "Upgrade", "options": [{"index": 0, "label": "Yes"}, {"index": 1, "label": "No"}]}]
	}`
	for _, proxyDelegation := range []bool{false, true} {
		msg, err := createVotingSessionMsg("sv1creator", []byte(file), proxyDelegation)
		require.NoError(t, err)
		require.Equal(t, proxyDelegation, msg.ProxyDelegation)
		require.Equal(t, "sv1creator", msg.Creator)
		require.Equal(t, uint64(1000), msg.SnapshotHeight)
	}

	inFile := strings.Replace(file, `"title": "Round",`, `"title": "Round", "proxy_delegation": true,`, 1)
	_, err := createVotingSessionMsg("sv1creator", []byte(inFile), true)
	require.ErrorContains(t, err, "--proxy-delegation")
}
