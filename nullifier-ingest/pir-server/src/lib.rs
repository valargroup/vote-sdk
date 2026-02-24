//! YPIR+SP server wrapper and shared types for the PIR HTTP server.
//!
//! This module encapsulates all YPIR operations, providing a clean interface
//! that both the HTTP server (`main.rs`) and the test harness (`pir-test`)
//! can use.

use std::io::Cursor;
use std::time::Instant;
use anyhow::Result;
use serde::{Deserialize, Serialize};

use std::alloc::{alloc_zeroed, dealloc, handle_alloc_error, Layout};

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
        let layout = Layout::from_size_align(len * 8, 64).unwrap();
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
pub struct TierServer<'a> {
    server: YServer<'a, u16>,
    offline: OfflinePrecomputedValues<'a>,
    scenario: YpirScenario,
}

impl<'a> TierServer<'a> {
    /// Initialize a YPIR+SP server from raw tier data.
    ///
    /// `data` is the flat binary tier file (rows × row_bytes).
    /// This performs the expensive offline precomputation.
    pub fn new(data: &'a [u8], scenario: YpirScenario) -> Self {
        let t0 = Instant::now();
        // Leak params so they live as long as 'a (process lifetime).
        let params: &'a _ = Box::leak(Box::new(
            params_for_scenario_simplepir(scenario.num_items as u64, scenario.item_size_bits as u64),
        ));

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
            server,
            offline,
            scenario,
        }
    }

    /// Answer a single YPIR+SP query.
    ///
    /// The query bytes must be in the length-prefixed format:
    /// `[8 bytes: packed_query_row byte length as LE u64][packed_query_row bytes][pub_params bytes]`
    ///
    /// Returns the serialized response as LE u64 bytes.
    pub fn answer_query(&mut self, query_bytes: &[u8]) -> Result<Vec<u8>> {
        // Validate length-prefixed format: [8: pqr_byte_len][pqr][pub_params]
        anyhow::ensure!(query_bytes.len() >= 8, "query too short: {} bytes", query_bytes.len());
        let pqr_byte_len =
            u64::from_le_bytes(query_bytes[..8].try_into().unwrap()) as usize;
        let payload_len = query_bytes.len() - 8; // safe: checked >= 8
        anyhow::ensure!(pqr_byte_len % 8 == 0, "pqr_byte_len {} not a multiple of 8", pqr_byte_len);
        anyhow::ensure!(pqr_byte_len <= payload_len,
            "pqr_byte_len {} exceeds payload ({})", pqr_byte_len, payload_len);
        let remaining = payload_len - pqr_byte_len; // safe: checked above
        anyhow::ensure!(pqr_byte_len > 0, "pqr section is empty");
        anyhow::ensure!(remaining > 0, "pub_params section is empty");
        anyhow::ensure!(remaining % 8 == 0, "pub_params section {} bytes not a multiple of 8", remaining);

        let pqr_u64_len = pqr_byte_len / 8;
        let pp_u64_len = remaining / 8;

        // Copy into 64-byte aligned memory for AVX-512 operations.
        let mut pqr = Aligned64::new(pqr_u64_len);
        for (i, chunk) in query_bytes[8..8 + pqr_byte_len].chunks_exact(8).enumerate() {
            pqr.as_mut_slice()[i] = u64::from_le_bytes(chunk.try_into().unwrap());
        }

        let mut pub_params = Aligned64::new(pp_u64_len);
        for (i, chunk) in query_bytes[8 + pqr_byte_len..].chunks_exact(8).enumerate() {
            pub_params.as_mut_slice()[i] = u64::from_le_bytes(chunk.try_into().unwrap());
        }

        // Run the YPIR online computation (returns Vec<u8> directly)
        Ok(self.server.perform_online_computation_simplepir(
            pqr.as_slice(),
            &self.offline,
            &[pub_params.as_slice()],
            None,
        ))
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
