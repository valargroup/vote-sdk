//! Spend authority gadget – 1:1 copy from the upstream Orchard action circuit.
//!
//! Upstream source (as of 2025-05-01):
//!   <https://github.com/zcash/orchard/blob/main/src/circuit.rs#L542-L558>
//!
//! The Orchard action circuit proves spend authority as follows:
//!
//! ```text
//! rk = [alpha] * SpendAuthG + ak_P
//! ```
//!
//! where `alpha` is a randomizer scalar, `SpendAuthG` is the Orchard spend
//! authorization generator (a Pallas fixed base), and `ak_P` is the spend
//! validating key. The verifier then checks that the transaction signature is
//! valid under `rk`, linking the ZKP to the signature without revealing `ak`.
//!
//! Reference: <https://zips.z.cash/protocol/protocol.pdf#spendauthsig>
//!            (§ 4.15, Spend Authorization Signature)
//!
//! This module extracts that logic into a reusable gadget so that both the
//! Orchard action circuit and the voting delegation/vote-proof circuits can
//! share the exact same constraint code.

use halo2_proofs::{
    circuit::{Layouter, Value},
    plonk::{Column, Instance as InstanceColumn, Error},
};
use pasta_curves::pallas;

use crate::constants::{OrchardFixedBases, OrchardFixedBasesFull};
use halo2_gadgets::ecc::{
    chip::EccChip, FixedPoint, Point, ScalarFixed,
};

/// Proves spend authority: `rk = [alpha] * SpendAuthG + ak_P`, then constrains
/// `rk` to the public instance columns at the given rows.
///
/// This is a 1:1 copy of the spend authority check from the upstream Orchard
/// action circuit:
///   <https://github.com/zcash/orchard/blob/main/src/circuit.rs#L542-L558>
///
/// # Arguments
///
/// * `ecc_chip`     – The ECC chip used for curve arithmetic.
/// * `layouter`     – Circuit layouter.
/// * `alpha`        – The randomizer scalar witness value.
/// * `ak_P`         – The spend validating key as a curve point.
/// * `primary`      – The instance column for public inputs.
/// * `rk_x_row`     – Row index in `primary` for the rk x-coordinate.
/// * `rk_y_row`     – Row index in `primary` for the rk y-coordinate.
///
/// ---
///
/// **Upstream Orchard equivalent** (for easy review – this function produces
/// the exact same constraints):
///
/// ```text
/// // https://github.com/zcash/orchard/blob/main/src/circuit.rs
/// //
/// // Spend authority (https://p.z.cash/ZKS:action-spend-authority)
/// {
///     let alpha =
///         ScalarFixed::new(ecc_chip.clone(), layouter.namespace(|| "alpha"), self.alpha)?;
///
///     // alpha_commitment = [alpha] SpendAuthG
///     let (alpha_commitment, _) = {
///         let spend_auth_g = OrchardFixedBasesFull::SpendAuthG;
///         let spend_auth_g = FixedPoint::from_inner(ecc_chip.clone(), spend_auth_g);
///         spend_auth_g.mul(layouter.namespace(|| "[alpha] SpendAuthG"), alpha)?
///     };
///
///     // [alpha] SpendAuthG + ak_P
///     let rk = alpha_commitment.add(layouter.namespace(|| "rk"), &ak_P)?;
///
///     // Constrain rk to equal public input
///     layouter.constrain_instance(rk.inner().x().cell(), config.primary, RK_X)?;
///     layouter.constrain_instance(rk.inner().y().cell(), config.primary, RK_Y)?;
/// }
/// ```
#[allow(non_snake_case)]
pub fn prove_spend_authority(
    ecc_chip: EccChip<OrchardFixedBases>,
    mut layouter: impl Layouter<pallas::Base>,
    alpha: Value<pallas::Scalar>,
    ak_P: &Point<pallas::Affine, EccChip<OrchardFixedBases>>,
    primary: Column<InstanceColumn>,
    rk_x_row: usize,
    rk_y_row: usize,
) -> Result<(), Error> {
    // ---------------------------------------------------------------
    // 1:1 copy of the upstream Orchard action circuit spend authority.
    // https://github.com/zcash/orchard/blob/main/src/circuit.rs#L542-L558
    // ---------------------------------------------------------------

    let alpha =
        ScalarFixed::new(ecc_chip.clone(), layouter.namespace(|| "alpha"), alpha)?;

    // alpha_commitment = [alpha] SpendAuthG
    let (alpha_commitment, _) = {
        let spend_auth_g = OrchardFixedBasesFull::SpendAuthG;
        let spend_auth_g = FixedPoint::from_inner(ecc_chip, spend_auth_g);
        spend_auth_g.mul(layouter.namespace(|| "[alpha] SpendAuthG"), alpha)?
    };

    // [alpha] SpendAuthG + ak_P
    let rk = alpha_commitment.add(layouter.namespace(|| "rk"), ak_P)?;

    // Constrain rk to equal public input
    layouter.constrain_instance(rk.inner().x().cell(), primary, rk_x_row)?;
    layouter.constrain_instance(rk.inner().y().cell(), primary, rk_y_row)?;

    Ok(())
}
