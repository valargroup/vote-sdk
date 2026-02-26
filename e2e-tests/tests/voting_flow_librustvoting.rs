//! E2E test exercising the full voting flow: VotingDb → TreeClient →
//! real ZKP #2 → cast-vote → helper (ZKP #3) → tally → finalize.
//!
//! Validates that the full library stack works: DB persistence of delegation
//! data, HTTP tree sync, witness generation, and proof generation all through
//! the librustvoting / vote-commitment-tree-client APIs.
//!
//! Share payloads are sent to the helper (integrated into zallyd), which
//! generates ZKP #3 and submits reveal-share TXs to the chain.

use base64::Engine;
use blake2b_simd::Params as Blake2bParams;
use e2e_tests::{
    api::{
        self, broadcast_cosmos_msg, commitment_tree_next_index, default_cosmos_tx_config,
        get_json, helper_server_url, import_hex_key,
        post_json_accept_committed, post_helper_json, tally_has_proposal,
        wait_for_round_status, SESSION_STATUS_ACTIVE, SESSION_STATUS_FINALIZED,
        SESSION_STATUS_TALLYING,
    },
    payloads::{
        cast_vote_payload_real, create_voting_session_payload, delegate_vote_payload,
        helper_share_payload,
    },
    setup::build_delegation_bundle_for_test,
};
use ff::{FromUniformBytes, PrimeField};
use group::{Curve, GroupEncoding};
use librustvoting::{NoopProgressReporter, VotingRoundParams};
use orchard::keys::SpendAuthorizingKey;
use pasta_curves::{arithmetic::CurveAffine, pallas};
use rand::SeedableRng;
use rand_chacha::ChaCha20Rng;
use vote_commitment_tree::TreeClient;
use vote_commitment_tree_client::http_sync_api::HttpTreeSyncApi;

const BLOCK_WAIT_MS: u64 = 6000;

/// Default vote manager secp256k1 private key (set in genesis).
const VOTE_MANAGER_PRIVKEY_HEX: &str =
    "b7e910eded435dd4e19c581b9a0b8e65104dcc4ebca8a1d55aa5c803e72ba2ee";
/// Bech32 address derived from VOTE_MANAGER_PRIVKEY_HEX with the "zvote" prefix.
const VOTE_MANAGER_ADDRESS: &str = "zvote15fjfr6rrs60vu4st6arrd94w5j6z7f6kxr92cg";

fn log_step(step: &str, msg: &str) {
    eprintln!("[E2E-lib] {}: {}", step, msg);
}

fn block_wait() {
    std::thread::sleep(std::time::Duration::from_millis(BLOCK_WAIT_MS));
}

