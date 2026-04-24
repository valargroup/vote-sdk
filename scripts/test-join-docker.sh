#!/usr/bin/env bash
# test-join-docker.sh — Build and run the join.sh Docker smoke test for
# linux/amd64 and linux/arm64.
#
# Docker cannot run macOS containers, so Darwin (amd64/arm64) is covered by
# the GitHub Actions workflow: .github/workflows/test-join.yml
# (uses macos-latest and macos-13 runners respectively).
#
# Requirements:
#   - Docker with buildx (docker buildx ls should show a multi-platform builder)
#   - Internet access to DO Spaces (binary download) and the voting-config CDN
#
# Usage:
#   ./scripts/test-join-docker.sh              # test both Linux platforms
#   ./scripts/test-join-docker.sh linux/amd64  # test one platform
#
# Exit code: 0 if all tested platforms pass, 1 if any fail.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOCKERFILE="${REPO_ROOT}/Dockerfile.join-test"

PLATFORMS=("linux/amd64" "linux/arm64")
if [ $# -gt 0 ]; then
  PLATFORMS=("$@")
fi

PASS=()
FAIL=()

for PLATFORM in "${PLATFORMS[@]}"; do
  TAG="join-test:${PLATFORM//\//-}"
  echo ""
  echo "════════════════════════════════════════════════"
  echo "  Platform: ${PLATFORM}"
  echo "════════════════════════════════════════════════"

  echo "--- Building image ---"
  if ! docker build \
      --platform "${PLATFORM}" \
      -f "${DOCKERFILE}" \
      -t "${TAG}" \
      "${REPO_ROOT}" 2>&1; then
    echo "FAIL: docker build failed for ${PLATFORM}"
    FAIL+=("${PLATFORM} (build)")
    continue
  fi

  echo ""
  echo "--- Running join.sh (SVOTE_SKIP_SERVICE=1) ---"
  if docker run --rm --platform "${PLATFORM}" "${TAG}"; then
    echo ""
    echo "PASS: ${PLATFORM}"
    PASS+=("${PLATFORM}")
  else
    echo ""
    echo "FAIL: ${PLATFORM} (exit $?)"
    FAIL+=("${PLATFORM} (run)")
  fi

  docker rmi "${TAG}" > /dev/null 2>&1 || true
done

echo ""
echo "════════════════════════════════════════════════"
echo "  Results"
echo "════════════════════════════════════════════════"
for p in "${PASS[@]+"${PASS[@]}"}"; do echo "  PASS  ${p}"; done
for f in "${FAIL[@]+"${FAIL[@]}"}"; do echo "  FAIL  ${f}"; done

[ "${#FAIL[@]}" -eq 0 ]
