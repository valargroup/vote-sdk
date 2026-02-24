//! PIR client library for private Merkle path retrieval.
//!
//! Provides [`PirClient`] which connects to a `pir-server` instance and
//! retrieves circuit-ready `ImtProofData` without revealing the queried
//! nullifier to the server.

use std::time::Instant;

use anyhow::{Context, Result};
use ff::PrimeField as _;
use pasta_curves::Fp;
use serde::{Deserialize, Serialize};

use imt_tree::hasher::PoseidonHasher;
use imt_tree::tree::{precompute_empty_hashes, TREE_DEPTH};
// Re-exported so downstream crates (e.g. librustvoting) can reference the type
// returned by PirClientBlocking::fetch_proof without a direct imt-tree dependency.
pub use imt_tree::ImtProofData;

use pir_export::tier0::Tier0Data;
use pir_export::tier1::Tier1Row;
use pir_export::tier2::Tier2Row;
use pir_export::{
    PIR_DEPTH, TIER0_LAYERS, TIER1_LAYERS, TIER1_LEAVES, TIER1_ROW_BYTES,
    TIER2_LEAVES, TIER2_ROW_BYTES,
};

use ypir::client::YPIRClient;

// ── Shared types (duplicated from pir-server to avoid feature unification) ───

/// Parameters needed for a YPIR scenario. Serialized over HTTP so the client
/// can reconstruct matching params locally.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct YpirScenario {
    pub num_items: usize,
    pub item_size_bits: usize,
}

/// Root and metadata returned by GET /root.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RootInfo {
    pub root29: String,
    pub root26: String,
    pub num_ranges: usize,
    pub pir_depth: usize,
    pub height: Option<u64>,
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

#[inline]
fn valid_leaves_for_row(num_ranges: usize, row_idx: usize) -> usize {
    let row_start = row_idx.saturating_mul(TIER2_LEAVES);
    num_ranges.saturating_sub(row_start).min(TIER2_LEAVES)
}

impl PirClient {
    /// Connect to a PIR server, downloading Tier 0 data and YPIR parameters.
    pub async fn connect(server_url: &str) -> Result<Self> {
        let http = reqwest::Client::new();
        let base = server_url.trim_end_matches('/');

        // Download Tier 0 data
        let t0 = Instant::now();
        let tier0_bytes = http
            .get(format!("{base}/tier0"))
            .send()
            .await?
            .error_for_status()?
            .bytes()
            .await?;
        eprintln!(
            "  Downloaded Tier 0: {} bytes in {:.1}s",
            tier0_bytes.len(),
            t0.elapsed().as_secs_f64()
        );
        let tier0 = Tier0Data::from_bytes(tier0_bytes.to_vec())?;

        // Download YPIR parameters for both tiers
        let tier1_scenario: YpirScenario = http
            .get(format!("{base}/params/tier1"))
            .send()
            .await?
            .json()
            .await?;
        let tier2_scenario: YpirScenario = http
            .get(format!("{base}/params/tier2"))
            .send()
            .await?
            .json()
            .await?;

        // Get root
        let root_info: RootInfo = http
            .get(format!("{base}/root"))
            .send()
            .await?
            .json()
            .await?;
        let root29_bytes = hex::decode(&root_info.root29)?;
        anyhow::ensure!(root29_bytes.len() == 32,
            "root29 hex decoded to {} bytes, expected 32", root29_bytes.len());
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
        let mut path = [Fp::default(); TREE_DEPTH]; // 29 siblings
        let hasher = PoseidonHasher::new();

        // ── Tier 0: plaintext lookup ─────────────────────────────────────
        let s1 = self
            .tier0
            .find_subtree(nullifier)
            .context("nullifier not found in any Tier 0 subtree")?;

        // Extract 11 siblings from Tier 0 (bottom-up levels 15..25)
        let tier0_siblings = self.tier0.extract_siblings(s1);
        for (i, &sib) in tier0_siblings.iter().enumerate() {
            path[PIR_DEPTH - TIER0_LAYERS + i] = sib; // path[15..26]
        }

        // ── Tier 1: YPIR query for row s1 ────────────────────────────────
        let tier1_row = self.ypir_query_tier1(s1).await?;
        let tier1 = Tier1Row::from_bytes(&tier1_row)?;

        let s2 = tier1
            .find_sub_subtree(nullifier)
            .context("nullifier not found in any Tier 1 sub-subtree")?;

        // Extract 8 siblings from Tier 1 (bottom-up levels 7..14)
        let tier1_siblings = tier1.extract_siblings(s2);
        for (i, &sib) in tier1_siblings.iter().enumerate() {
            path[PIR_DEPTH - TIER0_LAYERS - TIER1_LAYERS + i] = sib; // path[7..15]
        }

        // ── Tier 2: YPIR query for row (s1 * 256 + s2) ──────────────────
        let t2_row_idx = s1 * TIER1_LEAVES + s2;
        let tier2_row = self.ypir_query_tier2(t2_row_idx).await?;
        let tier2 = Tier2Row::from_bytes(&tier2_row)?;
        let valid_leaves = valid_leaves_for_row(self.num_ranges, t2_row_idx);

        let leaf_local_idx = tier2
            .find_leaf(nullifier, valid_leaves)
            .context("nullifier not found in Tier 2 leaf scan")?;

        // Extract 7 siblings from Tier 2 (bottom-up levels 0..6)
        let tier2_siblings = tier2.extract_siblings(leaf_local_idx, valid_leaves, &hasher);
        for (i, &sib) in tier2_siblings.iter().enumerate() {
            path[i] = sib; // path[0..7]
        }

        // ── Path padding (depth 26 → 29) ────────────────────────────────
        for level in PIR_DEPTH..TREE_DEPTH {
            path[level] = self.empty_hashes[level];
        }

        // ── Compute leaf position and range data ─────────────────────────
        let global_leaf_idx = t2_row_idx * TIER2_LEAVES + leaf_local_idx;
        let (low, width) = tier2.leaf_record(leaf_local_idx);

        Ok(ImtProofData {
            root: self.root29,
            low,
            width,
            leaf_pos: global_leaf_idx as u32,
            path,
        })
    }

