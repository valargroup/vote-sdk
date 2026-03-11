---
name: Gov Commitment and MinWeight
overview: Add conditions 7 (Gov Commitment Integrity) and 8 (Minimum Voting Weight) to the delegation circuit. Condition 7 verifies van_comm = Poseidon(g_d_new_x, pk_d_new_x, v_total, vote_round_id, van_comm_rand). Condition 8 enforces v_total >= 12,500,000 zatoshi via a 64-bit range check on the difference.
todos: []
isProject: false
---

# Gov Commitment Integrity and Minimum Voting Weight

## Recommendation on `vpk` (question #3)

`vpk` is a **full diversified address** -- the tuple `(g_d_new, pk_d_new)` -- which are the same ECC points already witnessed for the output note in condition 6. For the Poseidon hash, represent vpk as **two Pallas base field elements** by extracting x-coordinates: `g_d_new_x = ExtractP(g_d_new)` and `pk_d_new_x = ExtractP(pk_d_new)`.

This means the van_comm Poseidon becomes a **5-input hash** (`ConstantLength<5>`):

```
van_comm = Poseidon(g_d_new_x, pk_d_new_x, v_total, vote_round_id, van_comm_rand)
```

Rationale for 5 inputs vs nested hashing:
- Reuses cells already in the circuit from condition 6 (zero additional ECC ops)
- One Poseidon call instead of two (cheaper than `Poseidon(Poseidon(g_d_x, pk_d_x), v_total, ...))`)
- Both address components are explicitly bound (no reliance on subtle assumptions about point uniqueness from a single coordinate)
- The design doc's 4-input formula `Poseidon(voting_hotkey_pk, ...)` expands naturally since `voting_hotkey_pk` is a tuple

## Architecture: Per-note circuit with free-witness `v_i`

Conditions 7 and 8 stay **inside the existing per-note delegation circuit**, not in a separate aggregation circuit. This is consistent with the design doc's single-ZKP structure (conditions 1-15 all in one circuit).

The 4 note values `v_1..v_4` are added as **free private witnesses** (field elements). Today they have no in-circuit binding to actual note commitments -- that binding arrives with condition 9 (Old Note Commitment Integrity). This is safe because:

- Condition 7 binds `v_total` into `van_comm`, which is a public input
- `van_comm` enters the delegation commitment tree and is opened in ZKP #2 (vote proof), so inflating `v_total` would fail at vote time
- Condition 8 prevents dust (v_total < 0.125 ZEC) regardless of whether v_i are bound to real notes yet

## Files to change

### 1. [src/delegation/circuit.rs](src/delegation/circuit.rs) -- Main changes

**Config struct** -- add `range_check` field:

```rust
range_check: LookupRangeCheckConfig<pallas::Base, { sinsemilla::K }>,
```

The `range_check` is already created at line 333 but not stored. Store it and add a helper:

```rust
fn range_check_config(&self) -> LookupRangeCheckConfig<pallas::Base, { sinsemilla::K }> {
    self.range_check
}
```

**Circuit struct** -- add 5 new private witness fields:

```rust
v_1: Value<pallas::Base>,
v_2: Value<pallas::Base>,
v_3: Value<pallas::Base>,
v_4: Value<pallas::Base>,
van_comm_rand: Value<pallas::Base>,
```

**New builder method** `with_van_commitment_data`:

```rust
pub fn with_van_commitment_data(
    mut self,
    v_1: u64, v_2: u64, v_3: u64, v_4: u64,
    van_comm_rand: pallas::Base,
) -> Self {
    self.v_1 = Value::known(pallas::Base::from(v_1));
    self.v_2 = Value::known(pallas::Base::from(v_2));
    self.v_3 = Value::known(pallas::Base::from(v_3));
    self.v_4 = Value::known(pallas::Base::from(v_4));
    self.van_comm_rand = Value::known(van_comm_rand);
    self
}
```

**Synthesize changes:**

Step A: Restructure condition 3 block (rho binding, lines 619-661) to return `van_comm_cell` and `vote_round_id_cell`:

```rust
let (van_comm_cell, vote_round_id_cell) = {
    // ... existing rho binding logic ...
    (van_comm, vote_round_id)
};
```

Step B: Restructure condition 6 block (output note commit, lines 663-734) to return `g_d_new_x` and `pk_d_new_x`:

```rust
let (g_d_new_x, pk_d_new_x) = {
    let g_d_new = NonIdentityPoint::new(...)?;
    let pk_d_new = NonIdentityPoint::new(...)?;
    // ... existing NoteCommit + cmx constraint ...
    (g_d_new.extract_p().inner().clone(), pk_d_new.extract_p().inner().clone())
};
```

Step C: Add condition 7 block after condition 6:

```rust
// Condition 7: Gov Commitment Integrity
// van_comm = Poseidon(g_d_new_x, pk_d_new_x, v_total, vote_round_id, van_comm_rand)
let v_total = {
    let v_1 = assign_free_advice(layouter, config.advices[0], self.v_1)?;
    let v_2 = assign_free_advice(layouter, config.advices[0], self.v_2)?;
    let v_3 = assign_free_advice(layouter, config.advices[0], self.v_3)?;
    let v_4 = assign_free_advice(layouter, config.advices[0], self.v_4)?;
    let van_comm_rand = assign_free_advice(layouter, config.advices[0], self.van_comm_rand)?;

    // v_total = v_1 + v_2 + v_3 + v_4  (three AddChip additions)
    let add_chip = config.add_chip();
    let sum_12 = add_chip.add(layouter, &v_1, &v_2)?;
    let sum_123 = add_chip.add(layouter, &sum_12, &v_3)?;
    let v_total = add_chip.add(layouter, &sum_123, &v_4)?;

    // Poseidon(g_d_new_x, pk_d_new_x, v_total, vote_round_id, van_comm_rand)
    let derived_van_comm = {
        let msg = [g_d_new_x, pk_d_new_x, v_total.clone(), vote_round_id_cell, van_comm_rand];
        let hasher = PoseidonHash::<
            pallas::Base, _, poseidon::P128Pow5T3, ConstantLength<5>, 3, 2,
        >::init(config.poseidon_chip(), layouter)?;
        hasher.hash(layouter, msg)?
    };

    // Constrain: derived_van_comm == van_comm (from condition 3)
    layouter.assign_region(|| "van_comm integrity", |mut region| {
        region.constrain_equal(derived_van_comm.cell(), van_comm_cell.cell())
    })?;

    v_total
};
```

Step D: Add condition 8 block after condition 7:

```rust
// Condition 8: Minimum Voting Weight
// v_total >= 12,500,000 zatoshi (0.125 ZEC)
{
    const MIN_WEIGHT: u64 = 12_500_000;

    // Witness diff = v_total - MIN_WEIGHT
    let diff = v_total.value().map(|v| v - pallas::Base::from(MIN_WEIGHT));
    let diff = assign_free_advice(layouter, config.advices[0], diff)?;

    // Constrain: diff + MIN_WEIGHT = v_total
    // Assign MIN_WEIGHT as constant, then use AddChip: diff + min = v_total
    let min_weight = assign_free_advice(
        layouter, config.advices[0],
        Value::known(pallas::Base::from(MIN_WEIGHT)),
    )?;
    let recomputed = config.add_chip().add(layouter, &diff, &min_weight)?;
    layouter.assign_region(|| "v_total = diff + min_weight", |mut region| {
        region.constrain_equal(recomputed.cell(), v_total.cell())
    })?;

    // Range-check diff to [0, 2^70) -- ensures diff is non-negative
    // (if v_total < MIN_WEIGHT, diff wraps to ~2^254, failing the check)
    // 7 words * 10 bits/word = 70 bits >= 64 bits (sufficient for u64 sums)
    config.range_check_config().copy_check(
        layouter.namespace(|| "diff < 2^70"),
        diff,
        7,    // num_words
        true, // strict (running sum terminates at 0)
    )?;
}
```

### 2. [src/spec.rs](src/spec.rs) -- Out-of-circuit helper

Add a `van_commitment_hash` function next to the existing `rho_binding_hash` (line 245):

```rust
pub(crate) fn van_commitment_hash(
    g_d_new_x: pallas::Base,
    pk_d_new_x: pallas::Base,
    v_total: pallas::Base,
    vote_round_id: pallas::Base,
    van_comm_rand: pallas::Base,
) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, poseidon::ConstantLength<5>, 3, 2>::init()
        .hash([g_d_new_x, pk_d_new_x, v_total, vote_round_id, van_comm_rand])
}
```

### 3. [src/delegation/README.md](src/delegation/README.md) -- Documentation

Add sections for conditions 7 and 8 describing