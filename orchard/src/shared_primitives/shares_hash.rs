//! Shared circuit gadget for the shares-hash computation used in ZKP #2 and ZKP #3.
//!
//! Both the vote-proof circuit (ZKP #2, condition 10) and the share-reveal
//! circuit (ZKP #3, condition 3) compute exactly the same two-level Poseidon
//! hash over the five encrypted shares:
//!
//! ```text
//! share_comm_i = Poseidon(blind_i, c1_i_x, c2_i_x)   for i ∈ 0..5
//! shares_hash  = Poseidon(share_comm_0, …, share_comm_4)
//! ```
//!
//! This module extracts those constraints into a single, auditable gadget so
//! that both circuits provably execute the same hash logic.

use halo2_proofs::{
    circuit::{AssignedCell, Layouter},
    plonk,
};
use halo2_gadgets::poseidon::{
    primitives::{self as poseidon, ConstantLength},
    Hash as PoseidonHash, Pow5Chip as PoseidonChip,
};
use pasta_curves::pallas;

/// Computes a single blinded per-share commitment in-circuit:
///
/// ```text
/// share_comm = Poseidon(blind, c1_x, c2_x)
/// ```
///
/// The `index` is used only for namespace labels and has no effect on the
/// constraint system.
pub(crate) fn hash_share_commitment_in_circuit(
    chip: PoseidonChip<pallas::Base, 3, 2>,
    mut layouter: impl Layouter<pallas::Base>,
    blind: AssignedCell<pallas::Base, pallas::Base>,
    enc_c1: AssignedCell<pallas::Base, pallas::Base>,
    enc_c2: AssignedCell<pallas::Base, pallas::Base>,
    index: usize,
) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
    let hasher = PoseidonHash::<
        pallas::Base, _, poseidon::P128Pow5T3, ConstantLength<3>, 3, 2,
    >::init(
        chip,
        layouter.namespace(|| alloc::format!("share_comm_{index} Poseidon init")),
    )?;
    hasher.hash(
        layouter.namespace(|| {
            alloc::format!("share_comm_{index} = Poseidon(blind_{index}, c1_{index}, c2_{index})")
        }),
        [blind, enc_c1, enc_c2],
    )
}

/// Computes the two-level shares hash in-circuit:
///
/// ```text
/// share_comm_i = Poseidon(blind_i, c1_i_x, c2_i_x)   for i ∈ 0..5
/// shares_hash  = Poseidon(share_comm_0, …, share_comm_4)
/// ```
///
/// # Arguments
///
/// * `poseidon_chip` — A closure that returns a fresh `PoseidonChip` each time
///   it is called. It is called six times: once per per-share hash and once for
///   the outer hash. Typically `|| config.poseidon_chip()`.
/// * `layouter` — The circuit layouter.
/// * `blinds` — The five per-share blind factors.
/// * `enc_c1` — The five El Gamal `C1` x-coordinates.
/// * `enc_c2` — The five El Gamal `C2` x-coordinates.
///
/// Returns the `shares_hash` cell.
pub(crate) fn compute_shares_hash_in_circuit(
    poseidon_chip: impl Fn() -> PoseidonChip<pallas::Base, 3, 2>,
    mut layouter: impl Layouter<pallas::Base>,
    blinds: [AssignedCell<pallas::Base, pallas::Base>; 5],
    enc_c1: [AssignedCell<pallas::Base, pallas::Base>; 5],
    enc_c2: [AssignedCell<pallas::Base, pallas::Base>; 5],
) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
    // Per-share blinded commitments: share_comm_i = Poseidon(blind_i, c1_i, c2_i)
    let [b0, b1, b2, b3, b4] = blinds;
    let [c1_0, c1_1, c1_2, c1_3, c1_4] = enc_c1;
    let [c2_0, c2_1, c2_2, c2_3, c2_4] = enc_c2;

    let share_comm_0 = hash_share_commitment_in_circuit(
        poseidon_chip(),
        layouter.namespace(|| "share_comm_0"),
        b0, c1_0, c2_0, 0,
    )?;
    let share_comm_1 = hash_share_commitment_in_circuit(
        poseidon_chip(),
        layouter.namespace(|| "share_comm_1"),
        b1, c1_1, c2_1, 1,
    )?;
    let share_comm_2 = hash_share_commitment_in_circuit(
        poseidon_chip(),
        layouter.namespace(|| "share_comm_2"),
        b2, c1_2, c2_2, 2,
    )?;
    let share_comm_3 = hash_share_commitment_in_circuit(
        poseidon_chip(),
        layouter.namespace(|| "share_comm_3"),
        b3, c1_3, c2_3, 3,
    )?;
    let share_comm_4 = hash_share_commitment_in_circuit(
        poseidon_chip(),
        layouter.namespace(|| "share_comm_4"),
        b4, c1_4, c2_4, 4,
    )?;

    // Outer hash: shares_hash = Poseidon(share_comm_0, …, share_comm_4)
    let hasher = PoseidonHash::<
        pallas::Base,
        _,
        poseidon::P128Pow5T3,
        ConstantLength<5>,
        3, // WIDTH
        2, // RATE
    >::init(
        poseidon_chip(),
        layouter.namespace(|| "shares_hash Poseidon init"),
    )?;
    hasher.hash(
        layouter.namespace(|| "shares_hash = Poseidon(share_comms)"),
        [share_comm_0, share_comm_1, share_comm_2, share_comm_3, share_comm_4],
    )
}
