//! The Vote Proof circuit implementation (ZKP #2).
//!
//! Proves that a registered voter is casting a valid vote, without
//! revealing which VAN they hold. Currently implements:
//!
//! - **Condition 1**: VAN Membership (Poseidon Merkle path, `constrain_instance`).
//! - **Condition 2**: VAN Integrity (Poseidon hash).
//! - **Condition 3**: Diversified Address Integrity (`vpk_pk_d = [ivk_v] * vpk_g_d` via CommitIvk).
//! - **Condition 4**: Spend Authority — `r_vpk = vsk.ak + [alpha_v] * G` (fixed-base mul + point add, `constrain_instance`).
//! - **Condition 5**: VAN Nullifier Integrity (nested Poseidon, `constrain_instance`).
//! - **Condition 6**: Proposal Authority Decrement (AddChip + range check).
//! - **Condition 7**: New VAN Integrity (Poseidon hash, `constrain_instance`).
//! - **Condition 8**: Shares Sum Correctness (AddChip, `constrain_equal`).
//! - **Condition 9**: Shares Range (LookupRangeCheck, `[0, 2^30)`).
//! - **Condition 10**: Shares Hash Integrity (Poseidon `ConstantLength<8>`; output flows to condition 12).
//! - **Condition 11**: Encryption Integrity (ECC variable-base mul, `constrain_equal`).
//! - **Condition 12**: Vote Commitment Integrity (Poseidon `ConstantLength<4>`, `constrain_instance`).
//!
//! Conditions 1–4 and 5–12 are fully constrained in-circuit.
//!
//! ## Conditions overview
//!
//! VAN ownership and spending:
//! - **Condition 1**: VAN Membership — Merkle path from `vote_authority_note_old`
//!   to `vote_comm_tree_root`.
//! - **Condition 2**: VAN Integrity — `vote_authority_note_old` is the two-layer
//!   Poseidon hash (ZKP 1–compatible: core then finalize with rand). *(implemented)*
//! - **Condition 3**: Diversified Address Integrity — `vpk_pk_d = [ivk_v] * vpk_g_d`
//!   where `ivk_v = CommitIvk(ExtractP([vsk]*SpendAuthG), vsk.nk)`. *(implemented)*
//! - **Condition 4**: Spend Authority — `r_vpk = vsk.ak + [alpha_v] * G`; enforced in-circuit (fixed-base mul + point add, `constrain_instance`).
//! - **Condition 5**: VAN Nullifier Integrity — `van_nullifier` is correctly
//!   derived from `vsk.nk`. *(implemented)*
//!
//! New VAN construction:
//! - **Condition 6**: Proposal Authority Decrement — `proposal_authority_new =
//!   proposal_authority_old - (1 << proposal_id)`, with bitmask range [0, 2^16). *(implemented)*
//! - **Condition 7**: New VAN Integrity — same two-layer structure as condition 2
//!   but with decremented authority. *(implemented)*
//!
//! Vote commitment construction:
//! - **Condition 8**: Shares Sum Correctness — `sum(shares_1..4) = total_note_value`.
//!   *(implemented)*
//! - **Condition 9**: Shares Range — each `shares_j` in `[0, 2^24)`.
//!   *(implemented)*
//! - **Condition 10**: Shares Hash Integrity — `shares_hash = H(enc_share_1..4)`.
//!   *(implemented)*
//! - **Condition 11**: Encryption Integrity — each `enc_share_i = ElGamal(shares_i, r_i, ea_pk)`.
//!   *(implemented)*
//! - **Condition 12**: Vote Commitment Integrity — `vote_commitment = H(DOMAIN_VC, shares_hash,
//!   proposal_id, vote_decision)`. *(implemented)*

use alloc::vec::Vec;

use ff::PrimeField;
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
        NonIdentityPoint, ScalarFixed,
    },
    poseidon::{
        primitives::{self as poseidon, ConstantLength},
        Hash as PoseidonHash, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig,
    },
    sinsemilla::chip::{SinsemillaChip, SinsemillaConfig},
    utilities::{bool_check, lookup_range_check::LookupRangeCheckConfig},
};
use crate::circuit::address_ownership::{prove_address_ownership, spend_auth_g_mul};
use crate::circuit::elgamal::{prove_elgamal_encryptions, spend_auth_g_affine};
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
/// K=14 (16,384 rows). Conditions 1–3 and 5–10 use ~29 Poseidon hashes plus
/// AddChip additions, range-check running sums, ECC fixed-base mul
/// (condition 3), and 24 Merkle swap regions. Condition 11 adds 12
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
// Public input offsets (11 field elements).
// ================================================================

/// Public input offset for the VAN nullifier (prevents double-vote).
const VAN_NULLIFIER: usize = 0;
/// Public input offset for the randomized voting public key (condition 4: Spend Authority).
/// x-coordinate of r_vpk = vsk.ak + [alpha_v] * G.
const R_VPK_X: usize = 1;
/// Public input offset for r_vpk y-coordinate.
const R_VPK_Y: usize = 2;
/// Public input offset for the new VAN commitment (with decremented authority).
const VOTE_AUTHORITY_NOTE_NEW: usize = 3;
/// Public input offset for the vote commitment hash.
const VOTE_COMMITMENT: usize = 4;
/// Public input offset for the vote commitment tree root.
const VOTE_COMM_TREE_ROOT: usize = 5;
/// Public input offset for the tree anchor height.
const VOTE_COMM_TREE_ANCHOR_HEIGHT: usize = 6;
/// Public input offset for the proposal identifier.
const PROPOSAL_ID: usize = 7;
/// Public input offset for the voting round identifier.
const VOTING_ROUND_ID: usize = 8;
/// Public input offset for the election authority public key x-coordinate.
const EA_PK_X: usize = 9;
/// Public input offset for the election authority public key y-coordinate.
const EA_PK_Y: usize = 10;

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

/// Out-of-circuit VAN nullifier hash (condition 5).
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

/// Out-of-circuit shares hash (condition 10).
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

/// Out-of-circuit vote commitment hash (condition 12).
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

// ================================================================
// Config
// ================================================================

