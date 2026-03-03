//! Sync stress test: multiple delegations + concurrent tree sync clients.
//!
//! Exercises the full VAN/VC sync path under load:
//! chain KV wiring -> REST JSON serialization -> Rust HTTP client parsing ->
//! TreeClient incremental sync -> witness generation -> root consistency.
//!
//! Run with a live chain:
//!   cargo test --release --manifest-path e2e-tests/Cargo.toml --test sync_stress -- --nocapture --ignored

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use base64::Engine;
use e2e_tests::{
    api::{
        self, broadcast_cosmos_msg, commitment_tree_next_index, default_cosmos_tx_config, get_json,
        import_hex_key, post_json_accept_committed, wait_for_round_status, CosmosTxConfig,
        SESSION_STATUS_ACTIVE,
    },
    payloads::{create_voting_session_payload, delegate_vote_payload},
    setup::{build_multi_delegation_bundles, ensure_pallas_key_registered},
};
use ff::PrimeField;
use pasta_curves::pallas;
use vote_commitment_tree::TreeClient;
use vote_commitment_tree_client::http_sync_api::HttpTreeSyncApi;

const VOTE_MANAGER_PRIVKEY_HEX: &str =
    "b7e910eded435dd4e19c581b9a0b8e65104dcc4ebca8a1d55aa5c803e72ba2ee";
const VOTE_MANAGER_ADDRESS: &str = "zvote15fjfr6rrs60vu4st6arrd94w5j6z7f6kxr92cg";

struct SyncClientResult {
    client_id: usize,
    final_size: u64,
    final_root: Option<pallas::Base>,
    final_height: Option<u32>,
    witnesses: Vec<Option<vote_commitment_tree::MerklePath>>,
    sync_count: u32,
    sync_errors: Vec<String>,
}

