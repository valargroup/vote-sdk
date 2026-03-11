//! The Delegation circuit implementation.
//!
//! A single circuit proving all 15 conditions of the delegation ZKP:
//!
//! - **Condition 1**: Signed note commitment integrity.
//! - **Condition 2**: Nullifier integrity.
//! - **Condition 3**: Rho binding — keystone rho = Poseidon(cmx_1..5, van_comm, vote_round_id).
//! - **Condition 4**: Spend authority.
//! - **Condition 5**: CommitIvk & diversified address integrity.
//! - **Condition 6**: Output note commitment integrity.
//! - **Condition 7**: Governance commitment integrity (hashes `num_ballots`).
//! - **Condition 8**: Ballot scaling (`num_ballots = floor(v_total / 12,500,000)`).
//! - **Condition 9** (×5): Note commitment integrity.
//! - **Condition 10** (×5): Merkle path validity.
//! - **Condition 11** (×5): Diversified address integrity.
//! - **Condition 12** (×5): Private nullifier derivation.
//! - **Condition 13** (×5): IMT non-membership.
//! - **Condition 14** (×5): Governance nullifier publication.
//! - **Condition 15** (×5): Padded-note zero-value enforcement.

use alloc::vec::Vec;
use group::{Curve, GroupEncoding};
use halo2_proofs::{
    circuit::{floor_planner, AssignedCell, Layouter, Value},
    plonk::{self, Advice, Column, Constraints, Expression, Instance as InstanceColumn, Selector},
    poly::Rotation,
};
use pasta_curves::{arithmetic::CurveAffine, pallas, vesta};

use crate::circuit::address_ownership::prove_address_ownership;
use orchard::{
    circuit::{
        commit_ivk::{CommitIvkChip, CommitIvkConfig},
        gadget::{
            add_chip::{AddChip, AddConfig},
            mul_chip::{MulChip, MulConfig},
            assign_constant, assign_free_advice, derive_nullifier, note_commit, AddInstruction,
            MulInstruction,
        },
        note_commit::{NoteCommitChip, NoteCommitConfig},
    },
    constants::{OrchardCommitDomains, OrchardFixedBases, OrchardHashDomains},
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
            MerklePath as GadgetMerklePath,
        },
    },
    utilities::{
        bool_check,
        lookup_range_check::LookupRangeCheckConfig,
    },
};
use super::imt::IMT_DEPTH;
use super::imt_circuit::{ImtNonMembershipConfig, synthesize_imt_non_membership};
use crate::circuit::van_integrity;
use orchard::constants::MERKLE_DEPTH_ORCHARD;

// ================================================================
// Circuit size
// ================================================================

/// Circuit size (2^K rows).
///
/// K=14 (16,384 rows) fits all 15 conditions including 5 per-note slots
/// with Sinsemilla NoteCommit, Merkle paths, IMT non-membership, and
/// ECC operations.
pub const K: u32 = 14;

// ================================================================
// Public input offsets (13 field elements).
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
const VAN_COMM: usize = 4;
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
const GOV_NULL_5: usize = 12;

/// Gov null offsets indexed by note slot.
const GOV_NULL_OFFSETS: [usize; 5] = [GOV_NULL_1, GOV_NULL_2, GOV_NULL_3, GOV_NULL_4, GOV_NULL_5];

/// Maximum proposal authority — the default for a fresh delegation.
///
/// Represented as a 16-bit bitmask where each bit authorizes voting on the
/// corresponding proposal (proposal ID = bit index from LSB).  Full authority
/// is `2^16 - 1 = 65535`. Only bits 1–15 correspond to usable proposals
/// (proposal IDs are 1-indexed); bit 0 is the circuit's sentinel value,
/// permanently set and never decremented.
///
/// This constant is hashed into `van_comm` (condition 7) as a constant-
/// constrained witness, baked into the verification key so a malicious prover
/// cannot substitute a different authority value.
pub(crate) const MAX_PROPOSAL_AUTHORITY: u64 = 65535; // 2^16 - 1

/// Out-of-circuit rho binding hash used by the builder and tests.
pub(crate) fn rho_binding_hash(
    cmx_1: pallas::Base,
    cmx_2: pallas::Base,
    cmx_3: pallas::Base,
    cmx_4: pallas::Base,
    cmx_5: pallas::Base,
    van_comm: pallas::Base,
    vote_round_id: pallas::Base,
) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<7>, 3, 2>::init()
        .hash([cmx_1, cmx_2, cmx_3, cmx_4, cmx_5, van_comm, vote_round_id])
}

/// Ballot divisor for converting raw zatoshi balance to ballot count.
///
/// `num_ballots = floor(v_total / BALLOT_DIVISOR)`
pub(crate) const BALLOT_DIVISOR: u64 = 12_500_000;

/// Out-of-circuit governance commitment hash used by the builder and tests.
///
/// Delegates to `van_integrity::van_integrity_hash` with
/// `MAX_PROPOSAL_AUTHORITY` as the proposal authority (fresh delegation).
/// The `value` parameter is `num_ballots` (ballot count after floor-division),
/// NOT the raw zatoshi sum.
pub(crate) fn van_commitment_hash(
    g_d_new_x: pallas::Base,
    pk_d_new_x: pallas::Base,
    num_ballots: pallas::Base,
    vote_round_id: pallas::Base,
    van_comm_rand: pallas::Base,
) -> pallas::Base {
    van_integrity::van_integrity_hash(
        g_d_new_x,
        pk_d_new_x,
        num_ballots,
        vote_round_id,
        pallas::Base::from(MAX_PROPOSAL_AUTHORITY),
        van_comm_rand,
    )
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
    // Configuration for the MulChip which constrains a * b = c over field elements.
    // Used in condition 8 (ballot scaling) to compute num_ballots * BALLOT_DIVISOR.
    mul_config: MulConfig,
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
    // Used in condition 8 (ballot scaling) to range-check nb_minus_one (30 bits
    // direct) and remainder (24 bits via shift-by-2^6 into 30-bit check).
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
    // Per-note scope selection gate (condition 11).
    // Muxes between ivk (external) and ivk_internal based on is_internal flag.
    q_scope_select: Selector,
    // IMT non-membership gates (condition 13): conditional swap + interval check.
    imt_config: ImtNonMembershipConfig,
}