    /// Send a YPIR query for a Tier 1 row and return the decrypted row bytes.
    async fn ypir_query_tier1(&self, row_idx: usize) -> Result<Vec<u8>> {
        let t0 = Instant::now();
        let ypir_client = YPIRClient::from_db_sz(
            self.tier1_scenario.num_items as u64,
            self.tier1_scenario.item_size_bits as u64,
            true,
        );

        // Generate encrypted YPIR query
        let (query, seed) = ypir_client.generate_query_simplepir(row_idx);
        eprintln!(
            "  YPIR tier1 query generated in {:.1}ms",
            t0.elapsed().as_secs_f64() * 1000.0
        );

        // Serialize with length prefix: [8: pqr_byte_len][pqr][pub_params]
        let pqr = query.0.as_slice();
        let pp = query.1.as_slice();
        let pqr_byte_len = pqr.len() * 8;
        let mut payload = Vec::with_capacity(8 + (pqr.len() + pp.len()) * 8);
        payload.extend_from_slice(&(pqr_byte_len as u64).to_le_bytes());
        for &v in pqr {
            payload.extend_from_slice(&v.to_le_bytes());
        }
        for &v in pp {
            payload.extend_from_slice(&v.to_le_bytes());
        }

        // Send to server
        let t1 = Instant::now();
        let url = format!("{}/tier1/query", self.server_url);
        eprintln!(
            "  YPIR tier1 query payload: {} bytes (pqr={}, pp={})",
            payload.len(),
            pqr.len() * 8,
            pp.len() * 8,
        );
        let send_result = self
            .http
            .post(&url)
            .body(payload)
            .send()
            .await;
        let resp = match send_result {
            Ok(r) => r,
            Err(e) => {
                eprintln!("  YPIR tier1 send error: {:?}", e);
                return Err(e.into());
            }
        };
        let status = resp.status();
        let response_bytes = resp.bytes().await?;
        if !status.is_success() {
            anyhow::bail!("tier1 query failed: HTTP {} body={}", status, String::from_utf8_lossy(&response_bytes));
        }
        eprintln!(
            "  YPIR tier1 response: {} bytes in {:.1}s",
            response_bytes.len(),
            t1.elapsed().as_secs_f64()
        );

        // Decode YPIR response to get the plaintext row
        let t2 = Instant::now();
        let decoded = ypir_client.decode_response_simplepir(seed, &response_bytes);
        eprintln!(
            "  YPIR tier1 decoded in {:.1}ms",
            t2.elapsed().as_secs_f64() * 1000.0
        );

        anyhow::ensure!(decoded.len() >= TIER1_ROW_BYTES,
            "tier1 decoded response too short: {} bytes, expected >= {}", decoded.len(), TIER1_ROW_BYTES);
        Ok(decoded[..TIER1_ROW_BYTES].to_vec())
    }

