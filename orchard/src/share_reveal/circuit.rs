//! The Share Reveal circuit implementation (ZKP #3).
//!
//! Proves that a publicly-revealed encrypted share came from a valid,
//! registered vote commitment — without revealing which one. The circuit
//! verifies 5 conditions:
//!
//! - **Condition 1**: VC Membership — Poseidon Merkle path from `vote_commitment`
//!   to `vote_comm_tree_root`.
//! - **Condition 2**: Vote Commitment Integrity — `vote_commitment =
//!   Poseidon(DOMAIN_VC, shares_hash, proposal_id, vote_decision)`.
//! - **Condition 3**: Shares Hash Integrity — `share_comm_i =
//!   Poseidon(blind_i, c1_i_x, c2_i_x)` for each share, then `shares_hash =
//!   Poseidon(share_comm_0, share_comm_1, share_comm_2, share_comm_3, share_comm_4)`.
//! - **Condition 4**: Share Membership — `(enc_share_c1_x, enc_share_c2_x)`
//!   is the `share_index`-th pair from the 5 encrypted shares.
//! - **Condition 5**: Share Nullifier Integrity — `share_nullifier` is
//!   correctly derived via a 4-layer Poseidon chain that includes
//!   `voting_round_id` to prevent cross-round replay.
//!
//! ## Column layout
//!
//! Uses the same Poseidon configuration as ZKP #2:
//! - 10 advice columns (advices\[0..5\] general + Merkle swap, \[5\] partial S-box,
//!   \[6..9\] Poseidon state).
//! - 8 fixed columns for Poseidon round constants + constants.
//! - 1 instance column (7 public inputs).
//! - K = 14 (16,384 rows).

use alloc::vec::Vec;

use halo2_proofs::{
    circuit::{floor_planner, AssignedCell, Layouter, Value},
    plonk::{
        self, Advice, Column, Constraints, ConstraintSystem, Expression, Fixed,
        Instance as InstanceColumn, Selector,
    },
    poly::Rotation,
};
use pasta_curves::{pallas, vesta};

use halo2_gadgets::{
    poseidon::{
        primitives::{self as poseidon, ConstantLength},
        Hash as PoseidonHash, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig,
    },
    utilities::bool_check,
};

use crate::vote_proof::{DOMAIN_VC, VOTE_COMM_TREE_DEPTH};
use crate::shared_primitives::shares_hash::compute_shares_hash_in_circuit;

// ================================================================
// Constants
// ================================================================

/// Circuit size (2^K rows). Same as ZKP #1 and ZKP #2.
pub const K: u32 = 14;

// ================================================================
// Public input offsets (7 field elements).
// ================================================================

/// Public input offset for the share nullifier (prevents double-counting).
const SHARE_NULLIFIER: usize = 0;
/// Public input offset for the revealed share's C1 x-coordinate.
const ENC_SHARE_C1_X: usize = 1;
/// Public input offset for the revealed share's C2 x-coordinate.
const ENC_SHARE_C2_X: usize = 2;
/// Public input offset for the proposal identifier.
const PROPOSAL_ID: usize = 3;
/// Public input offset for the vote decision.
const VOTE_DECISION: usize = 4;
/// Public input offset for the vote commitment tree root.
const VOTE_COMM_TREE_ROOT: usize = 5;
/// Public input offset for the voting round identifier.
///
/// Constrained in-circuit: `voting_round_id` is hashed into the share
/// nullifier (condition 5) to bind it to a specific round. This prevents
/// cross-round proof replay — the commitment tree is global (not per-round),
/// so `vote_comm_tree_root` alone does not provide round scoping. The chain
/// also validates that `voting_round_id` matches an active session (Gov Steps
/// V1 §5.4 "Out-of-circuit checks").
const VOTING_ROUND_ID: usize = 6;

// ================================================================
// Out-of-circuit helpers
// ================================================================

/// Domain separator for share nullifiers, encoded as a Pallas base field element.
///
/// `"share spend"` → 32-byte zero-padded array → `Fp::from_repr`.
/// Must match `helper-server/src/nullifier.rs:25-31` byte-for-byte.
pub fn domain_tag_share_spend() -> pallas::Base {
    use ff::PrimeField;
    let mut bytes = [0u8; 32];
    let tag = b"share spend";
    bytes[..tag.len()].copy_from_slice(tag);
    // Encoding is canonical since the tag is short (top byte is zero).
    pallas::Base::from_repr(bytes).unwrap()
}

/// Out-of-circuit share nullifier hash (condition 5).
///
/// ```text
/// share_nullifier = Poseidon(domain_tag, vote_commitment, share_index, c1_x, c2_x, voting_round_id)
/// ```
///
/// Single `ConstantLength<6>` call (3 permutations at rate=2).
/// The `voting_round_id` input binds the nullifier to a specific
/// voting round, preventing cross-round proof replay (the commitment
/// tree is global, not per-round).
///
/// Matches `helper-server/src/nullifier.rs::derive_share_nullifier`.
pub fn share_nullifier_hash(
    vote_commitment: pallas::Base,
    share_index: pallas::Base,
    c1_x: pallas::Base,
    c2_x: pallas::Base,
    voting_round_id: pallas::Base,
) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<6>, 3, 2>::init().hash([
        domain_tag_share_spend(),
        vote_commitment,
        share_index,
        c1_x,
        c2_x,
        voting_round_id,
    ])
}

