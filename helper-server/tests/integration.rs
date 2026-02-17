//! End-to-end integration test: mock tree server + helper server.
//!
//! 1. Start mock tree server on a random port
//! 2. Insert test leaves via admin API (simulate MsgDelegateVote + MsgCastVote)
//! 3. Start helper server pointing at mock tree
//! 4. POST share payloads to helper server
//! 5. Verify shares are queued
//! 6. Verify helper server synced tree and can generate witness
//! 7. Verify MsgRevealShare was submitted (mock chain endpoint)

use axum::body::Body;
use base64::prelude::*;
use ff::PrimeField;
use http_body_util::BodyExt;
use pasta_curves::Fp;
use serde_json::{json, Value};
use tower::util::ServiceExt;

use helper_server::api::AppState;
use helper_server::mock_tree;
use helper_server::store::ShareStore;
use helper_server::tree::TreeSync;
use helper_server::types::Config;

fn fp_to_b64(val: u64) -> String {
    BASE64_STANDARD.encode(Fp::from(val).to_repr())
}

/// Start mock tree server on a random port and return the URL.
async fn start_mock_tree() -> (String, tokio::task::JoinHandle<()>) {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let url = format!("http://127.0.0.1:{}", port);

    let app = mock_tree::router();
    let handle = tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });

    // Give the server a moment to start.
    tokio::time::sleep(std::time::Duration::from_millis(50)).await;

    (url, handle)
}

/// Insert leaves into the mock tree via admin API.
async fn admin_append(mock_url: &str, leaves: &[String]) -> Value {
    let client = reqwest::Client::new();
    let resp = client
        .post(format!("{}/admin/append", mock_url))
        .json(&json!({ "leaves": leaves }))
        .send()
        .await
        .unwrap();
    assert!(resp.status().is_success(), "admin/append failed: {}", resp.status());
    resp.json().await.unwrap()
}

/// Check mock tree status.
async fn admin_status(mock_url: &str) -> Value {
    let client = reqwest::Client::new();
    let resp = client
        .get(format!("{}/admin/status", mock_url))
        .send()
        .await
        .unwrap();
    resp.json().await.unwrap()
}

/// Build a test share payload.
fn test_share_payload(
    share_index: u32,
    tree_position: u64,
    round_id_hex: &str,
) -> Value {
    // Build all 4 encrypted shares for ZKP #3.
    let all_enc_shares: Vec<Value> = (0..4)
        .map(|i| {
            json!({
                "c1": BASE64_STANDARD.encode(Fp::from(100 + i as u64).to_repr()),
                "c2": BASE64_STANDARD.encode(Fp::from(200 + i as u64).to_repr()),
                "share_index": i,
            })
        })
        .collect();

    json!({
        "shares_hash": BASE64_STANDARD.encode([0x42u8; 32]),
        "proposal_id": 0,
        "vote_decision": 1,
        "enc_share": {
            "c1": BASE64_STANDARD.encode(Fp::from(100 + share_index as u64).to_repr()),
            "c2": BASE64_STANDARD.encode(Fp::from(200 + share_index as u64).to_repr()),
            "share_index": share_index,
        },
        "share_index": share_index,
        "tree_position": tree_position,
        "vote_round_id": round_id_hex,
        "all_enc_shares": all_enc_shares,
    })
}

#[tokio::test]
async fn mock_tree_insert_and_query() {
    let (mock_url, _handle) = start_mock_tree().await;

    // Initially empty.
    let status = admin_status(&mock_url).await;
    assert_eq!(status["tree_size"], 0);

    // Insert 2 leaves (simulating MsgDelegateVote → VAN, then MsgCastVote → VAN + VC).
    let van = fp_to_b64(10);
    let vc = fp_to_b64(20);
    let result = admin_append(&mock_url, &[van, vc]).await;
    assert_eq!(result["leaves_added"], 2);
    assert_eq!(result["start_index"], 0);
    assert_eq!(result["height"], 1);

    // Tree state via chain API.
    let client = reqwest::Client::new();
    let resp: Value = client
        .get(format!("{}/zally/v1/commitment-tree/latest", mock_url))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert_eq!(resp["tree"]["next_index"], 2);
    assert_eq!(resp["tree"]["height"], 1);

    // Leaves via chain API.
    let resp: Value = client
        .get(format!(
            "{}/zally/v1/commitment-tree/leaves?from_height=1&to_height=1",
            mock_url
        ))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    let blocks = resp["blocks"].as_array().unwrap();
    assert_eq!(blocks.len(), 1);
    assert_eq!(blocks[0]["leaves"].as_array().unwrap().len(), 2);
}

