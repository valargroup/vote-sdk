//! PIR client library for private Merkle path retrieval.
//!
//! Provides [`PirClient`] which connects to a `pir-server` instance and
//! retrieves circuit-ready `ImtProofData` without revealing the queried
//! nullifier to the server.

use std::time::Instant;

use anyhow::{Context, Result};
use ff::PrimeField as _;
use pasta_curves::Fp;

use imt_tree::hasher::PoseidonHasher;
use imt_tree::tree::{precompute_empty_hashes, TREE_DEPTH};
// Re-exported so downstream crates (e.g. librustvoting) can reference the type
// returned by PirClientBlocking::fetch_proof without a direct imt-tree dependency.
pub use imt_tree::ImtProofData;

use pir_types::tier0::Tier0Data;
use pir_types::tier1::Tier1Row;
use pir_types::tier2::Tier2Row;
use pir_types::{
    serialize_ypir_query, RootInfo, YpirScenario, PIR_DEPTH, TIER0_LAYERS, TIER1_LAYERS,
    TIER1_LEAVES, TIER1_ROW_BYTES, TIER2_LEAVES, TIER2_ROW_BYTES,
};

use ypir::client::YPIRClient;

// ── Timing breakdown ─────────────────────────────────────────────────────────

/// Per-tier timing breakdown for a single YPIR query, measuring each stage
/// of the client-server round trip.
struct TierTiming {
    /// Client-side YPIR query generation time.
    gen_ms: f64,
    /// Size of the uploaded query payload.
    upload_bytes: usize,
    /// Size of the downloaded encrypted response.
    download_bytes: usize,
    /// Wall-clock round-trip time (upload + server compute + download).
    rtt_ms: f64,
    /// Client-side YPIR response decryption time.
    decode_ms: f64,
    /// Server-assigned request ID (from response header).
    server_req_id: Option<u64>,
    /// Server-reported total processing time.
    server_total_ms: Option<f64>,
    /// Server-reported query validation time.
    server_validate_ms: Option<f64>,
    /// Server-reported decode+copy time.
    server_decode_copy_ms: Option<f64>,
    /// Server-reported YPIR online computation time.
    server_compute_ms: Option<f64>,
    /// Estimated network + queue latency (RTT minus server time).
    net_queue_ms: Option<f64>,
    /// Estimated upload-to-server latency.
    upload_to_server_ms: Option<f64>,
    /// Estimated download-from-server latency.
    download_from_server_ms: f64,
}

/// Per-note timing breakdown covering both tier 1 and tier 2 YPIR queries.
struct NoteTiming {
    tier1: TierTiming,
    tier2: TierTiming,
    /// Total wall-clock time for this note's proof retrieval.
    total_ms: f64,
}

// ── HTTP-based PIR client ────────────────────────────────────────────────────

/// PIR client that connects to a `pir-server` instance over HTTP.
///
/// Downloads Tier 0 data and YPIR parameters during `connect()`, then
/// performs private queries via `fetch_proof()`.
pub struct PirClient {
    server_url: String,
    http: reqwest::Client,
    tier0: Tier0Data,
    tier1_scenario: YpirScenario,
    tier2_scenario: YpirScenario,
    num_ranges: usize,
    empty_hashes: [Fp; TREE_DEPTH],
    root29: Fp,
}

/// Return the number of populated leaves in a Tier 2 row, clamped to
/// [`TIER2_LEAVES`]. The final row may be only partially filled when
/// `num_ranges` is not a multiple of the row size.
#[inline]
fn valid_leaves_for_row(num_ranges: usize, row_idx: usize) -> usize {
    let row_start = row_idx.saturating_mul(TIER2_LEAVES);
    num_ranges.saturating_sub(row_start).min(TIER2_LEAVES)
}

// ── Shared tier-processing helpers ───────────────────────────────────────────

/// Copy `siblings` into `path` starting at `offset`.
#[inline]
fn fill_path(path: &mut [Fp; TREE_DEPTH], offset: usize, siblings: &[Fp]) {
    path[offset..offset + siblings.len()].copy_from_slice(siblings);
}

