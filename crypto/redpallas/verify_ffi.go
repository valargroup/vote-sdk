//go:build redpallas

// This file provides CGo bindings to the RedPallas signature verification
// function exported by the zally-circuits Rust static library.
//
// Only compiled when the "redpallas" build tag is set:
//
//	go test -tags redpallas ./crypto/redpallas/...
//
// It requires the Rust static library to be built first:
//
//	cargo build --release --manifest-path circuits/Cargo.toml
package redpallas

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

// VerifySpendAuthSig verifies a RedPallas SpendAuth signature using the
// Rust reddsa crate via CGo.
//
// Parameters:
//   - rk:      32-byte randomized spend authorization verification key
//   - sighash: 32-byte hash of the data that was signed
//   - sig:     64-byte RedPallas signature
//
// Returns nil on success, or an error describing the failure.
func VerifySpendAuthSig(rk, sighash, sig []byte) error {
	if len(rk) != 32 {
		return fmt.Errorf("redpallas: rk must be 32 bytes, got %d", len(rk))
	}
	if len(sighash) != 32 {
		return fmt.Errorf("redpallas: sighash must be 32 bytes, got %d", len(sighash))
	}
	if len(sig) != 64 {
		return fmt.Errorf("redpallas: sig must be 64 bytes, got %d", len(sig))
	}

	rc := C.zally_verify_redpallas_sig(
		(*C.uint8_t)(unsafe.Pointer(&rk[0])),
		C.size_t(len(rk)),
		(*C.uint8_t)(unsafe.Pointer(&sighash[0])),
		C.size_t(len(sighash)),
		(*C.uint8_t)(unsafe.Pointer(&sig[0])),
		C.size_t(len(sig)),
	)

	switch rc {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("redpallas: invalid inputs")
	case -2:
		return fmt.Errorf("redpallas: signature verification failed")
	case -3:
		return fmt.Errorf("redpallas: verification key deserialization error")
	default:
		return fmt.Errorf("redpallas: unknown error code %d", rc)
	}
}
