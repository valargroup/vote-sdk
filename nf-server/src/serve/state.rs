use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, AtomicUsize};
use std::sync::Arc;

use tokio::sync::RwLock;

pub(crate) use pir_server::ServingState;

/// Current lifecycle phase of the server, serialised in health/503 responses.
#[derive(Clone, serde::Serialize)]
#[serde(tag = "phase")]
pub(crate) enum ServerPhase {
    #[serde(rename = "serving")]
    Serving,
    #[serde(rename = "rebuilding")]
    Rebuilding {
        target_height: u64,
        progress: String,
        progress_pct: u8,
    },
    #[serde(rename = "error")]
    Error { message: String },
}

/// Top-level shared state, wrapped in `Arc` and passed to all axum handlers.
pub(crate) struct AppState {
    pub phase: RwLock<ServerPhase>,
    pub serving: RwLock<Option<ServingState>>,
    /// Prevents concurrent rebuilds. Held for the entire duration of a rebuild task.
    /// Wrapped in Arc so we can obtain an OwnedMutexGuard that is 'static.
    pub rebuild_lock: Arc<tokio::sync::Mutex<()>>,
    pub data_dir: PathBuf,
    pub pir_data_dir: PathBuf,
    pub lwd_urls: Vec<String>,
    pub chain_url: Option<String>,
    pub next_req_id: AtomicU64,
    pub inflight_requests: AtomicUsize,
}

/// Acquire the serving state read-guard or return 503 if unavailable (during rebuild).
macro_rules! require_serving {
    ($state:expr) => {{
        let guard = $state.serving.read().await;
        if guard.is_none() {
            let phase = $state.phase.read().await;
            let body = serde_json::to_string(&*phase).unwrap_or_default();
            return (
                axum::http::StatusCode::SERVICE_UNAVAILABLE,
                [(axum::http::header::CONTENT_TYPE, "application/json")],
                body,
            )
                .into_response();
        }
        guard
    }};
}
