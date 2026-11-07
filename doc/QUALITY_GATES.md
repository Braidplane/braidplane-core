# Community core validation

Run commands from the core repository root. Use the pinned
`bash scripts/go-toolchain.sh` wrapper for Go; keep its diagnostics unabridged.
The CLI lives under `cli/` in the root Go module. No check requires private
source, credentials, infrastructure or an installed agent skill.

## Select checks for the changed behavior

| Change | Checks |
|---|---|
| Go behavior | Focused package tests through the Go wrapper; `task test-go` for all hermetic core packages |
| CLI behavior or contracts | `task test-cli`; API contract changes also require `task generate-api` |
| Browser JavaScript | `task test-js`; JavaScript lint runs in `task lint` |
| Templ source | `task generate-templ` and affected Go view/handler tests |
| API schema | Edit `api-schema/schema.yml`, run `task generate-api`, test affected handlers and CLI consumers |
| SQL/schema | `task generate-sqlc` against a disposable core-only database; it generates and vets SQLC queries |
| Runtime/browser journey | Relevant actual Playwright cases using owned fixtures; follow [browser coverage](../frontend/e2e/README.md#community-coverage-policy) |
| Build inputs or dependency boundary | `task build`; `task containers` for all four core image targets |
| CI workflow or local action | `bash scripts/go-toolchain.sh test -short ./ci`; review [CI trust boundary](CI_TRUST_BOUNDARY.md) and run affected script checks |
| Documentation only | `task docs-check`; check commands against this checkout and execute changed operational procedures in owned fixtures |

Focused root package example:

```sh
bash scripts/go-toolchain.sh test -short ./service/pipelinesvc
```

Focused CLI package example:

```sh
bash scripts/go-toolchain.sh test -short ./cli/cmd/braidplane
```

Unit tests remain hermetic. Dependency-backed tests live in `e2e` packages and
need explicitly provisioned disposable fixtures. Keep normal Go caching;
`-count=1` is appropriate when a fresh external fixture requires a new execution.
A skipped integration test does not prove runtime behavior.

## Full core verification

`task test` runs core and CLI hermetic tests, JavaScript tests and Mailpit fixture
checks. `task lint` runs the pinned core/CLI Go linters, JavaScript lint and
package-boundary tests. `task build` builds CSS and all five executables.

[COMMUNITY_BUILD.md](../COMMUNITY_BUILD.md) documents the environment for
`task ci`. This task migrates a disposable core-only PostgreSQL database,
regenerates API/CLI/Templ/enums/wrappers/SQLC, tests, lints and builds. Set both
`DB_*` and `DATABASE_URL` to that same intended fixture. Never use retained data
for these commands. Review generation diffs and commit
source and output together; never hand-edit generated files.

The [core CI workflow](../.github/workflows/ci.yml) runs that task and rejects
uncommitted generated changes. Its Go E2E job runs `task test-go-e2e`, which
uses `go list` to discover every package under an `e2e` path that contains Go
tests. No package or test-name allowlist is maintained. Packages run sequentially
with fresh executions and package-owned fixtures. The gate requires every
discovered package to finish successfully with passing tests, and rejects any
skip, test failure, empty discovery or missing package result.

Run the same command locally with Docker, OpenSSL, Python 3 and matching PostgreSQL `pg_dump` and
`pg_restore` clients available. Provider, SPIRE, SMTP and private-network fixtures
are created and cleaned up by their owning suites. The native crash/rescue test
uses the real hour-long River timer; allow up to 90 minutes per package. CI supplies
recovery clients from the same pinned PostgreSQL 18.6 image as the disposable
server, mounting only the job-owned temporary directory. These wrappers use
Linux host networking. Browser E2E tests run in the separate browser job.

Its separate container matrix builds backend, frontend, CLI and migrator, and
checks all four administrator subcommands' help in the backend image without
network access. Local execution is distinct from hosted CI results.

## Persistence changes

Applied migrations are immutable and forward-only. Add the next numbered
`.up.sql` file under `postgres/migrations/`; preserve the existing catalog and
update `manifest.sha256` as `<highest version> <catalog digest>` followed by a
newline. The catalog digest is SHA-256 of the numerically version-sorted lines
`<filename>\t<SHA-256 of raw file bytes>\n`, matching
[`MigrationCatalogIdentity`](../postgres/migration_catalog.go). Preserve raw
migration bytes, including line endings. Core must continue installing
independently with only `core_schema_migrations`.

Validate SQLC generation/vetting and actual migrations on disposable databases,
including tenant boundaries and relevant failure paths. Never reset a shared
ledger or infer adoption from successful fresh installation.

## Evidence

Record the exact revision, command, pass/fail/skip results and fixture scope.
Browser discovery proves selection only. Prefer Vitest for UI logic and
Playwright for critical paths. Inspect manually when requested or when automated
checks cannot verify the change. Use synthetic identities and never capture
credentials or provider secret screens.
Keep local, hosted CI, release, deployment and provider acceptance separate.
