#!/usr/bin/env bash
#
# install-release.sh - Download a tagged shielded-vote release, verify its
# checksum, extract it, and atomically swap the /opt/shielded-vote/current
# symlink. Optionally restarts the svoted systemd unit.
#
# Usage:
#   install-release.sh --tag v1.2.3
#   TAG=v1.2.3 install-release.sh
#
# Environment overrides:
#   TAG              Release tag (alternative to --tag flag)
#   BASE_URL         DO Spaces base URL (default: https://vote.fra1.digitaloceanspaces.com)
#   INSTALL_ROOT     Root directory for releases (default: /opt/shielded-vote)
#   SKIP_RESTART     Set to 1 to skip systemctl restart (useful during cloud-init)

set -euo pipefail

BASE_URL="${BASE_URL:-https://vote.fra1.digitaloceanspaces.com}"
INSTALL_ROOT="${INSTALL_ROOT:-/opt/shielded-vote}"
SKIP_RESTART="${SKIP_RESTART:-0}"

while [ $# -gt 0 ]; do
  case "$1" in
    --tag)       TAG="$2"; shift 2 ;;
    --tag=*)     TAG="${1#--tag=}"; shift ;;
    --base)      BASE_URL="$2"; shift 2 ;;
    --base=*)    BASE_URL="${1#--base=}"; shift ;;
    --skip-restart) SKIP_RESTART=1; shift ;;
    *)           shift ;;
  esac
done

TAG="${TAG:?TAG must be set via --tag flag or TAG env var}"

ARCH_RAW=$(uname -m)
case "$ARCH_RAW" in
  x86_64)        ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)             echo "ERROR: Unsupported arch: $ARCH_RAW"; exit 1 ;;
esac

PLATFORM="linux-${ARCH}"
TARBALL="shielded-vote-${TAG}-${PLATFORM}.tar.gz"
RELEASE_DIR="${INSTALL_ROOT}/releases/${TAG}"

echo "=== Installing shielded-vote ${TAG} (${PLATFORM}) ==="

mkdir -p "${INSTALL_ROOT}/releases" /tmp/sv-install

echo "Downloading ${TARBALL}..."
curl -fsSL -o "/tmp/sv-install/${TARBALL}" "${BASE_URL}/${TARBALL}"
curl -fsSL -o "/tmp/sv-install/${TARBALL}.sha256" "${BASE_URL}/${TARBALL}.sha256"

echo "Verifying checksum..."
EXPECTED=$(awk '{print $1}' "/tmp/sv-install/${TARBALL}.sha256")
if command -v sha256sum > /dev/null 2>&1; then
  ACTUAL=$(sha256sum "/tmp/sv-install/${TARBALL}" | awk '{print $1}')
elif command -v shasum > /dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "/tmp/sv-install/${TARBALL}" | awk '{print $1}')
else
  echo "WARNING: No sha256sum or shasum found, skipping verification."
  ACTUAL="$EXPECTED"
fi

if [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "ERROR: Checksum mismatch!"
  echo "  Expected: ${EXPECTED}"
  echo "  Actual:   ${ACTUAL}"
  rm -rf /tmp/sv-install
  exit 1
fi
echo "Checksum OK."

echo "Extracting to ${RELEASE_DIR}..."
rm -rf "${RELEASE_DIR}"
mkdir -p "${RELEASE_DIR}"
tar xzf "/tmp/sv-install/${TARBALL}" -C "${RELEASE_DIR}" --strip-components=1
chmod +x "${RELEASE_DIR}/bin/"*

echo "Swapping symlink ${INSTALL_ROOT}/current -> ${RELEASE_DIR}"
ln -sfn "${RELEASE_DIR}" "${INSTALL_ROOT}/current.new"
mv -Tf "${INSTALL_ROOT}/current.new" "${INSTALL_ROOT}/current"

rm -rf /tmp/sv-install

if [ "$SKIP_RESTART" = "1" ]; then
  echo "Skipping systemd restart (SKIP_RESTART=1)."
else
  echo "Restarting svoted..."
  systemctl daemon-reload
  systemctl restart svoted
fi

echo "=== Done: shielded-vote ${TAG} installed ==="
