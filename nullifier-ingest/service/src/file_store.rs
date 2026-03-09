//! Flat-file storage for Orchard nullifiers with crash-safe checkpointing.
//!
//! # File Layout
//!
//! Two files inside a single data directory replace the old SQLite database:
//!
//! | File                      | Format                                           |
//! |---------------------------|--------------------------------------------------|
//! | `nullifiers.bin`          | Append-only, raw concatenation of 32-byte blobs. |
//! |                           | No header, no framing. Size = `count × 32`.      |
//! | `nullifiers.checkpoint`   | Fixed 16 bytes: `height: u64 LE ‖ offset: u64 LE`|
//!
//! `height` is the last fully-synced block height. `offset` is the byte
//! length of `nullifiers.bin` at the moment that height was committed.
//!
//! # Write Protocol (per batch)
//!
//! ```text
//! 1. Append N × 32 nullifier bytes to nullifiers.bin
//! 2. fsync(nullifiers.bin)
//! 3. Write (new_height, new_file_length) to nullifiers.checkpoint.tmp
//! 4. fsync(nullifiers.checkpoint.tmp)
//! 5. rename(nullifiers.checkpoint.tmp → nullifiers.checkpoint)   [atomic on POSIX]
//! ```
//!
//! The rename in step 5 is the **commit point**. Before it completes, the
//! checkpoint still refers to the previous batch's offset. After it completes,
//! both the data and the checkpoint are consistent.
//!
//! # Crash Recovery
//!
//! On startup, [`resume_height`](crate::sync_nullifiers::resume_height) runs:
//!
//! ```text
//! 1. Read checkpoint → (height, offset)
//! 2. Truncate nullifiers.bin to offset   (discards any bytes appended after
//!                                         the last committed checkpoint)
//! 3. Resume syncing from height + 1
//! ```
//!
//! There are three crash scenarios:
//!
//! | Crash point               | State on recovery                               |
//! |---------------------------|-------------------------------------------------|
//! | During step 1 (append)    | Checkpoint still points to previous batch.       |
//! |                           | Truncation removes the partial append.           |
//! | During step 3–4 (tmp)     | Old checkpoint is intact. Temp file is ignored.  |
//! |                           | Truncation removes the already-appended data.    |
//! | During step 5 (rename)    | POSIX rename is atomic — either old or new       |
//! |                           | checkpoint survives. Either way, truncation to   |
//! |                           | the surviving offset restores consistency.        |
//!
//! This gives the same atomicity guarantee as SQLite's `BEGIN`/`COMMIT`
//! without any of the B-tree overhead.

use std::fs::{self, File, OpenOptions};
use std::io::Write;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result};
use ff::PrimeField;
use pasta_curves::Fp;
use rayon::prelude::*;

const NULLIFIER_SIZE: usize = 32;
const CHECKPOINT_SIZE: usize = 16;
const INDEX_ENTRY_SIZE: usize = 16; // [u64 LE height][u64 LE offset]

pub fn nullifiers_path(dir: &Path) -> PathBuf {
    dir.join("nullifiers.bin")
}

pub fn checkpoint_path(dir: &Path) -> PathBuf {
    dir.join("nullifiers.checkpoint")
}

pub fn index_path(dir: &Path) -> PathBuf {
    dir.join("nullifiers.index")
}

/// Atomically save `(height, byte_offset)` via write-to-temp + `fsync` + rename.
pub fn save_checkpoint(dir: &Path, height: u64, offset: u64) -> Result<()> {
    let cp = checkpoint_path(dir);
    let tmp = dir.join("nullifiers.checkpoint.tmp");

    let mut buf = [0u8; CHECKPOINT_SIZE];
    buf[..8].copy_from_slice(&height.to_le_bytes());
    buf[8..].copy_from_slice(&offset.to_le_bytes());

    let mut f = File::create(&tmp).context("create checkpoint temp file")?;
    f.write_all(&buf).context("write checkpoint")?;
    f.sync_all().context("fsync checkpoint")?;
    drop(f);

    fs::rename(&tmp, &cp).context("rename checkpoint")?;

    // Also append to the index file so we can later binary-search for any height.
    append_index(dir, height, offset)?;

    Ok(())
}

