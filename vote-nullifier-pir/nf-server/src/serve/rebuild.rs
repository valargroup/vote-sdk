use std::sync::Arc;

use anyhow::Result;
use axum::extract::State;
use axum::http::StatusCode;
use axum::response::IntoResponse;
use tracing::{info, warn};

use nf_ingest::file_store;
use nf_ingest::sync_nullifiers;

use super::state::{AppState, ServerPhase};

// ── Snapshot management endpoints ─────────────────────────────────────────────

/// Request body for `POST /snapshot/prepare`.
#[derive(serde::Deserialize)]
pub(crate) struct PrepareRequest {
    /// Target block height to rebuild the snapshot at.
    height: u64,
}

/// Query the chain SDK for an active voting round.
///
/// Returns `Some(round_id)` if a round is currently active, `None` otherwise.
/// Used to prevent rebuilds during active rounds which would invalidate proofs.
async fn check_active_round(chain_url: &str) -> Result<Option<String>> {
    let url = format!("{}/shielded-vote/v1/rounds/active", chain_url.trim_end_matches('/'));
    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(5))
        .build()?;
    let resp = client.get(&url).send().await?;

    if !resp.status().is_success() {
        return Ok(None);
    }

    let body: serde_json::Value = resp.json().await?;
    if let Some(round) = body.get("round") {
        if round.is_object() && !round.is_null() {
            let round_id = round
                .get("vote_round_id")
                .and_then(|v| v.as_str())
                .unwrap_or("unknown")
                .to_string();
            return Ok(Some(round_id));
        }
    }
    Ok(None)
}

pub(crate) async fn post_snapshot_prepare(
    State(state): State<Arc<AppState>>,
    axum::Json(req): axum::Json<PrepareRequest>,
) -> impl IntoResponse {
    let height = req.height;

    if let Err(e) = nf_ingest::config::validate_export_height(height) {
        return (
            StatusCode::BAD_REQUEST,
            axum::Json(serde_json::json!({ "error": e.to_string() })),
        )
            .into_response();
    }

    let rebuild_guard = match Arc::clone(&state.rebuild_lock).try_lock_owned() {
        Ok(guard) => guard,
        Err(_) => {
            let phase = state.phase.read().await;
            return (
                StatusCode::CONFLICT,
                axum::Json(serde_json::json!({
                    "error": "rebuild already in progress",
                    "current": *phase,
                })),
            )
                .into_response();
        }
    };

    {
        let lwd_url = state.lwd_urls.first().cloned().unwrap_or_default();
        if !lwd_url.is_empty() {
            match sync_nullifiers::fetch_chain_tip(&lwd_url).await {
                Ok(tip) => {
                    if height > tip {
                        return (
                            StatusCode::BAD_REQUEST,
                            axum::Json(serde_json::json!({
                                "error": format!("height {} exceeds chain tip ({})", height, tip)
                            })),
                        )
                            .into_response();
                    }
                }
                Err(e) => {
                    warn!(error = %e, "failed to fetch chain tip, skipping validation");
                }
            }
        }
    }

    if let Some(chain_url) = &state.chain_url {
        match check_active_round(chain_url).await {
            Ok(Some(round_id)) => {
                return (
                    StatusCode::CONFLICT,
                    axum::Json(serde_json::json!({
                        "error": "cannot rebuild while round is active",
                        "round_id": round_id,
                    })),
                )
                    .into_response();
            }
            Ok(None) => {}
            Err(e) => {
                warn!(error = %e, "failed to check active round, proceeding anyway");
            }
        }
    }

    {
        let mut phase = state.phase.write().await;
        *phase = ServerPhase::Rebuilding {
            target_height: height,
            progress: "starting".to_string(),
            progress_pct: 0,
        };
    }

    let state_clone = Arc::clone(&state);
    tokio::task::spawn(async move {
        let _rebuild_guard = rebuild_guard;
        let result = run_rebuild(state_clone.clone(), height).await;
        if let Err(e) = result {
            let msg = format!("{:?}", e);
            warn!(error = %msg, "rebuild failed");
            let mut phase = state_clone.phase.write().await;
            *phase = ServerPhase::Error { message: msg };
        }
    });

    (
        StatusCode::ACCEPTED,
        axum::Json(serde_json::json!({
            "status": "rebuilding",
            "target_height": height,
        })),
    )
        .into_response()
}

