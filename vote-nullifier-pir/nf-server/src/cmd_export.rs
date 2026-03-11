//! `nf-server export` — build PIR tree from nullifier data and write tier files.

use std::path::PathBuf;
use std::time::Instant;

use anyhow::Result;
use clap::Args as ClapArgs;

use nf_ingest::config;
use nf_ingest::file_store;

#[derive(ClapArgs)]
pub struct Args {
    /// Directory containing nullifiers.bin and nullifiers.checkpoint.
    #[arg(long, default_value = ".")]
    data_dir: PathBuf,

    /// Output directory for tier files (tier0.bin, tier1.bin, tier2.bin, pir_root.json).
    #[arg(long, default_value = "./pir-data")]
    output_dir: PathBuf,

    /// Export at this target block height instead of the full checkpoint height.
    /// Must be >= NU5 activation (1,687,104) and a multiple of 10.
    #[arg(long)]
    target_height: Option<u64>,
}

pub fn run(args: Args) -> Result<()> {
    let t_total = Instant::now();

    if let Some(th) = args.target_height {
        config::validate_export_height(th)?;
    }

    let t0 = Instant::now();
    let (nfs, height) = if let Some(target_height) = args.target_height {
        eprintln!("Looking up index for target height {}...", target_height);
        let entry = file_store::offset_for_height(&args.data_dir, target_height)?;
        match entry {
            Some((idx_height, byte_offset)) => {
                eprintln!("  Index: height={}, offset={} bytes", idx_height, byte_offset);
                let nfs = file_store::load_nullifiers_up_to(&args.data_dir, byte_offset)?;
                eprintln!(
                    "  Loaded {} nullifiers (up to height {}) in {:.1}s",
                    nfs.len(), idx_height, t0.elapsed().as_secs_f64()
                );
                (nfs, Some(idx_height))
            }
            None => {
                anyhow::bail!(
                    "no index entry found for target height {} — \
                     the nullifier data may not be synced to this height yet",
                    target_height
                );
            }
        }
    } else {
        eprintln!("Loading nullifiers from {:?}...", args.data_dir.join("nullifiers.bin"));
        let nfs = file_store::load_all_nullifiers(&args.data_dir)?;
        eprintln!("  Loaded {} nullifiers in {:.1}s", nfs.len(), t0.elapsed().as_secs_f64());
        let height = file_store::load_checkpoint(&args.data_dir)?
            .map(|(h, _)| h);
        (nfs, height)
    };

    pir_export::build_and_export(nfs, &args.output_dir, height)?;

    eprintln!("\nDone! Total time: {:.1}s", t_total.elapsed().as_secs_f64());
    Ok(())
}
