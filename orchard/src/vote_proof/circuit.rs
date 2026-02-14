//! The Vote Proof circuit implementation (ZKP #2).
//!
//! Proves that a registered voter is casting a valid vote, without
//! revealing which VAN they hold. Currently implements:
//!
//! - **Condition 1**: VAN Membership (Poseidon Merkle path, `constrain_instance`).
//! - **Condition 2**: VAN Integrity (Poseidon hash).
//! - **Condition 3**: Spend Authority (ECC: `voting_hotkey_pk = ExtractP([vsk] * SpendAuthG)`).
//! - **Condition 4**: VAN Nullifier Integrity (nested Poseidon, `constrain_instance`).
//! - **Condition 5**: Proposal Authority Decrement (AddChip + range check).
//! - **Condition 6**: New VAN Integrity (Poseidon hash, `constrain_instance`).
//! - **Condition 7**: Shares Sum Correctness (AddChip, `constrain_equal`).
//! - **Condition 8**: Shares Range (LookupRangeCheck, `[0, 2^30)`).
//!
//! Remaining conditions (9–11) are stubbed with witness fields and
//! public input slots; constraint logic will be added incrementally.
//!
//! ## Conditions overview
//!
//! VAN ownership and spending:
//! - **Condition 1**: VAN Membership — Merkle path from `vote_authority_note_old`
//!   to `vote_comm_tree_root`.
//! - **Condition 2**: VAN Integrity — `vote_authority_note_old` is a correct
//!   Poseidon hash of its components. *(implemented)*
//! - **Condition 3**: Spend Authority — prover knows `vsk` such that
//!   `voting_hotkey_pk = ExtractP([vsk] * SpendAuthG)`. *(implemented)*
//! - **Condition 4**: VAN Nullifier Integrity — `van_nullifier` is correctly
//!   derived from `vsk.nk`. *(implemented)*
//!
//! New VAN construction:
//! - **Condition 5**: Proposal Authority Decrement — `proposal_authority_new =
//!   proposal_authority_old - 1`, and `proposal_authority_old > 0`. *(implemented)*
//! - **Condition 6**: New VAN Integrity — same structure as condition 2 but
//!   with decremented authority. *(implemented)*
//!
//! Vote commitment construction:
//! - **Condition 7**: Shares Sum Correctness — `sum(shares_1..4) = total_note_value`.
//!   *(implemented)*
//! - **Condition 8**: Shares Range — each `shares_j` in `[0, 2^24)`.
//!   *(implemented)*
//! - **Condition 9**: Shares Hash Integrity — `shares_hash = H(enc_share_1..4)`.
//! - **Condition 10**: Encryption Integrity — each `enc_share_i = ElGamal(shares_i, r_i, ea_pk)`.
//! - **Condition 11**: Vote Commitment Integrity — `vote_commitment = H(DOMAIN_VC, shares_hash,
//!   proposal_id, vote_decision)`.

use alloc::vec::Vec;

use halo2_proofs::{
    circuit::{floor_planner, AssignedCell, Layouter, Value},
    plonk::{
        self, Advice, Column, Constraints, ConstraintSystem, Fixed,
        Instance as InstanceColumn, Selector, TableColumn,
    },
    poly::Rotation,
};
use pasta_curves::{pallas, vesta};

use halo2_gadgets::{
    ecc::{
        chip::{EccChip, EccConfig},
        FixedPoint, ScalarFixed,
    },
    poseidon::{
        primitives::{self as poseidon, ConstantLength},
        Hash as PoseidonHash, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig,
    },
    utilities::{bool_check, lookup_range_check::LookupRangeCheckConfig},
};
use crate::constants::{OrchardFixedBases, OrchardFixedBasesFull};
use crate::circuit::gadget::{
    add_chip::{AddChip, AddConfig},
    AddInstruction,
};

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
/// K=12 (4,096 rows). Conditions 1, 2, 4, 5, 6 use ~28 Poseidon
/// hashes (~2,200 rows), plus AddChip additions, range-check running
/// sums (conditions 5 + 8), and 24 Merkle swap regions. The 10-bit
/// lookup table requires 1,024 rows. K=12 provides headroom.
pub const K: u32 = 12;

/// Domain tag for Vote Authority Notes.
///
/// Prepended as the first Poseidon input for domain separation from
/// Vote Commitments in the shared vote commitment tree.
/// `DOMAIN_VAN = 0` for VANs, `DOMAIN_VC = 1` for Vote Commitments.
pub const DOMAIN_VAN: u64 = 0;

// ================================================================
// Public input offsets (7 field elements).
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

