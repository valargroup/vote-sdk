#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: package-cosmovisor-archive.sh <tag> <linux-amd64|linux-arm64> <svoted> [output-dir]" >&2
}

if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then
  usage
  exit 2
fi

TAG="$1"
PLATFORM="$2"
SVOTED="$3"
OUTPUT_DIR="${4:-.}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"${SCRIPT_DIR}/release-channel.sh" "$TAG" >/dev/null

case "$PLATFORM" in
  linux-amd64|linux-arm64) ;;
  *)
    echo "unsupported Cosmovisor platform: $PLATFORM" >&2
    exit 2
    ;;
esac

if [ ! -f "$SVOTED" ]; then
  echo "svoted binary not found: $SVOTED" >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR"
OUTPUT_DIR="$(cd "$OUTPUT_DIR" && pwd)"
STAGE_DIR="$(mktemp -d)"
trap 'rm -rf "$STAGE_DIR"' EXIT

install -d "$STAGE_DIR/bin"
install -m 0755 "$SVOTED" "$STAGE_DIR/bin/svoted"

ARCHIVE_NAME="shielded-vote-${TAG}-cosmovisor-v1-${PLATFORM}.tar.gz"
ARCHIVE_PATH="${OUTPUT_DIR}/${ARCHIVE_NAME}"
tar czf "$ARCHIVE_PATH" -C "$STAGE_DIR" bin/svoted

if [ "$(tar tzf "$ARCHIVE_PATH")" != "bin/svoted" ]; then
  echo "Cosmovisor archive must contain only bin/svoted" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  DIGEST="$(sha256sum "$ARCHIVE_PATH" | awk '{print $1}')"
else
  DIGEST="$(shasum -a 256 "$ARCHIVE_PATH" | awk '{print $1}')"
fi
printf '%s  %s\n' "$DIGEST" "$ARCHIVE_NAME" > "${ARCHIVE_PATH}.sha256"

printf '%s\n' "$ARCHIVE_PATH"
