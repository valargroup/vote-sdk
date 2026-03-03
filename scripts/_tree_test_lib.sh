#!/usr/bin/env bash

tree_log_line() {
  local msg="$1"
  if [[ -n "${TREE_TEST_REPORT_FILE:-}" ]]; then
    echo "$msg" | tee -a "$TREE_TEST_REPORT_FILE"
  else
    echo "$msg"
  fi
}

tree_build_tx_env_prefix() {
  local zally_home="${ZALLY_HOME:-}"
  local zally_node_url="${ZALLY_NODE_URL:-}"
  local zally_pallas_pk_path="${ZALLY_PALLAS_PK_PATH:-}"

  # Local multi-validator default: val1 is the tx signing home.
  if [[ -z "$zally_home" && -d "$HOME/.zallyd-val1" ]]; then
    zally_home="$HOME/.zallyd-val1"
  fi
  if [[ -z "$zally_node_url" && -n "$zally_home" ]]; then
    zally_node_url="tcp://localhost:26157"
  fi
  if [[ -z "$zally_pallas_pk_path" && -f "$HOME/.zallyd-val1/pallas.pk" ]]; then
    zally_pallas_pk_path="$HOME/.zallyd-val1/pallas.pk"
  fi

  local prefix=""
  if [[ -n "$zally_home" ]]; then
    prefix+="ZALLY_HOME=\"${zally_home}\" "
  fi
  if [[ -n "$zally_node_url" ]]; then
    prefix+="ZALLY_NODE_URL=\"${zally_node_url}\" "
  fi
  if [[ -n "$zally_pallas_pk_path" ]]; then
    prefix+="ZALLY_PALLAS_PK_PATH=\"${zally_pallas_pk_path}\" "
  fi
  printf "%s" "$prefix"
}

tree_build_real_proof_load_cmd() {
  local sync_cmd vc_cmd tx_env
  tx_env="$(tree_build_tx_env_prefix)"
  sync_cmd="${tx_env}"'ZALLY_API_URL="'"${REAL_PROOF_API_URL}"'" HELPER_SERVER_URL="'"${REAL_PROOF_HELPER_URL}"'" ZALLY_STRESS_DELEGATION_COUNT="'"${REAL_PROOF_DELEGATION_COUNT}"'" ZALLY_E2E_VOTE_WINDOW_SECS="'"${REAL_PROOF_VOTE_WINDOW_SECS}"'" cargo test --release --manifest-path e2e-tests/Cargo.toml --test sync_stress -- --nocapture --ignored --test-threads='"${REAL_PROOF_TEST_THREADS}"
  if [[ "${REAL_PROOF_INCLUDE_VC_FLOW:-0}" == "1" ]]; then
    vc_cmd="${tx_env}"'ZALLY_API_URL="'"${REAL_PROOF_API_URL}"'" HELPER_SERVER_URL="'"${REAL_PROOF_HELPER_URL}"'" ZALLY_E2E_VOTE_WINDOW_SECS="'"${REAL_PROOF_VOTE_WINDOW_SECS}"'" cargo test --release --manifest-path e2e-tests/Cargo.toml --test voting_flow_librustvoting -- --nocapture --ignored --test-threads=1'
    cat <<EOF
${sync_cmd} && ${vc_cmd}
EOF
    return
  fi
  cat <<EOF
${sync_cmd}
EOF
}

tree_require_effective_load() {
  local phase="$1"
  local cmd="$2"
  if [[ -n "$cmd" ]]; then
    return 0
  fi
  if [[ "${ALLOW_EMPTY_LOAD:-0}" == "1" ]]; then
    tree_log_line "[${phase}] ALLOW_EMPTY_LOAD=1 set; running without a load generator."
    return 0
  fi
  tree_log_line "FAIL: no load command configured for ${phase}."
  tree_log_line "Set TREE_LOAD_CMD=<command> or use LOAD_MODE=real-proof."
  tree_log_line "Use ALLOW_EMPTY_LOAD=1 only for debugging static snapshots."
  return 1
}

tree_resolve_load_command() {
  if [[ -n "${TREE_LOAD_CMD:-}" ]]; then
    RESOLVED_LOAD_CMD="$TREE_LOAD_CMD"
    return
  fi
  if [[ "${LOAD_MODE:-deterministic}" == "real-proof" ]]; then
    RESOLVED_LOAD_CMD="$(tree_build_real_proof_load_cmd)"
    return
  fi
  RESOLVED_LOAD_CMD=""
}

tree_resolve_load_command_with_down() {
  if [[ -n "${TREE_LOAD_CMD:-}" ]]; then
    RESOLVED_LOAD_CMD="$TREE_LOAD_CMD"
    RESOLVED_LOAD_CMD_DURING_DOWN="${TREE_LOAD_CMD_DURING_DOWN:-$TREE_LOAD_CMD}"
    return
  fi
  if [[ "${LOAD_MODE:-deterministic}" == "real-proof" ]]; then
    RESOLVED_LOAD_CMD="$(tree_build_real_proof_load_cmd)"
    RESOLVED_LOAD_CMD_DURING_DOWN="$RESOLVED_LOAD_CMD"
    return
  fi
  RESOLVED_LOAD_CMD=""
  RESOLVED_LOAD_CMD_DURING_DOWN=""
}

