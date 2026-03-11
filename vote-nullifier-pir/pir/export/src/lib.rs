//! PIR tree builder and tier data exporter.
//!
//! Builds a depth-26 Merkle tree from nullifier ranges and exports the three
//! tier files consumed by `pir-server`:
//!
//! - **Tier 0** (192 KB): plaintext internal nodes (depths 0-10) + 2048
//!   subtree records at depth 11 (hash + min_key).
//! - **Tier 1** (24 MB): 2048 rows × 12,224 bytes. Each row is a depth-11
//!   subtree (7 layers of internal nodes + 128 leaf records with hash + min_key).
//! - **Tier 2** (6 GB): 262,144 rows × 24,512 bytes. Each row is a depth-18
//!   subtree (8 layers of internal nodes + 256 leaf records with key + value).

pub mod tier0;
pub mod tier1;
pub mod tier2;

use std::io::Write;
use std::time::Instant;

use anyhow::Result;
use ff::PrimeField as _;
use pasta_curves::Fp;
use tracing::info;

use imt_tree::hasher::PoseidonHasher;
use imt_tree::tree::{build_levels, commit_ranges, precompute_empty_hashes, Range, TREE_DEPTH};
use imt_tree::tree::build_nf_ranges;

// Re-export tier-layout constants and PirMetadata from pir-types so that
// existing consumers (tier submodules, tests, downstream crates) keep working.
pub use pir_types::{
    PirMetadata, PIR_DEPTH, TIER0_LAYERS, TIER1_INTERNAL_NODES, TIER1_ITEM_BITS, TIER1_LAYERS,
    TIER1_LEAVES, TIER1_ROWS, TIER1_ROW_BYTES, TIER2_INTERNAL_NODES, TIER2_ITEM_BITS,
    TIER2_LAYERS, TIER2_LEAVES, TIER2_ROWS, TIER2_ROW_BYTES,
};

/// Depth of the full circuit tree (unchanged from existing system).
pub const FULL_DEPTH: usize = TREE_DEPTH; // 29

// ── Tree building ────────────────────────────────────────────────────────────

/// Result of building the PIR tree.
pub struct PirTree {
    /// Depth-26 Merkle root.
    pub root26: Fp,
    /// Depth-29 Merkle root (extended with 3 empty hashes).
    pub root29: Fp,
    /// Tree levels (bottom-up): levels[0] = leaf hashes, levels[25] = root's children.
    pub levels: Vec<Vec<Fp>>,
    /// Gap ranges (sorted by low).
    pub ranges: Vec<Range>,
    /// Precomputed empty hashes for all 29 levels.
    pub empty_hashes: [Fp; TREE_DEPTH],
}

/// Build a depth-26 PIR tree from sorted nullifier ranges.
///
/// The ranges must already be constructed (e.g., via `imt_tree::build_nf_ranges`).
/// This function hashes them into leaf commitments and builds the depth-26 Merkle
/// tree, then extends the root to depth 29 for circuit compatibility.
pub fn build_pir_tree(ranges: Vec<Range>) -> Result<PirTree> {
    anyhow::ensure!(
        ranges.len() <= 1 << PIR_DEPTH,
        "too many ranges ({}) for PIR depth {} (max {})",
        ranges.len(), PIR_DEPTH, 1 << PIR_DEPTH
    );
    let t0 = Instant::now();
    let leaves = commit_ranges(&ranges);
    info!(
        count = leaves.len(),
        elapsed_s = format!("{:.1}", t0.elapsed().as_secs_f64()),
        "PIR leaf hashing"
    );

    let empty_hashes = precompute_empty_hashes();

    let t1 = Instant::now();
    let (root26, levels) = build_levels(leaves, &empty_hashes, PIR_DEPTH);
    info!(
        level_count = levels.len(),
        elapsed_s = format!("{:.1}", t1.elapsed().as_secs_f64()),
        "PIR tree built"
    );

    let root29 = extend_root(root26, &empty_hashes);
    info!(root29 = hex::encode(root29.to_repr()), "depth-29 root");

    Ok(PirTree {
        root26,
        root29,
        levels,
        ranges,
        empty_hashes,
    })
}