// ================================================================
// Config
// ================================================================

/// Configuration for the Share Reveal circuit.
///
/// Holds the Poseidon chip config, the Merkle swap gate selector,
/// and the share multiplexer gate selector. No ECC, Sinsemilla,
/// AddChip, or range check — ZKP #3 is pure Poseidon + multiplexing.
#[derive(Clone, Debug)]
pub struct Config {
    /// Public input column (7 field elements).
    primary: Column<InstanceColumn>,
    /// 10 advice columns for private witness data.
    advices: [Column<Advice>; 10],
    /// Poseidon hash chip configuration.
    poseidon_config: PoseidonConfig<pallas::Base, 3, 2>,
    /// Selector for the Merkle conditional swap gate (condition 1).
    q_merkle_swap: Selector,
    /// Selector for the share multiplexer gate (condition 4).
    ///
    /// Fires on a 2-row block:
    ///   Row 0: sel_0..sel_4 (advices[0..5]), c1_0..c1_3 (advices[5..9]), share_index (advices[9])
    ///   Row 1: c1_4 (advices[0]), c2_0..c2_4 (advices[1..6]), selected_c1 (advices[7]), selected_c2 (advices[8])
    q_share_mux: Selector,
}

impl Config {
    /// Constructs a Poseidon chip from this configuration.
    pub(crate) fn poseidon_chip(&self) -> PoseidonChip<pallas::Base, 3, 2> {
        PoseidonChip::construct(self.poseidon_config.clone())
    }
}

// ================================================================
// Circuit
// ================================================================

/// The Share Reveal circuit (ZKP #3).
///
/// Proves that a publicly-revealed encrypted share came from a valid,
/// registered vote commitment — without revealing which one.
#[derive(Clone, Debug, Default)]
pub struct Circuit {
    // === Condition 1: VC Membership ===
    /// Merkle authentication path (sibling hashes at each tree level).
    pub(crate) vote_comm_tree_path: Value<[pallas::Base; VOTE_COMM_TREE_DEPTH]>,
    /// Leaf position in the vote commitment tree.
    pub(crate) vote_comm_tree_position: Value<u32>,

    // === Condition 2: Vote Commitment Integrity ===
    /// Preimage component: hash of all 5 encrypted shares.
    pub(crate) shares_hash: Value<pallas::Base>,

    // === Condition 3: Shares Hash Integrity (blinded commitments) ===
    /// Per-share blind factors: share_comm_i = Poseidon(blind_i, c1_i_x, c2_i_x).
    pub(crate) share_blinds: [Value<pallas::Base>; 5],
    /// X-coordinates of C1_i = r_i * G for each share (via ExtractP).
    pub(crate) enc_share_c1_x: [Value<pallas::Base>; 5],
    /// X-coordinates of C2_i = shares_i * G + r_i * ea_pk for each share.
    pub(crate) enc_share_c2_x: [Value<pallas::Base>; 5],

    // === Condition 4: Share Membership ===
    /// Which of the 5 shares is being revealed (0..4).
    pub(crate) share_index: Value<pallas::Base>,

    // === Condition 5: Share Nullifier Integrity ===
    /// The vote commitment leaf value (links conditions 1, 2, and 5).
    pub(crate) vote_commitment: Value<pallas::Base>,
}

