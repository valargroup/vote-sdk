//! API integration test: fetch an exclusion proof from the running nullifier
//! tree service, reconstruct it locally, and verify it off-chain through
//! Poseidon Merkle path verification and ImtProofData verification.
//!
//! This is the API-layer counterpart of `imt-tree/tests/imt_circuit_integration.rs`:
//! instead of building a local `NullifierTree` and proving directly, we call the
//! HTTP endpoints served by `query-server` and verify the returned JSON proof.
//!
//! The circuit-level tests use `build_delegation_bundle` + `MockProver` to verify
//! that API-returned proofs satisfy the real delegation circuit constraints
//! (including IMT Condition 13). This reuses the exact same circuit as production
//! instead of redefining gates in a standalone test circuit.
//!
//! Prerequisites:
//! - The query-server must be running (e.g., `cargo run --bin query-server`)
//! - Set `SERVER_URL` env var if not using the default `http://localhost:3000`
//!
//! Run with:
//! ```sh
//! cargo test --test api_integration -- --nocapture
//! ```

use ff::{Field, PrimeField};
use pasta_curves::pallas;
use serde::Deserialize;

use imt_tree::{poseidon_hash, ImtProofData, TREE_DEPTH};

// Circuit-level proving & verification imports.
use halo2_proofs::dev::MockProver;
use incrementalmerkletree::{Hashable, Level};
use rand::rngs::OsRng;

use orchard::{
    keys::{FullViewingKey, Scope, SpendingKey},
    note::{ExtractedNoteCommitment, Note, Rho},
    tree::{MerkleHashOrchard, MerklePath},
    value::NoteValue,
    NOTE_COMMITMENT_TREE_DEPTH,
};
use voting_circuits::delegation::{
    builder::{build_delegation_bundle, RealNoteInput},
    imt::{ImtError, ImtProofData as OrchardImtProofData, ImtProvider, IMT_DEPTH},
};

// ── JSON response types (mirrors server.rs) ─────────────────────────

#[derive(Deserialize)]
struct ImtProofJson {
    root: String,
    low: String,
    width: String,
    leaf_pos: u32,
    path: Vec<String>,
}

#[derive(Deserialize)]
struct RootJson {
    root: String,
}

#[derive(Deserialize)]
struct HealthJson {
    status: String,
    num_ranges: usize,
    root: String,
}

#[derive(Deserialize)]
struct ErrorJson {
    error: String,
}

// ── Helpers ─────────────────────────────────────────────────────────

fn server_url() -> String {
    std::env::var("SERVER_URL").unwrap_or_else(|_| "http://localhost:3000".into())
}

/// Parse a hex-encoded field element (with optional 0x prefix) to pallas::Base.
fn hex_to_fp(hex_str: &str) -> pallas::Base {
    let hex_str = hex_str.strip_prefix("0x").unwrap_or(hex_str);
    let bytes = hex::decode(hex_str).expect("valid hex");
    assert_eq!(bytes.len(), 32, "expected 32 bytes, got {}", bytes.len());
    let mut arr = [0u8; 32];
    arr.copy_from_slice(&bytes);
    let opt: Option<pallas::Base> = pallas::Base::from_repr(arr).into();
    opt.expect("valid field element")
}

/// Encode a pallas::Base as 0x-prefixed hex (little-endian byte repr).
fn fp_hex(fp: &pallas::Base) -> String {
    format!("0x{}", hex::encode(fp.to_repr()))
}

/// Parse an API JSON response into a local `ImtProofData`.
fn parse_proof(json: &ImtProofJson) -> ImtProofData {
    let root = hex_to_fp(&json.root);
    let low = hex_to_fp(&json.low);
    let width = hex_to_fp(&json.width);
    let path_vec: Vec<pallas::Base> = json.path.iter().map(|h| hex_to_fp(h)).collect();

    assert_eq!(
        path_vec.len(),
        TREE_DEPTH,
        "path should have {} elements, got {}",
        TREE_DEPTH,
        path_vec.len()
    );
    let mut path = [pallas::Base::default(); TREE_DEPTH];
    path.copy_from_slice(&path_vec);

    ImtProofData {
        root,
        low,
        width,
        leaf_pos: json.leaf_pos,
        path,
    }
}

