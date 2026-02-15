# Vote Proof Circuit (ZKP 2)

Proves that a registered voter is casting a valid vote, without revealing which VAN they hold. The structure follows the delegation circuit's pattern (ZKP 1). All 11 conditions are fully constrained.

**Public inputs:** 9 field elements.
**Current K:** 14 (16,384 rows) — accommodates all 11 conditions, including 12 variable-base ECC scalar multiplications (condition 10), ~31 Poseidon hashes, and the 10-bit lookup table.

## Inputs

- Public (9 field elements)
   * **van_nullifier** (offset 0): the nullifier of the old VAN being spent (prevents double-vote).
   * **vote_authority_note_new** (offset 1): the new VAN commitment with decremented proposal authority.
   * **vote_commitment** (offset 2): the vote commitment hash `H(DOMAIN_VC, shares_hash, proposal_id, vote_decision)`.
   * **vote_comm_tree_root** (offset 3): root of the Poseidon-based vote commitment tree at anchor height.
   * **vote_comm_tree_anchor_height** (offset 4): the vote-chain height at which the tree is snapshotted.
   * **proposal_id** (offset 5): which proposal this vote is for.
   * **voting_round_id** (offset 6): the voting round identifier — prevents cross-round replay.
   * **ea_pk_x** (offset 7): x-coordinate of the election authority public key (El Gamal encryption key).
   * **ea_pk_y** (offset 8): y-coordinate of the election authority public key. Both coordinates are public to prevent sign-ambiguity attacks (using −ea_pk would corrupt the tally).

- Private (VAN ownership — conditions 1–4)
   * **vpk_g_d**: voting public key — diversified base point (full affine point from DiversifyHash(d)). Witnessed as `NonIdentityPoint`; x-coordinate extracted for Poseidon hashing (conditions 2, 6). This is the `vpk_d` component of the voting hotkey address. Matches ZKP 1 (delegation) VAN structure.
   * **vpk_pk_d**: voting public key — diversified transmission key (full affine point, pk_d = [ivk_v] * g_d). Witnessed as `NonIdentityPoint`; x-coordinate extracted for Poseidon hashing (conditions 2, 6). Condition 3 constrains this to equal `[ivk_v] * vpk_g_d`. Matches ZKP 1 VAN structure.
   * **total_note_value**: the voter's total delegated weight.
   * **proposal_authority_old**: remaining proposal authority bitmask in the old VAN.
   * **gov_comm_rand**: blinding randomness for the VAN commitment.
   * **vote_authority_note_old**: the old VAN commitment (two-layer Poseidon hash, same structure as ZKP 1 gov_comm).
   * **vote_comm_tree_path**: Poseidon-based Merkle authentication path (24 sibling hashes).
   * **vote_comm_tree_position**: leaf position in the vote commitment tree.
   * **vsk**: voting spending key (scalar for ECC multiplication). Used in condition 3 for `[vsk] * SpendAuthG`.
   * **rivk_v**: CommitIvk randomness (scalar). Blinding factor for `CommitIvk(ak, nk, rivk_v)` in condition 3.
   * **vsk_nk**: nullifier deriving key derived from `vsk`. Shared between condition 3 (CommitIvk `nk` input) and condition 4 (VAN nullifier keying).

- Private (vote commitment — conditions 7–11)
   * **shares_1..4**: the voting share vector (each in `[0, 2^24)`).
   * **enc_share_c1_x[0..3]**: x-coordinates of C1_i = r_i * G (El Gamal first component, via ExtractP).
   * **enc_share_c2_x[0..3]**: x-coordinates of C2_i = shares_i * G + r_i * ea_pk (El Gamal second component, via ExtractP).
   * **share_randomness[0..3]**: El Gamal encryption randomness per share (base field elements, converted to scalars via `ScalarVar::from_base` in-circuit).
   * **ea_pk**: election authority public key as a Pallas affine point (witnessed as `NonIdentityPoint`, constrained to public inputs at offsets 7–8).
   * **vote_decision**: the voter's choice.

