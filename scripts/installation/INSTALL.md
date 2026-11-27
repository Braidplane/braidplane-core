# Community single-host installation tools

This bundle is still being tested and is not yet a supported public release.
`release.json` records its version, source revision, image digests and target
platform.

Select the download for your Docker daemon's architecture. Check it with
`docker info --format '{{.OSType}}/{{.Architecture}}'`: `aarch64` and `arm64`
use the ARM64 release; `x86_64` and `amd64` use AMD64. AMD64 GitHub Releases
use the version tag (for example `v0.1.1`); ARM64 uses the version followed by
`-linux-arm64` (for example `v0.1.1-linux-arm64`). Download the record, manifest,
identity and installation archive from that same release. Preflight rejects a
manifest for the other platform. Keep the manifest's signed platform unchanged.
Creating the archive does not authorize image publication or customer deployment.

## Prerequisites

Use a Linux Docker host, Docker Engine with Compose, Python 3.11+ and OpenSSL.
Exact supported Engine/Compose versions and capacity limits remain unqualified.
The target architecture must match `release.json`. Preflight checks a Linux
Docker daemon, a matching native architecture and Compose support for readiness
waits. It reports Engine/Compose versions and daemon CPU/memory totals. Workload
capacity and supported host sizing still need testing. Customers must be able to
retrieve all six image references through the approved distribution route before
startup. The tools do not build images.

Obtain the approved archive, its digest and signatures from the release owner.
Verify its origin and signature using the approved release procedure before
extracting or running code. An internal checksum alone does not authenticate an
archive. From the extracted directory, check its files:

```sh
sha256sum --check SHA256SUMS
```

The archive contains tools, configuration assets and immutable image references.
Customer credentials, keys, certificates and database contents are excluded.
No private checkout, Task, Go, npm or personal secret store is needed.

## Prepare a new installation

Provide a customer DNS hostname, a host IPv4 bind address, SMTP endpoint and
sender address. Provision `ca.pem`, `database.pem`, `database.key`, `edge.pem`
and `edge.key` in a protected certificate directory. Certificates must chain to
the supplied CA. Include `database` in the database certificate's DNS SAN and
the customer application hostname in the edge certificate's DNS SAN. Install
customer trust in browsers separately. Keep CA signing keys out of the
installation directory.

Run from the extracted tools directory and replace the example values with your
customer's settings:

```sh
python3 -m scripts.installation.prepare /protected/braidplane release.json \
  app.customer.test 192.168.1.20 mail.customer.test:587 recovery@customer.test \
  /protected/customer-certificates
python3 -m scripts.installation.preflight /protected/braidplane app.customer.test
```

Preparation refuses an existing destination and generates separate operator,
migrator, runtime and executor credentials. Preserve the entire protected
installation directory with the database. Do not regenerate it for a restart.
Configure SMTP authentication and trust in the protected configuration before
startup if anonymous delivery with system-root verification is insufficient.

## Start, inspect and stop

```sh
python3 -m scripts.installation.lifecycle install /protected/braidplane
python3 -m scripts.installation.lifecycle status /protected/braidplane
python3 -m scripts.installation.lifecycle stop /protected/braidplane
python3 -m scripts.installation.lifecycle start /protected/braidplane
```

Only the TLS edge publishes a port: customer bind address, TCP 443. API, worker,
executor and frontend have private readiness endpoints. The PostgreSQL named
volume retains database state across stop/start. The installation's unique
Compose project name stays in `release.env`; preserve it during recovery. Docker
administrators can inspect container credentials and are trusted operators.

The bundled TLS edge sends a one-year HSTS policy for the application hostname.
For an existing installation or a custom TLS edge, apply the policy to its
HTTPS responses, including edge errors. Image-set upgrades retain the installed
Caddyfile. Review subdomain coverage separately before enabling it.

New bundles put the edge, frontend and API on a dedicated private IPv4 /29.
The edge forwards its observed client address. The frontend and API trust only
their configured adjacent proxy address. API-only bundles trust the edge at the
API. Preparation chooses a subnet from `10.254.0.0/16`. Use
`--proxy-subnet <private-IPv4-/29>` when that range overlaps Docker or site
networks. Keep the configured peer addresses and the proxy network together.

