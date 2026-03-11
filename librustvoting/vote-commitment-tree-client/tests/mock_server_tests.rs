//! Mock server tests for [`HttpTreeSyncApi`].
//!
//! These validate the full HTTP pipeline:
//! mock HTTP server → HttpTreeSyncApi → TreeClient.sync() → witness generation.

use base64::prelude::*;
use ff::PrimeField;
use pasta_curves::Fp;

use vote_commitment_tree::{TreeClient, TreeSyncApi};
use vote_commitment_tree_client::http_sync_api::HttpTreeSyncApi;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/// Encode an Fp element as a base64 string (matching Go's encoding/json for []byte).
fn fp_to_b64(x: u64) -> String {
    BASE64_STANDARD.encode(Fp::from(x).to_repr())
}

/// Encode raw Fp bytes as base64.
fn fp_bytes_to_b64(fp: Fp) -> String {
    BASE64_STANDARD.encode(fp.to_repr())
}

fn fp(x: u64) -> Fp {
    Fp::from(x)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

/// Basic: HttpTreeSyncApi.get_tree_state() parses a real JSON response.
#[test]
fn get_tree_state_parses_response() {
    let mut server = mockito::Server::new();
    let root_b64 = fp_bytes_to_b64(fp(42));

    let mock = server
        .mock("GET", "/zally/v1/commitment-tree/latest")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":10,"root":"{}","height":5}}}}"#,
            root_b64
        ))
        .create();

    let api = HttpTreeSyncApi::new(server.url());
    let state = api.get_tree_state().unwrap();
    assert_eq!(state.next_index, 10);
    assert_eq!(state.height, 5);
    assert_eq!(state.root, fp(42));
    mock.assert();
}

/// HttpTreeSyncApi.get_root_at_height() with a valid response.
#[test]
fn get_root_at_height_parses_response() {
    let mut server = mockito::Server::new();
    let root_b64 = fp_bytes_to_b64(fp(99));

    let mock = server
        .mock("GET", "/zally/v1/commitment-tree/7")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":3,"root":"{}","height":7}}}}"#,
            root_b64
        ))
        .create();

    let api = HttpTreeSyncApi::new(server.url());
    let root = api.get_root_at_height(7).unwrap();
    assert_eq!(root, Some(fp(99)));
    mock.assert();
}

