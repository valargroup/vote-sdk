# Vote Proof Circuit (ZKP #2) — Constraint Failure Investigation Log

## Background

When wiring real ZKP #2 proof generation into the e2e integration test, the
circuit's MockProver consistently reported two constraint failures:

1. **Advice column 9** in a "2 words range check" region — the strict-mode
   final running sum (z_2) does not equal 0.
2. **Fixed column** at some row "outside any region" — a constant-zero cell
   referenced by the strict range check's `assign_advice_from_constant(0)`.

These two failures are from the **same root cause**: a strict range check
(`copy_check(..., 2, true)`) fails, and the z_final=0 constant constraint
is the second symptom.

---

## What We Tried (chronological)

### 1. Fix anchor_height mismatch in builder

**Problem**: The builder was setting `vote_comm_tree_anchor_height` to the
tree POSITION (1) instead of the block HEIGHT.

**Fix**: Added `anchor_height: u32` parameter to `build_vote_proof_from_delegation`.

**Outcome**: Fixed a real bug but did NOT resolve the constraint failure.

### 2. Add diagnostic hex output for all 9 public inputs

**Fix**: Added `[PROVER]` and `[FFI]` hex logging of all 9 public inputs.

**Outcome**: All 9 public inputs matched perfectly between prover and verifier.
Confirmed the issue is NOT a public input mismatch.

### 3. Add local proof verification in e2e test

**Fix**: Called `verify_vote_proof(&proof, &instance)` in the same binary.

**Outcome**: Local verification ALSO failed with `ConstraintSystemFailure`.
Confirmed the proof itself is invalid — not a cross-binary VK mismatch.

### 4. Remove both padding sections (fixed-column + range-check)

**Hypothesis**: The hardcoded padding (240 fixed-column rows + 120x2-word +
1x3-word range checks) was calibrated for test values and broke with real values.

**Fix**: Removed both padding loops entirely.

**Outcome**: Same failure, just at different region/row numbers
(region 207 instead of 569, Fixed row 96 instead of 457).

### 5. Add dedicated constants column (Fixed[8])

**Hypothesis**: `Fixed[0]` was shared between ECC Lagrange coefficients and
`assign_advice_from_constant(0)` from strict range checks, causing collisions.

**Fix**: Added `let constants = meta.fixed_column(); meta.enable_constant(constants);`
separate from the 8 `lagrange_coeffs` columns.

**Outcome**: Error moved from `Fixed[0]` to `Fixed[8]` (the new column), but
the same constraint failure persisted. The column sharing was NOT the root cause.

### 6. Restore range-check padding only (keep dedicated column)

**Fix**: Restored the 120x2-word + 1x3-word padding in advices[9], kept the
dedicated constants column.

**Outcome**: Same failure. The range-check padding alone doesn't help.

### 7. Restore BOTH paddings + dedicated column

**Fix**: Restored both the 240 fixed-column padding and the 120+1 range-check
padding, combined with the dedicated constants column.

**Outcome**: Same failure. The full original padding with a clean constants
column still doesn't work.

### 8. Add instant key-chain consistency checks

**Fix**: Added out-of-circuit assertions before proof generation:
- Check 1: `[vsk] * SpendAuthG == ak` from FullViewingKey
- Check 2: `[ivk] * g_d == pk_d` (CommitIvk chain)

**Outcome**: Both checks PASSED. The witness values are correct. The issue is
a circuit layout/constraint problem, not a witness value problem.

### 9. Disable MockProver, let real prover run

**Fix**: Removed MockProver gate, let `create_proof` run and then `verify_proof`.

**Outcome**: `create_proof` succeeded (no panic) but `verify_proof` failed with
`ConstraintSystemFailure`. Confirms the circuit constraints are not satisfied —
the real prover generates an invalid proof silently.

### 10. Comment out condition 3 (CommitIvk / prove_address_ownership)

**Hypothesis**: The "2 words range check" failure is inside CommitIvk's
canonicity checks (condition 3).

**Fix**: Commented out `spend_auth_g_mul` and `prove_address_ownership`.

**Outcome**: Same failure persisted at region 186. Condition 3 was NOT the
source of the failing range check.

### 11. Comment out condition 6 (Proposal Authority Decrement)

