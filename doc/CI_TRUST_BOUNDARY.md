# CI trust boundary

Treat every pull-request checkout, script, test, dependency, generated file and
cache entry as untrusted. A maintainer may approve a workflow run, but its code
must still run without release or deployment authority.

## Where validation runs

Community validation runs on disposable standard GitHub-hosted `ubuntu-24.04`
VMs. Keep it off persistent private, legacy and product runners. Different
runner labels or services on one host still share that host. Access to its
Docker socket grants host administration.

The validation token can only read repository contents, and checkout discards
its credential. Validation jobs have no signing, package-write, deployment or
infrastructure identity. They have no deployment environment or private source
token. Fixtures and credentials are synthetic. Hosted VMs can reach the
internet; this setup does not block general network access.

Keep outside-fork workflow runs awaiting maintainer approval until the public
checks verify fork approval, token denial and runner exclusion. Both repository
and organization settings must prevent public
candidates from using private runners. See [public contribution acceptance](../COMMUNITY_BUILD.md#public-contribution-acceptance).

## Caches and artifacts

The `community-` caches hold public dependencies, compiled tools, browser
binaries and fixture image layers. Keep credentials, runtime volumes, sessions,
keys and Docker daemon state out of them. A cache miss must rebuild and run the
same required suites. PR code can alter restored files. A cache hit or an output
digest alone cannot prove that those files came from a trusted build.

Only the `verify` job on `main` saves Go build and lint caches. PR and runtime
matrix jobs restore those caches but do not save separate large copies that
compete for the repository's cache allowance. Keys retain their toolchain and
dependency partitions and cross-commit restore fallback. Cold runs bypass both
restore and save. Dependency, browser and container caches keep their existing
policies. Cache eviction can still cause a cold build; all checks remain required.

Validation builds images locally and uploads no artifacts. Release jobs must
use fresh VMs and an immutable source revision explicitly approved for release.
Give them separate identities with only the permissions each job needs. Never
restore Community caches, runner homes, executables or candidate artifacts into
a release job. Check setup actions for implicit caching, and check remote
Buildx caches too. Cache prefixes alone do not control access; GitHub also
scopes caches by repository and ref.

`core-release-plan.yml` accepts only completed, successful push-to-main Core CI
for its exact current main source. It checks required jobs again, computes a
version from merged reviewer labels and dispatches `core-release.yml` using the
reviewed source configuration. Its token can read source/PR metadata and dispatch Actions workflows, but cannot
write source/PRs, sign, publish packages or use deployment environments/secrets. It checks out trusted main code with no contribution cache or artifact
restore. PR CI, branch dispatches and stale main runs cannot dispatch candidates.
See [automatic candidates](CORE_RELEASE_AUTOMATION.md#reviewer-labels-and-automatic-candidates).

The dispatched `core-release.yml` workflow prepares OCI archives when
`CORE_RELEASE_PREPARATION_ENABLED` is `true`. Before enabling preparation, review
the source, version, platform, external image digests and artifact exposure.
Supply the successful main-push CI run and attempt for that exact source;
preparation checks them before building with a read-only token. Review the CI
admission, generated plan/changelog/input receipt, approval document and release
record in the job summary. The generated metadata lives in the candidate and
installation archive; the source checkout remains clean.

Publication requires `CORE_RELEASE_PUBLICATION_ENABLED=true` and the main-only
`core-release` environment. Under the v0 standing policy, that environment has
no required reviewers or wait timer; administrator bypass stays disabled.
Reviewed source and release labels authorize automatic v0 RC publication, including
breaking changes marked in the notes. The publisher validates the exact identity
document digest, candidate bytes, source and successful main CI before copying
archives by digest. Only the protected main publication job has signing/package
authority; contribution jobs cannot acquire it.

Stable publication requires an explicit main-branch promotion dispatch. It verifies
the original run and all ten signed subjects again, and reuses candidate bytes.
RC publication and promotion share a concurrency group.

Each run publishes its own candidate. A preparation-only completed run cannot be
promoted later just by enabling a flag; prepare a fresh candidate from the
reviewed workflow. Public artifact exposure is part of enabling this flow.
Disable preparation/publication when qualification or provider controls are
unavailable. Repository variables and the environment name alone do not enforce
main-only source, immutable identity or complete subject verification.

A private distribution validates and releases its own build from a reviewed
Core source revision. Public PR jobs must never receive its reader, updater,
signing or deployment identities. Verify the private CI, release workflows and
host controls separately.

## Local checks and review

Run the focused regression check with:

```sh
bash scripts/go-toolchain.sh test -short ./ci
```

`task test-go` also runs this package. The test checks which workflows and local
composite actions exist, their triggers and token permissions, runner selection,
checkout credentials, secret references, cache paths and namespaces, action
pins, and local-only image builds. It also checks the manual release
workflow's trigger, guard, environment name, runners, permissions and action
pins. It applies synthetic unsafe changes and checks that each one fails.
Review workflow and action changes for their execution and access boundaries.

A PR can change its own tests or shell commands, so this check cannot contain
malicious scripts. Review changes to workflows, actions, tests and toolchains
before approving execution. Protect those files with the repository's required
review policy.

Version selection, immutable source/artifact tags and interrupted-run recovery
are documented in [Core release automation](CORE_RELEASE_AUTOMATION.md). Configure
and verify its tag rules before granting the protected publisher contents-write
permission. Publication verifies every expected subject and signer before marking
the identity complete.

## Checks after publication

Local tests check the files in the repository. After authorized source
publication, record the exact source and workflow revision with the results of
these runtime checks:

- Use a real external fork and synthetic canaries to verify approval behavior,
  a read-only token, and denied secret, package, release, signing and deployment
  access.
- Verify that public candidates cannot reach repository or organization private
  runners or product administration paths. Confirm that privileged metadata jobs
  never check out candidate code.
- Safely probe for persistence through runner homes, caches and artifacts.
  Verify that later trusted jobs reject candidate output for promotion.
- Run every required suite on hosted VMs with cold and warm caches, retaining
  logs that explain failures.

Leave unavailable checks pending. If a trust control fails, disable contribution
execution, retain redacted evidence, fix the failure and repeat the affected
checks.

GitHub documents [untrusted checkout risks](https://docs.github.com/en/actions/reference/security/secure-use)
and [cache access rules](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching).
Recheck platform behavior and account controls during public acceptance.
