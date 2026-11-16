# Core release versioning and recovery

Release preparation and publication remain separate, explicitly authorized
operations. This automation does not enable either flag, change provider
settings, or grant approval. See [CI trust boundary](CI_TRUST_BOUNDARY.md).

## Review the version and notes

Batch eligible merges by default. A maintainer can select a single merge for an
expedited security release. Neither merging nor a PR label publishes anything.
Use `v0.0.0` as the baseline for the first release; thereafter supply the last
reserved version in the selected release line, including abandoned reservations.
Publication checks that this baseline exists and is current within that line.
Check `core-published/` separately to distinguish completed releases. Backports may use an older maintained minor
line. Never reuse a reserved version for another source or artifact.

Compatible bug fixes and performance fixes select the next patch automatically.
Features and incompatible changes require `--bump minor`; before v1, this resets
the patch number to zero. Review compatibility for the API, CLI, configuration,
connectors and installation/migration contracts. The tool checks the declared
classification; the maintainer reviews whether it is accurate. Pure docs, tests
and CI changes produce no release. Changes that affect distributed behavior,
including dependency repairs, must be classified by their user-visible effect.

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

The protected publication job receives contents-write permission only to create
these identities. Preparation and contribution CI retain read-only permissions.
The protected environment approval covers the exact `approval.json` digest,
source revision, workflow, plan, image and archive bytes shown in the summary.

Publication first reserves:

- `v0.X.Y`: reviewed source revision shared by all platforms.
- `core-candidate/v0.X.Y/linux-amd64` (or `linux-arm64`): exact approval digest,
  full artifact/migration record, source/workflow revision, and hashes of the
  reviewed plan, changelog, workflow and pinned toolchain file.

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
python3 -m unittest scripts.test_core_release_version scripts.test_core_release_identity
bash scripts/go-toolchain.sh test -short ./ci
```

Local tests do not prove environment reviewers, tag rules, public-fork denial,
registry publication or live signer acceptance. Qualify those controls in a
separately approved exact-artifact release rehearsal.
