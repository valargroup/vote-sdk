#!/usr/bin/env bash
#
# healthcheck.sh — Probe all production endpoints and exit non-zero on failure.
#
# Usage:
#   DOMAIN=example.com bash scripts/healthcheck.sh
#
# Optional env vars:
#   PRIMARY_URL       (default: https://vote-chain-primary.$DOMAIN)
#   SECONDARY_URL     (default: https://vote-chain-secondary.$DOMAIN)
#   TIMEOUT           (default: 10)
set -euo pipefail

DOMAIN="${DOMAIN:?DOMAIN must be set}"
TIMEOUT="${TIMEOUT:-10}"

PRIMARY_URL="${PRIMARY_URL:-https://vote-chain-primary.${DOMAIN}}"
SECONDARY_URL="${SECONDARY_URL:-https://vote-chain-secondary.${DOMAIN}}"

FAILED=0

check() {
  local name="$1"
  local url="$2"
  local expected_status="${3:-200}"

  STATUS=$(curl -sf -o /dev/null -w "%{http_code}" --max-time "$TIMEOUT" "$url" 2>/dev/null || echo "000")

  if [ "$STATUS" = "$expected_status" ]; then
    echo "[OK]   $name ($url) -> HTTP $STATUS"
  else
    echo "[FAIL] $name ($url) -> HTTP $STATUS (expected $expected_status)"
    FAILED=1
  fi
}

echo "=== Shielded Vote Production Health Check ==="
echo ""

check "Primary REST API (rounds)"   "${PRIMARY_URL}/shielded-vote/v1/rounds"
check "Secondary REST API (rounds)" "${SECONDARY_URL}/shielded-vote/v1/rounds"

echo ""

if [ "$FAILED" -eq 0 ]; then
  echo "All checks passed."
  exit 0
else
  echo "One or more checks failed!"
  exit 1
fi
