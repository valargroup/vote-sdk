//! E2E test for the full multi-validator key ceremony lifecycle:
//! idle REGISTERING → register all → deal → CONFIRMED → reinitialize → idle REGISTERING.
//!
//! Requires a running 3-validator chain started by `make init-multi` in sdk/.
//! After `init_multi.sh`, validators 2 and 3 have already registered Pallas
//! keys via `CreateValidatorWithPallasKey`, so the ceremony starts in active
//! REGISTERING. The test handles this by waiting for the EndBlocker timeout.
//!
//! The ceremony confirms via two paths:
//!   - Fast path: all validators ack before the DEALT timeout → immediate CONFIRMED.
//!   - Timeout path: >=2/3 validators acked when DEALT timeout fires → CONFIRMED,
//!     non-ackers stripped from ceremony state and jailed via staking module.
//! This test exercises the fast path (all 3 validators ack).

use base64::Engine;
use e2e_tests::{
    api::{
        get_all_validator_operator_addresses, get_ceremony_state_json, get_ceremony_status,
        post_json, CEREMONY_STATUS_CONFIRMED, CEREMONY_STATUS_REGISTERING,
    },
    payloads::reinitialize_ea_payload,
    setup::{bootstrap_ceremony_multi, ensure_ceremony_idle, load_multi_validator_info},
};

const BLOCK_WAIT_MS: u64 = 6000;

fn log_step(step: &str, msg: &str) {
    eprintln!("[E2E-ceremony] {}: {}", step, msg);
}

fn block_wait() {
    std::thread::sleep(std::time::Duration::from_millis(BLOCK_WAIT_MS));
}

