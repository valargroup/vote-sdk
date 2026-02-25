//! YPIR+SP server wrapper and shared types for the PIR HTTP server.
//!
//! This module encapsulates all YPIR operations, providing a clean interface
//! that both the HTTP server (`main.rs`) and the test harness (`pir-test`)
//! can use.

use anyhow::Result;
use serde::{Deserialize, Serialize};
use std::io::Cursor;
use std::time::Instant;

use std::alloc::{alloc_zeroed, dealloc, handle_alloc_error, Layout};

use spiral_rs::params::Params;
use ypir::params::{params_for_scenario_simplepir, DbRowsCols, PtModulusBits};
use ypir::serialize::{FilePtIter, OfflinePrecomputedValues};
use ypir::server::YServer;

/// 64-byte aligned u64 buffer for AVX-512 operations.
struct Aligned64 {
    ptr: *mut u64,
    len: usize,
    layout: Layout,
}

impl Aligned64 {
    fn new(len: usize) -> Self {
        assert!(len > 0, "Aligned64::new called with zero length");
        let size = len.checked_mul(8).expect("Aligned64 size overflow");
        let layout = Layout::from_size_align(size, 64).expect("Aligned64 invalid layout");
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

// Re-export constants from pir-export for convenience.
pub use pir_export::{
    TIER1_ITEM_BITS, TIER1_ROWS, TIER1_ROW_BYTES, TIER2_ITEM_BITS, TIER2_ROWS, TIER2_ROW_BYTES,
};

// ── YPIR scenario params ─────────────────────────────────────────────────────

/// Parameters needed for a YPIR scenario. Serialized over HTTP so the client
/// can reconstruct matching params locally.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct YpirScenario {
    pub num_items: usize,
    pub item_size_bits: usize,
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

        eprintln!(
            "  YPIR server init: {} items × {} bits",
            scenario.num_items, scenario.item_size_bits
        );

        // Use FilePtIter to pack raw bytes into 14-bit u16 values.
        // This matches how the YPIR standalone server reads database files.
        let bytes_per_row = scenario.item_size_bits / 8;
        let db_cols = params.db_cols_simplepir();
        let pt_bits = params.pt_modulus_bits();
        eprintln!(
            "  FilePtIter: bytes_per_row={}, db_cols={}, pt_bits={}",
            bytes_per_row, db_cols, pt_bits
        );
        let cursor = Cursor::new(data);
        let pt_iter = FilePtIter::new(cursor, bytes_per_row, db_cols, pt_bits);
        let server = YServer::<u16>::new(params, pt_iter, true, false, true);

        let t1 = Instant::now();
        eprintln!(
            "  YPIR server constructed in {:.1}s",
            (t1 - t0).as_secs_f64()
        );

        let offline = server.perform_offline_precomputation_simplepir(None, None, None);
        eprintln!(
            "  YPIR offline precomputation done in {:.1}s",
            t1.elapsed().as_secs_f64()
        );

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
        let pqr_byte_len = u64::from_le_bytes(query_bytes[..8].try_into().unwrap()) as usize;
        let payload_len = query_bytes.len() - 8; // safe: checked >= 8
        anyhow::ensure!(
            pqr_byte_len % 8 == 0,
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
            remaining % 8 == 0,
            "pub_params section {} bytes not a multiple of 8",
            remaining
        );
        let validate_ms = validate_start.elapsed().as_secs_f64() * 1000.0;

        let pqr_u64_len = pqr_byte_len / 8;
        let pp_u64_len = remaining / 8;

        // Copy into 64-byte aligned memory for AVX-512 operations.
        let decode_start = Instant::now();
        let mut pqr = Aligned64::new(pqr_u64_len);
        for (i, chunk) in query_bytes[8..8 + pqr_byte_len].chunks_exact(8).enumerate() {
            pqr.as_mut_slice()[i] = u64::from_le_bytes(chunk.try_into().unwrap());
        }

        let mut pub_params = Aligned64::new(pp_u64_len);
        for (i, chunk) in query_bytes[8 + pqr_byte_len..].chunks_exact(8).enumerate() {
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

// ── Root info ────────────────────────────────────────────────────────────────

/// Root and metadata returned by GET /root.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RootInfo {
    pub root29: String,
    pub root26: String,
    pub num_ranges: usize,
    pub pir_depth: usize,
    pub height: Option<u64>,
}

/// Health check response.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HealthInfo {
    pub status: String,
    pub tier1_rows: usize,
    pub tier2_rows: usize,
    pub tier1_row_bytes: usize,
    pub tier2_row_bytes: usize,
}

// ── OwnedTierState ────────────────────────────────────────────────────────────

/// Owns the tier data and the `TierServer`, avoiding `Box::leak` accumulation.
///
/// On drop, the server is dropped first (via `ManuallyDrop`), then the data
/// `Vec<u8>` is freed — reclaiming ~6 GB on each rebuild instead of leaking.
pub struct OwnedTierState {
    server: std::mem::ManuallyDrop<TierServer<'static>>,
    // Actual owner of the bytes; kept alive as long as the server is alive.
    _data: Vec<u8>,
}

impl OwnedTierState {
    /// Construct a new `OwnedTierState` from owned tier data and a YPIR scenario.
    ///
    /// # Safety
    ///
    /// We extend the lifetime of the data reference to `'static`. This is sound
    /// because:
    /// 1. `_data` lives in this struct and outlives `server` (ManuallyDrop + Drop order)
    /// 2. YPIR's `FilePtIter` is consumed during `YServer::new()` — after construction,
    ///    the server holds precomputed values, not references to the original data.
    /// 3. In-flight requests must be drained before dropping this struct.
    pub fn new(data: Vec<u8>, scenario: YpirScenario) -> Self {
        let data_ref: &'static [u8] = unsafe {
            std::mem::transmute::<&[u8], &'static [u8]>(data.as_slice())
        };
        let server = TierServer::new(data_ref, scenario);
        Self {
            server: std::mem::ManuallyDrop::new(server),
            _data: data,
        }
    }

    pub fn server(&self) -> &TierServer<'static> {
        &self.server
    }

    /// Return the YPIR hint bytes for this tier.
    pub fn hint_bytes(&self) -> Vec<u8> {
        self.server.hint_bytes()
    }

    /// Access the raw tier data for direct row reads.
    pub fn data(&self) -> &[u8] {
        &self._data
    }
}

impl Drop for OwnedTierState {
    fn drop(&mut self) {
        // Drop server first (releases any internal YPIR state).
        unsafe { std::mem::ManuallyDrop::drop(&mut self.server); }
        // Then self._data drops naturally, freeing the tier bytes.
    }
}

// Allow sending OwnedTierState between threads (needed for tokio spawn_blocking).
// This is safe because TierServer is only accessed via &self references through
// the AppState RwLock.
unsafe impl Send for OwnedTierState {}
unsafe impl Sync for OwnedTierState {}
