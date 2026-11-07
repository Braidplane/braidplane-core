# Credential encryption operations

Core encrypts persisted product secrets with AES-256-GCM. A versioned envelope
records the cipher format and key ID. Authenticated additional data (AAD) binds
the envelope header, expected tenant, resource, purpose and plaintext schema
version. Readers derive that context from the expected record, not the envelope.
Moving ciphertext to another context must fail authentication.

## Covered storage

All scopes below use the organization slug as the tenant and schema version `1`.

| Table and column | Protected data | Resource / purpose |
|---|---|---|
| `credentials.encrypted_payload` | Connector credentials, environment secrets and pipeline webhook signing secrets | Credential slug / `static-secret` or `pipeline-webhook-secret` |
| `credential_oauth_grants.encrypted_token_set` | OAuth tokens, including Slack webhook URLs and HTTP OAuth app snapshots | Stored credential slug / `oauth-token-set` |
| `credential_oauth_installs.encrypted_pkce_verifier` | HTTP PKCE verifier and the app snapshot, including its client secret | Install slug / `oauth-install-snapshot` |
| `credential_http_oauth_apps.encrypted_config` | Desired HTTP OAuth app settings and client secret | Data-source slug / `http-oauth-app` |
| `gitlab_proof_webhooks.encrypted_signing_token` | GitLab route key and signing token | Data-source slug / `gitlab-webhook-secret` |
| `pipeline_webhook_deliveries.encrypted_envelope` | Event body and mapped runtime input retained for delivery/retry | JSON array of receiver ID and delivery ID / `pipeline-webhook-envelope` |

Use the persisted credential identity for OAuth grants, including pending grants
activated in place and grants refreshed in place. Retry attempts keep the same
webhook envelope identity. A rename that changes a scope needs an authenticated
rewrite with the old and new context. Recreated resources must not inherit copied
ciphertext. Resource display names do not form part of AAD.

## Key configuration and custody