/// Append a 16-byte `[u64 LE height][u64 LE offset]` record to `nullifiers.index`.
///
/// Called from `save_checkpoint()` after each successful batch commit.
/// The index file is small (one entry per batch ≈ 10K blocks), so appending
/// without fsync is acceptable — the checkpoint file is the durability point.
pub fn append_index(dir: &Path, height: u64, offset: u64) -> Result<()> {
    let path = index_path(dir);
    let mut f = OpenOptions::new()
        .create(true)
        .append(true)
        .open(&path)
        .context("open index file for append")?;

    let mut buf = [0u8; INDEX_ENTRY_SIZE];
    buf[..8].copy_from_slice(&height.to_le_bytes());
    buf[8..].copy_from_slice(&offset.to_le_bytes());
    f.write_all(&buf).context("write index entry")?;
    Ok(())
}

/// Find the largest index entry with `height <= target_height`.
///
/// Returns `Some((height, offset))` or `None` if the index is empty or
/// all entries are above the target.
pub fn offset_for_height(dir: &Path, target_height: u64) -> Result<Option<(u64, u64)>> {
    let path = index_path(dir);
    if !path.exists() {
        return Ok(None);
    }
    let data = fs::read(&path).context("read index file")?;
    if data.len() % INDEX_ENTRY_SIZE != 0 {
        anyhow::bail!(
            "corrupt index file: size {} is not a multiple of {}",
            data.len(),
            INDEX_ENTRY_SIZE
        );
    }
    let n = data.len() / INDEX_ENTRY_SIZE;
    if n == 0 {
        return Ok(None);
    }

    // The index is append-only and heights are monotonically increasing,
    // so we can binary search.
    let entry = |i: usize| -> (u64, u64) {
        let off = i * INDEX_ENTRY_SIZE;
        let h = u64::from_le_bytes(data[off..off + 8].try_into().unwrap());
        let o = u64::from_le_bytes(data[off + 8..off + 16].try_into().unwrap());
        (h, o)
    };

    // Binary search: find last entry with height <= target_height
    let mut lo = 0usize;
    let mut hi = n;
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        let (h, _) = entry(mid);
        if h <= target_height {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }

    if lo == 0 {
        return Ok(None);
    }
    let (h, o) = entry(lo - 1);
    Ok(Some((h, o)))
}

/// One-time migration: create `nullifiers.index` from `nullifiers.checkpoint`
/// if the index doesn't exist yet. Future ingests will append naturally
/// via `save_checkpoint` → `append_index`.
pub fn rebuild_index(dir: &Path) -> Result<()> {
    let idx = index_path(dir);
    if idx.exists() {
        return Ok(());
    }
    // Only migrate if we have a checkpoint
    if let Some((height, offset)) = load_checkpoint(dir)? {
        append_index(dir, height, offset)?;
        eprintln!(
            "Migrated index: created nullifiers.index with single entry (height={}, offset={})",
            height, offset
        );
    }
    Ok(())
}

/// Load nullifiers up to `byte_offset` and convert to `Fp` in parallel.
///
/// Like `load_all_nullifiers` but reads only the first `byte_offset` bytes
/// of the data file. Used when exporting at a target height below the
/// current sync point.
pub fn load_nullifiers_up_to(dir: &Path, byte_offset: u64) -> Result<Vec<Fp>> {
    let path = nullifiers_path(dir);
    if !path.exists() {
        return Ok(Vec::new());
    }

    let byte_offset = byte_offset as usize;
    if byte_offset % NULLIFIER_SIZE != 0 {
        anyhow::bail!(
            "byte_offset {} is not a multiple of {}",
            byte_offset,
            NULLIFIER_SIZE
        );
    }

    let full_data = fs::read(&path).context("read nullifiers file")?;
    let data = if byte_offset < full_data.len() {
        &full_data[..byte_offset]
    } else {
        &full_data
    };

    parse_nullifier_bytes(data)
}

/// Load the checkpoint. Returns `Some((height, byte_offset))` or `None`.
pub fn load_checkpoint(dir: &Path) -> Result<Option<(u64, u64)>> {
    let cp = checkpoint_path(dir);
    if !cp.exists() {
        return Ok(None);
    }
    let data = fs::read(&cp).context("read checkpoint")?;
    if data.len() != CHECKPOINT_SIZE {
        anyhow::bail!(
            "corrupt checkpoint: expected {} bytes, got {}",
            CHECKPOINT_SIZE,
            data.len()
        );
    }
    let height = u64::from_le_bytes(data[..8].try_into().unwrap());
    let offset = u64::from_le_bytes(data[8..].try_into().unwrap());
    Ok(Some((height, offset)))
}

