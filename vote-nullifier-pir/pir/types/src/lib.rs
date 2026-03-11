//! Shared types and constants for the PIR subsystem.
//!
//! Wire types are serialized over HTTP between `pir-server` and `pir-client`.
//! Tier-layout constants define the data-format contract shared by all crates
//! (export, server, client, test).
//!
//! The default feature set is lightweight (only `serde`). Enable the `reader`
//! feature to get tier-data parsers ([`tier0::Tier0Data`], [`tier1::Tier1Row`],
//! [`tier2::Tier2Row`]) and Fp serialization helpers ([`fp_utils`]).

use serde::{Deserialize, Serialize};

#[cfg(feature = "reader")]
pub mod fp_utils;
#[cfg(feature = "reader")]
pub mod tier0;
#[cfg(feature = "reader")]
pub mod tier1;
#[cfg(feature = "reader")]
pub mod tier2;

// ── Tier-layout constants ────────────────────────────────────────────────────

/// Depth of the PIR Merkle tree (26 levels of edges from root to leaf).
/// Supports 2^26 = 67,108,864 leaf slots, enough for ~51M nullifier ranges.
pub const PIR_DEPTH: usize = 26;

/// Number of layers in Tier 0 (root at depth 0 down to subtree records at depth 11).
pub const TIER0_LAYERS: usize = 11;

/// Number of layers in each Tier 1 subtree (depth 11 to depth 18).
pub const TIER1_LAYERS: usize = 7;

/// Number of layers in each Tier 2 subtree (depth 18 to depth 26).
pub const TIER2_LAYERS: usize = 8;

/// Number of Tier 1 rows (one per depth-11 subtree).
pub const TIER1_ROWS: usize = 1 << TIER0_LAYERS; // 2048

/// Number of Tier 2 rows (one per depth-18 subtree).
pub const TIER2_ROWS: usize = 1 << (TIER0_LAYERS + TIER1_LAYERS); // 262,144

/// Number of leaves per Tier 1 subtree (at relative depth 7 = global depth 18).
pub const TIER1_LEAVES: usize = 1 << TIER1_LAYERS; // 128

/// Number of leaves per Tier 2 subtree (at relative depth 8 = global depth 26).
pub const TIER2_LEAVES: usize = 1 << TIER2_LAYERS; // 256

/// Internal nodes per Tier 1 row (relative depths 1-6: 2+4+...+64 = 126).
pub const TIER1_INTERNAL_NODES: usize = (1 << TIER1_LAYERS) - 2; // 126

/// Internal nodes per Tier 2 row (relative depths 1-7: 2+4+...+128 = 254).
pub const TIER2_INTERNAL_NODES: usize = (1 << TIER2_LAYERS) - 2; // 254

/// Byte size of one Tier 1 row: 126 × 32 (internal) + 128 × 64 (leaf records).
pub const TIER1_ROW_BYTES: usize = TIER1_INTERNAL_NODES * 32 + TIER1_LEAVES * 64; // 12,224

/// Byte size of one Tier 2 row: 254 × 32 (internal) + 256 × 64 (leaf records).
pub const TIER2_ROW_BYTES: usize = TIER2_INTERNAL_NODES * 32 + TIER2_LEAVES * 64; // 24,512

/// Tier 1 item size in bits (for YPIR parameter setup).
pub const TIER1_ITEM_BITS: usize = TIER1_ROW_BYTES * 8; // 97,792

/// Tier 2 item size in bits (for YPIR parameter setup).
pub const TIER2_ITEM_BITS: usize = TIER2_ROW_BYTES * 8; // 196,096

// ── Metadata ─────────────────────────────────────────────────────────────────

