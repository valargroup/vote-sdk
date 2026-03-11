//! `nf-server serve` — load tier files and start the PIR HTTP server.

use std::sync::atomic::{AtomicU64, AtomicUsize};
use std::sync::Arc;
use std::path::PathBuf;

use anyhow::Result;
use axum::extract::DefaultBodyLimit;
use axum::routing::{get, post};
use axum::Router;
use clap::Args as ClapArgs;
use tokio::sync::RwLock;

use nf_ingest::config;
use nf_ingest::file_store;

use crate::serve::handlers;
use crate::serve::rebuild;
use crate::serve::state::{AppState, ServerPhase};

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
    #[arg(long, env = "SVOTE_CHAIN_URL")]
    chain_url: Option<String>,
}

pub async fn run(args: Args) -> Result<()> {
    tracing_subscriber::fmt::init();

    let lwd_urls = config::resolve_lwd_urls(&args.lwd_url);

    file_store::rebuild_index(&args.data_dir)?;

    eprintln!("Loading tier files from {:?}...", args.pir_data_dir);
    let serving = pir_server::load_serving_state(&args.pir_data_dir)?;

    let state = Arc::new(AppState {
        phase: RwLock::new(ServerPhase::Serving),
        serving: RwLock::new(Some(serving)),
        rebuild_lock: Arc::new(tokio::sync::Mutex::new(())),
        data_dir: args.data_dir.clone(),
        pir_data_dir: args.pir_data_dir.clone(),
        lwd_urls,
        chain_url: args.chain_url,
        next_req_id: AtomicU64::new(0),
        inflight_requests: AtomicUsize::new(0),
    });

    let cors = tower_http::cors::CorsLayer::permissive();

    let app = Router::new()
        .route("/tier0", get(handlers::get_tier0))
        .route("/params/tier1", get(handlers::get_params_tier1))
        .route("/params/tier2", get(handlers::get_params_tier2))
        .route("/hint/tier1", get(handlers::get_hint_tier1))
        .route("/hint/tier2", get(handlers::get_hint_tier2))
        .route("/tier1/query", post(handlers::post_tier1_query))
        .route("/tier2/query", post(handlers::post_tier2_query))
        .route("/tier1/row/:idx", get(handlers::get_tier1_row))
        .route("/tier2/row/:idx", get(handlers::get_tier2_row))
        .route("/root", get(handlers::get_root))
        .route("/snapshot/prepare", post(rebuild::post_snapshot_prepare))
        .route("/snapshot/status", get(rebuild::get_snapshot_status))
        .route("/health", get(handlers::get_health))
        .layer(DefaultBodyLimit::max(512 * 1024 * 1024))
        .layer(cors)
        .with_state(state);

    let addr = format!("0.0.0.0:{}", args.port);
    eprintln!("Listening on {addr}");
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    axum::serve(listener, app).await?;

    Ok(())
}
