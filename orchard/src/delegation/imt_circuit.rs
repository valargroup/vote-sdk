//! IMT non-membership circuit gates and synthesis (condition 13).
//!
//! Extracted from `circuit.rs` for readability. Contains:
//! - `ImtSwapGate`: conditional swap at each Merkle level
//! - `IntervalGate`: interval inclusion check (low <= real_nf <= high)
//! - `synthesize_imt_non_membership`: orchestrates leaf hash → Merkle path → interval check

use halo2_proofs::{
    circuit::{AssignedCell, Layouter, Value},
    plonk::{self, Advice, Column, Constraints, Expression, Selector},
    poly::Rotation,
};
use pasta_curves::pallas;

use ff::Field;
use halo2_gadgets::{
    ecc::chip::EccConfig,
    poseidon::{
        primitives::{self as poseidon, ConstantLength},
        Hash as PoseidonHash, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig,
    },
    utilities::bool_check,
};

use crate::constants::OrchardFixedBases;

use super::imt::IMT_DEPTH;

// ================================================================
// ImtSwapGate
// ================================================================

/// Conditional swap gate for the IMT Poseidon Merkle path.
///
/// **Layout** (1 row):
/// - `advices[0]`: pos_bit (bool — left or right child)
/// - `advices[1]`: current (hash being walked up)
/// - `advices[2]`: sibling (authentication path node)
/// - `advices[3]`: left (output)
/// - `advices[4]`: right (output)
///
/// **Constraints**:
/// - `left = current + pos_bit * (sibling - current)`
/// - `left + right = current + sibling` (conservation)
/// - `bool_check(pos_bit)`
#[derive(Clone, Debug)]
pub(crate) struct ImtSwapGate {
    pub(crate) q_imt_swap: Selector,
    advices: [Column<Advice>; 5],
}

impl ImtSwapGate {
    /// Configures the gate. Uses `advices[0..5]`.
    pub(crate) fn configure(
        meta: &mut plonk::ConstraintSystem<pallas::Base>,
        advices: [Column<Advice>; 5],
    ) -> Self {
        let q_imt_swap = meta.selector();

        // IMT conditional swap gate (condition 13).
        // At each level of the Poseidon Merkle path, we need to place (current, sibling)
        // into (left, right) based on the position bit. If pos_bit=0, current is the
        // left child; if pos_bit=1, they swap.
        meta.create_gate("IMT conditional swap", |meta| {
            let q = meta.query_selector(q_imt_swap);
            let pos_bit = meta.query_advice(advices[0], Rotation::cur());
            let current = meta.query_advice(advices[1], Rotation::cur());
            let sibling = meta.query_advice(advices[2], Rotation::cur());
            let left = meta.query_advice(advices[3], Rotation::cur());
            let right = meta.query_advice(advices[4], Rotation::cur());

            Constraints::with_selector(
                q,
                [
                    // left = current + pos_bit * (sibling - current)
                    // i.e. left = current when pos_bit=0, left = sibling when pos_bit=1.
                    (
                        "swap left",
                        left.clone()
                            - current.clone()
                            - pos_bit.clone() * (sibling.clone() - current.clone()),
                    ),
                    // left + right = current + sibling (conservation: no values lost).
                    // given that left is oneof {current, sibling}, right is forced to be the other.
                    ("swap right", left + right - current - sibling),
                    // pos_bit must be 0 or 1.
                    ("bool_check pos_bit", bool_check(pos_bit)),
                ],
            )
        });

        ImtSwapGate {
            q_imt_swap,
            advices,
        }
    }

