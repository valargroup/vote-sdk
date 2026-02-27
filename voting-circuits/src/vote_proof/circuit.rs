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
//! - **Condition 10**: Shares Hash Integrity (Poseidon `ConstantLength<16>` over 16 blinded share commitments; output flows to condition 12).
//! - **Condition 11**: Encryption Integrity (ECC variable-base mul, `constrain_equal`).
//! - **Condition 12**: Vote Commitment Integrity (Poseidon `ConstantLength<5>`, `constrain_instance`).
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
//! - **Condition 8**: Shares Sum Correctness — `sum(shares_1..16) = total_note_value`.
//!   *(implemented)*
//! - **Condition 9**: Shares Range — each `shares_j` in `[0, 2^30)`.
//!   *(implemented)*
//! - **Condition 10**: Shares Hash Integrity — `shares_hash = H(enc_share_1..16)`.
//!   *(implemented)*
//! - **Condition 11**: Encryption Integrity — each `enc_share_i = ElGamal(shares_i, r_i, ea_pk)`.
//!   *(implemented)*
//! - **Condition 12**: Vote Commitment Integrity — `vote_commitment = H(DOMAIN_VC, voting_round_id,
//!   shares_hash, proposal_id, vote_decision)`. *(implemented)*

use alloc::vec::Vec;


use halo2_proofs::{
    circuit::{AssignedCell, Layouter, Value, floor_planner},
    plonk::{self, Advice, Column, ConstraintSystem, Fixed, Instance as InstanceColumn},
};
use pasta_curves::{pallas, vesta};

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
    utilities::lookup_range_check::LookupRangeCheckConfig,
};
use crate::circuit::address_ownership::{prove_address_ownership, spend_auth_g_mul};
use crate::circuit::elgamal::{EaPkInstanceLoc, prove_elgamal_encryptions};
use crate::circuit::poseidon_merkle::{MerkleSwapGate, synthesize_poseidon_merkle_path};
use orchard::circuit::commit_ivk::{CommitIvkChip, CommitIvkConfig};
use orchard::circuit::gadget::{add_chip::{AddChip, AddConfig}, assign_free_advice, AddInstruction};
use orchard::constants::{
    OrchardCommitDomains, OrchardFixedBases, OrchardHashDomains,
};
use crate::circuit::van_integrity;
use crate::circuit::vote_commitment;
use crate::shares_hash::compute_shares_hash_in_circuit;
#[cfg(test)]
use crate::shares_hash::hash_share_commitment_in_circuit;
use super::authority_decrement::{AuthorityDecrementChip, AuthorityDecrementConfig};

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
/// K=14 (16,384 rows). `CircuitCost::measure` reports a floor-planner
/// high-water mark of **3,512 rows** (21% of 16,384). The `V1` floor planner
/// packs non-overlapping regions into the same row range across different
/// columns, so the high-water mark is much lower than a naive sum-of-heights
/// estimate.
///
/// Key contributors (rough per-region heights, not per-column sums):
/// - 24-level Merkle path: 24 Poseidon regions stacked sequentially — the
///   tallest single stack in the circuit.
/// - ECC fixed- and variable-base multiplications packed alongside the
///   Poseidon regions in non-overlapping columns.
/// - 10-bit Sinsemilla/range-check lookup table: 1,024 fixed rows.
///
/// The `[v_i]*G` term uses `FixedPointShort` (22-window short-scalar path)
/// rather than `FixedPointBaseField` (85-window full-scalar path), saving
/// 315 rows (3,827 → 3,512 measured). Run the `row_budget` benchmark to
/// re-measure after circuit changes:
///   `cargo test --features vote-proof row_budget -- --nocapture --ignored`
pub const K: u32 = 13;

pub use van_integrity::DOMAIN_VAN;
pub use vote_commitment::DOMAIN_VC;

/// Maximum proposal_id bit index (exclusive upper bound). `proposal_id` is in `[1, MAX_PROPOSAL_ID)`,
/// i.e. valid values are 1–15. Bit 0 is permanently reserved as the sentinel/unset value and is
/// rejected by the non-zero gate in `AuthorityDecrementChip` (`q_cond_6`). This means a voting
/// round supports at most 15 proposals, not 16.
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
pub use vote_commitment::vote_commitment_hash;

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
/// ```text
/// van_nullifier = Poseidon(vsk_nk, domain_tag, voting_round_id, vote_authority_note_old)
/// ```
///
/// Single `ConstantLength<4>` call (2 permutations at rate=2).
/// Used by the builder and tests to compute the expected VAN nullifier.
pub fn van_nullifier_hash(
    vsk_nk: pallas::Base,
    voting_round_id: pallas::Base,
    vote_authority_note_old: pallas::Base,
) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<4>, 3, 2>::init().hash([
        vsk_nk,
        domain_van_nullifier(),
        voting_round_id,
        vote_authority_note_old,
    ])
}

/// Out-of-circuit Poseidon hash of two field elements.
///
/// `Poseidon(a, b)` with P128Pow5T3, ConstantLength<2>, width 3, rate 2.
/// Used for Merkle path computation (condition 1) and tests. This is the
/// same hash function used by `vote_commitment_tree::MerkleHashVote::combine`.
pub fn poseidon_hash_2(a: pallas::Base, b: pallas::Base) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([a, b])
}

/// Out-of-circuit per-share blinded commitment (condition 10).
///
/// Computes `Poseidon(blind, c1_x, c2_x)` for a single share.
/// The blind factor prevents anyone who sees the encrypted shares on-chain
/// from recomputing shares_hash and linking it to a specific vote commitment.
pub fn share_commitment(blind: pallas::Base, c1_x: pallas::Base, c2_x: pallas::Base) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<3>, 3, 2>::init()
        .hash([blind, c1_x, c2_x])
}

