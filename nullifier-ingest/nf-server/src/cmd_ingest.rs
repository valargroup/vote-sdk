use std::path::PathBuf;

use anyhow::Result;
use clap::Args as ClapArgs;

use nullifier_service::config;
use nullifier_service::file_store;
use nullifier_service::sync_nullifiers;

#[derive(ClapArgs)]
pub struct Args {
    /// Directory containing nullifiers.bin and nullifiers.checkpoint.
    #[arg(long, default_value = ".")]
    data_dir: PathBuf,

    /// Lightwalletd endpoint URL. Overridden by LWD_URLS env (comma-separated).
    #[arg(long, default_value = "https://zec.rocks:443")]
    lwd_url: String,

    /// Stop syncing at this block height (must be a multiple of 10).
    #[arg(long)]
    max_height: Option<u64>,

    /// Delete stale sidecar files (nullifiers.tree, tier files) after ingestion.
    #[arg(long)]
    invalidate: bool,
}

pub async fn run(args: Args) -> Result<()> {
    // Migrate legacy checkpoints to the index file format.
    file_store::rebuild_index(&args.data_dir)?;

    let lwd_urls = config::resolve_lwd_urls(&args.lwd_url);

    let dir = &args.data_dir;

    println!("Data directory: {}", dir.display());
    if let Some(h) = args.max_height {
        println!("Max height: {}", h);
    }
    println!(
        "Connecting to {} lightwalletd server(s): {}",
        lwd_urls.len(),
        lwd_urls.join(", ")
    );
    let t_start = std::time::Instant::now();

    let result = sync_nullifiers::sync(dir, &lwd_urls, args.max_height, |height, target, batch, total| {
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

        if args.invalidate {
            for name in config::STALE_FILES {
                let path = dir.join(name);
                if path.exists() {
                    std::fs::remove_file(&path)?;
                    println!("Deleted stale file: {}", path.display());
                }
            }
        }
    }

    let count = file_store::nullifier_count(dir)?;
    println!("Total nullifiers: {}", count);

    Ok(())
}
