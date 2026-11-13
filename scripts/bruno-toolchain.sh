#!/usr/bin/env bash
set -euo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ "${1:-}" = run ] && [ -n "${BRUNO_REPORT_DIR:-}" ]; then
  report="$(mktemp "$BRUNO_REPORT_DIR/run.XXXXXX")"
  mv "$report" "$report.json"
  # Headers and bodies may contain credentials; inventory needs filenames only.
  set -- "$@" --reporter-json "$report.json" --reporter-skip-all-headers --reporter-skip-body
fi

exec bash "$root_dir/scripts/node-toolchain.sh" node \
  "$root_dir/bruno/node_modules/@usebruno/cli/bin/bru.js" "$@"
