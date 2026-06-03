#!/usr/bin/env bash
# Shared helpers for Shielded-Vote chain upgrades (Cosmovisor staging + validation).

set -euo pipefail

readonly SVOTE_DEFAULT_DO_BASE='https://shielded-vote.nyc3.digitaloceanspaces.com'
readonly SVOTE_DEFAULT_GITHUB_REPO='valargroup/vote-sdk'
readonly SVOTE_DEFAULT_COSMOVISOR_VERSION='v1.6.0'
readonly SVOTE_COSMOVISOR_GITHUB_REPO='cosmos/cosmos-sdk'
readonly SVOTE_DAEMON_NAME='svoted'

# svote_upgrade_log ...
# Print a progress line to stderr so command substitutions can capture paths only.
svote_upgrade_log() {
  printf '==> %s\n' "$*" >&2
}

# svote_upgrade_die message
# Print an error to stderr and exit with status 1.
svote_upgrade_die() {
  echo "ERROR: $*" >&2
  exit 1
}

# svote_upgrade_warn message
# Print a warning to stderr without changing exit status.
svote_upgrade_warn() {
  echo "WARNING: $*" >&2
}

# svote_upgrade_resolve_do_base
# Resolve DO_BASE from SVOTE_DO_SPACES_BASE, bucket/region, or default; export DO_BASE (no trailing slash).
svote_upgrade_resolve_do_base() {
  local do_base_override="${SVOTE_DO_SPACES_BASE:-${DO_SPACES_BASE:-}}"
  local do_bucket="${SVOTE_DO_SPACES_BUCKET:-${DO_SPACES_BUCKET:-}}"
  local do_region="${SVOTE_DO_SPACES_REGION:-${DO_SPACES_REGION:-nyc3}}"
  if [ -n "${do_base_override}" ]; then
    DO_BASE="${do_base_override}"
  elif [ -n "${do_bucket}" ]; then
    DO_BASE="https://${do_bucket}.${do_region}.digitaloceanspaces.com"
  else
    DO_BASE="${SVOTE_DEFAULT_DO_BASE}"
  fi
  DO_BASE="${DO_BASE%/}"
  export DO_BASE
}

# svote_upgrade_detect_platform
# Set SVOTE_OS, SVOTE_ARCH, and SVOTE_PLATFORM from uname; die on unsupported OS/arch.
svote_upgrade_detect_platform() {
  case "$(uname -s)" in
    Linux)  SVOTE_OS="linux" ;;
    Darwin) SVOTE_OS="darwin" ;;
    *) svote_upgrade_die "Unsupported OS: $(uname -s). Linux is required for Cosmovisor upgrades." ;;
  esac
  case "$(uname -m)" in
    x86_64)        SVOTE_ARCH="amd64" ;;
    aarch64|arm64) SVOTE_ARCH="arm64" ;;
    *) svote_upgrade_die "Unsupported architecture: $(uname -m)." ;;
  esac
  SVOTE_PLATFORM="${SVOTE_OS}-${SVOTE_ARCH}"
  export SVOTE_OS SVOTE_ARCH SVOTE_PLATFORM
}

# svote_upgrade_sha256_file path
# Return lowercase hex SHA-256 of path; die if neither sha256sum nor shasum is available.
svote_upgrade_sha256_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    svote_upgrade_die "sha256sum or shasum is required."
  fi
}

# svote_upgrade_resolve_paths
# Populate and export validator paths (home, install dir, cosmovisor layout, systemd unit) from env defaults.
svote_upgrade_resolve_paths() {
  svote_upgrade_resolve_do_base
  svote_upgrade_detect_platform

  DAEMON_HOME="${SVOTE_HOME:-${DAEMON_HOME:-$HOME/.svoted}}"
  INSTALL_DIR="${SVOTE_INSTALL_DIR:-${INSTALL_DIR:-$HOME/.local/bin}}"
  SERVICE_NAME="${SVOTE_SERVICE_NAME:-svoted}"
  SERVICE_PATH="/etc/systemd/system/${SERVICE_NAME}.service"
  GITHUB_REPO="${SVOTE_GITHUB_REPO:-${GITHUB_REPO:-$SVOTE_DEFAULT_GITHUB_REPO}}"
  COSMOVISOR_VERSION="${SVOTE_COSMOVISOR_VERSION:-${COSMOVISOR_VERSION:-$SVOTE_DEFAULT_COSMOVISOR_VERSION}}"
  COSMOVISOR_BIN="${SVOTE_COSMOVISOR_BIN:-${COSMOVISOR_BIN:-${INSTALL_DIR}/cosmovisor}}"
  WRAPPER_BIN="${SVOTE_WRAPPER_SCRIPT:-${INSTALL_DIR}/svoted-wrapper.sh}"

  COSMVISOR_ROOT="${DAEMON_HOME}/cosmovisor"
  GENESIS_BIN_DIR="${COSMVISOR_ROOT}/genesis/bin"
  GENESIS_BIN="${GENESIS_BIN_DIR}/${SVOTE_DAEMON_NAME}"
  SERVICE_USER="${SVOTE_SERVICE_USER:-${SERVICE_USER:-}}"

  export DAEMON_HOME INSTALL_DIR SERVICE_NAME SERVICE_PATH SERVICE_USER
  export GITHUB_REPO COSMOVISOR_VERSION COSMOVISOR_BIN WRAPPER_BIN
  export COSMVISOR_ROOT GENESIS_BIN_DIR GENESIS_BIN
}

# svote_upgrade_require_linux_systemd_root
# Die unless running as root on Linux with systemctl available.
svote_upgrade_require_linux_systemd_root() {
  if [ "$(uname -s)" != "Linux" ]; then
    svote_upgrade_die "This script targets Linux hosts with systemd."
  fi
  if ! command -v systemctl >/dev/null 2>&1; then
    svote_upgrade_die "systemctl was not found. Use a systemd-based image."
  fi
  if [ "${EUID:-0}" -ne 0 ]; then
    svote_upgrade_die "This script must run as root (writes systemd units and may install under /usr/local/bin)."
  fi
}

# svote_upgrade_require_curl
# Die if curl is not on PATH.
svote_upgrade_require_curl() {
  command -v curl >/dev/null 2>&1 || svote_upgrade_die "curl is required."
}

# svote_upgrade_require_tools tool...
# Die if any named executable is missing from PATH.
svote_upgrade_require_tools() {
  local tool
  for tool in "$@"; do
    command -v "$tool" >/dev/null 2>&1 || svote_upgrade_die "${tool} is required."
  done
}

# svote_upgrade_systemd_unit_value key unit_file
# Print the value of key from Environment= lines in unit_file; return 1 if unit or key is missing.
svote_upgrade_systemd_unit_value() {
  local key="$1"
  local unit_file="$2"
  local line value

  [ -f "$unit_file" ] || return 1
  line=$(grep -E "^Environment=.*${key}=" "$unit_file" 2>/dev/null | head -n 1 || true)
  [ -n "$line" ] || return 1
  value=$(printf '%s\n' "$line" | sed -n "s/.*[\"']\\?${key}=\\([^\"' ]*\\)[\"' ]*.*/\\1/p" | head -n 1)
  [ -n "$value" ] || return 1
  printf '%s\n' "$value"
}