Image upgrades retain the installed Compose and Caddy files. To update an
existing bundle, back up those files and stage the packaged `proxy` network
with a free private /29. Keep the existing database and application `private`
network. Attach the edge only to `proxy`, and attach the frontend and API to
both networks. Put the `api-proxy` alias on the API's `proxy` attachment and
set the frontend's `BACKEND_API_URL` to `http://api-proxy:3001`. Match each
trusted address to its adjacent proxy. For an API-only bundle, route Caddy
directly to `api-proxy:3001` and configure the API to trust the edge.
Validate the rendered Compose configuration before a maintenance-window
recreate of the affected services. For custom proxies, have the edge replace
the incoming forwarding header with the observed client address. When the
frontend proxies requests, set the API's `SERVER_PROXY_HEADER` to
`X-Forwarded-For`, which is the header the frontend sends.

Stop preserves containers, volumes and keys. Failed startup also preserves them.
Inspect status and project logs before retrying. Never replace credentials or
reset the data volume to repair a failed installation. Stdout logs are bounded
per container; application temporary files have bounded tmpfs mounts. Runtime
application images use UID 65532, read-only roots and dropped capabilities.
PostgreSQL owns its data; database startup copies key/bootstrap files to
PostgreSQL-owned files without changing the customer originals.

Follow the first-result procedure below to run a Git-authored pipeline. Test
backup restoration before upgrading. Use the lifecycle commands for upgrades;
editing `release.json` after the first attempt is rejected. Retain the original
tools archive, protected installation directory and database until you have
verified recovery.

## Renew edge and database TLS certificates

Renew both server certificates before expiry using the CA already in
`certificates/ca.pem`. Put the application DNS hostname in the edge leaf's
DNS subject alternative name (SAN). Put `database` in the database leaf's
DNS SAN. The bundled PostgreSQL clients connect to that name with
`verify-full`. Keep the CA signing key in the existing signing system.
Use your CA's renewal schedule and alert on the expiry of both live leaves.
Use OpenSSL 3.0 or newer for the checks below.

Use a maintenance window. Pause new pipeline starts, including scheduled and
webhook-triggered runs, and wait for active runs to finish. Coordinate with
other operators so no backup, upgrade or restore overlaps the window. The
stock bundle needs two targeted container recreations, one at a time.

| Service | Action and impact |
| --- | --- |
| `edge` | Recreate Caddy to read the new bind-mounted files. The bundled Caddyfile disables its reload API. HTTPS drops while the single edge is replaced. |
| `database` | Recreate PostgreSQL to remount the certificate and copy the new key into its runtime path. Existing database sessions disconnect, and database-dependent requests can fail until clients reconnect. |
| `api`, `worker`, `executor`, `frontend` | Leave these services running with the existing CA. Verify that each recovers after PostgreSQL returns. |

### Validate and retain the certificate pairs

Set `INSTALLATION` to the absolute path of the running installation and
`HOST` to its application DNS hostname. Set `STAGED` and `PREVIOUS` to new
absolute paths outside the installation. Create `STAGED` and
`$STAGED/certificates` with mode 0700. In `$STAGED/certificates`, place `edge.pem`,
`edge.key`, `database.pem` and `database.key`. Copy the installed `ca.pem` into
that directory and copy the installed `release.json` into `$STAGED`. Set both
staged keys to mode 0600.

From the extracted tools directory on the Docker host, validate the staged
certificates. Preflight checks trust, hostnames, key pairs and at least one
day of remaining validity. Record each staged leaf's serial, fingerprint and
expiry to compare with the live endpoints.

```sh
show_leaf() {
  openssl x509 -in "$1" -noout -serial -enddate -ext subjectAltName &&
  openssl x509 -in "$1" -noout -sha256 -fingerprint
}

(
  set -e
  cmp "$STAGED/certificates/ca.pem" "$INSTALLATION/certificates/ca.pem"
  python3 -m scripts.installation.preflight "$STAGED" "$HOST"
  for name in edge database; do
    show_leaf "$STAGED/certificates/$name.pem"
  done
)
```

