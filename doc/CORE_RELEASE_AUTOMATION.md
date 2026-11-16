# Core release versioning and recovery

Release preparation and publication remain separate, explicitly authorized
operations. This automation does not enable either flag, change provider
settings, or grant approval. See [CI trust boundary](CI_TRUST_BOUNDARY.md).

## Reviewer labels and automatic release PRs

Before merging a product PR, the reviewer selects exactly one label:

| Label | Version choice |
|---|---|
| `release:patch` | Compatible bug or performance fix; next patch |
| `release:minor` | New feature; next minor, patch reset to zero |
| `release:breaking` | Incompatible change; explicit minor before v1 |
| `release:none` | Docs, tests, CI or tooling without a distributed behavior change |

`release:security` is an additional label for a disclosure-approved expedited
security release. It marks the notes and records the previous release as
superseded. Review compatibility across API, CLI, configuration, connectors and
installation/migration contracts; labels declare that review, not proof of it.
Missing, conflicting or unsupported release labels stop preparation. Fix the
merged PR's labels and dispatch preparation again; no changes are silently lost.

On each main push, `core-release-plan.yml` uses trusted main code and merged PR
metadata to create or refresh one `automation/core-release/` PR. Its only changed
files are `release-plan.json`, `release-inputs.json` and `CHANGELOG.md`. Notes use
PR titles and numbers. The input receipt records the source snapshot, PRs and
labels. Pure `release:none` batches create no release PR. PR #136 is classified
`release:none`; the next eligible product change starts the first release PR.

The default cadence batches pending merges into that release PR. A later feature
merge upgrades a pending patch to a minor release. Refreshes need review again;
configure the main review policy to dismiss stale approvals and require an
up-to-date branch. The bot refuses to overwrite reviewer edits or force-update
its branch. Before building artifacts, a source check requires that the reviewed
plan merge is the only change after its recorded source snapshot and changes
only those three files. An intervening code merge therefore requires a refreshed
plan, even if the release PR was merged before the refresh ran. Use merge or
squash merge for refreshed release PRs, whose history includes merge commits.

The reviewed bootstrap source in `ci/core-release-start.json` excludes history
before this automation. Subsequent plans resume from `release-inputs.json`.
Direct main commits without a merged PR stop preparation for operator
reconciliation. Generated release-plan PRs are excluded only when they change
exactly the three allowed files. The automatic path selects the highest reserved
stable pre-v1 version, or `v0.0.0` initially. Maintenance-line backports use the
manual path below. Never reuse a reserved version for different source/artifacts.

The repository must allow GitHub Actions to create pull requests and contain the
reviewer labels above. The main-only job has contents/PR write access, plus
Actions write access solely to dispatch the existing read-only `ci.yml` for its
new head: events created with `GITHUB_TOKEN` do not ordinarily start PR CI.
It has no signing, package, deployment or secret authority, and never checks out
contribution code or restores contribution caches. Set
`CORE_RELEASE_PLAN_ENABLED=false` to disable automatic preparation. Neither a
label nor merging the generated PR publishes artifacts.

After the plan merges, dispatch the existing `core-release.yml` on that main
revision, with the plan's version, platform, external image digests and successful
main-push CI run/attempt. It still requires the separate artifact approval below.

## Manual selection and exceptional recovery

Create a synthetic-format changes file outside the checkout, for example:

```json
[{"kind": "fix", "summary": "Repair interrupted pipeline retries."}]
```

Prepare the version and changelog locally:

```sh
python3 -m scripts.core_release_version v0.1.0 /tmp/core-changes.json
```

The command writes `release-plan.json` and adds the selected notes to
`CHANGELOG.md`. Move any corresponding notes out of Unreleased during review;
unselected notes remain there. Review and commit both files through a PR.
For a feature or breaking change, add `--bump minor`. For an expedited security
patch, add `--security --supersedes v0.1.0`; include only disclosure-approved
notes. `supersedes` records older releases replaced by this release without
rewriting their identities. Coordinate private security work before public
preparation: candidates and their logs can expose the repair.

Dispatch `core-release.yml` on the reviewed main revision with that plan's
version, one platform, approved external image digests, and its successful
main-push CI run/attempt. The builder refuses a missing or inconsistent plan or
changelog. Each platform requires its own reviewed artifact approval.

## Immutable identities and publication

Before enabling publication, the repository owner must configure tag rules for
`v*`, `core-candidate/**` and `core-published/**`: allow the release identity to
create tags, prohibit updates and deletion, and exclude bypass for release jobs.
Verify those provider controls separately. Local code uses create-only API calls
and refuses an existing tag with a different source or annotation; it cannot
prevent an administrator from changing tags outside this workflow.

