//! Vote commitment tree helpers for FFI.
//!
//! Provides stateless functions that build a `MemoryTreeServer` from a flat
//! byte array of leaves and return the root or an authentication path. These
//! are called by the `extern "C"` functions in [`crate::ffi`] (test-only).
//!
//! The stateful [`TreeHandle`] wraps a [`TreeServer`] backed by a
//! [`KvShardStore`] with live Go KV callbacks.

use pasta_curves::group::ff::PrimeField;
use pasta_curves::Fp;
pub use vote_commitment_tree::MERKLE_PATH_BYTES;
use vote_commitment_tree::{MemoryTreeServer, TreeServer};
pub use vote_commitment_tree::kv_shard_store::KvCallbacks;

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
    /// KV store or ShardTree storage failure.
    Storage,
}

// ---------------------------------------------------------------------------
// Leaf deserialization
// ---------------------------------------------------------------------------

/// Deserialize `leaf_count` leaves from a raw byte pointer.
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

/// Build an in-memory tree from a slice of field elements, checkpoint it, and
/// return it ready for root / path queries.
fn build_tree(leaves: &[Fp]) -> MemoryTreeServer {
    let mut tree = MemoryTreeServer::empty();
    for &leaf in leaves {
        tree.append(leaf).expect("append to in-memory tree must succeed");
    }
    tree.checkpoint(1).expect("checkpoint of in-memory tree must succeed");
    tree
}

// ---------------------------------------------------------------------------
// Public helpers (called by ffi.rs, test-only)
// ---------------------------------------------------------------------------

/// Compute the Poseidon Merkle root from raw leaf bytes.
///
/// Stateless helper: builds a fresh in-memory tree on every call (O(n)).
/// For repeated calls across blocks use the stateful [`TreeHandle`] API.
///
/// # Safety
/// `leaves_ptr` must be valid for `leaf_count * 32` bytes.
pub unsafe fn compute_root_from_raw(
    leaves_ptr: *const u8,
    leaf_count: usize,
) -> Result<[u8; 32], FfiError> {
    if leaf_count == 0 {
        let mut tree = MemoryTreeServer::empty();
        tree.checkpoint(1).expect("checkpoint must succeed");
        return Ok(tree.root().to_repr());
    }
    let leaves = deserialize_leaves(leaves_ptr, leaf_count)?;
    let tree = build_tree(&leaves);
    Ok(tree.root().to_repr())
}

/// Compute the Poseidon Merkle auth path from raw leaf bytes.
///
/// Stateless helper: builds a fresh in-memory tree on every call (O(n)).
/// For repeated calls across blocks use the stateful [`TreeHandle::path`] API.
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
// Stateful tree handle
// ---------------------------------------------------------------------------

/// Stateful Poseidon Merkle tree handle backed by the Cosmos KV store.
///
/// Wraps a [`TreeServer`] with a [`KvShardStore`] that calls back into Go for
/// every shard read/write, giving `ShardTree` true lazy loading. The Go keeper
/// holds one instance per process and calls `new_with_kv` once per cold start
/// (or rollback), passing the current block's KV proxy and leaf count.
pub struct TreeHandle {
    tree: TreeServer,
}

impl TreeHandle {
    /// Create a KV-backed tree handle.
    ///
    /// `cb` holds C function pointers to the Go KV store proxy.
    /// `next_position` is `CommitmentTreeState.NextIndex` from KV (0 on first boot).
    /// `latest_checkpoint` is initialised from the KV store's maximum checkpoint ID
    /// so that `root()` is correct even before the first checkpoint after restart.
    pub fn new_with_kv(cb: KvCallbacks, next_position: u64) -> Box<TreeHandle> {
        Box::new(TreeHandle {
            tree: TreeServer::new(cb, next_position),
        })
    }