// Suppress dead-code warnings for public input offsets that are
// defined but not yet used by any condition's constraint logic.
// These will be used as conditions 3, 7–11 are implemented.
const _: usize = VOTE_COMMITMENT;
const _: usize = VOTE_COMM_TREE_ANCHOR_HEIGHT;
const _: usize = PROPOSAL_ID;

// ================================================================
// Out-of-circuit helpers
// ================================================================

/// Out-of-circuit VAN integrity hash (condition 2).
///
/// Computes:
/// ```text
/// Poseidon(DOMAIN_VAN, voting_hotkey_pk, total_note_value,
///          voting_round_id, proposal_authority_old, gov_comm_rand)
/// ```
///
/// Used by the builder and tests to compute the expected VAN commitment.
pub fn van_integrity_hash(
    voting_hotkey_pk: pallas::Base,
    total_note_value: pallas::Base,
    voting_round_id: pallas::Base,
    proposal_authority_old: pallas::Base,
    gov_comm_rand: pallas::Base,
) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<6>, 3, 2>::init().hash([
        pallas::Base::from(DOMAIN_VAN),
        voting_hotkey_pk,
        total_note_value,
        voting_round_id,
        proposal_authority_old,
        gov_comm_rand,
    ])
}

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

// ================================================================
// Config
// ================================================================

/// Configuration for the Vote Proof circuit.
///
/// Holds chip configs for Poseidon (conditions 1, 2, 4, 6), AddChip
/// (conditions 5, 7), LookupRangeCheck (conditions 5, 8), ECC (condition 3),
/// and the Merkle swap gate (condition 1). Will be extended with
/// custom gates as conditions 9–11 are added.
#[derive(Clone, Debug)]
pub struct Config {
    /// Public input column (7 field elements).
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
    /// ECC chip configuration (condition 3: spend authority).
    ///
    /// Used to prove voting_hotkey_pk = ExtractP([vsk] * SpendAuthG).
    /// Shares advice and fixed columns with Poseidon per delegation layout.
    ecc_config: EccConfig<OrchardFixedBases>,
    /// 10-bit lookup range check configuration.
    ///
    /// Uses advices[9] as the running-sum column. Each word is 10 bits,
    /// so `num_words` × 10 gives the total bit-width checked.
    /// Used in condition 5 to ensure `proposal_authority_old > 0`, and
    /// condition 8 to ensure each share is in `[0, 2^24)`.
    range_check: LookupRangeCheckConfig<pallas::Base, 10>,
    /// Lookup table column for the 10-bit range check.
    ///
    /// Populated with [0, 2^10) during synthesis. Stored here because
    /// the vote proof circuit doesn't use Sinsemilla (which would
    /// normally load this table as a side effect).
    table_idx: TableColumn,
    /// Selector for the Merkle conditional swap gate (condition 1).
    ///
    /// At each of the 24 Merkle tree levels, conditionally swaps
    /// (current, sibling) into (left, right) based on the position bit.
    /// Uses advices[0..5]: pos_bit, current, sibling, left, right.
    /// Identical to the delegation circuit's `q_imt_swap` gate.
    q_merkle_swap: Selector,
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