/// HttpTreeSyncApi.get_root_at_height() with null tree returns None.
#[test]
fn get_root_at_height_null_tree() {
    let mut server = mockito::Server::new();

    let mock = server
        .mock("GET", "/zally/v1/commitment-tree/999")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(r#"{"tree":null}"#)
        .create();

    let api = HttpTreeSyncApi::new(server.url());
    let root = api.get_root_at_height(999).unwrap();
    assert!(root.is_none());
    mock.assert();
}

/// HttpTreeSyncApi.get_block_commitments() parses block data correctly.
#[test]
fn get_block_commitments_parses_response() {
    let mut server = mockito::Server::new();

    let body = format!(
        r#"{{"blocks":[{{"height":5,"start_index":0,"leaves":["{}","{}"]}}]}}"#,
        fp_to_b64(100),
        fp_to_b64(200),
    );

    let mock = server
        .mock(
            "GET",
            "/zally/v1/commitment-tree/leaves?from_height=1&to_height=10",
        )
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(body)
        .create();

    let api = HttpTreeSyncApi::new(server.url());
    let blocks = api.get_block_commitments(1, 10).unwrap();
    assert_eq!(blocks.len(), 1);
    assert_eq!(blocks[0].height, 5);
    assert_eq!(blocks[0].start_index, 0);
    assert_eq!(blocks[0].leaves.len(), 2);
    assert_eq!(blocks[0].leaves[0].inner(), fp(100));
    assert_eq!(blocks[0].leaves[1].inner(), fp(200));
    mock.assert();
}

/// Empty blocks response.
#[test]
fn get_block_commitments_empty() {
    let mut server = mockito::Server::new();

    let mock = server
        .mock(
            "GET",
            "/zally/v1/commitment-tree/leaves?from_height=1&to_height=10",
        )
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(r#"{"blocks":[]}"#)
        .create();

    let api = HttpTreeSyncApi::new(server.url());
    let blocks = api.get_block_commitments(1, 10).unwrap();
    assert!(blocks.is_empty());
    mock.assert();
}

/// Full sync pipeline: mock all three endpoints, create TreeClient, sync, verify.
#[test]
fn full_sync_pipeline() {
    let mut server = mockito::Server::new();

    // The "server" has 2 blocks:
    //   Block 1: leaf fp(10) at index 0
    //   Block 2: leaves fp(20), fp(30) at indices 1, 2
    //
    // We need to serve: get_tree_state, get_block_commitments, and
    // get_root_at_height for blocks 1 and 2.

    // Compute the expected roots using a real TreeServer.
    let mut tree_server = vote_commitment_tree::MemoryTreeServer::empty();
    tree_server.append(fp(10)).unwrap();
    tree_server.checkpoint(1).unwrap();
    let root_at_1 = tree_server.root_at_height(1).unwrap();

    tree_server.append(fp(20)).unwrap();
    tree_server.append(fp(30)).unwrap();
    tree_server.checkpoint(2).unwrap();
    let root_at_2 = tree_server.root_at_height(2).unwrap();

    // Mock: GET /zally/v1/commitment-tree/latest
    let _m_latest = server
        .mock("GET", "/zally/v1/commitment-tree/latest")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":3,"root":"{}","height":2}}}}"#,
            fp_bytes_to_b64(root_at_2),
        ))
        .create();

    // Mock: GET /zally/v1/commitment-tree/leaves?from_height=1&to_height=2
    let _m_leaves = server
        .mock(
            "GET",
            "/zally/v1/commitment-tree/leaves?from_height=1&to_height=2",
        )
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"blocks":[{{"height":1,"start_index":0,"leaves":["{}"]}},{{"height":2,"start_index":1,"leaves":["{}","{}"]}}]}}"#,
            fp_to_b64(10),
            fp_to_b64(20),
            fp_to_b64(30),
        ))
        .create();

    // Mock: GET /zally/v1/commitment-tree/1 (root verification after block 1)
    let _m_root1 = server
        .mock("GET", "/zally/v1/commitment-tree/1")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":1,"root":"{}","height":1}}}}"#,
            fp_bytes_to_b64(root_at_1),
        ))
        .create();

    // Mock: GET /zally/v1/commitment-tree/2 (root verification after block 2)
    let _m_root2 = server
        .mock("GET", "/zally/v1/commitment-tree/2")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":3,"root":"{}","height":2}}}}"#,
            fp_bytes_to_b64(root_at_2),
        ))
        .create();

    // Create client, mark position 0 (for witness generation), sync.
    let api = HttpTreeSyncApi::new(server.url());
    let mut client = TreeClient::empty();
    client.mark_position(0);
    client.mark_position(1);
    client.sync(&api).unwrap();

    // Verify sync results.
    assert_eq!(client.size(), 3);
    assert_eq!(client.last_synced_height(), Some(2));
    assert_eq!(client.root_at_height(1), Some(root_at_1));
    assert_eq!(client.root_at_height(2), Some(root_at_2));
    assert_eq!(client.root(), root_at_2);

    // Generate witness for position 0 at anchor height 2.
    let witness = client.witness(0, 2).unwrap();
    assert!(witness.verify(fp(10), root_at_2));

    // Generate witness for position 1 at anchor height 2.
    let witness1 = client.witness(1, 2).unwrap();
    assert!(witness1.verify(fp(20), root_at_2));
}

/// Incremental sync: sync block 1, then sync block 2 separately.
#[test]
fn incremental_sync() {
    let mut server = mockito::Server::new();

    // Build tree server for expected roots.
    let mut tree_server = vote_commitment_tree::MemoryTreeServer::empty();
    tree_server.append(fp(10)).unwrap();
    tree_server.checkpoint(1).unwrap();
    let root_at_1 = tree_server.root_at_height(1).unwrap();

    // --- First sync: only block 1 ---

    let _m_latest1 = server
        .mock("GET", "/zally/v1/commitment-tree/latest")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":1,"root":"{}","height":1}}}}"#,
            fp_bytes_to_b64(root_at_1),
        ))
        .expect(1)
        .create();

    let _m_leaves1 = server
        .mock(
            "GET",
            "/zally/v1/commitment-tree/leaves?from_height=1&to_height=1",
        )
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"blocks":[{{"height":1,"start_index":0,"leaves":["{}"]}}]}}"#,
            fp_to_b64(10),
        ))
        .expect(1)
        .create();

    let _m_root_h1 = server
        .mock("GET", "/zally/v1/commitment-tree/1")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":1,"root":"{}","height":1}}}}"#,
            fp_bytes_to_b64(root_at_1),
        ))
        .expect(1)
        .create();

    let api = HttpTreeSyncApi::new(server.url());
    let mut client = TreeClient::empty();
    client.mark_position(0);
    client.sync(&api).unwrap();

    assert_eq!(client.size(), 1);
    assert_eq!(client.last_synced_height(), Some(1));

    // Clean up first round mocks.
    _m_latest1.assert();
    _m_leaves1.assert();
    _m_root_h1.assert();

    // --- Second sync: add block 2 ---

    tree_server.append(fp(20)).unwrap();
    tree_server.append(fp(30)).unwrap();
    tree_server.checkpoint(2).unwrap();
    let root_at_2 = tree_server.root_at_height(2).unwrap();

    let _m_latest2 = server
        .mock("GET", "/zally/v1/commitment-tree/latest")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":3,"root":"{}","height":2}}}}"#,
            fp_bytes_to_b64(root_at_2),
        ))
        .expect(1)
        .create();

    // Incremental: only fetch from height 2.
    let _m_leaves2 = server
        .mock(
            "GET",
            "/zally/v1/commitment-tree/leaves?from_height=2&to_height=2",
        )
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"blocks":[{{"height":2,"start_index":1,"leaves":["{}","{}"]}}]}}"#,
            fp_to_b64(20),
            fp_to_b64(30),
        ))
        .expect(1)
        .create();

    let _m_root_h2 = server
        .mock("GET", "/zally/v1/commitment-tree/2")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":3,"root":"{}","height":2}}}}"#,
            fp_bytes_to_b64(root_at_2),
        ))
        .expect(1)
        .create();

    client.mark_position(1);
    client.sync(&api).unwrap();

    assert_eq!(client.size(), 3);
    assert_eq!(client.last_synced_height(), Some(2));
    assert_eq!(client.root(), root_at_2);

    // Witness from first sync still valid at new anchor.
    let w0 = client.witness(0, 2).unwrap();
    assert!(w0.verify(fp(10), root_at_2));

    // Witness from second sync.
    let w1 = client.witness(1, 2).unwrap();
    assert!(w1.verify(fp(20), root_at_2));
}

