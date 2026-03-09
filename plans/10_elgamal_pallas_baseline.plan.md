---
name: El Gamal on Pallas curve baseline
overview: Implement El Gamal encryption over the Pallas curve using the existing `mikelodder7/curvey` Go library — encryption/decryption, homomorphic accumulation, bounded discrete log (BSGS), and Chaum-Pedersen DLEQ proof — as a standalone crypto package that later integrates into the on-chain tally accumulator.
todos:
  - id: dep
    content: "Add github.com/mikelodder7/curvey dependency to sdk/go.mod and verify Pallas curve operations work with a smoke test"
    status: done
  - id: elgamal-core
    content: "Implement El Gamal: keypair generation, Encrypt (v,r) -> (r*G, v*G+r*pk), DecryptToPoint, HomomorphicAdd, with property-based tests for homomorphism"
    status: done
  - id: bsgs
    content: "Implement baby-step giant-step (BSGS) for bounded discrete log recovery of v from v*G, with configurable upper bound (default 2^32), precomputed table, and tests"
    status: done
  - id: dleq-proof
    content: "Implement Chaum-Pedersen DLEQ proof (prove/verify) for correct decryption, with Fiat-Shamir transcript, and tests"
    status: pending
  - id: serialization
    content: "Implement Ciphertext serialization matching the protobuf PallasPoint/ElGamalCiphertext format (64 bytes = two compressed points)"
    status: pending
  - id: integration-test
    content: "End-to-end test: keygen -> encrypt 4 shares -> homomorphic sum -> decrypt aggregate -> verify DLEQ proof -> recover plaintext value, matching the spec Appendix A+B flow"
    status: pending
isProject: true
---

# El Gamal on Pallas Curve — Baseline Implementation

## Context

The voting protocol (see `shielded_vote_book/appendices/el-gamal.md` and `shielded_vote_book/appendices/tally.md`) requires **additively homomorphic El Gamal encryption** over the **Pallas curve** for private vote tallying. Each voting share is encrypted under the election authority's public key `ea_pk`, shares are accumulated homomorphically on-chain, and only the aggregate is decrypted at tally time.

**Current state**: The tally accumulator uses plaintext `uint64` addition (`AddToTally` in `keeper.go`). `MsgRevealShare` carries a `uint64 vote_amount` field. Plan 09 explicitly deferred El Gamal as a non-goal. This plan builds the cryptographic foundation that will later replace the plaintext accumulator.

## Key Decision: Use `mikelodder7/curvey` for Pallas Curve

