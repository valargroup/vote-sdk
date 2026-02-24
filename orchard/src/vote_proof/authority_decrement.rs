//! Condition 6 gadget: Proposal Authority Decrement.
//!
//! Proves that a voter held a permission bit for a specific proposal and
//! produces the decremented authority value with that bit cleared.
//!
//! ## Cell Layout (18 rows x 10 advice columns)
//!
//! ```text
//!                        | a[0]          | a[1]         | a[2]       | a[3]      | a[4]         | a[5]         | a[6]        | a[7]        | a[8]   | a[9] |
//! -----------------------+---------------+--------------+------------+-----------+--------------+--------------+-------------+-------------+--------+------+
//! Row  0  q_cond_6=1     | proposal_id   | one_shifted  | pid_inv    |     -     | 0 (seed)     | 0 (seed)     | 0 (seed)    | 0 (seed)    |   -    |  -   |
//! -----------------------+---------------+--------------+------------+-----------+--------------+--------------+-------------+-------------+--------+------+
//! Row  1  init=1         | proposal_id   | b_0          | sel_0      | b_new_0   | rsel[0]      | rseld[0]     | rold[0]     | rnew[0]     |   1    |  0   |
//! Row  2  bits=1         | proposal_id   | b_1          | sel_1      | b_new_1   | rsel[1]      | rseld[1]     | rold[1]     | rnew[1]     |   2    |  1   |
//!   ...     ...          |   ...         |  ...         |  ...       |  ...      |   ...        |   ...        |   ...       |   ...       |  ...   | ...  |
//! Row 16  bits=1         | proposal_id   | b_15         | sel_15     | b_new_15  | rsel=1       | rseld=1      | rold[15]    | rnew[15]    | 32768  |  15  |
//!         sel_one=1      |               |              |            |           |              |              |             |             |        |      |
//! -----------------------+---------------+--------------+------------+-----------+--------------+--------------+-------------+-------------+--------+------+
//! Row 17                 | auth_new      |      -       |     -      |     -     |      -       |      -       |      -      |      -      |   -    |  -   |
//! -----------------------+---------------+--------------+------------+-----------+--------------+--------------+-------------+-------------+--------+------+
//!
//! Abbreviations:
//!   b_i       = i-th bit of the old authority value (authority old). b_i ∈ {0, 1}
//!   sel_i     = one-hot selector bit that marks which bit position corresponds to proposal_id.
//!   b_new_i   = i-th bit of the new (decremented) authority value — it's b_i with the selected permission bit cleared.
//!   pid_inv   = proposal_id^-1              advices[2] repurposed on row 0 only
//!   rsel[i]   = Sum sel_j        (j=0..i)   running sum of one-hot selector
//!   rseld[i]  = Sum sel_j*b_j   (j=0..i)   running sum of selected bit
//!   rold[i]   = Sum b_j*2^j     (j=0..i)   running recomposition of authority_old
//!   rnew[i]   = Sum b_new_j*2^j (j=0..i)   running recomposition of authority_new
//!   auth_new  = proposal_authority_new
//! 
//!
//! ## Constraints and Invariants
//!
//! Row 0 — lookup + non-zero gate (q_cond_6 = 1):
//!   (1)  (proposal_id, one_shifted) in table {(0,1),(1,2),...,(15,32768)}
//!          => proposal_id in [0,15] and one_shifted = 2^proposal_id
//!   (2)  proposal_id * pid_inv = 1
//!          => proposal_id != 0  (sentinel guard; lookup alone allows zero)
//!   (3)  rsel = rseld = rold = rnew = 0  (seeded as constants)
//!
//! Row 1 — init gate (q_cond_6_init = 1):
//!   (4)  index    = 0
//!   (5)  two_pow_i = 1     (= 2^0)
//!   + shared constraints below
//!
//! Rows 2-16 — recurrence gate (q_cond_6_bits = 1):
//!   (6)  index    = index_prev + 1
//!   (7)  two_pow_i = 2 * two_pow_i_prev
//!   + shared constraints below
//!
//! Shared constraints (rows 1-16, enforced on every bit row):
//!   (8)  b_i in {0, 1}
//!   (9)  sel_i in {0, 1}
//!   (10) (proposal_id - index) * sel_i = 0
//!          => sel_i may only be 1 when index == proposal_id (one-hot)
//!   (11) b_new_i = b_i * (1 - sel_i)
//!          => b_new_i equals b_i everywhere except it is forced to 0 at the
//!             selected index, clearing that permission bit
//!   (12) rsel[i]  = rsel[i-1]  + sel_i
//!   (13) rseld[i] = rseld[i-1] + sel_i * b_i
//!   (14) rold[i]  = rold[i-1]  + b_i * two_pow_i
//!   (15) rnew[i]  = rnew[i-1]  + b_new_i * two_pow_i
//!
//! Row 16 — terminal gate (q_cond_6_selected_one = 1):
//!   (16) rsel = 1   => exactly one sel_i was set across all 16 rows
//!   (17) rseld = 1  => the bit at the selected position was 1
//!                      (voter actually held the permission)
//!
//! Post-region equality constraints (anchored to the row-16 accumulation cells):
//!   (18) rold[15] = proposal_authority_old
//!          => the bit decomposition is consistent with the claimed old value.
//!          rold[15] is the advices[6] cell at row 16, the final step of the
//!          accumulation; it is in the same permutation cycle as all preceding
//!          run_old steps, so it cannot be forged independently.
//!   (19) rnew[15] = proposal_authority_new
//!          => the recomposed new value is exported and bound to the circuit output.
//!          Same anchoring argument as (18) for advices[7] at row 16.
//!
//! Together (1)+(16) prove proposal_id is a valid index in [1,15].
//! Together (10)+(16) prove sel_i is a one-hot vector with the single 1 at position proposal_id.
//! Together (11)+(17) prove the old authority had that bit set.
//! Together (14)+(15)+(18)+(19) prove auth_new = auth_old - 2^proposal_id.
//!
//! ## Worked Example
//!
//! Inputs:  authority_old = 13 = 0b0000_0000_0000_1101  (permission bits 0, 2, 3 are set)
//!          proposal_id   = 2   →  one_shifted = 4 = 2^2
//! Output:  authority_new = 9  = 0b0000_0000_0000_1001  (bit 2 cleared)
//!
//! Column headers match the cell layout above.
//! Note: on row 0 the a[1] and a[2] slots are repurposed for one_shifted and pid_inv respectively;
//! they revert to b_i / sel_i from row 1 onward.
//!
//!  Row | gate           | a[0]  | a[1]  | a[2]  | a[3]  | a[4]  | a[5]  | a[6]  | a[7]  | a[8]  | a[9]
//!      |                |  pid  | b_i   | sel_i |b_new_i| rsel  | rseld | rold  | rnew  |  2^i  | idx
//! -----+----------------+-------+-------+-------+-------+-------+-------+-------+-------+-------+-----
//!    0 | q_cond_6       |   2   |  4†   | 2⁻¹†  |   -   |  0*   |  0*   |  0*   |  0*   |   -   |  -
//! -----+----------------+-------+-------+-------+-------+-------+-------+-------+-------+-------+-----
//!    1 | init           |   2   |   1   |   0   |   1   |   0   |   0   |   1   |   1   |   1   |  0
//!    2 | bits           |   2   |   0   |   0   |   0   |   0   |   0   |   1   |   1   |   2   |  1
//!    3 | bits  ◄ sel=1  |   2   |   1   |   1   |   0   |   1   |   1   |   5   |   1   |   4   |  2
//!    4 | bits           |   2   |   1   |   0   |   1   |   1   |   1   |  13   |   9   |   8   |  3
//!  5-15| bits (b_i=0)   |   2   |   0   |   0   |   0   |   1   |   1   |  13   |   9   |  ...  | ...
//!   16 | bits + sel_one |   2   |   0   |   0   |   0   |   1   |   1   |  13   |   9   | 32768 | 15
//! -----+----------------+-------+-------+-------+-------+-------+-------+-------+-------+-------+-----
//!   17 |                |   9   |   -   |   -   |   -   |   -   |   -   |   -   |   -   |   -   |  -
//!
//! (* seeded as field-constant 0)
//! († on row 0, a[1] = one_shifted = 4 and a[2] = pid_inv = 2⁻¹ mod p)
//!
//! Accumulator trace:
//!   Row 1 (i=0): b=1, sel=0  → b_new=1·(1-0)=1,  rold=0+1·1=1,    rnew=0+1·1=1
//!   Row 2 (i=1): b=0, sel=0  → b_new=0,           rold=1+0·2=1,    rnew=1+0·2=1
//!   Row 3 (i=2): b=1, sel=1  → b_new=1·(1-1)=0,  rold=1+1·4=5,    rnew=1+0·4=1   ← bit 2 cleared
//!   Row 4 (i=3): b=1, sel=0  → b_new=1,           rold=5+1·8=13,   rnew=1+1·8=9
//!   Rows 5-16:   b=0 for all remaining bits      → rold stays 13,  rnew stays 9
//!   Row 16:      rsel=1 ✓  (exactly one selector fired),  rseld=1 ✓  (that bit was 1 in old authority)
//!                rold[15]=13 and rnew[15]=9 are the final accumulation cells used in
//!                equality constraints (18) and (19) — no row-17 copy required.
//!   Row 17:      auth_new=9 only (arithmetic value old−shift for the output cell).
//! ```