# svote_upgrade_autodetect_from_systemd_unit home_cli_set install_cli_set
# Override unset paths from svoted.service (User, home, install dir, wrapper); no-op if unit file absent.
svote_upgrade_autodetect_from_systemd_unit() {
  local home_cli_set="${1:-0}"
  local install_cli_set="${2:-0}"
  local unit_user detected_home detected_install detected_wrapper

  [ -f "$SERVICE_PATH" ] || return 0

  unit_user=$(grep -E '^User=' "$SERVICE_PATH" 2>/dev/null | head -n 1 | cut -d= -f2- | tr -d '[:space:]' || true)
  if [ -n "$unit_user" ] && [ "$unit_user" != "root" ]; then
    SERVICE_USER="$unit_user"
  fi

  if [ "$home_cli_set" != "1" ]; then
    detected_home=$(svote_upgrade_systemd_unit_value "SVOTE_HOME" "$SERVICE_PATH" || true)
    if [ -z "$detected_home" ]; then
      detected_home=$(svote_upgrade_systemd_unit_value "DAEMON_HOME" "$SERVICE_PATH" || true)
    fi
  fi

  if [ "$install_cli_set" != "1" ]; then
    detected_install=$(svote_upgrade_systemd_unit_value "SVOTE_INSTALL_DIR" "$SERVICE_PATH" || true)
    if [ -n "$detected_install" ]; then
      INSTALL_DIR="$detected_install"
    fi
  fi

  local detected_exec
  detected_exec=$(grep -E '^ExecStart=' "$SERVICE_PATH" 2>/dev/null | head -n 1 | cut -d= -f2- || true)
  if [ "$home_cli_set" != "1" ] && [ -z "$detected_home" ] && [ -n "$detected_exec" ]; then
    # Direct-mode units commonly set --home in ExecStart without SVOTE_HOME/DAEMON_HOME env.
    detected_home=$(printf '%s\n' "$detected_exec" | sed -n 's/.*--home[ =]\([^[:space:]]*\).*/\1/p' | head -n 1)
  fi
  if [ "$home_cli_set" != "1" ] && [ -n "$detected_home" ]; then
    DAEMON_HOME="$detected_home"
    SVOTE_HOME="$detected_home"
  fi
  detected_wrapper="${detected_exec%% *}"
  if [ -n "$detected_wrapper" ] && [ -x "$detected_wrapper" ] && [ "${detected_wrapper##*/}" = "svoted-wrapper.sh" ]; then
    WRAPPER_BIN="$detected_wrapper"
  fi
  if [ "$install_cli_set" != "1" ] && [ -n "$detected_wrapper" ]; then
    INSTALL_DIR="$(dirname "$detected_wrapper")"
  fi

  COSMOVISOR_BIN="${SVOTE_COSMOVISOR_BIN:-${COSMOVISOR_BIN:-${INSTALL_DIR}/cosmovisor}}"
  COSMVISOR_ROOT="${DAEMON_HOME}/cosmovisor"
  GENESIS_BIN_DIR="${COSMVISOR_ROOT}/genesis/bin"
  GENESIS_BIN="${GENESIS_BIN_DIR}/${SVOTE_DAEMON_NAME}"

  export SERVICE_USER DAEMON_HOME SVOTE_HOME INSTALL_DIR WRAPPER_BIN COSMOVISOR_BIN
  export COSMVISOR_ROOT GENESIS_BIN_DIR GENESIS_BIN

  if [ -n "${SERVICE_USER:-}" ]; then
    svote_upgrade_log "Detected validator service user: ${SERVICE_USER}"
  fi
  svote_upgrade_log "Using validator home: ${DAEMON_HOME}"
  svote_upgrade_log "Using install dir: ${INSTALL_DIR}"
}

# svote_upgrade_fixup_cosmovisor_ownership
# chown COSMVISOR_ROOT and COSMOVISOR_BIN to SERVICE_USER; no-op when user unset or unknown.
svote_upgrade_fixup_cosmovisor_ownership() {
  if [ -z "${SERVICE_USER:-}" ]; then
    return 0
  fi
  if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    svote_upgrade_warn "Service user ${SERVICE_USER} not found; skipping ownership fixup."
    return 0
  fi
  if [ -d "$COSMVISOR_ROOT" ]; then
    chown -R "${SERVICE_USER}:${SERVICE_USER}" "$COSMVISOR_ROOT"
  fi
  if [ -e "$COSMOVISOR_BIN" ]; then
    chown "${SERVICE_USER}:${SERVICE_USER}" "$COSMOVISOR_BIN"
  fi
  svote_upgrade_log "Set cosmovisor artifacts ownership to ${SERVICE_USER}"
}

# svote_upgrade_cosmovisor_asset_name
# Print the upstream Cosmovisor tarball filename for COSMOVISOR_VERSION and SVOTE_PLATFORM.
svote_upgrade_cosmovisor_asset_name() {
  printf 'cosmovisor-%s-%s.tar.gz' "$COSMOVISOR_VERSION" "$SVOTE_PLATFORM"
}

# svote_upgrade_cosmovisor_release_base
# Print the GitHub releases/download base URL for the pinned cosmovisor/v* tag (URL-encoded).
svote_upgrade_cosmovisor_release_base() {
  printf 'https://github.com/%s/releases/download/cosmovisor%%2F%s' \
    "$SVOTE_COSMOVISOR_GITHUB_REPO" "$COSMOVISOR_VERSION"
}

# svote_upgrade_download_with_fallback output spaces_url github_url label
# Download to output from Spaces first, then GitHub; die if both fail.
svote_upgrade_download_with_fallback() {
  local output="$1"
  local spaces_url="$2"
  local github_url="$3"
  local label="$4"

  if curl -fsSL --retry 3 --retry-delay 2 -o "$output" "$spaces_url"; then
    svote_upgrade_log "Downloaded ${label} from Spaces."
    return 0
  fi
  if curl -fsSL --retry 3 --retry-delay 2 -o "$output" "$github_url"; then
    svote_upgrade_warn "Downloaded ${label} from GitHub releases (Spaces fetch failed or object missing)."
    return 0
  fi
  svote_upgrade_die "Failed to download ${label} from Spaces and GitHub."
}

# svote_upgrade_ensure_wrapper_script
# Ensure WRAPPER_BIN exists and is executable; fetch from DO_BASE when missing.
svote_upgrade_ensure_wrapper_script() {
  if [ -x "$WRAPPER_BIN" ]; then
    return 0
  fi

  install -d -m 0755 "$(dirname "$WRAPPER_BIN")"

  if [ -n "${SVOTE_WRAPPER_SCRIPT:-}" ] && [ -f "${SVOTE_WRAPPER_SCRIPT}" ]; then
    cp "${SVOTE_WRAPPER_SCRIPT}" "$WRAPPER_BIN"
  elif curl -fsSL "${DO_BASE}/svoted-wrapper.sh" -o "$WRAPPER_BIN" 2>/dev/null; then
    :
  else
    svote_upgrade_die "Wrapper script missing: ${WRAPPER_BIN}. Set SVOTE_WRAPPER_SCRIPT or publish svoted-wrapper.sh to ${DO_BASE}/svoted-wrapper.sh."
  fi

  chmod 0755 "$WRAPPER_BIN"
  if [ -n "${SERVICE_USER:-}" ] && id "$SERVICE_USER" >/dev/null 2>&1; then
    chown "${SERVICE_USER}:${SERVICE_USER}" "$WRAPPER_BIN" 2>/dev/null || true
  fi
  svote_upgrade_log "Installed wrapper script at ${WRAPPER_BIN}"
}

