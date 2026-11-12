# Community installation tooling

Core owns the Community single-host bundle, including its generic Compose and
Caddy assets. This candidate is still being tested. It has no approved public release or supported
platforms yet. See [the operator guide](INSTALL.md) for preparation,
startup, administrator bootstrap, shutdown and compatible image-set upgrades.
Preparation supports `--api-only` to omit the frontend and route TLS directly to
the API; lifecycle and upgrade commands preserve the selected layout.
The archive also includes a synthetic Git/SQL/Git first-result example and its
expected JSON output; the operator guide covers authoring, deployment and readback.

## Release inputs

The release owner supplies a JSON manifest with exactly `version`,
`core_revision`, `platform` and `images`. The revision is a full lowercase Git
commit ID. Platform is `linux/amd64` or `linux/arm64`. Images contains exactly
`backend`, `frontend`, `migrate`, `cli`, `edge` and `postgres`, each identified as
`repository@sha256:<64 lowercase hex characters>` without a mutable tag.

The application images must come from the same reviewed revision. Before release
promotion, verify their source revision, availability, provenance, redistribution
approval and hardware support. Manifest syntax checks cannot verify those facts.
The PostgreSQL volume layout currently targets PostgreSQL 18;
changing its image requires separate database upgrade qualification.

```sh
python3 -m scripts.installation.release release.json release.env
python3 -m scripts.installation.package release.json braidplane-install.tar.gz
```

Both commands preserve existing output. Packaging prints the archive SHA-256
and uses a fixed file list, deterministic archive metadata and per-file
checksums. It includes tools, Compose/Caddy assets, runtime grants, the manifest,
license/notice and operator guide. It excludes customer credentials, runtime
state, caches and private keys. It contains references to image layers; customers
download the layers separately. These commands do not publish or deploy anything.

The extracted tools require Python 3.11+, OpenSSL and Docker/Compose, with no
private checkout, Task, Go, npm, personal secret store or third-party Python
packages. Tests exercise preparation after extraction with source imports
disabled. Checksums establish byte identity; approved signatures and verification
roots must establish archive authenticity before customer execution.

## Core image payload qualification

`scripts.core_release_candidate` creates the installation manifest from Buildx
metadata for the four Core images and the selected edge and PostgreSQL digests.
It rejects missing or malformed image digests. The release workflow must still
verify the build metadata and qualify the published images.

`scripts.build_core_candidate` builds local OCI archives with a fresh builder
and a pinned BuildKit image. It checks each archive against its image digest and
platform, then prepares the installation manifest, archive and release record.
It writes `approval.json` with the source, version, platform, record digest,
image digests and OCI archive hashes for review. It does not publish images.
The candidate validator checks those files against an independently supplied
approval digest. `scripts.publish_core_candidate` checks that digest, the source
and CI run before copying the archives by digest with a pinned ORAS image. It
then checks the registry images and creates SBOMs. The manual workflow uses
`CORE_RELEASE_PREPARATION_ENABLED` to allow candidate-only builds after exact
main-push CI admission. Its read-only preparation job shows the approval document
and release record for review. Publication requires
`CORE_RELEASE_PUBLICATION_ENABLED=true` and the release owner's approval in the
`core-release` environment. Require that review, allow only `main`, and disable
administrator bypass before enabling publication. Review the exact approval
document and release record before approving the waiting job. Each workflow run
publishes its own freshly prepared candidate; a completed preparation-only run
cannot be promoted by changing a flag afterward. Clear both enablement flags
after the approved run. Review the Buildx and
GitHub CLI identities used by the hosted runner as well. The current candidate
contains four Core images, two external image references and an installation
archive; it does not prepare standalone CLI archives or an archive SBOM. Agree
the complete release asset set before relying on this workflow. Public CI and
release acceptance remain separate checks after Core is public.

