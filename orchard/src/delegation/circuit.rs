//! The Delegation circuit implementation.
//!
//! Proves nullifier integrity for a single note: given private witness data
//! `(nk, rho_old, psi_old, cm_old)`, derives `nf_old` in-circuit and constrains
//! it to match the public input.
//!
//! Follows the 1-circuit-per-note pattern from the vote module. For multiple
//! notes, the builder layer creates multiple independent proofs.

use group::Curve;
use halo2_proofs::{
    circuit::{floor_planner, Layouter, Value},
    plonk::{self, Advice, Column, Instance as InstanceColumn},
};
use pasta_curves::{pallas, vesta};

use crate::{
    circuit::gadget::{
        add_chip::{AddChip, AddConfig},
        assign_free_advice, derive_nullifier,
    },
    constants::{OrchardCommitDomains, OrchardFixedBases, OrchardHashDomains},
    keys::FullViewingKey,
    note::{
        commitment::NoteCommitment,
        nullifier::Nullifier,
        Note,
    },
};
use halo2_gadgets::{
    ecc::{
        chip::{EccChip, EccConfig},
        Point,
    },
    poseidon::{primitives as poseidon, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig},
    sinsemilla::chip::{SinsemillaChip, SinsemillaConfig},
    utilities::lookup_range_check::LookupRangeCheckConfig,
};

/// Public input offset for the derived nullifier.
const NF_OLD: usize = 0;

/// Size of the delegation circuit.
/// 4096 rows
/// Sinsemilla lookup table ~1024 rows
/// ECC chip ~1000 rows
/// Poseidon chip ~200-300 rows
/// Range checks a few more rows but relatively negligible.
const K: u32 = 12;

/// Configuration for the Delegation circuit.
#[derive(Clone, Debug)]
pub struct Config {
    // The instace column (public inputs)
    primary: Column<InstanceColumn>,
    // 10 advice columns for private witness data.
    // This is the scratch space where the prover places intermediate values during computation.
    // Various chips use these columns
    // Poseidon: [5..9]
    // ECC: uses all 10
    // AddChip: uses [6..9]
    advices: [Column<Advice>; 10],
    // Configuration for the AddChip which constrains a + b = c over field elements.
    // Used inside DeriveNullifier to combine intermediate values.
    add_config: AddConfig,
    // Configuration for the ECCChip which provides elliptic curve operations
    // (point addition, scalar multiplication) on the Pallas curve with Orchard's fixes bases.
    // We use it to convert cm_old from NoteCommitment to a Field point for the DeriveNullifier function.
    ecc_config: EccConfig<OrchardFixedBases>,
    // Poseidon chip config. Used in the DeriveNullifier.
    poseidon_config: PoseidonConfig<pallas::Base, 3, 2>,
    // Sinsemilla config — only used for loading the lookup table that
    // LookupRangeCheckConfig (and thus EccChip) depends on.
    sinsemilla_config: SinsemillaConfig<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases>,
}

impl Config {
    fn add_chip(&self) -> AddChip {
        AddChip::construct(self.add_config.clone())
    }

    fn ecc_chip(&self) -> EccChip<OrchardFixedBases> {
        EccChip::construct(self.ecc_config.clone())
    }

    // Operating over the Pallas base field, with a width of 3 (state size) and rate of 2
    // 3 comes from the P128Pow5T3 construction used throughout Orchard (i.e. 3 is width)
    // Rate of 2 means that two elements are absorbed per permutation, so the hash completes
    // in fewer rounds than rate 1, roughly halving the number of Poseidon permutations.
    fn poseidon_chip(&self) -> PoseidonChip<pallas::Base, 3, 2> {
        PoseidonChip::construct(self.poseidon_config.clone())
    }
}

/// The Delegation circuit.
///
/// Proves nullifier integrity for a single note: the prover knows
/// `(nk, rho, psi, cm)` such that `nf_old = DeriveNullifier(nk, rho, psi, cm)`.
#[derive(Clone, Debug, Default)]
pub struct Circuit {
    nk: Value<pallas::Base>,
    rho_old: Value<pallas::Base>,
    psi_old: Value<pallas::Base>,
    cm_old: Value<NoteCommitment>,
}

impl Circuit {
    /// Constructs a `Circuit` from a note and its full viewing key.
    pub fn from_note_unchecked(fvk: &FullViewingKey, note: &Note) -> Self {
        let rho_old = note.rho();
        let psi_old = note.rseed().psi(&rho_old);
        Circuit {
            nk: Value::known(fvk.nk().inner()),
            rho_old: Value::known(rho_old.0),
            psi_old: Value::known(psi_old),
            cm_old: Value::known(note.commitment()),
        }
    }
}

