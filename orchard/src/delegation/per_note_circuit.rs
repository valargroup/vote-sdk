//! Per-note delegation circuit (conditions 9–15).
//!
//! Verifies properties of a single note slot in the governance voting system:
//! - **Condition 9**: Note commitment integrity
//! - **Condition 10**: Merkle path validity (conditional on `is_note_real`)
//! - **Condition 11**: Diversified address integrity (`pk_d = [ivk] * g_d`)
//! - **Condition 12**: Private nullifier derivation
//! - **Condition 13**: IMT non-membership (unconditional per spec §1.3.5)
//! - **Condition 14**: Governance nullifier integrity
//! - **Condition 15**: Padded notes have zero value
//!
//! This circuit is separate from the main delegation circuit (`circuit.rs`) which
//! handles conditions 1–8. The main circuit's free witnesses `cmx_1..4` and `v_1..4`
//! are bound to this circuit's public outputs (`CMX` and `VALUE`).
//!
//! ## Trust boundary: `ivk`
//!
//! `ivk` is currently an **unconstrained** private witness. The main circuit derives
//! `ivk` via `CommitIvk`. A future integration step must bind our `ivk` to the main
//! circuit's (via shared public input or circuit merge).

use group::Curve;
use halo2_proofs::{
    circuit::{floor_planner, Layouter, Value},
    plonk::{self, Advice, Column, Constraints, Expression, Instance as InstanceColumn, Selector},
    poly::Rotation,
};
use ff::Field;
use pasta_curves::pallas;

use crate::{
    circuit::{
        gadget::{
            add_chip::{AddChip, AddConfig},
            assign_free_advice, derive_nullifier, note_commit,
        },
        note_commit::{NoteCommitChip, NoteCommitConfig},
    },
    constants::{
        OrchardCommitDomains, OrchardFixedBases, OrchardHashDomains, MERKLE_DEPTH_ORCHARD,
    },
    note::commitment::{NoteCommitTrapdoor, NoteCommitment},
    spec::NonIdentityPallasPoint,
    tree::MerkleHashOrchard,
    value::NoteValue,
};
use halo2_gadgets::{
    ecc::{
        chip::{EccChip, EccConfig},
        NonIdentityPoint, Point, ScalarFixed, ScalarVar,
    },
    poseidon::{
        primitives::{self as poseidon, ConstantLength},
        Hash as PoseidonHash, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig,
    },
    sinsemilla::{
        chip::{SinsemillaChip, SinsemillaConfig},
        merkle::{
            chip::{MerkleChip, MerkleConfig},
            MerklePath,
        },
    },
    utilities::{bool_check, lookup_range_check::LookupRangeCheckConfig},
};

// Public input offsets.
/// Note commitment tree root (anchor).
const NC_ROOT: usize = 0;
/// Nullifier IMT root.
const NF_IMT_ROOT: usize = 1;
/// Voting round identifier.
const VOTE_ROUND_ID: usize = 2;
/// Governance nullifier (published).
const GOV_NULL: usize = 3;
/// ExtractP(cm) — extracted note commitment.
const CMX: usize = 4;
/// Note value v (for main circuit's sum).
const VALUE: usize = 5;

/// Size of the per-note delegation circuit (2^K rows).
const K: u32 = 14;

/// Depth of the nullifier Indexed Merkle Tree (Poseidon-based). Parameterizable.
const IMT_DEPTH: usize = 32;

/// Configuration for the per-note delegation circuit.
#[derive(Clone, Debug)]
pub struct Config {
    primary: Column<InstanceColumn>,
    q_per_note: Selector,
    q_imt_swap: Selector,
    q_imt_nonmember: Selector,
    advices: [Column<Advice>; 10],
    add_config: AddConfig,
    ecc_config: EccConfig<OrchardFixedBases>,
    poseidon_config: PoseidonConfig<pallas::Base, 3, 2>,
    merkle_config_1: MerkleConfig<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases>,
    merkle_config_2: MerkleConfig<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases>,
    sinsemilla_config_1:
        SinsemillaConfig<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases>,
    sinsemilla_config_2:
        SinsemillaConfig<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases>,
    note_commit_config: NoteCommitConfig,
}

impl Config {
    fn add_chip(&self) -> AddChip {
        AddChip::construct(self.add_config.clone())
    }

    fn ecc_chip(&self) -> EccChip<OrchardFixedBases> {
        EccChip::construct(self.ecc_config.clone())
    }

    fn poseidon_chip(&self) -> PoseidonChip<pallas::Base, 3, 2> {
        PoseidonChip::construct(self.poseidon_config.clone())
    }

    fn sinsemilla_chip_1(
        &self,
    ) -> SinsemillaChip<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases> {
        SinsemillaChip::construct(self.sinsemilla_config_1.clone())
    }

    fn merkle_chip_1(
        &self,
    ) -> MerkleChip<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases> {
        MerkleChip::construct(self.merkle_config_1.clone())
    }

    fn merkle_chip_2(
        &self,
    ) -> MerkleChip<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases> {
        MerkleChip::construct(self.merkle_config_2.clone())
    }

    fn note_commit_chip(&self) -> NoteCommitChip {
        NoteCommitChip::construct(self.note_commit_config.clone())
    }
}

/// The per-note delegation circuit.
///
/// Proves conditions 9–15 for a single note slot:
/// note commitment integrity, Merkle membership, address ownership,
/// private nullifier derivation, governance nullifier publication,
/// and padded-note value enforcement.
#[derive(Clone, Debug)]
pub struct Circuit {
    // Note data.
    g_d: Value<NonIdentityPallasPoint>,
    pk_d: Value<NonIdentityPallasPoint>,
    v: Value<NoteValue>,
    rho: Value<pallas::Base>,
    psi: Value<pallas::Base>,
    rcm: Value<NoteCommitTrapdoor>,
    cm: Value<NoteCommitment>,
    // Merkle path.
    path: Value<[MerkleHashOrchard; MERKLE_DEPTH_ORCHARD]>,
    pos: Value<u32>,
    // Keys.
    /// Incoming viewing key — **unconstrained** witness.
    /// TODO: Bind to main circuit's CommitIvk-derived ivk.
    ivk: Value<pallas::Base>,
    nk: Value<pallas::Base>,
    // Padding flag.
    is_note_real: Value<bool>,
    // Gov nullifier inputs.
    vote_round_id: Value<pallas::Base>,
    // IMT non-membership proof (condition 13).
    imt_low_nf: Value<pallas::Base>,
    imt_next_nf: Value<pallas::Base>,
    imt_leaf_pos: Value<u32>,
    imt_path: Value<[pallas::Base; IMT_DEPTH]>,
}