    /// Send a YPIR query for a Tier 2 row and return the decrypted row bytes.
    async fn ypir_query_tier2(&self, row_idx: usize) -> Result<Vec<u8>> {
        let t0 = Instant::now();
        let ypir_client = YPIRClient::from_db_sz(
            self.tier2_scenario.num_items as u64,
            self.tier2_scenario.item_size_bits as u64,
            true,
        );

        // Generate encrypted YPIR query
        let (query, seed) = ypir_client.generate_query_simplepir(row_idx);
        eprintln!(
            "  YPIR tier2 query generated in {:.1}ms",
            t0.elapsed().as_secs_f64() * 1000.0
        );

        // Serialize with length prefix: [8: pqr_byte_len][pqr][pub_params]
        let pqr = query.0.as_slice();
        let pp = query.1.as_slice();
        let pqr_byte_len = pqr.len() * 8;
        let mut payload = Vec::with_capacity(8 + (pqr.len() + pp.len()) * 8);
        payload.extend_from_slice(&(pqr_byte_len as u64).to_le_bytes());
        for &v in pqr {
            payload.extend_from_slice(&v.to_le_bytes());
        }
        for &v in pp {
            payload.extend_from_slice(&v.to_le_bytes());
        }

        // Send to server
        let t1 = Instant::now();
        let url = format!("{}/tier2/query", self.server_url);
        eprintln!(
            "  YPIR tier2 query payload: {} bytes (pqr={}, pp={})",
            payload.len(),
            pqr.len() * 8,
            pp.len() * 8,
        );
        let response_bytes = self
            .http
            .post(&url)
            .body(payload)
            .send()
            .await?
            .error_for_status()?
            .bytes()
            .await?;
        eprintln!(
            "  YPIR tier2 response: {} bytes in {:.1}s",
            response_bytes.len(),
            t1.elapsed().as_secs_f64()
        );

        // Decode YPIR response to get the plaintext row
        let t2 = Instant::now();
        let decoded = ypir_client.decode_response_simplepir(seed, &response_bytes);
        eprintln!(
            "  YPIR tier2 decoded in {:.1}ms",
            t2.elapsed().as_secs_f64() * 1000.0
        );

        anyhow::ensure!(decoded.len() >= TIER2_ROW_BYTES,
            "tier2 decoded response too short: {} bytes, expected >= {}", decoded.len(), TIER2_ROW_BYTES);
        Ok(decoded[..TIER2_ROW_BYTES].to_vec())
    }
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
    let hasher = PoseidonHasher::new();
    let tier0 = Tier0Data::from_bytes(tier0_data.to_vec())?;

    // ── Tier 0: plaintext lookup ─────────────────────────────────────────
    let s1 = tier0
        .find_subtree(nullifier)
        .context("nullifier not found in any Tier 0 subtree")?;

    let tier0_siblings = tier0.extract_siblings(s1);
    for (i, &sib) in tier0_siblings.iter().enumerate() {
        path[PIR_DEPTH - TIER0_LAYERS + i] = sib;
    }

    // ── Tier 1: direct row lookup (no YPIR in local mode) ────────────────
    let t1_offset = s1 * TIER1_ROW_BYTES;
    let tier1_row = &tier1_data[t1_offset..t1_offset + TIER1_ROW_BYTES];
    let tier1 = Tier1Row::from_bytes(tier1_row)?;

    let s2 = tier1
        .find_sub_subtree(nullifier)
        .context("nullifier not found in any Tier 1 sub-subtree")?;

    let tier1_siblings = tier1.extract_siblings(s2);
    for (i, &sib) in tier1_siblings.iter().enumerate() {
        path[PIR_DEPTH - TIER0_LAYERS - TIER1_LAYERS + i] = sib;
    }

    // ── Tier 2: direct row lookup (no YPIR in local mode) ────────────────
    let t2_row_idx = s1 * TIER1_LEAVES + s2;
    let t2_offset = t2_row_idx * TIER2_ROW_BYTES;
    let tier2_row = &tier2_data[t2_offset..t2_offset + TIER2_ROW_BYTES];
    let tier2 = Tier2Row::from_bytes(tier2_row)?;
    let valid_leaves = valid_leaves_for_row(num_ranges, t2_row_idx);

    let leaf_local_idx = tier2
        .find_leaf(nullifier, valid_leaves)
        .context("nullifier not found in Tier 2 leaf scan")?;

    let tier2_siblings = tier2.extract_siblings(leaf_local_idx, valid_leaves, &hasher);
    for (i, &sib) in tier2_siblings.iter().enumerate() {
        path[i] = sib;
    }

    // ── Path padding (depth 26 → 29) ────────────────────────────────────
    for level in PIR_DEPTH..TREE_DEPTH {
        path[level] = empty_hashes[level];
    }

    let global_leaf_idx = t2_row_idx * TIER2_LEAVES + leaf_local_idx;
    let (low, width) = tier2.leaf_record(leaf_local_idx);

    Ok(ImtProofData {
        root: root29,
        low,
        width,
        leaf_pos: global_leaf_idx as u32,
        path,
    })
}
