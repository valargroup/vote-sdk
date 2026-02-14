# Delegation Circuit (ZKP 1)

A single circuit proving all 15 conditions of the delegation ZKP at K=14 (16,384 rows). The circuit handles the keystone note (conditions 1–8) and four per-note slots (conditions 9–15 ×4) in one proof.

**Public inputs:** 12 field elements.
**Per-note slots:** 4 (unused slots are padded with zero-value notes).

## Inputs

- Public (12 field elements)
   * **nf_signed** (offset 0): the derived nullifier of the keystone note.
   * **rk** (offsets 1–2): the randomized public key for spend authorization (x, y coordinates).
   * **cmx_new** (offset 3): the extracted note commitment (`ExtractP(cm_new)`) of the output note.
   * **gov_comm** (offset 4): the governance commitment — a Pallas base field element identifying the governance context.
   * **vote_round_id** (offset 5): the vote round identifier — prevents cross-round replay.
   * **nc_root** (offset 6): the note commitment tree root (shared anchor for Merkle path verification).
   * **nf_imt_root** (offset 7): the nullifier Indexed Merkle Tree root (for non-membership proofs).
   * **gov_null_1..4** (offsets 8–11): per-note governance nullifiers, one per note slot.

- Private (keystone note)
   * **rho_signed** ("rho"): the nullifier of the note that was spent to create the signed note.
   * **psi_signed** ("psi"): a pseudorandom field element derived from the note's `rseed` and rho.
   * **cm_signed**: the note commitment, witnessed as an ECC point.
   * **nk**: nullifier deriving key.
   * **ak**: spend validating key (the long-lived public key for spend authorization).
   * **alpha**: a fresh random scalar used to rerandomize the spend authorization key for each transaction.
   * **rivk**: the randomness (blinding factor) for the CommitIvk Sinsemilla commitment.
   * **rcm_signed**: the note commitment trapdoor (randomness).
   * **g_d_signed**: the diversified generator from the note recipient's address.
   * **pk_d_signed**: the diversified transmission key from the note recipient's address.

- Private (output note — condition 6)
   * **g_d_new**: the diversified generator from the output note recipient's address.
   * **pk_d_new**: the diversified transmission key from the output note recipient's address.
   * **psi_new**: pseudorandom field element for the output note.
   * **rcm_new**: the output note commitment trapdoor.

- Private (per-note slot ×4 — conditions 9–15)
   * **g_d**: diversified generator from the note recipient's address.
   * **pk_d**: diversified transmission key from the note recipient's address.
   * **v**: the note value (in zatoshi).
   * **rho**: the nullifier of the note that was spent to create this note.
   * **psi**: pseudorandom field element derived from the note's `rseed` and rho.
   * **rcm**: note commitment trapdoor.
   * **cm**: note commitment, witnessed as an ECC point.
   * **path**: Sinsemilla-based Merkle authentication path (32 siblings).
   * **pos**: leaf position in the note commitment tree.
   * **is_note_real**: boolean flag — 1 for real notes, 0 for padded notes.
   * **imt_low**: the interval start (low bound of the bracketing leaf).
   * **imt_high**: the interval end (high bound of the bracketing leaf).
   * **imt_leaf_pos**: position of the leaf in the IMT.
   * **imt_path**: Poseidon-based IMT Merkle authentication path (29 pure siblings).

- Private (governance — condition 7)
   * **gov_comm_rand**: a random blinding factor for the governance commitment.

- Internal wires (not public inputs, not free witnesses)
   * **ivk**: derived in condition 5 (CommitIvk), shared with condition 11.
   * **nk**: witnessed once, shared with conditions 2, 12, and 14.
   * **cmx_1..4**: produced by per-note condition 9, consumed by condition 3.
   * **v_1..4**: produced by per-note condition 9, consumed by condition 7.

## 1. Signed Note Commitment Integrity

Purpose: ensure that the signed note commitment is correctly constructed. Establishes the binding link between spending authority, nullifier key, and the note itself.

