//! Delegation ZKP circuit.
//!
//! A single circuit proving all 15 conditions of the delegation ZKP,
//! including 4 per-note slots.
//! The builder layer creates padded notes for unused slots and
//! produces a single proof.

pub mod builder;
pub mod circuit;
pub mod imt;
pub mod imt_circuit;
pub mod prove;

pub use circuit::{Circuit, Instance, K};
pub use prove::{
    create_delegation_proof, delegation_params, delegation_proving_key,
    verify_delegation_proof, verify_delegation_proof_raw,
};
