pub mod hasher;
pub mod proof;
pub mod tree;
pub use proof::*;
pub use tree::*;

#[cfg(test)]
pub(crate) mod test_helpers;

use pasta_curves::Fp;

/// Convenience wrapper: Poseidon hash of two field elements.
///
/// This is the same hash used for leaf commitments (`hash(low, width)`) and
/// internal Merkle nodes (`hash(left, right)`).
pub fn poseidon_hash(left: Fp, right: Fp) -> Fp {
    hasher::PoseidonHasher::new().hash(left, right)
}