/// Locate the nullifier's subtree in Tier 0, fill its siblings into `path`,
/// and return the subtree index `s1`.
fn process_tier0(
    tier0: &Tier0Data,
    nullifier: Fp,
    path: &mut [Fp; TREE_DEPTH],
) -> Result<usize> {
    let s1 = tier0
        .find_subtree(nullifier)
        .context("nullifier not found in any Tier 0 subtree")?;
    fill_path(path, PIR_DEPTH - TIER0_LAYERS, &tier0.extract_siblings(s1));
    Ok(s1)
}

/// Parse a Tier 1 row, locate the nullifier's sub-subtree, fill its siblings
/// into `path`, and return the sub-subtree index `s2`.
fn process_tier1(
    tier1_row: &[u8],
    nullifier: Fp,
    path: &mut [Fp; TREE_DEPTH],
) -> Result<usize> {
    let tier1 = Tier1Row::from_bytes(tier1_row)?;
    let s2 = tier1
        .find_sub_subtree(nullifier)
        .context("nullifier not found in any Tier 1 sub-subtree")?;
    fill_path(path, PIR_DEPTH - TIER0_LAYERS - TIER1_LAYERS, &tier1.extract_siblings(s2));
    Ok(s2)
}

/// Parse a Tier 2 row, locate the nullifier's leaf, fill tier-2 and padding
/// siblings into `path`, and assemble the final [`ImtProofData`].
fn process_tier2_and_build(
    tier2_row: &[u8],
    t2_row_idx: usize,
    num_ranges: usize,
    nullifier: Fp,
    path: &mut [Fp; TREE_DEPTH],
    empty_hashes: &[Fp; TREE_DEPTH],
    root29: Fp,
) -> Result<ImtProofData> {
    let hasher = PoseidonHasher::new();
    let tier2 = Tier2Row::from_bytes(tier2_row)?;
    let valid_leaves = valid_leaves_for_row(num_ranges, t2_row_idx);

    let leaf_local_idx = tier2
        .find_leaf(nullifier, valid_leaves)
        .context("nullifier not found in Tier 2 leaf scan")?;

    // Fill the bottom 8 levels of the Merkle path with the tier-2 siblings
    fill_path(path, 0, &tier2.extract_siblings(leaf_local_idx, valid_leaves, &hasher));
    // Fill the last 3 levels with empty hashes.
    // At the current nullifier numbers, ~51, within 2^26 -> height 26 is sufficient.
    // Changing height would require regenerating . To bullet-proof the system, we create
    // circuits that assume height of 29 which fits 536M.
    // To bridge the gap between what we have today in PIR and the circuit's assumptions,
    // we add empty hash pads.
    fill_path(path, PIR_DEPTH, &empty_hashes[PIR_DEPTH..TREE_DEPTH]);

    let global_leaf_idx = t2_row_idx * TIER2_LEAVES + leaf_local_idx;
    let (low, width) = tier2.leaf_record(leaf_local_idx);

    Ok(ImtProofData {
        root: root29,
        low,
        width,
        leaf_pos: global_leaf_idx as u32,
        path: *path,
    })
}

impl PirClient {
    /// Connect to a PIR server, downloading Tier 0 data and YPIR parameters.
    pub async fn connect(server_url: &str) -> Result<Self> {
        let http = reqwest::Client::new();
        let base = server_url.trim_end_matches('/');

        // Download Tier 0 data, YPIR params, and root concurrently
        let t0 = Instant::now();
        let (tier0_resp, tier1_resp, tier2_resp, root_resp) = tokio::try_join!(
            http.get(format!("{base}/tier0")).send(),
            http.get(format!("{base}/params/tier1")).send(),
            http.get(format!("{base}/params/tier2")).send(),
            http.get(format!("{base}/root")).send(),
        )
        .map_err(|e| anyhow::anyhow!("connect fetch failed: {e}"))?;

        let tier0_bytes = tier0_resp.error_for_status()?.bytes().await?;
        log::debug!(
            "Downloaded Tier 0: {} bytes in {:.1}s",
            tier0_bytes.len(),
            t0.elapsed().as_secs_f64()
        );
        let tier0 = Tier0Data::from_bytes(tier0_bytes.to_vec())?;

        let tier1_scenario: YpirScenario = tier1_resp
            .error_for_status()
            .context("GET /params/tier1 failed")?
            .json()
            .await?;
        let tier2_scenario: YpirScenario = tier2_resp
            .error_for_status()
            .context("GET /params/tier2 failed")?
            .json()
            .await?;

        let root_info: RootInfo = root_resp
            .error_for_status()
            .context("GET /root failed")?
            .json()
            .await?;
        anyhow::ensure!(
            root_info.pir_depth == PIR_DEPTH,
            "server pir_depth {} != expected {}",
            root_info.pir_depth,
            PIR_DEPTH
        );
        let root29_bytes = hex::decode(&root_info.root29)?;
        anyhow::ensure!(
            root29_bytes.len() == 32,
            "root29 hex decoded to {} bytes, expected 32",
            root29_bytes.len()
        );
        let mut root29_arr = [0u8; 32];
        root29_arr.copy_from_slice(&root29_bytes);
        let root29 = Option::from(Fp::from_repr(root29_arr))
            .ok_or_else(|| anyhow::anyhow!("invalid root29 field element"))?;

        let empty_hashes = precompute_empty_hashes();

        Ok(Self {
            server_url: base.to_string(),
            http,
            tier0,
            tier1_scenario,
            tier2_scenario,
            num_ranges: root_info.num_ranges,
            empty_hashes,
            root29,
        })
    }

