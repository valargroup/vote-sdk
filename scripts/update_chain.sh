#!/usr/bin/env bash
# Updates an existing join.sh validator installation for coordinated x/upgrade halts.
# Placeholders latest, valargroup/vote-sdk, and https://shielded-vote.nyc3.digitaloceanspaces.com are substituted at
# publish time -- do not edit the published script by hand.
#
# Modes:
#   prepare        Stage Cosmovisor binaries only; never stop the running validator.
#   migrate        One-time migration from direct wrapper service to Cosmovisor.
#   verify-prestage Read-only PASS/FAIL checklist for operator runbooks.
#
# Example:
#   curl -fsSL https://shielded-vote.nyc3.digitaloceanspaces.com/update_chain.sh | sudo bash -s -- \
#     --mode prepare --plan-name v1_4_0 --tag v1.4.0
set -euo pipefail

readonly UPDATE_DEFAULT_RELEASE_TAG='latest'
readonly UPDATE_DEFAULT_GITHUB_REPO='valargroup/vote-sdk'
readonly UPDATE_DEFAULT_DO_BASE='https://shielded-vote.nyc3.digitaloceanspaces.com'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
COMMON_LIB=""
if [ -n "$SCRIPT_DIR" ] && [ -f "${SCRIPT_DIR}/_chain_upgrade_common.sh" ]; then
  COMMON_LIB="${SCRIPT_DIR}/_chain_upgrade_common.sh"
elif [ -f "/opt/shielded-vote/scripts/_chain_upgrade_common.sh" ]; then
  COMMON_LIB="/opt/shielded-vote/scripts/_chain_upgrade_common.sh"
fi
if [ -z "$COMMON_LIB" ]; then
  COMMON_TMP="$(mktemp)"
  DO_BASE_FOR_COMMON="${SVOTE_DO_SPACES_BASE:-${UPDATE_DEFAULT_DO_BASE}}"
  if [ "$DO_BASE_FOR_COMMON" = 'https://shielded-vote.nyc3.digitaloceanspaces.com' ]; then
    DO_BASE_FOR_COMMON='https://shielded-vote.nyc3.digitaloceanspaces.com'
  fi
  curl -fsSL "${DO_BASE_FOR_COMMON%/}/scripts/_chain_upgrade_common.sh" -o "$COMMON_TMP"
  COMMON_LIB="$COMMON_TMP"
fi
# shellcheck source=scripts/_chain_upgrade_common.sh
source "$COMMON_LIB"

MODE="prepare"
PLAN_NAME=""
RELEASE_TAG="$UPDATE_DEFAULT_RELEASE_TAG"
ALLOW_NO_PLAN=0
REQUIRE_COSMOVISOR_SERVICE=1
HOME_CLI_SET=0
INSTALL_CLI_SET=0
TIMEOUT_SECS=120