#[tokio::test]
async fn mock_tree_syncs_with_tree_client() {
    let (mock_url, _handle) = start_mock_tree().await;

    // Insert leaves.
    admin_append(&mock_url, &[fp_to_b64(42), fp_to_b64(43)]).await;

    // Sync via TreeClient.
    let tree = TreeSync::new(mock_url.clone());
    tree.mark_position(0);
    tree.mark_position(1);

    let tree_clone = tree.clone();
    tokio::task::spawn_blocking(move || tree_clone.sync())
        .await
        .unwrap()
        .unwrap();

    assert_eq!(tree.size(), 2);
    assert_eq!(tree.latest_height(), Some(1));

    // Witness generation.
    let w0 = tree.witness(0, 1).unwrap();
    assert!(w0.verify(Fp::from(42), tree.root()));
    let w1 = tree.witness(1, 1).unwrap();
    assert!(w1.verify(Fp::from(43), tree.root()));
}

#[tokio::test]
async fn share_intake_and_queue() {
    let config = Config {
        min_delay_secs: 0,
        max_delay_secs: 0,
        ..Config::default()
    };
    let store = ShareStore::new(&config);
    let tree = TreeSync::new("http://localhost:0".to_string());
    let app = helper_server::api::router(AppState {
        store: store.clone(),
        tree,
    });

    let round_id = hex::encode([0x0A; 32]);

    // Submit 4 shares.
    for i in 0..4u32 {
        let payload = test_share_payload(i, 1, &round_id);
        let resp = app
            .clone()
            .oneshot(
                axum::http::Request::builder()
                    .method("POST")
                    .uri("/api/v1/shares")
                    .header("content-type", "application/json")
                    .body(Body::from(serde_json::to_vec(&payload).unwrap()))
                    .unwrap(),
            )
            .await
            .unwrap();

        assert_eq!(resp.status(), 200, "share {} should be accepted", i);
        let body = resp.into_body().collect().await.unwrap().to_bytes();
        let json: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(json["status"], "queued");
    }

    // Check status endpoint.
    let resp = app
        .clone()
        .oneshot(
            axum::http::Request::builder()
                .method("GET")
                .uri("/api/v1/status")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    let body = resp.into_body().collect().await.unwrap().to_bytes();
    let json: Value = serde_json::from_slice(&body).unwrap();
    assert_eq!(json["status"], "ok");
    assert_eq!(json["queues"][&round_id]["total"], 4);
}

/// Helper: POST a raw JSON payload and return the response status.
async fn post_share(app: axum::Router, payload: &Value) -> u16 {
    let resp = app
        .oneshot(
            axum::http::Request::builder()
                .method("POST")
                .uri("/api/v1/shares")
                .header("content-type", "application/json")
                .body(Body::from(serde_json::to_vec(payload).unwrap()))
                .unwrap(),
        )
        .await
        .unwrap();
    resp.status().as_u16()
}

/// Valid payload template for validation tests.
fn valid_payload() -> Value {
    let all_enc_shares: Vec<Value> = (0..4)
        .map(|i| {
            json!({
                "c1": BASE64_STANDARD.encode(Fp::from(i as u64).to_repr()),
                "c2": BASE64_STANDARD.encode(Fp::from(i as u64 + 10).to_repr()),
                "share_index": i,
            })
        })
        .collect();

    // enc_share must match all_enc_shares[0] exactly.
    json!({
        "shares_hash": BASE64_STANDARD.encode([0u8; 32]),
        "proposal_id": 0,
        "vote_decision": 1,
        "enc_share": {
            "c1": BASE64_STANDARD.encode(Fp::from(0u64).to_repr()),
            "c2": BASE64_STANDARD.encode(Fp::from(10u64).to_repr()),
            "share_index": 0,
        },
        "share_index": 0,
        "tree_position": 0,
        "vote_round_id": hex::encode([0u8; 32]),
        "all_enc_shares": all_enc_shares,
    })
}

fn make_app() -> axum::Router {
    let config = Config::default();
    let store = ShareStore::new(&config);
    let tree = TreeSync::new("http://localhost:0".to_string());
    helper_server::api::router(AppState { store, tree })
}

#[tokio::test]
async fn validation_rejects_bad_base64() {
    let mut p = valid_payload();
    p["shares_hash"] = json!("not-valid-base64!!!");
    assert_eq!(post_share(make_app(), &p).await, 400);
}

#[tokio::test]
async fn validation_rejects_wrong_field_size() {
    // shares_hash: valid base64 but only 16 bytes instead of 32.
    let mut p = valid_payload();
    p["shares_hash"] = json!(BASE64_STANDARD.encode([0u8; 16]));
    assert_eq!(post_share(make_app(), &p).await, 400);

    // enc_share.c1: 64 bytes instead of 32.
    let mut p = valid_payload();
    p["enc_share"]["c1"] = json!(BASE64_STANDARD.encode([0u8; 64]));
    assert_eq!(post_share(make_app(), &p).await, 400);
}

#[tokio::test]
async fn validation_rejects_bad_share_index() {
    let mut p = valid_payload();
    p["enc_share"]["share_index"] = json!(4);
    assert_eq!(post_share(make_app(), &p).await, 400);
}

#[tokio::test]
async fn validation_rejects_bad_round_id() {
    // Non-hex.
    let mut p = valid_payload();
    p["vote_round_id"] = json!("not-hex!");
    assert_eq!(post_share(make_app(), &p).await, 400);

    // Wrong length (16 bytes hex instead of 32).
    let mut p = valid_payload();
    p["vote_round_id"] = json!(hex::encode([0u8; 16]));
    assert_eq!(post_share(make_app(), &p).await, 400);
}

#[tokio::test]
async fn validation_rejects_bad_all_enc_shares() {
    // Missing all_enc_shares (wrong count).
    let mut p = valid_payload();
    p["all_enc_shares"] = json!([]);
    assert_eq!(post_share(make_app(), &p).await, 400);

    // Only 3 entries instead of 4.
    let mut p = valid_payload();
    let arr = p["all_enc_shares"].as_array().unwrap().clone();
    p["all_enc_shares"] = json!(arr[..3]);
    assert_eq!(post_share(make_app(), &p).await, 400);

    // Wrong share_index ordering.
    let mut p = valid_payload();
    p["all_enc_shares"][0]["share_index"] = json!(2);
    assert_eq!(post_share(make_app(), &p).await, 400);
}

#[tokio::test]
async fn validation_rejects_enc_share_mismatch() {
    // enc_share.c1 differs from all_enc_shares[0].c1.
    let mut p = valid_payload();
    p["enc_share"]["c1"] = json!(BASE64_STANDARD.encode(Fp::from(9999u64).to_repr()));
    assert_eq!(post_share(make_app(), &p).await, 400);

    // enc_share.c2 differs from all_enc_shares[0].c2.
    let mut p = valid_payload();
    p["enc_share"]["c2"] = json!(BASE64_STANDARD.encode(Fp::from(9999u64).to_repr()));
    assert_eq!(post_share(make_app(), &p).await, 400);
}

#[tokio::test]
async fn validation_accepts_valid_payload() {
    assert_eq!(post_share(make_app(), &valid_payload()).await, 200);
}

#[tokio::test]
async fn end_to_end_share_processing() {
    // 1. Start mock tree server.
    let (mock_url, _handle) = start_mock_tree().await;

    // 2. Insert test leaves: VAN at position 0, VC at position 1.
    // The VC is what the share's tree_position points to.
    let van_leaf = fp_to_b64(10);
    let vc_leaf = fp_to_b64(20);
    admin_append(&mock_url, &[van_leaf, vc_leaf]).await;

    // 3. Set up helper server components with zero delay.
    let config = Config {
        min_delay_secs: 0,
        max_delay_secs: 0,
        tree_node_url: mock_url.clone(),
        chain_submit_url: mock_url.clone(),
        ..Config::default()
    };

    let store = ShareStore::new(&config);
    let tree = TreeSync::new(mock_url.clone());

    // Mark the VC position before syncing.
    tree.mark_position(1);

    // Sync the tree.
    let tree_clone = tree.clone();
    tokio::task::spawn_blocking(move || tree_clone.sync())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(tree.size(), 2);

    // 4. Enqueue a share.
    let round_id = hex::encode([0x0A; 32]);
    let payload: helper_server::types::SharePayload = serde_json::from_value(
        test_share_payload(0, 1, &round_id),
    )
    .unwrap();
    store.enqueue(payload);

    // 5. Verify share is queued.
    let status = store.status();
    assert_eq!(status[&round_id].total, 1);
    assert_eq!(status[&round_id].pending, 1);

    // 6. Take ready shares (delay is 0, so immediately ready).
    let ready = store.take_ready();
    assert_eq!(ready.len(), 1);

    // 7. Verify tree can generate witness for the VC position.
    let anchor = tree.latest_height().unwrap();
    let witness = tree.witness(1, anchor).unwrap();
    assert!(witness.verify(Fp::from(20), tree.root()));

    // 8. Submit MsgRevealShare to mock endpoint.
    let chain = helper_server::chain_client::ChainClient::new(mock_url.clone());
    let msg = helper_server::types::MsgRevealShareJson {
        share_nullifier: BASE64_STANDARD.encode(Fp::from(999).to_repr()),
        enc_share: BASE64_STANDARD.encode([0u8; 64]),
        proposal_id: 0,
        vote_decision: 1,
        proof: BASE64_STANDARD.encode([0u8; 192]),
        vote_round_id: BASE64_STANDARD.encode([0x0A; 32]),
        vote_comm_tree_anchor_height: anchor as u64,
    };
    let result = chain.submit_reveal_share(&msg).await.unwrap();
    assert_eq!(result.code, 0);
    assert!(result.tx_hash.starts_with("mock_"));
}

// ---------------------------------------------------------------------------
// Persistence / crash recovery tests
// ---------------------------------------------------------------------------

/// Helper to create a test SharePayload value.
fn make_test_payload(share_index: u32, round_id: &str) -> helper_server::types::SharePayload {
    serde_json::from_value(test_share_payload(share_index, 1, round_id)).unwrap()
}

#[tokio::test]
async fn crash_recovery_requeues_shares() {
    // Use a temp file so we can reopen it.
    let tmp = tempfile::NamedTempFile::new().unwrap();
    let db_path = tmp.path().to_str().unwrap().to_string();

    let round_id = hex::encode([0xBB; 32]);

    // Phase 1: enqueue shares, then drop the store (simulates crash).
    {
        let config = Config {
            min_delay_secs: 0,
            max_delay_secs: 0,
            db_path: db_path.clone(),
            ..Config::default()
        };
        let store = ShareStore::new(&config);
        store.enqueue(make_test_payload(0, &round_id));
        store.enqueue(make_test_payload(1, &round_id));

        let status = store.status();
        assert_eq!(status[&round_id].total, 2);
        // Drop store — simulates server crash.
    }

    // Phase 2: reopen from the same DB — shares should be recovered.
    {
        let config = Config {
            min_delay_secs: 0,
            max_delay_secs: 0,
            db_path: db_path.clone(),
            ..Config::default()
        };
        let store = ShareStore::new(&config);

        let status = store.status();
        assert_eq!(status[&round_id].total, 2);
        assert_eq!(status[&round_id].pending, 2);

        // Recovered shares should be immediately ready (delay=0).
        let ready = store.take_ready();
        assert_eq!(ready.len(), 2);
    }
}

#[tokio::test]
async fn witnessed_shares_reset_on_recovery() {
    let tmp = tempfile::NamedTempFile::new().unwrap();
    let db_path = tmp.path().to_str().unwrap().to_string();

    let round_id = hex::encode([0xCC; 32]);

    // Phase 1: enqueue, take_ready (→Witnessed), then "crash".
    {
        let config = Config {
            min_delay_secs: 0,
            max_delay_secs: 0,
            db_path: db_path.clone(),
            ..Config::default()
        };
        let store = ShareStore::new(&config);
        store.enqueue(make_test_payload(0, &round_id));

        let ready = store.take_ready();
        assert_eq!(ready.len(), 1);
        assert_eq!(ready[0].state, helper_server::types::ShareState::Witnessed);

        // Don't mark submitted — simulate crash mid-processing.
    }

    // Phase 2: recover — witnessed share should be back to Received.
    {
        let config = Config {
            min_delay_secs: 0,
            max_delay_secs: 0,
            db_path: db_path.clone(),
            ..Config::default()
        };
        let store = ShareStore::new(&config);

        let status = store.status();
        assert_eq!(status[&round_id].pending, 1);

        // Should be able to take_ready again.
        let ready = store.take_ready();
        assert_eq!(ready.len(), 1);
    }
}

#[tokio::test]
async fn submitted_shares_not_recovered() {
    let tmp = tempfile::NamedTempFile::new().unwrap();
    let db_path = tmp.path().to_str().unwrap().to_string();

    let round_id = hex::encode([0xDD; 32]);

    // Phase 1: enqueue, process through to Submitted.
    {
        let config = Config {
            min_delay_secs: 0,
            max_delay_secs: 0,
            db_path: db_path.clone(),
            ..Config::default()
        };
        let store = ShareStore::new(&config);
        store.enqueue(make_test_payload(0, &round_id));

        let ready = store.take_ready();
        assert_eq!(ready.len(), 1);

        store.mark_submitted(&round_id, 0);

        let status = store.status();
        assert_eq!(status[&round_id].submitted, 1);
    }

    // Phase 2: recover — submitted share should not be in-flight.
    {
        let config = Config {
            min_delay_secs: 0,
            max_delay_secs: 0,
            db_path: db_path.clone(),
            ..Config::default()
        };
        let store = ShareStore::new(&config);

        // No in-flight shares to take.
        let ready = store.take_ready();
        assert_eq!(ready.len(), 0);

        // Status still shows the submitted share.
        let status = store.status();
        assert_eq!(status[&round_id].submitted, 1);
        assert_eq!(status[&round_id].pending, 0);
    }
}
