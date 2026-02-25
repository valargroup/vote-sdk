use std::path::Path;

use anyhow::Result;
use tonic::transport::Channel;
use tonic::Request;

use crate::download::connect_lwd;
use crate::file_store;
use crate::rpc::compact_tx_streamer_client::CompactTxStreamerClient;
use crate::rpc::{BlockId, BlockRange, ChainSpec};

/// NU5 (Orchard) activation height on Zcash mainnet.
pub const NU5_ACTIVATION_HEIGHT: u64 = 1_687_104;

/// How many blocks to request per gRPC streaming call.
const BATCH_SIZE: u64 = 10_000;

/// Fetch the current chain tip height from a lightwalletd server.
///
/// Connects to the given URL and calls `GetLatestBlock`. Returns the height
/// of the chain tip as reported by the server.
pub async fn fetch_chain_tip(lwd_url: &str) -> Result<u64> {
    let mut client = connect_lwd(lwd_url).await?;
    let latest = client
        .get_latest_block(Request::new(ChainSpec {}))
        .await?;
    Ok(latest.into_inner().height)
}

/// Determine the block height to resume syncing from.
///
/// Reads the checkpoint file and truncates any uncommitted bytes from
/// the data file, then returns the last fully-committed height.
/// If no checkpoint exists, starts from NU5 activation.
pub fn resume_height(dir: &Path) -> Result<u64> {
    match file_store::load_checkpoint(dir)? {
        Some((h, offset)) if h >= NU5_ACTIVATION_HEIGHT => {
            file_store::truncate_to_checkpoint(dir, offset)?;
            Ok(h)
        }
        _ => Ok(NU5_ACTIVATION_HEIGHT),
    }
}

/// Stream blocks `[start, end]` from a single server and return collected
/// `(height, nullifier)` pairs.
async fn fetch_block_range(
    client: &mut CompactTxStreamerClient<Channel>,
    start: u64,
    end: u64,
) -> Result<Vec<(u64, Vec<u8>)>> {
    let mut stream = client
        .get_block_range(Request::new(BlockRange {
            start: Some(BlockId {
                height: start,
                hash: vec![],
            }),
            end: Some(BlockId {
                height: end,
                hash: vec![],
            }),
            spam_filter_threshold: 0,
        }))
        .await?
        .into_inner();

    let mut nf_buffer: Vec<(u64, Vec<u8>)> = Vec::new();
    while let Some(block) = stream.message().await? {
        for tx in block.vtx {
            for a in tx.actions {
                nf_buffer.push((block.height, a.nullifier));
            }
        }
    }
    Ok(nf_buffer)
}