    /// Assigns a single swap row. Returns `(left, right)`.
    pub(crate) fn assign(
        &self,
        region: &mut halo2_proofs::circuit::Region<'_, pallas::Base>,
        offset: usize,
        pos_bit: &AssignedCell<pallas::Base, pallas::Base>,
        current: &AssignedCell<pallas::Base, pallas::Base>,
        sibling: &AssignedCell<pallas::Base, pallas::Base>,
    ) -> Result<
        (
            AssignedCell<pallas::Base, pallas::Base>,
            AssignedCell<pallas::Base, pallas::Base>,
        ),
        plonk::Error,
    > {
        self.q_imt_swap.enable(region, offset)?;

        let pos_bit_cell =
            pos_bit.copy_advice(|| "pos_bit", region, self.advices[0], offset)?;
        let current_cell =
            current.copy_advice(|| "current", region, self.advices[1], offset)?;
        let sibling_cell =
            sibling.copy_advice(|| "sibling", region, self.advices[2], offset)?;

        let swap = pos_bit_cell
            .value()
            .copied()
            .zip(current_cell.value().copied())
            .zip(sibling_cell.value().copied())
            .map(|((bit, cur), sib)| {
                if bit == pallas::Base::zero() {
                    (cur, sib)
                } else {
                    (sib, cur)
                }
            });

        let left = region.assign_advice(
            || "left",
            self.advices[3],
            offset,
            || swap.map(|(l, _)| l),
        )?;

        let right = region.assign_advice(
            || "right",
            self.advices[4],
            offset,
            || swap.map(|(_, r)| r),
        )?;

        Ok((left, right))
    }
}

// ================================================================
// IntervalGate
// ================================================================

/// Interval check gate proving `low <= real_nf <= high`.
///
/// **Layout** (2 rows):
/// - Row 0: `advices[0]`=low, `advices[1]`=high, `advices[2]`=real_nf
/// - Row 1: `advices[0]`=y, `advices[1]`=x, `advices[2]`=x_shifted
///
/// **Constraints**:
/// - `y = high - low`
/// - `x = real_nf - low`
/// - `x_shifted = 2^250 - y + x - 1`
///
/// NOTE: The 250-bit range checks are only sound when every IMT bracket
/// has width < 2^250. The IMT MUST be initialized with ~17 sentinel
/// nullifiers at multiples of 2^250 before any real nullifiers are
/// inserted. This invariant holds permanently once established, since
/// inserting a nullifier only splits a bracket into two smaller ones.
#[derive(Clone, Debug)]
pub(crate) struct IntervalGate {
    pub(crate) q_interval: Selector,
    advices: [Column<Advice>; 3],
}

impl IntervalGate {
    /// Configures the gate. Uses `advices[0..3]`.
    pub(crate) fn configure(
        meta: &mut plonk::ConstraintSystem<pallas::Base>,
        advices: [Column<Advice>; 3],
    ) -> Self {
        // Interval check gate (condition 13, (low, high) leaf model).
        // Proves low <= real_nf <= high by constraining:
        //   x = real_nf - low  (range-checked to [0, 2^250) outside)
        //   x_shifted = 2^250 - y + x - 1  (range-checked to [0, 2^250) outside)
        // where y = high - low (the interval width).
        let q_interval = meta.selector();
        meta.create_gate("Interval check", |meta| {
            let q = meta.query_selector(q_interval);
            let low = meta.query_advice(advices[0], Rotation::cur());
            let high = meta.query_advice(advices[1], Rotation::cur());
            let real_nf = meta.query_advice(advices[2], Rotation::cur());
            let y = meta.query_advice(advices[0], Rotation::next());
            let x = meta.query_advice(advices[1], Rotation::next());
            let x_shifted = meta.query_advice(advices[2], Rotation::next());

            let two_250 = Expression::Constant(pallas::Base::from(2u64).pow([250, 0, 0, 0]));
            let one = Expression::Constant(pallas::Base::one());

            Constraints::with_selector(
                q,
                [
                    // Interval width.
                    ("y = high - low", y.clone() - (high - low.clone())),
                    // Lower bound: x = real_nf - low.
                    // Range-checking x to [0, 2^250) proves real_nf >= low,
                    // since a negative difference wraps to a large field element.
                    ("x = real_nf - low", x.clone() - (real_nf - low)),
                    // Upper bound: x_shifted = 2^250 - y + x - 1.
                    // If real_nf <= high then x <= y, so x_shifted <= 2^250 - 1 (passes).
                    // If real_nf > high then x > y, so x_shifted >= 2^250 (fails range check).
                    (
                        "x_shifted = 2^250 - y + x - 1",
                        x_shifted - (two_250 - y + x - one),
                    ),
                ],
            )
        });

        IntervalGate {
            q_interval,
            advices,
        }
    }

