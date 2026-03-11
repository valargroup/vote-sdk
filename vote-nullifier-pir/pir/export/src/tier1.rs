//! Tier 1 export: 2,048 rows, each a depth-11 subtree with 7 internal layers + leaf records.
//!
//! Row layout (12,224 bytes):
//! ```text
//! [internal nodes: 126 × 32 bytes, relative depths 1-6 in BFS order]
//!   depth 1: 2 nodes  → bytes [0..64)
//!   depth 2: 4 nodes  → bytes [64..192)
//!   depth 3: 8 nodes  → bytes [192..448)
//!   ...
//!   depth 6: 64 nodes → bytes [3008..4032)
//! [leaf records: 128 × (32-byte hash + 32-byte min_key)]
//!   record i: hash at 4032+i*64, min_key at 4032+i*64+32
//! ```
//!
//! Internal node at relative depth d (1..6), position p:
//!   byte offset = ((2^d - 2) + p) * 32
//!
//! Leaf record i (0..127):
//!   byte offset = 126 * 32 + i * 64

use std::io::Write;

use anyhow::Result;
use pasta_curves::Fp;

use imt_tree::tree::{Range, TREE_DEPTH};

use crate::{
    node_or_empty, subtree_min_key, write_fp, write_internal_nodes, PIR_DEPTH, TIER0_LAYERS,
    TIER1_INTERNAL_NODES, TIER1_LAYERS, TIER1_LEAVES, TIER1_ROWS, TIER1_ROW_BYTES, TIER2_LEAVES,
};

pub use pir_types::tier1::Tier1Row;

/// Export all Tier 1 rows to a writer.
///
/// Rows are computed and written one at a time to avoid materializing all rows
/// in memory.
pub fn export(
    levels: &[Vec<Fp>],
    ranges: &[Range],
    empty_hashes: &[Fp; TREE_DEPTH],
    writer: &mut impl Write,
) -> Result<()> {
    let mut buf = vec![0u8; TIER1_ROW_BYTES];

    for s in 0..TIER1_ROWS {
        write_row(levels, ranges, empty_hashes, s, &mut buf);
        writer.write_all(&buf)?;
    }

    Ok(())
}

/// Write a single Tier 1 row for subtree index `s` (at depth 11).
///
/// The subtree root is at bottom-up level `PIR_DEPTH - TIER0_LAYERS` = 15, index `s`.
fn write_row(
    levels: &[Vec<Fp>],
    ranges: &[Range],
    empty_hashes: &[Fp; TREE_DEPTH],
    s: usize,
    buf: &mut [u8],
) {
    buf.fill(0);
    let bu_base = PIR_DEPTH - TIER0_LAYERS; // 15: bottom-up level of subtree root

    // ── Internal nodes: relative depths 1 through (TIER1_LAYERS - 1) ─────
    let mut offset = write_internal_nodes(levels, empty_hashes, bu_base, TIER1_LAYERS, s, buf);

    debug_assert_eq!(offset, TIER1_INTERNAL_NODES * 32);

    // ── Leaf records: 128 entries at relative depth 7 (depth 18) ─────────
    //
    // Bottom-up level = bu_base - TIER1_LAYERS = 15 - 7 = 8.
    // Each record: 32-byte hash + 32-byte min_key.
    let bu_leaf = bu_base - TIER1_LAYERS; // 7
    let leaf_start = s * TIER1_LEAVES; // s * 256

    for i in 0..TIER1_LEAVES {
        let global_idx = leaf_start + i;

        // Hash of the depth-19 subtree root
        let hash = node_or_empty(levels, bu_leaf, global_idx, empty_hashes);
        write_fp(&mut buf[offset..], hash);
        offset += 32;

        // min_key: smallest `low` among all depth-26 leaves in this depth-19 subtree.
        // Each depth-19 subtree covers TIER2_LEAVES = 128 leaves.
        let range_start = global_idx * TIER2_LEAVES;
        let mk = subtree_min_key(ranges, range_start);
        write_fp(&mut buf[offset..], mk);
        offset += 32;
    }

    debug_assert_eq!(offset, TIER1_ROW_BYTES);
}
