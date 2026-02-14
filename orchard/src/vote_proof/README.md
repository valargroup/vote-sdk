# Vote Proof Circuit (ZKP 2)

Proves that a registered voter is casting a valid vote, without revealing which VAN they hold. The structure follows the delegation circuit's pattern (ZKP 1) and implements conditions incrementally.

**Public inputs:** 7 field elements.
**Current K:** 12 (4,096 rows) — accommodates conditions 2, 4, 5, 6 plus the 10-bit lookup table.

## Inputs

- Public (7 field elements)
   * **van_nullifier** (offset 0): the nullifier of the old VAN being spent (prevents double-vote).
   * **vote_authority_note_new** (offset 1): the new VAN commitment with decremented proposal authority.
   * **vote_commitment** (offset 2): the vote commitment hash.
   * **vote_comm_tree_root** (offset 3): root of the Poseidon-based vote commitment tree at anchor height.
   * **vote_comm_tree_anchor_height** (offset 4): the vote-chain height at which the tree is snapshotted.
   * **proposal_id** (offset 5): which proposal this vote is for.
   * **voting_round_id** (offset 6): the voting round identifier — prevents cross-round replay.

- Private (VAN ownership — conditions 1–4)
   * **voting_hotkey_pk**: the voting hotkey public key (x-coordinate of the ECC point derived from `vsk`).
   * **total_note_value**: the voter's total delegated weight.
   * **proposal_authority_old**: remaining proposal authority bitmask in the old VAN.
   * **gov_comm_rand**: blinding randomness for the VAN commitment.
   * **vote_authority_note_old**: the old VAN commitment (Poseidon hash of its components).
   * **vote_comm_tree_path**: Poseidon-based Merkle authentication path (24 sibling hashes).
   * **vote_comm_tree_position**: leaf position in the vote commitment tree.
   * **vsk**: voting spending key (scalar for ECC multiplication).
   * **vsk_nk**: nullifier deriving key derived from `vsk`.

- Private (vote commitment — conditions 7–11)
   * **shares_1..4**: the voting share vector (each in `[0, 2^24)`).
   * **share_randomness_1..4**: El Gamal encryption randomness per share.
   * **vote_decision**: the voter's choice.

- Internal wires (not public inputs, not free witnesses)
   * **voting_round_id cell**: copied from the instance column, used in condition 2 Poseidon hash and condition 4 inner hash.
   * **domain_van_nullifier cell**: constant encoding of `"vote authority spend"` (condition 4).
   * **proposal_authority_new**: derived as `proposal_authority_old - 1` (condition 5).
   * **shares_hash**: derived from encrypted shares (condition 9).

## Condition 2: VAN Integrity ✅

Purpose: prove that the old VAN commitment is correctly constructed from its components. This binds the voter's identity, weight, round, authority, and blinding factor into a single authenticated leaf in the vote commitment tree.

```
vote_authority_note_old = Poseidon(DOMAIN_VAN, voting_hotkey_pk, total_note_value,
                                   voting_round_id, proposal_authority_old, gov_comm_rand)
```

Where:
- **DOMAIN_VAN**: `0`. Domain separation tag for Vote Authority Notes (vs `DOMAIN_VC = 1` for Vote Commitments). Assigned via `assign_advice_from_constant` so the value is baked into the verification key.
- **voting_hotkey_pk**: the voting hotkey public key. A single Pallas base field element (the x-coordinate of the ECC point derived from `vsk`). Shared with condition 3 (spend authority check).
- **total_note_value**: the voter's total delegated weight. Shared with condition 7 (shares sum check).
- **voting_round_id**: the vote round identifier (public input at offset 6). Copied from the instance column via `assign_advice_from_instance`, ensuring the in-circuit value matches the verifier's public input.
- **proposal_authority_old**: remaining proposal authority bitmask. Shared with condition 5 (decrement check).
- **gov_comm_rand**: random blinding factor. Prevents observers from brute-forcing the address or weight from the public VAN commitment.
- **vote_authority_note_old**: the witnessed VAN commitment. Constrained to equal the Poseidon output via `constrain_equal`.

