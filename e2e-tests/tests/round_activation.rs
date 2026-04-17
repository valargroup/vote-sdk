//! Round activation e2e test (3-validator DKG).
//!
//! Verifies the per-round ceremony flow with a 3-validator chain:
//!   1. Ensures the local validator's Pallas key is registered.
//!   2. Creates a voting round (starts PENDING, triggers per-round DKG).
//!   3. Waits for all 3 validators to auto-deal and auto-ack via
//!      PrepareProposal, transitioning the round to ACTIVE.
//!   4. Asserts threshold=2, 3 ceremony validators, and 2 Feldman
//!      commitments (Joint-Feldman DKG with t=ceil(3/2)=2).
//!
//! Requires a 3-validator chain running via `make init-multi && make start-multi`.
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
    let vm_privkey = std::env::var("VM_PRIVKEYS")
        .expect("VM_PRIVKEYS env var must be set (comma-separated 64-char hex keys)")
        .split(',')
        .next()
        .expect("VM_PRIVKEYS must contain at least one key")
        .trim()
        .to_string();

    // Ensure the validator's Pallas key is in the global registry.
    // (Usually already registered via MsgCreateValidatorWithPallasKey during init.)
    e2e_tests::setup::ensure_pallas_key_registered();

    // Import vote manager key into keyring.
    let config = default_cosmos_tx_config();
    import_hex_key("admin-1", &vm_privkey, &config.home_dir);

    let admin_address = key_account_address("admin-1", &config.home_dir)
        .expect("admin-1 key must be in keyring after import");
    eprintln!("[E2E] Vote manager address: {}", admin_address);

    // Create a voting round — starts as PENDING, triggers per-round ceremony.
    let (mut body, _, round_id) =
        create_voting_session_payload(&admin_address, 300, None);
    let round_id_hex = hex::encode(round_id);
    body["@type"] = serde_json::json!("/svote.v1.MsgCreateVotingSession");

    let vm_config = e2e_tests::api::CosmosTxConfig {
        key_name: "admin-1".to_string(),
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

    // Wait for auto-deal + auto-ack across all 3 validators → round becomes ACTIVE.
    // With 3 validators each needing a proposer turn for deal and ack, this can
    // take 20+ blocks depending on weighted round-robin scheduling.
    eprintln!("[E2E] Waiting for round {} to become ACTIVE (3-validator DKG)...", &round_id_hex);
    wait_for_round_status(&round_id_hex, SESSION_STATUS_ACTIVE, 180_000, 2_000)
        .expect("round should become ACTIVE via per-round ceremony");
    eprintln!("[E2E] Round {} is ACTIVE", round_id_hex);

    // Verify DKG fields for the 3-validator ceremony.
    let round = get_round(&round_id_hex).expect("should be able to query ACTIVE round");

    let threshold = round.get("threshold").and_then(|t| t.as_u64()).unwrap_or(0);
    let ceremony_validators = round
        .get("ceremonyValidators")
        .or_else(|| round.get("ceremony_validators"))
        .and_then(|v| v.as_array());
    let n_validators = ceremony_validators.map(|a| a.len()).unwrap_or(0);
    let feldman_commitments = round
        .get("feldmanCommitments")
        .or_else(|| round.get("feldman_commitments"))
        .and_then(|v| v.as_array());
    let feldman_count = feldman_commitments.map(|a| a.len()).unwrap_or(0);
    let dkg_contributions = round
        .get("dkgContributions")
        .or_else(|| round.get("dkg_contributions"))
        .and_then(|v| v.as_array());
    let contributions_count = dkg_contributions.map(|a| a.len()).unwrap_or(0);
    let ceremony_acks = round
        .get("ceremonyAcks")
        .or_else(|| round.get("ceremony_acks"))
        .and_then(|v| v.as_array());
    let acks_count = ceremony_acks.map(|a| a.len()).unwrap_or(0);

    eprintln!("[E2E] DKG result: validators={}, threshold={}, feldman_commitments={}, contributions={}, acks={}",
        n_validators, threshold, feldman_count, contributions_count, acks_count);

    // Log individual validator addresses for diagnostics.
    if let Some(vals) = ceremony_validators {
        for (i, v) in vals.iter().enumerate() {
            let addr = v.get("validatorAddress")
                .or_else(|| v.get("validator_address"))
                .and_then(|a| a.as_str())
                .unwrap_or("unknown");
            eprintln!("[E2E]   validator[{}]: {}", i, addr);
        }
    }

    assert_eq!(n_validators, 3,
        "expected 3 ceremony validators, got {}", n_validators);
    assert_eq!(threshold, 2,
        "expected threshold=2 for 3 validators (ceil(3/2)), got {}", threshold);
    assert_eq!(feldman_count, 2,
        "expected 2 Feldman commitments (== threshold), got {}", feldman_count);
}