# svote_upgrade_download_release_tarball tag tmp_dir
# Download shielded-vote release tarball for tag/platform, verify .sha256, print local path; die on mismatch.
svote_upgrade_download_release_tarball() {
  local tag="$1"
  local tmp_dir="$2"
  local tarball_name="shielded-vote-${tag}-${SVOTE_PLATFORM}.tar.gz"
  local tarball_path="${tmp_dir}/${tarball_name}"
  local checksum_path="${tmp_dir}/${tarball_name}.sha256"
  local spaces_url="${DO_BASE}/binaries/vote-sdk/${tarball_name}"
  local github_url="https://github.com/${GITHUB_REPO}/releases/download/${tag}/${tarball_name}"

  svote_upgrade_log "Downloading release ${tag} for ${SVOTE_PLATFORM}"
  svote_upgrade_download_with_fallback "$tarball_path" "$spaces_url" "$github_url" "${tarball_name}"

  if curl -fsSL --retry 3 --retry-delay 2 -o "$checksum_path" "${spaces_url}.sha256" 2>/dev/null \
    || curl -fsSL --retry 3 --retry-delay 2 -o "$checksum_path" "${github_url}.sha256" 2>/dev/null; then
    local expected actual
    expected=$(awk '{print $1}' "$checksum_path" | tr 'A-F' 'a-f')
    actual=$(svote_upgrade_sha256_file "$tarball_path" | tr 'A-F' 'a-f')
    if [ "$expected" != "$actual" ]; then
      svote_upgrade_die "Release checksum mismatch for ${tarball_name} (expected=${expected}, actual=${actual})."
    fi
    svote_upgrade_log "Release checksum verified."
  else
    svote_upgrade_die "Checksum file missing for ${tarball_name}; refusing to install unverified binary."
  fi

  printf '%s\n' "$tarball_path"
}

# svote_upgrade_extract_svoted tarball_path tmp_dir tag
# Extract svoted from release tarball into tmp_dir; print path to executable copy.
svote_upgrade_extract_svoted() {
  local tarball_path="$1"
  local tmp_dir="$2"
  local tag="$3"
  local extract_dir="${tmp_dir}/shielded-vote-${tag}-${SVOTE_PLATFORM}"
  local output_bin="${tmp_dir}/svoted"

  tar xzf "$tarball_path" -C "$tmp_dir" "${extract_dir#${tmp_dir}/}/bin/svoted" 2>/dev/null \
    || tar xzf "$tarball_path" -C "$tmp_dir" "shielded-vote-${tag}-${SVOTE_PLATFORM}/bin/svoted"
  cp "${extract_dir}/bin/svoted" "$output_bin"
  chmod 0755 "$output_bin"
  printf '%s\n' "$output_bin"
}

# svote_upgrade_verify_binary_tag binary expected_tag
# Die if binary version output does not exactly match expected_tag.
svote_upgrade_verify_binary_tag() {
  local binary="$1"
  local expected_tag="$2"
  local actual_version

  actual_version=$("$binary" version 2>/dev/null | tr -d '[:space:]' || true)
  if [ -z "$actual_version" ]; then
    svote_upgrade_die "Could not read version from ${binary}."
  fi
  if [ "$actual_version" != "$expected_tag" ]; then
    svote_upgrade_die "Binary version mismatch: expected ${expected_tag}, got ${actual_version}."
  fi
}

# svote_upgrade_install_cosmovisor tmp_dir
# Install cosmovisor to COSMOVISOR_BIN from official GitHub release with SHA256SUMS check; no-op if already installed.
svote_upgrade_install_cosmovisor() {
  local tmp_dir="$1"
  local archive="${tmp_dir}/cosmovisor.tar.gz"
  local extract_dir="${tmp_dir}/cosmovisor-extract"
  local sums_path="${tmp_dir}/SHA256SUMS-cosmovisor.txt"
  local asset_name expected actual found release_base release_page

  if [ -x "$COSMOVISOR_BIN" ]; then
    svote_upgrade_log "Cosmovisor already installed at ${COSMOVISOR_BIN}"
    return 0
  fi

  release_base=$(svote_upgrade_cosmovisor_release_base)
  release_page="https://github.com/${SVOTE_COSMOVISOR_GITHUB_REPO}/releases/tag/cosmovisor%2F${COSMOVISOR_VERSION}"
  asset_name=$(svote_upgrade_cosmovisor_asset_name)

  svote_upgrade_log "Installing Cosmovisor ${COSMOVISOR_VERSION} for ${SVOTE_PLATFORM}"
  svote_upgrade_log "Official release: ${release_page}"

  if ! curl -fsSL --retry 3 --retry-delay 2 -o "$archive" "${release_base}/${asset_name}"; then
    svote_upgrade_die "Failed to download ${asset_name} from ${release_page}"
  fi
  if ! curl -fsSL --retry 3 --retry-delay 2 -o "$sums_path" \
    "${release_base}/SHA256SUMS-cosmovisor-${COSMOVISOR_VERSION}.txt"; then
    svote_upgrade_die "Failed to download SHA256SUMS for Cosmovisor ${COSMOVISOR_VERSION} from ${release_page}"
  fi

  expected=$(awk -v f="$asset_name" '$2 == f { print $1; exit }' "$sums_path" | tr 'A-F' 'a-f')
  if [ -z "$expected" ]; then
    svote_upgrade_die "SHA256SUMS for Cosmovisor ${COSMOVISOR_VERSION} does not contain ${asset_name}."
  fi
  actual=$(svote_upgrade_sha256_file "$archive" | tr 'A-F' 'a-f')
  if [ "$expected" != "$actual" ]; then
    svote_upgrade_die "Cosmovisor checksum mismatch (expected=${expected}, actual=${actual})."
  fi
  svote_upgrade_log "Cosmovisor checksum verified."

  mkdir -p "$extract_dir"
  tar xzf "$archive" -C "$extract_dir"
  found=$(find "$extract_dir" -type f -name cosmovisor | head -n 1 || true)
  if [ -z "$found" ]; then
    svote_upgrade_die "Cosmovisor archive did not contain a cosmovisor binary."
  fi
  install -d -m 0755 "$(dirname "$COSMOVISOR_BIN")"
  install -m 0755 "$found" "$COSMOVISOR_BIN"
  if [ -n "${SERVICE_USER:-}" ]; then
    chown "${SERVICE_USER}:${SERVICE_USER}" "$COSMOVISOR_BIN" "$(dirname "$COSMOVISOR_BIN")" 2>/dev/null || true
  fi
  svote_upgrade_log "Installed Cosmovisor at ${COSMOVISOR_BIN}"
}