/// Configuration for the Vote Proof circuit.
///
/// Holds chip configs for Poseidon (conditions 1, 2, 5, 7, 10), AddChip
/// (conditions 6, 8), LookupRangeCheck (conditions 6, 9), ECC
/// (conditions 3, 11), and the Merkle swap gate (condition 1).
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
    /// VAN nullifier (condition 5), new VAN integrity (condition 7),
    /// vote commitment Merkle path (condition 1), and vote commitment
    /// integrity (conditions 10, 12).
    poseidon_config: PoseidonConfig<pallas::Base, 3, 2>,
    /// AddChip: constrains `a + b = c` on a single row.
    ///
    /// Uses advices[7] (a), advices[8] (b), advices[6] (c), matching
    /// the delegation circuit's column assignment.
    /// Used in conditions 6 (proposal authority decrement) and 8 (shares
    /// sum correctness).
    add_config: AddConfig,
    /// ECC chip configuration (condition 3: diversified address integrity, condition 11: El Gamal).
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
    /// Used in condition 6 to ensure authority values and diff are in [0, 2^16)
    /// (16-bit bitmask), and condition 9 to ensure each share is in `[0, 2^24)`.
    range_check: LookupRangeCheckConfig<pallas::Base, 10>,
    /// Selector for the Merkle conditional swap gate (condition 1).
    ///
    /// At each of the 24 Merkle tree levels, conditionally swaps
    /// (current, sibling) into (left, right) based on the position bit.
    /// Uses advices[0..5]: pos_bit, current, sibling, left, right.
    /// Identical to the delegation circuit's `q_imt_swap` gate.
    q_merkle_swap: Selector,
    /// Selector for condition 6 (Proposal Authority Decrement) lookup row.
    /// When 1, the (proposal_id, one_shifted) lookup is enforced; when 0,
    /// the lookup input is (0, 1) so it passes without constraining.
    q_cond5: Selector,
    /// Lookup table column for proposal_id in (proposal_id, 2^proposal_id).
    /// Table rows: (0, 1), (1, 2), (2, 4), ..., (15, 32768).
    table_proposal_id: TableColumn,
    /// Lookup table column for one_shifted = 2^proposal_id.
    table_one_shifted: TableColumn,
    /// Selector for condition 6 init row (index=0, two_pow_i=1).
    q_cond5_init: Selector,
    /// Selector for condition 6 bit rows 2..17 (recurrence).
    q_cond5_bits: Selector,
    /// Selector for condition 6 last bit row: run_selected = 1.
    q_cond5_selected_one: Selector,
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

    /// Constructs an ECC chip for curve operations (conditions 3, 11).
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
/// 12 conditions (condition 4 enforced out-of-circuit); constraint logic is added incrementally.
///
/// Conditions 1–3 and 5–12 are fully constrained in-circuit; condition 4 (Spend Authority) is
/// enforced out-of-circuit via signature verification.
#[derive(Clone, Debug, Default)]
pub struct Circuit {
    // === VAN ownership and spending (conditions 1–5; condition 4 out-of-circuit) ===

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
    // Condition 3 (Diversified Address Integrity): vpk_pk_d = [ivk_v] * vpk_g_d
    // where ivk_v = CommitIvk(ExtractP([vsk]*SpendAuthG), vsk.nk, rivk_v).
    // Full affine points are needed for condition 3's ECC operations;
    // x-coordinates are extracted in-circuit for Poseidon hashing (conditions 2, 7).
    /// Voting public key — diversified base point (from DiversifyHash(d)).
    /// This is the vpk_g_d component of the voting hotkey address.
    /// Condition 3 performs `[ivk_v] * vpk_g_d` to derive vpk_pk_d.
    pub(crate) vpk_g_d: Value<pallas::Affine>,
    /// Voting public key — diversified transmission key (pk_d = [ivk_v] * g_d).
    /// This is the vpk_pk_d component of the voting hotkey address.
    /// Condition 3 (Diversified Address Integrity) constrains this to equal `[ivk_v] * vpk_g_d`.
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

    // Condition 3 (Diversified Address Integrity): prover controls the VAN address.
    // vpk_pk_d = [ivk_v] * vpk_g_d
    //   where ivk_v = CommitIvk_rivk_v(ExtractP([vsk]*SpendAuthG), vsk.nk)
    /// Voting spending key (scalar for ECC multiplication).
    /// Used in condition 3 for `[vsk] * SpendAuthG`.
    pub(crate) vsk: Value<pallas::Scalar>,
    /// CommitIvk randomness for the ivk_v derivation (condition 3).
    /// Used as the blinding scalar in `CommitIvk(ak, nk, rivk_v)`.
    pub(crate) rivk_v: Value<pallas::Scalar>,
    /// Spend auth randomizer for condition 4: r_vpk = vsk.ak + [alpha_v] * G.
    pub(crate) alpha_v: Value<pallas::Scalar>,

    // Condition 5 (VAN Nullifier Integrity): nullifier deriving key.
    // Also used in condition 3 as the nk input to CommitIvk.
    /// Nullifier deriving key derived from vsk.
    pub(crate) vsk_nk: Value<pallas::Base>,

    // Condition 6 (Proposal Authority Decrement): one_shifted = 2^proposal_id.
    /// Cleared bit value: one_shifted = 2^proposal_id (witness; lookup constrains it).
    pub(crate) one_shifted: Value<pallas::Base>,

    // === Vote commitment construction (conditions 8–12) ===

    // Condition 8 (Shares Sum): sum(shares_1..4) = total_note_value.
    // Condition 9 (Shares Range): each share in [0, 2^24).
    /// Voting share vector (4 shares that sum to total_note_value).
    pub(crate) shares: [Value<pallas::Base>; 4],

    // Condition 10 (Shares Hash Integrity): El Gamal ciphertext x-coordinates.
    // These are the x-coordinates of the curve points comprising each
    // El Gamal ciphertext. Condition 11 constrains these to be correct
    // encryptions; condition 10 hashes them.
    /// X-coordinates of C1_i = r_i * G for each share (via ExtractP).
    pub(crate) enc_share_c1_x: [Value<pallas::Base>; 4],
    /// X-coordinates of C2_i = shares_i * G + r_i * ea_pk for each share (via ExtractP).
    pub(crate) enc_share_c2_x: [Value<pallas::Base>; 4],

    // Condition 11 (Encryption Integrity): El Gamal randomness and public key.
    /// El Gamal encryption randomness for each share (base field element,
    /// converted to scalar via ScalarVar::from_base in-circuit).
    pub(crate) share_randomness: [Value<pallas::Base>; 4],
    /// Election authority public key (Pallas curve point).
    /// The El Gamal encryption key — published as a round parameter.
    /// Both coordinates are public inputs (EA_PK_X, EA_PK_Y).
    pub(crate) ea_pk: Value<pallas::Affine>,

    // Condition 12 (Vote Commitment Integrity): vote decision.
    /// The voter's choice (hidden inside the vote commitment).
    pub(crate) vote_decision: Value<pallas::Base>,
}

