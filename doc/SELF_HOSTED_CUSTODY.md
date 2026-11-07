# Self-hosted custody and network boundaries

Use this guide to configure Community core on customer infrastructure and check
where its data goes.

Set `DEPLOYMENT_PROFILE=self-hosted` for the API, worker and frontend. Supply
customer-owned keys and database credentials, and keep SMTP certificate
verification enabled.

## Customer-owned state and secrets

The customer owns PostgreSQL, application configuration, runtime identities,
selected source/sink systems, Git repositories, logs and backup destinations.

| Material | Configuration / custody |
|---|---|
| Product credential encryption | Restore the complete `CREDENTIAL_KEYS_HEX` keyring and matching `CREDENTIAL_CURRENT_KEY_ID` with the database. Empty settings use the original `ENCRYPTION_KEY_HEX` under key ID `initial`. Retain historical keys for backups; follow [credential encryption operations](CREDENTIAL_ENCRYPTION.md). |
| Queued auth-email and legacy application encryption | `ENCRYPTION_KEY_HEX`: independent random 32-byte key, hex encoded. Preserve it for queued email, legacy credential reads and distribution consumers. Product-key rotation does not rotate this key. |
| Execution request/result envelopes | `EXECUTION_REQUEST_KEY_HEX`: independent random 32-byte key, distinct from the application key. Keep it consistent across the cooperating processes. |
| Optional live Proof evidence | `PROOF_OBSERVATIONS_EVIDENCE_KEY_HEX`: separate key, required only when live observations are enabled. |
| Database credentials | `DB_USER`, `DB_PASSWORD`, `DB_DATABASE`, `DB_HOST`, `DB_PORT`; `DB_SSL_MODE=verify-full` and customer `DB_SSL_ROOT_CERT` where needed. Use separate migration/operator and runtime identities. The disposable PostgreSQL fixture tests runtime grants; verify TLS and roles together in the customer installation too. |
| Mail recovery / verification | Customer `SMTP_HOST`, sender address, credentials where required, and `SMTP_CA_CERT_FILE` or `SMTP_CA_CERT`. Never enable `SMTP_INSECURE_SKIP_VERIFY` outside local development. |
| Optional provider apps | Configure OAuth/App IDs, secrets, callback URLs, signing keys and webhook secrets for each integration you enable. |
| Session and recovery state | Database sessions, reset/verification/MFA token hashes and queue records. Backups contain sensitive state even when token payloads are encrypted. |
| Sources and results | Database metadata/credentials, run history, checkpoints, replay state, selected Git revisions, external sink effects and customer-selected files. Database restore does not undo external writes. |

