//! Shared circuit primitives extracted 1:1 from the upstream Orchard action circuit.
//!
//! Each sub-module contains circuit gadgets that are exact copies of the
//! corresponding code in [`zcash/orchard`]. This makes it easy to audit that
//! the voting circuits reuse the same constraint logic as mainline Orchard.
//!
//! [`zcash/orchard`]: https://github.com/zcash/orchard

pub mod spend_authority;