Stop if any check fails. Confirm the printed DNS SANs and that both leaves
expire later than the certificates in service. In the same shell, save the
installed pairs before replacing them.

```sh
(
  set -e
  umask 077
  mkdir -m 700 "$PREVIOUS"
  for name in edge database; do
    cp "$INSTALLATION/certificates/$name.pem" "$PREVIOUS/$name.pem"
    cp "$INSTALLATION/certificates/$name.key" "$PREVIOUS/$name.key"
  done
)
```

Continue once all four files are saved. Start from a shell without Compose
or installation environment overrides. Set `PROJECT` to the existing Compose
project name recorded as `BRAIDPLANE_PROJECT_NAME` in `release.env`.
Define the helpers and confirm that `dc ps` shows the intended installation.

```sh
dc() {
  docker compose --project-name "$PROJECT" --env-file "$INSTALLATION/release.env" \
    -f "$INSTALLATION/compose.json" "$@"
}

replace_pair() (
  name=$1
  from=$2
  certs="$INSTALLATION/certificates"
  install -m 0644 "$from/$name.pem" "$certs/.$name.pem.new" &&
  install -m 0600 "$from/$name.key" "$certs/.$name.key.new" &&
  mv -f "$certs/.$name.pem.new" "$certs/$name.pem" &&
  mv -f "$certs/.$name.key.new" "$certs/$name.key"
)
```

### Recreate and verify one service at a time

Record the live leaf identities before you start. Probe HTTPS and PostgreSQL
repeatedly during the window to measure the time from the last successful
probe to the first successful probe after each interruption.

Apply the edge pair first. Continue after the live leaf matches the staged
fingerprint, serial, hostname and expiry and HTTPS works with the existing CA.
Use `/readyz` to check application readiness over HTTPS. Run the checks from
a client that can resolve and reach the app hostname.

```sh
replace_pair edge "$STAGED/certificates" &&
  dc up -d --no-deps --force-recreate --pull never --wait --wait-timeout 180 edge
```

```sh
(
  set -e
  edge_leaf=$(mktemp)
  trap 'rm -f "$edge_leaf"' EXIT
  openssl s_client -connect "$HOST:443" -servername "$HOST" \
    -verify_hostname "$HOST" -verify_return_error \
    -no-CApath -no-CAstore \
    -CAfile "$INSTALLATION/certificates/ca.pem" </dev/null >"$edge_leaf"
  show_leaf "$edge_leaf"
  status=$(curl --fail --silent --show-error --output /dev/null \
    --write-out '%{http_code}' --cacert "$INSTALLATION/certificates/ca.pem" \
    "https://$HOST/readyz")
  test "$status" = 200
)
```

Apply the database pair after the edge checks pass. Use the existing project
and volume. A PostgreSQL reload alone does not copy the key into its runtime
path. The container recreation also remounts the replacement files at their
existing paths.

```sh
replace_pair database "$STAGED/certificates" &&
  dc up -d --no-deps --force-recreate --pull never --wait --wait-timeout 180 database
```

From the installation's private network, check the new live leaf with the
existing CA. The one-shot command below uses the image and CA mount of the
`grants` service and overrides its entrypoint to inspect TLS. It applies no
grants or migrations.

```sh
(
  set -e
  database_leaf=$(mktemp)
  trap 'rm -f "$database_leaf"' EXIT
  dc run --rm --no-deps -T --entrypoint openssl grants s_client \
    -starttls postgres -connect database:5432 -servername database \
    -verify_hostname database -verify_return_error \
    -no-CApath -no-CAstore \
    -CAfile /etc/braidplane/ca.pem </dev/null >"$database_leaf"
  show_leaf "$database_leaf"
)
```

