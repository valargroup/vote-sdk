//! The Delegation circuit implementation.
//!
//! A single circuit proving all 15 conditions of the delegation ZKP:
//!
//! - **Condition 1**: Signed note commitment integrity.
//! - **Condition 2**: Nullifier integrity.
//! - **Condition 3**: Rho binding — keystone rho = Poseidon(cmx_1..4, gov_comm, vote_round_id).
//! - **Condition 4**: Spend authority.
//! - **Condition 5**: CommitIvk & diversified address integrity.
//! - **Condition 6**: Output note commitment integrity.
//! - **Condition 7**: Governance commitment integrity.
//! - **Condition 8**: Minimum voting weight.
//! - **Condition 9** (×4): Note commitment integrity.
//! - **Condition 10** (×4): Merkle path validity.
//! - **Condition 11** (×4): Diversified address integrity.
//! - **Condition 12** (×4): Private nullifier derivation.
//! - **Condition 13** (×4): IMT non-membership.
//! - **Condition 14** (×4): Governance nullifier publication.
//! - **Condition 15** (×4): Padded-note zero-value enforcement.

use alloc::vec::Vec;
use ff::Field;
use group::{Curve, GroupEncoding};
use halo2_proofs::{
    circuit::{floor_planner, AssignedCell, Layouter, Value},
    plonk::{self, Advice, Column, Constraints, Expression, Instance as InstanceColumn, Selector},
    poly::Rotation,
};
use pasta_curves::{arithmetic::CurveAffine, pallas, vesta};

use crate::{
    circuit::{
        commit_ivk::{CommitIvkChip, CommitIvkConfig},
        gadget::{
            add_chip::{AddChip, AddConfig},
            assign_free_advice, commit_ivk, derive_nullifier, note_commit, AddInstruction,
        },
        note_commit::{NoteCommitChip, NoteCommitConfig},
    },
    constants::{
        OrchardCommitDomains, OrchardFixedBases, OrchardFixedBasesFull, OrchardHashDomains,
    },
    keys::{
        CommitIvkRandomness, DiversifiedTransmissionKey, FullViewingKey, NullifierDerivingKey,
        Scope, SpendValidatingKey,
    },
    note::{
        commitment::{NoteCommitTrapdoor, NoteCommitment},
        nullifier::Nullifier,
        Note,
    },
    primitives::redpallas::{SpendAuth, VerificationKey},
    spec::NonIdentityPallasPoint,
    tree::MerkleHashOrchard,
    value::NoteValue,
};
use halo2_gadgets::{
    ecc::{
        chip::{EccChip, EccConfig},
        FixedPoint, NonIdentityPoint, Point, ScalarFixed, ScalarVar,
    },
    poseidon::{
        primitives::{self as poseidon, ConstantLength},
        Hash as PoseidonHash, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig,
    },
    sinsemilla::{
        chip::{SinsemillaChip, SinsemillaConfig},
        merkle::{
            chip::{MerkleChip, MerkleConfig},
            MerklePath as GadgetMerklePath,
        },
    },
    utilities::{
        bool_check,
        lookup_range_check::LookupRangeCheckConfig,
    },
};
use super::imt::IMT_DEPTH;
use crate::constants::MERKLE_DEPTH_ORCHARD;

// ================================================================
// Public input offsets (12 field elements).
// ================================================================

/// Public input offset for the derived nullifier.
const NF_SIGNED: usize = 0;
/// Public input offset for rk (x-coordinate).
const RK_X: usize = 1;
/// Public input offset for rk (y-coordinate).
const RK_Y: usize = 2;
/// Public input offset for the output note's extracted commitment (condition 6).
const CMX_NEW: usize = 3;
/// Public input offset for the governance commitment.
const GOV_COMM: usize = 4;
/// Public input offset for the vote round identifier.
const VOTE_ROUND_ID: usize = 5;
/// Public input offset for the note commitment tree root.
const NC_ROOT: usize = 6;
/// Public input offset for the nullifier IMT root.
const NF_IMT_ROOT: usize = 7;
/// Public input offsets for per-note governance nullifiers (derived from real notes).
const GOV_NULL_1: usize = 8;
const GOV_NULL_2: usize = 9;
const GOV_NULL_3: usize = 10;
const GOV_NULL_4: usize = 11;

/// Gov null offsets indexed by note slot.
const GOV_NULL_OFFSETS: [usize; 4] = [GOV_NULL_1, GOV_NULL_2, GOV_NULL_3, GOV_NULL_4];

/// Maximum proposal authority — the default for a fresh delegation.
///
/// Represented as a 16-bit bitmask where each bit authorizes voting on the
/// corresponding proposal (proposal ID = bit index from LSB).  Full authority
/// is `2^16 - 1 = 65535`, meaning all 16 proposals are authorized.
///
/// This constant is hashed into `gov_comm` (condition 7) as a constant-
/// constrained witness, baked into the verification key so a malicious prover
/// cannot substitute a different authority value.
pub(crate) const MAX_PROPOSAL_AUTHORITY: u64 = 65535; // 2^16 - 1

/// Domain tag for Vote Authority Notes (see `spec::DOMAIN_VAN`).
///
/// Prepended as the first Poseidon input in `gov_comm` (condition 7) for
/// domain separation from Vote Commitments in the shared tree.
pub(crate) const DOMAIN_VAN: u64 = 0;

/// Out-of-circuit rho binding hash used by the builder and tests.
pub(crate) fn rho_binding_hash(
    cmx_1: pallas::Base,
    cmx_2: pallas::Base,
    cmx_3: pallas::Base,
    cmx_4: pallas::Base,
    gov_comm: pallas::Base,
    vote_round_id: pallas::Base,
) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<6>, 3, 2>::init()
        .hash([cmx_1, cmx_2, cmx_3, cmx_4, gov_comm, vote_round_id])
}

/// Out-of-circuit governance commitment hash used by the builder and tests.
pub(crate) fn gov_commitment_hash(
    g_d_new_x: pallas::Base,
    pk_d_new_x: pallas::Base,
    v_total: pallas::Base,
    vote_round_id: pallas::Base,
    gov_comm_rand: pallas::Base,
) -> pallas::Base {
    let gov_comm_core =
        poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<6>, 3, 2>::init().hash([
            pallas::Base::from(DOMAIN_VAN),
            g_d_new_x,
            pk_d_new_x,
            v_total,
            vote_round_id,
            pallas::Base::from(MAX_PROPOSAL_AUTHORITY),
        ]);

    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init()
        .hash([gov_comm_core, gov_comm_rand])
}

// ================================================================
// Config
// ================================================================

/// Configuration for the Delegation circuit.
#[derive(Clone, Debug)]
pub struct Config {
    // The instance column (public inputs)
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
    // We use it to convert cm_signed from NoteCommitment to a Field point for the DeriveNullifier function.
    ecc_config: EccConfig<OrchardFixedBases>,
    // Poseidon chip config. Used in the DeriveNullifier.
    poseidon_config: PoseidonConfig<pallas::Base, 3, 2>,
    // Sinsemilla config 1 — used for loading the lookup table that
    // LookupRangeCheckConfig (and thus EccChip) depends on, for CommitIvk,
    // and for the signed note's NoteCommit. Uses advices[..5].
    sinsemilla_config_1:
        SinsemillaConfig<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases>,
    // Sinsemilla config 2 — a second instance for the output note's NoteCommit.
    // Uses advices[5..] so the two Sinsemilla chips can lay out side-by-side.
    // Two are needed for each NoteCommit. If these were reused, gates would conflict.
    sinsemilla_config_2:
        SinsemillaConfig<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases>,
    // Configuration to handle decomposition and canonicity checking for CommitIvk.
    commit_ivk_config: CommitIvkConfig,
    // Configuration for decomposition and canonicity checking for the signed note's NoteCommit.
    signed_note_commit_config: NoteCommitConfig,
    // Configuration for decomposition and canonicity checking for the output note's NoteCommit.
    new_note_commit_config: NoteCommitConfig,
    // Range check configuration for the 10-bit lookup table.
    // Used in condition 8 (minimum voting weight) to verify that
    // v_total - MIN_WEIGHT fits in 70 bits (non-negative).
    range_check: LookupRangeCheckConfig<pallas::Base, 10>,
    // Merkle config 1 — Sinsemilla-based Merkle path verification for condition 10.
    // Paired with sinsemilla_config_1. Uses advices[..5].
    merkle_config_1: MerkleConfig<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases>,
    // Merkle config 2 — second Merkle chip for condition 10, paired with sinsemilla_config_2.
    // Uses advices[5..]. Two configs are required because MerkleChip alternates between
    // them at each tree level (even levels use config 1, odd levels use config 2).
    merkle_config_2: MerkleConfig<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases>,
    // Per-note custom gate selector (conditions 10, 13, 15).
    // Enforces: is_note_real is boolean, padded notes have v=0,
    // real notes' Merkle root matches nc_root, IMT root matches nf_imt_root.
    q_per_note: Selector,
    // IMT conditional swap gate selector (condition 13).
    // At each of the 29 IMT Merkle levels, swaps (current, sibling) into (left, right)
    // based on the position bit before Poseidon hashing (condition 13).
    q_imt_swap: Selector,
    // Interval check gate selector (condition 13).
    // Proves low <= real_nf <= high by constraining x and x_shifted
    // values that are then range-checked to [0, 2^250).
    q_interval: Selector,
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

    fn commit_ivk_chip(&self) -> CommitIvkChip {
        CommitIvkChip::construct(self.commit_ivk_config.clone())
    }

    fn sinsemilla_chip_1(
        &self,
    ) -> SinsemillaChip<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases> {
        SinsemillaChip::construct(self.sinsemilla_config_1.clone())
    }

    fn sinsemilla_chip_2(
        &self,
    ) -> SinsemillaChip<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases> {
        SinsemillaChip::construct(self.sinsemilla_config_2.clone())
    }

    fn note_commit_chip_signed(&self) -> NoteCommitChip {
        NoteCommitChip::construct(self.signed_note_commit_config.clone())
    }

