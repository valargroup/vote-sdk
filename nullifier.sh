#!/bin/bash
# nullifier.sh — Nullifier service setup and management helper for Shielded-Vote.
#
# Usage:
#   ./nullifier.sh <command>
#
# Commands:
#   install-deps  Install system dependencies (Rust/cargo, wget, build tools)
#   bootstrap     Download pre-built nullifier snapshot files
#   serve         Build and start the exclusion proof query server
#   status        Show nullifier ingestion status
#   run           Fully automated: install-deps → bootstrap → serve
#
# Environment overrides:
#   LWD_URL        Lightwallet server to fetch nullifiers from (default: https://zec.rocks:443)
#   DATA_DIR       Directory for nullifier data files           (default: nullifier-ingest)
#   PORT           Query server port                            (default: 3000)
#   BOOTSTRAP_URL  Base URL for snapshot downloads             (default: https://vote.fra1.digitaloceanspaces.com)

set -euo pipefail

# ─── Configuration ────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LWD_URL="${LWD_URL:-https://zec.rocks:443}"
DATA_DIR="${DATA_DIR:-${SCRIPT_DIR}/nullifier-ingest}"
PORT="${PORT:-3000}"
BOOTSTRAP_URL="${BOOTSTRAP_URL:-https://vote.fra1.digitaloceanspaces.com}"

# ─── Helpers ──────────────────────────────────────────────────────────────────

log()  { echo "  $*"; }
step() { echo ""; echo "=== $* ==="; }
die()  { echo ""; echo "ERROR: $*" >&2; exit 1; }

require_cmd() {
  command -v "$1" > /dev/null 2>&1 || die "$1 is required but not found in PATH."
}

# ─── Commands ─────────────────────────────────────────────────────────────────

cmd_install_deps() {
  step "Installing dependencies"

  OS_RAW=$(uname -s)
  case "$OS_RAW" in
    Linux)  OS="linux" ;;
    Darwin) OS="darwin" ;;
    *)      die "Unsupported OS: ${OS_RAW}. Supported: Linux, Darwin (macOS)." ;;
  esac

  # ── System packages ──────────────────────────────────────────────────────
  if [ "$OS" = "linux" ]; then
    step "Installing system packages (build-essential, wget, pkg-config, libssl-dev)"
    if command -v apt-get > /dev/null 2>&1; then
      apt-get update -qq
      apt-get install -y -qq build-essential wget pkg-config libssl-dev curl
    elif command -v yum > /dev/null 2>&1; then
      yum install -y gcc make wget pkgconfig openssl-devel curl
    else
      log "WARNING: Unknown package manager. Please install manually: build-essential wget pkg-config libssl-dev"
    fi
  fi

  if [ "$OS" = "darwin" ]; then
    if ! command -v wget > /dev/null 2>&1; then
      if command -v brew > /dev/null 2>&1; then
        log "Installing wget via Homebrew..."
        brew install wget
      else
        die "wget is required. Install Homebrew (https://brew.sh) then run: brew install wget"
      fi
    else
      log "wget already installed."
    fi
  fi

  # ── Rust / Cargo ─────────────────────────────────────────────────────────
  if command -v cargo > /dev/null 2>&1; then
    CARGO_VERSION=$(cargo --version 2>/dev/null | awk '{print $2}')
    log "Rust/cargo already installed: ${CARGO_VERSION}"
  else
    step "Installing Rust via rustup"
    curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain stable
    # Source the environment so cargo is available in the current shell.
    # shellcheck source=/dev/null
    . "${HOME}/.cargo/env"
    log "Rust installed: $(cargo --version)"
  fi

  # Ensure cargo is on PATH (in case it was just installed).
  if [ -f "${HOME}/.cargo/env" ]; then
    # shellcheck source=/dev/null
    . "${HOME}/.cargo/env"
  fi

  log "All dependencies satisfied."
}

