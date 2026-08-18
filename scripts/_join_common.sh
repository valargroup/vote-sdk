#!/usr/bin/env bash
# Shared helpers for Shielded-Vote join installers.

set -euo pipefail

svote_resolve_home() {
  if [ -n "${HOME:-}" ]; then
    return 0
  fi
  if command -v getent >/dev/null 2>&1; then
    HOME="$(getent passwd "$(id -u)" | cut -d: -f6)"
  else
    HOME="$(eval echo "~$(id -un)")"
  fi
  if [ -z "${HOME}" ] || [ "${HOME}" = "~$(id -un)" ]; then
    echo "ERROR: HOME is not set and could not be resolved for user $(id -un)." >&2
    exit 1
  fi
  export HOME
}

svote_default_voting_config_url_for_env() {
  case "$1" in
    prod)  echo "https://voting.valargroup.dev/prod/dynamic-voting-config.json" ;;
    stage) echo "https://voting.valargroup.dev/stage/dynamic-voting-config.json" ;;
    *)     echo "" ;;
  esac
}

svote_resolve_do_base() {
  local do_base_override="${SVOTE_DO_SPACES_BASE:-${DO_SPACES_BASE:-}}"
  local do_bucket="${SVOTE_DO_SPACES_BUCKET:-${DO_SPACES_BUCKET:-}}"
  local do_region="${SVOTE_DO_SPACES_REGION:-${DO_SPACES_REGION:-nyc3}}"
  if [ -n "${do_base_override}" ]; then
    DO_BASE="${do_base_override}"
  elif [ -n "${do_bucket}" ]; then
    DO_BASE="https://${do_bucket}.${do_region}.digitaloceanspaces.com"
  else
    DO_BASE="https://shielded-vote.nyc3.digitaloceanspaces.com"
  fi
  DO_BASE="${DO_BASE%/}"
  export DO_BASE
}

svote_brew_install_quiet() {
  local log_file="${SVOTE_INSTALL_LOG:-${TMPDIR:-/tmp}/shielded-vote-join-install.log}"
  local package_list="$*"

  if ! xcode-select -p >/dev/null 2>&1; then
    echo "ERROR: Xcode Command Line Tools are required for Homebrew." >&2
    echo "  Install them, then re-run: xcode-select --install" >&2
    return 1
  fi

  mkdir -p "$(dirname "${log_file}")"
  echo "Installing with Homebrew: ${package_list} (log: ${log_file})"
  {
    echo ""
    echo "[$(date)] brew install --force-bottle ${package_list}"
  } >> "${log_file}"

  if HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_ENV_HINTS=1 \
       brew install --force-bottle "$@" >> "${log_file}" 2>&1; then
    echo "Homebrew install complete: ${package_list}"
    return 0
  fi

  echo "ERROR: Homebrew install failed: ${package_list}" >&2
  if [ -f "${log_file}" ]; then
    tail -n 30 "${log_file}" 2>/dev/null | sed 's/^/  /' >&2
  fi
  return 1
}

svote_apt_lock_output_indicates_busy() {
  local output_file="$1"
  grep -Eiq 'Could not get lock|Unable to acquire.*lock|is another process using it|Waiting for cache lock|Could not open lock' "${output_file}"
}

svote_apt_get_with_lock_retry() {
  local description="$1"
  shift

  local timeout="${SVOTE_APT_LOCK_TIMEOUT:-300}"
  local interval="${SVOTE_APT_LOCK_RETRY_INTERVAL:-5}"
  local waited=0
  local output_file
  local status
  output_file=$(mktemp)

  while true; do
    : > "${output_file}"
    if sudo -E apt-get -o "DPkg::Lock::Timeout=${interval}" "$@" > "${output_file}" 2>&1; then
      rm -f "${output_file}"
      return 0
    fi
    status=$?
    if ! svote_apt_lock_output_indicates_busy "${output_file}"; then
      cat "${output_file}" >&2
      rm -f "${output_file}"
      return "${status}"
    fi
    if [ "${waited}" -ge "${timeout}" ]; then
      echo "ERROR: Timed out waiting for apt/dpkg lock while ${description}." >&2
      cat "${output_file}" >&2
      rm -f "${output_file}"
      return "${status}"
    fi
    echo "apt/dpkg is busy; waiting ${interval}s before retrying ${description}..."
    sleep "${interval}"
    waited=$((waited + interval))
  done
}

