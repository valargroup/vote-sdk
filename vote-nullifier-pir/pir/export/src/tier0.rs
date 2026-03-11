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
    subtree_min_key, write_fp, write_internal_nodes, node_or_empty, PIR_DEPTH, TIER0_LAYERS,
    TIER1_ROWS,
};

pub use pir_types::tier0::{Tier0Data, TIER0_BYTES, TIER0_INTERNAL_NODES};

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

    // Depth 0 = root (not part of the generic subtree loop)
    write_fp(&mut buf[offset..], *root);
    offset += 32;

    // Depths 1 through 10 — the whole top of the tree is subtree 0 at bu_base = PIR_DEPTH.
    offset += write_internal_nodes(levels, empty_hashes, PIR_DEPTH, TIER0_LAYERS, 0, &mut buf[offset..]);

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