Compare the live database certificate to the staged record. Check
`dc ps` and verify authenticated HTTPS access again. If a pipeline source
pins the database container's IP, confirm the configured address still
matches. Start a known database-backed pipeline using a fresh run ID.
Wait for it to succeed and compare its recorded version, step results and
expected output to a known result before resuming normal work.

If a file replacement fails, a service fails to come back, or the served leaf
differs from the staged leaf, stop the sequence and inspect the change and
the service's Compose logs. Roll back only if the previous leaf and key are
still valid and trusted. Run `replace_pair edge "$PREVIOUS"` or
`replace_pair database "$PREVIOUS"`, then repeat the recreation and endpoint
checks for that service against the saved leaf. Resume work after both
endpoints and the pipeline check pass. Retain the previous pairs securely
until you close the maintenance window.

### Tagged rehearsal

On 27 November 2026, Core `v0.4.0-rc.3-linux-arm64` (`3378e5dc`) ran in the
disposable `core-237-local` project on OrbStack's Linux ARM64 daemon. The full
single-host bundle used Caddy 2.11.9, PostgreSQL 18.6, Docker Engine 29.8.2
and Compose 5.5.0. Caddy published HTTPS on loopback port 36044. PostgreSQL
used a private network and a dedicated volume.

Both endpoints negotiated TLS 1.3. OpenSSL verified both live leaves against
the original fixture CA, and their fingerprints matched the staged leaves.
Both expiries moved from 11 December 2026 to 25 February 2027 at 10:13 UTC.
The `app.core-237.test` leaf changed
from serial `5C95` to `5C97`. The `database` leaf changed from `5C96` to
`5C98`.

| Change | Observed interruption |
| --- | --- |
| Edge recreation | 0.40 s between successful authenticated HTTPS probes around one refused connection |
| Database recreation | 0.60 s between successful PostgreSQL TLS probes and 1.62 s between successful authenticated HTTPS probes around seven HTTP 500 responses |

The probes targeted 200 ms intervals. These gaps bracket the observed
interruptions on this host. Compose returned after 1.02 s for the edge and
6.97 s for PostgreSQL, including its readiness wait. Only those two containers
were recreated. API, worker, executor and frontend kept their container IDs
and start times.

After renewal, the HTTPS login page returned 200 and authenticated HTTPS
recovered. The tagged CLI run `renewal-after` succeeded on the same pipeline
version as `renewal-baseline`. Its query returned one row only if the
PostgreSQL session used TLS. A separate `verify-full` query read the expected
value `42`. The synthetic spec was seeded from a local Git commit. Deployment
and execution used the tagged application.

The same fixture then served both saved leaves after rollback. Both renewed
leaves were reapplied and verified. HTTPS readiness returned 200, and
`renewal-reapplied` succeeded on the same pipeline version.

## First administrator and sign-in

After all application roles are ready, independently verify the first owner's
email address. Supply a password of 8 to 1024 bytes through a mode-0600 temporary
file or a protected secret-manager pipe. Keep it out of command arguments and
shell history. Replace the illustrative account fields before running:

```sh
docker compose --env-file /protected/braidplane/release.env \
  -f /protected/braidplane/compose.json run --rm --no-deps -T bootstrap \
  bootstrap --user owner --name 'Instance Owner' \
  --email owner@customer.test --email-ownership-verified \
  < /protected/bootstrap-password
```

Expected output: `instance administrator bootstrapped for user owner`. The
bootstrap container selects `/braidplane-admin` and uses the migration identity; normal services retain their
restricted identities. The command refuses to overwrite an existing account or
run when an instance administrator already exists. It does not open signup or
grant organization membership. Remove the temporary password file after securely
storing the credential.

Open `https://<customer-hostname>/auth/login` in a browser that trusts the customer
CA and sign in with the verified email and supplied password. Establish the
organization/project through the normal authenticated application flow. Password
recovery needs working customer SMTP. Local HTTP fixture validation covers
secure cookies, missing-CSRF rejection, login and authenticated profile access;
the maintainer's fresh-VM browser and pipeline rehearsal also passed. This does
not establish approved public distribution or independent operator acceptance.

## First result from customer Git

