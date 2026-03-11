---
name: Diversified Address Integrity
overview: "Add Condition 5 (Diversified Address Integrity) to the delegation circuit: compute ivk = CommitIvk_rivk(ExtractP(ak), nk) and constrain pk_d_signed = [ivk] * g_d_signed. Requires adding CommitIvkChip, new private witnesses (rivk, g_d_signed, pk_d_signed), and restructuring synthesize() to share ak_P and nk across checks."
todos:
  - id: add-imports
    content: "Add imports: commit_ivk gadget, CommitIvkChip/Config, CommitIvkRandomness, DiversifiedTransmissionKey, NullifierDerivingKey, Scope, NonIdentityPallasPoint, ScalarVar"
    status: completed
  - id: add-commit-ivk-config
    content: "Add commit_ivk_config: CommitIvkConfig to Config struct; add commit_ivk_chip() and sinsemilla_chip() helper methods"
    status: completed
  - id: configure-commit-ivk
    content: "Add CommitIvkChip::configure(meta, advices) call in configure() after sinsemilla_config; include commit_ivk_config in Config return"
    status: completed
  - id: update-circuit-struct
    content: "Change nk to Value<NullifierDerivingKey>; add rivk, g_d_signed, pk_d_signed fields to Circuit struct"
    status: completed
  - id: update-constructor
    content: "Update from_note_unchecked to extract rivk via fvk.rivk(Scope::External), g_d_signed and pk_d_signed from note recipient address; change nk to Value::known(*fvk.nk())"
    status: completed
  - id: restructure-synthesize
    content: "Lift ak_P and g_d_signed witnessing before nullifier block; change nk witness to use self.nk.map(|nk| nk.inner()); clone nk for derive_nullifier; simplify spend authority block to reuse shared ak_P"
    status: completed
  - id: add-address-integrity-block
    content: "Add diversified address integrity block after spend authority: compute ivk via commit_ivk, convert to ScalarVar, mul g_d_signed, constrain derived pk_d_signed to witnessed pk_d_signed"
    status: completed
  - id: update-tests
    content: "Add address_integrity_happy_path, address_integrity_wrong_rivk, and address_integrity_wrong_pk_d tests; add Scope import to test module"
    status: completed
isProject: false
---

# Delegation Circuit: Diversified Address Integrity (Condition 5)

## Spec Condition

> `ivk = ⊥` or `pk_d_signed = [ivk] * g_d_signed`
> where `ivk = CommitIvk_rivk(ExtractP(ak_P), nk)`
>
> Proves the signed note's address belongs to the same key material `(ak, nk)`.
> This is where `ivk` is established -- it will be reused for every real note ownership check below.

The `ivk = ⊥` case is handled internally by `CommitDomain::short_commit`: incomplete addition allows the identity to occur, and synthesis detects this edge case and aborts proof creation. No explicit conditional is needed in the circuit.

**Reference implementation**: [src/vote/circuit.rs](src/vote/circuit.rs) lines 733-777.

## What Changes

Only one file is modified: [src/delegation/circuit.rs](src/delegation/circuit.rs). No new public inputs. `K = 12` remains sufficient (adds ~400-500 rows to ~4096 budget).

---

### 1. Add imports

Add to the `crate::` import block:

- `commit_ivk` from `crate::circuit::gadget` (alongside existing `assign_free_advice`, `derive_nullifier`)
- `CommitIvkChip, CommitIvkConfig` from `crate::circuit::commit_ivk`
- `CommitIvkRandomness, DiversifiedTransmissionKey, NullifierDerivingKey` from `crate::keys`
- `Scope` from `crate::keys` (for the constructor)
- `NonIdentityPallasPoint` from `crate::spec`

Add to the `halo2_gadgets::ecc` import block:

- `ScalarVar` (alongside existing `FixedPoint, NonIdentityPoint, Point, ScalarFixed`)

### 2. Add `commit_ivk_config` to `Config` struct

Current `Config` ([circuit.rs lines 60-83](src/delegation/circuit.rs)):

```rust
pub struct Config {
    primary: Column<InstanceColumn>,
    advices: [Column<Advice>; 10],
    add_config: AddConfig,
    ecc_config: EccConfig<OrchardFixedBases>,
    poseidon_config: PoseidonConfig<pallas::Base, 3, 2>,
    sinsemilla_config: SinsemillaConfig<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases>,
    commit_ivk_config: CommitIvkConfig,  // NEW
}
```

