//! HTTP client wrapper for Shielded-Vote REST API.
//!
//! Uses reqwest blocking with retry on socket errors and optional polling
//! for round status.

use serde_json::Value;

/// Base URL for the chain REST API.
/// Uses 127.0.0.1 instead of localhost to avoid IPv6 resolution issues on macOS.
pub fn base_url() -> String {
    std::env::var("SVOTE_API_URL").unwrap_or_else(|_| "http://127.0.0.1:1317".to_string())
}

/// Helper server URL. Since the helper is now integrated into svoted, it
/// serves on the same port as the chain REST API (default 1317).
/// Uses 127.0.0.1 instead of localhost to avoid IPv6 resolution issues on macOS.
pub fn helper_server_url() -> String {
    std::env::var("HELPER_SERVER_URL")
        .unwrap_or_else(|_| base_url())
}

fn helper_api_token() -> Option<String> {
    std::env::var("HELPER_API_TOKEN")
        .ok()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
}

/// POST JSON to the helper server. Retries on connection and decode errors.
pub fn post_helper_json(path: &str, body: &Value) -> Result<(u16, Value), Box<dyn std::error::Error + Send + Sync>> {
    let url = format!("{}{}", helper_server_url(), path);
    let mut last_err: Option<Box<dyn std::error::Error + Send + Sync>> = None;
    let token = helper_api_token();
    for attempt in 0..MAX_RETRIES {
        let mut request = client().post(&url).json(body);
        if let Some(ref token) = token {
            request = request.header("X-Helper-Token", token);
        }
        match request.send() {
            Ok(resp) => {
                let status = resp.status().as_u16();
                match resp.json::<Value>() {
                    Ok(json) => return Ok((status, json)),
                    Err(e) => {
                        if attempt < MAX_RETRIES - 1 {
                            let backoff_ms = 200 * (attempt + 1) as u64;
                            eprintln!("[E2E] POST helper {} decode error (attempt {}/{}), retrying in {}ms...", path, attempt + 1, MAX_RETRIES, backoff_ms);
                            std::thread::sleep(std::time::Duration::from_millis(backoff_ms));
                            last_err = Some(e.into());
                            continue;
                        }
                        return Err(e.into());
                    }
                }
            }
            Err(e) => {
                if is_retryable(&e) && attempt < MAX_RETRIES - 1 {
                    let backoff_ms = 500 * (attempt + 1) as u64;
                    eprintln!("[E2E] POST helper {} connection error (attempt {}/{}), retrying in {}ms...", path, attempt + 1, MAX_RETRIES, backoff_ms);
                    std::thread::sleep(std::time::Duration::from_millis(backoff_ms));
                    last_err = Some(e.into());
                    continue;
                }
                return Err(e.into());
            }
        }
    }
    Err(last_err.unwrap_or_else(|| "max retries exhausted".into()))
}

/// Create a blocking client with a reasonable timeout.
pub fn client() -> reqwest::blocking::Client {
    reqwest::blocking::Client::builder()
        .timeout(std::time::Duration::from_secs(30))
        .build()
        .expect("reqwest client")
}

fn is_retryable(err: &reqwest::Error) -> bool {
    if err.is_connect() || err.is_timeout() {
        return true;
    }
    let msg = err.to_string();
    if msg.contains("connection reset") || msg.contains("ECONNRESET") {
        return true;
    }
    false
}

/// Max retries for 502 (node/CometBFT busy) and connection errors.
const MAX_RETRIES: u32 = 12;

/// Single POST with no retries. Use when 502 is an acceptable outcome (e.g. duplicate tx).
pub fn post_json_once(path: &str, body: &Value) -> Result<(u16, Value), Box<dyn std::error::Error + Send + Sync>> {
    let url = format!("{}{}", base_url(), path);
    let resp = client().post(&url).json(body).send()?;
    let status = resp.status().as_u16();
    let json: Value = resp.json()?;
    Ok((status, json))
}