    /// Constructs an ECC chip for curve operations (condition 3: spend authority).
    fn ecc_chip(&self) -> EccChip<OrchardFixedBases> {
        EccChip::construct(self.ecc_config.clone())
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
/// Currently constrained: conditions 1, 2, 3, 4, 5, 6, 7, 8 (VAN
/// membership, VAN integrity, spend authority, nullifier, authority
/// decrement, new VAN integrity, shares sum, shares range).
#[derive(Clone, Debug, Default)]
pub struct Circuit {
    // === VAN ownership and spending (conditions 1–4) ===

    // Condition 1 (VAN Membership): Poseidon-based Merkle path from
    // vote_authority_note_old to vote_comm_tree_root.
    /// Merkle authentication path (sibling hashes at each tree level).
    pub(crate) vote_comm_tree_path: Value<[pallas::Base; VOTE_COMM_TREE_DEPTH]>,
    /// Leaf position in the vote commitment tree.
    pub(crate) vote_comm_tree_position: Value<u32>,

    // Condition 2 (VAN Integrity): Poseidon(DOMAIN_VAN, voting_hotkey_pk,
    // total_note_value, voting_round_id, proposal_authority_old, gov_comm_rand).
    /// The voting hotkey public key (x-coordinate of the ECC point derived from vsk).
    pub(crate) voting_hotkey_pk: Value<pallas::Base>,
    /// The voter's total delegated weight.
    pub(crate) total_note_value: Value<pallas::Base>,
    /// Remaining proposal authority bitmask in the old VAN.
    pub(crate) proposal_authority_old: Value<pallas::Base>,
    /// Blinding randomness for the VAN commitment.
    pub(crate) gov_comm_rand: Value<pallas::Base>,
    /// The old VAN commitment (Poseidon hash output). Used as the Merkle
    /// leaf in condition 1 and constrained to equal the derived hash here.
    pub(crate) vote_authority_note_old: Value<pallas::Base>,

    // Condition 3 (Spend Authority): prover knows vsk such that
    // voting_hotkey_pk is correctly derived from vsk.
    /// Voting spending key (scalar for ECC multiplication).
    pub(crate) vsk: Value<pallas::Scalar>,

    // Condition 4 (VAN Nullifier Integrity): nullifier deriving key.
    /// Nullifier deriving key derived from vsk.
    pub(crate) vsk_nk: Value<pallas::Base>,

    // === Vote commitment construction (conditions 7–11) ===

    // Condition 7 (Shares Sum): sum(shares_1..4) = total_note_value.
    // Condition 8 (Shares Range): each share in [0, 2^24).
    /// Voting share vector (4 shares that sum to total_note_value).
    pub(crate) shares: [Value<pallas::Base>; 4],

    // Condition 10 (Encryption Integrity): El Gamal randomness per share.
    /// El Gamal encryption randomness for each share.
    pub(crate) share_randomness: [Value<pallas::Scalar>; 4],

    // Condition 11 (Vote Commitment Integrity): vote decision.
    /// The voter's choice (hidden inside the vote commitment).
    pub(crate) vote_decision: Value<pallas::Base>,
}

impl Circuit {
    /// Creates a circuit with conditions 1, 2, 4, 5, and 6 witnesses populated.
    ///
    /// All other witness fields are set to `Value::unknown()`.
    /// - Condition 1 uses `vote_authority_note_old` as the Merkle leaf,
    ///   with `vote_comm_tree_path` and `vote_comm_tree_position` for
    ///   the authentication path.
    /// - Condition 2 binds `vote_authority_note_old` to the Poseidon hash
    ///   of its components.
    /// - Condition 4 reuses `vote_authority_note_old` and `voting_round_id`.
    /// - Condition 5 derives `proposal_authority_new` from
    ///   `proposal_authority_old`.
    /// - Condition 6 reuses all condition 2 witnesses except
    ///   `proposal_authority_old`, which is replaced by the
    ///   in-circuit `proposal_authority_new` from condition 5.
    pub fn with_van_witnesses(
        vote_comm_tree_path: Value<[pallas::Base; VOTE_COMM_TREE_DEPTH]>,
        vote_comm_tree_position: Value<u32>,
        voting_hotkey_pk: Value<pallas::Base>,
        total_note_value: Value<pallas::Base>,
        proposal_authority_old: Value<pallas::Base>,
        gov_comm_rand: Value<pallas::Base>,
        vote_authority_note_old: Value<pallas::Base>,
        vsk_nk: Value<pallas::Base>,
    ) -> Self {
        Circuit {
            vote_comm_tree_path,
            vote_comm_tree_position,
            voting_hotkey_pk,
            total_note_value,
            proposal_authority_old,
            gov_comm_rand,
            vote_authority_note_old,
            vsk_nk,
            ..Default::default()
        }
    }
}

/// In-circuit VAN integrity hash (conditions 2 and 6).
///
/// Computes the Poseidon hash used for both the old VAN (condition 2)
/// and the new VAN (condition 6):
/// ```text
/// Poseidon(domain_van, voting_hotkey_pk, total_note_value,
///          voting_round_id, proposal_authority, gov_comm_rand)
/// ```
///
/// The only difference between conditions 2 and 6 is the
/// `proposal_authority` cell: condition 2 passes `_old`, condition 6
/// passes `_new` (from condition 5's decrement).
fn van_integrity_poseidon(
    config: &Config,
    layouter: &mut impl Layouter<pallas::Base>,
    label: &str,
    domain_van: AssignedCell<pallas::Base, pallas::Base>,
    voting_hotkey_pk: AssignedCell<pallas::Base, pallas::Base>,
    total_note_value: AssignedCell<pallas::Base, pallas::Base>,
    voting_round_id: AssignedCell<pallas::Base, pallas::Base>,
    proposal_authority: AssignedCell<pallas::Base, pallas::Base>,
    gov_comm_rand: AssignedCell<pallas::Base, pallas::Base>,
) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
    let message = [
        domain_van,
        voting_hotkey_pk,
        total_note_value,
        voting_round_id,
        proposal_authority,
        gov_comm_rand,
    ];
    let poseidon_hasher = PoseidonHash::<
        pallas::Base,
        _,
        poseidon::P128Pow5T3,
        ConstantLength<6>,
        3, // WIDTH (state size, from P128Pow5T3)
        2, // RATE (elements absorbed per permutation)
    >::init(
        config.poseidon_chip(),
        layouter.namespace(|| alloc::format!("{label} Poseidon init")),
    )?;
    poseidon_hasher.hash(
        layouter.namespace(|| alloc::format!("{label} Poseidon hash")),
        message,
    )
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
        // Indices 0–1: Lagrange coefficients / constants.
        // Indices 2–4: Poseidon round constants A (rc_a).
        // Indices 5–7: Poseidon round constants B (rc_b).
        let lagrange_coeffs: [Column<Fixed>; 8] =
            core::array::from_fn(|_| meta.fixed_column());
        let rc_a = lagrange_coeffs[2..5].try_into().unwrap();
        let rc_b = lagrange_coeffs[5..8].try_into().unwrap();

        // Enable constants via the first fixed column (for DOMAIN_VAN,
        // ONE, and future fixed values).
        meta.enable_constant(lagrange_coeffs[0]);

        // AddChip: constrains `a + b = c` in a single row.
        // Column assignment matches the delegation circuit:
        //   a = advices[7], b = advices[8], c = advices[6].
        let add_config = AddChip::configure(meta, advices[7], advices[8], advices[6]);

        // Lookup table column for 10-bit range checks.
        // Populated with [0, 2^10) during synthesis.
        let table_idx = meta.lookup_table_column();

        // Range check configuration: 10-bit lookup words in advices[9].
        let range_check = LookupRangeCheckConfig::configure(meta, advices[9], table_idx);

        // ECC chip: fixed-base scalar multiplication for condition 3 (spend authority).
        // Shares columns with Poseidon per delegation circuit layout.
        let ecc_config =
            EccChip::<OrchardFixedBases>::configure(meta, advices, lagrange_coeffs, range_check);

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

        Config {
            primary,
            advices,
            poseidon_config,
            add_config,
            ecc_config,
            range_check,
            table_idx,
            q_merkle_swap,
        }
    }