`CommitIvkChip::configure` takes only `(meta, advices)` -- it creates one selector and reuses the 10 advice columns. It does NOT need its own Sinsemilla config; the `commit_ivk` gadget receives a `SinsemillaChip` as a parameter at call time.

### 3. Add helper methods to `Config` impl

Add two new methods ([circuit.rs lines 85-101](src/delegation/circuit.rs)):

```rust
fn commit_ivk_chip(&self) -> CommitIvkChip {
    CommitIvkChip::construct(self.commit_ivk_config.clone())
}

fn sinsemilla_chip(&self) -> SinsemillaChip<OrchardHashDomains, OrchardCommitDomains, OrchardFixedBases> {
    SinsemillaChip::construct(self.sinsemilla_config.clone())
}
```

The existing single `sinsemilla_config` is reused for CommitIvk. A second Sinsemilla instance is not needed until NoteCommit (Condition 1) is added later.

### 4. Add `CommitIvkChip::configure` to `configure()`

Insert after the existing `sinsemilla_config` setup ([circuit.rs line 220](src/delegation/circuit.rs)):

```rust
let commit_ivk_config = CommitIvkChip::configure(meta, advices);
```

And add `commit_ivk_config` to the `Config` return struct at line 222.

### 5. Update `Circuit` struct fields

Current struct ([circuit.rs lines 108-116](src/delegation/circuit.rs)). Changes:

- **Change** `nk: Value<pallas::Base>` to `nk: Value<NullifierDerivingKey>` (type safety; matches vote circuit pattern at [src/vote/circuit.rs line 187](src/vote/circuit.rs))
- **Add** `rivk: Value<CommitIvkRandomness>` -- the randomness for CommitIvk
- **Add** `g_d_signed: Value<NonIdentityPallasPoint>` -- diversified generator from the note's address
- **Add** `pk_d_signed: Value<DiversifiedTransmissionKey>` -- diversified transmission key (witnessed for equality check)

`Default` derive still works: `Value<T>` defaults to `Value::unknown()` regardless of `T`.

### 6. Update `from_note_unchecked` constructor

Signature stays the same `(fvk, note, alpha)`. Extract new fields from existing parameters ([circuit.rs lines 118-131](src/delegation/circuit.rs)):

```rust
pub fn from_note_unchecked(fvk: &FullViewingKey, note: &Note, alpha: pallas::Scalar) -> Self {
    let sender_address = note.recipient();
    let rho_old = note.rho();
    let psi_old = note.rseed().psi(&rho_old);
    Circuit {
        nk: Value::known(*fvk.nk()),
        rho_old: Value::known(rho_old.0),
        psi_old: Value::known(psi_old),
        cm_old: Value::known(note.commitment()),
        ak: Value::known(fvk.clone().into()),
        alpha: Value::known(alpha),
        rivk: Value::known(fvk.rivk(Scope::External)),
        g_d_signed: Value::known(sender_address.g_d()),
        pk_d_signed: Value::known(*sender_address.pk_d()),
    }
}
```

`Scope::External` is correct because `Note::dummy` creates recipients with `fvk.address_at(0u32, Scope::External)` ([src/note.rs line 174](src/note.rs)).

### 7. Restructure `synthesize()` -- lift shared witnesses

Currently `ak_P` is scoped inside the spend authority block `{ ... }` ([circuit.rs lines 317-352](src/delegation/circuit.rs)) and `nk` is consumed by `derive_nullifier`. Both are needed by the new CommitIvk. Restructure to match the vote circuit's shared witness pattern ([src/vote/circuit.rs lines 470-585](src/vote/circuit.rs)):

**Move `ak_P` witnessing** before the nullifier integrity section (out of the spend authority block):

```rust
// Witness ak_P (shared between spend authority and CommitIvk)
let ak_P: Value<pallas::Point> = self.ak.as_ref().map(|ak| ak.into());
let ak_P = NonIdentityPoint::new(
    ecc_chip.clone(),
    layouter.namespace(|| "witness ak_P"),
    ak_P.map(|ak_P| ak_P.to_affine()),
)?;
```

**Witness `g_d_signed`** in the same shared section:

```rust
let g_d_signed = NonIdentityPoint::new(
    ecc_chip.clone(),
    layouter.namespace(|| "witness g_d_signed"),
    self.g_d_signed.as_ref().map(|gd| gd.to_affine()),
)?;
```

**Change `nk` witnessing** from `self.nk` to `self.nk.map(|nk| nk.inner())` (extract `pallas::Base` from `NullifierDerivingKey`).

**Clone `nk`** when passing to `derive_nullifier` so it remains available for `commit_ivk`:

```rust
let nf_old = derive_nullifier(
    ...,
    rho_old,
    &psi_old,
    &cm_old,
    nk.clone(),  // clone here so nk is still available for commit_ivk
)?;
```

**Simplify spend authority block** -- remove `ak_P` witnessing (already done above), just use the shared `ak_P`:

```rust
{
    let alpha = ScalarFixed::new(...)?;
    let (alpha_commitment, _) = { ... };
    let rk = alpha_commitment.add(layouter.namespace(|| "rk"), &ak_P)?;
    layouter.constrain_instance(rk.inner().x().cell(), config.primary, RK_X)?;
    layouter.constrain_instance(rk.inner().y().cell(), config.primary, RK_Y)?;
}
```

### 8. Add diversified address integrity block in `synthesize()`

Insert after the spend authority block, following the vote circuit pattern at [src/vote/circuit.rs lines 733-777](src/vote/circuit.rs):

```rust
// Diversified address integrity.
// ivk = ⊥ or pk_d_signed = [ivk] * g_d_signed
// where ivk = CommitIvk_rivk(ExtractP(ak_P), nk)
//
// The ⊥ case is handled internally by CommitDomain::short_commit:
// incomplete addition allows ⊥ to occur, and synthesis detects
// these edge cases and aborts proof creation.
{
    let ivk = {
        // ExtractP(ak_P) -- extract the x-coordinate from the curve point
        let ak = ak_P.extract_p().inner().clone();
        let rivk = ScalarFixed::new(
            ecc_chip.clone(),
            layouter.namespace(|| "rivk"),
            self.rivk.map(|rivk| rivk.inner()),
        )?;

        commit_ivk(
            config.sinsemilla_chip(),
            ecc_chip.clone(),
            config.commit_ivk_chip(),
            layouter.namespace(|| "CommitIvk"),
            ak,
            nk,
            rivk,
        )?
    };

    // Convert ivk (an x-coordinate) to a variable-base scalar for EC multiplication.
    let ivk = ScalarVar::from_base(
        ecc_chip.clone(),
        layouter.namespace(|| "ivk"),
        ivk.inner(),
    )?;

    // [ivk] g_d_signed -- derive the expected pk_d
    let (derived_pk_d_signed, _ivk) =
        g_d_signed.mul(layouter.namespace(|| "[ivk] g_d_signed"), ivk)?;

    // Witness pk_d_signed and constrain it to equal the derived value.
    let pk_d_signed = NonIdentityPoint::new(
        ecc_chip.clone(),
        layouter.namespace(|| "witness pk_d_signed"),
        self.pk_d_signed.map(|pk_d_signed| pk_d_signed.inner().to_affine()),
    )?;
    derived_pk_d_signed
        .constrain_equal(layouter.namespace(|| "pk_d_signed equality"), &pk_d_signed)?;
}
```

Key operations:
- `ak_P.extract_p()` extracts the x-coordinate (pallas::Base) from the Pallas point -- this is `ExtractP(ak)` from the spec
- `commit_ivk(...)` computes the Sinsemilla-based commitment `ivk = Commit^ivk_rivk(ak || nk)`
- `ScalarVar::from_base(...)` converts the field element `ivk` to a scalar suitable for variable-base multiplication
- `g_d_signed.mul(...)` computes `[ivk] * g_d_signed` as a variable-base scalar multiplication
- `constrain_equal` enforces the derived point equals the witnessed `pk_d_signed`

### 9. Instance struct -- no changes

No new public inputs. `ivk`, `g_d_signed`, and `pk_d_signed` all remain private. The public inputs stay as `[nf_old, rk_x, rk_y]`.

### 10. Update tests

**Existing tests pass without modification** -- `from_note_unchecked` now populates all new fields with correct values, and the MockProver will verify the new constraint alongside existing ones.

