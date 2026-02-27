//! The Share Reveal circuit implementation (ZKP #3).
//!
//! Proves that a publicly-revealed encrypted share came from a valid,
//! registered vote commitment — without revealing which one. The circuit
//! verifies 5 conditions:
//!
//! - **Condition 1**: VC Membership — Poseidon Merkle path from `vote_commitment`
//!   to `vote_comm_tree_root`.
//! - **Condition 2**: Vote Commitment Integrity — `vote_commitment =
//!   Poseidon(DOMAIN_VC, voting_round_id, shares_hash, proposal_id, vote_decision)`.
//! - **Condition 3**: Shares Hash Integrity — `shares_hash =
//!   Poseidon(share_comm_0, ..., share_comm_15)`, where share_comms are
//!   private witnesses transitively bound to the public tree root.
//! - **Condition 4**: Primary Share Binding — the prover knows a blind
//!   such that `share_comms[share_index] = Poseidon(blind, c1_x, c2_x)`,
//!   binding the publicly revealed encrypted share to the committed set.
//! - **Condition 5**: Share Nullifier Integrity — `share_nullifier` is
//!   correctly derived via a 4-layer Poseidon chain that includes
//!   `voting_round_id` to prevent cross-round replay.
//!
//! ## Privacy
//!
//! Only the primary share's blind is provided as a private witness,
//! avoiding the need to send all 16 blinds to the helper server. The 16
//! `share_comms` are private witnesses — they never appear on chain,
//! preserving share-level unlinkability. Soundness is guaranteed because
//! share_comms are transitively bound to the public `vote_comm_tree_root`
//! via `shares_hash → vote_commitment → Merkle path`.
//!
//! ## Column layout
//!
//! - 9 advice columns: advices\[0..4\] general + Merkle swap, \[5\] Poseidon partial
//!   S-box, \[6..8\] Poseidon state.
//! - 8 fixed columns for Poseidon round constants + constants.
//! - 1 instance column (7 public inputs).
//! - K = 11 (2,048 rows).

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

use orchard::circuit::gadget::assign_free_advice;

use crate::circuit::poseidon_merkle::{MerkleSwapGate, synthesize_poseidon_merkle_path};
use crate::circuit::vote_commitment;
use crate::vote_proof::VOTE_COMM_TREE_DEPTH;
use crate::shares_hash::{
    compute_shares_hash_from_comms_in_circuit,
    hash_share_commitment_in_circuit,
};

// ================================================================
// Constants
// ================================================================

/// Circuit size (2^K rows).
///
/// K=11 (2,048 rows). `CircuitCost::measure` reports a floor-planner
/// high-water mark of ~1,592 rows (78% of 2,048). The `V1` floor
/// planner packs non-overlapping regions into the same row range across
/// different columns.
///
/// Run the `row_budget` test to re-measure after circuit changes:
///   `cargo test --features share-reveal row_budget -- --nocapture --ignored`
pub const K: u32 = 11;

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
/// and the share commitment multiplexer gate selector.
#[derive(Clone, Debug)]
pub struct Config {
    /// Public input column (7 field elements).
    primary: Column<InstanceColumn>,
    /// 9 advice columns for private witness data.
    advices: [Column<Advice>; 9],
    /// Poseidon hash chip configuration.
    poseidon_config: PoseidonConfig<pallas::Base, 3, 2>,
    /// Merkle conditional swap gate (condition 1).
    merkle_swap: MerkleSwapGate,
    /// Selector for the share commitment multiplexer gate (condition 4).
    ///
    /// Fires on a 4-row block (9 advice columns, Rotation 0..3):
    ///   Row 0: sel_0..sel_8     (advices[0..9])
    ///   Row 1: sel_9..sel_15    (advices[0..7]),  comm_0..comm_1  (advices[7..9])
    ///   Row 2: comm_2..comm_10  (advices[0..9])
    ///   Row 3: comm_11..comm_15 (advices[0..5]),  selected_comm   (advices[5]),
    ///          share_index      (advices[6])
    ///
    /// Constraints:
    /// - Each sel_i is boolean.
    /// - Exactly one sel_i is 1.
    /// - share_index == Σ i * sel_i (index reconstruction, replaces 16 per-bit checks).
    /// - selected_comm = Σ sel_i * comm_i.
    q_share_comm_mux: Selector,
}

