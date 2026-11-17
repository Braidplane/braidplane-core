# Core release versioning and recovery

The normal flow is:

**Merge reviewed, labeled change → successful main CI → version and notes →
built candidate → verified GitHub prerelease → explicit stable promotion.**

There is no release-plan PR or version-only source commit. Generated metadata
stays outside the clean checkout and is bound to the exact candidate. All
eligible `v0` candidates publish automatically as prereleases. An explicit
promotion dispatch authorizes each stable release.
Only pre-v1 versions are supported by the current version-selection policy.

## Reviewer labels and automatic candidates

When opening or updating a PR, apply exactly one classification label and read
it back from GitHub before handoff. Contributors without label permissions state
the required classification for a maintainer to apply. Preserve an existing
reviewed choice; the reviewer confirms classification before merge.

| Label | Version choice |
|---|---|
| `release:patch` | Compatible bug or performance fix; next patch |
| `release:minor` | New feature; next minor, patch reset to zero |
| `release:breaking` | Incompatible change; minor bump and explicit breaking-change notes |
| `release:none` | Docs, tests, CI or tooling without a distributed behavior change |

Add `release:security` only for a disclosure-approved expedited security release.
It marks the notes and records the preceding version as superseded. Review
compatibility across API, CLI, configuration, connectors and installation/migration
contracts. Breaking changes are normal during v0 development and are marked
**Breaking change** in release notes; they follow the same candidate and
promotion path as other changes. Labels declare compatibility review; the tool does not
infer it.
Missing, conflicting or unsupported labels stop preparation until corrected.
The separate `PR release classification` workflow checks current GitHub labels
when a PR opens, reopens, receives commits, or gains or loses a label. It reuses
the release planner's classification rules without rerunning product CI on label
edits. Configure its `Release labels` check as required in the main branch
ruleset to block merges with invalid classifications. This workflow does not
verify disclosure approval for `release:security`; that remains a reviewer decision.
After fixing labels, rerun the failed automatic preparation run; there is no
need to rerun successful product CI just to select a release.

`core-release-plan.yml` listens for completed Community core CI runs. It accepts
only successful **push-to-main** CI for the exact current main source, and checks
the full required job inventory through `scripts.admit_core_ci`. PR CI, branch
rehearsals and stale main runs cannot dispatch a release candidate. It checks out
trusted main code with read-only contents/PR access and Actions write only to
dispatch the existing `core-release.yml`; it cannot write source, create PRs,
sign, publish packages, use deployment environments or restore contribution
artifacts/caches.

Eligible merged changes since the latest completed stable release are batched into
one candidate. The initial cursor is the reviewed source in `ci/core-release-start.json`.
Direct main commits without a merged PR stop preparation for operator reconciliation.
A `release:none`-only addition does not create a duplicate candidate. Reserved RC
numbers are never reused, even after an abandoned run. A feature or breaking change
selects the next minor target; its RC sequence starts independently. Only a completed
stable release advances the cursor.

The reviewed [release configuration](../ci/core-release.json) pins the selected
platform and external image digests. Change this configuration through normal review.
These pins do not independently qualify distribution or hardware support.

The dispatcher submits the source configuration and exact main CI run/attempt
to `core-release.yml`. That workflow checks CI again, selects the version when
its job starts and refuses changed configuration or source. This accounts for
an earlier queued candidate being published first; already-published or
`release:none` work cleanly skips publication. A main advance or
label/version race can safely stop an old dispatch; use the run for the current
successful main revision. Duplicate completion events do not start another
candidate for the same source.

## Authorization and enablement

`CORE_RELEASE_PREPARATION_ENABLED=true` enables automatic dispatch and public
candidate preparation. Review the merged source/configuration and exposure of
candidate artifacts before enabling it. The build is read-only and does not
publish images or sign artifacts.

Preparation produces `release-plan.json`, `release-inputs.json` and `CHANGELOG.md`
inside the candidate directory, not the repository. The input receipt records
its exact source and included PR classifications. The approval document binds
these bytes along with the image archives, compiled baseline and release record.
The installation archive contains those same metadata bytes. Its digest in the
signed record and archive provenance authenticates the notes and receipt without
another signing workflow or an additional source commit.

`CORE_RELEASE_PUBLICATION_ENABLED=true` enables automatic RC publication. The `core-release` environment must allow only `main`, have
no required reviewers or wait timer, and keep administrator bypass disabled.
Keep the immutable tag rules below active. Candidate validation enforces the
pre-v1 selection and exact source/configuration before registry or signing work.

