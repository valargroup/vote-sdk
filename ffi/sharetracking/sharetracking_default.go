//go:build !halo2

// Package sharetracking provides Go bindings for the cheap share-nullifier
// hash used by helpers to avoid redundant ZKP 3 work.
package sharetracking

import "fmt"

// ShareNullifierHash is unavailable without the Halo2 FFI build tag.
func ShareNullifierHash(voteCommitment [32]byte, shareIndex uint32, primaryBlind [32]byte) ([32]byte, error) {
	var nullifier [32]byte
	return nullifier, fmt.Errorf("sharetracking requires the 'halo2' build tag")
}
