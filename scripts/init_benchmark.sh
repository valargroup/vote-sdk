#!/bin/bash
set -e

# Benchmark helper settings:
# - disable unlinkability delays
# - enable queue-status polling
# - require a token for helper endpoints used by the benchmark harness

export ZALLY_HELPER_API_TOKEN="${ZALLY_HELPER_API_TOKEN:-benchmark-helper-token}"
export ZALLY_HELPER_EXPOSE_QUEUE_STATUS="${ZALLY_HELPER_EXPOSE_QUEUE_STATUS:-true}"
export ZALLY_HELPER_MEAN_DELAY="${ZALLY_HELPER_MEAN_DELAY:-0}"
export ZALLY_HELPER_MIN_DELAY="${ZALLY_HELPER_MIN_DELAY:-0}"
export ZALLY_HELPER_PROCESS_INTERVAL="${ZALLY_HELPER_PROCESS_INTERVAL:-1}"
export ZALLY_HELPER_MAX_CONCURRENT_PROOFS="${ZALLY_HELPER_MAX_CONCURRENT_PROOFS:-16}"

bash "$(dirname "$0")/init.sh"
