#!/usr/bin/env bash
# Runs the Go toolchain with shared, sandbox-safe caches and ephemeral temp data.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if ! ROOT_DIR="$(git -C "$SCRIPT_DIR/.." rev-parse --show-toplevel 2>/dev/null)"; then
    ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
fi
# shellcheck source=ci/toolchain/versions.env
source "$ROOT_DIR/ci/toolchain/versions.env"
export GOTOOLCHAIN="go${GO_VERSION}"
# shellcheck disable=SC1091
source "$ROOT_DIR/scripts/cache-dir.sh"

GO_VERSION="$(go env GOVERSION)"
GOOS="$(go env GOOS)"
GOARCH="$(go env GOARCH)"
CACHE_ROOT="$(braidplane_cache_dir "$ROOT_DIR" go)/${GO_VERSION}/${GOOS}-${GOARCH}"
GOCACHE="$CACHE_ROOT/build"
mkdir -p "$GOCACHE"
export GOCACHE

GOLANGCI_LINT_CACHE="$(braidplane_cache_dir "$ROOT_DIR" golangci-lint)"
mkdir -p "$GOLANGCI_LINT_CACHE"
export GOLANGCI_LINT_CACHE

TMP_BASE="${TMPDIR:-/tmp}"
TMP_BASE="${TMP_BASE%/}"
if ! GOTMPDIR="$(mktemp -d "${TMP_BASE}/braidplane-go.XXXXXX" 2>/dev/null)"; then
    mkdir -p "$ROOT_DIR/.worktree"
    GOTMPDIR="$(mktemp -d "$ROOT_DIR/.worktree/go-tmp.XXXXXX")"
fi
trap 'rm -rf "$GOTMPDIR"' EXIT
export GOTMPDIR

go "$@"
