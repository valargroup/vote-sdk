//! Remote client library for the Zally vote commitment tree.
//!
//! Provides [`HttpTreeSyncApi`] — an HTTP implementation of the
//! [`TreeSyncApi`](vote_commitment_tree::TreeSyncApi) trait that connects to
//! a running Zally chain node's REST API.
//!
//! Used by the `vote-tree-cli` binary and available as a library for
//! integration in other Rust tools.

pub mod http_sync_api;
pub mod types;
