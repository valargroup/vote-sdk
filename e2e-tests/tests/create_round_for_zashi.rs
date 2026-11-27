//! Create a voting round with real nc_root and nullifier_imt_root so that
//! Zashi can successfully generate ZKP #1 (delegation proof).
//!
//! The admin UI uses stub values (0xdd*32 for nc_root, 0xcc*32 for
//! nullifier_imt_root) which causes ZKP #1 to fail because the Merkle roots
//! don't match the real chain state. This test fetches the real values from
//! lightwalletd (via grpcurl) and the Ironwood PIR server, then creates a session
//! that Zashi can actually delegate to.
//!
//! Prerequisites:
//!   - Local svoted chain running (port 1317)
//!   - `grpcurl` installed (brew install grpcurl)
//!   - Validator Pallas key registered (done during chain init)
//!   - Ironwood PIR server reachable (default: http://46.101.255.48:3000)
//!
//! Environment variables:
//!   ZASHI_SNAPSHOT_HEIGHT  - Block height for the snapshot (default: latest - 100)
//!   ZASHI_LIGHTWALLETD     - Lightwalletd host:port (default: us.zec.stardust.rest:443)
//!   ZASHI_PIR_URL          - PIR server URL (default: http://46.101.255.48:3000)
//!   ZASHI_VOTE_WINDOW_SECS - Voting window in seconds (default: 604800 = 7 days)
//!   ZASHI_PROPOSAL_COUNT   - Generate that many two-option speed-test proposals
//!   ZASHI_PROXY_DELEGATION - `1` creates a proxy delegation round: the first
//!                            vote manager proposes it and the second, from
//!                            VM_PRIVKEYS, approves it (contracts-v1 §5.5)

use e2e_tests::{
    api::{
        self, broadcast_cosmos_msg, default_cosmos_tx_config, get_json,
        import_first_vote_manager_key, import_hex_key, key_account_address,
        wait_for_create_round_id, wait_for_round_status, CosmosTxConfig,
        FIRST_VOTE_MANAGER_KEY_NAME, SESSION_STATUS_ACTIVE,
    },
    payloads::{approve_coordinator_action_payload, coordinator_action_proposal_payload},
};
use serde_json::json;
use shielded_vote_circuits::nc_root::compute_nc_root;

fn log(msg: &str) {
    eprintln!("[create-round] {}", msg);
}

fn lightwalletd_host() -> String {
    std::env::var("ZASHI_LIGHTWALLETD").unwrap_or_else(|_| "us.zec.stardust.rest:443".to_string())
}

fn pir_url() -> String {
    std::env::var("ZASHI_PIR_URL").unwrap_or_else(|_| "http://46.101.255.48:3000".to_string())
}

