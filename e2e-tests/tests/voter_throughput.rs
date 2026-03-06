//! Voter throughput stress test: loads pre-generated fixtures and submits them
//! to a real chain, measuring server load throughput across all submission phases
//! including share reveals (real ZKP #3 via helper server).
//!
//! Tally (partial decryption + BSGS solve) is benchmarked separately — this test
//! focuses on how the server holds up under concurrent vote submissions.
//! Fixtures use a far-future vote_end_time so they are reusable indefinitely.
//!
//! Requires:
//! - A freshly initialized multi-validator chain built with `make install-ffi`
//! - Helper server configured for aggressive processing (HELPER_MEAN_DELAY=0, etc.)
//! - Pre-generated fixtures (downloaded automatically if missing; see `generate_fixtures.rs`)
//!
//! Run:
//!   VOTER_FIXTURE_DIR=fixtures/100 \
//!     cargo test --release --manifest-path e2e-tests/Cargo.toml \
//!     --test voter_throughput -- --ignored --nocapture
//!
//! Environment variables:
//!   VOTER_FIXTURE_DIR     - path to fixture directory (required)
//!   VOTER_FIXTURE_BASE_URL - optional override for fixture download base URL
//!   VOTER_CONCURRENCY     - number of concurrent submission workers (default: 50)
//!   VOTER_PHASE_TIMEOUT   - per-phase timeout in seconds (default: 600)
//!   WAVE_SIZE             - voters per share submission wave (default: 10)
//!   WAVE_INTERVAL_MS      - milliseconds between share waves (default: 1000)
//!   STRESS_VOTER_COUNT    - use only first N voters from fixtures (default: all)
//!   SHARE_STALL_TIMEOUT_SECS - fail only if share processing makes no progress
//!                             for this many seconds (default: 1800, 0 = disabled)

use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use base64::{engine::general_purpose::STANDARD as B64, Engine};
use e2e_tests::api::{
    broadcast_cosmos_msg, commitment_tree_latest, commitment_tree_next_index,
    default_cosmos_tx_config, get_all_validator_operator_addresses, get_round_ea_pk,
    get_helper_queue_status, import_hex_key, post_helper_json, post_json,
    wait_for_round_status, CosmosTxConfig, HelperQueueStatus, SESSION_STATUS_ACTIVE,
};
use e2e_tests::fixtures::ensure_voter_fixture_files;
use e2e_tests::metrics::{self, MetricsCollector, Sample};
use e2e_tests::payloads::{self, create_voting_session_payload};
use e2e_tests::setup::ensure_pallas_key_registered;
use ff::{Field, PrimeField};
use group::GroupEncoding;
use pasta_curves::pallas;
use rand::rngs::OsRng;
use serde::Deserialize;
use voting_circuits::vote_proof::{
    builder::build_vote_proof_from_delegation, circuit::VOTE_COMM_TREE_DEPTH,
};

const VOTE_MANAGER_PRIVKEY_HEX: &str =
    "b7e910eded435dd4e19c581b9a0b8e65104dcc4ebca8a1d55aa5c803e72ba2ee";
const VOTE_MANAGER_ADDRESS: &str = "zvote15fjfr6rrs60vu4st6arrd94w5j6z7f6kxr92cg";

// ---------------------------------------------------------------------------
// Fixture deserialization types (must match generate_fixtures.rs)
// ---------------------------------------------------------------------------

#[derive(Deserialize)]
struct DelegationFixture {
    payload: serde_json::Value,
    van_cmx_b64: String,
}

#[derive(Deserialize)]
struct CastVoteInputFixture {
    sk_b64: String,
    van_comm_rand_b64: String,
    total_note_value: u64,
    tree_position: u32,
    tree_path_b64: Vec<String>,
}

#[derive(Deserialize)]
struct FixtureManifest {
    count: usize,
    round_id_b64: String,
    round_fields: RoundFieldsSer,
    expected_tree_after_delegations: ExpectedTree,
    #[allow(dead_code)]
    expected_tree_after_cast_votes: ExpectedTree,
}

#[derive(Deserialize)]
struct RoundFieldsSer {
    snapshot_height: u64,
    snapshot_blockhash: String,
    proposals_hash: String,
    vote_end_time: u64,
    nullifier_imt_root: String,
    nc_root: String,
}

#[derive(Deserialize)]
#[allow(dead_code)]
struct ExpectedTree {
    root_b64: String,
    next_index: u64,
    delegation_anchor_height: u32,
}

fn b64_decode(s: &str) -> Vec<u8> {
    use base64::{engine::general_purpose::STANDARD, Engine};
    STANDARD.decode(s).expect("valid base64")
}

