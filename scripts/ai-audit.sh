#!/usr/bin/env bash
#
# ai-audit.sh — Automated ZK circuit security audit
#
# Collects spec + code from the repo, sends to Claude for adversarial
# audit, and posts a short Slack summary. Designed for scheduled CI.
#
# Usage:
#   ./scripts/ai-audit.sh collect   # gather context into /tmp/audit-context.txt
#   ./scripts/ai-audit.sh audit     # run AI audit, produce /tmp/audit-report.md
#   ./scripts/ai-audit.sh notify    # post report to Slack
#   ./scripts/ai-audit.sh all       # collect + audit + notify (default)
#
# Required env vars:
#   ANTHROPIC_API_KEY   — Anthropic API key (for audit step)
#   SLACK_WEBHOOK_URL   — Slack incoming webhook (for notify step)
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CONTEXT_FILE="/tmp/audit-context.txt"
REPORT_FILE="/tmp/audit-report.md"
PROMPT_FILE="$REPO_ROOT/scripts/audit-prompt.md"

# ─── Paths to spec and code ──────────────────────────────────────────

SPEC_FILES=(
  "$REPO_ROOT/orchard/src/vote_proof/README.md"
  "$REPO_ROOT/orchard/src/delegation/README.md"
  "$REPO_ROOT/.cursor/rules/security-audit.mdc"
)

# ── Code files ordered by security risk (highest first) ──────────────
#
# Tier 1: ZKP circuits (soundness-critical)
# Tier 2: Vote commitment tree (integrity of Merkle anchors)
# Tier 3: Cosmos SDK vote chain (on-chain verification, state)
# Tier 4: Helper server (ZKP #3 share reveal, relay)
# Tier 5: Nullifier ingest (exclusion proofs for ZKP #1)
#
CODE_FILES=(
  # ── Tier 1: ZKP #1 Delegation + ZKP #2 Vote Proof ──
  "$REPO_ROOT/orchard/src/vote_proof/circuit.rs"
  "$REPO_ROOT/orchard/src/delegation/circuit.rs"
  "$REPO_ROOT/orchard/src/delegation/builder.rs"
  "$REPO_ROOT/orchard/src/delegation/imt.rs"
  "$REPO_ROOT/orchard/src/circuit/gadget/add_chip.rs"

  # ── Tier 2: Vote Commitment Tree ──
  "$REPO_ROOT/vote-commitment-tree/src/hash.rs"
  "$REPO_ROOT/vote-commitment-tree/src/path.rs"
  "$REPO_ROOT/vote-commitment-tree/src/server.rs"
  "$REPO_ROOT/vote-commitment-tree/src/lib.rs"
  "$REPO_ROOT/vote-commitment-tree/src/anchor.rs"

  # ── Tier 3: Cosmos SDK Tally Chain ──
  "$REPO_ROOT/sdk/x/vote/keeper/msg_server.go"
  "$REPO_ROOT/sdk/x/vote/keeper/keeper.go"
  "$REPO_ROOT/sdk/x/vote/ante/validate.go"
  "$REPO_ROOT/sdk/x/vote/types/msgs.go"
  "$REPO_ROOT/sdk/crypto/elgamal/elgamal.go"
  "$REPO_ROOT/sdk/crypto/zkp/halo2/verify.go"
  "$REPO_ROOT/sdk/crypto/redpallas/verify.go"
  "$REPO_ROOT/sdk/app/ante.go"

  # ── Tier 4: Helper Server (ZKP #3 relay) ──
  "$REPO_ROOT/sdk/internal/helper/processor.go"
  "$REPO_ROOT/sdk/internal/helper/api.go"
  "$REPO_ROOT/sdk/internal/helper/types.go"
  "$REPO_ROOT/sdk/internal/helper/store.go"
  "$REPO_ROOT/sdk/internal/helper/submit.go"
  "$REPO_ROOT/sdk/internal/helper/helper.go"

  # ── Tier 5: Nullifier Ingest (exclusion proofs for ZKP #1) ──
  "$REPO_ROOT/nullifier-ingest/imt-tree/src/tree/nullifier_tree.rs"
  "$REPO_ROOT/nullifier-ingest/imt-tree/src/tree/mod.rs"
  "$REPO_ROOT/nullifier-ingest/imt-tree/src/proof.rs"
  "$REPO_ROOT/nullifier-ingest/imt-tree/src/hasher.rs"
  "$REPO_ROOT/nullifier-ingest/service/src/sync_nullifiers.rs"
  "$REPO_ROOT/nullifier-ingest/service/src/tree_db.rs"
)

# Protocol spec — committed copy (synced from Obsidian via scripts/sync-obsidian.sh)
COMMITTED_SPEC="$REPO_ROOT/docs/specs/gov-steps-v1.md"

