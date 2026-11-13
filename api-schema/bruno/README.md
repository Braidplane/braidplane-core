# Core Bruno API tests

These collections exercise Core's public HTTP API against disposable synthetic
fixtures. They were imported from `Braidplane/braidplane-managed` revision
`fb51a42e3efa5168cf1b017ba5e5bc09ae59c48b`.

| Collection | Request files |
|---|---:|
| Braidplane API | 147 |
| Data source verification | 133 |
| Multi-tenant isolation | 46 |
| Projects and pipeline lifecycle | 27 |
| Setup flow | 4 |
| GitHub pipeline lifecycle | 1 |

The six billing-profile requests remain owned by Managed. Core signup and
invitation requests omit Managed's legal-acceptance extension fields, and the
profile request omits Managed's billing address. The
remaining API behavior, authentication, authorization, connectors, credentials,
notifications, account removal and pipeline contracts belong to Core.

## Candidate CI

`task bruno-ci` runs all 357 candidate-local request files. The required browser
CI job provisions the connector services from `docker-compose.connectors.yml`,
starts Core with `task dev`, and runs both the four-request `task test-bruno`
smoke suite and these collections before Playwright.

The runner preserves lifecycle ordering, uses separate disposable identities
for account changes/removal, and restores the owned Git fixture after pipeline
deletion tests. `scripts/bruno-coverage.py` compares sanitized execution reports
with every checked-in request and fails for missing, skipped or failed requests.
Reports omit request/response headers and bodies and are removed after the check.

For local runs, provision the same owned connector services and Git fixture as
[`scripts/ci-browser.sh`](../../scripts/ci-browser.sh), start `task dev`, and export
the matching database, encryption keys, application URLs and connector ports.
Then run `task bruno-ci`. The tasks write to the selected database and mutate the
local Git fixture; use only disposable synthetic data. They never start or select
a hosted runtime. `scripts/gen-bruno-env.sh` writes ignored `Worktree` environments
from the calling shell's ports; it does not read retained environment files.

The four-request smoke suite's local setup is documented in
[Community build](../../COMMUNITY_BUILD.md#bruno-api-tests).

## Live provider acceptance

`github-pipelines/lifecycle.bru` remains explicitly excluded from candidate CI.
It exercises a deployed API and a dedicated private GitHub fixture repository
with task-owned credentials. This is Core feature coverage, but it cannot prove
the behavior of a local PR candidate.

Only use `task bruno-all` under explicit live-provider acceptance authority,
with the `BRAIDPLANE_GITHUB_E2E_*` configuration and credentials required by
`github-pipelines/lifecycle.cjs`. Missing live configuration fails before
mutating local API data or the Git fixture. `scripts/run-bruno-github-pipelines.sh --cleanup` uses the
ownership journal to clean up interrupted provider work; it does not grant
authority to mutate an unrelated repository. Ordinary CI has no live credentials.