/// Out-of-circuit shares hash (condition 10).
///
/// Computes blinded per-share commitments and hashes them together:
/// ```text
/// share_comm_i = Poseidon(blind_i, c1_i_x, c2_i_x)   for i in 0..16
/// shares_hash  = Poseidon(share_comm_0, ..., share_comm_15)
/// ```
///
/// The blind factors prevent anyone who sees the encrypted shares on-chain
/// from recomputing shares_hash and linking it to a specific vote commitment.
///
/// Used by the builder and tests to compute the expected shares hash.
pub fn shares_hash(
    share_blinds: [pallas::Base; 16],
    enc_share_c1_x: [pallas::Base; 16],
    enc_share_c2_x: [pallas::Base; 16],
) -> pallas::Base {
    let comms: [pallas::Base; 16] = core::array::from_fn(|i| {
        share_commitment(share_blinds[i], enc_share_c1_x[i], enc_share_c2_x[i])
    });
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<16>, 3, 2>::init().hash(comms)
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
    /// (16-bit bitmask), and condition 9 to ensure each share is in `[0, 2^30)`.
    range_check: LookupRangeCheckConfig<pallas::Base, 10>,
    /// Merkle conditional swap gate (condition 1).
    ///
    /// At each of the 24 Merkle tree levels, conditionally swaps
    /// (current, sibling) into (left, right) based on the position bit.
    /// Uses advices[0..5]: pos_bit, current, sibling, left, right.
    merkle_swap: MerkleSwapGate,
    /// Configuration for condition 6 (Proposal Authority Decrement).
    authority_decrement: AuthorityDecrementConfig,
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
    // van_comm_core = Poseidon(DOMAIN_VAN, vpk_g_d.x, vpk_pk_d.x, total_note_value,
    //                          voting_round_id, proposal_authority_old);
    // vote_authority_note_old = Poseidon(van_comm_core, van_comm_rand).
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
    // Condition 6:
    /// Remaining proposal authority bitmask in the old VAN.
    pub(crate) proposal_authority_old: Value<pallas::Base>,
    /// Blinding randomness for the VAN commitment.
    pub(crate) van_comm_rand: Value<pallas::Base>,
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
    /// `2^proposal_id`, supplied as a private witness and constrained by a lookup.
    ///
    /// Field arithmetic cannot express variable-exponent exponentiation as a
    /// polynomial gate, so the prover witnesses `one_shifted` directly. The lookup
    /// table `(0,1), (1,2), ..., (15,32768)` then proves `one_shifted == 2^proposal_id`.
    /// The bit-decomposition region uses this value to compute
    /// `proposal_authority_new = proposal_authority_old - one_shifted`.
    pub(crate) one_shifted: Value<pallas::Base>,

    // === Vote commitment construction (conditions 8–12) ===

    // Condition 8 (Shares Sum): sum(shares_1..16) = total_note_value.
    // Condition 9 (Shares Range): each share in [0, 2^30).
    /// Voting share vector (16 random shares that sum to total_note_value).
    /// The decomposition is chosen by the prover for amount privacy: the
    /// on-chain El Gamal ciphertexts reveal no weight fingerprint.
    pub(crate) shares: [Value<pallas::Base>; 16],

    // Condition 10 (Shares Hash Integrity): El Gamal ciphertext x-coordinates.
    // These are the x-coordinates of the curve points comprising each
    // El Gamal ciphertext. Condition 11 constrains these to be correct
    // encryptions; condition 10 hashes them.
    /// X-coordinates of C1_i = r_i * G for each share (via ExtractP).
    pub(crate) enc_share_c1_x: [Value<pallas::Base>; 16],
    /// X-coordinates of C2_i = shares_i * G + r_i * ea_pk for each share (via ExtractP).
    pub(crate) enc_share_c2_x: [Value<pallas::Base>; 16],

    // Condition 10 (Shares Hash Integrity): per-share blind factors for blinded commitments.
    /// Random blind factors: share_comm_i = Poseidon(blind_i, c1_i_x, c2_i_x).
    pub(crate) share_blinds: [Value<pallas::Base>; 16],

    // Condition 11 (Encryption Integrity): El Gamal randomness and public key.
    /// El Gamal encryption randomness for each share (base field element,
    /// converted to scalar via ScalarVar::from_base in-circuit).
    pub(crate) share_randomness: [Value<pallas::Base>; 16],
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
        van_comm_rand: Value<pallas::Base>,
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
            van_comm_rand,
            vote_authority_note_old,
            vsk,
            rivk_v,
            alpha_v,
            vsk_nk,
            ..Default::default()
        }
    }
}