```
NoteCommit_rcm_signed(repr(g_d_signed), repr(pk_d_signed), 0, rho_signed, psi_signed) = cm_signed
```

Where:
- **rcm_signed**: the note commitment randomness (trapdoor). A scalar derived from the note's `rseed` and `rho`. Blinds the commitment.
- **repr(g_d_signed)**: the diversified base point from the recipient's payment address.
- **repr(pk_d_signed)**: the diversified transmission key.
- **0**: the note value is hardcoded to zero (the keystone is always a dummy/zero-value note).
- **rho_signed**: the nullifier of the note that was spent to create this note. Bound by condition 3.
- **psi_signed**: pseudorandom field element from `rseed` and `rho`.
- **cm_signed**: the witnessed note commitment. The circuit recomputes NoteCommit and enforces strict equality.

The commitment binds together: **who the note belongs to** (g_d, pk_d), **how much it's worth** (0), **where it came from** (rho), **random uniqueness** (psi), **all blinded by randomness** (rcm).

**Constructions:** `SinsemillaChip` (config 1), `EccChip`, `NoteCommitChip` (signed).

## 2. Nullifier Integrity

Purpose: derive the standard Orchard nullifier deterministically from the note's secret components. Validate it against the one used in the exclusion proof.

```
nf_signed = DeriveNullifier_nk(rho_signed, psi_signed, cm_signed)
```

Where:
- **nk**: The nullifier deriving key associated with the note.
- **rho_signed** ("rho"): The nullifier of the note that was spent to create the signed note. A Pallas base field element that serves as a unique, per-note domain separator. rho ensures that even if two notes have identical contents, they will produce different nullifiers because they were created by spending different input notes.
- **psi_signed** ("psi"): A pseudorandom field element derived from the note's random seed `rseed` and its nullifier domain separator rho. Adds randomness to the nullifier so that even if two notes share the same rho and nk, they produce different nullifiers. Provided as a witness (not derived in-circuit) since derivation would require an expensive Blake2b.
- **cm_signed**: The note commitment, witnessed as an ECC point (the form `DeriveNullifier` expects).

**Function:** `DeriveNullifier`

```
DeriveNullifier_nk(rho, psi, cm) = ExtractP(
    [ (PRF_nf_Orchard_nk(rho) + psi) mod q_P ] * K_Orchard + cm
)
```

- `ExtractP` extracts the base field element from the resulting group element.
- `K_Orchard` is a fixed generator. Input to the `EccChip`.
- `PRF_nf_Orchard_nk(rho)` is the nullifier pseudorandom function. Uses Poseidon hash for PRF.

**Constructions:** `PoseidonChip`, `AddChip`, `EccChip`.

- **Why do we take PRF of rho?**
   * The primary reason is unlinkability. Rho is the nullifier of the note that was spend to create this note. In standard Orchard, nullifiers are published onchain. The PRF destroys the link.
- **Why not expose nf_old publicly?**
   * In standard Orchard, the nullifier is published to prevent double-spending. In this delegation circuit, nf_old is not directly exposed as a public input. Instead, it is checked against the exclusion interval and a domain nullifier is published instead. The standard nullifier stays hidden.

## 3. Rho Binding

Purpose: the signed note's rho is bound to the exact notes being delegated, the governance commitment, and the round. This makes the keystone signature non-replayable and scoped.

```
rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, gov_comm, vote_round_id)
```

Where:
- **cmx_1..4**: the extracted note commitments (`ExtractP(cm_i)`) of the four delegated notes. **These are internal wires** — produced by per-note condition 9 (note commitment integrity), not free witnesses. By hashing all four commitments into rho, the keystone signature is bound to the exact set of notes the delegator chose.
- **gov_comm**: the governance commitment (public input).
- **vote_round_id**: the vote round identifier (public input).

**Function:** `Poseidon` with `ConstantLength<6>`. Uses `Pow5Chip` / `P128Pow5T3` with rate 2 (3 absorption rounds for 6 inputs).

