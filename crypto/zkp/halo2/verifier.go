//go:build halo2

package halo2

import "github.com/z-cale/zally/crypto/zkp"

// Halo2Verifier implements zkp.Verifier using real Halo2 proof verification
// via CGo bindings to the Rust verifier. Currently only VerifyDelegation is
// wired to a real circuit (the toy circuit); the other methods are stubs that
// always succeed until those circuits are implemented.
type Halo2Verifier struct{}

// NewVerifier returns a Halo2Verifier backed by the Rust FFI library.
// This function is only available when built with the "halo2" build tag.
func NewVerifier() zkp.Verifier { return Halo2Verifier{} }

// VerifyDelegation verifies ZKP #1 using the toy circuit as a proof-of-concept.
// Convention: inputs.GovComm (32 bytes) is used as the public input to the toy
// circuit. This keeps inputs.Rk free for RedPallas signature verification.
// When the real delegation circuit is implemented, it will use ALL public inputs
// (including Rk) and this convention will be replaced.
func (h Halo2Verifier) VerifyDelegation(proof []byte, inputs zkp.DelegationInputs) error {
	return VerifyToyProof(proof, inputs.GovComm)
}

// VerifyVoteCommitment is a stub — real circuit not yet implemented.
func (h Halo2Verifier) VerifyVoteCommitment(proof []byte, inputs zkp.VoteCommitmentInputs) error {
	return nil
}

// VerifyVoteShare is a stub — real circuit not yet implemented.
func (h Halo2Verifier) VerifyVoteShare(proof []byte, inputs zkp.VoteShareInputs) error {
	return nil
}