# svote_upgrade_upgrade_bin_dir plan_name
# Print cosmovisor upgrades/<plan_name>/bin directory path under DAEMON_HOME.
svote_upgrade_upgrade_bin_dir() {
  local plan_name="$1"
  printf '%s/upgrades/%s/bin' "$COSMVISOR_ROOT" "$plan_name"
}

# svote_upgrade_upgrade_bin_path plan_name
# Print full path to staged svoted binary for the named upgrade plan.
svote_upgrade_upgrade_bin_path() {
  local plan_name="$1"
  printf '%s/%s' "$(svote_upgrade_upgrade_bin_dir "$plan_name")" "$SVOTE_DAEMON_NAME"
}

# svote_upgrade_stage_binary source_bin target_bin
# Atomically install source_bin to target_bin (0755) via a .new temp file.
svote_upgrade_stage_binary() {
  local source_bin="$1"
  local target_bin="$2"
  local target_dir
  target_dir=$(dirname "$target_bin")
  install -d -m 0755 "$target_dir"
  install -m 0755 "$source_bin" "${target_bin}.new"
  mv -f "${target_bin}.new" "$target_bin"
}

# svote_upgrade_verify_validator_identity_files
# Require priv_validator_key/state under DAEMON_HOME; export SVOTE_CONSENSUS_PUBKEY or die.
svote_upgrade_verify_validator_identity_files() {
  local priv_key="${DAEMON_HOME}/config/priv_validator_key.json"
  local priv_state="${DAEMON_HOME}/data/priv_validator_state.json"

  [ -f "$priv_key" ] || svote_upgrade_die "${priv_key} is missing."
  [ -f "$priv_state" ] || svote_upgrade_die "${priv_state} is missing (required to avoid double-sign risk)."

  local pubkey
  pubkey=$(jq -r '.pub_key.value // .pub_key.key // empty' "$priv_key" 2>/dev/null || true)
  if [ -z "$pubkey" ] || [ "$pubkey" = "null" ]; then
    svote_upgrade_die "Could not read consensus pubkey fingerprint from ${priv_key}."
  fi
  SVOTE_CONSENSUS_PUBKEY="$pubkey"
  export SVOTE_CONSENSUS_PUBKEY
}

# svote_upgrade_require_single_signer_ack
# Require SVOTE_ACK_SINGLE_SIGNER=1 or interactive YES before stop/migrate; die otherwise.
svote_upgrade_require_single_signer_ack() {
  if [ "${SVOTE_ACK_SINGLE_SIGNER:-0}" = "1" ]; then
    return 0
  fi
  if [ ! -t 0 ]; then
    svote_upgrade_die "Refusing to continue without SVOTE_ACK_SINGLE_SIGNER=1 in non-interactive mode. Confirm no second live signer uses this consensus key."
  fi
  echo "Consensus pubkey: ${SVOTE_CONSENSUS_PUBKEY}"
  echo "Confirm no other live validator process uses this consensus key."
  printf 'Type YES to continue: ' > /dev/tty
  local response
  read -r response < /dev/tty
  if [ "$response" != "YES" ]; then
    svote_upgrade_die "Aborted by operator."
  fi
}

# svote_upgrade_is_upgrade_invocation_pid pid
# Return 0 when pid belongs to this script's shell ancestry (avoid migrate self-match).
svote_upgrade_is_upgrade_invocation_pid() {
  local check_pid="$1"
  local ancestor="$$"
  while [ -n "$ancestor" ] && [ "$ancestor" -gt 0 ] 2>/dev/null; do
    if [ "$ancestor" -eq "$check_pid" ]; then
      return 0
    fi
    ancestor=$(ps -o ppid= -p "$ancestor" 2>/dev/null | tr -d '[:space:]' || true)
  done
  return 1
}

# svote_upgrade_is_signer_runtime_cmd cmd home
# Return 0 when cmd is a live svoted/cosmovisor runtime for home (not upgrade tooling).
svote_upgrade_is_signer_runtime_cmd() {
  local cmd="$1"
  local home="$2"

  case "$cmd" in
    *update_chain.sh*|*_chain_upgrade_common.sh*) return 1 ;;
  esac

  if ! printf '%s\n' "$cmd" | grep -Eq '(^|[ /])(svoted|cosmovisor)( |$|--|$)'; then
    return 1
  fi

  if printf '%s\n' "$cmd" | grep -Fq -- " --home ${home}"; then
    return 0
  fi
  if printf '%s\n' "$cmd" | grep -Fq -- "--home=${home}"; then
    return 0
  fi
  return 1
}

# svote_upgrade_find_signer_processes
# Print pgrep lines for svoted/cosmovisor processes scoped to DAEMON_HOME, or nothing.
svote_upgrade_find_signer_processes() {
  local line pid cmd
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    pid="${line%% *}"
    cmd="${line#"$pid "}"
    if svote_upgrade_is_upgrade_invocation_pid "$pid"; then
      continue
    fi
    if svote_upgrade_is_signer_runtime_cmd "$cmd" "$DAEMON_HOME"; then
      printf '%s\n' "$line"
    fi
  done < <(pgrep -af "${SVOTE_DAEMON_NAME}|cosmovisor" 2>/dev/null || true)
}

# svote_upgrade_assert_no_signer_processes
# Die if any signer process is still running for DAEMON_HOME.
svote_upgrade_assert_no_signer_processes() {
  local running
  running=$(svote_upgrade_find_signer_processes || true)
  if [ -n "$running" ]; then
    svote_upgrade_die "Signer process still running for ${DAEMON_HOME}: ${running}"
  fi
}

# svote_upgrade_stop_validator_service
# systemctl stop SERVICE_NAME and die if the unit or signer processes remain active.
svote_upgrade_stop_validator_service() {
  if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    svote_upgrade_log "Stopping ${SERVICE_NAME}"
    systemctl stop "$SERVICE_NAME"
  fi
  if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    svote_upgrade_die "${SERVICE_NAME} is still active after stop request."
  fi
  svote_upgrade_assert_no_signer_processes
}

# svote_upgrade_parse_plan_name plan_json
# Extract scheduled plan name from JSON (.plan.name or .name); print empty string when absent.
svote_upgrade_parse_plan_name() {
  local plan_json="$1"
  printf '%s\n' "$plan_json" | jq -r '.plan.name // .name // empty' 2>/dev/null || true
}

# svote_upgrade_parse_plan_height plan_json
# Extract scheduled halt height from JSON (.plan.height or .height); print empty string when absent.
svote_upgrade_parse_plan_height() {
  local plan_json="$1"
  printf '%s\n' "$plan_json" | jq -r '.plan.height // .height // empty' 2>/dev/null || true
}

# svote_upgrade_query_upgrade_plan
# Query on-chain upgrade plan as JSON; return empty on "no plan"; die on RPC/parse errors.
svote_upgrade_query_upgrade_plan() {
  local query_bin="${GENESIS_BIN}"
  local plan_json query_err
  if [ ! -x "$query_bin" ]; then
    query_bin="$(command -v svoted 2>/dev/null || true)"
  fi
  [ -n "$query_bin" ] || svote_upgrade_die "No svoted binary available to query upgrade plan."
  query_err=$(mktemp)
  if ! plan_json=$("$query_bin" query upgrade plan --home "$DAEMON_HOME" --output json 2>"$query_err"); then
    if grep -qi 'no upgrade plan\|not found\|no plan' "$query_err" 2>/dev/null; then
      rm -f "$query_err"
      return 0
    fi
    svote_upgrade_die "Failed to query upgrade plan: $(tr -d '\n' < "$query_err")"
  fi
  rm -f "$query_err"
  if [ -z "$plan_json" ] || [ "$plan_json" = "null" ]; then
    return 0
  fi
  if ! printf '%s\n' "$plan_json" | jq empty >/dev/null 2>&1; then
    svote_upgrade_die "Upgrade plan query returned invalid JSON."
  fi
  printf '%s\n' "$plan_json"
}