/// POST JSON to path; retry on 502 (node busy) and on retryable socket errors.
/// Stops retrying immediately if the 502 body indicates the tx is already in the
/// mempool cache (no point retrying — the server now returns 200 for that case,
/// but this guard catches older server versions or race windows).
pub fn post_json(path: &str, body: &Value) -> Result<(u16, Value), Box<dyn std::error::Error + Send + Sync>> {
    let url = format!("{}{}", base_url(), path);
    let mut last_err = None;
    for attempt in 0..MAX_RETRIES {
        match client().post(&url).json(body).send() {
            Ok(resp) => {
                let status = resp.status().as_u16();
                if status == 502 && attempt < MAX_RETRIES - 1 {
                    // Read body to check for "already in cache" before deciding to retry.
                    let json: Value = resp.json().unwrap_or(Value::Null);
                    let err_text = json.get("error").and_then(|e| e.as_str()).unwrap_or("");
                    if err_text.contains("already exists in cache") || err_text.contains("already in cache") {
                        eprintln!("[E2E] POST {} tx already in mempool cache; not retrying", path);
                        return Ok((status, json));
                    }
                    let backoff = 4 * (attempt + 1) as u64;
                    eprintln!("[E2E] POST {} returned 502 (attempt {}/{}), retrying in {}s...", path, attempt + 1, MAX_RETRIES, backoff);
                    std::thread::sleep(std::time::Duration::from_secs(backoff));
                    continue;
                }
                let json: Value = resp.json()?;
                return Ok((status, json));
            }
            Err(e) => {
                last_err = Some(e);
                if let Some(ref err) = last_err {
                    if is_retryable(err) && attempt < MAX_RETRIES - 1 {
                        let backoff_ms = 500 * (attempt + 1) as u64;
                        eprintln!("[E2E] POST {} connection error (attempt {}/{}), retrying in {}ms...", path, attempt + 1, MAX_RETRIES, backoff_ms);
                        std::thread::sleep(std::time::Duration::from_millis(backoff_ms));
                        continue;
                    }
                }
                break;
            }
        }
    }
    Err(last_err.unwrap_or_else(|| unreachable!()).into())
}

/// GET path; retry on 502 and on retryable socket errors.
pub fn get_json(path: &str) -> Result<(u16, Value), Box<dyn std::error::Error + Send + Sync>> {
    let url = format!("{}{}", base_url(), path);
    let mut last_err = None;
    for attempt in 0..MAX_RETRIES {
        match client().get(&url).send() {
            Ok(resp) => {
                let status = resp.status().as_u16();
                if status == 502 && attempt < MAX_RETRIES - 1 {
                    let backoff = 2 * (attempt + 1) as u64;
                    eprintln!("[E2E] GET {} returned 502 (attempt {}/{}), retrying in {}s...", path, attempt + 1, MAX_RETRIES, backoff);
                    std::thread::sleep(std::time::Duration::from_secs(backoff));
                    continue;
                }
                let json: Value = resp.json()?;
                return Ok((status, json));
            }
            Err(e) => {
                last_err = Some(e);
                if let Some(ref err) = last_err {
                    if is_retryable(err) && attempt < MAX_RETRIES - 1 {
                        let backoff_ms = 500 * (attempt + 1) as u64;
                        eprintln!("[E2E] GET {} connection error (attempt {}/{}), retrying in {}ms...", path, attempt + 1, MAX_RETRIES, backoff_ms);
                        std::thread::sleep(std::time::Duration::from_millis(backoff_ms));
                        continue;
                    }
                }
                break;
            }
        }
    }
    Err(last_err.unwrap_or_else(|| unreachable!()).into())
}

/// Session status enum (protobuf int32).
pub const SESSION_STATUS_UNSPECIFIED: i64 = 0;
pub const SESSION_STATUS_ACTIVE: i64 = 1;
pub const SESSION_STATUS_TALLYING: i64 = 2;
pub const SESSION_STATUS_FINALIZED: i64 = 3;
pub const SESSION_STATUS_PENDING: i64 = 4;

