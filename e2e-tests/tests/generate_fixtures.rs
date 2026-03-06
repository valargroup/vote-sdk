//! Offline fixture generator for the 10K voter stress test.
//!
//! Generates N real delegation + cast-vote proof bundles in parallel and saves
//! them to disk as reusable JSON fixtures. Run with:
//!
//!   FIXTURE_COUNT=100 FIXTURE_DIR=fixtures/100 \
//!     cargo test --release --manifest-path e2e-tests/Cargo.toml \
//!     --test generate_fixtures -- --ignored --nocapture
//!
//! Fixtures are reusable until the circuit changes. Proof generation is the
//! bottleneck (~30-60s per proof at K=14); verification is ~13ms.

use std::path::PathBuf;
use std::time::Instant;

use base64::{engine::general_purpose::STANDARD as B64, Engine};
use ff::PrimeField;
use pasta_curves::pallas;
use serde::{Deserialize, Serialize};

use e2e_tests::payloads;
use e2e_tests::fixtures::resolve_voter_fixture_dir;
use e2e_tests::setup::build_multi_delegation_bundles;

use vote_commitment_tree::MemoryTreeServer;
use voting_circuits::vote_proof::circuit::VOTE_COMM_TREE_DEPTH;

// ---------------------------------------------------------------------------
// Serializable fixture types
// ---------------------------------------------------------------------------

#[derive(Serialize, Deserialize)]
struct DelegationFixture {
    payload: serde_json::Value,
    van_cmx_b64: String,
}

/// Stores the *inputs* needed to generate a cast-vote proof at runtime.
/// Proof generation requires knowing the real on-chain anchor height, which
/// becomes a ZKP public input. The delegation proof is pre-generated because
/// it doesn't depend on chain state, but the cast-vote proof must be generated
/// after delegations are committed.
#[derive(Serialize, Deserialize)]
struct CastVoteInputFixture {
    sk_b64: String,
    van_comm_rand_b64: String,
    total_note_value: u64,
    tree_position: u32,
    tree_path_b64: Vec<String>,
}

#[derive(Serialize, Deserialize)]
struct ExpectedTree {
    root_b64: String,
    next_index: u64,
    delegation_anchor_height: u32,
}

#[derive(Serialize, Deserialize)]
struct FixtureManifest {
    count: usize,
    round_id_b64: String,
    round_fields: RoundFieldsSer,
    expected_tree_after_delegations: ExpectedTree,
    expected_tree_after_cast_votes: ExpectedTree,
}

#[derive(Serialize, Deserialize)]
struct RoundFieldsSer {
    snapshot_height: u64,
    snapshot_blockhash: String,
    proposals_hash: String,
    vote_end_time: u64,
    nullifier_imt_root: String,
    nc_root: String,
}

fn b64(bytes: &[u8]) -> String {
    B64.encode(bytes)
}

// ---------------------------------------------------------------------------
// Fixture generation
// ---------------------------------------------------------------------------