/// Loads a private witness value into a fresh advice cell.
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
        // 10 advice columns, matching ZKP #2 column layout.
        let advices: [Column<Advice>; 10] = core::array::from_fn(|_| meta.advice_column());
        for col in &advices {
            meta.enable_equality(*col);
        }

        // Instance column for public inputs.
        let primary = meta.instance_column();
        meta.enable_equality(primary);

        // 8 fixed columns shared between Poseidon round constants and
        // general constants.
        let lagrange_coeffs: [Column<Fixed>; 8] =
            core::array::from_fn(|_| meta.fixed_column());
        let rc_a = lagrange_coeffs[2..5].try_into().unwrap();
        let rc_b = lagrange_coeffs[5..8].try_into().unwrap();

        // Enable constants via the first fixed column.
        meta.enable_constant(lagrange_coeffs[0]);

        // Poseidon chip: P128Pow5T3 with width 3, rate 2.
        // State columns: advices[6..9], partial S-box: advices[5].
        let poseidon_config = PoseidonChip::configure::<poseidon::P128Pow5T3>(
            meta,
            advices[6..9].try_into().unwrap(),
            advices[5],
            rc_a,
            rc_b,
        );

        // Merkle conditional swap gate (condition 1).
        // Identical to ZKP #2's q_merkle_swap gate.
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
                    (
                        "swap left",
                        left.clone()
                            - current.clone()
                            - pos_bit.clone() * (sibling.clone() - current.clone()),
                    ),
                    ("swap right", left + right - current - sibling),
                    ("bool_check pos_bit", bool_check(pos_bit)),
                ],
            )
        });

        // Share multiplexer gate (condition 4).
        //
        // Fires on a 2-row block. Row 0 carries the selection bits,
        // C1 candidates, selected_c1, and share_index. Row 1 carries
        // C2 candidates and selected_c2.
        //
        // Constraints:
        // - Each sel_i is boolean.
        // - sel_i can only be 1 when share_index == i.
        // - Exactly one sel_i is 1.
        // - selected_c1 = Σ sel_i * c1_i (row 0).
        // - selected_c2 = Σ sel_i * c2_i (row 1).
        let q_share_mux = meta.selector();
        meta.create_gate("share multiplexer", |meta| {
            let q = meta.query_selector(q_share_mux);

            // Row 0: sel_0..sel_4 (advices[0..5]), c1_0..c1_3 (advices[5..8]), share_index (advices[9])
            let sel_0 = meta.query_advice(advices[0], Rotation::cur());
            let sel_1 = meta.query_advice(advices[1], Rotation::cur());
            let sel_2 = meta.query_advice(advices[2], Rotation::cur());
            let sel_3 = meta.query_advice(advices[3], Rotation::cur());
            let sel_4 = meta.query_advice(advices[4], Rotation::cur());
            let c1_0 = meta.query_advice(advices[5], Rotation::cur());
            let c1_1 = meta.query_advice(advices[6], Rotation::cur());
            let c1_2 = meta.query_advice(advices[7], Rotation::cur());
            let c1_3 = meta.query_advice(advices[8], Rotation::cur());
            let share_index = meta.query_advice(advices[9], Rotation::cur());

            // Row 1: c1_4 (advices[0]), c2_0..c2_4 (advices[1..6]), selected_c1 (advices[7]), selected_c2 (advices[8])
            let c1_4 = meta.query_advice(advices[0], Rotation::next());
            let c2_0 = meta.query_advice(advices[1], Rotation::next());
            let c2_1 = meta.query_advice(advices[2], Rotation::next());
            let c2_2 = meta.query_advice(advices[3], Rotation::next());
            let c2_3 = meta.query_advice(advices[4], Rotation::next());
            let c2_4 = meta.query_advice(advices[5], Rotation::next());
            let selected_c1 = meta.query_advice(advices[7], Rotation::next());
            let selected_c2 = meta.query_advice(advices[8], Rotation::next());

            let one = Expression::Constant(pallas::Base::one());
            let two = Expression::Constant(pallas::Base::from(2));
            let three = Expression::Constant(pallas::Base::from(3));
            let four = Expression::Constant(pallas::Base::from(4));

            Constraints::with_selector(
                q,
                [
                    // Boolean checks for each selection bit.
                    ("bool sel_0", bool_check(sel_0.clone())),
                    ("bool sel_1", bool_check(sel_1.clone())),
                    ("bool sel_2", bool_check(sel_2.clone())),
                    ("bool sel_3", bool_check(sel_3.clone())),
                    ("bool sel_4", bool_check(sel_4.clone())),
                    // Index matching: sel_i * (share_index - i) == 0.
                    (
                        "sel_0 * share_index",
                        sel_0.clone() * share_index.clone(),
                    ),
                    (
                        "sel_1 * (share_index - 1)",
                        sel_1.clone() * (share_index.clone() - one.clone()),
                    ),
                    (
                        "sel_2 * (share_index - 2)",
                        sel_2.clone() * (share_index.clone() - two),
                    ),
                    (
                        "sel_3 * (share_index - 3)",
                        sel_3.clone() * (share_index.clone() - three),
                    ),
                    (
                        "sel_4 * (share_index - 4)",
                        sel_4.clone() * (share_index - four),
                    ),
                    // Exactly one selection bit is 1.
                    (
                        "sum sel == 1",
                        sel_0.clone() + sel_1.clone() + sel_2.clone() + sel_3.clone() + sel_4.clone() - one,
                    ),
                    // C1 multiplexer.
                    (
                        "c1 mux",
                        selected_c1
                            - sel_0.clone() * c1_0
                            - sel_1.clone() * c1_1
                            - sel_2.clone() * c1_2
                            - sel_3.clone() * c1_3
                            - sel_4.clone() * c1_4,
                    ),
                    // C2 multiplexer.
                    (
                        "c2 mux",
                        selected_c2
                            - sel_0 * c2_0
                            - sel_1 * c2_1
                            - sel_2 * c2_2
                            - sel_3 * c2_3
                            - sel_4 * c2_4,
                    ),
                ],
            )
        });

        Config {
            primary,
            advices,
            poseidon_config,
            q_merkle_swap,
            q_share_mux,
        }
    }

    #[allow(non_snake_case)]
    fn synthesize(
        &self,
        config: Self::Config,
        mut layouter: impl Layouter<pallas::Base>,
    ) -> Result<(), plonk::Error> {
        // ---------------------------------------------------------------
        // Witness private inputs.
        // ---------------------------------------------------------------

        let vote_commitment = assign_free_advice(
            layouter.namespace(|| "witness vote_commitment"),
            config.advices[0],
            self.vote_commitment,
        )?;
        // Clone for conditions 2 and 5 (Merkle path in condition 1 copies
        // the cell, so the original reference remains valid).
        let vote_commitment_cond2 = vote_commitment.clone();
        let vote_commitment_cond5 = vote_commitment.clone();

        let shares_hash = assign_free_advice(
            layouter.namespace(|| "witness shares_hash"),
            config.advices[0],
            self.shares_hash,
        )?;
        let shares_hash_cond2 = shares_hash.clone();

        let share_index = assign_free_advice(
            layouter.namespace(|| "witness share_index"),
            config.advices[0],
            self.share_index,
        )?;
        let share_index_cond5 = share_index.clone();

        // Copy proposal_id and vote_decision from instance into advice.
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

        let vote_decision = layouter.assign_region(
            || "copy vote_decision from instance",
            |mut region| {
                region.assign_advice_from_instance(
                    || "vote_decision",
                    config.primary,
                    VOTE_DECISION,
                    config.advices[0],
                    0,
                )
            },
        )?;

        // ---------------------------------------------------------------
        // Witness all 10 encrypted share x-coordinates.
        // ---------------------------------------------------------------

        let enc_c1: [AssignedCell<pallas::Base, pallas::Base>; 5] = {
            let mut cells = Vec::with_capacity(5);
            for i in 0..5 {
                cells.push(assign_free_advice(
                    layouter.namespace(|| alloc::format!("witness enc_share_c1_x[{i}]")),
                    config.advices[0],
                    self.enc_share_c1_x[i],
                )?);
            }
            cells.try_into().unwrap()
        };
        let enc_c2: [AssignedCell<pallas::Base, pallas::Base>; 5] = {
            let mut cells = Vec::with_capacity(5);
            for i in 0..5 {
                cells.push(assign_free_advice(
                    layouter.namespace(|| alloc::format!("witness enc_share_c2_x[{i}]")),
                    config.advices[0],
                    self.enc_share_c2_x[i],
                )?);
            }
            cells.try_into().unwrap()
        };

        // Clone for condition 4 (condition 3's Poseidon consumes them).
        let enc_c1_cond4: [AssignedCell<pallas::Base, pallas::Base>; 5] =
            core::array::from_fn(|i| enc_c1[i].clone());
        let enc_c2_cond4: [AssignedCell<pallas::Base, pallas::Base>; 5] =
            core::array::from_fn(|i| enc_c2[i].clone());

        // ---------------------------------------------------------------
        // Condition 3: Shares Hash Integrity (blinded commitments).
        //
        // share_comm_i = Poseidon(blind_i, c1_i_x, c2_i_x)   for i in 0..5
        // shares_hash  = Poseidon(share_comm_0, ..., share_comm_4)
        //
        // Same hash structure as vote_proof condition 10 (ZKP #2), implemented
        // via the shared shared_primitives::shares_hash gadget.
        // ---------------------------------------------------------------

        // Witness the 5 blind factors.
        let blinds: [AssignedCell<pallas::Base, pallas::Base>; 5] = {
            let mut cells = Vec::with_capacity(5);
            for i in 0..5 {
                cells.push(assign_free_advice(
                    layouter.namespace(|| alloc::format!("witness share_blind[{i}]")),
                    config.advices[0],
                    self.share_blinds[i],
                )?);
            }
            cells.try_into().unwrap()
        };

        let derived_shares_hash = compute_shares_hash_in_circuit(
            || config.poseidon_chip(),
            layouter.namespace(|| "cond3: shares hash"),
            blinds,
            enc_c1,
            enc_c2,
        )?;

        // Constrain derived shares_hash == witnessed shares_hash.
        layouter.assign_region(
            || "cond3: shares_hash equality",
            |mut region| region.constrain_equal(derived_shares_hash.cell(), shares_hash.cell()),
        )?;

        // ---------------------------------------------------------------
        // Condition 2: Vote Commitment Integrity.
        //
        // vote_commitment = Poseidon(DOMAIN_VC, shares_hash,
        //                            proposal_id, vote_decision)
        //
        // Same hash as vote_proof::vote_commitment_hash and
        // vote_commitment_tree::vote_commitment_hash.
        // ---------------------------------------------------------------

        // DOMAIN_VC constant (baked into the VK).
        let domain_vc = layouter.assign_region(
            || "cond2: DOMAIN_VC constant",
            |mut region| {
                region.assign_advice_from_constant(
                    || "domain_vc",
                    config.advices[0],
                    0,
                    pallas::Base::from(DOMAIN_VC),
                )
            },
        )?;

        let derived_vc = {
            let message = [domain_vc, shares_hash_cond2, proposal_id, vote_decision];
            let hasher = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<4>,
                3,
                2,
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "cond2: vote commitment Poseidon init"),
            )?;
            hasher.hash(
                layouter.namespace(|| "cond2: vc = Poseidon(DOMAIN_VC, ...)"),
                message,
            )?
        };

        // Constrain derived vote_commitment == witnessed vote_commitment.
        layouter.assign_region(
            || "cond2: vote_commitment equality",
            |mut region| {
                region.constrain_equal(derived_vc.cell(), vote_commitment_cond2.cell())
            },
        )?;

        // ---------------------------------------------------------------
        // Condition 1: VC Membership.
        //
        // MerklePath(vote_commitment, position, path) = vote_comm_tree_root
        //
        // Poseidon-based Merkle path verification (24 levels). The hash
        // is Poseidon(left, right) with no level tag, matching
        // vote_commitment_tree::MerkleHashVote::combine.
        // ---------------------------------------------------------------
        {
            let mut current = vote_commitment;

            for i in 0..VOTE_COMM_TREE_DEPTH {
                // Witness position bit for this level.
                let pos_bit = assign_free_advice(
                    layouter.namespace(|| alloc::format!("cond1: merkle pos_bit {i}")),
                    config.advices[0],
                    self.vote_comm_tree_position
                        .map(|p| pallas::Base::from(((p >> i) & 1) as u64)),
                )?;

                // Witness sibling hash at this level.
                let sibling = assign_free_advice(
                    layouter.namespace(|| alloc::format!("cond1: merkle sibling {i}")),
                    config.advices[0],
                    self.vote_comm_tree_path.map(|path| path[i]),
                )?;

                // Conditional swap: order (current, sibling) by position bit.
                let (left, right) = layouter.assign_region(
                    || alloc::format!("cond1: merkle swap level {i}"),
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
                        3,
                        2,
                    >::init(
                        config.poseidon_chip(),
                        layouter
                            .namespace(|| alloc::format!("cond1: merkle hash init level {i}")),
                    )?;
                    hasher.hash(
                        layouter.namespace(|| {
                            alloc::format!("cond1: Poseidon(left, right) level {i}")
                        }),
                        [left, right],
                    )?
                };

                current = parent;
            }

            // Bind the computed Merkle root to the public input.
            layouter.constrain_instance(
                current.cell(),
                config.primary,
                VOTE_COMM_TREE_ROOT,
            )?;
        }

        // ---------------------------------------------------------------
        // Condition 4: Share Membership.
        //
        // The q_share_mux gate multiplexes the 5 encrypted share pairs
        // and constrains the selected pair to equal the public inputs
        // ENC_SHARE_C1_X and ENC_SHARE_C2_X.
        // ---------------------------------------------------------------

        let (selected_c1, selected_c2) = layouter.assign_region(
            || "cond4: share multiplexer",
            |mut region| {
                config.q_share_mux.enable(&mut region, 0)?;

                // Compute selection bits from share_index value.
                let sel_values: [Value<pallas::Base>; 5] = core::array::from_fn(|i| {
                    self.share_index.map(|idx| {
                        if idx == pallas::Base::from(i as u64) {
                            pallas::Base::one()
                        } else {
                            pallas::Base::zero()
                        }
                    })
                });

                // Row 0: sel_0..sel_4 (advices[0..5]), c1_0..c1_3 (advices[5..8]), share_index (advices[9])

                region.assign_advice(|| "sel_0", config.advices[0], 0, || sel_values[0])?;
                region.assign_advice(|| "sel_1", config.advices[1], 0, || sel_values[1])?;
                region.assign_advice(|| "sel_2", config.advices[2], 0, || sel_values[2])?;
                region.assign_advice(|| "sel_3", config.advices[3], 0, || sel_values[3])?;
                region.assign_advice(|| "sel_4", config.advices[4], 0, || sel_values[4])?;

                enc_c1_cond4[0].copy_advice(
                    || "c1_0",
                    &mut region,
                    config.advices[5],
                    0,
                )?;
                enc_c1_cond4[1].copy_advice(
                    || "c1_1",
                    &mut region,
                    config.advices[6],
                    0,
                )?;
                enc_c1_cond4[2].copy_advice(
                    || "c1_2",
                    &mut region,
                    config.advices[7],
                    0,
                )?;
                enc_c1_cond4[3].copy_advice(
                    || "c1_3",
                    &mut region,
                    config.advices[8],
                    0,
                )?;

                share_index.copy_advice(
                    || "share_index",
                    &mut region,
                    config.advices[9],
                    0,
                )?;

                // Row 1: c1_4 (advices[0]), c2_0..c2_4 (advices[1..6]), selected_c1 (advices[7]), selected_c2 (advices[8])

                enc_c1_cond4[4].copy_advice(
                    || "c1_4",
                    &mut region,
                    config.advices[0],
                    1,
                )?;
                enc_c2_cond4[0].copy_advice(
                    || "c2_0",
                    &mut region,
                    config.advices[1],
                    1,
                )?;
                enc_c2_cond4[1].copy_advice(
                    || "c2_1",
                    &mut region,
                    config.advices[2],
                    1,
                )?;
                enc_c2_cond4[2].copy_advice(
                    || "c2_2",
                    &mut region,
                    config.advices[3],
                    1,
                )?;
                enc_c2_cond4[3].copy_advice(
                    || "c2_3",
                    &mut region,
                    config.advices[4],
                    1,
                )?;
                enc_c2_cond4[4].copy_advice(
                    || "c2_4",
                    &mut region,
                    config.advices[5],
                    1,
                )?;

                // Compute selected_c1 = Σ sel_i * c1_i.
                let selected_c1_val = sel_values[0]
                    .zip(self.enc_share_c1_x[0])
                    .zip(sel_values[1].zip(self.enc_share_c1_x[1]))
                    .zip(sel_values[2].zip(self.enc_share_c1_x[2]))
                    .zip(sel_values[3].zip(self.enc_share_c1_x[3]))
                    .zip(sel_values[4].zip(self.enc_share_c1_x[4]))
                    .map(|(((((s0, c0), (s1, c1)), (s2, c2)), (s3, c3)), (s4, c4))| {
                        s0 * c0 + s1 * c1 + s2 * c2 + s3 * c3 + s4 * c4
                    });
                let selected_c1 = region.assign_advice(
                    || "selected_c1",
                    config.advices[7],
                    1,
                    || selected_c1_val,
                )?;

                // Compute selected_c2 = Σ sel_i * c2_i.
                let selected_c2_val = sel_values[0]
                    .zip(self.enc_share_c2_x[0])
                    .zip(sel_values[1].zip(self.enc_share_c2_x[1]))
                    .zip(sel_values[2].zip(self.enc_share_c2_x[2]))
                    .zip(sel_values[3].zip(self.enc_share_c2_x[3]))
                    .zip(sel_values[4].zip(self.enc_share_c2_x[4]))
                    .map(|(((((s0, c0), (s1, c1)), (s2, c2)), (s3, c3)), (s4, c4))| {
                        s0 * c0 + s1 * c1 + s2 * c2 + s3 * c3 + s4 * c4
                    });
                let selected_c2 = region.assign_advice(
                    || "selected_c2",
                    config.advices[8],
                    1,
                    || selected_c2_val,
                )?;

                Ok((selected_c1, selected_c2))
            },
        )?;

        // Clone for condition 5 before binding to instance.
        let selected_c1_cond5 = selected_c1.clone();
        let selected_c2_cond5 = selected_c2.clone();

        // Bind selected pair to public inputs.
        layouter.constrain_instance(
            selected_c1.cell(),
            config.primary,
            ENC_SHARE_C1_X,
        )?;
        layouter.constrain_instance(
            selected_c2.cell(),
            config.primary,
            ENC_SHARE_C2_X,
        )?;

        // ---------------------------------------------------------------
        // Condition 5: Share Nullifier Integrity.
        //
        // share_nullifier = Poseidon(domain_tag, vote_commitment, share_index,
        //                            selected_c1, selected_c2, voting_round_id)
        //
        // Single ConstantLength<6> Poseidon hash (3 permutations at rate=2).
        // The voting_round_id input binds the nullifier to the round,
        // preventing cross-round proof replay (the commitment tree is global).
        // Matches helper-server/src/nullifier.rs exactly.
        // ---------------------------------------------------------------
        {
            // Copy voting_round_id from instance into advice.
            let voting_round_id = layouter.assign_region(
                || "cond5: copy voting_round_id from instance",
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

            // "share spend" domain tag — constant-constrained so the
            // value is baked into the verification key.
            let domain_tag = layouter.assign_region(
                || "cond5: DOMAIN_SHARE_SPEND constant",
                |mut region| {
                    region.assign_advice_from_constant(
                        || "domain_share_spend",
                        config.advices[0],
                        0,
                        domain_tag_share_spend(),
                    )
                },
            )?;

            // share_nullifier = Poseidon(domain_tag, vote_commitment, share_index,
            //                            selected_c1, selected_c2, voting_round_id)
            let share_nullifier = {
                let hasher = PoseidonHash::<
                    pallas::Base,
                    _,
                    poseidon::P128Pow5T3,
                    ConstantLength<6>,
                    3,
                    2,
                >::init(
                    config.poseidon_chip(),
                    layouter.namespace(|| "cond5: share nullifier Poseidon init"),
                )?;
                hasher.hash(
                    layouter.namespace(|| "cond5: Poseidon(tag, vc, idx, c1, c2, round_id)"),
                    [domain_tag, vote_commitment_cond5, share_index_cond5,
                     selected_c1_cond5, selected_c2_cond5, voting_round_id],
                )?
            };

            layouter.constrain_instance(
                share_nullifier.cell(),
                config.primary,
                SHARE_NULLIFIER,
            )?;
        }

        Ok(())
    }
}

