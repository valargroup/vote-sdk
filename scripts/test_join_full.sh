#!/usr/bin/env bash
# test_join_full.sh — Docker smoke test for the observer full-node installer.
#
# Builds a clean Ubuntu image with join-full.sh and _join_common.sh, runs the
# installer with SVOTE_SKIP_SERVICE=1, starts svoted in the container, waits for
# catching_up=false, and verifies the most recent finalized round when one is
# present on the target network.
#
# Usage:
#   ./scripts/test_join_full.sh              # test linux/amd64 and linux/arm64
#   ./scripts/test_join_full.sh linux/amd64  # test one platform
#
# Optional:
#   SVOTE_ENV=stage ./scripts/test_join_full.sh
#   SVOTE_SKIP_SNAPSHOT=1 ./scripts/test_join_full.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PLATFORMS=("linux/amd64" "linux/arm64")
if [ $# -gt 0 ]; then
  PLATFORMS=("$@")
fi

DOCKERFILE=$(mktemp)
cleanup() {
  rm -f "$DOCKERFILE"
}
trap cleanup EXIT

cat > "$DOCKERFILE" <<'DOCKERFILE'
FROM ubuntu:24.04

RUN apt-get update -q && \
    apt-get install -y -q --no-install-recommends \
      ca-certificates \
      curl \
      jq \
      lz4 && \
    rm -rf /var/lib/apt/lists/*

COPY join-full.sh /join-full.sh
COPY scripts/_join_common.sh /scripts/_join_common.sh
RUN chmod +x /join-full.sh /scripts/_join_common.sh

ENV SVOTE_JOIN_COMMON=/scripts/_join_common.sh
ENV SVOTE_SKIP_SERVICE=1

CMD ["/join-full.sh"]
DOCKERFILE

PASS=()
FAIL=()

for PLATFORM in "${PLATFORMS[@]}"; do
  TAG="join-full-test:${PLATFORM//\//-}"
  echo ""
  echo "════════════════════════════════════════════════"
  echo "  Platform: ${PLATFORM}"
  echo "════════════════════════════════════════════════"

  echo "--- Building image ---"
  if ! docker build \
      --platform "${PLATFORM}" \
      -f "${DOCKERFILE}" \
      -t "${TAG}" \
      "${REPO_ROOT}"; then
    echo "FAIL: docker build failed for ${PLATFORM}"
    FAIL+=("${PLATFORM} (build)")
    continue
  fi

  echo ""
  echo "--- Running join-full.sh and sync verification ---"
  if docker run --rm --platform "${PLATFORM}" \
      -e "SVOTE_ENV=${SVOTE_ENV:-prod}" \
      -e "SVOTE_DO_SPACES_BASE=${SVOTE_DO_SPACES_BASE:-}" \
      -e "SVOTE_SKIP_SNAPSHOT=${SVOTE_SKIP_SNAPSHOT:-0}" \
      -e "SVOTE_FORCE_RESET=1" \
      "${TAG}" bash -lc '
        set -euo pipefail
        /join-full.sh

        APP_TOML="$HOME/.svoted-full/config/app.toml"
        sed -i.bak "/\[api\]/,/\[.*\]/ s/enable = false/enable = true/" "$APP_TOML"
        sed -i.bak "/\[api\]/,/\[.*\]/ s/enabled-unsafe-cors = false/enabled-unsafe-cors = true/" "$APP_TOML"
        rm -f "${APP_TOML}.bak"

        svoted start --home "$HOME/.svoted-full" > /tmp/svoted-full.log 2>&1 &
        pid=$!
        trap "kill $pid 2>/dev/null || true; wait $pid 2>/dev/null || true" EXIT

        for i in $(seq 1 "${SVOTE_FULL_SYNC_ATTEMPTS:-180}"); do
          status=$(svoted status --home "$HOME/.svoted-full" 2>/dev/null || true)
          if [ -n "$status" ]; then
            catching=$(echo "$status" | jq -r ".sync_info.catching_up // true")
            height=$(echo "$status" | jq -r ".sync_info.latest_block_height // 0")
            if [ "$catching" = "false" ] && [ "$height" != "0" ]; then
              echo "Full node synced at height $height"
              break
            fi
          fi
          sleep 2
        done

        final_status=$(svoted status --home "$HOME/.svoted-full" 2>/dev/null)
        test "$(echo "$final_status" | jq -r ".sync_info.catching_up")" = "false"

        round_id=$(curl -fsS http://127.0.0.1:1317/shielded-vote/v1/rounds \
          | jq -r ".rounds[]? | select(.status == 3 or .status == \"SESSION_STATUS_FINALIZED\") | .vote_round_id" \
          | tail -n 1)
        if [ -n "$round_id" ] && [ "$round_id" != "null" ]; then
          svoted query vote verify-tally "$round_id" --home "$HOME/.svoted-full" --node tcp://127.0.0.1:26657
        else
          echo "No finalized round found; full-node sync smoke passed without tally verification."
        fi
      '; then
    echo ""
    echo "PASS: ${PLATFORM}"
    PASS+=("${PLATFORM}")
  else
    echo ""
    echo "FAIL: ${PLATFORM}"
    FAIL+=("${PLATFORM} (run)")
  fi

  docker rmi "${TAG}" >/dev/null 2>&1 || true
done

echo ""
echo "════════════════════════════════════════════════"
echo "  Results"
echo "════════════════════════════════════════════════"
for p in "${PASS[@]+"${PASS[@]}"}"; do echo "  PASS  ${p}"; done
for f in "${FAIL[@]+"${FAIL[@]}"}"; do echo "  FAIL  ${f}"; done

[ "${#FAIL[@]}" -eq 0 ]