The bundled [pipeline](first-result/first-result.pipeline.json) reads
`first-result/input.json` from Git, calculates `41 + 1`, and writes one JSON row
back to Git. The [expected result](first-result/expected.json) is `[{"result":42}]`.
It requires no cloud database, object store or webhook.

Use a dedicated synthetic repository and branch reachable through a supported
Git transport. Its credential must permit reads and writes to that branch.
Keep destination protections enabled. Using your provider UI or a separate
authoring machine, commit these two files to the chosen branch:

- `first-result/input.json`, copied from this archive;
- `pipelines/installation-first-result.pipeline.json`, copied from
  `first-result/first-result.pipeline.json` in this archive.

The source and sink use the Git connection's configured default branch. Set
`config.default_branch` to the same committed branch as `GIT_BRANCH` before
creating the connection. Choose a branch whose rules permit the example's
generated-file commits. Keep application
credentials and keys out of Git. Create a fresh organization and project, then obtain a
personal access token through the authenticated application.

Set `INSTALLATION` to the prepared installation, `TOOLS` to the extracted archive,
and `CUSTOMER_INPUTS` to a mode-0700 directory owned by the current operator. In that directory, create a
mode-0600 `cli.env` with `BRAIDPLANE_SERVER_URL=https://<hostname>/api`,
`BRAIDPLANE_ORG`, `BRAIDPLANE_PROJECT`, `BRAIDPLANE_ENVIRONMENT=dev` and
`BRAIDPLANE_PAT`. The hostname must resolve from the CLI container. The supplied
CA is already mounted by the CLI service.

Also provide `git-connection.json` in that protected directory. For SSH it has
`type: "git"`, `name: "Installation Git"`, `slug: "installation-git"`,
`read_only: false`, `config.repo_url`, `config.default_branch`, and
`credentials.auth_type: "ssh"`. Set `credentials.ssh_private_key_file` and
`credentials.ssh_known_hosts_file` to protected files beside that JSON file.
Use provider-verified host keys. HTTPS credentials are also supported.

Set the shell's `BRAIDPLANE_PROJECT` and `GIT_BRANCH` to the project and committed
branch. Use a fresh project; do not replace another project's backend or overwrite
an existing saved operation. From that shell on the Docker host, use the pinned
CLI image in the installation:

```sh
umask 077
bp() {
  docker compose --env-file "$INSTALLATION/release.env" \
    -f "$INSTALLATION/compose.json" run --rm --no-deps -T \
    --user "$(id -u):$(id -g)" \
    --env-from-file "$CUSTOMER_INPUTS/cli.env" \
    --volume "$TOOLS/first-result:/first-result:ro" \
    --volume "$CUSTOMER_INPUTS:/customer-inputs:ro" cli "$@"
}
bp data-source create --file /customer-inputs/git-connection.json
bp project pipeline-backend set "$BRAIDPLANE_PROJECT" \
  --data-source installation-git --tracked-branch "$GIT_BRANCH" \
  --pipeline-dir pipelines --mode adopt_existing
bp query create --data-source installation-git --name 'Write installation result' \
  --statement-file /first-result/write-operation.json --parameters-json '[]'
bp pipeline apply --dir pipelines --pipeline installation-first-result
bp data-source-binding bind installation-git --pipeline installation-first-result \
  --data-source installation-git
bp pipeline validate --file /first-result/first-result.pipeline.json
bp pipeline get installation-first-result > "$CUSTOMER_INPUTS/pipeline-version.json"
SPEC_HASH="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["current_version_spec_hash"])' "$CUSTOMER_INPUTS/pipeline-version.json")"
bp pipeline deploy installation-first-result --spec-hash "$SPEC_HASH"
RUN_SLUG=installation-first-result-1
bp pipeline run start installation-first-result --run-slug "$RUN_SLUG"
bp pipeline run watch "$RUN_SLUG" --timeout-seconds 120 --output json
bp pipeline run get "$RUN_SLUG" --output json > "$CUSTOMER_INPUTS/run.json"
```

