//go:build halo2

package halo2

import "github.com/z-cale/zally/crypto/zkp"

// IsMock is false when built with the "halo2" tag — the real FFI verifier is active.
const IsMock = false

// Halo2Verifier implements zkp.Verifier using real Halo2 proof verification
// via CGo bindings to the Rust verifier. VerifyDelegation uses the real
// 15-condition delegation circuit (K=14, 13 public inputs). VerifyVoteCommitment
// uses the real 11-condition vote proof circuit (K=14, 9 public inputs).
type Halo2Verifier struct{}

// NewVerifier returns a Halo2Verifier backed by the Rust FFI library.
// This function is only available when built with the "halo2" build tag.
func NewVerifier() zkp.Verifier { return Halo2Verifier{} }

// VerifyDelegation verifies ZKP #1 using the real delegation circuit.
// All 13 public inputs (nf_signed, rk, cmx_new, van_cmx, vote_round_id,
// nc_root, nf_imt_root, gov_null_1..5) are passed to the Rust verifier.
func (h Halo2Verifier) VerifyDelegation(proof []byte, inputs zkp.DelegationInputs) error {
	return VerifyDelegationProof(proof, inputs)
}

// VerifyVoteCommitment verifies ZKP #2 using the real vote proof circuit.
// All 9 public inputs (van_nullifier, vote_authority_note_new, vote_commitment,
// vote_comm_tree_root, anchor_height, proposal_id, voting_round_id, ea_pk_x,
// ea_pk_y) are passed to the Rust verifier.
func (h Halo2Verifier) VerifyVoteCommitment(proof []byte, inputs zkp.VoteCommitmentInputs) error {
	return VerifyVoteProof(proof, inputs)
}

// VerifyVoteShare verifies ZKP #3 using the real share reveal circuit.
func (h Halo2Verifier) VerifyVoteShare(proof []byte, inputs zkp.VoteShareInputs) error {
	return VerifyShareRevealProof(proof, inputs)
}
