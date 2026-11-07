#!/usr/bin/env bash
# Prints Go package import paths for the requested local test boundary.

set -euo pipefail

mode="${1:-}"

case "$mode" in
  unit)
    bash scripts/go-toolchain.sh list ./... | awk '$0 !~ /\/e2e($|\/)/ { print }'
    ;;
  e2e)
    # Browser support executables are not Go test suites.
    bash scripts/go-toolchain.sh list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... | awk '$0 ~ /\/e2e($|\/)/ { print }'
    ;;
  *)
    echo "usage: scripts/go-test-packages.sh unit|e2e" >&2
    exit 2
    ;;
esac