    fn note_commit_chip_new(&self) -> NoteCommitChip {
        NoteCommitChip::construct(self.new_note_commit_config.clone())
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

    fn range_check_config(&self) -> LookupRangeCheckConfig<pallas::Base, 10> {
        self.range_check
    }
}

// ================================================================
// NoteSlotWitness
// ================================================================

/// Private witness data for a single note slot (conditions 9–15).
#[derive(Clone, Debug, Default)]
pub struct NoteSlotWitness {
    pub(crate) g_d: Value<NonIdentityPallasPoint>,
    pub(crate) pk_d: Value<NonIdentityPallasPoint>,
    pub(crate) v: Value<NoteValue>,
    pub(crate) rho: Value<pallas::Base>,
    pub(crate) psi: Value<pallas::Base>,
    pub(crate) rcm: Value<NoteCommitTrapdoor>,
    pub(crate) cm: Value<NoteCommitment>,
    pub(crate) path: Value<[MerkleHashOrchard; MERKLE_DEPTH_ORCHARD]>,
    pub(crate) pos: Value<u32>,
    pub(crate) is_note_real: Value<bool>,
    pub(crate) imt_low: Value<pallas::Base>,
    pub(crate) imt_high: Value<pallas::Base>,
    pub(crate) imt_leaf_pos: Value<u32>,
    pub(crate) imt_path: Value<[pallas::Base; IMT_DEPTH]>,
}

// ================================================================
// Circuit
// ================================================================

/// The Delegation circuit.
///
/// Proves all 15 conditions of the delegation ZKP (see README for details).
#[derive(Clone, Debug, Default)]
pub struct Circuit {
    // Signed note witnesses (conditions 1–5).
    nk: Value<NullifierDerivingKey>,
    rho_signed: Value<pallas::Base>,
    psi_signed: Value<pallas::Base>,
    cm_signed: Value<NoteCommitment>,
    ak: Value<SpendValidatingKey>,
    alpha: Value<pallas::Scalar>,
    rivk: Value<CommitIvkRandomness>,
    rcm_signed: Value<NoteCommitTrapdoor>,
    g_d_signed: Value<NonIdentityPallasPoint>,
    pk_d_signed: Value<DiversifiedTransmissionKey>,
    // Output note witnesses (condition 6).
    // These are free witnesses.
    g_d_new: Value<NonIdentityPallasPoint>,
    pk_d_new: Value<DiversifiedTransmissionKey>,
    psi_new: Value<pallas::Base>,
    rcm_new: Value<NoteCommitTrapdoor>,
    // Per-note slots (conditions 9–15).
    notes: [NoteSlotWitness; 4],
    // Gov commitment blinding factor (condition 7).
    gov_comm_rand: Value<pallas::Base>,
    // Condition 8 (minimum voting weight) has no witnesses — it uses v_total
    // derived in-circuit from per-note values plus the constant MIN_WEIGHT.
}

impl Circuit {
    /// Constructs a `Circuit` from a note, its full viewing key, and the spend auth randomizer.
    pub fn from_note_unchecked(fvk: &FullViewingKey, note: &Note, alpha: pallas::Scalar) -> Self {
        let sender_address = note.recipient();
        let rho_signed = note.rho();
        let psi_signed = note.rseed().psi(&rho_signed);
        let rcm_signed = note.rseed().rcm(&rho_signed);
        Circuit {
            nk: Value::known(*fvk.nk()),
            rho_signed: Value::known(rho_signed.into_inner()),
            psi_signed: Value::known(psi_signed),
            cm_signed: Value::known(note.commitment()),
            ak: Value::known(fvk.clone().into()),
            alpha: Value::known(alpha),
            rivk: Value::known(fvk.rivk(Scope::External)),
            rcm_signed: Value::known(rcm_signed),
            g_d_signed: Value::known(sender_address.g_d()),
            pk_d_signed: Value::known(*sender_address.pk_d()),
            ..Default::default()
        }
    }

    /// Sets the output note witness fields (condition 6).
    pub fn with_output_note(mut self, output_note: &Note) -> Self {
        let rho_new = output_note.rho();
        let psi_new = output_note.rseed().psi(&rho_new);
        let rcm_new = output_note.rseed().rcm(&rho_new);
        self.g_d_new = Value::known(output_note.recipient().g_d());
        self.pk_d_new = Value::known(*output_note.recipient().pk_d());
        self.psi_new = Value::known(psi_new);
        self.rcm_new = Value::known(rcm_new);
        self
    }

    /// Sets the four per-note slot witnesses (conditions 9–15).
    pub fn with_notes(mut self, notes: [NoteSlotWitness; 4]) -> Self {
        self.notes = notes;
        self
    }

    /// Sets the governance commitment blinding factor (condition 7).
    pub fn with_gov_comm_rand(mut self, gov_comm_rand: pallas::Base) -> Self {
        self.gov_comm_rand = Value::known(gov_comm_rand);
        self
    }
}

// ================================================================
// plonk::Circuit implementation
// ================================================================

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

