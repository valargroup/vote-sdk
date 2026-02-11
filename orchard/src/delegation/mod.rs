//! Delegation ZKP circuit.
//!
//! Proves nullifier integrity for a single note, following the 1-circuit-per-note
//! pattern from the vote module. For multiple notes (up to 4), the builder layer
//! creates multiple independent proofs and binds them externally.

pub mod circuit;
pub mod per_note_circuit;

pub use circuit::{Circuit, Instance};