/// E2E test: delegation → tree sync → ZKP #2 → cast-vote → helper server → tally,
/// all through the librustvoting VotingDb + vote-commitment-tree-client path.
#[test]
#[ignore = "requires running chain + helper server"]
fn voting_flow_librustvoting_path() {
    // ---- Setup: derive SpendingKey from seed (same ZIP-32 path as production) ----
    log_step(
        "Setup",
        "deriving SpendingKey from hotkey seed via ZIP-32...",
    );
    let seed = [0x42u8; 64];
    let sk =
        librustvoting::zkp2::derive_spending_key(&seed, 1).expect("derive_spending_key from seed");

    // ---- Step 0: Ensure Pallas key registered + import vote manager key ----
    e2e_tests::setup::ensure_pallas_key_registered();
    log_step("Step 0", "importing vote manager key into keyring...");
    let config = default_cosmos_tx_config();
    import_hex_key("vote-manager", VOTE_MANAGER_PRIVKEY_HEX, &config.home_dir);
    log_step("Step 0", "vote manager key imported ✓");

    let mut rng = ChaCha20Rng::seed_from_u64(43);

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
    let (mut body, _, round_id) =
        create_voting_session_payload(VOTE_MANAGER_ADDRESS, 120, Some(session_fields));
    let round_id_hex = hex::encode(&round_id);

    // ---- Step 1: Create voting session ----
    // MsgCreateVotingSession is a standard Cosmos SDK tx signed by the vote manager.
    log_step("Step 1", "create voting session (Cosmos SDK tx)");
    body["@type"] = serde_json::json!("/zvote.v1.MsgCreateVotingSession");
    let vm_config = e2e_tests::api::CosmosTxConfig {
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
    // Wait for the round to become ACTIVE. The round starts as PENDING and
    // transitions to ACTIVE once the per-round ceremony completes (auto-deal +
    // auto-ack via PrepareProposal, typically 2-3 blocks).
    log_step("Step 1b", &format!("waiting for round {} to become ACTIVE (per-round ceremony)...", &round_id_hex));
    wait_for_round_status(&round_id_hex, SESSION_STATUS_ACTIVE, 60_000, 2_000)
        .expect("round should become ACTIVE via per-round ceremony");

    // Read the EA public key from the round (auto-generated during ceremony).
    let ea_pk_bytes = api::get_round_ea_pk(&round_id_hex)
        .expect("ACTIVE round should have ea_pk set");
    assert_eq!(ea_pk_bytes.len(), 32, "ea_pk must be 32 bytes");
    log_step("Step 1b", &format!("ea_pk from round: {}", hex::encode(&ea_pk_bytes)));

    // ---- Step 2: Delegate vote (real ZKP #1) ----
    // The commitment tree is global across rounds/tests. Capture the current
    // next_index so we can compute our VAN/VC positions relative to it.
    let pre_delegate_next_index = commitment_tree_next_index().unwrap_or(0);
    let van_position = pre_delegate_next_index;
    log_step("Step 2", "delegate vote (ZKP #1)");
    let deleg_body = delegate_vote_payload(&round_id, &delegation_bundle);
    let (status, json) = post_json_accept_committed("/zally/v1/delegate-vote", &deleg_body, || {
        commitment_tree_next_index()
            .map(|n| n >= pre_delegate_next_index + 1)
            .unwrap_or(false)
    })
    .expect("POST delegate-vote");
    assert_eq!(
        status, 200,
        "delegate-vote: HTTP {}, body={:?}",
        status, json
    );
    assert_eq!(
        json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1),
        0,
        "delegation rejected: {:?}",
        json.get("log")
    );
    block_wait();

    // ---- Step 3: Wait for tree to include this delegation's VAN ----
    // Only van_cmx is appended to the tree (cmx_new is not included), so this
    // tx contributes exactly one new leaf at `van_position`.
    log_step(
        "Step 3",
        &format!(
            "waiting for commitment tree to include VAN at position {}",
            van_position
        ),
    );
    let mut anchor_height: u32 = 0;
    for _ in 0..30 {
        let (status, json) = get_json("/zally/v1/commitment-tree/latest").expect("GET tree latest");
        assert_eq!(status, 200);
        if let Some(tree) = json.get("tree") {
            let h = tree.get("height").and_then(|x| x.as_u64()).unwrap_or(0) as u32;
            let next_idx = tree.get("next_index").and_then(|x| x.as_u64()).unwrap_or(0);
            if h > 0 && next_idx >= pre_delegate_next_index + 1 {
                anchor_height = h;
                assert!(tree.get("root").is_some());
                break;
            }
        }
        std::thread::sleep(std::time::Duration::from_secs(2));
    }
    assert!(
        anchor_height > 0,
        "tree never included delegated VAN leaf after delegation"
    );

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

    // Create a single bundle (bundle_index = 0) and store ZKP #2 fields.
    // Other store_delegation_data fields (rho_signed, alpha, etc.) are only needed
    // for delegation proof reconstruction, not ZKP #2.
    {
        let conn = db.conn();
        librustvoting::storage::queries::insert_bundle(&conn, &round_id_hex, 0, &[])
            .expect("insert_bundle");
        librustvoting::storage::queries::store_delegation_data(
            &conn,
            &round_id_hex,
            0, // bundle_index
            vote_proof_data.van_comm_rand.to_repr().as_ref(),
            &[],        // dummy_nullifiers (not needed for ZKP #2)
            &[0u8; 32], // rho_signed
            &[],        // padded_cmx
            &[0u8; 32], // nf_signed
            &delegation_bundle.cmx_new,
            &[0u8; 32], // alpha
            &[0u8; 32], // rseed_signed
            &[0u8; 32], // rseed_output
            &delegation_bundle.van_cmx,
            vote_proof_data.total_note_value,
            1, // address_index (matches delegation output_recipient = fvk.address_at(1, External))
            &[], // padded_note_secrets (not needed for ZKP #2 test)
            &[0u8; 32], // pczt_sighash
        )
        .expect("store_delegation_data");
    }

    // VAN position is global tree index captured before delegation.
    db.store_van_position(
        &round_id_hex,
        0, // bundle_index
        u32::try_from(van_position).expect("van_position fits in u32"),
    )
    .expect("store_van_position");

    // ---- Step 5: Sync tree via TreeClient + HttpTreeSyncApi ----
    log_step("Step 5", "syncing vote commitment tree from chain");
    let base_url = api::base_url();
    let mut tree_client = TreeClient::empty();
    tree_client.mark_position(van_position);
    let sync_api = HttpTreeSyncApi::new(&base_url);
    tree_client
        .sync(&sync_api)
        .expect("TreeClient sync from chain");
    assert!(
        tree_client.size() >= van_position + 1,
        "tree should include VAN leaf after sync"
    );
    log_step(
        "Step 5",
        &format!(
            "synced {} leaves, last height {}",
            tree_client.size(),
            tree_client.last_synced_height().unwrap_or(0)
        ),
    );

    // ---- Step 6: Generate VAN witness ----
    log_step(
        "Step 6",
        &format!("generating VAN witness at position {}", van_position),
    );
    let witness = tree_client
        .witness(van_position, anchor_height)
        .expect("generate VAN witness");
    assert_eq!(
        witness.position(),
        u32::try_from(van_position).expect("van_position fits in u32")
    );

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
    let auth_path_bytes: Vec<[u8; 32]> = witness.auth_path().iter().map(|h| h.to_bytes()).collect();

    // ---- Step 7: Build vote commitment via VotingDb (real ZKP #2) ----
    log_step(
        "Step 7",
        "building vote commitment via VotingDb (K=14 proof, 30-60s)...",
    );
    let bundle = db
        .build_vote_commitment(
            &round_id_hex,
            0, // bundle_index
            &seed,
            1, // network_id (testnet)
            1, // proposal_id
            1, // choice (oppose)
            2, // num_options
            &auth_path_bytes,
            u32::try_from(van_position).expect("van_position fits in u32"),
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
    assert_eq!(bundle.enc_shares.len(), 16, "should have 16 encrypted shares");
    assert_eq!(bundle.shares_hash.len(), 32);

    // ---- Step 7b: Local proof verification (same binary = same VK) ----
    {
        log_step("Step 7b", "verifying vote proof locally...");
        let van_nf: pallas::Base = Option::from(pallas::Base::from_repr(
            bundle.van_nullifier.as_slice().try_into().unwrap(),
        ))
        .expect("van_nullifier");
        let r_vpk_arr: [u8; 32] = bundle.r_vpk_bytes.as_slice().try_into().unwrap();
        let r_vpk_affine: pallas::Affine =
            Option::from(pallas::Affine::from_bytes(&r_vpk_arr)).expect("decompress r_vpk");
        let r_vpk_coords = r_vpk_affine.coordinates().unwrap();
        let van_new: pallas::Base = Option::from(pallas::Base::from_repr(
            bundle
                .vote_authority_note_new
                .as_slice()
                .try_into()
                .unwrap(),
        ))
        .expect("van_new");
        let vc: pallas::Base = Option::from(pallas::Base::from_repr(
            bundle.vote_commitment.as_slice().try_into().unwrap(),
        ))
        .expect("vote_commitment");
        // vote_round_id is a Blake2b-256 hash — use wide reduction since ~75% of
        // hash values exceed the Pallas modulus.
        let mut vri_wide = [0u8; 64];
        vri_wide[..32].copy_from_slice(&round_id);
        let vri: pallas::Base = pallas::Base::from_uniform_bytes(&vri_wide);

        // EA PK coordinates
        let ea_pk_arr: [u8; 32] = ea_pk_bytes.as_slice().try_into().expect("ea_pk 32 bytes");
        let ea_pk_point: pallas::Point =
            Option::from(pallas::Point::from_bytes(&ea_pk_arr)).expect("ea_pk point");
        let ea_pk_affine = ea_pk_point.to_affine();
        let ea_coords = ea_pk_affine.coordinates().unwrap();

        let instance = orchard::vote_proof::Instance::from_parts(
            van_nf,
            *r_vpk_coords.x(),
            *r_vpk_coords.y(),
            van_new,
            vc,
            local_root,
            pallas::Base::from(anchor_height as u64),
            pallas::Base::from(1u64), // proposal_id
            vri,
            *ea_coords.x(),
            *ea_coords.y(),
        );
        orchard::vote_proof::verify_vote_proof(&bundle.proof, &instance)
            .expect("LOCAL vote proof verification must pass");
        log_step("Step 7b", "local verification PASSED");
    }

    // ---- Step 8: Submit cast-vote TX ----
    log_step("Step 8", "computing sighash and signing cast-vote TX");

    // 8a: Decompress r_vpk to get x, y coordinates for the payload.
    let r_vpk_arr: [u8; 32] = bundle.r_vpk_bytes.as_slice().try_into().unwrap();
    let r_vpk_affine: pallas::Affine =
        Option::from(pallas::Affine::from_bytes(&r_vpk_arr)).expect("decompress r_vpk");
    let coords = r_vpk_affine.coordinates().unwrap();
    let r_vpk_x_bytes = coords.x().to_repr();
    let r_vpk_y_bytes = coords.y().to_repr();

    // 8b: Compute canonical sighash (must match Go's ComputeCastVoteSighash).
    const CAST_VOTE_SIGHASH_DOMAIN: &[u8] = b"ZALLY_CAST_VOTE_SIGHASH_V0";
    let mut canonical = Vec::new();
    canonical.extend_from_slice(CAST_VOTE_SIGHASH_DOMAIN);
    // vote_round_id: pad to 32 bytes
    let mut buf32 = [0u8; 32];
    let vr_len = round_id.len().min(32);
    buf32[..vr_len].copy_from_slice(&round_id[..vr_len]);
    canonical.extend_from_slice(&buf32);
    // r_vpk: already 32 bytes
    canonical.extend_from_slice(&bundle.r_vpk_bytes);
    // van_nullifier: 32 bytes
    buf32 = [0u8; 32];
    let vn = &bundle.van_nullifier;
    buf32[..vn.len().min(32)].copy_from_slice(&vn[..vn.len().min(32)]);
    canonical.extend_from_slice(&buf32);
    // vote_authority_note_new: 32 bytes
    buf32 = [0u8; 32];
    let vn_new = &bundle.vote_authority_note_new;
    buf32[..vn_new.len().min(32)].copy_from_slice(&vn_new[..vn_new.len().min(32)]);
    canonical.extend_from_slice(&buf32);
    // vote_commitment: 32 bytes
    buf32 = [0u8; 32];
    let vc = &bundle.vote_commitment;
    buf32[..vc.len().min(32)].copy_from_slice(&vc[..vc.len().min(32)]);
    canonical.extend_from_slice(&buf32);
    // proposal_id: 4 bytes LE, padded to 32 bytes
    let mut pid_buf = [0u8; 32];
    pid_buf[..4].copy_from_slice(&1u32.to_le_bytes());
    canonical.extend_from_slice(&pid_buf);
    // anchor_height: 8 bytes LE, padded to 32 bytes
    let mut ah_buf = [0u8; 32];
    ah_buf[..8].copy_from_slice(&(anchor_height as u64).to_le_bytes());
    canonical.extend_from_slice(&ah_buf);

    let sighash_full = Blake2bParams::new().hash_length(32).hash(&canonical);
    let mut sighash = [0u8; 32];
    sighash.copy_from_slice(sighash_full.as_bytes());

    // 8c: Sign the sighash with the randomized voting key (rsk_v = ask_v.randomize(&alpha_v)).
    let alpha_v_arr: [u8; 32] = bundle.alpha_v.as_slice().try_into().unwrap();
    let alpha_v: pallas::Scalar =
        Option::from(pallas::Scalar::from_repr(alpha_v_arr)).expect("deserialize alpha_v");
    let ask_v = SpendAuthorizingKey::from(&vote_proof_data.sk);
    let rsk_v = ask_v.randomize(&alpha_v);
    let vote_auth_sig = rsk_v.sign(&mut rng, &sighash);
    let vote_auth_sig_bytes: [u8; 64] = (&vote_auth_sig).into();

    let cast_body = cast_vote_payload_real(
        &round_id,
        anchor_height,
        &bundle.van_nullifier,
        r_vpk_x_bytes.as_ref(),
        r_vpk_y_bytes.as_ref(),
        &bundle.vote_authority_note_new,
        &bundle.vote_commitment,
        1, // proposal_id
        &bundle.proof,
        &bundle.r_vpk_bytes,
        &sighash,
        &vote_auth_sig_bytes,
    );
    let cast_target_next_index = van_position + 3; // delegation leaf + 2 cast leaves

    let (status, json) = {
        let mut last = None;
        for attempt in 1..=3 {
            let result = post_json_accept_committed("/zally/v1/cast-vote", &cast_body, || {
                commitment_tree_next_index()
                    .map(|n| n >= cast_target_next_index)
                    .unwrap_or(false)
            })
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
    // Relative to this test's VAN leaf, cast-vote appends:
    // vote_authority_note_new at +1 and vote_commitment at +2.
    let vc_position = van_position + 2;
    let payloads = db
        .build_share_payloads(
            &bundle.enc_shares,
            &bundle,
            1, // vote_decision (oppose)
            2, // num_options
            vc_position, // vc_tree_position
        )
        .expect("VotingDb::build_share_payloads");
    assert_eq!(payloads.len(), 16, "should have 16 share payloads");
    for (i, p) in payloads.iter().enumerate() {
        assert_eq!(p.shares_hash, bundle.shares_hash);
        assert_eq!(p.proposal_id, 1);
        assert_eq!(p.vote_decision, 1);
        assert_eq!(p.tree_position, vc_position);
        assert_eq!(p.enc_share.share_index, i as u32);
    }
    log_step("Step 9", "share payloads built and validated");

    // ---- Step 10: Send share payloads to helper server ----
    let helper_url = helper_server_url();
    log_step(
        "Step 10",
        &format!("sending 16 share payloads to helper server at {}", helper_url),
    );

    let all_enc: Vec<(&[u8], &[u8], u32)> = payloads[0]
        .all_enc_shares
        .iter()
        .map(|s| (s.c1.as_slice(), s.c2.as_slice(), s.share_index))
        .collect();

    for (i, p) in payloads.iter().enumerate() {
        let body = helper_share_payload(
            &round_id,
            &p.shares_hash,
            p.proposal_id,
            p.vote_decision,
            &p.enc_share.c1,
            &p.enc_share.c2,
            p.enc_share.share_index,
            p.tree_position,
            &all_enc,
            &p.share_comms,
            &p.primary_blind,
        );
        let (status, json) = post_helper_json("/api/v1/shares", &body)
            .unwrap_or_else(|e| panic!("POST helper share {}: {}", i, e));
        assert_eq!(
            status, 200,
            "helper share {}: HTTP {}, body={:?}",
            i, status, json
        );
        log_step("Step 10", &format!("share {} accepted by helper server", i));
    }

    // Wait for helper server to process at least one share (tally appears on chain).
    log_step(
        "Step 10",
        "waiting for helper server to process shares and submit to chain...",
    );
    let tally_deadline = std::time::Instant::now() + std::time::Duration::from_secs(300);
    while std::time::Instant::now() < tally_deadline {
        if tally_has_proposal(&round_id_hex, 1) {
            log_step(
                "Step 10",
                "tally has entry — helper server submitted at least one share",
            );
            break;
        }
        std::thread::sleep(std::time::Duration::from_secs(3));
    }
    assert!(
        tally_has_proposal(&round_id_hex, 1),
        "helper server never submitted shares to chain within timeout"
    );

    // ---- Step 11: Verify tally has the encrypted ciphertext ----
    log_step("Step 11", "verifying tally has ciphertext");
    block_wait();
    let (status, json) =
        get_json(&format!("/zally/v1/tally/{}/1", round_id_hex)).expect("GET tally");
    assert_eq!(status, 200);
    let tally = json.get("tally").expect("tally");
    assert!(
        tally.get("1").is_some(),
        "tally should have entry for proposal 1"
    );

    // ---- Step 12: Wait for TALLYING ----
    let now_secs = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("system clock before unix epoch")
        .as_secs();
    let secs_until_vote_end = fields_for_db.vote_end_time.saturating_sub(now_secs);
    let wait_for_tallying_ms = (secs_until_vote_end.saturating_add(120))
        .saturating_mul(1000)
        .clamp(120_000, 900_000);
    log_step(
        "Step 12",
        &format!(
            "waiting for TALLYING (up to {}s)",
            wait_for_tallying_ms / 1000
        ),
    );
    wait_for_round_status(&round_id_hex, SESSION_STATUS_TALLYING, wait_for_tallying_ms, 3_000)
        .expect("wait for TALLYING");

    // ---- Step 13: Wait for FINALIZED (auto-tally via PrepareProposal) ----
    log_step(
        "Step 13",
        "waiting for FINALIZED via auto-tally (up to 60s)",
    );
    wait_for_round_status(&round_id_hex, SESSION_STATUS_FINALIZED, 60_000, 2_000)
        .expect("wait for FINALIZED (auto-tally)");

    // ---- Step 14: Tally results queryable ----
    log_step("Step 14", "tally results queryable");
    let (status, json) = get_json(&format!("/zally/v1/tally-results/{}", round_id_hex))
        .expect("GET tally-results");
    assert_eq!(
        status, 200,
        "GET tally-results: expected 200, got {} body={:?}",
        status, json
    );
    let results = json
        .get("results")
        .and_then(|r| r.as_array())
        .expect("results");
    assert!(!results.is_empty(), "tally results should not be empty");
    log_step("Step 14", &format!("tally results: {:?}", results));

    log_step(
        "Done",
        "librustvoting path: VotingDb → TreeClient → ZKP #2 → helper-server → tally ✓",
    );
}