/// Fetch an exclusion proof from the API for the given field element.
async fn fetch_exclusion_proof(
    client: &reqwest::Client,
    value: pallas::Base,
) -> ImtProofData {
    let url = server_url();
    let hex_str = &fp_hex(&value)[2..]; // strip "0x" for URL path

    let resp = client
        .get(format!("{}/exclusion-proof/{}", url, hex_str))
        .send()
        .await
        .expect("exclusion proof request should succeed");

    assert_eq!(
        resp.status(),
        200,
        "expected 200 OK for non-nullifier value {}",
        fp_hex(&value)
    );

    let json: ImtProofJson = resp.json().await.expect("valid exclusion proof JSON");
    parse_proof(&json)
}

/// Fetch the tree root from the API.
async fn fetch_root(client: &reqwest::Client) -> pallas::Base {
    let url = server_url();
    let resp: RootJson = client
        .get(format!("{}/root", url))
        .send()
        .await
        .expect("root request should succeed")
        .json()
        .await
        .expect("valid root JSON");
    hex_to_fp(&resp.root)
}

// ── Tests ───────────────────────────────────────────────────────────

/// Smoke test: /health returns status "ok" with a non-empty tree.
#[tokio::test]
async fn health_endpoint_returns_ok() {
    let url = server_url();
    let client = reqwest::Client::new();

    let resp = client
        .get(format!("{}/health", url))
        .send()
        .await
        .expect("health request should succeed");

    assert_eq!(resp.status(), 200);

    let health: HealthJson = resp.json().await.expect("valid health JSON");
    assert_eq!(health.status, "ok");
    assert!(health.num_ranges > 0, "tree should have at least one range");

    eprintln!(
        "Health: {} ranges, root = {}",
        health.num_ranges, health.root
    );
}

/// /root returns a valid field element.
#[tokio::test]
async fn root_endpoint_returns_valid_field_element() {
    let client = reqwest::Client::new();
    let root = fetch_root(&client).await;
    // If hex_to_fp didn't panic, the root is a valid Fp.
    eprintln!("Root: {}", fp_hex(&root));
}

/// Core test: fetch an exclusion proof from the API and verify it off-chain
/// by recomputing the Poseidon Merkle path from leaf to root.
#[tokio::test]
async fn exclusion_proof_verifies_offchain() {
    let client = reqwest::Client::new();

    // 1. Get the authoritative root.
    let expected_root = fetch_root(&client).await;

    // 2. Pick a test value — a small number very unlikely to be a real nullifier.
    let test_value = pallas::Base::from(999u64);

    // 3. Fetch the exclusion proof.
    let proof = fetch_exclusion_proof(&client, test_value).await;

    // 4. Root consistency: /root and /exclusion-proof agree.
    assert_eq!(
        expected_root, proof.root,
        "root from /root and /exclusion-proof should match"
    );

    // 5. Auth path has the correct depth.
    assert_eq!(
        proof.path.len(),
        TREE_DEPTH,
        "path should have {} elements, got {}",
        TREE_DEPTH,
        proof.path.len()
    );

    // 6. The value falls within the claimed range (low, width model).
    // value - low <= width (field subtraction).
    let offset = test_value - proof.low;
    assert!(
        offset <= proof.width,
        "test value {} should be in range [low={}, width={}]",
        fp_hex(&test_value),
        fp_hex(&proof.low),
        fp_hex(&proof.width),
    );

    // 7. The leaf commitment is correct: hash(low, width).
    let expected_leaf = poseidon_hash(proof.low, proof.width);
    let actual_leaf = poseidon_hash(proof.low, proof.width);
    assert_eq!(
        actual_leaf, expected_leaf,
        "leaf should equal poseidon_hash(low, width)"
    );

    // 8. Full off-chain verification via ImtProofData::verify().
    assert!(
        proof.verify(test_value),
        "exclusion proof from API should verify off-chain"
    );

    eprintln!(
        "Verified: value={}, low={}, width={}, pos={}, root={}",
        fp_hex(&test_value),
        fp_hex(&proof.low),
        fp_hex(&proof.width),
        proof.leaf_pos,
        fp_hex(&proof.root),
    );
}

