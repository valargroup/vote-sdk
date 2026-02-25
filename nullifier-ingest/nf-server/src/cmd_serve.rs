use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Instant;

use anyhow::Result;
use axum::body::Bytes;
use axum::extract::{DefaultBodyLimit, Path, State};
use axum::http::{HeaderValue, StatusCode};
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::Router;
use clap::Args as ClapArgs;
use tokio::sync::RwLock;

use pir_export::PirMetadata;
use pir_server::{
    HealthInfo, OwnedTierState, QueryTiming, RootInfo, YpirScenario,
    TIER1_ROWS, TIER1_ROW_BYTES, TIER2_ROWS, TIER2_ROW_BYTES,
};
use tracing::{info, warn};

use nullifier_service::file_store;
use nullifier_service::sync_nullifiers;

/// Default lightwalletd endpoints (shared with cmd_ingest).
const DEFAULT_LWD_URLS: &[&str] = &[
    "https://zec.rocks:443",
    "https://eu2.zec.stardust.rest:443",
    "https://eu.zec.stardust.rest:443",
];

#[derive(ClapArgs)]
pub struct Args {
    /// Listen port.
    #[arg(long, default_value = "3000")]
    port: u16,

    /// Directory containing tier0.bin, tier1.bin, tier2.bin, and pir_root.json.
    #[arg(long, default_value = "./pir-data")]
    pir_data_dir: PathBuf,

    /// Directory containing nullifiers.bin and nullifiers.checkpoint.
    /// Required for snapshot rebuilds via POST /snapshot/prepare.
    #[arg(long, default_value = ".")]
    data_dir: PathBuf,

    /// Lightwalletd endpoint URL(s) for syncing during rebuild.
    /// Can also be set via LWD_URLS env (comma-separated).
    #[arg(long, default_value = "https://zec.rocks:443")]
    lwd_url: String,

    /// Chain SDK URL for checking active rounds before rebuild.
    /// If set, POST /snapshot/prepare will reject rebuilds when a round is active.
    #[arg(long, env = "ZALLY_CHAIN_URL")]
    chain_url: Option<String>,
}

// ── Server phase model ────────────────────────────────────────────────────────