impl plonk::Circuit<pallas::Base> for Circuit {
    type Config = Config;
    type FloorPlanner = floor_planner::V1;

    fn without_witnesses(&self) -> Self {
        Self::default()
    }

    fn configure(meta: &mut plonk::ConstraintSystem<pallas::Base>) -> Self::Config {
        // Advice columns used in the circuit.
        let advices = [
            meta.advice_column(),
            meta.advice_column(),
            meta.advice_column(),
            meta.advice_column(),
            meta.advice_column(),
            meta.advice_column(),
            meta.advice_column(),
            meta.advice_column(),
            meta.advice_column(),
            meta.advice_column(),
        ];

        // Addition of two field elements.
        // 7,8,6 are chosen to share columns with the Poseidon chip to minimize column usage.
        let add_config = AddChip::configure(meta, advices[7], advices[8], advices[6]);

        // Fixed columns for the Sinsemilla generator lookup table.
        let table_idx = meta.lookup_table_column();
        let lookup = (
            table_idx,
            meta.lookup_table_column(),
            meta.lookup_table_column(),
        );

        // Instance column used for public inputs.
        let primary = meta.instance_column();
        meta.enable_equality(primary);

        // Permutation over all advice columns.
        for advice in advices.iter() {
            meta.enable_equality(*advice);
        }

        // Fixed columns shared between ECC and Poseidon chips.
        let lagrange_coeffs = [
            meta.fixed_column(),
            meta.fixed_column(),
            meta.fixed_column(),
            meta.fixed_column(),
            meta.fixed_column(),
            meta.fixed_column(),
            meta.fixed_column(),
            meta.fixed_column(),
        ];
        let rc_a = lagrange_coeffs[2..5].try_into().unwrap();
        let rc_b = lagrange_coeffs[5..8].try_into().unwrap();

        // Use the first Lagrange coefficient column for loading global constants.
        meta.enable_constant(lagrange_coeffs[0]);

        // Range check configuration using the right-most advice column.
        let range_check = LookupRangeCheckConfig::configure(meta, advices[9], table_idx);

        // Configuration for curve point operations.
        let ecc_config =
            EccChip::<OrchardFixedBases>::configure(meta, advices, lagrange_coeffs, range_check);

        // Configuration for the Poseidon hash.
        let poseidon_config = PoseidonChip::configure::<poseidon::P128Pow5T3>(
            meta,
            advices[6..9].try_into().unwrap(),
            advices[5],
            rc_a,
            rc_b,
        );

        // Sinsemilla config — we only need this to load the lookup table
        // that the range check (and thus ECC operations) depend on.
        let sinsemilla_config = SinsemillaChip::configure(
            meta,
            advices[..5].try_into().unwrap(),
            advices[6],
            lagrange_coeffs[0],
            lookup,
            range_check,
        );

        Config {
            primary,
            advices,
            add_config,
            ecc_config,
            poseidon_config,
            sinsemilla_config,
        }
    }