svote_install_missing_tools() {
  local missing=()
  local tool
  for tool in "$@"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      missing+=("$tool")
    fi
  done
  if [ "${#missing[@]}" -eq 0 ]; then
    return 0
  fi

  echo "Missing tools: ${missing[*]} - installing..."
  case "$(uname -s)" in
    Linux)
      if command -v apt-get >/dev/null 2>&1; then
        export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1
        svote_apt_get_with_lock_retry "refreshing package metadata" update -qq
        svote_apt_get_with_lock_retry "installing ${missing[*]}" install -y "${missing[@]}"
      elif command -v dnf >/dev/null 2>&1; then
        sudo dnf install -y "${missing[@]}"
      elif command -v yum >/dev/null 2>&1; then
        sudo yum install -y "${missing[@]}"
      elif command -v apk >/dev/null 2>&1; then
        sudo apk add --no-cache "${missing[@]}"
      else
        echo "ERROR: ${missing[*]} required. No supported package manager found." >&2
        exit 1
      fi
      ;;
    Darwin)
      if command -v brew >/dev/null 2>&1; then
        svote_brew_install_quiet "${missing[@]}"
      else
        echo "ERROR: ${missing[*]} required. Install with: brew install ${missing[*]}" >&2
        exit 1
      fi
      ;;
    *)
      echo "ERROR: ${missing[*]} required. Install them for your OS and re-run." >&2
      exit 1
      ;;
  esac
}

svote_sha256_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    echo "ERROR: sha256sum or shasum is required." >&2
    return 1
  fi
}

svote_discover_network() {
  SVOTE_ENV="${SVOTE_ENV:-prod}"
  case "$SVOTE_ENV" in
    prod|stage) ;;
    *)
      echo "ERROR: Unsupported SVOTE_ENV/--env: ${SVOTE_ENV}" >&2
      echo "  Expected one of: prod, stage." >&2
      exit 1
      ;;
  esac
  VOTING_CONFIG_URL="${VOTING_CONFIG_URL:-$(svote_default_voting_config_url_for_env "$SVOTE_ENV")}"

  echo "Fetching voting-config from ${VOTING_CONFIG_URL}..."
  VOTING_CONFIG=$(curl -fsSL --retry 5 --retry-delay 2 --retry-max-time 60 --connect-timeout 15 --max-time 60 "${VOTING_CONFIG_URL}")
  SEED_URL=$(echo "$VOTING_CONFIG" | jq -r '.vote_servers[0].url // empty')
  if [ -z "$SEED_URL" ] || [ "$SEED_URL" = "null" ]; then
    echo "ERROR: No vote_servers[0].url in voting-config." >&2
    exit 1
  fi

  NODE_INFO_URL="${SEED_URL%/}/cosmos/base/tendermint/v1beta1/node_info"
  NODE_INFO=$(curl -fsSL --retry 5 --retry-delay 2 --retry-max-time 60 --connect-timeout 15 --max-time 30 "${NODE_INFO_URL}")
  NODE_ID=$(echo "$NODE_INFO" | jq -r '.default_node_info.default_node_id // .default_node_info.id // empty')
  LISTEN_ADDR=$(echo "$NODE_INFO" | jq -r '.default_node_info.listen_addr // empty')
  NODE_CHAIN_ID=$(echo "$NODE_INFO" | jq -r '.default_node_info.network // empty')
  CHAIN_BINARY_VERSION=$(echo "$NODE_INFO" | jq -r '.application_version.version // empty')

  if [ -z "$NODE_ID" ] || [ -z "$NODE_CHAIN_ID" ]; then
    echo "ERROR: Could not resolve seed node identity from ${NODE_INFO_URL}" >&2
    exit 1
  fi
  if [ -n "${SVOTE_CHAIN_ID:-}" ] && [ "${SVOTE_CHAIN_ID}" != "$NODE_CHAIN_ID" ]; then
    echo "ERROR: Seed node chain ID mismatch. Expected ${SVOTE_CHAIN_ID}, got ${NODE_CHAIN_ID}." >&2
    exit 1
  fi
  CHAIN_ID="$NODE_CHAIN_ID"

  if [ -n "${SVOTE_RELEASE_VERSION:-}" ]; then
    CHAIN_BINARY_VERSION="${SVOTE_RELEASE_VERSION}"
  elif [ -z "$CHAIN_BINARY_VERSION" ] || [ "$CHAIN_BINARY_VERSION" = "null" ]; then
    CHAIN_BINARY_VERSION=$(curl -fsSL "${DO_BASE}/version.txt" | tr -d '[:space:]')
  fi
  if ! printf '%s\n' "$CHAIN_BINARY_VERSION" | grep -Eq '^v[0-9]+(\.[0-9]+)*([._-][A-Za-z0-9]+)*$'; then
    echo "ERROR: Active chain app version is not a valid release tag: ${CHAIN_BINARY_VERSION}" >&2
    exit 1
  fi

  SEED_HOST=$(echo "$SEED_URL" | sed -E 's|^https?://||; s|:[0-9]+$||; s|/.*||')
  P2P_PORT=$(echo "$LISTEN_ADDR" | sed -E 's|.*:([0-9]+)$|\1|')
  P2P_PORT="${P2P_PORT:-26656}"
  PERSISTENT_PEERS="${NODE_ID}@${SEED_HOST}:${P2P_PORT}"
  if [ -z "${SNAPSHOT_BASE_URL:-}" ]; then
    case "$CHAIN_ID" in
      svote-1) SNAPSHOT_BASE_URL="https://stage.snapshots.valargroup.org" ;;
      *)       SNAPSHOT_BASE_URL="https://snapshots.valargroup.org" ;;
    esac
  fi

  export SVOTE_ENV VOTING_CONFIG_URL SEED_URL NODE_INFO_URL NODE_ID LISTEN_ADDR
  export CHAIN_ID CHAIN_BINARY_VERSION PERSISTENT_PEERS SNAPSHOT_BASE_URL
}

