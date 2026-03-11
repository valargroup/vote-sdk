//! Shared Poseidon-based Merkle tree gadgets.
//!
//! Provides a conditional swap gate and a full Merkle path synthesis
//! function used by three circuits:
//!
//! - **ZKP #1** (delegation, condition 13): IMT non-membership, depth 29
//! - **ZKP #2** (vote proof, condition 1): VAN membership, depth 24
//! - **ZKP #3** (share reveal, condition 1): VC membership, depth 24
//!
//! The swap gate orders `(current, sibling)` into `(left, right)` based
//! on a position bit, then the path function hashes `Poseidon(left, right)`
//! at each level to walk from a leaf up to the root.

use halo2_proofs::{
    circuit::{AssignedCell, Layouter, Value},
    plonk::{self, Advice, Column, Constraints, Selector},
    poly::Rotation,
};
use pasta_curves::pallas;

use halo2_gadgets::{
    poseidon::{
        primitives::{self as poseidon, ConstantLength},
        Hash as PoseidonHash, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig,
    },
    utilities::bool_check,
};

use orchard::circuit::gadget::assign_free_advice;

// ================================================================
// MerkleSwapGate
// ================================================================

/// Conditional swap gate for Poseidon Merkle paths.
///
/// **Layout** (1 row, 5 advice columns):
///
/// | Col   | 0       | 1       | 2       | 3    | 4     |
/// |-------|---------|---------|---------|------|-------|
/// | Row 0 | pos_bit | current | sibling | left | right |
///
/// **Constraints**:
/// - `left = current + pos_bit * (sibling - current)`
/// - `left + right = current + sibling` (conservation)
/// - `bool_check(pos_bit)`
#[derive(Clone, Debug)]
pub struct MerkleSwapGate {
    pub selector: Selector,
    advices: [Column<Advice>; 5],
}

impl MerkleSwapGate {
    /// Configures the gate on `advices[0..5]`.
    pub fn configure(
        meta: &mut plonk::ConstraintSystem<pallas::Base>,
        advices: [Column<Advice>; 5],
    ) -> Self {
        let selector = meta.selector();

        meta.create_gate("Merkle conditional swap", |meta| {
            let q = meta.query_selector(selector);
            let pos_bit = meta.query_advice(advices[0], Rotation::cur());
            let current = meta.query_advice(advices[1], Rotation::cur());
            let sibling = meta.query_advice(advices[2], Rotation::cur());
            let left = meta.query_advice(advices[3], Rotation::cur());
            let right = meta.query_advice(advices[4], Rotation::cur());

            Constraints::with_selector(
                q,
                [
                    // pos_bit=0 → left=current; pos_bit=1 → left=sibling.
                    (
                        "swap left",
                        left.clone()
                            - current.clone()
                            - pos_bit.clone() * (sibling.clone() - current.clone()),
                    ),
                    // Conservation: left + right = current + sibling.
                    ("swap right", left + right - current - sibling),
                    // pos_bit must be boolean.
                    ("bool_check pos_bit", bool_check(pos_bit)),
                ],
            )
        });

        MerkleSwapGate { selector, advices }
    }

    /// Assigns a single swap row. Returns `(left, right)`.
    pub fn assign(
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
        self.selector.enable(region, offset)?;

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
// synthesize_poseidon_merkle_path
// ================================================================

/// Synthesizes a Poseidon Merkle path from `leaf` to the root.
///
/// At each of `DEPTH` levels (LSB-first):
/// 1. Witnesses the position bit and sibling hash.
/// 2. Conditionally swaps via [`MerkleSwapGate`].
/// 3. Hashes `Poseidon(left, right)` with P128Pow5T3.
///
/// Returns the computed root cell.
pub fn synthesize_poseidon_merkle_path<const DEPTH: usize>(
    swap_gate: &MerkleSwapGate,
    poseidon_config: &PoseidonConfig<pallas::Base, 3, 2>,
    layouter: &mut impl Layouter<pallas::Base>,
    advice_0: Column<Advice>,
    leaf: AssignedCell<pallas::Base, pallas::Base>,
    position: Value<u32>,
    path: Value<[pallas::Base; DEPTH]>,
    label: &str,
) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
    let mut current = leaf;

    for i in 0..DEPTH {
        let pos_bit = assign_free_advice(
            layouter.namespace(|| alloc::format!("{label} pos_bit {i}")),
            advice_0,
            position.map(|p| pallas::Base::from(((p >> i) & 1) as u64)),
        )?;

        let sibling = assign_free_advice(
            layouter.namespace(|| alloc::format!("{label} sibling {i}")),
            advice_0,
            path.map(|path| path[i]),
        )?;

        let (left, right) = layouter.assign_region(
            || alloc::format!("{label} swap level {i}"),
            |mut region| swap_gate.assign(&mut region, 0, &pos_bit, &current, &sibling),
        )?;

        let parent = {
            let hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<2>,
                3,
                2,
            >::init(
                PoseidonChip::construct(poseidon_config.clone()),
                layouter.namespace(|| alloc::format!("{label} hash init level {i}")),
            )?;
            hasher.hash(
                layouter.namespace(|| alloc::format!("{label} Poseidon(left, right) level {i}")),
                [left, right],
            )?
        };

        current = parent;
    }

    Ok(current)
}

// ================================================================
// Unit tests
// ================================================================

#[cfg(test)]
mod tests {
    use super::*;
    use alloc::vec::Vec;
    use halo2_proofs::{
        circuit::{Layouter, SimpleFloorPlanner},
        dev::MockProver,
        plonk::{Circuit, ConstraintSystem, Fixed, Instance},
    };

