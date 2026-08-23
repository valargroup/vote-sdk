#!/usr/bin/env bash
set -euo pipefail

circuit_manifest="circuits/Cargo.toml"
e2e_manifest="e2e-tests/Cargo.toml"
backend_tmp="$(mktemp -d)"
trap 'rm -rf "$backend_tmp"' EXIT

cargo check --manifest-path "$circuit_manifest" --all-targets --locked
cargo check --manifest-path "$circuit_manifest" --all-targets --locked \
  --no-default-features --features upstream

cargo tree --manifest-path "$circuit_manifest" --locked -e normal --prefix none \
  > "$backend_tmp/zakura-tree.txt"

if grep -Eq '^(halo2_gadgets|halo2_legacy_pdqsort|halo2_poseidon|halo2_proofs|orchard|pasta_curves|reddsa|redjubjub|sapling-crypto|sinsemilla|zcash_primitives) v' "$backend_tmp/zakura-tree.txt"; then
  echo "The Zakura circuit graph contains an upstream cryptography package:"
  grep -E '^(halo2_gadgets|halo2_legacy_pdqsort|halo2_poseidon|halo2_proofs|orchard|pasta_curves|reddsa|redjubjub|sapling-crypto|sinsemilla|zcash_primitives) v' "$backend_tmp/zakura-tree.txt"
  exit 1
fi

cargo tree --manifest-path "$circuit_manifest" --locked -e normal --prefix none \
  --no-default-features --features upstream > "$backend_tmp/upstream-tree.txt"

if grep -Eq '^zakura-[^ ]+ v' "$backend_tmp/upstream-tree.txt"; then
  echo "The upstream circuit graph contains a Zakura package:"
  grep -E '^zakura-[^ ]+ v' "$backend_tmp/upstream-tree.txt"
  exit 1
fi

cargo check --manifest-path "$e2e_manifest" --all-targets --locked
cargo tree --manifest-path "$e2e_manifest" --locked -e normal --prefix none \
  > "$backend_tmp/e2e-tree.txt"

# `zakura-wallet-lib` is the wallet selector facade used by both modes. In
# upstream mode it must be the only package whose name starts with `zakura-`.
if grep -E '^zakura-[^ ]+ v' "$backend_tmp/e2e-tree.txt" \
  | grep -Fv 'zakura-wallet-lib v' > /dev/null; then
  echo "The upstream E2E graph contains a Zakura package:"
  grep -E '^zakura-[^ ]+ v' "$backend_tmp/e2e-tree.txt" \
    | grep -Fv 'zakura-wallet-lib v'
  exit 1
fi

if cargo check --manifest-path "$circuit_manifest" --locked --no-default-features \
  > "$backend_tmp/neither.log" 2>&1; then
  echo "The circuit crate unexpectedly accepted no backend feature."
  exit 1
fi
if ! grep -Fq 'enable at least one upstream or Zakura dependency feature' "$backend_tmp/neither.log"; then
  cat "$backend_tmp/neither.log"
  echo "The no-backend build failed for an unexpected reason."
  exit 1
fi

if cargo check --manifest-path "$circuit_manifest" --locked --no-default-features \
  --features upstream,zakura > "$backend_tmp/both.log" 2>&1; then
  echo "The circuit crate unexpectedly accepted both backend features."
  exit 1
fi
if ! grep -Fq 'upstream and Zakura dependency features cannot be enabled together' "$backend_tmp/both.log"; then
  cat "$backend_tmp/both.log"
  echo "The dual-backend build failed for an unexpected reason."
  exit 1
fi
