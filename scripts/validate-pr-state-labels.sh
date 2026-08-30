#!/usr/bin/env bash
set -euo pipefail

base_ref="${PR_BASE_REF:-main}"
compatible=false
breaking=false
backport=false

for label in "$@"; do
  case "$label" in
    V:state/compatible)
      compatible=true
      ;;
    V:state/breaking)
      breaking=true
      ;;
    A:backport/*)
      if [[ "$label" =~ ^A:backport/v[0-9]+\.[0-9]+\.x$ ]]; then
        backport=true
      fi
      ;;
  esac
done

state_count=0
[ "$compatible" = true ] && state_count=$((state_count + 1))
[ "$breaking" = true ] && state_count=$((state_count + 1))
if [ "$state_count" -ne 1 ]; then
  echo "ERROR: apply exactly one of V:state/compatible or V:state/breaking" >&2
  exit 1
fi

if [[ "$base_ref" =~ ^v[0-9]+\.[0-9]+\.x$ ]] && [ "$breaking" = true ]; then
  echo "ERROR: state-breaking changes cannot target maintenance branch ${base_ref}" >&2
  exit 1
fi

if [ "$backport" = true ] && [ "$base_ref" != main ]; then
  echo "ERROR: backport target labels belong on source PRs against main" >&2
  exit 1
fi

if [ "$backport" = true ] && [ "$compatible" != true ]; then
  echo "ERROR: only state-compatible changes can request a maintenance backport" >&2
  exit 1
fi

echo "State compatibility labels are valid."
