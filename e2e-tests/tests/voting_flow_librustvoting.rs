//! E2E test exercising the **librustvoting** path: VotingDb → TreeClient →
//! real ZKP #2 → chain verification → share payloads.
//!
//! Unlike voting_flow.rs which calls the orchard builder directly, this test
//! validates that the full library stack works: DB persistence of delegation
//! data, HTTP tree sync, witness generation, and proof generation all through
//! the librustvoting / vote-commitment-tree-client APIs.

use base64::Engine;
use e2e_tests::{
    api::{
        self, commitment_tree_next_index, get_json, post_json, post_json_accept_committed,
        tally_has_proposal, wait_for_round_status, SESSION_STATUS_TALLYING,
    },
    elgamal,
    payloads::{
        cast_vote_payload_real, create_voting_session_payload, delegate_vote_payload,
        reveal_share_payload,
    },
    setup::build_delegation_bundle_for_test,
};
use ff::PrimeField;
use librustvoting::{NoopProgressReporter, VotingRoundParams};
use rand::SeedableRng;
use rand_chacha::ChaCha20Rng;
use vote_commitment_tree::TreeClient;
use vote_commitment_tree_client::http_sync_api::HttpTreeSyncApi;

const BLOCK_WAIT_MS: u64 = 6000;

fn log_step(step: &str, msg: &str) {
    eprintln!("[E2E-lib] {}: {}", step, msg);
}

fn block_wait() {
    std::thread::sleep(std::time::Duration::from_millis(BLOCK_WAIT_MS));
}