/// Returns the first validator's operator address from the staking module.
/// Queries the standard Cosmos SDK endpoint at the same base URL.
pub fn get_validator_operator_address() -> Option<String> {
    // Allow explicit override so callers in multi-validator setups can pin a
    // specific validator without relying on staking query ordering.
    if let Ok(addr) = std::env::var("SVOTE_VALIDATOR_ADDR") {
        if !addr.is_empty() {
            return Some(addr);
        }
    }

    let (status, json) = get_json("/cosmos/staking/v1beta1/validators").ok()?;
    if status != 200 {
        return None;
    }

    // If a moniker filter is set, prefer the matching validator.
    if let Ok(moniker) = std::env::var("SVOTE_VALIDATOR_MONIKER") {
        if !moniker.is_empty() {
            if let Some(addr) = json
                .get("validators")?
                .as_array()?
                .iter()
                .find(|v| {
                    v.get("description")
                        .and_then(|d| d.get("moniker"))
                        .and_then(|m| m.as_str())
                        == Some(moniker.as_str())
                })
                .and_then(|v| v.get("operator_address"))
                .and_then(|a| a.as_str())
                .map(|s| s.to_string())
            {
                return Some(addr);
            }
        }
    }

    json.get("validators")?
        .as_array()?
        .first()?
        .get("operator_address")?
        .as_str()
        .map(|s| s.to_string())
}


/// Returns ALL validator operator addresses from the staking module.
pub fn get_all_validator_operator_addresses() -> Option<Vec<String>> {
    let (status, json) = get_json("/cosmos/staking/v1beta1/validators").ok()?;
    if status != 200 {
        return None;
    }
    let validators = json.get("validators")?.as_array()?;
    let addrs: Vec<String> = validators
        .iter()
        .filter_map(|v| v.get("operator_address")?.as_str().map(|s| s.to_string()))
        .collect();
    if addrs.is_empty() {
        None
    } else {
        Some(addrs)
    }
}

/// Returns all validators' (operator_address, moniker) pairs from the staking module.
/// Used by multi-validator setup to match operator addresses to home directories.
pub fn get_validators_with_monikers() -> Option<Vec<(String, String)>> {
    let (status, json) = get_json("/cosmos/staking/v1beta1/validators").ok()?;
    if status != 200 {
        return None;
    }
    let validators = json.get("validators")?.as_array()?;
    let result: Vec<(String, String)> = validators
        .iter()
        .filter_map(|v| {
            let addr = v.get("operator_address")?.as_str()?.to_string();
            let moniker = v
                .get("description")
                .and_then(|d| d.get("moniker"))
                .and_then(|m| m.as_str())
                .unwrap_or("")
                .to_string();
            Some((addr, moniker))
        })
        .collect();
    if result.is_empty() {
        None
    } else {
        Some(result)
    }
}

/// Returns commitment tree next_index (number of leaves) from GET /shielded-vote/v1/commitment-tree/{round_id}/latest.
/// Used to detect if delegate/cast txs were committed after a 502 (e.g. EOF or tx in cache).
pub fn commitment_tree_next_index(round_id: &str) -> Option<u64> {
    let (status, json) = get_json(&format!("/shielded-vote/v1/commitment-tree/{}/latest", round_id)).ok()?;
    if status != 200 {
        return None;
    }
    let tree = json.get("tree")?;
    Some(tree.get("next_index").and_then(|v| v.as_u64()).unwrap_or(0))
}

/// Returns (height, root_b64, next_index) from the latest commitment tree state.
pub fn commitment_tree_latest(round_id: &str) -> Option<(u64, String, u64)> {
    let (status, json) = get_json(&format!("/shielded-vote/v1/commitment-tree/{}/latest", round_id)).ok()?;
    if status != 200 {
        return None;
    }
    let tree = json.get("tree")?;
    let height = tree.get("height")?.as_u64()?;
    let root = tree.get("root")?.as_str()?.to_string();
    let next_index = tree.get("next_index")?.as_u64()?;
    Some((height, root, next_index))
}

/// Returns true if the round's tally for the given proposal has at least one share (decision "1").
/// Used to detect if a reveal tx was committed after a 502.
pub fn tally_has_proposal(round_id_hex: &str, proposal_id: u64) -> bool {
    let path = format!("/shielded-vote/v1/tally/{}/{}", round_id_hex, proposal_id);
    let (status, json) = match get_json(&path) {
        Ok(x) => x,
        Err(_) => return false,
    };
    if status != 200 {
        return false;
    }
    json.get("tally")
        .and_then(|t| t.get("1"))
        .and_then(|v| v.as_str())
        .map(|s| !s.is_empty())
        == Some(true)
}

