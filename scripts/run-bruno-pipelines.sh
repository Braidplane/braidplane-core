#!/usr/bin/env bash
# Exercise the Git-authored pipeline lifecycle against the isolated Soft Serve
# repository owned by this worktree. Always restore the authored pipeline.
set -euo pipefail

lock_dir=".worktree/bruno-pipelines.lock"
mkdir -p .worktree
if ! mkdir "${lock_dir}" 2>/dev/null; then
  echo "Git-native Bruno lifecycle is already running in this worktree" >&2
  exit 1
fi

fixture_restored=true
cleanup() {
  local run_status="$?"
  local restore_status=0
  if [ "${fixture_restored}" = false ]; then
    task git-seed-state state=restored >/dev/null || restore_status="$?"
  fi
  rmdir "${lock_dir}" || true
  if [ "${run_status}" -ne 0 ]; then
    return "${run_status}"
  fi
  return "${restore_status}"
}
trap cleanup EXIT

seed_output="${BRUNO_SEED_OUTPUT:-}"
if [ -z "${seed_output}" ]; then
  seed_output="$(task fake-seed)"
fi
export BRUNO_SEED_OUTPUT="${seed_output}"

seed_value() {
  local output="$1"
  printf '%s\n' "$output" | sed -n 's/^seeded_git_commit_sha=//p' | head -n 1
}

require_seed_sha() {
  local state="$1"
  local sha="$2"
  if [[ ! "$sha" =~ ^[0-9a-f]{40}$ ]]; then
    echo "Git fixture state $state did not return a SHA-1 commit" >&2
    exit 1
  fi
}

base_sha="$(seed_value "$seed_output")"
require_seed_sha base "$base_sha"
EXPECTED_GIT_COMMIT_SHA="$base_sha" bash scripts/run-bruno-seeded.sh api-schema/bruno/projects _pipeline-session pipeline-lifecycle --include-org
fixture_restored=false
deleted_output="$(task git-seed-state state=deleted)"
deleted_sha="$(seed_value "$deleted_output")"
require_seed_sha deleted "$deleted_sha"
EXPECTED_GIT_COMMIT_SHA="$deleted_sha" bash scripts/run-bruno-seeded.sh api-schema/bruno/projects _pipeline-session pipeline-deleted --include-org
restored_output="$(task git-seed-state state=restored)"
restored_sha="$(seed_value "$restored_output")"
require_seed_sha restored "$restored_sha"
fixture_restored=true
EXPECTED_GIT_COMMIT_SHA="$restored_sha" bash scripts/run-bruno-seeded.sh api-schema/bruno/projects _pipeline-session pipeline-restored --include-org