json_field() {
  local json="$1"
  local expr="$2"
  python3 -c '
import json, sys
expr = sys.argv[1]
raw = sys.stdin.read()
if not raw.strip():
    print("")
    raise SystemExit(0)
try:
    data = json.loads(raw)
except json.JSONDecodeError:
    print("")
    raise SystemExit(0)
parts = [p for p in expr.strip(".").split(".") if p]
cur = data
for p in parts:
    if isinstance(cur, dict) and p in cur:
        cur = cur[p]
    else:
        print("")
        raise SystemExit(0)
if cur is None:
    print("")
elif isinstance(cur, (dict, list)):
    print(json.dumps(cur))
else:
    print(cur)
' "$expr" <<<"$json"
}

latest_height() {
  local rpc_port="$1"
  local status
  status="$(curl -sf "http://127.0.0.1:${rpc_port}/status")"
  json_field "$status" ".result.sync_info.latest_block_height"
}

app_hash_at_height() {
  local rpc_port="$1"
  local height="$2"
  local block_json
  block_json="$(curl -sf "http://127.0.0.1:${rpc_port}/block?height=${height}")"
  json_field "$block_json" ".result.block.header.app_hash"
}

tree_at_height() {
  local api_port="$1"
  local height="$2"
  local tmp_body
  tmp_body="$(mktemp)"
  local code
  code="$(curl -sS -o "$tmp_body" -w "%{http_code}" "http://127.0.0.1:${api_port}/zally/v1/commitment-tree/${height}")"
  if [[ "$code" == "200" ]]; then
    local body root next_index
    body="$(<"$tmp_body")"
    root="$(json_field "$body" ".tree.root")"
    next_index="$(json_field "$body" ".tree.next_index")"
    rm -f "$tmp_body"
    echo "${root}|${next_index}|1"
    return
  fi
  rm -f "$tmp_body"
  echo "|0|0"
}

assert_reachable() {
  for i in "${!VAL_NAMES[@]}"; do
    local name="${VAL_NAMES[$i]}"
    local rpc_port="${VAL_RPC_PORTS[$i]}"
    local api_port="${VAL_API_PORTS[$i]}"
    curl -sf "http://127.0.0.1:${rpc_port}/status" > /dev/null || {
      tree_log_line "FAIL: ${name} RPC unreachable"
      exit 1
    }
    curl -sf "http://127.0.0.1:${api_port}/zally/v1/commitment-tree/latest" > /dev/null || {
      tree_log_line "FAIL: ${name} API unreachable"
      exit 1
    }
  done
}

ensure_active_round_if_required() {
  if [[ "${LOAD_MODE:-deterministic}" == "real-proof" ]]; then
    # sync_stress creates and activates its own round.
    return
  fi
  if [[ "${REQUIRE_ACTIVE_ROUND:-1}" != "1" ]]; then
    return
  fi
  local active_json round_id
  active_json="$(curl -sf "http://127.0.0.1:1418/zally/v1/rounds/active" || true)"
  round_id="$(json_field "$active_json" ".round.vote_round_id")"
  if [[ -z "$round_id" ]]; then
    tree_log_line "FAIL: no ACTIVE round found on val1 (:1418)."
    tree_log_line "Create/activate one manually, or set REQUIRE_ACTIVE_ROUND=0."
    exit 1
  fi
}

tree_wait_height_advance() {
  local rpc_port="$1"
  local before after
  before="$(latest_height "$rpc_port")"
  for _ in $(seq 1 20); do
    sleep 1
    after="$(latest_height "$rpc_port")"
    if [[ "$after" -gt "$before" ]]; then
      return 0
    fi
  done
  return 1
}

tree_validator_pids_by_suffix() {
  local suffix="$1"
  pgrep -f "^zallyd start --home .*/\\.zallyd-${suffix}$" || true
}

tree_kill_validator_by_suffix() {
  local suffix="$1"
  local name="${2:-$suffix}"
  local pids
  pids="$(tree_validator_pids_by_suffix "$suffix")"
  if [[ -z "$pids" ]]; then
    tree_log_line "FAIL: ${name} process not found"
    return 1
  fi
  for pid in $pids; do
    kill "$pid"
  done
}

tree_wait_validator_down() {
  local rpc_port="$1"
  for _ in $(seq 1 20); do
    if ! curl -sf "http://127.0.0.1:${rpc_port}/status" > /dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

tree_restart_validator() {
  local suffix="$1"
  local log_file="$2"
  local home="${3:-$HOME/.zallyd-${suffix}}"
  ZALLY_PIR_URL="${ZALLY_PIR_URL:-http://localhost:3000}" nohup zallyd start --home "$home" >> "$log_file" 2>&1 &
}

tree_wait_validator_caught_up() {
  local rpc_port="$1"
  local max_wait="${2:-120}"
  for _ in $(seq 1 "$max_wait"); do
    local status catching_up
    status="$(curl -sf "http://127.0.0.1:${rpc_port}/status" || true)"
    if [[ -n "$status" ]]; then
      catching_up="$(json_field "$status" ".result.sync_info.catching_up")"
      if [[ "$catching_up" == "False" ]]; then
        return 0
      fi
    fi
    sleep 1
  done
  return 1
}
