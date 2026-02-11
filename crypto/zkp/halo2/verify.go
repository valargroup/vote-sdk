//go:build halo2

// Package halo2 provides CGo bindings to the Halo2 Rust proof verifier.
//
// This package is only compiled when the "halo2" build tag is set:
//
//	go test -tags halo2 ./crypto/zkp/halo2/...
//
// It requires the Rust static library to be built first:
//
//	cargo build --release --manifest-path circuits/Cargo.toml
//
// The library provides real ZKP verification on the Pallas curve using
// the zcash/halo2 proof system. Currently exposes a toy circuit verifier;
// real circuit verifiers (delegation, vote commitment, vote share) will
// be added as the circuits are implemented.
package halo2

/*
#cgo LDFLAGS: -L${SRCDIR}/../../../circuits/target/release -lzally_circuits -ldl -lm -lpthread
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation
#include "../../../circuits/include/zally_circuits.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// VerifyToyProof verifies a Halo2 proof for the toy circuit
// (constant * a^2 * b^2 = c) using the Rust verifier via CGo.
//
// Parameters:
//   - proof: serialized Halo2 proof bytes
//   - publicInput: the public input c as a 32-byte little-endian Pallas Fp element
//
// Returns nil on success, or an error describing the failure.
func VerifyToyProof(proof, publicInput []byte) error {
	if len(proof) == 0 {
		return fmt.Errorf("proof is empty")
	}
	if len(publicInput) != 32 {
		return fmt.Errorf("public input must be 32 bytes, got %d", len(publicInput))
	}

	rc := C.zally_verify_toy_proof(
		(*C.uint8_t)(unsafe.Pointer(&proof[0])),
		C.size_t(len(proof)),
		(*C.uint8_t)(unsafe.Pointer(&publicInput[0])),
		C.size_t(len(publicInput)),
	)

	switch rc {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("halo2: invalid inputs")
	case -2:
		return fmt.Errorf("halo2: proof verification failed")
	case -3:
		return fmt.Errorf("halo2: internal deserialization error")
	default:
		return fmt.Errorf("halo2: unknown error code %d", rc)
	}
}
