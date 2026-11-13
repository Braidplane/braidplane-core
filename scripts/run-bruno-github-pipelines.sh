#!/usr/bin/env bash
set -euo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ "$#" -gt 1 ] || { [ "$#" -eq 1 ] && [ "$1" != --cleanup ]; }; then
  echo 'usage: task bruno-github-pipelines [-- --cleanup]' >&2
  exit 2
fi
export BRAIDPLANE_GITHUB_E2E_CLEANUP="${1:-}"
# Credentials stay in the process environment. Bruno's diagnostic output may
# include HTTP headers; discard it rather than archiving or echoing it.
log_file="$(mktemp)"
chmod 600 "$log_file"
trap 'rm -f "$log_file"' EXIT
cd "$root_dir/api-schema/bruno/github-pipelines"
if ! bash "$root_dir/scripts/bruno-toolchain.sh" run lifecycle.bru --sandbox=developer >"$log_file" 2>&1; then
  echo 'GitHub lifecycle gate failed; inspect the sanitized run journal. Raw HTTP diagnostics were discarded.' >&2
  exit 1
fi
echo 'GitHub lifecycle gate passed; sanitized provenance and cleanup outcomes are in the run journal.'