The protected publication job receives contents-write permission to create
these identities. OCI candidate preparation and contribution CI remain read-only;
the separate main-only release-PR job has the metadata permissions described above.
The protected environment approval covers the exact `approval.json` digest,
source revision, workflow, plan, image and archive bytes shown in the summary.

Publication first reserves:

- `v0.X.Y`: reviewed source revision shared by all platforms.
- `core-candidate/v0.X.Y/linux-amd64` (or `linux-arm64`): exact approval digest,
  full artifact/migration record, source/workflow revision, and hashes of the
  reviewed plan, input receipt (when present), changelog, workflow and pinned toolchain file.

These annotated tags contain JSON and point directly at the source commit.
The approval digest binds the record, image archives and compiled baseline.
The record binds the installation archive, every image digest and the migration
catalog. The source revision also binds Dockerfile base/tool pins and lockfiles.
The publisher reuses the existing OCI copy, qualification, SBOM, provenance and
signing steps; it never promotes contribution artifacts or restores their caches.

After signing, the workflow verifies the record, installation archive, and both
provenance and SBOM for each of the four image subjects. Verification requires the
expected public repository, workflow, source/signer revision, hosted runner and
current run/attempt. Only then does it create
`core-published/v0.X.Y/linux-amd64` with the same identity annotation. A candidate
tag alone is not a completed release. Downstream consumers resolve the published
tag, read its record, and pin source, migration and OCI digests; they must not use
mutable image tags or infer success from an uploaded Actions artifact.

Keep the verification receipts, signature bundles, installation archive and
identity JSON from `core-release-evidence-*` in the approved release evidence
store before Actions retention expires. The Git tag retains the source/artifact
identity, not the archive bytes or run-specific verification receipts. Publication
of a download page or GitHub Release is a separate operation.

## Interrupted and retried runs

For release-PR preparation, rerun the workflow on current main:

```sh
gh workflow run core-release-plan.yml --ref main
```

An interrupted run reuses an identical branch for the same source and notes.
If main advanced before a PR was created, it prepares a fresh branch; the unused
branch remains untouched. A pending PR is refreshed with fast-forward commits.
Concurrent or custom reviewer edits cause refusal. Preserve those edits and
reconcile the plan, notes and input receipt manually against current main before
merging. Do not change the generated checksum to bypass the edit check.

If a stale plan already merged but has no artifact reservation, preparation
creates a repair PR covering its original inputs and intervening merges. Once a
version is reserved, publication retries must keep its original candidate bytes.
For an abandoned reserved version, prepare a new reviewed version using the
manual command above, including the abandoned changes and `--supersedes` version.
When `release-inputs.json` exists, retain its input records and update
`through_sha` to the main revision used to prepare that recovery PR; review the
receipt with the notes. The recovery PR must change only the same three files
and merge directly after that source snapshot. This is an exceptional operator
recovery, not an automatic abandonment or tag rewrite.

For artifact publication:

1. Inspect the exact run/attempt and retained publication journal. Missing
   `core-published/` means incomplete, even when some registry images exist.
2. Preserve the candidate artifact, approval digest, signature bundles and logs.
   If publication failed, rerun the failed publication job in the same run while
   the original candidate artifact is available. This consumes the original
   bytes and checks the original reservation. Reapprove the exact artifact in
   the protected environment. Completed digest copies can be repeated safely;
   verification must accept signatures from the new attempt before completion.
3. A fresh preparation or rerun of all jobs can produce different archive or
   image bytes. If its approval differs from the reserved identity, stop. Do not
   force-update, delete tags, clobber artifacts or weaken the check. Prepare a
   newly reviewed patch version. A reservation remains an audit trail even when
   abandoned. If the original candidate has expired, use a new version.
4. If verification fails after signing, retain the partial evidence and inspect
   the specific signer, subject or registry failure. Retry with the same candidate
   and current-attempt signing. Never create a completion tag by hand.
5. A later superseding or security release gets a new plan, version, source and
   approval. Older published identities remain available for audit and rollback
   decisions; migration compatibility still governs whether rollback is safe.

Local regression checks:

```sh
python3 -m unittest scripts.test_prepare_core_release scripts.test_core_release_version scripts.test_core_release_identity
bash scripts/go-toolchain.sh test -short ./ci
```

Local tests do not prove environment reviewers, tag rules, public-fork denial,
registry publication or live signer acceptance. Qualify those controls in a
separately approved exact-artifact release rehearsal.
