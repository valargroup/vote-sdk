//! HTTP handlers for the PIR server.
//!
//! Each handler acquires the shared [`AppState`] and returns 503 if the
//! server is currently rebuilding its snapshot. The YPIR query endpoints
//! track inflight request counts for backpressure monitoring.

use std::sync::Arc;

use axum::body::Bytes;
use axum::extract::{Path, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;

use pir_server::{
    HealthInfo, RootInfo,
    TIER1_ROWS, TIER1_ROW_BYTES, TIER2_ROWS, TIER2_ROW_BYTES,
    read_tier_row, dispatch_query,
};

use super::state::{AppState, ServerPhase};

// ── PIR data endpoints ───────────────────────────────────────────────────────

/// `GET /tier0` — Return the full Tier 0 binary blob (plaintext, small).
pub(crate) async fn get_tier0(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().expect("guaranteed Some by require_serving");
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        s.tier0_data.clone(),
    )
        .into_response()
}

/// `GET /params/tier1` — Return the Tier 1 YPIR scenario parameters as JSON.
pub(crate) async fn get_params_tier1(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().expect("guaranteed Some by require_serving");
    axum::Json(s.tier1_scenario.clone()).into_response()
}

/// `GET /params/tier2` — Return the Tier 2 YPIR scenario parameters as JSON.
pub(crate) async fn get_params_tier2(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().expect("guaranteed Some by require_serving");
    axum::Json(s.tier2_scenario.clone()).into_response()
}

/// `GET /hint/tier1` — Return precomputed YPIR hint for Tier 1.
pub(crate) async fn get_hint_tier1(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().expect("guaranteed Some by require_serving");
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        s.tier1_hint.clone(),
    )
        .into_response()
}

/// `GET /hint/tier2` — Return precomputed YPIR hint for Tier 2.
pub(crate) async fn get_hint_tier2(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().expect("guaranteed Some by require_serving");
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        s.tier2_hint.clone(),
    )
        .into_response()
}

// ── YPIR query endpoints ─────────────────────────────────────────────────────

/// `POST /tier1/query` — Process an encrypted YPIR query against Tier 1.
pub(crate) async fn post_tier1_query(State(state): State<Arc<AppState>>, body: Bytes) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().expect("guaranteed Some by require_serving");
    dispatch_query(&s.tier1, "tier1", &body, &state.next_req_id, &state.inflight_requests)
}

/// `POST /tier2/query` — Process an encrypted YPIR query against Tier 2.
pub(crate) async fn post_tier2_query(State(state): State<Arc<AppState>>, body: Bytes) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().expect("guaranteed Some by require_serving");
    dispatch_query(&s.tier2, "tier2", &body, &state.next_req_id, &state.inflight_requests)
}

// ── Tier row endpoints (raw row reads for debugging) ─────────────────────────

/// `GET /tier1/row/:idx` — Read a raw Tier 1 row from disk (for debugging).
pub(crate) async fn get_tier1_row(
    State(state): State<Arc<AppState>>,
    Path(idx): Path<usize>,
) -> impl IntoResponse {
    get_tier_row(&state, idx, "tier1.bin", TIER1_ROWS, TIER1_ROW_BYTES).await
}

/// `GET /tier2/row/:idx` — Read a raw Tier 2 row from disk (for debugging).
pub(crate) async fn get_tier2_row(
    State(state): State<Arc<AppState>>,
    Path(idx): Path<usize>,
) -> impl IntoResponse {
    get_tier_row(&state, idx, "tier2.bin", TIER2_ROWS, TIER2_ROW_BYTES).await
}

/// Shared handler for raw tier row reads. Validates index bounds and reads
/// the row directly from the tier binary file on disk.
async fn get_tier_row(
    state: &AppState,
    idx: usize,
    filename: &str,
    num_rows: usize,
    row_bytes: usize,
) -> axum::response::Response {
    let guard = state.serving.read().await;
    if guard.is_none() {
        let phase = state.phase.read().await;
        let body = serde_json::to_string(&*phase).unwrap_or_default();
        return (
            StatusCode::SERVICE_UNAVAILABLE,
            [(axum::http::header::CONTENT_TYPE, "application/json")],
            body,
        )
            .into_response();
    }
    if idx >= num_rows {
        return (StatusCode::NOT_FOUND, "row index out of range").into_response();
    }
    let path = state.pir_data_dir.join(filename);
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

// ── Root and health ──────────────────────────────────────────────────────────

/// `GET /root` — Return the current tree root hash and metadata as JSON.
pub(crate) async fn get_root(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().expect("guaranteed Some by require_serving");
    let info = RootInfo {
        root29: s.metadata.root29.clone(),
        root26: s.metadata.root26.clone(),
        num_ranges: s.metadata.num_ranges,
        pir_depth: s.metadata.pir_depth,
        height: s.metadata.height,
    };
    axum::Json(info).into_response()
}

/// `GET /health` — Return server health including phase and tier metadata.
pub(crate) async fn get_health(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let phase = state.phase.read().await;
    let serving = state.serving.read().await;

    let status = match &*phase {
        ServerPhase::Serving => "ok",
        ServerPhase::Rebuilding { .. } => "rebuilding",
        ServerPhase::Error { .. } => "error",
    };

    let (tier1_rows, tier2_rows) = match serving.as_ref() {
        Some(s) => (s.tier1_scenario.num_items, s.tier2_scenario.num_items),
        None => (0, 0),
    };

    let info = HealthInfo {
        status: status.to_string(),
        tier1_rows,
        tier2_rows,
        tier1_row_bytes: TIER1_ROW_BYTES,
        tier2_row_bytes: TIER2_ROW_BYTES,
    };
    axum::Json(info)
}
