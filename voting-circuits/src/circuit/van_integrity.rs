//! VAN (Vote Authority Note) integrity gadget.
//!
//! Shared two-layer Poseidon hash used by both ZKP #1 (delegation,
//! condition 7) and ZKP #2 (vote proof, conditions 2 and 6):
//!
//! ```text
//! van_comm_core = Poseidon(DOMAIN_VAN, g_d_x, pk_d_x, value,
//!                          voting_round_id, proposal_authority)
//! result = Poseidon(van_comm_core, van_comm_rand)
//! ```
//!
//! The first layer commits to the structural fields (domain tag,
//! diversified address, value, round, authority). The second layer
//! blinds the result with a random scalar, preventing observers from
//! brute-forcing the address or weight from the public commitment.

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

/// Domain tag for Vote Authority Notes.
///
/// Prepended as the first Poseidon input for domain separation from
/// Vote Commitments in the shared vote commitment tree.
/// `DOMAIN_VAN = 0` for VANs, `DOMAIN_VC = 1` for Vote Commitments.
pub const DOMAIN_VAN: u64 = 0;

// ================================================================
// Out-of-circuit helper
// ================================================================

/// Out-of-circuit VAN integrity hash.
///
/// Two-layer structure used by both ZKP #1 (delegation) and ZKP #2
/// (vote proof) for cross-circuit interoperability:
/// ```text
/// van_comm_core = Poseidon(DOMAIN_VAN, g_d_x, pk_d_x, value,
///                          voting_round_id, proposal_authority)
/// result = Poseidon(van_comm_core, van_comm_rand)
/// ```
///
/// Used by builders and tests to compute the expected VAN commitment.
pub fn van_integrity_hash(
    g_d_x: pallas::Base,
    pk_d_x: pallas::Base,
    value: pallas::Base,
    voting_round_id: pallas::Base,
    proposal_authority: pallas::Base,
    van_comm_rand: pallas::Base,
) -> pallas::Base {
    let van_comm_core =
        poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<6>, 3, 2>::init().hash([
            pallas::Base::from(DOMAIN_VAN),
            g_d_x,
            pk_d_x,
            value,
            voting_round_id,
            proposal_authority,
        ]);
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init()
        .hash([van_comm_core, van_comm_rand])
}

// ================================================================
// In-circuit gadget
// ================================================================

/// In-circuit VAN integrity hash.
///
/// Two-layer structure matching the out-of-circuit helper:
/// ```text
/// van_comm_core = Poseidon(domain_van, g_d_x, pk_d_x, value,
///                          voting_round_id, proposal_authority)
/// result = Poseidon(van_comm_core, van_comm_rand)
/// ```
///
/// Takes a `PoseidonConfig` so it can be used by any circuit that
/// configures a compatible Poseidon chip (P128Pow5T3, width 3, rate 2).
///
/// In ZKP #1 (delegation, condition 7) `proposal_authority` is
/// `MAX_PROPOSAL_AUTHORITY` (fresh delegation). In ZKP #2 (vote
/// proof) condition 2 passes `_old`, condition 6 passes `_new`
/// (from condition 5's decrement).
pub fn van_integrity_poseidon(
    poseidon_config: &PoseidonConfig<pallas::Base, 3, 2>,
    layouter: &mut impl Layouter<pallas::Base>,
    label: &str,
    domain_van: AssignedCell<pallas::Base, pallas::Base>,
    g_d_x: AssignedCell<pallas::Base, pallas::Base>,
    pk_d_x: AssignedCell<pallas::Base, pallas::Base>,
    value: AssignedCell<pallas::Base, pallas::Base>,
    voting_round_id: AssignedCell<pallas::Base, pallas::Base>,
    proposal_authority: AssignedCell<pallas::Base, pallas::Base>,
    van_comm_rand: AssignedCell<pallas::Base, pallas::Base>,
) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
    let core_message = [
        domain_van,
        g_d_x,
        pk_d_x,
        value,
        voting_round_id,
        proposal_authority,
    ];
    let poseidon_hasher_6 = PoseidonHash::<
        pallas::Base,
        _,
        poseidon::P128Pow5T3,
        ConstantLength<6>,
        3,
        2,
    >::init(
        PoseidonChip::construct(poseidon_config.clone()),
        layouter.namespace(|| alloc::format!("{label} core Poseidon init")),
    )?;
    let van_comm_core = poseidon_hasher_6.hash(
        layouter.namespace(|| alloc::format!("{label} Poseidon(core)")),
        core_message,
    )?;
    let poseidon_hasher_2 = PoseidonHash::<
        pallas::Base,
        _,
        poseidon::P128Pow5T3,
        ConstantLength<2>,
        3,
        2,
    >::init(
        PoseidonChip::construct(poseidon_config.clone()),
        layouter.namespace(|| alloc::format!("{label} final Poseidon init")),
    )?;
    poseidon_hasher_2.hash(
        layouter.namespace(|| alloc::format!("{label} Poseidon(core, rand)")),
        [van_comm_core, van_comm_rand],
    )
}
