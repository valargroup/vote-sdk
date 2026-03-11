//! Address ownership gadget: shared CommitIvk + pk_d derivation.
//!
//! Encapsulates the common "address ownership" and optional "SpendAuthG mul"
//! logic used by:
//!
//! - **ZKP #1 (delegation)**: Condition 4 (spend authority: `[alpha]*SpendAuthG + ak_P` → rk),
//!   Condition 5 (CommitIvk & diversified address: `pk_d_signed = [ivk]*g_d_signed`).
//! - **ZKP #2 (vote proof)**: Condition 3 (spend authority: `ak = ExtractP([vsk]*SpendAuthG)`;
//!   address ownership: `vpk_pk_d = [ivk_v]*vpk_g_d` via CommitIvk).
//!
//! The same CommitIvk + pk_d derivation pattern is used in the main Orchard action
//! circuit for spend authority and diversified address integrity.

use halo2_proofs::{
    circuit::{AssignedCell, Layouter},
    plonk::Error,
};
use pasta_curves::pallas;

use orchard::constants::{OrchardFixedBases, OrchardFixedBasesFull};
use orchard::circuit::commit_ivk::CommitIvkChip;
use halo2_gadgets::ecc::{
    chip::EccChip,
    NonIdentityPoint, Point, ScalarFixed, ScalarVar,
};

use orchard::constants::{OrchardCommitDomains, OrchardHashDomains};
use halo2_gadgets::sinsemilla::chip::SinsemillaChip;

// ================================================================
// SpendAuthG fixed-base multiplication
// ================================================================

/// Computes `[scalar] * SpendAuthG` using the Orchard fixed base.
///
/// Thin wrapper around the shared gadget in `orchard::shared_primitives` –
/// see [`orchard::shared_primitives::spend_authority`] for the upstream reference.
///
/// Used by delegation (condition 4: alpha), vote proof (condition 3: vsk).
/// Returns the resulting curve point so the caller can e.g. add `ak_P` for rk
/// (delegation) or call `extract_p()` for ak (vote proof).
pub fn spend_auth_g_mul(
    ecc_chip: EccChip<OrchardFixedBases>,
    layouter: impl Layouter<pallas::Base>,
    _label: &str,
    scalar: ScalarFixed<pallas::Affine, EccChip<OrchardFixedBases>>,
) -> Result<Point<pallas::Affine, EccChip<OrchardFixedBases>>, Error> {
    // The shared crate's prove_spend_authority does [scalar]*SpendAuthG + ak_P.
    // For call sites that only need the [scalar]*SpendAuthG multiplication
    // (e.g. vote proof's [vsk]*SpendAuthG to derive ak), we use the same
    // underlying gadget via a dedicated mul helper.
    use halo2_gadgets::ecc::FixedPoint;
    let spend_auth_g = OrchardFixedBasesFull::SpendAuthG;
    let spend_auth_g = FixedPoint::from_inner(ecc_chip, spend_auth_g);
    let (point, _) = spend_auth_g.mul(layouter, scalar)?;
    Ok(point)
}

// ================================================================
// Prove address ownership (CommitIvk + [ivk]*g_d → constrain pk_d)
// ================================================================

/// Proves address ownership: `ivk = CommitIvk(ak, nk, rivk)`, then
/// `pk_d_claimed = [ivk] * g_d` (constrain derived to claimed).
///
/// Same constraint flow as the Orchard action circuit: CommitIvk yields ivk,
/// then variable-base mul and equality constraint. Used by ZKP #1 (delegation,
/// condition 5) and ZKP #2 (vote proof, condition 3).
///
/// # Arguments
///
/// * `sinsemilla_chip` – Sinsemilla chip used by CommitIvk.
/// * `ecc_chip` – ECC chip for scalar mul and equality.
/// * `commit_ivk_chip` – CommitIvk chip (canonicity gates).
/// * `layouter` – Circuit layouter.
/// * `label` – Namespace label for the region.
/// * `ak` – Spend validating key x-coordinate (e.g. `ExtractP(ak_P)` or `ExtractP([vsk]*SpendAuthG)`).
/// * `nk` – Nullifier deriving key.
/// * `rivk` – CommitIvk randomness.
/// * `g_d` – Diversified base (non-identity point).
/// * `pk_d_claimed` – Claimed diversified transmission key; constrained to equal `[ivk]*g_d`.
///
/// Returns the ivk cell so callers (e.g. delegation) can reuse it for per-note
/// diversified address checks.
#[allow(clippy::type_complexity)]
pub fn prove_address_ownership(
    sinsemilla_chip: SinsemillaChip<
        OrchardHashDomains,
        OrchardCommitDomains,
        OrchardFixedBases,
    >,
    ecc_chip: EccChip<OrchardFixedBases>,
    commit_ivk_chip: CommitIvkChip,
    mut layouter: impl Layouter<pallas::Base>,
    label: &str,
    ak: AssignedCell<pallas::Base, pallas::Base>,
    nk: AssignedCell<pallas::Base, pallas::Base>,
    rivk: ScalarFixed<pallas::Affine, EccChip<OrchardFixedBases>>,
    g_d: &NonIdentityPoint<pallas::Affine, EccChip<OrchardFixedBases>>,
    pk_d_claimed: &NonIdentityPoint<pallas::Affine, EccChip<OrchardFixedBases>>,
) -> Result<AssignedCell<pallas::Base, pallas::Base>, Error> {
    use orchard::circuit::commit_ivk::gadgets::commit_ivk;

    let ivk = commit_ivk(
        sinsemilla_chip,
        ecc_chip.clone(),
        commit_ivk_chip,
        layouter.namespace(|| alloc::format!("{label} CommitIvk")),
        ak,
        nk,
        rivk,
    )?;

    let ivk_cell = ivk.inner().clone();

    let ivk_scalar = ScalarVar::from_base(
        ecc_chip.clone(),
        layouter.namespace(|| alloc::format!("{label} ivk as scalar")),
        ivk.inner(),
    )?;

    let (derived_pk_d, _) = g_d.mul(
        layouter.namespace(|| alloc::format!("{label} [ivk] g_d")),
        ivk_scalar,
    )?;

    derived_pk_d.constrain_equal(
        layouter.namespace(|| alloc::format!("{label} pk_d equality")),
        pk_d_claimed,
    )?;

    Ok(ivk_cell)
}