// ================================================================
// Instance (public inputs)
// ================================================================

/// Public inputs to the Share Reveal circuit (7 field elements).
///
/// These are the values posted to the vote chain that both the prover
/// and verifier agree on. The verifier checks the proof against these
/// values without seeing any private witnesses.
#[derive(Clone, Debug)]
pub struct Instance {
    /// Poseidon nullifier for this share (prevents double-counting).
    pub share_nullifier: pallas::Base,
    /// X-coordinate of the revealed share's El Gamal C1 component.
    pub enc_share_c1_x: pallas::Base,
    /// X-coordinate of the revealed share's El Gamal C2 component.
    pub enc_share_c2_x: pallas::Base,
    /// Which proposal this vote is for.
    pub proposal_id: pallas::Base,
    /// The voter's choice.
    pub vote_decision: pallas::Base,
    /// Root of the vote commitment tree at anchor height.
    pub vote_comm_tree_root: pallas::Base,
    /// The voting round identifier.
    pub voting_round_id: pallas::Base,
}

impl Instance {
    /// Constructs an [`Instance`] from its constituent parts.
    pub fn from_parts(
        share_nullifier: pallas::Base,
        enc_share_c1_x: pallas::Base,
        enc_share_c2_x: pallas::Base,
        proposal_id: pallas::Base,
        vote_decision: pallas::Base,
        vote_comm_tree_root: pallas::Base,
        voting_round_id: pallas::Base,
    ) -> Self {
        Instance {
            share_nullifier,
            enc_share_c1_x,
            enc_share_c2_x,
            proposal_id,
            vote_decision,
            vote_comm_tree_root,
            voting_round_id,
        }
    }

