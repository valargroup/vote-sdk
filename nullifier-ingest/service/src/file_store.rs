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

pub fn nullifiers_path(dir: &Path) -> PathBuf {
    dir.join("nullifiers.bin")
}

pub fn checkpoint_path(dir: &Path) -> PathBuf {
    dir.join("nullifiers.checkpoint")
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
    Ok(())
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

    let nullifiers: Vec<Fp> = data
        .par_chunks_exact(NULLIFIER_SIZE)
        .map(|chunk| {
            let mut arr = [0u8; 32];
            arr.copy_from_slice(chunk);
            Fp::from_repr(arr).unwrap()
        })
        .collect();

    Ok(nullifiers)
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
