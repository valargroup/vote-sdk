//! Standalone PIR HTTP server binary.
//!
//! This is the simpler, single-purpose alternative to `nf-server serve`.
//! It loads tier files from a directory, initialises YPIR server state,
//! and exposes the same HTTP API endpoints as `nf-server` in serve mode.
//!
//! Usage: `pir-server [PIR_DATA_DIR] [PORT]`

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, AtomicUsize};
use std::sync::Arc;

use anyhow::{Context, Result};
use axum::body::Bytes;
use axum::extract::{DefaultBodyLimit, Path, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::Router;

const MAX_BODY_BYTES: usize = 512 * 1024 * 1024;
const DEFAULT_PORT: u16 = 3001;

use pir_server::{
    HealthInfo, RootInfo, ServingState,
    TIER1_ROWS, TIER1_ROW_BYTES, TIER2_ROWS, TIER2_ROW_BYTES,
    read_tier_row, dispatch_query,
};
use tracing::info;

/// Shared application state: loaded tier data plus per-process counters.
struct AppState {
    serving: ServingState,
    data_dir: PathBuf,
    next_req_id: AtomicU64,
    inflight_requests: AtomicUsize,
}

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt::init();

    let data_dir = std::env::args()
        .nth(1)
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("./pir-data"));
    let port: u16 = match std::env::args().nth(2) {
        Some(s) => s.parse().context("invalid port number")?,
        None => DEFAULT_PORT,
    };

    info!(dir = ?data_dir, "Loading tier files");
    let serving = pir_server::load_serving_state(&data_dir)?;

    let state = Arc::new(AppState {
        serving,
        data_dir: data_dir.clone(),
        next_req_id: AtomicU64::new(0),
        inflight_requests: AtomicUsize::new(0),
    });

    let app = Router::new()
        .route("/tier0", get(get_tier0))
        .route("/params/tier1", get(get_params_tier1))
        .route("/params/tier2", get(get_params_tier2))
        .route("/hint/tier1", get(get_hint_tier1))
        .route("/hint/tier2", get(get_hint_tier2))
        .route("/tier1/query", post(post_tier1_query))
        .route("/tier2/query", post(post_tier2_query))
        .route("/tier1/row/:idx", get(get_tier1_row))
        .route("/tier2/row/:idx", get(get_tier2_row))
        .route("/root", get(get_root))
        .route("/health", get(get_health))
        .layer(DefaultBodyLimit::max(MAX_BODY_BYTES))
        .with_state(state);

    let addr = format!("0.0.0.0:{port}");
    info!(addr, "Listening");
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    axum::serve(listener, app).await?;

    Ok(())
}

// ── Handlers ─────────────────────────────────────────────────────────────────

async fn get_tier0(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        state.serving.tier0_data.clone(),
    )
}

async fn get_params_tier1(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    axum::Json(state.serving.tier1_scenario.clone())
}

async fn get_params_tier2(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    axum::Json(state.serving.tier2_scenario.clone())
}

async fn get_hint_tier1(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        state.serving.tier1_hint.clone(),
    )
}

async fn get_hint_tier2(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        state.serving.tier2_hint.clone(),
    )
}

async fn post_tier1_query(State(state): State<Arc<AppState>>, body: Bytes) -> impl IntoResponse {
    dispatch_query(&state.serving.tier1, "tier1", &body, &state.next_req_id, &state.inflight_requests)
}

async fn post_tier2_query(State(state): State<Arc<AppState>>, body: Bytes) -> impl IntoResponse {
    dispatch_query(&state.serving.tier2, "tier2", &body, &state.next_req_id, &state.inflight_requests)
}

async fn get_tier1_row(
    State(state): State<Arc<AppState>>,
    Path(idx): Path<usize>,
) -> impl IntoResponse {
    get_tier_row_inner(&state, idx, "tier1.bin", TIER1_ROWS, TIER1_ROW_BYTES)
}

async fn get_tier2_row(
    State(state): State<Arc<AppState>>,
    Path(idx): Path<usize>,
) -> impl IntoResponse {
    get_tier_row_inner(&state, idx, "tier2.bin", TIER2_ROWS, TIER2_ROW_BYTES)
}

fn get_tier_row_inner(
    state: &AppState,
    idx: usize,
    filename: &str,
    num_rows: usize,
    row_bytes: usize,
) -> axum::response::Response {
    if idx >= num_rows {
        return (StatusCode::NOT_FOUND, "row index out of range").into_response();
    }
    let path = state.data_dir.join(filename);
    let offset = (idx * row_bytes) as u64;
    match read_tier_row(&path, offset, row_bytes) {
        Ok(row) => (
            [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
            row,
        )
            .into_response(),
        Err(e) => (StatusCode::INTERNAL_SERVER_ERROR, format!("read error: {e}")).into_response(),
    }
}

async fn get_root(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let info = RootInfo {
        root29: state.serving.metadata.root29.clone(),
        root26: state.serving.metadata.root26.clone(),
        num_ranges: state.serving.metadata.num_ranges,
        pir_depth: state.serving.metadata.pir_depth,
        height: state.serving.metadata.height,
    };
    axum::Json(info)
}

async fn get_health(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let info = HealthInfo {
        status: "ok".to_string(),
        tier1_rows: state.serving.tier1_scenario.num_items,
        tier2_rows: state.serving.tier2_scenario.num_items,
        tier1_row_bytes: TIER1_ROW_BYTES,
        tier2_row_bytes: TIER2_ROW_BYTES,
    };
    axum::Json(info)
}