Before building a release, `scripts.admit_core_ci` can check a specified GitHub
Actions run and attempt against the candidate revision. The run must be a
successful push to main with every required Core CI job and GitHub-hosted runner
metadata. The output is an admission record. The release workflow must consume
it and enforce the reviewed source and runner trust boundary. Public CI and
fork acceptance still need their own checks.

Build the `release-evidence` Dockerfile target from a fresh checkout on a fresh,
trusted builder for the manifest platform. Keep the manifest and output outside
the checkout. Do not import Community CI caches or use a shared BuildKit state.
Then compare the four Core image digests in the manifest with that checkout and
its build outputs:

```sh
docker buildx build --builder "$RELEASE_BUILDER" --no-cache \
  --platform "$RELEASE_PLATFORM" --target release-evidence \
  --output "type=local,dest=$EVIDENCE_DIR" .
python3 -m scripts.qualify_core_images "$RELEASE_MANIFEST" . "$EVIDENCE_DIR"
```

The verifier checks the platform, application binaries, frontend assets,
migration files and the catalog compiled into the migrator. It removes its
temporary containers. Use a clean checkout at `core_revision` and build the
evidence from that checkout on an isolated trusted builder for the same
platform. The check does not cover third-party images, base layers, notices,
the installation archive, signatures or provenance. It publishes nothing.
Release and public verification remain separate gates.

To create SPDX 2.3 inventories for the four Core image digests, run the
digest-pinned Syft container through:

```sh
python3 -m scripts.core_release_sbom "$RELEASE_MANIFEST" "$SBOM_DIR" "$DOCKER_CONFIG"
```

Use a new output directory and an isolated Docker configuration with only the
registry access needed for these images. The scanner mounts that configuration
read-only. Remove temporary registry credentials after the scan. The tool checks
the registry's raw manifest against each digest and rejects multi-platform index
digests. Use the individual platform image manifest digest for each inventory.
The tool also checks the image configuration's OS and architecture against the
release manifest. Generation validates basic SPDX structure only; signatures and
independent subject verification remain required.

`scripts.verify_core_attestation` uses `gh attestation verify` to check one
attestation. Obtain the expected digest, repository, workflow path, source SHA,
signer SHA, run ID, run attempt, version and platform independently of the artifact. The
script checks the verified subject, public signer certificate, run and
transparency timestamp. An SPDX predicate must contain a nonempty inventory.
The signed provenance predicate must identify the repository, commit, version,
platform, source dependency, workflow revision and run invocation. The release
workflow must generate this predicate; GitHub's default predicate does not meet
the policy. `scripts.core_release_provenance` prepares it in a public, manually
dispatched main workflow whose source and workflow SHA match the manifest.
Before signing, a trusted workflow must build and qualify the actual bytes.
The predicate alone does not prove that happened. The verifier writes the raw
GitHub CLI result. Public acceptance also requires the release workflow to build
and publish the signed record.

`scripts.verify_core_release` checks the approved record digest and the source,
workflow, release and migration identities. It verifies provenance for the
record, archive and each Core image, verifies the image SBOMs, and checks the
archive's installation manifest. It writes a receipt only if every check passes.
To run it, obtain `record.json` and `installation.tar.gz` from the release
evidence. Prepare an expectations JSON object from the approved release
and independently checked workflow run. It must contain exactly these fields:

- `record_sha256`: SHA-256 of the approved record, without a `sha256:` prefix.
- `repository` and `workflow`: GitHub owner/repository and workflow path, such
  as `.github/workflows/core-release.yml`.
- `source_sha` and `signer_sha`: full Git commit IDs for the source and workflow.
- `run_id` and `attempt`: GitHub Actions run ID and attempt as strings.
- `version` and `platform`: approved release version and platform.
- `migration_version` and `migration_digest`: approved catalog version as a
  positive integer and its lowercase SHA-256 digest without a prefix.

Use a new output directory and an isolated Docker configuration with read access
to the images. The command also requires GitHub CLI access to the attestations:

