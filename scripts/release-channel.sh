#!/usr/bin/env bash
set -euo pipefail

tag="${1:-}"
case "$tag" in
  v[0-9]*.[0-9]*.[0-9]*-rc.[0-9]*)
    if [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]]; then
      echo "rc"
      exit 0
    fi
    ;;
  v[0-9]*.[0-9]*.[0-9]*)
    if [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      echo "stable"
      exit 0
    fi
    ;;
esac

echo "ERROR: release tag must be vN.N.N or vN.N.N-rc.N: ${tag:-<empty>}" >&2
exit 1
