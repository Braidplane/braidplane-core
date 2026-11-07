# Community browser workflow tests

Playwright exercises the Community frontend/backend. Git, run-inspection, HTTP
OAuth, notification connection and Proof fixtures render the production
frontend against isolated API replies. They prove UI integration; provider and
hosted acceptance require separate evidence.

The Google signup fixture uses disposable PostgreSQL, production auth/session
code. A local OIDC issuer exercises the
cross-site callback and SameSite=Lax session behavior. Google-hosted acceptance
is outside that fixture's scope.

## Community coverage policy

The critical profile retains 15 logical journeys / 25 executions. Chromium runs
all 15; Firefox and WebKit each run five: owned query preview/save, Git
import/bind/deploy, successful-run inspection, Proof Reconnect and HTTP OAuth
setup/reconnect. Auth, Google signup, persisted Builder save, acknowledgement
authoring, secret HTTP headers/pagination, Proof discovery, Slack setup and the
pending-progress navigation race run in Chromium. Auth and the real Builder
mutation remain single-engine because they share seeded state and throttles.

The full profile currently discovers 63 logical journeys / 173 executions.
`alpha-inventory.json` owns retained cases, reasons and budgets. Broad UI state
permutations belong in Vitest, Go or transport coverage when those layers can
prove the same behavior. Each added browser case needs a concrete browser-only
risk and an explicit inventory/budget update.

Run discovery from the repository root after installing frontend dependencies:

```sh
bash scripts/node-toolchain.sh node scripts/check-browser-inventory.cjs
```

Discovery checks full and critical selections and writes
`.worktree/playwright/inventory.json`. It does not start browsers or fixtures.

## Runtime qualification during cutover

The Community browser setup uses the core `fake-seed` command and its
neutral `pkg/fake` implementation.
Configure the authorized local API/frontend, database, Git and connector fixtures
before `task test-e2e`. The task defaults to the retained critical profile; set
`BRAIDPLANE_BROWSER_PROFILE=full` explicitly for the complete inventory. Runtime
startup and fixture provisioning require isolated local resources.
Do not treat successful discovery or parser tests as browser acceptance.

`scripts/ci-browser.sh` provisions disposable PostgreSQL, Git and Mailpit
fixtures in a fresh CI checkout, runs the critical profile and rejects an
incomplete report. It permits plaintext connectors and trusts forwarded client
addresses only within that loopback test environment. The hosted workflow is
still awaiting GitHub-hosted and public-fork qualification.

Existing explicitly provisioned isolated fixtures can supply the required
`E2E_*` identities/slugs and skip automatic seeding with `E2E_SKIP_FAKE_SEED=1`.
Use synthetic localhost credentials and separate sessions per browser. The
configured suite retains failure traces/screenshots; evidence-mode screenshots
remain opt-in. Do not capture credentials or provider secret screens.
