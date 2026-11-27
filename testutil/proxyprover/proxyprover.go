//go:build halo2 && redpallas && proxyenabled

// Package proxyprover binds the circuits crate's test-only provers
// (circuits/src/test_provers.rs) for the proxy delegation end-to-end test.
// A test importing it must link the library `make circuits-test-provers`
// builds before the production one, for example with CGO_LDFLAGS=-L<dir>.
package proxyprover

/*
#cgo LDFLAGS: -lshielded_vote_circuits -ldl -lm -lpthread
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation
#include "../../circuits/include/shielded_vote_circuits.h"
#include "../../circuits/include/shielded_vote_circuits_test_provers.h"
*/
import "C"

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// MerklePathLen is the length of a serialized Merkle path, as
// votetree.TreeHandle.Path returns it.
const MerklePathLen = 772

// Share is one of a commitment's 16 El Gamal shares, with what ZKP3 needs to
// reveal it.
type Share struct {
	C1, C2 [32]byte // compressed ciphertext points
	Blind  [32]byte // the blind of the share's commitment
	Value  uint64   // the plaintext, in ballots
}

// EncShare returns the 64-byte ciphertext a reveal carries.
func (s Share) EncShare() []byte {
	return append(append([]byte(nil), s.C1[:]...), s.C2[:]...)
}

// Commitment is a DC's or vote commitment's 16 shares and share
// commitments; a ZKP3 reveal of any share takes all 16 commitments.
type Commitment struct {
	Shares     [16]Share
	ShareComms [16][32]byte
}

// VAN is a full-authority vote authority note of a test identity: the seed
// of its spending key (address index 1), its weight and randomness, and its
// round. RoundID and Rand are canonical Pallas Fp encodings.
type VAN struct {
	KeySeed [32]byte
	Ballots uint64
	Rand    [32]byte
	RoundID [32]byte
}

// Commitment returns v's commitment, the van_cmx a registration appends.
func (v VAN) Commitment() ([32]byte, error) {
	var out [32]byte
	rc := C.sv_test_van_commitment(bytePtr(v.KeySeed[:]), C.uint64_t(v.Ballots), bytePtr(v.Rand[:]), bytePtr(v.RoundID[:]), (*C.uint8_t)(unsafe.Pointer(&out[0])))
	return out, rustError("van commitment", rc)
}

// DelegationWitness is one ZKP4 DC: it delegates Ballots of VAN, the leaf
// that MerklePath (MerklePathLen bytes) authenticates under the tree root at
// AnchorHeight, to DelegateIndex. For a batch's later DC, VAN is its
// predecessor's successor and MerklePath that VAN's single-leaf path.
type DelegationWitness struct {
	VAN           VAN
	MerklePath    []byte
	AnchorHeight  uint64
	EaPk          [32]byte
	Alpha         uint64
	DelegateIndex uint32
	Ballots       uint64
	Slot          uint8
	Seed          [32]byte
	SingleShare   bool
}

// Delegation is a proved ZKP4 DC: its proof and public outputs, the key Rsk
// that signs for RVpk, and its shares.
type Delegation struct {
	Proof                []byte
	VanNullifier         [32]byte
	RVpk                 [32]byte
	VoteAuthorityNoteNew [32]byte
	DC                   [32]byte
	Root                 [32]byte
	DcSlotNullifier      [32]byte
	Rsk                  [32]byte
	Commitment
}

// ProveDelegation builds and proves w with the real ZKP4 circuit.
func ProveDelegation(w DelegationWitness) (*Delegation, error) {
	if len(w.MerklePath) != MerklePathLen {
		return nil, fmt.Errorf("proxyprover: Merkle path is %d bytes, want %d", len(w.MerklePath), MerklePathLen)
	}
	proof := make([]byte, types.MaxProofSize)
	var proofLen C.size_t
	out := make([]byte, C.SV_TEST_PROXY_DELEGATION_OUT_LEN)
	rc := C.sv_test_prove_proxy_delegation(
		bytePtr(w.VAN.KeySeed[:]), C.uint64_t(w.VAN.Ballots), bytePtr(w.VAN.Rand[:]), bytePtr(w.VAN.RoundID[:]),
		bytePtr(w.MerklePath), C.size_t(len(w.MerklePath)), C.uint64_t(w.AnchorHeight),
		bytePtr(w.EaPk[:]), C.uint64_t(w.Alpha), C.uint32_t(w.DelegateIndex), C.uint64_t(w.Ballots),
		C.uint8_t(w.Slot), bytePtr(w.Seed[:]), C.bool(w.SingleShare),
		(*C.uint8_t)(unsafe.Pointer(&proof[0])), C.size_t(len(proof)), &proofLen,
		(*C.uint8_t)(unsafe.Pointer(&out[0])), C.size_t(len(out)),
	)
	if err := rustError("proxy delegation", rc); err != nil {
		return nil, err
	}
	d := &Delegation{Proof: proof[:proofLen]}
	for i, field := range []*[32]byte{&d.VanNullifier, &d.RVpk, &d.VoteAuthorityNoteNew, &d.DC, &d.Root, &d.DcSlotNullifier, &d.Rsk} {
		copy(field[:], out[32*i:])
	}
	d.Commitment = readCommitment(out[7*32:])
	return d, nil
}

