// Package zkp provides interfaces for zero-knowledge proof verification.
//
// The vote chain uses three distinct ZKP circuits (all based on Halo2):
//   - ZKP #1 (Delegation): Proves valid delegation registration from a keystone-signed action.
//   - ZKP #2 (Vote Commitment): Proves valid vote commitment creation.
//   - ZKP #3 (Vote Share): Proves valid vote share reveal.
//
// The current implementation is a mock that always succeeds; it will later be
// replaced by a CGo binding to the Halo2 Rust verifier.
package zkp

// DelegationInputs contains the public inputs for ZKP #1 (delegation registration).
type DelegationInputs struct {
	Rk                  []byte   // Randomized spend auth key (32 bytes)
	SignedNoteNullifier []byte   // Nullifier of the dummy signed note
	CmxNew              []byte   // Output note commitment
	EncMemo             []byte   // Encrypted memo
	GovComm             []byte   // Governance commitment
	GovNullifiers       [][]byte // Up to 4 governance nullifiers
	VoteRoundId         []byte   // The vote round this delegation belongs to
	NcRoot              []byte   // Note commitment tree root from session state
	NullifierImtRoot    []byte   // Nullifier IMT root from session state
}

// VoteCommitmentInputs contains the public inputs for ZKP #2 (vote commitment).
type VoteCommitmentInputs struct {
	VanNullifier         []byte // Vote authority note nullifier
	VoteAuthorityNoteNew []byte // New vote authority note commitment
	VoteCommitment       []byte // The vote commitment
	ProposalId           uint32 // Which proposal this vote is for
	VoteRoundId          []byte // The vote round
	AnchorHeight         uint64 // Commitment tree anchor height used by the proof
	VoteCommTreeRoot     []byte // 32-byte Pallas Fp: tree root at AnchorHeight (from on-chain state)
	EaPk                 []byte // 32-byte compressed Pallas point: election authority public key (from session)
}

// VoteShareInputs contains the public inputs for ZKP #3 (reveal vote share).
type VoteShareInputs struct {
	ShareNullifier []byte // Share nullifier (prevents double-reveal)
	EncShare       []byte // 64 bytes: ElGamal ciphertext (encrypted vote share)
	ProposalId     uint32 // Which proposal
	VoteDecision   uint32 // The vote choice
	VoteRoundId    []byte // The vote round
	AnchorHeight   uint64 // Commitment tree anchor height used by the proof
}

// Verifier defines the interface for zero-knowledge proof verification.
// Each method corresponds to one of the three ZKP circuits used in the voting protocol.
type Verifier interface {
	// VerifyDelegation verifies ZKP #1: delegation registration proof.
	VerifyDelegation(proof []byte, inputs DelegationInputs) error

	// VerifyVoteCommitment verifies ZKP #2: vote commitment proof.
	VerifyVoteCommitment(proof []byte, inputs VoteCommitmentInputs) error

	// VerifyVoteShare verifies ZKP #3: vote share reveal proof.
	VerifyVoteShare(proof []byte, inputs VoteShareInputs) error
}

// MockVerifier is a mock implementation that always returns nil (success).
// Used during development until a real Halo2 verifier is integrated.
type MockVerifier struct{}

// VerifyDelegation always returns nil.
func (MockVerifier) VerifyDelegation(proof []byte, inputs DelegationInputs) error {
	return nil
}

// VerifyVoteCommitment always returns nil.
func (MockVerifier) VerifyVoteCommitment(proof []byte, inputs VoteCommitmentInputs) error {
	return nil
}

// VerifyVoteShare always returns nil.
func (MockVerifier) VerifyVoteShare(proof []byte, inputs VoteShareInputs) error {
	return nil
}

// NewMockVerifier returns a new mock ZKP verifier.
func NewMockVerifier() Verifier {
	return MockVerifier{}
}
