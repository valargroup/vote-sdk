use std::io::Write;
use std::path::Path;
use std::time::Instant;

use anyhow::Result;
use ff::PrimeField as _;
use pasta_curves::Fp;
use rayon::prelude::*;

pub(crate) use crate::hasher::PoseidonHasher;
pub use crate::proof::ImtProofData;

mod nullifier_tree;
pub use nullifier_tree::*;

#[cfg(test)]
mod tests;

/// Depth of the nullifier range Merkle tree.
///
/// Each on-chain nullifier produces approximately one gap range (n nullifiers
/// -> n + 1 ranges). Zcash mainnet currently has under 64M Orchard nullifiers.
/// We plan for this circuit to support up to 256M nullifiers, so the tree
/// needs capacity for ~2^28 leaves: `log2(256 << 20) + 1 = 29`.
pub const TREE_DEPTH: usize = 29;

/// A gap range `[low, width]` representing an interval between two adjacent
/// on-chain nullifiers. `low` is the interval start and `width = high - low`
/// where `high` is the inclusive upper bound. Each leaf in the Merkle tree
/// commits to one range via `hash(low, width)`.
///
/// **Exclusion proof**: to prove a value `x` is not a nullifier, the prover
/// reveals a range `[low, width]` where `x - low <= width` plus a Merkle path
/// proving that range is committed in the tree. Field subtraction handles the
/// lower bound check: if `x < low`, the result wraps to a value larger than
/// any valid width.
///
/// Every on-chain nullifier `n` acts as a boundary between two adjacent ranges:
/// the range before it has upper bound `n - 1` and the range after has `low = n + 1`.
/// Because the bounds are `n +/- 1`, the nullifier `n` itself falls outside every
/// range -- so the membership check can only succeed for non-nullifier values.
///
/// Example with sorted nullifiers `[n1, n2]`:
/// ```text
///   Range 0: [0,    n1-1-0]       <- gap before n1
///   Range 1: [n1+1, n2-1-(n1+1)]  <- gap between n1 and n2
///   Range 2: [n2+1, MAX-(n2+1)]   <- gap after n2
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
/// Empty slots are filled with `hash(0, 0)` -- the commitment of an
/// empty (low=0, width=0) leaf. At each level of the tree, the empty hash is
/// computed by self-hashing the level below:
/// `empty[0] = hash(0, 0)`, `empty[i+1] = hash(empty[i], empty[i])`. Any
/// subtree consisting entirely of empty leaves collapses to the empty hash for
/// that level. Odd-length layers are padded with the empty hash before hashing
/// up to the next level.
///
/// This means the root is deterministic for a given set of nullifiers
/// regardless of the tree capacity -- adding more empty slots doesn't change
/// the root because they all reduce to the same empty subtree hashes.
pub type Range = [Fp; 2];

/// Build gap ranges from a sorted nullifier set.
///
/// For each consecutive pair of nullifiers, the gap `[low, width]` is emitted
/// where `width = high - low` (inclusive upper bound minus lower bound).
/// A final range `[last_nf + 1, Fp::MAX - (last_nf + 1)]` closes the space.
pub fn build_nf_ranges(nfs: impl IntoIterator<Item = Fp>) -> Vec<Range> {
    let mut prev = Fp::zero();
    let mut ranges = vec![];
    for r in nfs {
        if prev < r {
            let high = r - Fp::one();
            ranges.push([prev, high - prev]);
        }
        prev = r + Fp::one();
    }
    if prev != Fp::zero() {
        let high = Fp::one().neg();
        ranges.push([prev, high - prev]);
    }
    ranges
}

/// Hash each `(low, width)` range pair into a single leaf commitment.
pub fn commit_ranges(ranges: &[Range]) -> Vec<Fp> {
    ranges
        .par_iter()
        .map_init(PoseidonHasher::new, |hasher, [low, width]| {
            hasher.hash(*low, *width)
        })
        .collect()
}