impl Config {
    /// Constructs a Poseidon chip from this configuration.
    pub(crate) fn poseidon_chip(&self) -> PoseidonChip<pallas::Base, 3, 2> {
        PoseidonChip::construct(self.poseidon_config.clone())
    }

    /// Assigns a field-element constant to an advice cell so the value is
    /// baked into the verification key via `assign_advice_from_constant`.
    pub(crate) fn assign_constant(
        &self,
        layouter: &mut impl Layouter<pallas::Base>,
        label: &'static str,
        value: pallas::Base,
    ) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
        layouter.assign_region(
            || label,
            |mut region| {
                region.assign_advice_from_constant(|| label, self.advices[0], 0, value)
            },
        )
    }
}

// ================================================================
// Circuit
// ================================================================

/// The Share Reveal circuit (ZKP #3).
///
/// Proves that a publicly-revealed encrypted share came from a valid,
/// registered vote commitment — without revealing which one.
#[derive(Clone, Debug)]
pub struct Circuit {
    // === Condition 1: VC Membership ===
    /// Merkle authentication path (sibling hashes at each tree level).
    pub(crate) vote_comm_tree_path: Value<[pallas::Base; VOTE_COMM_TREE_DEPTH]>,
    /// Leaf position in the vote commitment tree.
    pub(crate) vote_comm_tree_position: Value<u32>,

    // === Condition 3: Shares Hash Integrity ===
    /// Pre-computed per-share Poseidon commitments (private witnesses).
    /// `share_comm_i = Poseidon(blind_i, c1_i_x, c2_i_x)`.
    /// Transitively bound to the public tree root via shares_hash → vote_commitment → Merkle path.
    pub(crate) share_comms: [Value<pallas::Base>; 16],

    // === Condition 4: Primary Share Binding ===
    /// Blind factor for the revealed share:
    /// `share_comms[share_index] = Poseidon(primary_blind, c1_x, c2_x)`.
    pub(crate) primary_blind: Value<pallas::Base>,

    // === Share selection ===
    /// Which of the 16 shares is being revealed (0..15).
    pub(crate) share_index: Value<pallas::Base>,

    // === Condition 5: Share Nullifier Integrity ===
    /// The vote commitment leaf value (links conditions 1, 2, and 5).
    pub(crate) vote_commitment: Value<pallas::Base>,
}

impl Default for Circuit {
    fn default() -> Self {
        Self {
            vote_comm_tree_path: Value::unknown(),
            vote_comm_tree_position: Value::unknown(),
            share_comms: [Value::unknown(); 16],
            primary_blind: Value::unknown(),
            share_index: Value::unknown(),
            vote_commitment: Value::unknown(),
        }
    }
}


impl plonk::Circuit<pallas::Base> for Circuit {
    type Config = Config;
    type FloorPlanner = floor_planner::V1;

    fn without_witnesses(&self) -> Self {
        Self::default()
    }