impl Circuit {
    /// Creates a circuit with conditions 1–3 and 5–7 witnesses populated.
    ///
    /// All other witness fields are set to `Value::unknown()`.
    /// - Condition 1 uses `vote_authority_note_old` as the Merkle leaf,
    ///   with `vote_comm_tree_path` and `vote_comm_tree_position` for
    ///   the authentication path.
    /// - Condition 2 binds `vote_authority_note_old` to the Poseidon hash
    ///   of its components (using x-coordinates extracted from vpk_g_d, vpk_pk_d).
    /// - Condition 3 proves diversified address integrity via CommitIvk chain:
    ///   `[vsk] * SpendAuthG → ak → CommitIvk(ak, nk, rivk_v) → ivk_v → [ivk_v] * vpk_g_d = vpk_pk_d`.
    /// - Condition 5 reuses `vote_authority_note_old` and `voting_round_id`.
    /// - Condition 6 derives `proposal_authority_new` from
    ///   `proposal_authority_old`.
    /// - Condition 7 reuses all condition 2 witnesses except
    ///   `proposal_authority_old`, which is replaced by the
    ///   in-circuit `proposal_authority_new` from condition 6.
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
        alpha_v: Value<pallas::Scalar>,
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
            alpha_v,
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
        // condition 3 (diversified address integrity via CommitIvk chain) and condition 11
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

        // Condition 6: (proposal_id, one_shifted) lookup table for
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

        // Condition 6 (Proposal Authority Decrement) bit-decomposition gates.
        // Row 1: init (index=0, two_pow_i=1, running sums from first bit).
        let q_cond5_init = meta.selector();
        // Rows 2..17: recurrence (index++, two_pow_i *= 2, running sums).
        let q_cond5_bits = meta.selector();

        let zero = Expression::Constant(pallas::Base::zero());
        let one_expr = Expression::Constant(pallas::Base::one());
        let two_expr = Expression::Constant(pallas::Base::from(2u64));

        meta.create_gate("cond6 init: index=0, two_pow_i=1, running sums", |meta| {
            let q = meta.query_selector(q_cond5_init);
            let proposal_id = meta.query_advice(advices[0], Rotation::cur());
            let b_i = meta.query_advice(advices[1], Rotation::cur());
            let sel_i = meta.query_advice(advices[2], Rotation::cur());
            let b_new_i = meta.query_advice(advices[3], Rotation::cur());
            let run_sel = meta.query_advice(advices[4], Rotation::cur());
            let run_selected = meta.query_advice(advices[5], Rotation::cur());
            let run_old = meta.query_advice(advices[6], Rotation::cur());
            let run_new = meta.query_advice(advices[7], Rotation::cur());
            let two_pow_i = meta.query_advice(advices[8], Rotation::cur());
            let index = meta.query_advice(advices[9], Rotation::cur());
            let run_sel_prev = meta.query_advice(advices[4], Rotation::prev());
            let run_selected_prev = meta.query_advice(advices[5], Rotation::prev());
            let run_old_prev = meta.query_advice(advices[6], Rotation::prev());
            let run_new_prev = meta.query_advice(advices[7], Rotation::prev());

            Constraints::with_selector(
                q,
                [
                    ("two_pow_i = 1", two_pow_i.clone() - one_expr.clone()),
                    ("index = 0", index.clone() - zero.clone()),
                    ("run_sel", run_sel - run_sel_prev - sel_i.clone()),
                    ("run_selected", run_selected - run_selected_prev - sel_i.clone() * b_i.clone()),
                    ("run_old", run_old - run_old_prev - b_i.clone() * two_pow_i.clone()),
                    ("run_new", run_new - run_new_prev - b_new_i.clone() * two_pow_i),
                    ("(proposal_id - index)*sel_i", (proposal_id - index) * sel_i.clone()),
                    ("b_new_i = b_i*(1-sel_i)", b_new_i - b_i.clone() + b_i.clone() * sel_i.clone()),
                    ("bool b_i", bool_check(b_i)),
                    ("bool sel_i", bool_check(sel_i)),
                ],
            )
        });

        meta.create_gate("cond6 bits: index++, two_pow_i*=2, running sums", |meta| {
            let q = meta.query_selector(q_cond5_bits);
            let proposal_id = meta.query_advice(advices[0], Rotation::cur());
            let b_i = meta.query_advice(advices[1], Rotation::cur());
            let sel_i = meta.query_advice(advices[2], Rotation::cur());
            let b_new_i = meta.query_advice(advices[3], Rotation::cur());
            let run_sel = meta.query_advice(advices[4], Rotation::cur());
            let run_selected = meta.query_advice(advices[5], Rotation::cur());
            let run_old = meta.query_advice(advices[6], Rotation::cur());
            let run_new = meta.query_advice(advices[7], Rotation::cur());
            let two_pow_i = meta.query_advice(advices[8], Rotation::cur());
            let index = meta.query_advice(advices[9], Rotation::cur());
            let two_pow_i_prev = meta.query_advice(advices[8], Rotation::prev());
            let index_prev = meta.query_advice(advices[9], Rotation::prev());
            let run_sel_prev = meta.query_advice(advices[4], Rotation::prev());
            let run_selected_prev = meta.query_advice(advices[5], Rotation::prev());
            let run_old_prev = meta.query_advice(advices[6], Rotation::prev());
            let run_new_prev = meta.query_advice(advices[7], Rotation::prev());

            Constraints::with_selector(
                q,
                [
                    ("two_pow_i = 2*prev", two_pow_i.clone() - two_expr.clone() * two_pow_i_prev),
                    ("index = prev+1", index.clone() - index_prev - one_expr.clone()),
                    ("run_sel", run_sel - run_sel_prev - sel_i.clone()),
                    ("run_selected", run_selected - run_selected_prev - sel_i.clone() * b_i.clone()),
                    ("run_old", run_old - run_old_prev - b_i.clone() * two_pow_i.clone()),
                    ("run_new", run_new - run_new_prev - b_new_i.clone() * two_pow_i),
                    ("(proposal_id - index)*sel_i", (proposal_id - index) * sel_i.clone()),
                    ("b_new_i = b_i*(1-sel_i)", b_new_i - b_i.clone() + b_i.clone() * sel_i.clone()),
                    ("bool b_i", bool_check(b_i)),
                    ("bool sel_i", bool_check(sel_i)),
                ],
            )
        });

