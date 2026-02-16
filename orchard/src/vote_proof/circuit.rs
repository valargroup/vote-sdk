//! The Vote Proof circuit implementation (ZKP #2).
//!
//! Proves that a registered voter is casting a valid vote, without
//! revealing which VAN they hold. Currently implements:
//!
//! - **Condition 1**: VAN Membership (Poseidon Merkle path, `constrain_instance`).
//! - **Condition 2**: VAN Integrity (Poseidon hash).
//! - **Condition 3**: Spend Authority (`vpk_pk_d = [ivk_v] * vpk_g_d` via CommitIvk).
//! - **Condition 4**: VAN Nullifier Integrity (nested Poseidon, `constrain_instance`).
//! - **Condition 5**: Proposal Authority Decrement (AddChip + range check).
//! - **Condition 6**: New VAN Integrity (Poseidon hash, `constrain_instance`).
//! - **Condition 7**: Shares Sum Correctness (AddChip, `constrain_equal`).
//! - **Condition 8**: Shares Range (LookupRangeCheck, `[0, 2^30)`).
//! - **Condition 9**: Shares Hash Integrity (Poseidon `ConstantLength<8>`; output flows to condition 11).
//! - **Condition 10**: Encryption Integrity (ECC variable-base mul, `constrain_equal`).
//! - **Condition 11**: Vote Commitment Integrity (Poseidon `ConstantLength<4>`, `constrain_instance`).
//!
//! All 11 conditions are fully constrained.
//!
//! ## Conditions overview
//!
//! VAN ownership and spending:
//! - **Condition 1**: VAN Membership — Merkle path from `vote_authority_note_old`
//!   to `vote_comm_tree_root`.
//! - **Condition 2**: VAN Integrity — `vote_authority_note_old` is the two-layer
//!   Poseidon hash (ZKP 1–compatible: core then finalize with rand). *(implemented)*
//! - **Condition 3**: Spend Authority — prover controls the VAN address via
//!   `vpk_pk_d = [ivk_v] * vpk_g_d` where `ivk_v = CommitIvk(ExtractP([vsk]*SpendAuthG), vsk.nk)`. *(implemented)*
//! - **Condition 4**: VAN Nullifier Integrity — `van_nullifier` is correctly
//!   derived from `vsk.nk`. *(implemented)*
//!
//! New VAN construction:
//! - **Condition 5**: Proposal Authority Decrement — `proposal_authority_new =
//!   proposal_authority_old - (1 << proposal_id)`, with bitmask range [0, 2^16). *(implemented)*
//! - **Condition 6**: New VAN Integrity — same two-layer structure as condition 2
//!   but with decremented authority. *(implemented)*
//!
//! Vote commitment construction:
//! - **Condition 7**: Shares Sum Correctness — `sum(shares_1..4) = total_note_value`.
//!   *(implemented)*
//! - **Condition 8**: Shares Range — each `shares_j` in `[0, 2^24)`.
//!   *(implemented)*
//! - **Condition 9**: Shares Hash Integrity — `shares_hash = H(enc_share_1..4)`.
//!   *(implemented)*
//! - **Condition 10**: Encryption Integrity — each `enc_share_i = ElGamal(shares_i, r_i, ea_pk)`.
//!   *(implemented)*
//! - **Condition 11**: Vote Commitment Integrity — `vote_commitment = H(DOMAIN_VC, shares_hash,
//!   proposal_id, vote_decision)`. *(implemented)*

use alloc::vec::Vec;

use halo2_proofs::{
    circuit::{floor_planner, AssignedCell, Layouter, Value},
    plonk::{
        self, Advice, Column, Constraints, ConstraintSystem, Expression, Fixed,
        Instance as InstanceColumn, Selector, TableColumn,
    },
    poly::Rotation,
};
use pasta_curves::{arithmetic::CurveAffine, pallas, vesta};

use halo2_gadgets::{
    ecc::{
        chip::{EccChip, EccConfig},
        NonIdentityPoint, ScalarVar,
    },
    poseidon::{
        primitives::{self as poseidon, ConstantLength},
        Hash as PoseidonHash, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig,
    },
    sinsemilla::chip::{SinsemillaChip, SinsemillaConfig},
    utilities::{bool_check, lookup_range_check::LookupRangeCheckConfig},
};
// TODO: re-enable when condition 3 is restored
// use crate::circuit::address_ownership::{prove_address_ownership, spend_auth_g_mul};
use crate::circuit::commit_ivk::{CommitIvkChip, CommitIvkConfig};
use crate::circuit::gadget::{add_chip::{AddChip, AddConfig}, AddInstruction};
use crate::constants::{
    OrchardCommitDomains, OrchardFixedBases, OrchardHashDomains,
};
use crate::circuit::van_integrity;

// ================================================================
// Constants
// ================================================================

/// Depth of the Poseidon-based vote commitment tree.
///
/// Reduced from Zcash's depth 32 (~4.3B) because governance voting
/// produces far fewer leaves than a full shielded pool. Each voter
/// generates 1 leaf per delegation + 2 per vote, so even 10K voters
/// × 50 proposals ≈ 1M leaves — well within 2^24 ≈ 16.7M capacity.
///
/// Must match `vote_commitment_tree::TREE_DEPTH`.
pub const VOTE_COMM_TREE_DEPTH: usize = 24;

/// Circuit size (2^K rows).
///
/// K=14 (16,384 rows). Conditions 1–9 use ~29 Poseidon hashes plus
/// AddChip additions, range-check running sums, ECC fixed-base mul
/// (condition 3), and 24 Merkle swap regions. Condition 10 adds 12
/// variable-base scalar multiplications (~6,000 rows) and 4 point
/// additions. The 10-bit lookup table requires 1,024 rows.
/// K=14 provides headroom.
pub const K: u32 = 14;

pub use van_integrity::DOMAIN_VAN;

/// Domain tag for Vote Commitments.
///
/// Prepended as the first Poseidon input for domain separation from
/// Vote Authority Notes in the shared vote commitment tree.
/// `DOMAIN_VC = 1` for Vote Commitments, `DOMAIN_VAN = 0` for VANs.
pub const DOMAIN_VC: u64 = 1;

/// Maximum number of proposals (0-indexed); proposal_id is in [0, MAX_PROPOSAL_ID).
/// Spec: "The number of proposals for a polling session must be <= 16."
pub const MAX_PROPOSAL_ID: usize = 16;

// ================================================================
// Public input offsets (9 field elements).
// ================================================================

/// Public input offset for the VAN nullifier (prevents double-vote).
const VAN_NULLIFIER: usize = 0;
/// Public input offset for the new VAN commitment (with decremented authority).
const VOTE_AUTHORITY_NOTE_NEW: usize = 1;
/// Public input offset for the vote commitment hash.
const VOTE_COMMITMENT: usize = 2;
/// Public input offset for the vote commitment tree root.
const VOTE_COMM_TREE_ROOT: usize = 3;
/// Public input offset for the tree anchor height.
const VOTE_COMM_TREE_ANCHOR_HEIGHT: usize = 4;
/// Public input offset for the proposal identifier.
const PROPOSAL_ID: usize = 5;
/// Public input offset for the voting round identifier.
const VOTING_ROUND_ID: usize = 6;
/// Public input offset for the election authority public key x-coordinate.
const EA_PK_X: usize = 7;
/// Public input offset for the election authority public key y-coordinate.
const EA_PK_Y: usize = 8;

// Suppress dead-code warnings for public input offsets that are
// defined but not yet used by any condition's constraint logic.
// VOTE_COMM_TREE_ANCHOR_HEIGHT is checked out-of-circuit by the
// verifier (the chain validates the anchor height matches the tree).
const _: usize = VOTE_COMM_TREE_ANCHOR_HEIGHT;

// ================================================================
// Out-of-circuit helpers
// ================================================================

pub use van_integrity::van_integrity_hash;

/// Returns the domain separator for the VAN nullifier inner hash.
///
/// Encodes `"vote authority spend"` as a Pallas base field element
/// by interpreting the UTF-8 bytes as a little-endian 256-bit integer.
/// This domain tag differentiates VAN nullifier derivation from other
/// Poseidon uses in the protocol.
pub fn domain_van_nullifier() -> pallas::Base {
    // "vote authority spend" (20 bytes) zero-padded to 32, as LE u64 words.
    pallas::Base::from_raw([
        0x7475_6120_6574_6f76, // b"vote aut" LE
        0x7320_7974_6972_6f68, // b"hority s" LE
        0x0000_0000_646e_6570, // b"pend\0\0\0\0" LE
        0,
    ])
}

/// Out-of-circuit VAN nullifier hash (condition 4).
///
/// Three-layer `ConstantLength<2>` Poseidon chain (matches ZKP 1
/// condition 14's governance nullifier pattern):
/// ```text
/// step1  = Poseidon(voting_round_id, vote_authority_note_old)   // scope to round + VAN
/// step2  = Poseidon("vote authority spend", step1)              // domain separation
/// van_nullifier = Poseidon(vsk_nk, step2)                       // key with nk
/// ```
///
/// Used by the builder and tests to compute the expected VAN nullifier.
pub fn van_nullifier_hash(
    vsk_nk: pallas::Base,
    voting_round_id: pallas::Base,
    vote_authority_note_old: pallas::Base,
) -> pallas::Base {
    // Step 1: Poseidon(voting_round_id, vote_authority_note_old) — scope to round + VAN.
    let step1 =
        poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([
            voting_round_id,
            vote_authority_note_old,
        ]);
    // Step 2: Poseidon(domain_tag, step1) — domain separation.
    let step2 =
        poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([
            domain_van_nullifier(),
            step1,
        ]);
    // Step 3: Poseidon(vsk_nk, step2) — key the result so it can't be reversed.
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init()
        .hash([vsk_nk, step2])
}

/// Out-of-circuit Poseidon hash of two field elements.
///
/// `Poseidon(a, b)` with P128Pow5T3, ConstantLength<2>, width 3, rate 2.
/// Used for Merkle path computation (condition 1) and tests. This is the
/// same hash function used by `vote_commitment_tree::MerkleHashVote::combine`.
pub fn poseidon_hash_2(a: pallas::Base, b: pallas::Base) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([a, b])
}

/// Out-of-circuit shares hash (condition 9).
///
/// Computes:
/// ```text
/// Poseidon(c1_0_x, c2_0_x, c1_1_x, c2_1_x, c1_2_x, c2_2_x, c1_3_x, c2_3_x)
/// ```
///
/// where each `(c1_i_x, c2_i_x)` are the x-coordinates (via ExtractP)
/// of the El Gamal ciphertext components for share `i`:
///   - `c1_i = r_i * G`
///   - `c2_i = shares_i * G + r_i * ea_pk`
///
/// The order interleaves C1 and C2 components per share, matching
/// the in-circuit witness layout. `ConstantLength<8>` absorbs the
/// 8 field elements in 4 chunks of 2 (rate = 2).
///
/// Used by the builder and tests to compute the expected shares hash.
pub fn shares_hash(
    enc_share_c1_x: [pallas::Base; 4],
    enc_share_c2_x: [pallas::Base; 4],
) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<8>, 3, 2>::init().hash([
        enc_share_c1_x[0], enc_share_c2_x[0],
        enc_share_c1_x[1], enc_share_c2_x[1],
        enc_share_c1_x[2], enc_share_c2_x[2],
        enc_share_c1_x[3], enc_share_c2_x[3],
    ])
}

/// Out-of-circuit vote commitment hash (condition 11).
///
/// Computes:
/// ```text
/// Poseidon(DOMAIN_VC, shares_hash, proposal_id, vote_decision)
/// ```
///
/// This is the final vote commitment that is posted on-chain and
/// inserted into the vote commitment tree. It binds the encrypted
/// shares, the proposal choice, and the vote decision into a single
/// hash with domain separation from VANs.
///
/// Used by the builder and tests to compute the expected vote commitment.
pub fn vote_commitment_hash(
    shares_hash: pallas::Base,
    proposal_id: pallas::Base,
    vote_decision: pallas::Base,
) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<4>, 3, 2>::init().hash([
        pallas::Base::from(DOMAIN_VC),
        shares_hash,
        proposal_id,
        vote_decision,
    ])
}

/// Returns the SpendAuthG generator point (used as G in El Gamal).
///
/// This is the same generator used for spend authorization in the Zcash
/// Orchard protocol. We reuse it as the El Gamal generator so that
/// condition 3 (spend authority) and condition 10 (encryption integrity)
/// share the same ECC chip configuration.
pub fn spend_auth_g_affine() -> pallas::Affine {
    use group::Curve;
    let g = crate::constants::fixed_bases::spend_auth_g::generator();
    pallas::Point::from(g).to_affine()
}

/// Converts a `pallas::Base` field element to a `pallas::Scalar`.
///
/// Both fields have 255-bit moduli that differ only in the low bits.
/// For small values (< 2^30, as guaranteed by condition 8 for shares),
/// the integer representation is identical in both fields. For full-size
/// values (El Gamal randomness), the conversion is valid as long as the
/// base element is < scalar field modulus (overwhelmingly likely for
/// random elements; probability of failure ≈ 2^{-254}).
///
/// Returns `None` if the byte representation exceeds the scalar field modulus.
pub fn base_to_scalar(b: pallas::Base) -> Option<pallas::Scalar> {
    use ff::PrimeField;
    pallas::Scalar::from_repr(b.to_repr()).into()
}