**Constraint:** The circuit computes `derived_rho = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, gov_comm, vote_round_id)` and enforces strict equality `derived_rho == rho_signed`. Since `rho_signed` is the same value used in both note commitment integrity (condition 1) and nullifier integrity (condition 2), this creates a three-way binding: the nullifier, the note commitment, and the delegation scope are all tied to the same rho.

**Constructions:** `PoseidonChip`.

## 4. Spend Authority

Purpose: proves spending authority while preserving unlinkability. Links to the Keystone spend-auth signature verified out-of-circuit.

```
rk = [alpha] * SpendAuthG + ak_P
```

Where:
- **ak** — the authorizing key, the long-lived public key for spend authorization.
- **alpha** — fresh randomness. If rk were the same across transactions, an observer could link them to the same spender.
- **SpendAuthG** — the fixed base generator point on the Pallas curve dedicated to spend authorization.

**Constructions:** `EccChip` (ScalarFixed, FixedPoint mul, add).

## 5. CommitIvk & Diversified Address Integrity

Purpose: proves the signed note's address belongs to the same key material `(ak, nk)`. Derives `ivk` — reused for every per-note ownership check (condition 11).

```
ivk = CommitIvk_rivk(ExtractP(ak_P), nk)
pk_d_signed = [ivk] * g_d_signed
```

Without address integrity, the nullifier integrity proves "I know (nk, rho, psi, cm) that produce this nullifier" and "I know ak such that rk = ak + [alpha] * G", but nothing ties ak to nk. A malicious prover could supply their own `ak` and someone else's `nk`.

`CommitIvk(ExtractP(ak), nk)` forces `ak` and `nk` to come from the same key tree. `pk_d_signed = [ivk] * g_d_signed` proves the note's destination address was derived from this specific ivk.

The `ivk = ⊥` case is handled internally by `CommitDomain::short_commit`: incomplete addition allows the identity to occur, and synthesis detects this edge case and aborts proof creation. No explicit conditional is needed in the circuit.

**ivk is internal** — it is NOT constrained to a public input. It flows directly to condition 11 (per-note address checks) via cell reuse.

Where:
- **ak_P** — the spend validating key (shared with condition 4). `ExtractP(ak_P)` extracts its x-coordinate.
- **nk** — the nullifier deriving key (shared with conditions 2, 12, 14).
- **rivk** — the CommitIvk randomness, extracted from the full viewing key via `fvk.rivk(Scope::External)`.
- **g_d_signed** — the diversified generator from the note recipient's address.
- **pk_d_signed** — the diversified transmission key from the note recipient's address.

**Constructions:** `CommitIvkChip`, `SinsemillaChip` (config 1), `EccChip`.

## 6. Output Note Commitment Integrity

Purpose: prove the output note's commitment is correctly constructed, with its `rho` chained from the signed note's nullifier. Creates a cryptographic link between spending the signed note and creating the output note.

```
ExtractP(NoteCommit_rcm_new(repr(g_d_new), repr(pk_d_new), 0, rho_new, psi_new)) in {cmx_new, bottom}
where rho_new = nf_signed mod q_P
```

Where:
- **rcm_new**: the output note commitment trapdoor.
- **repr(g_d_new)**: the diversified base point from the output note recipient's address.
- **repr(pk_d_new)**: the diversified transmission key from the output note recipient's address.
- **0**: the note value is hardcoded to zero.
- **rho_new**: set to `nf_signed` — the nullifier derived in condition 2. Enforced in-circuit by reusing the same cell.
- **psi_new**: pseudorandom field element derived from `rseed_new` and `rho_new`.
- **cmx_new**: the public input. `ExtractP` extracts the x-coordinate of the commitment point.

**Chain from condition 2**: The `nf_signed` cell computed in condition 2 is reused directly as `rho_new`. Since that cell is also constrained to the `NF_SIGNED` public input, the chain is: `nf_signed` (public) = `DeriveNullifier(nk, rho_signed, psi_signed, cm_signed)` = `rho_new` (input to output NoteCommit).