The shell variables select onboarding only; the CLI's environment comes from
the protected `cli.env`. The one-shot CLI uses the operator's UID to read the
mode-0600 input files.

Stop on any command failure. An accepted start is asynchronous. Watch must reach
`succeeded`; on timeout, inspect and watch the same run rather than starting a
replacement. Confirm `metadata.pipeline_version_spec_hash` in `run.json` equals
`SPEC_HASH`. The timeline must contain `read-fixture`, `calculate-result` and
`write-result`, each with one produced or written row.

Read the sink snapshot's `output.details.target_path`. Download that exact file
and commit through your Git provider, then compare its JSON with
`first-result/expected.json`. Run details retain metadata rather than row values.
In the application, open the same project's run history and select the same run
to inspect its version, three-step lineage and reported output path. Preserve
the source/output commits, spec hash and run identity with the acceptance record.

## Compatible image-set upgrades under qualification

Obtain and independently verify the approved newer tools bundle and its manifest.
Run this command from that extracted bundle, with `--release` pointing to its
`release.json`. The command checks that the bundle and candidate manifest match.
It changes image identities using the installed Compose/configuration
contract; it does not apply new topology or configuration formats. Confirm the
release owner supports that transition. The platform and exact PostgreSQL image
must stay unchanged. Downgrades and PostgreSQL upgrades are rejected.

When upgrading from unprefixed HTTPS cookies, sign in again and reselect any
remembered dashboard scope. Restart any GitHub connection setup in progress.

```sh
python3 -m scripts.installation.lifecycle upgrade /protected/braidplane \
  --release "$PWD/release.json" \
  --backup-directory /protected/backups/braidplane-before-upgrade
```

Use a new backup destination outside the installation directory. The command
pulls candidate images and checks backup filesystem free space before downtime.
It then stops application writes, creates a custom-format database dump and
checks it with `pg_restore --list`. The archive contains configuration, role
credentials, encryption keys and TLS material. Backups have
mode-0600 files in a mode-0700 directory and must be protected as sensitive data.
You still need to rehearse restoration; listing the dump's catalog cannot prove
that the backup will restore successfully.

Before stopping applications, backup and upgrade query `pg_database_size` through
the current database client and compare available backup filesystem bytes with
that measured size plus configuration bytes. Query failure or insufficient space
refuses downtime. This early check cannot reserve space or predict database growth,
concurrent disk consumption, tar overhead or Docker-host storage needs. Check
those separately using the recovery operating record; write failures still leave
partial backups that must not be counted as recovery points.

Before migration, the command writes `backup.json` format version 1 with the
original release identity, UTC writer-stop and completion times, and SHA-256
digests and byte counts for both payloads. A missing receipt means the backup
did not complete this protocol. Verify the protected copy after transfer:

```sh
python3 -m scripts.installation.backup /protected/backups/braidplane-before-upgrade
```

Verification reads files without extracting configuration or restoring data.
It rejects missing, empty, symlinked or changed payloads and unknown formats.
Checksums detect corruption; they do not authenticate a backup against an attacker
who can replace both payloads and receipt. Protect the receipt with the backup.
The writer-stop time is a recovery-point bound only when all database writers
are stopped, including external clients. Neither timestamp proves destination
consistency, decryptability, migration compatibility or successful recovery.

For a trusted backup, rehearse database restoration with Docker:

```sh
python3 -m scripts.installation.restore_probe /protected/backups/braidplane-before-upgrade
```

This command executes the backup's SQL, so verify its source through the protected
recovery channel first. It creates a uniquely named disposable PostgreSQL container
using the retained image digest and platform, with no network or published ports.
It restores into empty storage, reapplies retained role grants and requires one
clean migration-ledger row. It starts no application workers and removes its own
container and anonymous volume on exit. An interrupted process can leave that
uniquely named container behind; inspect it before removing its storage.
Successful output records elapsed seconds and schema version. This measures the
database drill only; key recovery, frozen-oracle comparisons, external-effect
reconciliation and full service RTO remain separate acceptance requirements.