/// In-circuit Poseidon hash for one share commitment: `Poseidon(blind, c1_x, c2_x)`.
///
/// Uses the same parameters as the out-of-circuit [`share_commitment`] (P128Pow5T3,
/// ConstantLength<3>, width 3, rate 2) so that native and in-circuit hashes match.

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
        let merkle_swap = MerkleSwapGate::configure(
            meta,
            [advices[0], advices[1], advices[2], advices[3], advices[4]],
        );

        // Condition 6: Proposal Authority Decrement.
        let authority_decrement = AuthorityDecrementChip::configure(meta, advices);

        Config {
            primary,
            advices,
            poseidon_config,
            add_config,
            ecc_config,
            sinsemilla_config,
            commit_ivk_config,
            range_check,
            merkle_swap,
            authority_decrement,
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
        AuthorityDecrementChip::load_table(&config.authority_decrement, &mut layouter)?;


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
        // Clone for condition 12 (vote commitment integrity) before
        // condition 2 consumes the original via van_integrity_poseidon.
        let voting_round_id_cond12 = voting_round_id.clone();

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

        let van_comm_rand = assign_free_advice(
            layouter.namespace(|| "witness van_comm_rand"),
            config.advices[0],
            self.van_comm_rand,
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
        // - vpk_g_d, vpk_pk_d, total_note_value, voting_round_id,
        //   van_comm_rand, domain_van: also used in condition 7 (new VAN integrity).
        // - total_note_value: also used in condition 8 (shares sum check).
        // - vsk_nk: also used in condition 5 (VAN nullifier).
        let vote_authority_note_old_cond1 = vote_authority_note_old.clone();
        let voting_round_id_cond4 = voting_round_id.clone();
        let domain_van_cond6 = domain_van.clone();
        let vpk_g_d_cond6 = vpk_g_d.clone();
        let vpk_pk_d_cond6 = vpk_pk_d.clone();
        let total_note_value_cond6 = total_note_value.clone();
        let total_note_value_cond8 = total_note_value.clone();
        let voting_round_id_cond6 = voting_round_id.clone();
        let van_comm_rand_cond6 = van_comm_rand.clone();
        let vsk_nk_cond4 = vsk_nk.clone();

        // ---------------------------------------------------------------
        // Condition 2: VAN Integrity (ZKP 1–compatible two-layer hash).
        // van_comm_core = Poseidon(DOMAIN_VAN, vpk_g_d, vpk_pk_d, total_note_value,
        //                          voting_round_id, proposal_authority_old)
        // vote_authority_note_old = Poseidon(van_comm_core, van_comm_rand)
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
            van_comm_rand,
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
        //
        // Uses the shared gadget from orchard::shared_primitives – a 1:1 copy of
        // the upstream Orchard spend authority check:
        //   https://github.com/zcash/orchard/blob/main/src/circuit.rs#L542-L558
        orchard::shared_primitives::spend_authority::prove_spend_authority(
            ecc_chip.clone(),
            layouter.namespace(|| "cond4 spend authority"),
            self.alpha_v,
            &vsk_ak_point,
            config.primary,
            R_VPK_X,
            R_VPK_Y,
        )?;

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
            let root = synthesize_poseidon_merkle_path::<VOTE_COMM_TREE_DEPTH>(
                &config.merkle_swap,
                &config.poseidon_config,
                &mut layouter,
                config.advices[0],
                vote_authority_note_old_cond1,
                self.vote_comm_tree_position,
                self.vote_comm_tree_path,
                "cond1: merkle",
            )?;

            // Bind the computed Merkle root to the VOTE_COMM_TREE_ROOT
            // public input. The verifier checks that the voter's VAN is
            // a leaf in the published vote commitment tree.
            layouter.constrain_instance(
                root.cell(),
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
        // van_nullifier = Poseidon(vsk_nk, domain_tag, voting_round_id, vote_authority_note_old)
        //
        // Single ConstantLength<4> Poseidon hash (2 permutations at rate=2).
        //
        // voting_round_id and vote_authority_note_old are reused from
        // condition 2 via cell equality — these cells flow directly into
        // the Poseidon state without being re-witnessed.
        // ---------------------------------------------------------------

        let van_nullifier = {
            let hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<4>,
                3, // WIDTH
                2, // RATE
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "VAN nullifier Poseidon init"),
            )?;
            hasher.hash(
                layouter.namespace(|| "Poseidon(vsk_nk, domain, round_id, van_old)"),
                [vsk_nk_cond4, domain_van_nf, voting_round_id_cond4, vote_authority_note_old],
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

        // Copy proposal_id from the public instance into an advice cell.
        let proposal_id = layouter.assign_region(
            || "copy proposal_id from instance",
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

        let proposal_authority_new = AuthorityDecrementChip::assign(
            &config.authority_decrement,
            &mut layouter,
            proposal_id.clone(),
            proposal_authority_old,
            self.one_shifted,
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
            van_comm_rand_cond6,
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
        // sum(share_0, ..., share_15) = total_note_value
        //
        // Proves the voting share decomposition is consistent with the
        // total delegated weight. Uses 15 chained AddChip additions:
        //   partial_1  = share_0  + share_1
        //   partial_2  = partial_1  + share_2
        //   ...
        //   shares_sum = partial_14 + share_15
        // Then constrains shares_sum == total_note_value (from condition 2).
        // ---------------------------------------------------------------

        // Witness the 16 plaintext shares. These cells are also used
        // by condition 9 (range check) and condition 11 (El Gamal
        // encryption inputs).
        let share_cells: [_; 16] = (0..16usize)
            .map(|i| assign_free_advice(
                layouter.namespace(|| alloc::format!("witness share_{i}")),
                config.advices[0],
                self.shares[i],
            ))
            .collect::<Result<Vec<_>, _>>()?
            .try_into()
            .expect("always 16 elements");

        // Chain 15 additions: share_0 + share_1 + ... + share_15.
        let shares_sum = share_cells[1..].iter().enumerate().try_fold(
            share_cells[0].clone(),
            |acc, (i, share)| {
                config.add_chip().add(
                    layouter.namespace(|| alloc::format!("shares sum step {}", i + 1)),
                    &acc,
                    share,
                )
            },
        )?;

        // Constrain: shares_sum == total_note_value.
        // This ensures the 16 shares decompose the voter's total delegated
        // weight without creating or destroying value.
        layouter.assign_region(
            || "shares sum == total_note_value",
            |mut region| {
                region.constrain_equal(shares_sum.cell(), total_note_value_cond8.cell())
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
        // share) is still secure: max sum of 16 shares ≈ 17.2B, well within
        // the Pallas field, and the homomorphic tally accumulates over
        // far fewer voters than 2^30.
        //
        // If a share exceeds 2^30 (or wraps around the field, e.g.
        // from underflow), the 3-word decomposition produces a non-zero
        // z_3 running sum, which fails the strict check.
        // ---------------------------------------------------------------

        // Share cells are cloned because copy_check takes ownership;
        // the originals remain available for condition 11 (El Gamal).
        for (i, cell) in share_cells.iter().enumerate() {
            config.range_check_config().copy_check(
                layouter.namespace(|| alloc::format!("share_{i} < 2^30")),
                cell.clone(),
                3,    // num_words: 3 × 10 = 30 bits
                true, // strict: running sum terminates at 0
            )?;
        }

        // ---------------------------------------------------------------
        // Condition 10: Shares Hash Integrity (blinded commitments).
        //
        // share_comm_i = Poseidon(blind_i, c1_i_x, c2_i_x)   for i in 0..16
        // shares_hash  = Poseidon(share_comm_0, ..., share_comm_15)
        //
        // The blind factors prevent anyone who sees the encrypted shares
        // on-chain from recomputing shares_hash and linking it to a
        // specific vote commitment. shares_hash is an internal wire;
        // it is not bound to the instance column. Condition 11 constrains
        // that each (c1_i_x, c2_i_x) is a valid El Gamal encryption of
        // shares_i. Condition 12 computes the full vote commitment
        // H(DOMAIN_VC, voting_round_id, shares_hash, proposal_id, vote_decision) and
        // binds that value to the VOTE_COMMITMENT public input.
        // ---------------------------------------------------------------

        // Witness the 16 blind factors and 32 El Gamal ciphertext x-coordinates.
        let blinds: [AssignedCell<pallas::Base, pallas::Base>; 16] = (0..16)
            .map(|i| {
                assign_free_advice(
                    layouter.namespace(|| alloc::format!("witness share_blind[{i}]")),
                    config.advices[0],
                    self.share_blinds[i],
                )
            })
            .collect::<Result<Vec<_>, _>>()?
            .try_into()
            .expect("always 16 elements");

        // Witness the 16 El Gamal c1 ciphertext x-coordinates.
        let enc_c1: [AssignedCell<pallas::Base, pallas::Base>; 16] = (0..16)
            .map(|i| assign_free_advice(
                layouter.namespace(|| alloc::format!("witness enc_c1_x[{i}]")),
                config.advices[0],
                self.enc_share_c1_x[i],
            ))
            .collect::<Result<Vec<_>, _>>()?
            .try_into()
            .expect("always 16 elements");

        // Witness the 16 El Gamal c2 ciphertext x-coordinates.
        let enc_c2: [AssignedCell<pallas::Base, pallas::Base>; 16] = (0..16)
            .map(|i| assign_free_advice(
                layouter.namespace(|| alloc::format!("witness enc_c2_x[{i}]")),
                config.advices[0],
                self.enc_share_c2_x[i],
            ))
            .collect::<Result<Vec<_>, _>>()?
            .try_into()
            .expect("always 16 elements");

        // Clone for Condition 11 before compute_shares_hash_in_circuit takes ownership.
        let enc_c1_cond11: [AssignedCell<pallas::Base, pallas::Base>; 16] =
            core::array::from_fn(|i| enc_c1[i].clone());
        let enc_c2_cond11: [AssignedCell<pallas::Base, pallas::Base>; 16] =
            core::array::from_fn(|i| enc_c2[i].clone());

        // Compute share_comm_i = Poseidon(blind_i, c1_i, c2_i) for each share,
        // then shares_hash = Poseidon(share_comm_0, ..., share_comm_15).
        let shares_hash = compute_shares_hash_in_circuit(
            || config.poseidon_chip(),
            layouter.namespace(|| "cond10: shares hash"),
            blinds,
            enc_c1,
            enc_c2,
        )?;

        // ---------------------------------------------------------------
        // Condition 11: Encryption Integrity.
        //
        // For each share i: C1_i = [r_i]*G, C2_i = [v_i]*G + [r_i]*ea_pk;
        // ExtractP(C1_i) and ExtractP(C2_i) are constrained to the
        // witnessed enc_share cells. Implemented by the shared
        // circuit::elgamal::prove_elgamal_encryptions gadget.
        // ---------------------------------------------------------------
        {
            let r_cells: [_; 16] = (0..16usize)
                .map(|i| assign_free_advice(
                    layouter.namespace(|| alloc::format!("witness r[{i}]")),
                    config.advices[0],
                    self.share_randomness[i],
                ))
                .collect::<Result<Vec<_>, _>>()?
                .try_into()
                .expect("always 16 elements");

            prove_elgamal_encryptions(
                ecc_chip.clone(),
                layouter.namespace(|| "cond11 El Gamal"),
                "cond11",
                self.ea_pk,
                EaPkInstanceLoc {
                    instance: config.primary,
                    x_row: EA_PK_X,
                    y_row: EA_PK_Y,
                },
                config.advices[0],
                share_cells,
                r_cells,
                enc_c1_cond11,
                enc_c2_cond11,
            )?;
        }

        // ---------------------------------------------------------------
        // Condition 12: Vote Commitment Integrity.
        //
        // vote_commitment = Poseidon(DOMAIN_VC, voting_round_id,
        //                            shares_hash, proposal_id, vote_decision)
        //
        // Binds the voting round, encrypted shares (via shares_hash from
        // condition 10), the proposal choice, and the vote decision into a
        // single commitment with domain separation from VANs (DOMAIN_VC = 1).
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

        // Compute vote_commitment = Poseidon(DOMAIN_VC, voting_round_id,
        //                                    shares_hash, proposal_id, vote_decision).
        let vote_commitment = vote_commitment::vote_commitment_poseidon(
            &config.poseidon_config,
            &mut layouter,
            "cond12",
            domain_vc,
            voting_round_id_cond12,
            shares_hash,
            proposal_id,
            vote_decision,
        )?;

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
    use pasta_curves::arithmetic::CurveAffine;
    use pasta_curves::pallas;
    use rand::rngs::OsRng;

    use orchard::constants::{
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

    /// Computes real El Gamal encryptions for 16 shares.
    ///
    /// Returns `(c1_x, c2_x, randomness, share_blinds, shares_hash_value)` where:
    /// - `c1_x[i]` and `c2_x[i]` are correct ciphertext x-coordinates
    /// - `randomness[i]` is the base field randomness used for each share
    /// - `share_blinds[i]` is the blind factor for each share commitment
    /// - `shares_hash_value` is the blinded Poseidon hash of all shares
    fn encrypt_shares(
        shares: [u64; 16],
        ea_pk: pallas::Point,
    ) -> ([pallas::Base; 16], [pallas::Base; 16], [pallas::Base; 16], [pallas::Base; 16], pallas::Base) {
        let mut c1_x = [pallas::Base::zero(); 16];
        let mut c2_x = [pallas::Base::zero(); 16];
        // Use small deterministic randomness (fits in both Base and Scalar).
        let randomness: [pallas::Base; 16] = core::array::from_fn(|i| {
            pallas::Base::from((i as u64 + 1) * 101)
        });
        // Deterministic blind factors for tests.
        let share_blinds: [pallas::Base; 16] = core::array::from_fn(|i| {
            pallas::Base::from(1001u64 + i as u64)
        });
        for i in 0..16 {
            let (c1, c2) = elgamal_encrypt(
                pallas::Base::from(shares[i]),
                randomness[i],
                ea_pk,
            );
            c1_x[i] = c1;
            c2_x[i] = c2;
        }
        let hash = shares_hash(share_blinds, c1_x, c2_x);
        (c1_x, c2_x, randomness, share_blinds, hash)
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
    /// Computes `H(DOMAIN_VC, voting_round_id, shares_hash, proposal_id, vote_decision)`
    /// and sets `circuit.vote_decision`. Returns the vote_commitment
    /// for use in the Instance. The `proposal_id` must match the
    /// instance's proposal_id so the circuit's condition 12 (which
    /// copies proposal_id from the instance) agrees with the instance.
    fn set_condition_11(
        circuit: &mut Circuit,
        shares_hash_val: pallas::Base,
        proposal_id: u64,
        voting_round_id: pallas::Base,
    ) -> pallas::Base {
        let proposal_id_base = pallas::Base::from(proposal_id);
        let vote_decision = pallas::Base::from(TEST_VOTE_DECISION);
        circuit.vote_decision = Value::known(vote_decision);
        vote_commitment_hash(voting_round_id, shares_hash_val, proposal_id_base, vote_decision)
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

        // total_note_value must be small enough that all 16 shares
        // fit in [0, 2^30) for condition 9's range check.
        let total_note_value = pallas::Base::from(10_000u64);
        let voting_round_id = pallas::Base::random(&mut rng);
        let van_comm_rand = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            vpk_g_d_x,
            vpk_pk_d_x,
            total_note_value,
            voting_round_id,
            proposal_authority_old,
            van_comm_rand,
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
            van_comm_rand,
        );

        // Create shares that sum to total_note_value (conditions 8 + 9).
        // Each share must be in [0, 2^30) for condition 9's range check.
        let shares_u64: [u64; 16] = [625; 16]; // sum = 10000

        // Condition 11: El Gamal encryption of shares under ea_pk.
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let ea_pk_x = *ea_pk_affine.coordinates().unwrap().x();
        let ea_pk_y = *ea_pk_affine.coordinates().unwrap().y();
        let (enc_c1_x, enc_c2_x, randomness, share_blinds, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(vpk_g_d_affine),
            Value::known(vpk_pk_d_affine),
            Value::known(total_note_value),
            Value::known(proposal_authority_old),
            Value::known(van_comm_rand),
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
        circuit.share_blinds = share_blinds.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);

        // Condition 12: vote commitment from shares_hash + proposal + decision.
        let vote_commitment = set_condition_11(&mut circuit, shares_hash_val, proposal_id, voting_round_id);

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

        let shares_u64: [u64; 16] = [625; 16];
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, share_blinds, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        // Use authority 13 (bit 3 set) and one_shifted = 8 so condition 6 is consistent;
        // only condition 2 (VAN hash) should fail due to wrong_van.
        let proposal_authority_old = pallas::Base::from(13u64);
        let van_comm_rand = pallas::Base::random(&mut rng);
        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(vpk_g_d_affine),
            Value::known(vpk_pk_d_affine),
            Value::known(pallas::Base::from(10_000u64)),
            Value::known(proposal_authority_old),
            Value::known(van_comm_rand),
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
        circuit.share_blinds = share_blinds.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, TEST_PROPOSAL_ID, instance.voting_round_id);
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
        let van_comm_rand = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total_note_value, voting_round_id,
            proposal_authority_old, van_comm_rand,
        );
        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_authority_note_old);
        let van_nullifier = van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);
        let one_shifted = pallas::Base::from(1u64 << proposal_id);
        let proposal_authority_new = proposal_authority_old - one_shifted;
        let vote_authority_note_new = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total_note_value, voting_round_id,
            proposal_authority_new, van_comm_rand,
        );

        let shares_u64: [u64; 16] = [625; 16];
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, share_blinds, shares_hash_val) =
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
            Value::known(van_comm_rand),
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
        circuit.share_blinds = share_blinds.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id, voting_round_id);

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
        let van_comm_rand = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            vpk_g_d_x,
            wrong_vpk_pk_d_x,
            total_note_value,
            voting_round_id,
            proposal_authority_old,
            van_comm_rand,
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
            van_comm_rand,
        );

        let shares_u64: [u64; 16] = [625; 16];
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, share_blinds, shares_hash_val) =
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
            Value::known(van_comm_rand),
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
        circuit.share_blinds = share_blinds.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id, voting_round_id);

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
        let van_comm_rand = pallas::Base::random(&mut rng);
        let proposal_id = 0u64; // vote on proposal 0 so one_shifted = 1, new = 4

        let vote_authority_note_old = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total_note_value, voting_round_id,
            proposal_authority_old, van_comm_rand,
        );
        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_authority_note_old);
        let van_nullifier = van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);
        let one_shifted = pallas::Base::from(1u64 << proposal_id);
        let proposal_authority_new = proposal_authority_old - one_shifted;
        let vote_authority_note_new = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total_note_value, voting_round_id,
            proposal_authority_new, van_comm_rand,
        );

        // Use a DIFFERENT vsk_nk in the circuit.
        let wrong_vsk_nk = pallas::Base::random(&mut rng);
        let alpha_v = pallas::Scalar::random(&mut rng);
        let g = pallas::Point::from(spend_auth_g_affine());
        let r_vpk = (g * vsk + g * alpha_v).to_affine();
        let r_vpk_x = *r_vpk.coordinates().unwrap().x();
        let r_vpk_y = *r_vpk.coordinates().unwrap().y();

        // Shares that sum to total_note_value (conditions 8 + 9).
        let shares_u64: [u64; 16] = [625; 16];

        // Condition 11: real El Gamal encryption.
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, share_blinds, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(vpk_g_d_affine),
            Value::known(vpk_pk_d_affine),
            Value::known(total_note_value),
            Value::known(proposal_authority_old),
            Value::known(van_comm_rand),
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
        circuit.share_blinds = share_blinds.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id, voting_round_id);

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
        // proposal_id = 0 is now forbidden (sentinel value); use the next smallest valid id.
        // Authority = 2 = 0b0010 has exactly bit 1 set, so proposal_id = 1 is valid.
        // After decrement: proposal_authority_new = 0 (minimum possible outcome).
        let (circuit, instance) =
            make_test_data_with_authority_and_proposal(pallas::Base::from(2u64), 1);

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

    /// proposal_id = 0 is the dummy sentinel value and must be rejected (Cond 6, gate).
    #[test]
    fn proposal_id_zero_fails() {
        // Authority = 1 = 0b0001 has bit 0 set, so this is otherwise a structurally
        // valid decrement — the only reason it must fail is the non-zero gate.
        let (circuit, instance) =
            make_test_data_with_authority_and_proposal(pallas::Base::one(), 0);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err(), "proposal_id = 0 must be rejected");
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

    /// authority=4 (0b0100, bit 2 set only), proposal_id=1 (bit 1 absent) →
    /// run_selected=0 at the terminal row, so "run_selected = 1" fails.
    /// Uses proposal_id=1 (not 0) to isolate this constraint from the
    /// proposal_id != 0 sentinel gate.
    #[test]
    fn proposal_authority_bit_not_set_fails() {
        let (circuit, instance) =
            make_test_data_with_authority_and_proposal(pallas::Base::from(4u64), 1);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    /// Condition 6 enforces run_sel = 1 (exactly one selector active) at the last bit row;
    /// see CONDITION_6_RUN_SEL_FIX.md. This test runs a valid proof (one selector) and
    /// verifies it passes; a zero-selector witness would be rejected by that gate.
    #[test]
    fn proposal_authority_condition6_run_sel_constraint() {
        let (circuit, instance) =
            make_test_data_with_authority_and_proposal(pallas::Base::from(3u64), 1);

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    // ================================================================
    // Condition 7 (New VAN Integrity) tests
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
        // proposal_id = 0 is now forbidden (sentinel); use proposal_id = 2 (bit 2 is set in 5).
        let proposal_id = 2u64;
        let van_comm_rand = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total_note_value, voting_round_id,
            proposal_authority_old, van_comm_rand,
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
            proposal_authority_new, van_comm_rand,
        );

        let alpha_v = pallas::Scalar::random(&mut rng);
        let g = pallas::Point::from(spend_auth_g_affine());
        let r_vpk = (g * vsk + g * alpha_v).to_affine();
        let r_vpk_x = *r_vpk.coordinates().unwrap().x();
        let r_vpk_y = *r_vpk.coordinates().unwrap().y();

        // Shares that sum to total_note_value (conditions 8 + 9).
        let shares_u64: [u64; 16] = [625; 16];

        // Condition 11: real El Gamal encryption.
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, share_blinds, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(vpk_g_d_affine),
            Value::known(vpk_pk_d_affine),
            Value::known(total_note_value),
            Value::known(proposal_authority_old),
            Value::known(van_comm_rand),
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
        circuit.share_blinds = share_blinds.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id, voting_round_id);

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
    // Condition 8 (Shares Sum Correctness) tests
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
    // Condition 9 (Shares Range) tests
    // ================================================================

    /// A share at the maximum valid value (2^30 - 1) should pass.
    #[test]
    fn shares_range_max_valid() {
        let max_share = pallas::Base::from((1u64 << 30) - 1); // 1,073,741,823
        let total = (0..16).fold(pallas::Base::zero(), |acc, _| acc + max_share);

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
        // proposal_id = 0 is now forbidden (sentinel); use proposal_id = 2 (bit 2 is set in 5).
        let proposal_id = 2u64;
        let van_comm_rand = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total, voting_round_id,
            proposal_authority_old, van_comm_rand,
        );
        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_authority_note_old);
        let van_nullifier = van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);
        let one_shifted = pallas::Base::from(1u64 << proposal_id);
        let proposal_authority_new = proposal_authority_old - one_shifted;
        let vote_authority_note_new = van_integrity_hash(
            vpk_g_d_x, vpk_pk_d_x, total, voting_round_id,
            proposal_authority_new, van_comm_rand,
        );

        // Condition 11: real El Gamal encryption with max-value shares.
        let max_share_u64 = (1u64 << 30) - 1;
        let shares_u64: [u64; 16] = [max_share_u64; 16];
        let (_ea_sk, ea_pk_point, ea_pk_affine) = generate_ea_keypair();
        let (enc_c1_x, enc_c2_x, randomness, share_blinds, shares_hash_val) =
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
            Value::known(van_comm_rand),
            Value::known(vote_authority_note_old),
            Value::known(vsk),
            Value::known(rivk_v),
            Value::known(vsk_nk),
            Value::known(alpha_v),
        );
        circuit.one_shifted = Value::known(one_shifted);
        circuit.shares = [Value::known(max_share); 16];
        circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
        circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
        circuit.share_blinds = share_blinds.map(Value::known);
        circuit.share_randomness = randomness.map(Value::known);
        circuit.ea_pk = Value::known(ea_pk_affine);
        let vc = set_condition_11(&mut circuit, shares_hash_val, proposal_id, voting_round_id);

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
    // Condition 10 (Shares Hash Integrity) tests
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

        let blinds: [pallas::Base; 16] =
            core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let c1_x: [pallas::Base; 16] =
            core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let c2_x: [pallas::Base; 16] =
            core::array::from_fn(|_| pallas::Base::random(&mut rng));

        let h1 = shares_hash(blinds, c1_x, c2_x);
        let h2 = shares_hash(blinds, c1_x, c2_x);
        assert_eq!(h1, h2);

        // Changing any component changes the hash.
        let mut c1_x_alt = c1_x;
        c1_x_alt[2] = pallas::Base::random(&mut rng);
        let h3 = shares_hash(blinds, c1_x_alt, c2_x);
        assert_ne!(h1, h3);

        // Swapping c1 and c2 also changes the hash.
        let h4 = shares_hash(blinds, c2_x, c1_x);
        assert_ne!(h1, h4);

        // Different blinds produce different hash.
        let blinds_alt: [pallas::Base; 16] =
            core::array::from_fn(|_| pallas::Base::random(&mut rng));
        let h5 = shares_hash(blinds_alt, c1_x, c2_x);
        assert_ne!(h1, h5);
    }

    /// Verifies the out-of-circuit share_commitment helper is deterministic
    /// and that input order matters (Poseidon(blind, c1, c2) ≠ Poseidon(blind, c2, c1)).
    #[test]
    fn share_commitment_deterministic() {
        let mut rng = OsRng;
        let blind = pallas::Base::random(&mut rng);
        let c1_x = pallas::Base::random(&mut rng);
        let c2_x = pallas::Base::random(&mut rng);

        let h1 = share_commitment(blind, c1_x, c2_x);
        let h2 = share_commitment(blind, c1_x, c2_x);
        assert_eq!(h1, h2);

        // Swapping c1 and c2 changes the hash.
        let h3 = share_commitment(blind, c2_x, c1_x);
        assert_ne!(h1, h3);

        // Different blind changes the hash.
        let blind_alt = pallas::Base::random(&mut rng);
        let h4 = share_commitment(blind_alt, c1_x, c2_x);
        assert_ne!(h1, h4);
    }

    /// Minimal circuit that computes one share commitment in-circuit and constrains
    /// the result to the instance column. Used to verify the in-circuit hash matches
    /// the native share_commitment.
    #[derive(Clone, Default)]
    struct ShareCommitmentTestCircuit {
        blind: pallas::Base,
        c1_x: pallas::Base,
        c2_x: pallas::Base,
    }

    #[derive(Clone)]
    struct ShareCommitmentTestConfig {
        primary: Column<InstanceColumn>,
        advices: [Column<Advice>; 5],
        poseidon_config: PoseidonConfig<pallas::Base, 3, 2>,
    }

    impl plonk::Circuit<pallas::Base> for ShareCommitmentTestCircuit {
        type Config = ShareCommitmentTestConfig;
        type FloorPlanner = floor_planner::V1;

        fn without_witnesses(&self) -> Self {
            Self::default()
        }

        fn configure(meta: &mut ConstraintSystem<pallas::Base>) -> Self::Config {
            let primary = meta.instance_column();
            meta.enable_equality(primary);
            let advices: [Column<Advice>; 5] = core::array::from_fn(|_| meta.advice_column());
            for col in &advices {
                meta.enable_equality(*col);
            }
            let fixed: [Column<Fixed>; 6] = core::array::from_fn(|_| meta.fixed_column());
            let constants = meta.fixed_column();
            meta.enable_constant(constants);
            let rc_a = fixed[0..3].try_into().unwrap();
            let rc_b = fixed[3..6].try_into().unwrap();
            let poseidon_config = PoseidonChip::configure::<poseidon::P128Pow5T3>(
                meta,
                advices[1..4].try_into().unwrap(),
                advices[4],
                rc_a,
                rc_b,
            );
            ShareCommitmentTestConfig {
                primary,
                advices,
                poseidon_config,
            }
        }

        fn synthesize(
            &self,
            config: Self::Config,
            mut layouter: impl Layouter<pallas::Base>,
        ) -> Result<(), plonk::Error> {
            let blind_cell = assign_free_advice(
                layouter.namespace(|| "blind"),
                config.advices[0],
                Value::known(self.blind),
            )?;
            let c1_cell = assign_free_advice(
                layouter.namespace(|| "c1_x"),
                config.advices[0],
                Value::known(self.c1_x),
            )?;
            let c2_cell = assign_free_advice(
                layouter.namespace(|| "c2_x"),
                config.advices[0],
                Value::known(self.c2_x),
            )?;
            let chip = PoseidonChip::construct(config.poseidon_config.clone());
            let result = hash_share_commitment_in_circuit(
                chip,
                layouter.namespace(|| "share_comm"),
                blind_cell,
                c1_cell,
                c2_cell,
                0,
            )?;
            layouter.constrain_instance(result.cell(), config.primary, 0)?;
            Ok(())
        }
    }

    /// Verifies that the in-circuit share commitment hash matches the native
    /// share_commitment(blind, c1_x, c2_x). The test builds a minimal circuit
    /// that computes the hash and constrains it to the instance column, then
    /// runs MockProver with the native hash as the public input.
    #[test]
    fn hash_share_commitment_in_circuit_matches_native() {
        let mut rng = OsRng;
        let blind = pallas::Base::random(&mut rng);
        let c1_x = pallas::Base::random(&mut rng);
        let c2_x = pallas::Base::random(&mut rng);

        let expected = share_commitment(blind, c1_x, c2_x);
        let circuit = ShareCommitmentTestCircuit {
            blind,
            c1_x,
            c2_x,
        };
        let instance = vec![vec![expected]];
        // K=10 (1024 rows) is enough for one Poseidon(3) region.
        const TEST_K: u32 = 10;
        let prover =
            MockProver::run(TEST_K, &circuit, instance).expect("MockProver::run failed");
        assert_eq!(prover.verify(), Ok(()));
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

        // encrypt_shares uses (i+1)*101 for i in 0..16 → 101, 202, ..., 1616.
        for r in (1u64..=16).map(|i| i * 101) {
            assert!(
                base_to_scalar(pallas::Base::from(r)).is_some(),
                "r = {} must convert for El Gamal",
                r
            );
        }
    }

    // ================================================================
    // Condition 12 (Vote Commitment Integrity) tests
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

        let rid = pallas::Base::random(&mut rng);
        let sh = pallas::Base::random(&mut rng);
        let pid = pallas::Base::from(5u64);
        let dec = pallas::Base::from(1u64);

        let h1 = vote_commitment_hash(rid, sh, pid, dec);
        let h2 = vote_commitment_hash(rid, sh, pid, dec);
        assert_eq!(h1, h2);

        // Changing any input changes the hash.
        let h3 = vote_commitment_hash(rid, sh, pallas::Base::from(6u64), dec);
        assert_ne!(h1, h3);

        // Changing voting_round_id changes the hash.
        let h4 = vote_commitment_hash(pallas::Base::from(999u64), sh, pid, dec);
        assert_ne!(h1, h4);

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

    /// Measures actual rows used by the vote-proof circuit via `CircuitCost::measure`.
    ///
    /// `CircuitCost` runs the floor planner against the circuit and tracks the
    /// highest row offset assigned in any column, giving the real "rows consumed"
    /// number rather than the theoretical 2^K capacity.
    ///
    /// Run with:
    ///   cargo test --features vote-proof row_budget -- --nocapture --ignored
    #[test]
    #[ignore]
    fn row_budget() {
        use std::println;
        use halo2_proofs::dev::CircuitCost;
        use pasta_curves::vesta;

        let (circuit, _) = make_test_data();

        // CircuitCost::measure runs the floor planner and returns layout statistics.
        // Fields are private, so extract them from the Debug representation.
        let cost = CircuitCost::<vesta::Point, _>::measure(K, &circuit);
        let debug = alloc::format!("{cost:?}");

        // Parse max_rows, max_advice_rows, max_fixed_rows from Debug string.
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

        println!("=== vote-proof circuit row budget (K={K}) ===");
        println!("  max_rows (floor-planner high-water mark): {max_rows}");
        println!("  max_advice_rows:                          {max_advice_rows}");
        println!("  max_fixed_rows:                           {max_fixed_rows}");
        println!("  2^K  (total available rows):              {total_available}");
        println!("  headroom:                                 {}", total_available.saturating_sub(max_rows));
        println!("  utilisation:                              {:.1}%",
            100.0 * max_rows as f64 / total_available as f64);
        println!();
        println!("  Full debug: {debug}");

        // ---------------------------------------------------------------
        // Witness-independence check: Circuit::default() (all unknowns)
        // must produce exactly the same layout as the filled circuit.
        // If these differ, the row count depends on witness values and
        // the measurement above cannot be trusted as a production bound.
        // ---------------------------------------------------------------
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

        // ---------------------------------------------------------------
        // VOTE_COMM_TREE_DEPTH sanity check: confirm the circuit constant
        // matches the canonical value in vote_commitment_tree::TREE_DEPTH
        // (24 as of this writing). A mismatch would mean test data uses a
        // shallower tree than production.
        // ---------------------------------------------------------------
        println!("  VOTE_COMM_TREE_DEPTH (circuit constant): {VOTE_COMM_TREE_DEPTH}");

        // ---------------------------------------------------------------
        // Minimum-K probe: find the smallest K at which MockProver passes.
        // Useful for evaluating whether K can be reduced.
        // ---------------------------------------------------------------
        for probe_k in 11u32..=K {
            let (c, inst) = make_test_data();
            match MockProver::run(probe_k, &c, vec![inst.to_halo2_instance()]) {
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