- Internal wires (not public inputs, not free witnesses)
   * **voting_round_id cell**: copied from the instance column, used in condition 2 Poseidon hash and condition 4 inner hash.
   * **domain_van_nullifier cell**: constant encoding of `"vote authority spend"` (condition 4).
   * **proposal_authority_new**: derived as `proposal_authority_old - 1` (condition 5).
   * **shares_hash**: Poseidon hash of 8 enc_share x-coordinates (condition 9). Internal wire consumed by condition 11.
   * **SpendAuthG x, y constants**: coordinates of the El Gamal generator (condition 10). Baked into the verification key via `assign_advice_from_constant`.
   * **ea_pk_x, ea_pk_y cells**: copied from the instance column (condition 10). Each ea_pk `NonIdentityPoint` witness is constrained to match these cells.
   * **DOMAIN_VC constant**: `1`. Domain separation tag for Vote Commitments (condition 11). Baked into the verification key.
   * **proposal_id cell**: copied from the instance column (condition 11). Used in the vote commitment Poseidon hash.

## Condition 2: VAN Integrity ✅

Purpose: prove that the old VAN commitment is correctly constructed from its components. Uses the **same two-layer hash structure as ZKP 1 (delegation)** so that a VAN created by the delegation circuit can be spent (opened) by the vote proof circuit.

```
gov_comm_core = Poseidon(DOMAIN_VAN, vpk_g_d, vpk_pk_d, total_note_value,
                         voting_round_id, proposal_authority_old)
vote_authority_note_old = Poseidon(gov_comm_core, gov_comm_rand)
```

Where:
- **DOMAIN_VAN**: `0`. Domain separation tag for Vote Authority Notes (vs `DOMAIN_VC = 1` for Vote Commitments). Assigned via `assign_advice_from_constant` so the value is baked into the verification key.
- **vpk_g_d**, **vpk_pk_d**: voting public key address components (diversified base and transmission key x-coordinates). Same encoding as in ZKP 1 condition 7, so a VAN created by delegation has the same commitment structure.
- **total_note_value**: the voter's total delegated weight. Shared with condition 7 (shares sum check).
- **voting_round_id**: the vote round identifier (public input at offset 6). Copied from the instance column via `assign_advice_from_instance`, ensuring the in-circuit value matches the verifier's public input.
- **proposal_authority_old**: remaining proposal authority bitmask. Shared with condition 5 (decrement check).
- **gov_comm_rand**: random blinding factor. Prevents observers from brute-forcing the address or weight from the public VAN commitment.
- **vote_authority_note_old**: the witnessed VAN commitment. Constrained to equal the two-layer Poseidon output via `constrain_equal`.

**Function:** Two Poseidon invocations: first `ConstantLength<6>` (core), then `ConstantLength<2>` (core, gov_comm_rand). Uses `Pow5Chip` / `P128Pow5T3` with rate 2. Matches delegation circuit condition 7 (gov_comm) structure.

**Constraint:** The circuit computes the two-layer hash and enforces strict equality with `vote_authority_note_old`. Since `vote_authority_note_old` will also be used as the Merkle leaf in condition 1, this creates a binding: the VAN membership proof and the VAN integrity check are tied to the same commitment.

