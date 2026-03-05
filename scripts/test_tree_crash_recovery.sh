#!/usr/bin/env bash
# test_tree_crash_recovery.sh
# Crash/restart determinism checks around commit boundaries.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"
source "$ROOT_DIR/sdk/scripts/_tree_test_lib.sh"

VAL_NAMES=("val1" "val2" "val3")
VAL_API_PORTS=(1418 1518 1618)
VAL_RPC_PORTS=(26157 26257 26357)

SAMPLE_BLOCKS="${SAMPLE_BLOCKS:-20}"
LOAD_MODE="${LOAD_MODE:-deterministic}" # deterministic | real-proof
TREE_LOAD_CMD="${TREE_LOAD_CMD:-}"
REQUIRE_ACTIVE_ROUND="${REQUIRE_ACTIVE_ROUND:-1}"
REAL_PROOF_DELEGATION_COUNT="${REAL_PROOF_DELEGATION_COUNT:-5}"
REAL_PROOF_API_URL="${REAL_PROOF_API_URL:-http://localhost:1418}"
REAL_PROOF_HELPER_URL="${REAL_PROOF_HELPER_URL:-$REAL_PROOF_API_URL}"
REAL_PROOF_VOTE_WINDOW_SECS="${REAL_PROOF_VOTE_WINDOW_SECS:-420}"
REAL_PROOF_TEST_THREADS="${REAL_PROOF_TEST_THREADS:-1}"
REAL_PROOF_INCLUDE_VC_FLOW="${REAL_PROOF_INCLUDE_VC_FLOW:-0}"
ZALLY_PIR_URL="${ZALLY_PIR_URL:-http://localhost:3000}"
export ZALLY_PIR_URL

TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
ARTIFACT_DIR="${ARTIFACT_DIR:-artifacts/tree-crash-recovery/$TIMESTAMP}"
mkdir -p "$ARTIFACT_DIR"
REPORT_FILE="$ARTIFACT_DIR/replay_consistency.txt"
SNAPSHOT_FILE="$ARTIFACT_DIR/per_height_snapshot.tsv"
TREE_TEST_REPORT_FILE="$REPORT_FILE"

echo "# Crash recovery consistency report" > "$REPORT_FILE"
echo -e "phase\theight\tvalidator\troot_hex\tnext_index\tapp_hash\tlatest_height" > "$SNAPSHOT_FILE"

run_load_if_configured() {
  local phase="$1"
  if [[ -z "$RESOLVED_LOAD_CMD" ]]; then
    echo "[${phase}] FAIL: load command is empty" | tee -a "$REPORT_FILE"
    return 1
  fi
  echo "[${phase}] Running load command: $RESOLVED_LOAD_CMD" | tee -a "$REPORT_FILE"
  bash -lc "$RESOLVED_LOAD_CMD"
}

compare_window_all() {
  local phase="$1"
  local min_latest=0
  for rpc in "${VAL_RPC_PORTS[@]}"; do
    local h
    h="$(latest_height "$rpc")"
    if [[ "$min_latest" == "0" || "$h" -lt "$min_latest" ]]; then
      min_latest="$h"
    fi
  done

  local start=$((min_latest - SAMPLE_BLOCKS + 1))
  if [[ "$start" -lt 1 ]]; then
    start=1
  fi

  echo "[${phase}] sampling heights ${start}..${min_latest}" | tee -a "$REPORT_FILE"
  local failed=0

  for ((h=start; h<=min_latest; h++)); do
    local baseline="" mismatch=0
    for i in "${!VAL_NAMES[@]}"; do
      local root next_index has_root app_hash latest
      IFS='|' read -r root next_index has_root <<<"$(tree_at_height "${VAL_API_PORTS[$i]}" "$h")"
      app_hash="$(app_hash_at_height "${VAL_RPC_PORTS[$i]}" "$h")"
      latest="$(latest_height "${VAL_RPC_PORTS[$i]}")"
      local key="${root}|${next_index}|${app_hash}|${has_root}"
      echo -e "${phase}\t${h}\t${VAL_NAMES[$i]}\t${root}\t${next_index}\t${app_hash}\t${latest}" >> "$SNAPSHOT_FILE"
      if [[ -z "$baseline" ]]; then
        baseline="$key"
      elif [[ "$key" != "$baseline" ]]; then
        mismatch=1
      fi
    done
    if [[ "$mismatch" == "1" ]]; then
      echo "[${phase}] mismatch at height=${h}" | tee -a "$REPORT_FILE"
      failed=1
    fi
  done
  return "$failed"
}

