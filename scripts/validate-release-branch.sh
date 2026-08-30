#!/usr/bin/env bash
set -euo pipefail

tag="${1:-}"
commit="${2:-$tag}"
repo="${3:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

release_branch="$("${script_dir}/release-branch.sh" "$tag")"
commit_sha="$(git -C "$repo" rev-parse --verify "${commit}^{commit}" 2>/dev/null)" || {
  echo "ERROR: release commit does not resolve: ${commit:-<empty>}" >&2
  exit 1
}

branch_ref=""
for candidate in \
  "refs/remotes/origin/${release_branch}" \
  "refs/heads/${release_branch}"
do
  if git -C "$repo" show-ref --verify --quiet "$candidate"; then
    branch_ref="$candidate"
    break
  fi
done

if [ -z "$branch_ref" ]; then
  echo "ERROR: maintenance branch does not exist: ${release_branch}" >&2
  exit 1
fi

if ! git -C "$repo" merge-base --is-ancestor "$commit_sha" "$branch_ref"; then
  echo "ERROR: ${tag} commit ${commit_sha} is not on ${release_branch}" >&2
  exit 1
fi

printf '%s is on %s\n' "$tag" "$release_branch"