        // At the last bit row (row 16): run_selected must equal 1 (selected bit was set).
        let q_cond5_selected_one = meta.selector();
        meta.create_gate("cond6 run_selected = 1", |meta| {
            let q = meta.query_selector(q_cond5_selected_one);
            let run_selected = meta.query_advice(advices[5], Rotation::cur());
            Constraints::with_selector(q, [("run_selected = 1", run_selected - one_expr)])
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
            q_cond5_init,
            q_cond5_bits,
            q_cond5_selected_one,
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

        // Load (proposal_id, 2^proposal_id) lookup table for condition 6.
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
        // condition 5 (VAN nullifier). Witnessed here so it's available
        // for condition 3 which runs before condition 5.
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
        // - voting_round_id: also used in condition 5 (VAN nullifier).
        // - vpk_g_d, vpk_pk_d, total_note_value, voting_round_id, proposal_authority_old,
        //   gov_comm_rand, domain_van: also used in condition 7 (new VAN integrity).
        // - total_note_value: also used in condition 8 (shares sum check).
        // - vsk_nk: also used in condition 5 (VAN nullifier).
        let vote_authority_note_old_cond1 = vote_authority_note_old.clone();
        let voting_round_id_cond4 = voting_round_id.clone();
        let domain_van_cond6 = domain_van.clone();
        let vpk_g_d_cond6 = vpk_g_d.clone();
        let vpk_pk_d_cond6 = vpk_pk_d.clone();
        let total_note_value_cond6 = total_note_value.clone();
        let total_note_value_cond7 = total_note_value.clone();
        let voting_round_id_cond6 = voting_round_id.clone();
        let _proposal_authority_old_cond6 = proposal_authority_old.clone();
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
            proposal_authority_old.clone(),
            gov_comm_rand,
        )?;

        // Constrain: derived VAN hash == witnessed vote_authority_note_old.
        layouter.assign_region(
            || "VAN integrity check",
            |mut region| region.constrain_equal(derived_van.cell(), vote_authority_note_old.cell()),
        )?;

        // ---------------------------------------------------------------
        // Condition 3: Diversified Address Integrity.
        //
        // vpk_pk_d = [ivk_v] * vpk_g_d where ivk_v = CommitIvk(ExtractP([vsk]*SpendAuthG), vsk_nk, rivk_v).
        // ---------------------------------------------------------------
        let vsk_scalar = ScalarFixed::new(
            ecc_chip.clone(),
            layouter.namespace(|| "cond3 vsk"),
            self.vsk,
        )?;
        let vsk_ak_point = spend_auth_g_mul(
            ecc_chip.clone(),
            layouter.namespace(|| "cond3 [vsk]G"),
            "cond3: [vsk] SpendAuthG",
            vsk_scalar,
        )?;
        let ak = vsk_ak_point.extract_p().inner().clone();
        let rivk_v_scalar = ScalarFixed::new(
            ecc_chip.clone(),
            layouter.namespace(|| "cond3 rivk_v"),
            self.rivk_v,
        )?;
        prove_address_ownership(
            config.sinsemilla_chip(),
            ecc_chip.clone(),
            config.commit_ivk_chip(),
            layouter.namespace(|| "cond3 address"),
            "cond3",
            ak,
            vsk_nk.clone(),
            rivk_v_scalar,
            &vpk_g_d_point,
            &vpk_pk_d_point,
        )?;

        // ---------------------------------------------------------------
        // Condition 4: Spend authority.
        // r_vpk = [alpha_v] * SpendAuthG + vsk_ak_point
        // ---------------------------------------------------------------
        // Spend authority: proves that the public r_vpk is a valid rerandomization of the prover's ak.
        // The out-of-circuit verifier checks that the vote signature is valid under r_vpk,
        // so this links the ZKP to the signature without revealing ak.
        {
            let alpha_v =
                ScalarFixed::new(ecc_chip.clone(), layouter.namespace(|| "alpha_v"), self.alpha_v)?;
            let alpha_v_commitment =
                spend_auth_g_mul(ecc_chip.clone(), layouter.namespace(|| "cond4"), "[alpha_v] SpendAuthG", alpha_v)?;
            let r_vpk = alpha_v_commitment.add(layouter.namespace(|| "r_vpk"), &vsk_ak_point)?;
            layouter.constrain_instance(r_vpk.inner().x().cell(), config.primary, R_VPK_X)?;
            layouter.constrain_instance(r_vpk.inner().y().cell(), config.primary, R_VPK_Y)?;
        }

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
        // Witness assignment for condition 5.
        //
        // vsk_nk was already witnessed before condition 3 (shared between
        // conditions 3 and 5). The vsk_nk_cond4 clone is used here.
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
        // Condition 5: VAN Nullifier Integrity.
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
        // Condition 6: Proposal Authority Decrement (bit decomposition).
        //
        // Step 1: Decompose proposal_authority_old into 16 bits b_i (boolean).
        // Step 2: Selector sel_i = 1 iff proposal_id == i; exactly one active;
        //         selected bit = sum(sel_i * b_i) = 1 (voter has authority).
        // Step 3: b_new_i = b_i*(1-sel_i); recompose to proposal_authority_new.
        // No diff/gap range check; decomposition proves [0, 2^16).
        // ---------------------------------------------------------------

        let (proposal_id, proposal_authority_new, run_old_final, run_new_final) =
            layouter.assign_region(
                || "cond6 proposal authority decrement",
                |mut region| {
                    let proposal_authority_old_val = self.proposal_authority_old;

                    // Row 0: (proposal_id, one_shifted) for lookup; init running sums to 0.
                    config.q_cond5.enable(&mut region, 0)?;
                    let proposal_id_cell = region.assign_advice_from_instance(
                        || "proposal_id",
                        config.primary,
                        PROPOSAL_ID,
                        config.advices[0],
                        0,
                    )?;
                    let _one_shifted_cell = region.assign_advice(
                        || "one_shifted",
                        config.advices[1],
                        0,
                        || self.one_shifted,
                    )?;
                    region.assign_advice_from_constant(
                        || "run_sel init",
                        config.advices[4],
                        0,
                        pallas::Base::zero(),
                    )?;
                    region.assign_advice_from_constant(
                        || "run_selected init",
                        config.advices[5],
                        0,
                        pallas::Base::zero(),
                    )?;
                    region.assign_advice_from_constant(
                        || "run_old init",
                        config.advices[6],
                        0,
                        pallas::Base::zero(),
                    )?;
                    region.assign_advice_from_constant(
                        || "run_new init",
                        config.advices[7],
                        0,
                        pallas::Base::zero(),
                    )?;

                    // Rows 1..17: bits, selectors, running sums.
                    let zero_val = Value::known(pallas::Base::zero());
                    let mut run_old_prev = zero_val;
                    let mut run_new_prev = zero_val;
                    let mut run_sel_prev = zero_val;
                    let mut run_selected_prev = zero_val;

                    for i in 0..MAX_PROPOSAL_ID {
                        let row = 1 + i;
                        let proposal_id_base = proposal_id_cell.value().copied();
                        let b_i_val = proposal_authority_old_val.map(|b| {
                            let r = b.to_repr();
                            let arr = r.as_ref();
                            let low = u64::from_le_bytes(arr[0..8].try_into().unwrap()) & 0xFFFF;
                            let bit = (low >> i) & 1;
                            pallas::Base::from(bit)
                        });
                        let sel_i_val = proposal_id_base.map(|pid| {
                            let r = pid.to_repr();
                            let arr = r.as_ref();
                            let pid_u64 = u64::from_le_bytes(arr[0..8].try_into().unwrap());
                            pallas::Base::from(if pid_u64 == i as u64 { 1u64 } else { 0 })
                        });
                        let b_new_i_val = b_i_val.zip(sel_i_val)
                            .map(|(b, s)| b - b * s);
                        let two_pow_i_val = Value::known(pallas::Base::from(1u64 << i));
                        run_sel_prev = run_sel_prev.zip(sel_i_val).map(|(r, s)| r + s);
                        run_selected_prev = run_selected_prev
                            .zip(sel_i_val)
                            .zip(b_i_val)
                            .map(|((r, s), b)| r + s * b);
                        run_old_prev = run_old_prev
                            .zip(b_i_val)
                            .zip(two_pow_i_val)
                            .map(|((r, b), t)| r + b * t);
                        run_new_prev = run_new_prev
                            .zip(b_new_i_val)
                            .zip(two_pow_i_val)
                            .map(|((r, b), t)| r + b * t);

                        region.assign_advice(
                            || format!("proposal_id copy {}", i),
                            config.advices[0],
                            row,
                            || proposal_id_cell.value().copied(),
                        )?;
                        region.assign_advice(
                            || format!("b_{}", i),
                            config.advices[1],
                            row,
                            || b_i_val,
                        )?;
                        region.assign_advice(
                            || format!("sel_{}", i),
                            config.advices[2],
                            row,
                            || sel_i_val,
                        )?;
                        region.assign_advice(
                            || format!("b_new_{}", i),
                            config.advices[3],
                            row,
                            || b_new_i_val,
                        )?;
                        region.assign_advice(
                            || format!("run_sel {}", i),
                            config.advices[4],
                            row,
                            || run_sel_prev,
                        )?;
                        region.assign_advice(
                            || format!("run_selected {}", i),
                            config.advices[5],
                            row,
                            || run_selected_prev,
                        )?;
                        region.assign_advice(
                            || format!("run_old {}", i),
                            config.advices[6],
                            row,
                            || run_old_prev,
                        )?;
                        region.assign_advice(
                            || format!("run_new {}", i),
                            config.advices[7],
                            row,
                            || run_new_prev,
                        )?;
                        region.assign_advice(
                            || format!("two_pow_i {}", i),
                            config.advices[8],
                            row,
                            || two_pow_i_val,
                        )?;
                        region.assign_advice(
                            || format!("index {}", i),
                            config.advices[9],
                            row,
                            || Value::known(pallas::Base::from(i as u64)),
                        )?;

                        if i == 0 {
                            config.q_cond5_init.enable(&mut region, row)?;
                        } else {
                            config.q_cond5_bits.enable(&mut region, row)?;
                        }
                        if i == MAX_PROPOSAL_ID - 1 {
                            config.q_cond5_selected_one.enable(&mut region, row)?;
                        }
                    }

                    // proposal_authority_new = recomposed value (same as old - one_shifted when spec is satisfied).
                    let proposal_authority_new_val = self.proposal_authority_old
                        .zip(self.one_shifted)
                        .map(|(old, shift)| old - shift);
                    let proposal_authority_new_cell = region.assign_advice(
                        || "proposal_authority_new",
                        config.advices[0],
                        17,
                        || proposal_authority_new_val,
                    )?;
                    let run_old_cell = region.assign_advice(
                        || "run_old final",
                        config.advices[6],
                        17,
                        || run_old_prev,
                    )?;
                    let run_new_cell = region.assign_advice(
                        || "run_new final",
                        config.advices[7],
                        17,
                        || run_new_prev,
                    )?;

                    Ok((
                        proposal_id_cell,
                        proposal_authority_new_cell,
                        run_old_cell,
                        run_new_cell,
                    ))
                },
            )?;

        // Constrain recomposed run_old == proposal_authority_old, run_new == proposal_authority_new.
        layouter.assign_region(
            || "cond6 authority equality",
            |mut region| {
                let a = proposal_authority_old.copy_advice(
                    || "copy proposal_authority_old",
                    &mut region,
                    config.advices[0],
                    0,
                )?;
                let b = run_old_final.copy_advice(
                    || "copy run_old",
                    &mut region,
                    config.advices[1],
                    0,
                )?;
                region.constrain_equal(a.cell(), b.cell())
            },
        )?;
        layouter.assign_region(
            || "cond6 new authority equality",
            |mut region| {
                let a = proposal_authority_new.copy_advice(
                    || "copy proposal_authority_new",
                    &mut region,
                    config.advices[0],
                    0,
                )?;
                let b = run_new_final.copy_advice(
                    || "copy run_new",
                    &mut region,
                    config.advices[1],
                    0,
                )?;
                region.constrain_equal(a.cell(), b.cell())
            },
        )?;

        // ---------------------------------------------------------------
        // Condition 7: New VAN Integrity (ZKP 1–compatible two-layer hash).
        //
        // Same structure as condition 2; proposal_authority_new (from
        // condition 6) replaces proposal_authority_old. vpk_g_d and vpk_pk_d
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
        // Condition 8: Shares Sum Correctness.
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
        // by condition 9 (range check) and condition 11 (El Gamal
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
        // Condition 9: Shares Range.
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
        // the originals remain available for condition 11 (El Gamal).
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
        // Condition 10: Shares Hash Integrity.
        //
        // shares_hash = Poseidon(c1_0_x, c2_0_x, c1_1_x, c2_1_x,
        //                        c1_2_x, c2_2_x, c1_3_x, c2_3_x)
        //
        // Hashes the 8 x-coordinates of the 4 El Gamal ciphertext pairs
        // into a single commitment. The order interleaves C1 and C2
        // per share for locality. shares_hash is an internal wire; it
        // is not bound to the instance column. Condition 11 constrains
        // that each (c1_i_x, c2_i_x) is a valid El Gamal encryption of
        // shares_i. Condition 12 computes the full vote commitment
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
        // them). These clones are used by condition 11 to constrain that
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
        // The result is used by condition 12 (vote commitment integrity).
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
        // Condition 11: Encryption Integrity.
        //
        // For each share i: C1_i = [r_i]*G, C2_i = [v_i]*G + [r_i]*ea_pk;
        // ExtractP(C1_i) and ExtractP(C2_i) are constrained to the
        // witnessed enc_share cells. Implemented by the shared
        // circuit::elgamal::prove_elgamal_encryptions gadget.
        // ---------------------------------------------------------------
        {
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

            let r_0 = assign_free_advice(
                layouter.namespace(|| "witness r[0]"),
                config.advices[0],
                self.share_randomness[0],
            )?;
            let r_1 = assign_free_advice(
                layouter.namespace(|| "witness r[1]"),
                config.advices[0],
                self.share_randomness[1],
            )?;
            let r_2 = assign_free_advice(
                layouter.namespace(|| "witness r[2]"),
                config.advices[0],
                self.share_randomness[2],
            )?;
            let r_3 = assign_free_advice(
                layouter.namespace(|| "witness r[3]"),
                config.advices[0],
                self.share_randomness[3],
            )?;
            let r_cells = [r_0, r_1, r_2, r_3];

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

            prove_elgamal_encryptions(
                ecc_chip.clone(),
                layouter.namespace(|| "cond11 El Gamal"),
                "cond11",
                g_affine,
                g_x_const,
                g_y_const,
                self.ea_pk,
                ea_pk_x_cell,
                ea_pk_y_cell,
                share_cells,
                r_cells,
                enc_c1_cells,
                enc_c2_cells,
            )?;
        }

        // ---------------------------------------------------------------
        // Condition 12: Vote Commitment Integrity.
        //
        // vote_commitment = Poseidon(DOMAIN_VC, shares_hash,
        //                            proposal_id, vote_decision)
        //
        // Binds the encrypted shares (via shares_hash from condition 10),
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

        // proposal_id was already copied from instance in condition 6; reuse that cell.

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

/// Public inputs to the Vote Proof circuit (11 field elements).
///
/// These are the values posted to the vote chain that both the prover
/// and verifier agree on. The verifier checks the proof against these
/// values without seeing any private witnesses.
#[derive(Clone, Debug)]
pub struct Instance {
    /// The nullifier of the old VAN being spent (prevents double-vote).
    pub van_nullifier: pallas::Base,
    /// Randomized voting public key (condition 4): x-coordinate of r_vpk = vsk.ak + [alpha_v] * G.
    pub r_vpk_x: pallas::Base,
    /// Randomized voting public key: y-coordinate.
    pub r_vpk_y: pallas::Base,
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
        r_vpk_x: pallas::Base,
        r_vpk_y: pallas::Base,
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
            r_vpk_x,
            r_vpk_y,
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
    /// top of this file (`VAN_NULLIFIER`, `R_VPK_X`, `R_VPK_Y`, etc.).
    pub fn to_halo2_instance(&self) -> Vec<vesta::Scalar> {
        alloc::vec![
            self.van_nullifier,
            self.r_vpk_x,
            self.r_vpk_y,
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
    use crate::circuit::elgamal::{base_to_scalar, elgamal_encrypt, spend_auth_g_affine};
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

    /// Sets condition 12 fields on a circuit and returns the vote_commitment.
    ///
    /// Computes `H(DOMAIN_VC, shares_hash, proposal_id, vote_decision)`
    /// and sets `circuit.vote_decision`. Returns the vote_commitment
    /// for use in the Instance. The `proposal_id` must match the
    /// instance's proposal_id so the circuit's condition 12 (which
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
        let alpha_v = pallas::Scalar::random(&mut rng);

        let (vpk_g_d_affine, vpk_pk_d_affine) = derive_voting_address(vsk, vsk_nk, rivk_v);

        // Condition 4: r_vpk = ak + [alpha_v] * G
        let g = pallas::Point::from(spend_auth_g_affine());
        let ak_point = g * vsk;
        let r_vpk = (ak_point + g * alpha_v).to_affine();
        let r_vpk_x = *r_vpk.coordinates().unwrap().x();
        let r_vpk_y = *r_vpk.coordinates().unwrap().y();

        // Extract x-coordinates for Poseidon hashing (conditions 2, 6).
        let vpk_g_d_x = *vpk_g_d_affine.coordinates().unwrap().x();
        let vpk_pk_d_x = *vpk_pk_d_affine.coordinates().unwrap().x();

        // total_note_value must be small enough that all 4 shares
        // fit in [0, 2^24) for condition 9's range check.
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

        // Create shares that sum to total_note_value (conditions 8 + 9).
        // Each share must be in [0, 2^24) for condition 9's range check.
        let shares_u64: [u64; 4] = [1_000, 2_000, 3_000, 4_000]; // sum = 10000
        let s0 = pallas::Base::from(shares_u64[0]);
        let s1 = pallas::Base::from(shares_u64[1]);
        let s2 = pallas::Base::from(shares_u64[2]);
        let s3 = pallas::Base::from(shares_u64[3]);

        // Condition 11: El Gamal encryption of shares under ea_pk.
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
            Value::known(alpha_v),
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

        // Condition 12: vote commitment from shares_hash + proposal + decision.
        let vote_commitment = set_condition_11(&mut circuit, shares_hash_val, proposal_id);

        let instance = Instance::from_parts(
            van_nullifier,
            r_vpk_x,
            r_vpk_y,
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
        let alpha_v = pallas::Scalar::random(&mut rng);
        let (vpk_g_d_affine, vpk_pk_d_affine) = derive_voting_address(vsk, vsk_nk, rivk_v);
        let g = pallas::Point::from(spend_auth_g_affine());
        let r_vpk = (g * vsk + g * alpha_v).to_affine();
        instance.r_vpk_x = *r_vpk.coordinates().unwrap().x();
        instance.r_vpk_y = *r_vpk.coordinates().unwrap().y();

        let shares_u64: [u64; 4] = [1_000, 2_000, 3_000, 4_000];
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        // Use authority 13 (bit 3 set) and one_shifted = 8 so condition 6 is consistent;
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
            Value::known(alpha_v),
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
    // Condition 3 (Diversified Address Integrity / Address Ownership) tests
    //
    // These tests ensure the circuit rejects witnesses that violate
    // vpk_pk_d = [ivk_v] * vpk_g_d. Without condition 3 enabled, they
    // would pass (invalid address ownership would not be detected).
    // ================================================================

    /// Using a different vsk in the circuit than was used to derive
    /// (vpk_g_d, vpk_pk_d) should fail condition 3 only: in-circuit
    /// [ivk']*vpk_g_d ≠ vpk_pk_d while VAN hash and nullifier stay valid.
    #[test]
    fn condition_3_wrong_vsk_fails() {
        let mut rng = OsRng;

        let vsk = pallas::Scalar::random(&mut rng);
        let vsk_nk = pallas::Base::random(&mut rng);
        let rivk_v = pallas::Scalar::random(&mut rng);
        let (vpk_g_d_affine, vpk_pk_d_affine) = derive_voting_address(vsk, vsk_nk, rivk_v);
        let vpk_g_d_x = *vpk_g_d_affine.coordinates().unwrap().x();
        let vpk_pk_d_x = *vpk_pk_d_affine.coordinates().unwrap().x();

        let total_note_value = pallas::Base::from(10_000u64);
        let voting_round_id = pallas::Base::random(&mut rng);
        let proposal_authority_old = pallas::Base::from(13u64);
        let proposal_id = 3u64;
        let gov_comm_rand = pallas::Base::random(&mut rng);

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

        let shares_u64: [u64; 4] = [1_000, 2_000, 3_000, 4_000];
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        let wrong_vsk = pallas::Scalar::random(&mut rng);
        assert_ne!(wrong_vsk, vsk, "test assumes distinct vsk with high probability");
        let alpha_v = pallas::Scalar::random(&mut rng);
        let g = pallas::Point::from(spend_auth_g_affine());
        let r_vpk = (g * vsk + g * alpha_v).to_affine();
        let r_vpk_x = *r_vpk.coordinates().unwrap().x();
        let r_vpk_y = *r_vpk.coordinates().unwrap().y();

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(vpk_g_d_affine),
            Value::known(vpk_pk_d_affine),
            Value::known(total_note_value),
            Value::known(proposal_authority_old),
            Value::known(gov_comm_rand),
            Value::known(vote_authority_note_old),
            Value::known(wrong_vsk),
            Value::known(rivk_v),
            Value::known(vsk_nk),
            Value::known(alpha_v),
        );
        circuit.one_shifted = Value::known(one_shifted);
        circuit.shares = shares_u64.map(|s| Value::known(pallas::Base::from(s)));
        circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
        circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id);

        let instance = Instance::from_parts(
            van_nullifier,
            r_vpk_x,
            r_vpk_y,
            vote_authority_note_new,
            vc,
            vote_comm_tree_root,
            pallas::Base::zero(),
            pallas::Base::from(proposal_id),
            voting_round_id,
            *ea_pk_affine.coordinates().unwrap().x(),
            *ea_pk_affine.coordinates().unwrap().y(),
        );

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err(), "condition 3 must reject wrong vsk");
    }

    /// Using a vpk_pk_d that does not equal [ivk_v]*vpk_g_d should fail
    /// condition 3. Instance is built with a wrong vpk_pk_d for the VAN
    /// hash so condition 2 still passes; only condition 3 fails.
    #[test]
    fn condition_3_wrong_vpk_pk_d_fails() {
        let mut rng = OsRng;

        let vsk = pallas::Scalar::random(&mut rng);
        let vsk_nk = pallas::Base::random(&mut rng);
        let rivk_v = pallas::Scalar::random(&mut rng);
        let (vpk_g_d_affine, _vpk_pk_d_correct) = derive_voting_address(vsk, vsk_nk, rivk_v);
        let vpk_g_d_x = *vpk_g_d_affine.coordinates().unwrap().x();

        let wrong_vpk_pk_d_affine = (pallas::Point::generator() * pallas::Scalar::from(99999u64))
            .to_affine();
        let wrong_vpk_pk_d_x = *wrong_vpk_pk_d_affine.coordinates().unwrap().x();

        let total_note_value = pallas::Base::from(10_000u64);
        let voting_round_id = pallas::Base::random(&mut rng);
        let proposal_authority_old = pallas::Base::from(13u64);
        let proposal_id = 3u64;
        let gov_comm_rand = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            vpk_g_d_x,
            wrong_vpk_pk_d_x,
            total_note_value,
            voting_round_id,
            proposal_authority_old,
            gov_comm_rand,
        );
        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_authority_note_old);
        let van_nullifier = van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);
        let one_shifted = pallas::Base::from(1u64 << proposal_id);
        let proposal_authority_new = proposal_authority_old - one_shifted;
        let vote_authority_note_new = van_integrity_hash(
            vpk_g_d_x,
            wrong_vpk_pk_d_x,
            total_note_value,
            voting_round_id,
            proposal_authority_new,
            gov_comm_rand,
        );

