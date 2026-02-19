use std::path::Path;
use std::time::Instant;

use anyhow::Result;

use imt_tree::{build_sentinel_tree, NullifierTree};

use crate::file_store;

/// Build a [`NullifierTree`] from the flat nullifier file, merging sentinel
/// nullifiers.
///
/// The delegation circuit's q_interval gate range-checks interval widths to
/// < 2^250. Sentinel nullifiers at k * 2^250 (k = 0..=16) partition the Pallas
/// field so that every gap range stays within this bound.
///
/// The checkpoint height from the data directory is embedded in the tree so
/// that it survives serialization via `save_full`/`load_full`.
pub fn tree_from_file(dir: &Path) -> Result<NullifierTree> {
    let t0 = Instant::now();
    let nfs = file_store::load_all_nullifiers(dir)?;
    eprintln!(
        "  Flat file: {} nullifiers loaded in {:.1}s",
        nfs.len(),
        t0.elapsed().as_secs_f64()
    );
    let mut tree = build_sentinel_tree(&nfs)?;

    // Embed the checkpoint height so it persists through save_full/load_full.
    if let Some((h, _)) = file_store::load_checkpoint(dir)? {
        tree.set_height(h);
    }

    Ok(tree)
}
