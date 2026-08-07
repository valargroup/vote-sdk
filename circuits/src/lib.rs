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
mod proof;
pub mod redpallas;
pub mod toy;
pub mod tx1;
pub mod votetree;

/// Re-export the delegation circuit's prove/verify API from the `voting-circuits` crate.
pub mod delegation {
    pub use voting_circuits::delegation::{
        create_delegation_proof, delegation_cached_keys, delegation_params, delegation_proving_key,
        warm_delegation_keys, Circuit, Instance, K,
    };

    pub mod builder {
        pub use voting_circuits::delegation::{
            build_delegation_bundle, DelegationBuildError, DelegationBundle, PaddedNoteData,
            PrecomputedRandomness, RealNoteInput,
        };
    }

    pub mod imt {
        pub use voting_circuits::delegation::{
            build_sentinel_list, derive_nullifier_domain, ImtError, ImtProofData, ImtProvider,
            SpacedLeafImtProvider, IMT_DEPTH,
        };
    }
}

/// Re-export the vote proof circuit's prove/verify API from the `voting-circuits` crate.
pub mod vote_proof {
    pub use voting_circuits::vote_proof::{
        vote_proof_cached_keys, vote_proof_params, vote_proof_proving_key, warm_vote_proof_keys,
        Circuit, Instance, K,
    };
}

/// Re-export the share reveal circuit's prove/verify API from the `voting-circuits` crate.
pub mod share_reveal {
    pub use voting_circuits::share_reveal::{
        create_share_reveal_proof, domain_tag_share_spend, share_nullifier_hash,
        share_reveal_cached_keys, share_reveal_params, share_reveal_proving_key,
        warm_share_reveal_keys, Circuit, Instance, K,
    };

    pub mod builder {
        pub use voting_circuits::share_reveal::{build_share_reveal, ShareRevealBundle};
    }
}
