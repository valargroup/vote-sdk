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
    node_or_empty, write_fp, PIR_DEPTH, TIER0_LAYERS, TIER1_LAYERS, TIER2_INTERNAL_NODES,
    TIER2_LAYERS, TIER2_LEAVES, TIER2_ROWS, TIER2_ROW_BYTES,
};

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
        if s > 0 && s % 100_000 == 0 {
            eprintln!("    Tier 2 progress: {}/{} rows", s, TIER2_ROWS);
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

    let mut offset = 0;

    // ── Internal nodes: relative depths 1 through 6 ──────────────────────
    //
    // Relative depth d corresponds to bottom-up level (bu_base - d).
    // At relative depth d, the subtree's nodes are at indices:
    //   s * 2^d .. s * 2^d + 2^d - 1
    for d in 1..TIER2_LAYERS {
        let bu_level = bu_base - d;
        let count = 1usize << d;
        let start = s * count;
        for i in 0..count {
            let val = node_or_empty(levels, bu_level, start + i, empty_hashes);
            write_fp(&mut buf[offset..], val);
            offset += 32;
        }
    }

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
            // Empty padding leaf: key=0, value=0.
            // Callers must bound lookups with `valid_leaves` so padding entries
            // are never interpreted as real ranges.
            write_fp(&mut buf[offset..], Fp::zero());
            offset += 32;
            write_fp(&mut buf[offset..], Fp::zero());
            offset += 32;
        }
    }

    debug_assert_eq!(offset, TIER2_ROW_BYTES);
}

/// Parse a single Tier 2 row from raw bytes.
pub struct Tier2Row<'a> {
    data: &'a [u8],
}

impl<'a> Tier2Row<'a> {
    pub fn from_bytes(data: &'a [u8]) -> Result<Self> {
        anyhow::ensure!(
            data.len() == TIER2_ROW_BYTES,
            "Tier 2 row size mismatch: got {} bytes, expected {}",
            data.len(),
            TIER2_ROW_BYTES
        );
        crate::validate_all_fp_chunks(data, "Tier 2 row")?;
        Ok(Self { data })
    }

    /// Internal node at relative depth d (1..7), position p (0..2^d - 1).
    pub fn internal_node(&self, rel_depth: usize, pos: usize) -> Fp {
        debug_assert!((1..TIER2_LAYERS).contains(&rel_depth));
        debug_assert!(pos < (1 << rel_depth));
        let bfs_idx = (1usize << rel_depth) - 2 + pos;
        let offset = bfs_idx * 32;
        crate::read_fp(&self.data[offset..offset + 32])
    }

    /// Leaf record at index i (0..255): (key=low, value=width).
    pub fn leaf_record(&self, i: usize) -> (Fp, Fp) {
        debug_assert!(i < TIER2_LEAVES);
        let base = TIER2_INTERNAL_NODES * 32 + i * 64;
        let key = crate::read_fp(&self.data[base..base + 32]);
        let value = crate::read_fp(&self.data[base + 32..base + 64]);
        (key, value)
    }

    /// Find the leaf containing `value` among the populated leaf records.
    ///
    /// Uses binary search on `low` values (same logic as `find_range_for_value`).
    /// Returns `Some(index)` if found, `None` if value is an existing nullifier.
    pub fn find_leaf(&self, value: Fp, valid_leaves: usize) -> Option<usize> {
        debug_assert!(valid_leaves <= TIER2_LEAVES);
        if valid_leaves == 0 {
            return None;
        }
        let base = TIER2_INTERNAL_NODES * 32;
        let idx = crate::binary_search_records(self.data, base, valid_leaves, 64, 0, value)?;

        // Verify value is within the range: value - low <= width
        let (low, width) = self.leaf_record(idx);
        if value - low <= width { Some(idx) } else { None }
    }

    /// Extract the 8 sibling hashes from this Tier 2 row for a given leaf index.
    ///
    /// Returns siblings at bottom-up levels 0..=7 (plan depths 26..=19).
    ///
    /// The sibling at the leaf level (bottom-up 0) is computed from the sibling leaf
    /// record when that sibling is populated, otherwise it uses the empty-leaf hash.
    pub fn extract_siblings(
        &self,
        leaf_idx: usize,
        valid_leaves: usize,
        hasher: &imt_tree::hasher::PoseidonHasher,
    ) -> [Fp; TIER2_LAYERS] {
        debug_assert!(valid_leaves <= TIER2_LEAVES);
        let mut siblings = [Fp::default(); TIER2_LAYERS];

        // Sibling at the leaf level (bottom-up 0)
        let sibling_leaf_idx = leaf_idx ^ 1;
        siblings[0] = if sibling_leaf_idx < valid_leaves {
            let (sib_low, sib_width) = self.leaf_record(sibling_leaf_idx);
            hasher.hash(sib_low, sib_width)
        } else {
            hasher.hash(Fp::zero(), Fp::zero())
        };

        // Siblings at relative depths 6..=1 (bottom-up levels 1..=6)
        let mut pos = leaf_idx;
        for rd in (1..TIER2_LAYERS).rev() {
            pos >>= 1;
            let sibling_pos = pos ^ 1;
            siblings[TIER2_LAYERS - rd] = self.internal_node(rd, sibling_pos);
        }

        siblings
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use imt_tree::hasher::PoseidonHasher;

    #[test]
    fn from_bytes_rejects_non_canonical_field_element() {
        let mut row = vec![0u8; TIER2_ROW_BYTES];
        row[0..32].fill(0xFF);
        let err = Tier2Row::from_bytes(&row)
            .err()
            .expect("row should be rejected");
        assert!(
            err.to_string().contains("invalid field element"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn partial_row_handles_p_minus_one_leaf_without_padding_collision() {
        let mut row = vec![0u8; TIER2_ROW_BYTES];
        let base = TIER2_INTERNAL_NODES * 32;

        // leaf[0] = [1, 3]
        crate::write_fp(&mut row[base..base + 32], Fp::one());
        crate::write_fp(&mut row[base + 32..base + 64], Fp::from(3u64));

        // leaf[1] = [p-1, 0] (a valid real range in edge cases)
        crate::write_fp(&mut row[base + 64..base + 96], -Fp::one());
        crate::write_fp(&mut row[base + 96..base + 128], Fp::zero());

        let tier2 = Tier2Row::from_bytes(&row).expect("valid synthetic Tier 2 row");
        let hasher = PoseidonHasher::new();
        let empty_leaf_hash = hasher.hash(Fp::zero(), Fp::zero());
        let p_minus_one_leaf_hash = hasher.hash(-Fp::one(), Fp::zero());

        // With 2 valid leaves, p-1 leaf must be discoverable and hashed as real data.
        let idx = tier2.find_leaf(-Fp::one(), 2).expect("p-1 leaf should be found");
        assert_eq!(idx, 1);
        let sibs_for_leaf0 = tier2.extract_siblings(0, 2, &hasher);
        assert_eq!(sibs_for_leaf0[0], p_minus_one_leaf_hash);

        // With only 1 valid leaf, sibling at index 1 is padding and must hash as empty.
        assert!(tier2.find_leaf(-Fp::one(), 1).is_none());
        let sibs_for_leaf0_partial = tier2.extract_siblings(0, 1, &hasher);
        assert_eq!(sibs_for_leaf0_partial[0], empty_leaf_hash);
    }
}
