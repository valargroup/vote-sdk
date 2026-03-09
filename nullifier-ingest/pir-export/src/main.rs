use std::path::PathBuf;
use std::time::Instant;

use anyhow::{Context, Result};
use clap::Parser;

use nullifier_service::file_store;

#[derive(Parser)]
#[command(name = "pir-export", about = "Build PIR tier databases from nullifier data")]
struct Args {
    /// Path to nullifiers.bin (sorted 32-byte Fp elements).
    #[arg(long)]
    nullifiers: PathBuf,

    /// Output directory for tier files.
    #[arg(long, default_value = "./pir-data")]
    output_dir: PathBuf,

    /// Path to nullifiers.checkpoint (16 bytes: [u64 height LE][u64 offset LE]).
    /// If provided, the sync height is embedded in root_info.json.
    #[arg(long)]
    checkpoint: Option<PathBuf>,
}

fn main() -> Result<()> {
    let args = Args::parse();
    let t_total = Instant::now();

    eprintln!("Loading nullifiers from {:?}...", args.nullifiers);
    let t0 = Instant::now();
    let data = std::fs::read(&args.nullifiers).context("read nullifiers file")?;
    let nfs = file_store::parse_nullifier_bytes(&data)?;
    eprintln!("  Loaded {} nullifiers in {:.1}s", nfs.len(), t0.elapsed().as_secs_f64());

    let height = match &args.checkpoint {
        Some(cp_path) => {
            let cp_data = std::fs::read(cp_path)
                .with_context(|| format!("read checkpoint file {:?}", cp_path))?;
            anyhow::ensure!(
                cp_data.len() >= 8,
                "checkpoint file too small: {} bytes (expected at least 8)",
                cp_data.len()
            );
            let h = u64::from_le_bytes(cp_data[..8].try_into().map_err(|_| {
                anyhow::anyhow!("checkpoint height prefix must be exactly 8 bytes")
            })?);
            eprintln!("  Checkpoint sync height: {}", h);
            Some(h)
        }
        None => None,
    };

    pir_export::build_and_export(nfs, &args.output_dir, height)?;

    eprintln!("\nDone! Total time: {:.1}s", t_total.elapsed().as_secs_f64());
    Ok(())
}
