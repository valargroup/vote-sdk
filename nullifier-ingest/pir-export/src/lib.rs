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
use serde::{Deserialize, Serialize};

use imt_tree::hasher::PoseidonHasher;
use imt_tree::tree::{build_levels, commit_ranges, precompute_empty_hashes, Range, TREE_DEPTH};

// ── Constants ────────────────────────────────────────────────────────────────

/// Depth of the PIR Merkle tree (26 levels of edges from root to leaf).
/// Supports 2^26 = 67,108,864 leaf slots, enough for ~51M nullifier ranges.
pub const PIR_DEPTH: usize = 26;

/// Depth of the full circuit tree (unchanged from existing system).
pub const FULL_DEPTH: usize = TREE_DEPTH; // 29

/// Number of layers in Tier 0 (root at depth 0 down to subtree records at depth 11).
pub const TIER0_LAYERS: usize = 11;

/// Number of layers in each Tier 1 subtree (depth 11 to depth 18).
pub const TIER1_LAYERS: usize = 7;

/// Number of layers in each Tier 2 subtree (depth 18 to depth 26).
pub const TIER2_LAYERS: usize = 8;

/// Number of Tier 1 rows (one per depth-11 subtree).
pub const TIER1_ROWS: usize = 1 << TIER0_LAYERS; // 2048

/// Number of Tier 2 rows (one per depth-18 subtree).
pub const TIER2_ROWS: usize = 1 << (TIER0_LAYERS + TIER1_LAYERS); // 262,144

/// Number of leaves per Tier 1 subtree (at relative depth 7 = global depth 18).
pub const TIER1_LEAVES: usize = 1 << TIER1_LAYERS; // 128

/// Number of leaves per Tier 2 subtree (at relative depth 8 = global depth 26).
pub const TIER2_LEAVES: usize = 1 << TIER2_LAYERS; // 256

/// Internal nodes per Tier 1 row (relative depths 1-6: 2+4+...+64 = 126).
pub const TIER1_INTERNAL_NODES: usize = (1 << TIER1_LAYERS) - 2; // 126

/// Internal nodes per Tier 2 row (relative depths 1-7: 2+4+...+128 = 254).
pub const TIER2_INTERNAL_NODES: usize = (1 << TIER2_LAYERS) - 2; // 254

/// Byte size of one Tier 1 row: 126 × 32 (internal) + 128 × 64 (leaf records).
pub const TIER1_ROW_BYTES: usize = TIER1_INTERNAL_NODES * 32 + TIER1_LEAVES * 64; // 12,224

/// Byte size of one Tier 2 row: 254 × 32 (internal) + 256 × 64 (leaf records).
pub const TIER2_ROW_BYTES: usize = TIER2_INTERNAL_NODES * 32 + TIER2_LEAVES * 64; // 24,512

/// Tier 1 item size in bits (for YPIR parameter setup).
pub const TIER1_ITEM_BITS: usize = TIER1_ROW_BYTES * 8; // 97,792

/// Tier 2 item size in bits (for YPIR parameter setup).
pub const TIER2_ITEM_BITS: usize = TIER2_ROW_BYTES * 8; // 196,096

// ── Metadata ─────────────────────────────────────────────────────────────────

