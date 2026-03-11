//! YPIR+SP server wrapper and shared types for the PIR HTTP server.
//!
//! This module encapsulates all YPIR operations, providing a clean interface
//! that both the HTTP server (`main.rs`) and the test harness (`pir-test`)
//! can use.

use anyhow::Result;
use std::io::Cursor;
use std::time::Instant;
use tracing::info;

use std::alloc::{alloc_zeroed, dealloc, handle_alloc_error, Layout};

use spiral_rs::params::Params;
use ypir::params::{params_for_scenario_simplepir, DbRowsCols, PtModulusBits};
use ypir::serialize::{FilePtIter, OfflinePrecomputedValues};
use ypir::server::YServer;

// Re-export shared types and constants so existing consumers can import from pir_server.
pub use pir_types::{
    HealthInfo, PirMetadata, RootInfo, YpirScenario, TIER1_ITEM_BITS, TIER1_ROWS, TIER1_ROW_BYTES,
    TIER2_ITEM_BITS, TIER2_ROWS, TIER2_ROW_BYTES,
};

const U64_BYTES: usize = std::mem::size_of::<u64>();
const AVX512_ALIGN: usize = 64;

/// 64-byte aligned u64 buffer for AVX-512 operations.
struct Aligned64 {
    ptr: *mut u64,
    len: usize,
    layout: Layout,
}

impl Aligned64 {
    fn new(len: usize) -> Self {
        assert!(len > 0, "Aligned64::new called with zero length");
        let size = len.checked_mul(U64_BYTES).expect("Aligned64 size overflow");
        let layout = Layout::from_size_align(size, AVX512_ALIGN).expect("Aligned64 invalid layout");
        let ptr = unsafe { alloc_zeroed(layout) as *mut u64 };
        if ptr.is_null() {
            handle_alloc_error(layout);
        }
        Self { ptr, len, layout }
    }

    fn as_slice(&self) -> &[u64] {
        unsafe { std::slice::from_raw_parts(self.ptr, self.len) }
    }

    fn as_mut_slice(&mut self) -> &mut [u64] {
        unsafe { std::slice::from_raw_parts_mut(self.ptr, self.len) }
    }
}

impl Drop for Aligned64 {
    fn drop(&mut self) {
        unsafe { dealloc(self.ptr as *mut u8, self.layout) }
    }
}

/// Tier 1 YPIR scenario.
pub fn tier1_scenario() -> YpirScenario {
    YpirScenario {
        num_items: TIER1_ROWS,
        item_size_bits: TIER1_ITEM_BITS,
    }
}

/// Tier 2 YPIR scenario.
pub fn tier2_scenario() -> YpirScenario {
    YpirScenario {
        num_items: TIER2_ROWS,
        item_size_bits: TIER2_ITEM_BITS,
    }
}

// ── PIR server state ─────────────────────────────────────────────────────────

/// Holds the YPIR server state for one tier.
///
/// Wraps the YPIR `YServer` and its offline precomputed values. Answers
/// individual queries via `answer_query`.
///
/// Owns the YPIR `Params` via a heap allocation. The `server` and `offline`
/// fields hold `&'a Params` references into this allocation. `ManuallyDrop`
/// ensures they are dropped before `_params` is freed.
pub struct TierServer<'a> {
    server: std::mem::ManuallyDrop<YServer<'a, u16>>,
    offline: std::mem::ManuallyDrop<OfflinePrecomputedValues<'a>>,
    _params: Box<Params>,
    scenario: YpirScenario,
}

/// Per-request timing breakdown for a single PIR query.
#[derive(Debug, Clone, Copy)]
pub struct QueryTiming {
    pub validate_ms: f64,
    pub decode_copy_ms: f64,
    pub online_compute_ms: f64,
    pub total_ms: f64,
    pub response_bytes: usize,
}

/// Server answer payload paired with its timing breakdown.
#[derive(Debug)]
pub struct QueryAnswer {
    pub response: Vec<u8>,
    pub timing: QueryTiming,
}