/// Run the full rebuild pipeline: ingest (if needed) → export → load.
async fn run_rebuild(state: Arc<AppState>, target_height: u64) -> Result<()> {
    let data_dir = state.data_dir.clone();
    let pir_data_dir = state.pir_data_dir.clone();
    let lwd_urls = state.lwd_urls.clone();

    {
        let mut phase = state.phase.write().await;
        *phase = ServerPhase::Rebuilding {
            target_height,
            progress: "checking sync state".to_string(),
            progress_pct: 0,
        };
    }

    let current_height = file_store::load_checkpoint(&data_dir)?
        .map(|(h, _)| h)
        .unwrap_or(0);

    if target_height > current_height {
        {
            let mut phase = state.phase.write().await;
            *phase = ServerPhase::Rebuilding {
                target_height,
                progress: format!("ingesting blocks {current_height}..{target_height}"),
                progress_pct: 2,
            };
        }

        let dd = data_dir.clone();
        let lwd = lwd_urls.clone();
        let state_ref = Arc::clone(&state);
        tokio::task::spawn_blocking(move || {
            let rt = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()?;
            rt.block_on(sync_nullifiers::sync(&dd, &lwd, Some(target_height), |h, t, _, _| {
                info!(height = h, target = t, "ingest progress");
                let pct = if t > 0 {
                    2 + ((h as f64 / t as f64) * 8.0) as u8
                } else {
                    5
                };
                if let Ok(mut phase) = state_ref.phase.try_write() {
                    *phase = ServerPhase::Rebuilding {
                        target_height,
                        progress: format!("ingesting {h}/{t}"),
                        progress_pct: pct,
                    };
                }
            }))?;
            Ok::<_, anyhow::Error>(())
        })
        .await??;
    }

    {
        let mut phase = state.phase.write().await;
        *phase = ServerPhase::Rebuilding {
            target_height,
            progress: "loading nullifiers".to_string(),
            progress_pct: 10,
        };
    }

    let dd = data_dir.clone();
    let pd = pir_data_dir.clone();
    let state_ref = Arc::clone(&state);
    tokio::task::spawn_blocking(move || {
        let entry = file_store::offset_for_height(&dd, target_height)?;
        let (idx_height, byte_offset) = entry.ok_or_else(|| {
            anyhow::anyhow!("no index entry for target height {}", target_height)
        })?;
        info!(height = idx_height, byte_offset, "Loading nullifiers for export");
        let nfs = file_store::load_nullifiers_up_to(&dd, byte_offset)?;
        info!(count = nfs.len(), "Nullifiers loaded");

        pir_export::build_and_export_with_progress(nfs, &pd, Some(idx_height), |msg, pct| {
            let overall_pct = 10 + (pct as u16 * 45 / 55).min(45) as u8;
            if let Ok(mut phase) = state_ref.phase.try_write() {
                *phase = ServerPhase::Rebuilding {
                    target_height,
                    progress: msg.to_string(),
                    progress_pct: overall_pct,
                };
            }
        })?;
        Ok::<_, anyhow::Error>(())
    })
    .await??;

    {
        let mut phase = state.phase.write().await;
        *phase = ServerPhase::Rebuilding {
            target_height,
            progress: "loading YPIR servers".to_string(),
            progress_pct: 60,
        };
    }

    let pd = pir_data_dir.clone();
    let new_serving = tokio::task::spawn_blocking(move || pir_server::load_serving_state(&pd)).await??;

    {
        let mut serving = state.serving.write().await;
        *serving = Some(new_serving);
    }
    {
        let mut phase = state.phase.write().await;
        *phase = ServerPhase::Serving;
    }

    info!(target_height, "rebuild complete");
    Ok(())
}

pub(crate) async fn get_snapshot_status(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let (phase_json, height, num_ranges) = {
        let phase = state.phase.read().await;
        let serving = state.serving.read().await;
        let h = serving.as_ref().and_then(|s| s.metadata.height);
        let n = serving.as_ref().map(|s| s.metadata.num_ranges);
        (serde_json::to_value(&*phase).unwrap_or_default(), h, n)
    };

    let zcash_tip = if let Some(lwd_url) = state.lwd_urls.first() {
        sync_nullifiers::fetch_chain_tip(lwd_url).await.ok()
    } else {
        None
    };

    let mut resp = phase_json;
    if let Some(obj) = resp.as_object_mut() {
        obj.insert("height".to_string(), serde_json::json!(height));
        obj.insert("num_ranges".to_string(), serde_json::json!(num_ranges));
        obj.insert("zcash_tip".to_string(), serde_json::json!(zcash_tip));
    }

    axum::Json(resp)
}
