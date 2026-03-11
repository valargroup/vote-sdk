# Gadgets

## Address ownership gadget

Shared circuit gadget that encapsulates **address ownership** and **SpendAuthG fixed-base multiplication** used by both ZKP #1 (delegation) and ZKP #2 (vote proof). Same pattern as the shared VAN integrity gadget (`van_integrity.rs`): chip-agnostic, takes config/chip refs and assigned cells.

### Why it exists

Both delegation and vote proof circuits enforce:

1. **SpendAuthG fixed-base mul** — `[scalar] * SpendAuthG` (delegation: `alpha` then add `ak_P` for `rk`; vote proof: `vsk` then `ExtractP` for `ak`).
2. **Address ownership** — `ivk = CommitIvk(ak, nk, rivk)`, then `pk_d = [ivk] * g_d`, constrained to the claimed address.

This module provides a single place for that constraint logic so delegation and vote proof don’t duplicate the same CommitIvk → scalar mul → constrain wiring.

### API

### `spend_auth_g_mul(ecc_chip, layouter, label, scalar) -> Result<Point, Error>`

Computes `[scalar] * SpendAuthG` using `OrchardFixedBasesFull::SpendAuthG`.

- **Delegation (condition 4):** `alpha` → `alpha_commitment`; caller adds `ak_P` and constrains to `rk` (public inputs).
- **Vote proof (condition 3):** `vsk` → `vsk_ak_point`; caller does `ak = vsk_ak_point.extract_p().inner().clone()` then passes `ak` into `prove_address_ownership`.

### `prove_address_ownership(..., ak, nk, rivk, g_d, pk_d_claimed) -> Result<AssignedCell<Base, Base>, Error>`

1. Calls `commit_ivk(..., ak, nk, rivk)` to get `ivk`.
2. Converts `ivk` to `ScalarVar`, computes `derived_pk_d = g_d.mul(ivk_scalar)`.
3. Constrains `derived_pk_d == pk_d_claimed`.

Returns the **ivk cell** so callers (e.g. delegation) can reuse it for per-note diversified address checks (e.g. condition 11).

- **Delegation (condition 5):** `ak` from `ExtractP(ak_P)`, `nk` from keystone note, `g_d_signed`, `pk_d_signed`. Returned `ivk_cell` is passed into per-note slot synthesis.
- **Vote proof (condition 3):** `ak` from `ExtractP([vsk]*SpendAuthG)`, `vsk_nk`, `rivk_v`, `vpk_g_d_point`, `vpk_pk_d_point`. Return value is unused.

### Dependencies

- Reuses existing **CommitIvk** from `commit_ivk.rs` (same `CommitIvkChip`, `SinsemillaChip`, `EccChip`). No new chips; this is a thin wrapper: CommitIvk + scalar mul + constrain_equal.

### Usage

- **Delegation:** `orchard/src/delegation/circuit.rs` — condition 4 uses `spend_auth_g_mul`, condition 5 uses `prove_address_ownership`.
- **Vote proof:** `orchard/src/vote_proof/circuit.rs` — condition 3 uses both.

The main Orchard action circuit could later call `spend_auth_g_mul` and `prove_address_ownership` for consistency (optional follow-up).

## VAN integrity gadget

Shared circuit gadget that encapsulates the **VAN (Vote Authority Note) integrity** two-layer Poseidon hash used by ZKP #1 (delegation, condition 7) and ZKP #2 (vote proof, conditions 2 and 6). Same pattern as the address ownership gadget: chip-agnostic, takes config and assigned cells.

### Why it exists

Both delegation and vote proof circuits need a shared commitment shape for the governance commitment / Vote Authority Note:

- **Delegation (condition 7):** when creating the output note, the circuit commits to `van_comm` = two-layer hash(domain_van, g_d, pk_d, value, round, proposal_authority; rand). This becomes the VAN that can later be spent in vote proof.
- **Vote proof (condition 2):** the voter proves the old VAN commitment is correctly formed (same hash structure) so that VANs created by delegation are valid leaves.
- **Vote proof (condition 6):** the new VAN commitment (after decrementing proposal authority) uses the same hash structure.

A single module provides the two-layer Poseidon (core hash then blind with rand) so both circuits use identical constraints and hashes.

### API

**`DOMAIN_VAN`** — `u64` constant `0`. Domain tag for Vote Authority Notes; `DOMAIN_VC = 1` for Vote Commitments. Prepended as the first Poseidon input for domain separation in the shared vote commitment tree.

**`van_integrity_hash(g_d_x, pk_d_x, value, voting_round_id, proposal_authority, van_comm_rand) -> pallas::Base`**

Out-of-circuit two-layer hash. Computes:

- `van_comm_core = Poseidon(DOMAIN_VAN, g_d_x, pk_d_x, value, voting_round_id, proposal_authority)`
- `result = Poseidon(van_comm_core, van_comm_rand)`

Used by builders and tests to compute the expected VAN / van_comm value.

**`van_integrity_poseidon(poseidon_config, layouter, label, domain_van, g_d_x, pk_d_x, value, voting_round_id, proposal_authority, van_comm_rand) -> Result<AssignedCell<Base, Base>, Error>`**