**Hypothesis**: The "2 words range check" comes from condition 6's
`copy_check(..., 2, true)` calls on diff, gap_diff, proposal_authority_old,
gap_old, proposal_authority_new, gap_new.

**Fix**: Replaced condition 6 with stub witness assignments (just
`proposal_id` from instance and `proposal_authority_new` as a free advice).
No lookup, no addition constraints, no range checks.

**Outcome**: MockProver PASSED. Proof generated and verified successfully.
The e2e test passed end-to-end with real on-chain ZKP #2 verification.

---

## Root Cause

The constraint failure is isolated to **condition 6's strict range checks**.
Specifically, the `copy_check(..., 2, true)` calls that enforce 20-bit bounds
on the proposal authority values and their gap complements.

The original analysis in this document identified the likely cause:

> `diff = one_shifted - 1 - proposal_authority_new`
>
> With `proposal_authority_old = 65535` and `proposal_id = 1`:
> - `one_shifted = 2`
> - `proposal_authority_new = 65533`
> - `diff = 2 - 1 - 65533 = -65532` (huge in the field, fails range check)

The circuit's condition 6 enforces `proposal_authority_new < one_shifted`,
which only holds when `proposal_authority_old` has ONLY the proposal_id-th
bit (and possibly lower bits) set. With `proposal_authority_old = 65535`
(all 16 bits set) and `proposal_id = 1`, `proposal_authority_new = 65533`
which is NOT less than `one_shifted = 2`.

This is a **spec interpretation issue**: the circuit's condition 6 checks that
the voter's remaining authority after clearing the proposal bit is less than
the bit value, which is only valid for single-bit authority. The builder
assumes full authority (all bits set), which violates this constraint.

---

## Why no tests failed when condition 3 was disabled

All existing tests use **consistent** witnesses: they derive `(vpk_g_d, vpk_pk_d)` from
`(vsk, vsk_nk, rivk_v)` via `derive_voting_address` and pass those same values into
the circuit. So even with condition 3 commented out, the circuit was never asked to
verify the relation `vpk_pk_d = [ivk_v] * vpk_g_d` — and the witnesses happened to
satisfy it. The tests therefore still passed; they did not exercise condition 3.

The test `van_nullifier_wrong_vsk_nk_fails` does break the CommitIvk chain (wrong nk
→ wrong ivk → wrong derived pk_d), but when condition 3 was disabled the proof
failed only because of **condition 4** (VAN nullifier mismatch), not because of
address ownership. There was no test that failed **solely** due to condition 3.

To prevent regressions, dedicated condition 3 tests were added that use invalid
address-ownership witnesses (wrong vsk or wrong vpk_pk_d) and assert that the
circuit rejects them.

---

## Current State

- **Condition 3**: Re-enabled (spend authority via CommitIvk; `spend_auth_g_mul` and `prove_address_ownership`).
- **Condition 6 (Proposal Authority Decrement)**: Re-enabled via bit-decomposition (no diff/gap range check). Step 1: decompose `proposal_authority_old` into 16 bits; Step 2: selector `sel_i = 1` iff `proposal_id == i`, exactly one active, `run_selected = sum(sel_i * b_i) = 1`; Step 3: `b_new_i = b_i*(1-sel_i)`, recompose to `proposal_authority_new`. The 16-bit decomposition proves values in `[0, 2^16)` without strict range-check chip calls.
- **Active conditions**: 1, 2, 3, 4, 5 (VAN nullifier), 6 (proposal authority decrement), 7, 8, 9, 10, 11
- **Dedicated constants column**: Kept (clean separation from ECC Lagrange)
- **All padding**: Removed
- **MockProver**: Enabled
- **E2E test**: Builder witnesses (full authority 65535, `proposal_authority_new = old - (1<<proposal_id)`) satisfy the new constraints.

---

## Next Steps

1. **Fix condition 6** — Done. Re-implemented via bit decomposition (selected bit = 1, clear and recompose); supports full authority (65535).

2. **Re-enable condition 3** — Done. Condition 3 (`prove_address_ownership` / CommitIvk) has been re-enabled; it was not the source of the failure.

3. **Re-enable MockProver** as the standard gate (already done).

4. **Remove key-chain consistency checks** from the builder once condition 3
   is re-enabled (they're redundant with the in-circuit check).