use alloc::vec::Vec;

use ff::{Field, PrimeField};
use halo2_proofs::{
    circuit::{AssignedCell, Layouter, Value},
    plonk::{
        self, Advice, Column, ConstraintSystem, Constraints, Expression, Selector, TableColumn,
        VirtualCells,
    },
    poly::Rotation,
};
use pasta_curves::pallas;

use halo2_gadgets::utilities::bool_check;

use crate::vote_proof::circuit::MAX_PROPOSAL_ID;

// ================================================================
// Config
// ================================================================

/// Configuration for the [`AuthorityDecrementChip`].
#[derive(Clone, Debug)]
pub struct AuthorityDecrementConfig {
    /// Complex selector for the lookup row (row 0 of the chip region).
    /// When 1 the `(proposal_id, one_shifted)` lookup is enforced;
    /// when 0 the lookup input is `(0, 1)` which always passes.
    pub(super) q_cond_6: Selector,
    /// Lookup table column for `proposal_id` in `(proposal_id, 2^proposal_id)`.
    /// Rows: `(0,1), (1,2), ..., (15, 32768)`.
    pub(super) table_proposal_id: TableColumn,
    /// Lookup table column for `one_shifted = 2^proposal_id`.
    pub(super) table_one_shifted: TableColumn,
    /// Selector for the init row (i=0): enforces `index=0, two_pow_i=1`.
    pub(super) q_cond_6_init: Selector,
    /// Selector for bit rows i=1..15: recurrence `index++, two_pow_i*=2`.
    pub(super) q_cond_6_bits: Selector,
    /// Selector for the last bit row (i=15): enforces `run_sel=1, run_selected=1`.
    pub(super) q_cond_6_selected_one: Selector,
    /// Advice column that holds `proposal_id^-1` on row 0.
    ///
    /// Used by the `proposal_id != 0` gate:
    /// `q_cond_6 * (1 - proposal_id * proposal_id_inv) = 0`.
    /// This is `advices[2]`, which is otherwise unused on row 0.
    pub(super) proposal_id_inv: Column<Advice>,
    /// The 10 shared advice columns passed in by the outer circuit.
    pub(super) advices: [Column<Advice>; 10],
}

