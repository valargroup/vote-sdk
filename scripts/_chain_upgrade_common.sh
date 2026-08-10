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
  CHAIN_API="${SVOTE_CHAIN_API:-${CHAIN_API:-}}"
  CHAIN_API="${CHAIN_API%/}"
  COSMOVISOR_VERSION="${SVOTE_COSMOVISOR_VERSION:-${COSMOVISOR_VERSION:-$SVOTE_DEFAULT_COSMOVISOR_VERSION}}"
  COSMOVISOR_BIN="${SVOTE_COSMOVISOR_BIN:-${COSMOVISOR_BIN:-${INSTALL_DIR}/cosmovisor}}"
  WRAPPER_BIN="${SVOTE_WRAPPER_SCRIPT:-${INSTALL_DIR}/svoted-wrapper.sh}"

  COSMVISOR_ROOT="${DAEMON_HOME}/cosmovisor"
  GENESIS_BIN_DIR="${COSMVISOR_ROOT}/genesis/bin"
  GENESIS_BIN="${GENESIS_BIN_DIR}/${SVOTE_DAEMON_NAME}"
  SERVICE_USER="${SVOTE_SERVICE_USER:-${SERVICE_USER:-}}"

  export DAEMON_HOME INSTALL_DIR SERVICE_NAME SERVICE_PATH SERVICE_USER
  export GITHUB_REPO CHAIN_API COSMOVISOR_VERSION COSMOVISOR_BIN WRAPPER_BIN
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
  # Parameter expansion instead of sed so the optional surrounding quotes are handled portably (BSD
  # sed, unlike GNU sed, does not support \? in BRE). Take the text after KEY=, cut at the next
  # whitespace, then strip a trailing double/single quote.
  value=${line#*"${key}"=}
  value=${value%%[[:space:]]*}
  value=${value%\"}
  value=${value%\'}
  [ -n "$value" ] || return 1
  printf '%s\n' "$value"
}

# svote_upgrade_autodetect_from_systemd_unit home_cli_set install_cli_set
# Override unset paths from svoted.service (User, home, install dir, wrapper); no-op if unit file absent.
svote_upgrade_autodetect_from_systemd_unit() {
  local home_cli_set="${1:-0}"
  local install_cli_set="${2:-0}"
  local unit_user detected_home detected_install detected_wrapper
  local install_dir_autodetected=0
  local wrapper_path_autodetected=0

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
      if [ "$detected_install" != "$INSTALL_DIR" ]; then
        install_dir_autodetected=1
      fi
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
    wrapper_path_autodetected=1
  fi
  if [ "$install_cli_set" != "1" ] && [ -n "$detected_wrapper" ]; then
    # If ExecStart points at a wrapper, treat that wrapper directory as the canonical install dir.
    # This captures real runtime layout even when unit env is stale/missing.
    detected_install="$(dirname "$detected_wrapper")"
    if [ "$detected_install" != "$INSTALL_DIR" ]; then
      install_dir_autodetected=1
    fi
    INSTALL_DIR="$detected_install"
  fi

  if [ "$install_dir_autodetected" = "1" ] && [ -z "${SVOTE_COSMOVISOR_BIN:-}" ]; then
    # Keep derived binaries aligned with autodetected INSTALL_DIR unless the operator explicitly
    # pinned SVOTE_COSMOVISOR_BIN. Prevents mixed-path state (e.g. INSTALL_DIR in /home but
    # COSMOVISOR_BIN still in /root) that leads to systemd 203/EXEC permission failures.
    COSMOVISOR_BIN="${INSTALL_DIR}/cosmovisor"
    if [ "$wrapper_path_autodetected" = "1" ]; then
      WRAPPER_BIN="${INSTALL_DIR}/svoted-wrapper.sh"
    fi
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

# svote_upgrade_genesis_binary_version
# Print the version string of the staged genesis binary; return 1 if missing or unreadable.
svote_upgrade_genesis_binary_version() {
  [ -x "$GENESIS_BIN" ] || return 1
  local version
  version=$("$GENESIS_BIN" version 2>/dev/null | tr -d '[:space:]' || true)
  [ -n "$version" ] || return 1
  printf '%s\n' "$version"
}

# svote_upgrade_assert_genesis_pre_upgrade target_tag
# Die if the staged genesis binary equals target_tag (genesis must remain the pre-upgrade build so
# cosmovisor does not run the upgrade binary before the trigger height).
svote_upgrade_assert_genesis_pre_upgrade() {
  local target_tag="$1"
  local genesis_version
  local plan_json="" plan_name="" plan_height="" current_height=""
  [ -x "$GENESIS_BIN" ] || svote_upgrade_die "Missing genesis binary: ${GENESIS_BIN}"
  genesis_version=$(svote_upgrade_genesis_binary_version || true)
  if [ -z "$genesis_version" ]; then
    svote_upgrade_die "Could not read version from genesis binary ${GENESIS_BIN}."
  fi
  if [ "$genesis_version" = "$target_tag" ]; then
    plan_json=$(svote_upgrade_query_upgrade_plan || true)
    if [ -n "$plan_json" ] && [ "$plan_json" != "null" ]; then
      plan_name=$(svote_upgrade_parse_plan_name "$plan_json")
      plan_height=$(svote_upgrade_parse_plan_height "$plan_json")
      current_height=$(svote_upgrade_query_block_height || true)
      case "$plan_height" in
        ''|*[!0-9]*) plan_height="" ;;
      esac
      case "$current_height" in
        ''|*[!0-9]*) current_height="" ;;
      esac
      if [ -n "$plan_height" ] && [ -n "$current_height" ] && [ "$current_height" -lt "$plan_height" ]; then
        svote_upgrade_die "BINARY UPDATED BEFORE TRIGGER: genesis binary ${GENESIS_BIN} is already ${target_tag} while chain is below upgrade height (current=${current_height}, planned=${plan_height}, plan=${plan_name:-<unknown>}). Restore a pre-upgrade genesis binary before migrating."
      fi
    fi
    svote_upgrade_die "Genesis binary ${GENESIS_BIN} is already at target tag ${target_tag}; this can cause BINARY UPDATED BEFORE TRIGGER behavior. Restore the pre-upgrade binary before migrating."
  fi
}

# svote_upgrade_runtime_svoted_candidate_ok candidate target_tag
# Return 0 when candidate is an executable svoted binary that is not already target_tag.
# An empty/unreadable version is treated as "not target" so a real running binary is not rejected.
svote_upgrade_runtime_svoted_candidate_ok() {
  local candidate="$1"
  local target_tag="$2"
  local version
  [ -n "$candidate" ] || return 1
  [ -x "$candidate" ] || return 1
  if [ -n "$target_tag" ]; then
    version=$("$candidate" version 2>/dev/null | tr -d '[:space:]' || true)
    [ "$version" = "$target_tag" ] && return 1
  fi
  return 0
}

# svote_upgrade_resolve_runtime_svoted [target_tag]
# Print the path to the current pre-upgrade svoted binary, derived from the validator service itself
# (never from a guessed INSTALL_DIR). Tries in order: SVOTED_BIN declared in the unit (join.sh
# secondaries), the ExecStart svoted binary (direct-mode primaries), the running signer process
# binary (truth), then an already-staged genesis binary. Dies when none resolve. When target_tag is
# given, candidates already at that tag are skipped so an upgraded binary is never chosen as genesis.
svote_upgrade_resolve_runtime_svoted() {
  local target_tag="${1:-}"
  local candidate exec_cmd first_token line cmd pid exe

  # 1. SVOTED_BIN declared in the systemd unit (join.sh records the absolute path here).
  candidate=$(svote_upgrade_systemd_unit_value "SVOTED_BIN" "$SERVICE_PATH" 2>/dev/null || true)
  if svote_upgrade_runtime_svoted_candidate_ok "$candidate" "$target_tag"; then
    printf '%s\n' "$candidate"
    return 0
  fi

  # 2. ExecStart svoted binary; direct-mode primaries run svoted as the first token.
  exec_cmd=$(svote_upgrade_detect_existing_execstart 2>/dev/null || true)
  if [ -n "$exec_cmd" ]; then
    first_token="${exec_cmd%% *}"
    if [ "${first_token##*/}" = "$SVOTE_DAEMON_NAME" ] \
      && svote_upgrade_runtime_svoted_candidate_ok "$first_token" "$target_tag"; then
      printf '%s\n' "$first_token"
      return 0
    fi
  fi

  # 3. The actual running signer process binary, scoped to DAEMON_HOME (cosmovisor supervisor skipped).
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    pid="${line%% *}"
    cmd="${line#* }"
    case "${cmd%% *}" in
      *cosmovisor*) continue ;;
    esac
    case "$pid" in
      ''|*[!0-9]*) continue ;;
    esac
    exe=$(readlink -f "/proc/${pid}/exe" 2>/dev/null || true)
    if svote_upgrade_runtime_svoted_candidate_ok "$exe" "$target_tag"; then
      printf '%s\n' "$exe"
      return 0
    fi
  done < <(svote_upgrade_find_signer_processes 2>/dev/null || true)

  # 4. Reuse an already-staged pre-upgrade genesis binary.
  if svote_upgrade_runtime_svoted_candidate_ok "$GENESIS_BIN" "$target_tag"; then
    printf '%s\n' "$GENESIS_BIN"
    return 0
  fi

  svote_upgrade_die "Could not resolve the current svoted binary from the validator service (checked SVOTED_BIN, ExecStart, the running process, and staged genesis at ${GENESIS_BIN}). Ensure the validator service is configured and running, or stage the genesis binary manually."
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