Protect secret files and backup access. Supply credentials through protected
configuration or process environments. Keep them out of command-line arguments.
A trusted database operator creates the first administrator through
[local bootstrap](INSTANCE_ADMIN.md#first-administrator-with-closed-registration).
Registration defaults to closed. Local login, email verification, MFA and SMTP
password recovery work independently of optional Google login.

## Outbound paths

To check outbound traffic in a clean installation, record DNS lookups, socket
destinations and browser requests for the paths below.
Allow only the expected destinations and test both enabled and unconfigured
features. Those checks establish whether the tested flows need a vendor service
at runtime.

| Caller | Destination and data | Disable / restrict |
|---|---|---|
| API, workers, frontend | Customer PostgreSQL: accounts, sessions, metadata, credentials, queue/run state | Required customer endpoint; narrow roles, verified TLS and network ACLs. |
| Worker mail delivery | Customer SMTP: recipient, verification/reset/MFA links or codes and configured notifications | Required for email-based flows; customer DNS/CA, verified TLS. |
| Google user login | Google discovery, token and signing-key endpoints; OAuth state/code and identity claims | Leave both Google client credentials empty. Discovery is deferred until an authentication request. [Implementation](../service/oidc/oidc.go). |
| Connector workers / executors | Selected source/sink and repository endpoints; credentials, introspection, queries, samples, payloads and writes | Enable only required connectors and destinations. Hostguard checks and tenant authorization do not replace deployment egress ACLs. |
| Connector-provider OAuth / webhooks | Configured providers: authorization, token refresh/revocation, repository setup, Proof checks and webhook management | Leave provider app settings empty when unused; do not run optional provider jobs against saved grants unintentionally. |
| Browser Workspace picker | Google account and API scripts, picker requests and selected file authorization | Scripts load on picker use; avoid that feature without a configured customer Workspace app. [Loader](../frontend/assets/js/workspace_connector.js). |
| Browser UI | Same-origin app assets/API calls, explicitly configured documentation and user-clicked external links | Assets are served locally; documentation URL can be empty. Links and optional provider journeys require separate browser capture. |
| Frontend | Configured backend API URL, authenticated user operations | Explicit customer backend; private HTTP is permitted only for a trusted service network, with HTTPS at the browser edge. |
| Public CLI | Configured API endpoint, caller-selected input/output files | Configure the API endpoint and protect CLI credentials. [Configuration](../cli/cmd/braidplane/auth_config.go). |
| Logs and traces | Explicit OTLP collector endpoints; structured diagnostic attributes | Empty `LOG_OTLP_LOGS_ENDPOINT` and `TRACING_OTLP_TRACES_ENDPOINT` disable export. Ambient `OTEL_EXPORTER_OTLP_*` variables alone do not enable it. |
| Metrics | A monitoring system connects to the incoming scrape endpoint; metrics are not pushed out | `METRICS_ENABLED=false` by default; restrict scrape access if enabled. |
| Operating system / deployment tooling | DNS, time synchronization, certificate/revocation services, image/package retrieval, backups and support exports | Customer network policy and separately approved tooling; not controlled by the application profile. |

The `self-hosted` profile accepts private HTTPS application and documentation
origins configured by the operator. Secure cookies, logging and SMTP checks,
and database TLS verification still apply. Private connector access requires a
separate grant. The profile rejects `ALLOW_PRIVATE_DATA_SOURCE_HOSTS=true`.
Use `private_postgres` to grant one organization and source access to a pinned
PostgreSQL endpoint (see [configuration](../CONFIG.md#pinned-private-postgresql-source)).
Other private connectors need further connectivity work; see their transport
policy and [data-source guidance](DATA_SOURCES.md). The fixture described below
checks that the tested account, provider and CLI flows cannot reach unauthorized
destinations.

Local file logging retains the active file plus one `.1` backup. A record may
exceed the size threshold; rotation happens before the next write. Self-hosted
file logging requires a positive size limit. Set retention separately for
container stdout, the database, queues and history. Review what an OTLP collector
or support bundle can expose before allowing it to receive credentials or
customer payloads.

## Separate migration and runtime database roles

Core migration 2 removes the former user billing-address columns. It refuses
to run while any of those fields is non-null, including an empty string.
Before upgrading an existing installation, stop application writes and preserve
those values through an operator-reviewed export or consumer-owned migration,
then clear the old fields. Keep the export protected with the database backup;
do not put personal data in source control, logs or issue reports.

Inspect and preserve any failed migration's ledger state before recovery.
Clearing the fields alone does not repair a dirty ledger. Never force a version
or replay the initial baseline to hide a failure; use a reviewed recovery plan.

Use a dedicated application database. Create a migration/schema owner and a
separate unprivileged runtime login with customer-managed passwords. The runtime
role must have no role memberships, own no application objects, and have none of
SUPERUSER, CREATEDB, CREATEROLE, REPLICATION or BYPASSRLS. Keep migration and
operator credentials out of running API/frontend/worker/executor containers.
Run core migrations with the migration owner's credentials, then apply:

```sh
psql -v ON_ERROR_STOP=1 -v migrator_role=customer_migrator \
  -v runtime_role=customer_runtime \
  -f scripts/self-hosted-runtime-grants.sql
```

Connect to the intended customer database as its schema and migration owner,
or as a trusted operator provisioning the database. Create the login roles and
set their passwords before running the script.

The script rejects unsafe or shared roles and removes PUBLIC schema access.
It gives the runtime role schema usage and table/sequence data access, plus
read-only access to `core_schema_migrations` for startup compatibility checks.
The runtime role cannot write to the migration ledger. The migration role gets
schema creation rights and default grants for objects it creates in future.
Existing objects must already belong to that role; the script leaves ownership
unchanged. Reapply it after upgrades, including the migration-ledger exception,
and review any additional schemas or extensions separately.

The prepared installation uses the runtime login for API, worker and frontend
processes. Protect that credential: it can read and change application data.
The dedicated executor uses a separate login with the narrow capability grants
in `scripts/installation/execution-grants.sql`; it has no general table access.
Use the protected operator identity when creating administrators. These roles
separate migration ownership from runtime data access. Verify the effective
grants and process/network controls in the deployment you operate.

Supply both SMTP credential fields or leave both empty. Startup rejects partial
credentials. A relay that allows anonymous delivery can use empty credentials
with verified TLS.

## Validation and its limits

These results cover the local fixture. Before release, test the packaging,
process and network isolation, and installation on a clean machine.

| Boundary | Current local evidence |
|---|---|
| Bootstrap and recovery | The [PostgreSQL suite](../service/instanceadminsvc/e2e/bootstrap_test.go) tests concurrent bootstrap attempts and rollback when the audit write fails. The [recovery suite](../service/instanceadminsvc/e2e/recovery_test.go) tests login, encrypted River arguments, worker reset links, password replacement and single-use tokens. |
| Runtime DB authority | The [grant fixture](../postgres/e2e/runtime_grants_test.go) checks data access, default grants and startup schema checks. It also tests repeated application of the grants, denied schema/ledger writes and rejection of unsafe roles. |
| Customer SMTP trust | The [SMTP suite](../service/email/e2e/customer_smtp_test.go) delivers recovery email with an explicit CA. It rejects unknown CAs, certificate-name mismatches and servers without STARTTLS. |
| Telemetry disabled | Logging and tracing tests confirm that empty application endpoints disable export even with ambient OTLP variables set. File-logging tests check rotation of the active and previous files. |
| Private PostgreSQL scope | The [connector-factory test](../backend/e2e/private_source_test.go) connects inside the internal network using the customer CA and confirms TLS is active. It rejects another tenant or source, the wrong port, a changed IP pin and an untrusted CA. Complete pipelines and executor isolation need separate tests. |
| Full local application | Fresh backend/migrator/frontend images run with PostgreSQL, SMTP and a customer TLS edge on a Docker internal network, with no published ports. A minimal customer config and fresh keys replace developer settings. |
| Customer DNS/CA and roles | Core migrations succeed with a separate migration login and customer-CA database TLS. API and control-worker readiness return 200 with the restricted runtime login. The frontend resolves the customer application name and connects over verified HTTPS. |
| HTTP account journey | Operator bootstrap and local login pass. Signup stays closed and Google stays disabled; unknown-account reset requests receive a generic response. The worker delivers reset mail through SMTP with customer-origin links. Password replacement succeeds and token reuse fails. |
| Frontend session protections | Tests through the TLS edge verify Secure, HttpOnly and SameSite=Lax cookies. Missing or invalid CSRF tokens are rejected; valid-CSRF login and shared-session profile API access succeed. Google and signup actions are hidden. |
| Account signup | The temporary signup-enabled fixture renders the signup form and creates an account through the API using a name, email and password. The operator restores closed registration afterward. |
| Public CLI trust | The session API creates a one-hour synthetic PAT in a protected fixture file. The public CLI authenticates to the customer HTTPS API. The same request fails certificate validation without customer CA trust. The test leaves the CLI credential store untouched and keeps TLS checks enabled. |
| Actual browser journey | Chromium trusts the fixture CA through NSS and passes login, cookie and profile-access checks. Unconfigured Google and signup actions stay hidden. Page requests use only the customer HTTPS origin; unexpected origins are rejected. Other browser features and background traffic need separate capture. |
| Optional providers | HubSpot, BigQuery, Workspace, GitLab and Slack onboarding return the local unconfigured-provider code; GitHub App returns its local unavailable code without an authorization URL. Failed source installs leave no partial sources. These attempts are included in the destination trace. |
| Process connect capture | The test tracer records API/worker startup, authentication/reset and CLI profile access. Parsed destinations match the exact DB, SMTP, TLS-edge and Docker-DNS allowlists; unknown formats or destinations fail the check. Normal images and capabilities are restored afterward. Unconnected datagrams and features outside the test are omitted. |
| Network baseline | Application containers attach only to the internal fixture network. A public TCP connection from a probe fails with network-unreachable. Other outbound traffic needs separate capture. |

Generated secrets, certificates and status-only receipts remain in ignored
`.worktree/` storage. The application test uses `EXECUTION_BACKEND=poller`
and explicit stdout-only logging. Its certificates contain CA signing key usage
and leaf key identifiers; no certificate-verification bypass is enabled.

The [acceptance fixture](../scripts/self_hosted_acceptance/README.md) repeated
these application and network checks from a newly generated directory. Its
preparation tests check secret-file permissions, distinct credentials and keys,
and protection against overwriting existing fixture state.

The current private-source path supports the pinned PostgreSQL endpoint and
denies other private destinations supplied in source payloads. Certificate and
tunnel support for other connectors needs further work. Complete pipelines and
executors need separate tests.

A release still needs an independent operator's walkthrough, backup and restore
tests, and outbound-traffic checks across more features and protocols. The
startup, account, provider and CLI results above cover the tested flows. General
private-network connectivity still needs verification.