        // Per-note custom gates (conditions 10, 13, 15).
        // q_per_note is a selector that activates these constraints only on rows
        // where note data is assigned. Each of the (up to 4) input notes gets one
        // such row; on all other rows the selector is 0 and the gate is inactive.
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
                    // Cond 15: padded notes must have v=0. Real notes pass trivially.
                    (
                        "(1 - is_note_real) * v = 0",
                        (one.clone() - is_note_real.clone()) * v,
                    ),
                    // Prevent is_note_real from being an arbitrary field element.
                    ("bool_check is_note_real", bool_check(is_note_real.clone())),
                    // Cond 10: real notes' Merkle root must match the public nc_root.
                    // Padded notes skip this (is_note_real=0 zeroes the expression).
                    (
                        "is_note_real * (root - anchor) = 0",
                        is_note_real * (root - anchor),
                    ),
                    // Cond 13: IMT root from non-membership proof must match public
                    // nf_imt_root. Not gated — padded notes check too (§1.3.5).
                    ("imt_root = nf_imt_root", imt_root - nf_imt_root),
                ],
            )
        });

        // IMT conditional swap gate (condition 13).
        // At each level of the Poseidon Merkle path, we need to place (current, sibling)
        // into (left, right) based on the position bit. If pos_bit=0, current is the
        // left child; if pos_bit=1, they swap.
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
                    // left = current + pos_bit * (sibling - current)
                    // i.e. left = current when pos_bit=0, left = sibling when pos_bit=1.
                    (
                        "swap left",
                        left.clone()
                            - current.clone()
                            - pos_bit.clone() * (sibling.clone() - current.clone()),
                    ),
                    // left + right = current + sibling (conservation: no values lost).
                    ("swap right", left + right - current - sibling),
                    // pos_bit must be 0 or 1.
                    ("bool_check pos_bit", bool_check(pos_bit)),
                ],
            )
        });

        // Interval check gate (condition 13, (low, high) leaf model).
        // Proves low <= real_nf <= high by constraining:
        //   x = real_nf - low  (range-checked to [0, 2^250) outside)
        //   x_shifted = 2^250 - y + x - 1  (range-checked to [0, 2^250) outside)
        // where y = high - low (the interval width).
        //
        // NOTE: The 250-bit range checks are only sound when every IMT bracket
        // has width < 2^250. The IMT MUST be initialized with ~17 sentinel
        // nullifiers at multiples of 2^250 before any real nullifiers are
        // inserted. This invariant holds permanently once established, since
        // inserting a nullifier only splits a bracket into two smaller ones.
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

        // Sinsemilla config 1 + Merkle config 1.
        // Sinsemilla: loads the lookup table, used for CommitIvk, and the signed
        // note's NoteCommit. Uses advices[..5].
        // Merkle: used for even levels of the per-note Merkle path (condition 10).
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

        // Sinsemilla config 2 + Merkle config 2.
        // Sinsemilla: used for the output note's NoteCommit. Uses advices[5..],
        // so the two Sinsemilla chips lay out side-by-side.
        // Merkle: used for odd levels of the per-note Merkle path (condition 10).
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

        // Configuration to handle decomposition and canonicity checking for CommitIvk.
        let commit_ivk_config = CommitIvkChip::configure(meta, advices);

        // Configuration for decomposition and canonicity checking for the signed note's NoteCommit.
        let signed_note_commit_config =
            NoteCommitChip::configure(meta, advices, sinsemilla_config_1.clone());

        // Configuration for decomposition and canonicity checking for the output note's NoteCommit.
        let new_note_commit_config =
            NoteCommitChip::configure(meta, advices, sinsemilla_config_2.clone());

        Config {
            primary,
            advices,
            add_config,
            ecc_config,
            poseidon_config,
            sinsemilla_config_1,
            sinsemilla_config_2,
            commit_ivk_config,
            signed_note_commit_config,
            new_note_commit_config,
            range_check,
            merkle_config_1,
            merkle_config_2,
            q_per_note,
            q_imt_swap,
            q_interval,
        }
    }

    #[allow(non_snake_case)]
    fn synthesize(
        &self,
        config: Self::Config,
        mut layouter: impl Layouter<pallas::Base>,
    ) -> Result<(), plonk::Error> {
        // Load the Sinsemilla generator lookup table (needed by ECC range checks).
        SinsemillaChip::load(config.sinsemilla_config_1.clone(), &mut layouter)?;

        // Construct the ECC chip.
        // It is needed to derive cm_signed and ak_P ECC points.
        let ecc_chip = config.ecc_chip();

        // Witness ak_P (spend validating key) as a non-identity curve point.
        // Shared between spend authority and CommitIvk.
        // If ak_P were allowed to be the identity point (zero of the curve group), it would be a degenerate
        // key with no cryptographic strength - any signature would trivially verify against it.
        // By constraining, we ensure that the delegated spend authority is backed by a real meaningful
        // public key.
        let ak_P: Value<pallas::Point> = self.ak.as_ref().map(|ak| ak.into());
        let ak_P = NonIdentityPoint::new(
            ecc_chip.clone(),
            layouter.namespace(|| "witness ak_P"),
            ak_P.map(|ak_P| ak_P.to_affine()),
        )?;

        // Witness g_d_signed (diversified generator from the note's address).
        // Shared between diversified address integrity check and (future) note commitment.
        let g_d_signed = NonIdentityPoint::new(
            ecc_chip.clone(),
            layouter.namespace(|| "witness g_d_signed"),
            self.g_d_signed.as_ref().map(|gd| gd.to_affine()),
        )?;

        // Witness nk (nullifier deriving key).
        let nk = assign_free_advice(
            layouter.namespace(|| "witness nk"),
            config.advices[0],
            self.nk.map(|nk| nk.inner()),
        )?;

        // Witness rho_signed.
        // This is the nullifier of the note that was spent to create this note. It is
        // a Nullifier type (a Pallas base field element) that serves as a unique, per-note domain
        // separator.
        // rho ensures that even if two notes have identical contents, they will produce
        // different nullifiers because they were created by spending different input notes.
        // rho provides deterministic, structural uniqueness. It is the nullifier of the
        // spend input note so it chains each note to its creation context. A single tx
        // can create multiple output notes from the same input. All those outputs share the same
        // rho. If nullifier derivation only used rho (no psi), outputs from the same input could collide.
        let rho_signed = assign_free_advice(
            layouter.namespace(|| "witness rho_signed"),
            config.advices[0],
            self.rho_signed,
        )?;

        // Witness psi_signed.
        // Pseudorandom field element derived from the note's random
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
        let psi_signed = assign_free_advice(
            layouter.namespace(|| "witness psi_signed"),
            config.advices[0],
            self.psi_signed,
        )?;

        // Witness cm_signed as an ECC point, which is the form DeriveNullifier expects.
        let cm_signed = Point::new(
            ecc_chip.clone(),
            layouter.namespace(|| "witness cm_signed"),
            self.cm_signed.as_ref().map(|cm| cm.inner().to_affine()),
        )?;

        // ---------------------------------------------------------------
        // Condition 2: Nullifier integrity.
        // nf_signed = DeriveNullifier_nk(rho_signed, psi_signed, cm_signed)
        // ---------------------------------------------------------------

        // Nullifier integrity: derive nf_signed = DeriveNullifier(nk, rho_signed, psi_signed, cm_signed).
        let nf_signed = derive_nullifier(
            layouter
                .namespace(|| "nf_signed = DeriveNullifier_nk(rho_signed, psi_signed, cm_signed)"),
            config.poseidon_chip(),
            config.add_chip(),
            ecc_chip.clone(),
            rho_signed.clone(), // clone so rho_signed remains available for note_commit
            &psi_signed,
            &cm_signed,
            nk.clone(), // clone so nk remains available for commit_ivk
        )?;

        // Constrain nf_signed to equal the public input.
        // Enforce that the nullifier computed inside the circuit matches the nullifier provided
        // as a public input from outside the circuit (supplied at NF_SIGNED of the public input)
        layouter.constrain_instance(nf_signed.inner().cell(), config.primary, NF_SIGNED)?;

        // ---------------------------------------------------------------
        // Condition 4: Spend authority.
        // rk = [alpha] * SpendAuthG + ak_P
        // ---------------------------------------------------------------

        // Spend authority
        // Proves that the public rk is a valid rerandomization of the prover's ak.
        // The out-of-circuit verifier checks that the keystone signature is valid under rk,
        // so this links the ZKP to the signature without revealing ak.
        {
            // Witness alpha (spend auth randomizer) as a full-width fixed scalar.
            let alpha =
                ScalarFixed::new(ecc_chip.clone(), layouter.namespace(|| "alpha"), self.alpha)?;

            // alpha_commitment = [alpha] SpendAuthG
            let (alpha_commitment, _) = {
                // SpendAuthG is a fixed generator point on the Pallas elliptic curve, used
                // specifically for spend authorization in the Orchard protocol.
                let spend_auth_g = OrchardFixedBasesFull::SpendAuthG;
                let spend_auth_g = FixedPoint::from_inner(ecc_chip.clone(), spend_auth_g);
                spend_auth_g.mul(layouter.namespace(|| "[alpha] SpendAuthG"), alpha)?
            };

            // rk = [alpha] SpendAuthG + ak_P
            let rk = alpha_commitment.add(layouter.namespace(|| "rk"), &ak_P)?;

            // Constrain rk to equal the public inputs (x and y coordinates).
            layouter.constrain_instance(rk.inner().x().cell(), config.primary, RK_X)?;
            layouter.constrain_instance(rk.inner().y().cell(), config.primary, RK_Y)?;
        }

        // ---------------------------------------------------------------
        // Condition 5: CommitIvk → ivk (internal wire, not a public input).
        // pk_d_signed = [ivk] * g_d_signed.
        // ---------------------------------------------------------------

        // Diversified address integrity.
        // ivk = ⊥ or pk_d_signed = [ivk] * g_d_signed
        // where ivk = CommitIvk_rivk(ExtractP(ak_P), nk)
        //
        // The ⊥ case is handled internally by CommitDomain::short_commit:
        // incomplete addition allows ⊥ to occur, and synthesis detects
        // these edge cases and aborts proof creation.
        let ivk_cell = {
            let ivk = {
                // ExtractP(ak_P) -- extract the x-coordinate from the curve point
                let ak = ak_P.extract_p().inner().clone();
                let rivk = ScalarFixed::new(
                    ecc_chip.clone(),
                    layouter.namespace(|| "rivk"),
                    self.rivk.map(|rivk| rivk.inner()),
                )?;

                // Commit ak and nk with rivk randomness, creating an ivk.
                commit_ivk(
                    config.sinsemilla_chip_1(),
                    ecc_chip.clone(),
                    config.commit_ivk_chip(),
                    layouter.namespace(|| "CommitIvk"),
                    ak,
                    nk.clone(),
                    rivk,
                )?
            };

            // ivk is internal — NOT constrained to a public input.
            let ivk_cell = ivk.inner().clone();

            // Convert ivk (an x-coordinate) to a variable-base scalar for EC multiplication.
            let ivk_scalar =
                ScalarVar::from_base(ecc_chip.clone(), layouter.namespace(|| "ivk"), ivk.inner())?;

            // [ivk] g_d_signed - derive the expected pk_d
            let (derived_pk_d_signed, _ivk) =
                g_d_signed.mul(layouter.namespace(|| "[ivk] g_d_signed"), ivk_scalar)?;

            // Witness pk_d_signed and constrain it to equal the derived value.
            let pk_d_signed = NonIdentityPoint::new(
                ecc_chip.clone(),
                layouter.namespace(|| "witness pk_d_signed"),
                self.pk_d_signed
                    .map(|pk_d_signed| pk_d_signed.inner().to_affine()),
            )?;
            derived_pk_d_signed
                .constrain_equal(layouter.namespace(|| "pk_d_signed equality"), &pk_d_signed)?;

            ivk_cell
        };

        // ---------------------------------------------------------------
        // Condition 1: Signed note commitment integrity.
        // NoteCommit_rcm_signed(g_d_signed, pk_d_signed, 0, rho_signed, psi_signed) = cm_signed
        // ---------------------------------------------------------------

        // signed note commitment integrity.
        // NoteCommit_rcm_signed(repr(g_d_signed), repr(pk_d_signed), 0,
        //                        rho_signed, psi_signed) = cm_signed
        // No null option: the signed note must have a valid commitment.
        {
            // Re-witness pk_d_signed for NoteCommit (need inner() from the constrained point).
            let pk_d_signed_for_nc = NonIdentityPoint::new(
                ecc_chip.clone(),
                layouter.namespace(|| "pk_d_signed for note_commit"),
                self.pk_d_signed
                    .map(|pk_d_signed| pk_d_signed.inner().to_affine()),
            )?;

            let rcm_signed = ScalarFixed::new(
                ecc_chip.clone(),
                layouter.namespace(|| "rcm_signed"),
                self.rcm_signed.as_ref().map(|rcm| rcm.inner()),
            )?;

            // The signed note's value is always 0.
            // Zero is enforced transitively: v_signed feeds into NoteCommit -> cm_signed
            // -> derive_nullifier -> nf_signed, which is constrained to the public input.
            // Any non-zero value would produce a different nf_signed, breaking the proof.
            let v_signed = assign_free_advice(
                layouter.namespace(|| "v_signed = 0"),
                config.advices[0],
                Value::known(NoteValue::zero()),
            )?;

            // Compute NoteCommit from witness data.
            let derived_cm_signed = note_commit(
                layouter.namespace(|| "NoteCommit_rcm_signed(g_d, pk_d, 0, rho, psi)"),
                config.sinsemilla_chip_1(),
                config.ecc_chip(),
                config.note_commit_chip_signed(),
                g_d_signed.inner(),
                pk_d_signed_for_nc.inner(),
                v_signed,
                rho_signed.clone(),
                psi_signed,
                rcm_signed,
            )?;

            // Strict equality — no null/bottom option.
            derived_cm_signed
                .constrain_equal(layouter.namespace(|| "cm_signed integrity"), &cm_signed)?;
        }

        // ---------------------------------------------------------------
        // Read shared public inputs from instance column.
        // ---------------------------------------------------------------

        // Rho binding (condition 3).
        // rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, gov_comm, vote_round_id)
        // Binds the signed note to the exact notes being delegated, the governance
        // commitment, and the round, making the keystone signature non-replayable.
        //
        // Public inputs live in the instance column, but gates can only constrain
        // advice cells. assign_advice_from_instance copies each public input into an
        // advice cell with a copy constraint, so the prover cannot substitute a
        // different value. The resulting cells are then passed into downstream gates.

        // gov_comm: used in condition 3 (rho binding hash) and condition 7 (gov
        // commitment integrity check).
        let gov_comm_cell = layouter.assign_region(
            || "copy gov_comm from instance",
            |mut region| {
                region.assign_advice_from_instance(
                    || "gov_comm",
                    config.primary,
                    GOV_COMM,
                    config.advices[0],
                    0,
                )
            },
        )?;

        // vote_round_id: used in condition 3 (rho binding hash) and condition 7
        // (gov commitment integrity check).
        let vote_round_id_cell = layouter.assign_region(
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

        // nc_root: the note commitment tree anchor. Each real note's Merkle root
        // is checked against this in condition 10 (via q_per_note gate).
        let nc_root_cell = layouter.assign_region(
            || "copy nc_root from instance",
            |mut region| {
                region.assign_advice_from_instance(
                    || "nc_root",
                    config.primary,
                    NC_ROOT,
                    config.advices[0],
                    0,
                )
            },
        )?;

        // nf_imt_root: the nullifier IMT root at snapshot height. Each note's IMT
        // non-membership proof root is checked against this in condition 13
        // (via q_per_note gate).
        let nf_imt_root_cell = layouter.assign_region(
            || "copy nf_imt_root from instance",
            |mut region| {
                region.assign_advice_from_instance(
                    || "nf_imt_root",
                    config.primary,
                    NF_IMT_ROOT,
                    config.advices[0],
                    0,
                )
            },
        )?;

        // ---------------------------------------------------------------
        // Conditions 9–15: prove ownership and unspentness of each delegated note.
        // ---------------------------------------------------------------

        // For each of the 4 note slots, synthesize_note_slot proves:
        //   - I know the note's contents and it has a valid commitment (cond 9)
        //   - The commitment exists in the mainchain note tree (cond 10)
        //   - The note belongs to my key (cond 11)
        //   - The note's nullifier is NOT in the spent-nullifier IMT (cond 12-13)
        //   - A governance nullifier is correctly derived for this note (cond 14)
        //   - Padded (unused) slots have zero value (cond 15)
        //
        // Returns three values per slot for use in the global conditions that follow:
        //   cmx_i      — hashed into rho_signed (condition 3)
        //   v_i        — summed into v_total (conditions 7 and 8)
        //   gov_null_i — exposed as public input

        let mut cmx_cells = Vec::with_capacity(4);
        let mut v_cells = Vec::with_capacity(4);
        let mut gov_null_cells = Vec::with_capacity(4);

        for i in 0..4 {
            let (cmx_i, v_i, gov_null_i) = synthesize_note_slot(
                &config,
                &mut layouter,
                ecc_chip.clone(),
                &ivk_cell,
                &nk,
                &vote_round_id_cell,
                &nc_root_cell,
                &nf_imt_root_cell,
                &self.notes[i],
                i,
                GOV_NULL_OFFSETS[i],
            )?;
            cmx_cells.push(cmx_i);
            v_cells.push(v_i);
            gov_null_cells.push(gov_null_i);
        }

        // ---------------------------------------------------------------
        // Condition 3: Rho binding.
        // rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, gov_comm, vote_round_id)
        // ---------------------------------------------------------------

        // The keystone note's rho is deterministically derived from the 4 note
        // commitments, the gov commitment, and the vote round. This binds the
        // keystone signature to the exact set of notes being delegated — replaying
        // the signature with different notes would produce a different rho, which
        // would change the nullifier (cond 2) and break the proof.
        {
            // Hash the 6 inputs: 4 note commitment x-coords (from cond 9),
            // gov_comm (public input), and vote_round_id (public input).
            let poseidon_message = [
                cmx_cells[0].clone(),
                cmx_cells[1].clone(),
                cmx_cells[2].clone(),
                cmx_cells[3].clone(),
                gov_comm_cell.clone(),
                vote_round_id_cell.clone(),
            ];
            let poseidon_hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<6>,
                3,
                2,
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "rho binding Poseidon init"),
            )?;
            let derived_rho = poseidon_hasher.hash(
                layouter.namespace(|| "Poseidon(cmx_1..4, gov_comm, vote_round_id)"),
                poseidon_message,
            )?;

            // The derived rho must equal the rho_signed used in condition 1 (note
            // commitment) and condition 2 (nullifier). This closes the binding.
            layouter.assign_region(
                || "rho binding equality",
                |mut region| region.constrain_equal(derived_rho.cell(), rho_signed.cell()),
            )?;
        }

        // ---------------------------------------------------------------
        // Condition 6: Output note commitment integrity.
        // Returns (g_d_new_x, pk_d_new_x) for condition 7.
        // ---------------------------------------------------------------

        // Output note commitment integrity (condition 6).
        //
        // ExtractP(NoteCommit_rcm_new(repr(g_d_new), repr(pk_d_new), 0,
        //          rho_new, psi_new)) ∈ {cmx_new, ⊥}
        //
        // where rho_new = nf_signed (the nullifier derived in condition 2).
        //
        // The output address (g_d_new, pk_d_new) is NOT checked against ivk.
        // The voting hotkey is bound transitively through gov_comm (condition 7)
        // which is hashed into rho_signed (condition 3), so the keystone
        // signature authenticates the output address without an in-circuit check.
        //
        // Returns g_d_new_x and pk_d_new_x for reuse in condition 7.
        let (g_d_new_x, pk_d_new_x) = {
            // Witness g_d_new (diversified generator of the output note's address).
            let g_d_new = NonIdentityPoint::new(
                ecc_chip.clone(),
                layouter.namespace(|| "witness g_d_new"),
                self.g_d_new.as_ref().map(|gd| gd.to_affine()),
            )?;

            // Witness pk_d_new (diversified transmission key of the output note's address).
            let pk_d_new = NonIdentityPoint::new(
                ecc_chip.clone(),
                layouter.namespace(|| "witness pk_d_new"),
                self.pk_d_new.map(|pk_d_new| pk_d_new.inner().to_affine()),
            )?;

            // rho_new = nf_signed: the output note's rho is chained from the
            // signed note's nullifier. This reuses the same cell that was
            // constrained to the public input in condition 2.
            let rho_new = nf_signed.inner().clone();

            // Witness psi_new.
            let psi_new = assign_free_advice(
                layouter.namespace(|| "witness psi_new"),
                config.advices[0],
                self.psi_new,
            )?;

            let rcm_new = ScalarFixed::new(
                ecc_chip.clone(),
                layouter.namespace(|| "rcm_new"),
                self.rcm_new.as_ref().map(|rcm_new| rcm_new.inner()),
            )?;

            // The output note's value is always 0.
            // Zero is enforced transitively: v_new feeds into NoteCommit -> cm_new,
            // whose x-coordinate is constrained to the CMX_NEW public input.
            // Any non-zero value would produce a different cmx, breaking the proof.
            let v_new = assign_free_advice(
                layouter.namespace(|| "v_new = 0"),
                config.advices[0],
                Value::known(NoteValue::zero()),
            )?;

            // Compute NoteCommit for the output note using the second chip pair.
            let cm_new = note_commit(
                layouter.namespace(|| "NoteCommit_rcm_new(g_d_new, pk_d_new, 0, rho_new, psi_new)"),
                config.sinsemilla_chip_2(),
                config.ecc_chip(),
                config.note_commit_chip_new(),
                g_d_new.inner(),
                pk_d_new.inner(),
                v_new,
                rho_new,
                psi_new,
                rcm_new,
            )?;

            // Extract the x-coordinate of the commitment point.
            let cmx = cm_new.extract_p();

            // Constrain cmx to equal the public input.
            layouter.constrain_instance(cmx.inner().cell(), config.primary, CMX_NEW)?;

            // Extract x-coordinates of the output address for condition 7.
            (
                g_d_new.extract_p().inner().clone(),
                pk_d_new.extract_p().inner().clone(),
            )
        };

        // ---------------------------------------------------------------
        // Condition 7: Gov commitment integrity.
        // gov_comm_core = Poseidon(DOMAIN_VAN, g_d_new_x, pk_d_new_x, v_total,
        //                          vote_round_id, MAX_PROPOSAL_AUTHORITY)
        // gov_comm = Poseidon(gov_comm_core, gov_comm_rand)
        // ---------------------------------------------------------------

        // Gov commitment integrity (condition 7).
        //
        // gov_comm_core = Poseidon(DOMAIN_VAN, g_d_new_x, pk_d_new_x, v_total,
        //                          vote_round_id, MAX_PROPOSAL_AUTHORITY)
        // gov_comm = Poseidon(gov_comm_core, gov_comm_rand)
        //
        // Proves that the governance commitment (public input) is correctly derived
        // from the domain tag, the output note's voting hotkey address, the total
        // voting weight, the vote round identifier, a blinding factor, and the
        // proposal authority bitmask (MAX_PROPOSAL_AUTHORITY = 65535 for full
        // authority).
        //
        // Uses two Poseidon invocations over even arities (6 then 2).
        let v_total = {
            let gov_comm_rand = assign_free_advice(
                layouter.namespace(|| "witness gov_comm_rand"),
                config.advices[0],
                self.gov_comm_rand,
            )?;

            // v_total = v_1 + v_2 + v_3 + v_4  (three AddChip additions)
            let add_chip = config.add_chip();
            let sum_12 =
                add_chip.add(layouter.namespace(|| "v_1 + v_2"), &v_cells[0], &v_cells[1])?;
            let sum_123 = add_chip.add(
                layouter.namespace(|| "(v_1 + v_2) + v_3"),
                &sum_12,
                &v_cells[2],
            )?;
            let v_total = add_chip.add(
                layouter.namespace(|| "(v_1 + v_2 + v_3) + v_4"),
                &sum_123,
                &v_cells[3],
            )?;

            // DOMAIN_VAN — constant-constrained domain tag for Vote Authority
            // Notes.  Provides domain separation from Vote Commitments in the
            // shared vote commitment tree.
            let domain_van = layouter.assign_region(
                || "DOMAIN_VAN constant",
                |mut region| {
                    region.assign_advice_from_constant(
                        || "domain_van",
                        config.advices[0],
                        0,
                        pallas::Base::from(DOMAIN_VAN),
                    )
                },
            )?;

            // MAX_PROPOSAL_AUTHORITY — constant-constrained so the value is
            // baked into the verification key and cannot be altered by a
            // malicious prover.
            let max_proposal_authority = layouter.assign_region(
                || "MAX_PROPOSAL_AUTHORITY constant",
                |mut region| {
                    region.assign_advice_from_constant(
                        || "max_proposal_authority",
                        config.advices[0],
                        0,
                        pallas::Base::from(MAX_PROPOSAL_AUTHORITY),
                    )
                },
            )?;

            // Poseidon core over the 6 structural fields.
            let gov_comm_core = {
                let core_message = [
                    domain_van,
                    g_d_new_x,
                    pk_d_new_x,
                    v_total.clone(),
                    vote_round_id_cell,
                    max_proposal_authority,
                ];
                let poseidon_hasher = PoseidonHash::<
                    pallas::Base,
                    _,
                    poseidon::P128Pow5T3,
                    ConstantLength<6>,
                    3,
                    2,
                >::init(
                    config.poseidon_chip(),
                    layouter.namespace(|| "gov_comm_core Poseidon init"),
                )?;
                poseidon_hasher.hash(
                    layouter.namespace(|| {
                        "Poseidon(DOMAIN_VAN, g_d_new_x, pk_d_new_x, v_total, round, authority)"
                    }),
                    core_message,
                )?
            };

            // Finalize with blinding randomness.
            let derived_gov_comm = {
                let poseidon_hasher = PoseidonHash::<
                    pallas::Base,
                    _,
                    poseidon::P128Pow5T3,
                    ConstantLength<2>,
                    3,
                    2,
                >::init(
                    config.poseidon_chip(),
                    layouter.namespace(|| "gov_comm finalize Poseidon init"),
                )?;
                poseidon_hasher.hash(
                    layouter.namespace(|| "Poseidon(gov_comm_core, gov_comm_rand)"),
                    [gov_comm_core, gov_comm_rand],
                )?
            };

            // Constrain: derived_gov_comm == gov_comm (from condition 3).
            layouter.assign_region(
                || "gov_comm integrity",
                |mut region| region.constrain_equal(derived_gov_comm.cell(), gov_comm_cell.cell()),
            )?;

            v_total
        };

        // ---------------------------------------------------------------
        // Condition 8: Minimum voting weight.
        // v_total >= 12,500,000 zatoshi (0.125 ZEC)
        // ---------------------------------------------------------------

        // Minimum voting weight (condition 8).
        //
        // v_total >= 12,500,000 zatoshi (0.125 ZEC)
        //
        // Proved by witnessing diff = v_total - MIN_WEIGHT, constraining
        // diff + MIN_WEIGHT == v_total, and range-checking diff to [0, 2^70).
        // If v_total < MIN_WEIGHT, diff wraps to ~2^254, failing the range check.
        {
            const MIN_WEIGHT: u64 = 12_500_000;

            // Witness diff = v_total - MIN_WEIGHT.
            let diff = v_total.value().map(|v| *v - pallas::Base::from(MIN_WEIGHT));
            let diff = assign_free_advice(
                layouter.namespace(|| "witness diff = v_total - MIN_WEIGHT"),
                config.advices[0],
                diff,
            )?;

            // Assign MIN_WEIGHT as a constant-constrained advice cell.
            // This binds the advice cell to the fixed column via enable_constant,
            // so the value is baked into the verification key and cannot be
            // altered by a malicious prover.
            let min_weight = layouter.assign_region(
                || "MIN_WEIGHT constant",
                |mut region| {
                    region.assign_advice_from_constant(
                        || "min_weight",
                        config.advices[0],
                        0,
                        pallas::Base::from(MIN_WEIGHT),
                    )
                },
            )?;

            // Constrain: diff + MIN_WEIGHT == v_total.
            let recomputed = config.add_chip().add(
                layouter.namespace(|| "diff + MIN_WEIGHT"),
                &diff,
                &min_weight,
            )?;
            layouter.assign_region(
                || "v_total = diff + MIN_WEIGHT",
                |mut region| region.constrain_equal(recomputed.cell(), v_total.cell()),
            )?;

            // Range-check diff to [0, 2^70) — ensures diff is non-negative.
            // 7 words * 10 bits/word = 70 bits >= 64 bits (sufficient for u64 sums).
            // If v_total < MIN_WEIGHT, diff wraps to ~2^254, failing this check.
            // Why 70 bits and not 64? Each v_i is a u64,
            // so v_total = v_1 + v_2 + v_3 + v_4 can be at most 4 * (2^64 - 1),
            // which needs ~66 bits. After subtracting MIN_WEIGHT,
            // the result still needs up to ~66 bits.
            // 70 bits (the next multiple of the 10-bit word size) provides sufficient headroom.
            config.range_check_config().copy_check(
                layouter.namespace(|| "diff < 2^70"),
                diff,
                7,    // num_words: 7 * 10 = 70 bits
                true, // strict: running sum terminates at 0
            )?;
        }
        Ok(())
    }
}