# Fallback: Obsidian symlink (only resolves on dev machines)
OBSIDIAN_SPEC="$REPO_ROOT/zcaloooors/Voting/Gov Steps V1.md"

# ─── collect ──────────────────────────────────────────────────────────

collect_context() {
  echo "=== Collecting audit context ==="
  > "$CONTEXT_FILE"

  # 1. Include protocol spec (committed copy first, symlink fallback)
  local spec_source=""
  if [ -f "$COMMITTED_SPEC" ]; then
    spec_source="$COMMITTED_SPEC"
    echo "--- Including protocol spec (committed copy) ---"
  elif [ -f "$OBSIDIAN_SPEC" ]; then
    spec_source="$OBSIDIAN_SPEC"
    echo "--- Including protocol spec (Obsidian symlink) ---"
  else
    echo "--- WARNING: No protocol spec found (run scripts/sync-obsidian.sh) ---"
  fi

  if [ -n "$spec_source" ]; then
    {
      echo "════════════════════════════════════════════════════════════════"
      echo "SOURCE: Gov Steps V1.md (Full Protocol Specification)"
      echo "════════════════════════════════════════════════════════════════"
      echo ""
      cat "$spec_source"
      echo ""
      echo ""
    } >> "$CONTEXT_FILE"
  fi

  # 2. Include all spec files
  for f in "${SPEC_FILES[@]}"; do
    if [ -f "$f" ]; then
      local rel="${f#$REPO_ROOT/}"
      echo "  + $rel"
      {
        echo "════════════════════════════════════════════════════════════════"
        echo "SOURCE: $rel"
        echo "════════════════════════════════════════════════════════════════"
        echo ""
        cat "$f"
        echo ""
        echo ""
      } >> "$CONTEXT_FILE"
    else
      echo "  ! Missing: $f"
    fi
  done

  # 3. Include code files
  for f in "${CODE_FILES[@]}"; do
    if [ -f "$f" ]; then
      local rel="${f#$REPO_ROOT/}"
      local lines
      lines=$(wc -l < "$f" | tr -d ' ')
      echo "  + $rel ($lines lines)"
      {
        echo "════════════════════════════════════════════════════════════════"
        echo "CODE: $rel ($lines lines)"
        echo "════════════════════════════════════════════════════════════════"
        echo ""
        cat "$f"
        echo ""
        echo ""
      } >> "$CONTEXT_FILE"
    else
      echo "  ! Missing: $f"
    fi
  done

  # 4. Include git diff against main (uncommitted changes)
  local scan_dirs="orchard/src/ vote-commitment-tree/src/ sdk/x/vote/ sdk/crypto/ sdk/app/ sdk/internal/helper/ nullifier-ingest/"
  local diff
  diff=$(cd "$REPO_ROOT" && git diff HEAD -- $scan_dirs 2>/dev/null || true)
  if [ -n "$diff" ]; then
    echo "  + uncommitted changes (git diff)"
    {
      echo "════════════════════════════════════════════════════════════════"
      echo "GIT DIFF: Uncommitted changes"
      echo "════════════════════════════════════════════════════════════════"
      echo ""
      echo "$diff"
      echo ""
      echo ""
    } >> "$CONTEXT_FILE"
  fi

  # 5. Include recent git log for change velocity context
  local log
  log=$(cd "$REPO_ROOT" && git log --oneline -20 -- $scan_dirs 2>/dev/null || true)
  if [ -n "$log" ]; then
    {
      echo "════════════════════════════════════════════════════════════════"
      echo "RECENT COMMITS: Last 20 commits across all audited repos"
      echo "════════════════════════════════════════════════════════════════"
      echo ""
      echo "$log"
      echo ""
      echo ""
    } >> "$CONTEXT_FILE"
  fi

  local size
  size=$(wc -c < "$CONTEXT_FILE" | tr -d ' ')
  echo "=== Context collected: $(( size / 1024 ))KB ==="
}

# ─── audit ────────────────────────────────────────────────────────────