    #[allow(non_snake_case)]
    fn synthesize(
        &self,
        config: Self::Config,
        mut layouter: impl Layouter<pallas::Base>,
    ) -> Result<(), plonk::Error> {
        // Load the Sinsemilla generator lookup table (needed by ECC range checks).
        SinsemillaChip::load(config.sinsemilla_config.clone(), &mut layouter)?;

        // Construct the ECC chip.
        // It is needed to derive cm_old ECC point from NoteCommitment.
        let ecc_chip = config.ecc_chip();

        // Witness nk (nullifier deriving key).
        let nk = assign_free_advice(
            layouter.namespace(|| "witness nk"),
            config.advices[0],
            self.nk,
        )?;

        // Witness rho_old.
        // This is the nullifier of the note that was spent to create this note. It is
        // a Nullifier type (a Pallas base field element) that serves as a unique, per-note domain
        // separator.
        // rho ensures that even if two notes have identical contents, they will produce
        // different nullifiers because they were created by spending different input notes.
        // rho provides deterministic, structural uniqueness. It is the nullifier of the
        // spend input note so it chains each note to its creation context. A single tx
        // can create multiple output notes from the same input. All those outputs share the same
        // rho. If nullifier derivation only used rho (no psi), outputs from the same input could collide.
        let rho_old = assign_free_advice(
            layouter.namespace(|| "witness rho_old"),
            config.advices[0],
            self.rho_old,
        )?;

        // Witness psi_old.
        // Pseudorandom field element ederived from the note's random
        // seed rseed and its nullifier domain separator rho.
        // It adds randomness to the nullifier so that even if two notes share the same
        // rho and nk, they produce different nullifiers.
        // We provide it as input instead of deriving in-circuit since derivation
        // would require an expensive Blake2b.
        // psi provides randomized uniqueness. It is derived from rseed which is
        // freshly random per note. So, even if multiple outputs are derived from the same note,
        // different rseed values produce different psi values. But if uniqueness relied only on psi
        // (i.e. only randomness), a faulty RNG would cause nullifier collisions. Together with rho,
        // they cover each other's weaknesses.
        // Additionally, there is a structural reason, if we only used psi, there would be an implicit chain:
        // each note's identity is linked to the note that was spend to create it. The randomized psi
        // breaks the chain, unblocking a requirement used in Orchard's security proof.
        let psi_old = assign_free_advice(
            layouter.namespace(|| "witness psi_old"),
            config.advices[0],
            self.psi_old,
        )?;

        // Witness cm_old as an ECC point, which is the form DeriveNullifier expects.
        let cm_old = Point::new(
            ecc_chip.clone(),
            layouter.namespace(|| "witness cm_old"),
            self.cm_old.as_ref().map(|cm| cm.inner().to_affine()),
        )?;

        // Nullifier integrity: derive nf_old = DeriveNullifier(nk, rho_old, psi_old, cm_old).
        let nf_old = derive_nullifier(
            layouter.namespace(|| "nf_old = DeriveNullifier_nk(rho_old, psi_old, cm_old)"),
            config.poseidon_chip(),
            config.add_chip(),
            ecc_chip,
            rho_old,
            &psi_old,
            &cm_old,
            nk,
        )?;

        // Constrain nf_old to equal the public input.
        // Enforce that the nullifier computed inside the circuit matches the nullifier provided
        // as a public input from outside the circuit (supplied at NF_OlD of the public input)
        layouter.constrain_instance(nf_old.inner().cell(), config.primary, NF_OLD)?;

        Ok(())
    }
}

/// Public inputs to the Delegation circuit.
#[derive(Clone, Debug)]
pub struct Instance {
    /// The derived nullifier (temporary public input; will be replaced by gov_null).
    pub nf_old: Nullifier,
}

impl Instance {
    /// Constructs an [`Instance`] from its constituent parts.
    pub fn from_parts(nf_old: Nullifier) -> Self {
        Instance { nf_old }
    }

    /// Returns the public inputs as a vector of field elements for halo2.
    pub fn to_halo2_instance(&self) -> Vec<vesta::Scalar> {
        vec![self.nf_old.0]
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{
        keys::{FullViewingKey, SpendingKey},
        note::Note,
    };
    use halo2_proofs::dev::MockProver;
    use rand::rngs::OsRng;

    /// Helper: create a dummy note and its corresponding circuit + expected nullifier.
    fn make_test_note() -> (Circuit, Nullifier) {
        let mut rng = OsRng;
        let (_sk, fvk, note) = Note::dummy(&mut rng, None);

        let nf = note.nullifier(&fvk);
        let circuit = Circuit::from_note_unchecked(&fvk, &note);

        (circuit, nf)
    }

    #[test]
    fn nullifier_integrity_happy_path() {
        let (circuit, nf) = make_test_note();

        let instance = Instance::from_parts(nf);
        let public_inputs = instance.to_halo2_instance();

        let prover = MockProver::run(
            K,
            &circuit,
            vec![public_inputs],
        )
        .unwrap();

        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn nullifier_integrity_wrong_key() {
        let mut rng = OsRng;

        // Create note with one key
        let (_sk1, fvk1, note) = Note::dummy(&mut rng, None);
        let circuit = Circuit::from_note_unchecked(&fvk1, &note);

        // But derive the expected nullifier with a different key
        let sk2 = SpendingKey::random(&mut rng);
        let fvk2: FullViewingKey = (&sk2).into();
        let wrong_nf = note.nullifier(&fvk2);

        let instance = Instance::from_parts(wrong_nf);
        let public_inputs = instance.to_halo2_instance();

        let prover = MockProver::run(
            K,
            &circuit,
            vec![public_inputs],
        )
        .unwrap();

        // The proof should fail: the derived nullifier won't match the public input.
        assert!(prover.verify().is_err());
    }

    #[test]
    fn nullifier_integrity_dummy_note() {
        // A dummy note (value = 0) should work identically.
        let (circuit, nf) = make_test_note();

        let instance = Instance::from_parts(nf);
        let public_inputs = instance.to_halo2_instance();

        let prover = MockProver::run(
            K,
            &circuit,
            vec![public_inputs],
        )
        .unwrap();

        assert_eq!(prover.verify(), Ok(()));
    }
}
