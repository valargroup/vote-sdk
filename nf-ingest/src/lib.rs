//! nf-ingest — shared infrastructure for syncing, storing,
//! and loading Zcash Orchard nullifiers.
//!
//! This crate is consumed by `nf-server` (ingest/export/serve commands)
//! and optionally by `pir-export` (standalone CLI).
//!
//! # Modules
//!
//! - [`config`] — Default lightwalletd URLs and stale-file detection.
//! - [`download`] — gRPC connection to lightwalletd via tonic.
//! - [`file_store`] — Flat-file nullifier storage (`nullifiers.bin`, checkpoint, index).
//! - [`sync_nullifiers`] — Incremental chain sync engine.
//! - [`rpc`] — Auto-generated protobuf types for the compact transaction streamer.

#[path = "./cash.z.wallet.sdk.rpc.rs"]
pub mod rpc;
pub mod config;
pub mod download;
pub mod file_store;
pub mod sync_nullifiers;

#[cfg(test)]
pub(crate) mod test_helpers;