/// E2E test: multi-validator ceremony lifecycle.
///
/// Flow:
///   1. Ensure ceremony is idle (handle post-init_multi state)
///   2. Register all 3 validators' Pallas keys
///   3. Deal EA key (ECIES-encrypt ea_sk to each validator's Pallas PK)
///   4. Wait for 3 auto-acks via PrepareProposal → CONFIRMED (fast path: all acked)
///   5. Verify CONFIRMED state: ea_pk, 3 validators, 3 acks
///   6. POST /reinitialize-ea
///   7. Verify idle REGISTERING with all fields cleared
#[test]
#[ignore = "requires running 3-validator chain (make init-multi)"]
fn ceremony_lifecycle_multi_validator() {
    // ---- Phase 0: Load validator info ----
    log_step("Phase 0", "loading multi-validator info...");
    let validators = load_multi_validator_info();
    assert_eq!(validators.len(), 3, "expected 3 validators");

    // Verify all 3 validators exist in the staking module.
    let all_addrs =
        get_all_validator_operator_addresses().expect("failed to query validators from staking");
    assert_eq!(
        all_addrs.len(),
        3,
        "expected 3 validators in staking module, found {}",
        all_addrs.len()
    );
    log_step(
        "Phase 0",
        &format!("loaded {} validators from disk + staking", validators.len()),
    );

    // Read EA keypair from val1's home directory.
    let ea_pk_path = format!("{}/ea.pk", validators[0].home_dir);
    let ea_sk_path = format!("{}/ea.sk", validators[0].home_dir);
    let ea_pk_bytes = std::fs::read(&ea_pk_path)
        .unwrap_or_else(|e| panic!("failed to read EA PK from {}: {}", ea_pk_path, e));
    let ea_sk_bytes = std::fs::read(&ea_sk_path)
        .unwrap_or_else(|e| panic!("failed to read EA SK from {}: {}", ea_sk_path, e));
    assert_eq!(ea_pk_bytes.len(), 32, "EA PK must be 32 bytes");
    assert_eq!(ea_sk_bytes.len(), 32, "EA SK must be 32 bytes");
    log_step(
        "Phase 0",
        &format!(
            "EA PK: {}, EA SK loaded from {}",
            hex::encode(&ea_pk_bytes),
            ea_sk_path
        ),
    );

    // ---- Phase 1: Ensure idle ----
    log_step(
        "Phase 1",
        "ensuring ceremony is idle (may wait for timeout)...",
    );
    ensure_ceremony_idle(&validators);

    // Verify idle state (REGISTERING with phase_timeout=0, or nil).
    let status = get_ceremony_status();
    assert!(
        status == Some(CEREMONY_STATUS_REGISTERING) || status.is_none(),
        "expected idle REGISTERING or nil after ensure, got {:?}",
        status
    );
    log_step("Phase 1", "ceremony is idle ✓");

    // ---- Phase 2: Full ceremony (register → deal → CONFIRMED) ----
    log_step(
        "Phase 2",
        "running full ceremony (register → deal → CONFIRMED)...",
    );
    bootstrap_ceremony_multi(&validators, &ea_sk_bytes, &ea_pk_bytes);

    // Verify CONFIRMED state.
    assert_eq!(
        get_ceremony_status(),
        Some(CEREMONY_STATUS_CONFIRMED),
        "ceremony should be CONFIRMED after bootstrap"
    );

    // Verify ea_pk matches what we dealt.
    let ceremony_json = get_ceremony_state_json().expect("ceremony state should exist");
    let chain_ea_pk_b64 = ceremony_json
        .get("ea_pk")
        .and_then(|v| v.as_str())
        .expect("ea_pk should be present in CONFIRMED ceremony");
    let chain_ea_pk = base64::engine::general_purpose::STANDARD
        .decode(chain_ea_pk_b64)
        .expect("ea_pk should be valid base64");
    assert_eq!(
        chain_ea_pk.as_slice(),
        ea_pk_bytes.as_slice(),
        "ea_pk on chain should match dealt key"
    );
    log_step("Phase 2", "ea_pk verified ✓");

    // Verify all 3 validators registered.
    let validators_json = ceremony_json
        .get("validators")
        .and_then(|v| v.as_array())
        .expect("validators array should exist in CONFIRMED ceremony");
    assert_eq!(
        validators_json.len(),
        3,
        "all 3 validators should be registered"
    );

    // Verify all 3 acks.
    let acks_json = ceremony_json
        .get("acks")
        .and_then(|v| v.as_array())
        .expect("acks array should exist in CONFIRMED ceremony");
    assert_eq!(acks_json.len(), 3, "all 3 validators should have acked");

    log_step("Phase 2", "ceremony CONFIRMED with 3 validators + 3 acks ✓");

    // ---- Phase 3: Reinitialize ----
    log_step("Phase 3", "sending POST /reinitialize-ea...");
    let body = reinitialize_ea_payload(&validators[0].operator_address);
    let (http_status, json) =
        post_json("/zally/v1/reinitialize-ea", &body).expect("POST reinitialize-ea");
    assert!(
        http_status == 200 && json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1) == 0,
        "reinitialize-ea failed: HTTP {}, body={:?}",
        http_status,
        json
    );
    log_step("Phase 3", "reinitialize-ea tx accepted");

    // Wait for the reinit tx to commit.
    block_wait();

    // ---- Phase 4: Verify idle REGISTERING + all fields cleared ----
    log_step(
        "Phase 4",
        "verifying reinitialize cleared all ceremony state...",
    );

    assert_eq!(
        get_ceremony_status(),
        Some(CEREMONY_STATUS_REGISTERING),
        "ceremony should be idle REGISTERING after reinit"
    );

    let ceremony_json = get_ceremony_state_json().expect("ceremony state should exist after reinit");

    // Validators should be cleared (field absent or empty array).
    let validators_after = ceremony_json
        .get("validators")
        .and_then(|v| v.as_array());
    assert!(
        validators_after.is_none() || validators_after.unwrap().is_empty(),
        "validators should be cleared after reinit, got {:?}",
        validators_after
    );

    // Payloads should be cleared.
    let payloads_after = ceremony_json.get("payloads").and_then(|v| v.as_array());
    assert!(
        payloads_after.is_none() || payloads_after.unwrap().is_empty(),
        "payloads should be cleared after reinit, got {:?}",
        payloads_after
    );

    // Acks should be cleared.
    let acks_after = ceremony_json.get("acks").and_then(|v| v.as_array());
    assert!(
        acks_after.is_none() || acks_after.unwrap().is_empty(),
        "acks should be cleared after reinit, got {:?}",
        acks_after
    );

    // ea_pk should be empty/absent.
    let ea_pk_after = ceremony_json.get("ea_pk");
    assert!(
        ea_pk_after.is_none()
            || ea_pk_after.unwrap().is_null()
            || ea_pk_after.unwrap().as_str() == Some(""),
        "ea_pk should be cleared after reinit, got {:?}",
        ea_pk_after
    );

    log_step("Phase 4", "all ceremony fields cleared ✓");
    log_step("DONE", "ceremony lifecycle test passed!");
}