// VoteWitness is one ZKP2 vote for Decision on ProposalID by VAN, which
// holds Authority. VAN, MerklePath and AnchorHeight are as in
// DelegationWitness, a 0x06 batch's later vote spending its predecessor's
// new VAN.
type VoteWitness struct {
	VAN          VAN
	Authority    uint64
	MerklePath   []byte
	AnchorHeight uint64
	EaPk         [32]byte
	Alpha        uint64
	ProposalID   uint32
	Decision     uint32
}

// Vote is a proved ZKP2 vote: its proof and public outputs, the key Rsk that
// signs for RVpk, and its shares.
type Vote struct {
	Proof                []byte
	VanNullifier         [32]byte
	RVpk                 [32]byte
	VoteAuthorityNoteNew [32]byte
	VoteCommitment       [32]byte
	Root                 [32]byte
	Rsk                  [32]byte
	Commitment
}

// ProveVote builds and proves w with the real ZKP2 circuit, in the standard
// share layout.
func ProveVote(w VoteWitness) (*Vote, error) {
	if len(w.MerklePath) != MerklePathLen {
		return nil, fmt.Errorf("proxyprover: Merkle path is %d bytes, want %d", len(w.MerklePath), MerklePathLen)
	}
	proof := make([]byte, types.MaxProofSize)
	var proofLen C.size_t
	out := make([]byte, C.SV_TEST_VOTE_OUT_LEN)
	rc := C.sv_test_prove_vote(
		bytePtr(w.VAN.KeySeed[:]), C.uint64_t(w.VAN.Ballots), bytePtr(w.VAN.Rand[:]), bytePtr(w.VAN.RoundID[:]),
		bytePtr(w.MerklePath), C.size_t(len(w.MerklePath)), C.uint64_t(w.AnchorHeight),
		C.uint32_t(w.ProposalID), C.uint32_t(w.Decision), C.uint64_t(w.Authority),
		bytePtr(w.EaPk[:]), C.uint64_t(w.Alpha),
		(*C.uint8_t)(unsafe.Pointer(&proof[0])), C.size_t(len(proof)), &proofLen,
		(*C.uint8_t)(unsafe.Pointer(&out[0])), C.size_t(len(out)),
	)
	if err := rustError("vote", rc); err != nil {
		return nil, err
	}
	v := &Vote{Proof: proof[:proofLen]}
	for i, field := range []*[32]byte{&v.VanNullifier, &v.RVpk, &v.VoteAuthorityNoteNew, &v.VoteCommitment, &v.Root, &v.Rsk} {
		copy(field[:], out[32*i:])
	}
	v.Commitment = readCommitment(out[6*32:])
	return v, nil
}

// Sign signs msg with the RedPallas SpendAuth key whose scalar is sk and
// returns the key's verification key and the signature.
func Sign(sk [32]byte, msg []byte) (vk [32]byte, sig [64]byte, err error) {
	rc := C.sv_test_redpallas_sign(bytePtr(sk[:]), bytePtr(msg), C.size_t(len(msg)), (*C.uint8_t)(unsafe.Pointer(&vk[0])), (*C.uint8_t)(unsafe.Pointer(&sig[0])))
	return vk, sig, rustError("sign", rc)
}

// readCommitment parses 16 share records of SV_TEST_SHARE_RECORD_LEN bytes.
func readCommitment(b []byte) Commitment {
	var c Commitment
	for i := range c.Shares {
		record := b[i*C.SV_TEST_SHARE_RECORD_LEN:]
		share := &c.Shares[i]
		copy(share.C1[:], record[0:32])
		copy(share.C2[:], record[32:64])
		copy(share.Blind[:], record[64:96])
		copy(c.ShareComms[i][:], record[96:128])
		share.Value = binary.LittleEndian.Uint64(record[128:136])
	}
	return c
}

// bytePtr returns a pointer to b's first byte, or nil for an empty b.
func bytePtr(b []byte) *C.uint8_t {
	if len(b) == 0 {
		return nil
	}
	return (*C.uint8_t)(unsafe.Pointer(&b[0]))
}

// rustError returns nil for rc 0, else an error naming op with the message
// sv_last_error stored.
func rustError(op string, rc C.int32_t) error {
	if rc == 0 {
		return nil
	}
	return fmt.Errorf("proxyprover: %s failed with code %d: %s", op, rc, C.GoString(C.sv_last_error()))
}
