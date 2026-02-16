//! El Gamal encryption integrity gadget for vote proof (ZKP #2).
//!
//! Proves that four ciphertext pairs (enc_share_c1_x[i], enc_share_c2_x[i]) are
//! valid El Gamal encryptions of the corresponding plaintext shares under the
//! election authority public key: C1_i = [r_i]*G, C2_i = [v_i]*G + [r_i]*ea_pk.
//!
//! Used by the vote proof circuit (Condition 11: Encryption Integrity). The
//! caller assigns SpendAuthG constants, ea_pk instance cells, share and
//! randomness cells, and the witnessed enc_share x-coordinates; this gadget
//! constrains the ECC computation to match those cells.
//!
//! Also provides out-of-circuit helpers: `spend_auth_g_affine`, `base_to_scalar`,
//! and `elgamal_encrypt` for the builder and tests.

use halo2_proofs::{
    circuit::{AssignedCell, Layouter},
    plonk::Error,
};
use pasta_curves::arithmetic::CurveAffine;
use pasta_curves::pallas;

use crate::constants::OrchardFixedBases;
use halo2_gadgets::ecc::{
    chip::EccChip,
    NonIdentityPoint, ScalarVar,
};

// ================================================================
// Out-of-circuit helpers
// ================================================================

/// Returns the SpendAuthG generator point (used as G in El Gamal).
///
/// Same generator as Orchard spend authorization; reused as the El Gamal
/// generator so condition 3 and condition 11 share the same ECC chip.
pub fn spend_auth_g_affine() -> pallas::Affine {
    use group::Curve;
    let g = crate::constants::fixed_bases::spend_auth_g::generator();
    pallas::Point::from(g).to_affine()
}

/// Converts a `pallas::Base` field element to a `pallas::Scalar`.
///
/// For small values (< 2^30) the integer representation is identical in both
/// fields. Returns `None` if the byte representation exceeds the scalar modulus.
pub fn base_to_scalar(b: pallas::Base) -> Option<pallas::Scalar> {
    use ff::PrimeField;
    pallas::Scalar::from_repr(b.to_repr()).into()
}

/// Out-of-circuit El Gamal encryption under SpendAuthG.
///
/// Computes C1 = [r]*SpendAuthG, C2 = [v]*SpendAuthG + [r]*ea_pk.
/// Returns (c1_x, c2_x). Used by the builder and tests.
pub fn elgamal_encrypt(
    share_value: pallas::Base,
    randomness: pallas::Base,
    ea_pk: pallas::Point,
) -> (pallas::Base, pallas::Base) {
    use group::Curve;

    let g = pallas::Point::from(spend_auth_g_affine());
    let r_scalar = base_to_scalar(randomness)
        .expect("randomness must be < scalar field modulus");
    let v_scalar = base_to_scalar(share_value)
        .expect("share value must be < scalar field modulus");

    let c1 = g * r_scalar;
    let c2 = g * v_scalar + ea_pk * r_scalar;

    let c1_x = *c1.to_affine().coordinates().unwrap().x();
    let c2_x = *c2.to_affine().coordinates().unwrap().x();
    (c1_x, c2_x)
}

// ================================================================
// In-circuit gadget
// ================================================================

