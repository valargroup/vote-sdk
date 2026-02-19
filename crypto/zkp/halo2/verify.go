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
	"encoding/hex"
	"fmt"
	"unsafe"

	"github.com/z-cale/zally/crypto/zkp"
)

// pallasFpModulus is the Pallas base field modulus in big-endian byte order:
// p = 0x40000000000000000000000000000000224698fc094cf91b992d30ed00000001
var pallasFpModulus = [32]byte{
	0x40, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x22, 0x46, 0x98, 0xfc, 0x09, 0x4c, 0xf9, 0x1b,
	0x99, 0x2d, 0x30, 0xed, 0x00, 0x00, 0x00, 0x01,
}

// isCanonicalPallasFp checks whether a 32-byte little-endian value is
// strictly less than the Pallas base field modulus p.
func isCanonicalPallasFp(b []byte) bool {
	// Compare in big-endian order (byte 31 is the most significant).
	for i := 31; i >= 0; i-- {
		be := 31 - i // index into big-endian modulus
		if b[i] < pallasFpModulus[be] {
			return true
		}
		if b[i] > pallasFpModulus[be] {
			return false
		}
	}
	// Equal to p — not canonical.
	return false
}

// validatePallasFp returns an error if b is not a canonical 32-byte
// little-endian Pallas Fp element. The name is included in the error
// message to identify which field is invalid.
func validatePallasFp(name string, b []byte) error {
	if len(b) != 32 {
		return fmt.Errorf("%s: expected 32 bytes, got %d", name, len(b))
	}
	if !isCanonicalPallasFp(b) {
		return fmt.Errorf("%s is not a canonical Pallas field element (got 0x%s)", name, hex.EncodeToString(b))
	}
	return nil
}

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

	// Validate Fp fields before the FFI call so we get a clear error
	// naming the exact field, instead of the opaque "-3" from Rust.
	// Slots skipped: rk (slot 1, compressed point), vote_round_id (slot 4, wide-reduced in Rust).
	if err := validatePallasFp("nf_signed", inputs.SignedNoteNullifier); err != nil {
		return err
	}
	if err := validatePallasFp("cmx_new", inputs.CmxNew); err != nil {
		return err
	}
	if err := validatePallasFp("van_cmx", inputs.VanCmx); err != nil {
		return err
	}
	if err := validatePallasFp("nc_root", inputs.NcRoot); err != nil {
		return err
	}
	if err := validatePallasFp("nf_imt_root", inputs.NullifierImtRoot); err != nil {
		return err
	}
	for i := 0; i < len(inputs.GovNullifiers); i++ {
		if len(inputs.GovNullifiers[i]) == chunkSize {
			if err := validatePallasFp(fmt.Sprintf("gov_nullifier_%d", i+1), inputs.GovNullifiers[i]); err != nil {
				return err
			}
		}
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

	// Validate Fp fields before the FFI call.
	// Slots skipped: anchor_height (slot 6, uint64), proposal_id (slot 7, uint32),
	// voting_round_id (slot 8, wide-reduced in Rust), ea_pk (slot 9, compressed point).
	if err := validatePallasFp("van_nullifier", inputs.VanNullifier); err != nil {
		return err
	}
	if err := validatePallasFp("r_vpk_x", inputs.RVpkX); err != nil {
		return err
	}
	if err := validatePallasFp("r_vpk_y", inputs.RVpkY); err != nil {
		return err
	}
	if err := validatePallasFp("vote_authority_note_new", inputs.VoteAuthorityNoteNew); err != nil {
		return err
	}
	if err := validatePallasFp("vote_commitment", inputs.VoteCommitment); err != nil {
		return err
	}
	if err := validatePallasFp("vote_comm_tree_root", inputs.VoteCommTreeRoot); err != nil {
		return err
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

// VerifyShareRevealProof verifies a real share reveal circuit proof (ZKP #3)
// using the Rust verifier via CGo.
//
// The inputs are serialized as 7 × 32-byte chunks (224 bytes):
//
//	[share_nullifier, enc_share_c1_x, enc_share_c2_x, proposal_id,
//	 vote_decision, vote_comm_tree_root, voting_round_id]
//
// All values are plain Fp elements (32-byte LE canonical encoding).
// enc_share is split into C1 and C2 x-coordinates (sign bit cleared).
//
// Returns nil on success, or an error describing the failure.
func VerifyShareRevealProof(proof []byte, inputs zkp.VoteShareInputs) error {
	if len(proof) == 0 {
		return fmt.Errorf("share reveal proof is empty")
	}

	const chunkSize = 32
	const numChunks = 7
	buf := make([]byte, numChunks*chunkSize)

	// Slot 0: share_nullifier
	if len(inputs.ShareNullifier) != chunkSize {
		return fmt.Errorf("share_nullifier must be %d bytes, got %d", chunkSize, len(inputs.ShareNullifier))
	}
	copy(buf[0:32], inputs.ShareNullifier)

	// Slots 1-2: enc_share_c1_x, enc_share_c2_x
	// EncShare is 64 bytes: C1 (32 bytes) || C2 (32 bytes), as compressed Pallas points.
	// Extract x-coordinates by clearing the sign bit (bit 7 of byte 31).
	if len(inputs.EncShare) != 64 {
		return fmt.Errorf("enc_share must be 64 bytes, got %d", len(inputs.EncShare))
	}
	copy(buf[32:64], inputs.EncShare[:32])
	buf[63] &= 0x7F // clear sign bit for c1_x
	copy(buf[64:96], inputs.EncShare[32:64])
	buf[95] &= 0x7F // clear sign bit for c2_x

	// Slot 3: proposal_id (encode as 32-byte LE Fp)
	binary.LittleEndian.PutUint64(buf[96:104], uint64(inputs.ProposalId))
	// bytes 104..128 already zero

	// Slot 4: vote_decision (encode as 32-byte LE Fp)
	binary.LittleEndian.PutUint64(buf[128:136], uint64(inputs.VoteDecision))
	// bytes 136..160 already zero

	// Slot 5: vote_comm_tree_root
	if len(inputs.VoteCommTreeRoot) != chunkSize {
		return fmt.Errorf("vote_comm_tree_root must be %d bytes, got %d", chunkSize, len(inputs.VoteCommTreeRoot))
	}
	copy(buf[160:192], inputs.VoteCommTreeRoot)

	// Slot 6: voting_round_id
	if len(inputs.VoteRoundId) != chunkSize {
		return fmt.Errorf("voting_round_id must be %d bytes, got %d", chunkSize, len(inputs.VoteRoundId))
	}
	copy(buf[192:224], inputs.VoteRoundId)

	// Validate Fp fields before the FFI call.
	// Slots skipped: proposal_id (slot 3, small uint), vote_decision (slot 4, small uint),
	// voting_round_id (slot 6, wide-reduced in Rust).
	if err := validatePallasFp("share_nullifier", inputs.ShareNullifier); err != nil {
		return err
	}
	// enc_share c1_x and c2_x are already in the buffer with sign bits cleared.
	if err := validatePallasFp("enc_share_c1_x", buf[32:64]); err != nil {
		return err
	}
	if err := validatePallasFp("enc_share_c2_x", buf[64:96]); err != nil {
		return err
	}
	if err := validatePallasFp("vote_comm_tree_root", inputs.VoteCommTreeRoot); err != nil {
		return err
	}

	rc := C.zally_verify_share_reveal_proof(
		(*C.uint8_t)(unsafe.Pointer(&proof[0])),
		C.size_t(len(proof)),
		(*C.uint8_t)(unsafe.Pointer(&buf[0])),
		C.size_t(len(buf)),
	)

	switch rc {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("share reveal halo2: invalid inputs")
	case -2:
		return fmt.Errorf("share reveal halo2: proof verification failed")
	case -3:
		return fmt.Errorf("share reveal halo2: internal deserialization error")
	default:
		return fmt.Errorf("share reveal halo2: unknown error code %d", rc)
	}
}