/// Server returning 500 propagates as an error.
#[test]
fn server_error_propagates() {
    let mut server = mockito::Server::new();

    let _m = server
        .mock("GET", "/zally/v1/commitment-tree/latest")
        .with_status(500)
        .with_body("internal server error")
        .create();

    let api = HttpTreeSyncApi::new(server.url());
    // get_tree_state should fail because the 500 response isn't valid JSON.
    let result = api.get_tree_state();
    assert!(result.is_err());
}

/// Empty tree state (height=0, no leaves) produces an empty sync.
#[test]
fn empty_tree_sync() {
    let mut server = mockito::Server::new();

    let zero_root_b64 = fp_bytes_to_b64(Fp::zero());

    let _m = server
        .mock("GET", "/zally/v1/commitment-tree/latest")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":0,"root":"{}","height":0}}}}"#,
            zero_root_b64
        ))
        .create();

    let api = HttpTreeSyncApi::new(server.url());
    let mut client = TreeClient::empty();
    client.sync(&api).unwrap(); // Should be a no-op.
    assert_eq!(client.size(), 0);
    assert_eq!(client.last_synced_height(), None);
}

/// Witness serialization round-trip through hex (simulating CLI verify flow).
#[test]
fn witness_hex_roundtrip() {
    let mut server = mockito::Server::new();

    let mut tree_server = vote_commitment_tree::MemoryTreeServer::empty();
    tree_server.append(fp(42)).unwrap();
    tree_server.checkpoint(1).unwrap();
    let root = tree_server.root_at_height(1).unwrap();

    let _m_latest = server
        .mock("GET", "/zally/v1/commitment-tree/latest")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":1,"root":"{}","height":1}}}}"#,
            fp_bytes_to_b64(root),
        ))
        .create();

    let _m_leaves = server
        .mock(
            "GET",
            "/zally/v1/commitment-tree/leaves?from_height=1&to_height=1",
        )
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"blocks":[{{"height":1,"start_index":0,"leaves":["{}"]}}]}}"#,
            fp_to_b64(42),
        ))
        .create();

    let _m_root = server
        .mock("GET", "/zally/v1/commitment-tree/1")
        .with_status(200)
        .with_header("content-type", "application/json")
        .with_body(format!(
            r#"{{"tree":{{"next_index":1,"root":"{}","height":1}}}}"#,
            fp_bytes_to_b64(root),
        ))
        .create();

    let api = HttpTreeSyncApi::new(server.url());
    let mut client = TreeClient::empty();
    client.mark_position(0);
    client.sync(&api).unwrap();

    let witness = client.witness(0, 1).unwrap();
    let witness_bytes = witness.to_bytes();
    let witness_hex = hex::encode(&witness_bytes);

    // Round-trip: hex → bytes → MerklePath → verify
    let decoded_bytes = hex::decode(&witness_hex).unwrap();
    let decoded_path = vote_commitment_tree::MerklePath::from_bytes(&decoded_bytes).unwrap();
    assert!(decoded_path.verify(fp(42), root));
}
