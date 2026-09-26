#!/usr/bin/env bash
set -euo pipefail

base=$1
head=$2

if git diff --quiet "$base" "$head" -- s2-specs; then
  exit 0
fi

if git diff --quiet "$base" "$head" -- s2/types.go; then
  echo 's2-specs changed without a review of s2/types.go comments. Update types.go or add the specsync-reviewed label after checking the docs.' >&2
  exit 1
fi