    /// Out-of-circuit Poseidon hash matching the in-circuit `Poseidon(left, right)`.
    fn poseidon_hash_2(a: pallas::Base, b: pallas::Base) -> pallas::Base {
        poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([a, b])
    }

    /// Computes a Merkle root out-of-circuit for test oracle comparison.
    fn merkle_root(leaf: pallas::Base, position: u32, path: &[pallas::Base]) -> pallas::Base {
        let mut current = leaf;
        for (i, &sibling) in path.iter().enumerate() {
            let (left, right) = if (position >> i) & 1 == 0 {
                (current, sibling)
            } else {
                (sibling, current)
            };
            current = poseidon_hash_2(left, right);
        }
        current
    }

    // ----------------------------------------------------------------
    // Minimal test circuit wrapping MerkleSwapGate + Poseidon path.
    //
    // Public instance layout: [expected_root].
    // ----------------------------------------------------------------

    const TEST_DEPTH: usize = 4;

    #[derive(Clone, Debug)]
    struct TestConfig {
        swap_gate: MerkleSwapGate,
        poseidon_config: PoseidonConfig<pallas::Base, 3, 2>,
        primary: Column<Instance>,
        advices: [Column<Advice>; 9],
    }

    #[derive(Clone)]
    struct TestCircuit {
        leaf: Value<pallas::Base>,
        position: Value<u32>,
        path: Value<[pallas::Base; TEST_DEPTH]>,
    }

    impl Default for TestCircuit {
        fn default() -> Self {
            Self {
                leaf: Value::unknown(),
                position: Value::unknown(),
                path: Value::unknown(),
            }
        }
    }

    impl Circuit<pallas::Base> for TestCircuit {
        type Config = TestConfig;
        type FloorPlanner = SimpleFloorPlanner;

        fn without_witnesses(&self) -> Self {
            Self::default()
        }

        fn configure(meta: &mut ConstraintSystem<pallas::Base>) -> TestConfig {
            let advices: [Column<Advice>; 9] = core::array::from_fn(|_| {
                let col = meta.advice_column();
                meta.enable_equality(col);
                col
            });

            let primary = meta.instance_column();
            meta.enable_equality(primary);

            let lagrange_coeffs: [Column<Fixed>; 8] =
                core::array::from_fn(|_| meta.fixed_column());
            meta.enable_constant(lagrange_coeffs[0]);

            let rc_a = lagrange_coeffs[2..5].try_into().unwrap();
            let rc_b = lagrange_coeffs[5..8].try_into().unwrap();

            let poseidon_config = PoseidonChip::configure::<poseidon::P128Pow5T3>(
                meta,
                advices[6..9].try_into().unwrap(),
                advices[5],
                rc_a,
                rc_b,
            );

            let swap_gate = MerkleSwapGate::configure(
                meta,
                [advices[0], advices[1], advices[2], advices[3], advices[4]],
            );

            TestConfig {
                swap_gate,
                poseidon_config,
                primary,
                advices,
            }
        }