#[test]
#[ignore]
fn generate_voter_fixtures() {
    let count: usize = std::env::var("FIXTURE_COUNT")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(10);

    let fixture_dir = resolve_voter_fixture_dir(
        PathBuf::from(std::env::var("FIXTURE_DIR").unwrap_or_else(|_| format!("fixtures/{count}")))
            .as_path(),
    )
    .expect("resolve fixture dir");

    eprintln!(
        "=== Generating {count} voter fixtures -> {} ===",
        fixture_dir.display()
    );
    std::fs::create_dir_all(&fixture_dir).expect("create fixture dir");

    let total_start = Instant::now();

    // -----------------------------------------------------------------------
    // Phase 1: Generate delegation bundles (real ZKP #1, parallel)
    // -----------------------------------------------------------------------
    eprintln!("\n--- Phase 1: Generating {count} delegation proofs (parallel) ---");
    let phase1_start = Instant::now();

    let (bundles, round_fields) =
        build_multi_delegation_bundles(count).expect("build_multi_delegation_bundles");

    let round_id = payloads::derive_round_id(&round_fields);
    eprintln!(
        "Phase 1 complete: {} delegations in {:.1}s",
        count,
        phase1_start.elapsed().as_secs_f64()
    );

    // Serialize delegation fixtures.
    let mut delegation_fixtures = Vec::with_capacity(count);
    let mut van_cmx_fps: Vec<pallas::Base> = Vec::with_capacity(count);

    for (payload, vote_data) in &bundles {
        let json_payload = payloads::delegate_vote_payload(&round_id, payload);
        van_cmx_fps.push(vote_data.van_comm);
        delegation_fixtures.push(DelegationFixture {
            payload: json_payload,
            van_cmx_b64: b64(&vote_data.van_comm.to_repr()),
        });
    }

    let deleg_path = fixture_dir.join("delegations.json");
    std::fs::write(
        &deleg_path,
        serde_json::to_string(&delegation_fixtures).unwrap(),
    )
    .expect("write delegations.json");
    eprintln!(
        "Wrote {} delegation fixtures to {}",
        count,
        deleg_path.display()
    );

    // -----------------------------------------------------------------------
    // Phase 2: Build synthetic vote commitment tree from van_cmx leaves
    // -----------------------------------------------------------------------
    eprintln!("\n--- Phase 2: Building synthetic vote commitment tree ({count} leaves) ---");
    let phase2_start = Instant::now();

    let mut tree = MemoryTreeServer::empty();
    for fp in &van_cmx_fps {
        tree.append(*fp).expect("tree append");
    }
    let deleg_checkpoint_height: u32 = 1;
    tree.checkpoint(deleg_checkpoint_height).expect("checkpoint");
    let tree_root_after_deleg = tree.root();

    eprintln!(
        "Phase 2 complete: tree has {} leaves, root={}, took {:.1}s",
        tree.size(),
        hex::encode(tree_root_after_deleg.to_repr()),
        phase2_start.elapsed().as_secs_f64()
    );

    // -----------------------------------------------------------------------
    // Phase 3: Extract cast-vote inputs (Merkle paths + per-voter secrets)
    // -----------------------------------------------------------------------
    eprintln!("\n--- Phase 3: Extracting cast-vote inputs ({count} voters) ---");
    let phase3_start = Instant::now();

    let mut witnesses: Vec<[pallas::Base; VOTE_COMM_TREE_DEPTH]> = Vec::with_capacity(count);
    for i in 0..count {
        let path = tree
            .path(i as u64, deleg_checkpoint_height)
            .unwrap_or_else(|| panic!("no path for position {i}"));
        let auth: [pallas::Base; VOTE_COMM_TREE_DEPTH] =
            path.auth_path().map(|h| h.inner());
        witnesses.push(auth);
    }

    let cast_input_fixtures: Vec<CastVoteInputFixture> = bundles
        .iter()
        .enumerate()
        .map(|(i, (_payload, vote_data))| CastVoteInputFixture {
            sk_b64: b64(vote_data.sk.to_bytes()),
            van_comm_rand_b64: b64(&vote_data.van_comm_rand.to_repr()),
            total_note_value: vote_data.total_note_value,
            tree_position: i as u32,
            tree_path_b64: witnesses[i].iter().map(|fp| b64(&fp.to_repr())).collect(),
        })
        .collect();

    eprintln!(
        "Phase 3 complete: {} cast-vote inputs extracted in {:.1}s",
        count,
        phase3_start.elapsed().as_secs_f64()
    );

    // -----------------------------------------------------------------------
    // Phase 4: Serialize to disk
    // -----------------------------------------------------------------------
    eprintln!("\n--- Phase 4: Writing fixtures to disk ---");

    let cast_path = fixture_dir.join("cast_vote_inputs.json");
    std::fs::write(
        &cast_path,
        serde_json::to_string(&cast_input_fixtures).unwrap(),
    )
    .expect("write cast_vote_inputs.json");
    eprintln!("Wrote {} cast-vote input fixtures to {}", count, cast_path.display());

    let manifest = FixtureManifest {
        count,
        round_id_b64: b64(&round_id),
        round_fields: RoundFieldsSer {
            snapshot_height: round_fields.snapshot_height,
            snapshot_blockhash: b64(&round_fields.snapshot_blockhash),
            proposals_hash: b64(&round_fields.proposals_hash),
            vote_end_time: round_fields.vote_end_time,
            nullifier_imt_root: b64(&round_fields.nullifier_imt_root),
            nc_root: b64(&round_fields.nc_root),
        },
        expected_tree_after_delegations: ExpectedTree {
            root_b64: b64(&tree_root_after_deleg.to_repr()),
            next_index: count as u64,
            delegation_anchor_height: deleg_checkpoint_height,
        },
        expected_tree_after_cast_votes: ExpectedTree {
            root_b64: String::new(),
            next_index: count as u64 * 3,
            delegation_anchor_height: deleg_checkpoint_height,
        },
    };

    let manifest_path = fixture_dir.join("manifest.json");
    std::fs::write(
        &manifest_path,
        serde_json::to_string_pretty(&manifest).unwrap(),
    )
    .expect("write manifest.json");
    eprintln!("Wrote manifest to {}", manifest_path.display());

    eprintln!(
        "\n=== All fixtures generated in {:.1}s ===",
        total_start.elapsed().as_secs_f64()
    );
    eprintln!("  Delegations: {count}");
    eprintln!("  Cast-vote inputs: {count}");
    eprintln!("  Fixture dir: {}", fixture_dir.display());
}
