#!/usr/bin/env bash
# verify_isolated_join_env.sh — assert join env is not coupled to stage/prod endpoints.
#
# Usage:
#   export VOTING_CONFIG_URL=...
#   export SVOTE_ADMIN_URL=...
#   export SVOTE_CHAIN_ID=upgrade-test-1
#   scripts/verify_isolated_join_env.sh
#
# Or pass flags:
#   scripts/verify_isolated_join_env.sh \
#     --voting-config-url https://... \
#     --admin-url http://... \
#     --chain-id upgrade-test-1
set -euo pipefail

VOTING_CONFIG_URL="${VOTING_CONFIG_URL:-}"
SVOTE_ADMIN_URL="${SVOTE_ADMIN_URL:-}"
SVOTE_CHAIN_ID="${SVOTE_CHAIN_ID:-}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --voting-config-url)
      VOTING_CONFIG_URL="$2"
      shift 2
      ;;
    --voting-config-url=*)
      VOTING_CONFIG_URL="${1#--voting-config-url=}"
      shift
      ;;
    --admin-url)
      SVOTE_ADMIN_URL="$2"
      shift 2
      ;;
    --admin-url=*)
      SVOTE_ADMIN_URL="${1#--admin-url=}"
      shift
      ;;
    --chain-id)
      SVOTE_CHAIN_ID="$2"
      shift 2
      ;;
    --chain-id=*)
      SVOTE_CHAIN_ID="${1#--chain-id=}"
      shift
      ;;
    --help|-h)
      echo "usage: verify_isolated_join_env.sh [--voting-config-url URL] [--admin-url URL] [--chain-id ID]"
      exit 0
      ;;
    *)
      echo "ERROR: unknown option: $1" >&2
      exit 1
      ;;
  esac
done

failures=0

warn_coupling() {
  local label="$1"
  local value="$2"
  local pattern="$3"
  if printf '%s\n' "$value" | grep -Eiq "$pattern"; then
    echo "[FAIL] ${label} appears coupled to stage/prod: ${value}" >&2
    failures=$((failures + 1))
  else
    echo "[PASS] ${label} not matched by forbidden pattern"
  fi
}

echo "=== Isolated join environment check ==="

[ -n "$VOTING_CONFIG_URL" ] || { echo "[FAIL] VOTING_CONFIG_URL unset" >&2; failures=$((failures + 1)); }
[ -n "$SVOTE_ADMIN_URL" ] || { echo "[FAIL] SVOTE_ADMIN_URL unset" >&2; failures=$((failures + 1)); }
[ -n "$SVOTE_CHAIN_ID" ] || { echo "[FAIL] SVOTE_CHAIN_ID unset" >&2; failures=$((failures + 1)); }

if [ -n "$SVOTE_CHAIN_ID" ]; then
  case "$SVOTE_CHAIN_ID" in
    svote-1|zvote-1)
      echo "[FAIL] SVOTE_CHAIN_ID is a production/stage chain id: ${SVOTE_CHAIN_ID}" >&2
      failures=$((failures + 1))
      ;;
    *)
      echo "[PASS] SVOTE_CHAIN_ID=${SVOTE_CHAIN_ID} (not svote-1/zvote-1)"
      ;;
  esac
fi

if [ -n "$VOTING_CONFIG_URL" ]; then
  warn_coupling "VOTING_CONFIG_URL" "$VOTING_CONFIG_URL" \
    'voting\.valargroup\.org/(prod|stage)|svote\.valargroup\.org|prod\.svote\.|stage\.svote\.'
fi

if [ -n "$SVOTE_ADMIN_URL" ]; then
  warn_coupling "SVOTE_ADMIN_URL" "$SVOTE_ADMIN_URL" \
    'svote\.valargroup\.org|prod\.svote\.|stage\.svote\.|vote-chain-primary\.valargroup\.org'
fi

if [ -n "$VOTING_CONFIG_URL" ] && command -v curl >/dev/null 2>&1 && command -v jq >/dev/null 2>&1; then
  if cfg=$(curl -fsSL --connect-timeout 10 --max-time 30 "$VOTING_CONFIG_URL" 2>/dev/null); then
    seed=$(printf '%s\n' "$cfg" | jq -r '.vote_servers[0].url // empty')
    if [ -n "$seed" ] && [ "$seed" != "null" ]; then
      warn_coupling "vote_servers[0].url from config" "$seed" \
        'svote\.valargroup\.org|prod\.svote\.|stage\.svote\.|vote-chain-primary\.valargroup\.org'
      echo "[INFO] seed URL from config: ${seed}"
    else
      echo "[WARN] voting-config has no vote_servers[0].url"
    fi
  else
    echo "[WARN] could not fetch VOTING_CONFIG_URL (check network before join)"
  fi
fi

echo
if [ "$failures" -gt 0 ]; then
  echo "Isolated join environment check failed (${failures} issues)." >&2
  exit 1
fi
echo "Isolated join environment check passed."
