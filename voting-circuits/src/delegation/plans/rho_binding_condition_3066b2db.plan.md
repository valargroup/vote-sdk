---
name: Rho Binding Condition
overview: "Implement Condition 3 (Rho Binding) in the delegation circuit: constrain rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, van_comm, vote_round_id) using a single ConstantLength<6> Poseidon hash, with new public inputs for van_comm and vote_round_id."
todos:
  - id: add-imports
    content: Add ConstantLength and PoseidonHash imports to delegation/circuit.rs
    status: completed
  - id: add-public-input-offsets
    content: Add VAN_COMM and VOTE_ROUND_ID public input offset constants
    status: completed
  - id: add-witness-fields
    content: Add cmx_1..4, van_comm, vote_round_id to Circuit struct
    status: completed
  - id: add-builder-method
    content: Add with_rho_binding builder method to Circuit
    status: completed
  - id: add-synthesize-block
    content: "Add rho binding constraint block in synthesize(): witness inputs, constrain public, Poseidon hash, equality check"
    status: completed
  - id: update-instance
    content: Add van_comm and vote_round_id to Instance struct and to_halo2_instance()
    status: completed
  - id: add-spec-fn
    content: Add rho_binding_hash spec function to src/spec.rs
    status: completed
  - id: update-existing-tests
    content: Update make_test_note and all existing tests to provide rho binding witnesses and public inputs
    status: completed
  - id: add-new-tests
    content: Add rho_binding_happy_path, wrong_cmx, wrong_van_comm, wrong_vote_round_id tests
    status: completed
isProject: false
---

# Rho Binding (Condition 3)

## Spec Condition

> `rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, van_comm, vote_round_id)`
>
> The signed note's rho is bound to the exact notes being delegated, the governance commitment, and the round. This makes the keystone signature non-replayable and scoped.

Only one file is modified: [src/delegation/circuit.rs](src/delegation/circuit.rs). K=12 remains sufficient (~300 additional rows, well within the ~1700 row headroom).

---

## Key Design Decision: `ConstantLength<6>` Poseidon

The existing codebase only uses `ConstantLength<2>` (e.g., `derive_nullifier` uses `Poseidon(nk, rho)`). For 6 inputs, two approaches exist:

- **Option A: Single `ConstantLength<6>` hash** — The `Pow5Chip` implements `PoseidonSpongeInstructions` generically for any L. With rate=2, it absorbs 2 elements per round (3 absorption rounds for 6 inputs). This directly matches the spec notation.
- **Option B: Chained `ConstantLength<2>` hashes** — Like `derive_domain_nullifier` does. Produces a different hash value than the spec, requires defining a separate out-of-circuit convention.

**Chosen: Option A.** It exactly matches the spec, is simpler code, and the domain separator includes the length for proper cryptographic separation. The same `PoseidonConfig` works because the config describes the physical gate layout (state columns, round constants), not the domain. A fresh chip instance from `config.poseidon_chip()` works with any `ConstantLength<L>`.

---

## What Changes

### 1. Add Imports

Update the `halo2_gadgets::poseidon` import in [src/delegation/circuit.rs](src/delegation/circuit.rs) line 52:

```rust
// Before:
poseidon::{primitives as poseidon, Pow5Chip as PoseidonChip, Pow5Config as PoseidonConfig},

// After:
poseidon::{
    primitives::{self as poseidon, ConstantLength},
    Hash as PoseidonHash,
    Pow5Chip as PoseidonChip,
    Pow5Config as PoseidonConfig,
},
```

### 2. Add Public Input Offsets

Add after the existing `RK_Y` constant (line 62):

```rust
const VAN_COMM: usize = 3;
const VOTE_ROUND_ID: usize = 4;
```

### 3. Add Private Witness Fields to `Circuit`

Add 6 new fields to the `Circuit` struct (line 149). These are private witness inputs — `cmx_1..4` will eventually be derived in-circuit by condition 10, but for now are free witnesses:

```rust
cmx_1: Value<pallas::Base>,
cmx_2: Value<pallas::Base>,
cmx_3: Value<pallas::Base>,
cmx_4: Value<pallas::Base>,
van_comm: Value<pallas::Base>,
vote_round_id: Value<pallas::Base>,
```

`Default` derive still works (`Value<T>` defaults to `Value::unknown()`).

### 4. Add `with_rho_binding` Builder Method

Add to `impl Circuit` (after `from_note_unchecked`, line 182). This sets the rho-binding witness fields. The existing `from_note_unchecked` is unchanged — it still constructs the signed-note fields. The builder method layers on the new fields:

```rust
pub fn with_rho_binding(
    mut self,
    cmx_1: pallas::Base,
    cmx_2: pallas::Base,
    cmx_3: pallas::Base,
    cmx_4: pallas::Base,
    van_comm: pallas::Base,
    vote_round_id: pallas::Base,
) -> Self {
    self.cmx_1 = Value::known(cmx_1);
    self.cmx_2 = Value::known(cmx_2);
    self.cmx_3 = Value::known(cmx_3);
    self.cmx_4 = Value::known(cmx_4);
    self.van_comm = Value::known(van_comm);
    self.vote_round_id = Value::known(vote_round_id);
    self
}
```

### 5. Synthesize: Rho Binding Block

Insert after the note commitment integrity block (after line 511). The `rho_signed` `AssignedCell` is already available from line 342:

```rust
// Rho binding (condition 3).
// rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, van_comm, vote_round_id)
// Binds the signed note to the exact notes being delegated, the governance
// commitment, and the round, making the keystone signature non-replayable.
{
    let cmx_1 = assign_free_advice(
        layouter.namespace(|| "witness cmx_1"), config.advices[0], self.cmx_1)?;
    let cmx_2 = assign_free_advice(
        layouter.namespace(|| "witness cmx_2"), config.advices[0], self.cmx_2)?;
    let cmx_3 = assign_free_advice(
        layouter.namespace(|| "witness cmx_3"), config.advices[0], self.cmx_3)?;
    let cmx_4 = assign_free_advice(
        layouter.namespace(|| "witness cmx_4"), config.advices[0], self.cmx_4)?;
    let van_comm = assign_free_advice(
        layouter.namespace(|| "witness van_comm"), config.advices[0], self.van_comm)?;
    let vote_round_id = assign_free_advice(
        layouter.namespace(|| "witness vote_round_id"), config.advices[0], self.vote_round_id)?;

    // Bind van_comm and vote_round_id to the public inputs.
    layouter.constrain_instance(van_comm.cell(), config.primary, VAN_COMM)?;
    layouter.constrain_instance(vote_round_id.cell(), config.primary, VOTE_ROUND_ID)?;

    // Poseidon hash over 6 inputs using ConstantLength<6>.
    let derived_rho = {
        let poseidon_message = [cmx_1, cmx_2, cmx_3, cmx_4, van_comm, vote_round_id];
        let poseidon_hasher = PoseidonHash::<
            pallas::Base, _, poseidon::P128Pow5T3, ConstantLength<6>, 3, 2,
        >::init(
            config.poseidon_chip(),
            layouter.namespace(|| "rho binding Poseidon init"),
        )?;
        poseidon_hasher.hash(
            layouter.namespace(|| "Poseidon(cmx_1..4, van_comm, vote_round_id)"),
            poseidon_message,
        )?
    };

    // Constrain: derived_rho == rho_signed.
    layouter.assign_region(
        || "rho binding equality",
        |mut region| region.constrain_equal(derived_rho.cell(), rho_signed.cell()),
    )?;
}
```

### 6. Update `Instance` Struct and `to_halo2_instance`

Add two new public fields to `Instance` (line 519):

