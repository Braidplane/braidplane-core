#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
root_dir="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
# shellcheck disable=SC1091
source "$script_dir/../ci/toolchain/versions.env"
# shellcheck disable=SC1091
source "$script_dir/cache-dir.sh"

version="${MIGRATE_VERSION:?}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"

case "$os" in
  linux|darwin) ;;
  *)
    echo "unsupported OS for migrate binary: $os" >&2
    exit 2
    ;;
esac

case "$arch" in
  x86_64|amd64)
    arch="amd64"
    ;;
  arm64|aarch64)
    arch="arm64"
    ;;
  *)
    echo "unsupported architecture for migrate binary: $arch" >&2
    exit 2
    ;;
esac

if [ -n "${MIGRATE_BIN_DIR:-}" ]; then
  bin_dir="$MIGRATE_BIN_DIR"
else
  bin_dir="$(braidplane_cache_dir "$root_dir" migrate)/${version}/${os}-${arch}"
fi
binary="${bin_dir}/migrate-v${version}"

install_binary() {
  local lock_dir="${bin_dir}.lock"
  local tmp_dir=""
  local waited_tenths=0
  local archive="migrate.${os}-${arch}.tar.gz"
  local release_url="https://github.com/golang-migrate/migrate/releases/download/v${version}"

  cleanup_install() {
    rm -rf "$tmp_dir" "$lock_dir"
  }

  mkdir -p "$bin_dir"
  while ! mkdir "$lock_dir" 2>/dev/null; do
    if (( waited_tenths >= 1200 )); then
      echo "timed out waiting for migrate cache lock: $lock_dir" >&2
      exit 1
    fi
    sleep 0.1
    waited_tenths=$((waited_tenths + 1))
  done
  trap cleanup_install EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  if [ -x "$binary" ]; then
    cleanup_install
    trap - EXIT INT TERM
    return 0
  fi

  tmp_dir="$(mktemp -d)"

  if ! curl -fsSL -o "$tmp_dir/$archive" "$release_url/$archive" ||
    ! curl -fsSL -o "$tmp_dir/sha256sum.txt" "$release_url/sha256sum.txt"; then
    cleanup_install
    trap - EXIT INT TERM
    return 1
  fi

  local expected_checksum
  expected_checksum="$(awk -v archive="$archive" '$2 == archive || $2 == "*" archive { print $1; exit }' "$tmp_dir/sha256sum.txt")"
  if [ -z "$expected_checksum" ]; then
    echo "migrate checksum missing for $archive" >&2
    cleanup_install
    trap - EXIT INT TERM
    return 1
  fi

  local actual_checksum
  if command -v sha256sum >/dev/null 2>&1; then
    actual_checksum="$(sha256sum "$tmp_dir/$archive" | awk '{ print $1 }')"
  else
    actual_checksum="$(shasum -a 256 "$tmp_dir/$archive" | awk '{ print $1 }')"
  fi
  if [ "$actual_checksum" != "$expected_checksum" ]; then
    echo "migrate checksum verification failed for $archive" >&2
    cleanup_install
    trap - EXIT INT TERM
    return 1
  fi

  if ! tar -xzf "$tmp_dir/$archive" -C "$tmp_dir"; then
    cleanup_install
    trap - EXIT INT TERM
    return 1
  fi
  mv "$tmp_dir/migrate" "${binary}.tmp"
  mv "${binary}.tmp" "$binary"
  chmod +x "$binary"
  cleanup_install
  trap - EXIT INT TERM
}

if [ ! -x "$binary" ]; then
  install_binary
fi

exec "$binary" "$@"
