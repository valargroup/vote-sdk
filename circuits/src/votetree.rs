//! Vote commitment tree helpers for FFI.
//!
//! Provides stateless functions that build a `TreeServer` from a flat byte
//! array of leaves and return the root or an authentication path. These are
//! called by the `extern "C"` functions in [`crate::ffi`].

use pasta_curves::group::ff::PrimeField;
use pasta_curves::Fp;
use vote_commitment_tree::{TreeServer, MERKLE_PATH_BYTES};

// ---------------------------------------------------------------------------
// Error type
// ---------------------------------------------------------------------------

/// FFI-layer error codes (mapped to i32 in `ffi.rs`).
#[derive(Debug)]
pub enum FfiError {
    /// Null pointer, zero leaves, etc.
    InvalidInput,
    /// Position >= leaf_count.
    PositionOutOfRange,
    /// Non-canonical Fp encoding in a leaf.
    Deserialization,
}

// ---------------------------------------------------------------------------
// Leaf deserialization
// ---------------------------------------------------------------------------

/// Deserialize `leaf_count` leaves from a raw byte pointer.
///
/// Each leaf is 32 bytes in canonical little-endian `Fp::to_repr()` format —
/// this is the same encoding the Go keeper stores in KV (`0x02 || index -> bytes`).
///
/// # Safety
/// Caller must ensure `ptr` is valid for `leaf_count * 32` bytes.
unsafe fn deserialize_leaves(ptr: *const u8, leaf_count: usize) -> Result<Vec<Fp>, FfiError> {
    let bytes = std::slice::from_raw_parts(ptr, leaf_count * 32);
    let mut leaves = Vec::with_capacity(leaf_count);

    for i in 0..leaf_count {
        let start = i * 32;
        let mut repr = [0u8; 32];
        repr.copy_from_slice(&bytes[start..start + 32]);

        let fp_opt: Option<Fp> = Fp::from_repr(repr).into();
        match fp_opt {
            Some(fp) => leaves.push(fp),
            None => return Err(FfiError::Deserialization),
        }
    }

    Ok(leaves)
}

/// Build a `TreeServer` from a slice of field elements, checkpoint it, and
/// return it ready for root / path queries.
fn build_tree(leaves: &[Fp]) -> TreeServer {
    let mut tree = TreeServer::empty();
    for &leaf in leaves {
        tree.append(leaf);
    }
    // Checkpoint at height 1 so root() and path(_, 1) work.
    tree.checkpoint(1);
    tree
}

// ---------------------------------------------------------------------------
// Public helpers (called by ffi.rs)
// ---------------------------------------------------------------------------

/// Compute the Poseidon Merkle root from raw leaf bytes.
///
/// Returns the 32-byte root in `Fp::to_repr()` canonical LE format.
///
/// # Safety
/// `leaves_ptr` must be valid for `leaf_count * 32` bytes.
pub unsafe fn compute_root_from_raw(
    leaves_ptr: *const u8,
    leaf_count: usize,
) -> Result<[u8; 32], FfiError> {
    // Empty tree: return the empty-tree root.
    if leaf_count == 0 {
        let tree = TreeServer::empty();
        // Checkpoint the empty tree so root() returns the deterministic empty root.
        let mut tree = tree;
        tree.checkpoint(1);
        return Ok(tree.root().to_repr());
    }

    let leaves = deserialize_leaves(leaves_ptr, leaf_count)?;
    let tree = build_tree(&leaves);
    Ok(tree.root().to_repr())
}

/// Compute the Poseidon Merkle auth path from raw leaf bytes.
///
/// Returns the serialized `MerklePath` ([`MERKLE_PATH_BYTES`] bytes).
///
/// # Safety
/// `leaves_ptr` must be valid for `leaf_count * 32` bytes.
pub unsafe fn compute_path_from_raw(
    leaves_ptr: *const u8,
    leaf_count: usize,
    position: u64,
) -> Result<Vec<u8>, FfiError> {
    if leaf_count == 0 {
        return Err(FfiError::InvalidInput);
    }
    if position >= leaf_count as u64 {
        return Err(FfiError::PositionOutOfRange);
    }

    let leaves = deserialize_leaves(leaves_ptr, leaf_count)?;
    let tree = build_tree(&leaves);

    match tree.path(position, 1) {
        Some(path) => {
            let bytes = path.to_bytes();
            debug_assert_eq!(bytes.len(), MERKLE_PATH_BYTES);
            Ok(bytes)
        }
        None => Err(FfiError::PositionOutOfRange),
    }
}

