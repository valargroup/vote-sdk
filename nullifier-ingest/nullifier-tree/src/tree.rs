use std::io::{Read, Write};
use std::path::Path;

use anyhow::Result;
use ff::PrimeField as _;
use halo2_gadgets::poseidon::primitives::{self as poseidon, ConstantLength, P128Pow5T3};
use pasta_curves::Fp;
use rusqlite::Connection;

/// Depth of the nullifier range Merkle tree.
///
/// Each on-chain nullifier produces approximately one gap range (n nullifiers
/// → n + 1 ranges). Zcash mainnet currently has under 64M Orchard nullifiers.
/// We plan for this circuit to support up to 256M nullifiers, so the tree
/// needs capacity for ~2^28 leaves: `log2(256 << 20) + 1 = 29`.
pub const TREE_DEPTH: usize = 29;

/// A gap range `[low, high]` representing an inclusive interval between two
/// adjacent on-chain nullifiers. Each leaf in the Merkle tree commits to one
/// range via `hash(low, high)`.
///
/// **Exclusion proof**: to prove a value `x` is not a nullifier, the prover
/// reveals a range `[low, high]` where `low <= x <= high` plus a Merkle path
/// proving that range is committed in the tree.
///
/// Every on-chain nullifier `n` acts as a boundary between two adjacent ranges:
/// the range before it has `high = n - 1` and the range after has `low = n + 1`.
/// Because the bounds are `n ± 1`, the nullifier `n` itself falls outside every
/// range — so `low <= x <= high` can only succeed for non-nullifier values.
///
/// Example with sorted nullifiers `[n1, n2]`:
/// ```text
///   Range 0: [0,    n1-1]   ← gap before n1
///   Range 1: [n1+1, n2-1]   ← gap between n1 and n2
///   Range 2: [n2+1, MAX ]   ← gap after n2
/// ```
/// `n1` is the boundary of ranges 0 and 1; `n2` is the boundary of ranges 1
/// and 2. Neither `n1` nor `n2` is contained in any range.
///
/// ## Tree structure and padding
///
/// The tree has a fixed depth of [`TREE_DEPTH`]. With `n` on-chain nullifiers
/// the tree contains `n + 1` populated leaves. The remaining `2^TREE_DEPTH -
/// (n + 1)` leaf slots are empty.
///
/// Empty slots are filled with a sentinel value `Fp::from(2)`. At each level
/// of the tree, the empty hash is computed by self-hashing the level below:
/// `empty[0] = 2`, `empty[i+1] = hash(empty[i], empty[i])`. Any
/// subtree consisting entirely of empty leaves collapses to the empty hash for
/// that level. Odd-length layers are padded with the empty hash before hashing
/// up to the next level.
///
/// This means the root is deterministic for a given set of nullifiers
/// regardless of the tree capacity — adding more empty slots doesn't change
/// the root because they all reduce to the same empty subtree hashes.
pub type Range = [Fp; 2];

/// Load all nullifiers from the database, sort them, and build the gap ranges.
pub fn list_nf_ranges(connection: &Connection) -> Result<Vec<Range>> {
    let mut s = connection.prepare("SELECT nullifier FROM nullifiers")?;
    let rows = s.query_map([], |r| {
        let v = r.get::<_, [u8; 32]>(0)?;
        let v = Fp::from_repr(v).unwrap();
        Ok(v)
    })?;
    let mut nfs = rows.collect::<Result<Vec<_>, _>>()?;
    nfs.sort();
    Ok(build_nf_ranges(nfs))
}

/// Compute the Merkle root over the nullifier gap-range tree.
pub fn compute_nf_root(connection: &Connection) -> Result<Fp> {
    let ranges = list_nf_ranges(connection)?;
    let leaves = commit_ranges(&ranges);
    let empty = precompute_empty_hashes();
    let (root, _) = build_levels(&leaves, &empty);
    Ok(root)
}

/// Build gap ranges from a sorted nullifier set.
///
/// For each consecutive pair of nullifiers, the gap `[prev, nf - 1]` is emitted.
/// A final range `[last_nf + 1, Fp::MAX]` closes the space.
pub fn build_nf_ranges(nfs: impl IntoIterator<Item = Fp>) -> Vec<Range> {
    let mut prev = Fp::zero();
    let mut ranges = vec![];
    for r in nfs {
        if prev < r {
            ranges.push([prev, r - Fp::one()]);
        }
        prev = r + Fp::one();
    }
    if prev != Fp::zero() {
        ranges.push([prev, Fp::one().neg()]);
    }
    ranges
}

