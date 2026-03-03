#!/usr/bin/env bash
# test_tree_consistency.sh
# Per-height VC/VAN tree consistency checks across multi-validator nodes.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"
source "$ROOT_DIR/sdk/scripts/_tree_test_lib.sh"

VAL_NAMES=("val1" "val2" "val3")
VAL_API_PORTS=(1418 1518 1618)
VAL_RPC_PORTS=(26157 26257 26357)

SAMPLE_BLOCKS="${SAMPLE_BLOCKS:-20}"
REQUIRE_ACTIVE_ROUND="${REQUIRE_ACTIVE_ROUND:-1}"
RUN_RESTART_PHASE="${RUN_RESTART_PHASE:-1}"
LOAD_MODE="${LOAD_MODE:-deterministic}" # deterministic | real-proof
TREE_LOAD_CMD="${TREE_LOAD_CMD:-}"
TREE_LOAD_CMD_DURING_DOWN="${TREE_LOAD_CMD_DURING_DOWN:-$TREE_LOAD_CMD}"
REAL_PROOF_DELEGATION_COUNT="${REAL_PROOF_DELEGATION_COUNT:-5}"
REAL_PROOF_API_URL="${REAL_PROOF_API_URL:-http://localhost:1418}"
REAL_PROOF_HELPER_URL="${REAL_PROOF_HELPER_URL:-$REAL_PROOF_API_URL}"
REAL_PROOF_VOTE_WINDOW_SECS="${REAL_PROOF_VOTE_WINDOW_SECS:-420}"
REAL_PROOF_TEST_THREADS="${REAL_PROOF_TEST_THREADS:-1}"
REAL_PROOF_INCLUDE_VC_FLOW="${REAL_PROOF_INCLUDE_VC_FLOW:-0}"
ZALLY_PIR_URL="${ZALLY_PIR_URL:-http://localhost:3000}"
export ZALLY_PIR_URL

TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
ARTIFACT_DIR="${ARTIFACT_DIR:-artifacts/tree-consistency/$TIMESTAMP}"
mkdir -p "$ARTIFACT_DIR"
DIFF_FILE="$ARTIFACT_DIR/per_height_diff.txt"
SNAPSHOT_FILE="$ARTIFACT_DIR/per_height_snapshot.tsv"

echo "# Per-height consistency diff" > "$DIFF_FILE"
echo -e "phase\theight\tvalidator\troot_hex\tnext_index\tapp_hash\tlatest_height" > "$SNAPSHOT_FILE"

run_load_phase() {
  local phase="$1"
  local cmd="$2"
  if [[ -z "$cmd" ]]; then
    echo "[${phase}] FAIL: load command is empty"
    return 1
  fi
  echo "[${phase}] Running load command:"
  echo "  $cmd"
  bash -lc "$cmd"
}

compare_height() {
  local phase="$1"
  local height="$2"
  shift 2
  local indexes=("$@")

  local baseline_key=""
  local mismatch=0

  for idx in "${indexes[@]}"; do
    local name="${VAL_NAMES[$idx]}"
    local api_port="${VAL_API_PORTS[$idx]}"
    local rpc_port="${VAL_RPC_PORTS[$idx]}"
    local latest root next_index has_root app_hash

    latest="$(latest_height "$rpc_port")"
    IFS='|' read -r root next_index has_root <<<"$(tree_at_height "$api_port" "$height")"
    app_hash="$(app_hash_at_height "$rpc_port" "$height")"
    local key="${root}|${next_index}|${app_hash}|${has_root}"

    echo -e "${phase}\t${height}\t${name}\t${root}\t${next_index}\t${app_hash}\t${latest}" >> "$SNAPSHOT_FILE"

    if [[ -z "$baseline_key" ]]; then
      baseline_key="$key"
    elif [[ "$key" != "$baseline_key" ]]; then
      mismatch=1
    fi
  done

  if [[ "$mismatch" == "1" ]]; then
    echo "[${phase}] mismatch at height=${height}" | tee -a "$DIFF_FILE"
    return 1
  fi
  return 0
}

sample_window() {
  local phase="$1"
  shift
  local indexes=("$@")
  local min_latest=0

  for idx in "${indexes[@]}"; do
    local h
    h="$(latest_height "${VAL_RPC_PORTS[$idx]}")"
    if [[ "$min_latest" == "0" || "$h" -lt "$min_latest" ]]; then
      min_latest="$h"
    fi
  done

  local start=$((min_latest - SAMPLE_BLOCKS + 1))
  if [[ "$start" -lt 1 ]]; then
    start=1
  fi

  echo "[${phase}] Sampling heights ${start}..${min_latest}"

  local failed=0
  for ((h=start; h<=min_latest; h++)); do
    if ! compare_height "$phase" "$h" "${indexes[@]}"; then
      failed=1
    fi
  done
  return "$failed"
}

kill_val2() {
  local pids
  pids="$(tree_validator_pids_by_suffix "val2")"
  if [[ -z "$pids" ]]; then
    echo "FAIL: val2 process not found"
    exit 1
  fi
  echo "Stopping val2 pid(s): ${pids}"
  tree_kill_validator_by_suffix "val2" "val2"
}

wait_val2_down() {
  if ! tree_wait_validator_down 26257; then
    echo "FAIL: val2 RPC still reachable after stop"
    return 1
  fi
}

restart_val2() {
  echo "Restarting val2..."
  tree_restart_validator "val2" "sdk/multi-val2.log" "$HOME/.zallyd-val2"
}

wait_val2_caught_up() {
  if ! tree_wait_validator_caught_up 26257 120; then
    echo "FAIL: val2 did not catch up in time"
    return 1
  fi
}

echo "=== VC/VAN tree consistency test ==="
echo "Artifacts: $ARTIFACT_DIR"
echo "Load mode: $LOAD_MODE"

assert_reachable
tree_resolve_load_command_with_down
ensure_active_round_if_required
tree_require_effective_load "phase1-load" "$RESOLVED_LOAD_CMD"
if [[ "$RUN_RESTART_PHASE" == "1" ]]; then
  tree_require_effective_load "phase2-load-val2-down" "$RESOLVED_LOAD_CMD_DURING_DOWN"
fi

run_load_phase "phase1-load" "$RESOLVED_LOAD_CMD"

phase1_failed=0
if ! sample_window "phase1" 0 1 2; then
  phase1_failed=1
fi

phase2_failed=0
phase3_failed=0

if [[ "$RUN_RESTART_PHASE" == "1" ]]; then
  kill_val2
  wait_val2_down
  if ! tree_wait_height_advance 26157; then
    echo "FAIL: chain did not advance on rpc :26157"
    exit 1
  fi

  run_load_phase "phase2-load-val2-down" "$RESOLVED_LOAD_CMD_DURING_DOWN"

  if ! sample_window "phase2-val2-down" 0 2; then
    phase2_failed=1
  fi

  restart_val2
  wait_val2_caught_up

  if ! sample_window "phase3-post-restart" 0 1 2; then
    phase3_failed=1
  fi
fi

if [[ "$phase1_failed" == "1" || "$phase2_failed" == "1" || "$phase3_failed" == "1" ]]; then
  echo "FAIL: inconsistencies found. See $DIFF_FILE and $SNAPSHOT_FILE"
  exit 1
fi

echo "PASS: per-height consistency checks succeeded."
echo "Snapshot: $SNAPSHOT_FILE"
echo "Diff:     $DIFF_FILE"

