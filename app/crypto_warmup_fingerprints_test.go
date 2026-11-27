package app_test

import (
	"context"
	"testing"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
	"github.com/valargroup/vote-sdk/testutil"
)

// TestCryptoWarmupPublishesCircuitFingerprints checks that verifier warm-up
// publishes halo2.CircuitFingerprints for capability field 5
// (docs/proxy-delegation/contracts-v1.md §8): published but empty in a build
// without the halo2 tag, whose stub returns none.
func TestCryptoWarmupPublishesCircuitFingerprints(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	require.Nil(t, ta.VoteKeeper().CircuitFingerprints(), "nothing is published before warm-up")

	require.NoError(t, ta.StartCryptoWarmup(context.Background(), log.NewNopLogger()))
	require.Equal(t, voteapi.CryptoReadinessStatusReady, ta.CryptoWarmupStatus().Status)

	want, err := halo2.CircuitFingerprints()
	require.NoError(t, err)
	got := ta.VoteKeeper().CircuitFingerprints()
	require.NotNil(t, got, "warm-up publishes the fingerprints")
	require.Len(t, got, len(want))
	for i := range want {
		require.True(t, proto.Equal(want[i], got[i]), "%v", got[i])
	}
}
