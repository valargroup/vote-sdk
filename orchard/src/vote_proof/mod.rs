//! Vote proof ZKP circuit (ZKP #2).
//!
//! Proves that a vote is well-formed and authorized with respect to
//! delegation and the vote commitment tree. The circuit verifies 11
//! conditions; constraint logic is added incrementally.
//!
//! Currently implemented:
//! - **Condition 1**: VAN Membership (Poseidon Merkle path, `constrain_instance`).
//! - **Condition 2**: VAN Integrity (Poseidon hash).
//! - **Condition 4**: VAN Nullifier Integrity (nested Poseidon, `constrain_instance`).
//! - **Condition 6**: Proposal Authority Decrement (bit decomposition).
//! - **Condition 6**: New VAN Integrity (Poseidon hash, `constrain_instance`).
//! - **Condition 7**: Shares Sum Correctness (AddChip, `constrain_equal`).
//! - **Condition 8**: Shares Range (LookupRangeCheck, `[0, 2^30)`).
//! - **Condition 9**: Shares Hash Integrity (Poseidon `ConstantLength<8>`, `constrain_instance`).
//! - **Condition 10**: Encryption Integrity (ECC variable-base mul, `constrain_equal`).
//! - **Condition 11**: Vote Commitment Integrity (Poseidon `ConstantLength<4>`, `constrain_instance`).
//!
//! - **Condition 3**: Spend Authority (CommitIvk chain, `constrain_equal`).
//!
//! All 11 conditions are fully constrained.

pub mod builder;
pub mod circuit;
pub mod prove;

pub use crate::circuit::elgamal::{base_to_scalar, elgamal_encrypt, spend_auth_g_affine};
pub use circuit::{
    domain_van_nullifier, poseidon_hash_2, shares_hash, van_integrity_hash, van_nullifier_hash,
    vote_commitment_hash, Circuit, Config, Instance, K, DOMAIN_VAN, DOMAIN_VC, VOTE_COMM_TREE_DEPTH,
};
pub use builder::{build_vote_proof_from_delegation, VoteProofBuildError, VoteProofBundle};
pub use prove::{
    create_vote_proof, verify_vote_proof, verify_vote_proof_raw, vote_proof_params,
    vote_proof_proving_key,
};