svote_acquire_binaries() {
  local install_create_val_tx="${1:-0}"

  if [ "${SVOTE_LOCAL_BINARIES:-0}" = "1" ] && command -v svoted >/dev/null 2>&1; then
    local local_version
    local_version=$(svoted version 2>/dev/null | tr -d '[:space:]' || true)
    if [ "$local_version" != "$CHAIN_BINARY_VERSION" ] && [ "${SVOTE_ALLOW_VERSION_MISMATCH:-0}" != "1" ]; then
      echo "ERROR: Local svoted version ${local_version:-<unknown>} does not match active chain ${CHAIN_BINARY_VERSION}." >&2
      exit 1
    fi
    echo "Using local svoted: $(command -v svoted)"
    return 0
  fi

  local os arch platform tarball_dir release_url checksum_url archive
  case "$(uname -s)" in
    Linux)  os="linux" ;;
    Darwin) os="darwin" ;;
    *) echo "ERROR: Unsupported OS: $(uname -s)." >&2; exit 1 ;;
  esac
  case "$(uname -m)" in
    x86_64)        arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) echo "ERROR: Unsupported architecture: $(uname -m)." >&2; exit 1 ;;
  esac
  platform="${os}-${arch}"
  mkdir -p "${INSTALL_DIR}"

  tarball_dir="shielded-vote-${CHAIN_BINARY_VERSION}-${platform}"
  release_url="${DO_BASE}/binaries/vote-sdk/${tarball_dir}.tar.gz"
  checksum_url="${release_url}.sha256"
  archive=$(mktemp)
  echo "Downloading ${release_url}..."
  curl -fsSL --retry 3 --connect-timeout 15 -o "${archive}" "${release_url}"
  if curl -fsSL -o "${archive}.sha256" "${checksum_url}" 2>/dev/null; then
    local expected actual
    expected=$(awk '{print $1}' "${archive}.sha256" | tr 'A-F' 'a-f')
    actual=$(svote_sha256_file "${archive}" | tr 'A-F' 'a-f')
    if [ "$expected" != "$actual" ]; then
      echo "ERROR: Release checksum mismatch." >&2
      exit 1
    fi
    rm -f "${archive}.sha256"
  else
    echo "WARNING: Checksum file not available - skipping verification."
  fi

  local members=("${tarball_dir}/bin/svoted")
  if [ "$install_create_val_tx" = "1" ]; then
    members+=("${tarball_dir}/bin/create-val-tx")
  fi
  tar xzf "${archive}" -C /tmp "${members[@]}"
  cp "/tmp/${tarball_dir}/bin/svoted" "${INSTALL_DIR}/svoted"
  chmod +x "${INSTALL_DIR}/svoted"
  if [ "$install_create_val_tx" = "1" ]; then
    cp "/tmp/${tarball_dir}/bin/create-val-tx" "${INSTALL_DIR}/create-val-tx"
    chmod +x "${INSTALL_DIR}/create-val-tx"
  fi
  rm -rf "${archive}" "/tmp/${tarball_dir}"
  export PATH="${INSTALL_DIR}:${PATH}"
  hash -r
  echo "Installed svoted to ${INSTALL_DIR}/svoted"
}