    /// Append a batch of leaves (each 32-byte canonical LE `Fp`).
    ///
    /// # Safety
    /// `ptr` must be valid for `count * 32` bytes.
    pub unsafe fn append_batch_raw(&mut self, ptr: *const u8, count: usize) -> Result<(), FfiError> {
        if count == 0 {
            return Ok(());
        }
        if ptr.is_null() {
            return Err(FfiError::InvalidInput);
        }
        let leaves = deserialize_leaves(ptr, count)?;
        for leaf in leaves {
            self.tree.append(leaf).map_err(|_| FfiError::Storage)?;
        }
        Ok(())
    }

    /// Append `count` leaves starting at `cursor` by reading them directly
    /// from the Cosmos KV store via KV callbacks.
    ///
    /// This skips the Go-side leaf fetch loop and CGO serialization, replacing
    /// the `newLeaves` allocation in `ensureTreeLoaded` with a single CGO call.
    pub fn append_from_kv(&mut self, cursor: u64, count: u64) -> Result<(), FfiError> {
        self.tree
            .append_from_kv(cursor, count)
            .map_err(|_| FfiError::Storage)
    }

    /// Snapshot the current tree state at `height` (block height).
    pub fn checkpoint(&mut self, height: u32) -> Result<(), FfiError> {
        self.tree
            .checkpoint(height)
            .map_err(|_| FfiError::Storage)
    }

    /// Reset the in-memory `latest_checkpoint` to `None` without touching the
    /// KV store. Used after handle recreation on rollback, where the KV store
    /// still contains checkpoints from the pre-rollback state. Clearing
    /// `latest_checkpoint` allows a fresh `Checkpoint(M)` call to establish
    /// the correct root for the rolled-back height M without triggering the
    /// monotonicity assertion.
    pub fn clear_checkpoint(&mut self) {
        self.tree.clear_latest_checkpoint();
    }

    /// Delete all tree-related KV data (shards, cap, checkpoints) through
    /// this handle's callbacks.
    ///
    /// The Go keeper calls this on the old handle just before closing it on
    /// rollback, so that the fresh handle created at `next_position = 0` sees
    /// an empty KV state and does not read stale pre-rollback shard data when
    /// `AppendFromKV` re-inserts the rolled-back leaf range.
    ///
    /// Returns `Err(FfiError::Storage)` if any KV callback fails.
    pub fn truncate_kv_data(&mut self) -> Result<(), FfiError> {
        self.tree
            .truncate_kv_data()
            .map_err(|_| FfiError::Storage)
    }

    /// Return the 32-byte Merkle root at the latest checkpoint.
    pub fn root(&self) -> [u8; 32] {
        self.tree.root().to_repr()
    }

    /// Return the number of leaves appended so far.
    pub fn size(&self) -> u64 {
        self.tree.size()
    }

    /// Return the serialized Merkle authentication path for `position` at
    /// `height`. Returns `None` if position is out of range or height has no
    /// checkpoint.
    pub fn path(&self, position: u64, height: u32) -> Option<Vec<u8>> {
        let path = self.tree.path(position, height)?;
        let bytes = path.to_bytes();
        debug_assert_eq!(bytes.len(), MERKLE_PATH_BYTES);
        Some(bytes)
    }