**Add new test imports** to the `mod tests` block:

```rust
use crate::keys::Scope;
```

**Add `address_integrity_happy_path` test** -- verifies the new constraint works with correct witnesses (may be redundant with `nullifier_integrity_happy_path` since both exercise the full circuit, but makes the test suite explicit):

```rust
#[test]
fn address_integrity_happy_path() {
    let (circuit, nf, rk) = make_test_note();
    let instance = Instance::from_parts(nf, rk);
    let public_inputs = instance.to_halo2_instance();
    let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
    assert_eq!(prover.verify(), Ok(()));
}
```

**Add `address_integrity_wrong_rivk` test** -- constructs a circuit with an incorrect `rivk` so the derived `ivk` is wrong:

```rust
#[test]
fn address_integrity_wrong_rivk() {
    let mut rng = OsRng;
    let (_sk, fvk, note) = Note::dummy(&mut rng, None);
    let nf = note.nullifier(&fvk);
    let ak: SpendValidatingKey = fvk.clone().into();
    let alpha = pallas::Scalar::random(&mut rng);
    let rk = ak.randomize(&alpha);
    let mut circuit = Circuit::from_note_unchecked(&fvk, &note, alpha);

    // Replace rivk with a different key's rivk
    let sk2 = SpendingKey::random(&mut rng);
    let fvk2: FullViewingKey = (&sk2).into();
    circuit.rivk = Value::known(fvk2.rivk(Scope::External));

    let instance = Instance::from_parts(nf, rk);
    let public_inputs = instance.to_halo2_instance();
    let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
    assert!(prover.verify().is_err());
}
```

This works because the child `mod tests` can access private fields of the parent module's `Circuit` struct.

**Add `address_integrity_wrong_pk_d` test** -- correct `ivk` and `g_d`, but the witnessed `pk_d` is from a different address:

```rust
#[test]
fn address_integrity_wrong_pk_d() {
    let mut rng = OsRng;
    let (_sk, fvk, note) = Note::dummy(&mut rng, None);
    let nf = note.nullifier(&fvk);
    let ak: SpendValidatingKey = fvk.clone().into();
    let alpha = pallas::Scalar::random(&mut rng);
    let rk = ak.randomize(&alpha);
    let mut circuit = Circuit::from_note_unchecked(&fvk, &note, alpha);

    // Replace pk_d with a different key's pk_d
    let sk2 = SpendingKey::random(&mut rng);
    let fvk2: FullViewingKey = (&sk2).into();
    let other_address = fvk2.address_at(0u32, Scope::External);
    circuit.pk_d_signed = Value::known(*other_address.pk_d());

    let instance = Instance::from_parts(nf, rk);
    let public_inputs = instance.to_halo2_instance();
    let prover = MockProver::run(K, &circuit, vec![public_inputs]).unwrap();
    assert!(prover.verify().is_err());
}
```

This works because `fvk2` has a completely different key tree, so `pk_d` from `fvk2`'s address will not satisfy `pk_d = [ivk] * g_d` where `ivk` is derived from `fvk1`'s key material.

### Circuit Size

No increase to `K = 12`. The diversified address integrity block adds approximately:

- 1 `CommitIvk` Sinsemilla commitment (~200-300 rows for short commit with canonicity checks)
- 1 `ScalarVar::from_base` conversion (~10 rows)
- 1 variable-base scalar multiplication (~255 rows for `g_d_signed.mul`)
- 1 point equality constraint (~5 rows)

**Total**: ~470-570 additional rows. Combined with the existing nullifier derivation (~400 rows) and spend authority (~260 rows), the circuit uses approximately ~1130-1230 rows out of the 4096-row budget at K=12. This leaves ample headroom for future conditions (NoteCommit, Merkle path, interval checks).

### Ordering of changes

Apply changes in this order to maintain a compilable circuit at each step:

1. **Imports** (step 1) — no behavioral change
2. **Config struct + helpers** (steps 2-4) — adds `commit_ivk_config` plumbing
3. **Circuit struct + constructor** (steps 5-6) — adds new witness fields
4. **Restructure synthesize** (step 7) — lifts shared witnesses, clones `nk`; existing tests still pass
5. **Address integrity block** (step 8) — adds the new constraint; all tests pass
6. **Tests** (step 10) — adds explicit coverage for the new constraint