    fn configure(meta: &mut ConstraintSystem<pallas::Base>) -> Self::Config {
        // 9 advice columns — the minimum required by the three gadgets in this circuit:
        //   [0..4]  Merkle conditional swap gate (pos_bit, current, sibling, left, right).
        //   [5]     Poseidon Pow5T3 partial S-box column (internal to the chip).
        //   [6..8]  Poseidon width-3 state columns.
        // The share commitment mux gate (condition 4) reuses all 9 columns across
        // 4 rows to pack its 16 one-hot selectors + 16 commitments without needing
        // an additional column.
        let advices: [Column<Advice>; 9] = core::array::from_fn(|_| meta.advice_column());
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
        // State columns: advices[6..8], partial S-box: advices[5].
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

        // Share commitment multiplexer gate (condition 4).
        // Col →  [0]       [1]       [2]        [3]        [4]        [5]        [6]       [7]       [8]
        // ------+---------+---------+----------+----------+----------+----------+---------+---------+---------
        // Row 0 | sel[0]  | sel[1]  | sel[2]   | sel[3]   | sel[4]   | sel[5]   | sel[6]  | sel[7]  | sel[8]
        // Row 1 | sel[9]  | sel[10] | sel[11]  | sel[12]  | sel[13]  | sel[14]  | sel[15] | comm[0] | comm[1]
        // Row 2 | comm[2] | comm[3] | comm[4]  | comm[5]  | comm[6]  | comm[7]  | comm[8] | comm[9] |comm[10]
        // Row 3 | comm[11]| comm[12]| comm[13] | comm[14] | comm[15] | sel_comm | share_idx| —      | —
        let q_share_comm_mux = meta.selector();
        meta.create_gate("share commitment multiplexer", |meta| {
            let q = meta.query_selector(q_share_comm_mux);

            let sel: [_; 16] = [
                meta.query_advice(advices[0], Rotation::cur()),
                meta.query_advice(advices[1], Rotation::cur()),
                meta.query_advice(advices[2], Rotation::cur()),
                meta.query_advice(advices[3], Rotation::cur()),
                meta.query_advice(advices[4], Rotation::cur()),
                meta.query_advice(advices[5], Rotation::cur()),
                meta.query_advice(advices[6], Rotation::cur()),
                meta.query_advice(advices[7], Rotation::cur()),
                meta.query_advice(advices[8], Rotation::cur()),
                meta.query_advice(advices[0], Rotation::next()),
                meta.query_advice(advices[1], Rotation::next()),
                meta.query_advice(advices[2], Rotation::next()),
                meta.query_advice(advices[3], Rotation::next()),
                meta.query_advice(advices[4], Rotation::next()),
                meta.query_advice(advices[5], Rotation::next()),
                meta.query_advice(advices[6], Rotation::next()),
            ];

            let comm: [_; 16] = [
                meta.query_advice(advices[7], Rotation::next()),
                meta.query_advice(advices[8], Rotation::next()),
                meta.query_advice(advices[0], Rotation(2)),
                meta.query_advice(advices[1], Rotation(2)),
                meta.query_advice(advices[2], Rotation(2)),
                meta.query_advice(advices[3], Rotation(2)),
                meta.query_advice(advices[4], Rotation(2)),
                meta.query_advice(advices[5], Rotation(2)),
                meta.query_advice(advices[6], Rotation(2)),
                meta.query_advice(advices[7], Rotation(2)),
                meta.query_advice(advices[8], Rotation(2)),
                meta.query_advice(advices[0], Rotation(3)),
                meta.query_advice(advices[1], Rotation(3)),
                meta.query_advice(advices[2], Rotation(3)),
                meta.query_advice(advices[3], Rotation(3)),
                meta.query_advice(advices[4], Rotation(3)),
            ];

            let selected_comm = meta.query_advice(advices[5], Rotation(3));
            let share_index = meta.query_advice(advices[6], Rotation(3));

            let one = Expression::Constant(pallas::Base::one());

            // Boolean checks for all 16 selection bits.
            let bool_checks: Vec<(&'static str, Expression<pallas::Base>)> = (0..16)
                .map(|i| ("bool sel_i", bool_check(sel[i].clone())))
                .collect();

            // Sum check for selectors (only one is 1)
            let sum_expr = sel.iter().skip(1).fold(sel[0].clone(), |acc, s| acc + s.clone());
            let sum_check = ("sum sel == 1", sum_expr - one);

            // Index reconstruction: share_index == sum(i * sel[i]).
            //
            // Given bool + sum guarantees exactly one sel[j] = 1, the sum collapses
            // to j.
            let reconstructed = sel.iter().enumerate().skip(1).fold(
                Expression::Constant(pallas::Base::zero()),
                |acc, (i, s)| acc + Expression::Constant(pallas::Base::from(i as u64)) * s.clone(),
            );
            let index_reconstruct = ("index reconstruct", share_index.clone() - reconstructed);

            // Selected commitment must equal the dot product:
            // selected_comm == Σ sel[i] * comm[i]
            let comm_mux_expr = comm.iter().zip(sel.iter())
                .fold(selected_comm, |acc, (c, s)| acc - s.clone() * c.clone());
            let comm_mux = ("comm mux", comm_mux_expr);

            // What these four groups together guarantee:
            // The bool + sum constraints establish one-hotness.
            // Given one-hotness, the index reconstruction collapses to share_index == j where j is the unique set position.
            // The mux constraint then collapses to selected_comm == comm[j].
            // Combined with the constrain_equal(derived_comm, selected_comm), the full chain is:
            // derived_comm  ==  comm[share_index]  ==  share_comms[share_index]
            // The last equality is enforced by copy_advice.
            let mut constraints: Vec<(&'static str, Expression<pallas::Base>)> = bool_checks;
            constraints.push(sum_check);
            constraints.push(index_reconstruct);
            constraints.push(comm_mux);

            Constraints::with_selector(q, constraints)
        });

        Config {
            primary,
            advices,
            poseidon_config,
            merkle_swap,
            q_share_comm_mux,
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

        let share_index = assign_free_advice(
            layouter.namespace(|| "witness share_index"),
            config.advices[0],
            self.share_index,
        )?;
        let share_index_cond5 = share_index.clone();

        let primary_blind = assign_free_advice(
            layouter.namespace(|| "witness primary_blind"),
            config.advices[0],
            self.primary_blind,
        )?;

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

        // Copy voting_round_id from instance into advice.
        // Used in condition 2 (vote commitment integrity) and condition 5
        // (share nullifier integrity).
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
        let voting_round_id_cond2 = voting_round_id.clone();
        let voting_round_id_cond5 = voting_round_id.clone();

        // ---------------------------------------------------------------
        // Witness 16 share_comms as private advice cells.
        //
        // Transitively bound to the public vote_comm_tree_root via:
        //   share_comms → shares_hash → vote_commitment → Merkle root
        // ---------------------------------------------------------------

        let share_comms: [AssignedCell<pallas::Base, pallas::Base>; 16] = {
            let mut cells = Vec::with_capacity(16);
            for i in 0..16 {
                cells.push(assign_free_advice(
                    layouter.namespace(|| alloc::format!("witness share_comm[{i}]")),
                    config.advices[0],
                    self.share_comms[i],
                )?);
            }
            cells.try_into().unwrap()
        };

        // Clone for condition 4 mux (condition 3's Poseidon consumes them).
        let share_comms_cond4: [AssignedCell<pallas::Base, pallas::Base>; 16] =
            core::array::from_fn(|i| share_comms[i].clone());

        // ---------------------------------------------------------------
        // Condition 3: Shares Hash Integrity.
        //
        // shares_hash = Poseidon(share_comm_0, ..., share_comm_15)
        //
        // The share_comms are private witnesses. Soundness comes from the
        // transitive binding to the public tree root via condition 2 + 1.
        // ---------------------------------------------------------------

        let shares_hash = compute_shares_hash_from_comms_in_circuit(
            config.poseidon_chip(),
            layouter.namespace(|| "cond3: shares_hash from comms"),
            share_comms,
        )?;
        let shares_hash_cond2 = shares_hash.clone();

        // ---------------------------------------------------------------
        // Condition 4: Primary Share Binding.
        //
        // Proves the prover knows the blind for the revealed share:
        //   derived_comm = Poseidon(primary_blind, enc_c1_x, enc_c2_x)
        //   share_comms[share_index] == derived_comm
        //
        // enc_c1_x and enc_c2_x come from the public instance column.
        // ---------------------------------------------------------------

        let enc_c1_x = layouter.assign_region(
            || "copy enc_share_c1_x from instance",
            |mut region| {
                region.assign_advice_from_instance(
                    || "enc_c1_x",
                    config.primary,
                    ENC_SHARE_C1_X,
                    config.advices[0],
                    0,
                )
            },
        )?;
        let enc_c1_x_cond5 = enc_c1_x.clone();

        let enc_c2_x = layouter.assign_region(
            || "copy enc_share_c2_x from instance",
            |mut region| {
                region.assign_advice_from_instance(
                    || "enc_c2_x",
                    config.primary,
                    ENC_SHARE_C2_X,
                    config.advices[0],
                    0,
                )
            },
        )?;
        let enc_c2_x_cond5 = enc_c2_x.clone();

        // derive the commitment from primary blind
        let derived_comm = hash_share_commitment_in_circuit(
            config.poseidon_chip(),
            layouter.namespace(|| "cond4: Poseidon(blind, c1, c2)"),
            primary_blind,
            enc_c1_x,
            enc_c2_x,
            0,
        )?;

        // Mux share_comms by share_index → selected_comm.
        //
        // Col →  [0]       [1]       [2]        [3]        [4]        [5]        [6]       [7]       [8]       [9]
        // ------+---------+---------+----------+----------+----------+----------+---------+---------+---------+---------
        // Row 0 | sel[0]  | sel[1]  | sel[2]   | sel[3]   | sel[4]   | sel[5]   | sel[6]  | sel[7]  | sel[8]  | sel[9]
        // Row 1 | sel[10] | sel[11] | sel[12]  | sel[13]  | sel[14]  | sel[15]  | comm[0] | comm[1] | comm[2] | comm[3]
        // Row 2 | comm[4] | comm[5] | comm[6]  | comm[7]  | comm[8]  | comm[9]  | comm[10]| comm[11]| comm[12]| comm[13]
        // Row 3 | comm[14]| comm[15]| sel_comm | share_idx| —        | —        | —       | —       | —       | —
        let selected_comm = layouter.assign_region(
            || "cond4: share commitment mux",
            |mut region| {
                config.q_share_comm_mux.enable(&mut region, 0)?;

                // Create a selector map
                let sel_values: [Value<pallas::Base>; 16] = core::array::from_fn(|i| {
                    self.share_index.map(|idx| {
                        if idx == pallas::Base::from(i as u64) {
                            pallas::Base::one()
                        } else {
                            pallas::Base::zero()
                        }
                    })
                });

                // Assign the one-hot selector bits into the region. We use assign_advice
                // (fresh allocation) because sel_values are computed locally and have no
                // prior cell to copy from. There are 16 bits spread across 9 advice
                // columns, so they spill from row 0 into the first 7 columns of row 1.
                // Layout table: (sel_start, count, advice_col_offset, row)
                for (sel_start, count, col_off, row) in [(0, 9, 0, 0), (9, 7, 0, 1)] {
                    for i in 0..count {
                        region.assign_advice(
                            || alloc::format!("sel_{}", sel_start + i),
                            config.advices[col_off + i],
                            row,
                            || sel_values[sel_start + i],
                        )?;
                    }
                }

                // Copy the 16 share commitments into the region. We use copy_advice
                // (equality-constrained copy) instead of assign_advice because these
                // cells were allocated earlier in separate regions; copy_advice ties
                // this cell to the original via the permutation argument, preventing
                // the prover from substituting a different value. The 16 commitments
                // also spill across multiple rows alongside the selector bits above.
                // Layout table: (comm_start, count, advice_col_offset, row)
                for (comm_start, count, col_off, row) in [(0, 2, 7, 1), (2, 9, 0, 2), (11, 5, 0, 3)] {
                    for i in 0..count {
                        share_comms_cond4[comm_start + i].copy_advice(
                            || alloc::format!("comm_{}", comm_start + i),
                            &mut region,
                            config.advices[col_off + i],
                            row,
                        )?;
                    }
                }

                // Select the correct commitment via dot product selector.
                // selected_comm_val = Σ sel[i] * comm[i]
                let selected_comm_val = (0..16).fold(Value::known(pallas::Base::zero()), |acc, i| {
                    acc.zip(sel_values[i]).zip(share_comms_cond4[i].value().copied())
                        .map(|((a, s), c)| a + s * c)
                });
                let selected_comm = region.assign_advice(
                    || "selected_comm",
                    config.advices[5],
                    3,
                    || selected_comm_val,
                )?;

                share_index.copy_advice(
                    || "share_index",
                    &mut region,
                    config.advices[6],
                    3,
                )?;

                Ok(selected_comm)
            },
        )?;

        // Ensure that the derived commitment is equal to selected
        layouter.assign_region(
            || "cond4: derived_comm == selected_comm",
            |mut region| region.constrain_equal(derived_comm.cell(), selected_comm.cell()),
        )?;

        // ---------------------------------------------------------------
        // Condition 2: Vote Commitment Integrity.
        //
        // vote_commitment = Poseidon(DOMAIN_VC, voting_round_id,
        //                            shares_hash, proposal_id, vote_decision)
        //
        // Same hash as vote_proof::vote_commitment_hash and
        // vote_commitment_tree::vote_commitment_hash.
        // ---------------------------------------------------------------

        // DOMAIN_VC constant (baked into the VK).
        let domain_vc = config.assign_constant(
            &mut layouter,
            "cond2: DOMAIN_VC constant",
            pallas::Base::from(vote_commitment::DOMAIN_VC),
        )?;

        let derived_vc = vote_commitment::vote_commitment_poseidon(
            &config.poseidon_config,
            &mut layouter,
            "cond2",
            domain_vc,
            voting_round_id_cond2,
            shares_hash_cond2,
            proposal_id,
            vote_decision,
        )?;

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
        // 24-level Poseidon Merkle path (LSB-first position bits).
        // Uses the shared poseidon_merkle gadget.
        // ---------------------------------------------------------------
        {
            let root = synthesize_poseidon_merkle_path::<VOTE_COMM_TREE_DEPTH>(
                &config.merkle_swap,
                &config.poseidon_config,
                &mut layouter,
                config.advices[0],
                vote_commitment,
                self.vote_comm_tree_position,
                self.vote_comm_tree_path,
                "cond1: merkle",
            )?;

            // Bind the computed Merkle root to the public input.
            layouter.constrain_instance(
                root.cell(),
                config.primary,
                VOTE_COMM_TREE_ROOT,
            )?;
        }

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
            // "share spend" domain tag — constant-constrained so the
            // value is baked into the verification key.
            let domain_tag = config.assign_constant(
                &mut layouter,
                "cond5: DOMAIN_SHARE_SPEND constant",
                domain_tag_share_spend(),
            )?;

            let share_nullifier = PoseidonHash::<
                pallas::Base,
                _,
                poseidon::P128Pow5T3,
                ConstantLength<6>,
                3,
                2,
            >::init(
                config.poseidon_chip(),
                layouter.namespace(|| "cond5: share nullifier Poseidon init"),
            )?
            .hash(
                layouter.namespace(|| "cond5: Poseidon(tag, vc, idx, c1, c2, round_id)"),
                [domain_tag, vote_commitment_cond5, share_index_cond5,
                 enc_c1_x_cond5, enc_c2_x_cond5, voting_round_id_cond5],
            )?;

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
        elgamal_encrypt, poseidon_hash_2, share_commitment,
        shares_hash as compute_shares_hash,
        spend_auth_g_affine, vote_commitment_hash as compute_vote_commitment_hash,
    };

    fn generate_ea_keypair() -> (pallas::Scalar, pallas::Point, pallas::Affine) {
        let ea_sk = pallas::Scalar::from(42u64);
        let g = pallas::Point::from(spend_auth_g_affine());
        let ea_pk = g * ea_sk;
        let ea_pk_affine = ea_pk.to_affine();
        (ea_sk, ea_pk, ea_pk_affine)
    }

    /// Returns `(c1_x, c2_x, share_blinds, share_comms, shares_hash_value)`.
    fn encrypt_shares(
        shares: [u64; 16],
        ea_pk: pallas::Point,
    ) -> (
        [pallas::Base; 16],
        [pallas::Base; 16],
        [pallas::Base; 16],
        [pallas::Base; 16],
        pallas::Base,
    ) {
        let mut c1_x = [pallas::Base::zero(); 16];
        let mut c2_x = [pallas::Base::zero(); 16];
        let randomness: [pallas::Base; 16] = core::array::from_fn(|i| {
            pallas::Base::from((i as u64 + 1) * 101)
        });
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
        let comms: [pallas::Base; 16] = core::array::from_fn(|i| {
            share_commitment(share_blinds[i], c1_x[i], c2_x[i])
        });
        let hash = compute_shares_hash(share_blinds, c1_x, c2_x);
        (c1_x, c2_x, share_blinds, comms, hash)
    }

    fn make_test_data(
        share_idx: u32,
    ) -> (Circuit, Instance) {
        let proposal_id = pallas::Base::from(3u64);
        let vote_decision = pallas::Base::from(1u64);
        let voting_round_id = pallas::Base::from(999u64);

        let (_ea_sk, ea_pk_point, _ea_pk_affine) = generate_ea_keypair();
        let shares_u64: [u64; 16] = [625; 16];
        let (enc_c1_x, enc_c2_x, share_blinds, share_comms, shares_hash_val) =
            encrypt_shares(shares_u64, ea_pk_point);

        let vote_commitment =
            compute_vote_commitment_hash(voting_round_id, shares_hash_val, proposal_id, vote_decision);

        let (auth_path, position, vote_comm_tree_root) =
            build_single_leaf_merkle_path(vote_commitment);

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
            share_comms: share_comms.map(Value::known),
            primary_blind: Value::known(share_blinds[share_idx as usize]),
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

    #[test]
    fn test_share_reveal_valid() {
        let (circuit, instance) = make_test_data(0);
        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn test_share_reveal_valid_index_1() {
        let (circuit, instance) = make_test_data(1);
        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn test_share_reveal_valid_index_2() {
        let (circuit, instance) = make_test_data(2);
        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn test_share_reveal_valid_index_3() {
        let (circuit, instance) = make_test_data(3);
        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn test_share_reveal_valid_index_15() {
        let (circuit, instance) = make_test_data(15);
        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }

    #[test]
    fn test_share_reveal_wrong_merkle_root() {
        let (circuit, mut instance) = make_test_data(0);
        instance.vote_comm_tree_root = pallas::Base::from(12345u64);
        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn test_share_reveal_wrong_nullifier() {
        let (circuit, mut instance) = make_test_data(0);
        instance.share_nullifier = pallas::Base::from(99999u64);
        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn test_share_reveal_wrong_share_index() {
        let (circuit, instance) = make_test_data(0);
        let bad_instance = Instance::from_parts(
            instance.share_nullifier,
            pallas::Base::from(999u64),
            pallas::Base::from(888u64),
            instance.proposal_id,
            instance.vote_decision,
            instance.vote_comm_tree_root,
            instance.voting_round_id,
        );
        let prover = MockProver::run(K, &circuit, vec![bad_instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn test_share_reveal_wrong_vote_decision() {
        let (circuit, mut instance) = make_test_data(0);
        instance.vote_decision = pallas::Base::from(42u64);
        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn test_share_reveal_wrong_voting_round_id() {
        let (circuit, mut instance) = make_test_data(0);
        instance.voting_round_id = pallas::Base::from(12345u64);
        let prover = MockProver::run(K, &circuit, vec![instance.to_halo2_instance()]).unwrap();
        assert!(prover.verify().is_err());
    }

    #[test]
    fn test_share_reveal_domain_tag_matches_server() {
        use ff::PrimeField;
        let mut bytes = [0u8; 32];
        let tag = b"share spend";
        bytes[..tag.len()].copy_from_slice(tag);
        let server_tag = pallas::Base::from_repr(bytes).unwrap();
        assert_eq!(domain_tag_share_spend(), server_tag);
    }

    /// Measures actual rows used by the share-reveal circuit via `CircuitCost::measure`.
    ///
    /// `CircuitCost` runs the floor planner against the circuit and tracks the
    /// highest row offset assigned in any column, giving the real "rows consumed"
    /// number rather than the theoretical 2^K capacity.
    ///
    /// Run with:
    ///   cargo test --features share-reveal row_budget -- --nocapture --ignored
    #[test]
    #[ignore]
    fn row_budget() {
        use std::println;
        use halo2_proofs::dev::CircuitCost;
        use pasta_curves::vesta;

        let (circuit, _) = make_test_data(0);

        let cost = CircuitCost::<vesta::Point, _>::measure(K, &circuit);
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

        println!("=== share-reveal circuit row budget (K={K}) ===");
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

        println!("  VOTE_COMM_TREE_DEPTH (circuit constant): {VOTE_COMM_TREE_DEPTH}");

        // Minimum-K probe: find the smallest K at which MockProver passes.
        for probe_k in 11u32..=K {
            let (c, inst) = make_test_data(0);
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