    /// Assigns the interval check region. Returns `(x, x_shifted)` for range checking.
    pub(crate) fn assign(
        &self,
        region: &mut halo2_proofs::circuit::Region<'_, pallas::Base>,
        offset: usize,
        low: &AssignedCell<pallas::Base, pallas::Base>,
        high: &AssignedCell<pallas::Base, pallas::Base>,
        real_nf: &AssignedCell<pallas::Base, pallas::Base>,
    ) -> Result<
        (
            AssignedCell<pallas::Base, pallas::Base>,
            AssignedCell<pallas::Base, pallas::Base>,
        ),
        plonk::Error,
    > {
        self.q_interval.enable(region, offset)?;

        // Row 0: low, high, real_nf
        low.copy_advice(|| "low", region, self.advices[0], offset)?;
        high.copy_advice(|| "high", region, self.advices[1], offset)?;
        real_nf.copy_advice(|| "real_nf", region, self.advices[2], offset)?;

        // Row 1: witness the derived values constrained by q_interval.
        // y = interval width, x = lower bound offset, x_shifted = upper bound check.
        // x and x_shifted are range-checked to [0, 2^250) after this region.
        let y = region.assign_advice(
            || "y = high - low",
            self.advices[0],
            offset + 1,
            || {
                high.value()
                    .copied()
                    .zip(low.value().copied())
                    .map(|(e, s)| e - s)
            },
        )?;

        let x = region.assign_advice(
            || "x = real_nf - low",
            self.advices[1],
            offset + 1,
            || {
                real_nf
                    .value()
                    .copied()
                    .zip(low.value().copied())
                    .map(|(nf, s)| nf - s)
            },
        )?;

        let two_250 = pallas::Base::from(2u64).pow([250, 0, 0, 0]);
        let x_shifted = region.assign_advice(
            || "x_shifted = 2^250 - y + x - 1",
            self.advices[2],
            offset + 1,
            || {
                y.value()
                    .copied()
                    .zip(x.value().copied())
                    .map(|(y_val, x_val)| two_250 - y_val + x_val - pallas::Base::one())
            },
        )?;

        Ok((x, x_shifted))
    }
}

// ================================================================
// ImtNonMembershipConfig
// ================================================================

/// Bundles both IMT gates and the columns they need.
#[derive(Clone, Debug)]
pub(crate) struct ImtNonMembershipConfig {
    pub(crate) swap_gate: ImtSwapGate,
    pub(crate) interval_gate: IntervalGate,
    /// The first advice column, used for free-witness assignments (pos_bit, sibling, low, high).
    pub(crate) advice_0: Column<Advice>,
}

impl ImtNonMembershipConfig {
    /// Configures both IMT gates. Uses `advices[0..5]` for the swap gate and
    /// `advices[0..3]` for the interval gate (overlapping is fine — different selectors).
    pub(crate) fn configure(
        meta: &mut plonk::ConstraintSystem<pallas::Base>,
        advices: &[Column<Advice>; 10],
    ) -> Self {
        let swap_gate = ImtSwapGate::configure(
            meta,
            [advices[0], advices[1], advices[2], advices[3], advices[4]],
        );
        let interval_gate = IntervalGate::configure(
            meta,
            [advices[0], advices[1], advices[2]],
        );
        ImtNonMembershipConfig {
            swap_gate,
            interval_gate,
            advice_0: advices[0],
        }
    }
}

// ================================================================
// synthesize_imt_non_membership
// ================================================================

