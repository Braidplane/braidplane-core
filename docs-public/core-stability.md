# Core stability

**v0.2.0 is an Alpha release.** GitHub published it on 20 November 2026 by
promoting the tested v0.2.0-rc.25 artifacts with their original signatures and
digests. Both versions have Git tags. A tag ending in `-rc.N` identifies a
candidate and GitHub marks its release **Pre-release**. Use the latest
non-prerelease for the promoted build. [v0.2.0 release notes](https://github.com/Braidplane/braidplane-core/releases/tag/v0.2.0)
· [Latest non-prerelease](https://github.com/Braidplane/braidplane-core/releases/latest)

## What you can evaluate in v0.2.0

| Surface | Coverage and limits |
|---|---|
| Application, CLI and API | The released commit passed Go and CLI tests, Playwright journeys for Git import, binding, deployment and run inspection, Bruno API checks, fixture-backed integration tests and container builds. [CI results](https://github.com/Braidplane/braidplane-core/actions/runs/44733158740) · [Checks for this tag](https://github.com/Braidplane/braidplane-core/blob/v0.2.0/doc/QUALITY_GATES.md) |
| Connectors | Preview operations are suitable for bounded evaluations. Check the read or write operation and authentication method you need. Stripe is read-only. FTP is experimental and needs provider verification for explicit FTPS. No connector has production-ready status in this release. [v0.2.0 operation and evidence matrix](https://github.com/Braidplane/braidplane-core/blob/v0.2.0/docs-public/connectors/support-levels.md) |
| Upgrades | Keep the application and CLI on the same release. During Alpha, minor versions may break APIs, generated clients or CLI behavior without a deprecation period. For example, v0.2.0 changes how HubSpot keyed upserts handle null and blank properties. Check the release notes and test clients against the target schema. [Tagged compatibility policy](https://github.com/Braidplane/braidplane-core/blob/v0.2.0/docs-public/api/alpha.md) |
| Database recovery | Migrations run forward. Back up the database and its encryption keys before upgrading. To roll back, restore the backup with its matching application. [Tagged recovery guide](https://github.com/Braidplane/braidplane-core/blob/v0.2.0/scripts/installation/RECOVERY.md) |

## Criteria for v1.0

The [v1.0 milestone](https://github.com/Braidplane/braidplane-core/milestone/1)
tracks the issues currently planned for the release. Its scope can change as
testing reveals gaps.

| Criterion | Status at v0.2.0 |
|---|---|
| Publish a tagged build with passing Core CI and verifiable artifacts. | Met for v0.2.0. Repeat for the v1.0 candidate. |
| Publish the supported API and CLI contracts, including how long deprecated behavior remains available. | Open. The Alpha policy allows breaking changes without a deprecation period. |
| Demonstrate installation, upgrade from the preceding supported release and restoration of data and keys on each supported platform. | Open for the v1.0 candidate. Publish the tested versions and recovery results. |
| Name the connector operations supported for production and publish provider tests for their authentication, writes and recovery where applicable. | Open. The v0.2.0 matrix lists preview and experimental operations. |

The Core maintainer, kerak19, updates this page and the README release link
at each non-prerelease promotion, and updates the affected claims when support
levels, v1.0 evidence or hosted trial terms change.