/// Append raw 32-byte nullifier blobs to the data file. Returns the new file
/// length (suitable as the `offset` argument to [`save_checkpoint`]).
///
/// Heights in the tuples are discarded — they exist only so the caller can
/// pass the same `Vec<(u64, Vec<u8>)>` that comes out of the gRPC stream.
pub fn append_nullifiers(dir: &Path, nfs: &[(u64, Vec<u8>)]) -> Result<u64> {
    let path = nullifiers_path(dir);
    let mut f = OpenOptions::new()
        .create(true)
        .append(true)
        .open(&path)
        .context("open nullifiers file for append")?;

    for (_, nf) in nfs {
        debug_assert_eq!(nf.len(), NULLIFIER_SIZE);
        f.write_all(nf)?;
    }
    f.sync_all().context("fsync nullifiers file")?;

    Ok(f.metadata()?.len())
}

/// Truncate the nullifier file to `offset` bytes, discarding any partial batch
/// written after the last committed checkpoint.
pub fn truncate_to_checkpoint(dir: &Path, offset: u64) -> Result<()> {
    let path = nullifiers_path(dir);
    if !path.exists() {
        return Ok(());
    }
    let f = OpenOptions::new()
        .write(true)
        .open(&path)
        .context("open nullifiers file for truncation")?;
    f.set_len(offset).context("truncate nullifiers file")?;
    f.sync_all().context("fsync after truncation")?;
    Ok(())
}

/// Bulk-read every nullifier and convert to `Fp` in parallel via Rayon.
pub fn load_all_nullifiers(dir: &Path) -> Result<Vec<Fp>> {
    let path = nullifiers_path(dir);
    if !path.exists() {
        return Ok(Vec::new());
    }

    let data = fs::read(&path).context("read nullifiers file")?;
    if data.len() % NULLIFIER_SIZE != 0 {
        anyhow::bail!(
            "corrupt nullifiers file: size {} is not a multiple of {}",
            data.len(),
            NULLIFIER_SIZE
        );
    }

    parse_nullifier_bytes(&data)
}

/// Parse raw 32-byte chunks into `Fp` field elements in parallel.
///
/// Returns an error if any chunk contains a non-canonical encoding
/// (value >= the Pallas field modulus).
pub fn parse_nullifier_bytes(data: &[u8]) -> Result<Vec<Fp>> {
    anyhow::ensure!(
        data.len() % NULLIFIER_SIZE == 0,
        "data length {} is not a multiple of {}",
        data.len(),
        NULLIFIER_SIZE
    );
    data.par_chunks_exact(NULLIFIER_SIZE)
        .enumerate()
        .map(|(i, chunk)| {
            let mut arr = [0u8; 32];
            arr.copy_from_slice(chunk);
            Option::from(Fp::from_repr(arr)).ok_or_else(|| {
                anyhow::anyhow!("non-canonical nullifier encoding at index {}", i)
            })
        })
        .collect()
}

