use std::io::{Read, Seek, SeekFrom};
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

use pir_export::PirMetadata;
use pir_server::{
    HealthInfo, OwnedTierState, QueryTiming, RootInfo, YpirScenario, TIER1_ROWS, TIER1_ROW_BYTES,
    TIER2_ROWS, TIER2_ROW_BYTES,
};
use tracing::{info, warn};

struct AppState {
    tier0_data: Bytes,
    data_dir: PathBuf,
    tier1: OwnedTierState,
    tier2: OwnedTierState,
    tier1_scenario: YpirScenario,
    tier2_scenario: YpirScenario,
    tier1_hint: Bytes,
    tier2_hint: Bytes,
    metadata: PirMetadata,
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
        Some(s) => s.parse().expect("invalid port number"),
        None => 3001,
    };

    let t_total = Instant::now();

    // Load tier files
    eprintln!("Loading tier files from {:?}...", data_dir);

    let tier0_data = Bytes::from(std::fs::read(data_dir.join("tier0.bin"))?);
    eprintln!("  Tier 0: {} bytes", tier0_data.len());

    // Read tier data into temporary buffers. These are dropped after YPIR
    // construction — the YPIR server copies everything into its own
    // `db_buf_aligned` during `YServer::new()`.
    let tier1_data = std::fs::read(data_dir.join("tier1.bin"))?;
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

    let tier2_data = std::fs::read(data_dir.join("tier2.bin"))?;
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
        serde_json::from_str(&std::fs::read_to_string(data_dir.join("pir_root.json"))?)?;
    eprintln!(
        "  Metadata: {} ranges, root29={}...",
        metadata.num_ranges,
        metadata.root29.get(..16).unwrap_or(&metadata.root29)
    );

    // Initialize YPIR servers. Pass borrowed data — OwnedTierState does not
    // retain it (YPIR copies into its own db_buf_aligned during construction).
    eprintln!("Initializing YPIR servers...");

    let tier1_scenario = pir_server::tier1_scenario();
    let mut tier1 = OwnedTierState::new(&tier1_data, tier1_scenario.clone());
    drop(tier1_data); // free ~48 MB
    let tier1_hint = Bytes::from(tier1.take_hint_bytes());
    eprintln!("  Tier 1 YPIR ready (hint: {} bytes)", tier1_hint.len());

    let tier2_scenario = pir_server::tier2_scenario();
    let mut tier2 = OwnedTierState::new(&tier2_data, tier2_scenario.clone());
    drop(tier2_data); // free ~6 GB
    let tier2_hint = Bytes::from(tier2.take_hint_bytes());
    eprintln!("  Tier 2 YPIR ready (hint: {} bytes)", tier2_hint.len());

    eprintln!("Server ready in {:.1}s", t_total.elapsed().as_secs_f64());

    let state = Arc::new(AppState {
        tier0_data,
        data_dir: data_dir.clone(),
        tier1,
        tier2,
        tier1_scenario,
        tier2_scenario,
        tier1_hint,
        tier2_hint,
        metadata,
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
        .layer(DefaultBodyLimit::max(512 * 1024 * 1024)) // 512 MB for YPIR queries
        .with_state(state);

    let addr = format!("0.0.0.0:{port}");
    eprintln!("Listening on {addr}");
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    axum::serve(listener, app).await?;

    Ok(())
}

// ── Handlers ─────────────────────────────────────────────────────────────────

async fn get_tier0(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        state.tier0_data.clone(),
    )
}

async fn get_params_tier1(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    axum::Json(state.tier1_scenario.clone())
}

async fn get_params_tier2(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    axum::Json(state.tier2_scenario.clone())
}

async fn get_hint_tier1(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        state.tier1_hint.clone(),
    )
}

async fn get_hint_tier2(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        state.tier2_hint.clone(),
    )
}

async fn post_tier1_query(State(state): State<Arc<AppState>>, body: Bytes) -> impl IntoResponse {
    let req_id = state.next_req_id.fetch_add(1, Ordering::Relaxed) + 1;
    let inflight = state.inflight_requests.fetch_add(1, Ordering::Relaxed) + 1;
    let _inflight_guard = InflightGuard::new(&state.inflight_requests);
    let t0 = Instant::now();
    info!(
        req_id,
        tier = "tier1",
        body_bytes = body.len(),
        inflight_requests = inflight,
        "pir_request_started"
    );
    match state.tier1.server().answer_query(&body) {
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
    info!(
        req_id,
        tier = "tier2",
        body_bytes = body.len(),
        inflight_requests = inflight,
        "pir_request_started"
    );
    match state.tier2.server().answer_query(&body) {
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
    if idx >= TIER1_ROWS {
        return (StatusCode::NOT_FOUND, "row index out of range").into_response();
    }
    let path = state.data_dir.join("tier1.bin");
    let offset = (idx * TIER1_ROW_BYTES) as u64;
    match read_tier_row(&path, offset, TIER1_ROW_BYTES) {
        Ok(row) => (
            [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
            row,
        )
            .into_response(),
        Err(e) => (StatusCode::INTERNAL_SERVER_ERROR, format!("read error: {e}")).into_response(),
    }
}

async fn get_tier2_row(
    State(state): State<Arc<AppState>>,
    Path(idx): Path<usize>,
) -> impl IntoResponse {
    if idx >= TIER2_ROWS {
        return (StatusCode::NOT_FOUND, "row index out of range").into_response();
    }
    let path = state.data_dir.join("tier2.bin");
    let offset = (idx * TIER2_ROW_BYTES) as u64;
    match read_tier_row(&path, offset, TIER2_ROW_BYTES) {
        Ok(row) => (
            [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
            row,
        )
            .into_response(),
        Err(e) => (StatusCode::INTERNAL_SERVER_ERROR, format!("read error: {e}")).into_response(),
    }
}

/// Read a single row from a tier file on disk.
fn read_tier_row(path: &std::path::Path, offset: u64, len: usize) -> std::io::Result<Vec<u8>> {
    let mut f = std::fs::File::open(path)?;
    f.seek(SeekFrom::Start(offset))?;
    let mut buf = vec![0u8; len];
    f.read_exact(&mut buf)?;
    Ok(buf)
}

async fn get_root(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let info = RootInfo {
        root29: state.metadata.root29.clone(),
        root26: state.metadata.root26.clone(),
        num_ranges: state.metadata.num_ranges,
        pir_depth: state.metadata.pir_depth,
        height: state.metadata.height,
    };
    axum::Json(info)
}

async fn get_health(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let info = HealthInfo {
        status: "ok".to_string(),
        tier1_rows: state.tier1_scenario.num_items,
        tier2_rows: state.tier2_scenario.num_items,
        tier1_row_bytes: TIER1_ROW_BYTES,
        tier2_row_bytes: TIER2_ROW_BYTES,
    };
    axum::Json(info)
}