Only after backup completes does it run candidate migrations, replace the installed
grant scripts with those from the candidate bundle, apply them and wait
for application readiness. It then commits the candidate identity. It preserves
the same database, keys and Compose project. A failed upgrade requests application
shutdown and retains `.upgrade-in-progress.json`, the candidate inputs and backup.
Start and further upgrade commands refuse an incomplete journal. Status and stop
remain available. Do not delete the journal or start old images against a possibly
changed schema; diagnose and follow the separately qualified recovery procedure.
No automatic rollback or volume reset is performed.

This entrypoint has command-sequence and failure-path tests. A local rehearsal
verified fresh-volume startup with loopback-registry digest pulls, a same-image
upgrade, and restoration of a data-bearing dump into a separate fixture database.
A schema-changing release transition and independent backup/restore rehearsal
remain required before customer use. Qualify the Git-authored pipeline through
an existing supported Git transport. Private-network Git support in Core issue
#28 is separate from installation acceptance.

## Persistent isolated restore

For persistent recovery into a new, nonexistent directory:

```sh
python3 -m scripts.installation.restore /protected/backups/braidplane-unique-backup-id \
  /protected/braidplane-recovery
```

Use only a trusted backup: restoration executes its retained SQL. The command
verifies the receipt and archive inventory, writes keys and configuration to
protected files, and restores the database and grants into empty storage in a new
Compose project. The recovery topology contains only the database and grant client,
with an internal network and no published ports. The original installation and
backup remain separate. Existing destinations are refused.
The recovery client always connects to the new database service with verified
TLS. Saved database host overrides cannot redirect its restore writes.

The `.restore-pending.json` hold blocks normal lifecycle startup and records the
completed database recovery or an incomplete attempt. Inspect with lifecycle
`status`; lifecycle `stop` remains available. Failures preserve the new storage
and hold for diagnosis. Do not remove the hold or replace the isolated topology
to resume work; reconciliation and service-resumption tooling remain unqualified.
Follow [the recovery operating record](RECOVERY.md) before any production adoption.

## Scheduled or incident backups

For recurring backups, schedule the [complete nightly maintenance window](RECOVERY.md#example-nightly-maintenance-window).
For a single backup, first fence external writers and drain active work as described
in [the recovery operating record](RECOVERY.md). Then run the backup command.

```sh
python3 -m scripts.installation.lifecycle backup /protected/braidplane \
  --backup-directory /protected/backups/braidplane-unique-backup-id
```

Use a new destination for every attempt. The command holds the installation lock, stops
application services and creates the same versioned dump/configuration backup
as an upgrade. It never migrates, changes the installed release or starts workers.
Application services stay stopped after the backup, including after a backup
failure. Inspect status before using `lifecycle start` to resume the original
installation. If the stop itself fails, services may still be running; check
their state before treating the installation as frozen.
Report command failure to the configured alert owner. Qualify scheduling, backup
age alerts, off-host copying and retention separately; this command does not
configure them. A backup does not require a new release manifest.

## API-only installation

See [the recovery operating record](RECOVERY.md) for retention, off-host copies,
key custody, recovery holds, external-effect reconciliation and qualification.

Add `--api-only` when preparing a new installation:

```sh
python3 -m scripts.installation.prepare /protected/braidplane-api release.json \
  api.customer.test 192.168.1.20 mail.customer.test:587 recovery@customer.test \
  /protected/customer-certificates --api-only
```

The edge certificate must cover the chosen API hostname. Preparation omits the
frontend container and routes HTTPS directly to `api-proxy:3001`. API, control worker,
poller executor, PostgreSQL, migrations, grants and administrator bootstrap stay
available. Use the same install/start/stop/status/upgrade commands and retain the
same data/key custody rules. The complete release manifest still identifies the
frontend image, but this layout does not run or pull that service.

Use an API client or CLI with endpoint-appropriate session/PAT/API-key
credentials. Browser login pages and frontend assets are absent. Browser-based
OAuth and email links require a customer application that implements those
surfaces; API-only mode does not provide a replacement UI. Choose the layout at
preparation time rather than deleting services from an existing installation.
