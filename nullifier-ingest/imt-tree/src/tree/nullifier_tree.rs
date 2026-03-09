use std::path::Path;
use std::time::Instant;

use anyhow::Result;
use ff::{Field, PrimeField as _};
use pasta_curves::Fp;

use super::{
    build_levels, build_nf_ranges, commit_ranges, find_range_for_value, load_full_tree, load_tree,
    precompute_empty_hashes, save_full_tree, save_tree, ImtProofData, Range, TREE_DEPTH,
};

/// A nullifier non-inclusion tree built from on-chain nullifiers.
///
/// Constructed from a set of nullifier field elements, this struct computes
/// gap ranges between consecutive nullifiers and commits each range as a
/// Merkle leaf. The resulting fixed-depth tree supports exclusion proofs:
/// given a value, [`prove`](NullifierTree::prove) produces proof data
/// showing the value is not a nullifier.
///
/// All intermediate hash levels are pre-computed and retained so that
/// generating a Merkle authentication path is O([`TREE_DEPTH`]) -- a simple
/// sibling lookup at each level -- instead of rebuilding the entire tree.
pub struct NullifierTree {
    ranges: Vec<Range>,
    /// `levels[i]` holds the node hashes at tree level `i`.
    /// Level 0 contains the leaf commitments (padded to even length).
    pub(crate) levels: Vec<Vec<Fp>>,
    /// Pre-computed empty subtree hashes for each level.
    empty_hashes: [Fp; TREE_DEPTH],
    root: Fp,
    /// The block height the tree was built from (if known).
    height: Option<u64>,
}

impl NullifierTree {
    /// Build a tree from an iterator of nullifier field elements.
    ///
    /// The nullifiers need not be sorted -- they are sorted internally.
    ///
    /// **Not public** -- use [`build_sentinel_tree`] to construct a tree
    /// with the sentinel invariant required by the delegation circuit.
    pub(crate) fn build(nfs: impl IntoIterator<Item = Fp>) -> Self {
        let mut nfs: Vec<Fp> = nfs.into_iter().collect();
        nfs.sort();
        let ranges = build_nf_ranges(nfs);
        Self::from_ranges(ranges)
    }

    /// Load a tree from a binary file written by [`save`](NullifierTree::save).
    pub fn load(path: &Path) -> Result<Self> {
        let ranges = load_tree(path)?;
        Ok(Self::from_ranges(ranges))
    }

    /// Build a tree from pre-computed gap ranges.
    ///
    /// **Not public** -- use [`build_sentinel_tree`] to construct a tree
    /// with the sentinel invariant required by the delegation circuit.
    pub(crate) fn from_ranges(ranges: Vec<Range>) -> Self {
        let t0 = Instant::now();
        let leaves = commit_ranges(&ranges);
        eprintln!("  Leaf hashing: {} leaves in {:.1}s", leaves.len(), t0.elapsed().as_secs_f64());

        let empty_hashes = precompute_empty_hashes();

        let t1 = Instant::now();
        let (root, levels) = build_levels(leaves, &empty_hashes, TREE_DEPTH);
        eprintln!("  Tree build ({} levels): {:.1}s", levels.len(), t1.elapsed().as_secs_f64());

        Self { ranges, levels, empty_hashes, root, height: None }
    }

    /// The Merkle root of the tree as an `Fp`.
    pub fn root(&self) -> Fp {
        self.root
    }

    /// The block height the tree was built from, if known.
    pub fn height(&self) -> Option<u64> {
        self.height
    }

    /// Set the block height the tree was built from.
    pub fn set_height(&mut self, height: u64) {
        self.height = Some(height);
    }

    /// The gap ranges committed in the tree.
    pub fn ranges(&self) -> &[Range] {
        &self.ranges
    }

    /// The number of gap ranges (leaves) in the tree.
    pub fn len(&self) -> usize {
        self.ranges.len()
    }

    /// Whether the tree has no ranges.
    pub fn is_empty(&self) -> bool {
        self.ranges.is_empty()
    }