/// POST JSON with retries; on 502, if `is_committed()` is true, treat as success and return (200, synthetic success).
/// Lets the test continue when the tx was committed but the API got EOF or "tx already in cache".
pub fn post_json_accept_committed<F>(
    path: &str,
    body: &Value,
    is_committed: F,
) -> Result<(u16, Value), Box<dyn std::error::Error + Send + Sync>>
where
    F: FnOnce() -> bool,
{
    let (status, json) = post_json(path, body)?;
    if status == 200 {
        return Ok((status, json));
    }
    if status == 502 {
        // Give the node a moment to make the committed block visible to queries.
        std::thread::sleep(std::time::Duration::from_secs(2));
        if is_committed() {
            eprintln!("[E2E] POST {} returned 502 but tx is committed on-chain; treating as success", path);
            return Ok((
                200,
                serde_json::json!({ "code": 0, "tx_hash": "", "log": "accepted (verified committed)" }),
            ));
        }
    }
    Ok((status, json))
}

// ---------------------------------------------------------------------------
// Standard Cosmos SDK tx signing and broadcasting via `svoted` CLI
// ---------------------------------------------------------------------------

/// Configuration for signing and broadcasting standard Cosmos SDK transactions.
pub struct CosmosTxConfig {
    /// Name of the signing key in the keyring (e.g. "validator").
    pub key_name: String,
    /// Path to the node's home directory (e.g. "$HOME/.svoted").
    pub home_dir: String,
    /// Chain ID (e.g. "svote-1").
    pub chain_id: String,
    /// CometBFT RPC endpoint (e.g. "tcp://localhost:26657").
    pub node_url: String,
}

/// Returns a default CosmosTxConfig for the single-validator dev chain setup.
pub fn default_cosmos_tx_config() -> CosmosTxConfig {
    let home = std::env::var("HOME").expect("HOME env var must be set");
    let home_dir = std::env::var("SVOTE_HOME")
        .unwrap_or_else(|_| format!("{}/.svoted", home));
    let node_url = std::env::var("SVOTE_NODE_URL")
        .unwrap_or_else(|_| "tcp://localhost:26657".to_string());
    CosmosTxConfig {
        key_name: "validator".to_string(),
        home_dir,
        chain_id: "svote-1".to_string(),
        node_url,
    }
}

/// Sign and broadcast a standard Cosmos SDK transaction containing the given
/// message. The message must include an `@type` field with the full protobuf
/// type URL (e.g. "/svote.v1.MsgRegisterPallasKey").
///
/// When `SVOTE_SSH_HOST` is set, sign and broadcast commands run on the remote
/// host via SSH (the remote has the vote module types registered in its `svoted`
/// binary). The remote binary path defaults to `svoted` but can be overridden
/// with `SVOTE_REMOTE_SVOTED`.
///
/// Returns `(200, json)` on successful broadcast or an error.
pub fn broadcast_cosmos_msg(
    msg: &Value,
    config: &CosmosTxConfig,
) -> Result<(u16, Value), Box<dyn std::error::Error + Send + Sync>> {
    let ssh_host = std::env::var("SVOTE_SSH_HOST").ok();
    if let Some(host) = ssh_host {
        broadcast_cosmos_msg_ssh(msg, config, &host)
    } else {
        broadcast_cosmos_msg_local(msg, config)
    }
}