/// Sync stress test: N delegations blasted into the chain while 3 concurrent
/// TreeClient instances incrementally sync and build witnesses.
///
/// Success criteria:
/// - All N delegations land on-chain (tree grows by N)
/// - All 3 sync clients converge to identical (size, root, height)
/// - All VAN witnesses verify against on-chain root
/// - No StartIndexMismatch or RootMismatch errors
#[test]
#[ignore = "requires running chain"]
fn sync_stress_multi_delegation() {
    let n: usize = std::env::var("ZALLY_STRESS_DELEGATION_COUNT")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(5);

    // ---- Phase 1: Pre-generate N delegation bundles (parallel ZKP #1 proofs) ----
    eprintln!(
        "[stress] Phase 1: pre-generating {} delegation bundles...",
        n
    );
    let (bundles, round_fields) =
        build_multi_delegation_bundles(n).expect("build_multi_delegation_bundles");
    eprintln!("[stress] Phase 1 complete: {} bundles ready", bundles.len());

    // ---- Phase 2: Create round + wait for ACTIVE ----
    ensure_pallas_key_registered();
    let config = default_cosmos_tx_config();
    import_hex_key("vote-manager", VOTE_MANAGER_PRIVKEY_HEX, &config.home_dir);

    let (mut body, _, round_id) =
        create_voting_session_payload(VOTE_MANAGER_ADDRESS, 300, Some(round_fields));
    let round_id_hex = hex::encode(&round_id);

    body["@type"] = serde_json::json!("/zvote.v1.MsgCreateVotingSession");
    let vm_config = CosmosTxConfig {
        key_name: "vote-manager".to_string(),
        home_dir: config.home_dir.clone(),
        chain_id: config.chain_id.clone(),
        node_url: config.node_url.clone(),
    };
    let (status, json) =
        broadcast_cosmos_msg(&body, &vm_config).expect("broadcast create-voting-session");
    assert_eq!(
        status, 200,
        "create session: HTTP {}, body={:?}",
        status, json
    );
    assert_eq!(
        json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1),
        0,
        "create session rejected: {:?}",
        json.get("log")
    );

    eprintln!(
        "[stress] Phase 2: waiting for round {} to become ACTIVE...",
        &round_id_hex
    );
    wait_for_round_status(&round_id_hex, SESSION_STATUS_ACTIVE, 60_000, 2_000)
        .expect("round should become ACTIVE");
    eprintln!("[stress] Phase 2 complete: round ACTIVE");

    // ---- Phase 3: Blast delegations + concurrent sync clients ----
    let pre_blast_next_index = commitment_tree_next_index().unwrap_or(0);
    let expected_tree_size = pre_blast_next_index + n as u64;
    eprintln!(
        "[stress] Phase 3: pre_blast_next_index={}, expecting tree to grow to {}",
        pre_blast_next_index, expected_tree_size
    );

    let stop_flag = Arc::new(AtomicBool::new(false));
    let base_url = api::base_url();

    // Spawn 3 concurrent sync client threads.
    let sync_handles: Vec<_> = (0..3)
        .map(|client_id| {
            let stop = Arc::clone(&stop_flag);
            let url = base_url.clone();
            let num_delegations = n as u64;
            let pre_blast = pre_blast_next_index;

            std::thread::spawn(move || {
                let mut tree_client = TreeClient::empty();
                // Mark VAN positions for all N delegations so witnesses are retained.
                for j in 0..num_delegations {
                    tree_client.mark_position(pre_blast + j);
                }

                let sync_api = HttpTreeSyncApi::new(&url);
                let mut sync_count = 0u32;
                let mut sync_errors: Vec<String> = Vec::new();

                while !stop.load(Ordering::Relaxed) {
                    match tree_client.sync(&sync_api) {
                        Ok(()) => sync_count += 1,
                        Err(e) => {
                            let msg = format!("{:?}", e);
                            eprintln!("[sync-client-{}] sync error: {}", client_id, msg);
                            sync_errors.push(msg);
                            break;
                        }
                    }
                    std::thread::sleep(std::time::Duration::from_millis(500));
                }

                // Final sync after stop signal to catch the last block.
                match tree_client.sync(&sync_api) {
                    Ok(()) => sync_count += 1,
                    Err(e) => {
                        let msg = format!("{:?}", e);
                        eprintln!("[sync-client-{}] final sync error: {}", client_id, msg);
                        sync_errors.push(msg);
                    }
                }

                let final_size = tree_client.size();
                let final_height = tree_client.last_synced_height();
                let final_root = final_height.and_then(|h| tree_client.root_at_height(h));

                // Generate witnesses for all VAN positions.
                let witnesses: Vec<Option<vote_commitment_tree::MerklePath>> = (0..num_delegations)
                    .map(|j| {
                        let pos = pre_blast + j;
                        final_height.and_then(|h| tree_client.witness(pos, h))
                    })
                    .collect();

                SyncClientResult {
                    client_id,
                    final_size,
                    final_root,
                    final_height,
                    witnesses,
                    sync_count,
                    sync_errors,
                }
            })
        })
        .collect();

    // Main thread: submit N delegations sequentially (as fast as possible).
    for (i, (payload, _)) in bundles.iter().enumerate() {
        let deleg_body = delegate_vote_payload(&round_id, payload);
        let target = pre_blast_next_index + i as u64 + 1;
        let (status, json) =
            post_json_accept_committed("/zally/v1/delegate-vote", &deleg_body, || {
                commitment_tree_next_index()
                    .map(|idx| idx >= target)
                    .unwrap_or(false)
            })
            .unwrap_or_else(|e| panic!("POST delegate-vote {}: {}", i, e));
        let code = json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1);
        eprintln!(
            "[stress] delegation {} submitted: status={}, code={}",
            i, status, code
        );
        assert!(
            status == 200 && code == 0,
            "delegation {} failed: status={}, code={}, log={:?}",
            i,
            status,
            code,
            json.get("log")
        );
    }

    // Poll until tree has grown by N.
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(120);
    loop {
        if let Some(next) = commitment_tree_next_index() {
            if next >= expected_tree_size {
                eprintln!(
                    "[stress] tree grown to {} (target: {})",
                    next, expected_tree_size
                );
                break;
            }
        }
        assert!(
            std::time::Instant::now() < deadline,
            "tree never grew to {} within 120s",
            expected_tree_size
        );
        std::thread::sleep(std::time::Duration::from_secs(2));
    }

    // Wait one more block for the last checkpoint to finalize.
    std::thread::sleep(std::time::Duration::from_secs(6));

    // Signal sync clients to stop and collect results.
    stop_flag.store(true, Ordering::Relaxed);

    let sync_results: Vec<_> = sync_handles
        .into_iter()
        .map(|h| h.join().expect("sync client thread panicked"))
        .collect();

    // ---- Phase 4: Verify ----
    eprintln!("[stress] Phase 4: verification");

    for result in &sync_results {
        assert!(
            result.sync_errors.is_empty(),
            "sync client {} encountered sync errors: {:?}",
            result.client_id,
            result.sync_errors
        );
    }

    // 4a: All sync clients agree on final state.
    let first = &sync_results[0];
    for result in &sync_results {
        assert_eq!(
            result.final_size, first.final_size,
            "client {} size {} != client 0 size {}",
            result.client_id, result.final_size, first.final_size
        );
        assert_eq!(
            result.final_root, first.final_root,
            "client {} root differs from client 0",
            result.client_id
        );
        assert_eq!(
            result.final_height, first.final_height,
            "client {} height {:?} != client 0 height {:?}",
            result.client_id, result.final_height, first.final_height
        );
    }
    eprintln!(
        "[stress] 4a: all sync clients agree: size={}, height={:?}",
        first.final_size, first.final_height
    );

    // 4b: Each client has valid witnesses for all VAN positions.
    let van_comms: Vec<pallas::Base> = bundles.iter().map(|(_, vpd)| vpd.van_comm).collect();
    let final_root_fp = first.final_root.expect("must have final root");

    for result in &sync_results {
        for (j, witness_opt) in result.witnesses.iter().enumerate() {
            let witness = witness_opt.as_ref().unwrap_or_else(|| {
                panic!(
                    "client {} missing witness for VAN position {}",
                    result.client_id,
                    pre_blast_next_index + j as u64
                )
            });
            assert!(
                witness.verify(van_comms[j], final_root_fp),
                "client {} witness for VAN {} failed verification",
                result.client_id,
                j
            );
        }
    }
    eprintln!(
        "[stress] 4b: all {} witnesses verify against final root for all 3 clients",
        n
    );

    // 4c: final_root matches on-chain root at final_height.
    let final_height = first.final_height.expect("must have final height");
    let (status, json) = get_json(&format!("/zally/v1/commitment-tree/{}", final_height))
        .expect("GET tree at final height");
    assert_eq!(status, 200);
    let on_chain_root_b64 = json
        .get("tree")
        .and_then(|t| t.get("root"))
        .and_then(|r| r.as_str())
        .expect("on-chain tree root");
    let on_chain_root_bytes = base64::engine::general_purpose::STANDARD
        .decode(on_chain_root_b64)
        .expect("decode on-chain root");
    let final_root_bytes = final_root_fp.to_repr();
    assert_eq!(
        on_chain_root_bytes.as_slice(),
        &final_root_bytes[..],
        "final root does not match on-chain root at height {}",
        final_height
    );
    eprintln!(
        "[stress] 4c: final root matches on-chain root at height {}",
        final_height
    );

    // Log sync stats.
    for result in &sync_results {
        eprintln!(
            "[stress] sync client {}: {} syncs, final size {}",
            result.client_id, result.sync_count, result.final_size
        );
    }

    eprintln!(
        "[stress] PASSED: {} delegations, 3 sync clients, all consistent",
        n
    );
}