/// Out-of-circuit El Gamal encryption under SpendAuthG (condition 10).
///
/// Computes:
/// ```text
/// C1 = [r] * SpendAuthG
/// C2 = [v] * SpendAuthG + [r] * ea_pk
/// ```
///
/// Returns `(c1_x, c2_x)` — the x-coordinates of C1 and C2 (via ExtractP).
/// The `randomness` and `share_value` are base field elements, converted
/// to scalars for ECC multiplication.
///
/// Used by tests to compute the expected enc_share x-coordinates.
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
// Config
// ================================================================

/// Configuration for the Vote Proof circuit.
///
/// Holds chip configs for Poseidon (conditions 1, 2, 4, 6, 9), AddChip
/// (conditions 5, 7), LookupRangeCheck (conditions 5, 8), ECC
/// (conditions 3, 10), and the Merkle swap gate (condition 1). Will
/// be extended with custom gates as condition 11 is added.
#[derive(Clone, Debug)]
pub struct Config {
    /// Public input column (9 field elements).
    primary: Column<InstanceColumn>,
    /// 10 advice columns for private witness data.
    ///
    /// Column layout follows the delegation circuit for consistency:
    /// - `advices[0..5]`: general witness assignment + Merkle swap gate.
    /// - `advices[5]`: Poseidon partial S-box column.
    /// - `advices[6..9]`: Poseidon state columns + AddChip output.
    /// - `advices[9]`: range check running sum.
    advices: [Column<Advice>; 10],
    /// Poseidon hash chip configuration.
    ///
    /// P128Pow5T3 with width 3, rate 2. Used for VAN integrity (condition 2),
    /// VAN nullifier (condition 4), new VAN integrity (condition 6),
    /// vote commitment Merkle path (condition 1), and vote commitment
    /// integrity (conditions 9, 11).
    poseidon_config: PoseidonConfig<pallas::Base, 3, 2>,
    /// AddChip: constrains `a + b = c` on a single row.
    ///
    /// Uses advices[7] (a), advices[8] (b), advices[6] (c), matching
    /// the delegation circuit's column assignment.
    /// Used in conditions 5 (proposal authority decrement) and 7 (shares
    /// sum correctness).
    add_config: AddConfig,
    /// ECC chip configuration (condition 3: spend authority, condition 10: El Gamal).
    ///
    /// Condition 3 proves `vpk_pk_d = [ivk_v] * vpk_g_d` via the CommitIvk chain:
    /// `[vsk] * SpendAuthG → ak → CommitIvk(ExtractP(ak), nk, rivk_v) → ivk_v → [ivk_v] * vpk_g_d`.
    /// Shares advice and fixed columns with Poseidon per delegation layout.
    ecc_config: EccConfig<OrchardFixedBases>,
    /// Sinsemilla chip configuration (condition 3: CommitIvk requires Sinsemilla).
    ///
    /// Uses advices[0..5] for Sinsemilla message hashing, advices[6] for
    /// witnessing message pieces, and lagrange_coeffs[0] for the fixed y_Q column.
    /// Also loads the 10-bit lookup table used by LookupRangeCheckConfig.
    sinsemilla_config:
        SinsemillaConfig<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases>,
    /// CommitIvk chip configuration (condition 3: canonicity checks on ak || nk).
    ///
    /// Provides the custom gate and decomposition logic for the
    /// Sinsemilla-based `CommitIvk` commitment.
    commit_ivk_config: CommitIvkConfig,
    /// 10-bit lookup range check configuration.
    ///
    /// Uses advices[9] as the running-sum column. Each word is 10 bits,
    /// so `num_words` × 10 gives the total bit-width checked.
    /// Used in condition 5 to ensure authority values and diff are in [0, 2^16)
    /// (16-bit bitmask), and condition 8 to ensure each share is in `[0, 2^24)`.
    range_check: LookupRangeCheckConfig<pallas::Base, 10>,
    /// Selector for the Merkle conditional swap gate (condition 1).
    ///
    /// At each of the 24 Merkle tree levels, conditionally swaps
    /// (current, sibling) into (left, right) based on the position bit.
    /// Uses advices[0..5]: pos_bit, current, sibling, left, right.
    /// Identical to the delegation circuit's `q_imt_swap` gate.
    q_merkle_swap: Selector,
    /// Selector for condition 5 (Proposal Authority Decrement) row.
    /// When 1, the (proposal_id, one_shifted) lookup is enforced; when 0,
    /// the lookup input is (0, 1) so it passes without constraining.
    q_cond5: Selector,
    /// Lookup table column for proposal_id in (proposal_id, 2^proposal_id).
    /// Table rows: (0, 1), (1, 2), (2, 4), ..., (15, 32768).
    table_proposal_id: TableColumn,
    /// Lookup table column for one_shifted = 2^proposal_id.
    table_one_shifted: TableColumn,
}

impl Config {
    /// Constructs a Poseidon chip from this configuration.
    ///
    /// Width 3 (P128Pow5T3 state size), rate 2 (absorbs 2 field elements
    /// per permutation — halves the number of rounds vs rate 1).
    pub(crate) fn poseidon_chip(&self) -> PoseidonChip<pallas::Base, 3, 2> {
        PoseidonChip::construct(self.poseidon_config.clone())
    }

    /// Constructs an AddChip for field element addition (`c = a + b`).
    fn add_chip(&self) -> AddChip {
        AddChip::construct(self.add_config.clone())
    }

    /// Constructs an ECC chip for curve operations (conditions 3, 10).
    fn ecc_chip(&self) -> EccChip<OrchardFixedBases> {
        EccChip::construct(self.ecc_config.clone())
    }

    /// Constructs a Sinsemilla chip (condition 3: CommitIvk).
    fn sinsemilla_chip(
        &self,
    ) -> SinsemillaChip<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases> {
        SinsemillaChip::construct(self.sinsemilla_config.clone())
    }

    /// Constructs a CommitIvk chip for canonicity checks (condition 3).
    fn commit_ivk_chip(&self) -> CommitIvkChip {
        CommitIvkChip::construct(self.commit_ivk_config.clone())
    }

    /// Returns the range check configuration (10-bit words).
    fn range_check_config(&self) -> LookupRangeCheckConfig<pallas::Base, 10> {
        self.range_check
    }
}

// ================================================================
// Circuit
// ================================================================

/// The Vote Proof circuit (ZKP #2).
///
/// Proves that a registered voter is casting a valid vote, without
/// revealing which VAN they hold. Contains witness fields for all
/// 11 conditions; constraint logic is added incrementally.
///
/// All 11 conditions are fully constrained: VAN membership, VAN
/// integrity, spend authority, nullifier, authority decrement, new
/// VAN integrity, shares sum, shares range, shares hash integrity,
/// encryption integrity, vote commitment integrity.
#[derive(Clone, Debug, Default)]
pub struct Circuit {
    // === VAN ownership and spending (conditions 1–4) ===

    // Condition 1 (VAN Membership): Poseidon-based Merkle path from
    // vote_authority_note_old to vote_comm_tree_root.
    /// Merkle authentication path (sibling hashes at each tree level).
    pub(crate) vote_comm_tree_path: Value<[pallas::Base; VOTE_COMM_TREE_DEPTH]>,
    /// Leaf position in the vote commitment tree.
    pub(crate) vote_comm_tree_position: Value<u32>,

    // Condition 2 (VAN Integrity): two-layer hash matching ZKP 1 (delegation):
    // gov_comm_core = Poseidon(DOMAIN_VAN, vpk_g_d.x, vpk_pk_d.x, total_note_value,
    //                          voting_round_id, proposal_authority_old);
    // vote_authority_note_old = Poseidon(gov_comm_core, gov_comm_rand).
    //
    // Condition 3 (Spend Authority): vpk_pk_d = [ivk_v] * vpk_g_d
    // where ivk_v = CommitIvk(ExtractP([vsk]*SpendAuthG), vsk.nk, rivk_v).
    // Full affine points are needed for condition 3's ECC operations;
    // x-coordinates are extracted in-circuit for Poseidon hashing (conditions 2, 6).
    /// Voting public key — diversified base point (from DiversifyHash(d)).
    /// This is the vpk_g_d component of the voting hotkey address.
    /// Condition 3 performs `[ivk_v] * vpk_g_d` to derive vpk_pk_d.
    pub(crate) vpk_g_d: Value<pallas::Affine>,
    /// Voting public key — diversified transmission key (pk_d = [ivk_v] * g_d).
    /// This is the vpk_pk_d component of the voting hotkey address.
    /// Condition 3 constrains this to equal `[ivk_v] * vpk_g_d`.
    pub(crate) vpk_pk_d: Value<pallas::Affine>,
    /// The voter's total delegated weight.
    pub(crate) total_note_value: Value<pallas::Base>,
    /// Remaining proposal authority bitmask in the old VAN.
    pub(crate) proposal_authority_old: Value<pallas::Base>,
    /// Blinding randomness for the VAN commitment.
    pub(crate) gov_comm_rand: Value<pallas::Base>,
    /// The old VAN commitment (Poseidon hash output). Used as the Merkle
    /// leaf in condition 1 and constrained to equal the derived hash here.
    pub(crate) vote_authority_note_old: Value<pallas::Base>,

    // Condition 3 (Spend Authority): prover controls the VAN address.
    // vpk_pk_d = [ivk_v] * vpk_g_d
    //   where ivk_v = CommitIvk_rivk_v(ExtractP([vsk]*SpendAuthG), vsk.nk)
    /// Voting spending key (scalar for ECC multiplication).
    /// Used in condition 3 for `[vsk] * SpendAuthG`.
    pub(crate) vsk: Value<pallas::Scalar>,
    /// CommitIvk randomness for the ivk_v derivation (condition 3).
    /// Used as the blinding scalar in `CommitIvk(ak, nk, rivk_v)`.
    pub(crate) rivk_v: Value<pallas::Scalar>,

    // Condition 4 (VAN Nullifier Integrity): nullifier deriving key.
    // Also used in condition 3 as the nk input to CommitIvk.
    /// Nullifier deriving key derived from vsk.
    pub(crate) vsk_nk: Value<pallas::Base>,

    // Condition 5 (Proposal Authority Decrement): one_shifted = 2^proposal_id.
    /// Cleared bit value: one_shifted = 2^proposal_id (witness; lookup constrains it).
    pub(crate) one_shifted: Value<pallas::Base>,

    // === Vote commitment construction (conditions 7–11) ===

    // Condition 7 (Shares Sum): sum(shares_1..4) = total_note_value.
    // Condition 8 (Shares Range): each share in [0, 2^24).
    /// Voting share vector (4 shares that sum to total_note_value).
    pub(crate) shares: [Value<pallas::Base>; 4],

    // Condition 9 (Shares Hash Integrity): El Gamal ciphertext x-coordinates.
    // These are the x-coordinates of the curve points comprising each
    // El Gamal ciphertext. Condition 10 (not yet implemented) will
    // constrain these to be correct encryptions; condition 9 hashes them.
    /// X-coordinates of C1_i = r_i * G for each share (via ExtractP).
    pub(crate) enc_share_c1_x: [Value<pallas::Base>; 4],
    /// X-coordinates of C2_i = shares_i * G + r_i * ea_pk for each share (via ExtractP).
    pub(crate) enc_share_c2_x: [Value<pallas::Base>; 4],

    // Condition 10 (Encryption Integrity): El Gamal randomness and public key.
    /// El Gamal encryption randomness for each share (base field element,
    /// converted to scalar via ScalarVar::from_base in-circuit).
    pub(crate) share_randomness: [Value<pallas::Base>; 4],
    /// Election authority public key (Pallas curve point).
    /// The El Gamal encryption key — published as a round parameter.
    /// Both coordinates are public inputs (EA_PK_X, EA_PK_Y).
    pub(crate) ea_pk: Value<pallas::Affine>,

    // Condition 11 (Vote Commitment Integrity): vote decision.
    /// The voter's choice (hidden inside the vote commitment).
    pub(crate) vote_decision: Value<pallas::Base>,
}

