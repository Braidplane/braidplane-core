#!/usr/bin/env bash
# Run every request with isolated lifecycle phases and verify actual execution.
set -euo pipefail

if [ "$#" -gt 1 ] || { [ "$#" -eq 1 ] && [ "$1" != --ci ]; }; then
  echo 'usage: run-bruno-all.sh [--ci]' >&2
  exit 2
fi
if [ "${1:-}" != --ci ]; then
  for key in BRAIDPLANE_GITHUB_E2E_CONFIG BRAIDPLANE_GITHUB_E2E_TOKEN BRAIDPLANE_GITHUB_E2E_APP_JWT BRAIDPLANE_GITHUB_E2E_PAT BRAIDPLANE_GITHUB_E2E_API_KEY; do
    if [ -z "${!key:-}" ]; then
      echo "bruno-all requires $key for live staging acceptance; use task bruno-ci for candidate-only coverage" >&2
      exit 1
    fi
  done
fi

BRUNO_REPORT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/braidplane-bruno-all.XXXXXX")"
export BRUNO_REPORT_DIR
trap 'rm -rf "$BRUNO_REPORT_DIR"' EXIT

# Account and credential changes stay on a newly registered disposable identity.
(cd "api-schema/bruno/Braidplane API" && bash ../../../scripts/bruno-toolchain.sh run \
  "Create a user.bru" authentication "Create a session.bru" \
  "Update user profile.bru" "Update user password.bru" "Enable email MFA.bru" \
  "Create MFA session challenge.bru" "Verify session MFA.bru" "Disable email MFA.bru" \
  organizations roles members data-sources queries --env Worktree --sandbox=developer -r)
(cd api-schema/bruno/setup-flow && bash ../../../scripts/bruno-toolchain.sh run --env Worktree -r)
(cd api-schema/bruno/data-sources-verification && bash ../../../scripts/bruno-toolchain.sh run \
  "_setup/User Onboarding.bru" "_setup/Verify Email.bru" "_setup/Create Session.bru" \
  "_setup/Org Bootstrap.bru" "_setup/Project Bootstrap.bru" --env Worktree -r)

seed_output="$(task fake-seed)"
export BRUNO_SEED_OUTPUT="$seed_output"

bash scripts/run-bruno-seeded.sh "api-schema/bruno/Braidplane API" \
  api-keys personal-access-tokens alert-policies notifications runtime-environments --include-org
bash scripts/run-bruno-seeded.sh api-schema/bruno/projects seeded --include-org
bash scripts/run-bruno-pipelines.sh
bash scripts/run-bruno-data-sources-verification.sh
(cd api-schema/bruno/multi-tenant-isolation && bash ../../../scripts/bruno-toolchain.sh run _setup org1 org2 z-cross-org member-invitations --env Worktree -r)
# Account removal must use its own disposable identities, never the shared suite seed.
BRUNO_SEED_OUTPUT='' bash scripts/run-bruno-seeded.sh "api-schema/bruno/Braidplane API" account-data-exit

if [ "${1:-}" != --ci ]; then
  bash scripts/run-bruno-github-pipelines.sh
fi
python3 scripts/bruno-coverage.py api-schema/bruno "$BRUNO_REPORT_DIR" "$@"