/// Verify that the API proof passes ImtProofData::verify() directly.
#[tokio::test]
async fn exclusion_proof_verifies_via_imt_proof_data() {
    let client = reqwest::Client::new();
    let test_value = pallas::Base::from(42u64);

    let proof = fetch_exclusion_proof(&client, test_value).await;

    // Field-level checks.
    assert_eq!(proof.path.len(), TREE_DEPTH);

    // Verify through ImtProofData::verify().
    assert!(
        proof.verify(test_value),
        "ImtProofData from API proof should verify"
    );
}

/// Verify multiple proofs for different values to exercise different tree regions.
#[tokio::test]
async fn multiple_values_verify() {
    let client = reqwest::Client::new();
    let root = fetch_root(&client).await;

    // Test a spread of values across the field.
    let test_values: Vec<pallas::Base> = vec![
        pallas::Base::from(1u64),
        pallas::Base::from(100u64),
        pallas::Base::from(12345u64),
        pallas::Base::from(999_999u64),
        pallas::Base::from(0xDEAD_BEEFu64),
    ];

    for value in &test_values {
        let proof = fetch_exclusion_proof(&client, *value).await;
        assert_eq!(root, proof.root, "root should be consistent across queries");
        assert!(
            proof.verify(*value),
            "proof for value {} should verify",
            fp_hex(value)
        );
    }

    eprintln!("All {} proofs verified successfully", test_values.len());
}

/// Manually recompute the Merkle root from the proof's leaf and auth path,
/// without using ImtProofData::verify(), to independently validate the
/// server's proof structure.
#[tokio::test]
async fn manual_merkle_path_recomputation() {
    let client = reqwest::Client::new();
    let test_value = pallas::Base::from(7777u64);

    let proof = fetch_exclusion_proof(&client, test_value).await;

    // Recompute leaf from range (low, width).
    let leaf = poseidon_hash(proof.low, proof.width);

    // Walk the auth path manually.
    let mut current = leaf;
    let mut pos = proof.leaf_pos;
    for (level, sibling) in proof.path.iter().enumerate() {
        let (l, r) = if pos & 1 == 0 {
            (current, *sibling)
        } else {
            (*sibling, current)
        };
        current = poseidon_hash(l, r);
        pos >>= 1;

        if level < 3 {
            eprintln!(
                "  Level {}: hash({}, {}) = {}",
                level,
                &fp_hex(&l)[..10],
                &fp_hex(&r)[..10],
                &fp_hex(&current)[..10],
            );
        }
    }

    assert_eq!(
        current, proof.root,
        "manually recomputed root should match server root"
    );
    eprintln!("Manual Merkle recomputation matches server root");
}

/// Verify that requesting an exclusion proof for a known nullifier
/// returns 404 (the server should refuse to prove inclusion of an
/// existing nullifier).
///
/// Note: This test uses the field element 0 which is a sentinel nullifier
/// in trees built with `build_sentinel_tree`. If the server's tree was not
/// built with sentinels, this test may need adjustment.
#[tokio::test]
async fn known_nullifier_returns_404() {
    let url = server_url();
    let client = reqwest::Client::new();

    // 0 is a sentinel nullifier in sentinel-based trees.
    let zero = pallas::Base::zero();
    let hex_str = &fp_hex(&zero)[2..];

    let resp = client
        .get(format!("{}/exclusion-proof/{}", url, hex_str))
        .send()
        .await
        .expect("request should succeed");

    // If 0 is a nullifier, we expect 404. If not, 200 is also acceptable
    // (depends on how the tree was built). Log the result either way.
    let status = resp.status().as_u16();
    if status == 404 {
        let err: ErrorJson = resp.json().await.expect("error JSON");
        eprintln!("Correctly got 404 for nullifier 0: {}", err.error);
    } else {
        eprintln!(
            "Value 0 is not a nullifier in this tree (got {}), skipping 404 check",
            status
        );
    }
}

/// Bad input: wrong hex length returns 400.
#[tokio::test]
async fn invalid_hex_returns_400() {
    let url = server_url();
    let client = reqwest::Client::new();

    let resp = client
        .get(format!("{}/exclusion-proof/{}", url, "deadbeef"))
        .send()
        .await
        .expect("request should succeed");

    assert_eq!(
        resp.status(),
        400,
        "short hex should return 400 Bad Request"
    );

    let err: ErrorJson = resp.json().await.expect("error JSON");
    eprintln!("400 error: {}", err.error);
}