impl<'a> TierServer<'a> {
    /// Initialize a YPIR+SP server from raw tier data.
    ///
    /// `data` is the flat binary tier file (rows × row_bytes).
    /// This performs the expensive offline precomputation.
    pub fn new(data: &'a [u8], scenario: YpirScenario) -> Self {
        let t0 = Instant::now();

        // Note: this is where server params are set.
        let params_box = Box::new(params_for_scenario_simplepir(
            scenario.num_items as u64,
            scenario.item_size_bits as u64,
        ));

        // SAFETY: We extend the reference lifetime to 'a. This is sound because:
        // 1. params_box is a heap allocation with a stable address
        // 2. server and offline are ManuallyDrop, dropped before _params in our Drop impl
        // 3. The reference remains valid for the entire lifetime of this struct
        let params: &'a Params = unsafe {
            std::mem::transmute::<&Params, &'a Params>(params_box.as_ref())
        };

        info!(
            num_items = scenario.num_items,
            item_size_bits = scenario.item_size_bits,
            "YPIR server init"
        );

        // Use FilePtIter to pack raw bytes into 14-bit u16 values.
        // This matches how the YPIR standalone server reads database files.
        let bytes_per_row = scenario.item_size_bits / 8;
        let db_cols = params.db_cols_simplepir();
        let pt_bits = params.pt_modulus_bits();
        info!(bytes_per_row, db_cols, pt_bits, "FilePtIter config");
        let cursor = Cursor::new(data);
        let pt_iter = FilePtIter::new(cursor, bytes_per_row, db_cols, pt_bits);
        let server = YServer::<u16>::new(params, pt_iter, true, false, true);

        let t1 = Instant::now();
        info!(elapsed_s = format!("{:.1}", (t1 - t0).as_secs_f64()), "YPIR server constructed");

        let offline = server.perform_offline_precomputation_simplepir(None, None, None);
        info!(elapsed_s = format!("{:.1}", t1.elapsed().as_secs_f64()), "YPIR offline precomputation done");

        Self {
            server: std::mem::ManuallyDrop::new(server),
            offline: std::mem::ManuallyDrop::new(offline),
            _params: params_box,
            scenario,
        }
    }

    /// Answer a single YPIR+SP query.
    ///
    /// The query bytes must be in the length-prefixed format:
    /// `[8 bytes: packed_query_row byte length as LE u64][packed_query_row bytes][pub_params bytes]`
    ///
    /// Returns the serialized response as LE u64 bytes.
    pub fn answer_query(&self, query_bytes: &[u8]) -> Result<QueryAnswer> {
        let total_start = Instant::now();

        // Validate length-prefixed format: [8: pqr_byte_len][pqr][pub_params]
        let validate_start = Instant::now();
        anyhow::ensure!(
            query_bytes.len() >= 8,
            "query too short: {} bytes",
            query_bytes.len()
        );
        let pqr_byte_len = u64::from_le_bytes(query_bytes[..U64_BYTES].try_into().unwrap()) as usize;
        let payload_len = query_bytes.len() - U64_BYTES;
        anyhow::ensure!(
            pqr_byte_len.is_multiple_of(U64_BYTES),
            "pqr_byte_len {} not a multiple of 8",
            pqr_byte_len
        );
        anyhow::ensure!(
            pqr_byte_len <= payload_len,
            "pqr_byte_len {} exceeds payload ({})",
            pqr_byte_len,
            payload_len
        );
        let remaining = payload_len - pqr_byte_len; // safe: checked above
        anyhow::ensure!(pqr_byte_len > 0, "pqr section is empty");
        anyhow::ensure!(remaining > 0, "pub_params section is empty");
        anyhow::ensure!(
            remaining.is_multiple_of(U64_BYTES),
            "pub_params section {} bytes not a multiple of {}",
            remaining, U64_BYTES
        );
        let validate_ms = validate_start.elapsed().as_secs_f64() * 1000.0;

        let pqr_u64_len = pqr_byte_len / U64_BYTES;
        let pp_u64_len = remaining / U64_BYTES;

        // Copy into 64-byte aligned memory for AVX-512 operations.
        let decode_start = Instant::now();
        let mut pqr = Aligned64::new(pqr_u64_len);
        for (i, chunk) in query_bytes[U64_BYTES..U64_BYTES + pqr_byte_len].chunks_exact(U64_BYTES).enumerate() {
            pqr.as_mut_slice()[i] = u64::from_le_bytes(chunk.try_into().unwrap());
        }

        let mut pub_params = Aligned64::new(pp_u64_len);
        for (i, chunk) in query_bytes[U64_BYTES + pqr_byte_len..].chunks_exact(U64_BYTES).enumerate() {
            pub_params.as_mut_slice()[i] = u64::from_le_bytes(chunk.try_into().unwrap());
        }
        let decode_copy_ms = decode_start.elapsed().as_secs_f64() * 1000.0;

        // Run the YPIR online computation (returns Vec<u8> directly)
        let compute_start = Instant::now();
        let response = self.server.perform_online_computation_simplepir(
            pqr.as_slice(),
            &self.offline,
            &[pub_params.as_slice()],
            None,
        );
        let online_compute_ms = compute_start.elapsed().as_secs_f64() * 1000.0;
        let total_ms = total_start.elapsed().as_secs_f64() * 1000.0;

        Ok(QueryAnswer {
            timing: QueryTiming {
                validate_ms,
                decode_copy_ms,
                online_compute_ms,
                total_ms,
                response_bytes: response.len(),
            },
            response,
        })
    }

    /// Return the YPIR scenario parameters for this tier.
    pub fn scenario(&self) -> &YpirScenario {
        &self.scenario
    }

    /// Return the SimplePIR hint (hint_0) that the client needs.
    ///
    /// Serialized as LE u64 bytes.
    pub fn hint_bytes(&self) -> Vec<u8> {
        self.offline
            .hint_0
            .iter()
            .flat_map(|v| v.to_le_bytes())
            .collect()
    }

    /// Extract the hint bytes and release the `hint_0` backing memory.
    ///
    /// `hint_0` is only needed for offline precomputation (already done) and for
    /// serving to clients. After extracting the bytes, the `Vec<u64>` is freed,
    /// saving ~64–112 MB per tier.
    pub fn take_hint_bytes(&mut self) -> Vec<u8> {
        let bytes = self
            .offline
            .hint_0
            .iter()
            .flat_map(|v| v.to_le_bytes())
            .collect();
        self.offline.hint_0 = vec![];
        bytes
    }
}

