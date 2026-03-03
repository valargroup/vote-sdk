#!/usr/bin/env bash
# test_tree_soak.sh
# "Soak" test = run the system continuously for a long window under repeated
# load and disruption to catch issues that short tests miss (memory growth,
# replay/catch-up drift, intermittent divergence, and restart instability).
# This script performs periodic per-height consistency checks while applying
# load and rolling validator restarts.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"
source "$ROOT_DIR/sdk/scripts/_tree_test_lib.sh"

VAL_NAMES=("val1" "val2" "val3")
VAL_API_PORTS=(1418 1518 1618)
VAL_RPC_PORTS=(26157 26257 26357)
VAL_HOME_SUFFIX=("val1" "val2" "val3")

DURATION_MINUTES="${DURATION_MINUTES:-60}"
SAMPLE_INTERVAL_SEC="${SAMPLE_INTERVAL_SEC:-30}"
RESTART_INTERVAL_SEC="${RESTART_INTERVAL_SEC:-900}"
LOAD_INTERVAL_SEC="${LOAD_INTERVAL_SEC:-120}"
SAMPLE_BLOCKS="${SAMPLE_BLOCKS:-8}"
FINAL_SAMPLE_BLOCKS="${FINAL_SAMPLE_BLOCKS:-30}"
LOAD_MODE="${LOAD_MODE:-deterministic}" # deterministic | real-proof
TREE_LOAD_CMD="${TREE_LOAD_CMD:-}"
REQUIRE_ACTIVE_ROUND="${REQUIRE_ACTIVE_ROUND:-1}"
REAL_PROOF_DELEGATION_COUNT="${REAL_PROOF_DELEGATION_COUNT:-5}"
REAL_PROOF_API_URL="${REAL_PROOF_API_URL:-http://localhost:1418}"
REAL_PROOF_HELPER_URL="${REAL_PROOF_HELPER_URL:-$REAL_PROOF_API_URL}"
REAL_PROOF_VOTE_WINDOW_SECS="${REAL_PROOF_VOTE_WINDOW_SECS:-420}"
REAL_PROOF_TEST_THREADS="${REAL_PROOF_TEST_THREADS:-1}"
REAL_PROOF_INCLUDE_VC_FLOW="${REAL_PROOF_INCLUDE_VC_FLOW:-0}"
FAIL_ON_LOAD_ERROR="${FAIL_ON_LOAD_ERROR:-1}"
ZALLY_PIR_URL="${ZALLY_PIR_URL:-http://localhost:3000}"
export ZALLY_PIR_URL

TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
ARTIFACT_DIR="${ARTIFACT_DIR:-artifacts/tree-soak/$TIMESTAMP}"
mkdir -p "$ARTIFACT_DIR"
SNAPSHOT_FILE="$ARTIFACT_DIR/snapshots.tsv"
RESTART_FILE="$ARTIFACT_DIR/restarts.log"
SUMMARY_FILE="$ARTIFACT_DIR/summary.json"
TREE_TEST_REPORT_FILE=""

echo -e "ts\tphase\theight\tvalidator\troot_hex\tnext_index\tapp_hash\tlatest_height\trss_kb" > "$SNAPSHOT_FILE"
: > "$RESTART_FILE"

rss_kb_for_validator() {
  local suffix="$1"
  local pid
  pid="$(pgrep -f "^zallyd start --home.*${suffix}" | head -n 1 || true)"
  if [[ -z "$pid" ]]; then
    echo "0"
    return
  fi
  ps -o rss= -p "$pid" | tr -d ' ' || echo "0"
}

run_load_if_due() {
  local now_epoch="$1"
  if [[ -z "$RESOLVED_LOAD_CMD" ]]; then
    return
  fi
  if [[ "$now_epoch" -lt "$NEXT_LOAD_EPOCH" ]]; then
    return
  fi
  echo "[load] $RESOLVED_LOAD_CMD"
  if ! bash -lc "$RESOLVED_LOAD_CMD"; then
    echo "[load] command failed"
    LOAD_FAILURE_COUNT=$((LOAD_FAILURE_COUNT + 1))
    MISMATCH_COUNT=$((MISMATCH_COUNT + 1))
    if [[ "$FAIL_ON_LOAD_ERROR" == "1" ]]; then
      return 1
    fi
    echo "[load] FAIL_ON_LOAD_ERROR=0, continuing soak"
  fi
  NEXT_LOAD_EPOCH=$((now_epoch + LOAD_INTERVAL_SEC))
}