The existing `approval.json` filename identifies the candidate's immutable
identity manifest; it does not imply a human approval step. The publisher checks
its digest, generated metadata, image/archive bytes, migration catalog and exact
main CI admission automatically. The flags can remain enabled for normal v0
development. Disable preparation/publication if qualification or provider
controls are unavailable; do not weaken checks or broaden permissions to recover.

## Immutable identities and publication

Protect `v*`, `core-candidate/**/*` and `core-published/**/*` tags against updates
and deletion, with no bypass actors. Creation remains allowed for the protected
publisher. Verify the provider rules separately; local create-only code cannot
prevent an administrator from changing rules outside this workflow.

Publication first reserves:

- `v0.X.Y-rc.N`: exact source revision shared by the candidate identity.
- `core-candidate/v0.X.Y-rc.N/linux-amd64` (or `linux-arm64`): exact approval digest,
  full artifact/migration record, source/workflow identity, candidate metadata
  hashes and hashes of the reviewed configuration, workflow and toolchain file.

The annotated tags contain JSON and point directly at the source commit. The
record binds the installation archive, every image digest and migration catalog.
The source also binds Dockerfile base/tool pins and dependency lockfiles. The
publisher reuses the existing OCI copy, image qualification, SBOM, provenance
and signing steps; contribution output is never promoted.

After signing, verification requires the expected public repository, workflow,
source/signer revision, hosted runner and current run/attempt for all ten subjects:
record and archive provenance, plus provenance and SBOM for each of four images.
Only then is `core-published/v0.X.Y-rc.N/linux-amd64` created and the GitHub prerelease published. A candidate tag or an
uploaded Actions artifact alone is not a completed release.

## Promote a candidate

Dispatch promotion from `main` with the candidate and platform:

```sh
gh workflow run core-release-promote.yml --repo Braidplane/braidplane-core --ref main -f candidate=v0.1.0-rc.2 -f platform=linux/amd64
```

The workflow selects the latest completed RC, checks its original successful run,
and verifies all ten signatures again. It does not rebuild. It creates the stable
version tag and GitHub Release with cumulative notes, then creates the stable
`core-published` tag after the release assets. RC publication and promotion share
a concurrency group with `queue: max`, so up to 100 pending runs wait without
replacing one another. Promotion selects the newest candidate source on main;
completion time breaks ties for the same source. The main-only `core-release` environment has no required
reviewers or wait timer, and administrator bypass is disabled.

The stable assets are identical to the candidate assets. The signed archive and
record retain the RC version; `promotion.json` binds the stable alias to the exact
RC identity. Stable release notes use the stable heading. Existing `v0.0.1`
remains available.

Downstream consumers pin source, migration and OCI digests. Retain verification
receipts, signature bundles, archive and identity JSON before Actions retention
expires. Tags retain identity, not downloadable archive bytes or run receipts.

## Interrupted and retried runs

1. Inspect the exact candidate run/attempt and retained journal. Missing
   `core-published/` means incomplete, even when registry images exist.
2. Preserve the candidate, approval digest, partial signatures and logs. Rerun a
   failed publication job in the same run while its original candidate is
   available. It validates those same bytes automatically. Digest copies can be repeated;
   verification requires the new attempt's signatures before completion.
3. A new build may produce different bytes. Never replace a reserved identity,
   delete tags or weaken checks. If the candidate was lost, prepare a
   newly reviewed version from the latest successful main source; the old
   reservation remains audit history and does not consume its pending changes.
4. For a terminal preparation failure before reservation, inspect the failure
   and retry that run. The dispatcher deliberately does not restart an existing
   run just because another completion event arrives. Manual `core-release.yml`
   dispatch remains available with the source configuration and exact successful
   main CI run/attempt. An optional expected version can refuse an unexpected
   selection. Incorrect values fail admission.
5. If source advances while a candidate is building or queued, its original
   artifact identity remains unchanged; newer main CI can queue a later candidate. An older candidate cannot reuse a
   version reserved for different source or bytes.

Superseding/security releases get new identities. Prior completed releases remain
available for audit and rollback decisions; database migration compatibility still
governs whether rollback is safe.

Local checks:

```sh
python3 -m unittest scripts.test_prepare_core_release scripts.test_core_release_version scripts.test_core_release_identity scripts.test_publish_core_release scripts.test_promote_core_release
bash scripts/go-toolchain.sh test -short ./ci
```

Local checks do not prove provider branch restrictions, tag enforcement, live
publication or signer acceptance. Record those results from the actual automatic
release run; a passing build or queued job alone is insufficient. See [CI trust boundary](CI_TRUST_BOUNDARY.md).