**Out-of-circuit helper:** `van_integrity::van_integrity_hash(vpk_g_d, vpk_pk_d, total_note_value, voting_round_id, proposal_authority_old, gov_comm_rand)` from the shared `circuit::van_integrity` module computes the same two-layer hash outside the circuit for builder and test use. (Note: the shared module's parameter names are `g_d_x`/`pk_d_x`.)

**Constructions:** `van_integrity::van_integrity_poseidon` (shared gadget from `circuit::van_integrity`).

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

Purpose: prove the voter controls the voting hotkey address delegated to in Phase 1–2. Uses the same CommitIvk chain as ZKP 1 (delegation) condition 5.

```
vsk_ak      = [vsk] * SpendAuthG               (fixed-base ECC mul)
ak          = ExtractP(vsk_ak)                  (x-coordinate)
ivk_v       = CommitIvk_rivk_v(ak, vsk.nk)     (Sinsemilla commitment)
vpk_pk_d    = [ivk_v] * vpk_g_d                (variable-base ECC mul)
```

Where:
- **vsk**: voting spending key (private witness, `pallas::Scalar`). The secret key that authorizes vote casting.
- **SpendAuthG**: fixed generator point on the Pallas curve, reused from the Zcash Orchard protocol. Used both here (condition 3) and in condition 10 (El Gamal generator).
- **ak**: the spend validating key's x-coordinate, derived in-circuit from `[vsk] * SpendAuthG` then `ExtractP`. Not a separate witness — it's an internal wire.
- **vsk_nk**: nullifier deriving key (private witness, `pallas::Base`). The same cell is shared with condition 4 (VAN nullifier keying). Witnessed before condition 3 in the synthesize flow.
- **rivk_v**: CommitIvk randomness (private witness, `pallas::Scalar`). Blinding factor for the Sinsemilla commitment.
- **ivk_v**: the incoming viewing key, derived in-circuit via `CommitIvk(ak, nk, rivk_v)`. Internal wire — flows from CommitIvk output to variable-base ECC mul input via `ScalarVar::from_base`.
- **vpk_g_d**: diversified base point (private witness, full affine point). Witnessed as `NonIdentityPoint`. The x-coordinate is extracted for Poseidon hashing in conditions 2 and 6.
- **vpk_pk_d**: diversified transmission key (private witness, full affine point). Witnessed as `NonIdentityPoint`. Constrained to equal the derived `[ivk_v] * vpk_g_d` via `Point::constrain_equal`.

**Structure:** Five steps in a scoped block:
1. `ScalarFixed::new(vsk)` → `FixedPoint::mul(SpendAuthG)` → `vsk_ak_point` (fixed-base scalar mul, ~500 rows)
2. `vsk_ak_point.extract_p()` → `ak` (x-coordinate extraction, 0 rows — just cell wiring)
3. `ScalarFixed::new(rivk_v)` → `commit_ivk_gadget(sinsemilla_chip, ecc_chip, commit_ivk_chip, ak, vsk_nk, rivk_v)` → `ivk_v` (Sinsemilla commitment with canonicity checks, ~2,000 rows)
4. `ScalarVar::from_base(ivk_v)` → `vpk_g_d_point.mul(ivk_v_scalar)` → `derived_vpk_pk_d` (variable-base scalar mul, ~500 rows)
5. `derived_vpk_pk_d.constrain_equal(vpk_pk_d_point)` — full point equality (both coordinates)

**Chip dependencies:** `SinsemillaChip` (for the CommitIvk Sinsemilla hash), `CommitIvkChip` (for ak/nk canonicity gate), `EccChip` (for fixed-base and variable-base scalar multiplication). The Sinsemilla chip also loads the 10-bit lookup table used by conditions 5 and 8 (replacing the manual table loading).

**Constraint:** The circuit derives vpk_pk_d from vsk → ak → ivk_v → [ivk_v] * vpk_g_d and enforces full point equality with the witnessed vpk_pk_d. Since vpk_pk_d's x-coordinate flows into conditions 2 and 6 (VAN integrity hashes), and vpk_g_d's x-coordinate flows into the same hashes, any mismatch in the key hierarchy would break conditions 2/3/6 simultaneously.

**Security properties:**
- **Key binding:** The CommitIvk chain cryptographically binds vsk to the VAN address (vpk_g_d, vpk_pk_d). A prover who doesn't know vsk cannot produce a valid ivk_v that maps vpk_g_d to vpk_pk_d.
- **Canonicity:** The CommitIvk gadget enforces canonical decomposition of ak and nk, preventing malleability attacks where different bit representations produce the same commitment.
- **Non-identity:** Both vpk_g_d and vpk_pk_d are witnessed as `NonIdentityPoint`, ensuring they are not the curve identity (which would trivially satisfy the constraint for any ivk_v).
- **Shared nk:** Using the same vsk_nk cell for both CommitIvk (condition 3) and the VAN nullifier (condition 4) ensures the nullifier is bound to the same key hierarchy that authorizes the vote.

**Out-of-circuit helper:** `derive_voting_address(vsk, nk, rivk_v)` in tests performs the same computation: `[vsk] * SpendAuthG → ExtractP → CommitIvk → [ivk_v] * g_d`. Uses `CommitDomain::short_commit` from `halo2_gadgets::sinsemilla::primitives`.

**Constructions:** `SinsemillaChip`, `CommitIvkChip`, `EccChip`, `FixedPoint`, `ScalarFixed`, `NonIdentityPoint`, `ScalarVar`, `Point::constrain_equal`.

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

Purpose: the new VAN has the same structure as the old (ZKP 1–compatible two-layer hash) except with decremented authority.

Same two-layer formula as condition 2: `gov_comm_core = Poseidon(DOMAIN_VAN, vpk_g_d, vpk_pk_d, total_note_value, voting_round_id, proposal_authority_new)` then `vote_authority_note_new = Poseidon(gov_comm_core, gov_comm_rand)`.

Where:
- **vpk_g_d**, **vpk_pk_d**, **total_note_value**, **voting_round_id**, **gov_comm_rand** are cell-equality-linked to the same witness cells used in condition 2.
- **proposal_authority_new**: flows from condition 5's output. This is the only difference between the condition 2 and condition 6 hashes.

**Constraint:** The circuit computes the two-layer hash and enforces `constrain_instance(derived_van_new, VOTE_AUTHORITY_NOTE_NEW)` — binding the result to the public input at offset 1.

**Out-of-circuit helper:** Reuses `van_integrity::van_integrity_hash(vpk_g_d, vpk_pk_d, total_note_value, voting_round_id, proposal_authority_new, gov_comm_rand)` with `proposal_authority_new = proposal_authority_old - 1`. (Note: the shared module's parameter names are `g_d_x`/`pk_d_x`.)

**Constructions:** `van_integrity::van_integrity_poseidon` (shared gadget from `circuit::van_integrity`).

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

## Condition 9: Shares Hash Integrity ✅

Purpose: commit to the 4 El Gamal ciphertext pairs so they can be verified in conditions 10 and 11 without re-witnessing.

```
shares_hash = Poseidon(c1_0_x, c2_0_x, c1_1_x, c2_1_x, c1_2_x, c2_2_x, c1_3_x, c2_3_x)
```

Where:
- **c1_i_x**: x-coordinate of `C1_i = r_i * G` (the El Gamal randomness point for share `i`), extracted via `ExtractP`. Private witness field `enc_share_c1_x[i]`.
- **c2_i_x**: x-coordinate of `C2_i = shares_i * G + r_i * ea_pk` (the El Gamal ciphertext point for share `i`), extracted via `ExtractP`. Private witness field `enc_share_c2_x[i]`.

The 8 x-coordinates are interleaved per share — `(c1_0, c2_0, c1_1, c2_1, ...)` — for locality. This matches the order used in ZKP 3 (vote reveal proof), where the server recomputes `shares_hash` from the 4 ciphertexts in the witness.

**Function:** `Poseidon` with `ConstantLength<8>`. Uses `Pow5Chip` / `P128Pow5T3` with rate 2 (4 absorption rounds for 8 inputs, ~5 permutations, ~320 rows).

**Constraint:** The circuit computes the Poseidon hash over all 8 witness values. The resulting `shares_hash` cell is an internal wire — it is not directly bound to any public input. Instead, condition 11 consumes it as an input to `H(DOMAIN_VC, shares_hash, proposal_id, vote_decision)`, which IS bound to `VOTE_COMMITMENT`.

**Relationship to other conditions:**
- Condition 10 constrains that the witnessed `(c1_i_x, c2_i_x)` values are valid El Gamal encryptions of the corresponding plaintext shares from conditions 7/8. The enc_share cells are cloned before the Poseidon hash and reused as `constrain_equal` targets in condition 10.
- Condition 11 chains `shares_hash` into the full vote commitment via `H(DOMAIN_VC, shares_hash, proposal_id, vote_decision)`, which is bound to `VOTE_COMMITMENT` at offset 2.

**Out-of-circuit helper:** `shares_hash()` computes the same Poseidon hash outside the circuit for builder and test use.

**Constructions:** `PoseidonChip` (reused from conditions 1, 2, 4, 6).

## Condition 10: Encryption Integrity ✅

Purpose: each ciphertext is a valid El Gamal encryption of the corresponding plaintext share under the election authority's public key.

```
For each share i (0..3):
    C1_i = [r_i] * G                        (randomness point)
    C2_i = [v_i] * G + [r_i] * ea_pk        (ciphertext point)
    ExtractP(C1_i) == enc_share_c1_x[i]      (link to condition 9)
    ExtractP(C2_i) == enc_share_c2_x[i]      (link to condition 9)
```

Where:
- **G**: SpendAuthG, the El Gamal generator. Both x and y coordinates are assigned via `assign_advice_from_constant`, baking them into the verification key. Each NonIdentityPoint witness of G is constrained to match these constants, preventing a malicious prover from using a different (or negated) generator.
- **r_i**: El Gamal randomness for share `i` (private witness, `pallas::Base`). Converted to `ScalarVar` via `ScalarVar::from_base` for variable-base ECC multiplication. The same cell is cloned and used for both `[r_i] * G` (C1) and `[r_i] * ea_pk` (C2), ensuring the same randomness binds both ciphertext components.
- **v_i**: plaintext share value from conditions 7/8. Cell-equality-linked to the same cells used in `AddChip` (condition 7) and range check (condition 8). Converted to `ScalarVar` via `ScalarVar::from_base` for ECC multiplication.
- **ea_pk**: election authority public key (Pallas curve point, public input at offsets 7–8). Witnessed as a `NonIdentityPoint` (on-curve constraint included). Both x and y coordinates are constrained to match the instance column cells, preventing a prover from using a different or negated key.
- **enc_share_c1_x[i]**, **enc_share_c2_x[i]**: the x-coordinate cells from condition 9's witness region. These are the same cells that were hashed into `shares_hash` by condition 9's Poseidon hash. Condition 10 constrains the ECC computation output to match them via `constrain_equal`, creating a binding between the Poseidon hash (condition 9) and the actual El Gamal encryption.

**Structure:** For each of the 4 shares (iterated in a loop):
1. Witness G as `NonIdentityPoint`, constrain x/y to SpendAuthG constants
2. `ScalarVar::from_base(r_i)` → variable-base mul → C1 point
3. `constrain_equal(ExtractP(C1), enc_c1_x[i])`
4. Witness G again as `NonIdentityPoint` (consumed by mul), constrain x/y
5. `ScalarVar::from_base(share[i])` → variable-base mul → vG point
6. Witness ea_pk as `NonIdentityPoint`, constrain x/y to public inputs
7. `ScalarVar::from_base(r_i clone)` → variable-base mul → rP point
8. `vG.add(rP)` → C2 point
9. `constrain_equal(ExtractP(C2), enc_c2_x[i])`

Total: 12 variable-base scalar multiplications (~6,000 rows), 4 point additions, 12 `NonIdentityPoint` witnesses (8× G + 4× ea_pk), 8 coordinate `constrain_equal` constraints. This is why K was bumped from 12 to 14.

**Scalar field handling:** All scalars (r_i, v_i) are base field elements converted via `ScalarVar::from_base`. This avoids cross-field consistency issues between `pallas::Base` and `pallas::Scalar`. For shares (< 2^30, guaranteed by condition 8), the integer representation is identical in both fields. For randomness, the probability of a base element exceeding the scalar field modulus is ≈ 2^{-254}.

**Security properties:**
- **Generator binding:** Each G witness is constrained to SpendAuthG's constant coordinates (both x and y), preventing the prover from using a negated generator. Using −G for v*G would produce C2 = −v*G + r*ea_pk, which decrypts to −v instead of v, corrupting the tally.
- **ea_pk binding:** Both ea_pk coordinates are public inputs, so the verifier checks them against the published round parameter. This prevents the prover from encrypting under a different key.
- **Randomness binding:** The same r_i cell (via clone) is used for both C1 and C2 computations. Cell equality ensures both `ScalarVar::from_base` calls decompose the same value.

**Out-of-circuit helpers:** `elgamal_encrypt()` computes the same El Gamal encryption outside the circuit. `spend_auth_g_affine()` returns the SpendAuthG generator as a Pallas affine point. `base_to_scalar()` converts base field elements to scalars.

**Constructions:** `EccChip`, `NonIdentityPoint`, `ScalarVar`, `Point::add`, `Point::extract_p`.

## Condition 11: Vote Commitment Integrity ✅

Purpose: the public vote commitment is correctly constructed from the shares hash, the proposal choice, and the vote decision. This is the final hash that is posted on-chain, inserted into the vote commitment tree, and later opened by ZKP #3 (vote reveal).

```
vote_commitment = Poseidon(DOMAIN_VC, shares_hash, proposal_id, vote_decision)
```

Where:
- **DOMAIN_VC**: `1`. Domain separation tag for Vote Commitments (vs `DOMAIN_VAN = 0`). Assigned via `assign_advice_from_constant` so the value is baked into the verification key. Prevents a vote commitment from ever colliding with a VAN in the shared vote commitment tree.
- **shares_hash**: the Poseidon hash of all 8 enc_share x-coordinates, computed in condition 9. This is a purely internal wire (not a public input) — it flows from condition 9's output cell directly into condition 11's Poseidon input, ensuring the vote commitment is bound to the actual El Gamal ciphertexts without re-hashing.
- **proposal_id**: which proposal this vote is for (public input at offset 5). Copied from the instance column via `assign_advice_from_instance`. The verifier checks it matches a valid proposal in the voting window.
- **vote_decision**: the voter's choice (private witness). Hidden inside the vote commitment — only revealed in ZKP #3 when individual shares are opened. The decision value is opaque to the circuit; its semantic meaning is defined by the application layer.

**Function:** `Poseidon` with `ConstantLength<4>`. Uses `Pow5Chip` / `P128Pow5T3` with rate 2 (2 absorption rounds for 4 inputs, ~3 permutations, ~200 rows).

**Constraint:** The circuit computes the Poseidon hash and enforces `constrain_instance(vote_commitment, VOTE_COMMITMENT)` — binding the derived value to the public input at offset 2. This is the terminal constraint of the vote commitment construction chain: conditions 7–8 validate the plaintext shares, condition 9 hashes the ciphertexts, condition 10 proves the ciphertexts are valid El Gamal encryptions, and condition 11 wraps everything into a single public commitment.

**Data flow (conditions 7–11):**
```
shares (7: sum, 8: range) ──┐
                             ├─ enc_shares (10: El Gamal) ──→ shares_hash (9: Poseidon<8>)
randomness ──────────────────┘                                       │
                                                                     ├─ vote_commitment (11: Poseidon<4>) ──→ VOTE_COMMITMENT
proposal_id ─────────────────────────────────────────────────────────┤
vote_decision ───────────────────────────────────────────────────────┘
```

**Out-of-circuit helper:** `vote_commitment_hash()` computes the same Poseidon hash outside the circuit for builder and test use.

**Constructions:** `PoseidonChip` (reused from conditions 1, 2, 4, 6, 9).

## Column Layout

| Columns | Use |
|---------|-----|
| `advices[0..5]` | General witness assignment, ECC (cond 3, 10), Sinsemilla/CommitIvk (cond 3) |
| `advices[5]` | Poseidon partial S-box |
| `advices[6]` | Poseidon state + AddChip output (c) |
| `advices[7]` | Poseidon state + AddChip input (a) |
| `advices[8]` | Poseidon state + AddChip input (b) |
| `advices[9]` | Range check running sum |
| `lagrange_coeffs[0]` | Constants (DOMAIN_VAN, DOMAIN_VC, ONE, SpendAuthG x/y) |
| `lagrange_coeffs[1]` | ECC Lagrange coefficients |
| `lagrange_coeffs[2..5]` | Poseidon rc_a |
| `lagrange_coeffs[5..8]` | Poseidon rc_b |
| `table_idx` (+ additional lookup columns) | 10-bit lookup table [0, 1024), Sinsemilla lookup (loaded by `SinsemillaChip`) |
| `primary` | 9 public inputs |

## Chip Summary

| Chip | Conditions | Role |
|------|-----------|------|
| `PoseidonChip` (Pow5) | 1, 2, 4, 6, 9, 11 | Poseidon hashing (Merkle paths, VAN integrity, nullifiers, shares hash, vote commitment) |
| `EccChip` | 3, 10 | Fixed-base and variable-base scalar multiplication, point addition, ExtractP |
| `SinsemillaChip` | 3 | Sinsemilla hash inside CommitIvk |
| `CommitIvkChip` | 3 | Canonicity gate for ak/nk decomposition in CommitIvk |
| `AddChip` | 5, 7 | Field element addition (authority decrement, shares sum) |
| `LookupRangeCheckConfig` | 5, 8 | 10-bit running-sum range checks |
