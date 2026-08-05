#!/usr/bin/env bash
# verify_upgrade_release_artifacts.sh — post-release smoke check for upgrade tooling on DO Spaces.
#
# Usage:
#   scripts/verify_upgrade_release_artifacts.sh v0.11.0
#   scripts/verify_upgrade_release_artifacts.sh v0.11.0 https://shielded-vote.nyc3.digitaloceanspaces.com
#   scripts/verify_upgrade_release_artifacts.sh --tag-scoped-only v0.11.0
set -euo pipefail

usage() {
  echo "usage: verify_upgrade_release_artifacts.sh [--tag-scoped-only] <tag> [do_base]" >&2
}

TAG_SCOPED_ONLY=false
if [ "${1:-}" = "--tag-scoped-only" ]; then
  TAG_SCOPED_ONLY=true
  shift
fi
if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  usage
  exit 2
fi

TAG="$1"
DO_BASE="${2:-${SVOTE_DO_SPACES_BASE:-https://shielded-vote.nyc3.digitaloceanspaces.com}}"
DO_BASE="${DO_BASE%/}"
PLATFORM="${SVOTE_PLATFORM:-linux-amd64}"
CURL_BIN="${CURL_BIN:-curl}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHANNEL="$("${SCRIPT_DIR}/release-channel.sh" "$TAG")"
TAGGED_UPGRADE_BASE="${DO_BASE}/scripts/upgrade/${TAG}"

failures=0

check_url() {
  local url="$1"
  local label="$2"
  if "$CURL_BIN" -fsSIL --retry 3 --retry-delay 2 "$url" >/dev/null 2>&1; then
    echo "[PASS] ${label}: ${url}"
  else
    echo "[FAIL] ${label}: ${url}" >&2
    failures=$((failures + 1))
  fi
}

check_script_help() {
  local url="$1"
  local tmp
  tmp=$(mktemp)
  if ! "$CURL_BIN" -fsSL --retry 3 "$url" -o "$tmp"; then
    echo "[FAIL] download ${url}" >&2
    failures=$((failures + 1))
    rm -f "$tmp"
    return
  fi
  if ! bash -n "$tmp" 2>/dev/null; then
    echo "[FAIL] bash -n ${url}" >&2
    failures=$((failures + 1))
    rm -f "$tmp"
    return
  fi
  if grep -q "UPDATE_DEFAULT_RELEASE_TAG='${TAG}'" "$tmp" 2>/dev/null \
    || grep -q "UPDATE_DEFAULT_RELEASE_TAG=\"${TAG}\"" "$tmp" 2>/dev/null; then
    echo "[PASS] update_chain.sh pinned to tag ${TAG}"
  else
    echo "[FAIL] update_chain.sh default tag is not ${TAG}" >&2
    failures=$((failures + 1))
  fi
  rm -f "$tmp"
}

echo "=== Upgrade release artifact verification ==="
echo "Tag:      ${TAG}"
echo "DO base:  ${DO_BASE}"
echo "Platform: ${PLATFORM}"
if [ "$TAG_SCOPED_ONLY" = true ]; then
  echo "Scope:    tag-scoped only"
else
  echo "Scope:    complete release channel"
fi
echo

for key in \
  "scripts/upgrade/${TAG}/update_chain.sh" \
  "scripts/upgrade/${TAG}/_chain_upgrade_common.sh" \
  "scripts/upgrade/${TAG}/prepare-upgrade-artifacts.sh" \
  "scripts/join/${TAG}/join.sh" \
  "scripts/svoted-wrapper/${TAG}/svoted-wrapper.sh"
do
  check_url "${DO_BASE}/${key}" "$key"
done

check_script_help "${TAGGED_UPGRADE_BASE}/update_chain.sh"

if [ "$CHANNEL" = "stable" ] && [ "$TAG_SCOPED_ONLY" = false ]; then
  check_url "${DO_BASE}/version.txt" "version.txt"
  published_version=$("$CURL_BIN" -fsSL "${DO_BASE}/version.txt" | tr -d '[:space:]' || true)
  if [ "$published_version" = "$TAG" ]; then
    echo "[PASS] version.txt matches ${TAG}"
  else
    echo "[FAIL] version.txt=${published_version:-<empty>} expected ${TAG}" >&2
    failures=$((failures + 1))
  fi

  for key in \
    "update_chain.sh" \
    "scripts/_chain_upgrade_common.sh" \
    "prepare-upgrade-artifacts.sh" \
    "join.sh" \
    "svoted-wrapper.sh"
  do
    check_url "${DO_BASE}/${key}" "$key"
  done
fi

tarball="shielded-vote-${TAG}-${PLATFORM}.tar.gz"
check_url "${DO_BASE}/binaries/vote-sdk/${tarball}" "release tarball"
check_url "${DO_BASE}/binaries/vote-sdk/${tarball}.sha256" "release checksum"

tmp_tar=$(mktemp)
tmp_sum=$(mktemp)
if "$CURL_BIN" -fsSL "${DO_BASE}/binaries/vote-sdk/${tarball}" -o "$tmp_tar" \
  && "$CURL_BIN" -fsSL "${DO_BASE}/binaries/vote-sdk/${tarball}.sha256" -o "$tmp_sum"; then
  expected=$(awk '{print $1}' "$tmp_sum" | tr 'A-F' 'a-f')
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$tmp_tar" | awk '{print $1}' | tr 'A-F' 'a-f')
  else
    actual=$(shasum -a 256 "$tmp_tar" | awk '{print $1}' | tr 'A-F' 'a-f')
  fi
  if [ "$expected" = "$actual" ]; then
    echo "[PASS] tarball SHA256 verified"
  else
    echo "[FAIL] tarball checksum mismatch" >&2
    failures=$((failures + 1))
  fi
fi
rm -f "$tmp_tar" "$tmp_sum"

echo
if [ "$failures" -gt 0 ]; then
  echo "Verification failed (${failures} checks)." >&2
  exit 1
fi
echo "All upgrade release artifact checks passed."