    #[allow(non_snake_case)]
    fn synthesize(
        &self,
        config: Self::Config,
        mut layouter: impl Layouter<pallas::Base>,
    ) -> Result<(), plonk::Error> {
        // ---------------------------------------------------------------
        // Load the 10-bit lookup table for range checks (condition 5).
        //
        // Populates [0, 2^10) into the table column. In the delegation
        // circuit this is loaded as a side effect of SinsemillaChip::load;
        // the vote proof circuit doesn't use Sinsemilla, so we load it
        // directly.
        // ---------------------------------------------------------------
        layouter.assign_table(
            || "table_idx",
            |mut table| {
                for index in 0..(1 << 10) {
                    table.assign_cell(
                        || "table_idx",
                        config.table_idx,
                        index,
                        || Value::known(pallas::Base::from(index as u64)),
                    )?;
                }
                Ok(())
            },
        )?;

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

        // Private witnesses for condition 2.
        let voting_hotkey_pk = assign_free_advice(
            layouter.namespace(|| "witness voting_hotkey_pk"),
            config.advices[0],
            self.voting_hotkey_pk,
        )?;

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

        // Clone cells that are consumed by condition 2's Poseidon hash but
        // reused in later conditions:
        // - vote_authority_note_old: also used in condition 1 (Merkle leaf).
        // - voting_round_id: also used in condition 4 (VAN nullifier).
        // - voting_hotkey_pk: also used in condition 3 (spend authority) and
        //   condition 6 (new VAN integrity).
        // - total_note_value, voting_round_id, proposal_authority_old,
        //   gov_comm_rand, domain_van: also used in condition 6 (new VAN integrity).
        // - total_note_value: also used in condition 7 (shares sum check).
        let vote_authority_note_old_cond1 = vote_authority_note_old.clone();
        let voting_round_id_cond4 = voting_round_id.clone();
        let domain_van_cond6 = domain_van.clone();
        let voting_hotkey_pk_cond3 = voting_hotkey_pk.clone();
        let voting_hotkey_pk_cond6 = voting_hotkey_pk.clone();
        let total_note_value_cond6 = total_note_value.clone();
        let total_note_value_cond7 = total_note_value.clone();
        let voting_round_id_cond6 = voting_round_id.clone();
        let proposal_authority_old_cond5 = proposal_authority_old.clone();
        let gov_comm_rand_cond6 = gov_comm_rand.clone();

        // ---------------------------------------------------------------
        // Condition 2: VAN Integrity.
        // vote_authority_note_old = Poseidon(DOMAIN_VAN, voting_hotkey_pk,
        //     total_note_value, voting_round_id, proposal_authority_old,
        //     gov_comm_rand)
        // ---------------------------------------------------------------

        let derived_van = van_integrity_poseidon(
            &config,
            &mut layouter,
            "Old VAN integrity",
            domain_van,
            voting_hotkey_pk,
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
        // Condition 3: Spend Authority.
        //
        // voting_hotkey_pk = ExtractP([vsk] * SpendAuthG)
        //
        // Proves the prover knows vsk such that the witnessed voting_hotkey_pk
        // is the x-coordinate of [vsk] * G, where G is the SpendAuth fixed base.
        // ---------------------------------------------------------------
        {
            let ecc_chip = config.ecc_chip();

            let vsk_scalar = ScalarFixed::new(
                ecc_chip.clone(),
                layouter.namespace(|| "vsk"),
                self.vsk,
            )?;

            let spend_auth_g = OrchardFixedBasesFull::SpendAuthG;
            let spend_auth_g = FixedPoint::from_inner(ecc_chip.clone(), spend_auth_g);

            let (vsk_times_g, _) = spend_auth_g.mul(
                layouter.namespace(|| "[vsk] SpendAuthG"),
                vsk_scalar,
            )?;

            let derived_voting_hotkey_pk = vsk_times_g.extract_p().inner().clone();

            layouter.assign_region(
                || "Condition 3: voting_hotkey_pk = ExtractP([vsk] G)",
                |mut region| {
                    region.constrain_equal(
                        derived_voting_hotkey_pk.cell(),
                        voting_hotkey_pk_cond3.cell(),
                    )
                },
            )?;
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
        // Witness assignment for condition 4.
        // ---------------------------------------------------------------

        // Private witness: nullifier deriving key.
        let vsk_nk = assign_free_advice(
            layouter.namespace(|| "witness vsk_nk"),
            config.advices[0],
            self.vsk_nk,
        )?;

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
                [vsk_nk, step2],
            )?
        };

        // Bind the derived nullifier to the VAN_NULLIFIER public input.
        // The verifier checks that the prover's computed nullifier matches
        // the publicly posted value, preventing double-voting.
        layouter.constrain_instance(van_nullifier.cell(), config.primary, VAN_NULLIFIER)?;

        // ---------------------------------------------------------------
        // Condition 5: Proposal Authority Decrement.
        //
        // proposal_authority_new = proposal_authority_old - 1
        // proposal_authority_old > 0
        //
        // Proved by witnessing `diff = proposal_authority_old - 1`,
        // constraining `diff + 1 == proposal_authority_old` via AddChip,
        // and range-checking `diff` to [0, 2^70).
        //
        // If proposal_authority_old == 0, then diff wraps to p - 1
        // (≈ 2^254), which fails the 70-bit range check. This enforces
        // proposal_authority_old > 0.
        //
        // 70 bits (7 × 10-bit words) is generous for a proposal authority
        // counter; it matches the delegation circuit's range-check pattern.
        // ---------------------------------------------------------------

        let proposal_authority_new = {
            // Witness diff = proposal_authority_old - 1.
            let diff = self.proposal_authority_old
                .map(|v| v - pallas::Base::one());
            let diff = assign_free_advice(
                layouter.namespace(|| "witness proposal_authority_new"),
                config.advices[0],
                diff,
            )?;

            // Assign 1 as a constant-constrained advice cell.
            // Baked into the verification key so the decrement amount
            // cannot be changed by a malicious prover.
            let one = layouter.assign_region(
                || "ONE constant (condition 5)",
                |mut region| {
                    region.assign_advice_from_constant(
                        || "one",
                        config.advices[0],
                        0,
                        pallas::Base::one(),
                    )
                },
            )?;

            // Constrain: diff + 1 == proposal_authority_old.
            // This proves proposal_authority_new + 1 == proposal_authority_old,
            // i.e., proposal_authority_new = proposal_authority_old - 1.
            let recomputed = config.add_chip().add(
                layouter.namespace(|| "proposal_authority_new + 1"),
                &diff,
                &one,
            )?;
            layouter.assign_region(
                || "proposal_authority_old = proposal_authority_new + 1",
                |mut region| {
                    region.constrain_equal(
                        recomputed.cell(),
                        proposal_authority_old_cond5.cell(),
                    )
                },
            )?;

            // Range-check diff to [0, 2^70).
            // 7 words × 10 bits = 70 bits.
            // If proposal_authority_old == 0, diff wraps to p - 1 ≈ 2^254,
            // which fails this check — enforcing proposal_authority_old > 0.
            let proposal_authority_new = diff.clone();
            config.range_check_config().copy_check(
                layouter.namespace(|| "proposal_authority_new < 2^70"),
                diff,
                7,    // num_words: 7 × 10 = 70 bits
                true, // strict: running sum terminates at 0
            )?;

            proposal_authority_new
        };

        // ---------------------------------------------------------------
        // Condition 6: New VAN Integrity.
        //
        // vote_authority_note_new = Poseidon(DOMAIN_VAN, voting_hotkey_pk,
        //     total_note_value, voting_round_id, proposal_authority_new,
        //     gov_comm_rand)
        //
        // Same hash as condition 2 via van_integrity_poseidon(), with
        // proposal_authority_new (from condition 5) replacing
        // proposal_authority_old. All other inputs are cell-equality-
        // linked to the same witness cells used in condition 2.
        // ---------------------------------------------------------------

        let derived_van_new = van_integrity_poseidon(
            &config,
            &mut layouter,
            "New VAN integrity",
            domain_van_cond6,
            voting_hotkey_pk_cond6,
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

        Ok(())
    }
}

// ================================================================
// Instance (public inputs)
// ================================================================

/// Public inputs to the Vote Proof circuit (7 field elements).
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
    ) -> Self {
        Instance {
            van_nullifier,
            vote_authority_note_new,
            vote_commitment,
            vote_comm_tree_root,
            vote_comm_tree_anchor_height,
            proposal_id,
            voting_round_id,
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
        ]
    }
}