/// Number of nullifiers in the file, derived from file size.
pub fn nullifier_count(dir: &Path) -> Result<u64> {
    let path = nullifiers_path(dir);
    if !path.exists() {
        return Ok(0);
    }
    Ok(fs::metadata(&path)?.len() / NULLIFIER_SIZE as u64)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_dir(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "nf_file_store_test_{}_{}",
            std::process::id(),
            name
        ));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn checkpoint_round_trip() {
        let dir = temp_dir("cp_rt");

        assert_eq!(load_checkpoint(&dir).unwrap(), None);

        save_checkpoint(&dir, 1_700_000, 1024).unwrap();
        assert_eq!(load_checkpoint(&dir).unwrap(), Some((1_700_000, 1024)));

        save_checkpoint(&dir, 1_800_000, 2048).unwrap();
        assert_eq!(load_checkpoint(&dir).unwrap(), Some((1_800_000, 2048)));

        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn append_and_load() {
        let dir = temp_dir("append");

        let nfs = vec![
            (100u64, vec![1u8; 32]),
            (100, vec![2u8; 32]),
            (200, vec![3u8; 32]),
        ];

        let offset = append_nullifiers(&dir, &nfs).unwrap();
        assert_eq!(offset, 96);

        let loaded = load_all_nullifiers(&dir).unwrap();
        assert_eq!(loaded.len(), 3);
        assert_eq!(nullifier_count(&dir).unwrap(), 3);

        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn truncate_discards_partial_batch() {
        let dir = temp_dir("trunc");

        let batch1 = vec![(100u64, vec![1u8; 32]), (100, vec![2u8; 32])];
        let offset1 = append_nullifiers(&dir, &batch1).unwrap();
        assert_eq!(offset1, 64);

        let batch2 = vec![(200u64, vec![3u8; 32])];
        let offset2 = append_nullifiers(&dir, &batch2).unwrap();
        assert_eq!(offset2, 96);

        truncate_to_checkpoint(&dir, offset1).unwrap();

        let loaded = load_all_nullifiers(&dir).unwrap();
        assert_eq!(loaded.len(), 2);

        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn index_append_and_lookup() {
        let dir = temp_dir("idx_lookup");

        // Simulate three batches with increasing heights
        append_index(&dir, 1_700_000, 1024).unwrap();
        append_index(&dir, 1_710_000, 2048).unwrap();
        append_index(&dir, 1_720_000, 3072).unwrap();

        // Exact match
        let (h, o) = offset_for_height(&dir, 1_710_000).unwrap().unwrap();
        assert_eq!(h, 1_710_000);
        assert_eq!(o, 2048);

        // Between entries — returns the floor
        let (h, o) = offset_for_height(&dir, 1_715_000).unwrap().unwrap();
        assert_eq!(h, 1_710_000);
        assert_eq!(o, 2048);

        // Above all entries
        let (h, o) = offset_for_height(&dir, 2_000_000).unwrap().unwrap();
        assert_eq!(h, 1_720_000);
        assert_eq!(o, 3072);

        // Below all entries
        assert_eq!(offset_for_height(&dir, 1_699_999).unwrap(), None);

        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn rebuild_index_migration() {
        let dir = temp_dir("idx_rebuild");

        // Write a checkpoint without an index file
        let nfs = vec![(100u64, vec![1u8; 32]), (100, vec![2u8; 32])];
        let offset = append_nullifiers(&dir, &nfs).unwrap();
        // Manually save checkpoint without the index side-effect
        // (simulating old code before index was added)
        let cp = checkpoint_path(&dir);
        let tmp = dir.join("nullifiers.checkpoint.tmp");
        let mut buf = [0u8; CHECKPOINT_SIZE];
        buf[..8].copy_from_slice(&1_700_000u64.to_le_bytes());
        buf[8..].copy_from_slice(&offset.to_le_bytes());
        let mut f = File::create(&tmp).unwrap();
        f.write_all(&buf).unwrap();
        f.sync_all().unwrap();
        drop(f);
        fs::rename(&tmp, &cp).unwrap();

        // No index file yet
        assert!(!index_path(&dir).exists());

        // Migration creates it
        rebuild_index(&dir).unwrap();
        assert!(index_path(&dir).exists());

        let (h, o) = offset_for_height(&dir, 1_700_000).unwrap().unwrap();
        assert_eq!(h, 1_700_000);
        assert_eq!(o, offset);

        // Second call is a no-op
        rebuild_index(&dir).unwrap();

        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn load_nullifiers_up_to_offset() {
        let dir = temp_dir("up_to");

        let nfs = vec![
            (100u64, vec![1u8; 32]),
            (100, vec![2u8; 32]),
            (200, vec![3u8; 32]),
        ];

        let _offset = append_nullifiers(&dir, &nfs).unwrap();

        // Load only the first 2 nullifiers (64 bytes)
        let loaded = load_nullifiers_up_to(&dir, 64).unwrap();
        assert_eq!(loaded.len(), 2);

        // Load all (96 bytes)
        let loaded = load_nullifiers_up_to(&dir, 96).unwrap();
        assert_eq!(loaded.len(), 3);

        // Load with offset beyond file size — returns all
        let loaded = load_nullifiers_up_to(&dir, 1024).unwrap();
        assert_eq!(loaded.len(), 3);

        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn empty_dir_returns_nothing() {
        let dir = temp_dir("empty");

        assert_eq!(load_all_nullifiers(&dir).unwrap().len(), 0);
        assert_eq!(nullifier_count(&dir).unwrap(), 0);
        assert_eq!(load_checkpoint(&dir).unwrap(), None);

        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn crash_recovery_truncates_uncommitted_bytes() {
        let dir = temp_dir("crash");

        // Batch 1: committed
        let batch1 = vec![(100u64, vec![1u8; 32]), (100, vec![2u8; 32])];
        let offset1 = append_nullifiers(&dir, &batch1).unwrap();
        save_checkpoint(&dir, 100, offset1).unwrap();

        // Batch 2: written but NOT checkpointed (simulates crash)
        let batch2 = vec![(200u64, vec![3u8; 32])];
        append_nullifiers(&dir, &batch2).unwrap();
        assert_eq!(nullifier_count(&dir).unwrap(), 3);

        // Recovery: read checkpoint and truncate
        let (height, offset) = load_checkpoint(&dir).unwrap().unwrap();
        assert_eq!(height, 100);
        assert_eq!(offset, 64);
        truncate_to_checkpoint(&dir, offset).unwrap();

        assert_eq!(nullifier_count(&dir).unwrap(), 2);

        let _ = fs::remove_dir_all(&dir);
    }
}
