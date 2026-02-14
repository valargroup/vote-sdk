//! Vote proof ZKP circuit (ZKP #2).
//!
//! Proves that a vote is well-formed and authorized with respect to
//! delegation and the vote commitment tree. The circuit verifies 11
//! conditions; constraint logic is added incrementally.
//!
//! Currently implemented:
//! - **Condition 2**: VAN Integrity (Poseidon hash).
//! - **Condition 4**: VAN Nullifier Integrity (nested Poseidon, `constrain_instance`).

pub mod circuit;

pub use circuit::{
    domain_van_nullifier, van_integrity_hash, van_nullifier_hash, Circuit, Config, Instance, K,
    DOMAIN_VAN, VOTE_COMM_TREE_DEPTH,
};
