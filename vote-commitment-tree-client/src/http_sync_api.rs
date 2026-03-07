//! HTTP implementation of [`TreeSyncApi`] for connecting to a running Zally chain node.
//!
//! Maps the three trait methods to the chain's REST endpoints:
//! - `get_tree_state()`        → `GET /shielded-vote/v1/commitment-tree/latest`
//! - `get_root_at_height(h)`   → `GET /shielded-vote/v1/commitment-tree/{h}`
//! - `get_block_commitments()` → `GET /shielded-vote/v1/commitment-tree/leaves?from_height=X&to_height=Y`

use pasta_curves::Fp;

use vote_commitment_tree::sync_api::{BlockCommitments, TreeState, TreeSyncApi};

use crate::types::{
    QueryCommitmentLeavesResponse, QueryCommitmentTreeResponse, QueryLatestTreeResponse,
};

// ---------------------------------------------------------------------------
// Error
// ---------------------------------------------------------------------------

/// Errors from the HTTP sync API.
#[derive(Debug, thiserror::Error)]
pub enum HttpSyncError {
    #[error("HTTP request failed: {0}")]
    Http(#[from] reqwest::Error),

    #[error("parse error: {0}")]
    Parse(#[from] crate::types::ParseError),

    #[error("server returned no tree state")]
    NoTreeState,
}

// ---------------------------------------------------------------------------
// HttpTreeSyncApi
// ---------------------------------------------------------------------------

/// HTTP-based implementation of [`TreeSyncApi`] for remote chain sync.
///
/// Uses `reqwest::blocking::Client` for synchronous HTTP calls, matching the
/// synchronous `TreeSyncApi` trait signature.
pub struct HttpTreeSyncApi {
    client: reqwest::blocking::Client,
    /// Base URL of the chain's REST API (e.g. `http://localhost:1317`).
    base_url: String,
}

impl HttpTreeSyncApi {
    /// Create a new HTTP sync API client.
    ///
    /// `base_url` should be the root of the chain's REST API, without a trailing
    /// slash (e.g. `http://localhost:1317`).
    pub fn new(base_url: impl Into<String>) -> Self {
        Self {
            client: reqwest::blocking::Client::new(),
            base_url: base_url.into(),
        }
    }

    /// Create with an existing reqwest client (for custom timeouts, etc.).
    pub fn with_client(client: reqwest::blocking::Client, base_url: impl Into<String>) -> Self {
        Self {
            client,
            base_url: base_url.into(),
        }
    }
}

impl TreeSyncApi for HttpTreeSyncApi {
    type Error = HttpSyncError;

    fn get_tree_state(&self) -> Result<TreeState, Self::Error> {
        let url = format!("{}/shielded-vote/v1/commitment-tree/latest", self.base_url);
        let resp: QueryLatestTreeResponse = self.client.get(&url).send()?.json()?;
        resp.tree
            .ok_or(HttpSyncError::NoTreeState)?
            .into_tree_state()
            .map_err(HttpSyncError::Parse)
    }

    fn get_root_at_height(&self, height: u32) -> Result<Option<Fp>, Self::Error> {
        let url = format!(
            "{}/shielded-vote/v1/commitment-tree/{}",
            self.base_url, height
        );
        let resp: QueryCommitmentTreeResponse = self.client.get(&url).send()?.json()?;
        match resp.tree {
            Some(state) => {
                let ts = state.into_tree_state().map_err(HttpSyncError::Parse)?;
                Ok(Some(ts.root))
            }
            None => Ok(None),
        }
    }

    fn get_block_commitments(
        &self,
        from_height: u32,
        to_height: u32,
    ) -> Result<Vec<BlockCommitments>, Self::Error> {
        let url = format!(
            "{}/shielded-vote/v1/commitment-tree/leaves?from_height={}&to_height={}",
            self.base_url, from_height, to_height
        );
        let resp: QueryCommitmentLeavesResponse = self.client.get(&url).send()?.json()?;
        resp.blocks
            .into_iter()
            .map(|b| b.into_block_commitments().map_err(HttpSyncError::Parse))
            .collect()
    }
}