    /// Perform private Merkle path retrieval for a nullifier.
    ///
    /// Returns circuit-ready `ImtProofData` with a 29-element path
    /// (26 PIR siblings + 3 empty-hash padding).
    pub async fn fetch_proof(&self, nullifier: Fp) -> Result<ImtProofData> {
        let (proof, _timing) = self.fetch_proof_inner(nullifier).await?;
        Ok(proof)
    }

    /// Perform private Merkle path retrieval for multiple nullifiers in parallel.
    ///
    /// All queries run concurrently via `try_join_all`, sharing the same
    /// `PirClient` (and thus the same HTTP client and Tier 0 data).
    pub async fn fetch_proofs(&self, nullifiers: &[Fp]) -> Result<Vec<ImtProofData>> {
        log::debug!(
            "[PIR] Starting parallel fetch for {} notes...",
            nullifiers.len()
        );
        let wall_start = Instant::now();

        let futures: Vec<_> = nullifiers
            .iter()
            .enumerate()
            .map(|(i, &nf)| async move {
                let (proof, timing) = self.fetch_proof_inner(nf).await?;
                Ok::<_, anyhow::Error>((i, proof, timing))
            })
            .collect();

        let results_with_timing = futures::future::try_join_all(futures).await?;
        let wall_ms = wall_start.elapsed().as_secs_f64() * 1000.0;

        print_timing_table(&results_with_timing, wall_ms);

        let proofs = results_with_timing
            .into_iter()
            .map(|(_, proof, _)| proof)
            .collect();
        Ok(proofs)
    }

    /// Fetch proof and return timing breakdown.
    async fn fetch_proof_inner(&self, nullifier: Fp) -> Result<(ImtProofData, NoteTiming)> {
        let note_start = Instant::now();
        let mut path = [Fp::default(); TREE_DEPTH];

        // Process tier 0 (plaintext)
        let s1 = process_tier0(&self.tier0, nullifier, &mut path)?;

        // Process tier 1 (PIR)
        let (tier1_row, tier1_timing) = self
            .ypir_query(&self.tier1_scenario, "tier1", s1, TIER1_ROW_BYTES)
            .await?;
        let s2 = process_tier1(&tier1_row, nullifier, &mut path)?;

        // Process tier 2 (PIR)
        let t2_row_idx = s1 * TIER1_LEAVES + s2;
        let (tier2_row, tier2_timing) = self
            .ypir_query(&self.tier2_scenario, "tier2", t2_row_idx, TIER2_ROW_BYTES)
            .await?;

        let proof = process_tier2_and_build(
            &tier2_row,
            t2_row_idx,
            self.num_ranges,
            nullifier,
            &mut path,
            &self.empty_hashes,
            self.root29,
        )?;

        let total_ms = note_start.elapsed().as_secs_f64() * 1000.0;
        Ok((proof, NoteTiming { tier1: tier1_timing, tier2: tier2_timing, total_ms }))
    }