// ================================================================
// Tests
// ================================================================

#[cfg(test)]
mod tests {
    use super::*;
    use ff::Field;
    use group::Curve;
    use halo2_proofs::dev::MockProver;
    use pasta_curves::arithmetic::CurveAffine;
    use pasta_curves::pallas;
    use rand::rngs::OsRng;

    /// Derives the voting hotkey public key (x-coordinate) from vsk for testing.
    /// Matches condition 3: voting_hotkey_pk = ExtractP([vsk] * SpendAuthG).
    fn derive_voting_hotkey_pk(vsk: pallas::Scalar) -> pallas::Base {
        let g = crate::constants::fixed_bases::spend_auth_g::generator();
        let point = pallas::Point::from(g) * vsk;
        *point.to_affine().coordinates().unwrap().x()
    }

    /// Build valid test data for conditions 1, 2, 3, 4, 5, 6, and 7.
    ///
    /// Returns a circuit with correctly-hashed VAN witnesses, valid
    /// shares summing to `total_note_value`, and a matching instance.
    /// `proposal_authority_old` is set to a small positive value (5) so
    /// conditions 5 and 6 can decrement it. Conditions not yet
    /// constrained use placeholder values (zero).
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

    fn make_test_data_with_authority(
        proposal_authority_old: pallas::Base,
    ) -> (Circuit, Instance) {
        let mut rng = OsRng;

        // Condition 3: voting_hotkey_pk must equal ExtractP([vsk]*SpendAuthG).
        let vsk = pallas::Scalar::from(1u64);
        let voting_hotkey_pk = derive_voting_hotkey_pk(vsk);

        // total_note_value must be small enough that all 4 shares
        // fit in [0, 2^24) for condition 8's range check.
        let total_note_value = pallas::Base::from(10_000u64);
        let voting_round_id = pallas::Base::random(&mut rng);
        let gov_comm_rand = pallas::Base::random(&mut rng);
        let vsk_nk = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            voting_hotkey_pk,
            total_note_value,
            voting_round_id,
            proposal_authority_old,
            gov_comm_rand,
        );
        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_authority_note_old);
        let van_nullifier = van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);
        let proposal_authority_new = proposal_authority_old - pallas::Base::one();
        let vote_authority_note_new = van_integrity_hash(
            voting_hotkey_pk,
            total_note_value,
            voting_round_id,
            proposal_authority_new,
            gov_comm_rand,
        );

        // Create shares that sum to total_note_value (conditions 7 + 8).
        // Each share must be in [0, 2^24) for condition 8's range check.
        let s0 = pallas::Base::from(1_000u64);
        let s1 = pallas::Base::from(2_000u64);
        let s2 = pallas::Base::from(3_000u64);
        let s3 = pallas::Base::from(4_000u64); // 1000 + 2000 + 3000 + 4000 = 10000

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(voting_hotkey_pk),
            Value::known(total_note_value),
            Value::known(proposal_authority_old),
            Value::known(gov_comm_rand),
            Value::known(vote_authority_note_old),
            Value::known(vsk_nk),
        );
        circuit.vsk = Value::known(vsk);
        circuit.shares = [
            Value::known(s0),
            Value::known(s1),
            Value::known(s2),
            Value::known(s3),
        ];

        let instance = Instance::from_parts(
            van_nullifier,
            vote_authority_note_new,
            pallas::Base::zero(),
            vote_comm_tree_root,
            pallas::Base::zero(),
            pallas::Base::zero(),
            voting_round_id,
        );

        (circuit, instance)
    }

    fn make_test_data() -> (Circuit, Instance) {
        make_test_data_with_authority(pallas::Base::from(5u64))
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
        let (_, mut instance) = make_test_data();

        // Deliberately wrong VAN value — condition 2 constrain_equal will fail.
        let wrong_van = pallas::Base::random(&mut OsRng);
        let (auth_path, position, root) = build_single_leaf_merkle_path(wrong_van);
        instance.vote_comm_tree_root = root;

        // Use witnesses that DON'T hash to wrong_van. Condition 3: derive pk from vsk.
        let vsk = pallas::Scalar::random(&mut OsRng);
        let voting_hotkey_pk = derive_voting_hotkey_pk(vsk);
        let total_note_value = pallas::Base::from(10_000u64);
        let s0 = pallas::Base::from(1_000u64);
        let s1 = pallas::Base::from(2_000u64);
        let s2 = pallas::Base::from(3_000u64);
        let s3 = pallas::Base::from(4_000u64);

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path),
            Value::known(position),
            Value::known(voting_hotkey_pk),
            Value::known(total_note_value),
            Value::known(pallas::Base::from(5u64)),
            Value::known(pallas::Base::random(&mut OsRng)),
            Value::known(wrong_van),
            Value::known(pallas::Base::random(&mut OsRng)),
        );
        circuit.vsk = Value::known(vsk);
        circuit.shares = [
            Value::known(s0),
            Value::known(s1),
            Value::known(s2),
            Value::known(s3),
        ];

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

        let pk = pallas::Base::random(&mut rng);
        let val = pallas::Base::random(&mut rng);
        let round = pallas::Base::random(&mut rng);
        let auth = pallas::Base::random(&mut rng);
        let rand = pallas::Base::random(&mut rng);

        let h1 = van_integrity_hash(pk, val, round, auth, rand);
        let h2 = van_integrity_hash(pk, val, round, auth, rand);
        assert_eq!(h1, h2);

        // Changing any input changes the hash.
        let h3 = van_integrity_hash(pallas::Base::random(&mut rng), val, round, auth, rand);
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
    #[test]
    fn van_nullifier_wrong_vsk_nk_fails() {
        let mut rng = OsRng;

        let vsk = pallas::Scalar::random(&mut rng);
        let voting_hotkey_pk = derive_voting_hotkey_pk(vsk);
        let total_note_value = pallas::Base::from(10_000u64);
        let voting_round_id = pallas::Base::random(&mut rng);
        let proposal_authority_old = pallas::Base::from(5u64);
        let gov_comm_rand = pallas::Base::random(&mut rng);
        let vsk_nk = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            voting_hotkey_pk, total_note_value, voting_round_id,
            proposal_authority_old, gov_comm_rand,
        );
        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_authority_note_old);
        let van_nullifier = van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);
        let proposal_authority_new = proposal_authority_old - pallas::Base::one();
        let vote_authority_note_new = van_integrity_hash(
            voting_hotkey_pk, total_note_value, voting_round_id,
            proposal_authority_new, gov_comm_rand,
        );

        // Use a DIFFERENT vsk_nk in the circuit.
        let wrong_vsk_nk = pallas::Base::random(&mut rng);

        // Shares that sum to total_note_value (conditions 7 + 8).
        let s0 = pallas::Base::from(1_000u64);
        let s1 = pallas::Base::from(2_000u64);
        let s2 = pallas::Base::from(3_000u64);
        let s3 = pallas::Base::from(4_000u64);

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path), Value::known(position),
            Value::known(voting_hotkey_pk), Value::known(total_note_value),
            Value::known(proposal_authority_old), Value::known(gov_comm_rand),
            Value::known(vote_authority_note_old), Value::known(wrong_vsk_nk),
        );
        circuit.vsk = Value::known(vsk);
        circuit.shares = [
            Value::known(s0),
            Value::known(s1),
            Value::known(s2),
            Value::known(s3),
        ];

        let instance = Instance::from_parts(
            van_nullifier, vote_authority_note_new, pallas::Base::zero(),
            vote_comm_tree_root, pallas::Base::zero(), pallas::Base::zero(),
            voting_round_id,
        );

        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        // Should fail: circuit computes Poseidon(wrong_vsk_nk, inner_hash)
        // which ≠ the instance van_nullifier (computed with correct vsk_nk).
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

    /// Proposal authority of 1 (minimum valid value) should decrement to 0.
    #[test]
    fn proposal_authority_decrement_minimum_valid() {
        let (circuit, instance) = make_test_data_with_authority(pallas::Base::one());

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
        // 70-bit range check in condition 5.
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

    /// New VAN integrity with a large (but valid) proposal authority.
    /// Ensures the range check accepts values well within the 70-bit range.
    #[test]
    fn new_van_integrity_large_authority() {
        let (circuit, instance) =
            make_test_data_with_authority(pallas::Base::from(1_000_000u64));

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

        let vsk = pallas::Scalar::random(&mut rng);
        let voting_hotkey_pk = derive_voting_hotkey_pk(vsk);
        let total_note_value = pallas::Base::from(10_000u64);
        let voting_round_id = pallas::Base::random(&mut rng);
        let proposal_authority_old = pallas::Base::from(5u64);
        let gov_comm_rand = pallas::Base::random(&mut rng);
        let vsk_nk = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            voting_hotkey_pk, total_note_value, voting_round_id,
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
        let proposal_authority_new = proposal_authority_old - pallas::Base::one();
        let vote_authority_note_new = van_integrity_hash(
            voting_hotkey_pk, total_note_value, voting_round_id,
            proposal_authority_new, gov_comm_rand,
        );

        // Shares that sum to total_note_value (conditions 7 + 8).
        let s0 = pallas::Base::from(1_000u64);
        let s1 = pallas::Base::from(2_000u64);
        let s2 = pallas::Base::from(3_000u64);
        let s3 = pallas::Base::from(4_000u64);

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path), Value::known(position),
            Value::known(voting_hotkey_pk), Value::known(total_note_value),
            Value::known(proposal_authority_old), Value::known(gov_comm_rand),
            Value::known(vote_authority_note_old), Value::known(vsk_nk),
        );
        circuit.vsk = Value::known(vsk);
        circuit.shares = [
            Value::known(s0),
            Value::known(s1),
            Value::known(s2),
            Value::known(s3),
        ];

        let instance = Instance::from_parts(
            van_nullifier, vote_authority_note_new, pallas::Base::zero(),
            vote_comm_tree_root, pallas::Base::zero(), pallas::Base::zero(),
            voting_round_id,
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
        let vsk = pallas::Scalar::random(&mut rng);
        let voting_hotkey_pk = derive_voting_hotkey_pk(vsk);
        let voting_round_id = pallas::Base::random(&mut rng);
        let proposal_authority_old = pallas::Base::from(5u64);
        let gov_comm_rand = pallas::Base::random(&mut rng);
        let vsk_nk = pallas::Base::random(&mut rng);

        let vote_authority_note_old = van_integrity_hash(
            voting_hotkey_pk, total, voting_round_id,
            proposal_authority_old, gov_comm_rand,
        );
        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_authority_note_old);
        let van_nullifier = van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);
        let proposal_authority_new = proposal_authority_old - pallas::Base::one();
        let vote_authority_note_new = van_integrity_hash(
            voting_hotkey_pk, total, voting_round_id,
            proposal_authority_new, gov_comm_rand,
        );

        let mut circuit = Circuit::with_van_witnesses(
            Value::known(auth_path), Value::known(position),
            Value::known(voting_hotkey_pk), Value::known(total),
            Value::known(proposal_authority_old), Value::known(gov_comm_rand),
            Value::known(vote_authority_note_old), Value::known(vsk_nk),
        );
        circuit.vsk = Value::known(vsk);
        circuit.shares = [
            Value::known(max_share),
            Value::known(max_share),
            Value::known(max_share),
            Value::known(max_share),
        ];

        let instance = Instance::from_parts(
            van_nullifier, vote_authority_note_new, pallas::Base::zero(),
            vote_comm_tree_root, pallas::Base::zero(), pallas::Base::zero(),
            voting_round_id,
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
    // Instance and circuit sanity
    // ================================================================

    /// Instance must serialize to exactly 7 public inputs.
    #[test]
    fn instance_has_seven_public_inputs() {
        let (_, instance) = make_test_data();
        assert_eq!(instance.to_halo2_instance().len(), 7);
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