/// Extend a depth-26 root to a depth-29 root by hashing with empty subtrees.
///
/// At each extension level, the existing root is the left child and an empty
/// subtree of the appropriate height is the right child. This produces the
/// same root as building a depth-29 tree with the same leaves (since all
/// leaf slots above 2^26 are empty).
pub fn extend_root(root26: Fp, empty_hashes: &[Fp; TREE_DEPTH]) -> Fp {
    let hasher = PoseidonHasher::new();
    let mut root = root26;
    for empty_hash in &empty_hashes[PIR_DEPTH..FULL_DEPTH] {
        root = hasher.hash(root, *empty_hash);
    }
    root
}

// ── Helpers ──────────────────────────────────────────────────────────────────

/// Get the min_key for a subtree given its leftmost leaf index.
///
/// Returns `ranges[leaf_start][0]` (the `low` value of the first range
/// in the subtree). For empty subtrees (leaf_start >= ranges.len()),
/// returns the largest Fp value so binary search skips them.
pub fn subtree_min_key(ranges: &[Range], leaf_start: usize) -> Fp {
    if leaf_start < ranges.len() {
        ranges[leaf_start][0]
    } else {
        // Sentinel: largest field element. Binary search with ≤ will skip these.
        Fp::one().neg() // p - 1
    }
}

pub use pir_types::fp_utils::write_fp;

/// Get a node hash from the tree levels, returning empty_hash if out of bounds.
#[inline]
pub fn node_or_empty(levels: &[Vec<Fp>], level: usize, index: usize, empty_hashes: &[Fp]) -> Fp {
    if index < levels[level].len() {
        levels[level][index]
    } else {
        empty_hashes[level]
    }
}

/// Write BFS-ordered internal node hashes for a subtree into `buf`.
///
/// Iterates relative depths 1 through `num_layers - 1`. At each depth `d`,
/// the bottom-up level is `bu_base - d` and the nodes are at global indices
/// `subtree_index * 2^d .. subtree_index * 2^d + 2^d - 1`.
///
/// Returns the number of bytes written (`((2^num_layers) - 2) * 32`).
pub fn write_internal_nodes(
    levels: &[Vec<Fp>],
    empty_hashes: &[Fp],
    bu_base: usize,
    num_layers: usize,
    subtree_index: usize,
    buf: &mut [u8],
) -> usize {
    let mut offset = 0;
    for d in 1..num_layers {
        let bu_level = bu_base - d;
        let count = 1usize << d;
        let start = subtree_index * count;
        for i in 0..count {
            let val = node_or_empty(levels, bu_level, start + i, empty_hashes);
            write_fp(&mut buf[offset..], val);
            offset += 32;
        }
    }
    offset
}

/// Exponent used for sentinel nullifier spacing: `2^SENTINEL_EXPONENT`.
const SENTINEL_EXPONENT: u64 = 250;

/// Number of sentinel nullifiers injected: `0, 1*step, 2*step, ..., SENTINEL_COUNT*step`.
const SENTINEL_COUNT: u64 = 16;

/// Sort raw nullifiers, inject circuit-required sentinels, and build gap ranges.
///
/// The sentinel values at `k * 2^250` for `k = 0..=16` are required by the
/// circuit's gap-width constraint. After injection the list is sorted,
/// deduplicated, and converted to gap ranges.
pub fn prepare_nullifiers(mut nfs: Vec<Fp>) -> Vec<Range> {
    use ff::Field;

    nfs.sort();
    let step = Fp::from(2u64).pow([SENTINEL_EXPONENT, 0, 0, 0]);
    let sentinels: Vec<Fp> = (0u64..=SENTINEL_COUNT).map(|k| step * Fp::from(k)).collect();
    nfs.extend(sentinels);
    nfs.sort();
    nfs.dedup();
    imt_tree::tree::build_nf_ranges(nfs)
}

/// Build a PIR tree from raw nullifiers (sort, sentinel injection, tree build)
/// and export all tier files.
///
/// This is the high-level entry point used by both the export CLI and the
/// serve command's rebuild logic.
pub fn build_and_export(
    nfs: Vec<Fp>,
    output_dir: &std::path::Path,
    height: Option<u64>,
) -> Result<PirTree> {
    build_and_export_with_progress(nfs, output_dir, height, |_, _| {})
}

