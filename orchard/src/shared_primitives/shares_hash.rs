//! Shared circuit gadget for the shares-hash computation used in ZKP #2 and ZKP #3.
//!
//! Both the vote-proof circuit (ZKP #2, condition 10) and the share-reveal
//! circuit (ZKP #3, condition 3) compute exactly the same two-level Poseidon
//! hash over the sixteen encrypted shares:
//!
//! ```text
//! share_comm_i = Poseidon(blind_i, c1_i_x, c2_i_x)   for i ∈ 0..16
//! shares_hash  = Poseidon(share_comm_0, …, share_comm_15)
//! ```
//!
//! This module extracts those constraints into a single, auditable gadget so
//! that both circuits provably execute the same hash logic.

use halo2_proofs::{
    circuit::{AssignedCell, Layouter, Value},
    plonk,
};
use halo2_gadgets::poseidon::{
    primitives::{self as poseidon, ConstantLength},
    Hash as PoseidonHash, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig,
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
/// share_comm_i = Poseidon(blind_i, c1_i_x, c2_i_x)   for i ∈ 0..16
/// shares_hash  = Poseidon(share_comm_0, …, share_comm_15)
/// ```
///
/// # Arguments
///
/// * `poseidon_chip` — A closure that returns a fresh `PoseidonChip` each time
///   it is called. It is called 17 times: once per per-share hash and once for
///   the outer hash. Typically `|| config.poseidon_chip()`.
/// * `layouter` — The circuit layouter.
/// * `blinds` — The 16 per-share blind factors.
/// * `enc_c1` — The 16 El Gamal `C1` x-coordinates.
/// * `enc_c2` — The 16 El Gamal `C2` x-coordinates.
///
/// Returns the `shares_hash` cell.
pub(crate) fn compute_shares_hash_in_circuit(
    poseidon_chip: impl Fn() -> PoseidonChip<pallas::Base, 3, 2>,
    mut layouter: impl Layouter<pallas::Base>,
    blinds: [AssignedCell<pallas::Base, pallas::Base>; 16],
    enc_c1: [AssignedCell<pallas::Base, pallas::Base>; 16],
    enc_c2: [AssignedCell<pallas::Base, pallas::Base>; 16],
) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
    // Per-share blinded commitments: share_comm_i = Poseidon(blind_i, c1_i, c2_i)
    let share_comms: [_; 16] = blinds
        .into_iter()
        .zip(enc_c1)
        .zip(enc_c2)
        .enumerate()
        .map(|(i, ((blind, c1), c2))| {
            hash_share_commitment_in_circuit(
                poseidon_chip(),
                layouter.namespace(|| alloc::format!("share_comm_{i}")),
                blind, c1, c2, i,
            )
        })
        .collect::<Result<alloc::vec::Vec<_>, _>>()?
        .try_into()
        .expect("always 16 elements");

    // Outer hash: shares_hash = Poseidon(share_comm_0, …, share_comm_15)
    let hasher = PoseidonHash::<
        pallas::Base,
        _,
        poseidon::P128Pow5T3,
        ConstantLength<16>,
        3, // WIDTH
        2, // RATE
    >::init(
        poseidon_chip(),
        layouter.namespace(|| "shares_hash Poseidon init"),
    )?;
    hasher.hash(
        layouter.namespace(|| "shares_hash = Poseidon(share_comms)"),
        share_comms,
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    use ff::Field;
    use halo2_proofs::{
        circuit::floor_planner,
        dev::MockProver,
        plonk::{Advice, Column, ConstraintSystem, Fixed, Instance as InstanceColumn},
    };
    use rand::rngs::OsRng;

    use crate::circuit::vote_proof::circuit::{share_commitment, shares_hash};

    // ---------------------------------------------------------------
    // Shared minimal circuit config (Poseidon only, no ECC).
    // ---------------------------------------------------------------

    #[derive(Clone)]
    struct TestConfig {
        primary: Column<InstanceColumn>,
        advice: Column<Advice>,
        poseidon_config: PoseidonConfig<pallas::Base, 3, 2>,
    }

    impl TestConfig {
        fn configure(meta: &mut ConstraintSystem<pallas::Base>) -> Self {
            let primary = meta.instance_column();
            meta.enable_equality(primary);

            // 5 advice columns: [0] general witness, [1..4] Poseidon state.
            let advices: [Column<Advice>; 5] = core::array::from_fn(|_| meta.advice_column());
            for col in &advices {
                meta.enable_equality(*col);
            }

            let fixed: [Column<Fixed>; 6] = core::array::from_fn(|_| meta.fixed_column());
            // Dedicated constants column required by Poseidon strict range checks.
            let constants = meta.fixed_column();
            meta.enable_constant(constants);
            let poseidon_config = PoseidonChip::configure::<poseidon::P128Pow5T3>(
                meta,
                advices[1..4].try_into().unwrap(),
                advices[4],
                fixed[0..3].try_into().unwrap(),
                fixed[3..6].try_into().unwrap(),
            );

            TestConfig { primary, advice: advices[0], poseidon_config }
        }

        fn poseidon_chip(&self) -> PoseidonChip<pallas::Base, 3, 2> {
            PoseidonChip::construct(self.poseidon_config.clone())
        }
    }

    /// Witnesses a single field element into the advice column.
    fn witness(
        mut layouter: impl Layouter<pallas::Base>,
        col: Column<Advice>,
        val: Value<pallas::Base>,
    ) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
        layouter.assign_region(
            || "witness",
            |mut region| region.assign_advice(|| "val", col, 0, || val),
        )
    }

    // ================================================================
    // hash_share_commitment_in_circuit
    // ================================================================

    /// Minimal circuit: computes `hash_share_commitment_in_circuit` and
    /// constrains the result to instance row 0.
    #[derive(Clone, Default)]
    struct HashShareCommCircuit {
        blind: pallas::Base,
        c1_x: pallas::Base,
        c2_x: pallas::Base,
    }

    impl plonk::Circuit<pallas::Base> for HashShareCommCircuit {
        type Config = TestConfig;
        type FloorPlanner = floor_planner::V1;

        fn without_witnesses(&self) -> Self { Self::default() }

        fn configure(meta: &mut ConstraintSystem<pallas::Base>) -> Self::Config {
            TestConfig::configure(meta)
        }

        fn synthesize(
            &self,
            config: Self::Config,
            mut layouter: impl Layouter<pallas::Base>,
        ) -> Result<(), plonk::Error> {
            let blind = witness(layouter.namespace(|| "blind"), config.advice, Value::known(self.blind))?;
            let c1   = witness(layouter.namespace(|| "c1"),   config.advice, Value::known(self.c1_x))?;
            let c2   = witness(layouter.namespace(|| "c2"),   config.advice, Value::known(self.c2_x))?;

            let result = hash_share_commitment_in_circuit(
                config.poseidon_chip(),
                layouter.namespace(|| "hash_share_comm"),
                blind, c1, c2, 0,
            )?;
            layouter.constrain_instance(result.cell(), config.primary, 0)
        }
    }

    /// In-circuit result matches the native `share_commitment` helper.
    #[test]
    fn hash_share_commitment_matches_native() {
        let mut rng = OsRng;
        let blind = pallas::Base::random(&mut rng);
        let c1_x  = pallas::Base::random(&mut rng);
        let c2_x  = pallas::Base::random(&mut rng);

        let expected = share_commitment(blind, c1_x, c2_x);
        let circuit = HashShareCommCircuit { blind, c1_x, c2_x };
        // K=10 (1024 rows) is enough for a single Poseidon(3) region.
        let prover = MockProver::run(10, &circuit, vec![vec![expected]])
            .expect("MockProver::run failed");
        assert_eq!(prover.verify(), Ok(()));
    }

    /// Swapping c1 and c2 produces a different hash (input order matters).
    #[test]
    fn hash_share_commitment_input_order_matters() {
        let mut rng = OsRng;
        let blind = pallas::Base::random(&mut rng);
        let c1_x  = pallas::Base::random(&mut rng);
        let c2_x  = pallas::Base::random(&mut rng);

        // Supply swapped c1/c2 as the expected value — should fail to verify.
        let wrong = share_commitment(blind, c2_x, c1_x);
        let circuit = HashShareCommCircuit { blind, c1_x, c2_x };
        let prover = MockProver::run(10, &circuit, vec![vec![wrong]])
            .expect("MockProver::run failed");
        assert!(prover.verify().is_err());
    }

    // ================================================================
    // compute_shares_hash_in_circuit
    // ================================================================

    /// Minimal circuit: computes `compute_shares_hash_in_circuit` over 16
    /// shares and constrains the result to instance row 0.
    #[derive(Clone)]
    struct ComputeSharesHashCircuit {
        blinds: [pallas::Base; 16],
        enc_c1: [pallas::Base; 16],
        enc_c2: [pallas::Base; 16],
    }

    impl Default for ComputeSharesHashCircuit {
        fn default() -> Self {
            Self {
                blinds: [pallas::Base::zero(); 16],
                enc_c1: [pallas::Base::zero(); 16],
                enc_c2: [pallas::Base::zero(); 16],
            }
        }
    }

    impl plonk::Circuit<pallas::Base> for ComputeSharesHashCircuit {
        type Config = TestConfig;
        type FloorPlanner = floor_planner::V1;

        fn without_witnesses(&self) -> Self { Self::default() }

        fn configure(meta: &mut ConstraintSystem<pallas::Base>) -> Self::Config {
            TestConfig::configure(meta)
        }

        fn synthesize(
            &self,
            config: Self::Config,
            mut layouter: impl Layouter<pallas::Base>,
        ) -> Result<(), plonk::Error> {
            let mut blind_cells = alloc::vec::Vec::with_capacity(16);
            let mut c1_cells    = alloc::vec::Vec::with_capacity(16);
            let mut c2_cells    = alloc::vec::Vec::with_capacity(16);
            for i in 0..16 {
                blind_cells.push(witness(layouter.namespace(|| alloc::format!("blind_{i}")), config.advice, Value::known(self.blinds[i]))?);
                c1_cells.push(witness(layouter.namespace(|| alloc::format!("c1_{i}")),    config.advice, Value::known(self.enc_c1[i]))?);
                c2_cells.push(witness(layouter.namespace(|| alloc::format!("c2_{i}")),    config.advice, Value::known(self.enc_c2[i]))?);
            }
            let blinds: [AssignedCell<pallas::Base, pallas::Base>; 16] = blind_cells.try_into().unwrap();
            let enc_c1: [AssignedCell<pallas::Base, pallas::Base>; 16] = c1_cells.try_into().unwrap();
            let enc_c2: [AssignedCell<pallas::Base, pallas::Base>; 16] = c2_cells.try_into().unwrap();

            let result = compute_shares_hash_in_circuit(
                || config.poseidon_chip(),
                layouter.namespace(|| "compute_shares_hash"),
                blinds,
                enc_c1,
                enc_c2,
            )?;
            layouter.constrain_instance(result.cell(), config.primary, 0)
        }
    }

    /// In-circuit result matches the native `shares_hash` helper.
    #[test]
    fn compute_shares_hash_matches_native() {
        let mut rng = OsRng;
        let blinds: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let enc_c1: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let enc_c2: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));

        let expected = shares_hash(blinds, enc_c1, enc_c2);
        let circuit = ComputeSharesHashCircuit { blinds, enc_c1, enc_c2 };
        // K=12 (4096 rows) comfortably fits 17 chained Poseidon regions.
        let prover = MockProver::run(12, &circuit, vec![vec![expected]])
            .expect("MockProver::run failed");
        assert_eq!(prover.verify(), Ok(()));
    }

    /// Corrupting any single enc_c1 value changes the output.
    #[test]
    fn compute_shares_hash_wrong_enc_c1_fails() {
        let mut rng = OsRng;
        let blinds: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let enc_c1: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let enc_c2: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));

        let correct = shares_hash(blinds, enc_c1, enc_c2);

        // Corrupt enc_c1[2] in the circuit but keep the correct expected hash.
        let mut circuit = ComputeSharesHashCircuit { blinds, enc_c1, enc_c2 };
        circuit.enc_c1[2] = pallas::Base::random(&mut rng);

        let prover = MockProver::run(12, &circuit, vec![vec![correct]])
            .expect("MockProver::run failed");
        assert!(prover.verify().is_err());
    }

    /// Every one of the 16 share positions contributes to the output hash.
    ///
    /// For each position `i` in `0..16`, the circuit with `enc_c1[i]` corrupted
    /// must fail to verify against the hash computed from the original inputs.
    /// This confirms that no share slot is silently ignored.
    #[test]
    fn all_16_share_positions_are_hashed() {
        let mut rng = OsRng;
        let blinds: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let enc_c1: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let enc_c2: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));

        let correct = shares_hash(blinds, enc_c1, enc_c2);

        for i in 0..16 {
            let mut circuit = ComputeSharesHashCircuit { blinds, enc_c1, enc_c2 };
            circuit.enc_c1[i] = pallas::Base::random(&mut rng);

            let prover = MockProver::run(12, &circuit, vec![vec![correct]])
                .unwrap_or_else(|e| panic!("MockProver::run failed at position {i}: {e}"));
            assert!(
                prover.verify().is_err(),
                "corrupting enc_c1[{i}] did not change the shares_hash — position is not hashed"
            );
        }
    }

    /// Corrupting a blind factor changes the output.
    #[test]
    fn compute_shares_hash_wrong_blind_fails() {
        let mut rng = OsRng;
        let blinds: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let enc_c1: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let enc_c2: [pallas::Base; 16] = core::array::from_fn(|_| pallas::Base::random(&mut rng));

        let correct = shares_hash(blinds, enc_c1, enc_c2);

        // Corrupt blinds[0] in the circuit but keep the correct expected hash.
        let mut circuit = ComputeSharesHashCircuit { blinds, enc_c1, enc_c2 };
        circuit.blinds[0] = pallas::Base::random(&mut rng);

        let prover = MockProver::run(12, &circuit, vec![vec![correct]])
            .expect("MockProver::run failed");
        assert!(prover.verify().is_err());
    }
}
