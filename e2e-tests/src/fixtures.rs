//! Helpers for managing large E2E fixture files.
//!
//! The voter throughput test can reuse large JSON fixtures generated offline.
//! To keep the repo lean, those files are downloaded on demand the first time
//! a test points at a fixture directory that does not already contain them.

use std::path::Path;

const DEFAULT_VOTER_FIXTURE_HOST: &str = "https://vote.fra1.digitaloceanspaces.com";
const VOTER_FIXTURE_FILES: [&str; 3] = ["manifest.json", "delegations.json", "cast_vote_inputs.json"];

#[must_use]
pub fn ensure_voter_fixture_files(
    dir: &Path,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    std::fs::create_dir_all(dir)?;

    let missing_files: Vec<&str> = VOTER_FIXTURE_FILES
        .iter()
        .copied()
        .filter(|name| !dir.join(name).exists())
        .collect();

    if missing_files.is_empty() {
        return Ok(());
    }

    let base_url = voter_fixture_base_url(dir)?;
    eprintln!(
        "[E2E] Missing {} voter fixture file(s) in {}; downloading from {}",
        missing_files.len(),
        dir.display(),
        base_url
    );

    for file_name in missing_files {
        download_fixture_file(dir, &base_url, file_name)?;
    }

    Ok(())
}

fn voter_fixture_base_url(dir: &Path) -> Result<String, Box<dyn std::error::Error + Send + Sync>> {
    if let Ok(url) = std::env::var("VOTER_FIXTURE_BASE_URL") {
        let trimmed = url.trim().trim_end_matches('/');
        if !trimmed.is_empty() {
            return Ok(trimmed.to_string());
        }
    }

    let dir_name = dir
        .file_name()
        .and_then(|name| name.to_str())
        .ok_or_else(|| format!("fixture directory '{}' has no final path component", dir.display()))?;

    Ok(format!("{DEFAULT_VOTER_FIXTURE_HOST}/{dir_name}"))
}

fn download_fixture_file(
    dir: &Path,
    base_url: &str,
    file_name: &str,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let url = format!("{}/{}", base_url.trim_end_matches('/'), file_name);
    let response = crate::api::client().get(&url).send()?;
    let status = response.status();

    if !status.is_success() {
        return Err(format!("failed to download {} from {}: HTTP {}", file_name, url, status).into());
    }

    let bytes = response.bytes()?;
    let destination = dir.join(file_name);
    let temp_path = dir.join(format!(".{}.part", file_name));

    std::fs::write(&temp_path, &bytes)?;
    std::fs::rename(&temp_path, &destination)?;

    eprintln!(
        "[E2E] Downloaded {} to {} ({} bytes)",
        file_name,
        destination.display(),
        bytes.len()
    );

    Ok(())
}