In-circuit two-layer hash with the same structure. Takes assigned cells and a `PoseidonConfig` (P128Pow5T3, width 3, rate 2). Returns the final hash cell. Callers constrain it to their witnessed commitment (delegation: van_comm; vote proof: vote_authority_note_old or vote_authority_note_new).

### Dependencies

- Uses **Poseidon** only: `halo2_gadgets::poseidon::Pow5Chip`, `P128Pow5T3`, `ConstantLength<6>` and `ConstantLength<2>`. No ECC or Sinsemilla; any circuit that already has a compatible Poseidon config can call `van_integrity_poseidon`.

### Usage

- **Delegation:** `orchard/src/delegation/circuit.rs` — condition 7 assigns `domain_van` from `DOMAIN_VAN`, then calls `van_integrity_poseidon` and constrains the result to `van_comm` (public input).
- **Vote proof:** `orchard/src/vote_proof/circuit.rs` — condition 2 (old VAN) and condition 6 (new VAN) call `van_integrity_poseidon` and constrain to `vote_authority_note_old` and `vote_authority_note_new` respectively.

### Constraint flow (conceptual)

```
Delegation (condition 7)     Vote proof (condition 2)    Vote proof (condition 6)
────────────────────────     ────────────────────────    ────────────────────────
domain_van, g_d_new,          domain_van, vpk_g_d,         domain_van, vpk_g_d,
pk_d_new, value=0,            vpk_pk_d, total_value,      vpk_pk_d, total_value,
vote_round_id,                vote_round_id,               vote_round_id,
MAX_PROPOSAL_AUTHORITY        proposal_authority_old      proposal_authority_new
        ↓                              ↓                            ↓
Poseidon(core)                Poseidon(core)                Poseidon(core)
        ↓                              ↓                            ↓
Poseidon(core, rand)          Poseidon(core, rand)          Poseidon(core, rand)
        ↓                              ↓                            ↓
constrain van_comm            constrain vote_authority_    constrain vote_authority_
(public input)                note_old                     note_new (public input)
```

### See also (VAN integrity)

- `address_ownership.rs` — shared SpendAuthG and CommitIvk-based address ownership (conditions 4/5 delegation, condition 3 vote proof).
- Delegation README: condition 7 (gov commitment integrity).
- Vote proof README: conditions 2 and 6.

## Constraint flow (conceptual)

```
ZKP 1 (delegation)          ZKP 2 (vote proof)
────────────────────       ────────────────────
ak witnessed               [vsk]*SpendAuthG → ak
    ↓                              ↓
CommitIvk (shared)         CommitIvk (shared)
    ↓                              ↓
[ivk]*g_d_signed          [ivk_v]*vpk_g_d
    ↓                              ↓
constrain pk_d_signed     constrain vpk_pk_d

[rk =] [alpha]*SpendAuthG + ak_P   (delegation only)
```

### See also

- `address_ownership.rs` — shared SpendAuthG and CommitIvk-based address ownership (conditions 4/5 delegation, condition 3 vote proof).
- `elgamal.rs` — shared El Gamal encryption integrity (vote proof condition 11).
- `van_integrity.rs` — shared VAN integrity Poseidon hash (conditions 2/6/7).
- `commit_ivk.rs` — Sinsemilla-based CommitIvk and canonicity gates.
- Delegation README: conditions 4 and 5.
- Vote proof README: condition 3.

## El Gamal gadget

Shared circuit gadget that proves **El Gamal encryption integrity** for the vote proof (ZKP #2, condition 11). For each of the 5 vote shares, it constrains that the witnessed ciphertext x-coordinates are correct: C1_i = [r_i]*G, C2_i = [v_i]*G + [r_i]*ea_pk, with G = SpendAuthG and ea_pk from public inputs.

### Why it exists

The vote proof must bind the Poseidon hash of enc_share x-coordinates (condition 10) to the actual ECC computation. Extracting this logic into a dedicated gadget keeps the vote proof synthesis simpler and allows layout/optimization work to be done in one place.

### API

**`prove_elgamal_encryptions(ecc_chip, layouter, namespace, ea_pk, ea_pk_loc, advice_col, share_cells, r_cells, enc_c1_cells, enc_c2_cells) -> Result<(), Error>`**

Per share i: constrains C1_i = [r_i]*G and C2_i = [v_i]*G + [r_i]*ea_pk, and `constrain_equal(ExtractP(C1_i), enc_c1_cells[i])` and similarly for C2. The gadget owns all ea_pk scaffolding: it witnesses the point internally and calls `layouter.constrain_instance` using the `EaPkInstanceLoc` descriptor (instance column + x/y row offsets). The caller supplies the four varying arrays and `ea_pk` as a `Value<Affine>`.

**Out-of-circuit (same module):** `spend_auth_g_affine()`, `base_to_scalar(b)`, `elgamal_encrypt(share_value, randomness, ea_pk)` — used by the vote proof builder and tests.

### Dependencies

- Uses **EccChip** only (same config as condition 3). No new columns; no change to K.

### Usage

- **Vote proof:** `orchard/src/vote_proof/circuit.rs` — condition 11 witnesses r_0..r_4, then calls `prove_elgamal_encryptions` with `EaPkInstanceLoc { instance: config.primary, x_row: EA_PK_X, y_row: EA_PK_Y }` and `config.advices[0]`. The gadget handles ea_pk witnessing, instance-pinning, and sign-cell constant assignment internally.
