//! IMT (Indexed Merkle Tree) utilities for the delegation proof system.
//!
//! Provides out-of-circuit helpers for building and verifying Poseidon-based
//! Indexed Merkle Tree non-membership proofs using the (low, high) leaf model.
//! Each leaf stores a (low, high) pair defining an interval; the leaf hash is
//! Poseidon(low, high), then a standard Merkle path authenticates the leaf.
//! A non-membership proof shows that a nullifier falls within the interval.
//! Used by the delegation circuit and builder.

use alloc::string::String;
use ff::PrimeField;
use halo2_gadgets::poseidon::primitives::{self as poseidon, ConstantLength};
use pasta_curves::pallas;

/// Depth of the nullifier Indexed Merkle Tree Merkle path (Poseidon-based).
/// Total Poseidon calls per proof = 1 (leaf hash) + 29 (path) = 30.
pub const IMT_DEPTH: usize = 29;

/// Domain tag for governance authorization nullifier (per spec §1.3.2, condition 14).
///
/// `"governance authorization"` encoded as a little-endian Pallas field element.
pub(crate) fn gov_auth_domain_tag() -> pallas::Base {
    let mut bytes = [0u8; 32];
    bytes[..24].copy_from_slice(b"governance authorization");
    pallas::Base::from_repr(bytes).unwrap()
}

/// Compute Poseidon hash of two field elements (out of circuit).
pub(crate) fn poseidon_hash_2(a: pallas::Base, b: pallas::Base) -> pallas::Base {
    poseidon::Hash::<_, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([a, b])
}

/// Compute governance nullifier out-of-circuit (per spec §1.3.2, condition 14).
///
/// `gov_null = Poseidon(nk, Poseidon(domain_tag, Poseidon(vote_round_id, real_nf)))`
///
/// where `domain_tag` = `"governance authorization"` as a field element.
pub(crate) fn gov_null_hash(
    nk: pallas::Base,
    vote_round_id: pallas::Base,
    real_nf: pallas::Base,
) -> pallas::Base {
    let step1 = poseidon_hash_2(vote_round_id, real_nf);
    let step2 = poseidon_hash_2(gov_auth_domain_tag(), step1);
    poseidon_hash_2(nk, step2)
}

/// IMT non-membership proof data ((low, high) leaf model).
///
/// Each leaf stores an explicit (low, high) pair defining an interval.
/// The leaf hash is `Poseidon(low, high)`, followed by a standard
/// 29-level Merkle path to the root.
#[derive(Clone, Debug)]
pub struct ImtProofData {
    /// The Merkle root of the IMT.
    pub root: pallas::Base,
    /// Interval start (low bound of the bracketing leaf).
    pub low: pallas::Base,
    /// Interval end (high bound of the bracketing leaf).
    pub high: pallas::Base,
    /// Position of the leaf in the tree.
    pub leaf_pos: u32,
    /// Sibling hashes along the 29-level Merkle path (pure siblings).
    pub path: [pallas::Base; IMT_DEPTH],
}

/// Error type for IMT proof fetching failures.
#[derive(Clone, Debug)]
pub struct ImtError(pub String);

impl core::fmt::Display for ImtError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        write!(f, "IMT error: {}", self.0)
    }
}

/// Trait for providing IMT non-membership proofs.
///
/// Implementations must return proofs against a consistent root — all proofs
/// from the same provider must share the same `root()` value.
pub trait ImtProvider {
    /// The current IMT root.
    fn root(&self) -> pallas::Base;
    /// Generate a non-membership proof for the given nullifier.
    fn non_membership_proof(&self, nf: pallas::Base) -> Result<ImtProofData, ImtError>;
}

// ================================================================
// Test-only
// ================================================================

#[cfg(any(test, feature = "test-dependencies"))]
use alloc::vec::Vec;
#[cfg(any(test, feature = "test-dependencies"))]
use ff::Field;

/// Precomputed empty subtree hashes for the IMT (Poseidon-based).
///
/// `empty[0] = Poseidon(0, 0)` (hash of an empty (low=0, high=0) leaf),
/// `empty[i] = Poseidon(empty[i-1], empty[i-1])` for i >= 1.
#[cfg(any(test, feature = "test-dependencies"))]
pub(crate) fn empty_imt_hashes() -> Vec<pallas::Base> {
    let empty_leaf = poseidon_hash_2(pallas::Base::zero(), pallas::Base::zero());
    let mut hashes = vec![empty_leaf];
    for _ in 1..=IMT_DEPTH {
        let prev = *hashes.last().unwrap();
        hashes.push(poseidon_hash_2(prev, prev));
    }
    hashes
}

