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
pub(crate) mod verifier_wire;
pub mod votetree;

/// Re-export the delegation circuit's prove/verify API from the `voting-circuits` crate.
pub mod delegation {
    pub use voting_circuits::delegation::{
        create_delegation_proof, delegation_params, delegation_proving_key,
        verify_delegation_proof, warm_delegation_keys, Circuit, Instance, CMX_NEW_PUBLIC_OFFSET,
        DOM_PUBLIC_OFFSET,
        GOV_NULL_1_PUBLIC_OFFSET, GOV_NULL_2_PUBLIC_OFFSET, GOV_NULL_3_PUBLIC_OFFSET,
        GOV_NULL_4_PUBLIC_OFFSET, GOV_NULL_5_PUBLIC_OFFSET, GOV_NULL_PUBLIC_OFFSETS, K,
        NC_ROOT_PUBLIC_OFFSET, NF_IMT_ROOT_PUBLIC_OFFSET, NF_SIGNED_PUBLIC_OFFSET,
        RK_X_PUBLIC_OFFSET, RK_Y_PUBLIC_OFFSET, VAN_COMM_PUBLIC_OFFSET,
        VOTE_ROUND_ID_PUBLIC_OFFSET,
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
        verify_vote_proof, vote_proof_cached_keys, vote_proof_params, vote_proof_proving_key,
        warm_vote_proof_keys, Circuit, Instance, EA_PK_X_PUBLIC_OFFSET, EA_PK_Y_PUBLIC_OFFSET, K,
        PROPOSAL_ID_PUBLIC_OFFSET,
        R_VPK_X_PUBLIC_OFFSET, R_VPK_Y_PUBLIC_OFFSET, VAN_NULLIFIER_PUBLIC_OFFSET,
        VOTE_AUTHORITY_NOTE_NEW_PUBLIC_OFFSET, VOTE_COMMITMENT_PUBLIC_OFFSET,
        VOTE_COMM_TREE_ANCHOR_HEIGHT_PUBLIC_OFFSET, VOTE_COMM_TREE_ROOT_PUBLIC_OFFSET,
        VOTING_ROUND_ID_PUBLIC_OFFSET,
    };
}

/// Re-export the share reveal circuit's prove/verify API from the `voting-circuits` crate.
pub mod share_reveal {
    pub use voting_circuits::share_reveal::{
        create_share_reveal_proof, domain_tag_share_spend, share_nullifier_hash,
        share_reveal_cached_keys, share_reveal_params, share_reveal_proving_key,
        verify_share_reveal_proof, warm_share_reveal_keys, Circuit, Instance,
        ENC_SHARE_C1_X_PUBLIC_OFFSET, ENC_SHARE_C1_Y_PUBLIC_OFFSET,
        ENC_SHARE_C2_X_PUBLIC_OFFSET, ENC_SHARE_C2_Y_PUBLIC_OFFSET, K, PROPOSAL_ID_PUBLIC_OFFSET,
        SHARE_NULLIFIER_PUBLIC_OFFSET, VOTE_COMM_TREE_ROOT_PUBLIC_OFFSET,
        VOTE_DECISION_PUBLIC_OFFSET, VOTING_ROUND_ID_PUBLIC_OFFSET,
    };

    pub mod builder {
        pub use voting_circuits::share_reveal::{build_share_reveal, ShareRevealBundle};
    }
}
