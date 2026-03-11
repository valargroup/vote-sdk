//! Shared test utilities for the imt-tree crate.
//!
//! Provides common constructors used across `tree::tests` and `proof::tests`
//! to avoid duplicating trivial helpers in every test module.

use pasta_curves::Fp;

/// Construct an `Fp` from a `u64` literal.
pub fn fp(v: u64) -> Fp {
    Fp::from(v)
}

/// Return the canonical four-nullifier test set `[10, 20, 30, 40]`.
///
/// Produces five gap ranges:
///   `[0,9]  [11,8]  [21,8]  [31,8]  [41, MAX-41]`
pub fn four_nullifiers() -> Vec<Fp> {
    vec![fp(10), fp(20), fp(30), fp(40)]
}
