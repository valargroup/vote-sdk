//! Tier 2 reader: parse and query a single Tier 2 row.

use pasta_curves::Fp;

use crate::fp_utils::{binary_search_records, read_fp, validate_all_fp_chunks};
use crate::{TIER2_INTERNAL_NODES, TIER2_LAYERS, TIER2_LEAVES, TIER2_ROW_BYTES};

/// Parsed Tier 2 row: internal nodes (relative depths 1-7) and leaf records at relative depth 8.
pub struct Tier2Row<'a> {
    data: &'a [u8],
}

impl<'a> Tier2Row<'a> {
    pub fn from_bytes(data: &'a [u8]) -> anyhow::Result<Self> {
        anyhow::ensure!(
            data.len() == TIER2_ROW_BYTES,
            "Tier 2 row size mismatch: got {} bytes, expected {}",
            data.len(),
            TIER2_ROW_BYTES
        );
        validate_all_fp_chunks(data, "Tier 2 row")?;
        Ok(Self { data })
    }

    /// Internal node at relative depth d (1..7), position p (0..2^d - 1).
    pub fn internal_node(&self, rel_depth: usize, pos: usize) -> Fp {
        debug_assert!((1..TIER2_LAYERS).contains(&rel_depth));
        debug_assert!(pos < (1 << rel_depth));
        let bfs_idx = (1usize << rel_depth) - 2 + pos;
        let offset = bfs_idx * 32;
        read_fp(&self.data[offset..offset + 32])
    }

    /// Leaf record at index i (0..255): (key=low, value=width).
    pub fn leaf_record(&self, i: usize) -> (Fp, Fp) {
        debug_assert!(i < TIER2_LEAVES);
        let base = TIER2_INTERNAL_NODES * 32 + i * 64;
        let key = read_fp(&self.data[base..base + 32]);
        let value = read_fp(&self.data[base + 32..base + 64]);
        (key, value)
    }

    /// Find the leaf containing `value` among the populated leaf records.
    ///
    /// Uses binary search on `low` values. Returns `Some(index)` if found,
    /// `None` if value is an existing nullifier.
    pub fn find_leaf(&self, value: Fp, valid_leaves: usize) -> Option<usize> {
        debug_assert!(valid_leaves <= TIER2_LEAVES);
        if valid_leaves == 0 {
            return None;
        }
        let base = TIER2_INTERNAL_NODES * 32;
        let idx = binary_search_records(self.data, base, valid_leaves, 64, 0, value)?;

        let (low, width) = self.leaf_record(idx);
        if value - low <= width { Some(idx) } else { None }
    }

    /// Extract the 8 sibling hashes from this Tier 2 row for a given leaf index.
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

        let sibling_leaf_idx = leaf_idx ^ 1;
        siblings[0] = if sibling_leaf_idx < valid_leaves {
            let (sib_low, sib_width) = self.leaf_record(sibling_leaf_idx);
            hasher.hash(sib_low, sib_width)
        } else {
            hasher.hash(Fp::zero(), Fp::zero())
        };

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
    use crate::fp_utils::write_fp;
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

        write_fp(&mut row[base..base + 32], Fp::one());
        write_fp(&mut row[base + 32..base + 64], Fp::from(3u64));

        write_fp(&mut row[base + 64..base + 96], -Fp::one());
        write_fp(&mut row[base + 96..base + 128], Fp::zero());

        let tier2 = Tier2Row::from_bytes(&row).expect("valid synthetic Tier 2 row");
        let hasher = PoseidonHasher::new();
        let empty_leaf_hash = hasher.hash(Fp::zero(), Fp::zero());
        let p_minus_one_leaf_hash = hasher.hash(-Fp::one(), Fp::zero());

        let idx = tier2.find_leaf(-Fp::one(), 2).expect("p-1 leaf should be found");
        assert_eq!(idx, 1);
        let sibs_for_leaf0 = tier2.extract_siblings(0, 2, &hasher);
        assert_eq!(sibs_for_leaf0[0], p_minus_one_leaf_hash);

        assert!(tier2.find_leaf(-Fp::one(), 1).is_none());
        let sibs_for_leaf0_partial = tier2.extract_siblings(0, 1, &hasher);
        assert_eq!(sibs_for_leaf0_partial[0], empty_leaf_hash);
    }
}
