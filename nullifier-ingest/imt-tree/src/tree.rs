use std::io::{Read, Write};
use std::path::Path;

use anyhow::Result;
use ff::{Field, PrimeField as _};
use halo2_gadgets::poseidon::primitives::{self as poseidon, ConstantLength, P128Pow5T3, Spec};
use pasta_curves::Fp;

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
/// Empty slots are filled with `poseidon_hash(0, 0)` — the commitment of an
/// empty (low=0, high=0) leaf. At each level of the tree, the empty hash is
/// computed by self-hashing the level below:
/// `empty[0] = poseidon_hash(0, 0)`, `empty[i+1] = hash(empty[i], empty[i])`. Any
/// subtree consisting entirely of empty leaves collapses to the empty hash for
/// that level. Odd-length layers are padded with the empty hash before hashing
/// up to the next level.
///
/// This means the root is deterministic for a given set of nullifiers
/// regardless of the tree capacity — adding more empty slots doesn't change
/// the root because they all reduce to the same empty subtree hashes.
pub type Range = [Fp; 2];

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
///
/// **Note**: This convenience wrapper re-initialises the hasher on every call
/// (including a ~6 KiB heap allocation for round constants). It is fine for
/// one-off hashes (proofs, tests) but should *not* be used in tight loops.
/// For bulk hashing see [`PoseidonHasher`].
pub fn poseidon_hash(left: Fp, right: Fp) -> Fp {
    poseidon::Hash::<_, P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([left, right])
}

/// A reusable Poseidon hasher that avoids per-call initialisation overhead.
///
/// `poseidon::Hash::init()` calls `P128Pow5T3::constants()` every time,
/// heap-allocating and copying 64 round constants (~6 KiB). During tree
/// building this adds up to ~128 M unnecessary allocations. `PoseidonHasher`
/// computes the constants once and implements the permutation inline,
/// producing identical results to [`poseidon_hash`].
///
/// Correctness is verified by `test_poseidon_hasher_equivalence`.
pub(crate) struct PoseidonHasher {
    round_constants: Vec<[Fp; 3]>,
    mds: [[Fp; 3]; 3],
    /// `ConstantLength<2>` capacity element: `L * 2^64` where `L = 2`.
    initial_capacity: Fp,
}

impl PoseidonHasher {
    /// Create a new hasher, computing round constants and MDS matrix once.
    pub(crate) fn new() -> Self {
        let (round_constants, mds, _) = P128Pow5T3::constants();
        // ConstantLength<L> encodes capacity as L * 2^64 (with output length 1).
        let initial_capacity = Fp::from_u128(2u128 << 64);
        PoseidonHasher {
            round_constants,
            mds,
            initial_capacity,
        }
    }

    /// Hash two field elements using Poseidon, equivalent to [`poseidon_hash`].
    ///
    /// For `ConstantLength<2>` with width = 3, rate = 2 the sponge absorbs
    /// both inputs in a single block (no padding), so the hash reduces to:
    ///
    /// ```text
    /// state = [left, right, capacity]
    /// permute(&mut state)
    /// return state[0]
    /// ```
    ///
    /// This equivalence is proven by the `orchard_spec_equivalence` test in
    /// halo2_gadgets and validated locally by `test_poseidon_hasher_equivalence`.
    #[inline]
    pub(crate) fn hash(&self, left: Fp, right: Fp) -> Fp {
        let mut state = [left, right, self.initial_capacity];
        self.permute(&mut state);
        state[0]
    }