# usage
# Print CLI help for update_chain.sh modes and options, then return (caller exits 0 on --help).
usage() {
  cat <<EOF
usage: update_chain.sh [options]

Options:
  --mode MODE             prepare | migrate | verify-prestage (default: prepare)
  --plan-name NAME        x/upgrade plan name (required for prepare/verify-prestage)
  --tag TAG               Release tag to stage (default: ${UPDATE_DEFAULT_RELEASE_TAG})
  --home PATH             Validator home (default: \$SVOTE_HOME or \$HOME/.svoted)
  --install-dir PATH      Binary install dir (default: \$SVOTE_INSTALL_DIR or \$HOME/.local/bin)
  --service-name NAME     systemd service name (default: svoted)
  --allow-no-plan         Allow staging before a plan is scheduled on-chain
  --skip-cosmovisor-service  verify-prestage: skip systemd Cosmovisor service checks
  --timeout-secs N        RPC readiness timeout after migrate restart (default: 120)
  --repo OWNER/REPO       GitHub repo for release fallback (default: ${UPDATE_DEFAULT_GITHUB_REPO})
  --do-base URL           DigitalOcean Spaces base URL (default: ${UPDATE_DEFAULT_DO_BASE})
  --help                  Show this help text.

Environment:
  SVOTE_ACK_SINGLE_SIGNER=1   Required in non-interactive mode for migrate/prepare service checks.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --mode)
      [ "$#" -ge 2 ] || svote_upgrade_die "--mode requires a value."
      MODE="$2"
      shift 2
      ;;
    --mode=*)
      MODE="${1#--mode=}"
      shift
      ;;
    --plan-name)
      [ "$#" -ge 2 ] || svote_upgrade_die "--plan-name requires a value."
      PLAN_NAME="$2"
      shift 2
      ;;
    --plan-name=*)
      PLAN_NAME="${1#--plan-name=}"
      shift
      ;;
    --tag)
      [ "$#" -ge 2 ] || svote_upgrade_die "--tag requires a value."
      RELEASE_TAG="$2"
      shift 2
      ;;
    --tag=*)
      RELEASE_TAG="${1#--tag=}"
      shift
      ;;
    --home)
      [ "$#" -ge 2 ] || svote_upgrade_die "--home requires a value."
      SVOTE_HOME="$2"
      HOME_CLI_SET=1
      shift 2
      ;;
    --home=*)
      SVOTE_HOME="${1#--home=}"
      HOME_CLI_SET=1
      shift
      ;;
    --install-dir)
      [ "$#" -ge 2 ] || svote_upgrade_die "--install-dir requires a value."
      SVOTE_INSTALL_DIR="$2"
      INSTALL_CLI_SET=1
      shift 2
      ;;
    --install-dir=*)
      SVOTE_INSTALL_DIR="${1#--install-dir=}"
      INSTALL_CLI_SET=1
      shift
      ;;
    --service-name)
      [ "$#" -ge 2 ] || svote_upgrade_die "--service-name requires a value."
      SVOTE_SERVICE_NAME="$2"
      shift 2
      ;;
    --service-name=*)
      SVOTE_SERVICE_NAME="${1#--service-name=}"
      shift
      ;;
    --allow-no-plan)
      ALLOW_NO_PLAN=1
      shift
      ;;
    --skip-cosmovisor-service)
      REQUIRE_COSMOVISOR_SERVICE=0
      shift
      ;;
    --timeout-secs)
      [ "$#" -ge 2 ] || svote_upgrade_die "--timeout-secs requires a value."
      TIMEOUT_SECS="$2"
      shift 2
      ;;
    --timeout-secs=*)
      TIMEOUT_SECS="${1#--timeout-secs=}"
      shift
      ;;
    --repo)
      [ "$#" -ge 2 ] || svote_upgrade_die "--repo requires OWNER/REPO."
      SVOTE_GITHUB_REPO="$2"
      shift 2
      ;;
    --repo=*)
      SVOTE_GITHUB_REPO="${1#--repo=}"
      shift
      ;;
    --do-base)
      [ "$#" -ge 2 ] || svote_upgrade_die "--do-base requires a URL."
      SVOTE_DO_SPACES_BASE="$2"
      shift 2
      ;;
    --do-base=*)
      SVOTE_DO_SPACES_BASE="${1#--do-base=}"
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      svote_upgrade_die "Unknown option: $1"
      ;;
  esac
done

case "$MODE" in
  prepare|migrate|verify-prestage) ;;
  *) svote_upgrade_die "Unsupported --mode: ${MODE}" ;;
esac

case "$TIMEOUT_SECS" in
  ''|*[!0-9]*) svote_upgrade_die "--timeout-secs must be a non-negative integer." ;;
esac

if [ -z "$PLAN_NAME" ] && [ "$MODE" != "migrate" ]; then
  svote_upgrade_die "--plan-name is required for mode ${MODE}."
fi

if [ "$UPDATE_DEFAULT_DO_BASE" != 'https://shielded-vote.nyc3.digitaloceanspaces.com' ]; then
  SVOTE_DO_SPACES_BASE="${SVOTE_DO_SPACES_BASE:-$UPDATE_DEFAULT_DO_BASE}"
fi
SVOTE_GITHUB_REPO="${SVOTE_GITHUB_REPO:-$UPDATE_DEFAULT_GITHUB_REPO}"

svote_upgrade_require_linux_systemd_root
svote_upgrade_require_curl
svote_upgrade_require_tools jq tar sed
svote_upgrade_resolve_paths
if [ "${EUID:-0}" -eq 0 ]; then
  svote_upgrade_autodetect_from_systemd_unit "$HOME_CLI_SET" "$INSTALL_CLI_SET"
fi

if [ ! -d "$DAEMON_HOME" ]; then
  svote_upgrade_die "Validator home not found: ${DAEMON_HOME}. Run join.sh first."
fi