/// Pre-compute the empty subtree hash at each tree level.
///
/// `empty[0] = hash(0, 0)` -- the hash of an empty (low=0, width=0) leaf.
/// `empty[i]` is the hash of a fully-empty subtree of height `i`, computed as
/// `hash(empty[i-1], empty[i-1])`.
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
/// Build Merkle tree levels bottom-up from leaf hashes.
///
/// `depth` controls the number of tree levels (use `TREE_DEPTH` for a full
/// depth-29 tree, or a smaller value like 26 for the PIR tree).
/// Returns `(root, levels)` where `levels[0]` contains leaf hashes and
/// `levels[depth-1]` contains the root's two children.
pub fn build_levels(mut leaves: Vec<Fp>, empty: &[Fp; TREE_DEPTH], depth: usize) -> (Fp, Vec<Vec<Fp>>) {
    let hasher = PoseidonHasher::new();
    let mut levels: Vec<Vec<Fp>> = Vec::with_capacity(depth);

    // Level 0 = leaf commitments, padded to even length.
    // Takes ownership of `leaves` to avoid a 1.6 GB memcpy at scale.
    if leaves.is_empty() {
        leaves.push(empty[0]);
    }
    if leaves.len() & 1 == 1 {
        leaves.push(empty[0]);
    }
    levels.push(leaves);

    const PAR_THRESHOLD: usize = 1024;

    for i in 0..depth - 1 {
        let prev = &levels[i];
        let pairs = prev.len() / 2;
        let mut next: Vec<Fp> = if pairs >= PAR_THRESHOLD {
            prev.par_chunks_exact(2)
                .map_init(PoseidonHasher::new, |h, pair| h.hash(pair[0], pair[1]))
                .collect()
        } else {
            (0..pairs)
                .map(|j| hasher.hash(prev[j * 2], prev[j * 2 + 1]))
                .collect()
        };
        if next.len() & 1 == 1 {
            next.push(empty[i + 1]);
        }
        levels.push(next);
    }

    let top = &levels[depth - 1];
    let root = hasher.hash(top[0], top[1]);

    (root, levels)
}

/// Find the gap-range index that contains `value`.
///
/// Returns `Some(i)` where `ranges[i]` is `[low, width]`,
/// or `None` if the value is an existing nullifier.
///
/// Membership check: `value - low <= width` (field subtraction). If `value < low`
/// the result wraps to a huge value > width, so this single comparison covers
/// both bounds.
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
    let [low, width] = ranges[idx];
    // value - low <= width: if value < low, field subtraction wraps to a huge
    // value that exceeds any valid width, so the check fails correctly.
    let offset = value - low;
    if offset <= width {
        Some(idx)
    } else {
        None
    }
}

/// Serialize gap ranges to a binary file.
///
/// Format: `[8-byte LE count][count x 2 x 32-byte Fp representations]`
pub fn save_tree(path: &Path, ranges: &[Range]) -> Result<()> {
    let mut f = std::fs::File::create(path)?;
    let count = ranges.len() as u64;
    f.write_all(&count.to_le_bytes())?;
    for [low, width] in ranges {
        f.write_all(&low.to_repr())?;
        f.write_all(&width.to_repr())?;
    }
    Ok(())
}

/// Deserialize gap ranges from a binary file written by [`save_tree`].
///
/// Uses a single `read` syscall followed by parallel parsing for speed.
pub fn load_tree(path: &Path) -> Result<Vec<Range>> {
    let t0 = Instant::now();
    let buf = std::fs::read(path)?;
    anyhow::ensure!(buf.len() >= 8, "tree file too small");
    let count = u64::from_le_bytes(buf[..8].try_into().unwrap()) as usize;
    let expected = 8 + count * 64;
    anyhow::ensure!(
        buf.len() >= expected,
        "tree file truncated: expected {} bytes, got {}",
        expected,
        buf.len()
    );
    let ranges: Vec<Range> = buf[8..8 + count * 64]
        .par_chunks_exact(64)
        .map(|chunk| {
            let low = Fp::from_repr(chunk[..32].try_into().unwrap()).unwrap();
            let width = Fp::from_repr(chunk[32..64].try_into().unwrap()).unwrap();
            [low, width]
        })
        .collect();
    eprintln!(
        "  File read: {} ranges loaded in {:.1}s",
        ranges.len(),
        t0.elapsed().as_secs_f64()
    );
    Ok(ranges)
}

