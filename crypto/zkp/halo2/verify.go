//go:build halo2

// Package halo2 provides CGo bindings to the Halo2 Rust proof verifier.
//
// This package is only compiled when the "halo2" build tag is set:
//
//	go test -tags halo2 ./crypto/zkp/halo2/...
//
// It requires the Rust static library to be built first:
//
//	cargo build --release --manifest-path sdk/circuits/Cargo.toml
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
	"encoding/binary"
	"fmt"
	"unsafe"

	"github.com/z-cale/zally/crypto/zkp"
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

// VerifyDelegationProof verifies a real delegation circuit proof (ZKP #1)
// using the Rust verifier via CGo.
//
// The inputs are serialized as 11 × 32-byte chunks (352 bytes):
//
//	[nf_signed, rk_compressed, cmx_new, van_cmx, vote_round_id,
//	 nc_root, nf_imt_root, gov_null_1, gov_null_2, gov_null_3, gov_null_4]
//
// The Rust FFI decompresses rk into (rk_x, rk_y) for the circuit's 12 field elements.
//
// Returns nil on success, or an error describing the failure.
func VerifyDelegationProof(proof []byte, inputs zkp.DelegationInputs) error {
	if len(proof) == 0 {
		return fmt.Errorf("delegation proof is empty")
	}

	// Serialize the DelegationInputs into 11 × 32-byte flat buffer.
	// Order must match the Rust FFI expectation.
	const chunkSize = 32
	const numChunks = 11
	buf := make([]byte, numChunks*chunkSize)

	// Helper: copy exactly 32 bytes from src into buf at offset, zero-padding if shorter.
	copyChunk := func(offset int, src []byte) error {
		if len(src) != chunkSize {
			return fmt.Errorf("expected %d bytes at offset %d, got %d", chunkSize, offset, len(src))
		}
		copy(buf[offset:offset+chunkSize], src)
		return nil
	}

	// Slot 0: nf_signed (SignedNoteNullifier)
	if err := copyChunk(0*chunkSize, inputs.SignedNoteNullifier); err != nil {
		return fmt.Errorf("nf_signed: %w", err)
	}
	// Slot 1: rk (compressed Pallas point)
	if err := copyChunk(1*chunkSize, inputs.Rk); err != nil {
		return fmt.Errorf("rk: %w", err)
	}
	// Slot 2: cmx_new
	if err := copyChunk(2*chunkSize, inputs.CmxNew); err != nil {
		return fmt.Errorf("cmx_new: %w", err)
	}
	// Slot 3: van_cmx
	if err := copyChunk(3*chunkSize, inputs.VanCmx); err != nil {
		return fmt.Errorf("van_cmx: %w", err)
	}
	// Slot 4: vote_round_id
	if err := copyChunk(4*chunkSize, inputs.VoteRoundId); err != nil {
		return fmt.Errorf("vote_round_id: %w", err)
	}
	// Slot 5: nc_root
	if err := copyChunk(5*chunkSize, inputs.NcRoot); err != nil {
		return fmt.Errorf("nc_root: %w", err)
	}
	// Slot 6: nf_imt_root
	if err := copyChunk(6*chunkSize, inputs.NullifierImtRoot); err != nil {
		return fmt.Errorf("nf_imt_root: %w", err)
	}
	// Slots 7–10: gov_nullifiers (pad to 4 with zeros)
	for i := 0; i < 4; i++ {
		offset := (7 + i) * chunkSize
		if i < len(inputs.GovNullifiers) && len(inputs.GovNullifiers[i]) == chunkSize {
			copy(buf[offset:offset+chunkSize], inputs.GovNullifiers[i])
		}
		// Else: already zero-filled by make().
	}

	rc := C.zally_verify_delegation_proof(
		(*C.uint8_t)(unsafe.Pointer(&proof[0])),
		C.size_t(len(proof)),
		(*C.uint8_t)(unsafe.Pointer(&buf[0])),
		C.size_t(len(buf)),
	)

	switch rc {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("delegation halo2: invalid inputs")
	case -2:
		return fmt.Errorf("delegation halo2: proof verification failed")
	case -3:
		return fmt.Errorf("delegation halo2: internal deserialization error")
	default:
		return fmt.Errorf("delegation halo2: unknown error code %d", rc)
	}
}

