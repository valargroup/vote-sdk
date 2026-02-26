---
name: Signed Note Commitment Integrity
overview: "Implement Condition 1 (Old Note Commitment Integrity) in the delegation circuit: compute NoteCommit in-circuit from the signed note's witness data and constrain it to equal the witnessed cm_signed. No null/bottom option — strict equality only."
todos:
  - id: add-imports
    content: "Add imports: NoteCommitChip, NoteCommitConfig, note_commit gadget, NoteCommitTrapdoor, NoteValue"
    status: completed
  - id: add-note-commit-config
    content: Add note_commit_config to Config struct; add note_commit_chip() helper; add NoteCommitChip::configure call in configure()
    status: completed
  - id: update-circuit-struct
    content: "Add rcm_signed: Value<NoteCommitTrapdoor> to Circuit struct; update from_note_unchecked to extract rcm_signed"
    status: completed
  - id: clone-rho-old
    content: Clone rho_old in derive_nullifier call so it remains available for note_commit
    status: completed
  - id: restructure-addr-integrity
    content: Change address integrity block to return pk_d_signed as NonIdentityPoint for reuse
    status: completed
  - id: add-note-commit-block
    content: "Add note commitment integrity block: witness rcm_signed + v_signed=0, call note_commit, constrain_equal to cm_old"
    status: completed
  - id: add-tests
    content: Add note_commit_integrity_happy_path, note_commit_integrity_wrong_rcm, and note_commit_integrity_wrong_cm tests
    status: completed
isProject: false
---

# Signed Note Commitment Integrity (Condition 1)

## Spec Condition

> `NoteCommit_rcm_signed(repr(g_d_signed), repr(pk_d_signed), 0, rho_signed, psi_signed) = cm_signed`
>
> The signed note's commitment is correctly constructed. **No null option** — strict equality, unlike Orchard's `∈ {cm, ⊥}`.

The `⊥` case is handled internally by `CommitDomain::commit`: incomplete addition constraints allow ⊥ to occur, and synthesis detects these edge cases and aborts proof creation. Since we don't allow ⊥, this means an invalid commitment simply prevents proof creation.

