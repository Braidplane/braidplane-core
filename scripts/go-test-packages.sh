#!/usr/bin/env bash
# Prints Go package import paths for the requested local test boundary.

set -euo pipefail

mode="${1:-}"

case "$mode" in
  unit)
    bash scripts/go-toolchain.sh list ./... | awk '$0 !~ /\/e2e($|\/)/ { print }'
    ;;
  e2e)
    index="${GO_E2E_SHARD_INDEX:-0}"
    count="${GO_E2E_SHARD_COUNT:-1}"
    if ! [[ "$index" =~ ^(0|[1-9][0-9]*)$ && "$count" =~ ^[1-9][0-9]*$ ]] || (( index >= count )); then
      echo "Go E2E shard index must be zero-based and less than a positive shard count" >&2
      exit 2
    fi
    # Browser support executables are not Go test suites. Sort before partitioning.
    bash scripts/go-toolchain.sh list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... |
      awk '$0 ~ /\/e2e($|\/)/ { print }' | LC_ALL=C sort |
      python3 scripts/shard-go-tests.py "$index" "$count"
    ;;
  *)
    echo "usage: scripts/go-test-packages.sh unit|e2e" >&2
    exit 2
    ;;
esac