    /// Set the leaf count directly (used if `next_position` was not passed at
    /// construction time).
    pub fn set_next_position(&mut self, pos: u64) {
        self.tree.set_next_position(pos);
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

    fn leaves_to_bytes(leaves: &[Fp]) -> Vec<u8> {
        let mut buf = Vec::with_capacity(leaves.len() * 32);
        for leaf in leaves {
            buf.extend_from_slice(&leaf.to_repr());
        }
        buf
    }

    #[test]
    fn test_vote_tree_root_ffi_roundtrip() {
        let leaves = vec![fp(1), fp(2), fp(3)];
        let mut tree = MemoryTreeServer::empty();
        for &l in &leaves {
            tree.append(l).unwrap();
        }
        tree.checkpoint(1).unwrap();
        let expected_root = tree.root();

        let bytes = leaves_to_bytes(&leaves);
        let root_bytes = unsafe { compute_root_from_raw(bytes.as_ptr(), leaves.len()) }.unwrap();

        assert_eq!(root_bytes, expected_root.to_repr());
    }

    #[test]
    fn test_vote_tree_path_ffi_roundtrip() {
        let leaves = vec![fp(10), fp(20), fp(30)];
        let mut tree = MemoryTreeServer::empty();
        for &l in &leaves {
            tree.append(l).unwrap();
        }
        tree.checkpoint(1).unwrap();
        let expected_root = tree.root();

        let bytes = leaves_to_bytes(&leaves);
        for pos in 0..leaves.len() as u64 {
            let path_bytes =
                unsafe { compute_path_from_raw(bytes.as_ptr(), leaves.len(), pos) }.unwrap();
            let path = MerklePath::from_bytes(&path_bytes).unwrap();
            assert!(path.verify(leaves[pos as usize], expected_root));
        }
    }

    #[test]
    fn test_ffi_empty_leaves() {
        let empty_tree_root = {
            let mut t = MemoryTreeServer::empty();
            t.checkpoint(1).unwrap();
            t.root().to_repr()
        };
        let root_bytes =
            unsafe { compute_root_from_raw(std::ptr::null(), 0) }.unwrap();
        assert_eq!(root_bytes, empty_tree_root);
    }

    #[test]
    fn test_ffi_position_out_of_range() {
        let leaves = leaves_to_bytes(&[fp(1)]);
        let result = unsafe { compute_path_from_raw(leaves.as_ptr(), 1, 5) };
        assert!(matches!(result, Err(FfiError::PositionOutOfRange)));
        let result = unsafe { compute_path_from_raw(leaves.as_ptr(), 1, 1) };
        assert!(matches!(result, Err(FfiError::PositionOutOfRange)));
    }

    #[test]
    fn test_ffi_encoding_parity() {
        let val = fp(12345678);
        let repr = val.to_repr();
        assert_eq!(repr.len(), 32);
        let recovered: Option<Fp> = Fp::from_repr(repr).into();
        assert_eq!(recovered.unwrap(), val);

        let bytes = leaves_to_bytes(&[val]);
        let ffi_root = unsafe { compute_root_from_raw(bytes.as_ptr(), 1) }.unwrap();

        let mut tree = MemoryTreeServer::empty();
        tree.append(val).unwrap();
        tree.checkpoint(1).unwrap();
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

        let root_bytes = unsafe { compute_root_from_raw(bytes.as_ptr(), 1) }.unwrap();
        let mut tree = MemoryTreeServer::empty();
        tree.append(leaf).unwrap();
        tree.checkpoint(1).unwrap();
        assert_eq!(root_bytes, tree.root().to_repr());

        let path_bytes = unsafe { compute_path_from_raw(bytes.as_ptr(), 1, 0) }.unwrap();
        let path = MerklePath::from_bytes(&path_bytes).unwrap();
        assert!(path.verify(leaf, tree.root()));
    }

    pub fn golden_leaves() -> Vec<Fp> {
        vec![fp(1), fp(2), fp(3)]
    }

    pub fn golden_root() -> [u8; 32] {
        let leaves = golden_leaves();
        let mut tree = MemoryTreeServer::empty();
        for &l in &leaves {
            tree.append(l).unwrap();
        }
        tree.checkpoint(1).unwrap();
        tree.root().to_repr()
    }

    #[test]
    fn test_golden_vector_root() {
        let leaves = golden_leaves();
        let bytes = leaves_to_bytes(&leaves);
        let root = unsafe { compute_root_from_raw(bytes.as_ptr(), leaves.len()) }.unwrap();
        let expected = golden_root();
        assert_eq!(root, expected);
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
                "golden path at position {pos} must verify"
            );
        }
    }
}