/// Synthesizes the IMT non-membership proof for a single note slot (condition 13).
///
/// Orchestrates:
/// 1. Witness low/high
/// 2. Poseidon leaf hash = Poseidon(low, high)
/// 3. 29-level Merkle path using `ImtSwapGate` at each level
/// 4. Interval check using `IntervalGate`
/// 5. Range checks on x, x_shifted to [0, 2^250)
///
/// Returns `imt_root` which the caller feeds into the `q_per_note` gate.
#[allow(clippy::too_many_arguments)]
pub(crate) fn synthesize_imt_non_membership(
    imt_config: &ImtNonMembershipConfig,
    poseidon_config: &PoseidonConfig<pallas::Base, 3, 2>,
    ecc_config: &EccConfig<OrchardFixedBases>,
    layouter: &mut impl Layouter<pallas::Base>,
    imt_low: Value<pallas::Base>,
    imt_high: Value<pallas::Base>,
    imt_leaf_pos: Value<u32>,
    imt_path: Value<[pallas::Base; IMT_DEPTH]>,
    real_nf: &AssignedCell<pallas::Base, pallas::Base>,
    slot: usize,
) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
    let s = slot;

    // Witness low and high explicitly.
    let imt_low_cell = {
        use crate::circuit::gadget::assign_free_advice;
        assign_free_advice(
            layouter.namespace(|| format!("note {s} imt_low")),
            imt_config.advice_0,
            imt_low,
        )?
    };

    let imt_high_cell = {
        use crate::circuit::gadget::assign_free_advice;
        assign_free_advice(
            layouter.namespace(|| format!("note {s} imt_high")),
            imt_config.advice_0,
            imt_high,
        )?
    };

    // Compute leaf hash: Poseidon(low, high).
    let leaf_hash = {
        let poseidon_hasher = PoseidonHash::<
            pallas::Base,
            _,
            poseidon::P128Pow5T3,
            ConstantLength<2>,
            3,
            2,
        >::init(
            PoseidonChip::construct(poseidon_config.clone()),
            layouter.namespace(|| format!("note {s} imt leaf hash init")),
        )?;
        poseidon_hasher.hash(
            layouter.namespace(|| format!("note {s} Poseidon(low, high)")),
            [imt_low_cell.clone(), imt_high_cell.clone()],
        )?
    };

    // Poseidon Merkle path from leaf_hash, 29 levels.
    // At each level, ImtSwapGate orders (current, sibling) by position bit,
    // then Poseidon(left, right) computes the parent.
    let mut current = leaf_hash;

    for i in 0..IMT_DEPTH {
        let pos_bit = {
            use crate::circuit::gadget::assign_free_advice;
            assign_free_advice(
                layouter.namespace(|| format!("note {s} imt pos_bit {i}")),
                imt_config.advice_0,
                imt_leaf_pos
                    .map(|p| pallas::Base::from(((p >> i) & 1) as u64)),
            )?
        };

        let sibling = {
            use crate::circuit::gadget::assign_free_advice;
            assign_free_advice(
                layouter.namespace(|| format!("note {s} imt sibling {i}")),
                imt_config.advice_0,
                imt_path.map(|path| path[i]),
            )?
        };

        let (left, right) = layouter.assign_region(
            || format!("note {s} imt swap level {i}"),
            |mut region| {
                imt_config.swap_gate.assign(
                    &mut region,
                    0,
                    &pos_bit,
                    &current,
                    &sibling,
                )
            },
        )?;

        let parent = {
            let poseidon_hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<2>,
                3,
                2,
            >::init(
                PoseidonChip::construct(poseidon_config.clone()),
                layouter.namespace(|| format!("note {s} imt path hash init level {i}")),
            )?;
            poseidon_hasher.hash(
                layouter.namespace(|| format!("note {s} Poseidon(left, right) level {i}")),
                [left, right],
            )?
        };
        current = parent;
    }
    // The computed root is checked against the public nf_imt_root in the
    // q_per_note gate in circuit.rs.
    let imt_root = current;

    // Interval check: prove low <= real_nf <= high.
    // The IntervalGate constrains y, x, x_shifted from the witnessed values.
    // Range checks on x and x_shifted enforce the interval inclusion.
    let (x, x_shifted) = layouter.assign_region(
        || format!("note {s} interval check"),
        |mut region| {
            imt_config.interval_gate.assign(
                &mut region,
                0,
                &imt_low_cell,
                &imt_high_cell,
                real_nf,
            )
        },
    )?;

    // Range checks enforce the interval inclusion.
    // x in [0, 2^250) proves low <= real_nf.
    // x_shifted in [0, 2^250) proves real_nf <= high.
    // 25 limbs × 10 bits = 250-bit range.
    ecc_config.lookup_config.copy_check(
        layouter.namespace(|| format!("note {s} x < 2^250")),
        x,
        25,
        true,
    )?;

    ecc_config.lookup_config.copy_check(
        layouter.namespace(|| format!("note {s} x_shifted < 2^250")),
        x_shifted,
        25,
        true,
    )?;

    Ok(imt_root)
}
