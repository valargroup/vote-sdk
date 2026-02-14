//! HTTP routes for the helper server.
//!
//! - `POST /api/v1/shares` — wallet submits a share payload
//! - `GET  /api/v1/status`  — health check, queue depth per round

use axum::extract::State;
use axum::http::StatusCode;
use axum::response::Json;
use axum::routing::{get, post};
use axum::Router;
use base64::prelude::*;
use serde::Serialize;
use std::collections::HashMap;

use crate::store::{QueueStatus, ShareStore};
use crate::types::SharePayload;

// ---------------------------------------------------------------------------
// App state
// ---------------------------------------------------------------------------

/// Shared state for helper server routes.
#[derive(Clone)]
pub struct AppState {
    pub store: ShareStore,
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------

pub fn router(state: AppState) -> Router {
    Router::new()
        .route("/api/v1/shares", post(handle_submit_share))
        .route("/api/v1/status", get(handle_status))
        .with_state(state)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

#[derive(Serialize)]
struct SubmitResponse {
    status: &'static str,
}

async fn handle_submit_share(
    State(state): State<AppState>,
    Json(payload): Json<SharePayload>,
) -> Result<Json<SubmitResponse>, (StatusCode, String)> {
    // Validate required fields.
    validate_payload(&payload)?;

    tracing::info!(
        round_id = %payload.vote_round_id,
        share_index = payload.enc_share.share_index,
        proposal_id = payload.proposal_id,
        tree_position = payload.tree_position,
        "share received"
    );

    state.store.enqueue(payload);

    Ok(Json(SubmitResponse { status: "queued" }))
}

#[derive(Serialize)]
struct StatusResponse {
    status: &'static str,
    queues: HashMap<String, QueueStatus>,
}

async fn handle_status(State(state): State<AppState>) -> Json<StatusResponse> {
    Json(StatusResponse {
        status: "ok",
        queues: state.store.status(),
    })
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

fn validate_payload(p: &SharePayload) -> Result<(), (StatusCode, String)> {
    // shares_hash: must be valid base64, 32 bytes.
    validate_b64_field(&p.shares_hash, 32, "shares_hash")?;

    // enc_share.c1, c2: must be valid base64, 32 bytes each.
    validate_b64_field(&p.enc_share.c1, 32, "enc_share.c1")?;
    validate_b64_field(&p.enc_share.c2, 32, "enc_share.c2")?;

    // share_index: 0..3
    if p.enc_share.share_index > 3 {
        return Err((
            StatusCode::BAD_REQUEST,
            "enc_share.share_index must be 0..3".into(),
        ));
    }

    // vote_round_id: must be valid hex, 32 bytes.
    let round_bytes = hex::decode(&p.vote_round_id)
        .map_err(|e| (StatusCode::BAD_REQUEST, format!("vote_round_id: {}", e)))?;
    if round_bytes.len() != 32 {
        return Err((
            StatusCode::BAD_REQUEST,
            format!(
                "vote_round_id: expected 32 bytes, got {}",
                round_bytes.len()
            ),
        ));
    }

    Ok(())
}

fn validate_b64_field(
    value: &str,
    expected_len: usize,
    field_name: &str,
) -> Result<(), (StatusCode, String)> {
    let bytes = BASE64_STANDARD
        .decode(value)
        .map_err(|e| (StatusCode::BAD_REQUEST, format!("{}: {}", field_name, e)))?;
    if bytes.len() != expected_len {
        return Err((
            StatusCode::BAD_REQUEST,
            format!(
                "{}: expected {} bytes, got {}",
                field_name,
                expected_len,
                bytes.len()
            ),
        ));
    }
    Ok(())
}
