use std::env;

use anyhow::Result;
use r2d2::Pool;
use r2d2_sqlite::SqliteConnectionManager;

use nullifier_service::db;
use nullifier_service::sync_nullifiers;

/// Default lightwalletd endpoints
const DEFAULT_LWD_URLS: &[&str] = &[
    "https://zec.rocks:443",
    "https://eu2.zec.stardust.rest:443",
    "https://eu.zec.stardust.rest:443",
];

/// Default SQLite database path
const DEFAULT_DB_PATH: &str = "nullifiers.db";

#[tokio::main]
async fn main() -> Result<()> {
    let lwd_urls: Vec<String> = env::var("LWD_URLS")
        .map(|s| s.split(',').map(|u| u.trim().to_string()).collect())
        .unwrap_or_else(|_| {
            env::var("LWD_URL")
                .map(|u| vec![u])
                .unwrap_or_else(|_| DEFAULT_LWD_URLS.iter().map(|s| s.to_string()).collect())
        });
    let db_path = env::var("DB_PATH").unwrap_or_else(|_| DEFAULT_DB_PATH.to_string());

    println!("Opening SQLite database: {}", db_path);
    let manager = SqliteConnectionManager::file(&db_path);
    let pool = Pool::new(manager)?;
    let connection = pool.get()?;

    db::create_schema(&connection)?;

    connection.execute_batch(
        "PRAGMA journal_mode = WAL;
         PRAGMA synchronous = NORMAL;
         PRAGMA cache_size = -256000;
         PRAGMA temp_store = MEMORY;
         PRAGMA mmap_size = 2147483648;",
    )?;

    sync_nullifiers::migrate_nullifiers_table(&connection)?;

    println!(
        "Connecting to {} lightwalletd server(s): {}",
        lwd_urls.len(),
        lwd_urls.join(", ")
    );
    let t_start = std::time::Instant::now();

    let result = sync_nullifiers::sync(&connection, &lwd_urls, |height, tip, batch, total| {
        let elapsed = t_start.elapsed().as_secs_f64();
        let bps = if elapsed > 0.0 {
            (height - sync_nullifiers::NU5_ACTIVATION_HEIGHT) as f64 / elapsed
        } else {
            0.0
        };
        let remaining = (tip - height) as f64 / bps.max(1.0);
        println!(
            "  height {}/{} | +{} nfs | {} total nfs | {:.0} blocks/s | ~{:.0}s remaining",
            height, tip, batch, total, bps, remaining
        );
    })
    .await?;

    if result.blocks_synced == 0 {
        println!("Already up to date!");
    } else {
        println!(
            "\nIngestion done! {} nullifiers across {} blocks in {:.1}s",
            result.nullifiers_synced,
            result.blocks_synced,
            t_start.elapsed().as_secs_f64()
        );
    }

    println!("Database: {}", db_path);
    let count: u64 = connection.query_row(
        "SELECT COUNT(*) FROM nullifiers",
        [],
        |r| r.get(0),
    )?;
    println!("Total nullifiers in DB: {}", count);

    sync_nullifiers::rebuild_index(&connection)?;

    Ok(())
}