    /// Verify that every gap range has width `< 2^250`.
    ///
    /// The delegation circuit's `q_interval` gate range-checks interval
    /// widths to 250 bits. If any range exceeds this bound, proofs built
    /// from this tree will silently fail the circuit check.
    ///
    /// In the `(low, width)` leaf model, `width` is stored directly in
    /// `range[1]`, so no subtraction is needed.
    ///
    /// Called automatically by [`build_sentinel_tree`]; you only need to
    /// call this directly if you are loading a tree from an untrusted source.
    pub fn verify_range_widths(&self) -> Result<()> {
        for (i, &[low, width]) in self.ranges.iter().enumerate() {
            // A value fits in 250 bits iff its big-endian byte 31 (the MSB
            // of the little-endian repr) has its top two bits clear (< 0x04).
            // Equivalently: the 256-bit repr must be < 2^250.
            let repr = width.to_repr();
            anyhow::ensure!(
                repr.as_ref()[31] < 0x04,
                "range {i} has width >= 2^250: low={low:?}, width={width:?}"
            );
        }
        Ok(())
    }

    /// The leaf commitment hashes (level 0 of the tree).
    ///
    /// Returns only the populated leaves, excluding any padding element
    /// that was added for even-length pairing.
    pub fn leaves(&self) -> &[Fp] {
        &self.levels[0][..self.ranges.len()]
    }

    /// Generate a non-membership proof for `value`.
    ///
    /// Returns `Some(proof)` if `value` falls within a gap range (i.e., is
    /// not a nullifier), or `None` if `value` is an existing nullifier.
    ///
    /// The returned [`ImtProofData`] can be fed directly to the delegation
    /// circuit's condition 13 (IMT non-membership verification).
    ///
    /// This is O([`TREE_DEPTH`]) -- it walks the pre-computed levels collecting
    /// sibling hashes rather than rebuilding the entire tree.
    pub fn prove(&self, value: Fp) -> Option<ImtProofData> {
        let idx = find_range_for_value(&self.ranges, value)?;
        let mut path = [Fp::zero(); TREE_DEPTH];
        let mut pos = idx;
        for level in 0..TREE_DEPTH {
            let sibling = pos ^ 1;
            path[level] = if sibling < self.levels[level].len() {
                self.levels[level][sibling]
            } else {
                self.empty_hashes[level]
            };
            pos >>= 1;
        }
        let [low, width] = self.ranges[idx];
        Some(ImtProofData {
            root: self.root,
            low,
            width,
            leaf_pos: idx as u32,
            path,
        })
    }

    /// Serialize the tree's ranges to a binary file.
    pub fn save(&self, path: &Path) -> Result<()> {
        save_tree(path, &self.ranges)
    }

    /// Serialize the full tree (ranges + all levels + root) to a binary file.
    ///
    /// On reload via [`load_full`](NullifierTree::load_full), zero hashing is
    /// required -- startup goes from minutes to seconds.
    pub fn save_full(&self, path: &Path) -> Result<()> {
        save_full_tree(path, &self.ranges, &self.levels, self.root, self.height)
    }

    /// Load a full tree from a binary file written by [`save_full`](NullifierTree::save_full).
    ///
    /// Zero hashing -- all data is read directly from the file.
    pub fn load_full(path: &Path) -> Result<Self> {
        let (ranges, levels, root, height) = load_full_tree(path)?;
        let empty_hashes = precompute_empty_hashes();
        Ok(Self { ranges, levels, empty_hashes, root, height })
    }
}

/// Build a [`NullifierTree`] pre-seeded with sentinel nullifiers at 2^250
/// boundaries to ensure all gap ranges satisfy the circuit's `< 2^250`
/// width constraint.
///
/// The sentinel nullifiers are placed at `k * 2^250` for `k = 0..=16`,
/// partitioning the Pallas field into 17 intervals each under 2^250 wide.
/// Any additional nullifiers from `extra` are merged in.
///
/// This is the **only public constructor** for building a new tree. The
/// sentinel invariant is verified before returning, so callers are
/// guaranteed every gap range fits in 250 bits (as required by the
/// delegation circuit's condition 13).
pub fn build_sentinel_tree(extra: &[Fp]) -> Result<NullifierTree> {
    let step = Fp::from(2u64).pow([250, 0, 0, 0]);
    let mut nullifiers: Vec<Fp> = (0u64..=16).map(|k| step * Fp::from(k)).collect();
    nullifiers.extend_from_slice(extra);
    let tree = NullifierTree::build(nullifiers);
    tree.verify_range_widths()?;
    Ok(tree)
}
