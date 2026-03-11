//! Tier 1 reader: parse and query a single Tier 1 row.

use pasta_curves::Fp;

use crate::fp_utils::{binary_search_records, read_fp, validate_all_fp_chunks};
use crate::{TIER1_INTERNAL_NODES, TIER1_LAYERS, TIER1_LEAVES, TIER1_ROW_BYTES};

/// Parsed Tier 1 row: internal nodes (relative depths 1-6) and leaf records at relative depth 7.
pub struct Tier1Row<'a> {
    data: &'a [u8],
}

impl<'a> Tier1Row<'a> {
    pub fn from_bytes(data: &'a [u8]) -> anyhow::Result<Self> {
        anyhow::ensure!(
            data.len() == TIER1_ROW_BYTES,
            "Tier 1 row size mismatch: got {} bytes, expected {}",
            data.len(),
            TIER1_ROW_BYTES
        );
        validate_all_fp_chunks(data, "Tier 1 row")?;
        Ok(Self { data })
    }

    /// Internal node at relative depth d (1..6), position p (0..2^d - 1).
    pub fn internal_node(&self, rel_depth: usize, pos: usize) -> Fp {
        debug_assert!((1..TIER1_LAYERS).contains(&rel_depth));
        debug_assert!(pos < (1 << rel_depth));
        let bfs_idx = (1usize << rel_depth) - 2 + pos;
        let offset = bfs_idx * 32;
        read_fp(&self.data[offset..offset + 32])
    }

    /// Leaf record at index i (0..127): (hash, min_key).
    pub fn leaf_record(&self, i: usize) -> (Fp, Fp) {
        debug_assert!(i < TIER1_LEAVES);
        let base = TIER1_INTERNAL_NODES * 32 + i * 64;
        let hash = read_fp(&self.data[base..base + 32]);
        let min_key = read_fp(&self.data[base + 32..base + 64]);
        (hash, min_key)
    }

    /// Binary search the 128 leaf min_keys to find which sub-subtree contains `value`.
    pub fn find_sub_subtree(&self, value: Fp) -> Option<usize> {
        let base = TIER1_INTERNAL_NODES * 32;
        binary_search_records(self.data, base, TIER1_LEAVES, 64, 32, value)
    }

    /// Extract the 7 sibling hashes from this Tier 1 row for a given sub-subtree index.
    ///
    /// Returns siblings at bottom-up levels 8..=14 (plan depths 18..=12).
    pub fn extract_siblings(&self, sub_idx: usize) -> [Fp; TIER1_LAYERS] {
        let mut siblings = [Fp::default(); TIER1_LAYERS];

        let sibling_leaf = sub_idx ^ 1;
        let (hash, _) = self.leaf_record(sibling_leaf);
        siblings[0] = hash;

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

    #[test]
    fn from_bytes_rejects_wrong_size() {
        let short = vec![0u8; TIER1_ROW_BYTES - 1];
        assert!(Tier1Row::from_bytes(&short).is_err());
    }

    #[test]
    fn find_sub_subtree_on_all_zeros() {
        let row = vec![0u8; TIER1_ROW_BYTES];
        let tier1 = Tier1Row::from_bytes(&row).unwrap();
        let result = tier1.find_sub_subtree(Fp::from(42u64));
        assert!(result.is_some());
        assert!(result.unwrap() < TIER1_LEAVES);
    }

    #[test]
    fn extract_siblings_returns_correct_count() {
        let row = vec![0u8; TIER1_ROW_BYTES];
        let tier1 = Tier1Row::from_bytes(&row).unwrap();
        let siblings = tier1.extract_siblings(0);
        assert_eq!(siblings.len(), TIER1_LAYERS);
    }

    #[test]
    fn leaf_record_round_trip_on_zeros() {
        let row = vec![0u8; TIER1_ROW_BYTES];
        let tier1 = Tier1Row::from_bytes(&row).unwrap();
        let (hash, min_key) = tier1.leaf_record(0);
        assert_eq!(hash, Fp::zero());
        assert_eq!(min_key, Fp::zero());
    }

    #[test]
    fn internal_node_matches_at_depth_1() {
        let row = vec![0u8; TIER1_ROW_BYTES];
        let tier1 = Tier1Row::from_bytes(&row).unwrap();
        let node = tier1.internal_node(1, 0);
        assert_eq!(node, Fp::zero());
    }
}
