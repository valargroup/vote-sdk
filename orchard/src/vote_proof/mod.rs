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
//! - **Condition 5**: Proposal Authority Decrement (AddChip + range check).
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

pub mod circuit;

pub use circuit::{
    base_to_scalar, domain_van_nullifier, elgamal_encrypt, poseidon_hash_2, shares_hash,
    spend_auth_g_affine, van_integrity_hash, van_nullifier_hash, vote_commitment_hash, Circuit,
    Config, Instance, K, DOMAIN_VAN, DOMAIN_VC, VOTE_COMM_TREE_DEPTH,
};
