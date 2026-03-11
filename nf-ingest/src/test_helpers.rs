//! Shared test utilities for the nf-ingest crate.

use std::path::PathBuf;

/// Create a unique temporary directory for a test, cleaning any previous run.
pub fn temp_dir(prefix: &str, name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "nf_{}_test_{}_{}",
        prefix,
        std::process::id(),
        name,
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}