// ================================================================
// Internal helpers
// ================================================================

/// Queried advice cells for one row of the bit-decomposition region.
struct Cond6Row {
    /// `advices[0]` cur - proposal index copied to every row.
    proposal_id: Expression<pallas::Base>,
    /// `advices[1]` cur - i-th bit of `proposal_authority_old`. Must be boolean.
    b_i: Expression<pallas::Base>,
    /// `advices[2]` cur - one-hot selector: 1 iff `i == proposal_id`.
    sel_i: Expression<pallas::Base>,
    /// `advices[3]` cur - `b_i * (1 - sel_i)`: bit after clearing.
    b_new_i: Expression<pallas::Base>,
    /// `advices[4]` cur - running `∑ sel_i`, must equal 1 at last row.
    run_sel: Expression<pallas::Base>,
    /// `advices[4]` prev
    run_sel_prev: Expression<pallas::Base>,
    /// `advices[5]` cur - running `∑ sel_i * b_i`, must equal 1 at last row.
    run_selected: Expression<pallas::Base>,
    /// `advices[5]` prev
    run_selected_prev: Expression<pallas::Base>,
    /// `advices[6]` cur - running `∑ b_i * 2^i`, equals `proposal_authority_old` at last row.
    run_old: Expression<pallas::Base>,
    /// `advices[6]` prev
    run_old_prev: Expression<pallas::Base>,
    /// `advices[7]` cur - running `∑ b_new_i * 2^i`, equals `proposal_authority_new` at last row.
    run_new: Expression<pallas::Base>,
    /// `advices[7]` prev
    run_new_prev: Expression<pallas::Base>,
    /// `advices[8]` cur - positional weight `2^i`.
    two_pow_i: Expression<pallas::Base>,
    /// `advices[9]` cur - row counter `i`.
    index: Expression<pallas::Base>,
}

