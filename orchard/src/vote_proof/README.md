# Vote Proof Circuit (ZKP 2)

Proves that a registered voter is casting a valid vote, without revealing which VAN they hold. The structure follows the delegation circuit's pattern (ZKP 1). Numbering matches Gov Steps V1 (ZKP #2): 12 conditions total; all conditions 1–12 are fully constrained in-circuit (condition 4 enforces spend authority `r_vpk = vsk.ak + [alpha_v]*G` in-circuit; the vote signature is verified out-of-circuit under `r_vpk`).

**Public inputs:** 11 field elements.
**Current K:** 14 (16,384 rows) — accommodates conditions 1–4 and 5–12, including 15 variable-base ECC scalar multiplications (condition 11), ~31 Poseidon hashes, and the 10-bit lookup table.

## Inputs

- Public (11 field elements)
   * **van_nullifier** (offset 0): the nullifier of the old VAN being spent (prevents double-vote).
   * **r_vpk_x** (offset 1): x-coordinate of the rerandomized voting key `r_vpk = vsk.ak + [alpha_v]*G` (condition 4).
   * **r_vpk_y** (offset 2): y-coordinate of `r_vpk`. Links to the vote signature verified out-of-circuit.
   * **vote_authority_note_new** (offset 3): the new VAN commitment with decremented proposal authority.
   * **vote_commitment** (offset 4): the vote commitment hash `H(DOMAIN_VC, shares_hash, proposal_id, vote_decision)`.
   * **vote_comm_tree_root** (offset 5): root of the Poseidon-based vote commitment tree at anchor height.
   * **vote_comm_tree_anchor_height** (offset 6): the vote-chain height at which the tree is snapshotted.
   * **proposal_id** (offset 7): which proposal this vote is for.
   * **voting_round_id** (offset 8): the voting round identifier — prevents cross-round replay.
   * **ea_pk_x** (offset 9): x-coordinate of the election authority public key (El Gamal encryption key).
   * **ea_pk_y** (offset 10): y-coordinate of the election authority public key. Both coordinates are public to prevent sign-ambiguity attacks (using −ea_pk would corrupt the tally).

- Private (VAN ownership — conditions 1–4, 5)
   * **vpk_g_d**: voting public key — diversified base point (full affine point from DiversifyHash(d)). Witnessed as `NonIdentityPoint`; x-coordinate extracted for Poseidon hashing (conditions 2, 7). This is the `vpk_d` component of the voting hotkey address. Matches ZKP 1 (delegation) VAN structure.
   * **vpk_pk_d**: voting public key — diversified transmission key (full affine point, pk_d = [ivk_v] * g_d). Witnessed as `NonIdentityPoint`; x-coordinate extracted for Poseidon hashing (conditions 2, 7). Condition 3 (Diversified Address Integrity) constrains this to equal `[ivk_v] * vpk_g_d`. Matches ZKP 1 VAN structure.
   * **total_note_value**: the voter's total delegated weight.
   * **proposal_authority_old**: remaining proposal authority bitmask in the old VAN.
   * **van_comm_rand**: blinding randomness for the VAN commitment.
   * **vote_authority_note_old**: the old VAN commitment (two-layer Poseidon hash, same structure as ZKP 1 van_comm).
   * **vote_comm_tree_path**: Poseidon-based Merkle authentication path (24 sibling hashes).
   * **vote_comm_tree_position**: leaf position in the vote commitment tree.
   * **vsk**: voting spending key (scalar for ECC multiplication). Used in condition 3 for `[vsk] * SpendAuthG`.
   * **rivk_v**: CommitIvk randomness (scalar). Blinding factor for `CommitIvk(ak, nk, rivk_v)` in condition 3.
   * **vsk_nk**: nullifier deriving key. Concretely `fvk.nk().inner()` — the standard Orchard `NullifierDerivingKey` derived from the spending key via `PRF_expand_nk(sk)`. The "vsk" prefix reflects its role in the voting key hierarchy (shared between condition 3's CommitIvk and condition 5's nullifier), not distinct key material. It is structurally identical to the `nk` used in ZKP 1's governance nullifier; cross-circuit uniqueness is ensured by the differing domain tags (see Condition 5).