svote_fetch_genesis() {
  local live_genesis_url="${SEED_URL%/}/shielded-vote/v1/genesis"
  local chain_genesis_url="${DO_BASE}/genesis/${CHAIN_ID}/genesis.json"
  local legacy_genesis_url="${DO_BASE}/genesis.json"
  local genesis_tmp genesis_source genesis_chain_id
  genesis_tmp=$(mktemp)

  if curl -fsSL -o "${genesis_tmp}" "${live_genesis_url}" 2>/dev/null; then
    genesis_source="${live_genesis_url}"
  elif curl -fsSL -o "${genesis_tmp}" "${chain_genesis_url}" 2>/dev/null; then
    genesis_source="${chain_genesis_url}"
  elif curl -fsSL -o "${genesis_tmp}" "${legacy_genesis_url}"; then
    genesis_source="${legacy_genesis_url}"
  else
    echo "ERROR: Could not fetch genesis.json." >&2
    exit 1
  fi

  genesis_chain_id=$(jq -r '.chain_id // empty' "${genesis_tmp}" 2>/dev/null || true)
  if [ "$genesis_chain_id" != "$CHAIN_ID" ]; then
    echo "ERROR: genesis.json chain_id mismatch. Expected ${CHAIN_ID}, got ${genesis_chain_id:-<empty>}." >&2
    echo "  Source: ${genesis_source}" >&2
    exit 1
  fi
  cp "${genesis_tmp}" "${HOME_DIR}/config/genesis.json"
  svoted genesis validate-genesis --home "${HOME_DIR}"
  rm -f "${genesis_tmp}"
  echo "Genesis validated (${genesis_source})."
}

svote_restore_latest_snapshot() {
  if [ "${SVOTE_SKIP_SNAPSHOT:-0}" = "1" ]; then
    echo "SVOTE_SKIP_SNAPSHOT=1: skipping snapshot restore; node will sync from genesis."
    return 0
  fi

  local tmp metadata archive listing state_file
  local snapshot_chain_id snapshot_url snapshot_checksum snapshot_height snapshot_date
  tmp=$(mktemp -d)
  metadata="${tmp}/latest.json"
  archive="${tmp}/snapshot.tar.lz4"
  listing="${tmp}/snapshot.files"
  state_file="${tmp}/priv_validator_state.json"
  trap 'rm -rf "${tmp}"' RETURN

  echo "Fetching snapshot metadata from ${SNAPSHOT_BASE_URL%/}/latest.json..."
  if ! curl -fsSL --connect-timeout 15 --max-time 60 -o "${metadata}" "${SNAPSHOT_BASE_URL%/}/latest.json"; then
    echo "WARNING: No snapshot metadata is available; node will sync from genesis."
    return 0
  fi
  snapshot_chain_id=$(jq -r '.chain_id // empty' "${metadata}")
  snapshot_url=$(jq -r '.url // empty' "${metadata}")
  snapshot_checksum=$(jq -r '.checksum // empty' "${metadata}")
  snapshot_height=$(jq -r '.height // empty' "${metadata}")
  snapshot_date=$(jq -r '.date // empty' "${metadata}")
  if [ "$snapshot_chain_id" != "$CHAIN_ID" ]; then
    echo "WARNING: Snapshot chain_id mismatch. Expected ${CHAIN_ID}, got ${snapshot_chain_id:-<empty>}; syncing from genesis."
    return 0
  fi
  if ! printf '%s\n' "${snapshot_checksum}" | grep -Eq '^[0-9a-fA-F]{64}$'; then
    echo "WARNING: Snapshot metadata lacks a valid checksum; syncing from genesis."
    return 0
  fi

  echo "Latest snapshot: height ${snapshot_height:-unknown} (${snapshot_date:-unknown date})"
  curl -fsSL --retry 3 --connect-timeout 15 -o "${archive}" "${snapshot_url}"
  local expected actual
  expected=$(printf '%s' "${snapshot_checksum}" | tr 'A-F' 'a-f')
  actual=$(svote_sha256_file "${archive}" | tr 'A-F' 'a-f')
  if [ "$actual" != "$expected" ]; then
    echo "ERROR: Snapshot checksum mismatch." >&2
    exit 1
  fi
  lz4 -dc "${archive}" | tar -tf - > "${listing}"
  if ! awk 'BEGIN { ok=1 } !/^data(\/|$)/ || /(^|\/)\.\.(\/|$)/ { print; ok=0 } END { exit ok ? 0 : 1 }' "${listing}" >/dev/null; then
    echo "ERROR: Snapshot archive contains unsafe paths." >&2
    exit 1
  fi
  cp "${HOME_DIR}/data/priv_validator_state.json" "${state_file}"
  rm -rf "${HOME_DIR}/data"
  lz4 -dc "${archive}" | tar -C "${HOME_DIR}" -xf -
  cp "${state_file}" "${HOME_DIR}/data/priv_validator_state.json"
  rm -rf "${HOME_DIR}/data/cs.wal"
  echo "Snapshot restored."
}