/// Metadata written to `pir_root.json` alongside the tier files.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PirMetadata {
    /// Hex-encoded depth-26 Merkle root.
    pub root26: String,
    /// Hex-encoded depth-29 Merkle root (circuit-compatible).
    pub root29: String,
    /// Number of populated leaf ranges in the tree.
    pub num_ranges: usize,
    /// PIR tree depth.
    pub pir_depth: usize,
    /// Tier 0 size in bytes.
    pub tier0_bytes: usize,
    /// Number of Tier 1 rows.
    pub tier1_rows: usize,
    /// Tier 1 row size in bytes.
    pub tier1_row_bytes: usize,
    /// Number of Tier 2 rows.
    pub tier2_rows: usize,
    /// Tier 2 row size in bytes.
    pub tier2_row_bytes: usize,
    /// Block height the tree was built from (if known).
    pub height: Option<u64>,
}

// ── Wire types ───────────────────────────────────────────────────────────────

/// Parameters describing a YPIR database scenario.
///
/// Serialized as JSON over HTTP so the client can reconstruct matching
/// YPIR parameters locally without knowing the tier layout constants.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct YpirScenario {
    pub num_items: usize,
    pub item_size_bits: usize,
}

/// Root hash and metadata returned by `GET /root`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RootInfo {
    pub root29: String,
    pub root26: String,
    pub num_ranges: usize,
    pub pir_depth: usize,
    pub height: Option<u64>,
}

/// Health check response returned by `GET /health`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HealthInfo {
    pub status: String,
    pub tier1_rows: usize,
    pub tier2_rows: usize,
    pub tier1_row_bytes: usize,
    pub tier2_row_bytes: usize,
}

const U64_BYTES: usize = std::mem::size_of::<u64>();

/// Serialize a YPIR SimplePIR query into the wire format expected by `pir-server`.
///
/// Layout: `[8-byte LE pqr_byte_len][pqr as LE u64s][pub_params as LE u64s]`
pub fn serialize_ypir_query(pqr: &[u64], pub_params: &[u64]) -> Vec<u8> {
    let pqr_byte_len = pqr.len() * U64_BYTES;
    let mut payload = Vec::with_capacity(U64_BYTES + (pqr.len() + pub_params.len()) * U64_BYTES);
    payload.extend_from_slice(&(pqr_byte_len as u64).to_le_bytes());
    for &v in pqr {
        payload.extend_from_slice(&v.to_le_bytes());
    }
    for &v in pub_params {
        payload.extend_from_slice(&v.to_le_bytes());
    }
    payload
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn serialize_ypir_query_empty() {
        let result = serialize_ypir_query(&[], &[]);
        assert_eq!(result.len(), U64_BYTES);
        assert_eq!(u64::from_le_bytes(result[..8].try_into().unwrap()), 0);
    }

    #[test]
    fn serialize_ypir_query_round_trip_layout() {
        let pqr = vec![1u64, 2, 3];
        let pp = vec![100u64, 200];
        let payload = serialize_ypir_query(&pqr, &pp);

        let expected_len = U64_BYTES + (pqr.len() + pp.len()) * U64_BYTES;
        assert_eq!(payload.len(), expected_len);

        let pqr_byte_len = u64::from_le_bytes(payload[..8].try_into().unwrap()) as usize;
        assert_eq!(pqr_byte_len, pqr.len() * U64_BYTES);

        for (i, &expected) in pqr.iter().enumerate() {
            let offset = U64_BYTES + i * U64_BYTES;
            let val = u64::from_le_bytes(payload[offset..offset + U64_BYTES].try_into().unwrap());
            assert_eq!(val, expected);
        }

        for (i, &expected) in pp.iter().enumerate() {
            let offset = U64_BYTES + pqr_byte_len + i * U64_BYTES;
            let val = u64::from_le_bytes(payload[offset..offset + U64_BYTES].try_into().unwrap());
            assert_eq!(val, expected);
        }
    }

    #[test]
    fn serialize_ypir_query_length_prefix_correctness() {
        let pqr = vec![42u64];
        let pp = vec![99u64];
        let payload = serialize_ypir_query(&pqr, &pp);

        let pqr_byte_len = u64::from_le_bytes(payload[..8].try_into().unwrap()) as usize;
        assert_eq!(pqr_byte_len, 8);

        let remaining = payload.len() - U64_BYTES - pqr_byte_len;
        assert_eq!(remaining, pp.len() * U64_BYTES);
    }
}