**Function:** `Poseidon` with `ConstantLength<6>`. Uses `Pow5Chip` / `P128Pow5T3` with rate 2 (3 absorption rounds for 6 inputs).

**Constraint:** The circuit computes `derived_van = Poseidon(DOMAIN_VAN, voting_hotkey_pk, total_note_value, voting_round_id, proposal_authority_old, gov_comm_rand)` and enforces strict equality `derived_van == vote_authority_note_old`. Since `vote_authority_note_old` will also be used as the Merkle leaf in condition 1, this creates a binding: the VAN membership proof and the VAN integrity check are tied to the same commitment.

**Out-of-circuit helper:** `van_integrity_hash()` computes the same Poseidon hash outside the circuit for builder and test use.

**Constructions:** `PoseidonChip`.

## Condition 1: VAN Membership ✅

Purpose: prove the voter's VAN is registered in the vote commitment tree, without revealing which one.

```
MerklePath(vote_authority_note_old, vote_comm_tree_position, vote_comm_tree_path) = vote_comm_tree_root
```

Where:
- **vote_authority_note_old**: the Merkle leaf. Cell-equality-linked to condition 2's derived VAN hash, binding the membership proof to the same commitment.
- **vote_comm_tree_position**: leaf position in the tree (private witness). At each level, the position bit determines child ordering.
- **vote_comm_tree_path**: 24 sibling hashes along the authentication path (private witness).
- **vote_comm_tree_root**: the public tree anchor (public input at offset 3).

**Function:** Poseidon-based Merkle path verification (24 levels). At each level, a conditional swap gate orders (current, sibling) into (left, right) based on the position bit, then `Poseidon(left, right)` computes the parent. The hash function matches `vote_commitment_tree::MerkleHashVote::combine` — `Poseidon(left, right)` with no level tag.

**Structure:** 24 swap regions (1 row each) + 24 Poseidon `ConstantLength<2>` hashes (~1,560 total rows). The swap gate (`q_merkle_swap`) constrains:
- `left = current + pos_bit * (sibling - current)` — selects current or sibling
- `left + right = current + sibling` — conservation
- `pos_bit ∈ {0, 1}` — boolean check

Identical to the delegation circuit's `q_imt_swap` gate.

**Constraint:** The circuit computes the Merkle root from the leaf and path, then enforces `constrain_instance(computed_root, VOTE_COMM_TREE_ROOT)` — binding the derived root to the public input at offset 3.

**Out-of-circuit helper:** `poseidon_hash_2()` computes `Poseidon(a, b)` outside the circuit for builder and test use.

**Constructions:** `PoseidonChip`, `q_merkle_swap` selector.

## Condition 3: Spend Authority ✅

Purpose: prove the voter controls the hotkey delegated to in Phase 1–2.

```
voting_hotkey_pk = ExtractP([vsk] * G)
```

Where **G** is the SpendAuth fixed base (`OrchardFixedBasesFull::SpendAuthG`), matching the delegation circuit's spend authority pattern. The circuit witnesses `vsk` as a full-width scalar, computes `[vsk] * SpendAuthG` via fixed-base scalar multiplication, extracts the x-coordinate, and constrains it to equal the witnessed `voting_hotkey_pk` (the same cell used in conditions 2, 4, 5, 6).

**Constructions:** `EccChip`, `ScalarFixed`, `FixedPoint`.

## Condition 4: VAN Nullifier Integrity ✅

Purpose: derive a nullifier that prevents double-voting without revealing the VAN.

```
step1          = Poseidon(voting_round_id, vote_authority_note_old)
step2          = Poseidon("vote authority spend", step1)
van_nullifier  = Poseidon(vsk_nk, step2)
```

Three-layer `ConstantLength<2>` chain matching ZKP 1 condition 14's governance nullifier pattern:

- **Step 1** `Poseidon(voting_round_id, vote_authority_note_old)` — scope to this round and VAN. Both cells are reused from condition 2 via cell equality (not re-witnessed), binding conditions 2 and 4 together.
- **Step 2** `Poseidon("vote authority spend", step1)` — domain separation. The tag is encoded as a Pallas field element from its UTF-8 bytes (see `domain_van_nullifier()`), assigned via `assign_advice_from_constant` so the value is baked into the verification key.
- **Step 3** `Poseidon(vsk_nk, step2)` — key with the nullifier deriving key. `vsk_nk` is a private witness derived from `vsk` out-of-circuit. Different from `vsk` itself: `vsk` is a scalar used for ECC in condition 3, while `vsk_nk` is a base field element.

**Structure:** Three chained `ConstantLength<2>` Poseidon hashes (3 permutations, 192 rows). Each step has a clear semantic role: scoping, domain separation, keying. This uniform structure matches ZKP 1 condition 14.

**Constraint:** The circuit computes the nested hash and enforces `constrain_instance(result, VAN_NULLIFIER)` — binding the derived value to the public input at offset 0. This is the first `constrain_instance` call in the circuit.

**Out-of-circuit helper:** `van_nullifier_hash()` computes the same nested Poseidon hash outside the circuit. `domain_van_nullifier()` returns the domain separator constant.

**Constructions:** `PoseidonChip` (reused from condition 2), `constrain_instance`.

## Condition 5: Proposal Authority Decrement ✅

Purpose: ensure the voter still has authority and correctly decrements it.

```
proposal_authority_new = proposal_authority_old - 1
proposal_authority_old > 0
```

Where:
- **proposal_authority_old**: the remaining proposal authority from the old VAN. Reused from condition 2's witness cell via cell equality.
- **proposal_authority_new**: witnessed as `proposal_authority_old - 1` and constrained via `AddChip`: `proposal_authority_new + 1 == proposal_authority_old`.
- **Range check**: `proposal_authority_new` is range-checked to [0, 2^70) using `LookupRangeCheckConfig` with 7 × 10-bit words. If `proposal_authority_old == 0`, the subtraction wraps to `p - 1 ≈ 2^254`, failing the range check — enforcing `proposal_authority_old > 0`.

**Structure:** One `AddChip` constraint (1 row) + one running-sum range check (8 rows). The constant `1` is assigned via `assign_advice_from_constant`, baking it into the verification key.

**Constructions:** `AddChip`, `LookupRangeCheckConfig`.

## Condition 6: New VAN Integrity ✅

Purpose: the new VAN has the same fields as the old except with decremented authority.

```
vote_authority_note_new = Poseidon(DOMAIN_VAN, voting_hotkey_pk, total_note_value,
                                   voting_round_id, proposal_authority_new, gov_comm_rand)
```

Where:
- All inputs except `proposal_authority_new` are cell-equality-linked to the same witness cells used in condition 2.
- **proposal_authority_new**: flows from condition 5's output. This is the only difference between the condition 2 and condition 6 Poseidon hashes.

**Function:** `Poseidon` with `ConstantLength<6>` (same as condition 2).

**Constraint:** The circuit computes the hash and enforces `constrain_instance(derived_van_new, VOTE_AUTHORITY_NOTE_NEW)` — binding the result to the public input at offset 1.

**Out-of-circuit helper:** Reuses `van_integrity_hash()` with `proposal_authority_old - 1` as the authority argument.

**Constructions:** `PoseidonChip`.

## Condition 7: Shares Sum Correctness ✅

Purpose: voting shares decomposition is consistent with the total delegated weight.

```
sum(share_0, share_1, share_2, share_3) = total_note_value
```

Where:
- **share_0..share_3**: the 4 plaintext voting shares (private witnesses). Each share represents a portion of the voter's delegated weight allocated to one of the 4 vote options. These cells will also be used by condition 8 (range check) and condition 10 (El Gamal encryption inputs).
- **total_note_value**: the voter's total delegated weight. Cell-equality-linked to the same witness cell used in condition 2 (VAN integrity), binding the shares decomposition to the authenticated VAN.