// VerifyVoteProof verifies a real vote proof circuit proof (ZKP #2)
// using the Rust verifier via CGo.
//
// The inputs are serialized as 10 × 32-byte chunks (320 bytes), matching the
// circuit's 11 public inputs (ea_pk is decompressed to x,y in Rust):
//
//	[van_nullifier, r_vpk_x, r_vpk_y, vote_authority_note_new, vote_commitment,
//	 vote_comm_tree_root, anchor_height_le, proposal_id_le, voting_round_id, ea_pk_compressed]
//
// Returns nil on success, or an error describing the failure.
func VerifyVoteProof(proof []byte, inputs zkp.VoteCommitmentInputs) error {
	if len(proof) == 0 {
		return fmt.Errorf("vote proof is empty")
	}

	// Serialize VoteCommitmentInputs into 10 × 32-byte flat buffer (condition 4: r_vpk in-circuit).
	const chunkSize = 32
	const numChunks = 10
	buf := make([]byte, numChunks*chunkSize)

	copyChunk := func(offset int, src []byte) error {
		if len(src) != chunkSize {
			return fmt.Errorf("expected %d bytes at offset %d, got %d", chunkSize, offset, len(src))
		}
		copy(buf[offset:offset+chunkSize], src)
		return nil
	}

	// Slot 0: van_nullifier (Fp)
	if err := copyChunk(0*chunkSize, inputs.VanNullifier); err != nil {
		return fmt.Errorf("van_nullifier: %w", err)
	}
	// Slots 1–2: r_vpk_x, r_vpk_y (condition 4: Spend Authority)
	if err := copyChunk(1*chunkSize, inputs.RVpkX); err != nil {
		return fmt.Errorf("r_vpk_x: %w", err)
	}
	if err := copyChunk(2*chunkSize, inputs.RVpkY); err != nil {
		return fmt.Errorf("r_vpk_y: %w", err)
	}
	// Slot 3: vote_authority_note_new (Fp)
	if err := copyChunk(3*chunkSize, inputs.VoteAuthorityNoteNew); err != nil {
		return fmt.Errorf("vote_authority_note_new: %w", err)
	}
	// Slot 4: vote_commitment (Fp)
	if err := copyChunk(4*chunkSize, inputs.VoteCommitment); err != nil {
		return fmt.Errorf("vote_commitment: %w", err)
	}
	// Slot 5: vote_comm_tree_root (Fp, from on-chain state)
	if err := copyChunk(5*chunkSize, inputs.VoteCommTreeRoot); err != nil {
		return fmt.Errorf("vote_comm_tree_root: %w", err)
	}
	// Slot 6: anchor_height (uint64 LE, zero-padded to 32 bytes)
	binary.LittleEndian.PutUint64(buf[6*chunkSize:], inputs.AnchorHeight)
	// Slot 7: proposal_id (uint32 LE, zero-padded to 32 bytes)
	binary.LittleEndian.PutUint32(buf[7*chunkSize:], inputs.ProposalId)
	// Slot 8: voting_round_id (Fp)
	if err := copyChunk(8*chunkSize, inputs.VoteRoundId); err != nil {
		return fmt.Errorf("voting_round_id: %w", err)
	}
	// Slot 9: ea_pk (compressed Pallas point, from session)
	if err := copyChunk(9*chunkSize, inputs.EaPk); err != nil {
		return fmt.Errorf("ea_pk: %w", err)
	}

	rc := C.zally_verify_vote_proof(
		(*C.uint8_t)(unsafe.Pointer(&proof[0])),
		C.size_t(len(proof)),
		(*C.uint8_t)(unsafe.Pointer(&buf[0])),
		C.size_t(len(buf)),
	)

	switch rc {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("vote proof halo2: invalid inputs")
	case -2:
		return fmt.Errorf("vote proof halo2: proof verification failed")
	case -3:
		return fmt.Errorf("vote proof halo2: internal deserialization error")
	default:
		return fmt.Errorf("vote proof halo2: unknown error code %d", rc)
	}
}
