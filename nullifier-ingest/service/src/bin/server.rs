use std::env;
use std::net::SocketAddr;
use std::path::Path;
use std::sync::Arc;

use anyhow::Result;
use axum::extract::{Path as AxumPath, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::routing::get;
use axum::{Json, Router};
use ff::PrimeField as _;
use pasta_curves::Fp;
use rusqlite::Connection;
use serde::Serialize;

use imt_tree::NullifierTree;
use nullifier_service::tree_db;

// ── JSON response types ─────────────────────────────────────────────────

/// Hex-encode an Fp's little-endian byte representation with a 0x prefix.
fn fp_hex(fp: &Fp) -> String {
    format!("0x{}", hex::encode(fp.to_repr()))
}

#[derive(Serialize)]
struct RangeJson {
    low: String,
    high: String,
}

#[derive(Serialize)]
struct ExclusionProofJson {
    range: RangeJson,
    position: u32,
    leaf: String,
    auth_path: Vec<String>,
    root: String,
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
struct ErrorJson {
    error: String,
}

// ── Shared state ────────────────────────────────────────────────────────

struct AppState {
    tree: NullifierTree,
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
            let [low, high] = proof.range;
            let json = ExclusionProofJson {
                range: RangeJson {
                    low: fp_hex(&low),
                    high: fp_hex(&high),
                },
                position: proof.position,
                leaf: fp_hex(&proof.leaf),
                auth_path: proof.auth_path.iter().map(fp_hex).collect(),
                root: fp_hex(&state.tree.root()),
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

// ── Main ────────────────────────────────────────────────────────────────

#[tokio::main]
async fn main() -> Result<()> {
    let port: u16 = env::var("PORT")
        .unwrap_or_else(|_| "3000".into())
        .parse()
        .expect("PORT must be a valid u16");

    // Load tree from binary file or SQLite database
    let tree = if let Ok(tree_path) = env::var("TREE_PATH") {
        eprintln!("Loading tree from file: {}", tree_path);
        NullifierTree::load(Path::new(&tree_path))?
    } else {
        let db_path = env::var("DB_PATH").unwrap_or_else(|_| "nullifiers.db".into());
        eprintln!("Loading tree from database: {}", db_path);
        let connection = Connection::open(&db_path)?;
        tree_db::tree_from_db(&connection)?
    };

    eprintln!(
        "Tree loaded: {} ranges, root = {}",
        tree.len(),
        fp_hex(&tree.root())
    );

    let state = Arc::new(AppState { tree });

    let app = Router::new()
        .route("/exclusion-proof/:nullifier", get(exclusion_proof))
        .route("/root", get(root))
        .route("/health", get(health))
        .with_state(state);

    let addr = SocketAddr::from(([0, 0, 0, 0], port));
    eprintln!("Listening on {}", addr);

    axum::Server::bind(&addr)
        .serve(app.into_make_service())
        .await?;

    Ok(())
}