/// Local sign + broadcast (original behavior).
fn broadcast_cosmos_msg_local(
    msg: &Value,
    config: &CosmosTxConfig,
) -> Result<(u16, Value), Box<dyn std::error::Error + Send + Sync>> {
    use std::io::Write;
    use std::process::Command;

    let unsigned_tx = build_unsigned_tx(msg);

    let tmp_dir = std::env::temp_dir();
    let ts = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let unsigned_path = tmp_dir.join(format!("sv_unsigned_{}.json", ts));
    let signed_path = tmp_dir.join(format!("sv_signed_{}.json", ts));

    // Write unsigned tx to temp file.
    {
        let mut f = std::fs::File::create(&unsigned_path)?;
        f.write_all(serde_json::to_string_pretty(&unsigned_tx)?.as_bytes())?;
    }

    // Sign via svoted tx sign.
    let sign_output = Command::new("svoted")
        .args([
            "tx", "sign",
            unsigned_path.to_str().unwrap(),
            "--from", &config.key_name,
            "--keyring-backend", "test",
            "--chain-id", &config.chain_id,
            "--home", &config.home_dir,
            "--node", &config.node_url,
            "--output-document", signed_path.to_str().unwrap(),
            "--yes",
        ])
        .output()?;

    // Clean up unsigned file.
    let _ = std::fs::remove_file(&unsigned_path);

    if !sign_output.status.success() {
        let _ = std::fs::remove_file(&signed_path);
        let stderr = String::from_utf8_lossy(&sign_output.stderr);
        return Err(format!("svoted tx sign failed: {}", stderr).into());
    }

    // Broadcast via svoted tx broadcast.
    let broadcast_output = Command::new("svoted")
        .args([
            "tx", "broadcast",
            signed_path.to_str().unwrap(),
            "--node", &config.node_url,
            "--output", "json",
        ])
        .output()?;

    // Clean up signed file.
    let _ = std::fs::remove_file(&signed_path);

    if !broadcast_output.status.success() {
        let stderr = String::from_utf8_lossy(&broadcast_output.stderr);
        return Err(format!("svoted tx broadcast failed: {}", stderr).into());
    }

    parse_broadcast_stdout(&broadcast_output.stdout)
}

/// Remote sign + broadcast via SSH.
///
/// Pipes the unsigned tx to a temp file on the remote host, signs and
/// broadcasts there, then cleans up. This lets us use the server's `svoted`
/// binary which has the vote module types registered.
fn broadcast_cosmos_msg_ssh(
    msg: &Value,
    config: &CosmosTxConfig,
    ssh_host: &str,
) -> Result<(u16, Value), Box<dyn std::error::Error + Send + Sync>> {
    use std::io::Write;
    use std::process::Command;

    let remote_svoted = std::env::var("SVOTE_REMOTE_SVOTED")
        .unwrap_or_else(|_| "svoted".to_string());

    let unsigned_tx = build_unsigned_tx(msg);
    let unsigned_json = serde_json::to_string_pretty(&unsigned_tx)?;

    let ts = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let remote_unsigned = format!("/tmp/sv_unsigned_{}.json", ts);
    let remote_signed = format!("/tmp/sv_signed_{}.json", ts);

    // Pipe unsigned tx to remote temp file.
    let mut upload = Command::new("ssh")
        .args([ssh_host, &format!("cat > {}", remote_unsigned)])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::piped())
        .spawn()?;
    upload
        .stdin
        .as_mut()
        .expect("stdin pipe")
        .write_all(unsigned_json.as_bytes())?;
    let upload_status = upload.wait()?;
    if !upload_status.success() {
        return Err("failed to upload unsigned tx to remote".into());
    }

    // Sign on remote.
    let sign_output = Command::new("ssh")
        .args([
            ssh_host,
            &format!(
                "{svoted} tx sign {unsigned} \
                 --from {from} \
                 --keyring-backend test \
                 --chain-id {chain_id} \
                 --home {home} \
                 --node {node} \
                 --output-document {signed} \
                 --yes",
                svoted = remote_svoted,
                unsigned = remote_unsigned,
                from = config.key_name,
                chain_id = config.chain_id,
                home = config.home_dir,
                node = config.node_url,
                signed = remote_signed,
            ),
        ])
        .output()?;

    // Clean up remote unsigned file regardless of sign outcome.
    let _ = Command::new("ssh")
        .args([ssh_host, &format!("rm -f {}", remote_unsigned)])
        .output();

    if !sign_output.status.success() {
        let _ = Command::new("ssh")
            .args([ssh_host, &format!("rm -f {}", remote_signed)])
            .output();
        let stderr = String::from_utf8_lossy(&sign_output.stderr);
        return Err(format!("svoted tx sign (remote) failed: {}", stderr).into());
    }

    // Broadcast on remote.
    let broadcast_output = Command::new("ssh")
        .args([
            ssh_host,
            &format!(
                "{svoted} tx broadcast {signed} \
                 --node {node} \
                 --output json",
                svoted = remote_svoted,
                signed = remote_signed,
                node = config.node_url,
            ),
        ])
        .output()?;

    // Clean up remote signed file.
    let _ = Command::new("ssh")
        .args([ssh_host, &format!("rm -f {}", remote_signed)])
        .output();

    if !broadcast_output.status.success() {
        let stderr = String::from_utf8_lossy(&broadcast_output.stderr);
        return Err(format!("svoted tx broadcast (remote) failed: {}", stderr).into());
    }

    parse_broadcast_stdout(&broadcast_output.stdout)
}

