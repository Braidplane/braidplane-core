#!/usr/bin/env bash
# Provisions and runs the pinned Node/npm toolchain from a shared local cache.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if ! ROOT_DIR="$(git -C "$SCRIPT_DIR/.." rev-parse --show-toplevel 2>/dev/null)"; then
    ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
fi
# shellcheck disable=SC1091
source "$ROOT_DIR/ci/toolchain/versions.env"
# shellcheck disable=SC1091
source "$ROOT_DIR/scripts/cache-dir.sh"

runtime_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
runtime_arch="$(uname -m)"

case "$runtime_os" in
    darwin|linux) ;;
    *)
        echo "node-toolchain: unsupported OS: $runtime_os" >&2
        exit 2
        ;;
esac

case "$runtime_arch" in
    x86_64|amd64)
        runtime_arch="x64"
        ;;
    arm64|aarch64)
        runtime_arch="arm64"
        ;;
    *)
        echo "node-toolchain: unsupported architecture: $runtime_arch" >&2
        exit 2
        ;;
esac

runtime_dir="$(braidplane_cache_dir "$ROOT_DIR" node)/${NODE_VERSION}/${runtime_os}-${runtime_arch}"
runtime_bin_dir="$runtime_dir/bin"
runtime_node="$runtime_bin_dir/node"

# install_runtime downloads the exact Node release once, verifies its published
# checksum, and atomically makes it available to every worktree sharing the cache.
install_runtime() {
    local lock_dir="${runtime_dir}.lock"
    local temporary_dir=""
    local waited_tenths=0
    local archive="node-v${NODE_VERSION}-${runtime_os}-${runtime_arch}.tar.xz"
    local release_url="https://nodejs.org/dist/v${NODE_VERSION}"

    # cleanup_install releases the cache lock and removes partial download state.
    cleanup_install() {
        rm -rf "$temporary_dir" "$lock_dir"
    }

    mkdir -p "$runtime_dir"
    while ! mkdir "$lock_dir" 2>/dev/null; do
        if [ -x "$runtime_node" ]; then
            return 0
        fi
        if (( waited_tenths >= 1200 )); then
            echo "node-toolchain: timed out waiting for runtime cache lock: $lock_dir" >&2
            exit 1
        fi
        sleep 0.1
        waited_tenths=$((waited_tenths + 1))
    done
    trap cleanup_install EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM

    if [ -x "$runtime_node" ]; then
        cleanup_install
        trap - EXIT INT TERM
        return 0
    fi

    temporary_dir="$(mktemp -d)"
    if ! curl -fsSL -o "$temporary_dir/$archive" "$release_url/$archive" ||
        ! curl -fsSL -o "$temporary_dir/SHASUMS256.txt" "$release_url/SHASUMS256.txt"; then
        cleanup_install
        trap - EXIT INT TERM
        return 1
    fi

    local expected_checksum
    expected_checksum="$(awk -v archive="$archive" '$2 == archive { print $1; exit }' "$temporary_dir/SHASUMS256.txt")"
    if [ -z "$expected_checksum" ]; then
        echo "node-toolchain: checksum missing for $archive" >&2
        cleanup_install
        trap - EXIT INT TERM
        return 1
    fi

    local actual_checksum
    if command -v sha256sum >/dev/null 2>&1; then
        actual_checksum="$(sha256sum "$temporary_dir/$archive" | awk '{ print $1 }')"
    else
        actual_checksum="$(shasum -a 256 "$temporary_dir/$archive" | awk '{ print $1 }')"
    fi
    if [ "$actual_checksum" != "$expected_checksum" ]; then
        echo "node-toolchain: checksum verification failed for $archive" >&2
        cleanup_install
        trap - EXIT INT TERM
        return 1
    fi

    if ! tar -xJf "$temporary_dir/$archive" -C "$temporary_dir"; then
        cleanup_install
        trap - EXIT INT TERM
        return 1
    fi
    rm -rf "${runtime_dir}.tmp"
    mv "$temporary_dir/node-v${NODE_VERSION}-${runtime_os}-${runtime_arch}" "${runtime_dir}.tmp"
    rm -rf "$runtime_dir"
    mv "${runtime_dir}.tmp" "$runtime_dir"
    cleanup_install
    trap - EXIT INT TERM
}

if [ ! -x "$runtime_node" ]; then
    install_runtime
fi

export PATH="$runtime_bin_dir:$PATH"

if [ "$(node --version)" != "v${NODE_VERSION}" ]; then
    echo "node-toolchain: expected Node v${NODE_VERSION}; found $(node --version)" >&2
    exit 1
fi

if [ "$(npm --version)" != "${NPM_VERSION}" ]; then
    echo "node-toolchain: expected npm ${NPM_VERSION}; found $(npm --version)" >&2
    exit 1
fi

if [ "${1:-}" = "npm" ] && [ -z "${NPM_CONFIG_CACHE:-}" ]; then
    # shellcheck disable=SC1091
    source "$ROOT_DIR/scripts/cache-dir.sh"
    NPM_CONFIG_CACHE="$(braidplane_cache_dir "$ROOT_DIR" npm)"
    export NPM_CONFIG_CACHE
fi

exec "$@"