    /// Send a YPIR query for a tier row and return the decrypted row bytes.
    /// This function handles the key client PIR operations:
    /// 1. Generate keys
    /// 2. Query
    /// 3. Recover 
    async fn ypir_query(
        &self,
        scenario: &YpirScenario,
        tier_name: &str,
        row_idx: usize,
        expected_row_bytes: usize,
    ) -> Result<(Vec<u8>, TierTiming)> {
        anyhow::ensure!(
            row_idx < scenario.num_items,
            "{} row_idx {} >= num_items {}",
            tier_name, row_idx, scenario.num_items
        );
        let t0 = Instant::now();
        let ypir_client = YPIRClient::from_db_sz(
            scenario.num_items as u64,
            scenario.item_size_bits as u64,
            true,
        );

        // Generate PIR query from a fresh secret created from OsRng seed.
        let (query, seed) = ypir_client.generate_query_simplepir(row_idx);
        let gen_ms = t0.elapsed().as_secs_f64() * 1000.0;

        // Serialize query
        let payload = serialize_ypir_query(query.0.as_slice(), query.1.as_slice());
        let upload_bytes = payload.len();

        // Send the request
        let t1 = Instant::now();
        let url = format!("{}/{}/query", self.server_url, tier_name);
        let send_result = self.http.post(&url).body(payload).send().await;
        let send_ms = t1.elapsed().as_secs_f64() * 1000.0;
        let resp = match send_result {
            Ok(r) => r,
            Err(e) => {
                log::warn!("YPIR {} send error: {:?}", tier_name, e);
                return Err(e.into());
            }
        };
        let server_req_id = parse_header_u64(resp.headers(), "x-pir-req-id");
        let server_total_ms = parse_header_f64(resp.headers(), "x-pir-server-total-ms");
        let server_validate_ms = parse_header_f64(resp.headers(), "x-pir-server-validate-ms");
        let server_decode_copy_ms = parse_header_f64(resp.headers(), "x-pir-server-decode-copy-ms");
        let server_compute_ms = parse_header_f64(resp.headers(), "x-pir-server-compute-ms");
        let status = resp.status();
        let response_bytes = resp.bytes().await?;
        if !status.is_success() {
            anyhow::bail!(
                "{} query failed: HTTP {} body={}",
                tier_name, status, String::from_utf8_lossy(&response_bytes)
            );
        }
        let rtt_ms = t1.elapsed().as_secs_f64() * 1000.0;
        let download_from_server_ms = (rtt_ms - send_ms).max(0.0);
        let net_queue_ms = server_total_ms.map(|server_ms| (rtt_ms - server_ms).max(0.0));
        let upload_to_server_ms = server_total_ms.map(|server_ms| {
            (send_ms - server_ms).max(0.0)
        });

        // Decode the response from the server.
        let t2 = Instant::now();
        let decoded = ypir_client.decode_response_simplepir(seed, &response_bytes);
        let decode_ms = t2.elapsed().as_secs_f64() * 1000.0;

        anyhow::ensure!(
            decoded.len() >= expected_row_bytes,
            "{} decoded response too short: {} bytes, expected >= {}",
            tier_name, decoded.len(), expected_row_bytes
        );
        Ok((
            decoded[..expected_row_bytes].to_vec(),
            TierTiming {
                gen_ms,
                upload_bytes,
                download_bytes: response_bytes.len(),
                rtt_ms,
                decode_ms,
                server_req_id,
                server_total_ms,
                server_validate_ms,
                server_decode_copy_ms,
                server_compute_ms,
                net_queue_ms,
                upload_to_server_ms,
                download_from_server_ms,
            },
        ))
    }
}

fn fmt_time(ms: f64) -> String {
    if ms >= 1000.0 {
        format!("{:>5.1}s ", ms / 1000.0)
    } else {
        format!("{:>5.0}ms", ms)
    }
}

fn fmt_opt_time(ms: Option<f64>) -> String {
    match ms {
        Some(v) => fmt_time(v),
        None => "  n/a ".to_string(),
    }
}