# svote_upgrade_is_signer_runtime_cmd cmd home inferred_home
# Return 0 when cmd is a signer runtime for home. inferred_home covers a direct
# `svoted start` that relies on the service user's default ~/.svoted path.
svote_upgrade_is_signer_runtime_cmd() {
  local cmd="$1"
  local home="$2"
  local inferred_home="${3:-}"
  local arg previous="" explicit_home="" saw_home=0
  local saw_start=0 saw_run_start=0
  local executable_name=""
  local -a argv=()

  case "$cmd" in
    *update_chain.sh*|*_chain_upgrade_common.sh*) return 1 ;;
  esac

  if ! printf '%s\n' "$cmd" | grep -Eq '(^|[ /])(svoted|cosmovisor)([[:space:]]|$)'; then
    return 1
  fi

  read -r -a argv <<< "$cmd"
  [ "${#argv[@]}" -gt 0 ] || return 1
  executable_name="${argv[0]##*/}"
  for arg in "${argv[@]}"; do
    if [ "$previous" = "--home" ]; then
      explicit_home="$arg"
      saw_home=1
    fi
    case "$arg" in
      --home=*)
        explicit_home="${arg#--home=}"
        saw_home=1
        ;;
      start)
        saw_start=1
        [ "$previous" = "run" ] && saw_run_start=1
        ;;
    esac
    previous="$arg"
  done

  case "$executable_name" in
    cosmovisor) [ "$saw_run_start" = "1" ] || return 1 ;;
    "$SVOTE_DAEMON_NAME") [ "$saw_start" = "1" ] || return 1 ;;
    *) return 1 ;;
  esac

  if [ "$saw_home" = "1" ]; then
    [ "$explicit_home" = "$home" ]
    return
  fi
  [ -n "$inferred_home" ] && [ "$inferred_home" = "$home" ]
}

# svote_upgrade_process_env_value pid key
# Print a process environment value from procfs, or return 1 when unavailable.
svote_upgrade_process_env_value() {
  local pid="$1"
  local key="$2"
  local proc_root="${SVOTE_PROC_ROOT:-/proc}"
  local value

  [ -r "${proc_root}/${pid}/environ" ] || return 1
  value=$(tr '\0' '\n' < "${proc_root}/${pid}/environ" 2>/dev/null \
    | sed -n "s/^${key}=//p" | tail -n 1)
  [ -n "$value" ] || return 1
  printf '%s\n' "$value"
}

# svote_upgrade_process_default_daemon_home pid
# Infer the default ~/.svoted path for a process owner from procfs and /etc/passwd.
svote_upgrade_process_default_daemon_home() {
  local pid="$1"
  local proc_root="${SVOTE_PROC_ROOT:-/proc}"
  local uid user_home

  [ -r "${proc_root}/${pid}/status" ] || return 1
  uid=$(awk '/^Uid:/{print $2; exit}' "${proc_root}/${pid}/status" 2>/dev/null || true)
  case "$uid" in
    ''|*[!0-9]*) return 1 ;;
  esac

  if command -v getent >/dev/null 2>&1; then
    user_home=$(getent passwd "$uid" 2>/dev/null | awk -F: 'NR == 1 { print $6 }')
  else
    user_home=$(awk -F: -v uid="$uid" '$3 == uid { print $6; exit }' /etc/passwd 2>/dev/null || true)
  fi
  [ -n "$user_home" ] || return 1
  printf '%s/.svoted\n' "${user_home%/}"
}

# svote_upgrade_process_executable_name pid
# Print the executable basename for pid from procfs.
svote_upgrade_process_executable_name() {
  local pid="$1"
  local proc_root="${SVOTE_PROC_ROOT:-/proc}"
  local executable
  executable=$(readlink "${proc_root}/${pid}/exe" 2>/dev/null || true)
  [ -n "$executable" ] || return 1
  executable="${executable% (deleted)}"
  printf '%s\n' "${executable##*/}"
}

# svote_upgrade_process_cmdline pid
# Print a process command line from procfs with NUL separators converted to spaces.
svote_upgrade_process_cmdline() {
  local pid="$1"
  local proc_root="${SVOTE_PROC_ROOT:-/proc}"
  [ -r "${proc_root}/${pid}/cmdline" ] || return 1
  tr '\0' ' ' < "${proc_root}/${pid}/cmdline" 2>/dev/null | sed 's/[[:space:]]*$//'
}

# svote_upgrade_find_signer_processes
# Print pid/cmdline for live signer processes scoped to DAEMON_HOME.
svote_upgrade_find_signer_processes() {
  local proc_root="${SVOTE_PROC_ROOT:-/proc}"
  local proc_dir pid executable_name cmd inferred_home

  for proc_dir in "${proc_root}"/[0-9]*; do
    [ -d "$proc_dir" ] || continue
    pid="${proc_dir##*/}"
    if svote_upgrade_is_upgrade_invocation_pid "$pid"; then
      continue
    fi
    executable_name=$(svote_upgrade_process_executable_name "$pid" 2>/dev/null || true)
    case "$executable_name" in
      "$SVOTE_DAEMON_NAME"|cosmovisor) ;;
      *) continue ;;
    esac
    cmd=$(svote_upgrade_process_cmdline "$pid" 2>/dev/null || true)
    [ -n "$cmd" ] || continue

    inferred_home=$(svote_upgrade_process_env_value "$pid" "DAEMON_HOME" 2>/dev/null || true)
    if [ -z "$inferred_home" ]; then
      inferred_home=$(svote_upgrade_process_env_value "$pid" "SVOTE_HOME" 2>/dev/null || true)
    fi
    if [ -z "$inferred_home" ]; then
      inferred_home=$(svote_upgrade_process_env_value "$pid" "HOME" 2>/dev/null || true)
      [ -z "$inferred_home" ] || inferred_home="${inferred_home%/}/.svoted"
    fi
    if [ -z "$inferred_home" ]; then
      inferred_home=$(svote_upgrade_process_default_daemon_home "$pid" 2>/dev/null || true)
    fi
    if svote_upgrade_is_signer_runtime_cmd "$cmd" "$DAEMON_HOME" "$inferred_home"; then
      printf '%s %s\n' "$pid" "$cmd"
    fi
  done
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