fn vote_window_secs() -> u64 {
    std::env::var("ZASHI_VOTE_WINDOW_SECS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(604800) // 7 days
}

/// Whether `ZASHI_PROXY_DELEGATION=1` asks for a proxy delegation round.
fn proxy_delegation() -> bool {
    std::env::var("ZASHI_PROXY_DELEGATION").is_ok_and(|value| value == "1")
}

/// Keyring name of the second vote manager, as init.sh names it.
const SECOND_VOTE_MANAGER_KEY_NAME: &str = "vote-manager-2";

/// Imports the second key of VM_PRIVKEYS as [`SECOND_VOTE_MANAGER_KEY_NAME`]
/// and returns its account address. A proxy round needs its approval.
fn import_second_vote_manager_key(home_dir: &str) -> String {
    let raw = std::env::var("VM_PRIVKEYS").expect("VM_PRIVKEYS must be set");
    let key = raw
        .split(',')
        .map(str::trim)
        .filter(|key| !key.is_empty())
        .nth(1)
        .expect("a proxy round needs a second key in VM_PRIVKEYS");
    import_hex_key(SECOND_VOTE_MANAGER_KEY_NAME, key, home_dir);
    key_account_address(SECOND_VOTE_MANAGER_KEY_NAME, home_dir)
        .expect("the second vote manager key must be in the keyring after import")
}

/// Polls `/shielded-vote/v1/tx/{hash}` until the tx commits, and returns
/// the value of attribute `key` of its first `kind` event.
fn committed_event_attribute(tx_hash: &str, kind: &str, key: &str) -> Option<String> {
    let path = format!("/shielded-vote/v1/tx/{tx_hash}");
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(60);
    loop {
        let (status, body) = get_json(&path).expect("GET the tx");
        if status == 200 {
            assert_eq!(body["code"].as_u64().unwrap_or(0), 0, "tx failed: {body}");
            return body["events"]
                .as_array()?
                .iter()
                .filter(|event| event["type"] == kind)
                .flat_map(|event| event["attributes"].as_array().into_iter().flatten())
                .find(|attribute| attribute["key"] == key)
                .and_then(|attribute| attribute["value"].as_str())
                .map(str::to_owned);
        }
        assert!(
            std::time::Instant::now() < deadline,
            "tx {tx_hash} did not commit: HTTP {status} {body}"
        );
        std::thread::sleep(std::time::Duration::from_millis(500));
    }
}

/// Broadcasts `msg` signed by `config`'s key and checks CheckTx accepted it.
fn broadcast_accepted(msg: &serde_json::Value, config: &CosmosTxConfig) -> serde_json::Value {
    let (status, json) = broadcast_cosmos_msg(msg, config).expect("broadcast");
    assert_eq!(
        status, 200,
        "{}: HTTP {status}, body={json:?}",
        msg["@type"]
    );
    assert_eq!(
        json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1),
        0,
        "{} rejected: {:?}",
        msg["@type"],
        json.get("log")
    );
    json
}

const LEGACY_PIR_DATASET_VERSION: u64 = 1;
const RUNTIME_TWO_TIER_PIR_DATASET_VERSION: u64 = 2;

/// Fetch the Ironwood note commitment tree root at a given height.
fn fetch_ironwood_nc_root(height: u64) -> [u8; 32] {
    let host = lightwalletd_host();
    log(&format!(
        "fetching tree state at height {} from {}...",
        height, host
    ));

    let output = std::process::Command::new("grpcurl")
        .args([
            "-d",
            &format!("{{\"height\": \"{}\"}}", height),
            &host,
            "cash.z.wallet.sdk.rpc.CompactTxStreamer/GetTreeState",
        ])
        .output()
        .expect("failed to run grpcurl — is it installed? (brew install grpcurl)");

    assert!(
        output.status.success(),
        "grpcurl failed: {}",
        String::from_utf8_lossy(&output.stderr)
    );

    let json: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("grpcurl output is not valid JSON");

    let ironwood_tree_hex = json["ironwoodTree"]
        .as_str()
        .expect("lightwalletd response missing ironwoodTree");
    assert!(
        !ironwood_tree_hex.is_empty(),
        "lightwalletd returned an empty ironwoodTree"
    );
    log(&format!(
        "tree state: height={}, ironwood_tree_hex_len={}",
        height,
        ironwood_tree_hex.len()
    ));

    compute_nc_root(ironwood_tree_hex)
        .expect("failed to parse Ironwood tree root from lightwalletd hex")
}

/// Fetch the Ironwood nullifier root at the snapshot height.
fn fetch_ironwood_nullifier_root(height: u64) -> [u8; 32] {
    let url = format!("{}/root", pir_url());
    log(&format!("fetching Ironwood PIR root from {}...", url));

    let resp = api::client()
        .get(&url)
        .send()
        .expect("failed to reach PIR server");
    let json: serde_json::Value = resp.json().expect("PIR /root response is not JSON");
    assert_eq!(
        json["nullifier_pool"].as_str(),
        Some("ironwood"),
        "PIR server is not serving Ironwood nullifiers"
    );
    let served_height = json["height"]
        .as_u64()
        .or_else(|| json["height"].as_str().and_then(|value| value.parse().ok()))
        .expect("PIR /root response missing height");
    assert_eq!(served_height, height, "PIR snapshot height mismatch");

    let root_hex = select_ironwood_nullifier_root(&json).unwrap_or_else(|err| panic!("{err}"));

    // Strip 0x prefix if present
    let hex_str = root_hex.strip_prefix("0x").unwrap_or(root_hex);
    let bytes = hex::decode(hex_str).expect("PIR root is not valid hex");
    assert_eq!(bytes.len(), 32, "PIR root must be 32 bytes");

    let mut arr = [0u8; 32];
    arr.copy_from_slice(&bytes);
    log(&format!("PIR root: {}", hex::encode(arr)));
    arr
}