/// Sync nullifiers from multiple lightwalletd servers into flat files.
///
/// Connects to each URL in `lwd_urls`, streams blocks from the resume point to
/// `max_height` (or chain tip when `None`) using parallel downloads (one batch
/// per server), and appends all Orchard nullifiers to the data file.  Calls
/// `progress` after each parallel cycle with
/// `(last_height, target_height, cycle_nullifier_count, total_nullifier_count)`.
pub async fn sync(
    dir: &Path,
    lwd_urls: &[String],
    max_height: Option<u64>,
    progress: impl Fn(u64, u64, u64, u64),
) -> Result<SyncResult> {
    std::fs::create_dir_all(dir)?;

    let mut clients = Vec::with_capacity(lwd_urls.len());
    for url in lwd_urls {
        clients.push(connect_lwd(url).await?);
    }
    let n = clients.len();

    let latest = clients[0]
        .get_latest_block(Request::new(ChainSpec {}))
        .await?;
    let chain_tip = latest.into_inner().height;

    let start = resume_height(dir)?;
    let existing = file_store::nullifier_count(dir)?;

    // Determine the effective sync target.
    //
    // If max_height is set but already lies below the current checkpoint (i.e.
    // we have synced past the requested height), we cannot rewind.  Instead we
    // advance to the next multiple-of-10 block height above the checkpoint so
    // there is always a clean forward stopping point.
    let target = match max_height {
        Some(h) if h < start => {
            let next_multiple = ((start / 10) + 1) * 10;
            eprintln!(
                "SYNC_HEIGHT {} is below current checkpoint {}; \
                 advancing target to next multiple of 10: {}",
                h, start, next_multiple
            );
            std::cmp::min(next_multiple, chain_tip)
        }
        Some(h) => std::cmp::min(h, chain_tip),
        None => chain_tip,
    };

    if start > NU5_ACTIVATION_HEIGHT {
        eprintln!(
            "Resuming from checkpoint: height {} ({} nullifiers on disk)",
            start, existing
        );
    } else {
        eprintln!("Starting fresh from NU5 activation height {}", NU5_ACTIVATION_HEIGHT);
    }
    if let Some(h) = max_height {
        eprintln!("Max height: {} (chain tip: {})", h, chain_tip);
    }
    eprintln!("Target: {} ({} blocks to sync)", target, target.saturating_sub(start));

    if start >= target {
        return Ok(SyncResult {
            chain_tip,
            blocks_synced: 0,
            nullifiers_synced: 0,
        });
    }

    let mut current = start + 1;
    let mut total_nfs: u64 = 0;
    let mut blocks_synced: u64 = 0;

    while current <= target {
        // Build up to N batch ranges, one per server
        let mut batch_ranges: Vec<(u64, u64)> = Vec::with_capacity(n);
        let mut batch_start = current;
        for _ in 0..n {
            if batch_start > target {
                break;
            }
            let batch_end = std::cmp::min(batch_start + BATCH_SIZE - 1, target);
            batch_ranges.push((batch_start, batch_end));
            batch_start = batch_end + 1;
        }

        // Spawn parallel downloads
        let mut handles = Vec::with_capacity(batch_ranges.len());
        for (i, &(range_start, range_end)) in batch_ranges.iter().enumerate() {
            let mut client = clients[i].clone();
            handles.push(tokio::spawn(async move {
                fetch_block_range(&mut client, range_start, range_end).await
            }));
        }

        // Await all, collect results
        let mut all_nfs: Vec<(u64, Vec<u8>)> = Vec::new();
        for handle in handles {
            all_nfs.extend(handle.await??);
        }
        let cycle_end = batch_ranges.last().unwrap().1;
        let cycle_nfs = all_nfs.len() as u64;

        // Append nullifiers then atomically commit the checkpoint
        let offset = file_store::append_nullifiers(dir, &all_nfs)?;
        file_store::save_checkpoint(dir, cycle_end, offset)?;

        drop(all_nfs);

        total_nfs += cycle_nfs;
        blocks_synced += cycle_end - current + 1;
        progress(cycle_end, target, cycle_nfs, total_nfs);

        current = cycle_end + 1;
    }

    Ok(SyncResult {
        chain_tip,
        blocks_synced,
        nullifiers_synced: total_nfs,
    })
}

/// Result of a sync operation.
pub struct SyncResult {
    pub chain_tip: u64,
    pub blocks_synced: u64,
    pub nullifiers_synced: u64,
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    fn temp_dir(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "nf_sync_test_{}_{}",
            std::process::id(),
            name
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn resume_height_fresh() {
        let dir = temp_dir("fresh");
        assert_eq!(resume_height(&dir).unwrap(), NU5_ACTIVATION_HEIGHT);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn resume_height_from_checkpoint() {
        let dir = temp_dir("resume");

        // Write some nullifiers and commit a checkpoint
        let nfs = vec![
            (1_700_000u64, vec![1u8; 32]),
            (1_700_000, vec![2u8; 32]),
            (1_700_001, vec![3u8; 32]),
        ];
        let offset = file_store::append_nullifiers(&dir, &nfs).unwrap();
        file_store::save_checkpoint(&dir, 1_700_001, offset).unwrap();

        let h = resume_height(&dir).unwrap();
        assert_eq!(h, 1_700_001);

        // All 3 nullifiers should still be present (checkpoint was exact)
        assert_eq!(file_store::nullifier_count(&dir).unwrap(), 3);

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn resume_height_truncates_uncommitted() {
        let dir = temp_dir("trunc");

        // Committed batch
        let batch1 = vec![(1_700_000u64, vec![1u8; 32]), (1_700_000, vec![2u8; 32])];
        let offset = file_store::append_nullifiers(&dir, &batch1).unwrap();
        file_store::save_checkpoint(&dir, 1_700_000, offset).unwrap();

        // Uncommitted partial batch (simulates crash)
        let batch2 = vec![(1_700_001u64, vec![3u8; 32])];
        file_store::append_nullifiers(&dir, &batch2).unwrap();
        assert_eq!(file_store::nullifier_count(&dir).unwrap(), 3);

        // resume_height should truncate back to the committed state
        let h = resume_height(&dir).unwrap();
        assert_eq!(h, 1_700_000);
        assert_eq!(file_store::nullifier_count(&dir).unwrap(), 2);

        let _ = std::fs::remove_dir_all(&dir);
    }
}