**Constructions:** `SinsemillaChip` (config 2), `EccChip`, `NoteCommitChip` (new).

## 7. Gov Commitment Integrity

Purpose: prove that the governance commitment (a public input) is correctly derived from the domain tag, the output note's voting hotkey address, the total voting weight, the vote round identifier, a blinding factor, and the proposal authority bitmask. Binds the delegated weight, voting hotkey, and authority scope into a single public commitment that ZKP #2 (vote proof) can open. The domain tag provides domain separation from Vote Commitments in the shared vote commitment tree.

```
gov_comm_core = Poseidon(DOMAIN_VAN, g_d_new_x, pk_d_new_x, v_total, vote_round_id, MAX_PROPOSAL_AUTHORITY)
gov_comm = Poseidon(gov_comm_core, gov_comm_rand)
```

Where:
- **DOMAIN_VAN**: `0`. Domain separation tag for Vote Authority Notes (vs `DOMAIN_VC = 1` for Vote Commitments). Assigned via `assign_advice_from_constant` so the value is baked into the verification key.
- **g_d_new_x**: the x-coordinate of the output note's diversified generator. Reuses the ECC point from condition 6.
- **pk_d_new_x**: the x-coordinate of the output note's diversified transmission key. Reuses the ECC point from condition 6.
- **v_total**: the sum `v_1 + v_2 + v_3 + v_4`, computed in-circuit via three `AddChip` additions. **Each `v_i` is an internal wire** — produced by per-note condition 9 (note commitment integrity), not a free witness. The value is bound to the actual note commitment.
- **vote_round_id**: the vote round identifier (public input, same cell as condition 3).
- **MAX_PROPOSAL_AUTHORITY**: `2^16 - 1 = 65535`. A 16-bit bitmask authorizing voting on all 16 proposals. Assigned via `assign_advice_from_constant` so the value is baked into the verification key.
- **gov_comm_rand**: a random blinding factor. Prevents observers from brute-forcing the address or weight from the public `gov_comm`.

**Function layout:** Two Poseidon hashes:
- `gov_comm_core` uses `ConstantLength<6>` over the structural fields.
- `gov_comm` finalizes with `ConstantLength<2>` over `(gov_comm_core, gov_comm_rand)`.

**Constructions:** `PoseidonChip`, `AddChip`.

## 8. Minimum Voting Weight

Purpose: prevent dust delegations by enforcing that the total delegated value meets a minimum threshold.

```
v_total >= 12,500,000 zatoshi  (0.125 ZEC)
```

**Approach:** The circuit witnesses `diff = v_total - MIN_WEIGHT`, constrains `diff + MIN_WEIGHT == v_total` via `AddChip`, and range-checks `diff` to `[0, 2^70)` using the `LookupRangeCheckConfig`.

- If `v_total >= MIN_WEIGHT`, then `diff` is a small non-negative integer that fits in 70 bits.
- If `v_total < MIN_WEIGHT`, then `diff` wraps to ~2^254 (field arithmetic is modular), which fails the range check.

**Why 70 bits?** 7 words x 10 bits/word = 70 bits. Comfortably covers u64 (64 bits) with 6 bits of headroom for the 4-note sum.

**Constructions:** `AddChip`, `LookupRangeCheckConfig`.

## 9. Note Commitment Integrity (x4)

Purpose: recompute each note's commitment in-circuit and extract `cmx` and `v` as internal wires for conditions 3 and 7.

```
NoteCommit_rcm(repr(g_d), repr(pk_d), v, rho, psi) = cm
cmx = ExtractP(cm)
```

The circuit recomputes NoteCommit from the per-note witness data and enforces strict equality against the witnessed `cm`. The resulting `cmx` (x-coordinate of the commitment point) flows to condition 3 (rho binding) and `v` flows to condition 7 (gov commitment) as internal wires — eliminating the need for `cmx_1..4` and `v_1..4` as free witnesses.