# svote_upgrade_assert_validator_service_stopped
# Die unless systemd reports an inactive/failed unit and no signer process remains for DAEMON_HOME.
svote_upgrade_assert_validator_service_stopped() {
  local active_state
  active_state=$(systemctl show "$SERVICE_NAME" -p ActiveState --value 2>/dev/null || true)
  case "$active_state" in
    inactive|failed) ;;
    *) svote_upgrade_die "${SERVICE_NAME} is not stopped after stop request (state=${active_state:-<unknown>})." ;;
  esac
  svote_upgrade_assert_no_signer_processes
}

# svote_upgrade_stop_validator_service
# Unconditionally stop SERVICE_NAME, including an activating unit, then enforce the signer-stop gate.
svote_upgrade_stop_validator_service() {
  svote_upgrade_log "Stopping ${SERVICE_NAME}"
  systemctl stop "$SERVICE_NAME"
  svote_upgrade_assert_validator_service_stopped
}

# svote_upgrade_pid_in_control_group pid control_group
# Return 0 when procfs places pid in the service cgroup (or one of its children).
svote_upgrade_pid_in_control_group() {
  local pid="$1"
  local control_group="$2"
  local proc_root="${SVOTE_PROC_ROOT:-/proc}"
  [ -n "$control_group" ] || return 1
  [ -r "${proc_root}/${pid}/cgroup" ] || return 1
  awk -F: -v expected="$control_group" '
    $3 == expected || index($3, expected "/") == 1 { found = 1 }
    END { exit(found ? 0 : 1) }
  ' "${proc_root}/${pid}/cgroup"
}

# svote_upgrade_check_single_managed_signer
# Return 0 only for one Cosmovisor supervisor and one svoted child in SERVICE_NAME's cgroup.
svote_upgrade_check_single_managed_signer() {
  local main_pid control_group line pid executable_name main_cmd main_is_direct=0
  local cosmovisor_count=0 svoted_count=0
  local running=""
  SVOTE_MANAGED_SIGNER_ERROR=""

  main_pid=$(systemctl show "$SERVICE_NAME" -p MainPID --value 2>/dev/null || true)
  control_group=$(systemctl show "$SERVICE_NAME" -p ControlGroup --value 2>/dev/null || true)
  case "$main_pid" in
    ''|0|*[!0-9]*)
      SVOTE_MANAGED_SIGNER_ERROR="${SERVICE_NAME} has no live MainPID"
      return 1
      ;;
  esac
  if [ -z "$control_group" ]; then
    SVOTE_MANAGED_SIGNER_ERROR="${SERVICE_NAME} has no systemd ControlGroup"
    return 1
  fi
  executable_name=$(svote_upgrade_process_executable_name "$main_pid" 2>/dev/null || true)
  main_cmd=$(svote_upgrade_process_cmdline "$main_pid" 2>/dev/null || true)
  if [ "$executable_name" = "cosmovisor" ]; then
    main_is_direct=1
  elif ! printf '%s\n' "$main_cmd" | grep -Eq '(^|[ /])svoted-wrapper\.sh([[:space:]]|$)'; then
    SVOTE_MANAGED_SIGNER_ERROR="${SERVICE_NAME} MainPID ${main_pid} is not Cosmovisor or svoted-wrapper.sh"
    return 1
  fi

  running=$(svote_upgrade_find_signer_processes || true)
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    pid="${line%% *}"
    executable_name=$(svote_upgrade_process_executable_name "$pid" 2>/dev/null || true)
    if ! svote_upgrade_pid_in_control_group "$pid" "$control_group"; then
      SVOTE_MANAGED_SIGNER_ERROR="unmanaged signer process outside ${SERVICE_NAME} cgroup: ${line}"
      return 1
    fi
    case "$executable_name" in
      cosmovisor)
        cosmovisor_count=$((cosmovisor_count + 1))
        if [ "$main_is_direct" = "1" ] && [ "$pid" != "$main_pid" ]; then
          SVOTE_MANAGED_SIGNER_ERROR="Cosmovisor pid ${pid} is not ${SERVICE_NAME} MainPID ${main_pid}"
          return 1
        fi
        ;;
      "$SVOTE_DAEMON_NAME") svoted_count=$((svoted_count + 1)) ;;
    esac
  done <<< "$running"

  if [ "$cosmovisor_count" -ne 1 ] || [ "$svoted_count" -ne 1 ]; then
    SVOTE_MANAGED_SIGNER_ERROR="expected one Cosmovisor supervisor and one svoted child; found cosmovisor=${cosmovisor_count}, svoted=${svoted_count}"
    return 1
  fi
  return 0
}

# svote_upgrade_assert_no_unmanaged_signers
# Die if any signer for DAEMON_HOME is outside SERVICE_NAME's systemd cgroup.
svote_upgrade_assert_no_unmanaged_signers() {
  local control_group line pid
  control_group=$(systemctl show "$SERVICE_NAME" -p ControlGroup --value 2>/dev/null || true)
  [ -n "$control_group" ] || svote_upgrade_die "${SERVICE_NAME} has no systemd ControlGroup."
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    pid="${line%% *}"
    if ! svote_upgrade_pid_in_control_group "$pid" "$control_group"; then
      svote_upgrade_die "Unmanaged signer process outside ${SERVICE_NAME} cgroup: ${line}"
    fi
  done < <(svote_upgrade_find_signer_processes || true)
}

# svote_upgrade_assert_single_managed_signer
# Die unless exactly one Cosmovisor-managed validator signer is running.
svote_upgrade_assert_single_managed_signer() {
  if ! svote_upgrade_check_single_managed_signer; then
    svote_upgrade_die "Signer supervision check failed: ${SVOTE_MANAGED_SIGNER_ERROR}"
  fi
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

# svote_upgrade_chain_api_get api_path
# Fetch one JSON endpoint from the configured HTTPS chain API.
svote_upgrade_chain_api_get() {
  local api_path="$1"
  [ -n "${CHAIN_API:-}" ] || return 1
  case "$CHAIN_API" in
    https://*) ;;
    http://127.0.0.1:*|http://localhost:*) ;;
    *) svote_upgrade_die "--chain-api must use HTTPS (localhost HTTP is allowed for tests)." ;;
  esac
  curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 10 --max-time 30 \
    "${CHAIN_API}${api_path}"
}

# svote_upgrade_validate_chain_api
# Require the remote API chain ID to match the validator's local genesis.
svote_upgrade_validate_chain_api() {
  local node_info local_chain_id remote_chain_id
  [ -n "${CHAIN_API:-}" ] || return 1
  local_chain_id=$(svote_upgrade_derive_chain_id_from_home "$DAEMON_HOME" || true)
  [ -n "$local_chain_id" ] || svote_upgrade_die "Could not derive chain ID from ${DAEMON_HOME}/config/genesis.json."
  node_info=$(svote_upgrade_chain_api_get "/cosmos/base/tendermint/v1beta1/node_info") \
    || svote_upgrade_die "Could not reach chain API ${CHAIN_API}."
  remote_chain_id=$(printf '%s\n' "$node_info" \
    | jq -r '.default_node_info.network // empty' 2>/dev/null || true)
  [ -n "$remote_chain_id" ] || svote_upgrade_die "Chain API ${CHAIN_API} returned no network ID."
  if [ "$remote_chain_id" != "$local_chain_id" ]; then
    svote_upgrade_die "Chain API network mismatch: local=${local_chain_id}, remote=${remote_chain_id}."
  fi
}