/// Build the unsigned tx JSON envelope around a message.
fn build_unsigned_tx(msg: &Value) -> Value {
    serde_json::json!({
        "body": {
            "messages": [msg],
            "memo": "",
            "timeout_height": "0",
            "extension_options": [],
            "non_critical_extension_options": []
        },
        "auth_info": {
            "signer_infos": [],
            "fee": {
                "amount": [],
                "gas_limit": "200000",
                "payer": "",
                "granter": ""
            }
        },
        "signatures": []
    })
}

/// Parse svoted broadcast stdout JSON, normalizing field names.
fn parse_broadcast_stdout(
    stdout_bytes: &[u8],
) -> Result<(u16, Value), Box<dyn std::error::Error + Send + Sync>> {
    let stdout = String::from_utf8_lossy(stdout_bytes);
    let mut result: Value = serde_json::from_str(&stdout)
        .map_err(|e| format!("failed to parse broadcast output: {} (raw: {})", e, stdout))?;

    // Normalize field names for compatibility with existing test assertions:
    // svoted outputs "txhash" and "raw_log"; tests expect "tx_hash" and "log".
    if let Some(obj) = result.as_object_mut() {
        if let Some(txhash) = obj.remove("txhash") {
            obj.insert("tx_hash".to_string(), txhash);
        }
        if let Some(raw_log) = obj.remove("raw_log") {
            obj.insert("log".to_string(), raw_log);
        }
    }

    Ok((200, result))
}

/// Returns the bech32 account address for a key in the test keyring.
///
/// Runs `svoted keys show <name> -a --keyring-backend test --home <home>`.
/// When `SVOTE_SSH_HOST` is set, the command is executed on the remote host
/// via SSH (the keyring lives there, not on the CI runner).
pub fn key_account_address(key_name: &str, home_dir: &str) -> Option<String> {
    use std::process::Command;

    let ssh_host = std::env::var("SVOTE_SSH_HOST").ok();
    let output = if let Some(ref host) = ssh_host {
        let remote_svoted = std::env::var("SVOTE_REMOTE_SVOTED")
            .unwrap_or_else(|_| "svoted".to_string());
        Command::new("ssh")
            .args([
                host.as_str(),
                &format!(
                    "{svoted} keys show {key} -a --keyring-backend test --home {home}",
                    svoted = remote_svoted,
                    key = key_name,
                    home = home_dir,
                ),
            ])
            .output()
            .ok()?
    } else {
        Command::new("svoted")
            .args([
                "keys", "show",
                key_name,
                "-a",
                "--keyring-backend", "test",
                "--home", home_dir,
            ])
            .output()
            .ok()?
    };

    if !output.status.success() {
        return None;
    }
    let addr = String::from_utf8(output.stdout).ok()?.trim().to_string();
    if addr.is_empty() { None } else { Some(addr) }
}