/// Print a detailed timing breakdown table for a batch of PIR proof fetches.
fn print_timing_table(results: &[(usize, ImtProofData, NoteTiming)], wall_ms: f64) {
    if !log::log_enabled!(log::Level::Debug) {
        return;
    }

    log::debug!("[PIR] ┌─────┬──────────┬─────────────┬──────────┬──────────┬─────────────┬──────────┬────────┐");
    log::debug!("[PIR] │ Note│ T1 keygen│ T1 upload+  │ T1 decode│ T2 keygen│ T2 upload+  │ T2 decode│ Total  │");
    log::debug!("[PIR] │     │ (client) │ server+down │ (client) │ (client) │ server+down │ (client) │        │");
    log::debug!("[PIR] ├─────┼──────────┼─────────────┼──────────┼──────────┼─────────────┼──────────┼────────┤");
    for &(i, _, ref t) in results {
        log::debug!(
            "[PIR] │  {i:>2} │  {:>6} │   {:>7}   │  {:>6} │  {:>6} │   {:>7}   │  {:>6} │{} │",
            fmt_time(t.tier1.gen_ms),
            fmt_time(t.tier1.rtt_ms),
            fmt_time(t.tier1.decode_ms),
            fmt_time(t.tier2.gen_ms),
            fmt_time(t.tier2.rtt_ms),
            fmt_time(t.tier2.decode_ms),
            fmt_time(t.total_ms),
        );
    }
    log::debug!("[PIR] └─────┴──────────┴─────────────┴──────────┴──────────┴─────────────┴──────────┴────────┘");
    log::debug!(
        "[PIR] Upload per note: T1={:.0}KB T2={:.1}MB  |  Wall clock: {:.2}s",
        results
            .first()
            .map(|(_, _, t)| t.tier1.upload_bytes)
            .unwrap_or(0) as f64
            / 1024.0,
        results
            .first()
            .map(|(_, _, t)| t.tier2.upload_bytes)
            .unwrap_or(0) as f64
            / (1024.0 * 1024.0),
        wall_ms / 1000.0,
    );

    for &(i, _, ref t) in results {
        log::trace!(
            "[PIR] Note {i:>2} transfer: T1 up={:.0}KB down={:.0}KB | T2 up={:.1}MB down={:.0}KB",
            t.tier1.upload_bytes as f64 / 1024.0,
            t.tier1.download_bytes as f64 / 1024.0,
            t.tier2.upload_bytes as f64 / (1024.0 * 1024.0),
            t.tier2.download_bytes as f64 / 1024.0,
        );
        log::trace!(
            "[PIR] Note {i:>2} server/net: T1 {} / {} | T2 {} / {}",
            fmt_opt_time(t.tier1.server_total_ms),
            fmt_opt_time(t.tier1.net_queue_ms),
            fmt_opt_time(t.tier2.server_total_ms),
            fmt_opt_time(t.tier2.net_queue_ms),
        );
        log::trace!(
            "[PIR] Note {i:>2} up/srv/down: T1 {} / {} / {} | T2 {} / {} / {}",
            fmt_opt_time(t.tier1.upload_to_server_ms),
            fmt_opt_time(t.tier1.server_total_ms),
            fmt_time(t.tier1.download_from_server_ms),
            fmt_opt_time(t.tier2.upload_to_server_ms),
            fmt_opt_time(t.tier2.server_total_ms),
            fmt_time(t.tier2.download_from_server_ms),
        );
        log::trace!(
            "[PIR] Note {i:>2} server stages: T1(v={} copy={} compute={}) T2(v={} copy={} compute={})",
            fmt_opt_time(t.tier1.server_validate_ms),
            fmt_opt_time(t.tier1.server_decode_copy_ms),
            fmt_opt_time(t.tier1.server_compute_ms),
            fmt_opt_time(t.tier2.server_validate_ms),
            fmt_opt_time(t.tier2.server_decode_copy_ms),
            fmt_opt_time(t.tier2.server_compute_ms),
        );
        log::trace!(
            "[PIR] Note {i:>2} req ids: T1={:?} T2={:?}",
            t.tier1.server_req_id, t.tier2.server_req_id
        );
    }
}

/// Parse an HTTP response header value as `f64`, returning `None` on missing or malformed values.
fn parse_header_f64(headers: &reqwest::header::HeaderMap, name: &'static str) -> Option<f64> {
    headers
        .get(name)
        .and_then(|v| v.to_str().ok())
        .and_then(|s| s.parse::<f64>().ok())
}

/// Parse an HTTP response header value as `u64`, returning `None` on missing or malformed values.
fn parse_header_u64(headers: &reqwest::header::HeaderMap, name: &'static str) -> Option<u64> {
    headers
        .get(name)
        .and_then(|v| v.to_str().ok())
        .and_then(|s| s.parse::<u64>().ok())
}

// ── Blocking wrapper ─────────────────────────────────────────────────────────

