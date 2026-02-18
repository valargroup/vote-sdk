use std::env;
use std::path::Path;

use anyhow::Result;

use nullifier_service::file_store;
use nullifier_service::sync_nullifiers;

/// Default lightwalletd endpoints
const DEFAULT_LWD_URLS: &[&str] = &[
    "https://zec.rocks:443",
    "https://eu2.zec.stardust.rest:443",
    "https://eu.zec.stardust.rest:443",
];

#[tokio::main]
async fn main() -> Result<()> {
    let lwd_urls: Vec<String> = env::var("LWD_URLS")
        .map(|s| s.split(',').map(|u| u.trim().to_string()).collect())
        .unwrap_or_else(|_| {
            env::var("LWD_URL")
                .map(|u| vec![u])
                .unwrap_or_else(|_| DEFAULT_LWD_URLS.iter().map(|s| s.to_string()).collect())
        });
    let data_dir = env::var("DATA_DIR").unwrap_or_else(|_| ".".to_string());
    let dir = Path::new(&data_dir);

    let max_height: Option<u64> = env::var("MAX_HEIGHT")
        .ok()
        .filter(|s| !s.is_empty())
        .map(|s| s.parse().expect("MAX_HEIGHT must be a valid integer"));

    println!("Data directory: {}", dir.display());
    if let Some(h) = max_height {
        println!("Max height: {}", h);
    }
    println!(
        "Connecting to {} lightwalletd server(s): {}",
        lwd_urls.len(),
        lwd_urls.join(", ")
    );
    let t_start = std::time::Instant::now();

    let result = sync_nullifiers::sync(dir, &lwd_urls, max_height, |height, target, batch, total| {
        let elapsed = t_start.elapsed().as_secs_f64();
        let bps = if elapsed > 0.0 {
            (height - sync_nullifiers::NU5_ACTIVATION_HEIGHT) as f64 / elapsed
        } else {
            0.0
        };
        let remaining = (target - height) as f64 / bps.max(1.0);
        println!(
            "  height {}/{} | +{} nfs | {} total nfs | {:.0} blocks/s | ~{:.0}s remaining",
            height, target, batch, total, bps, remaining
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

        // Delete the sidecar tree so the server rebuilds from the updated data.
        if env::var("INVALIDATE_TREE").is_ok() {
            let sidecar = dir.join("nullifiers.tree");
            if sidecar.exists() {
                std::fs::remove_file(&sidecar)?;
                println!("Deleted stale sidecar: {}", sidecar.display());
            }
        }
    }

    let count = file_store::nullifier_count(dir)?;
    println!("Total nullifiers: {}", count);

    Ok(())
}
