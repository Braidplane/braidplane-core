#!/usr/bin/env bash
# Runs documentation Python in a persistent worktree-local virtual environment.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
# Shared tooling owns its implementation; the caller owns the environment.
# shellcheck disable=SC1091
source "$SCRIPT_DIR/cache-dir.sh"

PIP_CACHE_DIR="$(braidplane_cache_dir "$ROOT_DIR" pip)"
mkdir -p "$PIP_CACHE_DIR"
export PIP_CACHE_DIR

VENV_DIR="$ROOT_DIR/.worktree/docs-venv"
if [ ! -x "$VENV_DIR/bin/python" ] || ! "$VENV_DIR/bin/python" -m pip --version >/dev/null 2>&1; then
    if ! python3 -m venv "$VENV_DIR"; then
        rm -rf "$VENV_DIR"
        echo "python-toolchain: failed to create $VENV_DIR; install Python 3 venv support (Ubuntu/WSL: sudo apt install python3-venv) and retry." >&2
        exit 1
    fi
fi

exec "$VENV_DIR/bin/python" "$@"
