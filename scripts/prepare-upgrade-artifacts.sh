#!/usr/bin/env bash
# Thin wrapper around update_chain.sh --mode prepare.
set -euo pipefail

exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/update_chain.sh" \
  --mode prepare "$@"