# svote_upgrade_validate_scheduled_plan expected_name allow_no_plan
# Die unless chain plan name matches and halt height is still in the future; honor allow_no_plan.
svote_upgrade_validate_scheduled_plan() {
  local expected_name="$1"
  local allow_no_plan="${2:-0}"
  local plan_json plan_name plan_height current_height

  plan_json=$(svote_upgrade_query_upgrade_plan)
  if [ -z "$plan_json" ] || [ "$plan_json" = "null" ]; then
    if [ "$allow_no_plan" = "1" ]; then
      svote_upgrade_warn "No upgrade plan is currently scheduled (--allow-no-plan set)."
      return 0
    fi
    svote_upgrade_die "No upgrade plan is currently scheduled. Use --allow-no-plan to pre-stage ahead of scheduling."
  fi

  plan_name=$(svote_upgrade_parse_plan_name "$plan_json")
  plan_height=$(svote_upgrade_parse_plan_height "$plan_json")
  if [ -z "$plan_name" ] || [ "$plan_name" = "null" ]; then
    svote_upgrade_die "Could not parse scheduled upgrade plan name."
  fi
  if [ "$plan_name" != "$expected_name" ]; then
    svote_upgrade_die "Scheduled plan name mismatch: expected ${expected_name}, chain has ${plan_name}."
  fi

  current_height=$(
    svote_upgrade_query_block_height || echo "0"
  )
  if [ "$current_height" != "0" ] && [ -n "$plan_height" ] && [ "$plan_height" != "null" ]; then
    if [ "$current_height" -ge "$plan_height" ]; then
      svote_upgrade_die "Scheduled upgrade height ${plan_height} has already passed (current=${current_height})."
    fi
  fi
  SVOTE_SCHEDULED_PLAN_NAME="$plan_name"
  SVOTE_SCHEDULED_PLAN_HEIGHT="$plan_height"
  export SVOTE_SCHEDULED_PLAN_NAME SVOTE_SCHEDULED_PLAN_HEIGHT
}

# svote_upgrade_query_block_height
# Print latest block height from svoted status JSON, or return 1 when query fails.
svote_upgrade_query_block_height() {
  local query_bin="${GENESIS_BIN}"
  if [ ! -x "$query_bin" ]; then
    query_bin="$(command -v svoted 2>/dev/null || true)"
  fi
  [ -n "$query_bin" ] || return 1
  "$query_bin" status --home "$DAEMON_HOME" 2>/dev/null | jq -r '.sync_info.latest_block_height // empty'
}

# svote_upgrade_assert_layout_ready plan_name
# Die unless genesis, upgrade, and cosmovisor binaries exist and are executable.
svote_upgrade_assert_layout_ready() {
  local plan_name="$1"
  local upgrade_bin
  upgrade_bin=$(svote_upgrade_upgrade_bin_path "$plan_name")

  [ -x "$GENESIS_BIN" ] || svote_upgrade_die "Missing genesis binary: ${GENESIS_BIN}"
  [ -x "$upgrade_bin" ] || svote_upgrade_die "Missing staged upgrade binary: ${upgrade_bin}"
  [ -x "$COSMOVISOR_BIN" ] || svote_upgrade_die "Missing cosmovisor binary: ${COSMOVISOR_BIN}"
}

# svote_upgrade_checklist_line status message
# Print one verify-prestage checklist line: [PASS|FAIL] message.
svote_upgrade_checklist_line() {
  local status="$1"
  local message="$2"
  printf '[%s] %s\n' "$status" "$message"
}

