# Per-Note Delegation Circuit (Conditions 9–15)

## Purpose

This circuit proves properties of a **single note slot** in the governance voting system. It is separate from the main delegation circuit (`circuit.rs`, conditions 1–8) to avoid merge conflicts during parallel development.

Each note slot (up to 4 per delegation) gets its own per-note proof. The main circuit's free witnesses `cmx_1..4` and `v_1..4` are bound to this circuit's public outputs (`CMX` and `VALUE`).

## Public Inputs

| Offset | Name | Description |
|--------|------|-------------|
| 0 | `NC_ROOT` | Note commitment tree root (anchor) |
| 1 | `NF_IMT_ROOT` | Nullifier IMT root |
| 2 | `VOTE_ROUND_ID` | Voting round identifier |
| 3 | `GOV_NULL` | Governance nullifier (published) |
| 4 | `CMX` | ExtractP(cm) — extracted note commitment |
| 5 | `VALUE` | Note value v (for main circuit's sum) |

## Conditions

| # | Condition | Status |
|---|-----------|--------|
| 9 | Note commitment integrity | Implemented |
| 10 | Merkle path validity (conditional on `is_note_real`) | Implemented |
| 11 | Diversified address integrity (`pk_d = [ivk] * g_d`) | Implemented |
| 12 | Private nullifier derivation | Implemented |
| 13 | IMT non-membership (unconditional per spec §1.3.5, Poseidon Merkle path, `IMT_DEPTH=32`) | Implemented |
| 14 | Governance nullifier integrity | Implemented |
| 15 | Padded notes have zero value | Implemented |

## Trust Boundary: `ivk`

`ivk` is an **unconstrained** private witness. The main circuit derives `ivk` via `CommitIvk` (see `circuit.rs`). Without binding, a prover could claim ownership of arbitrary notes.

**TODO**: The final integration step must bind this circuit's `ivk` to the main circuit's `CommitIvk`-derived value (via shared public input or circuit merge).

## Relationship to Main Circuit

```
Main Circuit (conditions 1-8)          Per-Note Circuit (conditions 9-15)
┌─────────────────────────┐            ┌──────────────────────────┐
│ cmx_1..4 (free witness) │◄──bind────►│ CMX (public output)      │
│ v_1..4   (free witness) │◄──bind────►│ VALUE (public output)    │
│ ivk (CommitIvk-derived) │◄──TODO────►│ ivk (unconstrained)      │
└─────────────────────────┘            └──────────────────────────┘
```

## Chip / Gadget Provenance

**Zero new cryptographic logic.** Every chip is reused from existing audited code. The only new code is three custom gates (`q_per_note`, `q_imt_swap`, `q_imt_nonmember`), which contain only simple field arithmetic constraints.

| Component | Source | Used For |
|-----------|--------|----------|
| `EccChip` | `halo2_gadgets::ecc` | Point witnesses, `[ivk]*g_d` mul (cond 11) |
| `SinsemillaChip` x2 | `halo2_gadgets::sinsemilla` | Lookup table, NoteCommit hashing |
| `MerkleChip` x2 | `halo2_gadgets::sinsemilla::merkle` | Merkle path verification (cond 10) |
| `PoseidonChip` | `halo2_gadgets::poseidon` | Gov nullifier hash (cond 14), IMT leaf + Merkle hashes (cond 13) |
| `NoteCommitChip` | `crate::circuit::note_commit` | Note commitment integrity (cond 9) |
| `AddChip` | `crate::circuit::gadget::add_chip` | Required by `derive_nullifier` |
| `note_commit()` | `crate::circuit::gadget` | Calls NoteCommit (cond 9) |
| `derive_nullifier()` | `crate::circuit::gadget` | Private nullifier derivation (cond 12) |
| `assign_free_advice()` | `crate::circuit::gadget` | All witness assignments |
| `bool_check()` | `halo2_gadgets::utilities` | Boolean check in `q_per_note` gate |
| `LookupRangeCheckConfig` | `halo2_gadgets::utilities` via `ecc_config.lookup_config` | Range checks for IMT non-membership diffs (cond 13) |
| **`q_per_note` gate** | **New (this file)** | Conds 10+13+15: Merkle root check (conditional on `is_note_real`), IMT root check (unconditional per spec §1.3.5), padded zero value, boolean check |
| **`q_imt_swap` gate** | **New (this file)** | Conditional swap at each IMT Merkle level (cond 13) |
| **`q_imt_nonmember` gate** | **New (this file)** | IsZero + diff computation for IMT non-membership (cond 13) |

## Circuit Size

K = 14 (16,384 rows). Estimated usage ~11,100 rows (including IMT Poseidon Merkle path).

## Remaining Work

- [x] **IMT non-membership (condition 13)** — Poseidon-based Indexed Merkle Tree non-membership proof. Witnesses a "low leaf" `(low_nf, next_nf)` bracketing `real_nf`, proves Merkle inclusion via 32-level Poseidon path, and range-checks ordering differences (250 bits). Tree depth is parameterized via `IMT_DEPTH` constant (currently 32).

- [ ] **Multi-note padding (up to 4 notes)** — Set up the builder layer to create up to 4 per-note proofs per delegation, with padded (dummy) notes for unused slots (`is_note_real = 0`, `v = 0`).

- [ ] **`ivk` binding** — `ivk` is currently an unconstrained witness. Must be bound to the main circuit's `CommitIvk`-derived `ivk` via shared public input or circuit merge. Without this, a prover can claim ownership of arbitrary notes.

- [ ] **`cmx`/`v` binding** — The main circuit's free witnesses `cmx_1..4` and `v_1..4` must be constrained against this circuit's public outputs (`CMX` at offset 4, `VALUE` at offset 5). Both sides exist but are not yet connected.

- [ ] **Gov nullifier personalization** — The spec says "TODO: Finalize personalization string." Current implementation uses bare chained-Poseidon (`Poseidon(nk, Poseidon(vote_round_id, real_nf))`). May need a domain separator once the spec is finalized.