fn select_ironwood_nullifier_root(json: &serde_json::Value) -> Result<&str, String> {
    let dataset_version = json["dataset_version"]
        .as_u64()
        .ok_or_else(|| "PIR /root response missing dataset_version".to_string())?;
    let root_field = match dataset_version {
        LEGACY_PIR_DATASET_VERSION => "root29",
        RUNTIME_TWO_TIER_PIR_DATASET_VERSION => "circuit_root",
        unsupported => {
            return Err(format!(
                "PIR server dataset version {unsupported} is not supported; expected 1 or 2"
            ));
        }
    };

    json[root_field]
        .as_str()
        .filter(|root| !root.is_empty())
        .ok_or_else(|| {
            format!("PIR dataset version {dataset_version} response missing {root_field}")
        })
}

#[test]
fn selects_version_specific_ironwood_nullifier_root() {
    let legacy = json!({
        "dataset_version": 1,
        "root29": "legacy",
        "circuit_root": "semantic",
    });
    assert_eq!(select_ironwood_nullifier_root(&legacy).unwrap(), "legacy");

    let two_tier = json!({
        "dataset_version": 2,
        "root29": "legacy",
        "circuit_root": "semantic",
    });
    assert_eq!(
        select_ironwood_nullifier_root(&two_tier).unwrap(),
        "semantic"
    );
}

#[test]
fn rejects_dataset_v2_without_circuit_root() {
    let response = json!({
        "dataset_version": 2,
        "root29": "legacy",
    });
    let err = select_ironwood_nullifier_root(&response).unwrap_err();
    assert!(err.contains("dataset version 2 response missing circuit_root"));
}

/// Get the latest block height from lightwalletd (Zcash mainnet).
fn get_lightwalletd_latest_height() -> u64 {
    let host = lightwalletd_host();
    log(&format!("fetching latest block height from {}...", host));

    let output = std::process::Command::new("grpcurl")
        .args([
            &host,
            "cash.z.wallet.sdk.rpc.CompactTxStreamer/GetLatestBlock",
        ])
        .output()
        .expect("failed to run grpcurl");

    assert!(
        output.status.success(),
        "grpcurl GetLatestBlock failed: {}",
        String::from_utf8_lossy(&output.stderr)
    );

    let json: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("grpcurl output is not valid JSON");
    let height = json["height"]
        .as_str()
        .and_then(|s| s.parse().ok())
        .or_else(|| json["height"].as_u64())
        .expect("failed to parse height from GetLatestBlock response");
    log(&format!("lightwalletd latest height: {}", height));
    height
}

/// Get the snapshot height: from env var, or default to a recent mainnet height.
fn snapshot_height() -> u64 {
    if let Ok(h) = std::env::var("ZASHI_SNAPSHOT_HEIGHT") {
        return h.parse().expect("ZASHI_SNAPSHOT_HEIGHT must be a number");
    }
    // Use latest Zcash mainnet height - 100 as a safe default.
    // The offset ensures the tree state is finalized and available.
    let latest = get_lightwalletd_latest_height();
    log(&format!(
        "no ZASHI_SNAPSHOT_HEIGHT set; using mainnet height {} - 100 = {}",
        latest,
        latest - 100
    ));
    latest - 100
}

