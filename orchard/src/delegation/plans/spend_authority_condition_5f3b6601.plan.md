---
name: Spend Authority Condition
overview: "Add Spend Authority (Condition 4) to the delegation circuit: witness `ak` and `alpha`, compute `rk = [alpha] * SpendAuthG + ak` in-circuit, and constrain `rk` to two new public inputs (x, y). No new chips required -- uses only the existing EccChip."
todos:
  - id: update-imports-and-constants
    content: Add imports (NonIdentityPoint, FixedPoint, ScalarFixed, OrchardFixedBasesFull, SpendValidatingKey, VerificationKey, SpendAuth, GroupEncoding) and public input offset constants RK_X=1, RK_Y=2
    status: completed
  - id: update-circuit-struct
    content: "Add `ak: Value<SpendValidatingKey>` and `alpha: Value<pallas::Scalar>` fields to Circuit struct; update `from_note_unchecked` to accept `alpha` parameter and extract `ak` from fvk"
    status: completed
  - id: update-synthesize
    content: "Clone ecc_chip before derive_nullifier; add spend authority block after nullifier integrity: witness ak_P as NonIdentityPoint, witness alpha as ScalarFixed, compute [alpha]*SpendAuthG + ak_P, constrain result to RK_X and RK_Y public inputs"
    status: completed
  - id: update-instance
    content: "Add `rk: VerificationKey<SpendAuth>` to Instance struct; update from_parts signature; update to_halo2_instance to serialize rk as (x, y) field elements"
    status: completed
  - id: update-tests
    content: Update make_test_note to produce alpha/rk; update existing tests to pass rk to Instance; add spend_authority_wrong_rk test that uses mismatched alpha and asserts verify failure
    status: completed
isProject: false
---

# Delegation Circuit: Spend Authority (Condition 4)

## Spec Condition

> `rk = SpendAuthSig.RandomizePublic(alpha, ak)` i.e. `rk = ak + [alpha] * G`
>
> The public `rk` is a valid rerandomization of `ak`. Links to the keystone signature verified out-of-circuit.

## What Changes

Only one file is modified: `[src/delegation/circuit.rs](src/delegation/circuit.rs)`. No new chips, no config changes, no new dependencies.

### 1. Add imports

Add these to the existing import block:

- `NonIdentityPoint`, `FixedPoint`, `ScalarFixed` from `halo2_gadgets::ecc`
- `OrchardFixedBasesFull` from `crate::constants`
- `SpendValidatingKey` from `crate::keys`

### 2. Add public input offsets

Current state has only `NF_OLD = 0`. Add two new constants:

```rust
const NF_OLD: usize = 0;
const RK_X: usize = 1;
const RK_Y: usize = 2;
```

### 3. Add witness fields to `Circuit` struct

Add two new private witness fields, following the vote circuit's pattern at [src/vote/circuit.rs lines 185-186](src/vote/circuit.rs):

```rust
pub struct Circuit {
    // ... existing fields ...
    ak: Value<SpendValidatingKey>,
    alpha: Value<pallas::Scalar>,
}
```

The `Default` derive still works because `Value<T>` defaults to `Value::unknown()`.

### 4. Update `from_note_unchecked` constructor

Add `alpha` as a new parameter. Extract `ak` from `fvk` using `fvk.clone().into()` (the existing `From<FullViewingKey> for SpendValidatingKey` at [src/keys.rs line 339](src/keys.rs)):

```rust
pub fn from_note_unchecked(
    fvk: &FullViewingKey,
    note: &Note,
    alpha: pallas::Scalar,
) -> Self {
    // ... existing fields ...
    ak: Value::known(fvk.clone().into()),
    alpha: Value::known(alpha),
}
```

### 5. Add spend authority block to `synthesize()`

Insert after the existing nullifier integrity block (after line 298), following the exact pattern from the vote circuit at [src/vote/circuit.rs lines 713-731](src/vote/circuit.rs):