// ================================================================
// Per-note slot synthesis (conditions 9–15).
// ================================================================

/// Synthesize conditions 9–15 for a single note slot.
///
/// Returns `(cmx_cell, v_cell, gov_null_cell)` — the extracted commitment,
/// value, and governance nullifier for use in the rho binding (condition 3),
/// gov commitment (condition 7), and gov nullifier (public input).
#[allow(clippy::too_many_arguments, non_snake_case)]
fn synthesize_note_slot(
    config: &Config,
    layouter: &mut impl Layouter<pallas::Base>,
    ecc_chip: EccChip<OrchardFixedBases>,
    ivk_cell: &AssignedCell<pallas::Base, pallas::Base>,
    nk_cell: &AssignedCell<pallas::Base, pallas::Base>,
    vote_round_id_cell: &AssignedCell<pallas::Base, pallas::Base>,
    nc_root_cell: &AssignedCell<pallas::Base, pallas::Base>,
    nf_imt_root_cell: &AssignedCell<pallas::Base, pallas::Base>,
    note: &NoteSlotWitness,
    slot: usize,
    gov_null_offset: usize,
) -> Result<
    (
        AssignedCell<pallas::Base, pallas::Base>,
        AssignedCell<pallas::Base, pallas::Base>,
        AssignedCell<pallas::Base, pallas::Base>,
    ),
    plonk::Error,
