# Contributing to Community core

This is the canonical source for Community implementation and the public CLI.
Core changes belong here.

## Propose a change

Search [GitHub Issues](https://github.com/Braidplane/braidplane-core/issues)
before opening a bug report or feature proposal. Use the supplied forms; no
private tracker account or internal tooling is required. Discuss substantial
changes before implementing them. Connector proposals should name the system,
operation and synthetic test plan; see [connector support](docs-public/connectors/support-levels.md).
Report vulnerabilities privately through [SECURITY.md](SECURITY.md).

Fork this repository, create a branch, and open a pull request against the
branch you started from. For setup, follow [COMMUNITY_BUILD.md](COMMUNITY_BUILD.md).
Start with focused checks in [quality gates](doc/QUALITY_GATES.md); regeneration
requires its documented disposable database. Use the
[pull request template](.github/PULL_REQUEST_TEMPLATE.md) to explain the problem,
behavior and validation, including any checks you could not run.

kerak19 maintains Community core and decides review priority, acceptance
and releases. Review covers correctness, tenant boundaries, compatibility,
rights and relevant test evidence. Review capacity is limited; there is no
promised review or merge date. Respond to feedback on the same PR. Maintainers
may request a smaller scope or close a proposal with a reason. Maintainers
handle any internal planning synchronization; GitHub remains the public thread.
Contribution CI remains subject to the public-control qualification described
in COMMUNITY_BUILD.md; a submitted PR is not a promise of automated execution.

## Contribution rights

By submitting a contribution, you confirm that you authored it or have permission
to submit it under this repository's Apache-2.0 license. Retain third-party
attribution and identify copied or generated material and its origin. Do not
submit confidential code or data. No separate CLA or DCO sign-off is required.
Maintainers may ask for provenance or rights clarification before accepting it.
See [release rights](doc/RELEASE_RIGHTS.md) for third-party exceptions.

Follow the [conduct and support guidance](SUPPORT.md#conduct).

## AI-assisted contributions

AI tools are welcome. Disclose substantial AI assistance in your PR description:
name the tool and explain what it helped with. Review and test the changes, and
be ready to explain your choices and respond to feedback. You remain responsible
for correctness, licensing and security. The contribution rights above apply to
AI-assisted work too.

Verify AI-discovered bugs or vulnerabilities before reporting them. Include
reproduction steps or other supporting evidence, and report vulnerabilities
privately through [SECURITY.md](SECURITY.md).

## Implementation and checks

Use an issue branch and describe the resulting behavior, affected contracts and
validation. Keep tenant data scoped to its organization and enforce permissions
at the HTTP boundary. Follow [package boundaries](doc/PACKAGE_BOUNDARIES.md) and
[configuration](CONFIG.md). Use existing packages, typed errors and validated slugs.
Keep source, fixtures and documentation self-contained. Use this repository's
GitHub issues for tracking references; exclude private tracker URLs and identifiers.

For API changes, edit `api-schema/schema.yml` first and regenerate the public
server/client and CLI projection. Commit generated output with its source. The
CLI executable and API projection live in `cli/`; shared pipeline contracts live
in `pkg/`. Run Go commands from the repository root or through the root tasks.
Keep the CLI backend-independent.

For pipeline features, follow the [pipeline guide](pkg/pipeline/AGENTS.md).

Follow [quality gates](doc/QUALITY_GATES.md) for affected checks and the
[design system](doc/DESIGN_SYSTEM.md) for product UI. Select checks for the changed
behavior. `task test` and `task lint` cover the full hermetic suite and policy
checks. Documentation edits need command/path
checks through `task docs-check`; run runtime checks when operational procedures
change. Use a
separate disposable core-only PostgreSQL database for `task ci`, which also
migrates, regenerates and builds. [COMMUNITY_BUILD.md](COMMUNITY_BUILD.md) documents
its required database environment. Never point fixture seeding at retained data.
Applied migrations are immutable and forward-only.

Review generated diffs and test behavior and failures at the layer that owns
them. Browser discovery verifies selection only; changed runtime journeys need
actual fixture/browser evidence. Use synthetic localhost identities and provider
fixtures. Builds, tests and review must not require private source or credentials.

Public CLI distribution must identify its exact core revision. Keep local build,
CI, release publication, deployment and hosted acceptance evidence distinct.
Publishing releases and deploying remain separate operations.
