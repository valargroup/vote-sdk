//! Helper server: receives delegated voting shares from wallets, delays them
//! for temporal unlinkability, generates Merkle witnesses and ZKP #3 (mocked),
//! and submits MsgRevealShare to the vote chain.

use clap::Parser;

use helper_server::api::AppState;
use helper_server::chain_client::ChainClient;
use helper_server::processor;
use helper_server::store::ShareStore;
use helper_server::tree::TreeSync;
use helper_server::types::Config;

#[derive(Parser)]
#[command(name = "helper-server", about = "Zally vote share relay server")]
struct Cli {
    /// Port to listen on.
    #[arg(long, default_value = "9090")]
    port: u16,

    /// Base URL of the chain's REST API (or mock tree dev server).
    #[arg(long, default_value = "http://localhost:8080")]
    tree_node: String,

    /// Base URL for MsgRevealShare submission.
    /// Defaults to --tree-node if not set.
    #[arg(long)]
    chain_submit: Option<String>,

    /// Minimum delay before submitting a share (seconds).
    #[arg(long, default_value = "10")]
    min_delay: u64,

    /// Maximum delay before submitting a share (seconds).
    #[arg(long, default_value = "300")]
    max_delay: u64,

    /// Tree sync interval (seconds).
    #[arg(long, default_value = "5")]
    sync_interval: u64,

    /// Share processing interval (seconds).
    #[arg(long, default_value = "2")]
    process_interval: u64,

    /// Path to the SQLite database file for share persistence.
    #[arg(long, default_value = "helper-server.db")]
    db_path: String,
}

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "helper_server=info".into()),
        )
        .init();

    let cli = Cli::parse();
    let config = Config {
        port: cli.port,
        tree_node_url: cli.tree_node.clone(),
        chain_submit_url: cli.chain_submit.unwrap_or_else(|| cli.tree_node.clone()),
        min_delay_secs: cli.min_delay,
        max_delay_secs: cli.max_delay,
        sync_interval_secs: cli.sync_interval,
        process_interval_secs: cli.process_interval,
        db_path: cli.db_path,
    };

    let store = ShareStore::new(&config);
    let tree_sync = TreeSync::new(config.tree_node_url.clone());
    let chain_client = ChainClient::new(config.chain_submit_url.clone());

    // Initial tree sync.
    tracing::info!(node = %config.tree_node_url, "performing initial tree sync");
    let tree_for_init = tree_sync.clone();
    match tokio::task::spawn_blocking(move || tree_for_init.sync()).await {
        Ok(Ok(())) => {
            tracing::info!(
                height = ?tree_sync.latest_height(),
                size = tree_sync.size(),
                "initial tree sync complete"
            );
        }
        Ok(Err(e)) => {
            tracing::warn!(error = %e, "initial tree sync failed (will retry in background)");
        }
        Err(e) => {
            tracing::error!(error = %e, "initial tree sync task panicked");
        }
    }

    // Spawn background tasks.
    tokio::spawn(tree_sync.clone().run_sync_loop(config.sync_interval_secs));
    tokio::spawn(processor::run_processor(
        store.clone(),
        tree_sync,
        chain_client,
        config.process_interval_secs,
    ));

    // Start HTTP server.
    let app = helper_server::api::router(AppState {
        store: store.clone(),
    });

    let addr = format!("0.0.0.0:{}", config.port);
    tracing::info!(addr = %addr, "helper server listening");
    let listener = tokio::net::TcpListener::bind(&addr).await.unwrap();
    axum::serve(listener, app).await.unwrap();
}
