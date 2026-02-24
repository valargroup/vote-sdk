//! Tier 0 export: plaintext internal nodes (depths 0-10) + subtree records at depth 11.
//!
//! Layout (196,576 bytes):
//! ```text
//! [depth 0: 1 × 32 bytes (root)]
//! [depth 1: 2 × 32 bytes]
//! [depth 2: 4 × 32 bytes]
//! ...
//! [depth 10: 1024 × 32 bytes]
//! [subtree records: 2048 × (32-byte hash + 32-byte min_key)]
//! ```
//!
//! BFS position of node at depth d, index i: `(2^d - 1) + i`.
//! Byte offset: `((2^d - 1) + i) * 32`.

use pasta_curves::Fp;

use imt_tree::tree::{Range, TREE_DEPTH};

use crate::{
    node_or_empty, subtree_min_key, write_fp, PIR_DEPTH, TIER0_LAYERS, TIER1_ROWS,
};

/// Number of internal nodes in Tier 0 (depths 0-10): 2^0 + 2^1 + ... + 2^10 = 2047.
const TIER0_INTERNAL_NODES: usize = (1 << TIER0_LAYERS) - 1; // 2047

/// Total size of Tier 0 data.
pub const TIER0_BYTES: usize = TIER0_INTERNAL_NODES * 32 + TIER1_ROWS * 64; // 196,576

/// Export Tier 0 as a flat binary blob.
///
/// The returned Vec contains all internal node hashes (depths 0-10 in BFS order)
/// followed by 2048 subtree records (hash + min_key) at depth 11.
pub fn export(
    root: &Fp,
    levels: &[Vec<Fp>],
    ranges: &[Range],
    empty_hashes: &[Fp; TREE_DEPTH],
) -> Vec<u8> {
    let mut buf = vec![0u8; TIER0_BYTES];
    let mut offset = 0;

    // ── Internal nodes: depths 0 through 10 ──────────────────────────────

    // Depth 0 = root
    write_fp(&mut buf[offset..], *root);
    offset += 32;

    // Depths 1 through 10.
    // Depth d in plan notation (top-down) corresponds to bottom-up level (PIR_DEPTH - d).
    for d in 1..=10 {
        let bu_level = PIR_DEPTH - d; // bottom-up level
        let count = 1usize << d;
        for i in 0..count {
            let val = node_or_empty(levels, bu_level, i, empty_hashes);
            write_fp(&mut buf[offset..], val);
            offset += 32;
        }
    }

    debug_assert_eq!(offset, TIER0_INTERNAL_NODES * 32);

    // ── Subtree records at depth 11 ──────────────────────────────────────
    //
    // Each record: 32-byte hash (the node hash at depth 11) + 32-byte min_key.
    // The hash is at bottom-up level (PIR_DEPTH - 11) = 15.
    let bu_level_11 = PIR_DEPTH - TIER0_LAYERS; // 15

    for s in 0..TIER1_ROWS {
        // Hash of the depth-11 subtree root
        let hash = node_or_empty(levels, bu_level_11, s, empty_hashes);
        write_fp(&mut buf[offset..], hash);
        offset += 32;

        // min_key: smallest `low` among all leaves in this subtree.
        // Each depth-11 subtree covers 2^(PIR_DEPTH - TIER0_LAYERS) = 2^15 = 32,768 leaves.
        let leaf_start = s * (1 << (PIR_DEPTH - TIER0_LAYERS));
        let mk = subtree_min_key(ranges, leaf_start);
        write_fp(&mut buf[offset..], mk);
        offset += 32;
    }

    debug_assert_eq!(offset, TIER0_BYTES);
    buf
}

/// Parse Tier 0 data: extract the root, internal node hashes, and subtree records.
pub struct Tier0Data {
    data: Vec<u8>,
}

impl Tier0Data {
    pub fn from_bytes(data: Vec<u8>) -> anyhow::Result<Self> {
        anyhow::ensure!(
            data.len() == TIER0_BYTES,
            "Tier 0 data size mismatch: got {} bytes, expected {}",
            data.len(),
            TIER0_BYTES
        );
        for (i, chunk) in data.chunks_exact(32).enumerate() {
            crate::validate_fp_bytes(chunk).map_err(|e| {
                anyhow::anyhow!("Tier 0 invalid field element at 32-byte chunk {}: {}", i, e)
            })?;
        }
        Ok(Self { data })
    }

    /// Root hash (depth 0).
    pub fn root(&self) -> Fp {
        crate::read_fp(&self.data[0..32])
    }

    /// Internal node hash at the given top-down depth and index.
    /// Valid for depth 0..=10.
    pub fn node_at(&self, depth: usize, index: usize) -> Fp {
        debug_assert!(depth <= 10);
        debug_assert!(index < (1 << depth));
        let bfs_pos = (1usize << depth) - 1 + index;
        let offset = bfs_pos * 32;
        crate::read_fp(&self.data[offset..offset + 32])
    }

    /// Number of subtree records (always 2048).
    pub fn num_subtrees(&self) -> usize {
        TIER1_ROWS
    }

    /// Subtree record at depth 11: (hash, min_key).
    pub fn subtree_record(&self, index: usize) -> (Fp, Fp) {
        debug_assert!(index < TIER1_ROWS);
        let base = TIER0_INTERNAL_NODES * 32 + index * 64;
        let hash = crate::read_fp(&self.data[base..base + 32]);
        let min_key = crate::read_fp(&self.data[base + 32..base + 64]);
        (hash, min_key)
    }

    /// Binary search the 2048 subtree min_keys to find which subtree contains `value`.
    ///
    /// Returns the subtree index (0..2047) or None if value is beyond all ranges.
    pub fn find_subtree(&self, value: Fp) -> Option<usize> {
        let base = TIER0_INTERNAL_NODES * 32;
        // Manual binary search: find last index where min_key ≤ value
        let mut lo = 0usize;
        let mut hi = TIER1_ROWS;
        while lo < hi {
            let mid = lo + (hi - lo) / 2;
            let mk = crate::read_fp(&self.data[base + mid * 64 + 32..base + mid * 64 + 64]);
            if mk <= value {
                lo = mid + 1;
            } else {
                hi = mid;
            }
        }
        // lo = first index where min_key > value
        if lo == 0 {
            None
        } else {
            Some(lo - 1)
        }
    }

    /// Extract the 11 sibling hashes from Tier 0 for a given depth-11 subtree index.
    ///
    /// Returns siblings at bottom-up levels 15..=25 (plan depths 11..=1).
    pub fn extract_siblings(&self, subtree_idx: usize) -> [Fp; TIER0_LAYERS_COUNT] {
        let mut siblings = [Fp::default(); TIER0_LAYERS_COUNT];

        // Sibling at depth 11 (bottom-up level 15): from subtree records
        let sibling_11 = subtree_idx ^ 1;
        let (hash, _) = self.subtree_record(sibling_11);
        siblings[0] = hash; // path[15] in the full proof

        // Siblings at depths 10..=1 (bottom-up levels 16..=25)
        let mut pos = subtree_idx;
        for d in (1..=10).rev() {
            pos >>= 1;
            let sibling_pos = pos ^ 1;
            siblings[TIER0_LAYERS_COUNT - d] = self.node_at(d, sibling_pos);
        }

        siblings
    }
}

/// Number of siblings extracted from Tier 0 (depths 1-11 = 11 siblings).
const TIER0_LAYERS_COUNT: usize = TIER0_LAYERS; // 11