- Private (vote commitment — conditions 8–12)
   * **shares_1..5**: the voting share vector (each in `[0, 2^24)`).
   * **enc_share_c1_x[0..4]**: x-coordinates of C1_i = r_i * G (El Gamal first component, via ExtractP).
   * **enc_share_c2_x[0..4]**: x-coordinates of C2_i = shares_i * G + r_i * ea_pk (El Gamal second component, via ExtractP).
   * **share_randomness[0..4]**: El Gamal encryption randomness per share (base field elements, converted to scalars via `ScalarVar::from_base` in-circuit).
   * **ea_pk**: election authority public key as a Pallas affine point (witnessed as `NonIdentityPoint`, constrained to public inputs at offsets 7–8).
   * **vote_decision**: the voter's choice.

- Internal wires (not public inputs, not free witnesses)
   * **voting_round_id cell**: copied from the instance column, used in condition 2 Poseidon hash and condition 5 inner hash.
   * **domain_van_nullifier cell**: constant encoding of `"vote authority spend"` (condition 5).
   * **proposal_authority_new**: derived as `proposal_authority_old - (1 << proposal_id)` (condition 6).
   * **shares_hash**: Poseidon hash of 10 enc_share x-coordinates (condition 10). Internal wire consumed by condition 12.
   * **SpendAuthG x, y constants**: coordinates of the El Gamal generator (condition 11). Baked into the verification key via `assign_advice_from_constant`.
   * **ea_pk_x, ea_pk_y cells**: copied from the instance column (condition 11). Each ea_pk `NonIdentityPoint` witness is constrained to match these cells.
   * **DOMAIN_VC constant**: `1`. Domain separation tag for Vote Commitments (condition 12). Baked into the verification key.
   * **proposal_id cell**: copied from the instance column (condition 12). Used in the vote commitment Poseidon hash.

## Condition 2: VAN Integrity ✅

Purpose: prove that the old VAN commitment is correctly constructed from its components. Uses the **same two-layer hash structure as ZKP 1 (delegation)** so that a VAN created by the delegation circuit can be spent (opened) by the vote proof circuit.

```
van_comm_core = Poseidon(DOMAIN_VAN, vpk_g_d, vpk_pk_d, total_note_value,
                         voting_round_id, proposal_authority_old)
vote_authority_note_old = Poseidon(van_comm_core, van_comm_rand)
```

Where:
- **DOMAIN_VAN**: `0`. Domain separation tag for Vote Authority Notes (vs `DOMAIN_VC = 1` for Vote Commitments). Assigned via `assign_advice_from_constant` so the value is baked into the verification key.
- **vpk_g_d**, **vpk_pk_d**: voting public key address components (diversified base and transmission key x-coordinates). Same encoding as in ZKP 1 condition 7, so a VAN created by delegation has the same commitment structure.
- **total_note_value**: the voter's total delegated weight. Shared with condition 8 (shares sum check).
- **voting_round_id**: the vote round identifier (public input at offset 6). Copied from the instance column via `assign_advice_from_instance`, ensuring the in-circuit value matches the verifier's public input.
- **proposal_authority_old**: remaining proposal authority bitmask. Shared with condition 6 (decrement check).
- **van_comm_rand**: random blinding factor. Prevents observers from brute-forcing the address or weight from the public VAN commitment.
- **vote_authority_note_old**: the witnessed VAN commitment. Constrained to equal the two-layer Poseidon output via `constrain_equal`.

**Function:** Two Poseidon invocations: first `ConstantLength<6>` (core), then `ConstantLength<2>` (core, van_comm_rand). Uses `Pow5Chip` / `P128Pow5T3` with rate 2. Matches delegation circuit condition 7 (van_comm) structure.

**Constraint:** The circuit computes the two-layer hash and enforces strict equality with `vote_authority_note_old`. Since `vote_authority_note_old` will also be used as the Merkle leaf in condition 1, this creates a binding: the VAN membership proof and the VAN integrity check are tied to the same commitment.

**Condition 4: Spend Authority** — enforced in-circuit. The spec requires `r_vpk = vsk.ak + [alpha_v] * G`. The circuit witnesses `alpha_v`, computes `[alpha_v]*SpendAuthG` via fixed-base mul, adds it to `vsk_ak_point` (from condition 3), and constrains the result to the instance column at `R_VPK_X` and `R_VPK_Y` (public input offsets 1 and 2). The vote signature is verified out-of-circuit under `r_vpk` over the transaction sighash.

