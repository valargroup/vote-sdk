//! HTTP client wrapper for Zally REST API.
//!
//! Uses reqwest blocking with retry on socket errors and optional polling
//! for round status.

use serde_json::Value;

/// Base URL for the chain REST API.
/// Default port 1318 matches init.sh (moved from 1317 to avoid Cursor IDE conflict).
/// Uses 127.0.0.1 instead of localhost to avoid IPv6 resolution issues on macOS.
pub fn base_url() -> String {
    std::env::var("ZALLY_API_URL").unwrap_or_else(|_| "http://127.0.0.1:1318".to_string())
}

/// Helper server URL (default port 9091 to avoid the chain's gRPC on 9090).
/// Uses 127.0.0.1 instead of localhost to avoid IPv6 resolution issues on macOS.
pub fn helper_server_url() -> String {
    std::env::var("HELPER_SERVER_URL")
        .unwrap_or_else(|_| "http://127.0.0.1:9091".to_string())
}

/// POST JSON to the helper server. Retries on connection errors.
pub fn post_helper_json(path: &str, body: &Value) -> Result<(u16, Value), Box<dyn std::error::Error + Send + Sync>> {
    let url = format!("{}{}", helper_server_url(), path);
    let mut last_err = None;
    for attempt in 0..MAX_RETRIES {
        match client().post(&url).json(body).send() {
            Ok(resp) => {
                let status = resp.status().as_u16();
                let json: Value = resp.json()?;
                return Ok((status, json));
            }
            Err(e) => {
                last_err = Some(e);
                if let Some(ref err) = last_err {
                    if is_retryable(err) && attempt < MAX_RETRIES - 1 {
                        let backoff_ms = 500 * (attempt + 1) as u64;
                        eprintln!("[E2E] POST helper {} connection error (attempt {}/{}), retrying in {}ms...", path, attempt + 1, MAX_RETRIES, backoff_ms);
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

/// Returns commitment tree next_index (number of leaves) from GET /zally/v1/commitment-tree/latest.
/// Used to detect if delegate/cast txs were committed after a 502 (e.g. EOF or tx in cache).
pub fn commitment_tree_next_index() -> Option<u64> {
    let (status, json) = get_json("/zally/v1/commitment-tree/latest").ok()?;
    if status != 200 {
        return None;
    }
    json.get("tree")?.get("next_index")?.as_u64()
}

/// Returns true if the round's tally for the given proposal has at least one share (decision "1").
/// Used to detect if a reveal tx was committed after a 502.
pub fn tally_has_proposal(round_id_hex: &str, proposal_id: u64) -> bool {
    let path = format!("/zally/v1/tally/{}/{}", round_id_hex, proposal_id);
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

/// Poll GET /zally/v1/round/{round_id_hex} until status equals expected or timeout.
pub fn wait_for_round_status(
    round_id_hex: &str,
    expected: i64,
    timeout_ms: u64,
    interval_ms: u64,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let path = format!("/zally/v1/round/{}", round_id_hex);
    let deadline = std::time::Instant::now() + std::time::Duration::from_millis(timeout_ms);
    let mut polls = 0u32;
    while std::time::Instant::now() < deadline {
        let (_, json) = get_json(&path)?;
        let status = json
            .get("round")
            .and_then(|r| r.get("status"))
            .and_then(|s| s.as_i64())
            .unwrap_or(SESSION_STATUS_UNSPECIFIED);
        polls += 1;
        if status == expected {
            eprintln!("[E2E] Round {} reached status {} after {} poll(s)", round_id_hex, expected, polls);
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