/// IMT provider with evenly-spaced brackets for testing ((low, high) leaf model).
///
/// Creates 17 brackets at intervals of 2^250, covering the entire Pallas field
/// (p ~= 16.something x 2^250). Each bracket k has low = k*step + 1 and
/// high = (k+1)*step - 1, stored as (low, high) leaves at positions 0..16 in
/// a 32-leaf subtree. Any hash-derived nullifier will fall within one bracket.
#[cfg(any(test, feature = "test-dependencies"))]
#[derive(Debug)]
pub struct SpacedLeafImtProvider {
    /// The root of the IMT.
    root: pallas::Base,
    /// Bracket data: `(low, high)` for each of the 17 brackets.
    leaves: Vec<(pallas::Base, pallas::Base)>,
    /// Bottom 5 levels of the 32-leaf subtree.
    /// `subtree_levels[0]` has 32 leaf hashes Poseidon(low, high),
    /// `subtree_levels[5]` has 1 subtree root.
    subtree_levels: Vec<Vec<pallas::Base>>,
}

#[cfg(any(test, feature = "test-dependencies"))]
impl SpacedLeafImtProvider {
    /// Create a new spaced-leaf IMT provider ((low, high) leaf model).
    ///
    /// Builds 17 brackets at positions 0..16 in a 32-leaf subtree:
    /// - Bracket k (k=0..15): low = k*step+1, high = (k+1)*step-1
    /// - Bracket 16: low = 16*step+1, high = p-1
    pub fn new() -> Self {
        let step = pallas::Base::from(2u64).pow([250, 0, 0, 0]);
        let empty = empty_imt_hashes();

        // Build 17 brackets.
        let mut leaves = Vec::with_capacity(17);
        for k in 0u64..17 {
            let low = step * pallas::Base::from(k) + pallas::Base::one();
            let high = if k < 16 {
                step * pallas::Base::from(k + 1) - pallas::Base::one()
            } else {
                -pallas::Base::one() // p - 1
            };
            leaves.push((low, high));
        }

        // Build 32-leaf subtree. Each leaf is Poseidon(low, high).
        let empty_leaf_hash = poseidon_hash_2(pallas::Base::zero(), pallas::Base::zero());
        let mut level0 = vec![empty_leaf_hash; 32];
        for (k, (low, high)) in leaves.iter().enumerate() {
            level0[k] = poseidon_hash_2(*low, *high);
        }

        let mut subtree_levels = vec![level0];
        for _l in 1..=5 {
            let prev = subtree_levels.last().unwrap();
            let mut current = Vec::with_capacity(prev.len() / 2);
            for j in 0..(prev.len() / 2) {
                current.push(poseidon_hash_2(prev[2 * j], prev[2 * j + 1]));
            }
            subtree_levels.push(current);
        }

        // Compute full root: hash subtree root up through levels 5..30 with empty siblings.
        let mut root = subtree_levels[5][0];
        for l in 5..IMT_DEPTH {
            root = poseidon_hash_2(root, empty[l]);
        }

        SpacedLeafImtProvider {
            root,
            leaves,
            subtree_levels,
        }
    }
}

#[cfg(any(test, feature = "test-dependencies"))]
impl ImtProvider for SpacedLeafImtProvider {
    fn root(&self) -> pallas::Base {
        self.root
    }

    fn non_membership_proof(&self, nf: pallas::Base) -> Result<ImtProofData, ImtError> {
        // Determine which bracket nf falls in: k = nf >> 250.
        // In the LE byte repr, bit 250 is bit 2 of byte 31.
        let repr = nf.to_repr();
        let k = (repr.as_ref()[31] >> 2) as usize;
        let k = k.min(16); // clamp to valid range

        let (low, high) = self.leaves[k];
        let leaf_pos = k as u32;

        let empty = empty_imt_hashes();

        // Build Merkle path (29 pure siblings).
        let mut path = [pallas::Base::zero(); IMT_DEPTH];

        // Levels 0..4: siblings from the 32-leaf subtree.
        let mut idx = k;
        for l in 0..5 {
            let sibling_idx = idx ^ 1;
            path[l] = self.subtree_levels[l][sibling_idx];
            idx >>= 1;
        }

        // Levels 5..28: empty subtree hashes (all leaves beyond position 31 are empty).
        for l in 5..IMT_DEPTH {
            path[l] = empty[l];
        }

        Ok(ImtProofData {
            root: self.root,
            low,
            high,
            leaf_pos,
            path,
        })
    }
}