# svote_upgrade_resolve_query_svoted [allow_empty]
# Resolve a usable svoted binary for status/plan queries. Tries runtime/service-derived locations
# before fallback paths. When allow_empty=1, return 1 instead of exiting if none resolve.
svote_upgrade_resolve_query_svoted() {
  local allow_empty="${1:-0}"
  local candidate resolved=""
  local attempted=""

  append_attempt() {
    local value="$1"
    [ -n "$value" ] || return 0
    if [ -n "$attempted" ]; then
      attempted="${attempted}, ${value}"
    else
      attempted="$value"
    fi
  }

  maybe_select_candidate() {
    local value="$1"
    [ -n "$value" ] || return 1
    append_attempt "$value"
    [ -x "$value" ] || return 1
    resolved="$value"
    return 0
  }

  candidate=$(svote_upgrade_resolve_runtime_svoted 2>/dev/null || true)
  maybe_select_candidate "$candidate" || true

  if [ -z "$resolved" ]; then
    maybe_select_candidate "${INSTALL_DIR}/${SVOTE_DAEMON_NAME}" || true
  fi

  if [ -z "$resolved" ]; then
    candidate=$(svote_upgrade_systemd_unit_value "SVOTED_BIN" "$SERVICE_PATH" 2>/dev/null || true)
    maybe_select_candidate "$candidate" || true
  fi

  if [ -z "$resolved" ]; then
    maybe_select_candidate "${GENESIS_BIN}" || true
  fi

  if [ -z "$resolved" ]; then
    candidate="$(command -v "$SVOTE_DAEMON_NAME" 2>/dev/null || true)"
    maybe_select_candidate "$candidate" || true
  fi

  if [ -z "$resolved" ]; then
    if [ "$allow_empty" = "1" ]; then
      return 1
    fi
    svote_upgrade_die "No svoted binary available to query upgrade plan/status. Tried: ${attempted:-<none>}."
  fi

  printf '%s\n' "$resolved"
}

# svote_upgrade_query_upgrade_plan
# Query on-chain upgrade plan as JSON; return empty on "no plan"; die on RPC/parse errors.
svote_upgrade_query_upgrade_plan() {
  local query_bin plan_json query_err local_error=""
  query_bin=$(svote_upgrade_resolve_query_svoted 1 2>/dev/null || true)
  if [ -n "$query_bin" ]; then
    query_err=$(mktemp)
    if plan_json=$("$query_bin" query upgrade plan --home "$DAEMON_HOME" --output json 2>"$query_err"); then
      rm -f "$query_err"
      if [ -z "$plan_json" ] || [ "$plan_json" = "null" ]; then
        return 0
      fi
      if ! printf '%s\n' "$plan_json" | jq empty >/dev/null 2>&1; then
        svote_upgrade_die "Upgrade plan query returned invalid JSON."
      fi
      printf '%s\n' "$plan_json"
      return 0
    fi
    if grep -qi 'no upgrade plan\|not found\|no plan' "$query_err" 2>/dev/null; then
      rm -f "$query_err"
      return 0
    fi
    local_error=$(tr -d '\n' < "$query_err")
    rm -f "$query_err"
  fi

  if [ -n "${CHAIN_API:-}" ]; then
    svote_upgrade_validate_chain_api
    plan_json=$(svote_upgrade_chain_api_get "/cosmos/upgrade/v1beta1/current_plan") \
      || svote_upgrade_die "Failed to query current plan from ${CHAIN_API}."
    if ! printf '%s\n' "$plan_json" | jq empty >/dev/null 2>&1; then
      svote_upgrade_die "Chain API returned invalid current-plan JSON."
    fi
    if [ "$(printf '%s\n' "$plan_json" | jq -r '.plan // empty')" = "" ]; then
      return 0
    fi
    printf '%s\n' "$plan_json"
    return 0
  fi

  if [ -n "$local_error" ]; then
    svote_upgrade_die "Failed to query upgrade plan: ${local_error}. Re-run with --chain-api when the local RPC is offline."
  fi
  svote_upgrade_die "No svoted binary or chain API is available to query the upgrade plan."
}

# svote_upgrade_query_applied_plan_height plan_name
# Print the applied height for plan_name, using the local RPC with chain API fallback.
svote_upgrade_query_applied_plan_height() {
  local plan_name="$1"
  local query_bin result query_err local_error="" height
  case "$plan_name" in
    ''|*[!A-Za-z0-9._-]*) svote_upgrade_die "Unsafe upgrade plan name: ${plan_name:-<empty>}." ;;
  esac

  query_bin=$(svote_upgrade_resolve_query_svoted 1 2>/dev/null || true)
  if [ -n "$query_bin" ]; then
    query_err=$(mktemp)
    if result=$("$query_bin" query upgrade applied "$plan_name" --home "$DAEMON_HOME" --output json 2>"$query_err"); then
      rm -f "$query_err"
      height=$(printf '%s\n' "$result" | jq -r '.height // empty' 2>/dev/null || true)
      case "$height" in
        ''|0|*[!0-9]*) svote_upgrade_die "Applied-plan query returned an invalid height for ${plan_name}." ;;
      esac
      printf '%s\n' "$height"
      return 0
    fi
    local_error=$(tr -d '\n' < "$query_err")
    rm -f "$query_err"
  fi

  if [ -n "${CHAIN_API:-}" ]; then
    svote_upgrade_validate_chain_api
    result=$(svote_upgrade_chain_api_get "/cosmos/upgrade/v1beta1/applied_plan/${plan_name}") \
      || svote_upgrade_die "Chain API does not confirm applied plan ${plan_name}."
    height=$(printf '%s\n' "$result" | jq -r '.height // empty' 2>/dev/null || true)
    case "$height" in
      ''|0|*[!0-9]*) svote_upgrade_die "Chain API returned an invalid applied height for ${plan_name}." ;;
    esac
    printf '%s\n' "$height"
    return 0
  fi

  svote_upgrade_die "Could not confirm applied plan ${plan_name}${local_error:+: ${local_error}}. Re-run with --chain-api when the local RPC is offline."
}

# svote_upgrade_validate_active_plan_recovery expected_name
# Require the local upgrade marker to match a plan that the chain reports applied at the same height.
svote_upgrade_validate_active_plan_recovery() {
  local expected_name="$1"
  local upgrade_info="${DAEMON_HOME}/data/upgrade-info.json"
  local parsed marker_name marker_height applied_height

  case "$expected_name" in
    ''|*[!A-Za-z0-9._-]*) svote_upgrade_die "Unsafe upgrade plan name: ${expected_name:-<empty>}." ;;
  esac
  parsed=$(svote_upgrade_read_upgrade_info) \
    || svote_upgrade_die "Active-upgrade recovery requires a valid ${upgrade_info}."
  marker_name="${parsed%%$'\t'*}"
  marker_height="${parsed#*$'\t'}"
  if [ "$marker_name" != "$expected_name" ]; then
    svote_upgrade_die "Upgrade marker mismatch: expected ${expected_name}, local marker is ${marker_name}."
  fi

  applied_height=$(svote_upgrade_query_applied_plan_height "$marker_name")
  if [ "$applied_height" != "$marker_height" ]; then
    svote_upgrade_die "Refusing active-upgrade recovery: ${marker_name} marker height ${marker_height} does not match applied height ${applied_height}."
  fi

  SVOTE_RECOVERY_PLAN_NAME="$marker_name"
  SVOTE_RECOVERY_PLAN_HEIGHT="$marker_height"
  export SVOTE_RECOVERY_PLAN_NAME SVOTE_RECOVERY_PLAN_HEIGHT
  svote_upgrade_log "Confirmed active applied-plan marker ${marker_name} at height ${marker_height}."
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
    if [ "$allow_no_plan" = "1" ]; then
      svote_upgrade_warn "No upgrade plan name present in chain response (--allow-no-plan set)."
      return 0
    fi
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
  local query_bin height result
  query_bin=$(svote_upgrade_resolve_query_svoted 1 2>/dev/null || true)
  if [ -n "$query_bin" ]; then
    height=$("$query_bin" status --home "$DAEMON_HOME" 2>/dev/null \
      | jq -r '.sync_info.latest_block_height // empty' 2>/dev/null || true)
    case "$height" in
      ''|*[!0-9]*) ;;
      *) printf '%s\n' "$height"; return 0 ;;
    esac
  fi

  [ -n "${CHAIN_API:-}" ] || return 1
  svote_upgrade_validate_chain_api
  result=$(svote_upgrade_chain_api_get "/cosmos/base/tendermint/v1beta1/blocks/latest") || return 1
  height=$(printf '%s\n' "$result" | jq -r '.block.header.height // empty' 2>/dev/null || true)
  case "$height" in
    ''|*[!0-9]*) return 1 ;;
  esac
  printf '%s\n' "$height"
}

