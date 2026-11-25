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
| Connectors | Start with the integration status in the [individual data-source guides](connectors/index.md). The release matrix lists preview and experimental operations. Look up the read or write operation and authentication method your pipeline needs, then check its limits and provider test results. No connector has production-ready status in this release. [v0.2.0 operation and evidence matrix](https://github.com/Braidplane/braidplane-core/blob/v0.2.0/docs-public/connectors/support-levels.md) |
| Upgrades | Keep the application and CLI on the same release. During Alpha, minor versions may break APIs, generated clients or CLI behavior without a deprecation period. For example, v0.2.0 changes how HubSpot keyed upserts handle null and blank properties. Check the release notes and test clients against the target schema. [Tagged compatibility policy](https://github.com/Braidplane/braidplane-core/blob/v0.2.0/docs-public/api/alpha.md) |
| Database recovery | Migrations run forward. Back up the database and its encryption keys before upgrading. After a failed upgrade, keep applications stopped and restore the matching database and keys into isolated storage. Reconcile external writes before resuming service. Service-resumption tooling still needs qualification. [Tagged recovery guide](https://github.com/Braidplane/braidplane-core/blob/v0.2.0/scripts/installation/RECOVERY.md) |

## When is a pipeline complete?

Evaluate the deployed version with an input whose expected output you know.
In run details, check the version, the source and transform steps, and the
delivery outcome for **each intended destination**. Then compare the destination
records or file with the expected keys, values and row count. A successful empty
run can be correct if you expected zero rows. For an incremental read, run a
second window with a known change and check which records it delivers.

| What you see | How to interpret it in v0.2.0 |
|---|---|
| Source or transform failed | Repair the cause. Starting a new run reads current source data, which may have changed since the failure. |
| Sink reports `confirmed` or `no_op` | Core recorded delivery as complete, or skipped dispatch because the input was empty. An unvisited sink has no delivery record. Check each sink separately, including on failed runs. |
| Sink reports `failed` or `unknown` | `failed` means this attempt failed before dispatch. With `unknown`, the provider may already have applied the write. Inspect the destination before starting another run. |
| Retained-input recovery is available | Recovery uses the original version and saved outputs, skips confirmed repeat-safe writes and leaves the source checkpoint unchanged. For a deterministic S3 overwrite, matching object contents and metadata can confirm an uncertain write without a second upload. Expired inputs or an unsafe attempted write can block recovery. |

The tagged worker-loss rehearsal changed source data after a crash and checked
that recovery used the original inputs and produced one write per effect on its
S3 fixture. Other destinations need evidence for their own write semantics.
[Sink outcomes and recovery limits](https://github.com/Braidplane/braidplane-core/blob/v0.2.0/doc/PIPELINE_SINK_RECOVERY.md)
· [Worker-loss rehearsal](https://github.com/Braidplane/braidplane-core/blob/v0.2.0/doc/PIPELINE_WORKER_RECOVERY.md#bounded-synthetic-qualification)

## How to evaluate a connector operation

Choose a read or write operation and the authentication method your pipeline
will use. After testing the connection, verify that the operation handles the
expected data and that you can account for the result after a failure. Use the release's
[connector evidence record](https://github.com/Braidplane/braidplane-core/blob/v0.2.0/docs-public/connectors/support-levels.md#evidence-record)
to see which checks have fixture coverage, historical provider evidence or
current live verification.

| Scope | What to verify |
|---|---|
| Read | Fetch known records across a page or batch boundary. Verify keys, counts and schema at the destination. For incremental modes, interrupt and resume across a checkpoint and document how late updates and deletes are handled. |
| Write and recovery | Verify the expected provider state after a successful write, rejection before dispatch and loss of acknowledgement. Show which writes can be confirmed, safely retried or must remain blocked. Record how an operator reconciles an uncertain outcome. |
| Credentials and provider limits | Exercise the claimed authentication mode through expiry or revocation and repair. Verify the effect of throttling, timeouts and configured size limits on completeness. |

Record the release, provider version, environment, operation, date, observed
result and remaining limits. Mark checks outside the claimed scope as inapplicable.
For a read-only operation, for example, verify read completeness and omit sink recovery.

## Criteria for v1.0

The [v1.0 milestone](https://github.com/Braidplane/braidplane-core/milestone/1)
tracks the issues currently planned for the release. Its scope can change as
testing reveals gaps.

| Criterion | Status at v0.2.0 |
|---|---|
| Publish a tagged build with passing Core CI and verifiable artifacts. | Met for v0.2.0. Repeat for the v1.0 candidate. |
| Publish the supported API and CLI contracts, including how long deprecated behavior remains available. | Open. The Alpha policy allows breaking changes without a deprecation period. |
| Demonstrate an end-to-end pipeline on the candidate with expected source and destination records, then interrupt execution and verify recovery or safe refusal for each claimed delivery mode. | Open. The v0.2.0 worker-loss rehearsal covers a bounded S3 workflow. |
| Demonstrate installation, upgrade from the preceding supported release and restoration of data and keys on each supported platform. | Open for the v1.0 candidate. Publish the tested versions and recovery results. |
| Name the connector operations supported for production and publish provider tests for their authentication, writes and recovery where applicable. | Open. The v0.2.0 matrix lists preview and experimental operations. |

The Core maintainer, kerak19, updates this page and the README release link
at each non-prerelease promotion, and updates the affected claims when support
levels, v1.0 evidence or hosted trial terms change.