> {
    let s = slot; // shorthand for format strings

    // ---------------------------------------------------------------
    // Condition 9: Note commitment integrity.
    // ---------------------------------------------------------------

    // Proves the prover knows the note's plaintext (address, value, rho, psi)
    // and that it hashes to the claimed commitment. This is the foundation —
    // all other per-note conditions build on these witnessed values.

    // Witness the note's address components as curve points.
    let g_d = NonIdentityPoint::new(
        ecc_chip.clone(),
        layouter.namespace(|| format!("note {s} witness g_d")),
        note.g_d.as_ref().map(|gd| gd.to_affine()),
    )?;

    let pk_d = NonIdentityPoint::new(
        ecc_chip.clone(),
        layouter.namespace(|| format!("note {s} witness pk_d")),
        note.pk_d.as_ref().map(|pk| pk.to_affine()),
    )?;

    // Witness the note's value, rho, and psi as field elements.
    let v = assign_free_advice(
        layouter.namespace(|| format!("note {s} witness v")),
        config.advices[0],
        note.v,
    )?;

    let rho = assign_free_advice(
        layouter.namespace(|| format!("note {s} witness rho")),
        config.advices[0],
        note.rho,
    )?;

    let psi = assign_free_advice(
        layouter.namespace(|| format!("note {s} witness psi")),
        config.advices[0],
        note.psi,
    )?;

    // Witness rcm (commitment randomness) as a fixed-base scalar for ECC.
    let rcm = ScalarFixed::new(
        ecc_chip.clone(),
        layouter.namespace(|| format!("note {s} rcm")),
        note.rcm.as_ref().map(|rcm| rcm.inner()),
    )?;

    // Witness the claimed commitment as a curve point.
    let cm = Point::new(
        ecc_chip.clone(),
        layouter.namespace(|| format!("note {s} witness cm")),
        note.cm.as_ref().map(|cm| cm.inner().to_affine()),
    )?;

    // Recompute NoteCommit from the plaintext and constrain it equals the
    // witnessed cm. If any input (g_d, pk_d, v, rho, psi, rcm) is wrong,
    // the recomputed commitment won't match and the proof fails.
    let derived_cm = note_commit(
        layouter.namespace(|| format!("note {s} NoteCommit")),
        config.sinsemilla_chip_1(),
        config.ecc_chip(),
        config.note_commit_chip_signed(),
        g_d.inner(),
        pk_d.inner(),
        v.clone(),
        rho.clone(),
        psi.clone(),
        rcm,
    )?;

    derived_cm.constrain_equal(layouter.namespace(|| format!("note {s} cm integrity")), &cm)?;

    // cmx = ExtractP(cm) — returned to caller.
    let cmx_cell = cm.extract_p().inner().clone();

    // Witness v as pallas::Base for use in the gov commitment sum (condition 7).
    // Constrain it equal to the NoteValue cell used in note_commit.
    let v_base = assign_free_advice(
        layouter.namespace(|| format!("note {s} witness v_base")),
        config.advices[0],
        note.v.map(|val| pallas::Base::from(val.inner())),
    )?;
    layouter.assign_region(
        || format!("note {s} v = v_base"),
        |mut region| region.constrain_equal(v.cell(), v_base.cell()),
    )?;

    // ---------------------------------------------------------------
    // Condition 11: Diversified address integrity.
    // pk_d = [ivk] * g_d
    // ---------------------------------------------------------------

    // Proves this note belongs to the prover's key. ivk (incoming viewing key)
    // was derived from ak and nk in condition 5. If pk_d != [ivk] * g_d, the
    // note was addressed to someone else and the prover can't claim it.

    // Convert ivk from a base field element to a scalar for ECC multiplication.
    let ivk_scalar = ScalarVar::from_base(
        ecc_chip.clone(),
        layouter.namespace(|| format!("note {s} ivk to scalar")),
        ivk_cell,
    )?;

    // Compute [ivk] * g_d and check it matches the witnessed pk_d.
    let (derived_pk_d, _ivk) = g_d.mul(
        layouter.namespace(|| format!("note {s} [ivk] g_d")),
        ivk_scalar,
    )?;

    // Constrain: derived_pk_d == pk_d.
    derived_pk_d.constrain_equal(
        layouter.namespace(|| format!("note {s} pk_d equality")),
        &pk_d,
    )?;

    // ---------------------------------------------------------------
    // Condition 12: Private nullifier derivation.
    // real_nf = DeriveNullifier_nk(rho, psi, cm)
    // ---------------------------------------------------------------

    // Derives the note's real mainchain nullifier in-circuit. This is NOT
    // published — it stays private. It's used for two things:
    //   1. IMT non-membership (cond 13): proves the note is unspent
    //   2. Gov nullifier derivation (cond 14): hashed into the public gov_null

    let real_nf = derive_nullifier(
        layouter.namespace(|| format!("note {s} real_nf = DeriveNullifier")),
        config.poseidon_chip(),
        config.add_chip(),
        ecc_chip.clone(),
        rho.clone(),
        &psi,
        &cm,
        nk_cell.clone(),
    )?;

    // ---------------------------------------------------------------
    // Condition 14: Governance nullifier integrity.
    // gov_null = Poseidon(nk, Poseidon(domain_tag, Poseidon(vote_round_id, real_nf)))
    // ---------------------------------------------------------------

    // Derives a governance-domain nullifier published on the vote chain to prevent
    // double-delegation. The three-layer Poseidon hash ensures:
    //   - Scoped to this voting round (vote_round_id)
    //   - Domain-separated from other nullifier uses ("governance authorization" tag)
    //   - Keyed by nk, so it can't be linked to real_nf even when real_nf is later
    //     revealed on mainchain
    //
    // The result is constrained to the public instance so the vote chain can
    // track which notes have already been delegated this round.

    // Domain tag = "governance authorization" as a field element. Assigned as a
    // constant so it's baked into the verification key.
    let domain_tag = layouter.assign_region(
        || format!("note {s} gov_auth domain tag"),
        |mut region| {
            region.assign_advice_from_constant(
                || "domain_tag",
                config.advices[0],
                0,
                crate::delegation::imt::gov_auth_domain_tag(),
            )
        },
    )?;

    // Step 1: Poseidon(vote_round_id, real_nf) — scope to this round + note.
    let intermediate = {
        let poseidon_hasher =
            PoseidonHash::<pallas::Base, _, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init(
                config.poseidon_chip(),
                layouter.namespace(|| format!("note {s} gov_null step 1 init")),
            )?;
        poseidon_hasher.hash(
            layouter.namespace(|| format!("note {s} Poseidon(vote_round_id, real_nf)")),
            [vote_round_id_cell.clone(), real_nf.inner().clone()],
        )?
    };

    // Step 2: Poseidon(domain_tag, intermediate) — domain separation.
    let tagged = {
        let poseidon_hasher =
            PoseidonHash::<pallas::Base, _, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init(
                config.poseidon_chip(),
                layouter.namespace(|| format!("note {s} gov_null step 2 init")),
            )?;
        poseidon_hasher.hash(
            layouter.namespace(|| format!("note {s} Poseidon(domain_tag, intermediate)")),
            [domain_tag, intermediate],
        )?
    };

    // Step 3: Poseidon(nk, tagged) — key the result so it can't be reversed.
    let gov_null = {
        let poseidon_hasher =
            PoseidonHash::<pallas::Base, _, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init(
                config.poseidon_chip(),
                layouter.namespace(|| format!("note {s} gov_null step 3 init")),
            )?;
        poseidon_hasher.hash(
            layouter.namespace(|| format!("note {s} Poseidon(nk, tagged)")),
            [nk_cell.clone(), tagged],
        )?
    };

    // Constrain gov_null to the public instance column so the vote chain sees it.
    let gov_null_cell = gov_null.clone();
    layouter.constrain_instance(gov_null.cell(), config.primary, gov_null_offset)?;

    // ---------------------------------------------------------------
    // Condition 10: Merkle path validity.
    // ---------------------------------------------------------------

    // Proves the note's commitment exists in the mainchain note commitment tree.
    // Computes the Sinsemilla-based Merkle root from the leaf (cmx = ExtractP(cm))
    // and the 32-level authentication path. The q_per_note gate then checks that
    // the computed root equals the public nc_root (for real notes only).

    let root = {
        // Convert the witnessed Merkle path siblings to raw field elements.
        let path = note
            .path
            .map(|typed_path| typed_path.map(|node| node.inner()));
        let merkle_inputs = GadgetMerklePath::construct(
            [config.merkle_chip_1(), config.merkle_chip_2()],
            OrchardHashDomains::MerkleCrh,
            note.pos,
            path,
        );
        // The leaf is the x-coordinate of the note commitment.
        let leaf = cm.extract_p().inner().clone();
        merkle_inputs
            .calculate_root(layouter.namespace(|| format!("note {s} Merkle path")), leaf)?
    };

    // ---------------------------------------------------------------
    // Condition 13: IMT non-membership.
    // ---------------------------------------------------------------

    // Proves the note's real nullifier (from cond 12) has NOT been spent on
    // mainchain. Uses a (low, high) leaf Indexed Merkle Tree where each leaf
    // stores an explicit interval [low, high].
    //
    // The proof has three parts:
    //   1. Leaf hash: Poseidon(low, high)
    //   2. Merkle path: 29 levels from the leaf hash to the root.
    //   3. Interval check: low <= real_nf <= high, proved by
    //      range-checking x and x_shifted to [0, 2^250).
    //
    // Witness low and high explicitly.
    let imt_low = assign_free_advice(
        layouter.namespace(|| format!("note {s} imt_low")),
        config.advices[0],
        note.imt_low,
    )?;

    let imt_high = assign_free_advice(
        layouter.namespace(|| format!("note {s} imt_high")),
        config.advices[0],
        note.imt_high,
    )?;

    // Compute leaf hash: Poseidon(low, high).
    let leaf_hash = {
        let poseidon_hasher = PoseidonHash::<
            pallas::Base, _, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2
        >::init(config.poseidon_chip(), layouter.namespace(|| format!("note {s} imt leaf hash init")))?;
        poseidon_hasher.hash(
            layouter.namespace(|| format!("note {s} Poseidon(low, high)")),
            [imt_low.clone(), imt_high.clone()],
        )?
    };

    // Poseidon Merkle path from leaf_hash, 29 levels.
    // At each level, q_imt_swap orders (current, sibling) by position bit,
    // then Poseidon(left, right) computes the parent.
    let mut current = leaf_hash;

    for i in 0..IMT_DEPTH {
        let pos_bit = assign_free_advice(
            layouter.namespace(|| format!("note {s} imt pos_bit {i}")),
            config.advices[0],
            note.imt_leaf_pos
                .map(|p| pallas::Base::from(((p >> i) & 1) as u64)),
        )?;

        let sibling = assign_free_advice(
            layouter.namespace(|| format!("note {s} imt sibling {i}")),
            config.advices[0],
            note.imt_path.map(|path| path[i]),
        )?;

        let (left, right) = layouter.assign_region(
            || format!("note {s} imt swap level {i}"),
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

        let parent = {
            let poseidon_hasher = PoseidonHash::<
                pallas::Base, _, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2
            >::init(config.poseidon_chip(), layouter.namespace(|| format!("note {s} imt path hash init level {i}")))?;
            poseidon_hasher.hash(
                layouter.namespace(|| format!("note {s} Poseidon(left, right) level {i}")),
                [left, right],
            )?
        };
        current = parent;
    }
    // The computed root is checked against the public nf_imt_root in the
    // q_per_note gate below.
    let imt_root = current;

    // Interval check: prove low <= real_nf <= high.
    // The q_interval gate constrains y, x, x_shifted from the witnessed values.
    // Range checks on x and x_shifted enforce the interval inclusion.
    let (x, x_shifted) = layouter.assign_region(
        || format!("note {s} interval check"),
        |mut region| {
            config.q_interval.enable(&mut region, 0)?;

            // Row 0: low, high, real_nf
            imt_low.copy_advice(|| "low", &mut region, config.advices[0], 0)?;
            imt_high.copy_advice(|| "high", &mut region, config.advices[1], 0)?;
            real_nf
                .inner()
                .copy_advice(|| "real_nf", &mut region, config.advices[2], 0)?;

            // Row 1: witness the derived values constrained by q_interval.
            // y = interval width, x = lower bound offset, x_shifted = upper bound check.
            // x and x_shifted are range-checked to [0, 2^250) after this region.
            let y = region.assign_advice(
                || "y = high - low",
                config.advices[0],
                1,
                || {
                    imt_high
                        .value()
                        .copied()
                        .zip(imt_low.value().copied())
                        .map(|(e, s)| e - s)
                },
            )?;

            let x = region.assign_advice(
                || "x = real_nf - low",
                config.advices[1],
                1,
                || {
                    real_nf
                        .inner()
                        .value()
                        .copied()
                        .zip(imt_low.value().copied())
                        .map(|(nf, s)| nf - s)
                },
            )?;

            let two_250 = pallas::Base::from(2u64).pow([250, 0, 0, 0]);
            let x_shifted = region.assign_advice(
                || "x_shifted = 2^250 - y + x - 1",
                config.advices[2],
                1,
                || {
                    y.value()
                        .copied()
                        .zip(x.value().copied())
                        .map(|(y_val, x_val)| two_250 - y_val + x_val - pallas::Base::one())
                },
            )?;

            Ok((x, x_shifted))
        },
    )?;

    // Range checks enforce the interval inclusion.
    // x in [0, 2^250) proves low <= real_nf.
    // x_shifted in [0, 2^250) proves real_nf <= high.
    // 25 limbs × 10 bits = 250-bit range.
    config.ecc_config.lookup_config.copy_check(
        layouter.namespace(|| format!("note {s} x < 2^250")),
        x,
        25,
        true,
    )?;

    config.ecc_config.lookup_config.copy_check(
        layouter.namespace(|| format!("note {s} x_shifted < 2^250")),
        x_shifted,
        25,
        true,
    )?;

    // ---------------------------------------------------------------
    // Custom gate region: conditions 10 + 13 + 15.
    // ---------------------------------------------------------------

    // Activates the q_per_note gate, which ties together results from the
    // preceding conditions into a single row of checks:
    //   - Cond 15: padded notes (is_note_real=0) must have v=0
    //   - Cond 10: real notes' Merkle root must match the public nc_root
    //   - Cond 13: IMT root must match public nf_imt_root
    //
    // All six values are copied from earlier regions via copy constraints,
    // so the gate operates on the same cells that the upstream gadgets produced.

    let is_note_real = assign_free_advice(
        layouter.namespace(|| format!("note {s} witness is_note_real")),
        config.advices[0],
        note.is_note_real.map(|b| pallas::Base::from(b as u64)),
    )?;

    layouter.assign_region(
        || format!("note {s} per-note checks"),
        |mut region| {
            config.q_per_note.enable(&mut region, 0)?;

            is_note_real.copy_advice(|| "is_note_real", &mut region, config.advices[0], 0)?;
            v.copy_advice(|| "v", &mut region, config.advices[1], 0)?;
            root.copy_advice(|| "calculated root", &mut region, config.advices[2], 0)?;
            nc_root_cell.copy_advice(|| "nc_root (anchor)", &mut region, config.advices[3], 0)?;
            imt_root.copy_advice(|| "imt_root", &mut region, config.advices[4], 0)?;
            nf_imt_root_cell.copy_advice(|| "nf_imt_root", &mut region, config.advices[5], 0)?;

            Ok(())
        },
    )?;

    // Return the three values needed by global conditions:
    //   cmx_cell   → condition 3 (rho binding hash)
    //   v_base     → conditions 7 & 8 (gov commitment, min weight)
    //   gov_null   → exposed as public input
    Ok((cmx_cell, v_base, gov_null_cell))
}

// ================================================================
// Instance
// ================================================================

/// Public inputs to the delegation circuit (12 field elements).
///
/// These are the values posted to the vote chain (§2.4) that both the prover
/// and verifier agree on. The verifier checks the proof against these values
/// without seeing any private witnesses.
#[derive(Clone, Debug)]
pub struct Instance {
    /// The derived nullifier of the keystone note.
    pub nf_signed: Nullifier,
    /// The randomized spend validating key.
    pub rk: VerificationKey<SpendAuth>,
    /// The extracted commitment of the output note.
    pub cmx_new: pallas::Base,
    /// The governance commitment hash.
    pub gov_comm: pallas::Base,
    /// The voting round identifier.
    pub vote_round_id: pallas::Base,
    /// The note commitment tree root (shared anchor).
    pub nc_root: pallas::Base,
    /// The nullifier IMT root.
    pub nf_imt_root: pallas::Base,
    /// Per-note governance nullifiers (4 slots).
    pub gov_null: [pallas::Base; 4],
}

impl Instance {
    /// Constructs an [`Instance`] from its constituent parts.
    pub fn from_parts(
        nf_signed: Nullifier,
        rk: VerificationKey<SpendAuth>,
        cmx_new: pallas::Base,
        gov_comm: pallas::Base,
        vote_round_id: pallas::Base,
        nc_root: pallas::Base,
        nf_imt_root: pallas::Base,
        gov_null: [pallas::Base; 4],
    ) -> Self {
        Instance {
            nf_signed,
            rk,
            cmx_new,
            gov_comm,
            vote_round_id,
            nc_root,
            nf_imt_root,
            gov_null,
        }
    }

    /// Serializes the public inputs into the flat field-element vector that
    /// halo2's `MockProver::run`, `create_proof`, and `verify_proof` expect.
    ///
    /// The order must match the instance column offsets defined at the top of
    /// this file (`NF_SIGNED`, `RK_X`, `RK_Y`, `CMX_NEW`, etc.).
    pub fn to_halo2_instance(&self) -> Vec<vesta::Scalar> {
        // rk is stored as compressed bytes but the circuit constrains it as
        // two field elements (x, y coordinates of the curve point).
        // Safety: VerificationKey<SpendAuth> guarantees a valid, non-identity
        // curve point, so both conversions are infallible.
        let rk = pallas::Point::from_bytes(&self.rk.clone().into())
            .expect("rk is a valid curve point (guaranteed by VerificationKey)")
            .to_affine()
            .coordinates()
            .expect("rk is not the identity point (guaranteed by VerificationKey)");

        vec![
            self.nf_signed.0,
            *rk.x(),
            *rk.y(),
            self.cmx_new,
            self.gov_comm,
            self.vote_round_id,
            self.nc_root,
            self.nf_imt_root,
            self.gov_null[0],
            self.gov_null[1],
            self.gov_null[2],
            self.gov_null[3],
        ]
    }
}

// ================================================================
// Test-only
// ================================================================

#[cfg(test)]
mod tests {
    use alloc::string::{String, ToString};
    use super::*;
    use crate::{
        delegation::imt::{gov_null_hash, ImtProofData, ImtProvider, SpacedLeafImtProvider},
        keys::{FullViewingKey, Scope, SpendValidatingKey, SpendingKey},
        note::{commitment::ExtractedNoteCommitment, Note, Rho},
    };
    use ff::Field;
    use halo2_proofs::dev::MockProver;
    use incrementalmerkletree::{Hashable, Level};
    use pasta_curves::{arithmetic::CurveAffine, pallas};
    use rand::rngs::OsRng;

    /// Size of the delegation circuit (2^K rows).
    ///
    /// K=14 (16,384 rows) fits all conditions including 4 per-note slots.
    /// (Poseidon IMT hashing requires more rows than the former Poseidon2 chip.)
    const K: u32 = 14;

    /// Helper: build a NoteSlotWitness for a note with a Merkle path and IMT proof.
    fn make_note_slot(
        note: &Note,
        auth_path: &[MerkleHashOrchard; MERKLE_DEPTH_ORCHARD],
        pos: u32,
        imt: &ImtProofData,
        is_real: bool,
    ) -> NoteSlotWitness {
        let rho = note.rho();
        let psi = note.rseed().psi(&rho);
        let rcm = note.rseed().rcm(&rho);
        let cm = note.commitment();
        let recipient = note.recipient();

        NoteSlotWitness {
            g_d: Value::known(recipient.g_d()),
            pk_d: Value::known(
                NonIdentityPallasPoint::from_bytes(&recipient.pk_d().to_bytes()).unwrap(),
            ),
            v: Value::known(note.value()),
            rho: Value::known(rho.into_inner()),
            psi: Value::known(psi),
            rcm: Value::known(rcm),
            cm: Value::known(cm),
            path: Value::known(*auth_path),
            pos: Value::known(pos),
            is_note_real: Value::known(is_real),
            imt_low: Value::known(imt.low),
            imt_high: Value::known(imt.high),
            imt_leaf_pos: Value::known(imt.leaf_pos),
            imt_path: Value::known(imt.path),
        }
    }

    /// Return value from `make_test_data` bundling all test artefacts.
    struct TestData {
        circuit: Circuit,
        instance: Instance,
    }

    /// Build a valid merged circuit with 1 real note + 3 padded notes.
    fn make_test_data() -> TestData {
        let mut rng = OsRng;

        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let output_recipient = fvk.address_at(1u32, Scope::External);

        // Key material.
        let nk_val = fvk.nk().inner();
        let ak: SpendValidatingKey = fvk.clone().into();

        let vote_round_id = pallas::Base::random(&mut rng);
        let gov_comm_rand = pallas::Base::random(&mut rng);

        // Shared IMT provider (consistent root for all notes).
        let imt_provider = SpacedLeafImtProvider::new();
        let nf_imt_root = imt_provider.root();

        // Real note (slot 0) with value = 13,000,000.
        let recipient = fvk.address_at(0u32, Scope::External);
        let note_value = NoteValue::from_raw(13_000_000);
        let (_, _, dummy_parent) = Note::dummy(&mut rng, None);
        let real_note = Note::new(
            recipient,
            note_value,
            Rho::from_nf_old(dummy_parent.nullifier(&fvk)),
            &mut rng,
        );

        // Build Merkle tree with real note at position 0.
        let cmx_real_e = ExtractedNoteCommitment::from(real_note.commitment());
        let cmx_real = cmx_real_e.inner();
        let empty_leaf = MerkleHashOrchard::empty_leaf();
        let leaves = [
            MerkleHashOrchard::from_cmx(&cmx_real_e),
            empty_leaf,
            empty_leaf,
            empty_leaf,
        ];
        let l1_0 = MerkleHashOrchard::combine(Level::from(0), &leaves[0], &leaves[1]);
        let l1_1 = MerkleHashOrchard::combine(Level::from(0), &leaves[2], &leaves[3]);
        let l2_0 = MerkleHashOrchard::combine(Level::from(1), &l1_0, &l1_1);

        let mut current = l2_0;
        for level in 2..MERKLE_DEPTH_ORCHARD {
            let sibling = MerkleHashOrchard::empty_root(Level::from(level as u8));
            current = MerkleHashOrchard::combine(Level::from(level as u8), &current, &sibling);
        }
        let nc_root = current.inner();

        let mut auth_path_0 = [MerkleHashOrchard::empty_leaf(); MERKLE_DEPTH_ORCHARD];
        auth_path_0[0] = leaves[1];
        auth_path_0[1] = l1_1;
        for level in 2..MERKLE_DEPTH_ORCHARD {
            auth_path_0[level] = MerkleHashOrchard::empty_root(Level::from(level as u8));
        }
        // IMT proof for real note (from shared provider).
        let real_nf = real_note.nullifier(&fvk);
        let imt_0 = imt_provider.non_membership_proof(real_nf.0);
        let gov_null_0 = gov_null_hash(nk_val, vote_round_id, real_nf.0);

        let slot_0 = make_note_slot(&real_note, &auth_path_0, 0u32, &imt_0, true);

        // Padded notes (slots 1-3): zero-value notes with addresses from the real ivk.
        let mut note_slots = vec![slot_0];
        let mut cmx_values = vec![cmx_real];
        let mut gov_nulls = vec![gov_null_0];

        let dummy_auth_path = [MerkleHashOrchard::empty_leaf(); MERKLE_DEPTH_ORCHARD];

        for i in 1..4u32 {
            // Use fvk.address_at() so pk_d = [ivk] * g_d with the REAL ivk.
            let pad_addr = fvk.address_at(100 + i, Scope::External);
            let (_, _, dummy) = Note::dummy(&mut rng, None);
            let pad_note = Note::new(
                pad_addr,
                NoteValue::zero(),
                Rho::from_nf_old(dummy.nullifier(&fvk)),
                &mut rng,
            );

            let pad_cmx = ExtractedNoteCommitment::from(pad_note.commitment()).inner();
            let pad_nf = pad_note.nullifier(&fvk);
            let pad_imt = imt_provider.non_membership_proof(pad_nf.0);
            let pad_gov_null = gov_null_hash(nk_val, vote_round_id, pad_nf.0);

            note_slots.push(make_note_slot(
                &pad_note,
                &dummy_auth_path,
                0u32,
                &pad_imt,
                false,
            ));
            cmx_values.push(pad_cmx);
            gov_nulls.push(pad_gov_null);
        }

        let notes: [NoteSlotWitness; 4] = note_slots.try_into().unwrap();

        // Values: real note = 13M, padded = 0.
        let v_total = pallas::Base::from(13_000_000u64);

        // Compute gov_comm.
        let g_d_new_x = *output_recipient
            .g_d()
            .to_affine()
            .coordinates()
            .unwrap()
            .x();
        let pk_d_new_x = *output_recipient
            .pk_d()
            .inner()
            .to_affine()
            .coordinates()
            .unwrap()
            .x();
        let gov_comm =
            gov_commitment_hash(g_d_new_x, pk_d_new_x, v_total, vote_round_id, gov_comm_rand);

        // Compute rho.
        let rho = rho_binding_hash(
            cmx_values[0],
            cmx_values[1],
            cmx_values[2],
            cmx_values[3],
            gov_comm,
            vote_round_id,
        );

        // Create signed note with this rho.
        let sender_address = fvk.address_at(0u32, Scope::External);
        let signed_note = Note::new(
            sender_address,
            NoteValue::zero(),
            Rho::from_nf_old(Nullifier(rho)),
            &mut rng,
        );
        let nf_signed = signed_note.nullifier(&fvk);

        // Create output note with rho = nf_signed.
        let output_note = Note::new(
            output_recipient,
            NoteValue::zero(),
            Rho::from_nf_old(nf_signed),
            &mut rng,
        );
        let cmx_new = ExtractedNoteCommitment::from(output_note.commitment()).inner();

        let alpha = pallas::Scalar::random(&mut rng);
        let rk = ak.randomize(&alpha);

        let circuit = Circuit::from_note_unchecked(&fvk, &signed_note, alpha)
            .with_output_note(&output_note)
            .with_notes(notes)
            .with_gov_comm_rand(gov_comm_rand);

        let instance = Instance::from_parts(
            nf_signed,
            rk,
            cmx_new,
            gov_comm,
            vote_round_id,
            nc_root,
            nf_imt_root,
            [gov_nulls[0], gov_nulls[1], gov_nulls[2], gov_nulls[3]],
        );

        TestData { circuit, instance }
    }

    #[test]
    fn happy_path() {
        let t = make_test_data();
        let pi = t.instance.to_halo2_instance();

        let prover = MockProver::run(K, &t.circuit, vec![pi]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn wrong_nf_fails() {
        let t = make_test_data();
        let mut instance = t.instance.clone();
        instance.nf_signed = Nullifier(pallas::Base::random(&mut OsRng));

        let pi = instance.to_halo2_instance();
        let prover = MockProver::run(K, &t.circuit, vec![pi]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn wrong_rk_fails() {
        let mut rng = OsRng;
        let t = make_test_data();

        let sk2 = SpendingKey::random(&mut rng);
        let fvk2: FullViewingKey = (&sk2).into();
        let ak2: SpendValidatingKey = fvk2.into();
        let wrong_rk = ak2.randomize(&pallas::Scalar::random(&mut rng));

        let mut instance = t.instance.clone();
        instance.rk = wrong_rk;

        let pi = instance.to_halo2_instance();
        let prover = MockProver::run(K, &t.circuit, vec![pi]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn wrong_gov_null_fails() {
        let t = make_test_data();
        let mut instance = t.instance.clone();
        instance.gov_null[0] = pallas::Base::random(&mut OsRng);

        let pi = instance.to_halo2_instance();
        let prover = MockProver::run(K, &t.circuit, vec![pi]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn wrong_nc_root_fails() {
        let t = make_test_data();
        let mut instance = t.instance.clone();
        instance.nc_root = pallas::Base::random(&mut OsRng);

        let pi = instance.to_halo2_instance();
        let prover = MockProver::run(K, &t.circuit, vec![pi]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn wrong_imt_root_fails() {
        let t = make_test_data();
        let mut instance = t.instance.clone();
        instance.nf_imt_root = pallas::Base::random(&mut OsRng);

        let pi = instance.to_halo2_instance();
        let prover = MockProver::run(K, &t.circuit, vec![pi]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn wrong_gov_comm_fails() {
        let t = make_test_data();
        let mut instance = t.instance.clone();
        instance.gov_comm = pallas::Base::random(&mut OsRng);

        let pi = instance.to_halo2_instance();
        let prover = MockProver::run(K, &t.circuit, vec![pi]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn wrong_vote_round_id_fails() {
        let t = make_test_data();
        let mut instance = t.instance.clone();
        instance.vote_round_id = pallas::Base::random(&mut OsRng);

        let pi = instance.to_halo2_instance();
        let prover = MockProver::run(K, &t.circuit, vec![pi]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn instance_to_halo2_roundtrip() {
        let t = make_test_data();
        let pi = t.instance.to_halo2_instance();
        assert_eq!(pi.len(), 12, "Expected exactly 12 public inputs");
        assert_eq!(pi[NF_SIGNED], t.instance.nf_signed.0);
        assert_eq!(pi[CMX_NEW], t.instance.cmx_new);
        assert_eq!(pi[GOV_COMM], t.instance.gov_comm);
        assert_eq!(pi[NC_ROOT], t.instance.nc_root);
        assert_eq!(pi[NF_IMT_ROOT], t.instance.nf_imt_root);
        assert_eq!(pi[GOV_NULL_1], t.instance.gov_null[0]);
    }

    #[test]
    fn default_circuit_shape() {
        let t = make_test_data();
        let empty = plonk::Circuit::without_witnesses(&t.circuit);
        let params = halo2_proofs::poly::commitment::Params::<vesta::Affine>::new(K);
        let vk = halo2_proofs::plonk::keygen_vk(&params, &empty);
        assert!(
            vk.is_ok(),
            "keygen_vk must succeed on without_witnesses circuit"
        );
    }

    // ----------------------------------------------------------------
    // Cost breakdown — per-region row counts via a custom Assignment
    // ----------------------------------------------------------------

    use std::collections::BTreeMap;

    use halo2_proofs::plonk::{Any, Assigned, Assignment, Column, Error, Fixed, FloorPlanner};

    struct RegionInfo {
        name: String,
        min_row: Option<usize>,
        max_row: Option<usize>,
    }

    impl RegionInfo {
        fn track_row(&mut self, row: usize) {
            self.min_row = Some(self.min_row.map_or(row, |m| m.min(row)));
            self.max_row = Some(self.max_row.map_or(row, |m| m.max(row)));
        }

        fn row_count(&self) -> usize {
            match (self.min_row, self.max_row) {
                (Some(lo), Some(hi)) => hi - lo + 1,
                _ => 0,
            }
        }
    }

    struct RegionTracker {
        regions: Vec<RegionInfo>,
        current_region: Option<usize>,
        total_rows: usize,
        namespace_stack: Vec<String>,
    }

    impl RegionTracker {
        fn new() -> Self {
            Self {
                regions: Vec::new(),
                current_region: None,
                total_rows: 0,
                namespace_stack: Vec::new(),
            }
        }

        fn current_prefix(&self) -> String {
            if self.namespace_stack.is_empty() {
                String::new()
            } else {
                format!("{}/", self.namespace_stack.join("/"))
            }
        }
    }

    impl Assignment<pallas::Base> for RegionTracker {
        fn enter_region<NR, N>(&mut self, name_fn: N)
        where
            NR: Into<String>,
            N: FnOnce() -> NR,
        {
            let idx = self.regions.len();
            let raw_name: String = name_fn().into();
            let prefixed = format!("{}{}", self.current_prefix(), raw_name);
            self.regions.push(RegionInfo {
                name: prefixed,
                min_row: None,
                max_row: None,
            });
            self.current_region = Some(idx);
        }

        fn exit_region(&mut self) {
            self.current_region = None;
        }

        fn enable_selector<A, AR>(
            &mut self,
            _: A,
            _selector: &Selector,
            row: usize,
        ) -> Result<(), Error>
        where
            A: FnOnce() -> AR,
            AR: Into<String>,
        {
            if let Some(idx) = self.current_region {
                self.regions[idx].track_row(row);
            }
            if row + 1 > self.total_rows {
                self.total_rows = row + 1;
            }
            Ok(())
        }

        fn query_instance(
            &self,
            _column: Column<InstanceColumn>,
            _row: usize,
        ) -> Result<Value<pallas::Base>, Error> {
            Ok(Value::unknown())
        }

        fn assign_advice<V, VR, A, AR>(
            &mut self,
            _: A,
            _column: Column<Advice>,
            row: usize,
            _to: V,
        ) -> Result<(), Error>
        where
            V: FnOnce() -> Value<VR>,
            VR: Into<Assigned<pallas::Base>>,
            A: FnOnce() -> AR,
            AR: Into<String>,
        {
            if let Some(idx) = self.current_region {
                self.regions[idx].track_row(row);
            }
            if row + 1 > self.total_rows {
                self.total_rows = row + 1;
            }
            Ok(())
        }

        fn assign_fixed<V, VR, A, AR>(
            &mut self,
            _: A,
            _column: Column<Fixed>,
            row: usize,
            _to: V,
        ) -> Result<(), Error>
        where
            V: FnOnce() -> Value<VR>,
            VR: Into<Assigned<pallas::Base>>,
            A: FnOnce() -> AR,
            AR: Into<String>,
        {
            if let Some(idx) = self.current_region {
                self.regions[idx].track_row(row);
            }
            if row + 1 > self.total_rows {
                self.total_rows = row + 1;
            }
            Ok(())
        }

        fn copy(
            &mut self,
            _left_column: Column<Any>,
            _left_row: usize,
            _right_column: Column<Any>,
            _right_row: usize,
        ) -> Result<(), Error> {
            Ok(())
        }

        fn fill_from_row(
            &mut self,
            _column: Column<Fixed>,
            _row: usize,
            _to: Value<Assigned<pallas::Base>>,
        ) -> Result<(), Error> {
            Ok(())
        }

        fn push_namespace<NR, N>(&mut self, name_fn: N)
        where
            NR: Into<String>,
            N: FnOnce() -> NR,
        {
            self.namespace_stack.push(name_fn().into());
        }

        fn pop_namespace(&mut self, _: Option<String>) {
            self.namespace_stack.pop();
        }
    }

    #[test]
    fn cost_breakdown() {
        // 1. Configure constraint system
        let mut cs = plonk::ConstraintSystem::default();
        let config = <Circuit as plonk::Circuit<pallas::Base>>::configure(&mut cs);

        // 2. Run floor planner with our tracker.
        //    Provide a fixed column for constants — the configure call above registered
        //    one via enable_constant, but cs.constants is pub(crate). We create a fresh
        //    fixed column; it won't match the real one but the V1 planner only needs
        //    *some* column to place constants into. Row counts are unaffected.
        let constants_col = cs.fixed_column();
        let circuit = Circuit::default();
        let mut tracker = RegionTracker::new();
        floor_planner::V1::synthesize(&mut tracker, &circuit, config, vec![constants_col])
            .unwrap();

        // 3. Collect and sort regions by row count (descending)
        let mut regions: Vec<_> = tracker
            .regions
            .iter()
            .filter(|r| r.row_count() > 0)
            .collect();
        regions.sort_by(|a, b| b.row_count().cmp(&a.row_count()));

        std::println!(
            "\n=== Delegation Circuit Cost Breakdown (K={}, {} total rows) ===",
            K,
            1u64 << K
        );
        std::println!("Total rows used: {}\n", tracker.total_rows);

        std::println!("Per-region (sorted by cost):");
        for r in &regions {
            std::println!(
                "  {:60} {:>6} rows  (rows {}-{})",
                r.name,
                r.row_count(),
                r.min_row.unwrap(),
                r.max_row.unwrap()
            );
        }

        // 4. Aggregate by top-level condition
        std::println!("\nAggregated by top-level condition:");
        let mut aggregated: BTreeMap<String, (usize, usize)> = BTreeMap::new();
        for r in &tracker.regions {
            if r.row_count() == 0 {
                continue;
            }
            let key = if r.name.starts_with("note ")
                && r.name.as_bytes().get(5).map_or(false, |b| b.is_ascii_digit())
            {
                if let Some(slash) = r.name.find('/') {
                    let rest = &r.name[slash + 1..];
                    let top = rest.split('/').next().unwrap_or(rest);
                    let top = if top.starts_with("MerkleCRH(") {
                        "Merkle path (Sinsemilla)"
                    } else if top.starts_with("Poseidon(left, right) level") {
                        "IMT Poseidon path"
                    } else if top.starts_with("imt swap level") {
                        "IMT swap"
                    } else {
                        top
                    };
                    format!("Per-note: {}", top)
                } else {
                    r.name.clone()
                }
            } else {
                let top = r.name.split('/').next().unwrap_or(&r.name);
                top.to_string()
            };
            let entry = aggregated.entry(key).or_insert((0, 0));
            entry.0 += r.row_count();
            entry.1 += 1;
        }
        let mut agg_sorted: Vec<_> = aggregated.into_iter().collect();
        agg_sorted.sort_by(|a, b| b.1 .0.cmp(&a.1 .0));
        for (name, (total, count)) in &agg_sorted {
            if *count > 1 {
                std::println!(
                    "  {:60} {:>6} rows  ({} x{})",
                    name, total, total / count, count
                );
            } else {
                std::println!("  {:60} {:>6} rows", name, total);
            }
        }
        std::println!();
    }
}