# svote_upgrade_verify_prestage plan_name expected_tag allow_no_plan require_cosmovisor_service
# Run staging and optional service PASS/FAIL checks; die with failure counts when any check fails.
svote_upgrade_verify_prestage() {
  local plan_name="$1"
  local expected_tag="$2"
  local allow_no_plan="${3:-0}"
  local require_cosmovisor_service="${4:-1}"
  local upgrade_bin
  local staging_failures=0
  local service_failures=0
  local plan_json plan_name_on_chain
  upgrade_bin=$(svote_upgrade_upgrade_bin_path "$plan_name")

  svote_upgrade_verify_validator_identity_files

  echo "=== Staging checks ==="
  plan_json=$(svote_upgrade_query_upgrade_plan)
  plan_name_on_chain=$(svote_upgrade_parse_plan_name "$plan_json")
  if [ -n "$plan_name_on_chain" ] && [ "$plan_name_on_chain" != "null" ]; then
    if [ "$plan_name_on_chain" = "$plan_name" ]; then
      svote_upgrade_checklist_line PASS "Scheduled upgrade plan matches ${plan_name}"
    else
      svote_upgrade_checklist_line FAIL "Scheduled plan mismatch (chain=${plan_name_on_chain}, expected=${plan_name})"
      staging_failures=$((staging_failures + 1))
    fi
  elif [ "$allow_no_plan" = "1" ]; then
    svote_upgrade_checklist_line PASS "No scheduled plan yet (--allow-no-plan)"
  else
    svote_upgrade_checklist_line FAIL "No scheduled upgrade plan on chain"
    staging_failures=$((staging_failures + 1))
  fi

  if [ -x "$GENESIS_BIN" ]; then
    svote_upgrade_checklist_line PASS "Genesis binary present (${GENESIS_BIN})"
  else
    svote_upgrade_checklist_line FAIL "Genesis binary missing (${GENESIS_BIN})"
    staging_failures=$((staging_failures + 1))
  fi

  if [ -x "$upgrade_bin" ]; then
    svote_upgrade_checklist_line PASS "Upgrade binary present (${upgrade_bin})"
    local actual_version
    actual_version=$("$upgrade_bin" version 2>/dev/null | tr -d '[:space:]' || true)
    if [ "$actual_version" = "$expected_tag" ]; then
      svote_upgrade_checklist_line PASS "Upgrade binary version matches ${expected_tag}"
    else
      svote_upgrade_checklist_line FAIL "Upgrade binary version mismatch (expected ${expected_tag}, got ${actual_version:-<unknown>})"
      staging_failures=$((staging_failures + 1))
    fi
  else
    svote_upgrade_checklist_line FAIL "Upgrade binary missing (${upgrade_bin})"
    staging_failures=$((staging_failures + 1))
  fi

  if [ -x "$COSMOVISOR_BIN" ]; then
    svote_upgrade_checklist_line PASS "Cosmovisor installed (${COSMOVISOR_BIN})"
  else
    svote_upgrade_checklist_line FAIL "Cosmovisor missing (${COSMOVISOR_BIN})"
    staging_failures=$((staging_failures + 1))
  fi

  if [ "$require_cosmovisor_service" = "1" ]; then
    local effective_mode effective_daemon_home effective_svote_home effective_moniker effective_exec
    echo "=== Service checks ==="
    effective_mode=$(svote_upgrade_systemd_effective_env_value "SVOTE_UPGRADE_MODE" || true)
    if [ "$effective_mode" = "cosmovisor" ]; then
      svote_upgrade_checklist_line PASS "systemd effective SVOTE_UPGRADE_MODE=cosmovisor"
    else
      svote_upgrade_checklist_line FAIL "systemd effective SVOTE_UPGRADE_MODE is ${effective_mode:-<unset>} (expected cosmovisor)"
      service_failures=$((service_failures + 1))
    fi

    effective_daemon_home=$(svote_upgrade_systemd_effective_env_value "DAEMON_HOME" || true)
    if [ "$effective_daemon_home" = "$DAEMON_HOME" ]; then
      svote_upgrade_checklist_line PASS "systemd effective DAEMON_HOME=${DAEMON_HOME}"
    else
      svote_upgrade_checklist_line FAIL "systemd effective DAEMON_HOME is ${effective_daemon_home:-<unset>} (expected ${DAEMON_HOME})"
      service_failures=$((service_failures + 1))
    fi

    effective_svote_home=$(svote_upgrade_systemd_effective_env_value "SVOTE_HOME" || true)
    if [ "$effective_svote_home" = "$DAEMON_HOME" ]; then
      svote_upgrade_checklist_line PASS "systemd effective SVOTE_HOME=${DAEMON_HOME}"
    else
      svote_upgrade_checklist_line FAIL "systemd effective SVOTE_HOME is ${effective_svote_home:-<unset>} (expected ${DAEMON_HOME})"
      service_failures=$((service_failures + 1))
    fi

    effective_moniker=$(svote_upgrade_systemd_effective_env_value "MONIKER" || true)
    if [ -n "$effective_moniker" ]; then
      svote_upgrade_checklist_line PASS "systemd effective MONIKER is set (${effective_moniker})"
    else
      svote_upgrade_checklist_line FAIL "systemd effective MONIKER is unset (required by svoted-wrapper.sh)"
      service_failures=$((service_failures + 1))
    fi

    effective_exec=$(svote_upgrade_systemd_effective_execstart || true)
    if [ "$effective_exec" = "$WRAPPER_BIN" ]; then
      svote_upgrade_checklist_line PASS "systemd effective ExecStart=${WRAPPER_BIN}"
    else
      svote_upgrade_checklist_line FAIL "systemd effective ExecStart is ${effective_exec:-<unset>} (expected ${WRAPPER_BIN})"
      service_failures=$((service_failures + 1))
    fi

    if svote_upgrade_has_cosmovisor_runtime_for_home; then
      svote_upgrade_checklist_line PASS "cosmovisor runtime process is active for ${DAEMON_HOME}"
    else
      svote_upgrade_checklist_line FAIL "cosmovisor runtime process missing for ${DAEMON_HOME}"
      service_failures=$((service_failures + 1))
    fi
  else
    svote_upgrade_log "Skipping service checks (--skip-cosmovisor-service)."
  fi

  if [ "$staging_failures" -gt 0 ] || [ "$service_failures" -gt 0 ]; then
    svote_upgrade_die "Pre-stage verification failed (staging=${staging_failures}, service=${service_failures})."
  fi
  svote_upgrade_log "Pre-stage verification passed."
}

# svote_upgrade_derive_moniker_from_home home
# Print validator moniker from config.toml when present; empty string otherwise.
svote_upgrade_derive_moniker_from_home() {
  local home="$1"
  local config_toml="${home}/config/config.toml"
  [ -f "$config_toml" ] || return 0
  sed -n 's/^moniker[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$config_toml" | head -n 1
}

# svote_upgrade_derive_chain_id_from_home home
# Print chain ID from genesis.json when present; empty string otherwise.
svote_upgrade_derive_chain_id_from_home() {
  local home="$1"
  local genesis="${home}/config/genesis.json"
  [ -f "$genesis" ] || return 0
  jq -r '.chain_id // empty' "$genesis" 2>/dev/null || true
}

# svote_upgrade_extract_direct_svoted_start_args exec_start_cmd home
# Print extra svoted start args from a direct ExecStart line after --home; empty when none.
svote_upgrade_extract_direct_svoted_start_args() {
  local exec_start_cmd="$1"
  local home="$2"
  local remainder

  case "$exec_start_cmd" in
    *svoted-wrapper*|*cosmovisor*) return 0 ;;
  esac
  case "$exec_start_cmd" in
    *svoted*) ;;
    *) return 0 ;;
  esac

  if [[ "$exec_start_cmd" == *"--home ${home}"* ]]; then
    remainder="${exec_start_cmd#*--home ${home}}"
  elif [[ "$exec_start_cmd" == *"--home=${home}"* ]]; then
    remainder="${exec_start_cmd#*--home=${home}}"
  else
    return 0
  fi

  remainder="${remainder#"${remainder%%[![:space:]]*}"}"
  printf '%s\n' "$remainder"
}

# svote_upgrade_set_systemd_environment_key tmp_unit key value
# Replace all Environment=key assignments and append a single canonical value under [Service].
svote_upgrade_set_systemd_environment_key() {
  local tmp_unit="$1"
  local key="$2"
  local value="$3"
  local tmp_filtered tmp_updated value_for_awk

  tmp_filtered=$(mktemp)
  tmp_updated=$(mktemp)

  awk -v key="$key" '
    /^[[:space:]]*Environment=/ {
      if ($0 ~ ("(^|[\"[:space:]])" key "=")) {
        next
      }
    }
    { print }
  ' "$tmp_unit" > "$tmp_filtered"

  value_for_awk=${value//\\/\\\\}
  value_for_awk=${value_for_awk//\"/\\\"}

  awk -v key="$key" -v value="$value_for_awk" '
    {
      print
      if ($0 ~ /^\[Service\]$/) {
        print "Environment=\"" key "=" value "\""
      }
    }
  ' "$tmp_filtered" > "$tmp_updated"

  mv "$tmp_updated" "$tmp_unit"
  rm -f "$tmp_filtered"
}

# svote_upgrade_extract_effective_env_value env_blob key
# Extract the last KEY=value token from a systemd Environment blob.
svote_upgrade_extract_effective_env_value() {
  local env_blob="$1"
  local key="$2"
  local token value=""

  for token in $env_blob; do
    token="${token#\"}"
    token="${token%\"}"
    case "$token" in
      "${key}="*)
        value="${token#${key}=}"
        ;;
    esac
  done
  [ -n "$value" ] || return 1
  printf '%s\n' "$value"
}

# svote_upgrade_systemd_effective_env_value key
# Extract the runtime-effective key from `systemctl show SERVICE_NAME -p Environment`.
svote_upgrade_systemd_effective_env_value() {
  local key="$1"
  local env_blob

  env_blob=$(systemctl show "$SERVICE_NAME" -p Environment --value 2>/dev/null || true)
  [ -n "$env_blob" ] || return 1
  svote_upgrade_extract_effective_env_value "$env_blob" "$key"
}

# svote_upgrade_systemd_effective_execstart
# Print the runtime-effective ExecStart command from systemctl show/cat; return 1 if unavailable.
svote_upgrade_systemd_effective_execstart() {
  local exec_blob path_value merged_line merged_cmd

  exec_blob=$(systemctl show "$SERVICE_NAME" -p ExecStart --value 2>/dev/null || true)
  if [ -n "$exec_blob" ] && [ "$exec_blob" != "[]" ]; then
    path_value=$(printf '%s\n' "$exec_blob" | sed -n 's/.*path=\([^ ;}][^ ;}]*\).*/\1/p' | head -n 1)
    if [ -n "$path_value" ]; then
      printf '%s\n' "$path_value"
      return 0
    fi
  fi

  merged_cmd=""
  while IFS= read -r merged_line; do
    case "$merged_line" in
      ExecStart=)
        merged_cmd=""
        ;;
      ExecStart=*)
        merged_cmd="${merged_line#ExecStart=}"
        ;;
    esac
  done < <(systemctl cat "$SERVICE_NAME" 2>/dev/null || true)
  [ -n "$merged_cmd" ] || return 1
  printf '%s\n' "$merged_cmd"
}

