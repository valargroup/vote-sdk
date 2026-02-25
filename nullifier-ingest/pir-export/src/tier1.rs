//! Tier 1 export: 2,048 rows, each a depth-11 subtree with 8 internal layers + leaf records.
//!
//! Row layout (24,512 bytes):
//! ```text
//! [internal nodes: 254 × 32 bytes, relative depths 1-7 in BFS order]
//!   depth 1: 2 nodes   → bytes [0..64)
//!   depth 2: 4 nodes   → bytes [64..192)
//!   depth 3: 8 nodes   → bytes [192..448)
//!   ...
//!   depth 7: 128 nodes → bytes [6080..8128)
//! [leaf records: 256 × (32-byte hash + 32-byte min_key)]
//!   record i: hash at 8128+i*64, min_key at 8128+i*64+32
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
use rayon::prelude::*;

use imt_tree::tree::{Range, TREE_DEPTH};

use crate::{
    node_or_empty, subtree_min_key, write_fp, PIR_DEPTH, TIER0_LAYERS, TIER1_INTERNAL_NODES,
    TIER1_LAYERS, TIER1_LEAVES, TIER1_ROWS, TIER1_ROW_BYTES, TIER2_LEAVES,
};

/// Export all Tier 1 rows to a writer.
///
/// Rows are computed in parallel using Rayon, then written sequentially.
pub fn export(
    levels: &[Vec<Fp>],
    ranges: &[Range],
    empty_hashes: &[Fp; TREE_DEPTH],
    writer: &mut impl Write,
) -> Result<()> {
    // Compute all rows in parallel
    let all_rows: Vec<Vec<u8>> = (0..TIER1_ROWS)
        .into_par_iter()
        .map(|s| {
            let mut buf = vec![0u8; TIER1_ROW_BYTES];
            write_row(levels, ranges, empty_hashes, s, &mut buf);
            buf
        })
        .collect();

    // Write sequentially
    for row in &all_rows {
        writer.write_all(row)?;
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

    let mut offset = 0;

    // ── Internal nodes: relative depths 1 through 7 ──────────────────────
    //
    // Relative depth d corresponds to bottom-up level (bu_base - d).
    // At relative depth d, the subtree's nodes are at indices:
    //   s * 2^d .. s * 2^d + 2^d - 1
    for d in 1..TIER1_LAYERS {
        let bu_level = bu_base - d;
        let count = 1usize << d;
        let start = s * count;
        for i in 0..count {
            let val = node_or_empty(levels, bu_level, start + i, empty_hashes);
            write_fp(&mut buf[offset..], val);
            offset += 32;
        }
    }

    debug_assert_eq!(offset, TIER1_INTERNAL_NODES * 32);

    // ── Leaf records: 256 entries at relative depth 8 (depth 19) ─────────
    //
    // Bottom-up level = bu_base - TIER1_LAYERS = 15 - 8 = 7.
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

/// Parse a single Tier 1 row from raw bytes.
pub struct Tier1Row<'a> {
    data: &'a [u8],
}

impl<'a> Tier1Row<'a> {
    pub fn from_bytes(data: &'a [u8]) -> Result<Self> {
        anyhow::ensure!(
            data.len() == TIER1_ROW_BYTES,
            "Tier 1 row size mismatch: got {} bytes, expected {}",
            data.len(),
            TIER1_ROW_BYTES
        );
        for (i, chunk) in data.chunks_exact(32).enumerate() {
            crate::validate_fp_bytes(chunk).map_err(|e| {
                anyhow::anyhow!("Tier 1 row invalid field element at 32-byte chunk {}: {}", i, e)
            })?;
        }
        Ok(Self { data })
    }

    /// Internal node at relative depth d (1..7), position p (0..2^d - 1).
    pub fn internal_node(&self, rel_depth: usize, pos: usize) -> Fp {
        debug_assert!((1..TIER1_LAYERS).contains(&rel_depth));
        debug_assert!(pos < (1 << rel_depth));
        let bfs_idx = (1usize << rel_depth) - 2 + pos;
        let offset = bfs_idx * 32;
        crate::read_fp(&self.data[offset..offset + 32])
    }

    /// Leaf record at index i (0..255): (hash, min_key).
    pub fn leaf_record(&self, i: usize) -> (Fp, Fp) {
        debug_assert!(i < TIER1_LEAVES);
        let base = TIER1_INTERNAL_NODES * 32 + i * 64;
        let hash = crate::read_fp(&self.data[base..base + 32]);
        let min_key = crate::read_fp(&self.data[base + 32..base + 64]);
        (hash, min_key)
    }

    /// Binary search the 256 leaf min_keys to find which sub-subtree contains `value`.
    pub fn find_sub_subtree(&self, value: Fp) -> Option<usize> {
        let base = TIER1_INTERNAL_NODES * 32;
        let mut lo = 0usize;
        let mut hi = TIER1_LEAVES;
        while lo < hi {
            let mid = lo + (hi - lo) / 2;
            let mk_offset = base + mid * 64 + 32;
            let mk = crate::read_fp(&self.data[mk_offset..mk_offset + 32]);
            if mk <= value {
                lo = mid + 1;
            } else {
                hi = mid;
            }
        }
        if lo == 0 {
            None
        } else {
            Some(lo - 1)
        }
    }

    /// Extract the 8 sibling hashes from this Tier 1 row for a given sub-subtree index.
    ///
    /// Returns siblings at bottom-up levels 7..=14 (plan depths 19..=12).
    pub fn extract_siblings(&self, sub_idx: usize) -> [Fp; TIER1_LAYERS] {
        let mut siblings = [Fp::default(); TIER1_LAYERS];

        // Sibling at relative depth 8 (bottom-up level 7): from leaf records
        let sibling_leaf = sub_idx ^ 1;
        let (hash, _) = self.leaf_record(sibling_leaf);
        siblings[0] = hash; // path[7] in the full proof

        // Siblings at relative depths 7..=1 (bottom-up levels 8..=14)
        let mut pos = sub_idx;
        for rd in (1..TIER1_LAYERS).rev() {
            pos >>= 1;
            let sibling_pos = pos ^ 1;
            siblings[TIER1_LAYERS - rd] = self.internal_node(rd, sibling_pos);
        }

        siblings
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn from_bytes_rejects_non_canonical_field_element() {
        let mut row = vec![0u8; TIER1_ROW_BYTES];
        row[0..32].fill(0xFF);
        let err = Tier1Row::from_bytes(&row)
            .err()
            .expect("row should be rejected");
        assert!(
            err.to_string().contains("invalid field element"),
            "unexpected error: {err}"
        );
    }
}