/// Hash two field elements using Poseidon with the P128Pow5T3 spec.
///
/// This is the single hash primitive used throughout the tree: leaf
/// commitments (`hash(low, high)`) and internal node combines
/// (`hash(left_child, right_child)`) both call this function.
///
/// Unlike the previous Sinsemilla-based hash, Poseidon does not use
/// level-based domain separation — the same function is applied at
/// every tree level. This is the standard approach for Poseidon
/// Merkle trees (Semaphore, Tornado Cash, etc.).
pub fn poseidon_hash(left: Fp, right: Fp) -> Fp {
    poseidon::Hash::<_, P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([left, right])
}

/// Hash each `(low, high)` range pair into a single leaf commitment.
pub fn commit_ranges(ranges: &[Range]) -> Vec<Fp> {
    ranges
        .iter()
        .map(|[low, high]| poseidon_hash(*low, *high))
        .collect()
}

/// Pre-compute the empty subtree hash at each tree level.
///
/// `empty[0]` is the sentinel empty leaf value `Fp::from(2)`.
/// `empty[i]` is the hash of a fully-empty subtree of height `i`, computed as
/// `poseidon_hash(empty[i-1], empty[i-1])`.
///
/// These are used during tree construction and proof generation to represent
/// the hash of any subtree that contains no populated leaves, avoiding the
/// need to recompute them on every call.
pub fn precompute_empty_hashes() -> [Fp; TREE_DEPTH] {
    let mut empty = [Fp::default(); TREE_DEPTH];
    empty[0] = Fp::from(2u64);
    for i in 1..TREE_DEPTH {
        empty[i] = poseidon_hash(empty[i - 1], empty[i - 1]);
    }
    empty
}

/// Build the Merkle tree bottom-up, retaining all intermediate levels.
///
/// Returns `(root, levels)` where `levels[i]` contains the node hashes at
/// tree level `i` (level 0 = padded leaf hashes). Each level is padded to
/// even length using the pre-computed empty hash for that level so that
/// pair-wise hashing produces the next level cleanly.
///
/// This uses [`TREE_DEPTH`] levels and retains every intermediate layer so
/// that Merkle auth paths can be extracted in O([`TREE_DEPTH`]) via simple
/// sibling lookups.
fn build_levels(leaves: &[Fp], empty: &[Fp; TREE_DEPTH]) -> (Fp, Vec<Vec<Fp>>) {
    let mut levels: Vec<Vec<Fp>> = Vec::with_capacity(TREE_DEPTH);

    // Level 0 = leaf commitments, padded to even length.
    let mut layer = leaves.to_vec();
    if layer.is_empty() {
        layer.push(empty[0]);
    }
    if layer.len() & 1 == 1 {
        layer.push(empty[0]);
    }
    levels.push(layer);

    // Hash pairs at each level to produce the next.
    for i in 0..TREE_DEPTH - 1 {
        let prev = &levels[i];
        let pairs = prev.len() / 2;
        let mut next = Vec::with_capacity(pairs + 1);
        for j in 0..pairs {
            next.push(poseidon_hash(prev[j * 2], prev[j * 2 + 1]));
        }
        if next.len() & 1 == 1 {
            next.push(empty[i + 1]);
        }
        levels.push(next);
    }

    // The final level has exactly two nodes; hash them to get the root.
    let top = &levels[TREE_DEPTH - 1];
    let root = poseidon_hash(top[0], top[1]);

    (root, levels)
}

/// Find the gap-range index that contains `value`.
///
/// Returns `Some(i)` where `ranges[i]` is `[low, high]` (inclusive),
/// or `None` if the value is an existing nullifier.
///
/// Uses binary search (`partition_point`) on the sorted, non-overlapping
/// ranges for O(log n) lookup instead of a linear scan.
pub fn find_range_for_value(ranges: &[Range], value: Fp) -> Option<usize> {
    // Find the first range whose `low` is greater than `value`.
    // All ranges before that index have `low <= value`.
    let i = ranges.partition_point(|[low, _]| *low <= value);
    if i == 0 {
        return None;
    }
    let idx = i - 1;
    let [low, high] = ranges[idx];
    if value >= low && value <= high {
        Some(idx)
    } else {
        None
    }
}

