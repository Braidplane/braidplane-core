#!/usr/bin/env bash
# Run the data-sources-verification Bruno collection with Git fixture secrets.
#
# Usage:
#   scripts/run-bruno-data-sources-verification.sh
set -euo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"


json_string_body() {
  python3 -c 'import json, sys; print(json.dumps(sys.stdin.read())[1:-1])'
}

scan_git_known_hosts() {
  local git_ssh_port="${GIT_SSH_PORT:-32223}"
  local attempt output

  for attempt in 1 2 3 4 5; do
    output="$(ssh-keyscan -p "${git_ssh_port}" -t ed25519 127.0.0.1 2>/dev/null || true)"
    output="$(printf '%s\n' "${output}" | grep -v '^#' || true)"
    if [ -n "${output}" ]; then
      printf '%s\n' "${output}"
      return 0
    fi
    printf 'waiting for git fixture SSH host key on 127.0.0.1:%s (attempt %s/5)\n' "${git_ssh_port}" "${attempt}" >&2
    sleep 1
  done

  echo "failed to scan git fixture SSH host key on 127.0.0.1:${git_ssh_port}" >&2
  return 1
}

git_private_key="$(json_string_body < "${GIT_TEST_SSH_KEY_PATH:?}")"
git_known_hosts="$(scan_git_known_hosts | json_string_body)"
# Public test-only RSA fixture; never use this identity with a real provider.
core_dir="$root_dir"
bigquery_service_account="$(json_string_body < "$core_dir/pkg/testutil/testdata/bigquery-service-account.json")"

seed_output="${BRUNO_SEED_OUTPUT:-}"
if [ -z "${seed_output}" ]; then
  seed_output="$(task fake-seed)"
fi

seed_value() {
  local key="$1"
  printf '%s\n' "${seed_output}" | sed -n "s/^${key}=//p" | head -n 1
}

user_email="$(seed_value owner_user_email)"
user_password="$(seed_value user_password)"
org_slug="$(seed_value organization_slug)"
project_slug="$(seed_value project_slug)"

if [ -z "${user_email}" ] || [ -z "${user_password}" ] || [ -z "${org_slug}" ] || [ -z "${project_slug}" ]; then
  echo "fake-seed did not return the data-source verification identity" >&2
  exit 1
fi

cd api-schema/bruno/data-sources-verification

bruno_env_file="$(mktemp "${TMPDIR:-/tmp}/braidplane-bruno-data-sources.XXXXXX")"
chmod 600 "${bruno_env_file}"
cleanup_bruno_env_file() {
  rm -f "${bruno_env_file}"
}
trap cleanup_bruno_env_file EXIT

# Keep secret material out of bru process arguments; Bruno reads it from a restricted temp env file.
while IFS= read -r line; do
  if [ "${line}" = "}" ]; then
    printf '  userPassword: "%s"\n' "$(printf '%s' "${user_password}" | json_string_body)"
    printf '  gitPrivateKey: %s\n' "${git_private_key}"
    printf '  gitSSHKnownHosts: %s\n' "${git_known_hosts}"
    printf '  bigqueryServiceAccount: %s\n' "${bigquery_service_account}"
  fi
  printf '%s\n' "${line}"
done < environments/Worktree.bru > "${bruno_env_file}"

bash "$root_dir/scripts/bruno-toolchain.sh" run \
  "_setup/Create Session.bru" \
  "_setup/Enable Prod Project Environment.bru" \
  runtime-variables ftp s3 http provider-connectors hubspot bigquery snowflake mongodb mysql postgres git cross-source \
  --env-file "${bruno_env_file}" -r \
  --env-var "userEmail=${user_email}" \
  --env-var "orgSlug=${org_slug}" \
  --env-var "pipelineProjectSlug=${project_slug}"