TMP_DIR=""
# cleanup
# Remove TMP_DIR on EXIT when set by run_stage_first.
cleanup() {
  if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup EXIT

# run_stage_first
# Download release, stage genesis/upgrade binaries and cosmovisor; never stop the running validator.
run_stage_first() {
  local current_bin="${INSTALL_DIR}/svoted"
  local staged_svoted upgrade_bin_dir upgrade_bin

  svote_upgrade_verify_validator_identity_files

  if [ "$MODE" != "verify-prestage" ]; then
    if [ "$ALLOW_NO_PLAN" = "0" ]; then
      svote_upgrade_validate_scheduled_plan "$PLAN_NAME" 0
    else
      svote_upgrade_validate_scheduled_plan "$PLAN_NAME" 1
    fi
  fi

  TMP_DIR=$(mktemp -d)
  local tarball_path extracted_svoted
  tarball_path=$(svote_upgrade_download_release_tarball "$RELEASE_TAG" "$TMP_DIR")
  extracted_svoted=$(svote_upgrade_extract_svoted "$tarball_path" "$TMP_DIR" "$RELEASE_TAG")
  svote_upgrade_verify_binary_tag "$extracted_svoted" "$RELEASE_TAG"

  svote_upgrade_install_cosmovisor "$TMP_DIR"

  if [ -x "$current_bin" ]; then
    staged_svoted="$current_bin"
  elif [ -x "$GENESIS_BIN" ]; then
    staged_svoted="$GENESIS_BIN"
  else
    svote_upgrade_die "Could not locate current svoted binary under ${INSTALL_DIR} or ${GENESIS_BIN}."
  fi

  svote_upgrade_log "Staging genesis binary at ${GENESIS_BIN}"
  svote_upgrade_stage_binary "$staged_svoted" "$GENESIS_BIN"

  upgrade_bin_dir=$(svote_upgrade_upgrade_bin_dir "$PLAN_NAME")
  upgrade_bin=$(svote_upgrade_upgrade_bin_path "$PLAN_NAME")
  svote_upgrade_log "Staging upgrade binary for plan ${PLAN_NAME} at ${upgrade_bin}"
  svote_upgrade_stage_binary "$extracted_svoted" "$upgrade_bin"

  svote_upgrade_assert_layout_ready "$PLAN_NAME"
  svote_upgrade_fixup_cosmovisor_ownership
  svote_upgrade_log "Staging complete; running validator was not stopped."
}

# run_verify_prestage
# Delegate to svote_upgrade_verify_prestage with script-level flags (plan, tag, service checks).
run_verify_prestage() {
  svote_upgrade_verify_prestage "$PLAN_NAME" "$RELEASE_TAG" "$ALLOW_NO_PLAN" "$REQUIRE_COSMOVISOR_SERVICE"
}

# run_migrate
# Stop validator, patch systemd for cosmovisor, restart, and wait for RPC; requires single-signer ack.
run_migrate() {
  local backup_unit

  if [ ! -x "$WRAPPER_BIN" ]; then
    svote_upgrade_die "Wrapper script missing: ${WRAPPER_BIN}. Re-run join.sh or publish svoted-wrapper.sh."
  fi

  svote_upgrade_require_single_signer_ack
  svote_upgrade_stop_validator_service

  backup_unit=$(svote_upgrade_patch_systemd_unit_for_cosmovisor)
  svote_upgrade_restart_service "$backup_unit"
  svote_upgrade_wait_for_rpc "$TIMEOUT_SECS"
  svote_upgrade_log "Migration to Cosmovisor service completed."
}

case "$MODE" in
  prepare)
    run_stage_first
    ;;
  verify-prestage)
    run_verify_prestage
    ;;
  migrate)
    if [ -z "$PLAN_NAME" ]; then
      svote_upgrade_die "--plan-name is required for migrate (needed to stage upgrade binary)."
    fi
    run_stage_first
    run_migrate
    if [ "$PLAN_NAME" != "" ]; then
      svote_upgrade_verify_prestage "$PLAN_NAME" "$RELEASE_TAG" "$ALLOW_NO_PLAN" "$REQUIRE_COSMOVISOR_SERVICE"
    fi
    ;;
esac

echo
echo "==========================================="
echo "  Chain upgrade preparation completed"
echo "==========================================="
echo
echo "  Mode:              ${MODE}"
echo "  Plan name:         ${PLAN_NAME:-<none>}"
echo "  Release tag:       ${RELEASE_TAG}"
echo "  Validator home:    ${DAEMON_HOME}"
echo "  Cosmovisor binary: ${COSMOVISOR_BIN}"
echo
echo "How to verify:"
echo "  sudo bash update_chain.sh --mode verify-prestage --plan-name ${PLAN_NAME} --tag ${RELEASE_TAG}"
echo "  svoted query upgrade plan --home ${DAEMON_HOME}"
echo "  journalctl -u ${SERVICE_NAME} -f"
