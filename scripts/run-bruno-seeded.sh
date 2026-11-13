#!/usr/bin/env bash
# Run a Bruno target with credentials from task fake-seed.
#
# Usage:
#   scripts/run-bruno-seeded.sh <collection-dir> <target> [<target>...] [--include-org]
set -euo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <collection-dir> <target> [<target>...] [--include-org]" >&2
  exit 2
fi

collection_dir="$1"
shift
include_org="false"
targets=()
for argument in "$@"; do
  if [ "$argument" = "--include-org" ]; then
    include_org="true"
    continue
  fi
  targets+=("$argument")
done

if [ "${#targets[@]}" -eq 0 ]; then
  echo "at least one Bruno target is required" >&2
  exit 2
fi

seed_output="${BRUNO_SEED_OUTPUT:-}"
if [ -z "$seed_output" ]; then
  if [ "${BRAIDPLANE_SEED_SCOPE:-full}" = core ]; then
    seed_output="$(task fake-seed -- --core-only)"
  else
    seed_output="$(task fake-seed)"
  fi
fi

declare -A seed_values=()
while IFS='=' read -r key value; do
  case "$key" in
    owner_user_email|member_user_email|user_password|organization_slug|project_slug|environment_slug|git_data_source_slug|pipeline_slug|notification_record_slug)
      if [[ -z "${seed_values[$key]+set}" ]]; then
        seed_values["$key"]="$value"
      fi
      ;;
  esac
done <<< "$seed_output"

user_email="${seed_values[owner_user_email]:-}"
member_email="${seed_values[member_user_email]:-}"
user_password="${seed_values[user_password]:-}"
org_slug="${seed_values[organization_slug]:-}"
project_slug="${seed_values[project_slug]:-}"
environment_slug="${seed_values[environment_slug]:-}"
git_data_source_slug="${seed_values[git_data_source_slug]:-}"
pipeline_slug="${seed_values[pipeline_slug]:-}"
notification_record_slug="${seed_values[notification_record_slug]:-}"
expected_git_commit_sha="${EXPECTED_GIT_COMMIT_SHA:-}"

if [ -z "$user_email" ] || [ -z "$user_password" ]; then
  echo "fake-seed did not return owner_user_email/user_password" >&2
  exit 1
fi

if [ "$include_org" = "true" ] && [ -z "$org_slug" ]; then
  echo "fake-seed did not return organization_slug" >&2
  exit 1
fi

cd "$collection_dir"

args=(run "${targets[@]}" --env Worktree -r
  --env-var "userEmail=$user_email"
  --env-var "memberUserEmail=$member_email"
  --env-var "userPassword=$user_password")
if [ "$include_org" = "true" ]; then
  args+=(--env-var "orgSlug=$org_slug")
fi
args+=(--env-var "seededOrgSlug=$org_slug"
  --env-var "projectSlug=$project_slug"
  --env-var "environmentSlug=$environment_slug"
  --env-var "gitDataSourceSlug=$git_data_source_slug"
  --env-var "pipelineSlug=$pipeline_slug"
  --env-var "expectedGitCommitSHA=$expected_git_commit_sha"
  --env-var "notificationRecordSlug=$notification_record_slug")
bash "$root_dir/scripts/bruno-toolchain.sh" "${args[@]}"