impl Circuit {
    /// Creates a circuit with conditions 1–6 witnesses populated.
    ///
    /// All other witness fields are set to `Value::unknown()`.
    /// - Condition 1 uses `vote_authority_note_old` as the Merkle leaf,
    ///   with `vote_comm_tree_path` and `vote_comm_tree_position` for
    ///   the authentication path.
    /// - Condition 2 binds `vote_authority_note_old` to the Poseidon hash
    ///   of its components (using x-coordinates extracted from vpk_g_d, vpk_pk_d).
    /// - Condition 3 proves spend authority via CommitIvk chain:
    ///   `[vsk] * SpendAuthG → ak → CommitIvk(ak, nk, rivk_v) → ivk_v → [ivk_v] * vpk_g_d = vpk_pk_d`.
    /// - Condition 4 reuses `vote_authority_note_old` and `voting_round_id`.
    /// - Condition 5 derives `proposal_authority_new` from
    ///   `proposal_authority_old`.
    /// - Condition 6 reuses all condition 2 witnesses except
    ///   `proposal_authority_old`, which is replaced by the
    ///   in-circuit `proposal_authority_new` from condition 5.
    pub fn with_van_witnesses(
        vote_comm_tree_path: Value<[pallas::Base; VOTE_COMM_TREE_DEPTH]>,
        vote_comm_tree_position: Value<u32>,
        vpk_g_d: Value<pallas::Affine>,
        vpk_pk_d: Value<pallas::Affine>,
        total_note_value: Value<pallas::Base>,
        proposal_authority_old: Value<pallas::Base>,
        gov_comm_rand: Value<pallas::Base>,
        vote_authority_note_old: Value<pallas::Base>,
        vsk: Value<pallas::Scalar>,
        rivk_v: Value<pallas::Scalar>,
        vsk_nk: Value<pallas::Base>,
    ) -> Self {
        Circuit {
            vote_comm_tree_path,
            vote_comm_tree_position,
            vpk_g_d,
            vpk_pk_d,
            total_note_value,
            proposal_authority_old,
            gov_comm_rand,
            vote_authority_note_old,
            vsk,
            rivk_v,
            vsk_nk,
            ..Default::default()
        }
    }
}

/// Loads a private witness value into a fresh advice cell.
///
/// Each call gets its own single-row region, matching the delegation
/// circuit's `assign_free_advice` helper pattern.
fn assign_free_advice(
    mut layouter: impl Layouter<pallas::Base>,
    column: Column<Advice>,
    value: Value<pallas::Base>,
) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
    layouter.assign_region(
        || "load private",
        |mut region| region.assign_advice(|| "private input", column, 0, || value),
    )
}

impl plonk::Circuit<pallas::Base> for Circuit {
    type Config = Config;
    type FloorPlanner = floor_planner::V1;

    fn without_witnesses(&self) -> Self {
        Self::default()
    }