# svote_upgrade_read_upgrade_info
# Print NAME<TAB>HEIGHT from data/upgrade-info.json, or return 1 if malformed.
svote_upgrade_read_upgrade_info() {
  local upgrade_info="${DAEMON_HOME}/data/upgrade-info.json"
  local name height
  [ -f "$upgrade_info" ] || return 1
  name=$(jq -er '.name | select(type == "string" and length > 0)' "$upgrade_info" 2>/dev/null || true)
  height=$(jq -er '.height | tostring | select(test("^[0-9]+$"))' "$upgrade_info" 2>/dev/null || true)
  case "$name" in
    ''|*[!A-Za-z0-9._-]*) return 1 ;;
  esac
  case "$height" in
    ''|0|*[!0-9]*) return 1 ;;
  esac
  printf '%s\t%s\n' "$name" "$height"
}

# svote_upgrade_prepare_stale_plan_recovery current_plan
# Prove a non-current upgrade-info marker was already applied before any service stop.
svote_upgrade_prepare_stale_plan_recovery() {
  local current_plan="$1"
  local upgrade_info="${DAEMON_HOME}/data/upgrade-info.json"
  local parsed marker_name marker_height applied_height
  SVOTE_STALE_PLAN_NAME=""
  SVOTE_STALE_PLAN_HEIGHT=""

  [ -e "$upgrade_info" ] || return 0
  parsed=$(svote_upgrade_read_upgrade_info) \
    || svote_upgrade_die "Refusing to alter malformed ${upgrade_info}."
  marker_name="${parsed%%$'\t'*}"
  marker_height="${parsed#*$'\t'}"

  if [ "$marker_name" = "$current_plan" ]; then
    svote_upgrade_die "${upgrade_info} contains the current plan ${current_plan}; refusing stale-plan recovery."
  fi
  applied_height=$(svote_upgrade_query_applied_plan_height "$marker_name")
  if [ "$applied_height" != "$marker_height" ]; then
    svote_upgrade_die "Refusing stale-plan recovery: ${marker_name} marker height ${marker_height} does not match applied height ${applied_height}."
  fi

  SVOTE_STALE_PLAN_NAME="$marker_name"
  SVOTE_STALE_PLAN_HEIGHT="$marker_height"
  export SVOTE_STALE_PLAN_NAME SVOTE_STALE_PLAN_HEIGHT
  svote_upgrade_log "Confirmed stale applied-plan marker ${marker_name} at height ${marker_height}."
}

# svote_upgrade_archive_stale_plan_marker
# With the signer stopped, point current at genesis and archive the proven stale marker.
svote_upgrade_archive_stale_plan_marker() {
  local marker_name="${SVOTE_STALE_PLAN_NAME:-}"
  local marker_height="${SVOTE_STALE_PLAN_HEIGHT:-}"
  local upgrade_info="${DAEMON_HOME}/data/upgrade-info.json"
  local current_link="${COSMVISOR_ROOT}/current"
  local current_target="" expected_genesis="${COSMVISOR_ROOT}/genesis"
  local expected_stale="${COSMVISOR_ROOT}/upgrades/${marker_name}"
  local parsed recovery_dir archive_path

  [ -n "$marker_name" ] || return 0
  parsed=$(svote_upgrade_read_upgrade_info) \
    || svote_upgrade_die "Stale upgrade marker changed before recovery: ${upgrade_info}."
  if [ "$parsed" != "${marker_name}"$'\t'"${marker_height}" ]; then
    svote_upgrade_die "Stale upgrade marker changed before recovery: expected ${marker_name}/${marker_height}."
  fi
  [ -x "$GENESIS_BIN" ] || svote_upgrade_die "Cannot recover stale marker without genesis binary ${GENESIS_BIN}."

  if [ -e "$current_link" ] && [ ! -L "$current_link" ]; then
    svote_upgrade_die "Cosmovisor current path is not a symlink: ${current_link}."
  fi
  if [ -L "$current_link" ]; then
    current_target=$(readlink "$current_link" 2>/dev/null || true)
    case "$current_target" in
      "$expected_genesis"|genesis|"$expected_stale"|"upgrades/${marker_name}") ;;
      *) svote_upgrade_die "Cosmovisor current points to unexpected target ${current_target:-<unreadable>}; refusing recovery." ;;
    esac
  fi

  install -d -m 0700 "$COSMVISOR_ROOT"
  ln -sfn "$expected_genesis" "$current_link"

  recovery_dir="${COSMVISOR_ROOT}/recovery"
  install -d -m 0700 "$recovery_dir"
  archive_path="${recovery_dir}/upgrade-info.${marker_name}.${marker_height}.$(date -u +%Y%m%dT%H%M%SZ).$$.json"
  mv "$upgrade_info" "$archive_path"
  chmod 0600 "$archive_path"
  SVOTE_STALE_UPGRADE_INFO_ARCHIVE="$archive_path"
  export SVOTE_STALE_UPGRADE_INFO_ARCHIVE
  svote_upgrade_log "Archived stale applied-plan marker at ${archive_path}."
}

