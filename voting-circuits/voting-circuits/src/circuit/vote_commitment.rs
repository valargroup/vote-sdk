//! Vote Commitment integrity gadget.
//!
//! Shared 5-input Poseidon hash used by both ZKP #2 (vote proof,
//! condition 12) and ZKP #3 (share reveal, condition 2):
//!
//! ```text
//! vote_commitment = Poseidon(DOMAIN_VC, voting_round_id,
//!                            shares_hash, proposal_id, vote_decision)
//! ```
//!
//! The domain tag bakes into the verification key, preventing malicious
//! provers from substituting VAN commitments for vote commitments in the
//! shared tree.

use pasta_curves::pallas;

use halo2_gadgets::poseidon::{
    primitives::{self as poseidon, ConstantLength},
    Hash as PoseidonHash, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig,
};
use halo2_proofs::{
    circuit::{AssignedCell, Layouter},
    plonk,
};

// ================================================================
// Constants
// ================================================================

/// Domain tag for Vote Commitments.
///
/// Prepended as the first Poseidon input for domain separation from
/// VANs (`DOMAIN_VAN = 0`) in the shared vote commitment tree.
pub const DOMAIN_VC: u64 = 1;

// ================================================================
// Out-of-circuit helper
// ================================================================

/// Out-of-circuit vote commitment hash.
///
/// Computes:
/// ```text
/// Poseidon(DOMAIN_VC, voting_round_id, shares_hash, proposal_id, vote_decision)
/// ```
///
/// Used by builders and tests to compute the expected vote commitment.
/// Must produce identical output to the in-circuit gadget.
pub fn vote_commitment_hash(
    voting_round_id: pallas::Base,
    shares_hash: pallas::Base,
    proposal_id: pallas::Base,
    vote_decision: pallas::Base,
) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<5>, 3, 2>::init().hash([
        pallas::Base::from(DOMAIN_VC),
        voting_round_id,
        shares_hash,
        proposal_id,
        vote_decision,
    ])
}

// ================================================================
// In-circuit gadget
// ================================================================

/// In-circuit vote commitment hash.
///
/// Computes `Poseidon(domain_vc, voting_round_id, shares_hash, proposal_id, vote_decision)`
/// matching the out-of-circuit helper above.
///
/// Takes a `PoseidonConfig` so it can be used by any circuit that
/// configures a compatible Poseidon chip (P128Pow5T3, width 3, rate 2).
/// The `domain_vc` cell must be assigned via `assign_advice_from_constant`
/// so the value is baked into the verification key.
///
/// Used by ZKP #2 (vote proof, condition 12) and ZKP #3 (share reveal,
/// condition 2).
pub fn vote_commitment_poseidon(
    poseidon_config: &PoseidonConfig<pallas::Base, 3, 2>,
    layouter: &mut impl Layouter<pallas::Base>,
    label: &str,
    domain_vc: AssignedCell<pallas::Base, pallas::Base>,
    voting_round_id: AssignedCell<pallas::Base, pallas::Base>,
    shares_hash: AssignedCell<pallas::Base, pallas::Base>,
    proposal_id: AssignedCell<pallas::Base, pallas::Base>,
    vote_decision: AssignedCell<pallas::Base, pallas::Base>,
) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
    let message = [domain_vc, voting_round_id, shares_hash, proposal_id, vote_decision];
    let hasher = PoseidonHash::<
        pallas::Base,
        _,
        poseidon::P128Pow5T3,
        ConstantLength<5>,
        3,
        2,
    >::init(
        PoseidonChip::construct(poseidon_config.clone()),
        layouter.namespace(|| alloc::format!("{label} Poseidon init")),
    )?;
    hasher.hash(
        layouter.namespace(|| alloc::format!("{label} Poseidon(DOMAIN_VC, ...)")),
        message,
    )
}