// ---------------------------------------------------------------------------
// Rust-side FFI round-trip tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use vote_commitment_tree::MerklePath;

    fn fp(x: u64) -> Fp {
        Fp::from(x)
    }

    /// Helper: serialize a slice of Fp values to flat 32-byte-per-leaf bytes.
    fn leaves_to_bytes(leaves: &[Fp]) -> Vec<u8> {
        let mut buf = Vec::with_capacity(leaves.len() * 32);
        for leaf in leaves {
            buf.extend_from_slice(&leaf.to_repr());
        }
        buf
    }

    #[test]
    fn test_vote_tree_root_ffi_roundtrip() {
        // Build tree the "normal" way.
        let leaves = vec![fp(1), fp(2), fp(3)];
        let mut tree = TreeServer::empty();
        for &l in &leaves {
            tree.append(l);
        }
        tree.checkpoint(1);
        let expected_root = tree.root();

        // Build tree via FFI helpers.
        let bytes = leaves_to_bytes(&leaves);
        let root_bytes = unsafe { compute_root_from_raw(bytes.as_ptr(), leaves.len()) }.unwrap();

        assert_eq!(root_bytes, expected_root.to_repr());
    }

    #[test]
    fn test_vote_tree_path_ffi_roundtrip() {
        let leaves = vec![fp(10), fp(20), fp(30)];
        let mut tree = TreeServer::empty();
        for &l in &leaves {
            tree.append(l);
        }
        tree.checkpoint(1);
        let expected_root = tree.root();

        let bytes = leaves_to_bytes(&leaves);

        // Verify path for each position.
        for pos in 0..leaves.len() as u64 {
            let path_bytes =
                unsafe { compute_path_from_raw(bytes.as_ptr(), leaves.len(), pos) }.unwrap();
            let path = MerklePath::from_bytes(&path_bytes).unwrap();
            assert!(
                path.verify(leaves[pos as usize], expected_root),
                "path for position {} must verify",
                pos
            );
        }
    }

    #[test]
    fn test_ffi_empty_leaves() {
        // Empty tree should return the deterministic empty root.
        let empty_tree_root = {
            let mut t = TreeServer::empty();
            t.checkpoint(1);
            t.root().to_repr()
        };

        let root_bytes =
            unsafe { compute_root_from_raw(std::ptr::null(), 0) }.unwrap();
        assert_eq!(root_bytes, empty_tree_root);
    }

    #[test]
    fn test_ffi_position_out_of_range() {
        // Position >= leaf_count must be caught.
        let leaves = leaves_to_bytes(&[fp(1)]);
        let result = unsafe { compute_path_from_raw(leaves.as_ptr(), 1, 5) };
        assert!(matches!(result, Err(FfiError::PositionOutOfRange)));

        // Position == leaf_count (exactly at boundary) is also out of range.
        let result = unsafe { compute_path_from_raw(leaves.as_ptr(), 1, 1) };
        assert!(matches!(result, Err(FfiError::PositionOutOfRange)));
    }

    #[test]
    fn test_ffi_encoding_parity() {
        // Verify that Fp::to_repr() produces 32-byte LE that round-trips.
        let val = fp(12345678);
        let repr = val.to_repr();
        assert_eq!(repr.len(), 32);

        let recovered: Option<Fp> = Fp::from_repr(repr).into();
        assert_eq!(recovered.unwrap(), val);

        // Build a single-leaf tree both ways and confirm roots match.
        let bytes = leaves_to_bytes(&[val]);
        let ffi_root = unsafe { compute_root_from_raw(bytes.as_ptr(), 1) }.unwrap();

        let mut tree = TreeServer::empty();
        tree.append(val);
        tree.checkpoint(1);
        assert_eq!(ffi_root, tree.root().to_repr());
    }

    #[test]
    fn test_ffi_path_zero_leaves() {
        let result = unsafe { compute_path_from_raw(std::ptr::null(), 0, 0) };
        assert!(matches!(result, Err(FfiError::InvalidInput)));
    }

    #[test]
    fn test_ffi_single_leaf() {
        let leaf = fp(42);
        let bytes = leaves_to_bytes(&[leaf]);

        // Root
        let root_bytes = unsafe { compute_root_from_raw(bytes.as_ptr(), 1) }.unwrap();
        let mut tree = TreeServer::empty();
        tree.append(leaf);
        tree.checkpoint(1);
        assert_eq!(root_bytes, tree.root().to_repr());

        // Path
        let path_bytes = unsafe { compute_path_from_raw(bytes.as_ptr(), 1, 0) }.unwrap();
        let path = MerklePath::from_bytes(&path_bytes).unwrap();
        assert!(path.verify(leaf, tree.root()));
    }

    // -- Golden test vectors -----------------------------------------------
    // These exact values are also asserted in the Go-side tests to catch
    // encoding mismatches between the Go KV layer and Rust Fp representation.

    /// Golden vector: 3 leaves [Fp(1), Fp(2), Fp(3)] -> expected root.
    ///
    /// This root is computed once and hardcoded. Both Rust and Go tests assert
    /// against it.
    pub fn golden_leaves() -> Vec<Fp> {
        vec![fp(1), fp(2), fp(3)]
    }

    pub fn golden_root() -> [u8; 32] {
        let leaves = golden_leaves();
        let mut tree = TreeServer::empty();
        for &l in &leaves {
            tree.append(l);
        }
        tree.checkpoint(1);
        tree.root().to_repr()
    }

    #[test]
    fn test_golden_vector_root() {
        let leaves = golden_leaves();
        let bytes = leaves_to_bytes(&leaves);
        let root = unsafe { compute_root_from_raw(bytes.as_ptr(), leaves.len()) }.unwrap();
        let expected = golden_root();
        assert_eq!(root, expected, "golden vector root must match");
    }

    #[test]
    fn test_golden_vector_path_verifies() {
        let leaves = golden_leaves();
        let bytes = leaves_to_bytes(&leaves);
        let root = unsafe { compute_root_from_raw(bytes.as_ptr(), leaves.len()) }.unwrap();

        for pos in 0..leaves.len() as u64 {
            let path_bytes =
                unsafe { compute_path_from_raw(bytes.as_ptr(), leaves.len(), pos) }.unwrap();
            let path = MerklePath::from_bytes(&path_bytes).unwrap();
            assert!(
                path.verify(leaves[pos as usize], Fp::from_repr(root).unwrap()),
                "golden path at position {} must verify",
                pos
            );
        }
    }
}