impl Default for Circuit {
    fn default() -> Self {
        Circuit {
            g_d: Value::default(),
            pk_d: Value::default(),
            v: Value::default(),
            rho: Value::default(),
            psi: Value::default(),
            rcm: Value::default(),
            cm: Value::default(),
            path: Value::default(),
            pos: Value::default(),
            ivk: Value::default(),
            nk: Value::default(),
            is_note_real: Value::default(),
            vote_round_id: Value::default(),
            imt_low_nf: Value::default(),
            imt_next_nf: Value::default(),
            imt_leaf_pos: Value::default(),
            imt_path: Value::default(),
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

        // Custom gate for conditions 10, 13, and 15.
        let q_per_note = meta.selector();
        meta.create_gate("Per-note checks", |meta| {
            let q_per_note = meta.query_selector(q_per_note);
            let is_note_real = meta.query_advice(advices[0], Rotation::cur());
            let v = meta.query_advice(advices[1], Rotation::cur());
            let root = meta.query_advice(advices[2], Rotation::cur());
            let anchor = meta.query_advice(advices[3], Rotation::cur());
            let imt_root = meta.query_advice(advices[4], Rotation::cur());
            let nf_imt_root = meta.query_advice(advices[5], Rotation::cur());

            let one = Expression::Constant(pallas::Base::one());

            Constraints::with_selector(
                q_per_note,
                [
                    // Condition 15: padded notes have zero value.
                    (
                        "(1 - is_note_real) * v = 0",
                        (one.clone() - is_note_real.clone()) * v,
                    ),
                    // Boolean check on is_note_real.
                    ("bool_check is_note_real", bool_check(is_note_real.clone())),
                    // Condition 10: real notes must have root = anchor.
                    (
                        "is_note_real * (root - anchor) = 0",
                        is_note_real * (root - anchor),
                    ),
                    // Condition 13: imt_root must match public input (unconditional
                    // per spec §1.3.5 — padded notes still prove IMT non-membership).
                    ("imt_root = nf_imt_root", imt_root - nf_imt_root),
                ],
            )
        });

        // IMT conditional swap gate (one per Merkle level).
        let q_imt_swap = meta.selector();
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
                    // Swap: left = current + pos_bit * (sibling - current)
                    (
                        "swap left",
                        left.clone() - current.clone()
                            - pos_bit.clone() * (sibling.clone() - current.clone()),
                    ),
                    // Complement: left + right = current + sibling
                    (
                        "swap right",
                        left + right - current - sibling,
                    ),
                    // pos_bit is boolean
                    ("bool_check pos_bit", bool_check(pos_bit)),
                ],
            )
        });

        // IMT non-membership gate (enabled once).
        let q_imt_nonmember = meta.selector();
        meta.create_gate("IMT non-membership", |meta| {
            let q = meta.query_selector(q_imt_nonmember);
            // Row 0
            let low_nf = meta.query_advice(advices[0], Rotation::cur());
            let next_nf = meta.query_advice(advices[1], Rotation::cur());
            let real_nf = meta.query_advice(advices[2], Rotation::cur());
            let is_max = meta.query_advice(advices[3], Rotation::cur());
            // Row 1
            let inv_nf = meta.query_advice(advices[0], Rotation::next());
            let diff1 = meta.query_advice(advices[1], Rotation::next());
            let diff2_mask = meta.query_advice(advices[2], Rotation::next());

            let one = Expression::Constant(pallas::Base::one());

            Constraints::with_selector(
                q,
                [
                    // IsZero(next_nf): is_max = 1 - next_nf * inv_nf
                    (
                        "is_max = 1 - next_nf * inv_nf",
                        is_max.clone() - (one.clone() - next_nf.clone() * inv_nf),
                    ),
                    // next_nf * is_max = 0
                    ("next_nf * is_max = 0", next_nf.clone() * is_max.clone()),
                    // bool_check(is_max)
                    ("bool_check is_max", bool_check(is_max.clone())),
                    // diff1 = real_nf - low_nf - 1
                    (
                        "diff1 = real_nf - low_nf - 1",
                        diff1 - (real_nf.clone() - low_nf - one.clone()),
                    ),
                    // diff2_mask = (1 - is_max) * (next_nf - real_nf - 1)
                    (
                        "diff2_mask = (1 - is_max) * (next_nf - real_nf - 1)",
                        diff2_mask - (one.clone() - is_max) * (next_nf - real_nf - one),
                    ),
                ],
            )
        });

        let add_config = AddChip::configure(meta, advices[7], advices[8], advices[6]);

        let table_idx = meta.lookup_table_column();
        let lookup = (
            table_idx,
            meta.lookup_table_column(),
            meta.lookup_table_column(),
        );

        let primary = meta.instance_column();
        meta.enable_equality(primary);

        for advice in advices.iter() {
            meta.enable_equality(*advice);
        }

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

        meta.enable_constant(lagrange_coeffs[0]);

        let range_check = LookupRangeCheckConfig::configure(meta, advices[9], table_idx);

        let ecc_config =
            EccChip::<OrchardFixedBases>::configure(meta, advices, lagrange_coeffs, range_check);

        let poseidon_config = PoseidonChip::configure::<poseidon::P128Pow5T3>(
            meta,
            advices[6..9].try_into().unwrap(),
            advices[5],
            rc_a,
            rc_b,
        );

        let (sinsemilla_config_1, merkle_config_1) = {
            let sinsemilla_config_1 = SinsemillaChip::configure(
                meta,
                advices[..5].try_into().unwrap(),
                advices[6],
                lagrange_coeffs[0],
                lookup,
                range_check,
            );
            let merkle_config_1 = MerkleChip::configure(meta, sinsemilla_config_1.clone());
            (sinsemilla_config_1, merkle_config_1)
        };

        let (sinsemilla_config_2, merkle_config_2) = {
            let sinsemilla_config_2 = SinsemillaChip::configure(
                meta,
                advices[5..].try_into().unwrap(),
                advices[7],
                lagrange_coeffs[1],
                lookup,
                range_check,
            );
            let merkle_config_2 = MerkleChip::configure(meta, sinsemilla_config_2.clone());
            (sinsemilla_config_2, merkle_config_2)
        };

        let note_commit_config =
            NoteCommitChip::configure(meta, advices, sinsemilla_config_1.clone());

        Config {
            primary,
            q_per_note,
            q_imt_swap,
            q_imt_nonmember,
            advices,
            add_config,
            ecc_config,
            poseidon_config,
            merkle_config_1,
            merkle_config_2,
            sinsemilla_config_1,
            sinsemilla_config_2,
            note_commit_config,
        }
    }

    #[allow(non_snake_case)]
    fn synthesize(
        &self,
        config: Self::Config,
        mut layouter: impl Layouter<pallas::Base>,
    ) -> Result<(), plonk::Error> {
        // Load the Sinsemilla generator lookup table.
        SinsemillaChip::load(config.sinsemilla_config_1.clone(), &mut layouter)?;

        let ecc_chip = config.ecc_chip();

        // ---------------------------------------------------------------
        // Condition 9: Note commitment integrity.
        // NoteCommit_rcm(repr(g_d), repr(pk_d), v, rho, psi) ∈ {cm, ⊥}
        // ---------------------------------------------------------------

        let g_d = NonIdentityPoint::new(
            ecc_chip.clone(),
            layouter.namespace(|| "witness g_d"),
            self.g_d.as_ref().map(|gd| gd.to_affine()),
        )?;

        let pk_d = NonIdentityPoint::new(
            ecc_chip.clone(),
            layouter.namespace(|| "witness pk_d"),
            self.pk_d.as_ref().map(|pk| pk.to_affine()),
        )?;

        let v = assign_free_advice(
            layouter.namespace(|| "witness v"),
            config.advices[0],
            self.v,
        )?;

        let rho = assign_free_advice(
            layouter.namespace(|| "witness rho"),
            config.advices[0],
            self.rho,
        )?;

        let psi = assign_free_advice(
            layouter.namespace(|| "witness psi"),
            config.advices[0],
            self.psi,
        )?;

        let rcm = ScalarFixed::new(
            ecc_chip.clone(),
            layouter.namespace(|| "rcm"),
            self.rcm.as_ref().map(|rcm| rcm.inner()),
        )?;

        let cm = Point::new(
            ecc_chip.clone(),
            layouter.namespace(|| "witness cm"),
            self.cm.as_ref().map(|cm| cm.inner().to_affine()),
        )?;

        let derived_cm = note_commit(
            layouter.namespace(|| "NoteCommit_rcm(g_d, pk_d, v, rho, psi)"),
            config.sinsemilla_chip_1(),
            config.ecc_chip(),
            config.note_commit_chip(),
            g_d.inner(),
            pk_d.inner(),
            v.clone(),
            rho.clone(),
            psi.clone(),
            rcm,
        )?;

        // Constrain: derived cm == witnessed cm.
        derived_cm.constrain_equal(layouter.namespace(|| "cm integrity"), &cm)?;

        // Expose cmx = ExtractP(cm).
        layouter.constrain_instance(cm.extract_p().inner().cell(), config.primary, CMX)?;

        // Expose note value v.
        layouter.constrain_instance(v.cell(), config.primary, VALUE)?;

        // ---------------------------------------------------------------
        // Condition 11: Diversified address integrity.
        // pk_d = [ivk] * g_d
        // ---------------------------------------------------------------

        let ivk = assign_free_advice(
            layouter.namespace(|| "witness ivk (unconstrained)"),
            config.advices[0],
            self.ivk,
        )?;

        let ivk_scalar = ScalarVar::from_base(
            ecc_chip.clone(),
            layouter.namespace(|| "ivk to scalar"),
            &ivk,
        )?;

        let (derived_pk_d, _ivk) = g_d.mul(layouter.namespace(|| "[ivk] g_d"), ivk_scalar)?;

        derived_pk_d.constrain_equal(layouter.namespace(|| "pk_d equality"), &pk_d)?;

        // ---------------------------------------------------------------
        // Condition 12: Real nullifier derivation (private).
        // real_nf = DeriveNullifier_nk(rho, psi, cm)
        // ---------------------------------------------------------------

        let nk = assign_free_advice(
            layouter.namespace(|| "witness nk"),
            config.advices[0],
            self.nk,
        )?;

        let real_nf = derive_nullifier(
            layouter.namespace(|| "real_nf = DeriveNullifier_nk(rho, psi, cm)"),
            config.poseidon_chip(),
            config.add_chip(),
            ecc_chip.clone(),
            rho.clone(),
            &psi,
            &cm,
            nk.clone(),
        )?;

        // ---------------------------------------------------------------
        // Condition 14: Governance nullifier integrity.
        // gov_null = Poseidon(nk, Poseidon(vote_round_id, real_nf))
        // ---------------------------------------------------------------

        let vote_round_id = layouter.assign_region(
            || "copy vote_round_id from instance",
            |mut region| {
                region.assign_advice_from_instance(
                    || "vote_round_id",
                    config.primary,
                    VOTE_ROUND_ID,
                    config.advices[0],
                    0,
                )
            },
        )?;

        // Hash step 1: intermediate = Poseidon(vote_round_id, real_nf)
        let intermediate = {
            let poseidon_hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<2>,
                3,
                2,
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "gov_null Poseidon step 1 init"),
            )?;
            poseidon_hasher.hash(
                layouter.namespace(|| "Poseidon(vote_round_id, real_nf)"),
                [vote_round_id, real_nf.inner().clone()],
            )?
        };

        // Hash step 2: gov_null = Poseidon(nk, intermediate)
        let gov_null = {
            let poseidon_hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<2>,
                3,
                2,
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "gov_null Poseidon step 2 init"),
            )?;
            poseidon_hasher.hash(
                layouter.namespace(|| "Poseidon(nk, intermediate)"),
                [nk, intermediate],
            )?
        };

        // Constrain gov_null to public input.
        layouter.constrain_instance(gov_null.cell(), config.primary, GOV_NULL)?;

        // ---------------------------------------------------------------
        // Condition 10: Merkle path validity.
        // If is_note_real = 1: valid path from ExtractP(cm) to nc_root.
        // If is_note_real = 0: skipped (custom gate allows any root).
        // ---------------------------------------------------------------

        let root = {
            let path = self
                .path
                .map(|typed_path| typed_path.map(|node| node.inner()));
            let merkle_inputs = MerklePath::construct(
                [config.merkle_chip_1(), config.merkle_chip_2()],
                OrchardHashDomains::MerkleCrh,
                self.pos,
                path,
            );
            let leaf = cm.extract_p().inner().clone();
            merkle_inputs.calculate_root(layouter.namespace(|| "Merkle path"), leaf)?
        };

        // ---------------------------------------------------------------
        // Condition 13: IMT non-membership.
        // Prove that real_nf is NOT in the nullifier Indexed Merkle Tree.
        // ---------------------------------------------------------------

        // Witness low leaf data.
        let low_nf = assign_free_advice(
            layouter.namespace(|| "witness imt_low_nf"),
            config.advices[0],
            self.imt_low_nf,
        )?;
        let next_nf = assign_free_advice(
            layouter.namespace(|| "witness imt_next_nf"),
            config.advices[0],
            self.imt_next_nf,
        )?;

        // Hash low leaf: leaf_hash = Poseidon(low_nf, next_nf)
        let leaf_hash = {
            let poseidon_hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<2>,
                3,
                2,
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "IMT leaf hash init"),
            )?;
            poseidon_hasher.hash(
                layouter.namespace(|| "Poseidon(low_nf, next_nf)"),
                [low_nf.clone(), next_nf.clone()],
            )?
        };

        // Poseidon Merkle path (IMT_DEPTH levels).
        let mut current = leaf_hash;
        for i in 0..IMT_DEPTH {
            // Witness position bit.
            let pos_bit = assign_free_advice(
                layouter.namespace(|| format!("imt pos_bit {}", i)),
                config.advices[0],
                self.imt_leaf_pos
                    .map(|p| pallas::Base::from(((p >> i) & 1) as u64)),
            )?;

            // Witness sibling.
            let sibling = assign_free_advice(
                layouter.namespace(|| format!("imt sibling {}", i)),
                config.advices[0],
                self.imt_path.map(|path| path[i]),
            )?;

            // Conditional swap via q_imt_swap gate.
            let (left, right) = layouter.assign_region(
                || format!("imt swap level {}", i),
                |mut region| {
                    config.q_imt_swap.enable(&mut region, 0)?;

                    let pos_bit_cell =
                        pos_bit.copy_advice(|| "pos_bit", &mut region, config.advices[0], 0)?;
                    let current_cell =
                        current.copy_advice(|| "current", &mut region, config.advices[1], 0)?;
                    let sibling_cell =
                        sibling.copy_advice(|| "sibling", &mut region, config.advices[2], 0)?;

                    let left = region.assign_advice(
                        || "left",
                        config.advices[3],
                        0,
                        || {
                            pos_bit_cell
                                .value()
                                .copied()
                                .zip(current_cell.value().copied())
                                .zip(sibling_cell.value().copied())
                                .map(|((bit, cur), sib)| {
                                    if bit == pallas::Base::zero() {
                                        cur
                                    } else {
                                        sib
                                    }
                                })
                        },
                    )?;

                    let right = region.assign_advice(
                        || "right",
                        config.advices[4],
                        0,
                        || {
                            current_cell
                                .value()
                                .copied()
                                .zip(sibling_cell.value().copied())
                                .zip(left.value().copied())
                                .map(|((cur, sib), l)| cur + sib - l)
                        },
                    )?;

                    Ok((left, right))
                },
            )?;

            // Hash: parent = Poseidon(left, right)
            let parent = {
                let poseidon_hasher = PoseidonHash::<
                    pallas::Base,
                    _,
                    poseidon::P128Pow5T3,
                    ConstantLength<2>,
                    3,
                    2,
                >::init(
                    config.poseidon_chip(),
                    layouter.namespace(|| format!("IMT level {} hash init", i)),
                )?;
                poseidon_hasher.hash(
                    layouter.namespace(|| format!("Poseidon(left, right) level {}", i)),
                    [left, right],
                )?
            };
            current = parent;
        }
        let imt_root = current;

        // Non-membership range proofs.
        let (diff1, diff2_mask) = layouter.assign_region(
            || "imt non-membership",
            |mut region| {
                config.q_imt_nonmember.enable(&mut region, 0)?;

                // Row 0: low_nf, next_nf, real_nf, is_max
                low_nf.copy_advice(|| "low_nf", &mut region, config.advices[0], 0)?;
                let next_nf_cell =
                    next_nf.copy_advice(|| "next_nf", &mut region, config.advices[1], 0)?;
                let real_nf_cell = real_nf
                    .inner()
                    .copy_advice(|| "real_nf", &mut region, config.advices[2], 0)?;

                let is_max = region.assign_advice(
                    || "is_max",
                    config.advices[3],
                    0,
                    || {
                        next_nf_cell.value().copied().map(|nf| {
                            if nf == pallas::Base::zero() {
                                pallas::Base::one()
                            } else {
                                pallas::Base::zero()
                            }
                        })
                    },
                )?;

                // Row 1: inv_nf, diff1, diff2_mask
                region.assign_advice(
                    || "inv_nf",
                    config.advices[0],
                    1,
                    || {
                        next_nf_cell.value().copied().map(|nf| {
                            if nf == pallas::Base::zero() {
                                pallas::Base::zero()
                            } else {
                                nf.invert().unwrap()
                            }
                        })
                    },
                )?;

                let diff1 = region.assign_advice(
                    || "diff1",
                    config.advices[1],
                    1,
                    || {
                        real_nf_cell
                            .value()
                            .copied()
                            .zip(low_nf.value().copied())
                            .map(|(rnf, lnf)| rnf - lnf - pallas::Base::one())
                    },
                )?;

                let diff2_mask = region.assign_advice(
                    || "diff2_mask",
                    config.advices[2],
                    1,
                    || {
                        is_max
                            .value()
                            .copied()
                            .zip(next_nf_cell.value().copied())
                            .zip(real_nf_cell.value().copied())
                            .map(|((im, nnf), rnf)| {
                                (pallas::Base::one() - im) * (nnf - rnf - pallas::Base::one())
                            })
                    },
                )?;

                Ok((diff1, diff2_mask))
            },
        )?;

        // Range check diff1 < 2^250 (25 words × 10 bits).
        config.ecc_config.lookup_config.copy_check(
            layouter.namespace(|| "diff1 < 2^250"),
            diff1,
            25, // num_words
            true,
        )?;

        // Range check diff2_mask < 2^250.
        config.ecc_config.lookup_config.copy_check(
            layouter.namespace(|| "diff2_mask < 2^250"),
            diff2_mask,
            25,
            true,
        )?;

        // ---------------------------------------------------------------
        // Conditions 10 + 13 + 15: Custom gate region.
        // is_note_real * (root - anchor) = 0       (condition 10)
        // imt_root = nf_imt_root                    (condition 13)
        // (1 - is_note_real) * v = 0                (condition 15)
        // bool_check(is_note_real)
        // ---------------------------------------------------------------

        let is_note_real = assign_free_advice(
            layouter.namespace(|| "witness is_note_real"),
            config.advices[0],
            self.is_note_real.map(|b| pallas::Base::from(b as u64)),
        )?;

        layouter.assign_region(
            || "per-note checks",
            |mut region| {
                config.q_per_note.enable(&mut region, 0)?;

                is_note_real.copy_advice(|| "is_note_real", &mut region, config.advices[0], 0)?;
                v.copy_advice(|| "v", &mut region, config.advices[1], 0)?;
                root.copy_advice(|| "calculated root", &mut region, config.advices[2], 0)?;
                region.assign_advice_from_instance(
                    || "nc_root (anchor)",
                    config.primary,
                    NC_ROOT,
                    config.advices[3],
                    0,
                )?;
                imt_root.copy_advice(|| "imt_root", &mut region, config.advices[4], 0)?;
                region.assign_advice_from_instance(
                    || "nf_imt_root",
                    config.primary,
                    NF_IMT_ROOT,
                    config.advices[5],
                    0,
                )?;

                Ok(())
            },
        )?;

        Ok(())
    }
}