```rust
// Spend authority: rk = [alpha] * SpendAuthG + ak_P
{
    // Witness ak_P as a non-identity point
    let ak_P: Value<pallas::Point> = self.ak.as_ref().map(|ak| ak.into());
    let ak_P = NonIdentityPoint::new(
        ecc_chip.clone(),
        layouter.namespace(|| "witness ak_P"),
        ak_P.map(|ak_P| ak_P.to_affine()),
    )?;

    // Witness alpha as a full-width scalar
    let alpha = ScalarFixed::new(
        ecc_chip.clone(),
        layouter.namespace(|| "alpha"),
        self.alpha,
    )?;

    // [alpha] * SpendAuthG
    let (alpha_commitment, _) = {
        let spend_auth_g = OrchardFixedBasesFull::SpendAuthG;
        let spend_auth_g = FixedPoint::from_inner(ecc_chip.clone(), spend_auth_g);
        spend_auth_g.mul(layouter.namespace(|| "[alpha] SpendAuthG"), alpha)?
    };

    // rk = [alpha] * SpendAuthG + ak_P
    let rk = alpha_commitment.add(layouter.namespace(|| "rk"), &ak_P)?;

    // Constrain rk to public inputs (x and y coordinates)
    layouter.constrain_instance(rk.inner().x().cell(), config.primary, RK_X)?;
    layouter.constrain_instance(rk.inner().y().cell(), config.primary, RK_Y)?;
}
```

Note: `ecc_chip` is currently consumed by `derive_nullifier`. Change the `derive_nullifier` call to use `ecc_chip.clone()` so the chip remains available for the spend authority block.

### 6. Update `Instance` struct

Add `rk` field and update serialization. Follow the vote circuit's pattern at [src/vote/circuit.rs lines 928-990](src/vote/circuit.rs):

```rust
pub struct Instance {
    pub nf_old: Nullifier,
    pub rk: VerificationKey<SpendAuth>,
}
```

Update `from_parts`:

```rust
pub fn from_parts(nf_old: Nullifier, rk: VerificationKey<SpendAuth>) -> Self {
    Instance { nf_old, rk }
}
```

Update `to_halo2_instance` to serialize `rk` as two field elements (x, y), matching the vote circuit at lines 976-983:

```rust
pub fn to_halo2_instance(&self) -> Vec<vesta::Scalar> {
    let rk = pallas::Point::from_bytes(&self.rk.clone().into())
        .unwrap()
        .to_affine()
        .coordinates()
        .unwrap();

    vec![self.nf_old.0, *rk.x(), *rk.y()]
}
```

This requires adding imports for `VerificationKey`, `SpendAuth`, and `group::GroupEncoding`.

### 7. Update tests

Update `make_test_note` to also produce `alpha`, `rk`, and `ak` -- following the pattern from [src/circuit.rs lines 985-987](src/circuit.rs):

```rust
fn make_test_note() -> (Circuit, Nullifier, VerificationKey<SpendAuth>) {
    let mut rng = OsRng;
    let (_sk, fvk, note) = Note::dummy(&mut rng, None);
    let nf = note.nullifier(&fvk);
    let ak: SpendValidatingKey = fvk.clone().into();
    let alpha = pallas::Scalar::random(&mut rng);
    let rk = ak.randomize(&alpha);
    let circuit = Circuit::from_note_unchecked(&fvk, &note, alpha);
    (circuit, nf, rk)
}
```

Update existing tests to pass `rk` into `Instance::from_parts(nf, rk)`.

Add one new test:

- `**spend_authority_wrong_rk**`: Construct the circuit with one `alpha`, but compute `rk` with a different random `alpha`. Assert `MockProver::verify()` fails.

### Circuit Size

No increase to `K = 12`. The spend authority block adds approximately:

- 1 fixed-base scalar multiplication (~255 rows for ScalarFixed)
- 1 point addition (~5 rows)
- 2 instance constraints (free)

This fits comfortably within the existing 4096-row budget alongside the nullifier derivation.