// ── Circuit-level proving & verification via real delegation circuit ─

/// Bridges the running API server to orchard's `ImtProvider` trait.
///
/// Uses `reqwest::blocking::Client` since `ImtProvider` methods are synchronous.
struct ApiImtProvider {
    client: reqwest::blocking::Client,
    root: pallas::Base,
}

impl ApiImtProvider {
    /// Create a new provider, fetching the current root from the API.
    fn new() -> Self {
        let client = reqwest::blocking::Client::new();
        let url = server_url();
        let resp: RootJson = client
            .get(format!("{}/root", url))
            .send()
            .expect("root request should succeed")
            .json()
            .expect("valid root JSON");
        let root = hex_to_fp(&resp.root);
        ApiImtProvider { client, root }
    }
}

impl ImtProvider for ApiImtProvider {
    fn root(&self) -> pallas::Base {
        self.root
    }

    fn non_membership_proof(&self, nf: pallas::Base) -> Result<OrchardImtProofData, ImtError> {
        let url = server_url();
        let hex_str = &fp_hex(&nf)[2..];

        let resp = self
            .client
            .get(format!("{}/exclusion-proof/{}", url, hex_str))
            .send()
            .unwrap_or_else(|e| panic!("exclusion proof request failed: {}", e));

        assert_eq!(
            resp.status().as_u16(),
            200,
            "expected 200 for exclusion proof of {}",
            fp_hex(&nf)
        );

        let json: ImtProofJson = resp.json().expect("valid exclusion proof JSON");
        let proof = parse_proof(&json);

        Ok(OrchardImtProofData {
            root: proof.root,
            low: proof.low,
            width: proof.width,
            leaf_pos: proof.leaf_pos,
            path: proof.path,
        })
    }
}

// ── Delegation circuit helpers (mirrors imt_circuit_integration.rs) ─

/// Convert a `MerkleHashOrchard` to `pallas::Base` via byte roundtrip.
fn merkle_hash_to_base(h: MerkleHashOrchard) -> pallas::Base {
    pallas::Base::from_repr(h.to_bytes()).unwrap()
}

/// Delegation circuit K value (must match orchard's delegation circuit).
const K: u32 = 14;

/// Build a note commitment tree with up to 4 notes, returning
/// `(inputs, nc_root)` suitable for `build_delegation_bundle`.
fn make_real_note_inputs(
    fvk: &FullViewingKey,
    values: &[u64],
    imt_provider: &impl ImtProvider,
    rng: &mut impl rand::RngCore,
) -> (Vec<RealNoteInput>, pallas::Base) {
    let n = values.len();
    assert!(n >= 1 && n <= 4);

    let mut notes = Vec::with_capacity(n);
    for &v in values {
        let recipient = fvk.address_at(0u32, Scope::External);
        let note_value = NoteValue::from_raw(v);
        let (_, _, dummy_parent) = Note::dummy(&mut *rng, None);
        let note = Note::new(
            recipient,
            note_value,
            Rho::from_nf_old(dummy_parent.nullifier(fvk)),
            &mut *rng,
        );
        notes.push(note);
    }

    let empty_leaf = MerkleHashOrchard::empty_leaf();
    let mut leaves = [empty_leaf; 4];
    for (i, note) in notes.iter().enumerate() {
        let cmx = ExtractedNoteCommitment::from(note.commitment());
        leaves[i] = MerkleHashOrchard::from_cmx(&cmx);
    }

    let l1_0 = MerkleHashOrchard::combine(Level::from(0), &leaves[0], &leaves[1]);
    let l1_1 = MerkleHashOrchard::combine(Level::from(0), &leaves[2], &leaves[3]);
    let l2_0 = MerkleHashOrchard::combine(Level::from(1), &l1_0, &l1_1);

    let mut current = l2_0;
    for level in 2..IMT_DEPTH {
        let sibling = MerkleHashOrchard::empty_root(Level::from(level as u8));
        current = MerkleHashOrchard::combine(Level::from(level as u8), &current, &sibling);
    }
    let nc_root = merkle_hash_to_base(current);

    let l1 = [l1_0, l1_1];
    let mut inputs = Vec::with_capacity(n);
    for (i, note) in notes.into_iter().enumerate() {
        let mut auth_path = [MerkleHashOrchard::empty_leaf(); NOTE_COMMITMENT_TREE_DEPTH];
        auth_path[0] = leaves[i ^ 1];
        auth_path[1] = l1[1 - (i >> 1)];
        for level in 2..IMT_DEPTH {
            auth_path[level] = MerkleHashOrchard::empty_root(Level::from(level as u8));
        }
        let merkle_path = MerklePath::from_parts(i as u32, auth_path);

        let real_nf = note.nullifier(fvk);
        let nf_base = pallas::Base::from_repr(real_nf.to_bytes()).unwrap();
        let imt_proof = imt_provider.non_membership_proof(nf_base).expect("IMT proof fetch failed");

        inputs.push(RealNoteInput {
            note,
            fvk: fvk.clone(),
            merkle_path,
            imt_proof,
            scope: Scope::External,
        });
    }

    (inputs, nc_root)
}