/// E2E test: delegation → tree sync → ZKP #2 → cast-vote → share payloads,
/// all through the librustvoting VotingDb + vote-commitment-tree-client path.
#[test]
#[ignore = "requires running chain: make init && make start"]
fn voting_flow_librustvoting_path() {
    // ---- Setup: derive SpendingKey from seed (same ZIP-32 path as production) ----
    log_step("Setup", "deriving SpendingKey from hotkey seed via ZIP-32...");
    let seed = [0x42u8; 64];
    let sk = librustvoting::zkp2::derive_spending_key(&seed, 1)
        .expect("derive_spending_key from seed");

    let mut rng = ChaCha20Rng::seed_from_u64(43);
    let (_elgamal_sk, elgamal_pk) = elgamal::keygen(&mut rng);
    let ea_pk_bytes = elgamal::marshal_public_key(&elgamal_pk);

    // Build delegation bundle using the seed-derived SpendingKey
    log_step(
        "Setup",
        "building delegation bundle with seed-derived key (K=14 proof, 30-60s)...",
    );
    let (delegation_bundle, session_fields, vote_proof_data) =
        build_delegation_bundle_for_test(Some(sk)).expect("build_delegation_bundle_for_test");
    log_step("Setup", "delegation bundle ready");

    // Save fields we need for DB before session_fields is consumed
    let fields_for_db = session_fields.clone();
    let (body, _, round_id) =
        create_voting_session_payload(&ea_pk_bytes, 120, Some(session_fields));
    let round_id_hex = hex::encode(&round_id);

    // ---- Step 1: Create voting session ----
    log_step("Step 1", "create voting session");
    let (status, json) =
        post_json("/zally/v1/create-voting-session", &body).expect("POST create-voting-session");
    assert_eq!(status, 200, "create session: HTTP {}, body={:?}", status, json);
    assert_eq!(
        json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1),
        0,
        "create session rejected: {:?}",
        json.get("log")
    );
    block_wait();

    // ---- Step 2: Delegate vote (real ZKP #1) ----
    log_step("Step 2", "delegate vote (ZKP #1)");
    let deleg_body = delegate_vote_payload(&round_id, &delegation_bundle);
    let (status, json) = post_json_accept_committed(
        "/zally/v1/delegate-vote",
        &deleg_body,
        || commitment_tree_next_index().map(|n| n >= 2).unwrap_or(false),
    )
    .expect("POST delegate-vote");
    assert_eq!(status, 200, "delegate-vote: HTTP {}, body={:?}", status, json);
    assert_eq!(
        json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1),
        0,
        "delegation rejected: {:?}",
        json.get("log")
    );
    block_wait();

    // ---- Step 3: Wait for tree to have root after delegation ----
    log_step("Step 3", "waiting for commitment tree (2 leaves)");
    let mut anchor_height: u32 = 0;
    for _ in 0..10 {
        let (status, json) =
            get_json("/zally/v1/commitment-tree/latest").expect("GET tree latest");
        assert_eq!(status, 200);
        if let Some(tree) = json.get("tree") {
            let h = tree.get("height").and_then(|x| x.as_u64()).unwrap_or(0) as u32;
            if h > 0 {
                anchor_height = h;
                assert!(tree.get("root").is_some());
                assert!(
                    tree.get("next_index")
                        .and_then(|x| x.as_u64())
                        .unwrap_or(0)
                        >= 2
                );
                break;
            }
        }
        std::thread::sleep(std::time::Duration::from_secs(2));
    }
    assert!(anchor_height > 0, "tree never populated after delegation");

    // ---- Step 4: Create VotingDb and persist delegation data ----
    log_step("Step 4", "creating VotingDb, persisting delegation data");
    let db = librustvoting::storage::VotingDb::open(":memory:").expect("open VotingDb");
    db.init_round(
        &VotingRoundParams {
            vote_round_id: round_id_hex.clone(),
            snapshot_height: fields_for_db.snapshot_height,
            ea_pk: ea_pk_bytes.to_vec(),
            nc_root: fields_for_db.nc_root.to_vec(),
            nullifier_imt_root: fields_for_db.nullifier_imt_root.to_vec(),
        },
        None,
    )
    .expect("init_round");

    // Store the fields ZKP #2 needs: gov_comm_rand, total_note_value, address_index.
    // Other store_delegation_data fields (rho_signed, alpha, etc.) are only needed
    // for delegation proof reconstruction, not ZKP #2.
    {
        let conn = db.conn();
        librustvoting::storage::queries::store_delegation_data(
            &conn,
            &round_id_hex,
            vote_proof_data.gov_comm_rand.to_repr().as_ref(),
            &[],          // dummy_nullifiers (not needed for ZKP #2)
            &[0u8; 32],   // rho_signed
            &[],          // padded_cmx
            &[0u8; 32],   // nf_signed
            &delegation_bundle.cmx_new,
            &[0u8; 32],   // alpha
            &[0u8; 32],   // rseed_signed
            &[0u8; 32],   // rseed_output
            &delegation_bundle.gov_comm,
            vote_proof_data.total_note_value,
            1, // address_index (matches delegation output_recipient = fvk.address_at(1, External))
        )
        .expect("store_delegation_data");
    }

    // VAN is at position 1 in the commitment tree (cmx_new=0, gov_comm=1)
    db.store_van_position(&round_id_hex, 1)
        .expect("store_van_position");

    // ---- Step 5: Sync tree via TreeClient + HttpTreeSyncApi ----
    log_step("Step 5", "syncing vote commitment tree from chain");
    let base_url = api::base_url();
    let mut tree_client = TreeClient::empty();
    tree_client.mark_position(1); // VAN at position 1
    let sync_api = HttpTreeSyncApi::new(&base_url);
    tree_client
        .sync(&sync_api)
        .expect("TreeClient sync from chain");
    assert!(tree_client.size() >= 2, "tree should have >= 2 leaves after sync");
    log_step(
        "Step 5",
        &format!(
            "synced {} leaves, last height {}",
            tree_client.size(),
            tree_client.last_synced_height().unwrap_or(0)
        ),
    );

    // ---- Step 6: Generate VAN witness ----
    log_step("Step 6", "generating VAN witness at position 1");
    let witness = tree_client
        .witness(1, anchor_height)
        .expect("generate VAN witness");
    assert_eq!(witness.position(), 1);

    // Verify local root matches on-chain root
    let local_root = tree_client
        .root_at_height(anchor_height)
        .expect("local root at anchor height");
    {
        let (status, json) = get_json(&format!("/zally/v1/commitment-tree/{}", anchor_height))
            .expect("GET tree at height");
        assert_eq!(status, 200);
        let on_chain_root_b64 = json
            .get("tree")
            .and_then(|t| t.get("root"))
            .and_then(|r| r.as_str())
            .expect("on-chain tree root");
        let on_chain_root_bytes = base64::engine::general_purpose::STANDARD
            .decode(on_chain_root_b64)
            .expect("decode on-chain root");
        let local_root_bytes = local_root.to_repr();
        assert_eq!(
            on_chain_root_bytes.as_slice(),
            &local_root_bytes[..],
            "TreeClient root does not match on-chain root"
        );
    }

    // Convert witness auth_path to byte arrays for build_vote_commitment
    let auth_path_bytes: Vec<[u8; 32]> = witness
        .auth_path()
        .iter()
        .map(|h| h.to_bytes())
        .collect();

    // ---- Step 7: Build vote commitment via VotingDb (real ZKP #2) ----
    log_step(
        "Step 7",
        "building vote commitment via VotingDb (K=14 proof, 30-60s)...",
    );
    let bundle = db
        .build_vote_commitment(
            &round_id_hex,
            &seed,
            1, // network_id (testnet)
            1, // proposal_id
            1, // choice (oppose)
            &auth_path_bytes,
            1,             // van_position
            anchor_height,
            &NoopProgressReporter,
        )
        .expect("VotingDb::build_vote_commitment");
    log_step("Step 7", "vote commitment built successfully");

    // Verify the bundle looks reasonable
    assert_eq!(bundle.van_nullifier.len(), 32);
    assert_eq!(bundle.vote_authority_note_new.len(), 32);
    assert_eq!(bundle.vote_commitment.len(), 32);
    assert_eq!(bundle.proposal_id, 1);
    assert!(!bundle.proof.is_empty());
    assert_eq!(bundle.enc_shares.len(), 4, "should have 4 encrypted shares");
    assert_eq!(bundle.shares_hash.len(), 32);

    // ---- Step 8: Submit cast-vote TX ----
    log_step("Step 8", "submitting cast-vote TX");
    let cast_body = cast_vote_payload_real(
        &round_id,
        anchor_height,
        &bundle.van_nullifier,
        &bundle.vote_authority_note_new,
        &bundle.vote_commitment,
        1, // proposal_id
        &bundle.proof,
    );

    let (status, json) = {
        let mut last = None;
        for attempt in 1..=3 {
            let result = post_json_accept_committed(
                "/zally/v1/cast-vote",
                &cast_body,
                || commitment_tree_next_index().map(|n| n >= 4).unwrap_or(false),
            )
            .expect("POST cast-vote");
            let code = result.1.get("code").and_then(|c| c.as_i64()).unwrap_or(-1);
            if result.0 == 200 && code == 0 {
                last = Some(result);
                break;
            }
            eprintln!(
                "[E2E-lib] Step 8 attempt {}: status={} code={} log={:?}",
                attempt,
                result.0,
                code,
                result.1.get("log").or(result.1.get("error"))
            );
            last = Some(result);
            if attempt < 3 {
                block_wait();
            }
        }
        last.expect("cast-vote: 3 attempts")
    };
    assert_eq!(status, 200, "cast-vote: HTTP {}, body={:?}", status, json);
    assert_eq!(
        json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1),
        0,
        "cast-vote rejected: code={:?} log={:?}",
        json.get("code"),
        json.get("log").or(json.get("error"))
    );
    block_wait();

    // ---- Step 9: Build share payloads via VotingDb ----
    log_step("Step 9", "building share payloads via VotingDb");
    // After cast-vote, tree has 4 leaves: cmx_new(0), gov_comm(1),
    // vote_authority_note_new(2), vote_commitment(3). VC is at position 3.
    let payloads = db
        .build_share_payloads(
            &bundle.enc_shares,
            &bundle,
            1, // vote_decision (oppose)
            3, // vc_tree_position
        )
        .expect("VotingDb::build_share_payloads");
    assert_eq!(payloads.len(), 4, "should have 4 share payloads");
    for (i, p) in payloads.iter().enumerate() {
        assert_eq!(p.shares_hash, bundle.shares_hash);
        assert_eq!(p.proposal_id, 1);
        assert_eq!(p.vote_decision, 1);
        assert_eq!(p.tree_position, 3);
        assert_eq!(p.enc_share.share_index, i as u32);
    }
    log_step("Step 9", "share payloads built and validated");

    // ---- Step 10: Submit reveal-share with first payload's enc_share ----
    log_step("Step 10", "submitting reveal-share from built payloads");

    // Get latest anchor height (tree now has 4 leaves after cast-vote)
    let (_, tree_json) =
        get_json("/zally/v1/commitment-tree/latest").expect("GET tree latest");
    let reveal_anchor = tree_json
        .get("tree")
        .and_then(|t| t.get("height"))
        .and_then(|h| h.as_u64())
        .unwrap_or(anchor_height as u64) as u32;

    // Convert the first share's ciphertext to base64 (c1 || c2, 64 bytes)
    let share = &payloads[0].enc_share;
    let enc_share_bytes: Vec<u8> = [share.c1.as_slice(), share.c2.as_slice()].concat();
    let enc_share_b64 =
        base64::engine::general_purpose::STANDARD.encode(&enc_share_bytes);

    let reveal_body = reveal_share_payload(&round_id, reveal_anchor, &enc_share_b64, 1, 1);
    let (status, json) = post_json_accept_committed(
        "/zally/v1/reveal-share",
        &reveal_body,
        || tally_has_proposal(&hex::encode(&round_id), 1),
    )
    .expect("POST reveal-share");
    assert_eq!(status, 200, "reveal-share: HTTP {}, body={:?}", status, json);
    assert_eq!(
        json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1),
        0,
        "reveal-share rejected: {:?}",
        json.get("log")
    );

    // ---- Step 11: Verify tally has the encrypted ciphertext ----
    log_step("Step 11", "verifying tally has ciphertext");
    block_wait();
    let (status, json) = get_json(&format!("/zally/v1/tally/{}/1", hex::encode(&round_id)))
        .expect("GET tally");
    assert_eq!(status, 200);
    let tally = json.get("tally").expect("tally");
    assert!(
        tally.get("1").is_some(),
        "tally should have entry for proposal 1"
    );

    // ---- Step 12: Wait for TALLYING ----
    log_step("Step 12", "waiting for TALLYING (up to 250s)");
    wait_for_round_status(
        &hex::encode(&round_id),
        SESSION_STATUS_TALLYING,
        250_000,
        3_000,
    )
    .expect("wait for TALLYING");

    log_step(
        "Done",
        "librustvoting path: VotingDb → TreeClient → ZKP #2 → chain ✓",
    );
}
