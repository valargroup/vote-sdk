// Package ncroot provides Go bindings to the Rust FFI function that computes
// the Orchard note commitment tree root from a hex-encoded frontier.
//
// The orchardTree field from lightwalletd's TreeState is a hex string encoding
// a serialized CommitmentTree. Computing the root requires Sinsemilla hashing
// which is only available in Rust. This package bridges Go ↔ Rust via CGo.
//
// It requires the Rust static library to be built first:
//
//	cargo build --release --manifest-path sdk/circuits/Cargo.toml
package ncroot

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

// ExtractNcRoot computes the Orchard note commitment tree root from a
// hex-encoded frontier string (the orchardTree field from lightwalletd).
//
// Returns the 32-byte Sinsemilla-based root.
func ExtractNcRoot(orchardTreeHex string) ([32]byte, error) {
	var root [32]byte

	if len(orchardTreeHex) == 0 {
		return root, fmt.Errorf("ncroot: empty orchard tree hex string")
	}

	hexBytes := []byte(orchardTreeHex)

	rc := C.zally_extract_nc_root(
		(*C.uint8_t)(unsafe.Pointer(&hexBytes[0])),
		C.size_t(len(hexBytes)),
		(*C.uint8_t)(unsafe.Pointer(&root[0])),
	)

	switch rc {
	case 0:
		return root, nil
	case -1:
		return root, fmt.Errorf("ncroot: invalid input")
	case -3:
		return root, fmt.Errorf("ncroot: failed to parse frontier or compute root")
	default:
		return root, fmt.Errorf("ncroot: unexpected error code %d", rc)
	}
}
