#!/usr/bin/env bash
# Source explicitly for local/CI startup without access to a real GitHub App.
# These credentials cannot authenticate to GitHub.
: "${FRONTEND_URL:?Set FRONTEND_URL before sourcing this script}"
if [ -n "${BRAIDPLANE_GITHUB_TEST_KEY_FILE:-}" ]; then
    github_test_key="$BRAIDPLANE_GITHUB_TEST_KEY_FILE"
    mkdir -p "$(dirname "$github_test_key")" || return
else
    github_test_dir="$(mktemp -d "${TMPDIR:-/tmp}/braidplane-github-test.XXXXXX")" || return
    github_test_key="$github_test_dir/app.pem"
fi
if [ ! -f "$github_test_key" ]; then
    openssl genrsa -out "$github_test_key" 2048 >/dev/null 2>&1 || return
fi
chmod 600 "$github_test_key" || return
export GITHUB_PROOF_APP_ID=1
export GITHUB_PROOF_CLIENT_ID=local-test-client
export GITHUB_PROOF_CLIENT_SECRET=local-test-secret
export GITHUB_PROOF_PRIVATE_KEY_FILE="$github_test_key"
export GITHUB_PROOF_CALLBACK_URL="${FRONTEND_URL%/}/github/callback"
export GITHUB_PROOF_INSTALLATION_URL=https://github.com/apps/braidplane-local-test/installations/new
export GITHUB_PROOF_WEBHOOK_SECRET=local-test-webhook-secret-not-for-production
unset github_test_dir github_test_key
