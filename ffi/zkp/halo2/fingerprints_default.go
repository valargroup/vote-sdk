//go:build !halo2

package halo2

import "github.com/valargroup/vote-sdk/x/vote/types"

// CircuitFingerprints returns no fingerprints when built without the "halo2"
// tag: a mock build has no verifying keys to fingerprint, so capability
// field 5 stays empty and wallets treat proxy delegation as unsupported
// (contracts-v1 §11.4).
func CircuitFingerprints() ([]*types.CircuitFingerprint, error) {
	return nil, nil
}