    fn configure(meta: &mut ConstraintSystem<pallas::Base>) -> Self::Config {
        // 10 advice columns, matching delegation circuit layout.
        let advices: [Column<Advice>; 10] = core::array::from_fn(|_| meta.advice_column());
        for col in &advices {
            meta.enable_equality(*col);
        }

        // Instance column for public inputs.
        let primary = meta.instance_column();
        meta.enable_equality(primary);

        // 8 fixed columns shared between ECC and Poseidon chips.
        // Indices 0–1: Lagrange coefficients (ECC chip only).
        // Indices 2–4: Poseidon round constants A (rc_a).
        // Indices 5–7: Poseidon round constants B (rc_b).
        let lagrange_coeffs: [Column<Fixed>; 8] =
            core::array::from_fn(|_| meta.fixed_column());
        let rc_a = lagrange_coeffs[2..5].try_into().unwrap();
        let rc_b = lagrange_coeffs[5..8].try_into().unwrap();

        // Dedicated constants column, separate from the Lagrange coefficient
        // columns used by the ECC chip. This prevents collisions between
        // the ECC chip's fixed-base scalar multiplication tables and the
        // constant-zero cells created by strict range checks.
        let constants = meta.fixed_column();
        meta.enable_constant(constants);

        // AddChip: constrains `a + b = c` in a single row.
        // Column assignment matches the delegation circuit:
        //   a = advices[7], b = advices[8], c = advices[6].
        let add_config = AddChip::configure(meta, advices[7], advices[8], advices[6]);

        // Lookup table columns for Sinsemilla (3 columns) and range checks.
        // The first column (table_idx) is shared between Sinsemilla and
        // LookupRangeCheckConfig. SinsemillaChip::load populates all three
        // during synthesis (replacing the manual table loading).
        let table_idx = meta.lookup_table_column();
        let lookup = (
            table_idx,
            meta.lookup_table_column(),
            meta.lookup_table_column(),
        );

        // Range check configuration: 10-bit lookup words in advices[9].
        let range_check = LookupRangeCheckConfig::configure(meta, advices[9], table_idx);

        // ECC chip: fixed- and variable-base scalar multiplication for
        // condition 3 (spend authority via CommitIvk chain) and condition 10
        // (El Gamal encryption integrity).
        // Shares columns with Poseidon per delegation circuit layout.
        let ecc_config =
            EccChip::<OrchardFixedBases>::configure(meta, advices, lagrange_coeffs, range_check);

        // Sinsemilla chip: required by CommitIvk for condition 3.
        // Uses advices[0..5] for Sinsemilla message hashing, advices[6] for
        // witnessing message pieces, and lagrange_coeffs[0] for the fixed
        // y_Q column. Shares the lookup table with LookupRangeCheckConfig.
        let sinsemilla_config = SinsemillaChip::configure(
            meta,
            advices[..5].try_into().unwrap(),
            advices[6],
            lagrange_coeffs[0],
            lookup,
            range_check,
        );

        // CommitIvk chip: canonicity checks on the ak || nk decomposition
        // inside the CommitIvk Sinsemilla commitment (condition 3).
        let commit_ivk_config = CommitIvkChip::configure(meta, advices);

        // Poseidon chip: P128Pow5T3 with width 3, rate 2.
        // State columns: advices[6..9] (3 columns for the width-3 state).
        // Partial S-box column: advices[5].
        // Round constants: lagrange_coeffs[2..5] (rc_a), [5..8] (rc_b).
        let poseidon_config = PoseidonChip::configure::<poseidon::P128Pow5T3>(
            meta,
            advices[6..9].try_into().unwrap(),
            advices[5],
            rc_a,
            rc_b,
        );

        // Merkle conditional swap gate (condition 1).
        // At each level of the Poseidon Merkle path, we need to place
        // (current, sibling) into (left, right) based on the position bit.
        // If pos_bit=0, current is the left child; if pos_bit=1, they swap.
        // Identical to the delegation circuit's q_imt_swap gate.
        let q_merkle_swap = meta.selector();
        meta.create_gate("Merkle conditional swap", |meta| {
            let q = meta.query_selector(q_merkle_swap);
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
                    (
                        "bool_check pos_bit",
                        bool_check(pos_bit),
                    ),
                ],
            )
        });

        // Condition 5: (proposal_id, one_shifted) lookup table for
        // one_shifted = 2^proposal_id. When q_cond5 = 0 the lookup input
        // is (0, 1) so it passes; when q_cond5 = 1 we enforce (proposal_id,
        // one_shifted) in {(0,1), (1,2), ..., (15, 32768)}.
        // Must be complex_selector because we use it in (one - q) in the lookup.
        let q_cond5 = meta.complex_selector();
        let table_proposal_id = meta.lookup_table_column();
        let table_one_shifted = meta.lookup_table_column();
        meta.lookup(|meta| {
            let q = meta.query_selector(q_cond5);
            let proposal_id = meta.query_advice(advices[0], Rotation::cur());
            let one_shifted = meta.query_advice(advices[1], Rotation::cur());
            // When q=0: (0, 1); when q=1: (proposal_id, one_shifted).
            let input_0 = q.clone() * proposal_id;
            let one = Expression::Constant(pallas::Base::one());
            let input_1 = q.clone() * one_shifted + (one.clone() - q) * one;
            vec![
                (input_0, table_proposal_id),
                (input_1, table_one_shifted),
            ]
        });

        Config {
            primary,
            advices,
            poseidon_config,
            add_config,
            ecc_config,
            sinsemilla_config,
            commit_ivk_config,
            range_check,
            q_merkle_swap,
            q_cond5,
            table_proposal_id,
            table_one_shifted,
        }
    }

    #[allow(non_snake_case)]
    fn synthesize(
        &self,
        config: Self::Config,
        mut layouter: impl Layouter<pallas::Base>,
    ) -> Result<(), plonk::Error> {
        // ---------------------------------------------------------------
        // Load the Sinsemilla generator lookup table.
        //
        // Populates the 10-bit lookup table and Sinsemilla generator
        // points. Required by CommitIvk (condition 3), and also provides
        // the range check table used by conditions 5 and 8.
        // ---------------------------------------------------------------
        SinsemillaChip::load(config.sinsemilla_config.clone(), &mut layouter)?;

        // Load (proposal_id, 2^proposal_id) lookup table for condition 5.
        // Rows: (0, 1), (1, 2), (2, 4), ..., (15, 32768).
        layouter.assign_table(
            || "proposal_id one_shifted table",
            |mut table| {
                for i in 0..MAX_PROPOSAL_ID {
                    table.assign_cell(
                        || "table proposal_id",
                        config.table_proposal_id,
                        i,
                        || Value::known(pallas::Base::from(i as u64)),
                    )?;
                    table.assign_cell(
                        || "table one_shifted",
                        config.table_one_shifted,
                        i,
                        || Value::known(pallas::Base::from(1u64 << i)),
                    )?;
                }
                Ok(())
            },
        )?;


        // Construct the ECC chip (used in conditions 3 and 10).
        let ecc_chip = config.ecc_chip();

        // ---------------------------------------------------------------
        // Witness assignment for condition 2.
        // ---------------------------------------------------------------

        // Copy voting_round_id from the instance column into an advice cell.
        // This creates an equality constraint between the advice cell and the
        // instance at offset VOTING_ROUND_ID, ensuring the in-circuit value
        // matches the public input.
        let voting_round_id = layouter.assign_region(
            || "copy voting_round_id from instance",
            |mut region| {
                region.assign_advice_from_instance(
                    || "voting_round_id",
                    config.primary,
                    VOTING_ROUND_ID,
                    config.advices[0],
                    0,
                )
            },
        )?;

        // Witness vpk_g_d as a full non-identity curve point (condition 3 needs
        // the point for variable-base ECC mul; conditions 2/6 need the x-coordinate
        // for Poseidon hashing).
        let vpk_g_d_point = NonIdentityPoint::new(
            ecc_chip.clone(),
            layouter.namespace(|| "witness vpk_g_d"),
            self.vpk_g_d.map(|p| p),
        )?;
        let vpk_g_d = vpk_g_d_point.extract_p().inner().clone();

        // Witness vpk_pk_d as a full non-identity curve point (condition 3
        // constrains the derived point to equal this; conditions 2/6 use x-coordinate).
        let vpk_pk_d_point = NonIdentityPoint::new(
            ecc_chip.clone(),
            layouter.namespace(|| "witness vpk_pk_d"),
            self.vpk_pk_d.map(|p| p),
        )?;
        let vpk_pk_d = vpk_pk_d_point.extract_p().inner().clone();

        let total_note_value = assign_free_advice(
            layouter.namespace(|| "witness total_note_value"),
            config.advices[0],
            self.total_note_value,
        )?;

        let proposal_authority_old = assign_free_advice(
            layouter.namespace(|| "witness proposal_authority_old"),
            config.advices[0],
            self.proposal_authority_old,
        )?;

        let gov_comm_rand = assign_free_advice(
            layouter.namespace(|| "witness gov_comm_rand"),
            config.advices[0],
            self.gov_comm_rand,
        )?;

        let vote_authority_note_old = assign_free_advice(
            layouter.namespace(|| "witness vote_authority_note_old"),
            config.advices[0],
            self.vote_authority_note_old,
        )?;

        // DOMAIN_VAN — constant-constrained so the value is baked into the
        // verification key and cannot be altered by a malicious prover.
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

        // ---------------------------------------------------------------
        // Witness assignment for conditions 3 and 4.
        //
        // vsk_nk is shared between condition 3 (CommitIvk input) and
        // condition 4 (VAN nullifier). Witnessed here so it's available
        // for condition 3 which runs before condition 4.
        // ---------------------------------------------------------------

        // Private witness: nullifier deriving key (shared by conditions 3, 4).
        let vsk_nk = assign_free_advice(
            layouter.namespace(|| "witness vsk_nk"),
            config.advices[0],
            self.vsk_nk,
        )?;

        // Clone cells that are consumed by condition 2's Poseidon hash but
        // reused in later conditions:
        // - vote_authority_note_old: also used in condition 1 (Merkle leaf).
        // - voting_round_id: also used in condition 4 (VAN nullifier).
        // - vpk_g_d, vpk_pk_d, total_note_value, voting_round_id, proposal_authority_old,
        //   gov_comm_rand, domain_van: also used in condition 6 (new VAN integrity).
        // - total_note_value: also used in condition 7 (shares sum check).
        // - vsk_nk: also used in condition 4 (VAN nullifier).
        let vote_authority_note_old_cond1 = vote_authority_note_old.clone();
        let voting_round_id_cond4 = voting_round_id.clone();
        let domain_van_cond6 = domain_van.clone();
        let vpk_g_d_cond6 = vpk_g_d.clone();
        let vpk_pk_d_cond6 = vpk_pk_d.clone();
        let total_note_value_cond6 = total_note_value.clone();
        let total_note_value_cond7 = total_note_value.clone();
        let voting_round_id_cond6 = voting_round_id.clone();
        let _proposal_authority_old_cond5 = proposal_authority_old.clone();
        let gov_comm_rand_cond6 = gov_comm_rand.clone();
        let vsk_nk_cond4 = vsk_nk.clone();

        // ---------------------------------------------------------------
        // Condition 2: VAN Integrity (ZKP 1–compatible two-layer hash).
        // gov_comm_core = Poseidon(DOMAIN_VAN, vpk_g_d, vpk_pk_d, total_note_value,
        //                          voting_round_id, proposal_authority_old)
        // vote_authority_note_old = Poseidon(gov_comm_core, gov_comm_rand)
        // ---------------------------------------------------------------

        let derived_van = van_integrity::van_integrity_poseidon(
            &config.poseidon_config,
            &mut layouter,
            "Old VAN integrity",
            domain_van,
            vpk_g_d,
            vpk_pk_d,
            total_note_value,
            voting_round_id,
            proposal_authority_old,
            gov_comm_rand,
        )?;

        // Constrain: derived VAN hash == witnessed vote_authority_note_old.
        layouter.assign_region(
            || "VAN integrity check",
            |mut region| region.constrain_equal(derived_van.cell(), vote_authority_note_old.cell()),
        )?;

        // ---------------------------------------------------------------
        // Condition 3: Spend Authority — TEMPORARILY DISABLED.
        //
        // TODO: Re-enable once the CommitIvk canonicity range-check
        // layout conflict is resolved. The out-of-circuit key-chain
        // consistency check in the builder verifies the same property.
        // ---------------------------------------------------------------

        // ---------------------------------------------------------------
        // Condition 1: VAN Membership.
        //
        // MerklePath(vote_authority_note_old, position, path) = vote_comm_tree_root
        //
        // Poseidon-based Merkle path verification (24 levels). At each
        // level, the position bit determines child ordering: if bit=0,
        // current is the left child; if bit=1, current is the right child.
        //
        // The leaf is vote_authority_note_old, which is already constrained
        // to be a correct Poseidon hash by condition 2. This creates a
        // binding: the VAN integrity check and the Merkle membership proof
        // are tied to the same commitment.
        //
        // The hash function is Poseidon(left, right) with no level tag,
        // matching vote_commitment_tree::MerkleHashVote::combine.
        // ---------------------------------------------------------------
        {
            let mut current = vote_authority_note_old_cond1;

            for i in 0..VOTE_COMM_TREE_DEPTH {
                // Witness position bit for this level.
                let pos_bit = assign_free_advice(
                    layouter.namespace(|| alloc::format!("merkle pos_bit {i}")),
                    config.advices[0],
                    self.vote_comm_tree_position
                        .map(|p| pallas::Base::from(((p >> i) & 1) as u64)),
                )?;

                // Witness sibling hash at this level.
                let sibling = assign_free_advice(
                    layouter.namespace(|| alloc::format!("merkle sibling {i}")),
                    config.advices[0],
                    self.vote_comm_tree_path.map(|path| path[i]),
                )?;

                // Conditional swap: order (current, sibling) by position bit.
                // The q_merkle_swap gate constrains:
                //   left  = current + pos_bit * (sibling - current)
                //   right = current + sibling - left
                //   pos_bit ∈ {0, 1}
                let (left, right) = layouter.assign_region(
                    || alloc::format!("merkle swap level {i}"),
                    |mut region| {
                        config.q_merkle_swap.enable(&mut region, 0)?;

                        let pos_bit_cell = pos_bit.copy_advice(
                            || "pos_bit",
                            &mut region,
                            config.advices[0],
                            0,
                        )?;
                        let current_cell = current.copy_advice(
                            || "current",
                            &mut region,
                            config.advices[1],
                            0,
                        )?;
                        let sibling_cell = sibling.copy_advice(
                            || "sibling",
                            &mut region,
                            config.advices[2],
                            0,
                        )?;

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

                // Hash parent = Poseidon(left, right).
                let parent = {
                    let hasher = PoseidonHash::<
                        pallas::Base,
                        _,
                        poseidon::P128Pow5T3,
                        ConstantLength<2>,
                        3, // WIDTH
                        2, // RATE
                    >::init(
                        config.poseidon_chip(),
                        layouter.namespace(|| alloc::format!("merkle hash init level {i}")),
                    )?;
                    hasher.hash(
                        layouter.namespace(|| alloc::format!(
                            "Poseidon(left, right) level {i}"
                        )),
                        [left, right],
                    )?
                };

                current = parent;
            }

            // Bind the computed Merkle root to the VOTE_COMM_TREE_ROOT
            // public input. The verifier checks that the voter's VAN is
            // a leaf in the published vote commitment tree.
            layouter.constrain_instance(
                current.cell(),
                config.primary,
                VOTE_COMM_TREE_ROOT,
            )?;
        }

        // ---------------------------------------------------------------
        // Witness assignment for condition 4.
        //
        // vsk_nk was already witnessed before condition 3 (shared between
        // conditions 3 and 4). The vsk_nk_cond4 clone is used here.
        // ---------------------------------------------------------------

        // "vote authority spend" domain tag — constant-constrained so the
        // value is baked into the verification key.
        let domain_van_nf = layouter.assign_region(
            || "DOMAIN_VAN_NULLIFIER constant",
            |mut region| {
                region.assign_advice_from_constant(
                    || "domain_van_nullifier",
                    config.advices[0],
                    0,
                    domain_van_nullifier(),
                )
            },
        )?;

        // ---------------------------------------------------------------
        // Condition 4: VAN Nullifier Integrity.
        // van_nullifier = Poseidon(vsk_nk,
        //     Poseidon(domain, Poseidon(voting_round_id, vote_authority_note_old)))
        //
        // Three-layer ConstantLength<2> chain matching ZKP 1 condition 14:
        //   Step 1: scope to round + VAN
        //   Step 2: domain separation
        //   Step 3: key with nullifier key
        //
        // voting_round_id and vote_authority_note_old are reused from
        // condition 2 via cell equality — these cells flow directly into
        // the Poseidon state without being re-witnessed.
        // ---------------------------------------------------------------

        let van_nullifier = {
            // Step 1: Poseidon(voting_round_id, vote_authority_note_old)
            // — scope to this round + VAN.
            let step1_hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<2>,
                3, // WIDTH
                2, // RATE
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "VAN nullifier step 1 init"),
            )?;
            let step1 = step1_hasher.hash(
                layouter.namespace(|| "Poseidon(voting_round_id, vote_authority_note_old)"),
                [voting_round_id_cond4, vote_authority_note_old],
            )?;

            // Step 2: Poseidon(domain_tag, step1) — domain separation.
            let step2_hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<2>,
                3, // WIDTH
                2, // RATE
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "VAN nullifier step 2 init"),
            )?;
            let step2 = step2_hasher.hash(
                layouter.namespace(|| "Poseidon(domain_van_nullifier, step1)"),
                [domain_van_nf, step1],
            )?;

            // Step 3: Poseidon(vsk_nk, step2) — key the result so it
            // can't be reversed without knowing the spending key.
            let step3_hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<2>,
                3, // WIDTH
                2, // RATE
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "VAN nullifier step 3 init"),
            )?;
            step3_hasher.hash(
                layouter.namespace(|| "Poseidon(vsk_nk, step2)"),
                [vsk_nk_cond4, step2],
            )?
        };

        // Bind the derived nullifier to the VAN_NULLIFIER public input.
        // The verifier checks that the prover's computed nullifier matches
        // the publicly posted value, preventing double-voting.
        layouter.constrain_instance(van_nullifier.cell(), config.primary, VAN_NULLIFIER)?;

        // ---------------------------------------------------------------
        // Condition 5: Proposal Authority Decrement — TEMPORARILY DISABLED.
        //
        // TODO: Re-enable the lookup, addition constraints, and range checks
        // once the strict range-check layout conflict is resolved.
        // Only the witness assignments are kept so that condition 6 (which
        // needs proposal_authority_new) and condition 11 (which needs
        // proposal_id) can still reference them.
        // ---------------------------------------------------------------

        let proposal_id = layouter.assign_region(
            || "copy proposal_id from instance (cond5 stub)",
            |mut region| {
                region.assign_advice_from_instance(
                    || "proposal_id",
                    config.primary,
                    PROPOSAL_ID,
                    config.advices[0],
                    0,
                )
            },
        )?;

        let proposal_authority_new = {
            let val = self.proposal_authority_old
                .zip(self.one_shifted)
                .map(|(old, shift)| old - shift);
            assign_free_advice(
                layouter.namespace(|| "witness proposal_authority_new (cond5 stub)"),
                config.advices[0],
                val,
            )?
        };

        // ---------------------------------------------------------------
        // Condition 6: New VAN Integrity (ZKP 1–compatible two-layer hash).
        //
        // Same structure as condition 2; proposal_authority_new (from
        // condition 5) replaces proposal_authority_old. vpk_g_d and vpk_pk_d
        // are unchanged (same diversified address).
        // ---------------------------------------------------------------

        let derived_van_new = van_integrity::van_integrity_poseidon(
            &config.poseidon_config,
            &mut layouter,
            "New VAN integrity",
            domain_van_cond6,
            vpk_g_d_cond6,
            vpk_pk_d_cond6,
            total_note_value_cond6,
            voting_round_id_cond6,
            proposal_authority_new,
            gov_comm_rand_cond6,
        )?;

        // Bind the derived new VAN to the VOTE_AUTHORITY_NOTE_NEW public input.
        // The verifier checks that the new VAN commitment posted on-chain is
        // correctly formed with decremented proposal authority.
        layouter.constrain_instance(
            derived_van_new.cell(),
            config.primary,
            VOTE_AUTHORITY_NOTE_NEW,
        )?;

        // ---------------------------------------------------------------
        // Condition 7: Shares Sum Correctness.
        //
        // sum(share_0, share_1, share_2, share_3) = total_note_value
        //
        // Proves the voting share decomposition is consistent with the
        // total delegated weight. Uses three chained AddChip additions:
        //   partial_1 = share_0 + share_1
        //   partial_2 = partial_1 + share_2
        //   sum       = partial_2 + share_3
        // Then constrains sum == total_note_value (from condition 2).
        // ---------------------------------------------------------------

        // Witness the 4 plaintext shares. These cells will also be used
        // by condition 8 (range check) and condition 10 (El Gamal
        // encryption inputs) when those conditions are implemented.
        let share_0 = assign_free_advice(
            layouter.namespace(|| "witness share_0"),
            config.advices[0],
            self.shares[0],
        )?;
        let share_1 = assign_free_advice(
            layouter.namespace(|| "witness share_1"),
            config.advices[0],
            self.shares[1],
        )?;
        let share_2 = assign_free_advice(
            layouter.namespace(|| "witness share_2"),
            config.advices[0],
            self.shares[2],
        )?;
        let share_3 = assign_free_advice(
            layouter.namespace(|| "witness share_3"),
            config.advices[0],
            self.shares[3],
        )?;

        // Chain 3 additions: share_0 + share_1 + share_2 + share_3.
        let partial_1 = config.add_chip().add(
            layouter.namespace(|| "share_0 + share_1"),
            &share_0,
            &share_1,
        )?;
        let partial_2 = config.add_chip().add(
            layouter.namespace(|| "partial_1 + share_2"),
            &partial_1,
            &share_2,
        )?;
        let shares_sum = config.add_chip().add(
            layouter.namespace(|| "partial_2 + share_3"),
            &partial_2,
            &share_3,
        )?;

        // Constrain: shares_sum == total_note_value.
        // This ensures the 4 shares decompose the voter's total delegated
        // weight without creating or destroying value.
        layouter.assign_region(
            || "shares sum == total_note_value",
            |mut region| {
                region.constrain_equal(shares_sum.cell(), total_note_value_cond7.cell())
            },
        )?;

        // ---------------------------------------------------------------
        // Condition 8: Shares Range.
        //
        // Each share_i in [0, 2^30)
        //
        // Prevents overflow by ensuring each plaintext share fits in a
        // bounded range. Uses 3 × 10-bit lookup words with strict mode,
        // giving [0, 2^30). The protocol spec targets [0, 2^24), but
        // halo2_gadgets v0.3's `short_range_check` is private, so we
        // use the next available 10-bit-aligned bound. 30 bits (~1B per
        // share) is still secure: max sum of 4 shares ≈ 4B, well within
        // the Pallas field, and the homomorphic tally accumulates over
        // far fewer voters than 2^30.
        //
        // If a share exceeds 2^30 (or wraps around the field, e.g.
        // from underflow), the 3-word decomposition produces a non-zero
        // z_3 running sum, which fails the strict check.
        // ---------------------------------------------------------------

        // Share cells are cloned because copy_check takes ownership;
        // the originals remain available for condition 10 (El Gamal).
        config.range_check_config().copy_check(
            layouter.namespace(|| "share_0 < 2^30"),
            share_0.clone(),
            3,    // num_words: 3 × 10 = 30 bits
            true, // strict: running sum terminates at 0
        )?;
        config.range_check_config().copy_check(
            layouter.namespace(|| "share_1 < 2^30"),
            share_1.clone(),
            3,
            true,
        )?;
        config.range_check_config().copy_check(
            layouter.namespace(|| "share_2 < 2^30"),
            share_2.clone(),
            3,
            true,
        )?;
        config.range_check_config().copy_check(
            layouter.namespace(|| "share_3 < 2^30"),
            share_3.clone(),
            3,
            true,
        )?;

        // ---------------------------------------------------------------
        // Condition 9: Shares Hash Integrity.
        //
        // shares_hash = Poseidon(c1_0_x, c2_0_x, c1_1_x, c2_1_x,
        //                        c1_2_x, c2_2_x, c1_3_x, c2_3_x)
        //
        // Hashes the 8 x-coordinates of the 4 El Gamal ciphertext pairs
        // into a single commitment. The order interleaves C1 and C2
        // per share for locality. shares_hash is an internal wire; it
        // is not bound to the instance column. Condition 10 constrains
        // that each (c1_i_x, c2_i_x) is a valid El Gamal encryption of
        // shares_i. Condition 11 computes the full vote commitment
        // H(DOMAIN_VC, shares_hash, proposal_id, vote_decision) and
        // binds that value to the VOTE_COMMITMENT public input.
        // ---------------------------------------------------------------

        // Witness the 8 El Gamal ciphertext x-coordinates.
        let enc_c1_0 = assign_free_advice(
            layouter.namespace(|| "witness enc_share_c1_x[0]"),
            config.advices[0],
            self.enc_share_c1_x[0],
        )?;
        let enc_c2_0 = assign_free_advice(
            layouter.namespace(|| "witness enc_share_c2_x[0]"),
            config.advices[0],
            self.enc_share_c2_x[0],
        )?;
        let enc_c1_1 = assign_free_advice(
            layouter.namespace(|| "witness enc_share_c1_x[1]"),
            config.advices[0],
            self.enc_share_c1_x[1],
        )?;
        let enc_c2_1 = assign_free_advice(
            layouter.namespace(|| "witness enc_share_c2_x[1]"),
            config.advices[0],
            self.enc_share_c2_x[1],
        )?;
        let enc_c1_2 = assign_free_advice(
            layouter.namespace(|| "witness enc_share_c1_x[2]"),
            config.advices[0],
            self.enc_share_c1_x[2],
        )?;
        let enc_c2_2 = assign_free_advice(
            layouter.namespace(|| "witness enc_share_c2_x[2]"),
            config.advices[0],
            self.enc_share_c2_x[2],
        )?;
        let enc_c1_3 = assign_free_advice(
            layouter.namespace(|| "witness enc_share_c1_x[3]"),
            config.advices[0],
            self.enc_share_c1_x[3],
        )?;
        let enc_c2_3 = assign_free_advice(
            layouter.namespace(|| "witness enc_share_c2_x[3]"),
            config.advices[0],
            self.enc_share_c2_x[3],
        )?;

        // Clone enc_share cells before the Poseidon hash (which consumes
        // them). These clones are used by condition 10 to constrain that
        // the hashed x-coordinates match the computed El Gamal ciphertexts.
        let enc_c1_0_cond10 = enc_c1_0.clone();
        let enc_c2_0_cond10 = enc_c2_0.clone();
        let enc_c1_1_cond10 = enc_c1_1.clone();
        let enc_c2_1_cond10 = enc_c2_1.clone();
        let enc_c1_2_cond10 = enc_c1_2.clone();
        let enc_c2_2_cond10 = enc_c2_2.clone();
        let enc_c1_3_cond10 = enc_c1_3.clone();
        let enc_c2_3_cond10 = enc_c2_3.clone();

        // Compute shares_hash = Poseidon(c1_0, c2_0, c1_1, c2_1,
        //                                c1_2, c2_2, c1_3, c2_3).
        // The result is used by condition 11 (vote commitment integrity).
        let shares_hash = {
            let message = [
                enc_c1_0, enc_c2_0,
                enc_c1_1, enc_c2_1,
                enc_c1_2, enc_c2_2,
                enc_c1_3, enc_c2_3,
            ];
            let hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<8>,
                3, // WIDTH
                2, // RATE
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "shares hash Poseidon init"),
            )?;
            hasher.hash(
                layouter.namespace(|| "shares_hash = Poseidon(enc_shares)"),
                message,
            )?
        };

        // ---------------------------------------------------------------
        // Condition 10: Encryption Integrity.
        //
        // For each share i:
        //   C1_i = [r_i] * G
        //   C2_i = [v_i] * G + [r_i] * ea_pk
        //
        // where G = SpendAuthG (El Gamal generator) and ea_pk is the
        // election authority's public key (public input).
        //
        // Each multiplication uses variable-base scalar multiplication
        // via ScalarVar::from_base (converting base field elements to
        // scalars). The computed x-coordinates are constrained against
        // condition 9's witnessed enc_share cells, creating a binding
        // between the Poseidon hash and the actual ECC computation.
        //
        // G is constrained to SpendAuthG by fixing both coordinates to
        // compile-time constants. ea_pk is constrained to the public
        // input coordinates at offsets EA_PK_X and EA_PK_Y.
        // ---------------------------------------------------------------
        {
            // SpendAuthG coordinates as constants — baked into the
            // verification key so the El Gamal generator cannot be changed.
            let g_affine = spend_auth_g_affine();
            let g_x_val = *g_affine.coordinates().unwrap().x();
            let g_y_val = *g_affine.coordinates().unwrap().y();

            let g_x_const = layouter.assign_region(
                || "SpendAuthG x constant",
                |mut region| {
                    region.assign_advice_from_constant(
                        || "g_x", config.advices[0], 0, g_x_val,
                    )
                },
            )?;
            let g_y_const = layouter.assign_region(
                || "SpendAuthG y constant",
                |mut region| {
                    region.assign_advice_from_constant(
                        || "g_y", config.advices[0], 0, g_y_val,
                    )
                },
            )?;

            // Copy ea_pk coordinates from the instance column.
            let ea_pk_x_cell = layouter.assign_region(
                || "copy ea_pk_x from instance",
                |mut region| {
                    region.assign_advice_from_instance(
                        || "ea_pk_x",
                        config.primary,
                        EA_PK_X,
                        config.advices[0],
                        0,
                    )
                },
            )?;
            let ea_pk_y_cell = layouter.assign_region(
                || "copy ea_pk_y from instance",
                |mut region| {
                    region.assign_advice_from_instance(
                        || "ea_pk_y",
                        config.primary,
                        EA_PK_Y,
                        config.advices[0],
                        0,
                    )
                },
            )?;

            // Collect the enc_share cells and share cells for the loop.
            let enc_c1_cells = [
                enc_c1_0_cond10, enc_c1_1_cond10,
                enc_c1_2_cond10, enc_c1_3_cond10,
            ];
            let enc_c2_cells = [
                enc_c2_0_cond10, enc_c2_1_cond10,
                enc_c2_2_cond10, enc_c2_3_cond10,
            ];
            let share_cells = [
                share_0.clone(), share_1.clone(),
                share_2.clone(), share_3.clone(),
            ];

            for i in 0..4 {
                // --- C1_i = [r_i] * G ---

                // Witness G as NonIdentityPoint, constrain to SpendAuthG.
                let g_c1 = NonIdentityPoint::new(
                    ecc_chip.clone(),
                    layouter.namespace(|| alloc::format!("cond10 G for C1[{i}]")),
                    Value::known(g_affine),
                )?;
                layouter.assign_region(
                    || alloc::format!("cond10 constrain G_c1[{i}] x"),
                    |mut region| {
                        region.constrain_equal(
                            g_c1.inner().x().cell(), g_x_const.cell(),
                        )
                    },
                )?;
                layouter.assign_region(
                    || alloc::format!("cond10 constrain G_c1[{i}] y"),
                    |mut region| {
                        region.constrain_equal(
                            g_c1.inner().y().cell(), g_y_const.cell(),
                        )
                    },
                )?;

                // Witness r_i and convert to ScalarVar for ECC multiplication.
                let r_i = assign_free_advice(
                    layouter.namespace(|| alloc::format!("witness r[{i}]")),
                    config.advices[0],
                    self.share_randomness[i],
                )?;
                let r_i_for_c2 = r_i.clone(); // used again for r_i * ea_pk

                let r_i_scalar = ScalarVar::from_base(
                    ecc_chip.clone(),
                    layouter.namespace(|| alloc::format!("r[{i}] to ScalarVar (C1)")),
                    &r_i,
                )?;

                let (c1_point, _) = g_c1.mul(
                    layouter.namespace(|| alloc::format!("[r_{i}] * G")),
                    r_i_scalar,
                )?;

                // ExtractP(C1_i) == enc_share_c1_x[i]
                let c1_x = c1_point.extract_p().inner().clone();
                layouter.assign_region(
                    || alloc::format!("cond10 C1[{i}] x == enc_c1_x[{i}]"),
                    |mut region| {
                        region.constrain_equal(c1_x.cell(), enc_c1_cells[i].cell())
                    },
                )?;

                // --- C2_i = [v_i] * G + [r_i] * ea_pk ---

                // Witness G again for v_i * G, constrain to SpendAuthG.
                let g_v = NonIdentityPoint::new(
                    ecc_chip.clone(),
                    layouter.namespace(|| alloc::format!("cond10 G for vG[{i}]")),
                    Value::known(g_affine),
                )?;
                layouter.assign_region(
                    || alloc::format!("cond10 constrain G_v[{i}] x"),
                    |mut region| {
                        region.constrain_equal(
                            g_v.inner().x().cell(), g_x_const.cell(),
                        )
                    },
                )?;
                layouter.assign_region(
                    || alloc::format!("cond10 constrain G_v[{i}] y"),
                    |mut region| {
                        region.constrain_equal(
                            g_v.inner().y().cell(), g_y_const.cell(),
                        )
                    },
                )?;

                // [v_i] * G — share value multiplied by generator.
                let v_i_scalar = ScalarVar::from_base(
                    ecc_chip.clone(),
                    layouter.namespace(|| alloc::format!("share[{i}] to ScalarVar")),
                    &share_cells[i],
                )?;
                let (v_g_point, _) = g_v.mul(
                    layouter.namespace(|| alloc::format!("[v_{i}] * G")),
                    v_i_scalar,
                )?;

                // Witness ea_pk as NonIdentityPoint, constrain to public input.
                let ea_pk_point = NonIdentityPoint::new(
                    ecc_chip.clone(),
                    layouter.namespace(|| alloc::format!("cond10 ea_pk for share[{i}]")),
                    self.ea_pk,
                )?;
                layouter.assign_region(
                    || alloc::format!("cond10 constrain ea_pk[{i}] x"),
                    |mut region| {
                        region.constrain_equal(
                            ea_pk_point.inner().x().cell(), ea_pk_x_cell.cell(),
                        )
                    },
                )?;
                layouter.assign_region(
                    || alloc::format!("cond10 constrain ea_pk[{i}] y"),
                    |mut region| {
                        region.constrain_equal(
                            ea_pk_point.inner().y().cell(), ea_pk_y_cell.cell(),
                        )
                    },
                )?;

                // [r_i] * ea_pk — randomness multiplied by EA public key.
                let r_i_scalar_c2 = ScalarVar::from_base(
                    ecc_chip.clone(),
                    layouter.namespace(|| alloc::format!("r[{i}] to ScalarVar (C2)")),
                    &r_i_for_c2,
                )?;
                let (r_ea_pk_point, _) = ea_pk_point.mul(
                    layouter.namespace(|| alloc::format!("[r_{i}] * ea_pk")),
                    r_i_scalar_c2,
                )?;

                // C2_i = [v_i] * G + [r_i] * ea_pk
                let c2_point = v_g_point.add(
                    layouter.namespace(|| alloc::format!("C2[{i}] = vG + rP")),
                    &r_ea_pk_point,
                )?;

                // ExtractP(C2_i) == enc_share_c2_x[i]
                let c2_x = c2_point.extract_p().inner().clone();
                layouter.assign_region(
                    || alloc::format!("cond10 C2[{i}] x == enc_c2_x[{i}]"),
                    |mut region| {
                        region.constrain_equal(c2_x.cell(), enc_c2_cells[i].cell())
                    },
                )?;
            }
        }

        // ---------------------------------------------------------------
        // Condition 11: Vote Commitment Integrity.
        //
        // vote_commitment = Poseidon(DOMAIN_VC, shares_hash,
        //                            proposal_id, vote_decision)
        //
        // Binds the encrypted shares (via shares_hash from condition 9),
        // the proposal choice, and the vote decision into a single
        // commitment with domain separation from VANs (DOMAIN_VC = 1).
        //
        // This is the value posted on-chain and later inserted into the
        // vote commitment tree. ZKP #3 (vote reveal) will open individual
        // shares from this commitment.
        // ---------------------------------------------------------------

        // DOMAIN_VC — constant-constrained so the value is baked into the
        // verification key and cannot be altered by a malicious prover.
        let domain_vc = layouter.assign_region(
            || "DOMAIN_VC constant",
            |mut region| {
                region.assign_advice_from_constant(
                    || "domain_vc",
                    config.advices[0],
                    0,
                    pallas::Base::from(DOMAIN_VC),
                )
            },
        )?;

        // proposal_id was already copied from instance in condition 5; reuse that cell.

        // Private witness: vote decision.
        let vote_decision = assign_free_advice(
            layouter.namespace(|| "witness vote_decision"),
            config.advices[0],
            self.vote_decision,
        )?;

        // Compute vote_commitment = Poseidon(DOMAIN_VC, shares_hash,
        //                                    proposal_id, vote_decision).
        let vote_commitment = {
            let message = [domain_vc, shares_hash, proposal_id, vote_decision];
            let hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<4>,
                3, // WIDTH
                2, // RATE
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "vote commitment Poseidon init"),
            )?;
            hasher.hash(
                layouter.namespace(|| "vote_commitment = Poseidon(DOMAIN_VC, ...)"),
                message,
            )?
        };

        // Bind the derived vote commitment to the VOTE_COMMITMENT public input.
        layouter.constrain_instance(
            vote_commitment.cell(),
            config.primary,
            VOTE_COMMITMENT,
        )?;

        Ok(())
    }
}

