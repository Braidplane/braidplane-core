#!/usr/bin/env bash
# Resolves a writable cache directory shared by Braidplane worktrees.
set -euo pipefail

cache_dir_is_writable() {
    local candidate="$1"

    mkdir -p "$candidate" 2>/dev/null && [ -w "$candidate" ]
}

# braidplane_cache_dir returns one exact shared cache leaf when that approved
# leaf is writable. A worktree-local fallback keeps sandboxed invocations
# working without probing the broader, intentionally unapproved parent.
braidplane_cache_dir() {
    local root_dir="$1"
    local leaf="$2"
    local shared_base=""
    local shared_cache=""
    local fallback_cache="${root_dir}/.worktree/cache/${leaf}"

    if [ -n "${XDG_CACHE_HOME:-}" ]; then
        shared_base="$XDG_CACHE_HOME"
    elif [ -n "${HOME:-}" ]; then
        shared_base="${HOME}/.cache"
    fi
    if [ -n "$shared_base" ]; then
        shared_cache="${shared_base}/braidplane/${leaf}"
    fi

    if [ -n "$shared_base" ] && cache_dir_is_writable "$shared_cache"; then
        printf '%s\n' "$shared_cache"
        return 0
    fi

    cache_dir_is_writable "$fallback_cache" || true
    printf '%s\n' "$fallback_cache"
}