    /// Serializes public inputs for halo2 proof creation/verification.
    ///
    /// The order must match the instance column offsets defined at the
    /// top of this file.
    pub fn to_halo2_instance(&self) -> Vec<vesta::Scalar> {
        alloc::vec![
            self.share_nullifier,
            self.enc_share_c1_x,
            self.enc_share_c2_x,
            self.proposal_id,
            self.vote_decision,
            self.vote_comm_tree_root,
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
    use group::Curve;
    use halo2_proofs::dev::MockProver;
    use pasta_curves::pallas;

    use crate::vote_proof::{
        elgamal_encrypt, poseidon_hash_2, shares_hash as compute_shares_hash,
        spend_auth_g_affine, vote_commitment_hash as compute_vote_commitment_hash,
    };

    /// Generates an El Gamal keypair for testing.
    fn generate_ea_keypair() -> (pallas::Scalar, pallas::Point, pallas::Affine) {
        let ea_sk = pallas::Scalar::from(42u64);
        let g = pallas::Point::from(spend_auth_g_affine());
        let ea_pk = g * ea_sk;
        let ea_pk_affine = ea_pk.to_affine();
        (ea_sk, ea_pk, ea_pk_affine)
    }

    /// Computes real El Gamal encryptions for 5 shares.
    ///
    /// Returns `(c1_x, c2_x, share_blinds, shares_hash_value)`.
    fn encrypt_shares(
        shares: [u64; 5],
        ea_pk: pallas::Point,
    ) -> ([pallas::Base; 5], [pallas::Base; 5], [pallas::Base; 5], pallas::Base) {
        let mut c1_x = [pallas::Base::zero(); 5];
        let mut c2_x = [pallas::Base::zero(); 5];
        let randomness: [pallas::Base; 5] = [
            pallas::Base::from(101u64),
            pallas::Base::from(202u64),
            pallas::Base::from(303u64),
            pallas::Base::from(404u64),
            pallas::Base::from(505u64),
        ];
        let share_blinds: [pallas::Base; 5] = [
            pallas::Base::from(1001u64),
            pallas::Base::from(1002u64),
            pallas::Base::from(1003u64),
            pallas::Base::from(1004u64),
            pallas::Base::from(1005u64),
        ];
        for i in 0..5 {
            let (c1, c2) = elgamal_encrypt(
                pallas::Base::from(shares[i]),
                randomness[i],
                ea_pk,
            );
            c1_x[i] = c1;
            c2_x[i] = c2;
        }
        let hash = compute_shares_hash(share_blinds, c1_x, c2_x);
        (c1_x, c2_x, share_blinds, hash)
    }

    /// Build valid test data for all 5 conditions.
    ///
    /// Constructs a single-leaf Merkle path (position 0) with the vote
    /// commitment as the leaf, real El Gamal ciphertexts, and a valid
    /// share nullifier.
    fn make_test_data(
        share_idx: u32,
    ) -> (Circuit, Instance) {
        let proposal_id = pallas::Base::from(3u64);
        let vote_decision = pallas::Base::from(1u64);
        let voting_round_id = pallas::Base::from(999u64);

        // Encrypt 5 shares.
        let (_ea_sk, ea_pk_point, _ea_pk_affine) = generate_ea_keypair();
        let shares_u64: [u64; 5] = [1_000, 2_000, 3_000, 2_500, 1_500];
        let (enc_c1_x, enc_c2_x, share_blinds, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        // Compute vote commitment.
        let vote_commitment =
            compute_vote_commitment_hash(shares_hash_val, proposal_id, vote_decision);

        // Build single-leaf Merkle path at position 0.
        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_commitment);

        // Derive share nullifier.
        let share_index_fp = pallas::Base::from(share_idx as u64);
        let share_nullifier = share_nullifier_hash(
            vote_commitment,
            share_index_fp,
            enc_c1_x[share_idx as usize],
            enc_c2_x[share_idx as usize],
            voting_round_id,
        );

        let circuit = Circuit {
            vote_comm_tree_path: Value::known(auth_path),
            vote_comm_tree_position: Value::known(position),
            shares_hash: Value::known(shares_hash_val),
            share_blinds: share_blinds.map(Value::known),
            enc_share_c1_x: enc_c1_x.map(Value::known),
            enc_share_c2_x: enc_c2_x.map(Value::known),
            share_index: Value::known(share_index_fp),
            vote_commitment: Value::known(vote_commitment),
        };

        let instance = Instance::from_parts(
            share_nullifier,
            enc_c1_x[share_idx as usize],
            enc_c2_x[share_idx as usize],
            proposal_id,
            vote_decision,
            vote_comm_tree_root,
            voting_round_id,
        );

        (circuit, instance)
    }

    /// Build a Merkle path for a single leaf at position 0.
    ///
    /// All siblings are empty subtree roots (same pattern as
    /// vote_proof tests).
    fn build_single_leaf_merkle_path(
        leaf: pallas::Base,
    ) -> ([pallas::Base; VOTE_COMM_TREE_DEPTH], u32, pallas::Base) {
        // Precompute empty roots at each level.
        let mut empty_roots = [pallas::Base::zero(); VOTE_COMM_TREE_DEPTH];
        empty_roots[0] = poseidon_hash_2(pallas::Base::zero(), pallas::Base::zero());
        for i in 1..VOTE_COMM_TREE_DEPTH {
            empty_roots[i] = poseidon_hash_2(empty_roots[i - 1], empty_roots[i - 1]);
        }

        // Compute root: leaf is at position 0, so it's always the left child.
        let auth_path = empty_roots;
        let mut current = leaf;
        for i in 0..VOTE_COMM_TREE_DEPTH {
            current = poseidon_hash_2(current, auth_path[i]);
        }
        (auth_path, 0, current)
    }

    #[test]
    fn test_share_reveal_valid() {
        let (circuit, instance) = make_test_data(0);
        let prover = MockProver::run(
            K,
            &circuit,
            vec![instance.to_halo2_instance()],
        )
        .unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn test_share_reveal_valid_index_1() {
        let (circuit, instance) = make_test_data(1);
        let prover = MockProver::run(
            K,
            &circuit,
            vec![instance.to_halo2_instance()],
        )
        .unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn test_share_reveal_valid_index_2() {
        let (circuit, instance) = make_test_data(2);
        let prover = MockProver::run(
            K,
            &circuit,
            vec![instance.to_halo2_instance()],
        )
        .unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn test_share_reveal_valid_index_3() {
        let (circuit, instance) = make_test_data(3);
        let prover = MockProver::run(
            K,
            &circuit,
            vec![instance.to_halo2_instance()],
        )
        .unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn test_share_reveal_wrong_merkle_root() {
        let (circuit, mut instance) = make_test_data(0);
        // Corrupt the tree root.
        instance.vote_comm_tree_root = pallas::Base::from(12345u64);
        let prover = MockProver::run(
            K,
            &circuit,
            vec![instance.to_halo2_instance()],
        )
        .unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn test_share_reveal_wrong_nullifier() {
        let (circuit, mut instance) = make_test_data(0);
        // Corrupt the share nullifier.
        instance.share_nullifier = pallas::Base::from(99999u64);
        let prover = MockProver::run(
            K,
            &circuit,
            vec![instance.to_halo2_instance()],
        )
        .unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn test_share_reveal_wrong_share_index() {
        let (circuit, instance) = make_test_data(0);
        // The circuit uses share_index=0, but put index-1's ciphertext
        // in the public input.
        let bad_instance = Instance::from_parts(
            instance.share_nullifier,
            // Use index 1's c1/c2 instead of index 0's:
            pallas::Base::from(999u64), // wrong c1
            pallas::Base::from(888u64), // wrong c2
            instance.proposal_id,
            instance.vote_decision,
            instance.vote_comm_tree_root,
            instance.voting_round_id,
        );
        let prover = MockProver::run(
            K,
            &circuit,
            vec![bad_instance.to_halo2_instance()],
        )
        .unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn test_share_reveal_wrong_vote_decision() {
        let (circuit, mut instance) = make_test_data(0);
        // Corrupt the vote decision. This breaks VC integrity (condition 2)
        // because the hash won't match.
        instance.vote_decision = pallas::Base::from(42u64);
        let prover = MockProver::run(
            K,
            &circuit,
            vec![instance.to_halo2_instance()],
        )
        .unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn test_share_reveal_wrong_voting_round_id() {
        let (circuit, mut instance) = make_test_data(0);
        // Corrupt the voting round id. This breaks condition 5 because
        // voting_round_id is hashed into the nullifier.
        instance.voting_round_id = pallas::Base::from(12345u64);
        let prover = MockProver::run(
            K,
            &circuit,
            vec![instance.to_halo2_instance()],
        )
        .unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn test_share_reveal_domain_tag_matches_server() {
        // Verify that our domain_tag_share_spend() matches the
        // helper-server's encoding.
        use ff::PrimeField;
        let mut bytes = [0u8; 32];
        let tag = b"share spend";
        bytes[..tag.len()].copy_from_slice(tag);
        let server_tag = pallas::Base::from_repr(bytes).unwrap();
        assert_eq!(domain_tag_share_spend(), server_tag);
    }
}