fn load_fixtures(
    dir: &PathBuf,
    voter_cap: Option<usize>,
) -> (
    FixtureManifest,
    Vec<DelegationFixture>,
    Vec<CastVoteInputFixture>,
) {
    ensure_voter_fixture_files(dir).expect("download voter fixtures");

    let mut manifest: FixtureManifest = serde_json::from_str(
        &std::fs::read_to_string(dir.join("manifest.json")).expect("read manifest.json"),
    )
    .expect("parse manifest.json");

    let mut delegations: Vec<DelegationFixture> = serde_json::from_str(
        &std::fs::read_to_string(dir.join("delegations.json")).expect("read delegations.json"),
    )
    .expect("parse delegations.json");

    let mut cv_inputs: Vec<CastVoteInputFixture> = serde_json::from_str(
        &std::fs::read_to_string(dir.join("cast_vote_inputs.json"))
            .expect("read cast_vote_inputs.json"),
    )
    .expect("parse cast_vote_inputs.json");

    assert!(
        delegations.len() >= manifest.count,
        "delegation count mismatch"
    );
    assert!(cv_inputs.len() >= manifest.count, "cast_vote_input count mismatch");

    let total_count = manifest.count;
    let use_count = match voter_cap {
        Some(cap) if cap < manifest.count => {
            eprintln!(
                "STRESS_VOTER_COUNT={} — using first {} of {} available fixtures",
                cap, cap, manifest.count
            );
            cap
        }
        _ => manifest.count,
    };

    delegations.truncate(use_count);
    cv_inputs.truncate(use_count);
    manifest.count = use_count;

    // When subsetting (use_count < total), the fixture Merkle paths are from
    // the full N-leaf tree. Rebuild a local tree with only the first use_count
    // leaves and recompute auth paths so they match the on-chain tree.
    if use_count < total_count {
        eprintln!("Recomputing Merkle paths for {} leaves (fixtures had {} leaves)...", use_count, total_count);
        let mut local_tree = vote_commitment_tree::MemoryTreeServer::empty();
        for d in &delegations {
            let van_cmx_bytes: [u8; 32] = B64.decode(&d.van_cmx_b64).unwrap().try_into().unwrap();
            let fp: pallas::Base = Option::from(pallas::Base::from_repr(van_cmx_bytes)).unwrap();
            local_tree.append(fp).expect("tree append");
        }
        local_tree.checkpoint(1).expect("checkpoint");

        for (i, cv) in cv_inputs.iter_mut().enumerate() {
            let path = local_tree
                .path(i as u64, 1)
                .unwrap_or_else(|| panic!("no path for position {i}"));
            cv.tree_path_b64 = path
                .auth_path()
                .map(|h| B64.encode(h.inner().to_repr()))
                .to_vec();
        }
        eprintln!("Merkle paths recomputed for {} voters", use_count);
    }

    (manifest, delegations, cv_inputs)
}

fn concurrency() -> usize {
    std::env::var("VOTER_CONCURRENCY")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(50)
}

fn phase_timeout() -> Duration {
    let secs: u64 = std::env::var("VOTER_PHASE_TIMEOUT")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(600);
    Duration::from_secs(secs)
}

fn wave_size() -> usize {
    std::env::var("WAVE_SIZE")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(10)
}