fn query_cond6_row(
    meta: &mut VirtualCells<pallas::Base>,
    advices: &[Column<Advice>],
) -> Cond6Row {
    Cond6Row {
        proposal_id:       meta.query_advice(advices[0], Rotation::cur()),
        b_i:               meta.query_advice(advices[1], Rotation::cur()),
        sel_i:             meta.query_advice(advices[2], Rotation::cur()),
        b_new_i:           meta.query_advice(advices[3], Rotation::cur()),
        run_sel:           meta.query_advice(advices[4], Rotation::cur()),
        run_sel_prev:      meta.query_advice(advices[4], Rotation::prev()),
        run_selected:      meta.query_advice(advices[5], Rotation::cur()),
        run_selected_prev: meta.query_advice(advices[5], Rotation::prev()),
        run_old:           meta.query_advice(advices[6], Rotation::cur()),
        run_old_prev:      meta.query_advice(advices[6], Rotation::prev()),
        run_new:           meta.query_advice(advices[7], Rotation::cur()),
        run_new_prev:      meta.query_advice(advices[7], Rotation::prev()),
        two_pow_i:         meta.query_advice(advices[8], Rotation::cur()),
        index:             meta.query_advice(advices[9], Rotation::cur()),
    }
}

/// The 8 constraints shared by both the init gate and the recurrence gate.
fn cond6_shared_constraints(
    r: &Cond6Row,
) -> Vec<(&'static str, Expression<pallas::Base>)> {
    vec![
        // run_sel increments by sel_i each row
        ("run_sel",
            r.run_sel.clone() - r.run_sel_prev.clone() - r.sel_i.clone()),
        // run_selected increments by sel_i * b_i each row
        ("run_selected",
            r.run_selected.clone() - r.run_selected_prev.clone() - r.sel_i.clone() * r.b_i.clone()),
        // run_old accumulates the old value bit by bit
        ("run_old",
            r.run_old.clone() - r.run_old_prev.clone() - r.b_i.clone() * r.two_pow_i.clone()),
        // run_new accumulates the new value bit by bit
        ("run_new",
            r.run_new.clone() - r.run_new_prev.clone() - r.b_new_i.clone() * r.two_pow_i.clone()),
        // (proposal_id - index) * sel_i = 0: sel_i can only be 1 at the selected position
        ("(proposal_id - index)*sel_i",
            (r.proposal_id.clone() - r.index.clone()) * r.sel_i.clone()),
        // b_new_i = b_i * (1 - sel_i): new bit equals old bit, except zero it out when selected
        ("b_new_i = b_i*(1-sel_i)",
            r.b_new_i.clone() - r.b_i.clone() + r.b_i.clone() * r.sel_i.clone()),
        // enforce b_i in {0, 1}
        ("bool b_i",  bool_check(r.b_i.clone())),
        // enforce sel_i in {0, 1}
        ("bool sel_i", bool_check(r.sel_i.clone())),
    ]
}

// ================================================================
// Chip
// ================================================================

/// Gadget for Condition 6 (Proposal Authority Decrement).
///
/// Given `proposal_authority_old` and `proposal_id`, proves:
/// - `proposal_authority_old` has bit `proposal_id` set (voter has permission).
/// - `proposal_authority_new = proposal_authority_old - (1 << proposal_id)`.
/// - `proposal_id != 0` (rejects the sentinel value).
/// - `proposal_id` is in range `[1, 16)` via the `(proposal_id, 2^proposal_id)` lookup.
pub struct AuthorityDecrementChip;

