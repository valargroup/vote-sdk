//! Shared configuration constants and helpers for the nullifier pipeline.

/// Default lightwalletd gRPC endpoints used when no override is provided.
pub const DEFAULT_LWD_URLS: &[&str] = &[
    "https://zec.rocks:443",
    "https://eu2.zec.stardust.rest:443",
    "https://eu.zec.stardust.rest:443",
];

/// The default single URL used in CLI `--lwd-url` defaults.
/// When the resolved URL list contains only this entry (and no `LWD_URLS` env
/// override was set), the full `DEFAULT_LWD_URLS` list is used instead.
const DEFAULT_SINGLE_LWD_URL: &str = "https://zec.rocks:443";

/// Stale sidecar and PIR tier files that should be deleted after re-ingestion
/// so the next export rebuilds from fresh data.
pub const STALE_FILES: &[&str] = &[
    "nullifiers.tree",
    "pir-data/tier0.bin",
    "pir-data/tier1.bin",
    "pir-data/tier2.bin",
    "pir-data/pir_root.json",
];

/// Resolve lightwalletd URLs from the `LWD_URLS` env var, a CLI-provided URL,
/// or the hardcoded defaults.
///
/// Priority:
/// 1. `LWD_URLS` env var (comma-separated) if set and non-empty
/// 2. `cli_url` if it differs from the default single URL
/// 3. `DEFAULT_LWD_URLS` as a fallback
pub fn resolve_lwd_urls(cli_url: &str) -> Vec<String> {
    let urls: Vec<String> = std::env::var("LWD_URLS")
        .map(|s| s.split(',').map(|u| u.trim().to_string()).collect())
        .unwrap_or_else(|_| vec![cli_url.to_string()]);

    if urls.len() == 1 && urls[0] == DEFAULT_SINGLE_LWD_URL {
        DEFAULT_LWD_URLS.iter().map(|s| s.to_string()).collect()
    } else {
        urls
    }
}
