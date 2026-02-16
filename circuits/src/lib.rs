//! Zally Circuits: Halo2 ZKP circuits, RedPallas signature verification,
//! and FFI layer for Go via CGo.
//!
//! This crate provides:
//! - Circuit definitions for the Zally vote chain's three ZKP types
//! - RedPallas (RedDSA over Pallas) spend-auth signature verification
//! - C-compatible FFI functions for calling from Go via CGo
//!
//! Includes the toy circuit for pipeline validation, and the real
//! delegation circuit (ZKP #1) for production proof verification.

pub mod toy;
pub mod redpallas;
pub mod votetree;
pub mod ffi;

/// Re-export the delegation circuit's prove/verify API from the `orchard` crate.
pub mod delegation {
    pub use orchard::delegation::{
        verify_delegation_proof, verify_delegation_proof_raw,
        create_delegation_proof, delegation_params, delegation_proving_key,
        Circuit, Instance, K,
    };
    pub use orchard::delegation::builder;
    pub use orchard::delegation::imt;
}

/// Re-export the vote proof circuit's prove/verify API from the `orchard` crate.
pub mod vote_proof {
    pub use orchard::vote_proof::{
        verify_vote_proof, verify_vote_proof_raw,
        vote_proof_params, vote_proof_proving_key,
        Circuit, Instance, K,
    };
}

/// Re-export the share reveal circuit's prove/verify API from the `orchard` crate.
pub mod share_reveal {
    pub use orchard::share_reveal::{
        verify_share_reveal_proof, verify_share_reveal_proof_raw,
        create_share_reveal_proof, share_reveal_params, share_reveal_proving_key,
        domain_tag_share_spend, share_nullifier_hash,
        Circuit, Instance, K,
    };
    pub use orchard::share_reveal::builder;
}