**Reference implementation**: [src/circuit.rs](src/circuit.rs) lines 616-642 (Orchard action circuit's old note commitment integrity).

## What Changes

Only one file is modified: [src/delegation/circuit.rs](src/delegation/circuit.rs). No new public inputs. `K = 12` remains sufficient.

---

### 1. Add imports

Add to the `crate::circuit` import block in [src/delegation/circuit.rs](src/delegation/circuit.rs) line 26:

- `note_commit` from `gadget` (alongside existing `assign_free_advice`, `commit_ivk`, `derive_nullifier`)
- `NoteCommitChip, NoteCommitConfig` from `note_commit` (new sub-import from `crate::circuit::note_commit`)

Add to the `crate::note::commitment` import block (line 38):

- `NoteCommitTrapdoor` (alongside existing `NoteCommitment`)

Add to the `crate` imports:

- `value::NoteValue` — needed for witnessing the zero value

### 2. Add `note_commit_config` to `Config` struct

In the `Config` struct ([circuit.rs](src/delegation/circuit.rs) lines 71-96), add:

```rust
note_commit_config: NoteCommitConfig,
```

Add helper method to `impl Config`:

```rust
fn note_commit_chip(&self) -> NoteCommitChip {
    NoteCommitChip::construct(self.note_commit_config.clone())
}
```

### 3. Add `NoteCommitChip::configure` to `configure()`

Insert after the existing `commit_ivk_config` setup ([circuit.rs](src/delegation/circuit.rs) line 255):

```rust
let note_commit_config =
    NoteCommitChip::configure(meta, advices, sinsemilla_config.clone());
```

`NoteCommitChip::configure` takes `(meta, advices, sinsemilla_config)` — it reuses the existing Sinsemilla config and 10 advice columns. It creates selectors for decomposition/canonicity gates (b, d, e, g, h decompositions; g_d, pk_d, value, rho, psi canonicity; y-coordinate canonicity).

Include `note_commit_config` in the `Config` return struct.

### 4. Add `rcm_signed` to `Circuit` struct

In the `Circuit` struct ([circuit.rs](src/delegation/circuit.rs) lines 133-144), add:

```rust
rcm_signed: Value<NoteCommitTrapdoor>,
```

`Default` derive still works: `Value<T>` defaults to `Value::unknown()`.

### 5. Update `from_note_unchecked` constructor

Extract `rcm_signed` from the note, following the Orchard pattern ([src/circuit.rs](src/circuit.rs) line 159):

```rust
let rcm_signed = note.rseed().rcm(&rho_old);
// ...
rcm_signed: Value::known(rcm_signed),
```

### 6. Add note commitment integrity block in `synthesize()`

This goes **after** the diversified address integrity block, which must be restructured to return `pk_d_signed` so it can be reused.

**Step 6a: Restructure address integrity to return `pk_d_signed**`

Currently `pk_d_signed` is scoped inside `{ ... }`. Change the block to return `pk_d_signed`, following the Orchard pattern at [src/circuit.rs](src/circuit.rs) lines 571-614:

```rust
let pk_d_signed = {
    // ... existing address integrity code ...
    pk_d_signed  // return the NonIdentityPoint
};
```

**Step 6b: Add the note commitment integrity block**

After the address integrity block, insert:

```rust
// Old note commitment integrity.
// NoteCommit_rcm_signed(repr(g_d_signed), repr(pk_d_signed), 0,
//                        rho_signed, psi_signed) = cm_signed
// No null option: the signed note must have a valid commitment.
{
    let rcm_signed = ScalarFixed::new(
        ecc_chip.clone(),
        layouter.namespace(|| "rcm_signed"),
        self.rcm_signed.as_ref().map(|rcm| rcm.inner()),
    )?;

    // The signed note's value is always 0.
    let v_signed = assign_free_advice(
        layouter.namespace(|| "v_signed = 0"),
        config.advices[0],
        Value::known(NoteValue::zero()),
    )?;

    // Compute NoteCommit from witness data.
    let derived_cm_signed = gadget::note_commit(
        layouter.namespace(|| "NoteCommit_rcm_signed(g_d, pk_d, 0, rho, psi)"),
        config.sinsemilla_chip(),
        config.ecc_chip(),
        config.note_commit_chip(),
        g_d_signed.inner(),
        pk_d_signed.inner(),
        v_signed,
        rho_old,
        psi_old,
        rcm_signed,
    )?;

    // Strict equality — no null/bottom option.
    derived_cm_signed.constrain_equal(
        layouter.namespace(|| "cm_signed integrity"),
        &cm_old,
    )?;
}
```

Key differences from Orchard:

- Value is hardcoded as `NoteValue::zero()` (the signed/dummy note always has v=0)
- Strict equality constraint (no conditional on enable_spends)
- Reuses `g_d_signed`, `pk_d_signed`, `rho_old`, `psi_old`, `cm_old` already witnessed earlier in the circuit

**Note on variable consumption**: `rho_old` and `psi_old` are consumed by `derive_nullifier` earlier. They need to be **cloned** before being passed to `derive_nullifier` so they remain available for `note_commit`. This requires changing the nullifier integrity section to clone these values.

### 7. Clone `rho_old` and `psi_old` for reuse

Currently at [circuit.rs](src/delegation/circuit.rs) lines 354-363:

```rust
let nf_old = derive_nullifier(
    ...,
    rho_old,      // consumed
    &psi_old,     // borrowed (already fine)
    &cm_old,
    nk.clone(),
)?;
```

`rho_old` is moved into `derive_nullifier`. Change to `rho_old.clone()`:

```rust
let nf_old = derive_nullifier(
    ...,
    rho_old.clone(),  // clone so rho_old remains available for note_commit
    &psi_old,
    &cm_old,
    nk.clone(),
)?;
```

### 8. Tests

**Existing tests pass without modification** — `from_note_unchecked` now populates `rcm_signed` with correct data, and the MockProver verifies all constraints.

**Add `note_commit_integrity_happy_path` test** — explicit coverage:

```rust
#[test]
fn note_commit_integrity_happy_path() {
    let (circuit, nf, rk) = make_test_note();
    let instance = Instance::from_parts(nf, rk);
    let public_inputs = instance.to_halo2_instance();
    let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
    assert_eq!(prover.verify(), Ok(()));
}
```

**Add `note_commit_integrity_wrong_rcm` test** — wrong commitment randomness:

```rust
#[test]
fn note_commit_integrity_wrong_rcm() {
    let mut rng = OsRng;
    let (_sk, fvk, note) = Note::dummy(&mut rng, None);
    let nf = note.nullifier(&fvk);
    let ak: SpendValidatingKey = fvk.clone().into();
    let alpha = pallas::Scalar::random(&mut rng);
    let rk = ak.randomize(&alpha);
    let mut circuit = Circuit::from_note_unchecked(&fvk, &note, alpha);

    // Replace rcm_signed with a random trapdoor
    use crate::note::commitment::NoteCommitTrapdoor;
    circuit.rcm_signed = Value::known(NoteCommitTrapdoor(pallas::Scalar::random(&mut rng)));

    let instance = Instance::from_parts(nf, rk);
    let public_inputs = instance.to_halo2_instance();
    let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
    assert!(prover.verify().is_err());
}
```

**Add `note_commit_integrity_wrong_cm` test** — correct inputs but wrong witnessed cm:

```rust
#[test]
fn note_commit_integrity_wrong_cm() {
    let mut rng = OsRng;
    let (_sk1, fvk1, note1) = Note::dummy(&mut rng, None);
    let (_sk2, _fvk2, note2) = Note::dummy(&mut rng, None);

    // Build circuit from note1 but use note2's commitment
    let nf = note1.nullifier(&fvk1);
    let ak: SpendValidatingKey = fvk1.clone().into();
    let alpha = pallas::Scalar::random(&mut rng);
    let rk = ak.randomize(&alpha);
    let mut circuit = Circuit::from_note_unchecked(&fvk1, &note1, alpha);
    circuit.cm_old = Value::known(note2.commitment());

    let instance = Instance::from_parts(nf, rk);
    let public_inputs = instance.to_halo2_instance();
    let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
    assert!(prover.verify().is_err());
}
```

This test verifies that both the note commitment integrity AND nullifier integrity fail when `cm_old` is tampered with (since the nullifier is derived from `cm_old` too).

### Circuit Size

NoteCommit adds approximately:

- Sinsemilla hash of message pieces: ~500-600 rows
- Decomposition and canonicity checks: ~200-300 rows
- Scalar fixed multiplication (rcm): ~255 rows

**Total addition**: ~950-1150 rows. Combined with existing ~1130-1230 rows, the circuit uses approximately ~2080-2380 rows out of the 4096-row budget at K=12. This leaves headroom for future conditions (Merkle path, SMT non-membership, governance nullifiers).

### Ordering of changes

Apply changes in this order to maintain a compilable circuit at each step:

1. **Imports** (step 1)
2. **Config struct + helpers** (steps 2-3) — adds `note_commit_config` plumbing
3. **Circuit struct + constructor** (steps 4-5) — adds `rcm_signed` witness
4. **Clone rho_old** (step 7) — needed before note_commit can consume it
5. **Restructure address integrity** (step 6a) — return `pk_d_signed`
6. **Note commitment integrity block** (step 6b) — adds the new constraint
7. **Tests** (step 8) — adds explicit coverage