/// Public inputs to the per-note delegation circuit.
#[derive(Clone, Debug)]
pub struct Instance {
    /// Note commitment tree root (anchor).
    pub nc_root: pallas::Base,
    /// Nullifier IMT root.
    pub nf_imt_root: pallas::Base,
    /// Voting round identifier.
    pub vote_round_id: pallas::Base,
    /// Governance nullifier (published).
    pub gov_null: pallas::Base,
    /// ExtractP(cm) — extracted note commitment.
    pub cmx: pallas::Base,
    /// Note value v.
    pub v: pallas::Base,
}

impl Instance {
    /// Constructs an [`Instance`] from its constituent parts.
    pub fn from_parts(
        nc_root: pallas::Base,
        nf_imt_root: pallas::Base,
        vote_round_id: pallas::Base,
        gov_null: pallas::Base,
        cmx: pallas::Base,
        v: pallas::Base,
    ) -> Self {
        Instance {
            nc_root,
            nf_imt_root,
            vote_round_id,
            gov_null,
            cmx,
            v,
        }
    }

    /// Returns the public inputs as a vector of field elements for halo2.
    pub fn to_halo2_instance(&self) -> Vec<pallas::Base> {
        vec![
            self.nc_root,
            self.nf_imt_root,
            self.vote_round_id,
            self.gov_null,
            self.cmx,
            self.v,
        ]
    }
}

