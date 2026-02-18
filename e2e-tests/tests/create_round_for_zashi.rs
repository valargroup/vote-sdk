//! Create a voting round with real nc_root and nullifier_imt_root so that
//! Zashi can successfully generate ZKP #1 (delegation proof).
//!
//! The admin UI uses stub values (0xdd*32 for nc_root, 0xcc*32 for
//! nullifier_imt_root) which causes ZKP #1 to fail because the Merkle roots
//! don't match the real chain state. This test fetches the real values from
//! lightwalletd (via grpcurl) and the IMT server, then creates a session
//! that Zashi can actually delegate to.
//!
//! Prerequisites:
//!   - Local zallyd chain running (port 1318)
//!   - `grpcurl` installed (brew install grpcurl)
//!   - EA ceremony already CONFIRMED on the chain
//!   - IMT server reachable (default: http://46.101.255.48:3000)
//!
//! Environment variables:
//!   ZASHI_SNAPSHOT_HEIGHT  - Block height for the snapshot (default: latest - 10)
//!   ZASHI_LIGHTWALLETD     - Lightwalletd host:port (default: us.zec.stardust.rest:443)
//!   ZASHI_IMT_URL          - IMT server URL (default: http://46.101.255.48:3000)
//!   ZASHI_VOTE_WINDOW_SECS - Voting window in seconds (default: 604800 = 7 days)

use e2e_tests::{
    api::{
        self, broadcast_cosmos_msg, default_cosmos_tx_config, import_hex_key,
        wait_for_round_status, SESSION_STATUS_ACTIVE,
    },
    payloads::{self, SetupRoundFields},
};
use prost::Message;
use serde_json::json;

/// Default vote manager secp256k1 private key (set in genesis).
const VOTE_MANAGER_PRIVKEY_HEX: &str =
    "b7e910eded435dd4e19c581b9a0b8e65104dcc4ebca8a1d55aa5c803e72ba2ee";
/// Bech32 address derived from VOTE_MANAGER_PRIVKEY_HEX with the "zvote" prefix.
const VOTE_MANAGER_ADDRESS: &str = "zvote15fjfr6rrs60vu4st6arrd94w5j6z7f6kxr92cg";

fn log(msg: &str) {
    eprintln!("[create-round] {}", msg);
}

fn lightwalletd_host() -> String {
    std::env::var("ZASHI_LIGHTWALLETD")
        .unwrap_or_else(|_| "us.zec.stardust.rest:443".to_string())
}

fn imt_url() -> String {
    std::env::var("ZASHI_IMT_URL")
        .unwrap_or_else(|_| "http://46.101.255.48:3000".to_string())
}