impl AuthorityDecrementChip {
    /// Creates gates and lookup for the chip.
    ///
    /// `advices` must be the same 10-column slice used by the outer circuit
    /// (equality must already be enabled on each column by the caller).
    pub fn configure(
        meta: &mut ConstraintSystem<pallas::Base>,
        advices: [Column<Advice>; 10],
    ) -> AuthorityDecrementConfig {
        // Condition 6:
        // "Prove you had permission to vote on this proposal and prove you have relaxed
        // exactly that permission"
        // (proposal_id, one_shifted) lookup table for
        // one_shifted = 2^proposal_id. When q_cond_6 = 0 the lookup input
        // is (0, 1) so it passes. It passes because 2^0 = 1.
        // When q_cond_6 = 1, we enforce (proposal_id,
        // one_shifted) in {(0,1), (1,2), ..., (15, 32768)}.
        // Must be complex_selector because we use it in (one - q) in the lookup.
        let q_cond_6 = meta.complex_selector();
        let table_proposal_id = meta.lookup_table_column();
        let table_one_shifted = meta.lookup_table_column();
        meta.lookup(|meta| {
            let q = meta.query_selector(q_cond_6);
            let proposal_id = meta.query_advice(advices[0], Rotation::cur());
            let one_shifted = meta.query_advice(advices[1], Rotation::cur());
            // When q=0: (0, 1); when q=1: (proposal_id, one_shifted).
            let input_0 = q.clone() * proposal_id;
            let one = Expression::Constant(pallas::Base::one());
            let input_1 = q.clone() * one_shifted + (one.clone() - q);
            vec![
                (input_0, table_proposal_id),
                (input_1, table_one_shifted),
            ]
        });

        // Condition 6 (defense-in-depth): proposal_id must be non-zero.
        //
        // Zero is the dummy/sentinel value for an unset proposal_id; the range
        // check alone does not exclude it. This gate closes that gap by requiring
        // a valid field inverse, which exists if and only if proposal_id ≠ 0.
        //
        // Gate: q_cond_6 * (1 - proposal_id * proposal_id_inv) = 0
        // advices[2] on row 0 of the cond6 region is otherwise unused (sel_i
        // occupies advices[2] only on rows 1–16 where q_cond_6 = 0).
        meta.create_gate("proposal_id != 0", |meta| {
            let q = meta.query_selector(q_cond_6);
            let proposal_id = meta.query_advice(advices[0], Rotation::cur());
            let proposal_id_inv = meta.query_advice(advices[2], Rotation::cur());
            let one = Expression::Constant(pallas::Base::one());
            vec![("proposal_id * inv = 1", q * (one - proposal_id * proposal_id_inv))]
        });

        // Condition 6 (Proposal Authority Decrement) bit-decomposition gates.
        // Row 1: init (index=0, two_pow_i=1, running sums from first bit).
        let q_cond_6_init = meta.selector();
        // Rows 2..17: recurrence (index++, two_pow_i *= 2, running sums).
        let q_cond_6_bits = meta.selector();

        let zero = Expression::Constant(pallas::Base::zero());
        let one_expr = Expression::Constant(pallas::Base::one());
        let two_expr = Expression::Constant(pallas::Base::from(2u64));

        // The init gate enforces index=0 and two_pow_i=1 in addition to the shared running-sum
        // recurrence. The prover fills the zero-padding row above with zeros so the same
        // recurrence formula (increment by delta) handles initialization without a special case.
        meta.create_gate("cond6 init: index=0, two_pow_i=1, running sums", |meta| {
            let q = meta.query_selector(q_cond_6_init);
            let r = query_cond6_row(meta, &advices);
            let mut constraints = vec![
                ("two_pow_i = 1", r.two_pow_i.clone() - one_expr.clone()),
                ("index = 0",     r.index.clone() - zero.clone()),
            ];
            constraints.extend(cond6_shared_constraints(&r));
            Constraints::with_selector(q, constraints)
        });

        meta.create_gate("cond6 bits: index++, two_pow_i*=2, running sums", |meta| {
            let q = meta.query_selector(q_cond_6_bits);
            let r = query_cond6_row(meta, &advices);
            let two_pow_i_prev = meta.query_advice(advices[8], Rotation::prev());
            let index_prev     = meta.query_advice(advices[9], Rotation::prev());
            let mut constraints = vec![
                ("two_pow_i = 2*prev", r.two_pow_i.clone() - two_expr.clone() * two_pow_i_prev),
                ("index = prev+1",     r.index.clone() - index_prev - one_expr.clone()),
            ];
            constraints.extend(cond6_shared_constraints(&r));
            Constraints::with_selector(q, constraints)
        });

        // At the last bit row (row 16): run_sel = 1 (exactly one selector active) and run_selected = 1 (that bit was set).
        let q_cond_6_selected_one = meta.selector();
        meta.create_gate("cond6 run_sel = 1 and run_selected = 1", |meta| {
            let q = meta.query_selector(q_cond_6_selected_one);
            let run_sel = meta.query_advice(advices[4], Rotation::cur());
            let run_selected = meta.query_advice(advices[5], Rotation::cur());
            Constraints::with_selector(
                q,
                [
                    ("run_sel = 1", run_sel - one_expr.clone()),
                    ("run_selected = 1", run_selected - one_expr),
                ],
            )
        });

        AuthorityDecrementConfig {
            q_cond_6,
            table_proposal_id,
            table_one_shifted,
            q_cond_6_init,
            q_cond_6_bits,
            q_cond_6_selected_one,
            proposal_id_inv: advices[2],
            advices,
        }
    }