The `v` cell is a `NoteValue` inside NoteCommit. A separate `v_base` cell (as `pallas::Base`) is constrained equal to `v` and returned for the `AddChip` sum in condition 7.

**Constructions:** `SinsemillaChip` (config 1), `EccChip`, `NoteCommitChip` (signed config, reused from condition 1).

## 10. Merkle Path Validity (x4)

Purpose: prove that the note's commitment exists in the note commitment tree, gated by `is_note_real`.

```
root = MerklePath(cmx, pos, path)
is_note_real * (root - nc_root) = 0
```

The `GadgetMerklePath` gadget computes the Merkle root from the leaf (`cmx`) and the 32-level authentication path using Sinsemilla hashing. The `q_per_note` custom gate then enforces that either the computed root equals the public `nc_root` anchor or `is_note_real = 0` (padded note — root mismatch is allowed).

For padded notes, the path can be all-zeros; the Merkle computation still runs but the root-check gate is gated off.

**Constructions:** `MerkleChip` (configs 1+2), `SinsemillaChip` (configs 1+2 via MerkleChip), `q_per_note`.

## 11. Diversified Address Integrity (x4)

Purpose: prove each note's address was derived from the shared `ivk` established in condition 5.

```
pk_d = [ivk] * g_d
```

Where:
- **ivk** — the internal wire from condition 5. Converted to `ScalarVar` and reused for all 4 note slots.
- **g_d** — the diversified generator from the note's address.
- **pk_d** — the diversified transmission key from the note's address.

This ensures all four delegated notes belong to the same wallet (same `ivk` derived from `ak` and `nk`).

**Constructions:** `EccChip` (ScalarVar, variable-base mul).

## 12. Private Nullifier Derivation (x4)

Purpose: derive each note's true Orchard nullifier in-circuit. This nullifier is NOT published — it feeds into condition 13 (IMT non-membership) and condition 14 (governance nullifier).

```
real_nf = DeriveNullifier_nk(rho, psi, cm)
```

Same `DeriveNullifier` construction as condition 2, but applied to each delegated note. Uses the shared `nk` cell (witnessed once, reused across all slots).

**Constructions:** `PoseidonChip`, `AddChip`, `EccChip`.

## 13. IMT Non-Membership (x4)

Purpose: prove the note's nullifier has NOT been spent, using a Poseidon-based Indexed Merkle Tree with a (low, high) leaf model. Each leaf stores an explicit interval `[low, high]`.

**Approach:**

1. **Leaf hash**: `leaf_hash = Poseidon(low, high)` — authenticates both interval bounds via the Merkle root.

2. **Merkle path** (29 levels, starting from `leaf_hash`): At each level, a `q_imt_swap` gate conditionally swaps `(current, sibling)` into `(left, right)` based on the position bit, then `Poseidon(left, right)` computes the parent. The swap gate constrains:
   - `left = current + pos_bit * (sibling - current)`
   - `left + right = current + sibling`
   - `bool_check(pos_bit)`

3. **Root check**: The `q_per_note` gate constrains `imt_root = nf_imt_root` (the public input).

4. **Interval check** (`q_interval` gate): Proves `low <= real_nf <= high` (fully inclusive):
   - `y = high - low` (interval width)
   - `x = real_nf - low` (offset into interval)
   - `x_shifted = 2^250 - y + x - 1` (shifted for upper bound check)
   - `x` is range-checked to `[0, 2^250)` → `real_nf >= low`
   - `x_shifted` is range-checked to `[0, 2^250)` → `real_nf <= high`

**Leaf authentication**: `high` is authenticated via `Poseidon(low, high) → Merkle root` — a forged `high` produces the wrong root. No parity constraint needed; if the prover swaps (low, high), the interval check fails because `nf - low` wraps to a large field element.