```rust
pub van_comm: pallas::Base,
pub vote_round_id: pallas::Base,
```

Update `from_parts` to accept them and `to_halo2_instance` to include them at offsets 3 and 4:

```rust
pub fn to_halo2_instance(&self) -> Vec<vesta::Scalar> {
    let rk = /* ... existing rk logic ... */;
    vec![self.nf_signed.0, *rk.x(), *rk.y(), self.van_comm, self.vote_round_id]
}
```

### 7. Out-of-Circuit Spec Function

Add to [src/spec.rs](src/spec.rs) (after `prf_nf`, ~line 237). This provides the matching out-of-circuit computation for testing and the builder layer:

```rust
/// Rho binding hash for the delegation circuit (condition 3).
/// rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, van_comm, vote_round_id)
pub(crate) fn rho_binding_hash(
    cmx_1: pallas::Base, cmx_2: pallas::Base,
    cmx_3: pallas::Base, cmx_4: pallas::Base,
    van_comm: pallas::Base, vote_round_id: pallas::Base,
) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, poseidon::ConstantLength<6>, 3, 2>::init()
        .hash([cmx_1, cmx_2, cmx_3, cmx_4, van_comm, vote_round_id])
}
```

### 8. Update Existing Tests

All existing tests must provide the new public inputs (`van_comm`, `vote_round_id`) in the instance vector, and the circuit must have consistent rho binding witnesses. Two approaches:

- **Option A**: Modify `make_test_note` to construct a note with correct rho binding (compute rho from 4 dummy cmx values).
- **Option B**: Keep `make_test_note` as-is and use it only for a simpler helper; create a new `make_test_note_with_rho_binding` for the full flow.

**Chosen: Option A.** Update `make_test_note` so every existing test still passes. The new helper:

1. Creates 4 dummy notes, extracts `cmx_1..4 = ExtractP(cm_i)`
2. Picks random `van_comm`, `vote_round_id`
3. Computes `rho = rho_binding_hash(cmx_1..4, van_comm, vote_round_id)`
4. Creates the signed note via `Note::dummy(rng, Some(Nullifier(rho)))`
5. Builds the circuit via `Circuit::from_note_unchecked(...).with_rho_binding(cmx_1..4, van_comm, vote_round_id)`
6. Returns the circuit, `Instance` (now including `van_comm`, `vote_round_id`), and the cmx values

### 9. New Tests

`**rho_binding_happy_path**`: Full end-to-end test with correctly constructed rho. MockProver should pass.

`**rho_binding_wrong_cmx**`: Build circuit with correct rho binding, then tamper with `cmx_1` in the circuit witness. The Poseidon hash will differ from `rho_signed`, so the equality constraint fails.

`**rho_binding_wrong_van_comm_public_input**`: Build circuit with correct witness, but supply a different `van_comm` in the public instance. The `constrain_instance` on `van_comm` will fail.

`**rho_binding_wrong_vote_round_id**`: Same pattern — correct witness but wrong `vote_round_id` in the public instance. The `constrain_instance` on `vote_round_id` will fail.

---

## Row Budget

Poseidon `ConstantLength<6>` with rate 2: 3 absorption rounds + 1 squeeze ~~= 4 permutations x ~70 rows = ~280 rows. Plus 6 advice assignments (~~6 rows) and 2 `constrain_instance` + 1 equality constraint (minimal). Total addition: ~300 rows. Previous estimate: ~2080-2380 used out of 4096 at K=12. After: ~2380-2680. Still fits with ~1400 rows of headroom.

## Ordering of Changes

1. Imports (step 1)
2. Constants — public input offsets (step 2)
3. Circuit struct — add witness fields (step 3)
4. Builder method (step 4)
5. Synthesize — rho binding block (step 5)
6. Instance struct + `to_halo2_instance` (step 6)
7. Spec function (step 7)
8. Update existing tests (step 8)
9. Add new tests (step 9)