/// Proves that for each share i, (enc_c1_x[i], enc_c2_x[i]) is a valid
/// El Gamal encryption of share_cells[i] under randomness r_cells[i] and
/// public key ea_pk: C1_i = [r_i]*G, C2_i = [v_i]*G + [r_i]*ea_pk.
///
/// Caller must assign:
/// - `g_x_const`, `g_y_const`: SpendAuthG coordinates (advice-from-constant).
/// - `ea_pk_x_cell`, `ea_pk_y_cell`: ea_pk coordinates (advice-from-instance).
/// - `r_cells`: four advice cells holding the El Gamal randomness values.
/// - `share_cells`, `enc_c1_cells`, `enc_c2_cells`: share values and
///   ciphertext x-coordinates (already witnessed).
///
#[allow(clippy::too_many_arguments)]
pub(in crate::circuit) fn prove_elgamal_encryptions(
    ecc_chip: EccChip<OrchardFixedBases>,
    mut layouter: impl Layouter<pallas::Base>,
    namespace: &str,
    g_affine: pallas::Affine,
    g_x_const: AssignedCell<pallas::Base, pallas::Base>,
    g_y_const: AssignedCell<pallas::Base, pallas::Base>,
    ea_pk: halo2_proofs::circuit::Value<pallas::Affine>,
    ea_pk_x_cell: AssignedCell<pallas::Base, pallas::Base>,
    ea_pk_y_cell: AssignedCell<pallas::Base, pallas::Base>,
    share_cells: [AssignedCell<pallas::Base, pallas::Base>; 4],
    r_cells: [AssignedCell<pallas::Base, pallas::Base>; 4],
    enc_c1_cells: [AssignedCell<pallas::Base, pallas::Base>; 4],
    enc_c2_cells: [AssignedCell<pallas::Base, pallas::Base>; 4],
) -> Result<(), Error> {
    for i in 0..4 {
        // --- C1_i = [r_i] * G ---

        let g_c1 = NonIdentityPoint::new(
            ecc_chip.clone(),
            layouter.namespace(|| alloc::format!("{namespace} G for C1[{i}]")),
            halo2_proofs::circuit::Value::known(g_affine),
        )?;
        layouter.assign_region(
            || alloc::format!("{namespace} constrain G_c1[{i}] x"),
            |mut region| {
                region.constrain_equal(g_c1.inner().x().cell(), g_x_const.cell())
            },
        )?;
        layouter.assign_region(
            || alloc::format!("{namespace} constrain G_c1[{i}] y"),
            |mut region| {
                region.constrain_equal(g_c1.inner().y().cell(), g_y_const.cell())
            },
        )?;

        let r_i_scalar_c1 = ScalarVar::from_base(
            ecc_chip.clone(),
            layouter.namespace(|| alloc::format!("{namespace} r[{i}] to ScalarVar (C1)")),
            &r_cells[i],
        )?;

        let (c1_point, _) = g_c1.mul(
            layouter.namespace(|| alloc::format!("{namespace} [r_{i}] * G")),
            r_i_scalar_c1,
        )?;

        let c1_x = c1_point.extract_p().inner().clone();
        layouter.assign_region(
            || alloc::format!("{namespace} C1[{i}] x == enc_c1_x[{i}]"),
            |mut region| region.constrain_equal(c1_x.cell(), enc_c1_cells[i].cell()),
        )?;

        // --- C2_i = [v_i] * G + [r_i] * ea_pk ---

        let g_v = NonIdentityPoint::new(
            ecc_chip.clone(),
            layouter.namespace(|| alloc::format!("{namespace} G for vG[{i}]")),
            halo2_proofs::circuit::Value::known(g_affine),
        )?;
        layouter.assign_region(
            || alloc::format!("{namespace} constrain G_v[{i}] x"),
            |mut region| {
                region.constrain_equal(g_v.inner().x().cell(), g_x_const.cell())
            },
        )?;
        layouter.assign_region(
            || alloc::format!("{namespace} constrain G_v[{i}] y"),
            |mut region| {
                region.constrain_equal(g_v.inner().y().cell(), g_y_const.cell())
            },
        )?;

        let v_i_scalar = ScalarVar::from_base(
            ecc_chip.clone(),
            layouter.namespace(|| alloc::format!("{namespace} share[{i}] to ScalarVar")),
            &share_cells[i],
        )?;
        let (v_g_point, _) = g_v.mul(
            layouter.namespace(|| alloc::format!("{namespace} [v_{i}] * G")),
            v_i_scalar,
        )?;

        let ea_pk_point = NonIdentityPoint::new(
            ecc_chip.clone(),
            layouter.namespace(|| alloc::format!("{namespace} ea_pk for share[{i}]")),
            ea_pk,
        )?;
        layouter.assign_region(
            || alloc::format!("{namespace} constrain ea_pk[{i}] x"),
            |mut region| {
                region.constrain_equal(
                    ea_pk_point.inner().x().cell(),
                    ea_pk_x_cell.cell(),
                )
            },
        )?;
        layouter.assign_region(
            || alloc::format!("{namespace} constrain ea_pk[{i}] y"),
            |mut region| {
                region.constrain_equal(
                    ea_pk_point.inner().y().cell(),
                    ea_pk_y_cell.cell(),
                )
            },
        )?;

        let r_i_scalar_c2 = ScalarVar::from_base(
            ecc_chip.clone(),
            layouter.namespace(|| alloc::format!("{namespace} r[{i}] to ScalarVar (C2)")),
            &r_cells[i],
        )?;
        let (r_ea_pk_point, _) = ea_pk_point.mul(
            layouter.namespace(|| alloc::format!("{namespace} [r_{i}] * ea_pk")),
            r_i_scalar_c2,
        )?;

        let c2_point = v_g_point.add(
            layouter.namespace(|| alloc::format!("{namespace} C2[{i}] = vG + rP")),
            &r_ea_pk_point,
        )?;

        let c2_x = c2_point.extract_p().inner().clone();
        layouter.assign_region(
            || alloc::format!("{namespace} C2[{i}] x == enc_c2_x[{i}]"),
            |mut region| region.constrain_equal(c2_x.cell(), enc_c2_cells[i].cell()),
        )?;
    }
    Ok(())
}
