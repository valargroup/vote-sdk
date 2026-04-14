//! Round activation e2e test.
//!
//! Verifies the per-round ceremony flow: ensures the validator's Pallas key
//! is registered, creates a voting round (which starts PENDING), then waits
//! for auto-deal and auto-ack via PrepareProposal to transition the round
//! to ACTIVE.
//!
//! Usage (chain must be running via `make init && make start`):
//!
//!   cargo test --release --manifest-path e2e-tests/Cargo.toml \
//!     round_activation -- --nocapture --ignored

use e2e_tests::{
    api::{
        broadcast_cosmos_msg, default_cosmos_tx_config, get_round, import_hex_key,
        key_account_address, wait_for_round_status, SESSION_STATUS_ACTIVE,
    },
    payloads::create_voting_session_payload,
};

#[test]
#[ignore = "requires running chain"]
fn round_activation() {
    let vm_privkey = std::env::var("VM_PRIVKEY")
        .expect("VM_PRIVKEY env var must be set (64-char hex secp256k1 private key)");

    // Ensure the validator's Pallas key is in the global registry.
    // (Usually already registered via MsgCreateValidatorWithPallasKey during init.)
    e2e_tests::setup::ensure_pallas_key_registered();

    // Import vote manager key into keyring.
    let config = default_cosmos_tx_config();
    import_hex_key("vote-manager", &vm_privkey, &config.home_dir);

    // Derive the vote-manager address from the keyring so the test works with
    // any VM_PRIVKEY (not just one specific hardcoded key).
    let vote_manager_address = key_account_address("vote-manager", &config.home_dir)
        .expect("vote-manager key must be in keyring after import");
    eprintln!("[E2E] Vote manager address: {}", vote_manager_address);

    // Create a voting round — starts as PENDING, triggers per-round ceremony.
    let (mut body, _, round_id) =
        create_voting_session_payload(&vote_manager_address, 180, None);
    let round_id_hex = hex::encode(round_id);
    body["@type"] = serde_json::json!("/svote.v1.MsgCreateVotingSession");

    let vm_config = e2e_tests::api::CosmosTxConfig {
        key_name: "vote-manager".to_string(),
        home_dir: config.home_dir.clone(),
        chain_id: config.chain_id.clone(),
        node_url: config.node_url.clone(),
    };
    let (status, json) =
        broadcast_cosmos_msg(&body, &vm_config).expect("broadcast create-voting-session");
    assert_eq!(status, 200, "create session: HTTP {}, body={:?}", status, json);
    assert_eq!(
        json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1),
        0,
        "create session rejected: {:?}",
        json.get("log")
    );

    // Wait for auto-deal + auto-ack → round becomes ACTIVE.
    eprintln!("[E2E] Waiting for round {} to become ACTIVE (auto-deal + auto-ack)...", &round_id_hex);
    wait_for_round_status(&round_id_hex, SESSION_STATUS_ACTIVE, 120_000, 2_000)
        .expect("round should become ACTIVE via per-round ceremony");
    eprintln!("[E2E] Round {} is ACTIVE", round_id_hex);

    // Verify TSS fields when multiple validators are present.
    let round = get_round(&round_id_hex).expect("should be able to query ACTIVE round");
    let threshold = round.get("threshold").and_then(|t| t.as_u64()).unwrap_or(0);
    let n_validators = round
        .get("ceremonyValidators")
        .or_else(|| round.get("ceremony_validators"))
        .and_then(|v| v.as_array())
        .map(|a| a.len())
        .unwrap_or(0);
    let feldman_count = round
        .get("feldmanCommitments")
        .or_else(|| round.get("feldman_commitments"))
        .and_then(|v| v.as_array())
        .map(|a| a.len())
        .unwrap_or(0);

    if n_validators >= 2 {
        assert!(threshold >= 2, "threshold should be >= 2 for {} validators, got {}", n_validators, threshold);
        assert_eq!(feldman_count, threshold as usize,
            "feldman_commitments count should equal threshold (got {} commitments, threshold={})",
            feldman_count, threshold);
        eprintln!("[E2E] DKG mode: threshold={}, feldman_commitments={}, validators={}", threshold, feldman_count, n_validators);
    } else {
        eprintln!("[E2E] Single-validator mode: threshold={}, validators={}", threshold, n_validators);
    }
}
