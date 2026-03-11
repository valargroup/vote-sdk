//! Toy Halo2 circuit for validating the FFI pipeline.
//!
//! Proves knowledge of private (a, b) such that `constant * a^2 * b^2 = c`
//! where `c` is a public input and `constant` is fixed at 7.
//!
//! Adapted from the official halo2 simple-example:
//! https://github.com/zcash/halo2/blob/main/halo2_proofs/examples/simple-example.rs

use std::marker::PhantomData;

use group::ff::Field;
use halo2_proofs::{
    circuit::{AssignedCell, Chip, Layouter, Region, SimpleFloorPlanner, Value},
    plonk::{Advice, Circuit, Column, ConstraintSystem, Error, Fixed, Instance, Selector},
    poly::Rotation,
};

/// The constant multiplier baked into the circuit: constant * a^2 * b^2 = c.
pub const CIRCUIT_CONSTANT: u64 = 7;

/// The circuit parameter k (log2 of the number of rows). k=4 gives 16 rows,
/// which is more than enough for this toy circuit.
pub const K: u32 = 4;

// ---------------------------------------------------------------------------
// Chip definition
// ---------------------------------------------------------------------------

trait NumericInstructions<F: Field>: Chip<F> {
    type Num;

    fn load_private(
        &self,
        layouter: impl Layouter<F>,
        a: Value<F>,
    ) -> Result<Self::Num, Error>;

    fn load_constant(
        &self,
        layouter: impl Layouter<F>,
        constant: F,
    ) -> Result<Self::Num, Error>;

    fn mul(
        &self,
        layouter: impl Layouter<F>,
        a: Self::Num,
        b: Self::Num,
    ) -> Result<Self::Num, Error>;

    fn expose_public(
        &self,
        layouter: impl Layouter<F>,
        num: Self::Num,
        row: usize,
    ) -> Result<(), Error>;
}

#[derive(Clone, Debug)]
pub struct FieldConfig {
    advice: [Column<Advice>; 2],
    instance: Column<Instance>,
    s_mul: Selector,
}

struct FieldChip<F: Field> {
    config: FieldConfig,
    _marker: PhantomData<F>,
}

impl<F: Field> FieldChip<F> {
    fn construct(config: FieldConfig) -> Self {
        Self {
            config,
            _marker: PhantomData,
        }
    }

    fn configure(
        meta: &mut ConstraintSystem<F>,
        advice: [Column<Advice>; 2],
        instance: Column<Instance>,
        constant: Column<Fixed>,
    ) -> FieldConfig {
        meta.enable_equality(instance);
        meta.enable_constant(constant);
        for column in &advice {
            meta.enable_equality(*column);
        }
        let s_mul = meta.selector();

        meta.create_gate("mul", |meta| {
            let lhs = meta.query_advice(advice[0], Rotation::cur());
            let rhs = meta.query_advice(advice[1], Rotation::cur());
            let out = meta.query_advice(advice[0], Rotation::next());
            let s_mul = meta.query_selector(s_mul);
            vec![s_mul * (lhs * rhs - out)]
        });

        FieldConfig {
            advice,
            instance,
            s_mul,
        }
    }
}

impl<F: Field> Chip<F> for FieldChip<F> {
    type Config = FieldConfig;
    type Loaded = ();

    fn config(&self) -> &Self::Config {
        &self.config
    }

    fn loaded(&self) -> &Self::Loaded {
        &()
    }
}

#[derive(Clone)]
struct Number<F: Field>(AssignedCell<F, F>);

impl<F: Field> NumericInstructions<F> for FieldChip<F> {
    type Num = Number<F>;

    fn load_private(
        &self,
        mut layouter: impl Layouter<F>,
        value: Value<F>,
    ) -> Result<Self::Num, Error> {
        let config = self.config();
        layouter.assign_region(
            || "load private",
            |mut region| {
                region
                    .assign_advice(|| "private input", config.advice[0], 0, || value)
                    .map(Number)
            },
        )
    }

