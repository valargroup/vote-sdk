#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: verify-voting-config-release-asset.sh <tag>" >&2
  exit 2
fi

TAG="$1"
REPO="${GH_REPO:-${GITHUB_REPOSITORY:-}}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -z "$REPO" ]; then
  echo "GH_REPO or GITHUB_REPOSITORY is required." >&2
  exit 2
fi

"${SCRIPT_DIR}/release-channel.sh" "$TAG" >/dev/null

for attempt in $(seq 1 6); do
  stage_dir="$(mktemp -d)"
  if gh release download "$TAG" \
      --repo "$REPO" \
      --pattern voting-config-linux-amd64 \
      --pattern voting-config-linux-amd64.sha256 \
      --dir "$stage_dir" \
      && (cd "$stage_dir" && sha256sum --check voting-config-linux-amd64.sha256)
  then
    expected_digest="sha256:$(sha256sum "$stage_dir/voting-config-linux-amd64" | awk '{print $1}')"
    actual_digest="$(
      gh release view "$TAG" --repo "$REPO" --json assets \
        --jq '.assets[] | select(.name == "voting-config-linux-amd64") | .digest'
    )"
    if [ "$actual_digest" = "$expected_digest" ]; then
      echo "Verified voting-config-linux-amd64 for ${TAG}: ${expected_digest}"
      exit 0
    fi
  fi

  echo "voting-config release asset is not verifiable yet (attempt ${attempt}/6)." >&2
  sleep 5
done

echo "Failed to verify voting-config release assets for ${TAG}." >&2
exit 1