/// Serialize gap ranges to a binary file.
///
/// Format: `[8-byte LE count][count × 2 × 32-byte Fp representations]`
pub fn save_tree(path: &Path, ranges: &[Range]) -> Result<()> {
    let mut f = std::fs::File::create(path)?;
    let count = ranges.len() as u64;
    f.write_all(&count.to_le_bytes())?;
    for [low, high] in ranges {
        f.write_all(&low.to_repr())?;
        f.write_all(&high.to_repr())?;
    }
    Ok(())
}

/// Deserialize gap ranges from a binary file written by [`save_tree`].
pub fn load_tree(path: &Path) -> Result<Vec<Range>> {
    let mut f = std::fs::File::open(path)?;
    let mut buf8 = [0u8; 8];
    f.read_exact(&mut buf8)?;
    let count = u64::from_le_bytes(buf8) as usize;
    let mut ranges = Vec::with_capacity(count);
    let mut buf32 = [0u8; 32];
    for _ in 0..count {
        let mut pair = [Fp::zero(); 2];
        for fp in pair.iter_mut() {
            f.read_exact(&mut buf32)?;
            let v: Option<Fp> = Fp::from_repr(buf32).into();
            *fp = v.ok_or_else(|| anyhow::anyhow!("invalid Fp representation in tree file"))?;
        }
        ranges.push(pair);
    }
    Ok(ranges)
}

/// A nullifier non-inclusion tree built from on-chain nullifiers.
///
/// Constructed from a set of nullifier field elements, this struct computes
/// gap ranges between consecutive nullifiers and commits each range as a
/// Merkle leaf. The resulting fixed-depth tree supports exclusion proofs:
/// given a value, [`prove`](NullifierTree::prove) produces proof data
/// showing the value is not a nullifier.
///
/// All intermediate hash levels are pre-computed and retained so that
/// generating a Merkle authentication path is O([`TREE_DEPTH`]) — a simple
/// sibling lookup at each level — instead of rebuilding the entire tree.
pub struct NullifierTree {
    ranges: Vec<Range>,
    /// `levels[i]` holds the node hashes at tree level `i`.
    /// Level 0 contains the leaf commitments (padded to even length).
    levels: Vec<Vec<Fp>>,
    /// Pre-computed empty subtree hashes for each level.
    empty_hashes: [Fp; TREE_DEPTH],
    root: Fp,
}

/// An exclusion proof demonstrating that a value is not in the nullifier set.
///
/// Contains the gap range `[low, high]` that includes the value, the leaf
/// commitment, and a Merkle authentication path proving the range is
/// committed in the tree.
pub struct ExclusionProof {
    /// The gap range containing the proven value (`low <= value <= high`).
    pub range: Range,
    /// Leaf index in the Merkle tree.
    pub position: u32,
    /// The leaf commitment `hash(low, high)`.
    pub leaf: Fp,
    /// Merkle authentication path (sibling hashes from leaf to root).
    pub auth_path: Vec<Fp>,
}

impl NullifierTree {
    /// Build a tree from an iterator of nullifier field elements.
    ///
    /// The nullifiers need not be sorted — they are sorted internally.
    pub fn build(nfs: impl IntoIterator<Item = Fp>) -> Self {
        let mut nfs: Vec<Fp> = nfs.into_iter().collect();
        nfs.sort();
        let ranges = build_nf_ranges(nfs);
        Self::from_ranges(ranges)
    }

    /// Build a tree from nullifiers stored in the database.
    pub fn from_db(connection: &Connection) -> Result<Self> {
        let ranges = list_nf_ranges(connection)?;
        Ok(Self::from_ranges(ranges))
    }

    /// Load a tree from a binary file written by [`save`](NullifierTree::save).
    pub fn load(path: &Path) -> Result<Self> {
        let ranges = load_tree(path)?;
        Ok(Self::from_ranges(ranges))
    }

    /// Build a tree from pre-computed gap ranges.
    fn from_ranges(ranges: Vec<Range>) -> Self {
        let leaves = commit_ranges(&ranges);
        let empty_hashes = precompute_empty_hashes();
        let (root, levels) = build_levels(&leaves, &empty_hashes);
        Self { ranges, levels, empty_hashes, root }
    }