```sh
python3 -m scripts.verify_core_release record.json installation.tar.gz \
  expectations.json "$VERIFY_OUTPUT" "$DOCKER_CONFIG"
```

The output contains the individual verification results and, on success,
`verification.json`. If a check fails, preserve the partial output as evidence
and use a new directory for the next attempt. Remove temporary credentials after
the check. A receipt covers only the artifacts and checks described above.
Third-party image provenance, redistribution clearance and operational
qualification remain separate. The registry platform check still needs testing
against actual release images.
`scripts.core_release_record` prepares the record from a clean candidate
checkout and matching archive; it does not qualify image bytes. The release
workflow's publication job requires explicit enablement and protected review.
Retain the exact candidate's publication and verification receipts before
claiming release acceptance.

## Database and runtime boundaries

Runtime grants use the existing `scripts/self-hosted-runtime-grants.sql`.
`execution-grants.sql` adds narrowly scoped executor capabilities and read-only
migration identity, while denying tenant-table access. It rejects executor
privileges, inherited memberships and object ownership. PUBLIC function execution
is revoked, including the dedicated migrator's global default, so future
functions cannot implicitly expand executor access. Reapply the reviewed grants
after catalog changes. Application roles retain tenant authorization and normal
credential checks.

The readiness executable checks `/readyz` on local port 3001 for backend roles
and 3002 for frontend. Probes ignore proxies, reject redirects, require HTTP 200
and allow two seconds. The edge waits for frontend readiness. Docker restart
policies restart exited processes; unhealthy status alone does not restart them.

## Validation

```sh
python3 -m unittest scripts.test_installation_release scripts.test_installation_compose scripts.test_installation_prepare scripts.test_installation_preflight scripts.test_installation_lifecycle scripts.test_installation_package scripts.test_installation_upgrade scripts.test_installation_runtime
bash scripts/go-toolchain.sh test ./cmd/healthcheck ./scripts/installation
BRAIDPLANE_INSTALL_DATABASE_TEST=1 python3 -m unittest scripts.test_installation_database scripts.test_installation_restore_probe
BRAIDPLANE_INSTALL_DATABASE_TEST=1 python3 -m unittest scripts.test_installation_backup_database.BackupDatabaseTest
```

The opt-in database test owns a digest-pinned PostgreSQL container with no network
or published ports, applies the real catalog/grants and checks allowed/denied
capabilities. It removes only that container and its anonymous volume. Config
loader tests use generated synthetic files without database/provider connections.
Upgrade command tests cover backup ordering and failures. A local fixture also
exercised real backup/restore and a same-image/catalog version transition; this
does not establish schema-changing release upgrade acceptance.

The isolated restore probe test also dumps the real Core catalog, restores it
into empty network-isolated PostgreSQL storage using the retained image digest,
reapplies grants and verifies a clean migration ledger. The local arm64 database
drill passed in 2.863 seconds with PostgreSQL 18.6. Its application release
metadata and configuration are synthetic; it does not measure service RTO,
credential recovery or external-effect reconciliation.

The standalone backup also passed against the prepared bundle's actual Compose
database and grant services with synthetic TLS, role passwords and application
keys. The isolated restored database matched a frozen pre-backup row oracle and
omitted one deliberately inserted post-backup row. A second backup containing a
dirty migration ledger was refused by the restore probe. This covers database
restore-point loss and rejection, not complete application recovery. Application
services were absent in this fixture, so draining active runs still needs testing.
The latest clean restore probe took 2.899 seconds locally; full service RTO
remains unmeasured.

The same real Compose fixture also passed persistent isolated restoration into a
new directory/project, with retained key/configuration bytes, only database and
grant services, no published ports and a startup hold. Reusing the destination
and normal lifecycle startup were refused. The persistent database phase took
7.428 seconds locally. Production service resumption and execution by an
independent operator still need testing.