**250-bit range bound assumption:** The 250-bit range check constrains bracket intervals to `< 2^250`. Since the Pallas field has order `p ≈ 2^254.9`, the IMT operator must pre-populate sentinel leaves at intervals of at most `2^250` to ensure every nullifier falls within a valid bracket. With ~17 evenly-spaced brackets (at multiples of `2^250`), the entire field is covered. The `SpacedLeafImtProvider` implements this strategy.

**Constructions:** `PoseidonChip`, `LookupRangeCheckConfig`, `q_imt_swap`, `q_interval`, `q_per_note`.

## 14. Governance Nullifier Publication (x4)

Purpose: derive a domain-separated governance nullifier that is published as a public input. This prevents double-voting without revealing the note's true Orchard nullifier.

```
gov_null = Poseidon(nk, Poseidon(domain_tag, Poseidon(vote_round_id, real_nf)))
```

Three nested Poseidon hashes (each `ConstantLength<2>`):
1. `intermediate = Poseidon(vote_round_id, real_nf)` — binds to the voting round.
2. `tagged = Poseidon(domain_tag, intermediate)` — separates from other nullifier domains. `domain_tag` = `"governance authorization"` encoded as a little-endian Pallas field element, assigned via `assign_advice_from_constant` so the value is baked into the verification key.
3. `gov_null = Poseidon(nk, tagged)` — binds to the nullifier key, making it unforgeable.

The result is constrained to the public input at offset `GOV_NULL_1..4`.

**Constructions:** `PoseidonChip`.

## 15. Padded-Note Zero-Value Enforcement (x4)

Purpose: ensure padded (unused) note slots contribute zero voting weight.

```
(1 - is_note_real) * v = 0
bool_check(is_note_real)
```

The `q_per_note` custom gate enforces:
1. `is_note_real` is boolean (0 or 1).
2. If `is_note_real = 0`, then `v = 0`. A padded note cannot carry value.

For real notes (`is_note_real = 1`), the constraint is trivially satisfied and `v` can be any value.

**Constructions:** `q_per_note`.

## FAQ

- **"Why is cm_signed witnessed as a Point but ak_P as a NonIdentityPoint?"** — ak_P being identity would be a degenerate key (any signature verifies). cm_signed being identity is cryptographically negligible and caught by the equality constraint with the recomputed commitment.

- **"What if the same proof is submitted twice?"** — The nullifier nf_signed is a public input. The consuming protocol must track spent nullifiers. The circuit itself is stateless.

- **"Why are psi and rcm witnessed, not derived in-circuit?"** — Both are derived from `rseed` using Blake2b out-of-circuit and provided as private inputs. If either is incorrect, the recomputed commitment will not match, and the proof will fail.

- **"Why two Sinsemilla configs (and two NoteCommitChips)?"** — This mirrors the audited Orchard action circuit, which uses two Sinsemilla configs (one for spend-side NoteCommit, one for output-side NoteCommit) with column assignments `advices[..5]` and `advices[5..]`. Each `SinsemillaChip::configure` call creates its own selectors and gates, and each `NoteCommitChip::configure` creates decomposition/canonicity gates tied to the Sinsemilla config it receives — so two Sinsemilla configs require two NoteCommitChips. We replicate this exact layout so the delegation circuit inherits the audited chip wiring without modification. It may be possible to collapse to a single config (condition 9 already runs 4 NoteCommits on config 1 without conflict), but reusing the known-correct pattern avoids the need for a separate audit of the chip interaction.

- **"Why do padded notes use the real ivk?"** — Padded notes must pass condition 11 (`pk_d = [ivk] * g_d`) using the same ivk derived in condition 5. The builder creates padded notes with `fvk.address_at(...)` so their addresses are valid under the real ivk. This is safe because padded notes have `v = 0` (enforced by condition 15) and `is_note_real = 0` (so condition 10 skips the Merkle root check). They contribute nothing to the vote weight but their governance nullifiers are still published (condition 14), which is harmless — the consuming protocol can ignore nullifiers for zero-value notes or treat them as no-ops.