    fn load_constant(
        &self,
        mut layouter: impl Layouter<F>,
        constant: F,
    ) -> Result<Self::Num, Error> {
        let config = self.config();
        layouter.assign_region(
            || "load constant",
            |mut region| {
                region
                    .assign_advice_from_constant(
                        || "constant value",
                        config.advice[0],
                        0,
                        constant,
                    )
                    .map(Number)
            },
        )
    }

    fn mul(
        &self,
        mut layouter: impl Layouter<F>,
        a: Self::Num,
        b: Self::Num,
    ) -> Result<Self::Num, Error> {
        let config = self.config();
        layouter.assign_region(
            || "mul",
            |mut region: Region<'_, F>| {
                config.s_mul.enable(&mut region, 0)?;
                a.0.copy_advice(|| "lhs", &mut region, config.advice[0], 0)?;
                b.0.copy_advice(|| "rhs", &mut region, config.advice[1], 0)?;
                let value = a.0.value().copied() * b.0.value();
                region
                    .assign_advice(|| "lhs * rhs", config.advice[0], 1, || value)
                    .map(Number)
            },
        )
    }

    fn expose_public(
        &self,
        mut layouter: impl Layouter<F>,
        num: Self::Num,
        row: usize,
    ) -> Result<(), Error> {
        let config = self.config();
        layouter.constrain_instance(num.0.cell(), config.instance, row)
    }
}

// ---------------------------------------------------------------------------
// Circuit definition
// ---------------------------------------------------------------------------

/// The toy circuit: proves knowledge of (a, b) such that constant * a^2 * b^2 = c.
#[derive(Default)]
pub struct ToyCircuit<F: Field> {
    pub constant: F,
    pub a: Value<F>,
    pub b: Value<F>,
}

impl<F: Field> Circuit<F> for ToyCircuit<F> {
    type Config = FieldConfig;
    type FloorPlanner = SimpleFloorPlanner;

    fn without_witnesses(&self) -> Self {
        Self::default()
    }

    fn configure(meta: &mut ConstraintSystem<F>) -> Self::Config {
        let advice = [meta.advice_column(), meta.advice_column()];
        let instance = meta.instance_column();
        let constant = meta.fixed_column();
        FieldChip::configure(meta, advice, instance, constant)
    }

    fn synthesize(
        &self,
        config: Self::Config,
        mut layouter: impl Layouter<F>,
    ) -> Result<(), Error> {
        let field_chip = FieldChip::<F>::construct(config);

        let a = field_chip.load_private(layouter.namespace(|| "load a"), self.a)?;
        let b = field_chip.load_private(layouter.namespace(|| "load b"), self.b)?;
        let constant =
            field_chip.load_constant(layouter.namespace(|| "load constant"), self.constant)?;

        // constant * a^2 * b^2 = constant * (a*b)^2
        let ab = field_chip.mul(layouter.namespace(|| "a * b"), a, b)?;
        let absq = field_chip.mul(layouter.namespace(|| "ab * ab"), ab.clone(), ab)?;
        let c = field_chip.mul(layouter.namespace(|| "constant * absq"), constant, absq)?;

        field_chip.expose_public(layouter.namespace(|| "expose c"), c, 0)
    }
}

// ---------------------------------------------------------------------------
// Prove / verify helpers (used by FFI and tests)
// ---------------------------------------------------------------------------

use halo2_proofs::{
    pasta::{EqAffine, Fp},
    plonk::{create_proof, keygen_pk, keygen_vk, verify_proof, SingleVerifier},
    poly::commitment::Params,
    transcript::{Blake2bRead, Blake2bWrite, Challenge255},
};
use rand_core::OsRng;

/// Generate the IPA params (SRS) for our toy circuit.
/// Deterministic for a given `k`.
pub fn toy_params() -> Params<EqAffine> {
    Params::new(K)
}

