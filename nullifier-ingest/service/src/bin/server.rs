use std::env;
use std::net::SocketAddr;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use anyhow::Result;
use axum::extract::{Path as AxumPath, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::routing::get;
use axum::{Json, Router};
use tower_http::cors::{Any, CorsLayer};
use ff::PrimeField as _;
use pasta_curves::Fp;
use serde::Serialize;
use tokio::sync::Mutex;

use imt_tree::NullifierTree;
use nullifier_service::download::connect_lwd;
use nullifier_service::rpc::compact_tx_streamer_client::CompactTxStreamerClient;
use nullifier_service::rpc::BlockId;
use nullifier_service::{file_store, nc_root, tree_db};

// ── JSON response types ─────────────────────────────────────────────────

/// Hex-encode an Fp's little-endian byte representation with a 0x prefix.
fn fp_hex(fp: &Fp) -> String {
    format!("0x{}", hex::encode(fp.to_repr()))
}

#[derive(Serialize)]
struct ImtProofJson {
    root: String,
    low: String,
    high: String,
    leaf_pos: u32,
    path: Vec<String>,
}

#[derive(Serialize)]
struct RootJson {
    root: String,
}

#[derive(Serialize)]
struct HealthJson {
    status: String,
    num_ranges: usize,
    root: String,
}

#[derive(Serialize)]
struct StatusJson {
    latest_height: Option<u64>,
    nullifier_count: u64,
}

#[derive(Serialize)]
struct ErrorJson {
    error: String,
}

// ── Shared state ────────────────────────────────────────────────────────

struct AppState {
    tree: NullifierTree,
    data_dir: PathBuf,
    lwd_url: Option<String>,
    lwd_client: Mutex<Option<CompactTxStreamerClient<tonic::transport::Channel>>>,
}

#[derive(Serialize)]
struct NcRootJson {
    nc_root: String,
    height: u64,
}

// ── Handlers ────────────────────────────────────────────────────────────

async fn exclusion_proof(
    State(state): State<Arc<AppState>>,
    AxumPath(nullifier_hex): AxumPath<String>,
) -> impl IntoResponse {
    // Strip optional 0x prefix
    let hex_str = nullifier_hex.strip_prefix("0x").unwrap_or(&nullifier_hex);

    // Decode hex to 32 bytes
    let bytes = match hex::decode(hex_str) {
        Ok(b) if b.len() == 32 => {
            let mut arr = [0u8; 32];
            arr.copy_from_slice(&b);
            arr
        }
        Ok(b) => {
            return (
                StatusCode::BAD_REQUEST,
                Json(ErrorJson {
                    error: format!("expected 32 bytes (64 hex chars), got {} bytes", b.len()),
                }),
            )
                .into_response();
        }
        Err(e) => {
            return (
                StatusCode::BAD_REQUEST,
                Json(ErrorJson {
                    error: format!("invalid hex: {}", e),
                }),
            )
                .into_response();
        }
    };

    // Parse as Fp
    let fp_opt: Option<Fp> = Fp::from_repr(bytes).into();
    let value = match fp_opt {
        Some(v) => v,
        None => {
            return (
                StatusCode::BAD_REQUEST,
                Json(ErrorJson {
                    error: "value is not a valid field element".into(),
                }),
            )
                .into_response();
        }
    };

    // Generate proof
    match state.tree.prove(value) {
        Some(proof) => {
            let json = ImtProofJson {
                root: fp_hex(&proof.root),
                low: fp_hex(&proof.low),
                high: fp_hex(&proof.high),
                leaf_pos: proof.leaf_pos,
                path: proof.path.iter().map(fp_hex).collect(),
            };
            (StatusCode::OK, Json(json)).into_response()
        }
        None => (
            StatusCode::NOT_FOUND,
            Json(ErrorJson {
                error: "value is an existing nullifier — no exclusion proof possible".into(),
            }),
        )
            .into_response(),
    }
}

async fn root(State(state): State<Arc<AppState>>) -> Json<RootJson> {
    Json(RootJson {
        root: fp_hex(&state.tree.root()),
    })
}

async fn health(State(state): State<Arc<AppState>>) -> Json<HealthJson> {
    Json(HealthJson {
        status: "ok".into(),
        num_ranges: state.tree.len(),
        root: fp_hex(&state.tree.root()),
    })
}

async fn status(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let latest_height = file_store::load_checkpoint(&state.data_dir)
        .ok()
        .flatten()
        .map(|(h, _)| h);

    let nullifier_count = file_store::nullifier_count(&state.data_dir).unwrap_or(0);

    Json(StatusJson {
        latest_height,
        nullifier_count,
    })
}

/// GET /nc-root/:height — fetch the Orchard note commitment tree root at a given block height.
///
/// Connects to lightwalletd, fetches the TreeState, and computes the real Sinsemilla root
/// from the serialized frontier.
async fn nc_root_handler(
    State(state): State<Arc<AppState>>,
    AxumPath(height): AxumPath<u64>,
) -> impl IntoResponse {
    let lwd_url = match &state.lwd_url {
        Some(url) => url.clone(),
        None => {
            return (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(ErrorJson {
                    error: "LWD_URL not configured — cannot fetch tree state".into(),
                }),
            )
                .into_response();
        }
    };

    // Lazily connect to lightwalletd (reuse across requests).
    let mut client_guard = state.lwd_client.lock().await;
    if client_guard.is_none() {
        match connect_lwd(&lwd_url).await {
            Ok(c) => *client_guard = Some(c),
            Err(e) => {
                return (
                    StatusCode::BAD_GATEWAY,
                    Json(ErrorJson {
                        error: format!("failed to connect to lightwalletd: {}", e),
                    }),
                )
                    .into_response();
            }
        }
    }
    let client = client_guard.as_mut().unwrap();

    let tree_state = match client
        .get_tree_state(BlockId {
            height,
            hash: Vec::new(),
        })
        .await
    {
        Ok(resp) => resp.into_inner(),
        Err(e) => {
            // Drop the client on error so the next request reconnects.
            *client_guard = None;
            return (
                StatusCode::BAD_GATEWAY,
                Json(ErrorJson {
                    error: format!("lightwalletd GetTreeState failed: {}", e),
                }),
            )
                .into_response();
        }
    };

    match nc_root::root_from_frontier_hex(&tree_state.orchard_tree) {
        Ok(root_bytes) => {
            let json = NcRootJson {
                nc_root: format!("0x{}", hex::encode(root_bytes)),
                height: tree_state.height,
            };
            (StatusCode::OK, Json(json)).into_response()
        }
        Err(e) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(ErrorJson {
                error: format!("failed to compute nc_root: {}", e),
            }),
        )
            .into_response(),
    }
}