fn vote_window_secs() -> u64 {
    std::env::var("ZASHI_VOTE_WINDOW_SECS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(604800) // 7 days
}

/// Fetch TreeState at a given height from lightwalletd via grpcurl.
/// Returns the protobuf-encoded TreeState bytes.
fn fetch_tree_state(height: u64) -> Vec<u8> {
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

    let network = json["network"].as_str().unwrap_or("").to_string();
    let hash = json["hash"].as_str().unwrap_or("").to_string();
    let time = json["time"].as_u64().unwrap_or(0) as u32;
    let sapling_tree = json["saplingTree"].as_str().unwrap_or("").to_string();
    let orchard_tree = json["orchardTree"].as_str().unwrap_or("").to_string();

    log(&format!(
        "tree state: network={}, height={}, orchard_tree_len={}",
        network,
        height,
        orchard_tree.len() / 2
    ));

    // Construct protobuf TreeState
    let tree_state = zcash_client_backend::proto::service::TreeState {
        network,
        height,
        hash,
        time,
        sapling_tree,
        orchard_tree,
    };

    let mut buf = Vec::new();
    tree_state.encode(&mut buf).expect("encode TreeState");
    buf
}

/// Fetch the nullifier IMT root from the IMT server.
fn fetch_imt_root() -> [u8; 32] {
    let url = format!("{}/root", imt_url());
    log(&format!("fetching IMT root from {}...", url));

    let resp = api::client()
        .get(&url)
        .send()
        .expect("failed to reach IMT server");
    let json: serde_json::Value = resp.json().expect("IMT /root response is not JSON");
    let root_hex = json["root"]
        .as_str()
        .expect("IMT /root response missing 'root' field");

    // Strip 0x prefix if present
    let hex_str = root_hex.strip_prefix("0x").unwrap_or(root_hex);
    let bytes = hex::decode(hex_str).expect("IMT root is not valid hex");
    assert_eq!(bytes.len(), 32, "IMT root must be 32 bytes");

    let mut arr = [0u8; 32];
    arr.copy_from_slice(&bytes);
    log(&format!("IMT root: {}", hex::encode(arr)));
    arr
}

/// Get the latest block height from lightwalletd (Zcash mainnet).
fn get_lightwalletd_latest_height() -> u64 {
    let host = lightwalletd_host();
    log(&format!("fetching latest block height from {}...", host));

    let output = std::process::Command::new("grpcurl")
        .args([&host, "cash.z.wallet.sdk.rpc.CompactTxStreamer/GetLatestBlock"])
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

fn to_base64(bytes: &[u8]) -> String {
    base64::Engine::encode(&base64::engine::general_purpose::STANDARD, bytes)
}

#[test]
#[ignore = "requires running chain + grpcurl + IMT server"]
fn create_round_for_zashi() {
    // ---- Step 0: Ensure ceremony is CONFIRMED ----
    log("checking ceremony status...");
    let (ea_sk_bytes, ea_pk_bytes) = e2e_tests::setup::load_ea_keypair();
    e2e_tests::setup::bootstrap_ceremony(&ea_sk_bytes, &ea_pk_bytes);
    log("ceremony CONFIRMED ✓");

    // ---- Step 1: Import vote manager key ----
    log("importing vote manager key...");
    let config = default_cosmos_tx_config();
    import_hex_key("vote-manager", VOTE_MANAGER_PRIVKEY_HEX, &config.home_dir);
    log("vote manager key ready ✓");

    // ---- Step 2: Get snapshot height ----
    let snap_height = snapshot_height();
    log(&format!("snapshot height: {}", snap_height));

    // ---- Step 3: Fetch real nc_root from lightwalletd ----
    let tree_state_bytes = fetch_tree_state(snap_height);
    let nc_root = librustvoting::witness::extract_nc_root(&tree_state_bytes)
        .expect("failed to extract nc_root from tree state");
    assert_eq!(nc_root.len(), 32);
    log(&format!("nc_root: {}", hex::encode(&nc_root)));

    // ---- Step 4: Fetch real nullifier_imt_root from IMT server ----
    let nullifier_imt_root = fetch_imt_root();

    // ---- Step 5: Compute session fields and round_id ----
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs();
    let vote_end_time = now + vote_window_secs();

    let snapshot_blockhash = [0xAAu8; 32]; // placeholder — chain doesn't validate this
    let proposals_hash = [0xBBu8; 32]; // placeholder — chain doesn't validate this

    let mut nc_root_arr = [0u8; 32];
    nc_root_arr.copy_from_slice(&nc_root);

    let fields = SetupRoundFields {
        snapshot_height: snap_height,
        snapshot_blockhash,
        proposals_hash,
        vote_end_time,
        nullifier_imt_root,
        nc_root: nc_root_arr,
    };
    let round_id = payloads::derive_round_id(&fields);
    let round_id_hex = hex::encode(round_id);
    log(&format!("round_id: {}", round_id_hex));

    // ---- Step 6: Build and broadcast MsgCreateVotingSession ----
    log("creating voting session...");
    let body = json!({
        "@type": "/zvote.v1.MsgCreateVotingSession",
        "creator": VOTE_MANAGER_ADDRESS,
        "snapshot_height": snap_height,
        "snapshot_blockhash": to_base64(&snapshot_blockhash),
        "proposals_hash": to_base64(&proposals_hash),
        "vote_end_time": vote_end_time,
        "nullifier_imt_root": to_base64(&nullifier_imt_root),
        "nc_root": to_base64(&nc_root),
        "vk_zkp1": to_base64(&[0xf1u8; 64]),
        "vk_zkp2": to_base64(&[0xf2u8; 64]),
        "vk_zkp3": to_base64(&[0xf3u8; 64]),
        "proposals": proposals(),
    });

    let vm_config = api::CosmosTxConfig {
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
    log("session TX broadcast ✓");

    // ---- Step 7: Wait for round to become ACTIVE ----
    log(&format!(
        "waiting for round {} to become ACTIVE...",
        &round_id_hex
    ));
    wait_for_round_status(&round_id_hex, SESSION_STATUS_ACTIVE, 30_000, 1_000)
        .expect("round should become ACTIVE");

    log("========================================");
    log(&format!("ROUND CREATED SUCCESSFULLY"));
    log(&format!("  round_id:    {}", round_id_hex));
    log(&format!("  snapshot:    {}", snap_height));
    log(&format!("  nc_root:     {}", hex::encode(&nc_root)));
    log(&format!("  imt_root:    {}", hex::encode(&nullifier_imt_root)));
    log(&format!(
        "  vote_end:    {} ({}s from now)",
        vote_end_time,
        vote_window_secs()
    ));
    log("========================================");
    log("Zashi should now be able to see this round and delegate (ZKP #1).");
}
