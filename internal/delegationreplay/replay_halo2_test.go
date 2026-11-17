//go:build halo2

package delegationreplay

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
)

// Exercise the actual delegation FFI with a deliberately invalid transcript,
// then replay the persisted evidence through that same real verifier.
func TestRealDelegationRejectionRoundTrip(t *testing.T) {
	inputs := testInputs()
	inputs.Rk = elgamal.PallasGenerator().ToAffineCompressed()
	proof := make([]byte, 11328)
	v := halo2.NewVerifier()
	failure := v.VerifyDelegation(proof, inputs)
	require.ErrorContains(t, failure, "proof verification failed")
	b, err := newBundle(proof, inputs, Metadata{Phase: "check_tx"}, testIdentity(), failure, time.Now())
	require.NoError(t, err)
	r := testRecorder(t)
	require.NoError(t, r.write(b))
	loaded, err := OpenBundle(filepath.Join(r.dir, b.ID+".json"))
	require.NoError(t, err)
	result, err := Replay(loaded, testIdentity(), v, false)
	require.NoError(t, err)
	require.Equal(t, "rejection reproduced", result)
}