The three credential settings are documented in [CONFIG.md](../CONFIG.md#security-configuration)
and implemented in [config/backend/credentials.go](../config/backend/credentials.go).
When both key ID and keyring settings are empty, new writes use key ID `initial`
with `security.encryption_key_hex`. Legacy unversioned reads also use that original
application key unless `security.credential_disable_legacy` is `true`.

For explicit configuration, set `security.credential_current_key_id` and supply
`security.credential_keys_hex` as a JSON object of key IDs to 32-byte hex keys.
Key IDs contain 1–64 ASCII letters, digits, hyphens, underscores or dots.
The current ID must exist in the object. Use independent random keys; repository
defaults, all-zero values and the execution-request key are invalid explicit keys.
Keep key values secret; key IDs are metadata. Assign a new ID to every new key
and keep the original ID/material pair for historical reads. Never reuse an
existing ID with different key material.

Queued authentication email still uses `security.encryption_key_hex` through its
own cipher. Distribution extensions also receive the original application cipher
and key. Rotating the product keyring does not rotate these consumers. Keep their
key until their queued records, extension state and retained backups no longer
need it. Execution request/result envelopes and optional Proof evidence have
separate keys and retention rules.

Follow the public [customer-owned state and secrets guidance](SELF_HOSTED_CUSTODY.md#customer-owned-state-and-secrets)
for database and configuration custody. The distribution owns hosted backup,
restore and deployment runbooks. Complete these fields in its protected operating
record before rotation or recovery:

| Required operator field | Value |
|---|---|
| Key custodian and alternate | Operator to complete |
| Approved protected recovery source and version | Operator to complete; secret values stay in that source |
| Independent recovery path when the original host/configuration is lost | Operator to complete |
| Access authorization, approver and expiry | Operator to complete |
| Database backup identifier, restore point and integrity check | Operator to complete |
| Retained key IDs and matching configuration version | Operator to complete |
| Recovery objective, drill date and independent reviewer | Operator to complete |

## Rotation, rollback and retirement

1. Take a recoverable database backup and retain its matching keys in the approved
   protected recovery source. Verify restore access through the independent path.
2. Add the new key to every credential-reading process while retaining existing
   IDs and the original application key. Deploy readers before selecting the new
   current ID everywhere. Use the same key material for each ID across processes.
3. Select the new current ID. New product writes now use it; historical keys remain
   available for reads. Record configuration versions and deployment boundaries.
4. Sweep all six storage families, including retained consumed installs,
   soft-deleted owners and pending or failed hooks. Skip SQL NULL and empty-byte
   tombstones. Authenticate each record before rewriting it. Preserve lifecycle
   metadata, grant revisions, refresh claims, expiry and retry state.
5. Use bounded transactions and compare the original ciphertext before updating.
   Reread records that changed concurrently before retrying them. After an
   interruption, resume and check that a second pass makes no changes.
   Stop rewriting a record if authentication fails.
6. Count remaining legacy and historical-key records in each family. Investigate
   every failure. Disable legacy reads only after the live census is clear and the
   restore procedure can still read retained backups.
7. Retire an old key only after all live records and retained backups no longer
   require it. Check email queues and extension consumers separately before retiring
   the original application key. Record approval and the destruction outcome.

With the matching release and protected configuration loaded, run the local
administrator executable:

```sh
./out/braidplane-admin credential-rekey --batch-size 50
```

The local operator executable lives under `cmd/admin`. The backend container
includes it at `/braidplane-admin`; select it with `--entrypoint` when using the image.
Run the command as an authorized database/key operator, separately from serving
traffic. It has no public HTTP endpoint and does not contact providers. All
credential-reading processes must understand the versioned format before the
first new-format write. All writers must select the same current key before a
sweep can establish completion.

For the first rollout from an unversioned-only release, pause product writes and
drain/stop the old API and worker processes. Upgrade every reader, preserve the
original key, then resume traffic and workers. Mixed old/new binaries cannot
safely share new ciphertext. Later key rotations between version-aware releases
use the reader-first sequence above.

The command emits a run ID, current key ID and one JSON progress record after
each committed batch. `after.family` and `after.id` are a metadata-only checkpoint.
On interruption, resume with the last emitted pair:

```sh
./out/braidplane-admin credential-rekey --after-family 3 --after-id 42
```

Each transaction scans at most 100 rows, then pauses for one second before the
next transaction. Each changed row writes an organization security-audit event
in the same transaction; the run ID is its request ID. Audit failure rolls back
the batch. Progress includes scanned, rekeyed, current and concurrent-conflict
counts. A concurrent ciphertext replacement or tombstone is preserved, counted
as a conflict, and makes the completed command exit unsuccessfully. Repeat from
the beginning after conflicts and after all writers have switched keys. Require
a full second pass with zero rekeyed rows and conflicts before declaring the
live census clear. Authentication or missing-key failure stops the batch without
advancing its checkpoint. Keep historical and legacy keys available until both
that census and backup/retirement conditions pass.

Rollback must retain every key used since the backup or rollout began. A prior
binary that only understands unversioned ciphertext cannot read new envelopes.
Use a compatible reader, or restore the matched database/configuration pair under
the distribution's restore procedure. Database restore does not undo provider
token rotation or external writes; reconcile those effects before resuming work.
Generating a replacement key cannot recover ciphertext whose key was lost.

## Isolated host and configuration loss drill

Run this drill in a disposable, isolated environment with synthetic fixtures.
Do not use production credentials, send real email or contact production providers.
Exercise the normal API/service paths with real PostgreSQL storage. Direct cipher
calls alone do not test recovery through those paths.

1. Create a synthetic static connector credential and an environment secret through
   their normal services. Create an OAuth grant against a controlled provider
   fixture; include its HTTP app and install snapshot when testing HTTP OAuth.
2. Resolve the connector through its normal connector/query path, the environment
   secret through runtime resolution, and OAuth through grant resolution and refresh.
   Record success without storing returned secrets or provider tokens.
3. Back up the database and save matching protected key/configuration versions.
   Record the restore point, configuration digest and fixture identities.
4. Remove access to the original disposable host and its local configuration.
   Restore onto a fresh isolated host using only the approved independent recovery
   path. Have the authorized alternate obtain the protected configuration.
5. Restore the database with the matching keys. Repeat the same real resolution
   paths and an OAuth refresh against the controlled provider. Verify persisted
   configuration and token snapshots remain usable.
6. In a separate disposable copy, remove a required key and confirm resolution
   fails closed. Restore it and confirm resolution succeeds. Check that changing
   tenant, resource or purpose rejects a versioned ciphertext without provider calls.
7. Repeat after rotation, then restore a pre-rotation backup with its matched keys.
   Verify rollback compatibility and repeat the storage census before retirement.
8. Destroy disposable fixture state and secret copies according to the approved
   retention policy. Record the independent review and any unresolved failures.

Keep evidence metadata-only: timestamps, build/configuration digests, backup IDs,
key IDs, fixture IDs, record counts, typed error codes and pass/fail outcomes.
Do not retain plaintext, key values, ciphertext dumps, tokens, provider responses,
or secret-bearing command output. Completion and key-retirement approval need
their own operator records; the procedure alone supplies neither.

Repeat the drill after any keyring or recovery-source change, and record the
operator's chosen calendar cadence. Each self-hosted operator is responsible for
key custody, access approvals, retained backups and recovery targets. Core
supplies the encryption mechanism, synthetic tests and procedure.

## Core synthetic qualification

```sh
bash scripts/go-toolchain.sh test ./service/credentialrekeysvc/e2e -count=1 -v
```

This suite owns a disposable Docker PostgreSQL fixture. Install `pg_dump` and
`pg_restore` clients compatible with the fixture server for the recovery test.
It verifies all six ciphertext families, cursor resume, idempotency, concurrent
replacement/revocation safety, atomic audit rollback and unchanged lifecycle
metadata. The recovery case verifies a custom-format dump's digest and byte count,
deletes the runtime key file, restores into a fresh empty database and retrieves
matching synthetic keys from a separate mode-0600 recovery file. Static connector,
environment-secret and OAuth resolution use their real service/repository paths.
Missing/wrong keys fail without changing restored records. Output records only
backup/build digests, byte count, schema version, key ID, elapsed time and outcome.

The logical restore uses another database on the same disposable server. It does
not qualify physical host loss, preserved ownership/grants, a published release
or a particular operator's recovery source. Each installation still rehearses
its own matching release, protected key source and backup procedure.