// ── Main ────────────────────────────────────────────────────────────────

#[tokio::main]
async fn main() -> Result<()> {
    let port: u16 = env::var("PORT")
        .unwrap_or_else(|_| "3000".into())
        .parse()
        .expect("PORT must be a valid u16");

    // Load tree: prefer full-tree file > ranges file > flat nullifier data.
    // After a flat-file build, the full tree is saved as a sidecar so
    // subsequent restarts skip all hashing.
    let tree = if let Ok(tree_file) = env::var("TREE_FILE") {
        eprintln!("Loading full tree from file: {}", tree_file);
        NullifierTree::load_full(Path::new(&tree_file))?
    } else if let Ok(tree_path) = env::var("TREE_PATH") {
        eprintln!("Loading tree from ranges file: {}", tree_path);
        NullifierTree::load(Path::new(&tree_path))?
    } else {
        let data_dir = env::var("DATA_DIR").unwrap_or_else(|_| ".".into());
        let dir = Path::new(&data_dir);
        let sidecar = dir.join("nullifiers.tree");

        if sidecar.exists() {
            eprintln!("Loading full tree from sidecar: {}", sidecar.display());
            NullifierTree::load_full(&sidecar)?
        } else {
            eprintln!(
                "Building tree from flat file: {}",
                dir.join("nullifiers.bin").display()
            );
            let tree = tree_db::tree_from_file(dir)?;

            eprintln!("Saving full tree to sidecar: {}", sidecar.display());
            tree.save_full(&sidecar)?;

            tree
        }
    };

    eprintln!(
        "Tree loaded: {} ranges, root = {}",
        tree.len(),
        fp_hex(&tree.root())
    );

    let data_dir = PathBuf::from(env::var("DATA_DIR").unwrap_or_else(|_| ".".into()));
    let lwd_url = env::var("LWD_URL").ok().or_else(|| {
        env::var("LWD_URLS").ok().and_then(|s| {
            s.split(',').next().map(|u| u.trim().to_string())
        })
    });
    if let Some(ref url) = lwd_url {
        eprintln!("Lightwalletd URL for /nc-root: {}", url);
    } else {
        eprintln!("Warning: LWD_URL not set — /nc-root endpoint will be unavailable");
    }

    let state = Arc::new(AppState {
        tree,
        data_dir,
        lwd_url,
        lwd_client: Mutex::new(None),
    });

    let cors = CorsLayer::new()
        .allow_origin(Any)
        .allow_methods(Any)
        .allow_headers(Any);

    let app = Router::new()
        .route("/exclusion-proof/:nullifier", get(exclusion_proof))
        .route("/root", get(root))
        .route("/nc-root/:height", get(nc_root_handler))
        .route("/health", get(health))
        .route("/status", get(status))
        .layer(cors)
        .with_state(state);

    let addr = SocketAddr::from(([0, 0, 0, 0], port));
    eprintln!("Listening on {}", addr);

    axum::Server::bind(&addr)
        .serve(app.into_make_service())
        .await?;

    Ok(())
}
