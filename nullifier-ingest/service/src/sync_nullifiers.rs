use anyhow::Result;
use rusqlite::{params, Connection};
use tonic::transport::Channel;
use tonic::Request;

use crate::db;
use crate::download::connect_lwd;
use crate::rpc::compact_tx_streamer_client::CompactTxStreamerClient;
use crate::rpc::{BlockId, BlockRange, ChainSpec};

/// NU5 (Orchard) activation height on Zcash mainnet.
pub const NU5_ACTIVATION_HEIGHT: u64 = 1_687_104;

/// How many blocks to request per gRPC streaming call.
const BATCH_SIZE: u64 = 10_000;

/// Insert a batch of `(height, nullifier)` pairs into the database within the
/// current transaction.
pub fn insert_nullifiers(connection: &Connection, nfs: &[(u64, Vec<u8>)]) -> Result<()> {
    let mut stmt = connection
        .prepare_cached("INSERT INTO nullifiers(height, nullifier) VALUES (?1, ?2)")?;
    for (height, nf) in nfs {
        stmt.execute(params![height, nf])?;
    }
    Ok(())
}

/// Determine the block height to resume syncing from.
///
/// If a checkpoint exists, nullifiers at that height are deleted (partial batch)
/// and syncing resumes from the block before. Otherwise starts from NU5 activation.
pub fn resume_height(connection: &Connection) -> Result<u64> {
    match db::load_checkpoint(connection)? {
        Some(h) if h >= NU5_ACTIVATION_HEIGHT => {
            db::delete_nullifiers_at_height(connection, h)?;
            Ok(h - 1)
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

/// Sync nullifiers from multiple lightwalletd servers into the database.
///
/// Connects to each URL in `lwd_urls`, streams blocks from the resume point to
/// chain tip using parallel downloads (one batch per server), and inserts all
/// Orchard nullifiers. Calls `progress` after each parallel cycle with
/// `(last_height, chain_tip, cycle_nullifier_count, total_nullifier_count)`.
pub async fn sync(
    connection: &Connection,
    lwd_urls: &[String],
    progress: impl Fn(u64, u64, u64, u64),
) -> Result<SyncResult> {
    let mut clients = Vec::with_capacity(lwd_urls.len());
    for url in lwd_urls {
        clients.push(connect_lwd(url).await?);
    }
    let n = clients.len();

    let latest = clients[0]
        .get_latest_block(Request::new(ChainSpec {}))
        .await?;
    let chain_tip = latest.into_inner().height;

    let start = resume_height(connection)?;

    if start >= chain_tip {
        return Ok(SyncResult {
            chain_tip,
            blocks_synced: 0,
            nullifiers_synced: 0,
        });
    }

    let mut current = start + 1;
    let mut total_nfs: u64 = 0;
    let mut blocks_synced: u64 = 0;

    while current <= chain_tip {
        // Build up to N batch ranges, one per server
        let mut batch_ranges: Vec<(u64, u64)> = Vec::with_capacity(n);
        let mut batch_start = current;
        for _ in 0..n {
            if batch_start > chain_tip {
                break;
            }
            let batch_end = std::cmp::min(batch_start + BATCH_SIZE - 1, chain_tip);
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

        connection.execute_batch("BEGIN")?;
        insert_nullifiers(connection, &all_nfs)?;
        db::save_checkpoint(connection, cycle_end)?;
        connection.execute_batch("COMMIT")?;

        drop(all_nfs);

        total_nfs += cycle_nfs;
        blocks_synced += cycle_end - current + 1;
        progress(cycle_end, chain_tip, cycle_nfs, total_nfs);

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

/// Migrate the nullifiers table: remove the column-level UNIQUE constraint
/// so that bulk inserts don't pay the cost of index maintenance on every row.
/// Idempotent.
pub fn migrate_nullifiers_table(connection: &Connection) -> Result<()> {
    let has_autoindex: bool = connection.query_row(
        "SELECT COUNT(*) > 0 FROM sqlite_master
         WHERE type='index' AND name='sqlite_autoindex_nullifiers_1'",
        [],
        |r| r.get(0),
    )?;

    if !has_autoindex {
        return Ok(());
    }

    let row_count: u64 =
        connection.query_row("SELECT COUNT(*) FROM nullifiers", [], |r| r.get(0))?;
    println!(
        "Migrating nullifiers table ({} rows): removing column UNIQUE constraint for bulk perf...",
        row_count
    );
    let t = std::time::Instant::now();

    connection.execute_batch(
        "CREATE TABLE nullifiers_new(
            height INTEGER NOT NULL,
            nullifier BLOB NOT NULL
         );
         INSERT INTO nullifiers_new SELECT * FROM nullifiers;
         DROP TABLE nullifiers;
         ALTER TABLE nullifiers_new RENAME TO nullifiers;",
    )?;

    println!(
        "Migration complete in {:.1}s. Unique index will be built after ingestion finishes.",
        t.elapsed().as_secs_f64()
    );
    Ok(())
}

/// Recreate the unique index on nullifiers after bulk loading completes.
/// Idempotent.
pub fn rebuild_index(connection: &Connection) -> Result<()> {
    let has_index: bool = connection.query_row(
        "SELECT COUNT(*) > 0 FROM sqlite_master
         WHERE type='index' AND tbl_name='nullifiers'
         AND (name='idx_nullifiers' OR name='sqlite_autoindex_nullifiers_1')",
        [],
        |r| r.get(0),
    )?;

    if has_index {
        return Ok(());
    }

    println!("Building unique index on nullifiers...");
    let t = std::time::Instant::now();
    connection.execute_batch("CREATE UNIQUE INDEX idx_nullifiers ON nullifiers(nullifier);")?;
    println!("Index built in {:.1}s", t.elapsed().as_secs_f64());
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::db;

    fn setup_db() -> Connection {
        let conn = Connection::open_in_memory().unwrap();
        db::create_schema(&conn).unwrap();
        conn
    }

    #[test]
    fn test_checkpoint_round_trip() {
        let conn = setup_db();

        assert_eq!(db::load_checkpoint(&conn).unwrap(), None);

        db::save_checkpoint(&conn, 1_700_000).unwrap();
        assert_eq!(db::load_checkpoint(&conn).unwrap(), Some(1_700_000));

        // Overwrite
        db::save_checkpoint(&conn, 1_800_000).unwrap();
        assert_eq!(db::load_checkpoint(&conn).unwrap(), Some(1_800_000));
    }

    #[test]
    fn test_insert_and_delete_nullifiers() {
        let conn = setup_db();

        let nfs = vec![
            (100u64, vec![1u8; 32]),
            (100, vec![2u8; 32]),
            (200, vec![3u8; 32]),
        ];

        conn.execute_batch("BEGIN").unwrap();
        insert_nullifiers(&conn, &nfs).unwrap();
        conn.execute_batch("COMMIT").unwrap();

        let count: u64 = conn
            .query_row("SELECT COUNT(*) FROM nullifiers", [], |r| r.get(0))
            .unwrap();
        assert_eq!(count, 3);

        // Delete height 100 — should remove 2 rows
        db::delete_nullifiers_at_height(&conn, 100).unwrap();
        let count: u64 = conn
            .query_row("SELECT COUNT(*) FROM nullifiers", [], |r| r.get(0))
            .unwrap();
        assert_eq!(count, 1);
    }

    #[test]
    fn test_resume_height_fresh() {
        let conn = setup_db();
        assert_eq!(resume_height(&conn).unwrap(), NU5_ACTIVATION_HEIGHT);
    }

    #[test]
    fn test_resume_height_deletes_checkpoint_block() {
        let conn = setup_db();

        // Insert some nullifiers and checkpoint
        let nfs = vec![
            (1_700_000u64, vec![1u8; 32]),
            (1_700_000, vec![2u8; 32]),
            (1_700_001, vec![3u8; 32]),
        ];
        conn.execute_batch("BEGIN").unwrap();
        insert_nullifiers(&conn, &nfs).unwrap();
        db::save_checkpoint(&conn, 1_700_001).unwrap();
        conn.execute_batch("COMMIT").unwrap();

        // Resume should delete nullifiers at height 1_700_001 and return 1_700_000
        let h = resume_height(&conn).unwrap();
        assert_eq!(h, 1_700_000);

        let count: u64 = conn
            .query_row("SELECT COUNT(*) FROM nullifiers", [], |r| r.get(0))
            .unwrap();
        assert_eq!(count, 2); // only height 1_700_000 remains
    }
}