run_audit() {
  echo "=== Running AI audit ==="

  if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
    echo "ERROR: ANTHROPIC_API_KEY not set"
    exit 1
  fi

  if [ ! -f "$CONTEXT_FILE" ]; then
    echo "Context file missing, collecting first..."
    collect_context
  fi

  if [ ! -f "$PROMPT_FILE" ]; then
    echo "ERROR: Audit prompt not found at $PROMPT_FILE"
    exit 1
  fi

  local timestamp
  timestamp=$(date -u '+%Y-%m-%d %H:%M UTC')

  # Build user message file (avoids ARG_MAX limits for large context)
  local user_msg_file="/tmp/audit-user-message.txt"
  {
    echo "Audit timestamp: $timestamp"
    echo ""
    echo "Below is the full context: protocol specs, circuit READMEs, Halo2 circuit code, and recent changes. Perform the audit and produce the report as specified in your instructions."
    echo ""
    cat "$CONTEXT_FILE"
  } > "$user_msg_file"

  # Build JSON payload via file-based jq (--rawfile avoids ARG_MAX)
  local payload_file="/tmp/audit-payload.json"
  jq -n \
    --rawfile system "$PROMPT_FILE" \
    --rawfile user "$user_msg_file" \
    '{
      model: "claude-opus-4-6",
      max_tokens: 4096,
      system: $system,
      messages: [
        { role: "user", content: $user }
      ]
    }' > "$payload_file"

  echo "  Calling Anthropic API..."

  local response
  response=$(curl -sS --max-time 120 \
    https://api.anthropic.com/v1/messages \
    -H "Content-Type: application/json" \
    -H "x-api-key: $ANTHROPIC_API_KEY" \
    -H "anthropic-version: 2023-06-01" \
    -d @"$payload_file")

  # Extract the text content from the response
  local report
  report=$(echo "$response" | jq -r '.content[0].text // empty')

  if [ -z "$report" ]; then
    echo "ERROR: Empty response from API"
    echo "Raw response:" >&2
    echo "$response" | jq '.' >&2 || echo "$response" >&2
    # Write error report
    {
      echo "# Audit Failed"
      echo ""
      echo "**Timestamp:** $timestamp"
      echo ""
      echo "The AI audit failed to produce a report. Check the workflow logs."
      echo ""
      echo "API response:"
      echo '```json'
      echo "$response" | jq '.' 2>/dev/null || echo "$response"
      echo '```'
    } > "$REPORT_FILE"
    exit 1
  fi

  # Write report
  echo "$report" > "$REPORT_FILE"

  local report_size
  report_size=$(wc -c < "$REPORT_FILE" | tr -d ' ')
  echo "=== Audit complete: $(( report_size / 1024 ))KB report ==="
}

# ─── notify ───────────────────────────────────────────────────────────

post_to_slack() {
  echo "=== Posting to Slack ==="

  if [ -z "${SLACK_WEBHOOK_URL:-}" ]; then
    echo "ERROR: SLACK_WEBHOOK_URL not set"
    exit 1
  fi

  if [ ! -f "$REPORT_FILE" ]; then
    echo "ERROR: No report file found at $REPORT_FILE"
    exit 1
  fi

  local report
  report=$(cat "$REPORT_FILE")

  local run_url="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-z-cale/zally}/actions/runs/${GITHUB_RUN_ID:-0}"
  local timestamp
  timestamp=$(date -u '+%Y-%m-%d %H:%M UTC')

  # Truncate for Slack (max ~3000 chars in a block, leave room for wrapper)
  local max_len=2800
  local truncated="false"
  if [ ${#report} -gt $max_len ]; then
    report="${report:0:$max_len}

..._(truncated — full report in CI artifact)_"
    truncated="true"
  fi

  # Build Slack payload using Block Kit for nice formatting
  local payload
  payload=$(jq -n \
    --arg report "$report" \
    --arg run_url "$run_url" \
    --arg timestamp "$timestamp" \
    --arg truncated "$truncated" \
    '{
      blocks: [
        {
          type: "header",
          text: {
            type: "plain_text",
            text: ":shield: ZK Circuit Audit Report",
            emoji: true
          }
        },
        {
          type: "context",
          elements: [
            {
              type: "mrkdwn",
              text: ("*" + $timestamp + "*  |  <" + $run_url + "|View full run>")
            }
          ]
        },
        {
          type: "divider"
        },
        {
          type: "section",
          text: {
            type: "mrkdwn",
            text: $report
          }
        }
      ]
    }')

  local status_code
  status_code=$(curl -sS -o /dev/null -w '%{http_code}' \
    -X POST "$SLACK_WEBHOOK_URL" \
    -H "Content-Type: application/json" \
    -d "$payload")

  if [ "$status_code" = "200" ]; then
    echo "=== Posted to Slack successfully ==="
  else
    echo "WARNING: Slack returned HTTP $status_code"
    # Try simpler fallback payload (in case Block Kit fails)
    local fallback
    fallback=$(jq -n --arg text ":shield: *ZK Audit Report* ($timestamp)\n\n$report\n\n<$run_url|Full run>" \
      '{ text: $text }')
    curl -sS -o /dev/null \
      -X POST "$SLACK_WEBHOOK_URL" \
      -H "Content-Type: application/json" \
      -d "$fallback" || true
  fi
}

# ─── main ─────────────────────────────────────────────────────────────

cmd="${1:-all}"
case "$cmd" in
  collect)
    collect_context
    ;;
  audit)
    run_audit
    ;;
  notify)
    post_to_slack
    ;;
  all)
    collect_context
    run_audit
    post_to_slack
    ;;
  *)
    echo "Usage: $0 {collect|audit|notify|all}"
    exit 1
    ;;
esac