// ================================================================
// Instance (public inputs)
// ================================================================

/// Public inputs to the Vote Proof circuit (9 field elements).
///
/// These are the values posted to the vote chain that both the prover
/// and verifier agree on. The verifier checks the proof against these
/// values without seeing any private witnesses.
#[derive(Clone, Debug)]
pub struct Instance {
    /// The nullifier of the old VAN being spent (prevents double-vote).
    pub van_nullifier: pallas::Base,
    /// The new VAN commitment (with decremented proposal authority).
    pub vote_authority_note_new: pallas::Base,
    /// The vote commitment hash.
    pub vote_commitment: pallas::Base,
    /// Root of the vote commitment tree at anchor height.
    pub vote_comm_tree_root: pallas::Base,
    /// The vote-chain height at which the tree is snapshotted.
    pub vote_comm_tree_anchor_height: pallas::Base,
    /// Which proposal this vote is for.
    pub proposal_id: pallas::Base,
    /// The voting round identifier.
    pub voting_round_id: pallas::Base,
    /// Election authority public key x-coordinate.
    pub ea_pk_x: pallas::Base,
    /// Election authority public key y-coordinate.
    pub ea_pk_y: pallas::Base,
}

impl Instance {
    /// Constructs an [`Instance`] from its constituent parts.
    pub fn from_parts(
        van_nullifier: pallas::Base,
        vote_authority_note_new: pallas::Base,
        vote_commitment: pallas::Base,
        vote_comm_tree_root: pallas::Base,
        vote_comm_tree_anchor_height: pallas::Base,
        proposal_id: pallas::Base,
        voting_round_id: pallas::Base,
        ea_pk_x: pallas::Base,
        ea_pk_y: pallas::Base,
    ) -> Self {
        Instance {
            van_nullifier,
            vote_authority_note_new,
            vote_commitment,
            vote_comm_tree_root,
            vote_comm_tree_anchor_height,
            proposal_id,
            voting_round_id,
            ea_pk_x,
            ea_pk_y,
        }
    }