**Library**: [`github.com/mikelodder7/curvey`](https://github.com/mikelodder7/curvey) — Apache 2.0, derived from Coinbase's kryptology library.

**What it provides**:
- Complete Pallas curve implementation in pure Go (no CGo/FFI)
- Constant-time field arithmetic using optimized 4-limb Montgomery representation (`native.Field4`)
- Full `Fp` (base field) and `Fq` (scalar field) with add, sub, mul, inv, sqrt, pow
- Point operations: Add, Sub, Double, ScalarMul, Neg, Identity, Generator, SumOfProducts
- Compressed point encoding (32 bytes, sign bit in top bit) matching Zcash format
- Hash-to-curve via SSWU map with Blake2b XMD
- Well-tested with known test vectors

**What we build on top**: Only the El Gamal scheme (encrypt/decrypt/accumulate), BSGS discrete log, and DLEQ proof. The curve arithmetic is fully handled.

**API we use from curvey**:

```go
import "github.com/mikelodder7/curvey"

// Points (PointPallas implements curvey.Point interface)
G  := new(curvey.PointPallas).Generator()       // Pallas generator
id := new(curvey.PointPallas).Identity()         // Point at infinity
p3 := p1.Add(p2)                                 // Point addition
p3  = p1.Sub(p2)                                 // Point subtraction
p2  = p1.Mul(scalar)                             // Scalar multiplication
p2  = p1.Neg()                                   // Negation
ok  = p1.Equal(p2)                               // Equality
ok  = p1.IsIdentity()                            // Identity check
bs  = p1.ToAffineCompressed()                    // Serialize to 32 bytes
p1, err = new(curvey.PointPallas).FromAffineCompressed(bs) // Deserialize

// Scalars (ScalarPallas implements curvey.Scalar interface)
s  := new(curvey.ScalarPallas).Random(rand.Reader) // Random scalar in Fq
s   = new(curvey.ScalarPallas).New(42)              // Scalar from int
s, err = new(curvey.ScalarPallas).SetBigInt(v)      // Scalar from big.Int
s3  = s1.Add(s2)                                    // Scalar addition
s3  = s1.Mul(s2)                                    // Scalar multiplication
s2  = s1.Neg()                                      // Scalar negation
bs  = s1.Bytes()                                    // Serialize to 32 bytes
```

## Package Location

```
sdk/crypto/elgamal/         # El Gamal scheme on Pallas via curvey
  elgamal.go                # Keypair, Encrypt, Decrypt (to v*G), HomomorphicAdd
  elgamal_test.go
  bsgs.go                   # Baby-step giant-step for discrete log recovery
  bsgs_test.go
  dleq.go                   # Chaum-Pedersen DLEQ proof (prove + verify)
  dleq_test.go
  serialize.go              # Ciphertext <-> bytes conversion for protobuf
  serialize_test.go
  tally.go                  # Tally aggregation helpers
  tally_test.go
```

This follows the existing pattern: `sdk/crypto/redpallas/` and `sdk/crypto/zkp/halo2/`.

## Implementation Details

### Step 1: Add Dependency (`sdk/go.mod`)

```bash
cd sdk && go get github.com/mikelodder7/curvey@latest
```

Smoke test to verify Pallas works:

```go
func TestCurveySmokeTest(t *testing.T) {
    G := new(curvey.PointPallas).Generator()
    s := new(curvey.ScalarPallas).New(7)
    p := G.Mul(s)
    require.False(t, p.IsIdentity())
    require.True(t, p.IsOnCurve())

    // Serialize round-trip
    bs := p.ToAffineCompressed()
    require.Len(t, bs, 32)
    p2, err := new(curvey.PointPallas).FromAffineCompressed(bs)
    require.NoError(t, err)
    require.True(t, p.Equal(p2))
}
```

### Step 2: El Gamal Core (`sdk/crypto/elgamal/elgamal.go`)

```go
package elgamal

import (
    "io"
    "github.com/mikelodder7/curvey"
)

// PublicKey is the election authority's public key: ea_pk = ea_sk * G
type PublicKey struct {
    Point curvey.Point // *PointPallas
}

// SecretKey is the election authority's secret key.
type SecretKey struct {
    Scalar curvey.Scalar // *ScalarPallas
}

// Ciphertext is an El Gamal ciphertext: (C1, C2) = (r*G, v*G + r*pk)
type Ciphertext struct {
    C1 curvey.Point // r * G
    C2 curvey.Point // v * G + r * pk
}

// KeyGen generates an election authority keypair.
func KeyGen(rng io.Reader) (*SecretKey, *PublicKey) {
    sk := new(curvey.ScalarPallas).Random(rng)
    pk := new(curvey.PointPallas).Generator().Mul(sk)
    return &SecretKey{Scalar: sk}, &PublicKey{Point: pk}
}

// Encrypt encrypts a value v under pk with fresh randomness.
// Enc(v, r) = (r*G, v*G + r*pk)
func Encrypt(pk *PublicKey, v uint64, rng io.Reader) *Ciphertext {
    r := new(curvey.ScalarPallas).Random(rng)
    return EncryptWithRandomness(pk, v, r)
}

// EncryptWithRandomness encrypts with explicit randomness (for ZKP witness reproduction).
func EncryptWithRandomness(pk *PublicKey, v uint64, r curvey.Scalar) *Ciphertext {
    G := new(curvey.PointPallas).Generator()
    vScalar := new(curvey.ScalarPallas).New(int(v))  // Note: for large v, use SetBigInt
    C1 := G.Mul(r)                                    // r * G
    C2 := G.Mul(vScalar).Add(pk.Point.Mul(r))         // v * G + r * pk
    return &Ciphertext{C1: C1, C2: C2}
}

// DecryptToPoint decrypts to v*G (not v itself).
// C2 - ea_sk * C1 = v*G
func DecryptToPoint(sk *SecretKey, ct *Ciphertext) curvey.Point {
    skC1 := ct.C1.Mul(sk.Scalar)  // ea_sk * C1
    return ct.C2.Sub(skC1)         // C2 - ea_sk * C1 = v*G
}

// HomomorphicAdd sums two ciphertexts component-wise.
// Enc(a) + Enc(b) = Enc(a + b)
func HomomorphicAdd(a, b *Ciphertext) *Ciphertext {
    return &Ciphertext{
        C1: a.C1.Add(b.C1),  // (r_a + r_b) * G
        C2: a.C2.Add(b.C2),  // (a + b) * G + (r_a + r_b) * pk
    }
}

// EncryptZero encrypts the value 0 (useful for initializing accumulators).
func EncryptZero() *Ciphertext {
    id := new(curvey.PointPallas).Identity()
    return &Ciphertext{C1: id, C2: id}
}
```

**Key property tests**:
- `DecryptToPoint(Encrypt(v)) == v*G` for various v
- `DecryptToPoint(HomomorphicAdd(Encrypt(a), Encrypt(b))) == (a+b)*G`
- `HomomorphicAdd` is commutative and associative
- `HomomorphicAdd(ct, EncryptZero()) == ct`
- `Encrypt(0)` decrypts to identity point

### Step 3: Baby-Step Giant-Step (`sdk/crypto/elgamal/bsgs.go`)

Recovers `v` from `v*G` with `v` bounded by `[0, N)`.

```go
// BSGSTable precomputes a lookup table for baby-step giant-step discrete log.
type BSGSTable struct {
    table map[[32]byte]uint64  // compressed baby-step point -> index
    m     uint64               // sqrt(N), baby step count
    mG    curvey.Point         // m * G (giant step increment)
}

// NewBSGSTable precomputes a table for values in [0, N).
// Memory: ~sqrt(N) * 40 bytes.  For N = 2^32: table has 2^16 entries (~2.5 MB).
func NewBSGSTable(N uint64) *BSGSTable

// Solve recovers v from vG = v * G, or returns error if v >= N.
func (t *BSGSTable) Solve(vG curvey.Point) (uint64, error)
```

Algorithm:
1. Baby steps: precompute `table[i*G] = i` for `i in [0, m)` where `m = ceil(sqrt(N))`
2. Giant steps: for `j = 0, 1, ...`, compute `vG - j*mG` and look up in table
3. If found at baby step `i`: `v = j*m + i`

For `N = 2^32`: table has `2^16 = 65,536` entries. Lookup is O(2^16) in worst case.

### Step 4: Chaum-Pedersen DLEQ Proof (`sdk/crypto/elgamal/dleq.go`)

Proves correct decryption: the same `ea_sk` that generated `ea_pk = ea_sk * G` was used to compute `ea_sk * C1`.

Statement: prove `log_G(ea_pk) == log_C1(ea_sk * C1)` without revealing `ea_sk`.

```go
// DLEQProof is a non-interactive proof of discrete log equality.
type DLEQProof struct {
    Challenge curvey.Scalar  // c = H(G, pk, C1, skC1, R1, R2)
    Response  curvey.Scalar  // s = k - c * sk  (mod q)
}

// ProveDLEQ generates a DLEQ proof using Fiat-Shamir.
func ProveDLEQ(sk *SecretKey, pk *PublicKey, c1 curvey.Point, skC1 curvey.Point, rng io.Reader) *DLEQProof

// VerifyDLEQ checks a DLEQ proof.
// Verifies: s*G + c*pk == R1  AND  s*C1 + c*skC1 == R2
func VerifyDLEQ(proof *DLEQProof, pk *PublicKey, c1 curvey.Point, skC1 curvey.Point) bool
```

Fiat-Shamir protocol:
1. Prover samples random `k`, computes `R1 = k*G`, `R2 = k*C1`
2. Challenge: `c = H(G || pk || C1 || skC1 || R1 || R2)` using Blake2b
3. Response: `s = k - c * sk`
4. Verifier reconstructs `R1 = s*G + c*pk`, `R2 = s*C1 + c*skC1`, recomputes `c`, checks equality

### Step 5: Ciphertext Serialization (`sdk/crypto/elgamal/serialize.go`)

Matches the protobuf `ElGamalCiphertext { PallasPoint c1; PallasPoint c2; }`:

```go
// MarshalCiphertext serializes a ciphertext to 64 bytes (two compressed points).
func MarshalCiphertext(ct *Ciphertext) [64]byte

// UnmarshalCiphertext deserializes a ciphertext from 64 bytes.
func UnmarshalCiphertext(data [64]byte) (*Ciphertext, error)

// MarshalDLEQProof serializes a DLEQ proof to 64 bytes (two scalars).
func MarshalDLEQProof(proof *DLEQProof) [64]byte

// UnmarshalDLEQProof deserializes a DLEQ proof from 64 bytes.
func UnmarshalDLEQProof(data [64]byte) (*DLEQProof, error)
```

### Step 6: End-to-End Tally Test (`sdk/crypto/elgamal/tally_test.go`)

```go
func TestFullTallyFlow(t *testing.T) {
    // 1. Election authority generates keypair
    sk, pk := KeyGen(rand.Reader)

    // 2. Voter 1: 99 ZEC decomposed into 4 shares [64, 32, 2, 1]
    shares1 := []uint64{64, 32, 2, 1}
    // 3. Voter 2: 128 ZEC decomposed into 4 shares [128, 0, 0, 0]
    shares2 := []uint64{128, 0, 0, 0}

    // 4. Encrypt all shares
    var allCts []*Ciphertext
    for _, v := range append(shares1, shares2...) {
        allCts = append(allCts, Encrypt(pk, v, rand.Reader))
    }

    // 5. On-chain accumulation: sum all ciphertexts for (proposal=0, decision=YES)
    acc := EncryptZero()
    for _, ct := range allCts {
        acc = HomomorphicAdd(acc, ct)
    }

    // 6. EA decrypts aggregate
    vG := DecryptToPoint(sk, acc)

    // 7. EA recovers plaintext via BSGS
    table := NewBSGSTable(1 << 32)
    total, err := table.Solve(vG)
    require.NoError(t, err)
    require.Equal(t, uint64(227), total) // 99 + 128

    // 8. EA produces DLEQ proof of correct decryption
    skC1 := acc.C1.Mul(sk.Scalar)
    proof := ProveDLEQ(sk, pk, acc.C1, skC1, rand.Reader)

    // 9. Anyone verifies
    require.True(t, VerifyDLEQ(proof, pk, acc.C1, skC1))

    // 10. Serialize/deserialize round-trip
    ctBytes := MarshalCiphertext(acc)
    accRT, err := UnmarshalCiphertext(ctBytes)
    require.NoError(t, err)
    require.True(t, acc.C1.Equal(accRT.C1))
    require.True(t, acc.C2.Equal(accRT.C2))
}
```

## Sequencing

```
Step 1: Add curvey dep ──► Step 2: El Gamal core ──┬──► Step 3: BSGS ──────┐
                                                     │                       │
                                                     ├──► Step 4: DLEQ ─────┤
                                                     │                       │
                                                     └──► Step 5: Serialize ─┤
                                                                             │
                                                                             ▼
                                                                    Step 6: E2E tally test
```

Step 1 is a prerequisite. Step 2 (El Gamal core) depends on it. Steps 3, 4, and 5 can be done in parallel after step 2. Step 6 requires all prior steps.

## BSGS Table Sizing

Per spec: per-share values are bounded by `2^24` (ZKP #2 cond. 8), but the aggregate across all voters could reach ~2.1 * 10^15 zatoshi. Default bound of `2^32` uses a table of `2^16` entries (~2.5 MB). The EA only runs BSGS once at tally time (not on-chain), so even `2^40` with a two-level decomposition is feasible if needed.

## Future Work (out of scope for this plan)

- **On-chain integration**: Replace `uint64` tally accumulator with `ElGamalCiphertext` in the keeper
- **Proto changes**: Add `ElGamalCiphertext` message to `tx.proto` for `MsgRevealShare.enc_share`
- **DLEQ verification in `MsgSubmitTally`**: Validate `decryption_proof` field against on-chain `ea_pk`
- **Constant-time hardening**: Audit curvey's scalar mul for timing side channels in our usage
- **Benchmark**: Profile on-chain accumulation (one HomomorphicAdd per MsgRevealShare)