/// Generate a proving key for the toy circuit.
pub fn toy_proving_key(
    params: &Params<EqAffine>,
) -> (
    halo2_proofs::plonk::ProvingKey<EqAffine>,
    halo2_proofs::plonk::VerifyingKey<EqAffine>,
) {
    let empty_circuit = ToyCircuit::<Fp> {
        constant: Fp::from(CIRCUIT_CONSTANT),
        a: Value::unknown(),
        b: Value::unknown(),
    };
    let vk = keygen_vk(params, &empty_circuit).expect("keygen_vk should not fail");
    let pk = keygen_pk(params, vk.clone(), &empty_circuit).expect("keygen_pk should not fail");
    (pk, vk)
}

/// Create a proof for the toy circuit with given private inputs (a, b).
/// Returns the serialized proof bytes.
pub fn create_toy_proof(a: u64, b: u64) -> (Vec<u8>, Fp) {
    let params = toy_params();
    let (pk, _vk) = toy_proving_key(&params);

    let constant = Fp::from(CIRCUIT_CONSTANT);
    let a_fp = Fp::from(a);
    let b_fp = Fp::from(b);
    let c = constant * a_fp.square() * b_fp.square();

    let circuit = ToyCircuit::<Fp> {
        constant,
        a: Value::known(a_fp),
        b: Value::known(b_fp),
    };

    let public_inputs = vec![c];

    let mut transcript = Blake2bWrite::<_, EqAffine, Challenge255<_>>::init(vec![]);
    create_proof(
        &params,
        &pk,
        &[circuit],
        &[&[&public_inputs]],
        OsRng,
        &mut transcript,
    )
    .expect("proof generation should not fail");
    let proof = transcript.finalize();

    (proof, c)
}

/// Verify a toy circuit proof given proof bytes and a public input.
/// Returns Ok(()) if verification succeeds, Err with message otherwise.
pub fn verify_toy(proof: &[u8], public_input: &Fp) -> Result<(), String> {
    let params = toy_params();
    let (_pk, vk) = toy_proving_key(&params);

    let public_inputs = vec![*public_input];

    let strategy = SingleVerifier::new(&params);
    let mut transcript =
        Blake2bRead::<_, EqAffine, Challenge255<_>>::init(proof);

    verify_proof(&params, &vk, strategy, &[&[&public_inputs]], &mut transcript)
        .map_err(|e| format!("verification failed: {:?}", e))
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use halo2_proofs::{dev::MockProver, pasta::Fp};

    #[test]
    fn test_mock_prover_valid() {
        let constant = Fp::from(CIRCUIT_CONSTANT);
        let a = Fp::from(2u64);
        let b = Fp::from(3u64);
        let c = constant * a.square() * b.square();

        let circuit = ToyCircuit {
            constant,
            a: Value::known(a),
            b: Value::known(b),
        };

        let prover = MockProver::run(K, &circuit, vec![vec![c]]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn test_mock_prover_wrong_output() {
        let constant = Fp::from(CIRCUIT_CONSTANT);
        let a = Fp::from(2u64);
        let b = Fp::from(3u64);
        let wrong_c = Fp::from(999u64);

        let circuit = ToyCircuit {
            constant,
            a: Value::known(a),
            b: Value::known(b),
        };

        let prover = MockProver::run(K, &circuit, vec![vec![wrong_c]]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn test_real_prove_verify() {
        let (proof, c) = create_toy_proof(2, 3);
        assert!(verify_toy(&proof, &c).is_ok());
    }

    #[test]
    fn test_real_verify_wrong_input() {
        let (proof, _c) = create_toy_proof(2, 3);
        let wrong_c = Fp::from(999u64);
        assert!(verify_toy(&proof, &wrong_c).is_err());
    }

    #[test]
    fn test_real_verify_corrupted_proof() {
        let (mut proof, c) = create_toy_proof(2, 3);
        proof[0] ^= 0xFF;
        assert!(verify_toy(&proof, &c).is_err());
    }
}