# svote_upgrade_has_cosmovisor_runtime_for_home
# Return 0 when a running cosmovisor process includes DAEMON_HOME in argv.
svote_upgrade_has_cosmovisor_runtime_for_home() {
  pgrep -af "cosmovisor" 2>/dev/null | grep -F -- "$DAEMON_HOME" >/dev/null 2>&1
}

# svote_upgrade_assert_cosmovisor_runtime
# Die unless effective systemd mode is cosmovisor and a cosmovisor process is running for DAEMON_HOME.
svote_upgrade_assert_cosmovisor_runtime() {
  local mode effective_exec main_pid main_cmd

  mode=$(svote_upgrade_systemd_effective_env_value "SVOTE_UPGRADE_MODE" || true)
  if [ "$mode" != "cosmovisor" ]; then
    svote_upgrade_die "Effective SVOTE_UPGRADE_MODE is ${mode:-<unset>} (expected cosmovisor)."
  fi

  effective_exec=$(svote_upgrade_systemd_effective_execstart || true)
  if [ -n "$effective_exec" ] && [ "$effective_exec" != "$WRAPPER_BIN" ]; then
    svote_upgrade_die "Effective ExecStart is ${effective_exec} (expected ${WRAPPER_BIN})."
  fi

  if ! svote_upgrade_has_cosmovisor_runtime_for_home; then
    main_pid=$(systemctl show "$SERVICE_NAME" -p MainPID --value 2>/dev/null || true)
    main_cmd=""
    if [ -n "$main_pid" ] && [ "$main_pid" != "0" ]; then
      main_cmd=$(ps -o command= -p "$main_pid" 2>/dev/null || true)
    fi
    svote_upgrade_die "No cosmovisor runtime process found for ${DAEMON_HOME} (service main_pid=${main_pid:-<unknown>} cmd=${main_cmd:-<unknown>})."
  fi
}

# svote_upgrade_escape_systemd_env_value value
# Escape backslashes/double-quotes for Environment="KEY=value" lines.
svote_upgrade_escape_systemd_env_value() {
  local escaped="$1"
  escaped="${escaped//\\/\\\\}"
  escaped="${escaped//\"/\\\"}"
  printf '%s\n' "$escaped"
}

# svote_upgrade_detect_existing_execstart
# Return the last merged ExecStart command (after reset semantics) from service + drop-ins.
svote_upgrade_detect_existing_execstart() {
  local line existing_exec=""

  while IFS= read -r line; do
    case "$line" in
      ExecStart=)
        existing_exec=""
        ;;
      ExecStart=*)
        existing_exec="${line#ExecStart=}"
        ;;
    esac
  done < <(systemctl cat "$SERVICE_NAME" 2>/dev/null || cat "$SERVICE_PATH")

  [ -n "$existing_exec" ] || return 1
  printf '%s\n' "$existing_exec"
}

