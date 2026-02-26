//go:build halo2

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

// GenerateShareRevealProof generates a ZKP #3 (share reveal) proof using the
// Rust Halo2 prover via CGo. This is a CPU-intensive operation (~30-60s).
//
// Parameters:
//   - merklePath: 772-byte serialized Merkle path (from votetree.TreeHandle.Path)
//   - shareComms: 16 per-share Poseidon commitments, 32 bytes each
//   - primaryBlind: blind factor for the revealed share, 32 bytes
//   - encC1X: x-coordinate of the revealed share's C1, 32 bytes (compressed)
//   - encC2X: x-coordinate of the revealed share's C2, 32 bytes (compressed)
//   - shareIndex: which of the 16 shares (0..15)
//   - proposalID: proposal being voted on
//   - voteDecision: vote choice
//   - roundID: 32-byte raw Blake2b-256 round identifier
//
// Returns the proof bytes, share nullifier (32 bytes), tree root (32 bytes), or error.
func GenerateShareRevealProof(
	merklePath []byte,
	shareComms [16][32]byte,
	primaryBlind [32]byte,
	encC1X [32]byte,
	encC2X [32]byte,
	shareIndex uint32,
	proposalID, voteDecision uint32,
	roundID [32]byte,
) (proof []byte, nullifier [32]byte, treeRoot [32]byte, err error) {
	if len(merklePath) != 772 {
		return nil, nullifier, treeRoot, fmt.Errorf("merklePath must be 772 bytes, got %d", len(merklePath))
	}
	if shareIndex > 15 {
		return nil, nullifier, treeRoot, fmt.Errorf("shareIndex must be 0..15, got %d", shareIndex)
	}

	// Flatten shareComms into 512 contiguous bytes.
	var commsBuf [512]byte
	for i := 0; i < 16; i++ {
		copy(commsBuf[i*32:(i+1)*32], shareComms[i][:])
	}

	// Allocate proof output buffer (8 KiB is generous for Halo2 IPA proofs).
	const proofCapacity = 8192
	var proofBuf [proofCapacity]byte
	var proofLen C.size_t

	rc := C.zally_generate_share_reveal(
		(*C.uint8_t)(unsafe.Pointer(&merklePath[0])),
		C.size_t(len(merklePath)),
		(*C.uint8_t)(unsafe.Pointer(&commsBuf[0])),
		C.size_t(512),
		(*C.uint8_t)(unsafe.Pointer(&primaryBlind[0])),
		(*C.uint8_t)(unsafe.Pointer(&encC1X[0])),
		(*C.uint8_t)(unsafe.Pointer(&encC2X[0])),
		C.uint32_t(shareIndex),
		C.uint32_t(proposalID),
		C.uint32_t(voteDecision),
		(*C.uint8_t)(unsafe.Pointer(&roundID[0])),
		C.size_t(32),
		(*C.uint8_t)(unsafe.Pointer(&proofBuf[0])),
		C.size_t(proofCapacity),
		&proofLen,
		(*C.uint8_t)(unsafe.Pointer(&nullifier[0])),
		(*C.uint8_t)(unsafe.Pointer(&treeRoot[0])),
	)

	switch rc {
	case 0:
		proof = make([]byte, int(proofLen))
		copy(proof, proofBuf[:int(proofLen)])
		return proof, nullifier, treeRoot, nil
	case -1:
		return nil, nullifier, treeRoot, fmt.Errorf("share reveal: invalid inputs")
	case -3:
		return nil, nullifier, treeRoot, fmt.Errorf("share reveal: deserialization error (non-canonical Fp)")
	case -5:
		return nil, nullifier, treeRoot, fmt.Errorf("share reveal: proof generation failed")
	default:
		return nil, nullifier, treeRoot, fmt.Errorf("share reveal: unknown error code %d", rc)
	}
}