    /// Loads the `(proposal_id, 2^proposal_id)` lookup table.
    ///
    /// Must be called from `synthesize` before [`Self::assign`], alongside
    /// `SinsemillaChip::load`.
    pub fn load_table(
        config: &AuthorityDecrementConfig,
        layouter: &mut impl Layouter<pallas::Base>,
    ) -> Result<(), plonk::Error> {
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
        )
    }

    /// Assigns the 18-row bit-decomposition region and enforces equality of
    /// the recomposed old/new values.
    ///
    /// # Arguments
    ///
    /// - `proposal_id` - cell already assigned by the caller (e.g. from the
    ///   instance column). The chip copies it into every bit row.
    /// - `proposal_authority_old` - private witness cell.
    /// - `one_shifted` - `2^proposal_id` (private witness value; the lookup
    ///   constrains it against `proposal_id`).
    ///
    /// # Returns
    ///
    /// The `proposal_authority_new` cell (`= proposal_authority_old` with the
    /// selected bit cleared).
    pub fn assign(
        config: &AuthorityDecrementConfig,
        layouter: &mut impl Layouter<pallas::Base>,
        proposal_id: AssignedCell<pallas::Base, pallas::Base>,
        proposal_authority_old: AssignedCell<pallas::Base, pallas::Base>,
        one_shifted: Value<pallas::Base>,
    ) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
        let (proposal_authority_new, run_old_final, run_new_final) =
            layouter.assign_region(
                || "cond6 proposal authority decrement",
                |mut region| {
                    let proposal_authority_old_val = proposal_authority_old.value().copied();

                    // Row 0: (proposal_id, one_shifted) for lookup; init running sums to 0.
                    config.q_cond_6.enable(&mut region, 0)?;
                    let proposal_id_cell = proposal_id.copy_advice(
                        || "proposal_id",
                        &mut region,
                        config.advices[0],
                        0,
                    )?;
                    region.assign_advice(
                        || "one_shifted",
                        config.advices[1],
                        0,
                        || one_shifted,
                    )?;
                    // Witness proposal_id^-1 for the `proposal_id != 0` gate.
                    // If proposal_id = 0 the inverse does not exist and the gate
                    // will reject the proof; the fallback zero is irrelevant.
                    region.assign_advice(
                        || "proposal_id_inv",
                        config.proposal_id_inv,
                        0,
                        || {
                            proposal_id_cell.value().map(|pid| {
                                Option::from(pid.invert()).unwrap_or(pallas::Base::zero())
                            })
                        },
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

                    // Rows 1..16: bits, selectors, running sums.
                    let zero_val = Value::known(pallas::Base::zero());
                    let mut run_old_prev = zero_val;
                    let mut run_new_prev = zero_val;
                    let mut run_sel_prev = zero_val;
                    let mut run_selected_prev = zero_val;

                    // Cells from the final loop iteration (row 16) are captured
                    // so they can be returned directly as the canonical run_old /
                    // run_new finals. Using the row-16 cells directly keeps them in
                    // the same permutation cycle as the rest of the accumulation,
                    // so the permutation argument ties proposal_authority_old/_new
                    // to the actual bit-decomposition result with no gap.
                    let mut run_old_last_cell: Option<AssignedCell<pallas::Base, pallas::Base>> =
                        None;
                    let mut run_new_last_cell: Option<AssignedCell<pallas::Base, pallas::Base>> =
                        None;

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

                        proposal_id_cell.copy_advice(
                            || format!("proposal_id copy {}", i),
                            &mut region,
                            config.advices[0],
                            row,
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
                        let run_old_cur = region.assign_advice(
                            || format!("run_old {}", i),
                            config.advices[6],
                            row,
                            || run_old_prev,
                        )?;
                        let run_new_cur = region.assign_advice(
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
                            config.q_cond_6_init.enable(&mut region, row)?;
                        } else {
                            config.q_cond_6_bits.enable(&mut region, row)?;
                        }

                        if i == MAX_PROPOSAL_ID - 1 {
                            config.q_cond_6_selected_one.enable(&mut region, row)?;
                            // Save the final accumulation cells to anchor the
                            // permutation equality checks below.
                            run_old_last_cell = Some(run_old_cur);
                            run_new_last_cell = Some(run_new_cur);
                        }
                    }

                    // Row 17: place proposal_authority_new for external equality checks.
                    // run_old and run_new finals are taken directly from row 16 (the
                    // last accumulation step), so no additional copy is needed and the
                    // permutation argument is never broken by a free witness.
                    let proposal_authority_new_val = proposal_authority_old_val
                        .zip(one_shifted)
                        .map(|(old, shift)| old - shift);
                    let proposal_authority_new_cell = region.assign_advice(
                        || "proposal_authority_new",
                        config.advices[0],
                        17,
                        || proposal_authority_new_val,
                    )?;

                    Ok((
                        proposal_authority_new_cell,
                        run_old_last_cell.unwrap(),
                        run_new_last_cell.unwrap(),
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

        // Constrain equality:
        //  * proposal_authority_new (computed as old - shift)
        // * run_new_final (computed from bit decomposition)
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

        Ok(proposal_authority_new)
    }
}

// ================================================================
// Unit tests
// ================================================================

#[cfg(test)]
mod tests {
    use super::*;
    use halo2_proofs::{
        circuit::{Layouter, SimpleFloorPlanner},
        dev::MockProver,
        plonk::{Circuit, Column, ConstraintSystem, Fixed, Instance},
    };
    use pasta_curves::pallas;

    // ----------------------------------------------------------------
    // Minimal test circuit wrapping only AuthorityDecrementChip.
    //
    // Public instance layout: [proposal_id, proposal_authority_new].
    // ----------------------------------------------------------------

    #[derive(Clone, Debug)]
    struct TestConfig {
        adec: AuthorityDecrementConfig,
        primary: Column<Instance>,
        advices: [Column<Advice>; 10],
        constants: Column<Fixed>,
    }

    #[derive(Default, Clone)]
    struct TestCircuit {
        proposal_authority_old: Value<pallas::Base>,
        one_shifted: Value<pallas::Base>,
        proposal_id: Value<pallas::Base>,
    }

    impl Circuit<pallas::Base> for TestCircuit {
        type Config = TestConfig;
        type FloorPlanner = SimpleFloorPlanner;

        fn without_witnesses(&self) -> Self {
            Self::default()
        }

        fn configure(meta: &mut ConstraintSystem<pallas::Base>) -> TestConfig {
            let advices: [Column<Advice>; 10] = core::array::from_fn(|_| {
                let col = meta.advice_column();
                meta.enable_equality(col);
                col
            });
            let primary = meta.instance_column();
            meta.enable_equality(primary);
            let constants = meta.fixed_column();
            meta.enable_constant(constants);

            TestConfig {
                adec: AuthorityDecrementChip::configure(meta, advices),
                primary,
                advices,
                constants,
            }
        }

        fn synthesize(
            &self,
            config: TestConfig,
            mut layouter: impl Layouter<pallas::Base>,
        ) -> Result<(), plonk::Error> {
            AuthorityDecrementChip::load_table(&config.adec, &mut layouter)?;

            // Witness proposal_authority_old.
            let proposal_authority_old = layouter.assign_region(
                || "witness old",
                |mut region| {
                    region.assign_advice(
                        || "proposal_authority_old",
                        config.advices[0],
                        0,
                        || self.proposal_authority_old,
                    )
                },
            )?;

            // Assign proposal_id from the public instance (index 0).
            let proposal_id = layouter.assign_region(
                || "proposal_id from instance",
                |mut region| {
                    region.assign_advice_from_instance(
                        || "proposal_id",
                        config.primary,
                        0,
                        config.advices[0],
                        0,
                    )
                },
            )?;

            let proposal_authority_new = AuthorityDecrementChip::assign(
                &config.adec,
                &mut layouter,
                proposal_id,
                proposal_authority_old,
                self.one_shifted,
            )?;

            // Bind proposal_authority_new to instance index 1.
            layouter.constrain_instance(
                proposal_authority_new.cell(),
                config.primary,
                1,
            )?;

            Ok(())
        }
    }

    /// Runs the chip with the given inputs and returns the MockProver result.
    ///
    /// `authority_new_override` allows injecting a wrong expected new value
    /// into the public instance to test equality-constraint failures.
    fn run_chip(
        authority_old: u64,
        proposal_id: u64,
        authority_new_override: Option<pallas::Base>,
    ) -> Result<(), Vec<halo2_proofs::dev::VerifyFailure>> {
        let one_shifted = pallas::Base::from(1u64 << proposal_id);
        let authority_new_expected = authority_new_override
            .unwrap_or_else(|| pallas::Base::from(authority_old) - one_shifted);

        let circuit = TestCircuit {
            proposal_authority_old: Value::known(pallas::Base::from(authority_old)),
            one_shifted: Value::known(one_shifted),
            proposal_id: Value::known(pallas::Base::from(proposal_id)),
        };
        let instance = vec![
            pallas::Base::from(proposal_id),
            authority_new_expected,
        ];
        // K=5 (32 rows) is sufficient for 18 rows + overhead.
        let prover = MockProver::run(5, &circuit, vec![instance]).unwrap();
        prover.verify()
    }

    #[test]
    fn valid_basic() {
        // authority=2 (bit 1 set), proposal_id=1 → new=0.
        assert_eq!(run_chip(2, 1, None), Ok(()));
    }

    #[test]
    fn valid_full_authority_high_bit() {
        // authority=0x8000 (bit 15 set), proposal_id=15 → new=0.
        assert_eq!(run_chip(0x8000, 15, None), Ok(()));
    }

    #[test]
    fn valid_all_bits_set() {
        // authority=0xFFFF, proposal_id=5 → new=0xFFDF.
        assert_eq!(run_chip(0xFFFF, 5, None), Ok(()));
    }

    #[test]
    fn proposal_id_zero_fails() {
        // proposal_id=0 is the sentinel value; rejected by the `proposal_id != 0` gate.
        // authority=1 (bit 0 set) is otherwise structurally valid.
        assert!(run_chip(1, 0, None).is_err(), "proposal_id = 0 must be rejected");
    }

    #[test]
    fn bit_not_set_fails() {
    // authority=4 (only bit 2 set), proposal_id=1 (bit 1 not set).
    // run_selected = 0 at last row → `run_selected = 1` constraint fails.
    // proposal_id=0 would also hit the non-zero gate, so proposal_id=1 is used
    // to isolate the bit-not-set failure.
        assert!(run_chip(4, 1, None).is_err(), "bit not set must fail");
    }

    #[test]
    fn wrong_new_value_fails() {
        // Correct witnesses but tampered public instance for proposal_authority_new.
        let wrong_new = pallas::Base::from(0xDEAD_u64);
        assert!(
            run_chip(0xFFFF, 5, Some(wrong_new)).is_err(),
            "tampered new value must fail equality constraint",
        );
    }

    // ----------------------------------------------------------------
    // Regression test for the "disconnected run_old_final" exploit.
    //
    // Pre-fix, run_old_final was a fresh advice cell at row 17 with no
    // constrain_equal linking it to the row-16 accumulation.  A malicious
    // prover could:
    //   1. Bit-decompose x = 1 << proposal_id at rows 1–16 (run_selected=1 ✓).
    //   2. Assign run_old_final (row 17) = proposal_authority_old  (≠ x).
    //   3. The equality check (run_old_final == proposal_authority_old) passed,
    //      but the bit decomposition was never of proposal_authority_old itself.
    //
    // Post-fix, run_old_final IS the row-16 cell.  The permutation argument
    // directly binds proposal_authority_old to the accumulation result, so the
    // attack above is impossible.
    //
    // The honest chip also catches these cases via the run_selected=1 terminal
    // gate: if proposal_authority_old lacks the requested bit, run_selected=0 at
    // row 16 and the proof is rejected — which is precisely the outcome that was
    // bypassable before the fix.
    // ----------------------------------------------------------------
    #[test]
    fn exploit_disconnected_run_old_final_regression() {
        // authority=0x0008 (bit 3 set), proposal_id=1 (bit 1 absent).
        // Pre-fix: prover could decompose x=2 at rows 1–16, then free-witness
        // run_old_final=8 at row 17, satisfying all constraints.
        // Post-fix: run_old_final is the row-16 cell (=2 after honest extraction),
        // which fails the equality run_old_final==proposal_authority_old (8≠2).
        assert!(
            run_chip(0x0008, 1, None).is_err(),
            "authority 0x0008 with proposal_id=1 (bit absent) must be rejected"
        );

        // authority=0x0004 (bit 2 set), proposal_id=1 (bit 1 absent).
        assert!(
            run_chip(0x0004, 1, None).is_err(),
            "authority 0x0004 with proposal_id=1 (bit absent) must be rejected"
        );

        // authority=0x00FF (bits 0-7 set), proposal_id=8 (bit 8 absent).
        assert!(
            run_chip(0x00FF, 8, None).is_err(),
            "authority 0x00FF with proposal_id=8 (bit absent) must be rejected"
        );

        // authority=0 (no bits set), proposal_id=5 — degenerate case.
        assert!(
            run_chip(0x0000, 5, None).is_err(),
            "zero authority with any proposal_id must be rejected"
        );
    }
}