compare_window() {
  local phase="$1"
  local sample_blocks="$2"
  local min_latest=0
  for rpc in "${VAL_RPC_PORTS[@]}"; do
    local h
    h="$(latest_height "$rpc")"
    if [[ "$min_latest" == "0" || "$h" -lt "$min_latest" ]]; then
      min_latest="$h"
    fi
  done

  local start=$((min_latest - sample_blocks + 1))
  if [[ "$start" -lt 1 ]]; then
    start=1
  fi

  local phase_failed=0
  for ((h=start; h<=min_latest; h++)); do
    local baseline="" mismatch=0
    local ts
    ts="$(date +%s)"
    for i in "${!VAL_NAMES[@]}"; do
      local root next_index has_root app_hash latest rss
      IFS='|' read -r root next_index has_root <<<"$(tree_at_height "${VAL_API_PORTS[$i]}" "$h")"
      app_hash="$(app_hash_at_height "${VAL_RPC_PORTS[$i]}" "$h")"
      latest="$(latest_height "${VAL_RPC_PORTS[$i]}")"
      rss="$(rss_kb_for_validator "${VAL_HOME_SUFFIX[$i]}")"
      echo -e "${ts}\t${phase}\t${h}\t${VAL_NAMES[$i]}\t${root}\t${next_index}\t${app_hash}\t${latest}\t${rss}" >> "$SNAPSHOT_FILE"
      local key="${root}|${next_index}|${app_hash}|${has_root}"
      if [[ -z "$baseline" ]]; then
        baseline="$key"
      elif [[ "$key" != "$baseline" ]]; then
        mismatch=1
      fi
    done
    if [[ "$mismatch" == "1" ]]; then
      phase_failed=1
    fi
  done

  if [[ "$phase_failed" == "1" ]]; then
    MISMATCH_COUNT=$((MISMATCH_COUNT + 1))
    echo "mismatch phase=${phase} range=${start}-${min_latest}"
  fi
}

rolling_restart() {
  local idx="$1"
  local name="${VAL_NAMES[$idx]}"
  local suffix="${VAL_HOME_SUFFIX[$idx]}"
  local rpc_port="${VAL_RPC_PORTS[$idx]}"
  local ts
  ts="$(date +%s)"

  local pids
  pids="$(tree_validator_pids_by_suffix "$suffix")"
  if [[ -z "$pids" ]]; then
    echo -e "${ts}\t${name}\tmissing_pid\tskipped" >> "$RESTART_FILE"
    return
  fi
  echo "[restart] ${name} pid(s)=${pids}"
  tree_kill_validator_by_suffix "$suffix" "$name"
  sleep 2
  tree_restart_validator "$suffix" "sdk/multi-${suffix}.log" "$HOME/.zallyd-${suffix}"
  if tree_wait_validator_caught_up "$rpc_port" 180; then
    echo -e "${ts}\t${name}\trestarted\tcaught_up" >> "$RESTART_FILE"
  else
    echo -e "${ts}\t${name}\trestarted\ttimeout" >> "$RESTART_FILE"
    MISMATCH_COUNT=$((MISMATCH_COUNT + 1))
  fi
}