# svote_upgrade_validate_recovery_current_link target_plan
# Accept only genesis, the target plan, or an earlier plan whose applied height the chain confirms.
svote_upgrade_validate_recovery_current_link() {
  local target_plan="$1"
  local current_link="${COSMVISOR_ROOT}/current"
  local current_target current_plan current_bin applied_height

  case "$target_plan" in
    ''|*[!A-Za-z0-9._-]*) svote_upgrade_die "Unsafe upgrade plan name: ${target_plan:-<empty>}." ;;
  esac
  [ -L "$current_link" ] \
    || svote_upgrade_die "Active-upgrade recovery requires ${current_link} to be a symlink."
  current_target=$(readlink "$current_link" 2>/dev/null || true)
  case "$current_target" in
    "$COSMVISOR_ROOT/genesis"|genesis)
      [ -x "$GENESIS_BIN" ] \
        || svote_upgrade_die "Cosmovisor current points to genesis, but ${GENESIS_BIN} is missing."
      SVOTE_RECOVERY_CURRENT_TARGET="$current_target"
      export SVOTE_RECOVERY_CURRENT_TARGET
      svote_upgrade_log "Cosmovisor current points to genesis."
      return 0
      ;;
    "$COSMVISOR_ROOT/upgrades/"*)
      current_plan="${current_target#"$COSMVISOR_ROOT/upgrades/"}"
      ;;
    upgrades/*)
      current_plan="${current_target#upgrades/}"
      ;;
    *)
      svote_upgrade_die "Cosmovisor current points outside the supported layout: ${current_target:-<unreadable>}."
      ;;
  esac

  case "$current_plan" in
    ''|*[!A-Za-z0-9._-]*) svote_upgrade_die "Unsafe current upgrade target: ${current_target}." ;;
  esac
  current_bin="$(svote_upgrade_upgrade_bin_path "$current_plan")"
  [ -x "$current_bin" ] \
    || svote_upgrade_die "Cosmovisor current upgrade binary is missing: ${current_bin}."
  if [ "$current_plan" = "$target_plan" ]; then
    SVOTE_RECOVERY_CURRENT_TARGET="$current_target"
    export SVOTE_RECOVERY_CURRENT_TARGET
    svote_upgrade_log "Cosmovisor current already points to target plan ${target_plan}."
    return 0
  fi

  applied_height=$(svote_upgrade_query_applied_plan_height "$current_plan")
  SVOTE_RECOVERY_CURRENT_TARGET="$current_target"
  export SVOTE_RECOVERY_CURRENT_TARGET
  svote_upgrade_log "Confirmed current plan ${current_plan} was previously applied at height ${applied_height}."
}

# svote_upgrade_activate_recovery_plan target_plan expected_tag
# With the validator stopped, atomically select the verified target upgrade directory.
svote_upgrade_activate_recovery_plan() {
  local target_plan="$1"
  local expected_tag="$2"
  local current_link="${COSMVISOR_ROOT}/current"
  local target_dir target_bin active_state selected_current parsed expected_marker

  case "$target_plan" in
    ''|*[!A-Za-z0-9._-]*) svote_upgrade_die "Unsafe upgrade plan name: ${target_plan:-<empty>}." ;;
  esac
  target_dir="${COSMVISOR_ROOT}/upgrades/${target_plan}"
  target_bin="$(svote_upgrade_upgrade_bin_path "$target_plan")"
  active_state=$(systemctl show "$SERVICE_NAME" -p ActiveState --value 2>/dev/null || true)
  case "$active_state" in
    inactive|failed) ;;
    *) svote_upgrade_die "Refusing to change Cosmovisor current while ${SERVICE_NAME} is ${active_state:-<unknown>}." ;;
  esac
  svote_upgrade_assert_no_signer_processes
  [ -n "${SVOTE_RECOVERY_CURRENT_TARGET:-}" ] \
    || svote_upgrade_die "Recovery current target was not validated before stopping ${SERVICE_NAME}."
  selected_current=$(readlink "$current_link" 2>/dev/null || true)
  if [ "$selected_current" != "$SVOTE_RECOVERY_CURRENT_TARGET" ]; then
    svote_upgrade_die "Cosmovisor current changed during recovery: expected ${SVOTE_RECOVERY_CURRENT_TARGET}, found ${selected_current:-<unreadable>}."
  fi
  parsed=$(svote_upgrade_read_upgrade_info) \
    || svote_upgrade_die "Upgrade marker changed or became invalid during recovery."
  expected_marker="${SVOTE_RECOVERY_PLAN_NAME:-}"$'\t'"${SVOTE_RECOVERY_PLAN_HEIGHT:-}"
  if [ "$parsed" != "$expected_marker" ]; then
    svote_upgrade_die "Upgrade marker changed during recovery: expected ${SVOTE_RECOVERY_PLAN_NAME:-<unset>}/${SVOTE_RECOVERY_PLAN_HEIGHT:-<unset>}."
  fi
  svote_upgrade_verify_binary_tag "$target_bin" "$expected_tag"

  ln -sfn "$target_dir" "$current_link"
  selected_current=$(readlink "$current_link" 2>/dev/null || true)
  if [ "$selected_current" != "$target_dir" ]; then
    svote_upgrade_die "Failed to point Cosmovisor current at ${target_dir}."
  fi
  svote_upgrade_log "Selected Cosmovisor upgrade plan ${target_plan}."
}

# svote_upgrade_assert_current_upgrade target_plan expected_tag
# Require Cosmovisor current to resolve to the target directory with the expected binary version.
svote_upgrade_assert_current_upgrade() {
  local target_plan="$1"
  local expected_tag="$2"
  local current_link="${COSMVISOR_ROOT}/current"
  local target_dir target_bin selected_current

  case "$target_plan" in
    ''|*[!A-Za-z0-9._-]*) svote_upgrade_die "Unsafe upgrade plan name: ${target_plan:-<empty>}." ;;
  esac
  target_dir="${COSMVISOR_ROOT}/upgrades/${target_plan}"
  target_bin="$(svote_upgrade_upgrade_bin_path "$target_plan")"
  selected_current=$(readlink "$current_link" 2>/dev/null || true)
  case "$selected_current" in
    "$target_dir"|"upgrades/${target_plan}") ;;
    *) svote_upgrade_die "Cosmovisor current points to ${selected_current:-<unreadable>}, expected ${target_dir}." ;;
  esac
  svote_upgrade_verify_binary_tag "$target_bin" "$expected_tag"
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
    local genesis_version
    genesis_version=$(svote_upgrade_genesis_binary_version || true)
    if [ -z "$genesis_version" ]; then
      svote_upgrade_checklist_line FAIL "Genesis binary version unreadable (${GENESIS_BIN})"
      staging_failures=$((staging_failures + 1))
    elif [ "$genesis_version" = "$expected_tag" ]; then
      svote_upgrade_checklist_line FAIL "Genesis binary must stay pre-upgrade but equals target ${expected_tag} (${GENESIS_BIN})"
      staging_failures=$((staging_failures + 1))
    else
      svote_upgrade_checklist_line PASS "Genesis binary is pre-upgrade build (${genesis_version})"
    fi
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
    local effective_mode effective_daemon_home effective_svote_home effective_exec
    local effective_allow_download effective_must_checksum
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

    effective_allow_download=$(svote_upgrade_systemd_effective_env_value "DAEMON_ALLOW_DOWNLOAD_BINARIES" || true)
    if [ "$effective_allow_download" = "true" ]; then
      svote_upgrade_checklist_line PASS "Cosmovisor binary auto-download is enabled"
    else
      svote_upgrade_checklist_line FAIL "systemd effective DAEMON_ALLOW_DOWNLOAD_BINARIES is ${effective_allow_download:-<unset>} (expected true)"
      service_failures=$((service_failures + 1))
    fi

    effective_must_checksum=$(svote_upgrade_systemd_effective_env_value "DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM" || true)
    if [ "$effective_must_checksum" = "true" ]; then
      svote_upgrade_checklist_line PASS "Cosmovisor requires download checksums"
    else
      svote_upgrade_checklist_line FAIL "systemd effective DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM is ${effective_must_checksum:-<unset>} (expected true)"
      service_failures=$((service_failures + 1))
    fi

    effective_exec=$(svote_upgrade_systemd_effective_execstart || true)
    if { [ -n "$effective_exec" ] && [ "${effective_exec##*/}" = "cosmovisor" ] \
      && svote_upgrade_systemd_effective_execstart_is_cosmovisor_run_start; } \
      || { [ -n "$effective_exec" ] && [ "${effective_exec##*/}" = "svoted-wrapper.sh" ]; }; then
      svote_upgrade_checklist_line PASS "systemd effective ExecStart is Cosmovisor-managed"
    else
      svote_upgrade_checklist_line FAIL "systemd effective ExecStart is ${effective_exec:-<unset>} (expected cosmovisor run start or svoted-wrapper.sh)"
      service_failures=$((service_failures + 1))
    fi

    if svote_upgrade_has_cosmovisor_runtime_for_home; then
      svote_upgrade_checklist_line PASS "cosmovisor runtime process is active for ${DAEMON_HOME}"
    else
      svote_upgrade_checklist_line FAIL "cosmovisor runtime process missing for ${DAEMON_HOME}"
      service_failures=$((service_failures + 1))
    fi

    if svote_upgrade_check_single_managed_signer; then
      svote_upgrade_checklist_line PASS "exactly one Cosmovisor-managed svoted signer is active"
    else
      svote_upgrade_checklist_line FAIL "signer supervision: ${SVOTE_MANAGED_SIGNER_ERROR}"
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
  local spaced_home_arg="--home ${home}"
  local equals_home_arg="--home=${home}"

  case "$exec_start_cmd" in
    *svoted-wrapper*|*cosmovisor*) return 0 ;;
  esac
  case "$exec_start_cmd" in
    *svoted*) ;;
    *) return 0 ;;
  esac

  if [[ "$exec_start_cmd" == *"$spaced_home_arg"* ]]; then
    remainder="${exec_start_cmd#*"$spaced_home_arg"}"
  elif [[ "$exec_start_cmd" == *"$equals_home_arg"* ]]; then
    remainder="${exec_start_cmd#*"$equals_home_arg"}"
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
  local key_prefix="${key}="
  local token value=""

  for token in $env_blob; do
    token="${token#\"}"
    token="${token%\"}"
    case "$token" in
      "$key_prefix"*)
        value="${token#"$key_prefix"}"
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
# Return 0 when a running cosmovisor process is executing run start for DAEMON_HOME.
svote_upgrade_has_cosmovisor_runtime_for_home() {
  pgrep -af "cosmovisor" 2>/dev/null \
    | grep -E '(^| )[^ ]*cosmovisor( |$)' \
    | grep -F -- "run start" \
    | grep -F -- "$DAEMON_HOME" >/dev/null 2>&1
}

