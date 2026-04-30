#!/bin/bash
set -e

# Benchmark helper settings:
# - enable queue-status polling
# - require a token for helper endpoints used by the benchmark harness

export SVOTE_HELPER_API_TOKEN="${SVOTE_HELPER_API_TOKEN:-benchmark-helper-token}"
export SVOTE_HELPER_EXPOSE_QUEUE_STATUS="${SVOTE_HELPER_EXPOSE_QUEUE_STATUS:-true}"
export SVOTE_HELPER_MAX_CONCURRENT_PROOFS="${SVOTE_HELPER_MAX_CONCURRENT_PROOFS:-16}"

bash "$(dirname "$0")/init.sh"