    /// Poseidon permutation with P128Pow5T3 parameters (R_F = 8, R_P = 56).
    fn permute(&self, state: &mut [Fp; 3]) {
        const R_F_HALF: usize = 4; // full_rounds / 2
        const R_P: usize = 56;

        let rcs = &self.round_constants;
        let mut ri = 0;

        // First half: full rounds (S-box on every element).
        for _ in 0..R_F_HALF {
            let rc = &rcs[ri];
            state[0] = (state[0] + rc[0]).pow_vartime([5]);
            state[1] = (state[1] + rc[1]).pow_vartime([5]);
            state[2] = (state[2] + rc[2]).pow_vartime([5]);
            self.apply_mds(state);
            ri += 1;
        }

        // Partial rounds (S-box on first element only).
        for _ in 0..R_P {
            let rc = &rcs[ri];
            state[0] += rc[0];
            state[1] += rc[1];
            state[2] += rc[2];
            state[0] = state[0].pow_vartime([5]);
            self.apply_mds(state);
            ri += 1;
        }

        // Second half: full rounds.
        for _ in 0..R_F_HALF {
            let rc = &rcs[ri];
            state[0] = (state[0] + rc[0]).pow_vartime([5]);
            state[1] = (state[1] + rc[1]).pow_vartime([5]);
            state[2] = (state[2] + rc[2]).pow_vartime([5]);
            self.apply_mds(state);
            ri += 1;
        }
    }

    #[inline(always)]
    fn apply_mds(&self, state: &mut [Fp; 3]) {
        let [s0, s1, s2] = *state;
        state[0] = self.mds[0][0] * s0 + self.mds[0][1] * s1 + self.mds[0][2] * s2;
        state[1] = self.mds[1][0] * s0 + self.mds[1][1] * s1 + self.mds[1][2] * s2;
        state[2] = self.mds[2][0] * s0 + self.mds[2][1] * s1 + self.mds[2][2] * s2;
    }
}

/// Hash each `(low, high)` range pair into a single leaf commitment.
pub fn commit_ranges(ranges: &[Range]) -> Vec<Fp> {
    let hasher = PoseidonHasher::new();
    ranges
        .iter()
        .map(|[low, high]| hasher.hash(*low, *high))
        .collect()
}

/// Pre-compute the empty subtree hash at each tree level.
///
/// `empty[0] = poseidon_hash(0, 0)` — the hash of an empty (low=0, high=0) leaf.
/// `empty[i]` is the hash of a fully-empty subtree of height `i`, computed as
/// `poseidon_hash(empty[i-1], empty[i-1])`.
///
/// These are used during tree construction and proof generation to represent
/// the hash of any subtree that contains no populated leaves, avoiding the
/// need to recompute them on every call.
pub fn precompute_empty_hashes() -> [Fp; TREE_DEPTH] {
    let hasher = PoseidonHasher::new();
    let mut empty = [Fp::default(); TREE_DEPTH];
    empty[0] = hasher.hash(Fp::zero(), Fp::zero());
    for i in 1..TREE_DEPTH {
        empty[i] = hasher.hash(empty[i - 1], empty[i - 1]);
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
    let hasher = PoseidonHasher::new();
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
            next.push(hasher.hash(prev[j * 2], prev[j * 2 + 1]));
        }
        if next.len() & 1 == 1 {
            next.push(empty[i + 1]);
        }
        levels.push(next);
    }

    // The final level has exactly two nodes; hash them to get the root.
    let top = &levels[TREE_DEPTH - 1];
    let root = hasher.hash(top[0], top[1]);

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

    /// Load a tree from a binary file written by [`save`](NullifierTree::save).
    pub fn load(path: &Path) -> Result<Self> {
        let ranges = load_tree(path)?;
        Ok(Self::from_ranges(ranges))
    }

    /// Build a tree from pre-computed gap ranges.
    pub fn from_ranges(ranges: Vec<Range>) -> Self {
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

    /// Convert this exclusion proof into a circuit-compatible proof structure.
    ///
    /// Returns an [`ImtProofData`] that can be fed directly to the delegation
    /// circuit's condition 13 (IMT non-membership verification).
    ///
    /// # Panics
    ///
    /// Panics if `auth_path.len() != TREE_DEPTH` (29).
    pub fn to_imt_proof_data(&self, root: Fp) -> ImtProofData {
        let path: [Fp; TREE_DEPTH] = self
            .auth_path
            .clone()
            .try_into()
            .expect("auth_path must have exactly TREE_DEPTH elements");
        ImtProofData {
            root,
            low: self.range[0],
            high: self.range[1],
            leaf_pos: self.position,
            path,
        }
    }
}