    /// The Merkle root of the tree as an `Fp`.
    pub fn root(&self) -> Fp {
        self.root
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

    /// The leaf commitment hashes (level 0 of the tree).
    ///
    /// Returns only the populated leaves, excluding any padding element
    /// that was added for even-length pairing.
    pub fn leaves(&self) -> &[Fp] {
        &self.levels[0][..self.ranges.len()]
    }

    /// Generate an exclusion proof for `value`.
    ///
    /// Returns `Some(proof)` if `value` falls within a gap range (i.e., is
    /// not a nullifier), or `None` if `value` is an existing nullifier.
    ///
    /// This is O([`TREE_DEPTH`]) — it walks the pre-computed levels collecting
    /// sibling hashes rather than rebuilding the entire tree.
    pub fn prove(&self, value: Fp) -> Option<ExclusionProof> {
        let idx = find_range_for_value(&self.ranges, value)?;
        let mut auth_path = Vec::with_capacity(TREE_DEPTH);
        let mut pos = idx;
        for level in 0..TREE_DEPTH {
            let sibling = pos ^ 1;
            let hash = if sibling < self.levels[level].len() {
                self.levels[level][sibling]
            } else {
                self.empty_hashes[level]
            };
            auth_path.push(hash);
            pos >>= 1;
        }
        Some(ExclusionProof {
            range: self.ranges[idx],
            position: idx as u32,
            leaf: self.levels[0][idx],
            auth_path,
        })
    }

    /// Serialize the tree's ranges to a binary file.
    pub fn save(&self, path: &Path) -> Result<()> {
        save_tree(path, &self.ranges)
    }
}

impl ExclusionProof {
    /// Verify the proof against a known Merkle root.
    ///
    /// Checks that `value` is within the gap range and that the
    /// authentication path recomputes to `root`.
    pub fn verify(&self, value: Fp, root: Fp) -> bool {
        let [low, high] = self.range;
        if value < low || value > high {
            return false;
        }
        // Recompute the leaf commitment
        let expected_leaf = poseidon_hash(low, high);
        if self.leaf != expected_leaf {
            return false;
        }
        // Walk the auth path from leaf to root
        let mut current = self.leaf;
        let mut pos = self.position;
        for sibling in self.auth_path.iter() {
            let (l, r) = if pos & 1 == 0 {
                (current, *sibling)
            } else {
                (*sibling, current)
            };
            current = poseidon_hash(l, r);
            pos >>= 1;
        }
        current == root
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Helper: make an Fp from a u64.
    fn fp(v: u64) -> Fp {
        Fp::from(v)
    }

    // 4 nullifiers: 10, 20, 30, 40
    // Expected 5 gap ranges:
    //   [0, 9]    [11, 19]    [21, 29]    [31, 39]    [41, MAX]

    fn four_nullifiers() -> Vec<Fp> {
        vec![fp(10), fp(20), fp(30), fp(40)]
    }

    #[test]
    fn test_build_ranges_from_4_nullifiers() {
        let ranges = build_nf_ranges(four_nullifiers());
        assert_eq!(ranges.len(), 5);

        assert_eq!(ranges[0], [fp(0), fp(9)]);
        assert_eq!(ranges[1], [fp(11), fp(19)]);
        assert_eq!(ranges[2], [fp(21), fp(29)]);
        assert_eq!(ranges[3], [fp(31), fp(39)]);
        // Last range: [41, Fp::MAX]
        assert_eq!(ranges[4][0], fp(41));
        assert_eq!(ranges[4][1], Fp::one().neg());
    }

    #[test]
    fn test_nullifiers_not_in_any_range() {
        let ranges = build_nf_ranges(four_nullifiers());
        for &nf in &four_nullifiers() {
            assert!(
                find_range_for_value(&ranges, nf).is_none(),
                "nullifier {:?} should not be in any gap range",
                nf
            );
        }
    }

    #[test]
    fn test_non_nullifiers_found_in_ranges() {
        let ranges = build_nf_ranges(four_nullifiers());

        // Values in each gap
        assert_eq!(find_range_for_value(&ranges, fp(0)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(5)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(9)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(11)), Some(1));
        assert_eq!(find_range_for_value(&ranges, fp(15)), Some(1));
        assert_eq!(find_range_for_value(&ranges, fp(25)), Some(2));
        assert_eq!(find_range_for_value(&ranges, fp(35)), Some(3));
        assert_eq!(find_range_for_value(&ranges, fp(41)), Some(4));
        assert_eq!(find_range_for_value(&ranges, fp(1000)), Some(4));
    }

    #[test]
    fn test_merkle_root_is_deterministic() {
        let tree1 = NullifierTree::build(four_nullifiers());
        let tree2 = NullifierTree::build(four_nullifiers());
        assert_eq!(tree1.root(), tree2.root());
    }

    #[test]
    fn test_merkle_paths_verify_for_each_range() {
        let tree = NullifierTree::build(four_nullifiers());
        let root = tree.root();

        // Verify an exclusion proof for a value in every range
        let test_values = [fp(5), fp(15), fp(25), fp(35), fp(41)];
        for (i, &value) in test_values.iter().enumerate() {
            let proof = tree.prove(value).expect("should produce proof");
            assert_eq!(proof.position, i as u32);
            assert!(
                proof.verify(value, root),
                "exclusion proof for range {} does not verify",
                i
            );
        }
    }

    #[test]
    fn test_exclusion_proof_end_to_end() {
        let tree = NullifierTree::build(four_nullifiers());
        let root = tree.root();

        // Prove that 15 is not a nullifier
        let value = fp(15);
        let proof = tree.prove(value).expect("should produce proof");
        assert_eq!(proof.position, 1); // range [11, 19]

        let [low, high] = proof.range;
        assert_eq!(low, fp(11));
        assert_eq!(high, fp(19));
        assert!(value >= low && value <= high);
        assert!(proof.verify(value, root));
    }

    #[test]
    fn test_proof_verify_rejects_wrong_value() {
        let tree = NullifierTree::build(four_nullifiers());
        let root = tree.root();

        let proof = tree.prove(fp(15)).unwrap();
        // A value outside the range should fail verification
        assert!(!proof.verify(fp(5), root));
        // A nullifier itself should also fail (outside range bounds)
        assert!(!proof.verify(fp(10), root));
    }

    #[test]
    fn test_proof_verify_rejects_wrong_root() {
        let tree = NullifierTree::build(four_nullifiers());

        let proof = tree.prove(fp(15)).unwrap();
        assert!(!proof.verify(fp(15), Fp::zero()));
    }

    #[test]
    fn test_nullifier_has_no_proof() {
        let tree = NullifierTree::build(four_nullifiers());
        for &nf in &four_nullifiers() {
            assert!(
                tree.prove(nf).is_none(),
                "nullifier {:?} should not have an exclusion proof",
                nf
            );
        }
    }

    #[test]
    fn test_tree_len() {
        let tree = NullifierTree::build(four_nullifiers());
        assert_eq!(tree.len(), 5);
        assert!(!tree.is_empty());
    }

    #[test]
    fn test_save_load_round_trip() {
        let tree = NullifierTree::build(four_nullifiers());
        let dir = std::env::temp_dir().join("nullifier_tree_test");
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("ranges.bin");

        tree.save(&path).unwrap();
        let loaded = NullifierTree::load(&path).unwrap();
        assert_eq!(tree.root(), loaded.root());
        assert_eq!(tree.ranges(), loaded.ranges());

        std::fs::remove_file(&path).unwrap();
    }

    #[test]
    fn test_unsorted_input_produces_same_tree() {
        let sorted = NullifierTree::build(four_nullifiers());
        let unsorted = NullifierTree::build(vec![fp(30), fp(10), fp(40), fp(20)]);
        assert_eq!(sorted.root(), unsorted.root());
    }

    #[test]
    fn test_precompute_empty_hashes_chain() {
        let empty = precompute_empty_hashes();

        // Level 0 is the sentinel empty leaf.
        assert_eq!(empty[0], Fp::from(2u64));

        // Each subsequent level is the self-hash of the level below.
        for i in 1..TREE_DEPTH {
            let expected = poseidon_hash(empty[i - 1], empty[i - 1]);
            assert_eq!(
                empty[i], expected,
                "empty hash mismatch at level {}",
                i
            );
        }
    }

    #[test]
    fn test_build_levels_consistency() {
        let tree = NullifierTree::build(four_nullifiers());

        // Verify that each level is correctly derived from the level below.
        for i in 0..TREE_DEPTH - 1 {
            let prev = &tree.levels[i];
            let next = &tree.levels[i + 1];
            let pairs = prev.len() / 2;
            for j in 0..pairs {
                let expected = poseidon_hash(prev[j * 2], prev[j * 2 + 1]);
                assert_eq!(
                    next[j], expected,
                    "level {} node {} does not match hash of level {} children",
                    i + 1, j, i
                );
            }
        }

        // Root should be the hash of the top-level pair.
        let top = &tree.levels[TREE_DEPTH - 1];
        let expected_root = poseidon_hash(top[0], top[1]);
        assert_eq!(tree.root(), expected_root);
    }

    #[test]
    fn test_leaves_accessor() {
        let tree = NullifierTree::build(four_nullifiers());
        let leaves = tree.leaves();
        assert_eq!(leaves.len(), 5); // 4 nullifiers → 5 ranges
        // Verify leaves match commit_ranges output.
        let expected = commit_ranges(tree.ranges());
        assert_eq!(leaves, expected.as_slice());
    }

    #[test]
    fn test_find_range_empty_ranges() {
        let ranges: Vec<Range> = vec![];
        assert_eq!(find_range_for_value(&ranges, fp(0)), None);
        assert_eq!(find_range_for_value(&ranges, fp(42)), None);
    }

    #[test]
    fn test_find_range_single_range() {
        // Single nullifier at 100 produces two ranges: [0, 99] and [101, MAX]
        let ranges = build_nf_ranges(vec![fp(100)]);
        assert_eq!(ranges.len(), 2);

        assert_eq!(find_range_for_value(&ranges, fp(0)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(99)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(100)), None); // nullifier
        assert_eq!(find_range_for_value(&ranges, fp(101)), Some(1));
        assert_eq!(find_range_for_value(&ranges, fp(999)), Some(1));
    }

    #[test]
    fn test_find_range_exact_boundaries() {
        let ranges = build_nf_ranges(four_nullifiers());
        // Exact low boundaries
        assert_eq!(find_range_for_value(&ranges, fp(0)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(11)), Some(1));
        assert_eq!(find_range_for_value(&ranges, fp(21)), Some(2));
        assert_eq!(find_range_for_value(&ranges, fp(31)), Some(3));
        assert_eq!(find_range_for_value(&ranges, fp(41)), Some(4));

        // Exact high boundaries
        assert_eq!(find_range_for_value(&ranges, fp(9)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(19)), Some(1));
        assert_eq!(find_range_for_value(&ranges, fp(29)), Some(2));
        assert_eq!(find_range_for_value(&ranges, fp(39)), Some(3));
    }

    #[test]
    fn test_find_range_consecutive_nullifiers() {
        // Consecutive nullifiers: 10, 11, 12 produce no gap between them
        let ranges = build_nf_ranges(vec![fp(10), fp(11), fp(12)]);
        // [0, 9], [13, MAX]
        assert_eq!(ranges.len(), 2);

        assert_eq!(find_range_for_value(&ranges, fp(5)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(9)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(10)), None);
        assert_eq!(find_range_for_value(&ranges, fp(11)), None);
        assert_eq!(find_range_for_value(&ranges, fp(12)), None);
        assert_eq!(find_range_for_value(&ranges, fp(13)), Some(1));
    }

    #[test]
    fn test_find_range_binary_search_large_set() {
        // Build a large set of evenly-spaced nullifiers and verify the binary
        // search returns the same results as a naive linear scan.
        let nullifiers: Vec<Fp> = (0..10_000u64).map(|i| fp(i * 3 + 1)).collect();
        let ranges = build_nf_ranges(nullifiers.clone());

        // Verify every nullifier is excluded
        for nf in &nullifiers {
            assert!(find_range_for_value(&ranges, *nf).is_none());
        }

        // Verify mid-gap values are found in the correct range
        for (i, window) in nullifiers.windows(2).enumerate() {
            let mid = window[0] + Fp::one(); // one above the nullifier
            let result = find_range_for_value(&ranges, mid);
            assert!(
                result.is_some(),
                "mid-gap value between nf[{}] and nf[{}] not found",
                i,
                i + 1
            );
            let idx = result.unwrap();
            let [low, high] = ranges[idx];
            assert!(
                mid >= low && mid <= high,
                "value not within returned range at index {}",
                idx
            );
        }
    }

    #[test]
    fn test_find_range_agrees_with_linear_scan() {
        // Reference linear implementation for cross-checking
        fn linear_find(ranges: &[Range], value: Fp) -> Option<usize> {
            for (i, [low, high]) in ranges.iter().enumerate() {
                if value >= *low && value <= *high {
                    return Some(i);
                }
            }
            None
        }

        let nullifiers: Vec<Fp> = (0..500u64).map(|i| fp(i * 7 + 3)).collect();
        let ranges = build_nf_ranges(nullifiers);

        // Test a sweep of values including nullifiers, boundaries, and gaps
        for v in 0..4000u64 {
            let val = fp(v);
            assert_eq!(
                find_range_for_value(&ranges, val),
                linear_find(&ranges, val),
                "disagreement at value {}",
                v
            );
        }
    }
}
