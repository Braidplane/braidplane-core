# Community build and validation

Build the Community backend, frontend and CLI from this repository.

## Toolchain and source

Use Bash, Git, Python 3, OpenSSL, curl, tar, xz, Go and Task. The wrappers select
Go 1.26.10, Node 24.19.0 and npm 11.17.0 from `ci/toolchain/versions.env`. They download public
releases into local caches; Go modules and frontend packages use their lockfiles.
The public CLI lives in `cli/`; shared neutral contracts live in `pkg/`.
Both use the root Go module. Build from this root
with `GOWORK=off`; no Community build tag is needed.

## Worktrees

Create a sibling checkout with `task worktree-create name=my-feature`. After
merging and archiving any evidence, run `task worktree-remove name=my-feature`
from the main checkout. Removal requires an attached branch preserving HEAD and
refuses the current, dirty, locked or unregistered checkout and symbolic links.
Preserve detached work on a branch before removal; unmerged branches are retained.

Ignored build output and caches are deleted with the checkout. Retained
`.worktree/community` keys and runtime state block removal: stop the owned
application, preserve its keys with its database, and move the retained directory
somewhere safe first. The task does not stop services or delete Docker resources.

## Executables and unit tests

```sh
task build
task test
task lint
```

`task build` produces `out/braidplane-server`, `out/braidplane-admin`, `out/frontend`, `out/migrate`,
`out/braidplane` and the ignored `frontend/assets/css/app.css`. The server runs
API, worker and executor roles. The standalone frontend uses Community routes.

Tests use synthetic fixtures. `test-go` excludes dependency-backed `e2e` packages;
`task test-go-e2e` discovers and runs all Go E2E packages without a package or
test-name allowlist. Packages run sequentially on their disposable fixtures;
Docker, OpenSSL, Python 3 and matching PostgreSQL recovery clients
must be available. Suites own their provider, SPIRE, SMTP and private-network
fixtures. The native crash/rescue rehearsal takes about an hour; the command
allows 90 minutes per package. Missing, skipped or failed packages fail the task.
`test-cli` and `test-js` cover the public CLI and browser-side logic. Browser
journeys use `task test-e2e` and its documented runtime fixtures.
`test-go` prepares the pinned CUE tool before running tests so tool compilation
is outside the per-test timeout.

For local Unix-socket daemons selected through Docker contexts, Go E2E may report
`rootless Docker not found` even when `docker info` succeeds. Confirm that the
current context is your local test daemon, then export its endpoint for Go clients:

```sh
docker context show
docker context inspect --format '{{.Endpoints.docker.Host}}'
export DOCKER_HOST="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
task test-go-e2e
```

If Docker's automatic network pools are exhausted, set
`BRAIDPLANE_E2E_NETWORK_SUBNET` to an explicitly chosen unused IPv4 CIDR before
running S3-backed tests. Check it against Docker networks and host routes first.
The value configures only the new package-owned network; normal cleanup and the
resource reaper remain enabled. An unset value uses Docker's automatic pool.
An explicit CIDR can serve only one fixture package at a time: run a single
package, use Go's `-p 1`, or choose separate unused CIDRs for concurrent runs.
No existing network or daemon configuration is changed. Package-owned fixture
ports bind to loopback; FTP retains its explicit passive-port mapping.


## Code generation and database validation

Prepare a disposable core-only PostgreSQL database. Set `DB_HOST`, `DB_PORT`,
`DB_USER`, `DB_PASSWORD`, `DB_DATABASE` and `DB_SSL_MODE` for the migrator. Set
`DATABASE_URL` to that same database for SQLC. Credentials belong in the process
environment. These commands do not start or reset a database:

```sh
task migrate-run
task generate
```

The core catalog uses `core_schema_migrations`. `generate` projects the public
API, CLI client, Templ templates, enums, logging wrappers and SQLC outputs. It
also prepares SQLC queries against the selected database. Commit source and
regenerated files together. `task ci` applies the core catalog, regenerates,
tests, lints and builds. CI checks the generated tree remains clean.

Community installs use the core catalog. Never apply a fresh baseline over
existing application tables.

## Containers

One Dockerfile owns four targets, each using public digest-pinned base images:

```sh
docker build --target backend -t braidplane-core-backend:local .
docker build --target frontend -t braidplane-core-frontend:local .
docker build --target migrate -t braidplane-core-migrate:local .
docker build --target cli -t braidplane-core-cli:local .
```

`task containers` builds all four locally. Images run as UID/GID 65532. The
backend includes Git/OpenSSH and TLS roots; the frontend includes built CSS,
static assets and canonical icons. The migrator includes the core catalog.
The CLI has a writable temporary home; mount a protected configuration directory
when persistent authentication is required.

The checked-in `config.toml` supplies local defaults. Configure reachable
container database/API addresses and other settings through `CONFIG.md`; local
`localhost` defaults refer to the container itself. Provider credentials and
email delivery are needed only for the configured product flows. GitHub App configuration is optional. An incomplete App configuration is rejected.

## Run locally

```sh
task dev
```

This development command explicitly enables public signup unless
`SELF_SERVICE_REGISTRATION_ENABLED=false` is set. Normal configuration defaults
to closed registration.