/// Build the PIR tree and export tier files, calling `on_progress(message, pct)`
/// at each major stage so callers can report progress to users.
pub fn build_and_export_with_progress(
    nfs: Vec<Fp>,
    output_dir: &std::path::Path,
    height: Option<u64>,
    on_progress: impl Fn(&str, u8),
) -> Result<PirTree> {
    on_progress("sorting nullifiers", 0);
    let t1 = std::time::Instant::now();
    let ranges = prepare_nullifiers(nfs);
    info!(
        count = ranges.len(),
        elapsed_s = format!("{:.1}", t1.elapsed().as_secs_f64()),
        "ranges built"
    );

    on_progress("building Merkle tree", 15);
    info!(depth = PIR_DEPTH, "building PIR tree");
    let tree = build_pir_tree(ranges)?;
    info!(depth = PIR_DEPTH, root = hex::encode(tree.root26.to_repr()), "root-26");
    info!(depth = FULL_DEPTH, root = hex::encode(tree.root29.to_repr()), "root-29");

    on_progress("writing tier files", 40);
    info!(?output_dir, "exporting tier files");
    export_all(&tree, output_dir, height)?;

    on_progress("tier files written", 55);
    Ok(tree)
}

/// Export all tier files and metadata to the given directory.
pub fn export_all(tree: &PirTree, output_dir: &std::path::Path, height: Option<u64>) -> Result<()> {
    std::fs::create_dir_all(output_dir)?;

    // Tier 0
    let t0 = Instant::now();
    let tier0_data = tier0::export(&tree.root26, &tree.levels, &tree.ranges, &tree.empty_hashes);
    std::fs::write(output_dir.join("tier0.bin"), &tier0_data)?;
    info!(bytes = tier0_data.len(), elapsed_s = format!("{:.1}", t0.elapsed().as_secs_f64()), "Tier 0 exported");

    // Tier 1
    let t1 = Instant::now();
    let mut f1 = std::io::BufWriter::new(std::fs::File::create(output_dir.join("tier1.bin"))?);
    tier1::export(&tree.levels, &tree.ranges, &tree.empty_hashes, &mut f1)?;
    f1.flush()?;
    info!(elapsed_s = format!("{:.1}", t1.elapsed().as_secs_f64()), "Tier 1 exported");

    // Tier 2
    let t2 = Instant::now();
    let mut f2 = std::io::BufWriter::new(std::fs::File::create(output_dir.join("tier2.bin"))?);
    tier2::export(&tree.levels, &tree.ranges, &tree.empty_hashes, &mut f2)?;
    f2.flush()?;
    info!(elapsed_s = format!("{:.1}", t2.elapsed().as_secs_f64()), "Tier 2 exported");

    // Metadata
    let metadata = PirMetadata {
        root26: hex::encode(tree.root26.to_repr()),
        root29: hex::encode(tree.root29.to_repr()),
        num_ranges: tree.ranges.len(),
        pir_depth: PIR_DEPTH,
        tier0_bytes: tier0_data.len(),
        tier1_rows: TIER1_ROWS,
        tier1_row_bytes: TIER1_ROW_BYTES,
        tier2_rows: TIER2_ROWS,
        tier2_row_bytes: TIER2_ROW_BYTES,
        height,
    };
    let json = serde_json::to_string_pretty(&metadata)?;
    std::fs::write(output_dir.join("pir_root.json"), json)?;
    info!("metadata written to pir_root.json");

    Ok(())
}

// ── Test utilities ───────────────────────────────────────────────────────────

/// Build gap ranges from raw nullifiers with sentinel nullifiers injected.
///
/// Sentinels are 17 evenly-spaced points across the Pallas field (`k * 2^250`
/// for k in 0..=16). This matches [`imt_tree::tree::build_sentinel_tree`] but
/// returns the flat range vector instead of a full `NullifierTree`.
///
/// Available in tests and when the `test-util` feature is enabled.
pub fn build_ranges_with_sentinels(raw_nfs: &[Fp]) -> Vec<Range> {
    use ff::Field as _;
    let step = Fp::from(2u64).pow([250, 0, 0, 0]);
    let sentinels: Vec<Fp> = (0u64..=16).map(|k| step * Fp::from(k)).collect();
    let mut all_nfs: Vec<Fp> = sentinels;
    all_nfs.extend_from_slice(raw_nfs);
    all_nfs.sort();
    all_nfs.dedup();
    build_nf_ranges(all_nfs)
}