impl Drop for TierServer<'_> {
    fn drop(&mut self) {
        // Drop server and offline first (they hold &Params references into _params).
        // Then _params drops naturally, freeing the heap allocation.
        unsafe {
            std::mem::ManuallyDrop::drop(&mut self.server);
            std::mem::ManuallyDrop::drop(&mut self.offline);
        }
    }
}

// ── OwnedTierState ────────────────────────────────────────────────────────────

/// Owns a `TierServer` constructed from tier data.
///
/// The raw tier bytes are NOT retained — YPIR's `FilePtIter` is consumed during
/// `YServer::new()`, which copies everything into its own `db_buf_aligned`.
/// Dropping the source data after construction saves ~6 GB.
pub struct OwnedTierState {
    server: TierServer<'static>,
}

impl OwnedTierState {
    /// Construct a new `OwnedTierState` from borrowed tier data and a YPIR scenario.
    ///
    /// The data slice only needs to live for the duration of this call.
    ///
    /// # Safety
    ///
    /// We extend the lifetime of the data reference to `'static`. This is sound
    /// because YPIR's `FilePtIter` is consumed during `YServer::new()` — after
    /// construction, the server holds precomputed values in its own
    /// `db_buf_aligned`, not references to the original data. The `'static`
    /// lifetime on `TierServer` constrains only `params: &'a Params` (pointing
    /// to the owned `Box<Params>`), not the input data.
    pub fn new(data: &[u8], scenario: YpirScenario) -> Self {
        let data_ref: &'static [u8] = unsafe {
            std::mem::transmute::<&[u8], &'static [u8]>(data)
        };
        let server = TierServer::new(data_ref, scenario);
        Self { server }
    }

    pub fn server(&self) -> &TierServer<'static> {
        &self.server
    }

    /// Extract the YPIR hint bytes and release the internal `hint_0` memory.
    pub fn take_hint_bytes(&mut self) -> Vec<u8> {
        self.server.take_hint_bytes()
    }
}