/// The proposals for the voting round.
fn proposals() -> serde_json::Value {
    if let Ok(raw_count) = std::env::var("ZASHI_PROPOSAL_COUNT") {
        let count = raw_count
            .parse::<u32>()
            .expect("ZASHI_PROPOSAL_COUNT must be a positive integer");
        assert!(
            (1..=zcash_voting::MAX_PROPOSAL_ID).contains(&count),
            "ZASHI_PROPOSAL_COUNT must be between 1 and {}",
            zcash_voting::MAX_PROPOSAL_ID
        );
        return speed_test_proposals(count);
    }

    json!([
        {
            "id": 1,
            "title": "Zcash Shielded Assets (ZSAs)",
            "description": "What is your general sentiment toward including Zcash Shielded Assets (ZSAs) as a protocol feature?\n\nReference: ZIP-227",
            "options": [{"index": 0, "label": "Support"}, {"index": 1, "label": "Oppose"}]
        },
        {
            "id": 2,
            "title": "Network Sustainability Mechanism (NSM)",
            "description": "What is your general sentiment toward adding protocol support for the Network Sustainability Mechanism (NSM), including smoothing the issuance curve, which allows ZEC to be removed from circulation and later reissued as future block rewards to help sustain network security while preserving the 21 million ZEC supply cap?",
            "options": [{"index": 0, "label": "Support"}, {"index": 1, "label": "Oppose"}]
        },
        {
            "id": 3,
            "title": "Consensus Accounts",
            "description": "What is your general sentiment toward adding protocol support for consensus accounts, which generalize the functionality of the dev fund lockbox and reduce the operational expense of collecting ZCG funds and miner rewards?",
            "options": [{"index": 0, "label": "Support"}, {"index": 1, "label": "Oppose"}]
        },
        {
            "id": 4,
            "title": "Orchard Quantum Recoverability",
            "description": "What is your general sentiment toward Orchard quantum recoverability, which aims to ensure that if the security of elliptic curve-based cryptography came into doubt (due to the emergence of a cryptographically relevant quantum computer or otherwise), then new Orchard funds could remain recoverable by a later protocol — as opposed to having to be burnt in order to avoid an unbounded balance violation?\n\nReference: ZIP-2005",
            "options": [{"index": 0, "label": "Support"}, {"index": 1, "label": "Oppose"}]
        }
    ])
}

fn speed_test_proposals(count: u32) -> serde_json::Value {
    serde_json::Value::Array(
        (1..=count)
            .map(|id| {
                json!({
                    "id": id,
                    "title": format!("Atomic batch speed question {id}"),
                    "description": format!(
                        "Local performance test question {id} of {count}."
                    ),
                    "options": [
                        {"index": 0, "label": "Yes"},
                        {"index": 1, "label": "No"}
                    ]
                })
            })
            .collect(),
    )
}

#[test]
fn generates_requested_speed_test_proposals() {
    let generated = speed_test_proposals(zcash_voting::MAX_PROPOSAL_ID);
    let proposals = generated.as_array().unwrap();
    assert_eq!(proposals.len(), zcash_voting::MAX_PROPOSAL_ID as usize);
    assert_eq!(proposals[0]["id"], 1);
    assert_eq!(
        proposals[zcash_voting::MAX_PROPOSAL_ID as usize - 1]["id"],
        zcash_voting::MAX_PROPOSAL_ID
    );
}

fn to_base64(bytes: &[u8]) -> String {
    base64::Engine::encode(&base64::engine::general_purpose::STANDARD, bytes)
}