# svote_upgrade_systemd_effective_execstart_is_cosmovisor_run_start
# Return 0 when systemd effective ExecStart argv includes cosmovisor run start.
svote_upgrade_systemd_effective_execstart_is_cosmovisor_run_start() {
  local exec_blob
  exec_blob=$(systemctl show "$SERVICE_NAME" -p ExecStart --value 2>/dev/null || true)
  [ -n "$exec_blob" ] || return 1
  [ "$exec_blob" != "[]" ] || return 1
  printf '%s\n' "$exec_blob" | grep -Eq 'argv\[\]=[^;]*cosmovisor([^;]*) run start([ ;]|$)'
}

# svote_upgrade_assert_cosmovisor_service_config
# Die unless systemd is configured to run Cosmovisor directly for this validator.
svote_upgrade_assert_cosmovisor_service_config() {
  local mode effective_exec

  mode=$(svote_upgrade_systemd_effective_env_value "SVOTE_UPGRADE_MODE" || true)
  if [ "$mode" != "cosmovisor" ]; then
    svote_upgrade_die "Effective SVOTE_UPGRADE_MODE is ${mode:-<unset>} (expected cosmovisor)."
  fi

  effective_exec=$(svote_upgrade_systemd_effective_execstart || true)
  if [ -n "$effective_exec" ] && [ "${effective_exec##*/}" != "cosmovisor" ]; then
    svote_upgrade_die "Effective ExecStart is ${effective_exec} (expected cosmovisor binary)."
  fi
  if ! svote_upgrade_systemd_effective_execstart_is_cosmovisor_run_start; then
    svote_upgrade_die "Effective ExecStart is not a cosmovisor run start command."
  fi
}

