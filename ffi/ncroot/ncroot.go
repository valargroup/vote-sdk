// Package ncroot computes the Ironwood note commitment tree root from a
// hex-encoded lightwalletd frontier.
//
// Ironwood uses the Orchard protocol's serialized frontier and Sinsemilla hash.
//
// It requires the Rust static library to be built first:
//
//	cargo build --release --manifest-path circuits/Cargo.toml
package ncroot

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

// ExtractNcRoot computes the Ironwood note commitment tree root from a
// hex-encoded frontier string.
//
// Returns the 32-byte Sinsemilla-based root.
func ExtractNcRoot(frontierHex string) ([32]byte, error) {
	var root [32]byte

	if len(frontierHex) == 0 {
		return root, fmt.Errorf("ncroot: empty Ironwood frontier")
	}

	hexBytes := []byte(frontierHex)

	rc := C.sv_extract_nc_root(
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
