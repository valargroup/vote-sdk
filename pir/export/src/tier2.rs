//! Tier 2 export: 262,144 rows, each a depth-18 subtree with 8 internal layers + leaf records.
//!
//! Row layout (24,512 bytes):
//! ```text
//! [internal nodes: 254 × 32 bytes, relative depths 1-7 in BFS order]
//!   depth 1: 2 nodes   → bytes [0..64)
//!   depth 2: 4 nodes   → bytes [64..192)
//!   ...
//!   depth 7: 128 nodes → bytes [6080..8128)
//! [leaf records: 256 × (32-byte key + 32-byte value)]
//!   record i: key (low) at 8128+i*64, value (width) at 8128+i*64+32
//! ```
//!
//! Internal node at relative depth d (1..7), position p:
//!   byte offset = ((2^d - 2) + p) * 32
//!
//! Leaf record i (0..255):
//!   byte offset = 254 * 32 + i * 64

use std::io::Write;

use anyhow::Result;
use pasta_curves::Fp;

use imt_tree::tree::{Range, TREE_DEPTH};

use crate::{
    write_fp, write_internal_nodes, PIR_DEPTH, TIER0_LAYERS, TIER1_LAYERS, TIER2_INTERNAL_NODES,
    TIER2_LAYERS, TIER2_LEAVES, TIER2_ROWS, TIER2_ROW_BYTES,
};

pub use pir_types::tier2::Tier2Row;

const PROGRESS_INTERVAL: usize = 100_000;

/// Export all Tier 2 rows to a writer.
///
/// Rows are computed and written one at a time to avoid materializing all rows
/// in memory (~6 GB if collected).
pub fn export(
    levels: &[Vec<Fp>],
    ranges: &[Range],
    empty_hashes: &[Fp; TREE_DEPTH],
    writer: &mut impl Write,
) -> Result<()> {
    let mut buf = vec![0u8; TIER2_ROW_BYTES];

    for s in 0..TIER2_ROWS {
        write_row(levels, ranges, empty_hashes, s, &mut buf);
        writer.write_all(&buf)?;
        if s > 0 && s % PROGRESS_INTERVAL == 0 {
            tracing::info!(row = s, total = TIER2_ROWS, "Tier 2 export progress");
        }
    }

    Ok(())
}

/// Write a single Tier 2 row for subtree index `s` (at depth 18).
///
/// The subtree root is at bottom-up level `PIR_DEPTH - TIER0_LAYERS - TIER1_LAYERS` = 8,
/// index `s`.
fn write_row(
    levels: &[Vec<Fp>],
    ranges: &[Range],
    empty_hashes: &[Fp; TREE_DEPTH],
    s: usize,
    buf: &mut [u8],
) {
    buf.fill(0);
    let bu_base = PIR_DEPTH - TIER0_LAYERS - TIER1_LAYERS; // 7: bottom-up level of subtree root

    // ── Internal nodes: relative depths 1 through (TIER2_LAYERS - 1) ─────
    let mut offset = write_internal_nodes(levels, empty_hashes, bu_base, TIER2_LAYERS, s, buf);

    debug_assert_eq!(offset, TIER2_INTERNAL_NODES * 32);

    // ── Leaf records: 256 entries at relative depth 8 (depth 26 = tree leaves) ──
    //
    // Each record: 32-byte key (low) + 32-byte value (width).
    // These are the raw range data, NOT hashes — the client hashes them as needed.
    let leaf_start = s * TIER2_LEAVES; // s * 128

    for i in 0..TIER2_LEAVES {
        let global_idx = leaf_start + i;
        if global_idx < ranges.len() {
            let [low, width] = ranges[global_idx];
            write_fp(&mut buf[offset..], low);
            offset += 32;
            write_fp(&mut buf[offset..], width);
            offset += 32;
        } else {
            write_fp(&mut buf[offset..], Fp::zero());
            offset += 32;
            write_fp(&mut buf[offset..], Fp::zero());
            offset += 32;
        }
    }

    debug_assert_eq!(offset, TIER2_ROW_BYTES);
}