// Allow sending OwnedTierState between threads (needed for tokio spawn_blocking).
// This is safe because TierServer is only accessed via &self references through
// the AppState RwLock.
unsafe impl Send for OwnedTierState {}
unsafe impl Sync for OwnedTierState {}

// ── Shared HTTP helpers ──────────────────────────────────────────────────────

use axum::http::{HeaderValue, StatusCode};
use axum::response::IntoResponse;
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use tracing::warn;

/// RAII guard that decrements an atomic inflight counter on drop.
pub struct InflightGuard<'a> {
    inflight: &'a AtomicUsize,
}

impl<'a> InflightGuard<'a> {
    pub fn new(inflight: &'a AtomicUsize) -> Self {
        Self { inflight }
    }
}

impl Drop for InflightGuard<'_> {
    fn drop(&mut self) {
        self.inflight.fetch_sub(1, Ordering::Relaxed);
    }
}

/// Write PIR query timing breakdown as HTTP response headers.
///
/// Used by both `pir-server` and `nf-server` to expose server-side stage
/// timing so the client can split RTT into server vs network/queue.
pub fn write_timing_headers(headers: &mut axum::http::HeaderMap, req_id: u64, timing: QueryTiming) {
    let entries: [(&str, String); 6] = [
        ("x-pir-req-id", req_id.to_string()),
        ("x-pir-server-total-ms", format!("{:.3}", timing.total_ms)),
        ("x-pir-server-validate-ms", format!("{:.3}", timing.validate_ms)),
        ("x-pir-server-decode-copy-ms", format!("{:.3}", timing.decode_copy_ms)),
        ("x-pir-server-compute-ms", format!("{:.3}", timing.online_compute_ms)),
        ("x-pir-server-response-bytes", timing.response_bytes.to_string()),
    ];
    for (name, value) in entries {
        // HeaderValue::from_str only fails on non-visible-ASCII; numeric
        // formatting always produces valid values.
        if let Ok(hv) = HeaderValue::from_str(&value) {
            headers.insert(name, hv);
        }
    }
}

/// Read a single row from a tier binary file on disk.
pub fn read_tier_row(path: &std::path::Path, offset: u64, len: usize) -> std::io::Result<Vec<u8>> {
    use std::io::{Read, Seek, SeekFrom};
    let mut f = std::fs::File::open(path)?;
    f.seek(SeekFrom::Start(offset))?;
    let mut buf = vec![0u8; len];
    f.read_exact(&mut buf)?;
    Ok(buf)
}