// TESTS

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{
        keys::{FullViewingKey, Scope, SpendValidatingKey, SpendingKey},
        note::{commitment::ExtractedNoteCommitment, Note},
        tree::MerklePath as OrchardMerklePath,
    };
    use ff::Field;
    use halo2_gadgets::poseidon::primitives::{self as poseidon, ConstantLength};
    use halo2_proofs::{
        dev::MockProver,
        plonk::{self as halo2_plonk, Circuit as Halo2Circuit, SingleVerifier},
        poly::commitment::Params,
        transcript::{Blake2bRead, Blake2bWrite},
    };
    use incrementalmerkletree::{Altitude, Hashable};
    use pasta_curves::{pallas, vesta};
    use rand::rngs::OsRng;

    /// Compute Poseidon hash of two field elements (out of circuit).
    fn poseidon_hash_2(a: pallas::Base, b: pallas::Base) -> pallas::Base {
        poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([a, b])
    }

    /// Helper: compute gov_null out-of-circuit.
    /// gov_null = Poseidon(nk, Poseidon(vote_round_id, real_nf))
    fn gov_null_hash(
        nk: pallas::Base,
        vote_round_id: pallas::Base,
        real_nf: pallas::Base,
    ) -> pallas::Base {
        let intermediate = poseidon_hash_2(vote_round_id, real_nf);
        poseidon_hash_2(nk, intermediate)
    }

    /// Compute the IMT root given a leaf hash, position, and Merkle siblings.
    fn imt_compute_root(
        leaf_hash: pallas::Base,
        pos: u32,
        path: &[pallas::Base; IMT_DEPTH],
    ) -> pallas::Base {
        let mut current = leaf_hash;
        for i in 0..IMT_DEPTH {
            let bit = (pos >> i) & 1;
            if bit == 0 {
                current = poseidon_hash_2(current, path[i]);
            } else {
                current = poseidon_hash_2(path[i], current);
            }
        }
        current
    }

    /// Precomputed empty subtree hashes for the IMT (Poseidon-based).
    /// empty[0] = Poseidon(0, 0), empty[i] = Poseidon(empty[i-1], empty[i-1]).
    fn empty_imt_hashes() -> Vec<pallas::Base> {
        let mut hashes = vec![poseidon_hash_2(pallas::Base::zero(), pallas::Base::zero())];
        for _ in 1..=IMT_DEPTH {
            let prev = *hashes.last().unwrap();
            hashes.push(poseidon_hash_2(prev, prev));
        }
        hashes
    }

    /// IMT proof data for testing.
    struct ImtProofData {
        root: pallas::Base,
        low_nf: pallas::Base,
        next_nf: pallas::Base,
        leaf_pos: u32,
        path: [pallas::Base; IMT_DEPTH],
    }

    /// Build an IMT with a single populated leaf at position 0 (rest empty).
    /// Returns the proof data for that leaf.
    fn build_single_leaf_imt(low_nf: pallas::Base, next_nf: pallas::Base) -> ImtProofData {
        let empty = empty_imt_hashes();
        let mut path = [pallas::Base::zero(); IMT_DEPTH];
        for i in 0..IMT_DEPTH {
            path[i] = empty[i];
        }
        let leaf_hash = poseidon_hash_2(low_nf, next_nf);
        let root = imt_compute_root(leaf_hash, 0, &path);
        ImtProofData {
            root,
            low_nf,
            next_nf,
            leaf_pos: 0,
            path,
        }
    }

    /// Build an IMT with two populated leaves at positions 0 and 1.
    /// Returns proof data for the leaf at `proof_pos` (must be 0 or 1).
    fn build_two_leaf_imt(
        leaf0: (pallas::Base, pallas::Base),
        leaf1: (pallas::Base, pallas::Base),
        proof_pos: u32,
    ) -> ImtProofData {
        assert!(proof_pos <= 1);
        let empty = empty_imt_hashes();
        let h0 = poseidon_hash_2(leaf0.0, leaf0.1);
        let h1 = poseidon_hash_2(leaf1.0, leaf1.1);

        let (low_nf, next_nf) = if proof_pos == 0 { leaf0 } else { leaf1 };

        let mut path = [pallas::Base::zero(); IMT_DEPTH];
        // Level 0 sibling is the other leaf's hash.
        path[0] = if proof_pos == 0 { h1 } else { h0 };
        // Levels 1..31: sibling is empty subtree hash at that level.
        for i in 1..IMT_DEPTH {
            path[i] = empty[i];
        }

        // Compute the root by hashing up from the proof leaf.
        let leaf_hash = if proof_pos == 0 { h0 } else { h1 };
        let root = imt_compute_root(leaf_hash, proof_pos, &path);

        ImtProofData {
            root,
            low_nf,
            next_nf,
            leaf_pos: proof_pos,
            path,
        }
    }

    /// Dummy IMT data for padded notes.
    /// Builds a single-leaf IMT with low_nf = real_nf - 1, next_nf = real_nf + 1
    /// so diff1 = diff2_mask = 0. Returns full ImtProofData including root.
    fn dummy_imt_for_padded(real_nf: pallas::Base) -> ImtProofData {
        build_single_leaf_imt(
            real_nf - pallas::Base::one(),
            real_nf + pallas::Base::one(),
        )
    }

    /// Return value from `make_test_data` bundling all test artefacts.
    struct TestData {
        circuit: Circuit,
        instance: Instance,
    }

    /// Build a valid per-note circuit with a real note, valid Merkle path, and valid IMT proof.
    fn make_test_data() -> TestData {
        let mut rng = OsRng;

        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let recipient = fvk.address_at(0u32, Scope::External);

        // Create a note with some value.
        let note_value = NoteValue::from_raw(42);
        let (_, _, dummy_parent) = Note::dummy(&mut rng, None);
        let note = Note::new(
            recipient,
            note_value,
            dummy_parent.nullifier(&fvk),
            &mut rng,
        );

        let rho = note.rho();
        let psi = note.rseed().psi(&rho);
        let rcm = note.rseed().rcm(&rho);
        let cm = note.commitment();
        let cmx = ExtractedNoteCommitment::from(cm.clone()).inner();

        // Build a Merkle tree with this note as a leaf at position 0.
        // At position 0 (all-left path), each sibling is the empty subtree
        // root at the corresponding altitude.
        let (merkle_path, anchor) = {
            let mut auth_path_base = [pallas::Base::zero(); MERKLE_DEPTH_ORCHARD];
            let mut current = MerkleHashOrchard::from_base(cmx);
            for i in 0..MERKLE_DEPTH_ORCHARD {
                let sibling = MerkleHashOrchard::empty_root(Altitude::from(i as u8));
                auth_path_base[i] = sibling.inner();
                current = MerkleHashOrchard::combine(Altitude::from(i as u8), &current, &sibling);
            }
            let path = OrchardMerklePath::new(0u32, auth_path_base);
            (path, current.inner())
        };

        // Keys.
        let nk_val = fvk.nk().inner();
        let ivk_val = {
            let ak: SpendValidatingKey = fvk.clone().into();
            let ak_p = pallas::Point::from(&ak);
            let rivk = fvk.rivk(Scope::External);
            crate::spec::commit_ivk(&crate::spec::extract_p(&ak_p), &nk_val, &rivk.inner()).unwrap()
        };

        // Compute real_nf out-of-circuit.
        let real_nf = note.nullifier(&fvk);

        let vote_round_id = pallas::Base::random(&mut rng);

        // Compute gov_null out-of-circuit.
        let gov_null = gov_null_hash(nk_val, vote_round_id, real_nf.0);

        // Build IMT non-membership proof: bracket real_nf with a close low leaf.
        // Leaf (real_nf - 2, real_nf + 2) proves real_nf is not in the tree.
        let imt = build_single_leaf_imt(
            real_nf.0 - pallas::Base::from(2u64),
            real_nf.0 + pallas::Base::from(2u64),
        );

        let circuit = Circuit {
            g_d: Value::known(recipient.g_d()),
            pk_d: Value::known(
                NonIdentityPallasPoint::from_bytes(&recipient.pk_d().to_bytes()).unwrap(),
            ),
            v: Value::known(note_value),
            rho: Value::known(rho.0),
            psi: Value::known(psi),
            rcm: Value::known(rcm),
            cm: Value::known(cm),
            path: Value::known(merkle_path.auth_path()),
            pos: Value::known(merkle_path.position()),
            ivk: Value::known(ivk_val),
            nk: Value::known(nk_val),
            is_note_real: Value::known(true),
            vote_round_id: Value::known(vote_round_id),
            imt_low_nf: Value::known(imt.low_nf),
            imt_next_nf: Value::known(imt.next_nf),
            imt_leaf_pos: Value::known(imt.leaf_pos),
            imt_path: Value::known(imt.path),
        };

        let instance = Instance::from_parts(
            anchor,
            imt.root,
            vote_round_id,
            gov_null,
            cmx,
            pallas::Base::from(42u64), // v as field element
        );

        TestData { circuit, instance }
    }

    #[test]
    fn scaffold_empty_circuit() {
        let circuit = Circuit::default();
        // Default circuit with no witnesses — MockProver may fail synthesis
        // due to Value::unknown(). We only verify the circuit shape is valid
        // (configure doesn't panic).
        let public_inputs = vec![pallas::Base::zero(); 6];
        let result = MockProver::run(K, &circuit, vec![public_inputs]);
        // Either synthesis succeeds (and verify may fail) or synthesis
        // returns an error — both are acceptable for a shape test.
        if let Ok(prover) = result {
            let _ = prover.verify();
        }
    }

    #[test]
    fn per_note_happy_path() {
        let t = make_test_data();
        let public_inputs = t.instance.to_halo2_instance();

        let prover = MockProver::run(K, &t.circuit, vec![public_inputs]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn per_note_wrong_cm() {
        let t = make_test_data();
        let mut circuit = t.circuit.clone();
        // Tamper with cm — note commitment integrity (condition 9) should fail.
        let (_, _, wrong_note) = Note::dummy(&mut OsRng, None);
        circuit.cm = Value::known(wrong_note.commitment());

        let public_inputs = t.instance.to_halo2_instance();
        let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn per_note_wrong_ivk() {
        let t = make_test_data();
        let mut circuit = t.circuit.clone();
        // Tamper with ivk — address integrity (condition 11) should fail.
        circuit.ivk = Value::known(pallas::Base::random(&mut OsRng));

        let public_inputs = t.instance.to_halo2_instance();
        let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn per_note_wrong_gov_null() {
        let t = make_test_data();
        // Tamper with gov_null public input — condition 14 should fail.
        let mut instance = t.instance.clone();
        instance.gov_null = pallas::Base::random(&mut OsRng);

        let public_inputs = instance.to_halo2_instance();
        let prover = MockProver::run(K, &t.circuit, vec![public_inputs]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn per_note_wrong_vote_round_id() {
        let t = make_test_data();
        // Tamper with vote_round_id — condition 14 should fail (gov_null won't match).
        let mut instance = t.instance.clone();
        instance.vote_round_id = pallas::Base::random(&mut OsRng);

        let public_inputs = instance.to_halo2_instance();
        let prover = MockProver::run(K, &t.circuit, vec![public_inputs]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn per_note_imt_wrong_root() {
        let t = make_test_data();
        // Tamper with NF_IMT_ROOT — condition 13 should fail.
        let mut instance = t.instance.clone();
        instance.nf_imt_root = pallas::Base::random(&mut OsRng);

        let public_inputs = instance.to_halo2_instance();
        let prover = MockProver::run(K, &t.circuit, vec![public_inputs]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn per_note_imt_wrong_low_leaf() {
        let t = make_test_data();
        let mut circuit = t.circuit.clone();
        // Set low_nf > real_nf so diff1 wraps around, failing the range check.
        // low_nf = p-1 is larger than any hash-derived real_nf with overwhelming probability.
        circuit.imt_low_nf = Value::known(-pallas::Base::one());

        let public_inputs = t.instance.to_halo2_instance();
        let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn per_note_imt_max_leaf() {
        // Test the max-leaf edge case: low leaf has next_nf = 0 (is the maximum).
        let mut rng = OsRng;

        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let recipient = fvk.address_at(0u32, Scope::External);

        let note_value = NoteValue::from_raw(42);
        let (_, _, dummy_parent) = Note::dummy(&mut rng, None);
        let note = Note::new(
            recipient,
            note_value,
            dummy_parent.nullifier(&fvk),
            &mut rng,
        );

        let rho = note.rho();
        let psi = note.rseed().psi(&rho);
        let rcm = note.rseed().rcm(&rho);
        let cm = note.commitment();
        let cmx = ExtractedNoteCommitment::from(cm.clone()).inner();

        let (merkle_path, anchor) = {
            let mut auth_path_base = [pallas::Base::zero(); MERKLE_DEPTH_ORCHARD];
            let mut current = MerkleHashOrchard::from_base(cmx);
            for i in 0..MERKLE_DEPTH_ORCHARD {
                let sibling = MerkleHashOrchard::empty_root(Altitude::from(i as u8));
                auth_path_base[i] = sibling.inner();
                current = MerkleHashOrchard::combine(Altitude::from(i as u8), &current, &sibling);
            }
            let path = OrchardMerklePath::new(0u32, auth_path_base);
            (path, current.inner())
        };

        let nk_val = fvk.nk().inner();
        let ivk_val = {
            let ak: SpendValidatingKey = fvk.clone().into();
            let ak_p = pallas::Point::from(&ak);
            let rivk = fvk.rivk(Scope::External);
            crate::spec::commit_ivk(&crate::spec::extract_p(&ak_p), &nk_val, &rivk.inner()).unwrap()
        };

        let real_nf = note.nullifier(&fvk);
        let vote_round_id = pallas::Base::random(&mut rng);
        let gov_null = gov_null_hash(nk_val, vote_round_id, real_nf.0);

        // Build a single-leaf IMT where the low leaf is the maximum
        // (next_nf = 0). The leaf value is close to real_nf so the
        // difference fits in the 250-bit range check.
        let imt = build_single_leaf_imt(
            real_nf.0 - pallas::Base::from(2u64), // low_nf just below real_nf
            pallas::Base::zero(),                  // next_nf = 0 → max leaf
        );

        let circuit = Circuit {
            g_d: Value::known(recipient.g_d()),
            pk_d: Value::known(
                NonIdentityPallasPoint::from_bytes(&recipient.pk_d().to_bytes()).unwrap(),
            ),
            v: Value::known(note_value),
            rho: Value::known(rho.0),
            psi: Value::known(psi),
            rcm: Value::known(rcm),
            cm: Value::known(cm),
            path: Value::known(merkle_path.auth_path()),
            pos: Value::known(merkle_path.position()),
            ivk: Value::known(ivk_val),
            nk: Value::known(nk_val),
            is_note_real: Value::known(true),
            vote_round_id: Value::known(vote_round_id),
            imt_low_nf: Value::known(imt.low_nf),
            imt_next_nf: Value::known(imt.next_nf),
            imt_leaf_pos: Value::known(imt.leaf_pos),
            imt_path: Value::known(imt.path),
        };

        let instance = Instance::from_parts(
            anchor,
            imt.root,
            vote_round_id,
            gov_null,
            cmx,
            pallas::Base::from(42u64),
        );

        let public_inputs = instance.to_halo2_instance();
        let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn per_note_padded_zero_value() {
        let mut rng = OsRng;

        // Padded note: is_note_real = false, v = 0.
        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let recipient = fvk.address_at(0u32, Scope::External);

        let note_value = NoteValue::zero();
        let (_, _, dummy_parent) = Note::dummy(&mut rng, None);
        let note = Note::new(
            recipient,
            note_value,
            dummy_parent.nullifier(&fvk),
            &mut rng,
        );

        let rho = note.rho();
        let psi = note.rseed().psi(&rho);
        let rcm = note.rseed().rcm(&rho);
        let cm = note.commitment();
        let cmx = ExtractedNoteCommitment::from(cm.clone()).inner();

        let nk_val = fvk.nk().inner();
        let ivk_val = {
            let ak: SpendValidatingKey = fvk.clone().into();
            let ak_p = pallas::Point::from(&ak);
            let rivk = fvk.rivk(Scope::External);
            crate::spec::commit_ivk(&crate::spec::extract_p(&ak_p), &nk_val, &rivk.inner()).unwrap()
        };

        let real_nf = note.nullifier(&fvk);
        let vote_round_id = pallas::Base::random(&mut rng);
        let gov_null = gov_null_hash(nk_val, vote_round_id, real_nf.0);

        // Build Merkle path (position 0, zero siblings — garbage for padded note).
        let merkle_path =
            OrchardMerklePath::new(0u32, [pallas::Base::zero(); MERKLE_DEPTH_ORCHARD]);

        // Dummy IMT data: diff1 = diff2_mask = 0 (passes range checks).
        // IMT root check is unconditional per spec §1.3.5, so we need a valid root.
        let imt = dummy_imt_for_padded(real_nf.0);

        let circuit = Circuit {
            g_d: Value::known(recipient.g_d()),
            pk_d: Value::known(
                NonIdentityPallasPoint::from_bytes(&recipient.pk_d().to_bytes()).unwrap(),
            ),
            v: Value::known(note_value),
            rho: Value::known(rho.0),
            psi: Value::known(psi),
            rcm: Value::known(rcm),
            cm: Value::known(cm),
            path: Value::known(merkle_path.auth_path()),
            pos: Value::known(merkle_path.position()),
            ivk: Value::known(ivk_val),
            nk: Value::known(nk_val),
            is_note_real: Value::known(false),
            vote_round_id: Value::known(vote_round_id),
            imt_low_nf: Value::known(imt.low_nf),
            imt_next_nf: Value::known(imt.next_nf),
            imt_leaf_pos: Value::known(imt.leaf_pos),
            imt_path: Value::known(imt.path),
        };

        // Anchor is arbitrary for padded notes (cond 10 skipped), but
        // IMT root must match (cond 13 is unconditional per spec §1.3.5).
        let instance = Instance::from_parts(
            pallas::Base::random(&mut rng), // arbitrary anchor (skipped)
            imt.root,                        // must match computed IMT root
            vote_round_id,
            gov_null,
            cmx,
            pallas::Base::zero(), // v = 0
        );

        let public_inputs = instance.to_halo2_instance();
        let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn per_note_padded_nonzero_value_fails() {
        let mut rng = OsRng;

        // Padded note with non-zero value should fail condition 15.
        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let recipient = fvk.address_at(0u32, Scope::External);

        let note_value = NoteValue::from_raw(100);
        let (_, _, dummy_parent) = Note::dummy(&mut rng, None);
        let note = Note::new(
            recipient,
            note_value,
            dummy_parent.nullifier(&fvk),
            &mut rng,
        );

        let rho = note.rho();
        let psi = note.rseed().psi(&rho);
        let rcm = note.rseed().rcm(&rho);
        let cm = note.commitment();
        let cmx = ExtractedNoteCommitment::from(cm.clone()).inner();

        let nk_val = fvk.nk().inner();
        let ivk_val = {
            let ak: SpendValidatingKey = fvk.clone().into();
            let ak_p = pallas::Point::from(&ak);
            let rivk = fvk.rivk(Scope::External);
            crate::spec::commit_ivk(&crate::spec::extract_p(&ak_p), &nk_val, &rivk.inner()).unwrap()
        };

        let real_nf = note.nullifier(&fvk);
        let vote_round_id = pallas::Base::random(&mut rng);
        let gov_null = gov_null_hash(nk_val, vote_round_id, real_nf.0);

        let merkle_path =
            OrchardMerklePath::new(0u32, [pallas::Base::zero(); MERKLE_DEPTH_ORCHARD]);

        let imt = dummy_imt_for_padded(real_nf.0);

        let circuit = Circuit {
            g_d: Value::known(recipient.g_d()),
            pk_d: Value::known(
                NonIdentityPallasPoint::from_bytes(&recipient.pk_d().to_bytes()).unwrap(),
            ),
            v: Value::known(note_value),
            rho: Value::known(rho.0),
            psi: Value::known(psi),
            rcm: Value::known(rcm),
            cm: Value::known(cm),
            path: Value::known(merkle_path.auth_path()),
            pos: Value::known(merkle_path.position()),
            ivk: Value::known(ivk_val),
            nk: Value::known(nk_val),
            is_note_real: Value::known(false), // padded, but v != 0
            vote_round_id: Value::known(vote_round_id),
            imt_low_nf: Value::known(imt.low_nf),
            imt_next_nf: Value::known(imt.next_nf),
            imt_leaf_pos: Value::known(imt.leaf_pos),
            imt_path: Value::known(imt.path),
        };

        let instance = Instance::from_parts(
            pallas::Base::random(&mut rng), // arbitrary anchor (skipped)
            imt.root,                        // valid IMT root
            vote_round_id,
            gov_null,
            cmx,
            pallas::Base::from(100u64),
        );

        let public_inputs = instance.to_halo2_instance();
        let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn per_note_real_wrong_merkle_path_fails() {
        let t = make_test_data();

        // Tamper with anchor — Merkle root won't match for a real note.
        let mut instance = t.instance.clone();
        instance.nc_root = pallas::Base::random(&mut OsRng);

        let public_inputs = instance.to_halo2_instance();
        let prover = MockProver::run(K, &t.circuit, vec![public_inputs]).unwrap();
        assert!(prover.verify().is_err());
    }

    // ================================================================
    // Tests below match circuit.rs quality standards.
    // ================================================================

    #[test]
    fn instance_to_halo2_roundtrip() {
        let t = make_test_data();
        let pi = t.instance.to_halo2_instance();

        assert_eq!(pi.len(), 6, "Expected exactly 6 public inputs");
        assert_eq!(pi[NC_ROOT], t.instance.nc_root, "Offset 0 must be nc_root");
        assert_eq!(pi[NF_IMT_ROOT], t.instance.nf_imt_root, "Offset 1 must be nf_imt_root");
        assert_eq!(pi[VOTE_ROUND_ID], t.instance.vote_round_id, "Offset 2 must be vote_round_id");
        assert_eq!(pi[GOV_NULL], t.instance.gov_null, "Offset 3 must be gov_null");
        assert_eq!(pi[CMX], t.instance.cmx, "Offset 4 must be cmx");
        assert_eq!(pi[VALUE], t.instance.v, "Offset 5 must be v");
    }

    #[test]
    fn default_circuit_is_consistent_with_without_witnesses() {
        let t = make_test_data();
        let empty = Halo2Circuit::without_witnesses(&t.circuit);

        let params = Params::<vesta::Affine>::new(K);
        let vk = halo2_plonk::keygen_vk(&params, &empty);
        assert!(vk.is_ok(), "keygen_vk must succeed on without_witnesses circuit");
    }

    #[test]
    fn cross_constraint_nk_binds_nullifier_and_gov_null() {
        // nk is shared between DeriveNullifier (condition 12) and the gov_null
        // Poseidon hash (condition 14). If it were possible to split nk across
        // those constraints, an attacker could derive a nullifier with one key
        // but a governance nullifier with another. Since nk is a single cell,
        // both constraints see the same value and the proof must fail when we
        // tamper with nk.
        let t = make_test_data();
        let mut circuit = t.circuit.clone();

        // Replace nk with a different key's nk.
        let sk2 = SpendingKey::random(&mut OsRng);
        let fvk2: FullViewingKey = (&sk2).into();
        circuit.nk = Value::known(fvk2.nk().inner());

        let public_inputs = t.instance.to_halo2_instance();
        let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn per_note_imt_two_leaf_tree() {
        // Exercise the two-leaf IMT helper: tree has two real leaves,
        // and we prove non-membership of real_nf which falls between them.
        let mut rng = OsRng;

        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let recipient = fvk.address_at(0u32, Scope::External);

        let note_value = NoteValue::from_raw(42);
        let (_, _, dummy_parent) = Note::dummy(&mut rng, None);
        let note = Note::new(
            recipient,
            note_value,
            dummy_parent.nullifier(&fvk),
            &mut rng,
        );

        let rho = note.rho();
        let psi = note.rseed().psi(&rho);
        let rcm = note.rseed().rcm(&rho);
        let cm = note.commitment();
        let cmx = ExtractedNoteCommitment::from(cm.clone()).inner();

        let (merkle_path, anchor) = {
            let mut auth_path_base = [pallas::Base::zero(); MERKLE_DEPTH_ORCHARD];
            let mut current = MerkleHashOrchard::from_base(cmx);
            for i in 0..MERKLE_DEPTH_ORCHARD {
                let sibling = MerkleHashOrchard::empty_root(Altitude::from(i as u8));
                auth_path_base[i] = sibling.inner();
                current = MerkleHashOrchard::combine(Altitude::from(i as u8), &current, &sibling);
            }
            let path = OrchardMerklePath::new(0u32, auth_path_base);
            (path, current.inner())
        };

        let nk_val = fvk.nk().inner();
        let ivk_val = {
            let ak: SpendValidatingKey = fvk.clone().into();
            let ak_p = pallas::Point::from(&ak);
            let rivk = fvk.rivk(Scope::External);
            crate::spec::commit_ivk(&crate::spec::extract_p(&ak_p), &nk_val, &rivk.inner()).unwrap()
        };

        let real_nf = note.nullifier(&fvk);
        let vote_round_id = pallas::Base::random(&mut rng);
        let gov_null = gov_null_hash(nk_val, vote_round_id, real_nf.0);

        // Two-leaf IMT: leaf0 brackets real_nf from below, leaf1 is unrelated.
        // Leaf0 = (real_nf - 2, real_nf + 2), Leaf1 = (real_nf + 10, real_nf + 20).
        let imt = build_two_leaf_imt(
            (real_nf.0 - pallas::Base::from(2u64), real_nf.0 + pallas::Base::from(2u64)),
            (real_nf.0 + pallas::Base::from(10u64), real_nf.0 + pallas::Base::from(20u64)),
            0, // prove using leaf 0
        );

        let circuit = Circuit {
            g_d: Value::known(recipient.g_d()),
            pk_d: Value::known(
                NonIdentityPallasPoint::from_bytes(&recipient.pk_d().to_bytes()).unwrap(),
            ),
            v: Value::known(note_value),
            rho: Value::known(rho.0),
            psi: Value::known(psi),
            rcm: Value::known(rcm),
            cm: Value::known(cm),
            path: Value::known(merkle_path.auth_path()),
            pos: Value::known(merkle_path.position()),
            ivk: Value::known(ivk_val),
            nk: Value::known(nk_val),
            is_note_real: Value::known(true),
            vote_round_id: Value::known(vote_round_id),
            imt_low_nf: Value::known(imt.low_nf),
            imt_next_nf: Value::known(imt.next_nf),
            imt_leaf_pos: Value::known(imt.leaf_pos),
            imt_path: Value::known(imt.path),
        };

        let instance = Instance::from_parts(
            anchor,
            imt.root,
            vote_round_id,
            gov_null,
            cmx,
            pallas::Base::from(42u64),
        );

        let public_inputs = instance.to_halo2_instance();
        let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    // ================================================================
    // Real prove/verify cycle (not MockProver).
    // Exercises the IPA polynomial commitment scheme, the Blake2b
    // transcript, and constraint-degree checks that MockProver skips.
    // ================================================================

    #[test]
    fn real_prove_verify_roundtrip() {
        let t = make_test_data();

        // Key generation from the empty (without-witnesses) circuit.
        let params = Params::<vesta::Affine>::new(K);
        let circuit_default = Circuit::default();
        let vk = halo2_plonk::keygen_vk(&params, &circuit_default).unwrap();
        let pk = halo2_plonk::keygen_pk(&params, vk.clone(), &circuit_default).unwrap();

        let pi = t.instance.to_halo2_instance();

        // Create proof.
        let mut transcript = Blake2bWrite::<_, vesta::Affine, _>::init(vec![]);
        halo2_plonk::create_proof(
            &params,
            &pk,
            &[t.circuit],
            &[&[&pi]],
            &mut OsRng,
            &mut transcript,
        )
        .unwrap();
        let proof_bytes = transcript.finalize();

        // Verify proof.
        let strategy = SingleVerifier::new(&params);
        let mut transcript = Blake2bRead::init(&proof_bytes[..]);
        assert!(
            halo2_plonk::verify_proof(&params, &vk, strategy, &[&[&pi]], &mut transcript).is_ok()
        );
    }
}