/// Circuit-compatible IMT non-membership proof data.
///
/// Mirrors the `ImtProofData` struct from the delegation circuit. Each field
/// maps directly to a circuit witness:
///
/// - `root`: public input, checked against the IMT root in the instance column
/// - `low`, `high`: witnessed interval bounds, hashed to the leaf commitment
/// - `leaf_pos`: position bits determine swap ordering at each Merkle level
/// - `path`: sibling hashes for the 29-level Merkle authentication path
#[derive(Clone, Debug)]
pub struct ImtProofData {
    /// The Merkle root of the IMT.
    pub root: Fp,
    /// Interval start (low bound of the bracketing leaf).
    pub low: Fp,
    /// Interval end (high bound of the bracketing leaf).
    pub high: Fp,
    /// Position of the leaf in the tree.
    pub leaf_pos: u32,
    /// Sibling hashes along the 29-level Merkle path (pure siblings).
    pub path: [Fp; TREE_DEPTH],
}

impl ImtProofData {
    /// Verify this proof out-of-circuit.
    ///
    /// Checks that `value` falls within `[low, high]` and that the Merkle
    /// path recomputes to `root`.
    pub fn verify(&self, value: Fp) -> bool {
        if value < self.low || value > self.high {
            return false;
        }
        let leaf = poseidon_hash(self.low, self.high);
        let mut current = leaf;
        let mut pos = self.leaf_pos;
        for sibling in self.path.iter() {
            let (l, r) = if pos & 1 == 0 {
                (current, *sibling)
            } else {
                (*sibling, current)
            };
            current = poseidon_hash(l, r);
            pos >>= 1;
        }
        current == self.root
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
/// This is the required initialization for any tree whose proofs will be
/// verified by the delegation circuit (condition 13), which range-checks
/// interval widths to 250 bits.
pub fn build_sentinel_tree(extra: &[Fp]) -> NullifierTree {
    let step = Fp::from(2u64).pow([250, 0, 0, 0]);
    let mut nullifiers: Vec<Fp> = (0u64..=16).map(|k| step * Fp::from(k)).collect();
    nullifiers.extend_from_slice(extra);
    NullifierTree::build(nullifiers)
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
        assert!(!proof.verify(fp(5), root));
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
        let dir = std::env::temp_dir().join("imt_tree_test");
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

        assert_eq!(empty[0], poseidon_hash(Fp::zero(), Fp::zero()));

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

        let top = &tree.levels[TREE_DEPTH - 1];
        let expected_root = poseidon_hash(top[0], top[1]);
        assert_eq!(tree.root(), expected_root);
    }

    #[test]
    fn test_leaves_accessor() {
        let tree = NullifierTree::build(four_nullifiers());
        let leaves = tree.leaves();
        assert_eq!(leaves.len(), 5);
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
        let ranges = build_nf_ranges(vec![fp(100)]);
        assert_eq!(ranges.len(), 2);

        assert_eq!(find_range_for_value(&ranges, fp(0)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(99)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(100)), None);
        assert_eq!(find_range_for_value(&ranges, fp(101)), Some(1));
        assert_eq!(find_range_for_value(&ranges, fp(999)), Some(1));
    }

    #[test]
    fn test_find_range_exact_boundaries() {
        let ranges = build_nf_ranges(four_nullifiers());
        assert_eq!(find_range_for_value(&ranges, fp(0)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(11)), Some(1));
        assert_eq!(find_range_for_value(&ranges, fp(21)), Some(2));
        assert_eq!(find_range_for_value(&ranges, fp(31)), Some(3));
        assert_eq!(find_range_for_value(&ranges, fp(41)), Some(4));

        assert_eq!(find_range_for_value(&ranges, fp(9)), Some(0));
        assert_eq!(find_range_for_value(&ranges, fp(19)), Some(1));
        assert_eq!(find_range_for_value(&ranges, fp(29)), Some(2));
        assert_eq!(find_range_for_value(&ranges, fp(39)), Some(3));
    }

    #[test]
    fn test_find_range_consecutive_nullifiers() {
        let ranges = build_nf_ranges(vec![fp(10), fp(11), fp(12)]);
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
        let nullifiers: Vec<Fp> = (0..10_000u64).map(|i| fp(i * 3 + 1)).collect();
        let ranges = build_nf_ranges(nullifiers.clone());

        for nf in &nullifiers {
            assert!(find_range_for_value(&ranges, *nf).is_none());
        }

        for (i, window) in nullifiers.windows(2).enumerate() {
            let mid = window[0] + Fp::one();
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

    // ── Verifier soundness: tampered proof inputs ────────────────────

    #[test]
    fn test_verify_rejects_tampered_auth_path_level_0() {
        let tree = NullifierTree::build(four_nullifiers());
        let root = tree.root();
        let value = fp(15);
        let mut proof = tree.prove(value).unwrap();

        proof.auth_path[0] = proof.auth_path[0] + Fp::one();
        assert!(
            !proof.verify(value, root),
            "tampered auth_path[0] should fail verification"
        );
    }

    #[test]
    fn test_verify_rejects_tampered_auth_path_mid_level() {
        let tree = NullifierTree::build(four_nullifiers());
        let root = tree.root();
        let value = fp(15);
        let mut proof = tree.prove(value).unwrap();

        let mid = TREE_DEPTH / 2;
        proof.auth_path[mid] = Fp::zero();
        assert!(
            !proof.verify(value, root),
            "tampered auth_path[{}] should fail verification",
            mid
        );
    }

    #[test]
    fn test_verify_rejects_tampered_leaf() {
        let tree = NullifierTree::build(four_nullifiers());
        let root = tree.root();
        let value = fp(15);
        let mut proof = tree.prove(value).unwrap();

        proof.leaf = Fp::from(999u64);
        assert!(
            !proof.verify(value, root),
            "tampered leaf commitment should fail verification"
        );
    }

    #[test]
    fn test_verify_rejects_tampered_position() {
        let tree = NullifierTree::build(four_nullifiers());
        let root = tree.root();
        let value = fp(15);
        let mut proof = tree.prove(value).unwrap();
        assert_eq!(proof.position, 1);

        proof.position = 0;
        assert!(!proof.verify(value, root), "position 0 (wrong) should fail");

        proof.position = 2;
        assert!(!proof.verify(value, root), "position 2 (wrong) should fail");

        proof.position = u32::MAX;
        assert!(!proof.verify(value, root), "position MAX (wrong) should fail");
    }

    #[test]
    fn test_verify_rejects_swapped_range_bounds() {
        let tree = NullifierTree::build(four_nullifiers());
        let root = tree.root();
        let value = fp(15);
        let mut proof = tree.prove(value).unwrap();

        let [low, high] = proof.range;
        proof.range = [high, low];
        assert!(
            !proof.verify(value, root),
            "swapped range bounds should fail verification"
        );
    }

    // ── Tree behavior at different scales ────────────────────────────

    #[test]
    fn test_single_nullifier_tree() {
        let tree = NullifierTree::build(vec![fp(100)]);
        assert_eq!(tree.len(), 2);

        let ranges = tree.ranges();
        assert_eq!(ranges[0], [fp(0), fp(99)]);
        assert_eq!(ranges[1][0], fp(101));
        assert_eq!(ranges[1][1], Fp::one().neg());

        let root = tree.root();

        let proof_low = tree.prove(fp(50)).unwrap();
        assert_eq!(proof_low.position, 0);
        assert!(proof_low.verify(fp(50), root));

        let proof_high = tree.prove(fp(200)).unwrap();
        assert_eq!(proof_high.position, 1);
        assert!(proof_high.verify(fp(200), root));

        assert!(tree.prove(fp(100)).is_none());
    }

    #[test]
    fn test_consecutive_nullifiers_collapse_gap() {
        let tree = NullifierTree::build(vec![fp(5), fp(6), fp(7)]);

        assert_eq!(tree.len(), 2);
        assert_eq!(tree.ranges()[0], [fp(0), fp(4)]);
        assert_eq!(tree.ranges()[1][0], fp(8));

        let root = tree.root();

        assert!(tree.prove(fp(2)).unwrap().verify(fp(2), root));
        assert!(tree.prove(fp(100)).unwrap().verify(fp(100), root));

        for nf in [5u64, 6, 7] {
            assert!(tree.prove(fp(nf)).is_none(), "nullifier {} should have no proof", nf);
        }
    }

    #[test]
    fn test_adjacent_nullifiers_differ_by_one() {
        let tree = NullifierTree::build(vec![fp(5), fp(6)]);

        assert_eq!(tree.len(), 2);
        assert_eq!(tree.ranges()[0], [fp(0), fp(4)]);
        assert_eq!(tree.ranges()[1][0], fp(7));

        let root = tree.root();
        assert!(tree.prove(fp(4)).unwrap().verify(fp(4), root));
        assert!(tree.prove(fp(7)).unwrap().verify(fp(7), root));
        assert!(tree.prove(fp(5)).is_none());
        assert!(tree.prove(fp(6)).is_none());
    }

    #[test]
    fn test_nullifier_at_zero() {
        let tree = NullifierTree::build(vec![Fp::zero()]);
        assert_eq!(tree.len(), 1);
        assert_eq!(tree.ranges()[0][0], fp(1));
        assert_eq!(tree.ranges()[0][1], Fp::one().neg());

        let root = tree.root();
        assert!(tree.prove(Fp::zero()).is_none());
        assert!(tree.prove(fp(1)).unwrap().verify(fp(1), root));
        assert!(tree.prove(fp(1000)).unwrap().verify(fp(1000), root));
    }

    #[test]
    fn test_nullifier_at_zero_and_one() {
        let tree = NullifierTree::build(vec![Fp::zero(), fp(1)]);
        assert_eq!(tree.len(), 1);
        assert_eq!(tree.ranges()[0][0], fp(2));

        let root = tree.root();
        assert!(tree.prove(Fp::zero()).is_none());
        assert!(tree.prove(fp(1)).is_none());
        assert!(tree.prove(fp(2)).unwrap().verify(fp(2), root));
    }

    #[test]
    fn test_larger_tree_200_nullifiers() {
        let nullifiers: Vec<Fp> = (1..=200u64).map(|i| fp(i * 1000)).collect();
        let tree = NullifierTree::build(nullifiers.clone());

        assert_eq!(tree.len(), 201);

        let root = tree.root();

        let test_indices = [0usize, 1, 50, 100, 150, 199, 200];
        for &idx in &test_indices {
            let range = tree.ranges()[idx];
            let value = range[0];
            let proof = tree.prove(value).unwrap();
            assert_eq!(proof.position, idx as u32);
            assert!(proof.verify(value, root), "proof at leaf index {} does not verify", idx);
        }

        for nf in &nullifiers {
            assert!(tree.prove(*nf).is_none());
        }
    }

    #[test]
    fn test_larger_tree_different_sizes_have_different_roots() {
        let tree_100 = NullifierTree::build((1..=100u64).map(fp));
        let tree_200 = NullifierTree::build((1..=200u64).map(fp));
        assert_ne!(tree_100.root(), tree_200.root());
    }

    #[test]
    fn test_duplicate_nullifiers_produce_same_tree() {
        let with_dups = NullifierTree::build(vec![fp(10), fp(10), fp(20), fp(20), fp(30)]);
        let without_dups = NullifierTree::build(vec![fp(10), fp(20), fp(30)]);
        assert_eq!(with_dups.root(), without_dups.root());
        assert_eq!(with_dups.ranges(), without_dups.ranges());
    }

    // ================================================================
    // End-to-end sentinel tree + circuit-compatible proof tests
    // ================================================================

    #[test]
    fn test_sentinel_tree_all_ranges_under_2_250() {
        let tree = build_sentinel_tree(&[]);

        let two_250 = Fp::from(2u64).pow([250, 0, 0, 0]);

        for (i, [low, high]) in tree.ranges().iter().enumerate() {
            let width = *high - *low;
            let max_width = two_250 - Fp::one();
            let check = max_width - width;
            let repr = check.to_repr();
            assert!(
                repr.as_ref()[31] < 0x40,
                "range {} has width >= 2^250: low={:?}, high={:?}",
                i, low, high
            );
        }
    }

    #[test]
    fn test_sentinel_tree_with_extra_nullifiers() {
        let extras = vec![fp(42), fp(1000000), fp(999999999)];
        let tree = build_sentinel_tree(&extras);

        for nf in &extras {
            assert!(tree.prove(*nf).is_none(), "nullifier should be excluded");
        }

        let proof = tree.prove(fp(43)).unwrap();
        assert!(proof.verify(fp(43), tree.root()));
    }

    #[test]
    fn test_imt_proof_data_round_trip() {
        let tree = build_sentinel_tree(&[fp(42), fp(100)]);
        let value = fp(50);

        let proof = tree.prove(value).expect("value should be in a gap");
        assert!(proof.verify(value, tree.root()));

        let imt = proof.to_imt_proof_data(tree.root());
        assert_eq!(imt.root, tree.root());
        assert_eq!(imt.low, proof.range[0]);
        assert_eq!(imt.high, proof.range[1]);
        assert_eq!(imt.leaf_pos, proof.position);
        assert_eq!(imt.path.len(), TREE_DEPTH);

        assert!(imt.verify(value), "ImtProofData should verify the same value");
    }

    #[test]
    fn test_imt_proof_data_rejects_wrong_value() {
        let tree = build_sentinel_tree(&[fp(42), fp(100)]);
        let value = fp(50);
        let proof = tree.prove(value).expect("value should be in a gap");
        let imt = proof.to_imt_proof_data(tree.root());

        assert!(!imt.verify(fp(42)), "nullifier should not verify");
        assert!(!imt.verify(fp(100)), "nullifier should not verify");
    }

    #[test]
    fn test_e2e_sentinel_tree_proof_gen_and_verify() {
        let extra_nfs = vec![fp(12345), fp(67890), fp(111111)];
        let tree = build_sentinel_tree(&extra_nfs);

        let test_value = fp(50000);
        assert!(tree.prove(test_value).is_some(), "test value should be in a gap range");

        let proof = tree.prove(test_value).unwrap();

        assert!(proof.verify(test_value, tree.root()));

        let imt = proof.to_imt_proof_data(tree.root());
        assert!(imt.verify(test_value));

        assert_eq!(imt.path.len(), TREE_DEPTH);
        assert_eq!(proof.auth_path.len(), TREE_DEPTH);

        let tree2 = build_sentinel_tree(&extra_nfs);
        assert_eq!(tree.root(), tree2.root());
    }

    #[test]
    fn test_empty_hashes_match_circuit_convention() {
        let empty = precompute_empty_hashes();
        let expected_leaf = poseidon_hash(Fp::zero(), Fp::zero());
        assert_eq!(empty[0], expected_leaf);

        for i in 1..TREE_DEPTH {
            assert_eq!(empty[i], poseidon_hash(empty[i - 1], empty[i - 1]));
        }
    }

    #[test]
    fn test_poseidon_hasher_equivalence() {
        let hasher = PoseidonHasher::new();

        assert_eq!(
            hasher.hash(Fp::zero(), Fp::zero()),
            poseidon_hash(Fp::zero(), Fp::zero()),
        );

        assert_eq!(hasher.hash(fp(1), fp(2)), poseidon_hash(fp(1), fp(2)));
        assert_eq!(hasher.hash(fp(42), fp(0)), poseidon_hash(fp(42), fp(0)));

        let a = fp(0xDEAD_BEEF);
        let b = fp(0xCAFE_BABE);
        assert_eq!(hasher.hash(a, b), poseidon_hash(a, b));

        assert_eq!(
            hasher.hash(Fp::one().neg(), Fp::one()),
            poseidon_hash(Fp::one().neg(), Fp::one()),
        );
    }
}