build_summary() {
  local end_epoch="$1"
  python3 - "$SNAPSHOT_FILE" "$RESTART_FILE" "$SUMMARY_FILE" "$DURATION_MINUTES" "$SAMPLE_INTERVAL_SEC" "$RESTART_INTERVAL_SEC" "$MISMATCH_COUNT" "$LOAD_FAILURE_COUNT" "$end_epoch" <<'PY'
import csv
import json
import statistics
import sys
from collections import defaultdict

snap_path, restart_path, out_path, duration_m, sample_sec, restart_sec, mismatch_count, load_failure_count, end_epoch = sys.argv[1:]
duration_m = int(duration_m)
sample_sec = int(sample_sec)
restart_sec = int(restart_sec)
mismatch_count = int(mismatch_count)
load_failure_count = int(load_failure_count)

rows = []
with open(snap_path, "r", encoding="utf-8") as f:
    reader = csv.DictReader(f, delimiter="\t")
    for row in reader:
        rows.append(row)

heights_by_ts = defaultdict(list)
rss_by_validator = defaultdict(list)
next_indexes = []
for row in rows:
    ts = int(row["ts"])
    latest = int(row["latest_height"]) if row["latest_height"] else 0
    rss = int(row["rss_kb"]) if row["rss_kb"] else 0
    ni = int(row["next_index"]) if row["next_index"] else 0
    heights_by_ts[ts].append(latest)
    rss_by_validator[row["validator"]].append(rss)
    next_indexes.append(ni)

interval_samples = []
for ts in sorted(heights_by_ts):
    vals = heights_by_ts[ts]
    if vals:
        interval_samples.append(sum(vals) / len(vals))

block_intervals = []
for i in range(1, len(interval_samples)):
    delta_blocks = interval_samples[i] - interval_samples[i - 1]
    if delta_blocks > 0:
        block_intervals.append(sample_sec / delta_blocks)

def pct(values, p):
    if not values:
        return None
    if len(values) == 1:
        return values[0]
    values = sorted(values)
    k = (len(values) - 1) * (p / 100.0)
    f = int(k)
    c = min(f + 1, len(values) - 1)
    if f == c:
        return values[f]
    return values[f] + (values[c] - values[f]) * (k - f)

restart_events = 0
with open(restart_path, "r", encoding="utf-8") as f:
    for line in f:
        if line.strip():
            restart_events += 1

peak_rss = {k: max(v) if v else 0 for k, v in rss_by_validator.items()}
summary = {
    "duration_minutes": duration_m,
    "sample_interval_sec": sample_sec,
    "restart_interval_sec": restart_sec,
    "timestamp_end_epoch": int(end_epoch),
    "mismatch_count": mismatch_count,
    "restart_events": restart_events,
    "samples_recorded": len(rows),
    "consistency_pass": mismatch_count == 0,
    "load_failures": load_failure_count,
    "metrics": {
        "seconds_per_block_estimate_p50": pct(block_intervals, 50),
        "seconds_per_block_estimate_p95": pct(block_intervals, 95),
        "max_next_index_observed": max(next_indexes) if next_indexes else 0,
        "peak_rss_kb_by_validator": peak_rss,
    },
    "notes": {
        "tx_throughput_per_block": "derive from configured load command logs",
        "duplicate_nullifier_counts": "derive from app logs / tx responses",
    },
}

with open(out_path, "w", encoding="utf-8") as f:
    json.dump(summary, f, indent=2, sort_keys=True)
PY
}

echo "=== VC/VAN soak test ==="
echo "Artifacts: $ARTIFACT_DIR"
echo "Load mode: $LOAD_MODE"

assert_reachable
tree_resolve_load_command
ensure_active_round_if_required
tree_require_effective_load "soak-load" "$RESOLVED_LOAD_CMD"

START_EPOCH="$(date +%s)"
END_EPOCH=$((START_EPOCH + DURATION_MINUTES * 60))
NEXT_SAMPLE_EPOCH="$START_EPOCH"
NEXT_RESTART_EPOCH=$((START_EPOCH + RESTART_INTERVAL_SEC))
NEXT_LOAD_EPOCH="$START_EPOCH"
MISMATCH_COUNT=0
LOAD_FAILURE_COUNT=0
restart_cycle=0
LOAD_ERROR_FATAL=0

while true; do
  now="$(date +%s)"
  if [[ "$now" -ge "$END_EPOCH" ]]; then
    break
  fi

  if ! run_load_if_due "$now"; then
    LOAD_ERROR_FATAL=1
    break
  fi

  if [[ "$now" -ge "$NEXT_SAMPLE_EPOCH" ]]; then
    compare_window "soak" "$SAMPLE_BLOCKS"
    NEXT_SAMPLE_EPOCH=$((now + SAMPLE_INTERVAL_SEC))
  fi

  if [[ "$now" -ge "$NEXT_RESTART_EPOCH" ]]; then
    # Avoid restarting val1 because val2+val3 do not have >2/3 stake.
    if [[ "$restart_cycle" -eq 0 ]]; then
      rolling_restart 1
      restart_cycle=1
    else
      rolling_restart 2
      restart_cycle=0
    fi
    NEXT_RESTART_EPOCH=$((now + RESTART_INTERVAL_SEC))
  fi

  sleep 2
done

compare_window "final-sweep" "$FINAL_SAMPLE_BLOCKS"
build_summary "$(date +%s)"

if [[ "$LOAD_ERROR_FATAL" == "1" ]]; then
  echo "FAIL: soak load command failed."
  echo "See $SUMMARY_FILE and $SNAPSHOT_FILE"
  exit 1
fi

if [[ "$MISMATCH_COUNT" -gt 0 ]]; then
  echo "FAIL: soak detected consistency mismatches (count=$MISMATCH_COUNT)."
  echo "See $SUMMARY_FILE and $SNAPSHOT_FILE"
  exit 1
fi

echo "PASS: soak completed without detected consistency mismatches."
echo "Summary: $SUMMARY_FILE"