The fixture also invokes the real lifecycle upgrade with a synthetic candidate
whose PostgreSQL client persists a dirty ledger and exits with failure. It
verified retention of the failure journal and original installed identity,
startup refusal, only the database remaining running, and successful restoration
of the clean pre-migration backup. All image slots use the fixture PostgreSQL
digest. An actual application release transition still needs a separate rehearsal.
Image rollback is deferred; the supported upgrade direction is forward.

To rehearse an image upgrade against the running Core application, build all
four targets from this checkout, then run the opt-in fixture:

```sh
docker build --target backend -t braidplane-recovery-backend:local .
docker build --target frontend -t braidplane-recovery-frontend:local .
docker build --target migrate -t braidplane-recovery-migrate:local .
docker build --target cli -t braidplane-recovery-cli:local .
BRAIDPLANE_INSTALL_IMAGE_TEST=1 python3 -m unittest scripts.test_installation_image_upgrade.ImageUpgradeTest
```

The fixture uses a loopback-only registry and new API-only installation storage.
It upgrades between distinct backend image IDs with the same application code
and catalog, checks actual readiness and retains the administrator and keys.
It removes its containers and database volume; image tags remain in the local
cache. It does not publish externally or qualify a schema-changing release.

The pipeline service also has a logical restore drill:

```sh
bash scripts/go-toolchain.sh test ./service/pipelinesvc/e2e -run '^TestBackupRestorePreservesRunHistoryAndExecutesNewRun$' -count=1 -v
```

It executes a source-query pipeline, backs up the disposable database, and runs
another pipeline after the backup. After restoration into a fresh database, the
original run history matches exactly and the post-backup run is absent. A new run
succeeds against the frozen source oracle, writes new history and leaves the old
history unchanged. This test uses the real runner and persistence stores with a
synthetic query adapter. It uses another database on the same disposable server
and has no HTTP admission, queue workers or external sink. Production service
resumption and external-effect reconciliation still need qualification.

Local arm64 image builds and an actual Compose fixture verified startup after a
port repair, retained-database retry, administrator bootstrap, HTTPS frontend
login, protected cookies, CSRF rejection and untrusted-CA rejection. The fixture
used local image IDs and synthetic TLS with an ephemeral loopback edge port.
A second fixture used a loopback-only registry and actual digest pulls for a
fresh-volume lifecycle install. Two same-image/catalog upgrades passed, including
a data-bearing backup restored into a separate database with its administrator
row and migration identity intact. This does not qualify the published
distribution route or a fresh supported VM.

## Remaining qualification

The Git-authored pipeline acceptance uses an existing supported Git transport.
Private-network Git support is tracked separately in [Core issue #28](https://github.com/Braidplane/braidplane-core/issues/28)
and is not a prerequisite for qualifying this bundle. Keep destination protections enabled.
An internal fresh-VM rehearsal passed on Ubuntu 24.04.5 arm64, Engine 29.1.3,
Compose 2.40.3 and PostgreSQL 18.6, with two CPUs, 4 GiB configured memory and a
20 GB virtual disk. It covered empty-volume installation, browser login, a
three-step Git/SQL/Git pipeline, independent output readback, visible lineage and
history, and retained keys/history after restart. Five application/database
roles reported healthy; the edge passed actual customer-CA HTTPS requests.
No application container reported an OOM kill. This is one synthetic workload,
not a minimum sizing or load-capacity claim. The maintainer used an internal
artifact mirror; public distribution and independent execution remain unqualified. Real
compatible-version upgrades, backup restoration, disk/memory limits and complete
negative-input checks remain required. Record exact Engine, Compose, PostgreSQL
and artifact identities for those runs.

Approved published images/archive, signatures/provenance, the final supported
platform/version guide, public navigation and independent clean-machine execution
remain separate acceptance gates. This tooling does not establish HA, hosted
acceptance or provider qualification.