**Structure:** Three chained `AddChip` additions (3 rows):
- `partial_1 = share_0 + share_1`
- `partial_2 = partial_1 + share_2`
- `shares_sum = partial_2 + share_3`

**Constraint:** `constrain_equal(shares_sum, total_note_value)` — the sum of all 4 shares must exactly equal the voter's total delegated weight. This prevents the voter from creating or destroying voting power during the share decomposition.

**Constructions:** `AddChip` (reused from condition 5).

## Condition 8: Shares Range ✅

Purpose: prevent overflow by ensuring each share fits in a bounded range.

```
Each share_i in [0, 2^30)
```

Where:
- **share_0..share_3**: the 4 plaintext voting shares from condition 7. Each share must fit in a bounded range to prevent overflow when shares are used in El Gamal encryption (condition 10) and accumulated homomorphically during tally.

**Bound:** The protocol spec targets `[0, 2^24)`, but halo2_gadgets v0.3's `short_range_check` is `pub(crate)` (private to the gadget crate), so the exact 24-bit decomposition (2 × 10-bit + 4-bit short check) is unavailable. We use the next 10-bit-aligned bound: `[0, 2^30)` via 3 × 10-bit words with strict mode. 30 bits (~1B per share) is secure: max sum of 4 shares ≈ 4B, well within the Pallas field, and the homomorphic tally accumulates over far fewer voters than 2^30.

**Structure:** For each share, one `copy_check` call (4 calls total, ~16 rows):
- `copy_check(share_i, 3, true)` — decomposes the share into 3 × 10-bit lookup words. Each word is range-checked via the 10-bit lookup table. The `true` (strict) flag constrains the final running sum `z_3` to equal 0, enforcing `share < 2^30`.

If a share exceeds `2^30` or is a wrapped large field element (e.g. `p - k` from underflow), the 3-word decomposition produces a non-zero `z_3`, which fails the strict check.

**Note:** Share cells are cloned for `copy_check` (which takes ownership). The original cells remain available for condition 10 (El Gamal encryption inputs).

**Constructions:** `LookupRangeCheckConfig` (reused from condition 5).

## Condition 9: Shares Hash Integrity (TODO)

Purpose: shares hash is correctly computed over the 4 encrypted shares.

```
shares_hash = H(enc_share_1, enc_share_2, enc_share_3, enc_share_4)
```

**Constructions:** `PoseidonChip`.

## Condition 10: Encryption Integrity (TODO)

Purpose: each ciphertext is a valid El Gamal encryption of the corresponding plaintext share.

```
Each enc_share_i = ElGamal(shares_i, r_i, ea_pk) = (r_i * G, shares_i * G + r_i * ea_pk)
```

**Constructions:** `EccChip`.

## Condition 11: Vote Commitment Integrity (TODO)

Purpose: the public vote commitment is correctly constructed from the shares hash and the vote choice.

```
vote_commitment = H(DOMAIN_VC, shares_hash, proposal_id, vote_decision)
```

**Constructions:** `PoseidonChip`.

## Column Layout

| Columns | Current use | Future use |
|---------|------------|------------|
| `advices[0..5]` | General witness assignment | Sinsemilla/Merkle (if needed) |
| `advices[5]` | Poseidon partial S-box | — |
| `advices[6]` | Poseidon state + AddChip output (c) | — |
| `advices[7]` | Poseidon state + AddChip input (a) | — |
| `advices[8]` | Poseidon state + AddChip input (b) | — |
| `advices[9]` | Range check running sum | — |
| `lagrange_coeffs[0]` | Constants (DOMAIN_VAN, ONE) | Constants (DOMAIN_VC, etc.) |
| `lagrange_coeffs[1]` | Unused | ECC / Sinsemilla |
| `lagrange_coeffs[2..5]` | Poseidon rc_a | — |
| `lagrange_coeffs[5..8]` | Poseidon rc_b | — |
| `table_idx` | 10-bit lookup table [0, 1024) | — |
| `primary` | 7 public inputs | — |