impl Config {
    fn add_chip(&self) -> AddChip {
        AddChip::construct(self.add_config.clone())
    }

    fn mul_chip(&self) -> MulChip {
        MulChip::construct(self.mul_config.clone())
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
    pub(crate) imt_width: Value<pallas::Base>,
    pub(crate) imt_leaf_pos: Value<u32>,
    pub(crate) imt_path: Value<[pallas::Base; IMT_DEPTH]>,
    /// Whether this note uses the internal (change) scope.
    /// When true, `ivk_internal` is used for Condition 11 instead of `ivk`.
    pub(crate) is_internal: Value<bool>,
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
    rivk_internal: Value<CommitIvkRandomness>,
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
    notes: [NoteSlotWitness; 5],
    // Gov commitment blinding factor (condition 7).
    van_comm_rand: Value<pallas::Base>,
    // Condition 8 (ballot scaling) witnesses.
    // num_ballots = floor(v_total / BALLOT_DIVISOR), remainder = v_total % BALLOT_DIVISOR.
    num_ballots: Value<pallas::Base>,
    remainder: Value<pallas::Base>,
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
            rivk_internal: Value::known(fvk.rivk(Scope::Internal)),
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

    /// Sets the five per-note slot witnesses (conditions 9–15).
    pub fn with_notes(mut self, notes: [NoteSlotWitness; 5]) -> Self {
        self.notes = notes;
        self
    }

    /// Sets the governance commitment blinding factor (condition 7).
    pub fn with_van_comm_rand(mut self, van_comm_rand: pallas::Base) -> Self {
        self.van_comm_rand = Value::known(van_comm_rand);
        self
    }

    /// Sets the ballot scaling witnesses (condition 8).
    pub fn with_ballot_scaling(mut self, num_ballots: pallas::Base, remainder: pallas::Base) -> Self {
        self.num_ballots = Value::known(num_ballots);
        self.remainder = Value::known(remainder);
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
        // ── Column declarations ──────────────────────────────────────────

        // 10 advice columns — the minimum budget that satisfies every sub-chip
        // simultaneously when their column ranges are overlapped:
        //
        //   EccChip               advices[0..10]  (needs all 10)
        //   Sinsemilla/Merkle #1  advices[0..5] + advices[6] as witness
        //   Sinsemilla/Merkle #2  advices[5..10] + advices[7] as witness
        //   PoseidonChip          advices[5..9]   (partial-sbox + state)
        //   AddChip / MulChip     advices[6..9]
        //   LookupRangeCheck      advices[9]
        //
        // The two Sinsemilla pairs intentionally share advices[5..7]; each pair's
        // gates are gated by their own selectors and are never active on the same
        // rows, so the overlap is safe. Without it we would need 12 columns. EccChip
        // is the widest consumer and already requires 10, so everything else fits
        // within that budget. This matches the upstream Orchard column count.
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

        // Instance column used for public inputs.
        let primary = meta.instance_column();

        // Fixed columns for the Sinsemilla generator lookup table.
        let table_idx = meta.lookup_table_column();
        let lookup = (
            table_idx,
            meta.lookup_table_column(),
            meta.lookup_table_column(),
        );

        // 8 fixed columns shared between ECC (Lagrange interpolation coefficients)
        // and Poseidon (round constants). Different rows hold different data.
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

        // ── Column properties ────────────────────────────────────────────

        // Enable equality constraints (permutation argument) on all advice columns
        // and the instance column, so any cell can be copy-constrained to any other.
        meta.enable_equality(primary);
        for advice in advices.iter() {
            meta.enable_equality(*advice);
        }

        // Use the first Lagrange coefficient column for loading global constants.
        meta.enable_constant(lagrange_coeffs[0]);

        // ── Custom gates ─────────────────────────────────────────────────

        // Per-note custom gates (conditions 10, 13, 15).
        // q_per_note is a selector that activates these constraints only on rows
        // where note data is assigned. Each of the (up to 5) input notes gets one
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

        // Scope selection gate (condition 11): muxes between external and internal ivk.
        // Per-note, selects ivk or ivk_internal based on the is_internal flag, so that
        // internal (change) notes use ivk_internal for the pk_d ownership check.
        let q_scope_select = meta.selector();
        meta.create_gate("scope ivk select", |meta| {
            let q = meta.query_selector(q_scope_select);
            let is_internal = meta.query_advice(advices[0], Rotation::cur());
            let ivk = meta.query_advice(advices[1], Rotation::cur());
            let ivk_internal = meta.query_advice(advices[2], Rotation::cur());
            let selected_ivk = meta.query_advice(advices[3], Rotation::cur());
            // selected_ivk = ivk + is_internal * (ivk_internal - ivk)
            let expected = ivk.clone() + is_internal.clone() * (ivk_internal - ivk);
            Constraints::with_selector(q, [
                ("bool_check is_internal", bool_check(is_internal)),
                ("scope select", selected_ivk - expected),
            ])
        });

        // IMT non-membership gates (condition 13): conditional swap + interval check.
        let imt_config = ImtNonMembershipConfig::configure(meta, &advices);

        // ── Chip configurations ──────────────────────────────────────────

        let add_config = AddChip::configure(meta, advices[7], advices[8], advices[6]);
        let mul_config = MulChip::configure(meta, advices[7], advices[8], advices[6]);

        // Range check configuration using the right-most advice column.
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

        // Two Sinsemilla + Merkle chip pairs. NoteCommit internally needs two
        // Sinsemilla instances (one per hash), so we can't reuse a single config.
        // The Merkle chips alternate between the two at each tree level
        // (even levels use pair 1, odd levels use pair 2) for the same reason.
        //
        // Column layout:
        //   Pair 1: main = advices[0..5], witness = advices[6]
        //   Pair 2: main = advices[5..10], witness = advices[7]
        //
        // The pairs intentionally overlap on advices[5..7] to keep the total
        // column count at 10 (matching upstream Orchard). This is safe because
        // each pair's gates are gated by their own selectors, and the two chips
        // are never assigned to the same rows.
        let configure_sinsemilla_merkle =
            |meta: &mut plonk::ConstraintSystem<pallas::Base>,
             advice_cols: [Column<Advice>; 5],
             witness_col: Column<Advice>,
             lagrange_col: Column<plonk::Fixed>| {
                let sinsemilla =
                    SinsemillaChip::configure(meta, advice_cols, witness_col, lagrange_col, lookup, range_check);
                let merkle = MerkleChip::configure(meta, sinsemilla.clone());
                (sinsemilla, merkle)
            };

        let (sinsemilla_config_1, merkle_config_1) = configure_sinsemilla_merkle(
            meta, advices[..5].try_into().unwrap(), advices[6], lagrange_coeffs[0],
        );
        let (sinsemilla_config_2, merkle_config_2) = configure_sinsemilla_merkle(
            meta, advices[5..].try_into().unwrap(), advices[7], lagrange_coeffs[1],
        );

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
            mul_config,
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
            q_scope_select,
            imt_config,
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

        // Witness pk_d_signed (diversified transmission key). Used by condition 5 (address
        // ownership) and condition 1 (signed note commitment).
        let pk_d_signed = NonIdentityPoint::new(
            ecc_chip.clone(),
            layouter.namespace(|| "witness pk_d_signed"),
            self.pk_d_signed
                .as_ref()
                .map(|pk_d_signed| pk_d_signed.inner().to_affine()),
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

        // Spend authority: proves that the public rk is a valid rerandomization of the prover's ak.
        // The out-of-circuit verifier checks that the keystone signature is valid under rk,
        // so this links the ZKP to the signature without revealing ak.
        //
        // Uses the shared gadget from orchard::shared_primitives – a 1:1 copy of
        // the upstream Orchard spend authority check:
        //   https://github.com/zcash/orchard/blob/main/src/circuit.rs#L542-L558
        // Note: RK_X and RK_Y are public inputs.ß
        orchard::shared_primitives::spend_authority::prove_spend_authority(
            ecc_chip.clone(),
            layouter.namespace(|| "cond4 spend authority"),
            self.alpha,
            &ak_P.clone().into(),
            config.primary,
            RK_X,
            RK_Y,
        )?;

        // ---------------------------------------------------------------
        // Condition 5: CommitIvk → ivk (internal wire, not a public input).
        // pk_d_signed = [ivk] * g_d_signed.
        // ---------------------------------------------------------------

        // Diversified address integrity via shared address_ownership gadget.
        // ivk = ⊥ or pk_d_signed = [ivk] * g_d_signed where ivk = CommitIvk_rivk(ExtractP(ak_P), nk).
        // The ⊥ case is handled internally by CommitDomain::short_commit.
        //
        // Save ak cell before prove_address_ownership consumes it — we need it
        // again below for deriving ivk_internal.
        let ak = ak_P.extract_p().inner().clone();
        let ak_for_internal = ak.clone();
        let rivk = ScalarFixed::new(
            ecc_chip.clone(),
            layouter.namespace(|| "rivk"),
            self.rivk.map(|rivk| rivk.inner()),
        )?;
        let ivk_cell = prove_address_ownership(
            config.sinsemilla_chip_1(),
            ecc_chip.clone(),
            config.commit_ivk_chip(),
            layouter.namespace(|| "cond5"),
            "cond5",
            ak,
            nk.clone(),
            rivk,
            &g_d_signed,
            &pk_d_signed,
        )?;

        // ---------------------------------------------------------------
        // Derive ivk_internal = CommitIvk(ak, nk, rivk_internal).
        // Used by Condition 11 for notes with internal (change) scope.
        // ---------------------------------------------------------------
        let ivk_internal_cell = {
            use orchard::circuit::commit_ivk::gadgets::commit_ivk;
            let rivk_internal = ScalarFixed::new(
                ecc_chip.clone(),
                layouter.namespace(|| "rivk_internal"),
                self.rivk_internal.map(|rivk| rivk.inner()),
            )?;
            let ivk_internal = commit_ivk(
                config.sinsemilla_chip_1(),
                ecc_chip.clone(),
                config.commit_ivk_chip(),
                layouter.namespace(|| "commit_ivk_internal"),
                ak_for_internal,
                nk.clone(),
                rivk_internal,
            )?;
            ivk_internal.inner().clone()
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
            // Copy-constrain to the condition-5 witness so both cells are bound to the same
            // point. Without this, a malicious prover could supply a different point here;
            // soundness is still preserved through the nf_signed public-input chain, but the
            // explicit equality closes the gap and makes the intent unambiguous.
            pk_d_signed_for_nc.constrain_equal(
                layouter.namespace(|| "pk_d_signed_for_nc == pk_d_signed"),
                &pk_d_signed,
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
        // rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, cmx_5, van_comm, vote_round_id)
        // Binds the signed note to the exact notes being delegated, the governance
        // commitment, and the round, making the keystone signature non-replayable.
        //
        // Public inputs live in the instance column, but gates can only constrain
        // advice cells. assign_advice_from_instance copies each public input into an
        // advice cell with a copy constraint, so the prover cannot substitute a
        // different value. The resulting cells are then passed into downstream gates.

        // van_comm: used in condition 3 (rho binding hash) and condition 7 (gov
        // commitment integrity check).
        let van_comm_cell = layouter.assign_region(
            || "copy van_comm from instance",
            |mut region| {
                region.assign_advice_from_instance(
                    || "van_comm",
                    config.primary,
                    VAN_COMM,
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

        // For each of the 5 note slots, synthesize_note_slot proves:
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

        let mut cmx_cells = Vec::with_capacity(5);
        let mut v_cells = Vec::with_capacity(5);
        let mut gov_null_cells = Vec::with_capacity(5);

        for i in 0..5 {
            let (cmx_i, v_i, gov_null_i) = synthesize_note_slot(
                &config,
                &mut layouter,
                ecc_chip.clone(),
                &ivk_cell,
                &ivk_internal_cell,
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
        // rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, cmx_5, van_comm, vote_round_id)
        // ---------------------------------------------------------------

        // The keystone note's rho is deterministically derived from the 5 note
        // commitments, the gov commitment, and the vote round. This binds the
        // keystone signature to the exact set of notes being delegated — replaying
        // the signature with different notes would produce a different rho, which
        // would change the nullifier (cond 2) and break the proof.
        {
            // Hash the 7 inputs: 5 note commitment x-coords (from cond 9),
            // van_comm (public input), and vote_round_id (public input).
            let poseidon_message = [
                cmx_cells[0].clone(),
                cmx_cells[1].clone(),
                cmx_cells[2].clone(),
                cmx_cells[3].clone(),
                cmx_cells[4].clone(),
                van_comm_cell.clone(),
                vote_round_id_cell.clone(),
            ];
            let poseidon_hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<7>,
                3,
                2,
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "rho binding Poseidon init"),
            )?;
            let derived_rho = poseidon_hasher.hash(
                layouter.namespace(|| "Poseidon(cmx_1..5, van_comm, vote_round_id)"),
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
        // The voting hotkey is bound transitively through van_comm (condition 7)
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
        // Compute v_total = v_1 + v_2 + v_3 + v_4 + v_5 (used by conditions 7 & 8).
        // ---------------------------------------------------------------

        // v_total = v_1 + v_2 + v_3 + v_4 + v_5  (four AddChip additions)
        let add_chip = config.add_chip();
        let sum_12 =
            add_chip.add(layouter.namespace(|| "v_1 + v_2"), &v_cells[0], &v_cells[1])?;
        let sum_123 = add_chip.add(
            layouter.namespace(|| "(v_1 + v_2) + v_3"),
            &sum_12,
            &v_cells[2],
        )?;
        let sum_1234 = add_chip.add(
            layouter.namespace(|| "(v_1 + v_2 + v_3) + v_4"),
            &sum_123,
            &v_cells[3],
        )?;
        let v_total = add_chip.add(
            layouter.namespace(|| "(v_1 + v_2 + v_3 + v_4) + v_5"),
            &sum_1234,
            &v_cells[4],
        )?;

        // ---------------------------------------------------------------
        // Condition 8: Ballot scaling.
        // num_ballots = floor(v_total / BALLOT_DIVISOR)
        // Proved by: num_ballots * BALLOT_DIVISOR + remainder == v_total,
        //            range checks on num_ballots and remainder,
        //            and a non-zero check on num_ballots.
        // ---------------------------------------------------------------

        // Ballot scaling (condition 8).
        //
        // Converts the raw zatoshi balance into a ballot count via floor-division:
        //   num_ballots = floor(v_total / 12,500,000)
        //
        // Constraints:
        //   1. num_ballots * BALLOT_DIVISOR + remainder == v_total
        //   2. remainder < 2^24   (24-bit range check via shift-by-2^6)
        //   3. 0 < num_ballots <= 2^30  (via nb_minus_one 30-bit range check)
        //
        // Range check implementation: the lookup table operates in 10-bit words,
        // so it directly checks multiples of 10 bits. For remainder (24 bits),
        // we multiply by 2^6 before a 30-bit check. For num_ballots, 30 bits is
        // already a multiple of 10, so nb_minus_one is checked directly with
        // 3 words — no shift needed. 2^30 ballots × 0.125 ZEC ≈ 134M ZEC,
        // well above the 21M ZEC supply, so 30 bits is a safe upper bound.
        //
        // The nb_minus_one check simultaneously enforces both the upper bound
        // and non-zero: if nb_minus_one < 2^30 then num_ballots ∈ [1, 2^30].
        // If num_ballots = 0, nb_minus_one wraps to p-1 ≈ 2^254, failing the check.
        let num_ballots = {
            // Witness num_ballots and remainder as free advice.
            let num_ballots = assign_free_advice(
                layouter.namespace(|| "witness num_ballots"),
                config.advices[0],
                self.num_ballots,
            )?;

            let remainder = assign_free_advice(
                layouter.namespace(|| "witness remainder"),
                config.advices[0],
                self.remainder,
            )?;

            // Assign the BALLOT_DIVISOR constant (baked into verification key).
            let ballot_divisor = assign_constant(
                layouter.namespace(|| "BALLOT_DIVISOR constant"),
                config.advices[0],
                pallas::Base::from(BALLOT_DIVISOR),
            )?;

            // product = num_ballots * BALLOT_DIVISOR
            let product = config.mul_chip().mul(
                layouter.namespace(|| "num_ballots * BALLOT_DIVISOR"),
                &num_ballots,
                &ballot_divisor,
            )?;

            // reconstructed = product + remainder
            let reconstructed = config.add_chip().add(
                layouter.namespace(|| "product + remainder"),
                &product,
                &remainder,
            )?;

            // Constrain: reconstructed == v_total
            layouter.assign_region(
                || "num_ballots * BALLOT_DIVISOR + remainder == v_total",
                |mut region| region.constrain_equal(reconstructed.cell(), v_total.cell()),
            )?;

            // Range check remainder to [0, 2^24).
            // 24 is not a multiple of 10, so we multiply by 2^(30-24) = 2^6 = 64
            // and range-check the shifted value to 30 bits (3 words × 10 bits).
            // If remainder >= 2^24, then remainder * 64 >= 2^30, failing the check.
            let shift_6 = assign_constant(
                layouter.namespace(|| "2^6 shift constant"),
                config.advices[0],
                pallas::Base::from(1u64 << 6),
            )?;
            let remainder_shifted = config.mul_chip().mul(
                layouter.namespace(|| "remainder * 2^6"),
                &remainder,
                &shift_6,
            )?;
            config.range_check_config().copy_check(
                layouter.namespace(|| "remainder * 2^6 < 2^30 (i.e. remainder < 2^24)"),
                remainder_shifted,
                3,    // num_words: 3 * 10 = 30 bits
                true, // strict: running sum terminates at 0
            )?;

            // Non-zero and upper bound: 0 < num_ballots <= 2^30.
            // Witness nb_minus_one = num_ballots - 1 and constrain
            // nb_minus_one + 1 == num_ballots. Range-check nb_minus_one
            // directly to 30 bits (3 words × 10 — no shift needed).
            // This single check enforces both bounds: if nb_minus_one < 2^30
            // then num_ballots ∈ [1, 2^30]. If num_ballots = 0, nb_minus_one
            // wraps to p - 1 ≈ 2^254, which fails the range check.
            let one = assign_constant(
                layouter.namespace(|| "one constant"),
                config.advices[0],
                pallas::Base::one(),
            )?;

            let nb_minus_one = num_ballots.value().map(|v| *v - pallas::Base::one());
            let nb_minus_one = assign_free_advice(
                layouter.namespace(|| "witness nb_minus_one"),
                config.advices[0],
                nb_minus_one,
            )?;

            let nb_recomputed = config.add_chip().add(
                layouter.namespace(|| "nb_minus_one + 1"),
                &nb_minus_one,
                &one,
            )?;
            layouter.assign_region(
                || "nb_minus_one + 1 == num_ballots",
                |mut region| region.constrain_equal(nb_recomputed.cell(), num_ballots.cell()),
            )?;

            config.range_check_config().copy_check(
                layouter.namespace(|| "nb_minus_one < 2^30"),
                nb_minus_one,
                3,    // num_words: 3 * 10 = 30 bits
                true, // strict: running sum terminates at 0
            )?;

            num_ballots
        };

        // ---------------------------------------------------------------
        // Condition 7: Gov commitment integrity.
        // van_comm_core = Poseidon(DOMAIN_VAN, g_d_new_x, pk_d_new_x, num_ballots,
        //                          vote_round_id, MAX_PROPOSAL_AUTHORITY)
        // van_comm = Poseidon(van_comm_core, van_comm_rand)
        // ---------------------------------------------------------------

        // Gov commitment integrity (condition 7).
        //
        // van_comm_core = Poseidon(DOMAIN_VAN, g_d_new_x, pk_d_new_x, num_ballots,
        //                          vote_round_id, MAX_PROPOSAL_AUTHORITY)
        // van_comm = Poseidon(van_comm_core, van_comm_rand)
        //
        // Proves that the governance commitment (public input) is correctly derived
        // from the domain tag, the output note's voting hotkey address, the ballot
        // count (floor-divided from v_total), the vote round identifier, a blinding
        // factor, and the proposal authority bitmask (MAX_PROPOSAL_AUTHORITY = 65535
        // for full authority).
        //
        // Uses two Poseidon invocations over even arities (6 then 2).
        {
            let van_comm_rand = assign_free_advice(
                layouter.namespace(|| "witness van_comm_rand"),
                config.advices[0],
                self.van_comm_rand,
            )?;

            // DOMAIN_VAN — domain tag for Vote Authority Notes. Provides domain
            // separation from Vote Commitments in the shared vote commitment tree.
            let domain_van = assign_constant(
                layouter.namespace(|| "DOMAIN_VAN constant"),
                config.advices[0],
                pallas::Base::from(van_integrity::DOMAIN_VAN),
            )?;

            // MAX_PROPOSAL_AUTHORITY — baked into the verification key so the
            // prover cannot alter it.
            let max_proposal_authority = assign_constant(
                layouter.namespace(|| "MAX_PROPOSAL_AUTHORITY constant"),
                config.advices[0],
                pallas::Base::from(MAX_PROPOSAL_AUTHORITY),
            )?;

            // Two-layer Poseidon hash via the shared VAN integrity gadget.
            // Uses num_ballots (from condition 8) instead of v_total.
            let derived_van_comm = van_integrity::van_integrity_poseidon(
                &config.poseidon_config,
                &mut layouter,
                "Gov commitment",
                domain_van,
                g_d_new_x,
                pk_d_new_x,
                num_ballots,
                vote_round_id_cell,
                max_proposal_authority,
                van_comm_rand,
            )?;

            // Constrain: derived_van_comm == van_comm (from condition 3).
            layouter.assign_region(
                || "van_comm integrity",
                |mut region| region.constrain_equal(derived_van_comm.cell(), van_comm_cell.cell()),
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
    ivk_internal_cell: &AssignedCell<pallas::Base, pallas::Base>,
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
    // Condition 11: Diversified address integrity (scope-aware).
    // pk_d = [selected_ivk] * g_d
    // where selected_ivk = ivk (external) or ivk_internal, based on is_internal.
    // ---------------------------------------------------------------

    // Proves this note belongs to the prover's key. External notes use ivk
    // (derived from rivk in condition 5); internal (change) notes use
    // ivk_internal (derived from rivk_internal). The q_scope_select gate
    // constrains the mux: selected_ivk = ivk + is_internal * (ivk_internal - ivk).

    // Witness the is_internal flag for this note.
    let is_internal = assign_free_advice(
        layouter.namespace(|| format!("note {s} witness is_internal")),
        config.advices[0],
        note.is_internal.map(|b| pallas::Base::from(b as u64)),
    )?;

    // Mux between ivk and ivk_internal using the q_scope_select custom gate.
    let selected_ivk = layouter.assign_region(
        || format!("note {s} scope ivk select"),
        |mut region| {
            config.q_scope_select.enable(&mut region, 0)?;

            is_internal.copy_advice(|| "is_internal", &mut region, config.advices[0], 0)?;
            ivk_cell.copy_advice(|| "ivk", &mut region, config.advices[1], 0)?;
            ivk_internal_cell.copy_advice(|| "ivk_internal", &mut region, config.advices[2], 0)?;

            // Compute the muxed value: ivk + is_internal * (ivk_internal - ivk)
            let selected = ivk_cell.value().zip(ivk_internal_cell.value()).zip(is_internal.value()).map(
                |((ivk, ivk_int), flag)| {
                    if *flag == pallas::Base::one() { *ivk_int } else { *ivk }
                },
            );
            region.assign_advice(|| "selected_ivk", config.advices[3], 0, || selected)
        },
    )?;

    // Convert selected_ivk to a scalar for ECC multiplication.
    let ivk_scalar = ScalarVar::from_base(
        ecc_chip.clone(),
        layouter.namespace(|| format!("note {s} selected_ivk to scalar")),
        &selected_ivk,
    )?;

    // Compute [selected_ivk] * g_d and check it matches the witnessed pk_d.
    let (derived_pk_d, _ivk) = g_d.mul(
        layouter.namespace(|| format!("note {s} [selected_ivk] g_d")),
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
    // gov_null = Poseidon(nk, domain_tag, vote_round_id, real_nf)
    // ---------------------------------------------------------------

    // Derives a governance-domain nullifier published on the vote chain to prevent
    // double-delegation. Single ConstantLength<4> Poseidon hash (2 permutations
    // at rate=2) that:
    //   - Is keyed by nk, so it can't be linked to real_nf even when real_nf is
    //     later revealed on mainchain
    //   - Is domain-separated from other nullifier uses ("governance authorization" tag)
    //   - Is scoped to this voting round (vote_round_id)
    //
    // The result is constrained to the public instance so the vote chain can
    // track which notes have already been delegated this round.

    // Domain tag = "governance authorization" as a field element, baked into
    // the verification key.
    let domain_tag = assign_constant(
        layouter.namespace(|| format!("note {s} gov_auth domain tag")),
        config.advices[0],
        crate::delegation::imt::gov_auth_domain_tag(),
    )?;

    // Poseidon(nk, domain_tag, vote_round_id, real_nf)
    let gov_null = {
        let poseidon_hasher =
            PoseidonHash::<pallas::Base, _, poseidon::P128Pow5T3, ConstantLength<4>, 3, 2>::init(
                config.poseidon_chip(),
                layouter.namespace(|| format!("note {s} gov_null init")),
            )?;
        poseidon_hasher.hash(
            layouter.namespace(|| format!("note {s} Poseidon(nk, domain_tag, vote_round_id, real_nf)")),
            [nk_cell.clone(), domain_tag, vote_round_id_cell.clone(), real_nf.inner().clone()],
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

    let imt_root = synthesize_imt_non_membership(
        &config.imt_config,
        &config.poseidon_config,
        &config.ecc_config,
        layouter,
        note.imt_low,
        note.imt_width,
        note.imt_leaf_pos,
        note.imt_path,
        real_nf.inner(),
        s,
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

/// Public inputs to the delegation circuit (13 field elements).
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
    pub van_comm: pallas::Base,
    /// The voting round identifier.
    pub vote_round_id: pallas::Base,
    /// The note commitment tree root (shared anchor).
    pub nc_root: pallas::Base,
    /// The nullifier IMT root.
    pub nf_imt_root: pallas::Base,
    /// Per-note governance nullifiers (5 slots).
    pub gov_null: [pallas::Base; 5],
}

impl Instance {
    /// Constructs an [`Instance`] from its constituent parts.
    pub fn from_parts(
        nf_signed: Nullifier,
        rk: VerificationKey<SpendAuth>,
        cmx_new: pallas::Base,
        van_comm: pallas::Base,
        vote_round_id: pallas::Base,
        nc_root: pallas::Base,
        nf_imt_root: pallas::Base,
        gov_null: [pallas::Base; 5],
    ) -> Self {
        Instance {
            nf_signed,
            rk,
            cmx_new,
            van_comm,
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
            self.van_comm,
            self.vote_round_id,
            self.nc_root,
            self.nf_imt_root,
            self.gov_null[0],
            self.gov_null[1],
            self.gov_null[2],
            self.gov_null[3],
            self.gov_null[4],
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
    use crate::delegation::imt::{gov_null_hash, ImtProofData, ImtProvider, SpacedLeafImtProvider};
    use orchard::{
        keys::{FullViewingKey, Scope, SpendValidatingKey, SpendingKey},
        note::{commitment::ExtractedNoteCommitment, Note, Rho},
    };
    use ff::Field;
    use halo2_proofs::dev::MockProver;
    use incrementalmerkletree::{Hashable, Level};
    use pasta_curves::{arithmetic::CurveAffine, pallas};
    use rand::rngs::OsRng;

    // Re-use the public K constant from the circuit module.
    use super::K;

    /// Helper: build a NoteSlotWitness for a note with a Merkle path and IMT proof.
    fn make_note_slot(
        note: &Note,
        auth_path: &[MerkleHashOrchard; MERKLE_DEPTH_ORCHARD],
        pos: u32,
        imt: &ImtProofData,
        is_real: bool,
        is_internal: bool,
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
            imt_width: Value::known(imt.width),
            imt_leaf_pos: Value::known(imt.leaf_pos),
            imt_path: Value::known(imt.path),
            is_internal: Value::known(is_internal),
        }
    }

    /// Return value from `make_test_data` bundling all test artefacts.
    struct TestData {
        circuit: Circuit,
        instance: Instance,
    }

    /// Build a valid merged circuit with 1 real note + 4 padded notes.
    fn make_test_data() -> TestData {
        let mut rng = OsRng;

        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let output_recipient = fvk.address_at(1u32, Scope::External);

        // Key material.
        let nk_val = fvk.nk().inner();
        let ak: SpendValidatingKey = fvk.clone().into();

        let vote_round_id = pallas::Base::random(&mut rng);
        let van_comm_rand = pallas::Base::random(&mut rng);

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
        let imt_0 = imt_provider.non_membership_proof(real_nf.0).unwrap();
        let gov_null_0 = gov_null_hash(nk_val, vote_round_id, real_nf.0);

        let slot_0 = make_note_slot(&real_note, &auth_path_0, 0u32, &imt_0, true, false);

        // Padded notes (slots 1-4): zero-value notes with addresses from the real ivk.
        let mut note_slots = vec![slot_0];
        let mut cmx_values = vec![cmx_real];
        let mut gov_nulls = vec![gov_null_0];

        let dummy_auth_path = [MerkleHashOrchard::empty_leaf(); MERKLE_DEPTH_ORCHARD];

        for i in 1..5u32 {
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
            let pad_imt = imt_provider.non_membership_proof(pad_nf.0).unwrap();
            let pad_gov_null = gov_null_hash(nk_val, vote_round_id, pad_nf.0);

            note_slots.push(make_note_slot(
                &pad_note,
                &dummy_auth_path,
                0u32,
                &pad_imt,
                false,
                false,
            ));
            cmx_values.push(pad_cmx);
            gov_nulls.push(pad_gov_null);
        }

        let notes: [NoteSlotWitness; 5] = note_slots.try_into().unwrap();

        // Values: real note = 13M, padded = 0.
        // Ballot scaling: 13,000,000 / 12,500,000 = 1 ballot, remainder = 500,000.
        let v_total_u64: u64 = 13_000_000;
        let num_ballots_u64 = v_total_u64 / BALLOT_DIVISOR;
        let remainder_u64 = v_total_u64 % BALLOT_DIVISOR;
        let num_ballots_field = pallas::Base::from(num_ballots_u64);

        // Compute van_comm.
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
        let van_comm =
            van_commitment_hash(g_d_new_x, pk_d_new_x, num_ballots_field, vote_round_id, van_comm_rand);

        // Compute rho.
        let rho = rho_binding_hash(
            cmx_values[0],
            cmx_values[1],
            cmx_values[2],
            cmx_values[3],
            cmx_values[4],
            van_comm,
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
            .with_van_comm_rand(van_comm_rand)
            .with_ballot_scaling(
                pallas::Base::from(num_ballots_u64),
                pallas::Base::from(remainder_u64),
            );

        let instance = Instance::from_parts(
            nf_signed,
            rk,
            cmx_new,
            van_comm,
            vote_round_id,
            nc_root,
            nf_imt_root,
            [gov_nulls[0], gov_nulls[1], gov_nulls[2], gov_nulls[3], gov_nulls[4]],
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
    fn wrong_van_comm_fails() {
        let t = make_test_data();
        let mut instance = t.instance.clone();
        instance.van_comm = pallas::Base::random(&mut OsRng);

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
        assert_eq!(pi.len(), 13, "Expected exactly 13 public inputs");
        assert_eq!(pi[NF_SIGNED], t.instance.nf_signed.0);
        assert_eq!(pi[CMX_NEW], t.instance.cmx_new);
        assert_eq!(pi[VAN_COMM], t.instance.van_comm);
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

    // Condition 10: is_note_real = 1 with a non-existent note claiming non-zero value.
    // The Merkle path check is the only guard here — condition 15 allows v > 0 on
    // "real" notes, but condition 10 requires the commitment to open to nc_root.
    #[test]
    fn fake_real_note_nonzero_value_fails() {
        let mut rng = OsRng;
        let t = make_test_data();
        let mut circuit = t.circuit;
        let pi = t.instance.to_halo2_instance();

        // Build a note with v > 0 using a fresh key; it is NOT in the commitment
        // tree that make_test_data() built (nc_root only covers slot 0's real note).
        let sk2 = SpendingKey::random(&mut rng);
        let fvk2: FullViewingKey = (&sk2).into();
        let addr2 = fvk2.address_at(0u32, Scope::External);
        let (_, _, dummy_parent) = Note::dummy(&mut rng, None);
        let fake_note = Note::new(
            addr2,
            NoteValue::from_raw(100), // v > 0: not a zero-value padded note
            Rho::from_nf_old(dummy_parent.nullifier(&fvk2)),
            &mut rng,
        );

        let imt_provider = SpacedLeafImtProvider::new();
        let fake_nf = fake_note.nullifier(&fvk2);
        let fake_imt = imt_provider.non_membership_proof(fake_nf.0).unwrap();

        // All empty siblings — this auth path does not open to nc_root.
        let dummy_auth_path = [MerkleHashOrchard::empty_leaf(); MERKLE_DEPTH_ORCHARD];
        // is_note_real = true activates condition 10: is_note_real * (root - nc_root) = 0.
        let fake_slot = make_note_slot(&fake_note, &dummy_auth_path, 0u32, &fake_imt, true, false);

        // Replace slot 1 (was padded, v=0, is_note_real=false) with the fake claim.
        circuit.notes[1] = fake_slot;

        let prover = MockProver::run(K, &circuit, vec![pi]).unwrap();
        // Condition 10: the dummy auth path produces a computed root ≠ nc_root,
        // so the Merkle path check rejects the non-existent "real" note.
        assert!(prover.verify().is_err());
    }

    // Condition 11 copy-constraint: confirms that ivk from condition 5 (the signed
    // note's key) is enforced in ALL per-note pk_d ownership checks via copy constraint.
    // If the per-note addresses use a different key, condition 11 fails even though
    // condition 10 (Merkle path) is skipped (is_note_real = false).
    #[test]
    fn different_ivk_per_note_fails() {
        let mut rng = OsRng;
        let t = make_test_data();
        let mut circuit = t.circuit;
        let pi = t.instance.to_halo2_instance();

        // Build a note from a different key (fvk2). The circuit derives ivk1 in
        // condition 5 (from fvk1, the signed note's key) and the copy constraint
        // propagates ivk1 into every condition 11 per-note ownership check.
        // For this foreign slot: [ivk1] * g_d_fvk2 ≠ pk_d_fvk2, so condition 11 fails.
        let sk2 = SpendingKey::random(&mut rng);
        let fvk2: FullViewingKey = (&sk2).into();
        let addr2 = fvk2.address_at(100u32, Scope::External);
        let (_, _, dummy_parent) = Note::dummy(&mut rng, None);
        let foreign_note = Note::new(
            addr2,
            NoteValue::zero(),
            Rho::from_nf_old(dummy_parent.nullifier(&fvk2)),
            &mut rng,
        );

        let imt_provider = SpacedLeafImtProvider::new();
        let foreign_nf = foreign_note.nullifier(&fvk2);
        let foreign_imt = imt_provider.non_membership_proof(foreign_nf.0).unwrap();

        let dummy_auth_path = [MerkleHashOrchard::empty_leaf(); MERKLE_DEPTH_ORCHARD];
        // is_note_real = false: condition 10 (Merkle root check) is skipped.
        // Condition 11 still applies to all slots and cannot be bypassed.
        let foreign_slot = make_note_slot(
            &foreign_note,
            &dummy_auth_path,
            0u32,
            &foreign_imt,
            false,
            false,
        );

        circuit.notes[1] = foreign_slot;

        let prover = MockProver::run(K, &circuit, vec![pi]).unwrap();
        // Condition 11: the copy constraint forces ivk from condition 5 (fvk1's ivk)
        // into this check; [ivk1] * g_d_fvk2 ≠ pk_d_fvk2 so the constraint fails,
        // confirming substitution of a foreign ivk in condition 11 is impossible.
        assert!(prover.verify().is_err());
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

    /// Measures actual rows used by the delegation circuit via `CircuitCost::measure`.
    ///
    /// `CircuitCost` runs the floor planner against the circuit and tracks the
    /// highest row offset assigned in any column, giving the real "rows consumed"
    /// number rather than the theoretical 2^K capacity.
    ///
    /// Run with:
    ///   cargo test row_budget -- --nocapture --ignored
    #[test]
    #[ignore]
    fn row_budget() {
        use std::println;
        use halo2_proofs::dev::CircuitCost;
        use pasta_curves::vesta;

        let t = make_test_data();

        let cost = CircuitCost::<vesta::Point, _>::measure(K, &t.circuit);
        let debug = alloc::format!("{cost:?}");

        let extract = |field: &str| -> usize {
            let prefix = alloc::format!("{field}: ");
            debug.split(&prefix)
                .nth(1)
                .and_then(|s| s.split([',', ' ', '}']).next())
                .and_then(|n| n.parse().ok())
                .unwrap_or(0)
        };

        let max_rows         = extract("max_rows");
        let max_advice_rows  = extract("max_advice_rows");
        let max_fixed_rows   = extract("max_fixed_rows");
        let total_available  = 1usize << K;

        println!("=== delegation circuit row budget (K={K}) ===");
        println!("  max_rows (floor-planner high-water mark): {max_rows}");
        println!("  max_advice_rows:                          {max_advice_rows}");
        println!("  max_fixed_rows:                           {max_fixed_rows}");
        println!("  2^K  (total available rows):              {total_available}");
        println!("  headroom:                                 {}", total_available.saturating_sub(max_rows));
        println!("  utilisation:                              {:.1}%",
            100.0 * max_rows as f64 / total_available as f64);
        println!();
        println!("  Full debug: {debug}");

        // Witness-independence check: Circuit::default() (all unknowns)
        // must produce exactly the same layout as the filled circuit.
        let cost_default = CircuitCost::<vesta::Point, _>::measure(K, &Circuit::default());
        let debug_default = alloc::format!("{cost_default:?}");
        let max_rows_default = debug_default
            .split("max_rows: ").nth(1)
            .and_then(|s| s.split([',', ' ', '}']).next())
            .and_then(|n| n.parse::<usize>().ok())
            .unwrap_or(0);
        if max_rows_default == max_rows {
            println!("  Witness-independence: PASS \
                (Circuit::default() max_rows={max_rows_default} == filled max_rows={max_rows})");
        } else {
            println!("  Witness-independence: FAIL \
                (Circuit::default() max_rows={max_rows_default} != filled max_rows={max_rows}) \
                — row count depends on witness values!");
        }

        println!("  MERKLE_DEPTH_ORCHARD (circuit constant): {MERKLE_DEPTH_ORCHARD}");
        println!("  IMT_DEPTH (circuit constant):             {IMT_DEPTH}");

        // Minimum-K probe: find the smallest K at which MockProver passes.
        for probe_k in 11u32..=K {
            let t = make_test_data();
            match MockProver::run(probe_k, &t.circuit, vec![t.instance.to_halo2_instance()]) {
                Err(_) => {
                    println!("  K={probe_k}: not enough rows (synthesizer rejected)");
                    continue;
                }
                Ok(p) => match p.verify() {
                    Ok(()) => {
                        println!("  Minimum viable K: {probe_k} (2^{probe_k} = {} rows, {:.1}% headroom)",
                            1usize << probe_k,
                            100.0 * (1.0 - max_rows as f64 / (1usize << probe_k) as f64));
                        break;
                    }
                    Err(_) => println!("  K={probe_k}: too small"),
                },
            }
        }
    }
}
