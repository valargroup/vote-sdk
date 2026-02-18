#!/bin/bash
# remote-ceremony.sh — Bootstrap the EA key ceremony on a remote chain via SSH.
#
# The ECIES crypto and REST queries run locally; only zallyd sign/broadcast
# commands execute on the remote host (which has the vote module types
# registered in its binary).
#
# Usage:
#   ./scripts/remote-ceremony.sh <ssh-host> [key-dir]
#
# Arguments:
#   ssh-host  SSH host alias or address (e.g. "zally")
#   key-dir   Local directory to cache key files (default: /tmp/zally-ceremony)
#
# The script fetches ea.sk, ea.pk, and pallas.pk from the remote if not
# already present locally, then runs the ceremony_bootstrap test.
set -e

SSH_HOST="${1:?Usage: $0 <ssh-host> [key-dir]}"
KEY_DIR="${2:-/tmp/zally-ceremony}"
E2E_DIR="$(cd "$(dirname "$0")/../e2e-tests" && pwd)"

REMOTE_HOME="/opt/zally-chain/.zallyd"
REMOTE_ZALLYD="/opt/zally-chain/zallyd"

# Derive the HTTPS URL from the SSH host. If the host looks like a bare
# alias (no dots), we can't guess the URL — require ZALLY_API_URL to be set.
if [ -n "$ZALLY_API_URL" ]; then
    API_URL="$ZALLY_API_URL"
else
    # Try to resolve the SSH host to an IP for the sslip.io URL pattern.
    IP=$(ssh -G "$SSH_HOST" 2>/dev/null | awk '/^hostname / {print $2}')
    if echo "$IP" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'; then
        SSLIP=$(echo "$IP" | tr '.' '-')
        API_URL="https://${SSLIP}.sslip.io"
    else
        echo "ERROR: Cannot derive API URL from SSH host '$SSH_HOST'."
        echo "Set ZALLY_API_URL explicitly."
        exit 1
    fi
fi

echo "=== Remote Ceremony Bootstrap ==="
echo "SSH host:   $SSH_HOST"
echo "API URL:    $API_URL"
echo "Key dir:    $KEY_DIR"
echo ""

# ---------------------------------------------------------------------------
# Fetch key files from remote if not present locally
# ---------------------------------------------------------------------------
mkdir -p "$KEY_DIR"

for keyfile in ea.sk ea.pk pallas.pk; do
    local_path="$KEY_DIR/$keyfile"
    if [ -f "$local_path" ]; then
        echo "Key $keyfile already cached at $local_path"
    else
        echo "Fetching $keyfile from remote..."
        scp "${SSH_HOST}:${REMOTE_HOME}/${keyfile}" "$local_path"
    fi
done

# ---------------------------------------------------------------------------
# Run ceremony bootstrap
# ---------------------------------------------------------------------------
echo ""
echo "Running ceremony bootstrap..."

export ZALLY_API_URL="$API_URL"
export ZALLY_SSH_HOST="$SSH_HOST"
export ZALLY_REMOTE_ZALLYD="$REMOTE_ZALLYD"
export ZALLY_HOME="$REMOTE_HOME"
export ZALLY_NODE_URL="tcp://localhost:26657"
export ZALLY_EA_SK_PATH="$KEY_DIR/ea.sk"
export ZALLY_EA_PK_PATH="$KEY_DIR/ea.pk"
export ZALLY_PALLAS_PK_PATH="$KEY_DIR/pallas.pk"

cargo test --release \
    --manifest-path "$E2E_DIR/Cargo.toml" \
    ceremony_bootstrap -- --nocapture --ignored

echo ""
echo "=== Ceremony bootstrap complete ==="
echo ""
echo "Verify: curl -s $API_URL/zally/v1/ceremony | jq .ceremony.status"