**Out-of-circuit helper:** `van_integrity::van_integrity_hash(vpk_g_d, vpk_pk_d, total_note_value, voting_round_id, proposal_authority_old, van_comm_rand)` from the shared `circuit::van_integrity` module computes the same two-layer hash outside the circuit for builder and test use. (Note: the shared module's parameter names are `g_d_x`/`pk_d_x`.)

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

## Condition 3: Diversified Address Integrity ✅

Purpose: prove the voter controls the voting hotkey address delegated to in Phase 1–2 (spec: Diversified Address Integrity). Uses the same CommitIvk chain as ZKP 1 (delegation) condition 5, implemented via the shared **`circuit::address_ownership`** gadget (ZKP 1 and ZKP 2 both call `spend_auth_g_mul` and `prove_address_ownership`).

```
vsk_ak      = [vsk] * SpendAuthG               (fixed-base ECC mul)
ak          = ExtractP(vsk_ak)                  (x-coordinate)
ivk_v       = CommitIvk_rivk_v(ak, vsk.nk)     (Sinsemilla commitment)
vpk_pk_d    = [ivk_v] * vpk_g_d                (variable-base ECC mul)
```

Where:
- **vsk**: voting spending key (private witness, `pallas::Scalar`). The secret key that authorizes vote casting.
- **SpendAuthG**: fixed generator point on the Pallas curve, reused from the Zcash Orchard protocol. Used both here (condition 3) and in condition 11 (El Gamal generator).
- **ak**: the spend validating key's x-coordinate, derived in-circuit from `[vsk] * SpendAuthG` then `ExtractP`. Not a separate witness — it's an internal wire.
- **vsk_nk**: nullifier deriving key (private witness, `pallas::Base`). The same cell is shared with condition 5 (VAN nullifier keying). Witnessed before condition 3 in the synthesize flow.
- **rivk_v**: CommitIvk randomness (private witness, `pallas::Scalar`). Blinding factor for the Sinsemilla commitment.
- **ivk_v**: the incoming viewing key, derived in-circuit via `CommitIvk(ak, nk, rivk_v)`. Internal wire — flows from CommitIvk output to variable-base ECC mul input via `ScalarVar::from_base`.
- **vpk_g_d**: diversified base point (private witness, full affine point). Witnessed as `NonIdentityPoint`. The x-coordinate is extracted for Poseidon hashing in conditions 2 and 7.
- **vpk_pk_d**: diversified transmission key (private witness, full affine point). Witnessed as `NonIdentityPoint`. Constrained to equal the derived `[ivk_v] * vpk_g_d` via `Point::constrain_equal`.

**Structure:** Uses the shared `circuit::address_ownership` gadget:
1. `spend_auth_g_mul(ecc_chip, layouter, "cond3: [vsk] SpendAuthG", vsk_scalar)` → `vsk_ak_point` (fixed-base scalar mul)
2. `vsk_ak_point.extract_p()` → `ak` (x-coordinate extraction)
3. `prove_address_ownership(..., ak, vsk_nk, rivk_v_scalar, &vpk_g_d_point, &vpk_pk_d_point)` — CommitIvk, `[ivk_v]*vpk_g_d`, and constrain to vpk_pk_d (same flow as ZKP 1 condition 5)

**Chip dependencies:** `SinsemillaChip`, `CommitIvkChip`, `EccChip` (used inside the shared gadget). The Sinsemilla chip also loads the 10-bit lookup table used by conditions 6 and 9.

**Constraint:** The circuit derives vpk_pk_d from vsk → ak → ivk_v → [ivk_v] * vpk_g_d and enforces full point equality with the witnessed vpk_pk_d. Since vpk_pk_d's x-coordinate flows into conditions 2 and 7 (VAN integrity hashes), and vpk_g_d's x-coordinate flows into the same hashes, any mismatch in the key hierarchy would break conditions 2/3/7 simultaneously.

**Security properties:**
- **Key binding:** The CommitIvk chain cryptographically binds vsk to the VAN address (vpk_g_d, vpk_pk_d). A prover who doesn't know vsk cannot produce a valid ivk_v that maps vpk_g_d to vpk_pk_d.
- **Canonicity:** The CommitIvk gadget enforces canonical decomposition of ak and nk, preventing malleability attacks where different bit representations produce the same commitment.
- **Non-identity:** Both vpk_g_d and vpk_pk_d are witnessed as `NonIdentityPoint`, ensuring they are not the curve identity (which would trivially satisfy the constraint for any ivk_v).
- **Shared nk:** Using the same vsk_nk cell for both CommitIvk (condition 3) and the VAN nullifier (condition 5) ensures the nullifier is bound to the same key hierarchy that authorizes the vote.

**Out-of-circuit helper:** `derive_voting_address(vsk, nk, rivk_v)` in tests performs the same computation: `[vsk] * SpendAuthG → ExtractP → CommitIvk → [ivk_v] * g_d`. Uses `CommitDomain::short_commit` from `halo2_gadgets::sinsemilla::primitives`.

**Constructions:** Shared `circuit::address_ownership::spend_auth_g_mul` and `circuit::address_ownership::prove_address_ownership`; `SinsemillaChip`, `CommitIvkChip`, `EccChip`, `ScalarFixed`, `NonIdentityPoint`, `Point::constrain_equal`.

## Condition 4: Spend Authority ✅

Purpose: bind the rerandomized voting public key `r_vpk` to the spending key and a randomizer so the verifier can check the vote signature out-of-circuit under `r_vpk`.

```
vsk_ak_point   = [vsk] * SpendAuthG        (from condition 3)
alpha_v_commit = [alpha_v] * SpendAuthG    (fixed-base ECC mul)
r_vpk_derived  = alpha_v_commit + vsk_ak_point
constrain_instance(r_vpk_derived, R_VPK_X), constrain_instance(r_vpk_derived.y(), R_VPK_Y)
```

Where:
- **vsk_ak_point**: same point as in condition 3 (`[vsk]*SpendAuthG`), reused via the existing fixed-base mul.
- **alpha_v**: spend auth randomizer (private witness, `pallas::Scalar`).
- **r_vpk_derived**: in-circuit result constrained to the instance column at offsets 1 (x) and 2 (y).

**Constraint:** The circuit computes `r_vpk = vsk.ak + [alpha_v]*G` and constrains it to the public inputs `r_vpk_x`, `r_vpk_y`. The vote signature is verified out-of-circuit under `r_vpk` over the transaction sighash.

**Constructions:** `spend_auth_g_mul` (same as condition 3), `Point::add`, `constrain_instance`.

## Condition 5: VAN Nullifier Integrity ✅

Purpose: derive a nullifier that prevents double-voting without revealing the VAN.

```
van_nullifier = Poseidon(vsk_nk, domain_van_nullifier, voting_round_id, vote_authority_note_old)
```

Single `ConstantLength<4>` call matching ZKP 1 condition 14's governance nullifier pattern (`gov_null = Poseidon(nk, domain_tag, vote_round_id, real_nf)`):

- **`vsk_nk`**: nullifier deriving key (private witness, base field element). Concretely `fvk.nk().inner()` — structurally the same value as the `nk` used in ZKP 1. The same cell is shared with condition 3 (CommitIvk), binding the nullifier to the authenticated key hierarchy.
- **`domain_van_nullifier`**: `"vote authority spend"` (20 bytes) zero-padded to 32 and interpreted as a little-endian Pallas field element. Assigned via `assign_advice_from_constant` so the value is **baked into the verification key** — a prover cannot substitute a different value. This tag is the sole cross-circuit separator between this nullifier and ZKP 1's governance nullifier, which uses `"governance authorization"` under the same key. The two tags produce distinct field elements, so a collision would require breaking Poseidon.
- **`voting_round_id`**: cell-equality-linked to condition 2's instance copy, scoping the nullifier to this round.
- **`vote_authority_note_old`**: cell-equality-linked to condition 2's derived VAN hash, binding conditions 2 and 5 together.

**Structure:** Single `ConstantLength<4>` Poseidon hash (2 permutations at rate 2, ~130 rows). This is the same flat 4-input structure used by ZKP 1 condition 14 (`gov_null_hash` in `delegation/imt.rs`).

**Constraint:** The circuit computes the nested hash and enforces `constrain_instance(result, VAN_NULLIFIER)` — binding the derived value to the public input at offset 0. This is the first `constrain_instance` call in the circuit.

**Out-of-circuit helper:** `van_nullifier_hash()` computes the same nested Poseidon hash outside the circuit. `domain_van_nullifier()` returns the domain separator constant.

**Constructions:** `PoseidonChip` (reused from condition 2), `constrain_instance`.

## Condition 6: Proposal Authority Decrement ✅

Purpose: ensure the voter has authority for the voted proposal and correctly clears that bit in the authority bitmask (spec-aligned).

**Spec (Gov Steps V1 §3.5 Step 2, ZKP #2 Condition 6):** `proposal_authority` is a 16-bit bitmask; one vote consumes the bit for the chosen proposal: `proposal_authority_new = proposal_authority_old - (1 << proposal_id)`, and the `proposal_id`-th bit of `proposal_authority_old` must be 1.

**Implementation (bit decomposition):**

1. **Decompose** `proposal_authority_old` into 16 bits `b_i` (each boolean), with recomposition `sum(b_i * 2^i) = proposal_authority_old`.
2. **Selector** `sel_i = 1` iff `proposal_id == i` (exactly one active); constrain `run_selected = sum(sel_i * b_i) = 1` so the selected bit is set (voter has authority).
3. **Clear and recompose**: `b_new_i = b_i*(1-sel_i)`; then `sum(b_new_i * 2^i) = proposal_authority_new`. Constrain this to equal the witnessed `proposal_authority_new` (and thus the new VAN in condition 7).

No diff/gap or strict range-check chip; the 16-bit decomposition implies `proposal_authority_old` and `proposal_authority_new` are in `[0, 2^16)`. The existing `(proposal_id, one_shifted)` lookup constrains `proposal_id in [0, 15]` and `one_shifted = 2^proposal_id`; a separate non-zero gate (`q_cond_6 * (1 - proposal_id * proposal_id_inv) = 0`) additionally rejects `proposal_id = 0`, making the effective circuit range `[1, 15]`. Bit 0 is permanently reserved as the sentinel/unset value. A voting round therefore supports at most 15 proposals. The builder provides `one_shifted` and `proposal_authority_new = old - one_shifted`.

**Structure:** One region: row 0 has `proposal_id`, `one_shifted` (lookup); rows 1..17 have bits, selectors, running sums; gates for init (row 1), recurrence (rows 2..17), and `run_selected = 1` at the last bit row. Equality constraints bind recomposed `run_old` to `proposal_authority_old` and `run_new` to `proposal_authority_new`.

**Constructions:** Lookup table (`table_proposal_id`, `table_one_shifted`), `AddChip`, `LookupRangeCheckConfig` (10-bit words; 16-bit range enforced via limb checks and `(2^16 - 1) - value` gap check).

## Condition 7: New VAN Integrity ✅

Purpose: the new VAN has the same structure as the old (ZKP 1–compatible two-layer hash) except with decremented authority.

Same two-layer formula as condition 2: `van_comm_core = Poseidon(DOMAIN_VAN, vpk_g_d, vpk_pk_d, total_note_value, voting_round_id, proposal_authority_new)` then `vote_authority_note_new = Poseidon(van_comm_core, van_comm_rand)`.

Where:
- **vpk_g_d**, **vpk_pk_d**, **total_note_value**, **voting_round_id**, **van_comm_rand** are cell-equality-linked to the same witness cells used in condition 2.
- **proposal_authority_new**: flows from condition 6's output. This is the only difference between the condition 2 and condition 7 hashes.

**Constraint:** The circuit computes the two-layer hash and enforces `constrain_instance(derived_van_new, VOTE_AUTHORITY_NOTE_NEW)` — binding the result to the public input at offset 3.

**Out-of-circuit helper:** Reuses `van_integrity::van_integrity_hash(vpk_g_d, vpk_pk_d, total_note_value, voting_round_id, proposal_authority_new, van_comm_rand)` with `proposal_authority_new = proposal_authority_old - (1 << proposal_id)`. (Note: the shared module's parameter names are `g_d_x`/`pk_d_x`.)

**Constructions:** `van_integrity::van_integrity_poseidon` (shared gadget from `circuit::van_integrity`).

## Condition 8: Shares Sum Correctness ✅

Purpose: voting shares decomposition is consistent with the total delegated weight.

```
sum(share_0, share_1, share_2, share_3, share_4) = total_note_value
```

Where:
- **share_0..share_4**: the 5 plaintext voting shares (private witnesses). Each share represents a portion of the voter's delegated weight allocated to one of the 5 vote options. These cells will also be used by condition 9 (range check) and condition 11 (El Gamal encryption inputs).
- **total_note_value**: the voter's total delegated weight. Cell-equality-linked to the same witness cell used in condition 2 (VAN integrity), binding the shares decomposition to the authenticated VAN.

**Structure:** Four chained `AddChip` additions (4 rows):
- `partial_1 = share_0 + share_1`
- `partial_2 = partial_1 + share_2`
- `partial_3 = partial_2 + share_3`
- `shares_sum = partial_3 + share_4`

**Constraint:** `constrain_equal(shares_sum, total_note_value)` — the sum of all 5 shares must exactly equal the voter's total delegated weight. This prevents the voter from creating or destroying voting power during the share decomposition.

**Constructions:** `AddChip` (reused from condition 6).

## Condition 9: Shares Range ✅

Purpose: prevent overflow by ensuring each share fits in a bounded range.

```
Each share_i in [0, 2^30)
```

Where:
- **share_0..share_4**: the 5 plaintext voting shares from condition 8. Each share must fit in a bounded range to prevent overflow when shares are used in El Gamal encryption (condition 11) and accumulated homomorphically during tally.

**Bound:** The protocol spec targets `[0, 2^24)`, but halo2_gadgets v0.3's `short_range_check` is `pub(crate)` (private to the gadget crate), so the exact 24-bit decomposition (2 × 10-bit + 4-bit short check) is unavailable. We use the next 10-bit-aligned bound: `[0, 2^30)` via 3 × 10-bit words with strict mode. 30 bits (~1B per share) is secure: max sum of 5 shares ≈ 5B, well within the Pallas field, and the homomorphic tally accumulates over far fewer voters than 2^30.

**Structure:** For each share, one `copy_check` call (5 calls total, ~20 rows):
- `copy_check(share_i, 3, true)` — decomposes the share into 3 × 10-bit lookup words. Each word is range-checked via the 10-bit lookup table. The `true` (strict) flag constrains the final running sum `z_3` to equal 0, enforcing `share < 2^30`.

If a share exceeds `2^30` or is a wrapped large field element (e.g. `p - k` from underflow), the 3-word decomposition produces a non-zero `z_3`, which fails the strict check.

**Note:** Share cells are cloned for `copy_check` (which takes ownership). The original cells remain available for condition 11 (El Gamal encryption inputs).

**Constructions:** `LookupRangeCheckConfig` (reused from condition 6).

## Condition 10: Shares Hash Integrity ✅

Purpose: commit to the 5 El Gamal ciphertext pairs so they can be verified in conditions 11 and 12 without re-witnessing.

```
shares_hash = Poseidon(c1_0_x, c2_0_x, c1_1_x, c2_1_x, c1_2_x, c2_2_x, c1_3_x, c2_3_x, c1_4_x, c2_4_x)
```

Where:
- **c1_i_x**: x-coordinate of `C1_i = r_i * G` (the El Gamal randomness point for share `i`), extracted via `ExtractP`. Private witness field `enc_share_c1_x[i]`.
- **c2_i_x**: x-coordinate of `C2_i = shares_i * G + r_i * ea_pk` (the El Gamal ciphertext point for share `i`), extracted via `ExtractP`. Private witness field `enc_share_c2_x[i]`.

The 10 x-coordinates are interleaved per share — `(c1_0, c2_0, c1_1, c2_1, ...)` — for locality. This matches the order used in ZKP 3 (vote reveal proof), where the server recomputes `shares_hash` from the 5 ciphertexts in the witness.

**Function:** `Poseidon` with `ConstantLength<10>`. Uses `Pow5Chip` / `P128Pow5T3` with rate 2 (5 absorption rounds for 10 inputs, ~6 permutations, ~400 rows).

**Constraint:** The circuit computes the Poseidon hash over all 8 witness values. The resulting `shares_hash` cell is an internal wire — it is not directly bound to any public input. Instead, condition 12 consumes it as an input to `H(DOMAIN_VC, shares_hash, proposal_id, vote_decision)`, which IS bound to `VOTE_COMMITMENT`.

**Relationship to other conditions:**
- Condition 11 constrains that the witnessed `(c1_i_x, c2_i_x)` values are valid El Gamal encryptions of the corresponding plaintext shares from conditions 8/9. The enc_share cells are cloned before the Poseidon hash and reused as `constrain_equal` targets in condition 11.
- Condition 12 chains `shares_hash` into the full vote commitment via `H(DOMAIN_VC, shares_hash, proposal_id, vote_decision)`, which is bound to `VOTE_COMMITMENT` at offset 2.

**Out-of-circuit helper:** `shares_hash()` computes the same Poseidon hash outside the circuit for builder and test use.

**Constructions:** `PoseidonChip` (reused from conditions 1, 2, 5, 7).

## Condition 11: Encryption Integrity ✅

Purpose: each ciphertext is a valid El Gamal encryption of the corresponding plaintext share under the election authority's public key. Implemented by the shared **`circuit::elgamal::prove_elgamal_encryptions`** gadget.

```
For each share i (0..4):
    C1_i = [r_i] * G                        (randomness point)
    C2_i = [v_i] * G + [r_i] * ea_pk        (ciphertext point)
    ExtractP(C1_i) == enc_share_c1_x[i]      (link to condition 10)
    ExtractP(C2_i) == enc_share_c2_x[i]      (link to condition 10)
```

Where:
- **G**: SpendAuthG, the El Gamal generator. Both x and y coordinates are assigned via `assign_advice_from_constant`, baking them into the verification key. Each NonIdentityPoint witness of G is constrained to match these constants, preventing a malicious prover from using a different (or negated) generator.
- **r_i**: El Gamal randomness for share `i` (private witness, `pallas::Base`). Converted to `ScalarVar` via `ScalarVar::from_base` for variable-base ECC multiplication. The same cell is cloned and used for both `[r_i] * G` (C1) and `[r_i] * ea_pk` (C2), ensuring the same randomness binds both ciphertext components.
- **v_i**: plaintext share value from conditions 8/9. Cell-equality-linked to the same cells used in `AddChip` (condition 8) and range check (condition 9). Converted to `ScalarVar` via `ScalarVar::from_base` for ECC multiplication.
- **ea_pk**: election authority public key (Pallas curve point, public input at offsets 7–8). Witnessed as a `NonIdentityPoint` (on-curve constraint included). Both x and y coordinates are constrained to match the instance column cells, preventing a prover from using a different or negated key.
- **enc_share_c1_x[i]**, **enc_share_c2_x[i]**: the x-coordinate cells from condition 10's witness region. These are the same cells that were hashed into `shares_hash` by condition 10's Poseidon hash. Condition 11 constrains the ECC computation output to match them via `constrain_equal`, creating a binding between the Poseidon hash (condition 10) and the actual El Gamal encryption.

**Structure:** For each of the 5 shares (iterated in a loop):
1. Witness G as `NonIdentityPoint`, constrain x/y to SpendAuthG constants
2. `ScalarVar::from_base(r_i)` → variable-base mul → C1 point
3. `constrain_equal(ExtractP(C1), enc_c1_x[i])`
4. Witness G again as `NonIdentityPoint` (consumed by mul), constrain x/y
5. `ScalarVar::from_base(share[i])` → variable-base mul → vG point
6. Witness ea_pk as `NonIdentityPoint`, constrain x/y to public inputs
7. `ScalarVar::from_base(r_i clone)` → variable-base mul → rP point
8. `vG.add(rP)` → C2 point
9. `constrain_equal(ExtractP(C2), enc_c2_x[i])`

Total: 15 variable-base scalar multiplications (~7,500 rows), 5 point additions, 15 `NonIdentityPoint` witnesses (10× G + 5× ea_pk), 10 coordinate `constrain_equal` constraints. This is why K was bumped from 12 to 14.

**Scalar field handling:** All scalars (r_i, v_i) are base field elements converted via `ScalarVar::from_base`. This avoids cross-field consistency issues between `pallas::Base` and `pallas::Scalar`. For shares (< 2^30, guaranteed by condition 9), the integer representation is identical in both fields. For randomness, the probability of a base element exceeding the scalar field modulus is ≈ 2^{-254}.

**Security properties:**
- **Generator binding:** Each G witness is constrained to SpendAuthG's constant coordinates (both x and y), preventing the prover from using a negated generator. Using −G for v*G would produce C2 = −v*G + r*ea_pk, which decrypts to −v instead of v, corrupting the tally.
- **ea_pk binding:** Both ea_pk coordinates are public inputs, so the verifier checks them against the published round parameter. This prevents the prover from encrypting under a different key.
- **Randomness binding:** The same r_i cell (via clone) is used for both C1 and C2 computations. Cell equality ensures both `ScalarVar::from_base` calls decompose the same value.

**Out-of-circuit helpers:** In `circuit::elgamal`: `elgamal_encrypt()` computes the same El Gamal encryption outside the circuit; `spend_auth_g_affine()` returns the SpendAuthG generator; `base_to_scalar()` converts base field elements to scalars.

**Constructions:** Shared `circuit::elgamal::prove_elgamal_encryptions`; `EccChip`, `NonIdentityPoint`, `ScalarVar`, `Point::add`, `Point::extract_p`.

## Condition 12: Vote Commitment Integrity ✅

Purpose: the public vote commitment is correctly constructed from the shares hash, the proposal choice, and the vote decision. This is the final hash that is posted on-chain, inserted into the vote commitment tree, and later opened by ZKP #3 (vote reveal).

```
vote_commitment = Poseidon(DOMAIN_VC, shares_hash, proposal_id, vote_decision)
```

Where:
- **DOMAIN_VC**: `1`. Domain separation tag for Vote Commitments (vs `DOMAIN_VAN = 0`). Assigned via `assign_advice_from_constant` so the value is baked into the verification key. Prevents a vote commitment from ever colliding with a VAN in the shared vote commitment tree.
- **shares_hash**: the Poseidon hash of all 10 enc_share x-coordinates, computed in condition 10. This is a purely internal wire (not a public input) — it flows from condition 10's output cell directly into condition 12's Poseidon input, ensuring the vote commitment is bound to the actual El Gamal ciphertexts without re-hashing.
- **proposal_id**: which proposal this vote is for (public input at offset 5). Copied from the instance column via `assign_advice_from_instance`. The verifier checks it matches a valid proposal in the voting window.
- **vote_decision**: the voter's choice (private witness). Hidden inside the vote commitment — only revealed in ZKP #3 when individual shares are opened. The decision value is opaque to the circuit; its semantic meaning is defined by the application layer.

**Function:** `Poseidon` with `ConstantLength<4>`. Uses `Pow5Chip` / `P128Pow5T3` with rate 2 (2 absorption rounds for 4 inputs, ~3 permutations, ~200 rows).

**Constraint:** The circuit computes the Poseidon hash and enforces `constrain_instance(vote_commitment, VOTE_COMMITMENT)` — binding the derived value to the public input at offset 2. This is the terminal constraint of the vote commitment construction chain: conditions 8–9 validate the plaintext shares, condition 10 hashes the ciphertexts, condition 11 proves the ciphertexts are valid El Gamal encryptions, and condition 12 wraps everything into a single public commitment.

**Data flow (conditions 8–12):**
```
shares (8: sum, 9: range) ──┐
                             ├─ enc_shares (11: El Gamal) ──→ shares_hash (10: Poseidon<10>)
randomness ──────────────────┘                                       │
                                                                     ├─ vote_commitment (12: Poseidon<4>) ──→ VOTE_COMMITMENT
proposal_id ─────────────────────────────────────────────────────────┤
vote_decision ───────────────────────────────────────────────────────┘
```

**Out-of-circuit helper:** `vote_commitment_hash()` computes the same Poseidon hash outside the circuit for builder and test use.

**Constructions:** `PoseidonChip` (reused from conditions 1, 2, 5, 7, 10).

## Column Layout

| Columns | Use |
|---------|-----|
| `advices[0..5]` | General witness assignment, ECC (cond 3, 4, 11), Sinsemilla/CommitIvk (cond 3) |
| `advices[5]` | Poseidon partial S-box |
| `advices[6]` | Poseidon state + AddChip output (c) |
| `advices[7]` | Poseidon state + AddChip input (a) |
| `advices[8]` | Poseidon state + AddChip input (b) |
| `advices[9]` | Range check running sum |
| `lagrange_coeffs[0]` | Constants (DOMAIN_VAN, DOMAIN_VC, ONE, SpendAuthG x/y) |
| `lagrange_coeffs[1]` | ECC Lagrange coefficients |
| `lagrange_coeffs[2..5]` | Poseidon rc_a |
| `lagrange_coeffs[5..8]` | Poseidon rc_b |
| `table_idx` (+ additional lookup columns) | 10-bit lookup table [0, 1024), Sinsemilla lookup (loaded by `SinsemillaChip`); (proposal_id, 2^proposal_id) table for condition 6 |
| `primary` | 11 public inputs |

## Chip Summary

| Chip | Conditions | Role |
|------|-----------|------|
| `PoseidonChip` (Pow5) | 1, 2, 5, 7, 10, 12 | Poseidon hashing (Merkle paths, VAN integrity, nullifiers, shares hash, vote commitment) |
| `EccChip` | 3, 4, 11 | Fixed-base and variable-base scalar multiplication, point addition, ExtractP (cond 4: [alpha_v]*G + vsk_ak) |
| `SinsemillaChip` | 3 | Sinsemilla hash inside CommitIvk |
| `CommitIvkChip` | 3 | Canonicity gate for ak/nk decomposition in CommitIvk |
| `AddChip` | 6, 8 | Field element addition (authority decrement, shares sum) |
| `LookupRangeCheckConfig` | 6, 9 | 10-bit running-sum range checks |