#[derive(Clone, serde::Serialize)]
#[serde(tag = "phase")]
enum ServerPhase {
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

/// All data needed to serve PIR queries. Replaced atomically on rebuild.
struct ServingState {
    tier0_data: Vec<u8>,
    tier1: OwnedTierState,
    tier2: OwnedTierState,
    tier1_scenario: YpirScenario,
    tier2_scenario: YpirScenario,
    tier1_hint: Vec<u8>,
    tier2_hint: Vec<u8>,
    metadata: PirMetadata,
}

struct AppState {
    phase: RwLock<ServerPhase>,
    serving: RwLock<Option<ServingState>>,
    data_dir: PathBuf,
    pir_data_dir: PathBuf,
    lwd_urls: Vec<String>,
    chain_url: Option<String>,
    next_req_id: AtomicU64,
    inflight_requests: AtomicUsize,
}

// ── Startup / loading ─────────────────────────────────────────────────────────

/// Load tier files from disk and construct ServingState.
fn load_serving_state(pir_data_dir: &std::path::Path) -> Result<ServingState> {
    let t_total = Instant::now();

    let tier0_data = std::fs::read(pir_data_dir.join("tier0.bin"))?;
    eprintln!("  Tier 0: {} bytes", tier0_data.len());

    let tier1_data = std::fs::read(pir_data_dir.join("tier1.bin"))?;
    eprintln!(
        "  Tier 1: {} bytes ({} rows)",
        tier1_data.len(),
        tier1_data.len() / TIER1_ROW_BYTES
    );
    anyhow::ensure!(
        tier1_data.len() == TIER1_ROWS * TIER1_ROW_BYTES,
        "tier1.bin size mismatch: got {} bytes, expected {}",
        tier1_data.len(),
        TIER1_ROWS * TIER1_ROW_BYTES
    );

    let tier2_data = std::fs::read(pir_data_dir.join("tier2.bin"))?;
    eprintln!(
        "  Tier 2: {} bytes ({} rows)",
        tier2_data.len(),
        tier2_data.len() / TIER2_ROW_BYTES
    );
    anyhow::ensure!(
        tier2_data.len() == TIER2_ROWS * TIER2_ROW_BYTES,
        "tier2.bin size mismatch: got {} bytes, expected {}",
        tier2_data.len(),
        TIER2_ROWS * TIER2_ROW_BYTES
    );

    let metadata: PirMetadata =
        serde_json::from_str(&std::fs::read_to_string(pir_data_dir.join("pir_root.json"))?)?;
    eprintln!(
        "  Metadata: {} ranges, root29={}...",
        metadata.num_ranges,
        metadata.root29.get(..16).unwrap_or(&metadata.root29)
    );

    // Initialize YPIR servers
    eprintln!("Initializing YPIR servers...");
    let tier1_scenario = pir_server::tier1_scenario();
    let tier1 = OwnedTierState::new(tier1_data, tier1_scenario.clone());
    let tier1_hint = tier1.hint_bytes();
    eprintln!("  Tier 1 YPIR ready (hint: {} bytes)", tier1_hint.len());

    let tier2_scenario = pir_server::tier2_scenario();
    let tier2 = OwnedTierState::new(tier2_data, tier2_scenario.clone());
    let tier2_hint = tier2.hint_bytes();
    eprintln!("  Tier 2 YPIR ready (hint: {} bytes)", tier2_hint.len());

    eprintln!("Server ready in {:.1}s", t_total.elapsed().as_secs_f64());

    Ok(ServingState {
        tier0_data,
        tier1,
        tier2,
        tier1_scenario,
        tier2_scenario,
        tier1_hint,
        tier2_hint,
        metadata,
    })
}

pub async fn run(args: Args) -> Result<()> {
    tracing_subscriber::fmt::init();

    // Resolve lightwalletd URLs
    let lwd_urls: Vec<String> = std::env::var("LWD_URLS")
        .map(|s| s.split(',').map(|u| u.trim().to_string()).collect())
        .unwrap_or_else(|_| vec![args.lwd_url.clone()]);
    let lwd_urls = if lwd_urls.len() == 1 && lwd_urls[0] == "https://zec.rocks:443" {
        DEFAULT_LWD_URLS.iter().map(|s| s.to_string()).collect()
    } else {
        lwd_urls
    };

    // Ensure the index file exists (migration from old format)
    file_store::rebuild_index(&args.data_dir)?;

    eprintln!("Loading tier files from {:?}...", args.pir_data_dir);
    let serving = load_serving_state(&args.pir_data_dir)?;

    let state = Arc::new(AppState {
        phase: RwLock::new(ServerPhase::Serving),
        serving: RwLock::new(Some(serving)),
        data_dir: args.data_dir.clone(),
        pir_data_dir: args.pir_data_dir.clone(),
        lwd_urls,
        chain_url: args.chain_url,
        next_req_id: AtomicU64::new(0),
        inflight_requests: AtomicUsize::new(0),
    });

    let cors = tower_http::cors::CorsLayer::permissive();

    let app = Router::new()
        // PIR data endpoints
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
        // Snapshot management endpoints
        .route("/snapshot/prepare", post(post_snapshot_prepare))
        .route("/snapshot/status", get(get_snapshot_status))
        // Health
        .route("/health", get(get_health))
        .layer(DefaultBodyLimit::max(512 * 1024 * 1024)) // 512 MB for YPIR queries
        .layer(cors)
        .with_state(state);

    let addr = format!("0.0.0.0:{}", args.port);
    eprintln!("Listening on {addr}");
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    axum::serve(listener, app).await?;

    Ok(())
}

// ── Macro: return 503 during rebuild ──────────────────────────────────────────

/// Acquire the serving state or return 503 if unavailable (during rebuild).
macro_rules! require_serving {
    ($state:expr) => {{
        let guard = $state.serving.read().await;
        if guard.is_none() {
            let phase = $state.phase.read().await;
            let body = serde_json::to_string(&*phase).unwrap_or_default();
            return (
                StatusCode::SERVICE_UNAVAILABLE,
                [(axum::http::header::CONTENT_TYPE, "application/json")],
                body,
            )
                .into_response();
        }
        guard
    }};
}

// ── Snapshot management endpoints ─────────────────────────────────────────────

#[derive(serde::Deserialize)]
struct PrepareRequest {
    height: u64,
}

/// Check if there's an active voting round by querying the chain SDK.
/// Returns `Ok(Some(round_id))` if active, `Ok(None)` if not.
async fn check_active_round(chain_url: &str) -> Result<Option<String>> {
    let url = format!("{}/zally/v1/rounds/active", chain_url.trim_end_matches('/'));
    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(5))
        .build()?;
    let resp = client.get(&url).send().await?;

