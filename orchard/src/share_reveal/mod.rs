//! Share Reveal ZKP circuit (ZKP #3).
//!
//! Proves that a publicly-revealed encrypted share came from a valid,
//! registered vote commitment — without revealing which one.
//!
//! The circuit verifies 5 conditions:
//! - **Condition 1**: VC Membership (Poseidon Merkle path).
//! - **Condition 2**: Vote Commitment Integrity (ConstantLength<4> Poseidon).
//! - **Condition 3**: Shares Hash Integrity (blinded per-share commitments,
//!   then ConstantLength<16> Poseidon over the 16 commitments).
//! - **Condition 4**: Share Membership (custom mux gate).
//! - **Condition 5**: Share Nullifier Integrity (4-layer Poseidon chain with
//!   `voting_round_id` binding).

pub mod builder;
pub mod circuit;
pub mod prove;

pub use circuit::{
    domain_tag_share_spend, share_nullifier_hash, Circuit, Config, Instance, K,
};
pub use prove::{
    create_share_reveal_proof, share_reveal_params, share_reveal_proving_key,
    verify_share_reveal_proof, verify_share_reveal_proof_raw,
};
