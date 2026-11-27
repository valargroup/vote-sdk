#!/usr/bin/env bash
# Shared settings and helpers for the isolated e2e localnet, sourced by the
# other scripts in this directory.
#
# The localnet never touches ~/.svoted*, ~/go/bin or the default ports: its
# binaries, homes, logs and pid files all live under LOCALNET_DIR, and every
# listener is moved off the Cosmos and CometBFT defaults. init.sh reads the
# settings and records them in $LOCALNET_DIR/localnet.env for the other
# scripts; docs/localnet-isolated.md documents each one.

# The scripts that source this file read its variables.
# shellcheck disable=SC2034

set -euo pipefail

LOCALNET_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCALNET_REPO_ROOT="$(cd "$LOCALNET_SCRIPT_DIR/../.." && pwd)"

LOCALNET_DIR="${LOCALNET_DIR:-${TMPDIR:-/tmp}/svote-localnet-isolated}"
LOCALNET_DIR="${LOCALNET_DIR%/}"
# Resolve symlinks in the parent (macOS /tmp and $TMPDIR are links into
# /private), so pid checks compare the same path however the caller spelled it.
if [ -d "$(dirname "$LOCALNET_DIR")" ]; then
    LOCALNET_DIR="$(cd "$(dirname "$LOCALNET_DIR")" && pwd -P)/$(basename "$LOCALNET_DIR")"
fi
LOCALNET_BIN_DIR="$LOCALNET_DIR/bin"
LOCALNET_SVOTED="$LOCALNET_BIN_DIR/svoted"
LOCALNET_CREATE_VAL_TX="$LOCALNET_BIN_DIR/create-val-tx"
LOCALNET_ENV_FILE="$LOCALNET_DIR/localnet.env"
LOCALNET_MARKER="$LOCALNET_DIR/.svote-localnet-isolated"
LOCALNET_LOG_DIR="$LOCALNET_DIR/logs"
LOCALNET_PID_DIR="$LOCALNET_DIR/pids"
LOCALNET_DENOM="usvote"

# The proxy delegation verifier a PROXY=1 genesis trusts (contracts-v1 §9,
# GenesisState field 22). DEVELOPMENT ONLY: its secret seed is public, the
# SHA-256 of "svote-localnet-isolated/dev-proxy-verifier/v1", so anyone can
# sign attestations with it; never put its key in a real chain's params.
# env.sh exports the seed for the e2e tests, which sign ATTEST with it and
# check that it derives this public key.
LOCALNET_PROXY_VERIFIER_ID=1
LOCALNET_PROXY_VERIFIER_SEED_HEX="4c3d8a9e62ae663a09864829d8fadfa14366db2513aac484ae56f1a16f590e88"
LOCALNET_PROXY_VERIFIER_PUBKEY_B64="/MAC61aEHv/PXC0yCgLziX6pEjG2Ru6+duUyB+3e7SI="
# The PROXY=1 genesis max_delegates, the most the params accept. The default,
# 0, would leave the registry full (contracts-v1 §5.3 register step 6).
LOCALNET_PROXY_MAX_DELEGATES=2000

# localnet_log prints one progress line with the localnet prefix.
localnet_log() {
    echo "[localnet-isolated] $*"
}

# localnet_die prints an error and exits non-zero.
localnet_die() {
    echo "[localnet-isolated] ERROR: $*" >&2
    exit 1
}

# localnet_require_safe_dir refuses a LOCALNET_DIR that could hold someone
# else's data: relative paths, /, $HOME, ~/.svoted*, the repository, or an
# existing non-empty directory that this tooling did not create.
localnet_require_safe_dir() {
    case "$LOCALNET_DIR" in
        /*) ;;
        *) localnet_die "LOCALNET_DIR must be an absolute path, got '$LOCALNET_DIR'" ;;
    esac
    case "$LOCALNET_DIR" in
        / | "$HOME" | "$HOME"/.svoted* | "$LOCALNET_REPO_ROOT" | "$LOCALNET_REPO_ROOT"/*)
            localnet_die "refusing LOCALNET_DIR=$LOCALNET_DIR; pick a dedicated directory"
            ;;
    esac
    if [ -d "$LOCALNET_DIR" ] && [ ! -f "$LOCALNET_MARKER" ] \
        && [ -n "$(ls -A "$LOCALNET_DIR" 2>/dev/null)" ]; then
        localnet_die "$LOCALNET_DIR is not empty and was not created by this tooling"
    fi
}

# localnet_load_env loads the settings init.sh recorded, so start, stop and
# test act on the chain that exists rather than on the caller's environment.
localnet_load_env() {
    localnet_require_safe_dir
    [ -f "$LOCALNET_ENV_FILE" ] \
        || localnet_die "no localnet at $LOCALNET_DIR; run make localnet-isolated-init first"
    # shellcheck disable=SC1090
    . "$LOCALNET_ENV_FILE"
}

# localnet_range prints FROM to TO, one per line, and nothing when FROM > TO,
# where BSD seq would count down.
localnet_range() {
    local i="$1"
    while [ "$i" -le "$2" ]; do
        echo "$i"
        i=$((i + 1))
    done
}

# localnet_port prints a validator's port: BASE plus (INDEX - 1) strides.
localnet_port() {
    echo $(( $1 + ($2 - 1) * LOCALNET_PORT_STRIDE ))
}

# localnet_home prints the home directory of validator INDEX.
localnet_home() {
    echo "$LOCALNET_DIR/val$1"
}

# localnet_build_tags prints the svoted build tags for the PROXY mode.
localnet_build_tags() {
    if [ "$PROXY" = "1" ]; then
        echo "halo2,redpallas,proxyenabled"
    else
        echo "halo2,redpallas"
    fi
}

# localnet_port_listening reports whether anything listens on TCP PORT.
localnet_port_listening() {
    lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
}

# localnet_validator_ports prints every port validator INDEX listens on.
localnet_validator_ports() {
    local base
    for base in "$LOCALNET_API_PORT" "$LOCALNET_RPC_PORT" "$LOCALNET_P2P_PORT" \
        "$LOCALNET_ABCI_PORT" "$LOCALNET_PROMETHEUS_PORT" "$LOCALNET_GRPC_PORT" \
        "$LOCALNET_GRPC_WEB_PORT" "$LOCALNET_PPROF_PORT"; do
        localnet_port "$base" "$1"
    done
}

# localnet_pid_of prints the pid of validator INDEX when the process recorded
# in its pid file is still our svoted for that home, and fails otherwise. The
# command-line check keeps a recycled pid from ever naming someone else's
# process.
localnet_pid_of() {
    local index="$1"
    local pid_file="$LOCALNET_PID_DIR/val$index.pid"
    local pid command
    [ -f "$pid_file" ] || return 1
    pid="$(cat "$pid_file")"
    case "$pid" in
        '' | *[!0-9]*) return 1 ;;
    esac
    command="$(ps -p "$pid" -o command= 2>/dev/null || true)"
    case "$command" in
        "$LOCALNET_SVOTED start --home $(localnet_home "$index")"*)
            echo "$pid"
            return 0
            ;;
    esac
    return 1
}

# localnet_any_running reports whether any validator of this localnet runs.
localnet_any_running() {
    local i
    for i in $(localnet_range 1 "$LOCALNET_VALIDATORS"); do
        if localnet_pid_of "$i" >/dev/null; then
            return 0
        fi
    done
    return 1
}
