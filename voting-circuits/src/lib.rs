//! Governance ZKP circuits for the Shielded-Vote protocol.
//!
//! Contains three circuits:
//! - **Delegation** (ZKP #1): Proves delegation of voting rights.
//! - **Vote Proof** (ZKP #2): Proves a valid, authorized vote.
//! - **Share Reveal** (ZKP #3): Proves a revealed share belongs to a registered vote commitment.

#![no_std]
#![deny(missing_debug_implementations)]
#![deny(unsafe_code)]

#[macro_use]
extern crate alloc;

#[cfg(feature = "std")]
extern crate std;

pub mod circuit;
pub mod shares_hash;

pub mod delegation;

pub mod vote_proof;

#[cfg(feature = "share-reveal")]
pub mod share_reveal;