This builds the executables and CSS, starts loopback PostgreSQL and Mailpit,
applies core migrations, and runs API, worker and frontend processes. Open
<http://127.0.0.1:3002> and create an account. Verification messages appear in
Mailpit at <http://127.0.0.1:38025>. GitHub and Google credentials are optional;
configure them only to use their integrations.

Ctrl-C stops the three application processes. The database and Mailpit remain
available; `docker compose -p braidplane-community stop` stops them. Run
`task dev` again to restart. Keep the same Compose project and containers.
Deleting containers or volumes can discard the development database.

Application keys are created once in `.worktree/community/keys.env` with mode
0600. Keep them with the database: encrypted data requires its original key.
Startup refuses to generate replacements when a database container already
exists. Mailpit's local TLS identity is also reused. Logs are written to
`.worktree/community/*.log`. These synthetic credentials and certificates are
for local development.

Defaults use Compose project `braidplane-community` and ports 35432 (database),
31025 (SMTP), 38025 (Mailpit), 3001 (API), 3002 (frontend) and 3003 (worker).
Override `COMPOSE_PROJECT_NAME`, `DB_PORT`, `MAILPIT_SMTP_PORT`,
`MAILPIT_WEB_PORT`, `SERVER_PORT`, `FRONTEND_SERVER_PORT` or
`COMMUNITY_WORKER_PORT` before starting if those names or ports are in use.
Each process needs a distinct port.

For a closed installation, use the local
[first-administrator bootstrap](doc/INSTANCE_ADMIN.md#first-administrator-with-closed-registration).
With development signup enabled, create and verify the first account normally, then use the local
[instance administrator command](doc/INSTANCE_ADMIN.md) to grant its global
administrator role. Account creation never grants that role automatically.
The same command manages additional administrators and prevents revoking the
last one.

Browser and connector E2E fixtures have separate requirements; see
[frontend E2E](frontend/e2e/README.md). Starting the application alone does not
qualify those journeys.

## CI and artifacts

The [CI trust boundary](doc/CI_TRUST_BOUNDARY.md) describes validation isolation,
cache rules and release safeguards.

`.github/workflows/ci.yml` uses disposable `ubuntu-24.04` GitHub-hosted runners,
read-only repository tokens and synthetic fixtures. It regenerates, tests,
lints and builds Core and the CLI, builds the docs strictly, runs every Go E2E
package through `task test-go-e2e`, runs the critical browser profile, and builds
all four container targets. The Go E2E packages run sequentially on fixtures they
own; skipped or missing tests fail the check.

The `community-gate` job has no token permissions or checkout. It fails if any
required validation job fails, is cancelled or is skipped. Protect changes to
workflows and checks with required review: a PR can change its own check code.

Caches hold dependencies, compiled tools, browser binaries and fixture image
layers. Runtime volumes, credentials, sessions, keys and Docker daemon state
stay out of them. A cache miss must run the same required suites. Validation
uploads no artifacts and does not publish images or deploy.

For an authorized maintainer rehearsal without repository cache reuse:

```sh
gh workflow run ci.yml --ref <reviewed-branch> -f cold_cache=true
```

The dispatch-only input defaults to false. It skips repository cache restore
and save, pulls fixture images without archived layers, and disables Docker
cache import/export and layer reuse. The required suites and job timeouts stay
the same. Fresh hosted VMs still include GitHub's preinstalled software.

### Public contribution acceptance

Keep outside-fork workflow runs awaiting maintainer approval until the actual
public destination passes its fork and trust checks. Before approving a
controlled fork rehearsal:

1. Read back fork approval, token and secret-delivery policies and repository
   and organization runner access. Public candidate code must use disposable
   hosted VMs, with no access to private runners or product administration.
2. Run a real external fork PR against the exact candidate. Record required-suite
   completeness, runner resources, approval behavior and denied write/secret
   access. A same-repository rehearsal cannot prove GitHub's fork controls.
3. Verify default-branch rules, require `community-gate`, and protect workflow
   changes with the approved review policy.
4. Review the fork, cache, artifact and runner-isolation results with the trust
   owner. Keep ordinary outside-fork runs unapproved if any required check fails.

Hosted runners have Internet access. Read-only tokens and absent secrets do not
establish network isolation. The release workflow remains disabled and needs
separate qualification and authorization. Release jobs must verify reviewed
source and artifacts without restoring Community caches.

Local checks, hosted CI, public-fork acceptance and artifact publication have
separate receipts. Builds and local startup do not qualify a distribution.

## Documentation

Install Python 3.12 and Task, then run the same gate used by CI:

```sh
task docs-check
```

The task creates `.worktree/docs-venv`, installs hash-locked wheels, runs gate
regression tests, checks the Markdown inventory and local links/anchors, and
builds `mkdocs.public.yml` with `--strict` into `out/docs`. It uses reserved
staging URLs and runs directly from this checkout.
See [documentation policy](doc/DOCUMENTATION.md) for navigation and exclusions.

For customer-owned secrets, network paths and remaining self-hosted qualification,
see [custody and network boundaries](doc/SELF_HOSTED_CUSTODY.md).