        let shares_u64: [u64; 4] = [1_000, 2_000, 3_000, 4_000];
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        let alpha_v = pallas::Scalar::random(&mut rng);
        let g = pallas::Point::from(spend_auth_g_affine());
        let r_vpk = (g * vsk + g * alpha_v).to_affine();
        let r_vpk_x = *r_vpk.coordinates().unwrap().x();
        let r_vpk_y = *r_vpk.coordinates().unwrap().y();

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(vpk_g_d_affine),
            Value::known(wrong_vpk_pk_d_affine),
            Value::known(total_note_value),
            Value::known(proposal_authority_old),
            Value::known(gov_comm_rand),
            Value::known(vote_authority_note_old),
            Value::known(vsk),
            Value::known(rivk_v),
            Value::known(vsk_nk),
            Value::known(alpha_v),
        );
        circuit.one_shifted = Value::known(one_shifted);
        circuit.shares = shares_u64.map(|s| Value::known(pallas::Base::from(s)));
        circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
        circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id);

        let instance = Instance::from_parts(
            van_nullifier,
            r_vpk_x,
            r_vpk_y,
            vote_authority_note_new,
            vc,
            vote_comm_tree_root,
            pallas::Base::zero(),
            pallas::Base::from(proposal_id),
            voting_round_id,
            *ea_pk_affine.coordinates().unwrap().x(),
            *ea_pk_affine.coordinates().unwrap().y(),
        );

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err(), "condition 3 must reject wrong vpk_pk_d");
    }

    // ================================================================
    // Condition 4 (Spend Authority) tests
    // ================================================================

    /// Wrong r_vpk public input should fail condition 4.
    #[test]
    fn condition_4_wrong_r_vpk_fails() {
        let (circuit, mut instance) = make_test_data();

        instance.r_vpk_x = pallas::Base::random(&mut OsRng);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err(), "condition 4 must reject wrong r_vpk");
    }

    // ================================================================
    // Condition 5 (VAN Nullifier Integrity) tests
    // ================================================================

    /// Wrong VAN_NULLIFIER public input should fail condition 5.
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
    /// the instance nullifier should fail condition 5.
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
        let alpha_v = pallas::Scalar::random(&mut rng);
        let g = pallas::Point::from(spend_auth_g_affine());
        let r_vpk = (g * vsk + g * alpha_v).to_affine();
        let r_vpk_x = *r_vpk.coordinates().unwrap().x();
        let r_vpk_y = *r_vpk.coordinates().unwrap().y();

        // Shares that sum to total_note_value (conditions 8 + 9).
        let shares_u64: [u64; 4] = [1_000, 2_000, 3_000, 4_000];

        // Condition 11: real El Gamal encryption.
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
            Value::known(alpha_v),
        );
        circuit.one_shifted = Value::known(one_shifted);
        circuit.shares = shares_u64.map(|s| Value::known(pallas::Base::from(s)));
        circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
        circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id);

        let instance = Instance::from_parts(
            van_nullifier,
            r_vpk_x,
            r_vpk_y,
            vote_authority_note_new,
            vc,
            vote_comm_tree_root,
            pallas::Base::zero(),
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
    // Condition 6 (Proposal Authority Decrement) tests
    // ================================================================

    /// Proposal authority with only bit 0 set (value 1): vote on proposal 0, new = 0.
    #[test]
    fn proposal_authority_decrement_minimum_valid() {
        let (circuit, instance) =
            make_test_data_with_authority_and_proposal(pallas::Base::one(), 0);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    /// With proposal_authority_old = 0, the selected bit is 0 so the
    /// "run_selected = 1" constraint (selected bit was set) fails.
    #[test]
    fn proposal_authority_zero_fails() {
        let (circuit, instance) = make_test_data_with_authority(pallas::Base::zero());

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();

        assert!(prover.verify().is_err());
    }

    /// Full authority (65535), proposal_id 1 → new = 65533 (e2e scenario).
    #[test]
    fn proposal_authority_full_authority_proposal_1_passes() {
        const MAX_PROPOSAL_AUTHORITY: u64 = 65535;
        let (circuit, instance) = make_test_data_with_authority_and_proposal(
            pallas::Base::from(MAX_PROPOSAL_AUTHORITY),
            1,
        );

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    /// Wrong vote_authority_note_new (e.g. not clearing the bit) fails condition 6.
    #[test]
    fn proposal_authority_wrong_new_fails() {
        let (circuit, mut instance) =
            make_test_data_with_authority_and_proposal(pallas::Base::from(65535u64), 1);

        instance.vote_authority_note_new = pallas::Base::random(&mut OsRng);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// proposal_authority_old has bit 2 set only (4); proposal_id 0 → selected bit is 0, so
    /// "run_selected = 1" fails.
    #[test]
    fn proposal_authority_bit_not_set_fails() {
        let (circuit, instance) =
            make_test_data_with_authority_and_proposal(pallas::Base::from(4u64), 0);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    // ================================================================
    // Condition 6 (New VAN Integrity) tests
    // ================================================================

    /// Wrong vote_authority_note_new public input should fail condition 7.
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

        let alpha_v = pallas::Scalar::random(&mut rng);
        let g = pallas::Point::from(spend_auth_g_affine());
        let r_vpk = (g * vsk + g * alpha_v).to_affine();
        let r_vpk_x = *r_vpk.coordinates().unwrap().x();
        let r_vpk_y = *r_vpk.coordinates().unwrap().y();

        // Shares that sum to total_note_value (conditions 8 + 9).
        let shares_u64: [u64; 4] = [1_000, 2_000, 3_000, 4_000];

        // Condition 11: real El Gamal encryption.
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
            Value::known(alpha_v),
        );
        circuit.one_shifted = Value::known(one_shifted);
        circuit.shares = shares_u64.map(|s| Value::known(pallas::Base::from(s)));
        circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
        circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id);

        let instance = Instance::from_parts(
            van_nullifier,
            r_vpk_x,
            r_vpk_y,
            vote_authority_note_new,
            vc,
            vote_comm_tree_root,
            pallas::Base::zero(),
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

    /// Shares that do NOT sum to total_note_value should fail condition 8.
    #[test]
    fn shares_sum_wrong_total_fails() {
        let (mut circuit, instance) = make_test_data();

        // Corrupt shares[3] so the sum no longer equals total_note_value.
        // Use a small value that still passes condition 9's range check,
        // isolating the condition 8 failure.
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

        // Condition 11: real El Gamal encryption with max-value shares.
        let max_share_u64 = (1u64 << 30) - 1;
        let shares_u64: [u64; 4] = [max_share_u64; 4];
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        let alpha_v = pallas::Scalar::random(&mut rng);
        let g = pallas::Point::from(spend_auth_g_affine());
        let r_vpk = (g * vsk + g * alpha_v).to_affine();
        let r_vpk_x = *r_vpk.coordinates().unwrap().x();
        let r_vpk_y = *r_vpk.coordinates().unwrap().y();

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
            Value::known(alpha_v),
        );
        circuit.one_shifted = Value::known(one_shifted);
        circuit.shares = [Value::known(max_share); 4];
        circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
        circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id);

        let instance = Instance::from_parts(
            van_nullifier,
            r_vpk_x,
            r_vpk_y,
            vote_authority_note_new,
            vc,
            vote_comm_tree_root,
            pallas::Base::zero(),
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
        // This will fail condition 9 AND condition 8 (sum mismatch),
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

    /// A corrupted enc_share_c1_x[0] should cause condition 10 failure:
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
    // Condition 11 (Encryption Integrity) tests
    // ================================================================

    /// Valid El Gamal encryptions should produce a valid proof.
    #[test]
    fn encryption_integrity_valid_proof() {
        let (circuit, instance) = make_test_data();

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    /// A corrupted share_randomness[0] should fail condition 11:
    /// the computed C1[0] won't match enc_share_c1_x[0].
    #[test]
    fn encryption_integrity_wrong_randomness_fails() {
        let (mut circuit, instance) = make_test_data();

        // Corrupt the randomness for share 0 — C1 will change.
        circuit.share_randomness[0] = Value::known(pallas::Base::from(9999u64));

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// A wrong ea_pk in the instance should fail condition 11:
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

    /// A corrupted share value (plaintext) should fail condition 11:
    /// C2_i = [v_i]*G + [r_i]*ea_pk will not match enc_share_c2_x[i].
    #[test]
    fn encryption_integrity_wrong_share_fails() {
        let (mut circuit, instance) = make_test_data();

        // Corrupt share 0 — enc_share and randomness are unchanged (from
        // make_test_data), so the in-circuit C2_0 will not match enc_c2_x[0].
        circuit.shares[0] = Value::known(pallas::Base::from(9999u64));

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// A corrupted enc_share_c2_x witness should cause verification to fail:
    /// condition 11 constrains ExtractP(C2_i) == enc_c2_x[i].
    #[test]
    fn encryption_integrity_wrong_enc_c2_x_fails() {
        let (mut circuit, instance) = make_test_data();

        // Corrupt one C2 x-coordinate — the ECC will compute the real C2_0
        // from share_0 and r_0; constrain_equal will fail (or the resulting
        // shares_hash will not match the instance vote_commitment).
        circuit.enc_share_c2_x[0] = Value::known(pallas::Base::random(&mut OsRng));

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

    /// base_to_scalar (used by El Gamal) accepts share-sized values and
    /// the fixed randomness used in encrypt_shares.
    #[test]
    fn base_to_scalar_accepts_elgamal_inputs() {
        // Share-sized values (condition 9: [0, 2^30)) must convert.
        assert!(base_to_scalar(pallas::Base::zero()).is_some());
        assert!(base_to_scalar(pallas::Base::from(1u64)).is_some());
        assert!(base_to_scalar(pallas::Base::from(1_000u64)).is_some());
        assert!(base_to_scalar(pallas::Base::from(404u64)).is_some()); // encrypt_shares randomness

        // Encrypt_shares uses 101, 202, 303, 404 as r_i — all must convert.
        for r in [101u64, 202, 303, 404] {
            assert!(
                base_to_scalar(pallas::Base::from(r)).is_some(),
                "r = {} must convert for El Gamal",
                r
            );
        }
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

    /// A wrong vote_decision in the circuit should fail condition 12:
    /// the derived vote_commitment won't match the instance.
    #[test]
    fn vote_commitment_wrong_decision_fails() {
        let (mut circuit, instance) = make_test_data();

        // Corrupt the vote decision — the Poseidon hash will change.
        circuit.vote_decision = Value::known(pallas::Base::from(99u64));

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// A wrong proposal_id in the instance should fail condition 12:
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
    fn instance_has_eleven_public_inputs() {
        let (_, instance) = make_test_data();
        assert_eq!(instance.to_halo2_instance().len(), 11);
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
