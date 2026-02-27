// Package roundid provides Go bindings to the Rust FFI function that derives
// vote_round_id from session fields via Poseidon hash.
//
// The round ID is a canonical Pallas Fp element (32 bytes LE), computed as
// Poseidon(<8 Fp elements>) from the 6 session setup fields. This ensures the
// round ID is always a valid field element for use in ZKP circuits.
//
// It requires the Rust static library to be built first:
//
//	cargo build --release --manifest-path sdk/circuits/Cargo.toml
package roundid

/*
#cgo LDFLAGS: -L${SRCDIR}/../../circuits/target/release -lzally_circuits -ldl -lm -lpthread
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation
#include "../../circuits/include/zally_circuits.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// DeriveRoundID computes vote_round_id from the 6 session setup fields
// via Poseidon hash (matching the Rust derive_round_id_poseidon function).
//
// Returns a 32-byte canonical Pallas Fp element.
func DeriveRoundID(
	snapshotHeight uint64,
	snapshotBlockhash []byte,
	proposalsHash []byte,
	voteEndTime uint64,
	nullifierImtRoot []byte,
	ncRoot []byte,
) ([32]byte, error) {
	var roundID [32]byte

	if len(snapshotBlockhash) != 32 || len(proposalsHash) != 32 ||
		len(nullifierImtRoot) != 32 || len(ncRoot) != 32 {
		return roundID, fmt.Errorf("roundid: all byte inputs must be 32 bytes")
	}

	rc := C.zally_derive_round_id(
		C.uint64_t(snapshotHeight),
		(*C.uint8_t)(unsafe.Pointer(&snapshotBlockhash[0])),
		(*C.uint8_t)(unsafe.Pointer(&proposalsHash[0])),
		C.uint64_t(voteEndTime),
		(*C.uint8_t)(unsafe.Pointer(&nullifierImtRoot[0])),
		(*C.uint8_t)(unsafe.Pointer(&ncRoot[0])),
		(*C.uint8_t)(unsafe.Pointer(&roundID[0])),
	)

	switch rc {
	case 0:
		return roundID, nil
	case -1:
		return roundID, fmt.Errorf("roundid: invalid input (null pointer)")
	case -3:
		errMsg := C.GoString(C.zally_last_error())
		return roundID, fmt.Errorf("roundid: %s", errMsg)
	default:
		return roundID, fmt.Errorf("roundid: unexpected error code %d", rc)
	}
}
