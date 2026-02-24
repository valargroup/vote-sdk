use std::path::PathBuf;
use std::sync::Arc;
use std::time::Instant;

use anyhow::Result;
use axum::body::Bytes;
use axum::extract::{DefaultBodyLimit, Path, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::Router;
use tokio::sync::Mutex;

use pir_export::PirMetadata;
use pir_server::{
    HealthInfo, RootInfo, TierServer, YpirScenario, TIER1_ROW_BYTES, TIER1_ROWS, TIER2_ROW_BYTES,
    TIER2_ROWS,
};

struct AppState {
    tier0_data: Vec<u8>,
    tier1_data: &'static [u8],
    tier2_data: &'static [u8],
    tier1: Mutex<TierServer<'static>>,
    tier2: Mutex<TierServer<'static>>,
    tier1_scenario: YpirScenario,
    tier2_scenario: YpirScenario,
    tier1_hint: Vec<u8>,
    tier2_hint: Vec<u8>,
    metadata: PirMetadata,
}

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt::init();

    let data_dir = std::env::args()
        .nth(1)
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("./pir-data"));
    let port: u16 = std::env::args()
        .nth(2)
        .and_then(|s| s.parse().ok())
        .unwrap_or(3001);

    let t_total = Instant::now();

    // Load tier files
    eprintln!("Loading tier files from {:?}...", data_dir);

    let tier0_data = std::fs::read(data_dir.join("tier0.bin"))?;
    eprintln!("  Tier 0: {} bytes", tier0_data.len());

    let tier1_data = std::fs::read(data_dir.join("tier1.bin"))?;
    eprintln!("  Tier 1: {} bytes ({} rows)", tier1_data.len(), tier1_data.len() / TIER1_ROW_BYTES);
    anyhow::ensure!(tier1_data.len() == TIER1_ROWS * TIER1_ROW_BYTES,
        "tier1.bin size mismatch: got {} bytes, expected {}", tier1_data.len(), TIER1_ROWS * TIER1_ROW_BYTES);

    let tier2_data = std::fs::read(data_dir.join("tier2.bin"))?;
    eprintln!("  Tier 2: {} bytes ({} rows)", tier2_data.len(), tier2_data.len() / TIER2_ROW_BYTES);
    anyhow::ensure!(tier2_data.len() == TIER2_ROWS * TIER2_ROW_BYTES,
        "tier2.bin size mismatch: got {} bytes, expected {}", tier2_data.len(), TIER2_ROWS * TIER2_ROW_BYTES);

    let metadata: PirMetadata =
        serde_json::from_str(&std::fs::read_to_string(data_dir.join("pir_root.json"))?)?;
    eprintln!(
        "  Metadata: {} ranges, root29={}...",
        metadata.num_ranges,
        metadata.root29.get(..16).unwrap_or(&metadata.root29)
    );

    // Initialize YPIR servers
    eprintln!("Initializing YPIR servers...");

    let tier1_scenario = pir_server::tier1_scenario();
    // Leak the data so TierServer can have a 'static lifetime.
    // These are massive allocations that live for the entire process anyway.
    let tier1_data_static: &'static [u8] = Box::leak(tier1_data.into_boxed_slice());
    let tier1_server = TierServer::new(tier1_data_static, tier1_scenario.clone());
    let tier1_hint = tier1_server.hint_bytes();
    eprintln!("  Tier 1 YPIR ready (hint: {} bytes)", tier1_hint.len());

    let tier2_scenario = pir_server::tier2_scenario();
    let tier2_data_static: &'static [u8] = Box::leak(tier2_data.into_boxed_slice());
    let tier2_server = TierServer::new(tier2_data_static, tier2_scenario.clone());
    let tier2_hint = tier2_server.hint_bytes();
    eprintln!("  Tier 2 YPIR ready (hint: {} bytes)", tier2_hint.len());

    eprintln!(
        "Server ready in {:.1}s",
        t_total.elapsed().as_secs_f64()
    );

    let state = Arc::new(AppState {
        tier0_data,
        tier1_data: tier1_data_static,
        tier2_data: tier2_data_static,
        tier1: Mutex::new(tier1_server),
        tier2: Mutex::new(tier2_server),
        tier1_scenario,
        tier2_scenario,
        tier1_hint,
        tier2_hint,
        metadata,
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

async fn post_tier1_query(
    State(state): State<Arc<AppState>>,
    body: Bytes,
) -> impl IntoResponse {
    let t0 = Instant::now();
    eprintln!("Tier 1 query: received {} bytes", body.len());
    let mut server = state.tier1.lock().await;
    match server.answer_query(&body) {
        Ok(response) => {
            eprintln!("Tier 1 query: answered in {:.1}ms, response {} bytes",
                t0.elapsed().as_secs_f64() * 1000.0, response.len());
            (
                StatusCode::OK,
                [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
                response,
            ).into_response()
        }
        Err(e) => {
            eprintln!("Tier 1 query: malformed request: {e}");
            (StatusCode::BAD_REQUEST, e.to_string()).into_response()
        }
    }
}

async fn post_tier2_query(
    State(state): State<Arc<AppState>>,
    body: Bytes,
) -> impl IntoResponse {
    let t0 = Instant::now();
    eprintln!("Tier 2 query: received {} bytes", body.len());
    let mut server = state.tier2.lock().await;
    match server.answer_query(&body) {
        Ok(response) => {
            eprintln!("Tier 2 query: answered in {:.1}ms, response {} bytes",
                t0.elapsed().as_secs_f64() * 1000.0, response.len());
            (
                StatusCode::OK,
                [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
                response,
            ).into_response()
        }
        Err(e) => {
            eprintln!("Tier 2 query: malformed request: {e}");
            (StatusCode::BAD_REQUEST, e.to_string()).into_response()
        }
    }
}

async fn get_tier1_row(
    State(state): State<Arc<AppState>>,
    Path(idx): Path<usize>,
) -> impl IntoResponse {
    if idx >= TIER1_ROWS {
        return (StatusCode::NOT_FOUND, "row index out of range").into_response();
    }
    let offset = idx * TIER1_ROW_BYTES;
    let row = &state.tier1_data[offset..offset + TIER1_ROW_BYTES];
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
    if idx >= TIER2_ROWS {
        return (StatusCode::NOT_FOUND, "row index out of range").into_response();
    }
    let offset = idx * TIER2_ROW_BYTES;
    let row = &state.tier2_data[offset..offset + TIER2_ROW_BYTES];
    (
        [(axum::http::header::CONTENT_TYPE, "application/octet-stream")],
        row.to_vec(),
    )
        .into_response()
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
