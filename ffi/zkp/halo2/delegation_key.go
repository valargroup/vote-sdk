package halo2

import "errors"

// DelegationKeyFingerprintLen is the length in bytes of a delegation circuit
// key fingerprint.
const DelegationKeyFingerprintLen = 32

// ErrDelegationKeyFingerprintUnavailable is returned by
// DelegationKeyFingerprint when the binary was built without the "halo2" tag.
// Such a build uses the mock verifier and has no delegation circuit key, so
// there is no fingerprint to compare a client's against.
var ErrDelegationKeyFingerprintUnavailable = errors.New("halo2: delegation key fingerprint unavailable without the halo2 build tag")