fn wave_interval() -> Duration {
    let ms: u64 = std::env::var("WAVE_INTERVAL_MS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(1000);
    Duration::from_millis(ms)
}

fn share_stall_timeout() -> Option<Duration> {
    let secs: u64 = std::env::var("SHARE_STALL_TIMEOUT_SECS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(1800);
    if secs == 0 {
        None
    } else {
        Some(Duration::from_secs(secs))
    }
}

fn min_successes(total: usize) -> usize {
    if total == 0 {
        0
    } else {
        (total * 95).div_ceil(100)
    }
}


// ---------------------------------------------------------------------------
// Submission result tracking
// ---------------------------------------------------------------------------

struct SubmitResult {
    succeeded: usize,
    #[allow(dead_code)]
    tx_hashes: Vec<(usize, Option<String>)>,
}

/// Submit payloads concurrently to the given API path.
/// Returns success count and per-voter tx_hash for position resolution.
fn submit_concurrent(
    payloads: Vec<(usize, serde_json::Value)>,
    path: &str,
    phase_name: &str,
    collector: &Arc<MetricsCollector>,
    concurrency: usize,
) -> SubmitResult {
    let total = payloads.len();
    let queue = Arc::new(std::sync::Mutex::new(payloads));
    let succeeded = Arc::new(AtomicUsize::new(0));
    let submitted = Arc::new(AtomicUsize::new(0));
    let results = Arc::new(std::sync::Mutex::new(Vec::<(usize, Option<String>)>::new()));

    let handles: Vec<_> = (0..concurrency)
        .map(|_worker| {
            let queue = Arc::clone(&queue);
            let succeeded = Arc::clone(&succeeded);
            let submitted = Arc::clone(&submitted);
            let collector = Arc::clone(collector);
            let results = Arc::clone(&results);
            let path = path.to_string();
            let phase = phase_name.to_string();

            std::thread::spawn(move || {
                loop {
                    let item = {
                        let mut q = queue.lock().unwrap();
                        if q.is_empty() { None } else { Some(q.remove(0)) }
                    };
                    let (idx, payload) = match item {
                        Some(i) => i,
                        None => break,
                    };

                    let start = Instant::now();
                    let result = post_json(&path, &payload);
                    let latency = start.elapsed();

                    let (http_status, success, tx_hash) = match result {
                        Ok((status, json)) => {
                            let code = json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1);
                            let hash = json
                                .get("tx_hash")
                                .and_then(|h| h.as_str())
                                .map(|s| s.to_string());
                            if code != 0 {
                                let log_msg = json.get("log").and_then(|l| l.as_str()).unwrap_or("");
                                eprintln!("[{phase}] voter {idx} FAILED code={code}: {log_msg}");
                            }
                            (status, status == 200 && code == 0, hash)
                        }
                        Err(_) => (0, false, None),
                    };

                    collector.record(Sample {
                        phase: phase.clone(),
                        timestamp: start,
                        latency,
                        http_status,
                        success,
                    });

                    if success {
                        succeeded.fetch_add(1, Ordering::Relaxed);
                    }

                    results.lock().unwrap().push((idx, tx_hash));

                    let done = submitted.fetch_add(1, Ordering::Relaxed) + 1;
                    if done % 100 == 0 || done == total {
                        let ok = succeeded.load(Ordering::Relaxed);
                        eprintln!(
                            "[{phase}] {done}/{total} submitted ({ok} succeeded), latency={:.0}ms",
                            latency.as_secs_f64() * 1000.0
                        );
                    }
                }
            })
        })
        .collect();

    for h in handles {
        h.join().expect("worker thread panicked");
    }

    let mut tx_hashes = Arc::try_unwrap(results).unwrap().into_inner().unwrap();
    tx_hashes.sort_by_key(|(idx, _)| *idx);

    SubmitResult {
        succeeded: succeeded.load(Ordering::Relaxed),
        tx_hashes,
    }
}

/// Wait for the commitment tree to reach a target next_index.
fn wait_for_tree_size(target: u64, timeout: Duration) {
    let deadline = Instant::now() + timeout;
    let mut last_size = 0u64;
    while Instant::now() < deadline {
        if let Some(next) = commitment_tree_next_index() {
            if next >= target {
                eprintln!("[tree] reached size {} (target: {})", next, target);
                return;
            }
            if next != last_size {
                eprintln!(
                    "[tree] size {} / {} ({:.1}%)",
                    next,
                    target,
                    next as f64 / target as f64 * 100.0
                );
                last_size = next;
            }
        }
        std::thread::sleep(Duration::from_secs(3));
    }
    let actual = commitment_tree_next_index().unwrap_or(0);
    panic!(
        "tree never reached size {} (got {} after {:.0}s)",
        target,
        actual,
        timeout.as_secs_f64()
    );
}

/// Runtime result of generating one cast-vote proof.
struct CastVoteBundle {
    payload: serde_json::Value,
    share_payloads: Vec<serde_json::Value>,
}

/// Generate a real cast-vote proof + share payloads for one voter at runtime.
fn generate_cast_vote(
    input: &CastVoteInputFixture,
    round_id: &pallas::Base,
    anchor_height: u32,
    ea_pk: pallas::Affine,
) -> CastVoteBundle {
    let sk_bytes: [u8; 32] = B64.decode(&input.sk_b64).unwrap().try_into().unwrap();
    let sk = orchard::keys::SpendingKey::from_bytes(sk_bytes).unwrap();
    let van_comm_rand_bytes: [u8; 32] = B64.decode(&input.van_comm_rand_b64).unwrap().try_into().unwrap();
    let van_comm_rand: pallas::Base = Option::from(pallas::Base::from_repr(van_comm_rand_bytes)).unwrap();

    let tree_path: [pallas::Base; VOTE_COMM_TREE_DEPTH] = input
        .tree_path_b64
        .iter()
        .map(|s| {
            let bytes: [u8; 32] = B64.decode(s).unwrap().try_into().unwrap();
            Option::from(pallas::Base::from_repr(bytes)).unwrap()
        })
        .collect::<Vec<_>>()
        .try_into()
        .unwrap();

    let mut rng = OsRng;
    let alpha_v = pallas::Scalar::random(&mut rng);

    let bundle = build_vote_proof_from_delegation(
        &sk,
        1,
        input.total_note_value,
        van_comm_rand,
        *round_id,
        tree_path,
        input.tree_position,
        anchor_height,
        1,
        1,
        ea_pk,
        alpha_v,
        (1u64 << 16) - 1,
        &mut rng,
    )
    .expect("cast-vote proof generation failed");

    let ask = orchard::keys::SpendAuthorizingKey::from(&sk);
    let rsk = ask.randomize(&alpha_v);
    let sighash = {
        let mut canonical = Vec::new();
        canonical.extend_from_slice(b"ZALLY_CAST_VOTE_SIGHASH_V0");
        let mut buf32 = [0u8; 32];
        let vr = round_id.to_repr();
        buf32[..vr.as_ref().len().min(32)]
            .copy_from_slice(&vr.as_ref()[..vr.as_ref().len().min(32)]);
        canonical.extend_from_slice(&buf32);
        canonical.extend_from_slice(&bundle.r_vpk_bytes);
        buf32 = [0u8; 32];
        let vn = bundle.instance.van_nullifier.to_repr();
        buf32[..vn.as_ref().len().min(32)]
            .copy_from_slice(&vn.as_ref()[..vn.as_ref().len().min(32)]);
        canonical.extend_from_slice(&buf32);
        buf32 = [0u8; 32];
        let van_new = bundle.instance.vote_authority_note_new.to_repr();
        buf32[..van_new.as_ref().len().min(32)]
            .copy_from_slice(&van_new.as_ref()[..van_new.as_ref().len().min(32)]);
        canonical.extend_from_slice(&buf32);
        buf32 = [0u8; 32];
        let vc = bundle.instance.vote_commitment.to_repr();
        buf32[..vc.as_ref().len().min(32)]
            .copy_from_slice(&vc.as_ref()[..vc.as_ref().len().min(32)]);
        canonical.extend_from_slice(&buf32);
        let mut pid_buf = [0u8; 32];
        pid_buf[..4].copy_from_slice(&1u32.to_le_bytes());
        canonical.extend_from_slice(&pid_buf);
        let mut ah_buf = [0u8; 32];
        ah_buf[..8].copy_from_slice(&(anchor_height as u64).to_le_bytes());
        canonical.extend_from_slice(&ah_buf);
        let h = blake2b_simd::Params::new().hash_length(32).hash(&canonical);
        let mut out = [0u8; 32];
        out.copy_from_slice(h.as_bytes());
        out
    };
    let sig = rsk.sign(&mut rng, &sighash);
    let sig_bytes: [u8; 64] = (&sig).into();

    let payload = payloads::cast_vote_payload_real(
        &round_id.to_repr(),
        anchor_height,
        &bundle.instance.van_nullifier.to_repr(),
        &bundle.instance.vote_authority_note_new.to_repr(),
        &bundle.instance.vote_commitment.to_repr(),
        1,
        &bundle.proof,
        &bundle.r_vpk_bytes,
        &sig_bytes,
    );

    let round_id_hex = hex::encode(round_id.to_repr());
    let shares_hash_b64 = B64.encode(bundle.shares_hash.to_repr());
    let share_payloads: Vec<serde_json::Value> = (0..16u32)
        .map(|share_idx| {
            let es = &bundle.encrypted_shares[share_idx as usize];
            serde_json::json!({
                "shares_hash": shares_hash_b64,
                "proposal_id": 1,
                "vote_decision": 1,
                "enc_share": {
                    "c1": B64.encode(&es.c1),
                    "c2": B64.encode(&es.c2),
                    "share_index": share_idx,
                },
                "share_index": share_idx,
                "tree_position": 0, // filled at runtime after position resolution
                "vote_round_id": round_id_hex,
                "share_comms": bundle.share_comms.iter().map(|c| B64.encode(c.to_repr())).collect::<Vec<_>>(),
                "primary_blind": B64.encode(bundle.share_blinds[share_idx as usize].to_repr()),
            })
        })
        .collect();

    CastVoteBundle { payload, share_payloads }
}

#[test]
#[ignore = "requires running chain + pre-generated fixtures"]
fn voter_throughput_stress() {
    let fixture_dir = PathBuf::from(
        std::env::var("VOTER_FIXTURE_DIR").expect("VOTER_FIXTURE_DIR must be set"),
    );
    let workers = concurrency();
    let timeout = phase_timeout();
    let w_interval = wave_interval();
    let share_stall = share_stall_timeout();

    // -----------------------------------------------------------------------
    // Load fixtures
    // -----------------------------------------------------------------------
    eprintln!("\n--- Loading fixtures ---");
    let voter_cap: Option<usize> = std::env::var("STRESS_VOTER_COUNT")
        .ok()
        .and_then(|v| v.parse().ok());
    let (manifest, delegations, cv_inputs) = load_fixtures(&fixture_dir, voter_cap);
    let count = manifest.count;
    let w_size = wave_size();
    eprintln!(
        "Loaded {} delegations + {} cast-vote inputs from fixtures",
        count, count
    );

    eprintln!("\n=== Voter Throughput Stress Test (Full Pipeline) ===");
    eprintln!("  Voters:          {count}");
    eprintln!("  Wave size:       {w_size} voters ({} shares)", w_size * 16);
    eprintln!("  Wave interval:   {}ms", w_interval.as_millis());
    eprintln!("  Phase timeout:   {:.0}s", timeout.as_secs_f64());
    eprintln!(
        "  Share stall:     {}",
        share_stall
            .map(|d| format!("{}s", d.as_secs()))
            .unwrap_or_else(|| "disabled".to_string())
    );

    let round_id = b64_decode(&manifest.round_id_b64);
    let round_id_hex = hex::encode(&round_id);

    // -----------------------------------------------------------------------
    // Setup: create round on-chain with matching round_fields
    // -----------------------------------------------------------------------
    eprintln!("\n--- Setup: creating voting round on-chain ---");
    ensure_pallas_key_registered();

    let config = default_cosmos_tx_config();
    import_hex_key("vote-manager", VOTE_MANAGER_PRIVKEY_HEX, &config.home_dir);

    let rf = &manifest.round_fields;
    let setup_round_fields = e2e_tests::payloads::SetupRoundFields {
        snapshot_height: rf.snapshot_height,
        snapshot_blockhash: b64_decode(&rf.snapshot_blockhash).try_into().unwrap(),
        proposals_hash: b64_decode(&rf.proposals_hash).try_into().unwrap(),
        vote_end_time: rf.vote_end_time,
        nullifier_imt_root: b64_decode(&rf.nullifier_imt_root).try_into().unwrap(),
        nc_root: b64_decode(&rf.nc_root).try_into().unwrap(),
    };

    let (mut body, _, _derived_round_id) =
        create_voting_session_payload(VOTE_MANAGER_ADDRESS, 600, Some(setup_round_fields));
    body["@type"] = serde_json::json!("/zvote.v1.MsgCreateVotingSession");

    let vm_config = CosmosTxConfig {
        key_name: "vote-manager".to_string(),
        home_dir: config.home_dir.clone(),
        chain_id: config.chain_id.clone(),
        node_url: config.node_url.clone(),
    };
    let (status, json) =
        broadcast_cosmos_msg(&body, &vm_config).expect("broadcast create-voting-session");
    let code = json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1);
    assert!(
        status == 200 && code == 0,
        "create session failed: status={status}, code={code}, log={:?}",
        json.get("log")
    );

    eprintln!("Waiting for round {} to become ACTIVE...", &round_id_hex);
    wait_for_round_status(&round_id_hex, SESSION_STATUS_ACTIVE, 90_000, 2_000)
        .expect("round should become ACTIVE");
    eprintln!("Round ACTIVE");

    let overall_start = Instant::now();
    let collector = Arc::new(MetricsCollector::new());
    let pre_blast_index = commitment_tree_next_index().expect("query initial tree size");
    assert_eq!(
        pre_blast_index, 0,
        "voter_throughput requires a fresh chain with an empty commitment tree, got next_index={pre_blast_index}"
    );

    // -----------------------------------------------------------------------
    // Phase 1: Delegations (sequential to preserve fixture tree ordering)
    // -----------------------------------------------------------------------
    eprintln!("\n--- Phase 1: Submitting {} delegations (sequential for tree ordering) ---", count);
    let phase1_submit_start = Instant::now();

    let deleg_payloads: Vec<(usize, serde_json::Value)> = delegations
        .into_iter()
        .enumerate()
        .map(|(i, d)| (i, d.payload))
        .collect();

    let deleg_result = submit_concurrent(
        deleg_payloads,
        "/zally/v1/delegate-vote",
        "delegation",
        &collector,
        1, // sequential: preserve fixture tree leaf ordering
    );
    let deleg_ok = deleg_result.succeeded;
    let phase1_submit_elapsed = phase1_submit_start.elapsed();
    eprintln!(
        "Phase 1 submit: {deleg_ok}/{count} delegations in {:.1}s",
        phase1_submit_elapsed.as_secs_f64()
    );

    let expected_after_deleg = count as u64;
    wait_for_tree_size(expected_after_deleg, timeout);

    // -----------------------------------------------------------------------
    // Phase 2: Verify tree + generate cast-vote proofs with real anchor height
    // -----------------------------------------------------------------------
    eprintln!("\n--- Phase 2: Verifying tree state + generating cast-vote proofs ---");
    std::thread::sleep(Duration::from_secs(6));
    let (on_chain_height, on_chain_root, tree_size) =
        commitment_tree_latest().expect("query tree latest");
    eprintln!(
        "Tree next_index: {} (expected: {}), height: {}, root: {}",
        tree_size, expected_after_deleg, on_chain_height, &on_chain_root[..20]
    );
    assert_eq!(
        tree_size, expected_after_deleg,
        "tree size {} did not match expected {} on fresh chain",
        tree_size,
        expected_after_deleg
    );
    if manifest.expected_tree_after_delegations.next_index == count as u64 {
        assert_eq!(
            on_chain_root,
            manifest.expected_tree_after_delegations.root_b64,
            "on-chain tree root after delegations did not match fixture manifest"
        );
    }

    let round_id_fp: pallas::Base = {
        let bytes: [u8; 32] = b64_decode(&manifest.round_id_b64).try_into().unwrap();
        Option::from(pallas::Base::from_repr(bytes)).unwrap()
    };
    let ea_pk_bytes = get_round_ea_pk(&round_id_hex)
        .expect("ACTIVE round should have ea_pk from ceremony");
    let ea_pk_arr: [u8; 32] = ea_pk_bytes.try_into().expect("ea_pk must be 32 bytes");
    let ea_pk: pallas::Affine = Option::from(pallas::Affine::from_bytes(&ea_pk_arr))
        .expect("ea_pk decompression");
    eprintln!("EA public key from round: {}", hex::encode(&ea_pk_arr));

    let anchor_height = on_chain_height as u32;
    let proof_threads: usize = std::env::var("PROOF_GEN_THREADS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(4);
    eprintln!(
        "Generating {} cast-vote proofs with anchor_height={}, threads={}...",
        count, anchor_height, proof_threads
    );
    let phase2_proof_start = Instant::now();

    let completed = Arc::new(AtomicUsize::new(0));
    let total_proofs = count;
    let mut cv_bundles: Vec<(usize, CastVoteBundle)> = Vec::with_capacity(count);
    let mut remaining: Vec<(usize, CastVoteInputFixture)> =
        cv_inputs.into_iter().enumerate().collect();

    while !remaining.is_empty() {
        let chunk_size = proof_threads.min(remaining.len());
        let chunk: Vec<(usize, CastVoteInputFixture)> = remaining.drain(..chunk_size).collect();
        let handles: Vec<_> = chunk
            .into_iter()
            .map(|(idx, input)| {
                let round_id_fp = round_id_fp;
                let ea_pk = ea_pk;
                let completed = Arc::clone(&completed);
                std::thread::spawn(move || {
                    let bundle = generate_cast_vote(&input, &round_id_fp, anchor_height, ea_pk);
                    let done = completed.fetch_add(1, Ordering::Relaxed) + 1;
                    if done % 10 == 0 || done == total_proofs {
                        eprintln!("[cast-vote-gen] {done}/{total_proofs} proofs complete");
                    }
                    (idx, bundle)
                })
            })
            .collect();
        for h in handles {
            cv_bundles.push(h.join().expect("proof gen thread panicked"));
        }
    }
    cv_bundles.sort_by_key(|(idx, _)| *idx);
    let phase2_proof_elapsed = phase2_proof_start.elapsed();
    eprintln!(
        "Phase 2 proof gen: {} proofs in {:.1}s ({:.1}s/proof avg)",
        count,
        phase2_proof_elapsed.as_secs_f64(),
        phase2_proof_elapsed.as_secs_f64() / count as f64
    );

    // -----------------------------------------------------------------------
    // Phase 3: Cast-votes (submit all concurrently, wait for on-chain)
    // -----------------------------------------------------------------------
    eprintln!("\n--- Phase 3: Submitting {} cast-votes ---", count);
    let phase3_submit_start = Instant::now();

    let cast_payloads: Vec<(usize, serde_json::Value)> = cv_bundles
        .iter()
        .map(|(idx, b)| (*idx, b.payload.clone()))
        .collect();

    let cast_result = submit_concurrent(
        cast_payloads,
        "/zally/v1/cast-vote",
        "cast_vote",
        &collector,
        1, // sequential: deterministic tree ordering for position computation
    );
    let cast_ok = cast_result.succeeded;
    let phase3_submit_elapsed = phase3_submit_start.elapsed();
    eprintln!(
        "Phase 3 submit: {cast_ok}/{count} cast-votes in {:.1}s",
        phase3_submit_elapsed.as_secs_f64()
    );

    let expected_after_cast = expected_after_deleg + (count as u64) * 2;
    wait_for_tree_size(expected_after_cast, timeout);

    // -----------------------------------------------------------------------
    // Phase 3b: Compute tree positions from sequential ordering
    // -----------------------------------------------------------------------
    // Delegations appended van_cmx at positions [0, count).
    // Cast-votes (sequential) append vote_authority_note_new and vote_commitment:
    //   voter i → VAN_new at count+2*i, VC at count+2*i+1
    let positions: HashMap<usize, u64> = (0..count)
        .map(|i| (i, expected_after_deleg + 2 * i as u64 + 1))
        .collect();
    eprintln!(
        "Computed {} deterministic tree positions (sequential cast-vote ordering)",
        positions.len()
    );

    // -----------------------------------------------------------------------
    // Phase 4: Share reveals — wave-based submission to helper server
    // -----------------------------------------------------------------------
    let total_shares_expected = positions.len() * 16;
    eprintln!(
        "\n--- Phase 4: Submitting share payloads ({} voters × 16 = {} shares, wave_size={}) ---",
        positions.len(),
        total_shares_expected,
        w_size
    );
    let phase4_start = Instant::now();

    let mut voters_with_positions: Vec<(usize, u64)> = positions.into_iter().collect();
    voters_with_positions.sort_by_key(|(idx, _)| *idx);

    let mut total_enqueued = 0usize;
    let mut wave_num = 0usize;
    let mut wave_timings: Vec<(usize, Duration, usize)> = Vec::new();

    for wave_voters in voters_with_positions.chunks(w_size) {
        wave_num += 1;
        let wave_start = Instant::now();

        let mut wave_payloads = Vec::new();
        for (voter_idx, vc_position) in wave_voters {
            if let Some((_, bundle)) = cv_bundles.iter().find(|(idx, _)| idx == voter_idx) {
                for mut share_payload in bundle.share_payloads.clone() {
                    share_payload["tree_position"] = serde_json::json!(*vc_position);
                    wave_payloads.push(share_payload);
                }
            }
        }

        let wave_count = wave_payloads.len();
        let wave_ok;
        let wave_fail;

        let queue = Arc::new(std::sync::Mutex::new(wave_payloads));
        let wave_succeeded = Arc::new(AtomicUsize::new(0));
        let share_err_logged = Arc::new(AtomicUsize::new(0));

        let handles: Vec<_> = (0..workers.min(wave_count))
            .map(|_| {
                let queue = Arc::clone(&queue);
                let wave_succeeded = Arc::clone(&wave_succeeded);
                let share_err_logged = Arc::clone(&share_err_logged);
                let collector = Arc::clone(&collector);
                std::thread::spawn(move || {
                    loop {
                        let payload = {
                            let mut q = queue.lock().unwrap();
                            q.pop()
                        };
                        let payload = match payload {
                            Some(p) => p,
                            None => break,
                        };

                        let start = Instant::now();
                        let mut http_status = 0u16;
                        let mut success = false;
                        for retry in 0..3u32 {
                            if retry > 0 {
                                std::thread::sleep(Duration::from_millis(500 * retry as u64));
                            }
                            match post_helper_json("/api/v1/shares", &payload) {
                                Ok((status, ref json)) => {
                                    http_status = status;
                                    if status == 200 {
                                        success = true;
                                        break;
                                    }
                                    let logged = share_err_logged.fetch_add(1, Ordering::Relaxed);
                                    if logged < 5 {
                                        eprintln!("[share] HTTP {} (attempt {}): {:?}", status, retry + 1, json);
                                    }
                                }
                                Err(ref e) => {
                                    let logged = share_err_logged.fetch_add(1, Ordering::Relaxed);
                                    if logged < 5 {
                                        eprintln!("[share] error (attempt {}): {}", retry + 1, e);
                                    }
                                }
                            }
                        }
                        let latency = start.elapsed();

                        collector.record(Sample {
                            phase: "share_enqueue".to_string(),
                            timestamp: start,
                            latency,
                            http_status,
                            success,
                        });

                        if success {
                            wave_succeeded.fetch_add(1, Ordering::Relaxed);
                        }
                    }
                })
            })
            .collect();

        for h in handles {
            h.join().expect("share worker panicked");
        }

        wave_ok = wave_succeeded.load(Ordering::Relaxed);
        wave_fail = wave_count - wave_ok;
        total_enqueued += wave_ok;
        let wave_elapsed = wave_start.elapsed();
        wave_timings.push((wave_num, wave_elapsed, wave_ok));

        eprintln!(
            "[wave {wave_num}] {wave_ok}/{wave_count} shares enqueued in {:.1}s ({wave_fail} failed)",
            wave_elapsed.as_secs_f64()
        );

        if !wave_voters.is_empty()
            && voters_with_positions
                .chunks(w_size)
                .nth(wave_num)
                .is_some()
        {
            std::thread::sleep(w_interval);
        }
    }

    let phase4_elapsed = phase4_start.elapsed();
    eprintln!(
        "Phase 4 complete: {total_enqueued}/{total_shares_expected} shares enqueued in {wave_num} waves ({:.1}s)",
        phase4_elapsed.as_secs_f64()
    );
    assert!(
        total_enqueued > 0,
        "helper accepted zero shares; aborting before long share-processing wait"
    );

    // -----------------------------------------------------------------------
    // Phase 5: Monitor helper processing
    // -----------------------------------------------------------------------
    eprintln!("\n--- Phase 5: Monitoring share processing ---");
    let phase5_start = Instant::now();
    let mut last_queue_status = HelperQueueStatus::default();
    let mut last_terminal_count = 0u64;
    let mut last_progress_time = Instant::now();
    let mut last_log_time = Instant::now();
    let final_queue_status: HelperQueueStatus;

    loop {
        if let Some(status) = get_helper_queue_status(&round_id_hex) {
            let terminal = status.submitted + status.failed;
            if terminal > last_terminal_count {
                last_terminal_count = terminal;
                last_progress_time = Instant::now();
            }

            if status.submitted != last_queue_status.submitted
                || status.failed != last_queue_status.failed
                || status.pending != last_queue_status.pending
                || last_log_time.elapsed() > Duration::from_secs(30)
            {
                let elapsed = phase5_start.elapsed();
                let rate = if elapsed.as_secs_f64() > 0.0 {
                    status.submitted as f64 / elapsed.as_secs_f64()
                } else {
                    0.0
                };
                eprintln!(
                    "[shares] terminal {terminal}/{total_enqueued} (submitted={}, failed={}, pending={}) ({:.1} submitted/sec, {:.0}s elapsed)",
                    status.submitted,
                    status.failed,
                    status.pending,
                    rate,
                    elapsed.as_secs_f64()
                );
                last_log_time = Instant::now();
            }

            last_queue_status = status;
            if terminal >= total_enqueued as u64 {
                eprintln!(
                    "[shares] all queued shares reached terminal state in {:.1}s (submitted={}, failed={})",
                    phase5_start.elapsed().as_secs_f64(),
                    last_queue_status.submitted,
                    last_queue_status.failed
                );
                final_queue_status = last_queue_status.clone();
                break;
            }
        } else if last_log_time.elapsed() > Duration::from_secs(30) {
            eprintln!("[shares] queue-status unavailable; retrying...");
            last_log_time = Instant::now();
        }

        if let Some(stall_timeout) = share_stall {
            if last_progress_time.elapsed() > stall_timeout {
                panic!(
                    "share processing stalled for {:.0}s with submitted={}, failed={}, pending={} (target terminal count={})",
                    last_progress_time.elapsed().as_secs_f64(),
                    last_queue_status.submitted,
                    last_queue_status.failed,
                    last_queue_status.pending,
                    total_enqueued
                );
            }
        }

        std::thread::sleep(Duration::from_secs(5));
    }

    let phase5_elapsed = phase5_start.elapsed();
    let final_share_count = final_queue_status.submitted;
    let final_failed_share_count = final_queue_status.failed;

    // -----------------------------------------------------------------------
    // Final verification
    // -----------------------------------------------------------------------
    eprintln!("\n--- Final verification ---");
    let validators = get_all_validator_operator_addresses().unwrap_or_default();
    let final_tree_size = commitment_tree_next_index().unwrap_or(0);
    let overall_elapsed = overall_start.elapsed();

    // -----------------------------------------------------------------------
    // Comprehensive report
    // -----------------------------------------------------------------------
    let all_samples = collector.snapshot();
    let total_wall = collector.wall_time();
    let agg = metrics::compute_aggregate(&all_samples, total_wall);

    let artifacts_dir = std::path::PathBuf::from("artifacts/voter-throughput");
    let _ = metrics::write_report(&agg, &artifacts_dir);

    fn fmt_dur(secs: f64) -> String {
        if secs >= 60.0 {
            format!("{:.1}m", secs / 60.0)
        } else {
            format!("{:.1}s", secs)
        }
    }

    let sep = "═══════════════════════════════════════════════════════════════════";
    eprintln!("\n{sep}");
    eprintln!(
        "  STRESS TEST REPORT: {} VOTERS (real ZKP #1/#2/#3, server load)",
        count
    );
    eprintln!("{sep}");

    eprintln!("\n  Configuration");
    eprintln!("  ─────────────────────────────────────────────────────────────");
    eprintln!("  Voters:              {}", count);
    eprintln!(
        "  Validators:          {} ({})",
        validators.len(),
        validators.join(", ")
    );
    eprintln!(
        "  Tree leaves:         {} (n + 2n cast-vote leaves)",
        final_tree_size
    );
    eprintln!("  ZKP mode:            REAL Halo2 (K=14 deleg, K=13 cast-vote, K=11 share-reveal)");
    eprintln!("  Share arrival:       {} voters every {}ms", w_size, w_interval.as_millis());
    eprintln!(
        "  Chain:               live CometBFT ({} validator{})",
        validators.len(),
        if validators.len() != 1 { "s" } else { "" }
    );

    let server_active_time = phase1_submit_elapsed.as_secs_f64()
        + phase3_submit_elapsed.as_secs_f64()
        + phase5_elapsed.as_secs_f64();
    let per_share_ms = if final_share_count > 0 {
        phase5_elapsed.as_secs_f64() * 1000.0 / final_share_count as f64
    } else {
        0.0
    };

    eprintln!("\n  Server Load Timings (what the chain actually processes)");
    eprintln!("  ─────────────────────────────────────────────────────────────");
    eprintln!(
        "  {:46} {:>10} {:>10}",
        "Phase", "Duration", "tx/s"
    );
    eprintln!(
        "  {:46} {:>10} {:>10}",
        "─────", "────────", "────"
    );
    eprintln!(
        "  {:46} {:>10} {:>9.0}",
        format!("1. Submit {} delegations (ZKP #1 verify, {} ok)", count, deleg_ok),
        fmt_dur(phase1_submit_elapsed.as_secs_f64()),
        deleg_ok as f64 / phase1_submit_elapsed.as_secs_f64()
    );
    eprintln!(
        "  {:46} {:>10} {:>9.0}",
        format!("2. Submit {} cast-votes (ZKP #2 verify, {} ok)", count, cast_ok),
        fmt_dur(phase3_submit_elapsed.as_secs_f64()),
        cast_ok as f64 / phase3_submit_elapsed.as_secs_f64()
    );
    eprintln!(
        "  {:46} {:>10} {:>9.1}",
        format!(
            "3. ZKP #3 gen + submit ({}/{} shares)",
            final_share_count, total_shares_expected
        ),
        fmt_dur(phase5_elapsed.as_secs_f64()),
        final_share_count as f64 / phase5_elapsed.as_secs_f64()
    );
    eprintln!(
        "  {:46} {:>7.0}ms {:>10}",
        "   └ per share (ZKP #3 prove + verify + tx)",
        per_share_ms,
        "—"
    );
    eprintln!(
        "  {:46} {:>10} {:>10}",
        "SERVER ACTIVE TIME",
        fmt_dur(server_active_time),
        "—"
    );
    eprintln!(
        "  {:46} {:>10} {:>10}",
        "WALL CLOCK (incl. ZKP #2 generation)",
        fmt_dur(overall_elapsed.as_secs_f64()),
        "—"
    );

    eprintln!("\n  Client-Side Prep (not server load)");
    eprintln!("  ─────────────────────────────────────────────────────────────");
    eprintln!(
        "  ZKP #1 proofs:       pre-generated offline (K=14, ~6s/proof)"
    );
    eprintln!(
        "  ZKP #2 proofs:       {} generated at runtime in {} ({:.1}s/proof avg, K=13)",
        count,
        fmt_dur(phase2_proof_elapsed.as_secs_f64()),
        phase2_proof_elapsed.as_secs_f64() / count as f64
    );

    eprintln!(
        "\n  Per-Tx Latency (HTTP → CheckTx → ZKP verify → response)"
    );
    eprintln!("  ─────────────────────────────────────────────────────────────");
    eprintln!(
        "  {:15} {:>8} {:>8} {:>8} {:>8} {:>8} {:>8}  {}",
        "Phase", "ok/total", "p50", "p95", "p99", "max", "min", "ZKP"
    );
    eprintln!(
        "  {:15} {:>8} {:>8} {:>8} {:>8} {:>8} {:>8}  {}",
        "─────", "────────", "───", "───", "───", "───", "───", "───"
    );
    let zkp_labels = ["#1 K=14", "#2 K=13", "—"];
    for (i, p) in agg.phases.iter().enumerate() {
        let zkp = zkp_labels.get(i).unwrap_or(&"—");
        eprintln!(
            "  {:15} {:>4}/{:<4} {:>6.0}ms {:>6.0}ms {:>6.0}ms {:>6.0}ms {:>6.0}ms  {}",
            p.phase,
            p.succeeded,
            p.total_submitted,
            p.latency_p50_ms,
            p.latency_p95_ms,
            p.latency_p99_ms,
            p.latency_max_ms,
            p.latency_min_ms,
            zkp
        );
    }

    eprintln!("\n  Success Rates");
    eprintln!("  ─────────────────────────────────────────────────────────────");
    for p in &agg.phases {
        let pct = p.success_rate * 100.0;
        let symbol = if pct >= 95.0 { "✓" } else { "✗" };
        eprintln!(
            "  {}: {}/{} ({:.1}%) {}",
            p.phase, p.succeeded, p.total_submitted, pct, symbol
        );
    }

    eprintln!("\n  Share Processing Detail (ZKP #3, K=11)");
    eprintln!("  ─────────────────────────────────────────────────────────────");
    eprintln!("  Shares enqueued:     {}", total_shares_expected);
    eprintln!("  Shares processed:    {} (ZKP #3 generated + verified on-chain)", final_share_count);
    eprintln!("  Shares failed:       {}", final_failed_share_count);
    eprintln!(
        "  Throughput:          {:.2} shares/sec",
        if phase5_elapsed.as_secs_f64() > 0.0 {
            final_share_count as f64 / phase5_elapsed.as_secs_f64()
        } else {
            0.0
        }
    );
    eprintln!("  Per-share cost:      {:.0}ms (ZKP #3 prove + MsgRevealShare verify + tx)", per_share_ms);
    eprintln!("  Concurrent provers:  16 (max_concurrent_proofs)");
    eprintln!("  Arrival pattern:     {} voters every {}ms ({} waves)", w_size, w_interval.as_millis(), wave_num);

    eprintln!("\n  Tree Verification");
    eprintln!("  ─────────────────────────────────────────────────────────────");
    eprintln!("  Final tree size:     {}", final_tree_size);
    eprintln!("  Expected tree size:  {}", expected_after_cast);
    eprintln!(
        "  Match:               {}",
        if final_tree_size == expected_after_cast {
            "✓"
        } else {
            "✗"
        }
    );

    eprintln!("\n{sep}");
    let min_voter_successes = min_successes(count);
    let min_share_enqueue_successes = min_successes(total_shares_expected);
    let min_share_submit_successes = min_successes(total_enqueued);
    let pass = deleg_ok >= min_voter_successes
        && cast_ok >= min_voter_successes
        && total_enqueued >= min_share_enqueue_successes
        && final_share_count as usize >= min_share_submit_successes
        && final_tree_size == expected_after_cast;
    eprintln!(
        "  {}: {} voters, real ZKP #1/#2/#3, multi-validator",
        if pass { "PASSED" } else { "FAILED" },
        count
    );
    eprintln!("{sep}");

    assert!(
        deleg_ok >= min_voter_successes,
        "delegation success rate {}/{} < 95%",
        deleg_ok,
        count,
    );
    assert!(
        cast_ok >= min_voter_successes,
        "cast_vote success rate {}/{} < 95%",
        cast_ok,
        count,
    );
    assert!(
        total_enqueued >= min_share_enqueue_successes,
        "share enqueue success rate {}/{} < 95%",
        total_enqueued,
        total_shares_expected,
    );
    assert!(
        final_share_count as usize >= min_share_submit_successes,
        "share processing success rate {}/{} < 95%",
        final_share_count,
        total_enqueued,
    );
    assert_eq!(
        final_tree_size, expected_after_cast,
        "unexpected final tree size on fresh chain"
    );
}