/// Synchronous wrapper around [`PirClient`] for use from non-async code.
///
/// Owns a Tokio runtime internally so callers (e.g. librustvoting, which must
/// stay synchronous for the Halo2 prover) don't need to manage one.
pub struct PirClientBlocking {
    inner: PirClient,
    rt: tokio::runtime::Runtime,
}

impl PirClientBlocking {
    /// Connect to a PIR server (blocking). Downloads Tier 0 data and YPIR params.
    pub fn connect(server_url: &str) -> Result<Self> {
        let rt = tokio::runtime::Runtime::new()?;
        let inner = rt.block_on(PirClient::connect(server_url))?;
        Ok(Self { inner, rt })
    }

    /// Perform a private Merkle path retrieval for a nullifier (blocking).
    pub fn fetch_proof(&self, nullifier: Fp) -> Result<ImtProofData> {
        self.rt.block_on(self.inner.fetch_proof(nullifier))
    }

    /// Perform private Merkle path retrieval for multiple nullifiers in parallel (blocking).
    pub fn fetch_proofs(&self, nullifiers: &[Fp]) -> Result<Vec<ImtProofData>> {
        self.rt.block_on(self.inner.fetch_proofs(nullifiers))
    }

    /// The depth-29 root (PIR depth 26 padded to tree depth 29).
    pub fn root29(&self) -> Fp {
        self.inner.root29
    }
}

// ── Local (in-process) PIR client ────────────────────────────────────────────

