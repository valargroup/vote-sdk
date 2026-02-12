// Package elgamal implements additively homomorphic El Gamal encryption
// over the Pallas curve using the mikelodder7/curvey library.
//
// It provides keypair generation, encryption/decryption, homomorphic
// ciphertext addition, baby-step giant-step discrete log recovery,
// and Chaum-Pedersen DLEQ proofs for correct decryption.
//
// # Dependency: mikelodder7/curvey
//
// This package depends on github.com/mikelodder7/curvey for all elliptic
// curve operations on the Pallas curve. Because cryptographic code is a
// high-value target for supply-chain attacks, the following steps have been
// taken to establish confidence in this dependency:
//
// Maintainer vetting:
//   - Author is Michael Lodder, a known cryptographer and active maintainer
//     of Hyperledger Ursa (Hyperledger's core crypto library).
//   - Contributor to Hyperledger Indy, AnonCreds v2.0, and LIT Protocol
//     threshold cryptography.
//   - GitHub account active since 2012; 56 public repos, almost all
//     cryptographic primitives (VSS, Paillier, accumulators, DKG, FROST).
//
// Code provenance:
//   - The curvey library is derived from Coinbase's kryptology library
//     (github.com/coinbase/kryptology, 868 stars, now archived).
//   - The Pallas implementation, native field arithmetic, and curve
//     interface are structurally identical to the Coinbase original.
//   - Michael Lodder added the Pallas curve and extracted the standalone
//     curvey package after kryptology was archived.
//
// Supply-chain integrity:
//   - The module is registered in Go's checksum database (sum.golang.org),
//     ensuring byte-for-byte reproducibility across all consumers.
//   - go.sum pins the exact module hash; any tampering would require an
//     explicit go get -u to take effect.
//   - All 10 commits in the repo are by a single author (no rogue
//     contributors), and v1.1.1 is a lightweight tag on commit a278380.
//   - No GitHub Actions, build hooks, CGo, or assembly — pure Go only.
//   - Dependencies are all mainstream: filippo.io/edwards25519,
//     btcsuite/btcd, bwesterb/go-ristretto, golang.org/x/crypto.
//   - Zero known CVEs or advisories as of 2026-02-12.
//
// Runtime safety:
//   - The library is pure math with no network, filesystem, or OS calls.
//   - No init() functions that execute at import time.
//   - All operations are constant-time per the library's design goals.
//
// Mathematical correctness tests (curvey_correctness_test.go):
//   - Curve constants (p, q, BitSize, generator coordinates) verified
//     against the canonical Pasta curves specification.
//   - Generator satisfies the curve equation y^2 = x^3 + 5 (mod p).
//   - Group order tests: q*G = identity, (q-1)*G + G = identity,
//     scalar(q) = 0, scalar(q+1) = 1.
//   - Serialization round-trips (compressed, uncompressed, scalar)
//     fuzzed over 100 random points/scalars each.
//   - Group law stress tests (50 random iterations each):
//     distributivity, scalar associativity, point commutativity,
//     point associativity, doubling, subtraction.
//   - Scalar field arithmetic cross-checked against independent
//     math/big modular operations (add, mul, sub, neg, invert)
//     over 100 random pairs each.
//
// Residual risk:
//   - No formal third-party audit of the Pallas field arithmetic exists
//     for either curvey or the Coinbase kryptology original.
//   - A sophisticated cryptographic backdoor would not be caught by
//     functional tests. Unlike conventional malware (network calls,
//     file exfiltration), a crypto backdoor lives entirely within the
//     math and produces outputs that look correct to every test while
//     being subtly exploitable by someone who knows the flaw. Examples
//     specific to this codebase:
//
//     1. Biased scalar generation: ScalarPallas.Random() hashes 64
//     seed bytes through BLAKE2b to produce a scalar. If the hash-to-
//     field reduction were slightly wrong (e.g. non-uniform mod q),
//     the resulting scalars would cluster in a subrange. Every test
//     passes — random scalars still look random — but an attacker
//     could brute-force secret keys or encryption randomness in far
//     fewer than q attempts. Detection requires statistical analysis
//     over millions of samples, not unit tests.
//
//     2. Wrong curve constant: If a Montgomery multiplication constant
//     in native/pasta/fq (e.g. the R^2 mod q used for Montgomery
//     form conversion) were off by one bit, field arithmetic would
//     silently operate in a slightly different ring. Point addition
//     and scalar multiplication would still appear self-consistent
//     (our group law tests would pass) because both sides of each
//     equation use the same wrong arithmetic. But the effective group
//     order would differ from the real Pallas order, meaning
//     ciphertexts produced here could not interoperate with a correct
//     implementation, and the discrete log security level would be
//     unknown. Our TestPallasGroupOrder and TestPallasBaseFieldModulus
//     tests catch the high-level constants (p, q) but not the internal
//     Montgomery representation.
//
//     3. Non-constant-time code path: If scalar multiplication leaks
//     timing information through a data-dependent branch (e.g. early
//     exit on zero bits of the scalar), an attacker observing response
//     times could recover secret keys via side-channel analysis. The
//     library claims constant-time operation, but this is impossible
//     to verify through functional tests — it requires timing analysis
//     or formal verification of the compiled binary.
//
//     4. Weak hash-to-curve: The PointPallas.Hash() method maps
//     arbitrary bytes to a curve point via SSWU + BLAKE2b. If the
//     mapping had a subtle flaw (e.g. mapping to a small subgroup),
//     hash-derived points could be distinguishable from random, which
//     would break protocols that rely on the random oracle model. This
//     is not exercised by our El Gamal code today but would matter for
//     future Fiat-Shamir or commitment schemes.
//
//   - These classes of bugs are by nature invisible to black-box
//     testing. They require either: (a) line-by-line audit of the
//     native/pasta/ field arithmetic against a reference implementation
//     (e.g. the Rust pasta_curves crate), (b) formal verification of
//     the Montgomery multiplication and reduction routines, or
//     (c) cross-implementation differential testing where the same
//     inputs are fed to curvey and an independent Pallas implementation
//     and every intermediate value is compared.
//   - If this system handles real votes or funds, a formal audit of
//     native/pasta/ field arithmetic is recommended before production.
package elgamal