cmd_bootstrap() {
  step "Bootstrapping nullifier snapshot files"

  require_cmd wget
  require_cmd make

  log "Data directory: ${DATA_DIR}"
  log "Bootstrap URL:  ${BOOTSTRAP_URL}"

  if [ -f "${DATA_DIR}/nullifiers.checkpoint" ]; then
    log "Snapshot files already present — skipping download."
    return 0
  fi

  log "Downloading snapshot files..."
  mkdir -p "${DATA_DIR}"
  wget -q --show-progress -O "${DATA_DIR}/nullifiers.bin"        "${BOOTSTRAP_URL}/nullifiers.bin"
  wget -q --show-progress -O "${DATA_DIR}/nullifiers.checkpoint" "${BOOTSTRAP_URL}/nullifiers.checkpoint"
  wget -q --show-progress -O "${DATA_DIR}/nullifiers.tree"       "${BOOTSTRAP_URL}/nullifiers.tree"
  log "Bootstrap complete."
}

cmd_serve() {
  step "Starting nullifier exclusion proof query server"

  require_cmd cargo
  require_cmd make

  SERVICE_DIR="${SCRIPT_DIR}/nullifier-ingest/service"
  [ -d "${SERVICE_DIR}" ] || die "Service directory not found: ${SERVICE_DIR}. Are you running from the shielded-vote repo root?"

  log "Data directory: ${DATA_DIR}"
  log "Port:           ${PORT}"
  log "Building and starting query-server (first run compiles Rust — may take a few minutes)..."
  echo ""

  cd "${SERVICE_DIR}"
  DATA_DIR="${DATA_DIR}" PORT="${PORT}" cargo run --release --bin query-server
}

cmd_status() {
  step "Nullifier service status"

  NF="${DATA_DIR}/nullifiers.bin"
  CP="${DATA_DIR}/nullifiers.checkpoint"
  TREE="${DATA_DIR}/nullifiers.tree"

  log "Data directory: ${DATA_DIR}"

  if [ -f "${NF}" ]; then
    SIZE=$(ls -lh "${NF}" | awk '{print $5}')
    BYTES=$(wc -c < "${NF}" | tr -d ' ')
    COUNT=$((BYTES / 32))
    log "nullifiers.bin:        ${COUNT} nullifiers (${SIZE})"
  else
    log "nullifiers.bin:        not found"
  fi

  if [ -f "${CP}" ]; then
    HEIGHT=$(od -An -t u8 -j 0 -N 8 "${CP}" | tr -d ' ')
    OFFSET=$(od -An -t u8 -j 8 -N 8 "${CP}" | tr -d ' ')
    log "checkpoint:            height=${HEIGHT}  offset=${OFFSET}"
  else
    log "checkpoint:            none"
  fi

  if [ -f "${TREE}" ]; then
    TSIZE=$(ls -lh "${TREE}" | awk '{print $5}')
    log "nullifiers.tree:       ${TSIZE} (sidecar cached)"
  else
    log "nullifiers.tree:       not present (will rebuild on serve)"
  fi

  echo ""
  if pgrep -x query-server > /dev/null 2>&1; then
    log "query-server:  RUNNING (port ${PORT})"
  else
    log "query-server:  not running"
  fi
}

cmd_run() {
  step "Nullifier service — fully automated setup"

  # ── Step 1/3: Install dependencies ───────────────────────────────────────
  step "Step 1/3 — Install dependencies"
  cmd_install_deps

  # Ensure cargo is available after potential install.
  if [ -f "${HOME}/.cargo/env" ]; then
    # shellcheck source=/dev/null
    . "${HOME}/.cargo/env"
  fi

  # ── Step 2/3: Bootstrap snapshot files ───────────────────────────────────
  step "Step 2/3 — Bootstrap nullifier snapshot"
  cmd_bootstrap

  # ── Step 3/3: Build and start the server ─────────────────────────────────
  step "Step 3/3 — Start query server"
  echo ""
  echo "============================================="
  echo "  Setup complete — starting query-server"
  echo "============================================="
  echo ""
  log "Data directory: ${DATA_DIR}"
  log "Port:           ${PORT}"
  echo ""

  cmd_serve
}

# ─── Dispatch ─────────────────────────────────────────────────────────────────

usage() {
  grep '^#' "$0" | grep -v '^#!/' | sed 's/^# \{0,1\}//'
  exit 1
}

COMMAND="${1:-}"
shift || true

case "${COMMAND}" in
  install-deps) cmd_install_deps "$@" ;;
  bootstrap)    cmd_bootstrap    "$@" ;;
  serve)        cmd_serve        "$@" ;;
  status)       cmd_status       "$@" ;;
  run)          cmd_run          "$@" ;;
  *)            usage ;;
esac