kill_val2() {
  local pids
  pids="$(tree_validator_pids_by_suffix "val2")"
  if [[ -z "$pids" ]]; then
    echo "FAIL: val2 process not found" | tee -a "$REPORT_FILE"
    exit 1
  fi
  tree_kill_validator_by_suffix "val2" "val2"
}

wait_val2_down() {
  tree_wait_validator_down 26257
}

restart_val2() {
  tree_restart_validator "val2" "sdk/multi-val2.log" "$HOME/.zallyd-val2"
}

wait_val2_caught_up() {
  tree_wait_validator_caught_up 26257 120
}

echo "=== VC/VAN crash recovery determinism test ==="
echo "Artifacts: $ARTIFACT_DIR"
echo "Load mode: $LOAD_MODE" | tee -a "$REPORT_FILE"

assert_reachable
tree_resolve_load_command
ensure_active_round_if_required
tree_require_effective_load "crash-recovery-load" "$RESOLVED_LOAD_CMD"
run_load_if_configured "pre-crash"

pre_failed=0
if ! compare_window_all "baseline"; then
  pre_failed=1
fi

phase_a_failed=0
phase_b_failed=0

# Phase A: stop before next commit window, then restart/catch up.
echo "[phase-a] stopping val2 before next commit window" | tee -a "$REPORT_FILE"
kill_val2
if ! wait_val2_down; then
  echo "FAIL: val2 did not go down in phase-a" | tee -a "$REPORT_FILE"
  exit 1
fi
if ! tree_wait_height_advance 26157; then
  echo "FAIL: chain did not continue with val2 down (phase-a)" | tee -a "$REPORT_FILE"
  exit 1
fi
run_load_if_configured "phase-a-down"
restart_val2
if ! wait_val2_caught_up; then
  echo "FAIL: val2 failed to catch up in phase-a" | tee -a "$REPORT_FILE"
  exit 1
fi
if ! compare_window_all "phase-a-post-restart"; then
  phase_a_failed=1
fi

# Phase B: detect a commit then kill immediately after observed height bump.
echo "[phase-b] targeting immediate post-commit stop" | tee -a "$REPORT_FILE"
if ! tree_wait_height_advance 26157; then
  echo "FAIL: unable to observe commit boundary on val1 (phase-b)" | tee -a "$REPORT_FILE"
  exit 1
fi
kill_val2
if ! wait_val2_down; then
  echo "FAIL: val2 did not go down in phase-b" | tee -a "$REPORT_FILE"
  exit 1
fi
run_load_if_configured "phase-b-down"
restart_val2
if ! wait_val2_caught_up; then
  echo "FAIL: val2 failed to catch up in phase-b" | tee -a "$REPORT_FILE"
  exit 1
fi
if ! compare_window_all "phase-b-post-restart"; then
  phase_b_failed=1
fi

if [[ "$pre_failed" == "1" || "$phase_a_failed" == "1" || "$phase_b_failed" == "1" ]]; then
  echo "FAIL: replay consistency mismatch detected." | tee -a "$REPORT_FILE"
  echo "See: $REPORT_FILE and $SNAPSHOT_FILE"
  exit 1
fi

echo "PASS: crash/restart replay consistency checks succeeded." | tee -a "$REPORT_FILE"
echo "Snapshot: $SNAPSHOT_FILE"