/// Perform a complete local PIR proof retrieval without HTTP.
///
/// This is used by `pir-test local` mode. It takes the tier data directly
/// (as built by `pir-export`) and performs the YPIR operations in-process.
pub fn fetch_proof_local(
    tier0_data: &[u8],
    tier1_data: &[u8],
    tier2_data: &[u8],
    num_ranges: usize,
    nullifier: Fp,
    empty_hashes: &[Fp; TREE_DEPTH],
    root29: Fp,
) -> Result<ImtProofData> {
    let mut path = [Fp::default(); TREE_DEPTH];
    let tier0 = Tier0Data::from_bytes(tier0_data.to_vec())?;

    let s1 = process_tier0(&tier0, nullifier, &mut path)?;

    // ── Tier 1: direct row lookup (no YPIR in local mode) ────────────────
    let t1_offset = s1 * TIER1_ROW_BYTES;
    anyhow::ensure!(
        t1_offset + TIER1_ROW_BYTES <= tier1_data.len(),
        "tier1 data too short: need {} bytes at offset {}, have {}",
        TIER1_ROW_BYTES,
        t1_offset,
        tier1_data.len()
    );
    let s2 = process_tier1(
        &tier1_data[t1_offset..t1_offset + TIER1_ROW_BYTES],
        nullifier,
        &mut path,
    )?;

    // ── Tier 2: direct row lookup (no YPIR in local mode) ────────────────
    let t2_row_idx = s1 * TIER1_LEAVES + s2;
    let t2_offset = t2_row_idx * TIER2_ROW_BYTES;
    anyhow::ensure!(
        t2_offset + TIER2_ROW_BYTES <= tier2_data.len(),
        "tier2 data too short: need {} bytes at offset {}, have {}",
        TIER2_ROW_BYTES,
        t2_offset,
        tier2_data.len()
    );

    process_tier2_and_build(
        &tier2_data[t2_offset..t2_offset + TIER2_ROW_BYTES],
        t2_row_idx,
        num_ranges,
        nullifier,
        &mut path,
        empty_hashes,
        root29,
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use ff::Field;
    use pasta_curves::Fp;
    use pir_export::build_ranges_with_sentinels;

    /// Build a tree and export all three tier blobs.
    struct TestFixture {
        tier0_data: Vec<u8>,
        tier1_data: Vec<u8>,
        tier2_data: Vec<u8>,
        ranges: Vec<[Fp; 2]>,
        empty_hashes: [Fp; TREE_DEPTH],
        root29: Fp,
    }

    impl TestFixture {
        fn build(raw_nfs: &[Fp]) -> Self {
            let ranges = build_ranges_with_sentinels(raw_nfs);
            let tree = pir_export::build_pir_tree(ranges.clone()).unwrap();

            let tier0_data = pir_export::tier0::export(
                &tree.root26,
                &tree.levels,
                &tree.ranges,
                &tree.empty_hashes,
            );
            let mut tier1_data = Vec::new();
            pir_export::tier1::export(
                &tree.levels,
                &tree.ranges,
                &tree.empty_hashes,
                &mut tier1_data,
            )
            .unwrap();
            let mut tier2_data = Vec::new();
            pir_export::tier2::export(
                &tree.levels,
                &tree.ranges,
                &tree.empty_hashes,
                &mut tier2_data,
            )
            .unwrap();

            Self {
                tier0_data,
                tier1_data,
                tier2_data,
                ranges,
                empty_hashes: tree.empty_hashes,
                root29: tree.root29,
            }
        }
    }

    // ── fetch_proof_local round-trip ──────────────────────────────────────

    #[test]
    fn fetch_proof_local_verifies_for_known_ranges() {
        let mut rng = rand::thread_rng();
        let raw_nfs: Vec<Fp> = (0..100).map(|_| Fp::random(&mut rng)).collect();
        let fix = TestFixture::build(&raw_nfs);

        for &[low, _] in fix.ranges.iter().take(20) {
            let proof = fetch_proof_local(
                &fix.tier0_data,
                &fix.tier1_data,
                &fix.tier2_data,
                fix.ranges.len(),
                low,
                &fix.empty_hashes,
                fix.root29,
            )
            .expect("fetch_proof_local should succeed for a valid range low");
            assert!(
                proof.verify(low),
                "proof should verify for value {}",
                hex::encode(low.to_repr())
            );
        }
    }

    #[test]
    fn fetch_proof_local_correct_root_and_path_length() {
        let raw_nfs: Vec<Fp> = (1u64..=50).map(|i| Fp::from(i * 997)).collect();
        let fix = TestFixture::build(&raw_nfs);

        let value = fix.ranges[0][0];
        let proof = fetch_proof_local(
            &fix.tier0_data,
            &fix.tier1_data,
            &fix.tier2_data,
            fix.ranges.len(),
            value,
            &fix.empty_hashes,
            fix.root29,
        )
        .unwrap();

        assert_eq!(proof.root, fix.root29);
        assert_eq!(proof.path.len(), TREE_DEPTH);
    }

    // ── process_tier0 ────────────────────────────────────────────────────

    #[test]
    fn process_tier0_fills_correct_path_region() {
        let raw_nfs: Vec<Fp> = (1u64..=30).map(|i| Fp::from(i * 1013)).collect();
        let fix = TestFixture::build(&raw_nfs);
        let tier0 = Tier0Data::from_bytes(fix.tier0_data).unwrap();

        let value = fix.ranges[0][0];
        let mut path = [Fp::default(); TREE_DEPTH];
        let s1 = process_tier0(&tier0, value, &mut path).unwrap();

        assert!(s1 < pir_types::TIER1_ROWS);

        let tier0_region = &path[PIR_DEPTH - TIER0_LAYERS..PIR_DEPTH];
        assert!(
            tier0_region.iter().any(|&v| v != Fp::default()),
            "tier0 should write at least one non-zero sibling"
        );

        let below = &path[..PIR_DEPTH - TIER0_LAYERS];
        assert!(
            below.iter().all(|&v| v == Fp::default()),
            "path below tier0 region should be untouched"
        );
    }

    #[test]
    fn process_tier0_handles_arbitrary_field_element() {
        let raw_nfs: Vec<Fp> = (1u64..=10).map(|i| Fp::from(i * 7)).collect();
        let fix = TestFixture::build(&raw_nfs);
        let tier0 = Tier0Data::from_bytes(fix.tier0_data).unwrap();

        // Sentinel nullifiers span the field, so every non-nullifier value
        // falls in some gap range. Verify this doesn't panic and returns a
        // valid subtree index.
        let bogus = Fp::from(u64::MAX);
        let mut path = [Fp::default(); TREE_DEPTH];
        let s1 = process_tier0(&tier0, bogus, &mut path).unwrap();
        assert!(s1 < pir_types::TIER1_ROWS);
    }

    // ── process_tier1 ────────────────────────────────────────────────────

    #[test]
    fn process_tier1_fills_correct_path_region() {
        let raw_nfs: Vec<Fp> = (1u64..=30).map(|i| Fp::from(i * 1013)).collect();
        let fix = TestFixture::build(&raw_nfs);
        let tier0 = Tier0Data::from_bytes(fix.tier0_data.clone()).unwrap();

        let value = fix.ranges[0][0];
        let mut path = [Fp::default(); TREE_DEPTH];
        let s1 = process_tier0(&tier0, value, &mut path).unwrap();

        let t1_offset = s1 * TIER1_ROW_BYTES;
        let tier1_row = &fix.tier1_data[t1_offset..t1_offset + TIER1_ROW_BYTES];
        let s2 = process_tier1(tier1_row, value, &mut path).unwrap();

        assert!(s2 < TIER1_LEAVES);

        let tier1_region = &path[PIR_DEPTH - TIER0_LAYERS - TIER1_LAYERS..PIR_DEPTH - TIER0_LAYERS];
        assert!(
            tier1_region.iter().any(|&v| v != Fp::default()),
            "tier1 should write at least one non-zero sibling"
        );
    }

    // ── process_tier2_and_build ───────────────────────────────────────────

    #[test]
    fn process_tier2_and_build_produces_verifiable_proof() {
        let raw_nfs: Vec<Fp> = (1u64..=30).map(|i| Fp::from(i * 1013)).collect();
        let fix = TestFixture::build(&raw_nfs);
        let tier0 = Tier0Data::from_bytes(fix.tier0_data.clone()).unwrap();

        let value = fix.ranges[0][0];
        let mut path = [Fp::default(); TREE_DEPTH];

        let s1 = process_tier0(&tier0, value, &mut path).unwrap();
        let t1_offset = s1 * TIER1_ROW_BYTES;
        let s2 = process_tier1(
            &fix.tier1_data[t1_offset..t1_offset + TIER1_ROW_BYTES],
            value,
            &mut path,
        )
        .unwrap();

        let t2_row_idx = s1 * TIER1_LEAVES + s2;
        let t2_offset = t2_row_idx * TIER2_ROW_BYTES;
        let proof = process_tier2_and_build(
            &fix.tier2_data[t2_offset..t2_offset + TIER2_ROW_BYTES],
            t2_row_idx,
            fix.ranges.len(),
            value,
            &mut path,
            &fix.empty_hashes,
            fix.root29,
        )
        .unwrap();

        assert!(proof.verify(value));
        assert_eq!(proof.root, fix.root29);
        assert!(proof.low <= value);
    }

    // ── valid_leaves_for_row ──────────────────────────────────────────────

    #[test]
    fn valid_leaves_for_row_basic() {
        assert_eq!(valid_leaves_for_row(TIER2_LEAVES, 0), TIER2_LEAVES);
        assert_eq!(valid_leaves_for_row(TIER2_LEAVES + 1, 0), TIER2_LEAVES);
        assert_eq!(valid_leaves_for_row(TIER2_LEAVES + 1, 1), 1);
        assert_eq!(valid_leaves_for_row(0, 0), 0);
        assert_eq!(valid_leaves_for_row(1, 0), 1);
        assert_eq!(valid_leaves_for_row(1, 1), 0);
    }

    // ── fetch_proof_local error paths ─────────────────────────────────────

    #[test]
    fn fetch_proof_local_rejects_truncated_tier1() {
        let raw_nfs: Vec<Fp> = (1u64..=10).map(|i| Fp::from(i * 7)).collect();
        let fix = TestFixture::build(&raw_nfs);

        let result = fetch_proof_local(
            &fix.tier0_data,
            &fix.tier1_data[..TIER1_ROW_BYTES / 2],
            &fix.tier2_data,
            fix.ranges.len(),
            fix.ranges[0][0],
            &fix.empty_hashes,
            fix.root29,
        );
        assert!(result.is_err());
    }

    #[test]
    fn fetch_proof_local_rejects_truncated_tier2() {
        let raw_nfs: Vec<Fp> = (1u64..=10).map(|i| Fp::from(i * 7)).collect();
        let fix = TestFixture::build(&raw_nfs);

        let result = fetch_proof_local(
            &fix.tier0_data,
            &fix.tier1_data,
            &fix.tier2_data[..TIER2_ROW_BYTES / 2],
            fix.ranges.len(),
            fix.ranges[0][0],
            &fix.empty_hashes,
            fix.root29,
        );
        assert!(result.is_err());
    }
}