        fn synthesize(
            &self,
            config: TestConfig,
            mut layouter: impl Layouter<pallas::Base>,
        ) -> Result<(), plonk::Error> {
            let leaf = assign_free_advice(
                layouter.namespace(|| "leaf"),
                config.advices[0],
                self.leaf,
            )?;

            let root = synthesize_poseidon_merkle_path::<TEST_DEPTH>(
                &config.swap_gate,
                &config.poseidon_config,
                &mut layouter,
                config.advices[0],
                leaf,
                self.position,
                self.path,
                "test merkle",
            )?;

            layouter.constrain_instance(root.cell(), config.primary, 0)?;
            Ok(())
        }
    }

    fn run_merkle(
        leaf: pallas::Base,
        position: u32,
        path: [pallas::Base; TEST_DEPTH],
        expected_root: pallas::Base,
    ) -> Result<(), Vec<halo2_proofs::dev::VerifyFailure>> {
        let circuit = TestCircuit {
            leaf: Value::known(leaf),
            position: Value::known(position),
            path: Value::known(path),
        };
        let prover = MockProver::run(11, &circuit, vec![vec![expected_root]]).unwrap();
        prover.verify()
    }

    #[test]
    fn position_zero_valid() {
        let leaf = pallas::Base::from(42u64);
        let path = [
            pallas::Base::from(1u64),
            pallas::Base::from(2u64),
            pallas::Base::from(3u64),
            pallas::Base::from(4u64),
        ];
        let root = merkle_root(leaf, 0, &path);
        assert_eq!(run_merkle(leaf, 0, path, root), Ok(()));
    }

    #[test]
    fn position_nonzero_valid() {
        let leaf = pallas::Base::from(99u64);
        let path = [
            pallas::Base::from(10u64),
            pallas::Base::from(20u64),
            pallas::Base::from(30u64),
            pallas::Base::from(40u64),
        ];
        // position=5 (binary 0101): swap at levels 0 and 2.
        let root = merkle_root(leaf, 5, &path);
        assert_eq!(run_merkle(leaf, 5, path, root), Ok(()));
    }

    #[test]
    fn all_right_child_valid() {
        let leaf = pallas::Base::from(7u64);
        let path = [
            pallas::Base::from(11u64),
            pallas::Base::from(22u64),
            pallas::Base::from(33u64),
            pallas::Base::from(44u64),
        ];
        // position=0xF (binary 1111): swap at every level.
        let root = merkle_root(leaf, 0xF, &path);
        assert_eq!(run_merkle(leaf, 0xF, path, root), Ok(()));
    }

    #[test]
    fn wrong_root_fails() {
        let leaf = pallas::Base::from(42u64);
        let path = [
            pallas::Base::from(1u64),
            pallas::Base::from(2u64),
            pallas::Base::from(3u64),
            pallas::Base::from(4u64),
        ];
        let wrong_root = pallas::Base::from(0xDEADu64);
        assert!(
            run_merkle(leaf, 0, path, wrong_root).is_err(),
            "wrong root must fail",
        );
    }

    #[test]
    fn wrong_leaf_fails() {
        let leaf = pallas::Base::from(42u64);
        let path = [
            pallas::Base::from(1u64),
            pallas::Base::from(2u64),
            pallas::Base::from(3u64),
            pallas::Base::from(4u64),
        ];
        let correct_root = merkle_root(leaf, 0, &path);
        let tampered_leaf = pallas::Base::from(43u64);
        assert!(
            run_merkle(tampered_leaf, 0, path, correct_root).is_err(),
            "tampered leaf must fail",
        );
    }

    #[test]
    fn wrong_sibling_fails() {
        let leaf = pallas::Base::from(42u64);
        let path = [
            pallas::Base::from(1u64),
            pallas::Base::from(2u64),
            pallas::Base::from(3u64),
            pallas::Base::from(4u64),
        ];
        let correct_root = merkle_root(leaf, 0, &path);
        let mut tampered_path = path;
        tampered_path[2] = pallas::Base::from(999u64);
        assert!(
            run_merkle(leaf, 0, tampered_path, correct_root).is_err(),
            "tampered sibling must fail",
        );
    }

    #[test]
    fn wrong_position_fails() {
        let leaf = pallas::Base::from(42u64);
        let path = [
            pallas::Base::from(1u64),
            pallas::Base::from(2u64),
            pallas::Base::from(3u64),
            pallas::Base::from(4u64),
        ];
        let root_at_pos_0 = merkle_root(leaf, 0, &path);
        assert!(
            run_merkle(leaf, 1, path, root_at_pos_0).is_err(),
            "wrong position must produce different root",
        );
    }
}