/// Serialize a full Merkle tree (ranges + all levels + root + height) to a binary file.
///
/// Format:
/// ```text
/// [8-byte LE range_count]
/// [range_count x 2 x 32-byte Fp]        -- ranges
/// [for each of TREE_DEPTH levels:
///     [8-byte LE level_len]
///     [level_len x 32-byte Fp]           -- node hashes at this level
/// ]
/// [32-byte Fp root]
/// [8-byte LE height]                     -- optional trailer (0 = unknown)
/// ```
///
/// On reload via [`load_full_tree`], zero hashing is required -- all data is
/// read directly from the file.
pub fn save_full_tree(
    path: &Path,
    ranges: &[Range],
    levels: &[Vec<Fp>],
    root: Fp,
    height: Option<u64>,
) -> Result<()> {
    let t0 = Instant::now();
    let mut f = std::fs::File::create(path)?;

    // Ranges
    let range_count = ranges.len() as u64;
    f.write_all(&range_count.to_le_bytes())?;
    for [low, width] in ranges {
        f.write_all(&low.to_repr())?;
        f.write_all(&width.to_repr())?;
    }

    // Levels
    for level in levels {
        let level_len = level.len() as u64;
        f.write_all(&level_len.to_le_bytes())?;
        for node in level {
            f.write_all(&node.to_repr())?;
        }
    }

    // Root
    f.write_all(&root.to_repr())?;

    // Height trailer (0 means unknown)
    f.write_all(&height.unwrap_or(0).to_le_bytes())?;

    eprintln!(
        "  Full tree saved: {} ranges, {} levels, height={:?} in {:.1}s",
        ranges.len(),
        levels.len(),
        height,
        t0.elapsed().as_secs_f64(),
    );
    Ok(())
}

/// Deserialize a full Merkle tree from a binary file written by [`save_full_tree`].
///
/// Returns `(ranges, levels, root, height)` with zero hashing -- all data is
/// read directly from the file using bulk I/O and parallel parsing.
///
/// Backwards-compatible: files written without the height trailer return `None`.
pub fn load_full_tree(path: &Path) -> Result<(Vec<Range>, Vec<Vec<Fp>>, Fp, Option<u64>)> {
    let t0 = Instant::now();
    let buf = std::fs::read(path)?;
    eprintln!(
        "  File read: {:.1} MB in {:.1}s",
        buf.len() as f64 / (1024.0 * 1024.0),
        t0.elapsed().as_secs_f64()
    );

    let t1 = Instant::now();
    let mut pos = 0usize;

    // Helper: read N bytes from buf
    macro_rules! read_bytes {
        ($n:expr) => {{
            let end = pos + $n;
            anyhow::ensure!(end <= buf.len(), "unexpected EOF in full tree file");
            let slice = &buf[pos..end];
            pos = end;
            slice
        }};
    }

    // Ranges
    let range_count = u64::from_le_bytes(read_bytes!(8).try_into().unwrap()) as usize;
    let range_bytes = &buf[pos..pos + range_count * 64];
    pos += range_count * 64;
    let ranges: Vec<Range> = range_bytes
        .par_chunks_exact(64)
        .map(|chunk| {
            let low = Fp::from_repr(chunk[..32].try_into().unwrap()).unwrap();
            let width = Fp::from_repr(chunk[32..64].try_into().unwrap()).unwrap();
            [low, width]
        })
        .collect();

    // Levels
    let mut levels: Vec<Vec<Fp>> = Vec::with_capacity(TREE_DEPTH);
    for _ in 0..TREE_DEPTH {
        let level_len = u64::from_le_bytes(read_bytes!(8).try_into().unwrap()) as usize;
        let level_bytes = &buf[pos..pos + level_len * 32];
        pos += level_len * 32;
        let level: Vec<Fp> = level_bytes
            .par_chunks_exact(32)
            .map(|chunk| Fp::from_repr(chunk.try_into().unwrap()).unwrap())
            .collect();
        levels.push(level);
    }

    // Root
    let root_bytes: [u8; 32] = buf[pos..pos + 32].try_into()
        .map_err(|_| anyhow::anyhow!("unexpected EOF reading root"))?;
    let root = Fp::from_repr(root_bytes).unwrap();
    pos += 32;

    // Height trailer (optional, backwards-compatible with old files)
    let height = if pos + 8 <= buf.len() {
        let h = u64::from_le_bytes(buf[pos..pos + 8].try_into().unwrap());
        if h > 0 { Some(h) } else { None }
    } else {
        None
    };

    eprintln!(
        "  Full tree parsed: {} ranges, {} levels, height={:?} in {:.1}s",
        ranges.len(),
        levels.len(),
        height,
        t1.elapsed().as_secs_f64()
    );

    Ok((ranges, levels, root, height))
}