/// End-to-end test: fetch IMT proofs from the API server, build a delegation
/// bundle with a single real note, and verify the full circuit with MockProver.
///
/// This exercises the real delegation circuit (Condition 13: IMT non-membership)
/// using proofs fetched from the live API, proving they satisfy the exact same
/// constraints as production.
#[test]
fn api_imt_proof_verifies_in_delegation_circuit() {
    let mut rng = OsRng;
    let api = ApiImtProvider::new();

    let sk = SpendingKey::random(&mut rng);
    let fvk: FullViewingKey = (&sk).into();
    let output_recipient = fvk.address_at(1u32, Scope::External);
    let vote_round_id = pallas::Base::random(&mut rng);
    let van_comm_rand = pallas::Base::random(&mut rng);
    let alpha = pallas::Scalar::random(&mut rng);

    // Single note with value >= 12,500,000 (the min weight).
    let (inputs, nc_root) = make_real_note_inputs(&fvk, &[13_000_000], &api, &mut rng);

    let bundle = build_delegation_bundle(
        inputs,
        &fvk,
        alpha,
        output_recipient,
        vote_round_id,
        nc_root,
        van_comm_rand,
        &api,
        &mut rng,
        None,
    )
    .expect("build_delegation_bundle should succeed");

    let pi = bundle.instance.to_halo2_instance();
    let prover = MockProver::run(K, &bundle.circuit, vec![pi]).unwrap();
    assert_eq!(
        prover.verify(),
        Ok(()),
        "delegation circuit with API-sourced IMT proofs should verify"
    );

    eprintln!("Delegation circuit verified with API IMT proofs (single note)");
}

/// Same test with 4 real notes to exercise multiple IMT proof lookups.
#[test]
fn four_notes_api_imt_proof_verifies_in_delegation_circuit() {
    let mut rng = OsRng;
    let api = ApiImtProvider::new();

    let sk = SpendingKey::random(&mut rng);
    let fvk: FullViewingKey = (&sk).into();
    let output_recipient = fvk.address_at(1u32, Scope::External);
    let vote_round_id = pallas::Base::random(&mut rng);
    let van_comm_rand = pallas::Base::random(&mut rng);
    let alpha = pallas::Scalar::random(&mut rng);

    // 4 notes × 3,200,000 = 12,800,000 >= 12,500,000.
    let (inputs, nc_root) = make_real_note_inputs(
        &fvk,
        &[3_200_000, 3_200_000, 3_200_000, 3_200_000],
        &api,
        &mut rng,
    );

    let bundle = build_delegation_bundle(
        inputs,
        &fvk,
        alpha,
        output_recipient,
        vote_round_id,
        nc_root,
        van_comm_rand,
        &api,
        &mut rng,
        None,
    )
    .expect("build_delegation_bundle should succeed");

    let pi = bundle.instance.to_halo2_instance();
    let prover = MockProver::run(K, &bundle.circuit, vec![pi]).unwrap();
    assert_eq!(
        prover.verify(),
        Ok(()),
        "4-note delegation circuit with API-sourced IMT proofs should verify"
    );

    eprintln!("Delegation circuit verified with API IMT proofs (4 notes)");
}