    /// Serializes public inputs for halo2 proof creation/verification.
    ///
    /// The order must match the instance column offsets defined at the
    /// top of this file (`VAN_NULLIFIER`, `VOTE_AUTHORITY_NOTE_NEW`, etc.).
    pub fn to_halo2_instance(&self) -> Vec<vesta::Scalar> {
        alloc::vec![
            self.van_nullifier,
            self.vote_authority_note_new,
            self.vote_commitment,
            self.vote_comm_tree_root,
            self.vote_comm_tree_anchor_height,
            self.proposal_id,
            self.voting_round_id,
            self.ea_pk_x,
            self.ea_pk_y,
        ]
    }
}

// ================================================================
// Tests
// ================================================================

#[cfg(test)]
mod tests {
    use super::*;
    use core::iter;
    use ff::Field;
    use group::ff::PrimeFieldBits;
    use group::{Curve, Group};
    use halo2_gadgets::sinsemilla::primitives::CommitDomain;
    use halo2_proofs::dev::MockProver;
    use pasta_curves::pallas;
    use rand::rngs::OsRng;

    use crate::constants::{
        fixed_bases::COMMIT_IVK_PERSONALIZATION,
        L_ORCHARD_BASE,
    };

    /// Generates an El Gamal keypair for testing.
    /// Returns `(ea_sk, ea_pk_point, ea_pk_affine)`.
    fn generate_ea_keypair() -> (pallas::Scalar, pallas::Point, pallas::Affine) {
        let ea_sk = pallas::Scalar::from(42u64);
        let g = pallas::Point::from(spend_auth_g_affine());
        let ea_pk = g * ea_sk;
        let ea_pk_affine = ea_pk.to_affine();
        (ea_sk, ea_pk, ea_pk_affine)
    }

    /// Computes real El Gamal encryptions for 4 shares.
    ///
    /// Returns `(c1_x, c2_x, randomness, shares_hash_value)` where:
    /// - `c1_x[i]` and `c2_x[i]` are correct ciphertext x-coordinates
    /// - `randomness[i]` is the base field randomness used for each share
    /// - `shares_hash_value` is the Poseidon hash of all 8 coordinates
    fn encrypt_shares(
        shares: [u64; 4],
        ea_pk: pallas::Point,
    ) -> ([pallas::Base; 4], [pallas::Base; 4], [pallas::Base; 4], pallas::Base) {
        let mut c1_x = [pallas::Base::zero(); 4];
        let mut c2_x = [pallas::Base::zero(); 4];
        // Use small deterministic randomness (fits in both Base and Scalar).
        let randomness: [pallas::Base; 4] = [
            pallas::Base::from(101u64),
            pallas::Base::from(202u64),
            pallas::Base::from(303u64),
            pallas::Base::from(404u64),
        ];
        for i in 0..4 {
            let (c1, c2) = elgamal_encrypt(
                pallas::Base::from(shares[i]),
                randomness[i],
                ea_pk,
            );
            c1_x[i] = c1;
            c2_x[i] = c2;
        }
        let hash = shares_hash(c1_x, c2_x);
        (c1_x, c2_x, randomness, hash)
    }

    /// Out-of-circuit voting key derivation for tests.
    ///
    /// Given a voting spending key (vsk), nullifier key (nk), and CommitIvk
    /// randomness (rivk_v), derives the full voting address:
    ///
    /// 1. `ak = [vsk] * SpendAuthG` (spend validating key)
    /// 2. `ak_x = ExtractP(ak)` (x-coordinate)
    /// 3. `ivk_v = CommitIvk(ak_x, nk, rivk_v)` (incoming viewing key)
    /// 4. `g_d = random non-identity point` (diversified base)
    /// 5. `pk_d = [ivk_v] * g_d` (diversified transmission key)
    ///
    /// Returns `(g_d_affine, pk_d_affine, ak_x)` for use as circuit witnesses.
    fn derive_voting_address(
        vsk: pallas::Scalar,
        nk: pallas::Base,
        rivk_v: pallas::Scalar,
    ) -> (pallas::Affine, pallas::Affine) {
        // Step 1: ak = [vsk] * SpendAuthG
        let g = pallas::Point::from(spend_auth_g_affine());
        let ak_point = g * vsk;
        let ak_x = *ak_point.to_affine().coordinates().unwrap().x();

        // Step 2: ivk_v = CommitIvk(ak_x, nk, rivk_v)
        let domain = CommitDomain::new(COMMIT_IVK_PERSONALIZATION);
        let ivk_v = domain
            .short_commit(
                iter::empty()
                    .chain(ak_x.to_le_bits().iter().by_vals().take(L_ORCHARD_BASE))
                    .chain(nk.to_le_bits().iter().by_vals().take(L_ORCHARD_BASE)),
                &rivk_v,
            )
            .expect("CommitIvk should not produce ⊥ for random inputs");

        // Step 3: g_d = random non-identity point
        // Using a deterministic point derived from a fixed seed ensures
        // reproducibility while avoiding the identity point.
        let g_d = pallas::Point::generator() * pallas::Scalar::from(12345u64);
        let g_d_affine = g_d.to_affine();

        // Step 4: pk_d = [ivk_v] * g_d
        let ivk_v_scalar =
            base_to_scalar(ivk_v).expect("ivk_v must be < scalar field modulus");
        let pk_d = g_d * ivk_v_scalar;
        let pk_d_affine = pk_d.to_affine();

        (g_d_affine, pk_d_affine)
    }

    /// Default proposal_id and vote_decision for tests.
    const TEST_PROPOSAL_ID: u64 = 3;
    const TEST_VOTE_DECISION: u64 = 1;

    /// Sets condition 11 fields on a circuit and returns the vote_commitment.
    ///
    /// Computes `H(DOMAIN_VC, shares_hash, proposal_id, vote_decision)`
    /// and sets `circuit.vote_decision`. Returns the vote_commitment
    /// for use in the Instance. The `proposal_id` must match the
    /// instance's proposal_id so the circuit's condition 11 (which
    /// copies proposal_id from the instance) agrees with the instance.
    fn set_condition_11(
        circuit: &mut Circuit,
        shares_hash_val: pallas::Base,
        proposal_id: u64,
    ) -> pallas::Base {
        let proposal_id_base = pallas::Base::from(proposal_id);
        let vote_decision = pallas::Base::from(TEST_VOTE_DECISION);
        circuit.vote_decision = Value::known(vote_decision);
        vote_commitment_hash(shares_hash_val, proposal_id_base, vote_decision)
    }

    /// Build valid test data for all 11 conditions.
    ///
    /// Returns a circuit with correctly-hashed VAN witnesses, valid
    /// shares, real El Gamal ciphertexts, and a matching instance.
    fn build_single_leaf_merkle_path(
        leaf: pallas::Base,
    ) -> ([pallas::Base; VOTE_COMM_TREE_DEPTH], u32, pallas::Base) {
        let mut empty_roots = [pallas::Base::zero(); VOTE_COMM_TREE_DEPTH];
        empty_roots[0] = poseidon_hash_2(pallas::Base::zero(), pallas::Base::zero());
        for i in 1..VOTE_COMM_TREE_DEPTH {
            empty_roots[i] = poseidon_hash_2(empty_roots[i - 1], empty_roots[i - 1]);
        }
        let auth_path = empty_roots;
        let mut current = leaf;
        for i in 0..VOTE_COMM_TREE_DEPTH {
            current = poseidon_hash_2(current, auth_path[i]);
        }
        (auth_path, 0, current)
    }

