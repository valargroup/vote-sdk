// Package votecommitment provides Go bindings to the Rust FFI function that
// computes the vote commitment hash via Poseidon.
//
// The vote commitment is Poseidon(DOMAIN_VC=1, round_id, shares_hash,
// proposal_id, vote_decision) — a canonical Pallas Fp element (32 bytes LE).
//
// It requires the Rust static library to be built first:
//
//	cargo build --release --manifest-path sdk/circuits/Cargo.toml
package votecommitment

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

// VoteCommitmentHash computes the vote commitment Poseidon hash from the
// given inputs. Returns a 32-byte canonical Pallas Fp element.
func VoteCommitmentHash(roundID, sharesHash [32]byte, proposalID, voteDecision uint32) ([32]byte, error) {
	var commitment [32]byte

	rc := C.sv_vote_commitment_hash(
		(*C.uint8_t)(unsafe.Pointer(&roundID[0])),
		(*C.uint8_t)(unsafe.Pointer(&sharesHash[0])),
		C.uint32_t(proposalID),
		C.uint32_t(voteDecision),
		(*C.uint8_t)(unsafe.Pointer(&commitment[0])),
	)

	switch rc {
	case 0:
		return commitment, nil
	case -1:
		return commitment, fmt.Errorf("votecommitment: invalid input (null pointer)")
	case -3:
		errMsg := C.GoString(C.sv_last_error())
		return commitment, fmt.Errorf("votecommitment: %s", errMsg)
	default:
		return commitment, fmt.Errorf("votecommitment: unexpected error code %d", rc)
	}
}