/// Metadata written to `pir_root.json` alongside the tier files.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PirMetadata {
    /// Hex-encoded depth-26 Merkle root.
    pub root26: String,
    /// Hex-encoded depth-29 Merkle root (circuit-compatible).
    pub root29: String,
    /// Number of populated leaf ranges in the tree.
    pub num_ranges: usize,
    /// PIR tree depth.
    pub pir_depth: usize,
    /// Tier 0 size in bytes.
    pub tier0_bytes: usize,
    /// Number of Tier 1 rows.
    pub tier1_rows: usize,
    /// Tier 1 row size in bytes.
    pub tier1_row_bytes: usize,
    /// Number of Tier 2 rows.
    pub tier2_rows: usize,
    /// Tier 2 row size in bytes.
    pub tier2_row_bytes: usize,
    /// Block height the tree was built from (if known).
    pub height: Option<u64>,
}

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
    eprintln!(
        "  PIR leaf hashing: {} leaves in {:.1}s",
        leaves.len(),
        t0.elapsed().as_secs_f64()
    );

    let empty_hashes = precompute_empty_hashes();

    let t1 = Instant::now();
    let (root26, levels) = build_levels(leaves, &empty_hashes, PIR_DEPTH);
    eprintln!(
        "  PIR tree build ({} levels): {:.1}s",
        levels.len(),
        t1.elapsed().as_secs_f64()
    );

    let root29 = extend_root(root26, &empty_hashes);
    eprintln!("  Depth-29 root: {}", hex::encode(root29.to_repr()));

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
    for level in PIR_DEPTH..FULL_DEPTH {
        root = hasher.hash(root, empty_hashes[level]);
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

/// Write an Fp value as 32 little-endian bytes into `buf`.
#[inline]
pub fn write_fp(buf: &mut [u8], fp: Fp) {
    buf[..32].copy_from_slice(&fp.to_repr());
}

/// Read an Fp value from 32 little-endian bytes.
#[inline]
pub(crate) fn read_fp(buf: &[u8]) -> Fp {
    let mut arr = [0u8; 32];
    arr.copy_from_slice(&buf[..32]);
    Fp::from_repr(arr).expect("read_fp: non-canonical Fp (caller must validate first)")
}

/// Validate that a 32-byte slice is a canonical Fp encoding.
#[inline]
pub fn validate_fp_bytes(buf: &[u8]) -> anyhow::Result<()> {
    anyhow::ensure!(
        buf.len() == 32,
        "invalid field element byte length: got {}, expected 32",
        buf.len()
    );
    let mut arr = [0u8; 32];
    arr.copy_from_slice(buf);
    let fp = Fp::from_repr(arr);
    anyhow::ensure!(
        bool::from(fp.is_some()),
        "non-canonical field element encoding"
    );
    Ok(())
}

/// Get a node hash from the tree levels, returning empty_hash if out of bounds.
#[inline]
pub fn node_or_empty(levels: &[Vec<Fp>], level: usize, index: usize, empty_hashes: &[Fp]) -> Fp {
    if index < levels[level].len() {
        levels[level][index]
    } else {
        empty_hashes[level]
    }
}

/// Validate that every 32-byte chunk in `data` is a canonical Fp encoding.
///
/// Used by `Tier0Data::from_bytes`, `Tier1Row::from_bytes`, and `Tier2Row::from_bytes`
/// to reject tier data containing non-canonical field elements.
pub fn validate_all_fp_chunks(data: &[u8], tier_label: &str) -> anyhow::Result<()> {
    for (i, chunk) in data.chunks_exact(32).enumerate() {
        validate_fp_bytes(chunk).map_err(|e| {
            anyhow::anyhow!("{} invalid field element at 32-byte chunk {}: {}", tier_label, i, e)
        })?;
    }
    Ok(())
}

/// Binary search a sequence of 64-byte records for the last entry whose key <= `value`.
///
/// Each record has a 32-byte key at offset `key_offset_in_record` within the record.
/// `base` is the byte offset in `data` where the record array starts.
/// `count` is the number of records.
///
/// Returns `Some(index)` of the last record with key <= value, or `None` if all keys > value.
pub fn binary_search_records(
    data: &[u8],
    base: usize,
    count: usize,
    record_size: usize,
    key_offset: usize,
    value: Fp,
) -> Option<usize> {
    let mut lo = 0usize;
    let mut hi = count;
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        let k_off = base + mid * record_size + key_offset;
        let k = read_fp(&data[k_off..k_off + 32]);
        if k <= value {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    if lo == 0 { None } else { Some(lo - 1) }
}

/// Sort raw nullifiers, inject circuit-required sentinels, and build gap ranges.
///
/// The sentinel values at `k * 2^250` for `k = 0..=16` are required by the
/// circuit's gap-width constraint. After injection the list is sorted,
/// deduplicated, and converted to gap ranges.
pub fn prepare_nullifiers(mut nfs: Vec<Fp>) -> Vec<Range> {
    use ff::Field;

    nfs.sort();
    let step = Fp::from(2u64).pow([250, 0, 0, 0]);
    let sentinels: Vec<Fp> = (0u64..=16).map(|k| step * Fp::from(k)).collect();
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
    eprintln!(
        "  {} ranges built in {:.1}s",
        ranges.len(),
        t1.elapsed().as_secs_f64()
    );

    on_progress("building Merkle tree", 15);
    eprintln!("Building depth-{} PIR tree...", PIR_DEPTH);
    let tree = build_pir_tree(ranges)?;
    eprintln!(
        "  Root-{}: {}",
        PIR_DEPTH,
        hex::encode(tree.root26.to_repr())
    );
    eprintln!(
        "  Root-{}: {}",
        FULL_DEPTH,
        hex::encode(tree.root29.to_repr())
    );

    on_progress("writing tier files", 40);
    eprintln!("Exporting tier files to {:?}...", output_dir);
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
    eprintln!("  Tier 0 exported: {} bytes in {:.1}s", tier0_data.len(), t0.elapsed().as_secs_f64());

    // Tier 1
    let t1 = Instant::now();
    let mut f1 = std::io::BufWriter::new(std::fs::File::create(output_dir.join("tier1.bin"))?);
    tier1::export(&tree.levels, &tree.ranges, &tree.empty_hashes, &mut f1)?;
    f1.flush()?;
    eprintln!("  Tier 1 exported in {:.1}s", t1.elapsed().as_secs_f64());

    // Tier 2
    let t2 = Instant::now();
    let mut f2 = std::io::BufWriter::new(std::fs::File::create(output_dir.join("tier2.bin"))?);
    tier2::export(&tree.levels, &tree.ranges, &tree.empty_hashes, &mut f2)?;
    f2.flush()?;
    eprintln!("  Tier 2 exported in {:.1}s", t2.elapsed().as_secs_f64());

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
    eprintln!("  Metadata written to pir_root.json");

    Ok(())
}