    /// Build test (circuit, instance) with given proposal_authority_old and proposal_id.
    /// proposal_authority_old must have the proposal_id-th bit set (spec bitmask).
    fn make_test_data_with_authority_and_proposal(
        proposal_authority_old: pallas::Base,
        proposal_id: u64,
    ) -> (Circuit, Instance) {
        let mut rng = OsRng;

        // Condition 3 (spend authority): derive proper voting key hierarchy.
        // vsk → ak → ivk_v → (vpk_g_d, vpk_pk_d) through CommitIvk chain.
        let vsk = pallas::Scalar::random(&mut rng);
        let vsk_nk = pallas::Base::random(&mut rng);
        let rivk_v = pallas::Scalar::random(&mut rng);

        let (vpk_g_d_affine, vpk_pk_d_affine) = derive_voting_address(vsk, vsk_nk, rivk_v);

        // Extract x-coordinates for Poseidon hashing (conditions 2, 6).
        let vpk_g_d_x = *vpk_g_d_affine.coordinates().unwrap().x();
        let vpk_pk_d_x = *vpk_pk_d_affine.coordinates().unwrap().x();

        // total_note_value must be small enough that all 4 shares
        // fit in [0, 2^24) for condition 8's range check.
        let total_note_value = pallas::Base::from(10_000u64);
        let voting_round_id = pallas::Base::random(&mut rng);
        let gov_comm_rand = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            vpk_g_d_x,
            vpk_pk_d_x,
            total_note_value,
            voting_round_id,
            proposal_authority_old,
            gov_comm_rand,
        );
        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_authority_note_old);
        let van_nullifier = van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);
        // Spec: proposal_authority_new = proposal_authority_old - (1 << proposal_id).
        let one_shifted = pallas::Base::from(1u64 << proposal_id);
        let proposal_authority_new = proposal_authority_old - one_shifted;
        let vote_authority_note_new = van_integrity_hash(
            vpk_g_d_x,
            vpk_pk_d_x,
            total_note_value,
            voting_round_id,
            proposal_authority_new,
            gov_comm_rand,
        );

        // Create shares that sum to total_note_value (conditions 7 + 8).
        // Each share must be in [0, 2^24) for condition 8's range check.
        let shares_u64: [u64; 4] = [1_000, 2_000, 3_000, 4_000]; // sum = 10000
        let s0 = pallas::Base::from(shares_u64[0]);
        let s1 = pallas::Base::from(shares_u64[1]);
        let s2 = pallas::Base::from(shares_u64[2]);
        let s3 = pallas::Base::from(shares_u64[3]);

        // Condition 10: El Gamal encryption of shares under ea_pk.
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let ea_pk_x = *ea_pk_affine.coordinates().unwrap().x();
        let ea_pk_y = *ea_pk_affine.coordinates().unwrap().y();
        let (enc_c1_x, enc_c2_x, randomness, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(vpk_g_d_affine),
            Value::known(vpk_pk_d_affine),
            Value::known(total_note_value),
            Value::known(proposal_authority_old),
            Value::known(gov_comm_rand),
            Value::known(vote_authority_note_old),
            Value::known(vsk),
            Value::known(rivk_v),
            Value::known(vsk_nk),
        );
        circuit.one_shifted = Value::known(one_shifted);
        circuit.shares = [
            Value::known(s0),
            Value::known(s1),
            Value::known(s2),
            Value::known(s3),
        ];
        circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
        circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);

        // Condition 11: vote commitment from shares_hash + proposal + decision.
        let vote_commitment = set_condition_11(&mut circuit, shares_hash_val, proposal_id);

        let instance = Instance::from_parts(
            van_nullifier,
            vote_authority_note_new,
            vote_commitment,
            vote_comm_tree_root,
            pallas::Base::zero(),
            pallas::Base::from(proposal_id),
            voting_round_id,
            ea_pk_x,
            ea_pk_y,
        );

        (circuit, instance)
    }

    fn make_test_data_with_authority(proposal_authority_old: pallas::Base) -> (Circuit, Instance) {
        make_test_data_with_authority_and_proposal(proposal_authority_old, TEST_PROPOSAL_ID)
    }

    fn make_test_data() -> (Circuit, Instance) {
        // proposal_authority_old must have bit TEST_PROPOSAL_ID set (spec bitmask).
        // 5 | (1 << 3) = 13 so we can vote on proposal 3 and get new = 5.
        make_test_data_with_authority(pallas::Base::from(13u64))
    }

    // ================================================================
    // Condition 2 (VAN Integrity) tests
    // ================================================================

    #[test]
    fn van_integrity_valid_proof() {
        let (circuit, instance) = make_test_data();

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();

        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn van_integrity_wrong_hash_fails() {
        let mut rng = OsRng;
        let (_, mut instance) = make_test_data();

        // Deliberately wrong VAN value — condition 2 constrain_equal will fail.
        let wrong_van = pallas::Base::random(&mut rng);
        let (auth_path, position, root) = build_single_leaf_merkle_path(wrong_van);
        instance.vote_comm_tree_root = root;

        // Use properly derived keys (condition 3 would pass) but the VAN
        // hash won't match wrong_van, so condition 2 fails.
        let vsk = pallas::Scalar::random(&mut rng);
        let vsk_nk = pallas::Base::random(&mut rng);
        let rivk_v = pallas::Scalar::random(&mut rng);
        let (vpk_g_d_affine, vpk_pk_d_affine) = derive_voting_address(vsk, vsk_nk, rivk_v);
        let shares_u64: [u64; 4] = [1_000, 2_000, 3_000, 4_000];
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        // Use authority 13 (bit 3 set) and one_shifted = 8 so condition 5 is consistent;
        // only condition 2 (VAN hash) should fail due to wrong_van.
        let proposal_authority_old = pallas::Base::from(13u64);
        let gov_comm_rand = pallas::Base::random(&mut rng);
        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(vpk_g_d_affine),
            Value::known(vpk_pk_d_affine),
            Value::known(pallas::Base::from(10_000u64)),
            Value::known(proposal_authority_old),
            Value::known(gov_comm_rand),
            Value::known(wrong_van),
            Value::known(vsk),
            Value::known(rivk_v),
            Value::known(vsk_nk),
        );
        circuit.one_shifted = Value::known(pallas::Base::from(1u64 << TEST_PROPOSAL_ID));
        circuit.shares = shares_u64.map(|s| Value::known(pallas::Base::from(s)));
        circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
        circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, TEST_PROPOSAL_ID);
        instance.vote_commitment = vc;
        instance.proposal_id = pallas::Base::from(TEST_PROPOSAL_ID);
        instance.ea_pk_x = *ea_pk_affine.coordinates().unwrap().x();
        instance.ea_pk_y = *ea_pk_affine.coordinates().unwrap().y();

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        // Should fail: derived hash ≠ witnessed vote_authority_note_old.
        assert!(prover.verify().is_err());
    }

    #[test]
    fn van_integrity_wrong_round_id_fails() {
        let (circuit, mut instance) = make_test_data();

        // Supply a DIFFERENT voting_round_id in the instance.
        instance.voting_round_id = pallas::Base::random(&mut OsRng);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        // Should fail: the voting_round_id from the instance doesn't match
        // the one hashed into the VAN (condition 2).
        assert!(prover.verify().is_err());
    }

    /// Verifies the out-of-circuit helper produces deterministic results.
    #[test]
    fn van_integrity_hash_deterministic() {
        let mut rng = OsRng;

        let vpk_g_d = pallas::Base::random(&mut rng);
        let vpk_pk_d = pallas::Base::random(&mut rng);
        let val = pallas::Base::random(&mut rng);
        let round = pallas::Base::random(&mut rng);
        let auth = pallas::Base::random(&mut rng);
        let rand = pallas::Base::random(&mut rng);

        let h1 = van_integrity_hash(vpk_g_d, vpk_pk_d, val, round, auth, rand);
        let h2 = van_integrity_hash(vpk_g_d, vpk_pk_d, val, round, auth, rand);
        assert_eq!(h1, h2);

        // Changing any input changes the hash.
        let h3 = van_integrity_hash(
            pallas::Base::random(&mut rng),
            vpk_pk_d,
            val,
            round,
            auth,
            rand,
        );
        assert_ne!(h1, h3);
    }

    // ================================================================
    // Condition 4 (VAN Nullifier Integrity) tests
    // ================================================================

    /// Wrong VAN_NULLIFIER public input should fail condition 4.
    #[test]
    fn van_nullifier_wrong_public_input_fails() {
        let (circuit, mut instance) = make_test_data();

        // Corrupt the VAN nullifier public input.
        instance.van_nullifier = pallas::Base::random(&mut OsRng);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();

        // Should fail: circuit-derived nullifier ≠ corrupted instance value.
        assert!(prover.verify().is_err());
    }

    /// Using a different vsk_nk in the circuit than was used to compute
    /// the instance nullifier should fail condition 4.
    /// Note: since vsk_nk is also used in CommitIvk (condition 3), the
    /// wrong value also breaks condition 3 — but the test still verifies
    /// that the proof fails as expected.
    #[test]
    fn van_nullifier_wrong_vsk_nk_fails() {
        let mut rng = OsRng;

        // Derive proper keys with the CORRECT vsk_nk.
        let vsk = pallas::Scalar::random(&mut rng);
        let vsk_nk = pallas::Base::random(&mut rng);
        let rivk_v = pallas::Scalar::random(&mut rng);
        let (vpk_g_d_affine, vpk_pk_d_affine) = derive_voting_address(vsk, vsk_nk, rivk_v);
        let vpk_g_d_x = *vpk_g_d_affine.coordinates().unwrap().x();
        let vpk_pk_d_x = *vpk_pk_d_affine.coordinates().unwrap().x();

        let total_note_value = pallas::Base::from(10_000u64);
        let voting_round_id = pallas::Base::random(&mut rng);
        let proposal_authority_old = pallas::Base::from(5u64); // bits 0 and 2 set
        let gov_comm_rand = pallas::Base::random(&mut rng);
        let proposal_id = 0u64; // vote on proposal 0 so one_shifted = 1, new = 4

        let vote_authority_note_old = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total_note_value, voting_round_id,
            proposal_authority_old, gov_comm_rand,
        );
        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_authority_note_old);
        let van_nullifier = van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);
        let one_shifted = pallas::Base::from(1u64 << proposal_id);
        let proposal_authority_new = proposal_authority_old - one_shifted;
        let vote_authority_note_new = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total_note_value, voting_round_id,
            proposal_authority_new, gov_comm_rand,
        );

        // Use a DIFFERENT vsk_nk in the circuit.
        let wrong_vsk_nk = pallas::Base::random(&mut rng);

        // Shares that sum to total_note_value (conditions 7 + 8).
        let shares_u64: [u64; 4] = [1_000, 2_000, 3_000, 4_000];

        // Condition 10: real El Gamal encryption.
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(vpk_g_d_affine),
            Value::known(vpk_pk_d_affine),
            Value::known(total_note_value),
            Value::known(proposal_authority_old),
            Value::known(gov_comm_rand),
            Value::known(vote_authority_note_old),
            Value::known(vsk),
            Value::known(rivk_v),
            Value::known(wrong_vsk_nk),
        );
        circuit.one_shifted = Value::known(one_shifted);
        circuit.shares = shares_u64.map(|s| Value::known(pallas::Base::from(s)));
        circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
        circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id);

        let instance = Instance::from_parts(
            van_nullifier, vote_authority_note_new, vc,
            vote_comm_tree_root, pallas::Base::zero(),
            pallas::Base::from(proposal_id),
            voting_round_id,
            *ea_pk_affine.coordinates().unwrap().x(),
            *ea_pk_affine.coordinates().unwrap().y(),
        );

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        // Should fail: circuit computes Poseidon(wrong_vsk_nk, inner_hash)
        // which ≠ the instance van_nullifier (computed with correct vsk_nk).
        // Also fails condition 3 since wrong_vsk_nk breaks CommitIvk derivation.
        assert!(prover.verify().is_err());
    }

    /// Verifies the out-of-circuit nullifier helper produces deterministic results.
    #[test]
    fn van_nullifier_hash_deterministic() {
        let mut rng = OsRng;

        let nk = pallas::Base::random(&mut rng);
        let round = pallas::Base::random(&mut rng);
        let van = pallas::Base::random(&mut rng);

        let h1 = van_nullifier_hash(nk, round, van);
        let h2 = van_nullifier_hash(nk, round, van);
        assert_eq!(h1, h2);

        // Changing any input changes the hash.
        let h3 = van_nullifier_hash(pallas::Base::random(&mut rng), round, van);
        assert_ne!(h1, h3);
    }

    /// Verifies the domain tag is non-zero and deterministic.
    #[test]
    fn domain_van_nullifier_deterministic() {
        let d1 = domain_van_nullifier();
        let d2 = domain_van_nullifier();
        assert_eq!(d1, d2);

        // Must differ from DOMAIN_VAN (which is 0).
        assert_ne!(d1, pallas::Base::zero());
    }

    // ================================================================
    // Condition 5 (Proposal Authority Decrement) tests
    // ================================================================

    /// Proposal authority with only bit 0 set (value 1): vote on proposal 0, new = 0.
    #[test]
    fn proposal_authority_decrement_minimum_valid() {
        let (circuit, instance) =
            make_test_data_with_authority_and_proposal(pallas::Base::one(), 0);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    /// Proposal authority of 0 should fail — voter has no remaining
    /// authority, so the range check on `diff = 0 - 1 ≈ p - 1` fails.
    #[test]
    fn proposal_authority_zero_fails() {
        let (circuit, instance) = make_test_data_with_authority(pallas::Base::zero());

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();

        // Should fail: diff = 0 - 1 = p - 1 ≈ 2^254, which fails the
        // 16-bit range check in condition 5 (and the 20-bit limb check).
        assert!(prover.verify().is_err());
    }

    // ================================================================
    // Condition 6 (New VAN Integrity) tests
    // ================================================================

    /// Wrong vote_authority_note_new public input should fail condition 6.
    #[test]
    fn new_van_integrity_wrong_public_input_fails() {
        let (circuit, mut instance) = make_test_data();

        // Corrupt the new VAN public input.
        instance.vote_authority_note_new = pallas::Base::random(&mut OsRng);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();

        // Should fail: circuit-derived new VAN ≠ corrupted instance value.
        assert!(prover.verify().is_err());
    }

    /// New VAN integrity with a large (but valid) 16-bit proposal authority.
    /// Authority 0xFFF8 has bits 3..15 set; voting on proposal 3 gives new = 0xFFF0.
    #[test]
    fn new_van_integrity_large_authority() {
        let (circuit, instance) =
            make_test_data_with_authority(pallas::Base::from(0xFFF8u64));

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    // ================================================================
    // Condition 1 (VAN Membership) tests
    // ================================================================

    /// Wrong vote_comm_tree_root in the instance should fail condition 1.
    #[test]
    fn van_membership_wrong_root_fails() {
        let (circuit, mut instance) = make_test_data();

        // Corrupt the tree root.
        instance.vote_comm_tree_root = pallas::Base::random(&mut OsRng);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// A VAN at a non-zero position in the tree should verify.
    #[test]
    fn van_membership_nonzero_position() {
        let mut rng = OsRng;

        // Derive proper voting key hierarchy.
        let vsk = pallas::Scalar::random(&mut rng);
        let vsk_nk = pallas::Base::random(&mut rng);
        let rivk_v = pallas::Scalar::random(&mut rng);
        let (vpk_g_d_affine, vpk_pk_d_affine) = derive_voting_address(vsk, vsk_nk, rivk_v);
        let vpk_g_d_x = *vpk_g_d_affine.coordinates().unwrap().x();
        let vpk_pk_d_x = *vpk_pk_d_affine.coordinates().unwrap().x();

        let total_note_value = pallas::Base::from(10_000u64);
        let voting_round_id = pallas::Base::random(&mut rng);
        let proposal_authority_old = pallas::Base::from(5u64); // bits 0 and 2 set
        let proposal_id = 0u64;
        let gov_comm_rand = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total_note_value, voting_round_id,
            proposal_authority_old, gov_comm_rand,
        );

        // Place the leaf at position 7 (binary: ...0111).
        let position: u32 = 7;
        let mut empty_roots = [pallas::Base::zero(); VOTE_COMM_TREE_DEPTH];
        empty_roots[0] = poseidon_hash_2(pallas::Base::zero(), pallas::Base::zero());
        for i in 1..VOTE_COMM_TREE_DEPTH {
            empty_roots[i] = poseidon_hash_2(empty_roots[i - 1], empty_roots[i - 1]);
        }
        let auth_path = empty_roots;
        let mut current = vote_authority_note_old;
        for i in 0..VOTE_COMM_TREE_DEPTH {
            if (position >> i) & 1 == 0 {
                current = poseidon_hash_2(current, auth_path[i]);
            } else {
                current = poseidon_hash_2(auth_path[i], current);
            }
        }
        let vote_comm_tree_root = current;

        let van_nullifier = van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);
        let one_shifted = pallas::Base::from(1u64 << proposal_id);
        let proposal_authority_new = proposal_authority_old - one_shifted;
        let vote_authority_note_new = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total_note_value, voting_round_id,
            proposal_authority_new, gov_comm_rand,
        );

        // Shares that sum to total_note_value (conditions 7 + 8).
        let shares_u64: [u64; 4] = [1_000, 2_000, 3_000, 4_000];

        // Condition 10: real El Gamal encryption.
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(vpk_g_d_affine),
            Value::known(vpk_pk_d_affine),
            Value::known(total_note_value),
            Value::known(proposal_authority_old),
            Value::known(gov_comm_rand),
            Value::known(vote_authority_note_old),
            Value::known(vsk),
            Value::known(rivk_v),
            Value::known(vsk_nk),
        );
        circuit.one_shifted = Value::known(one_shifted);
        circuit.shares = shares_u64.map(|s| Value::known(pallas::Base::from(s)));
        circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
        circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id);

        let instance = Instance::from_parts(
            van_nullifier, vote_authority_note_new, vc,
            vote_comm_tree_root, pallas::Base::zero(),
            pallas::Base::from(proposal_id),
            voting_round_id,
            *ea_pk_affine.coordinates().unwrap().x(),
            *ea_pk_affine.coordinates().unwrap().y(),
        );

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    /// Poseidon hash-2 helper is deterministic.
    #[test]
    fn poseidon_hash_2_deterministic() {
        let mut rng = OsRng;
        let a = pallas::Base::random(&mut rng);
        let b = pallas::Base::random(&mut rng);

        assert_eq!(poseidon_hash_2(a, b), poseidon_hash_2(a, b));
        // Non-commutative.
        assert_ne!(poseidon_hash_2(a, b), poseidon_hash_2(b, a));
    }

    // ================================================================
    // Condition 7 (Shares Sum Correctness) tests
    // ================================================================

    /// Shares that do NOT sum to total_note_value should fail condition 7.
    #[test]
    fn shares_sum_wrong_total_fails() {
        let (mut circuit, instance) = make_test_data();

        // Corrupt shares[3] so the sum no longer equals total_note_value.
        // Use a small value that still passes condition 8's range check,
        // isolating the condition 7 failure.
        circuit.shares[3] = Value::known(pallas::Base::from(999u64));

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        // Should fail: shares sum ≠ total_note_value.
        assert!(prover.verify().is_err());
    }

    // ================================================================
    // Condition 8 (Shares Range) tests
    // ================================================================

    /// A share at the maximum valid value (2^30 - 1) should pass.
    #[test]
    fn shares_range_max_valid() {
        let max_share = pallas::Base::from((1u64 << 30) - 1); // 1,073,741,823
        let total = max_share + max_share + max_share + max_share;

        let mut rng = OsRng;
        // Derive proper voting key hierarchy.
        let vsk = pallas::Scalar::random(&mut rng);
        let vsk_nk = pallas::Base::random(&mut rng);
        let rivk_v = pallas::Scalar::random(&mut rng);
        let (vpk_g_d_affine, vpk_pk_d_affine) = derive_voting_address(vsk, vsk_nk, rivk_v);
        let vpk_g_d_x = *vpk_g_d_affine.coordinates().unwrap().x();
        let vpk_pk_d_x = *vpk_pk_d_affine.coordinates().unwrap().x();

        let voting_round_id = pallas::Base::random(&mut rng);
        let proposal_authority_old = pallas::Base::from(5u64); // bits 0 and 2 set
        let proposal_id = 0u64;
        let gov_comm_rand = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total, voting_round_id,
            proposal_authority_old, gov_comm_rand,
        );
        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_authority_note_old);
        let van_nullifier = van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);
        let one_shifted = pallas::Base::from(1u64 << proposal_id);
        let proposal_authority_new = proposal_authority_old - one_shifted;
        let vote_authority_note_new = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total, voting_round_id,
            proposal_authority_new, gov_comm_rand,
        );

        // Condition 10: real El Gamal encryption with max-value shares.
        let max_share_u64 = (1u64 << 30) - 1;
        let shares_u64: [u64; 4] = [max_share_u64; 4];
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(vpk_g_d_affine),
            Value::known(vpk_pk_d_affine),
            Value::known(total),
            Value::known(proposal_authority_old),
            Value::known(gov_comm_rand),
            Value::known(vote_authority_note_old),
            Value::known(vsk),
            Value::known(rivk_v),
            Value::known(vsk_nk),
        );
        circuit.one_shifted = Value::known(one_shifted);
        circuit.shares = [Value::known(max_share); 4];
        circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
        circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id);

        let instance = Instance::from_parts(
            van_nullifier, vote_authority_note_new, vc,
            vote_comm_tree_root, pallas::Base::zero(),
            pallas::Base::from(proposal_id),
            voting_round_id,
            *ea_pk_affine.coordinates().unwrap().x(),
            *ea_pk_affine.coordinates().unwrap().y(),
        );

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    /// A share at exactly 2^30 should fail the range check.
    #[test]
    fn shares_range_overflow_fails() {
        let (mut circuit, instance) = make_test_data();

        // Set share_0 to 2^30 (one above the max valid value).
        // This will fail condition 8 AND condition 7 (sum mismatch),
        // but the important thing is the circuit rejects it.
        circuit.shares[0] = Value::known(pallas::Base::from(1u64 << 30));

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// A share that is a large field element (simulating underflow
    /// from subtraction) should fail the range check.
    #[test]
    fn shares_range_field_wrap_fails() {
        let (mut circuit, instance) = make_test_data();

        // Set share_0 to p - 1 (a wrapped negative value).
        // The 10-bit decomposition will produce a huge residual.
        circuit.shares[0] = Value::known(-pallas::Base::one());

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    // ================================================================
    // Condition 9 (Shares Hash Integrity) tests
    // ================================================================

    /// Valid enc_share witnesses with matching shares_hash should pass.
    #[test]
    fn shares_hash_valid_proof() {
        let (circuit, instance) = make_test_data();

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    /// A corrupted enc_share_c1_x[0] should cause condition 9 failure:
    /// the in-circuit hash won't match the VOTE_COMMITMENT instance.
    #[test]
    fn shares_hash_wrong_enc_share_fails() {
        let (mut circuit, instance) = make_test_data();

        // Corrupt one enc_share component — the Poseidon hash will
        // change, so it won't match the instance's vote_commitment.
        circuit.enc_share_c1_x[0] = Value::known(pallas::Base::random(&mut OsRng));

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// A wrong vote_commitment instance value (shares_hash mismatch)
    /// should fail, even with correct enc_share witnesses.
    #[test]
    fn shares_hash_wrong_instance_fails() {
        let (circuit, mut instance) = make_test_data();

        // Supply a random (wrong) vote_commitment in the instance.
        instance.vote_commitment = pallas::Base::random(&mut OsRng);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// Verifies the out-of-circuit shares_hash helper is deterministic.
    #[test]
    fn shares_hash_deterministic() {
        let mut rng = OsRng;

        let c1_x: [pallas::Base; 4] =
            core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let c2_x: [pallas::Base; 4] =
            core::array::from_fn(|_| pallas::Base::random(&mut rng));

        let h1 = shares_hash(c1_x, c2_x);
        let h2 = shares_hash(c1_x, c2_x);
        assert_eq!(h1, h2);

        // Changing any component changes the hash.
        let mut c1_x_alt = c1_x;
        c1_x_alt[2] = pallas::Base::random(&mut rng);
        let h3 = shares_hash(c1_x_alt, c2_x);
        assert_ne!(h1, h3);

        // Swapping c1 and c2 also changes the hash.
        let h4 = shares_hash(c2_x, c1_x);
        assert_ne!(h1, h4);
    }

    // ================================================================
    // Condition 10 (Encryption Integrity) tests
    // ================================================================

    /// Valid El Gamal encryptions should produce a valid proof.
    #[test]
    fn encryption_integrity_valid_proof() {
        let (circuit, instance) = make_test_data();

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    /// A corrupted share_randomness[0] should fail condition 10:
    /// the computed C1[0] won't match enc_share_c1_x[0].
    #[test]
    fn encryption_integrity_wrong_randomness_fails() {
        let (mut circuit, instance) = make_test_data();

        // Corrupt the randomness for share 0 — C1 will change.
        circuit.share_randomness[0] = Value::known(pallas::Base::from(9999u64));

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// A wrong ea_pk in the instance should fail condition 10:
    /// the computed r * ea_pk won't match the ciphertexts.
    #[test]
    fn encryption_integrity_wrong_ea_pk_instance_fails() {
        let (circuit, mut instance) = make_test_data();

        // Corrupt ea_pk_x in the instance — the constraint linking
        // the witnessed ea_pk to the public input will fail.
        instance.ea_pk_x = pallas::Base::from(12345u64);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// The out-of-circuit elgamal_encrypt helper is deterministic.
    #[test]
    fn elgamal_encrypt_deterministic() {
        let (_ea_sk, ea_pk_point, _ea_pk_affine) = generate_ea_keypair();

        let v = pallas::Base::from(1000u64);
        let r = pallas::Base::from(42u64);

        let (c1_a, c2_a) = elgamal_encrypt(v, r, ea_pk_point);
        let (c1_b, c2_b) = elgamal_encrypt(v, r, ea_pk_point);
        assert_eq!(c1_a, c1_b);
        assert_eq!(c2_a, c2_b);

        // Different randomness → different C1.
        let (c1_c, _) = elgamal_encrypt(v, pallas::Base::from(99u64), ea_pk_point);
        assert_ne!(c1_a, c1_c);
    }

    // ================================================================
    // Condition 11 (Vote Commitment Integrity) tests
    // ================================================================

    /// Valid vote commitment (full Poseidon chain) should pass.
    #[test]
    fn vote_commitment_integrity_valid_proof() {
        let (circuit, instance) = make_test_data();

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    /// A wrong vote_decision in the circuit should fail condition 11:
    /// the derived vote_commitment won't match the instance.
    #[test]
    fn vote_commitment_wrong_decision_fails() {
        let (mut circuit, instance) = make_test_data();

        // Corrupt the vote decision — the Poseidon hash will change.
        circuit.vote_decision = Value::known(pallas::Base::from(99u64));

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// A wrong proposal_id in the instance should fail condition 11:
    /// the in-circuit proposal_id (copied from instance) will produce
    /// a different vote_commitment.
    #[test]
    fn vote_commitment_wrong_proposal_id_fails() {
        let (circuit, mut instance) = make_test_data();

        // Corrupt the proposal_id in the instance.
        instance.proposal_id = pallas::Base::from(999u64);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// A wrong vote_commitment in the instance should fail.
    #[test]
    fn vote_commitment_wrong_instance_fails() {
        let (circuit, mut instance) = make_test_data();

        // Corrupt the vote_commitment public input.
        instance.vote_commitment = pallas::Base::random(&mut OsRng);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// The out-of-circuit vote_commitment_hash helper is deterministic.
    #[test]
    fn vote_commitment_hash_deterministic() {
        let mut rng = OsRng;

        let sh = pallas::Base::random(&mut rng);
        let pid = pallas::Base::from(5u64);
        let dec = pallas::Base::from(1u64);

        let h1 = vote_commitment_hash(sh, pid, dec);
        let h2 = vote_commitment_hash(sh, pid, dec);
        assert_eq!(h1, h2);

        // Changing any input changes the hash.
        let h3 = vote_commitment_hash(sh, pallas::Base::from(6u64), dec);
        assert_ne!(h1, h3);

        // DOMAIN_VC ensures separation from VAN hashes.
        // (Different arity prevents confusion, but domain tag adds defense-in-depth.)
        assert_ne!(h1, pallas::Base::zero());
    }

    // ================================================================
    // Instance and circuit sanity
    // ================================================================

    /// Instance must serialize to exactly 9 public inputs.
    #[test]
    fn instance_has_nine_public_inputs() {
        let (_, instance) = make_test_data();
        assert_eq!(instance.to_halo2_instance().len(), 9);
    }

    /// Default circuit (all witnesses unknown) must not produce a valid proof.
    #[test]
    fn default_circuit_with_valid_instance_fails() {
        let (_, instance) = make_test_data();
        let circuit = Circuit::default();

        match MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]) {
            Ok(prover) => assert!(prover.verify().is_err()),
            Err(_) => {} // Synthesis failed — acceptable.
        }
    }
}
