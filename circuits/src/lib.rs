//! Zally Circuits: Halo2 ZKP circuits, RedPallas signature verification,
//! and FFI layer for Go via CGo.
//!
//! This crate provides:
//! - Circuit definitions for the Zally vote chain's three ZKP types
//! - RedPallas (RedDSA over Pallas) spend-auth signature verification
//! - C-compatible FFI functions for calling from Go via CGo
//!
//! Currently contains a toy circuit for validating the Halo2 FFI pipeline
//! and real RedPallas signature verification via the `reddsa` crate.

pub mod toy;
pub mod redpallas;
pub mod votetree;
pub mod ffi;