/// Import a hex-encoded secp256k1 private key into the svoted test keyring.
///
/// Runs `svoted keys import-hex <name> <hex> --keyring-backend test --home <home>`.
/// When `SVOTE_SSH_HOST` is set, the command is executed on the remote host via SSH.
/// Silently succeeds if the key already exists (duplicate import).
pub fn import_hex_key(name: &str, hex_privkey: &str, home_dir: &str) {
    use std::process::Command;

    let ssh_host = std::env::var("SVOTE_SSH_HOST").ok();
    let output = if let Some(ref host) = ssh_host {
        let remote_svoted = std::env::var("SVOTE_REMOTE_SVOTED")
            .unwrap_or_else(|_| "svoted".to_string());
        Command::new("ssh")
            .args([
                host.as_str(),
                &format!(
                    "{svoted} keys import-hex {name} {hex} --keyring-backend test --home {home}",
                    svoted = remote_svoted,
                    name = name,
                    hex = hex_privkey,
                    home = home_dir,
                ),
            ])
            .output()
            .expect("failed to run svoted keys import-hex via SSH")
    } else {
        Command::new("svoted")
            .args([
                "keys", "import-hex",
                name,
                hex_privkey,
                "--keyring-backend", "test",
                "--home", home_dir,
            ])
            .output()
            .expect("failed to run svoted keys import-hex")
    };

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        // "duplicated address" or "already exists" means the key was previously imported.
        if stderr.contains("duplicated") || stderr.contains("already exists") || stderr.contains("overwrite") {
            eprintln!("[E2E] Key '{}' already in keyring, skipping import", name);
            return;
        }
        panic!(
            "svoted keys import-hex failed: {}",
            stderr
        );
    }
    eprintln!("[E2E] Imported key '{}' into keyring at {}", name, home_dir);
}

/// Keyring name that init.sh assigns to the first vote-manager key (VM_PRIVKEYS[0]).
pub const FIRST_VOTE_MANAGER_KEY_NAME: &str = "vote-manager-1";

/// Imports the first key from VM_PRIVKEYS into the keyring under
/// [`FIRST_VOTE_MANAGER_KEY_NAME`] and returns its bech32 account address.
///
/// Panics if VM_PRIVKEYS is unset, empty, or the keyring import fails to surface
/// the imported address. Every e2e test that needs to sign as a vote manager
/// uses this helper.
pub fn import_first_vote_manager_key(home_dir: &str) -> String {
    // Mirror the bash helper in scripts/_vote_manager_keys_lib.sh: skip empty
    // entries (leading/trailing/double comma) so ",abc" or " , abc" takes
    // "abc" rather than silently passing "" to svoted keys import-hex.
    let raw = std::env::var("VM_PRIVKEYS")
        .expect("VM_PRIVKEYS env var must be set (comma-separated 64-char hex keys)");
    let vm_privkey = raw
        .split(',')
        .map(str::trim)
        .find(|s| !s.is_empty())
        .expect("VM_PRIVKEYS must contain at least one non-empty key")
        .to_string();
    import_hex_key(FIRST_VOTE_MANAGER_KEY_NAME, &vm_privkey, home_dir);
    key_account_address(FIRST_VOTE_MANAGER_KEY_NAME, home_dir).unwrap_or_else(|| {
        panic!("{} key must be in keyring after import", FIRST_VOTE_MANAGER_KEY_NAME)
    })
}

/// Sign and broadcast a ceremony message via standard Cosmos SDK tx flow,
/// with retries on transient failures (same retry logic as post_json).
pub fn broadcast_cosmos_msg_with_retries(
    msg: &Value,
    config: &CosmosTxConfig,
) -> Result<(u16, Value), Box<dyn std::error::Error + Send + Sync>> {
    let mut last_err = None;
    for attempt in 0..3u32 {
        match broadcast_cosmos_msg(msg, config) {
            Ok((status, json)) => {
                let code = json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1);
                if code == 0 {
                    return Ok((status, json));
                }
                // Non-zero code: return immediately (not a transient failure).
                return Ok((status, json));
            }
            Err(e) => {
                let msg = e.to_string();
                eprintln!(
                    "[E2E] broadcast_cosmos_msg attempt {}/{}: {}",
                    attempt + 1,
                    3,
                    msg
                );
                last_err = Some(e);
                if attempt < 2 {
                    std::thread::sleep(std::time::Duration::from_secs(4));
                }
            }
        }
    }
    Err(last_err.unwrap())
}