# svote_upgrade_assert_cosmovisor_runtime
# Die unless the service is configured for Cosmovisor and its runtime process is live.
svote_upgrade_assert_cosmovisor_runtime() {
  local main_pid main_cmd

  svote_upgrade_assert_cosmovisor_service_config

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

# svote_upgrade_configure_autodownload_dropin
# Enable Cosmovisor downloads while requiring checksums in a systemd drop-in.
svote_upgrade_configure_autodownload_dropin() {
  local dropin_dir
  dropin_dir="$(dirname "$SERVICE_PATH")/${SERVICE_NAME}.service.d"
  local dropin_path="${dropin_dir}/zz-cosmovisor-autodownload.conf"
  local backup_suffix
  [ -f "$SERVICE_PATH" ] || svote_upgrade_die "systemd unit not found: ${SERVICE_PATH}."
  install -d -m 0755 "$dropin_dir"
  if [ -f "$dropin_path" ]; then
    backup_suffix="$(date -u +%Y%m%dT%H%M%SZ)"
    cp -p "$dropin_path" "${dropin_path}.bak.${backup_suffix}"
  fi
  {
    printf '[Service]\n'
    printf 'Environment="DAEMON_ALLOW_DOWNLOAD_BINARIES=true"\n'
    printf 'Environment="DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=true"\n'
  } > "${dropin_path}.new"
  chmod 0644 "${dropin_path}.new"
  mv -f "${dropin_path}.new" "$dropin_path"
  svote_upgrade_log "Enabled checksum-required Cosmovisor auto-download in ${dropin_path}."
}

# svote_upgrade_assert_autodownload_enabled
# Confirm the effective systemd Cosmovisor download safeguards after restart.
svote_upgrade_assert_autodownload_enabled() {
  local effective_allow_download effective_must_checksum
  effective_allow_download=$(svote_upgrade_systemd_effective_env_value "DAEMON_ALLOW_DOWNLOAD_BINARIES" || true)
  effective_must_checksum=$(svote_upgrade_systemd_effective_env_value "DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM" || true)
  [ "$effective_allow_download" = "true" ] \
    || svote_upgrade_die "Systemd effective DAEMON_ALLOW_DOWNLOAD_BINARIES is ${effective_allow_download:-<unset>} (expected true)."
  [ "$effective_must_checksum" = "true" ] \
    || svote_upgrade_die "Systemd effective DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM is ${effective_must_checksum:-<unset>} (expected true)."
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

# svote_upgrade_patch_systemd_unit_for_cosmovisor
# Rewrite main unit for direct cosmovisor startup and remove drop-ins; print backup path.
svote_upgrade_patch_systemd_unit_for_cosmovisor() {
  local backup_path
  backup_path="${SERVICE_PATH}.bak.$(date +%Y%m%d%H%M%S)"
  local dropin_dir old_exec inferred_args derived_chain_id service_desc service_user
  local daemon_home_escaped cosmovisor_bin_escaped chain_id_escaped install_dir_escaped svoted_bin_escaped
  local start_args_escaped
  dropin_dir="$(dirname "$SERVICE_PATH")/${SERVICE_NAME}.service.d"
  local dropin backup_suffix

  if [ ! -f "$SERVICE_PATH" ]; then
    svote_upgrade_die "systemd unit not found: ${SERVICE_PATH}. Run join.sh first."
  fi

  cp -p "$SERVICE_PATH" "$backup_path"

  old_exec=$(svote_upgrade_detect_existing_execstart || true)
  if [ -z "${SVOTE_WRAPPER_SVOTED_START_ARGS:-}" ] && [ -n "$old_exec" ]; then
    inferred_args=$(svote_upgrade_extract_direct_svoted_start_args "$old_exec" "$DAEMON_HOME" || true)
    if [ -n "$inferred_args" ]; then
      SVOTE_WRAPPER_SVOTED_START_ARGS="$inferred_args"
      export SVOTE_WRAPPER_SVOTED_START_ARGS
      svote_upgrade_log "Inferred wrapper start args from direct ExecStart: ${SVOTE_WRAPPER_SVOTED_START_ARGS}"
    fi
  fi

  derived_chain_id=$(svote_upgrade_derive_chain_id_from_home "$DAEMON_HOME" || true)
  if [ -n "$derived_chain_id" ]; then
    SVOTE_CHAIN_ID="$derived_chain_id"
  fi
  service_desc=$(grep -E '^Description=' "$SERVICE_PATH" 2>/dev/null | head -n 1 | cut -d= -f2- || true)
  service_user=$(grep -E '^User=' "$SERVICE_PATH" 2>/dev/null | head -n 1 | cut -d= -f2- | tr -d '[:space:]' || true)

  if [ -z "$service_desc" ]; then
    service_desc="Shielded-Vote validator (${SERVICE_NAME})"
  fi
  if [ -z "$service_user" ]; then
    service_user="${SERVICE_USER:-root}"
  fi
  if [ "$service_user" != "root" ] && [ "${COSMOVISOR_BIN#/root/}" != "$COSMOVISOR_BIN" ]; then
    svote_upgrade_die "Refusing to migrate ${SERVICE_NAME}: service user ${service_user} cannot execute cosmovisor from root-owned path ${COSMOVISOR_BIN}. Re-run with --install-dir /home/${service_user}/.local/bin or set SVOTE_COSMOVISOR_BIN=/home/${service_user}/.local/bin/cosmovisor."
  fi
  if [ ! -x "$COSMOVISOR_BIN" ]; then
    svote_upgrade_die "Cosmovisor binary missing or not executable at ${COSMOVISOR_BIN}."
  fi

  daemon_home_escaped=$(svote_upgrade_escape_systemd_env_value "$DAEMON_HOME")
  cosmovisor_bin_escaped=$(svote_upgrade_escape_systemd_env_value "$COSMOVISOR_BIN")
  chain_id_escaped=$(svote_upgrade_escape_systemd_env_value "${SVOTE_CHAIN_ID:-}")
  install_dir_escaped=$(svote_upgrade_escape_systemd_env_value "$INSTALL_DIR")
  svoted_bin_escaped=$(svote_upgrade_escape_systemd_env_value "${INSTALL_DIR}/${SVOTE_DAEMON_NAME}")
  start_args_escaped=""
  if [ -n "${SVOTE_WRAPPER_SVOTED_START_ARGS:-}" ]; then
    start_args_escaped=" $(svote_upgrade_escape_systemd_env_value "${SVOTE_WRAPPER_SVOTED_START_ARGS}")"
  fi

  {
    printf '[Unit]\n'
    printf 'Description=%s\n' "$service_desc"
    printf 'After=network.target\n'
    printf '\n'
    printf '[Service]\n'
    printf 'Type=simple\n'
    printf 'User=%s\n' "$service_user"
    printf 'EnvironmentFile=-/etc/default/svoted\n'
    printf 'ExecStart=%s run start --home %s%s\n' "$cosmovisor_bin_escaped" "$daemon_home_escaped" "$start_args_escaped"
    printf 'Environment="SVOTE_UPGRADE_MODE=cosmovisor"\n'
    printf 'Environment="DAEMON_HOME=%s"\n' "$daemon_home_escaped"
    printf 'Environment="SVOTE_HOME=%s"\n' "$daemon_home_escaped"
    printf 'Environment="SVOTE_CHAIN_ID=%s"\n' "$chain_id_escaped"
    printf 'Environment="COSMOVISOR_BIN=%s"\n' "$cosmovisor_bin_escaped"
    printf 'Environment="DAEMON_NAME=%s"\n' "$SVOTE_DAEMON_NAME"
    printf 'Environment="DAEMON_ALLOW_DOWNLOAD_BINARIES=true"\n'
    printf 'Environment="DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=true"\n'
    printf 'Environment="SVOTE_INSTALL_DIR=%s"\n' "$install_dir_escaped"
    printf 'Environment="SVOTED_BIN=%s"\n' "$svoted_bin_escaped"
    if [ -n "${SVOTE_WRAPPER_SVOTED_START_ARGS:-}" ]; then
      printf 'Environment="SVOTE_WRAPPER_SVOTED_START_ARGS=%s"\n' "$(svote_upgrade_escape_systemd_env_value "$SVOTE_WRAPPER_SVOTED_START_ARGS")"
    fi
    printf 'Restart=on-failure\n'
    printf 'RestartSec=5\n'
    printf 'StandardOutput=journal\n'
    printf 'StandardError=journal\n'
    printf '\n'
    printf '[Install]\n'
    printf 'WantedBy=multi-user.target\n'
  } > "${SERVICE_PATH}.tmp"
  mv -f "${SERVICE_PATH}.tmp" "$SERVICE_PATH"
  chmod 0644 "$SERVICE_PATH"

  if [ -d "$dropin_dir" ]; then
    backup_suffix="$(date +%Y%m%d%H%M%S)"
    for dropin in "$dropin_dir"/*.conf; do
      [ -f "$dropin" ] || continue
      cp -p "$dropin" "${dropin}.bak.pre-migrate.${backup_suffix}"
      rm -f "$dropin"
      svote_upgrade_log "Removed drop-in override ${dropin}"
    done
  fi

  svote_upgrade_log "Rewrote ${SERVICE_PATH} for direct cosmovisor startup"
  printf '%s\n' "$backup_path"
}

# svote_upgrade_restart_service backup_unit
# daemon-reload and restart SERVICE_NAME. Leave it stopped on failure so a direct
# signer is never started alongside a partially migrated Cosmovisor service.
svote_upgrade_restart_service() {
  local backup_unit="$1"
  systemctl daemon-reload
  if ! systemctl restart "$SERVICE_NAME"; then
    svote_upgrade_warn "Failed to restart ${SERVICE_NAME}; previous unit remains at ${backup_unit:-<none>}."
    return 1
  fi
}

# svote_upgrade_wait_for_rpc timeout_secs [allow_wrapper]
# Poll svoted status until RPC responds or timeout_secs elapses; fail fast on broken runtime.
svote_upgrade_wait_for_rpc() {
  local timeout_secs="${1:-120}"
  local allow_wrapper="${2:-0}"
  local deadline=$((SECONDS + timeout_secs))
  local query_bin="${GENESIS_BIN}"
  local effective_mode effective_exec unit_active missing_runtime_checks=0
  if [ ! -x "$query_bin" ]; then
    if command -v svote_upgrade_resolve_query_svoted >/dev/null 2>&1; then
      query_bin="$(svote_upgrade_resolve_query_svoted 1 2>/dev/null || true)"
    else
      query_bin="$(command -v svoted 2>/dev/null || true)"
    fi
  fi
  while [ "$SECONDS" -le "$deadline" ]; do
    effective_mode=$(svote_upgrade_systemd_effective_env_value "SVOTE_UPGRADE_MODE" || true)
    if [ -n "$effective_mode" ] && [ "$effective_mode" != "cosmovisor" ]; then
      svote_upgrade_die "Service migrated with unexpected SVOTE_UPGRADE_MODE=${effective_mode} (expected cosmovisor)."
    fi

    effective_exec=$(svote_upgrade_systemd_effective_execstart || true)
    if [ "$allow_wrapper" = "1" ] && [ "${effective_exec##*/}" = "svoted-wrapper.sh" ]; then
      :
    else
      if [ -n "$effective_exec" ] && [ "${effective_exec##*/}" != "cosmovisor" ]; then
        svote_upgrade_die "Service migrated with unexpected ExecStart=${effective_exec} (expected cosmovisor binary)."
      fi
      if ! svote_upgrade_systemd_effective_execstart_is_cosmovisor_run_start; then
        svote_upgrade_die "Service migrated with unexpected ExecStart command (expected cosmovisor run start)."
      fi
    fi

    if systemctl is-failed --quiet "$SERVICE_NAME" 2>/dev/null; then
      svote_upgrade_die "Service ${SERVICE_NAME} entered failed state after migrate restart."
    fi

    unit_active=0
    if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
      unit_active=1
    fi

    svote_upgrade_assert_no_unmanaged_signers

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
