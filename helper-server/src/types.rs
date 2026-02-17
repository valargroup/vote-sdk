//! Wire format types and internal processing state for the helper server.
//!
//! [`SharePayload`] matches Adam's iOS `SharePayload` / `EncryptedShare` types
//! in VotingModels.swift. All byte arrays are base64-encoded on the wire.

use std::time::Instant;

use serde::{Deserialize, Serialize};

// ---------------------------------------------------------------------------
// Wire format (wallet → helper server)
// ---------------------------------------------------------------------------

/// Encrypted El Gamal share component, matching iOS `EncryptedShare`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EncryptedShareWire {
    /// El Gamal C1 component (32 bytes, base64).
    pub c1: String,
    /// El Gamal C2 component (32 bytes, base64).
    pub c2: String,
    /// Which of the 4 shares (0..3).
    pub share_index: u32,
}

/// Share payload sent by wallets, matching iOS `SharePayload`.
///
/// The wallet sends 4 of these per vote (one per share). The helper server
/// delays and submits them independently.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SharePayload {
    /// Hash of all 4 encrypted shares (32 bytes, base64).
    pub shares_hash: String,
    /// Proposal being voted on.
    pub proposal_id: u32,
    /// Vote decision (0 = support, 1 = oppose, 2 = skip).
    pub vote_decision: u32,
    /// The encrypted share to relay.
    pub enc_share: EncryptedShareWire,
    /// Index within the decomposition (redundant with enc_share.share_index,
    /// but present at both levels in the spec).
    pub share_index: u32,
    /// VC leaf index in the vote commitment tree.
    pub tree_position: u64,
    /// Vote round identifier (32 bytes, hex).
    ///
    /// Not in the current iOS SharePayload struct, but required by the helper
    /// server to key the share queue by round. The iOS client has this data
    /// via VoteCommitmentBundle.voteRoundId and will include it when
    /// buildSharePayloads is wired to the real network layer.
    pub vote_round_id: String,
    /// All 4 encrypted shares (C1/C2 pairs). Needed for ZKP #3 to
    /// recompute shares_hash in-circuit.
    /// TODO: This is a temp hack
    pub all_enc_shares: Vec<EncryptedShareWire>,
}

// ---------------------------------------------------------------------------
// Internal processing state
// ---------------------------------------------------------------------------

/// Processing state for a queued share.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ShareState {
    /// Received, waiting for delay to elapse.
    Received,
    /// Merkle witness generated, ready to submit.
    Witnessed,
    /// MsgRevealShare submitted to chain.
    Submitted,
    /// Processing failed (will be retried).
    Failed,
}

/// A share payload with processing metadata.
#[derive(Debug, Clone)]
pub struct QueuedShare {
    pub payload: SharePayload,
    pub received_at: Instant,
    /// When this share becomes eligible for submission.
    pub scheduled_submit_at: Instant,
    pub state: ShareState,
    /// Number of submission attempts (for retry backoff).
    pub attempts: u32,
}

// ---------------------------------------------------------------------------
// Wire format (helper server → chain)
// ---------------------------------------------------------------------------

/// MsgRevealShare JSON payload, matching the Go chain's REST API expectations.
///
/// Byte fields are base64-encoded (Go's default `encoding/json` for `[]byte`).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MsgRevealShareJson {
    /// Poseidon nullifier for this share (32 bytes, base64).
    pub share_nullifier: String,
    /// ElGamal ciphertext: C1 || C2 (64 bytes, base64).
    pub enc_share: String,
    pub proposal_id: u32,
    pub vote_decision: u32,
    /// ZKP #3 proof (base64). Currently mocked.
    pub proof: String,
    /// Vote round identifier (32 bytes, base64).
    pub vote_round_id: String,
    /// Anchor height for the VC Merkle witness.
    pub vote_comm_tree_anchor_height: u64,
}

/// Chain broadcast result, matching Go `BroadcastResult`.
#[derive(Debug, Clone, Deserialize)]
pub struct BroadcastResult {
    pub tx_hash: String,
    pub code: u32,
    #[serde(default)]
    pub log: String,
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

/// Helper server configuration.
#[derive(Debug, Clone)]
pub struct Config {
    /// Port to listen on.
    pub port: u16,
    /// Base URL of the chain's REST API (or mock tree dev server).
    pub tree_node_url: String,
    /// Base URL for MsgRevealShare submission.
    /// Defaults to same as tree_node_url.
    pub chain_submit_url: String,
    /// Minimum delay before submitting a share (seconds).
    pub min_delay_secs: u64,
    /// Maximum delay before submitting a share (seconds).
    pub max_delay_secs: u64,
    /// How often to re-sync the tree (seconds).
    pub sync_interval_secs: u64,
    /// How often to check for shares ready to submit (seconds).
    pub process_interval_secs: u64,
    /// Path to the SQLite database file. Use ":memory:" for in-memory DB.
    pub db_path: String,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            port: 9090,
            tree_node_url: "http://localhost:8080".into(),
            chain_submit_url: "http://localhost:8080".into(),
            min_delay_secs: 10,
            max_delay_secs: 300,
            sync_interval_secs: 5,
            process_interval_secs: 2,
            db_path: ":memory:".into(),
        }
    }
}