/// Lightweight per-round helper queue status from `/shielded-vote/v1/queue-status`.
#[derive(Clone, Debug, Default)]
pub struct HelperQueueStatus {
    pub total: u64,
    pub pending: u64,
    pub submitted: u64,
    pub failed: u64,
}

/// Query the helper's queue-status endpoint for a round.
pub fn get_helper_queue_status(round_id_hex: &str) -> Option<HelperQueueStatus> {
    let url = format!("{}/shielded-vote/v1/queue-status", helper_server_url());
    let mut request = client().get(&url);
    if let Some(token) = helper_api_token() {
        request = request.header("X-Helper-Token", token);
    }
    let resp = request.send().ok()?;
    if !resp.status().is_success() {
        return None;
    }
    let json: Value = resp.json().ok()?;
    let round = json.get(round_id_hex)?;
    Some(HelperQueueStatus {
        total: round.get("total")?.as_u64()?,
        pending: round.get("pending")?.as_u64()?,
        submitted: round.get("submitted")?.as_u64()?,
        failed: round.get("failed")?.as_u64()?,
    })
}

/// Returns the full round JSON object from a round query.
/// Returns None if the round doesn't exist or the query fails.
pub fn get_round(round_id_hex: &str) -> Option<Value> {
    let path = format!("/shielded-vote/v1/round/{}", round_id_hex);
    let (status, json) = get_json(&path).ok()?;
    if status != 200 {
        return None;
    }
    json.get("round").cloned()
}

/// Returns the EA public key (base64-decoded) from a round query.
/// Returns None if the round doesn't exist, has no ea_pk, or ea_pk is empty.
pub fn get_round_ea_pk(round_id_hex: &str) -> Option<Vec<u8>> {
    use base64::engine::general_purpose::STANDARD as B64;
    use base64::Engine;

    let path = format!("/shielded-vote/v1/round/{}", round_id_hex);
    let (status, json) = get_json(&path).ok()?;
    if status != 200 {
        return None;
    }
    let ea_pk_b64 = json.get("round")?.get("ea_pk")?.as_str()?;
    if ea_pk_b64.is_empty() {
        return None;
    }
    B64.decode(ea_pk_b64).ok()
}

/// Poll GET /shielded-vote/v1/round/{round_id_hex} until status reaches expected or timeout.
/// Accepts statuses that are "at or past" the expected status in the round lifecycle
/// (PENDING → ACTIVE → TALLYING → FINALIZED), so fast transitions between polls
/// don't cause spurious timeouts.
pub fn wait_for_round_status(
    round_id_hex: &str,
    expected: i64,
    timeout_ms: u64,
    interval_ms: u64,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    // Round lifecycle order (proto enum values don't match lifecycle order).
    // PENDING(4) → ACTIVE(1) → TALLYING(2) → FINALIZED(3)
    fn lifecycle_rank(status: i64) -> i64 {
        match status {
            4 => 0, // PENDING
            1 => 1, // ACTIVE
            2 => 2, // TALLYING
            3 => 3, // FINALIZED
            _ => -1,
        }
    }

    let path = format!("/shielded-vote/v1/round/{}", round_id_hex);
    let deadline = std::time::Instant::now() + std::time::Duration::from_millis(timeout_ms);
    let expected_rank = lifecycle_rank(expected);
    let mut polls = 0u32;
    while std::time::Instant::now() < deadline {
        let (_, json) = get_json(&path)?;
        let status = json
            .get("round")
            .and_then(|r| r.get("status"))
            .and_then(|s| s.as_i64())
            .unwrap_or(SESSION_STATUS_UNSPECIFIED);
        polls += 1;
        if lifecycle_rank(status) >= expected_rank {
            eprintln!("[E2E] Round {} reached status {} (wanted {}) after {} poll(s)", round_id_hex, status, expected, polls);
            return Ok(());
        }
        if polls == 1 || polls % 10 == 0 {
            eprintln!("[E2E] Round {} status={} (waiting for {}), poll #{}", round_id_hex, status, expected, polls);
        }
        std::thread::sleep(std::time::Duration::from_millis(interval_ms));
    }
    Err(format!(
        "timeout waiting for round {} to reach status {} after {} polls",
        round_id_hex, expected, polls
    )
    .into())
}
