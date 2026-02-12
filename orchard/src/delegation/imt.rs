//! IMT (Indexed Merkle Tree) utilities for the delegation proof system.
//!
//! Provides out-of-circuit helpers for building and verifying Poseidon-based
//! Indexed Merkle Tree non-membership proofs using the paired-leaf model.
//! Each leaf pair (nf_start, nf_end) at adjacent even/odd positions defines
//! an interval; a non-membership proof shows that a nullifier falls within
//! the interval. Used by the delegation circuit and builder.

use ff::PrimeField;
use halo2_gadgets::poseidon::primitives::{self as poseidon, ConstantLength};
use pasta_curves::pallas;

/// Depth of the nullifier Indexed Merkle Tree (Poseidon-based).
pub const IMT_DEPTH: usize = 32;

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

/// IMT non-membership proof data (paired-leaf model).
///
/// In this model, each pair of adjacent leaves at even/odd positions defines
/// an interval `[nf_start, nf_end]`. The Merkle path starts from the even leaf
/// (`nf_start`), and the odd sibling (`nf_end`) is `path[0]`.
#[derive(Clone, Debug)]
pub struct ImtProofData {
    /// The Merkle root of the IMT.
    pub root: pallas::Base,
    /// The even-position leaf (start of the bracketing interval).
    pub nf_start: pallas::Base,
    /// Position of the even leaf in the tree (must be even).
    pub leaf_pos: u32,
    /// Sibling hashes along the Merkle path. `path[0]` = nf_end (the odd sibling).
    pub path: [pallas::Base; IMT_DEPTH],
}

/// Trait for providing IMT non-membership proofs.
///
/// Implementations must return proofs against a consistent root — all proofs
/// from the same provider must share the same `root()` value.
pub trait ImtProvider {
    /// The current IMT root.
    fn root(&self) -> pallas::Base;
    /// Generate a non-membership proof for the given nullifier.
    fn non_membership_proof(&self, nf: pallas::Base) -> ImtProofData;
}

// ================================================================
// Test-only
// ================================================================

#[cfg(test)]
use ff::Field;

/// Precomputed empty subtree hashes for the IMT (Poseidon-based).
///
/// `empty[0] = 0` (raw leaf value), `empty[i] = Poseidon(empty[i-1], empty[i-1])` for i >= 1.
#[cfg(test)]
pub(crate) fn empty_imt_hashes() -> Vec<pallas::Base> {
    let mut hashes = vec![pallas::Base::zero()];
    for _ in 1..=IMT_DEPTH {
        let prev = *hashes.last().unwrap();
        hashes.push(poseidon_hash_2(prev, prev));
    }
    hashes
}

/// IMT provider with evenly-spaced brackets for testing (paired-leaf model).
///
/// Creates 17 brackets at intervals of 2^250, covering the entire Pallas field
/// (p ~= 16.something x 2^250). Each bracket k has nf_start = k*step + 1 and
/// nf_end = (k+1)*step - 1, placed as paired leaves at even/odd positions in
/// a 64-leaf subtree. Any hash-derived nullifier will fall within one bracket.
#[cfg(test)]
#[derive(Debug)]
pub struct SpacedLeafImtProvider {
    /// The root of the IMT.
    root: pallas::Base,
    /// Bracket data: `(nf_start, nf_end)` for each of the 17 brackets.
    leaves: Vec<(pallas::Base, pallas::Base)>,
    /// Bottom 6 levels of the 64-leaf subtree.
    /// `subtree_levels[0]` has 64 raw leaves, `subtree_levels[6]` has 1 subtree root.
    subtree_levels: Vec<Vec<pallas::Base>>,
}

#[cfg(test)]
impl SpacedLeafImtProvider {
    /// Create a new spaced-leaf IMT provider (paired-leaf model).
    ///
    /// Builds 17 brackets as 34 leaves at positions 0..33 in a 64-leaf subtree:
    /// - Bracket k (k=0..15): nf_start = k*step+1, nf_end = (k+1)*step-1
    /// - Bracket 16: nf_start = 16*step+1, nf_end = p-1
    pub fn new() -> Self {
        let step = pallas::Base::from(2u64).pow([250, 0, 0, 0]);
        let empty = empty_imt_hashes();

        // Build 17 brackets.
        let mut leaves = Vec::with_capacity(17);
        for k in 0u64..17 {
            let nf_start = step * pallas::Base::from(k) + pallas::Base::one();
            let nf_end = if k < 16 {
                step * pallas::Base::from(k + 1) - pallas::Base::one()
            } else {
                -pallas::Base::one() // p - 1
            };
            leaves.push((nf_start, nf_end));
        }

        // Build 64-position subtree. Paired leaves: position 2k = nf_start, 2k+1 = nf_end.
        let mut level0 = vec![empty[0]; 64];
        for (k, (nf_start, nf_end)) in leaves.iter().enumerate() {
            level0[2 * k] = *nf_start;
            level0[2 * k + 1] = *nf_end;
        }

        let mut subtree_levels = vec![level0];
        for _l in 1..=6 {
            let prev = subtree_levels.last().unwrap();
            let mut current = Vec::with_capacity(prev.len() / 2);
            for j in 0..(prev.len() / 2) {
                current.push(poseidon_hash_2(prev[2 * j], prev[2 * j + 1]));
            }
            subtree_levels.push(current);
        }

        // Compute full root: hash subtree root up through levels 6..31 with empty siblings.
        let mut root = subtree_levels[6][0];
        for l in 6..IMT_DEPTH {
            root = poseidon_hash_2(root, empty[l]);
        }

        SpacedLeafImtProvider {
            root,
            leaves,
            subtree_levels,
        }
    }
}

#[cfg(test)]
impl ImtProvider for SpacedLeafImtProvider {
    fn root(&self) -> pallas::Base {
        self.root
    }

    fn non_membership_proof(&self, nf: pallas::Base) -> ImtProofData {
        // Determine which bracket nf falls in: k = nf >> 250.
        // In the LE byte repr, bit 250 is bit 2 of byte 31.
        let repr = nf.to_repr();
        let k = (repr.as_ref()[31] >> 2) as usize;
        let k = k.min(16); // clamp to valid range

        let (nf_start, _nf_end) = self.leaves[k];
        let leaf_pos = (2 * k) as u32; // even position

        let empty = empty_imt_hashes();

        // Build Merkle path.
        let mut path = [pallas::Base::zero(); IMT_DEPTH];

        // Levels 0..5: siblings from the 64-leaf subtree.
        let mut idx = 2 * k;
        for l in 0..6 {
            let sibling_idx = idx ^ 1;
            path[l] = self.subtree_levels[l][sibling_idx];
            idx >>= 1;
        }

        // Levels 6..31: empty subtree hashes (all leaves beyond position 63 are empty).
        for l in 6..IMT_DEPTH {
            path[l] = empty[l];
        }

        ImtProofData {
            root: self.root,
            nf_start,
            leaf_pos,
            path,
        }
    }
}