/// Process a PIR query against a tier server with inflight tracking,
/// structured logging, and timing response headers.
///
/// Shared between `pir-server` (standalone binary) and `nf-server serve`.
/// Callers resolve the `ServingState` and pass the relevant `OwnedTierState`.
pub fn dispatch_query(
    tier_state: &OwnedTierState,
    tier: &str,
    body: &[u8],
    next_req_id: &AtomicU64,
    inflight_requests: &AtomicUsize,
) -> axum::response::Response {
    let req_id = next_req_id.fetch_add(1, Ordering::Relaxed) + 1;
    let inflight = inflight_requests.fetch_add(1, Ordering::Relaxed) + 1;
    let _inflight_guard = InflightGuard::new(inflight_requests);
    let t0 = Instant::now();
    info!(req_id, tier, body_bytes = body.len(), inflight_requests = inflight, "pir_request_started");

    match tier_state.server().answer_query(body) {
        Ok(answer) => {
            let handler_ms = t0.elapsed().as_secs_f64() * 1000.0;
            let mut response = (
                StatusCode::OK,
                [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
                answer.response,
            )
                .into_response();
            write_timing_headers(response.headers_mut(), req_id, answer.timing);
            info!(
                req_id,
                tier,
                status = 200,
                handler_ms = format!("{handler_ms:.3}"),
                validate_ms = format!("{:.3}", answer.timing.validate_ms),
                decode_copy_ms = format!("{:.3}", answer.timing.decode_copy_ms),
                compute_ms = format!("{:.3}", answer.timing.online_compute_ms),
                server_total_ms = format!("{:.3}", answer.timing.total_ms),
                response_bytes = answer.timing.response_bytes,
                "pir_request_finished"
            );
            response
        }
        Err(e) => {
            warn!(
                req_id,
                tier,
                status = 400,
                handler_ms = format!("{:.3}", t0.elapsed().as_secs_f64() * 1000.0),
                error = %e,
                "pir_request_failed"
            );
            (StatusCode::BAD_REQUEST, e.to_string()).into_response()
        }
    }
}

// ── ServingState ─────────────────────────────────────────────────────────────

use axum::body::Bytes;

/// All data needed to serve PIR queries for all tiers.
///
/// Holds loaded tier data, initialized YPIR servers, precomputed hints,
/// and tree metadata. Used by both the standalone `pir-server` binary
/// and `nf-server` in serve mode.
///
/// Raw tier data is NOT kept in memory — YPIR copies it into its own
/// internal representation during construction. Hints and tier0 use
/// `Bytes` (reference-counted) to avoid cloning on each HTTP response.
pub struct ServingState {
    pub tier0_data: Bytes,
    pub tier1: OwnedTierState,
    pub tier2: OwnedTierState,
    pub tier1_scenario: YpirScenario,
    pub tier2_scenario: YpirScenario,
    pub tier1_hint: Bytes,
    pub tier2_hint: Bytes,
    pub metadata: PirMetadata,
}

/// Load tier files from disk, initialize YPIR servers, and return a
/// ready-to-serve [`ServingState`].
///
/// Reads `tier0.bin`, `tier1.bin`, `tier2.bin`, and `pir_root.json` from
/// `pir_data_dir`. Raw tier data is consumed during YPIR initialization
/// and dropped to save ~6 GB.
pub fn load_serving_state(pir_data_dir: &std::path::Path) -> Result<ServingState> {
    let t_total = Instant::now();

    let tier0_data = Bytes::from(std::fs::read(pir_data_dir.join("tier0.bin"))?);
    info!(bytes = tier0_data.len(), "Tier 0 loaded");

    let tier1_data = std::fs::read(pir_data_dir.join("tier1.bin"))?;
    info!(bytes = tier1_data.len(), rows = tier1_data.len() / TIER1_ROW_BYTES, "Tier 1 loaded");
    anyhow::ensure!(
        tier1_data.len() == TIER1_ROWS * TIER1_ROW_BYTES,
        "tier1.bin size mismatch: got {} bytes, expected {}",
        tier1_data.len(),
        TIER1_ROWS * TIER1_ROW_BYTES
    );

    let tier2_data = std::fs::read(pir_data_dir.join("tier2.bin"))?;
    info!(bytes = tier2_data.len(), rows = tier2_data.len() / TIER2_ROW_BYTES, "Tier 2 loaded");
    anyhow::ensure!(
        tier2_data.len() == TIER2_ROWS * TIER2_ROW_BYTES,
        "tier2.bin size mismatch: got {} bytes, expected {}",
        tier2_data.len(),
        TIER2_ROWS * TIER2_ROW_BYTES
    );

    let metadata: PirMetadata =
        serde_json::from_str(&std::fs::read_to_string(pir_data_dir.join("pir_root.json"))?)?;
    info!(num_ranges = metadata.num_ranges, "Metadata loaded");

    info!("Initializing YPIR servers");
    let tier1_scenario = tier1_scenario();
    let mut tier1 = OwnedTierState::new(&tier1_data, tier1_scenario.clone());
    drop(tier1_data);
    let tier1_hint = Bytes::from(tier1.take_hint_bytes());
    info!(hint_bytes = tier1_hint.len(), "Tier 1 YPIR ready");

    let tier2_scenario = tier2_scenario();
    let mut tier2 = OwnedTierState::new(&tier2_data, tier2_scenario.clone());
    drop(tier2_data);
    let tier2_hint = Bytes::from(tier2.take_hint_bytes());
    info!(hint_bytes = tier2_hint.len(), "Tier 2 YPIR ready");

    info!(elapsed_s = format!("{:.1}", t_total.elapsed().as_secs_f64()), "Server ready");

    Ok(ServingState {
        tier0_data,
        tier1,
        tier2,
        tier1_scenario,
        tier2_scenario,
        tier1_hint,
        tier2_hint,
        metadata,
    })
}