    if !resp.status().is_success() {
        // 404 or error likely means no active round
        return Ok(None);
    }

    let body: serde_json::Value = resp.json().await?;
    // The response has { "round": { "vote_round_id": "...", "status": "..." } }
    // If the round object exists and has a status, there's an active round.
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

async fn post_snapshot_prepare(
    State(state): State<Arc<AppState>>,
    axum::Json(req): axum::Json<PrepareRequest>,
) -> impl IntoResponse {
    let height = req.height;

    // Validate height
    if height < sync_nullifiers::NU5_ACTIVATION_HEIGHT {
        return (
            StatusCode::BAD_REQUEST,
            axum::Json(serde_json::json!({
                "error": format!(
                    "height {} is below NU5 activation ({})",
                    height,
                    sync_nullifiers::NU5_ACTIVATION_HEIGHT
                )
            })),
        )
            .into_response();
    }
    if height % 10 != 0 {
        return (
            StatusCode::BAD_REQUEST,
            axum::Json(serde_json::json!({
                "error": format!("height {} must be a multiple of 10", height)
            })),
        )
            .into_response();
    }

    // Check if already rebuilding
    {
        let phase = state.phase.read().await;
        if let ServerPhase::Rebuilding { .. } = &*phase {
            return (
                StatusCode::CONFLICT,
                axum::Json(serde_json::json!({
                    "error": "rebuild already in progress",
                    "current": *phase,
                })),
            )
                .into_response();
        }
    }

    // Validate height <= chain tip by querying lightwalletd
    {
        let lwd_url = state.lwd_urls.first().cloned().unwrap_or_default();
        if !lwd_url.is_empty() {
            match sync_nullifiers::fetch_chain_tip(&lwd_url).await {
                Ok(tip) => {
                    if height > tip {
                        return (
                            StatusCode::BAD_REQUEST,
                            axum::Json(serde_json::json!({
                                "error": format!(
                                    "height {} exceeds chain tip ({})",
                                    height, tip
                                )
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

    // Check for active voting round (if chain URL is configured)
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
            Ok(None) => {} // No active round, proceed
            Err(e) => {
                warn!(error = %e, "failed to check active round, proceeding anyway");
            }
        }
    }

    // Set phase to Rebuilding, clear serving state
    {
        let mut phase = state.phase.write().await;
        *phase = ServerPhase::Rebuilding {
            target_height: height,
            progress: "starting".to_string(),
            progress_pct: 0,
        };
    }
    {
        let mut serving = state.serving.write().await;
        // Drop old serving state. Wait for inflight requests to drain.
        let drain_start = Instant::now();
        while state.inflight_requests.load(Ordering::Relaxed) > 0 {
            if drain_start.elapsed().as_secs() > 30 {
                warn!("Timed out waiting for inflight requests to drain");
                break;
            }
            tokio::time::sleep(tokio::time::Duration::from_millis(100)).await;
        }
        *serving = None; // Drop old state, free memory
    }

    // Spawn rebuild in background
    let state_clone = Arc::clone(&state);
    tokio::task::spawn(async move {
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

    // Step 1: Check if we need to ingest more blocks
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
        // Need to ingest up to target_height
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
                eprintln!("  ingest: {h}/{t}");
                let pct = if t > 0 {
                    2 + ((h as f64 / t as f64) * 8.0) as u8 // 2–10%
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

    // Step 2: Export at target height (10–55%)
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
        eprintln!(
            "  Export: loading nullifiers up to height {} (offset={})",
            idx_height, byte_offset
        );
        let nfs = file_store::load_nullifiers_up_to(&dd, byte_offset)?;
        eprintln!("  Loaded {} nullifiers", nfs.len());

        pir_export::build_and_export_with_progress(nfs, &pd, Some(idx_height), |msg, pct| {
            // Map export's 0–55% into our 10–55% range
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

    // Step 3: Load new tier files (60–95%)
    {
        let mut phase = state.phase.write().await;
        *phase = ServerPhase::Rebuilding {
            target_height,
            progress: "loading YPIR servers".to_string(),
            progress_pct: 60,
        };
    }

    let pd = pir_data_dir.clone();
    let new_serving = tokio::task::spawn_blocking(move || load_serving_state(&pd)).await??;

    // Step 4: Swap in new serving state
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

async fn get_snapshot_status(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let phase = state.phase.read().await;
    let serving = state.serving.read().await;

    let height = serving.as_ref().and_then(|s| s.metadata.height);
    let num_ranges = serving.as_ref().map(|s| s.metadata.num_ranges);

    // Fetch Zcash mainnet chain tip (best-effort, don't block on failure).
    let zcash_tip = if let Some(lwd_url) = state.lwd_urls.first() {
        sync_nullifiers::fetch_chain_tip(lwd_url).await.ok()
    } else {
        None
    };

    let mut resp = serde_json::to_value(&*phase).unwrap_or_default();
    if let Some(obj) = resp.as_object_mut() {
        obj.insert("height".to_string(), serde_json::json!(height));
        obj.insert("num_ranges".to_string(), serde_json::json!(num_ranges));
        obj.insert("zcash_tip".to_string(), serde_json::json!(zcash_tip));
    }

    axum::Json(resp)
}

// ── PIR data handlers ─────────────────────────────────────────────────────────

async fn get_tier0(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().unwrap();
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        s.tier0_data.clone(),
    )
        .into_response()
}

async fn get_params_tier1(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().unwrap();
    axum::Json(s.tier1_scenario.clone()).into_response()
}

async fn get_params_tier2(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().unwrap();
    axum::Json(s.tier2_scenario.clone()).into_response()
}

async fn get_hint_tier1(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().unwrap();
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        s.tier1_hint.clone(),
    )
        .into_response()
}

async fn get_hint_tier2(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().unwrap();
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        s.tier2_hint.clone(),
    )
        .into_response()
}

async fn post_tier1_query(State(state): State<Arc<AppState>>, body: Bytes) -> impl IntoResponse {
    let req_id = state.next_req_id.fetch_add(1, Ordering::Relaxed) + 1;
    let inflight = state.inflight_requests.fetch_add(1, Ordering::Relaxed) + 1;
    let _inflight_guard = InflightGuard::new(&state.inflight_requests);
    let t0 = Instant::now();

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
    let s = guard.as_ref().unwrap();

    info!(
        req_id,
        tier = "tier1",
        body_bytes = body.len(),
        inflight_requests = inflight,
        "pir_request_started"
    );
    match s.tier1.server().answer_query(&body) {
        Ok(answer) => {
            let handler_ms = t0.elapsed().as_secs_f64() * 1000.0;
            let mut response = (
                StatusCode::OK,
                [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
                answer.response,
            )
                .into_response();
            write_timing_headers(response.headers_mut(), req_id, answer.timing);
            info!(
                req_id,
                tier = "tier1",
                status = 200,
                handler_ms = format!("{handler_ms:.3}"),
                validate_ms = format!("{:.3}", answer.timing.validate_ms),
                decode_copy_ms = format!("{:.3}", answer.timing.decode_copy_ms),
                compute_ms = format!("{:.3}", answer.timing.online_compute_ms),
                server_total_ms = format!("{:.3}", answer.timing.total_ms),
                response_bytes = answer.timing.response_bytes,
                "pir_request_finished"
            );
            response
        }
        Err(e) => {
            warn!(
                req_id,
                tier = "tier1",
                status = 400,
                handler_ms = format!("{:.3}", t0.elapsed().as_secs_f64() * 1000.0),
                error = %e,
                "pir_request_failed"
            );
            (StatusCode::BAD_REQUEST, e.to_string()).into_response()
        }
    }
}

async fn post_tier2_query(State(state): State<Arc<AppState>>, body: Bytes) -> impl IntoResponse {
    let req_id = state.next_req_id.fetch_add(1, Ordering::Relaxed) + 1;
    let inflight = state.inflight_requests.fetch_add(1, Ordering::Relaxed) + 1;
    let _inflight_guard = InflightGuard::new(&state.inflight_requests);
    let t0 = Instant::now();

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
    let s = guard.as_ref().unwrap();

    info!(
        req_id,
        tier = "tier2",
        body_bytes = body.len(),
        inflight_requests = inflight,
        "pir_request_started"
    );
    match s.tier2.server().answer_query(&body) {
        Ok(answer) => {
            let handler_ms = t0.elapsed().as_secs_f64() * 1000.0;
            let mut response = (
                StatusCode::OK,
                [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
                answer.response,
            )
                .into_response();
            write_timing_headers(response.headers_mut(), req_id, answer.timing);
            info!(
                req_id,
                tier = "tier2",
                status = 200,
                handler_ms = format!("{handler_ms:.3}"),
                validate_ms = format!("{:.3}", answer.timing.validate_ms),
                decode_copy_ms = format!("{:.3}", answer.timing.decode_copy_ms),
                compute_ms = format!("{:.3}", answer.timing.online_compute_ms),
                server_total_ms = format!("{:.3}", answer.timing.total_ms),
                response_bytes = answer.timing.response_bytes,
                "pir_request_finished"
            );
            response
        }
        Err(e) => {
            warn!(
                req_id,
                tier = "tier2",
                status = 400,
                handler_ms = format!("{:.3}", t0.elapsed().as_secs_f64() * 1000.0),
                error = %e,
                "pir_request_failed"
            );
            (StatusCode::BAD_REQUEST, e.to_string()).into_response()
        }
    }
}

struct InflightGuard<'a> {
    inflight: &'a AtomicUsize,
}

impl<'a> InflightGuard<'a> {
    fn new(inflight: &'a AtomicUsize) -> Self {
        Self { inflight }
    }
}

impl Drop for InflightGuard<'_> {
    fn drop(&mut self) {
        self.inflight.fetch_sub(1, Ordering::Relaxed);
    }
}

fn write_timing_headers(headers: &mut axum::http::HeaderMap, req_id: u64, timing: QueryTiming) {
    // Expose server-side stage timing so the client can split RTT into server vs network/queue.
    headers.insert(
        "x-pir-req-id",
        HeaderValue::from_str(&req_id.to_string()).expect("req_id header must be valid"),
    );
    headers.insert(
        "x-pir-server-total-ms",
        HeaderValue::from_str(&format!("{:.3}", timing.total_ms))
            .expect("timing header must be valid"),
    );
    headers.insert(
        "x-pir-server-validate-ms",
        HeaderValue::from_str(&format!("{:.3}", timing.validate_ms))
            .expect("timing header must be valid"),
    );
    headers.insert(
        "x-pir-server-decode-copy-ms",
        HeaderValue::from_str(&format!("{:.3}", timing.decode_copy_ms))
            .expect("timing header must be valid"),
    );
    headers.insert(
        "x-pir-server-compute-ms",
        HeaderValue::from_str(&format!("{:.3}", timing.online_compute_ms))
            .expect("timing header must be valid"),
    );
    headers.insert(
        "x-pir-server-response-bytes",
        HeaderValue::from_str(&timing.response_bytes.to_string())
            .expect("response size header must be valid"),
    );
}

async fn get_tier1_row(
    State(state): State<Arc<AppState>>,
    Path(idx): Path<usize>,
) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().unwrap();
    if idx >= TIER1_ROWS {
        return (StatusCode::NOT_FOUND, "row index out of range").into_response();
    }
    let tier1_data = s.tier1.data();
    let offset = idx * TIER1_ROW_BYTES;
    let row = &tier1_data[offset..offset + TIER1_ROW_BYTES];
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        row.to_vec(),
    )
        .into_response()
}

async fn get_tier2_row(
    State(state): State<Arc<AppState>>,
    Path(idx): Path<usize>,
) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().unwrap();
    if idx >= TIER2_ROWS {
        return (StatusCode::NOT_FOUND, "row index out of range").into_response();
    }
    let tier2_data = s.tier2.data();
    let offset = idx * TIER2_ROW_BYTES;
    let row = &tier2_data[offset..offset + TIER2_ROW_BYTES];
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        row.to_vec(),
    )
        .into_response()
}

async fn get_root(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let guard = require_serving!(state);
    let s = guard.as_ref().unwrap();
    let info = RootInfo {
        root29: s.metadata.root29.clone(),
        root26: s.metadata.root26.clone(),
        num_ranges: s.metadata.num_ranges,
        pir_depth: s.metadata.pir_depth,
        height: s.metadata.height,
    };
    axum::Json(info).into_response()
}

async fn get_health(State(state): State<Arc<AppState>>) -> impl IntoResponse {
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
