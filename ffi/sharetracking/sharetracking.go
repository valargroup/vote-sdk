//go:build halo2

// Package sharetracking provides Go bindings for the cheap share-nullifier
// hash used by helpers to avoid redundant ZKP 3 work.
package sharetracking

/*
#cgo LDFLAGS: -L${SRCDIR}/../../circuits/target/release -lshielded_vote_circuits -ldl -lm -lpthread
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation
#include "../../circuits/include/shielded_vote_circuits.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// ShareNullifierHash computes:
//
//	Poseidon(domain_tag_share_spend, vote_commitment, share_index, primary_blind)
//
// and returns the canonical 32-byte Pallas Fp encoding.
func ShareNullifierHash(voteCommitment [32]byte, shareIndex uint32, primaryBlind [32]byte) ([32]byte, error) {
	var nullifier [32]byte

	rc := C.sv_share_nullifier_hash(
		(*C.uint8_t)(unsafe.Pointer(&voteCommitment[0])),
		C.uint32_t(shareIndex),
		(*C.uint8_t)(unsafe.Pointer(&primaryBlind[0])),
		(*C.uint8_t)(unsafe.Pointer(&nullifier[0])),
	)

	switch rc {
	case 0:
		return nullifier, nil
	case -1:
		return nullifier, fmt.Errorf("sharetracking: invalid input (null pointer)")
	case -3:
		errMsg := C.GoString(C.sv_last_error())
		return nullifier, fmt.Errorf("sharetracking: %s", errMsg)
	case -6:
		errMsg := C.GoString(C.sv_last_error())
		return nullifier, fmt.Errorf("sharetracking: %s", errMsg)
	default:
		return nullifier, fmt.Errorf("sharetracking: unexpected error code %d", rc)
	}
}