# svote_upgrade_neutralize_conflicting_direct_dropins migrate_dropin
# Remove non-migrate ExecStart overrides and direct-mode env from drop-ins (idempotent).
svote_upgrade_neutralize_conflicting_direct_dropins() {
  local migrate_dropin="$1"
  local dropin_dir dropin tmp_sanitized

  dropin_dir="$(dirname "$SERVICE_PATH")/${SERVICE_NAME}.service.d"
  [ -d "$dropin_dir" ] || return 0

  for dropin in "$dropin_dir"/*.conf; do
    [ -f "$dropin" ] || continue
    [ "$dropin" = "$migrate_dropin" ] && continue

    if ! grep -Eq '^[[:space:]]*Environment=.*(SVOTE_UPGRADE_MODE|DAEMON_HOME|SVOTE_HOME|MONIKER|SVOTE_CHAIN_ID|COSMOVISOR_BIN|DAEMON_NAME|DAEMON_ALLOW_DOWNLOAD_BINARIES|SVOTE_INSTALL_DIR|SVOTED_BIN|SVOTE_WRAPPER_SVOTED_START_ARGS)=|^[[:space:]]*ExecStart=' "$dropin" 2>/dev/null; then
      continue
    fi

    tmp_sanitized=$(mktemp)
    awk '
      /^[[:space:]]*Environment=/ && /(SVOTE_UPGRADE_MODE|DAEMON_HOME|SVOTE_HOME|MONIKER|SVOTE_CHAIN_ID|COSMOVISOR_BIN|DAEMON_NAME|DAEMON_ALLOW_DOWNLOAD_BINARIES|SVOTE_INSTALL_DIR|SVOTED_BIN|SVOTE_WRAPPER_SVOTED_START_ARGS)=/ { next }
      # Keep migrate deterministic: only z-cosmovisor.conf may define ExecStart.
      /^[[:space:]]*ExecStart=/ { next }
      { print }
    ' "$dropin" > "$tmp_sanitized"

    if ! cmp -s "$dropin" "$tmp_sanitized" 2>/dev/null; then
      cp -p "$dropin" "${dropin}.bak.pre-migrate.$(date +%Y%m%d%H%M%S)"
      mv -f "$tmp_sanitized" "$dropin"
      svote_upgrade_log "Neutralized conflicting direct-mode directives in ${dropin}"
    else
      rm -f "$tmp_sanitized"
    fi
  done
}

# svote_upgrade_patch_systemd_unit_for_cosmovisor
# Write deterministic migrate drop-in for cosmovisor mode; print backup path; die if unit missing.
svote_upgrade_patch_systemd_unit_for_cosmovisor() {
  local backup_path="${SERVICE_PATH}.bak.$(date +%Y%m%d%H%M%S)"
  local dropin_dir migrate_dropin primary_dropin old_exec inferred_args derived_moniker derived_chain_id
  local daemon_home_escaped moniker_escaped cosmovisor_bin_escaped wrapper_args_escaped
  dropin_dir="$(dirname "$SERVICE_PATH")/${SERVICE_NAME}.service.d"
  # Use a lexicographically-late name so this drop-in wins over earlier files like primary.conf.
  migrate_dropin="${dropin_dir}/z-cosmovisor.conf"
  primary_dropin="${dropin_dir}/primary.conf"

  if [ ! -f "$SERVICE_PATH" ]; then
    svote_upgrade_die "systemd unit not found: ${SERVICE_PATH}. Run join.sh first."
  fi

  cp -p "$SERVICE_PATH" "$backup_path"
  install -d -m 0755 "$dropin_dir"

  old_exec=$(svote_upgrade_detect_existing_execstart || true)
  if [ -z "${SVOTE_WRAPPER_SVOTED_START_ARGS:-}" ] && [ -n "$old_exec" ]; then
    inferred_args=$(svote_upgrade_extract_direct_svoted_start_args "$old_exec" "$DAEMON_HOME" || true)
    if [ -n "$inferred_args" ]; then
      SVOTE_WRAPPER_SVOTED_START_ARGS="$inferred_args"
      export SVOTE_WRAPPER_SVOTED_START_ARGS
      svote_upgrade_log "Inferred wrapper start args from direct ExecStart: ${SVOTE_WRAPPER_SVOTED_START_ARGS}"
    fi
  fi

  derived_moniker=$(svote_upgrade_derive_moniker_from_home "$DAEMON_HOME" || true)
  derived_chain_id=$(svote_upgrade_derive_chain_id_from_home "$DAEMON_HOME" || true)

  svote_upgrade_neutralize_conflicting_direct_dropins "$migrate_dropin"

  daemon_home_escaped=$(svote_upgrade_escape_systemd_env_value "$DAEMON_HOME")
  cosmovisor_bin_escaped=$(svote_upgrade_escape_systemd_env_value "$COSMOVISOR_BIN")

  {
    printf '[Service]\n'
    printf 'ExecStart=\n'
    printf 'ExecStart=%s\n' "$WRAPPER_BIN"
    printf 'Environment="SVOTE_UPGRADE_MODE=cosmovisor"\n'
    printf 'Environment="DAEMON_HOME=%s"\n' "$daemon_home_escaped"
    printf 'Environment="SVOTE_HOME=%s"\n' "$daemon_home_escaped"
    if [ -n "$derived_moniker" ]; then
      moniker_escaped=$(svote_upgrade_escape_systemd_env_value "$derived_moniker")
      printf 'Environment="MONIKER=%s"\n' "$moniker_escaped"
    fi
    if [ -n "$derived_chain_id" ]; then
      printf 'Environment="SVOTE_CHAIN_ID=%s"\n' "$(svote_upgrade_escape_systemd_env_value "$derived_chain_id")"
    fi
    printf 'Environment="COSMOVISOR_BIN=%s"\n' "$cosmovisor_bin_escaped"
    printf 'Environment="DAEMON_NAME=%s"\n' "$SVOTE_DAEMON_NAME"
    printf 'Environment="DAEMON_ALLOW_DOWNLOAD_BINARIES=false"\n'
    printf 'Environment="SVOTE_INSTALL_DIR=%s"\n' "$(svote_upgrade_escape_systemd_env_value "$INSTALL_DIR")"
    printf 'Environment="SVOTED_BIN=%s"\n' "$(svote_upgrade_escape_systemd_env_value "${INSTALL_DIR}/${SVOTE_DAEMON_NAME}")"
    if [ -n "${SVOTE_WRAPPER_SVOTED_START_ARGS:-}" ]; then
      wrapper_args_escaped=$(svote_upgrade_escape_systemd_env_value "$SVOTE_WRAPPER_SVOTED_START_ARGS")
      printf 'Environment="SVOTE_WRAPPER_SVOTED_START_ARGS=%s"\n' "$wrapper_args_escaped"
    fi
  } > "${migrate_dropin}.tmp"
  mv -f "${migrate_dropin}.tmp" "$migrate_dropin"
  chmod 0644 "$migrate_dropin"

  if [ -f "$primary_dropin" ]; then
    cp -p "$migrate_dropin" "${primary_dropin}.tmp"
    mv -f "${primary_dropin}.tmp" "$primary_dropin"
    chmod 0644 "$primary_dropin"
    svote_upgrade_log "Replaced existing ${primary_dropin} with cosmovisor-wrapper override"
  fi

  svote_upgrade_log "Wrote migrate drop-in ${migrate_dropin} (deterministic precedence)"
  printf '%s\n' "$backup_path"
}

# svote_upgrade_restart_service backup_unit
# daemon-reload and restart SERVICE_NAME; restore backup_unit on failure then die.
svote_upgrade_restart_service() {
  local backup_unit="$1"
  systemctl daemon-reload
  if ! systemctl restart "$SERVICE_NAME"; then
    if [ -n "$backup_unit" ] && [ -f "$backup_unit" ]; then
      svote_upgrade_warn "Restart failed; restoring previous systemd unit from ${backup_unit}"
      cp -p "$backup_unit" "$SERVICE_PATH"
      systemctl daemon-reload
      systemctl restart "$SERVICE_NAME" || true
    fi
    svote_upgrade_die "Failed to restart ${SERVICE_NAME} after migration."
  fi
}

# svote_upgrade_wait_for_rpc timeout_secs
# Poll svoted status until RPC responds or timeout_secs elapses; fail fast on broken runtime.
svote_upgrade_wait_for_rpc() {
  local timeout_secs="${1:-120}"
  local deadline=$((SECONDS + timeout_secs))
  local query_bin="${GENESIS_BIN}"
  local effective_mode effective_exec unit_active missing_runtime_checks=0
  if [ ! -x "$query_bin" ]; then
    query_bin="$(command -v svoted 2>/dev/null || true)"
  fi
  while [ "$SECONDS" -le "$deadline" ]; do
    effective_mode=$(svote_upgrade_systemd_effective_env_value "SVOTE_UPGRADE_MODE" || true)
    if [ -n "$effective_mode" ] && [ "$effective_mode" != "cosmovisor" ]; then
      svote_upgrade_die "Service migrated with unexpected SVOTE_UPGRADE_MODE=${effective_mode} (expected cosmovisor)."
    fi

    effective_exec=$(svote_upgrade_systemd_effective_execstart || true)
    if [ -n "$effective_exec" ] && [ "$effective_exec" != "$WRAPPER_BIN" ]; then
      svote_upgrade_die "Service migrated with unexpected ExecStart=${effective_exec} (expected ${WRAPPER_BIN})."
    fi

    if systemctl is-failed --quiet "$SERVICE_NAME" 2>/dev/null; then
      svote_upgrade_die "Service ${SERVICE_NAME} entered failed state after migrate restart."
    fi

    unit_active=0
    if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
      unit_active=1
    fi

    if [ -n "$query_bin" ] && "$query_bin" status --home "$DAEMON_HOME" >/dev/null 2>&1; then
      return 0
    fi

    if [ "$unit_active" = "1" ]; then
      if svote_upgrade_has_cosmovisor_runtime_for_home; then
        missing_runtime_checks=0
      else
        missing_runtime_checks=$((missing_runtime_checks + 1))
        if [ "$missing_runtime_checks" -ge 3 ]; then
          svote_upgrade_die "Service is active but no cosmovisor runtime process found for ${DAEMON_HOME}."
        fi
      fi
    fi
    sleep 3
  done
  svote_upgrade_die "Timed out waiting for svoted RPC after restart."
}
