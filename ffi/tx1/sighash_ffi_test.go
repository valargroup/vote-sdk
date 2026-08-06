//go:build redpallas

package tx1

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeDelegationSighashMatchesWalletFixture(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "testutil", "testdata", "delegation_tx1_effects_v1.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var fixture struct {
		TX1Effects string `json:"tx1_effects"`
		Sighash    string `json:"sighash"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	effects, err := base64.StdEncoding.DecodeString(fixture.TX1Effects)
	require.NoError(t, err)
	expected, err := base64.StdEncoding.DecodeString(fixture.Sighash)
	require.NoError(t, err)

	actual, err := ComputeDelegationSighash(effects)
	require.NoError(t, err)
	require.Equal(t, expected, actual)

	clear(effects[1+64 : 1+96])
	_, err = ComputeDelegationSighash(effects)
	require.ErrorContains(t, err, "invalid Ironwood action 0")
}