#[test]
#[ignore = "requires running chain + grpcurl + Ironwood PIR server"]
fn create_round_for_zashi() {
    // ---- Step 0: Ensure Pallas key registered ----
    e2e_tests::setup::ensure_pallas_key_registered();

    // ---- Step 1: Import vote manager key ----
    log("importing vote manager key...");
    let config = default_cosmos_tx_config();
    let vote_manager_address = import_first_vote_manager_key(&config.home_dir);
    log("vote manager key ready ✓");

    // ---- Step 2: Get snapshot height ----
    let snap_height = snapshot_height();
    log(&format!("snapshot height: {}", snap_height));

    // ---- Step 3: Fetch real nc_root from lightwalletd ----
    let nc_root = fetch_ironwood_nc_root(snap_height);
    log(&format!("nc_root: {}", hex::encode(nc_root)));

    // ---- Step 4: Fetch real Ironwood nullifier root ----
    let nullifier_imt_root = fetch_ironwood_nullifier_root(snap_height);

    // ---- Step 5: Compute session fields ----
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs();
    let vote_end_time = now + vote_window_secs();

    let snapshot_blockhash = [0xAAu8; 32]; // placeholder — chain doesn't validate this
    let proposals_hash = [0xBBu8; 32]; // placeholder — chain doesn't validate this

    // ---- Step 6: Build and broadcast coordinator action proposal ----
    log("creating voting session...");
    let mut create_body = json!({
        "creator": vote_manager_address,
        "snapshot_height": snap_height,
        "snapshot_blockhash": to_base64(&snapshot_blockhash),
        "proposals_hash": to_base64(&proposals_hash),
        "vote_end_time": vote_end_time,
        "nullifier_imt_root": to_base64(&nullifier_imt_root),
        "nc_root": to_base64(&nc_root),
        "proposals": proposals(),
    });
    if proxy_delegation() {
        create_body["proxy_delegation"] = json!(true);
    }
    let body = coordinator_action_proposal_payload(
        &vote_manager_address,
        create_body,
        "/svote.v1.MsgCreateVotingSession",
    );

    let vm_config = api::CosmosTxConfig {
        key_name: FIRST_VOTE_MANAGER_KEY_NAME.to_string(),
        home_dir: config.home_dir.clone(),
        chain_id: config.chain_id.clone(),
        node_url: config.node_url.clone(),
    };
    let mut json = broadcast_accepted(&body, &vm_config);
    if proxy_delegation() {
        // One approval is below a proxy round's floor; the second executes it.
        let tx_hash = json["tx_hash"].as_str().expect("tx_hash").to_owned();
        let action_id: u64 =
            committed_event_attribute(&tx_hash, "propose_coordinator_action", "action_id")
                .and_then(|id| id.parse().ok())
                .expect("the proposal names its action id");
        log(&format!(
            "proxy round proposed as action {action_id}; approving..."
        ));
        let approver = import_second_vote_manager_key(&config.home_dir);
        let approver_config = api::CosmosTxConfig {
            key_name: SECOND_VOTE_MANAGER_KEY_NAME.to_string(),
            home_dir: config.home_dir.clone(),
            chain_id: config.chain_id.clone(),
            node_url: config.node_url.clone(),
        };
        json = broadcast_accepted(
            &approve_coordinator_action_payload(&approver, action_id),
            &approver_config,
        );
    }
    let round_id_hex = wait_for_create_round_id(&json).expect("create tx should emit round_id");
    log(&format!("round_id: {}", round_id_hex));
    log("session TX broadcast ✓");

    // ---- Step 7: Wait for round to become ACTIVE ----
    log(&format!(
        "waiting for round {} to become ACTIVE...",
        round_id_hex
    ));
    wait_for_round_status(&round_id_hex, SESSION_STATUS_ACTIVE, 30_000, 1_000)
        .expect("round should become ACTIVE");

    log("========================================");
    log("ROUND CREATED SUCCESSFULLY");
    log(&format!("  round_id:    {}", round_id_hex));
    log(&format!("  proxy:       {}", proxy_delegation()));
    log(&format!("  snapshot:    {}", snap_height));
    log(&format!("  nc_root:     {}", hex::encode(nc_root)));
    log(&format!(
        "  imt_root:    {}",
        hex::encode(nullifier_imt_root)
    ));
    log(&format!(
        "  vote_end:    {} ({}s from now)",
        vote_end_time,
        vote_window_secs()
    ));
    log("========================================");
    log("Zashi should now be able to see this round and delegate (ZKP #1).");
}
