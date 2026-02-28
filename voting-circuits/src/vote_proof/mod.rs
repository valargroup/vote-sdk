//! Vote proof ZKP circuit (ZKP #2).
//!
//! Proves that a vote is well-formed and authorized with respect to
//! delegation and the vote commitment tree. The circuit verifies 12
//! conditions; all are fully constrained.
//!
//! - **Condition 1**: VAN Membership (Poseidon Merkle path, `constrain_instance`).
//! - **Condition 2**: VAN Integrity (Poseidon hash).
//! - **Condition 3**: Diversified Address Integrity (CommitIvk chain, `constrain_equal`).
//! - **Condition 4**: Spend Authority (fixed-base mul + point add, `constrain_instance`).
//! - **Condition 5**: VAN Nullifier Integrity (nested Poseidon, `constrain_instance`).
//! - **Condition 6**: Proposal Authority Decrement (AddChip + range check).
//! - **Condition 7**: New VAN Integrity (Poseidon hash, `constrain_instance`).
//! - **Condition 8**: Shares Sum Correctness (AddChip, `constrain_equal`).
//! - **Condition 9**: Shares Range (LookupRangeCheck, `[0, 2^30)`).
//! - **Condition 10**: Shares Hash Integrity (Poseidon `ConstantLength<16>` over 16 blinded share commitments; output flows to condition 12).
//! - **Condition 11**: Encryption Integrity (ECC variable-base mul, `constrain_equal`).
//! - **Condition 12**: Vote Commitment Integrity (Poseidon `ConstantLength<5>`, `constrain_instance`).

pub mod builder;
pub mod circuit;
pub(crate) mod authority_decrement;
pub mod prove;

pub use crate::circuit::elgamal::{base_to_scalar, elgamal_encrypt, spend_auth_g_affine};
pub use circuit::{
    domain_van_nullifier, poseidon_hash_2, share_commitment, shares_hash, van_integrity_hash,
    van_nullifier_hash, vote_commitment_hash, Circuit, Config, Instance, K, DOMAIN_VAN, DOMAIN_VC,
    VOTE_COMM_TREE_DEPTH,
};
pub use builder::{build_vote_proof_from_delegation, EncryptedShareOutput, VoteProofBuildError, VoteProofBundle};
pub use prove::{
    create_vote_proof, verify_vote_proof, verify_vote_proof_raw, vote_proof_params,
    vote_proof_proving_key,
};
