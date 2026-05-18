//! Shielded-Vote Circuits: Halo2 ZKP circuits, RedPallas signature verification,
//! and FFI layer for Go via CGo.
//!
//! This crate provides:
//! - Circuit definitions for the Shielded-Vote chain's three ZKP types
//! - RedPallas (RedDSA over Pallas) spend-auth signature verification
//! - C-compatible FFI functions for calling from Go via CGo
//!
//! Includes the toy circuit for pipeline validation, and the real
//! delegation circuit (ZKP #1) for production proof verification.

pub mod ffi;
pub mod nc_root;
pub mod redpallas;
pub mod toy;
pub mod votetree;

/// Re-export the delegation circuit's prove/verify API from the `voting-circuits` crate.
pub mod delegation {
    pub use voting_circuits::delegation::builder;
    pub use voting_circuits::delegation::imt;
    pub use voting_circuits::delegation::{
        create_delegation_proof, delegation_params, delegation_proving_key,
        Circuit, Instance, K,
    };
}

/// Re-export the vote proof circuit's prove/verify API from the `voting-circuits` crate.
pub mod vote_proof {
    pub use voting_circuits::vote_proof::{
        vote_proof_params, vote_proof_proving_key,
        Circuit, Instance, K,
    };
}

/// Re-export the share reveal circuit's prove/verify API from the `voting-circuits` crate.
pub mod share_reveal {
    pub use voting_circuits::share_reveal::builder;
    pub use voting_circuits::share_reveal::{
        create_share_reveal_proof, domain_tag_share_spend, share_nullifier_hash,
        share_reveal_cached_keys, share_reveal_params, share_reveal_proving_key,
        Circuit, Instance, K,
    };
}
