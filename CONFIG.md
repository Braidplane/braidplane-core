# Configuration Reference

The backend, frontend, release migrations and local fake-data seeder read
configuration in this order:

1. Command-line flags (highest priority)
2. Environment variables
3. TOML config file (`config.toml`)
4. Default values (lowest priority)

## Configuration Methods

### TOML File
Backend, frontend, release migrations and the local fake-data seeder default to
`config.toml`. Override with `CONFIG_PATH`, or `--config` for the seeder.

Supply the required secrets before starting the backend. To leave the optional
GitHub App unconfigured, keep every `github_proof` value empty or zero. This
example omits those settings.

```toml
frontend_url = "http://localhost:3002"
backend_api_url = "http://localhost:3001"
default_language = "en"

[deployment]
profile = "local"

[server]
host = "0.0.0.0"
port = 3000

[frontend_server]
host = "0.0.0.0"
port = 3002

[database]
host = "localhost"
port = 5432

[security]
allow_private_data_source_hosts = false

[proof]
git_checks_enabled = false

[bigquery]
base_url = ""
token_url = ""
allow_private_hosts = false
```

### Environment Variables
```bash
export SERVER_HOST=0.0.0.0
export SERVER_PORT=3000
export FRONTEND_SERVER_PORT=3002
export DEPLOYMENT_PROFILE=local
export DB_HOST=localhost
export DB_PORT=5432
export ALLOW_PRIVATE_DATA_SOURCE_HOSTS=false
export PROOF_GIT_CHECKS_ENABLED=false
```

### Command-line Flags
```bash
./braidplane-server api --deployment-profile=local
./braidplane-server worker --deployment-profile=local
./braidplane-frontend --deployment-profile=local --frontend-server-host=0.0.0.0 --frontend-server-port=3002 --backend-api-url=http://localhost:3001
./braidplane-migrate --db-host=localhost --db-port=5432 up
bash scripts/go-toolchain.sh run ./cmd/fake --config config.toml --db-host=localhost --db-port=5432
```

The backend requires a Kong role command on each invocation.
`api` starts HTTP and job producers without River consumers. `worker` starts
River consumers without the public API, serving `/healthz`, `/readyz` and
optional metrics for private operations.

Run independently launched API and worker processes on distinct `server.port`
values; see [the Community walkthrough](COMMUNITY_BUILD.md). Both roles must
select the same `execution.backend` and database. The default `river` backend
dispatches encrypted execution envelopes to a worker; `poller` requires an
explicit isolated-executor deployment and never falls back to River.

## Configuration Variables

Runtime environment records such as `dev`, `stg`, and `prod` describe pipeline
targets in the product. See Runtime Environments for the environment entity,
project enablement, data-source bindings and pipeline deployment pointers.
Process settings such as `deployment.profile`, `logging.environment`, URLs,
database settings and secrets describe the running Braidplane deployment.

The user-facing `braidplane` resource CLI stores saved login metadata in a
separate user config file and uses `BRAIDPLANE_*` environment variables for API
authentication. See CLI Commands for that login configuration; it is separate
from the process settings in this guide.

### Feature controls

Use positive boolean settings for temporary feature controls in the relevant
existing section, with new behavior defaulting to `false`. Controls load at
startup and apply to the whole deployment. Provide
matching TOML, environment and CLI forms with the precedence above,
for example `proof.git_checks_enabled`, `PROOF_GIT_CHECKS_ENABLED`, and `--proof-git-checks-enabled`.

Name controls after capabilities and check them at the narrow exposure
boundary. Do not use them for authorization, tenant isolation, validation,
redaction, or conditional migrations. The owning rollout must document
enablement, rollback, and the cleanup issue that removes the temporary control.

### Deployment security profile

`deployment.profile` selects the safety checks the backend and standalone
frontend run at startup. CI behavior is configured separately. Use `local` for
developer worktrees and local CI-style runs. Use `staging` for pre-production
deployments, `production` for live deployments and `self-hosted` for installations
with private application origins or a pinned private PostgreSQL source. These
deployment profiles reject unsafe local defaults. Staging and production
currently apply the same Core startup checks.

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Profile | `deployment.profile` | `DEPLOYMENT_PROFILE` | `--deployment-profile` | `local` | string | Startup safety profile. Valid values are `local`, `staging`, `production`, and `self-hosted`. |

Non-local startup checks:

- Backend and standalone frontend reject local, loopback, or wildcard database hosts, empty database credentials, and `postgres`/`postgres` credentials.
- Staging and production require `database.ssl_mode` to be `require`, `verify-ca` or `verify-full`, including databases on private networks. The `self-hosted` profile requires `verify-full`; supply the customer CA through `database.ssl_root_cert` when needed.
- Backend and standalone frontend require secure session cookies and reject debug logging plus full HTTP request/response body logging.
- Backend requires `frontend_url`, `auth.google_redirect_uri`, and the CORS origin to use HTTPS. Staging and production require public origins. The `self-hosted` profile permits operator-configured private IP application and documentation origins; local, loopback and wildcard hosts remain rejected. The Google redirect URI must use the same origin as `frontend_url` and end with `/api/auth/google/callback`; `cors.allowed_origins` must contain exactly that application origin.
- Backend requires `server.drain_timeout` to be greater than `0s` outside local development so readiness can turn not-ready before the listener starts graceful shutdown.
- Backend rejects local SMTP hosts, `smtp.insecure_skip_verify = true`, missing or repository-default no-reply addresses, and development no-reply placeholders.
- Standalone frontend requires `frontend_server.drain_timeout` to be greater than `0s` outside local development and rejects `backend_api_url` values that target local, loopback, or wildcard hosts. HTTP is allowed here for trusted private service-to-service links; browser-facing URLs must still be HTTPS at the reverse proxy.

Private application origins are a deployment setting. They leave
`allow_private_hosts`, tenant authorization and connector access unchanged.
Configure customer DNS and TLS trust in the browser and process trust stores;
certificate verification stays enabled.

### Backend Server Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Host | `server.host` | `SERVER_HOST` | `--server-host` | `0.0.0.0` | string | Server host to bind to |
| Port | `server.port` | `SERVER_PORT` | `--server-port` | `3000` | int | Server port to listen on |
| Shutdown Timeout | `server.shutdown_timeout` | `SERVER_SHUTDOWN_TIMEOUT` | `--server-shutdown-timeout` | `30s` | duration | HTTP graceful shutdown timeout. River worker cleanup waits for active jobs independently of this deadline. |
| Drain Timeout | `server.drain_timeout` | `SERVER_DRAIN_TIMEOUT` | `--server-drain-timeout` | `0s` | duration | Readiness drain window before graceful shutdown starts. Must be greater than `0s` in non-local deployment profiles. |
| Static Cache Max Age | `server.static_cache_max_age` | `SERVER_STATIC_CACHE_MAX_AGE` | `--server-static-cache-max-age` | `0` | int | Max-Age for static assets in seconds. `0` disables caching (dev). Set to `31536000` in production. |
| Proxy Header | `server.proxy_header` | `SERVER_PROXY_HEADER` | `--server-proxy-header` | `` | string | Trusted reverse proxy header used to derive client IP, such as `X-Forwarded-For`. When the standalone frontend proxies browser calls to the backend, configure this only for the frontend's exact peer addresses or CIDRs. Leave empty unless direct traffic is restricted to trusted proxies. |
| Trusted Proxies | `server.trusted_proxies` | `SERVER_TRUSTED_PROXIES` | `--server-trusted-proxies` | `[]` | []string | Reverse proxy IPs or CIDR ranges trusted to set `server.proxy_header`. Environment variables and flags use comma-separated values. |

### Standalone Frontend Server Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Host | `frontend_server.host` | `FRONTEND_SERVER_HOST` | `--frontend-server-host` | `0.0.0.0` | string | Standalone frontend server host to bind to |
| Port | `frontend_server.port` | `FRONTEND_SERVER_PORT` | `--frontend-server-port` | `3002` | int | Standalone frontend server port to listen on |
| Shutdown Timeout | `frontend_server.shutdown_timeout` | `FRONTEND_SERVER_SHUTDOWN_TIMEOUT` | `--frontend-server-shutdown-timeout` | `30s` | duration | Standalone frontend graceful shutdown timeout |
| Drain Timeout | `frontend_server.drain_timeout` | `FRONTEND_SERVER_DRAIN_TIMEOUT` | `--frontend-server-drain-timeout` | `0s` | duration | Standalone frontend readiness drain window before graceful shutdown starts. Must be greater than `0s` in non-local deployment profiles. |
| Static Cache Max Age | `frontend_server.static_cache_max_age` | `FRONTEND_SERVER_STATIC_CACHE_MAX_AGE` | `--frontend-server-static-cache-max-age` | `0` | int | Max-Age for successful standalone frontend assets whose sole `v` query matches the file's 12-character SHA-256 prefix. `0`, unversioned or mismatched requests, extra query parameters, and error responses use `no-store`. Set to `31536000` in production. |
| Proxy Header | `frontend_server.proxy_header` | `FRONTEND_SERVER_PROXY_HEADER` | `--frontend-server-proxy-header` | `` | string | Trusted reverse proxy header used by the standalone frontend to derive client IP. |
| Trusted Proxies | `frontend_server.trusted_proxies` | `FRONTEND_SERVER_TRUSTED_PROXIES` | `--frontend-server-trusted-proxies` | `[]` | []string | Reverse proxy IPs or CIDR ranges trusted by the standalone frontend. Environment variables and flags use comma-separated values. |

### Database Configuration

The backend, standalone frontend, and release migration command all use this
PostgreSQL connection. The standalone frontend uses it only for browser session
storage so forwarded `/api/*` requests share the same session table as the
backend. Supply `DB_USER`/`DB_PASSWORD` for each process through its environment.
Grant the frontend access to its configured session table, which defaults to
`user_sessions`. Use separate runtime and migration logins with the permissions
each process needs.

The prepared installation currently supplies the same runtime login and
`runtime.env` to the API, worker and frontend. That file includes application
and execution keys. The frontend uses only session storage in its code, but
its supplied database privileges and environment are broader. Session-only
frontend grants require a separately configured deployment.

The core migrator selects its catalog with `up --path <directory>` and defaults
to the `core_schema_migrations` ledger. Ledger names must be lowercase PostgreSQL
identifiers of at most 63 characters. Runtime readiness requires the exact clean
catalog version. Before migrating an existing database, verify its compatibility
with the selected catalog; changing the ledger name alone cannot establish it.

Use TLS for non-local database connections and give runtime and migration roles
only the privileges each needs. Verify those grants in the deployment itself;
local fixtures use different permissions.

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Host | `database.host` | `DB_HOST` | `--db-host` | `localhost` | string | Database server hostname |
| Port | `database.port` | `DB_PORT` | `--db-port` | `35432` | int | Database server port |
| User | `database.user` | `DB_USER` | `--db-user` | `postgres` | string | Database username |
| Password | `database.password` | `DB_PASSWORD` | `--db-password` | `postgres` | string | Database password |
| Database | `database.database` | `DB_DATABASE` | `--db-database` | `braidplane` | string | Database name |
| SSL Mode | `database.ssl_mode` | `DB_SSL_MODE` | `--db-ssl-mode` | `disable` | string | SSL mode (disable, require, verify-ca, verify-full) |
| SSL Root Certificate | `database.ssl_root_cert` | `DB_SSL_ROOT_CERT` | `--db-ssl-root-cert` | empty | string | PEM root certificate path used by PostgreSQL `verify-ca`/`verify-full`; required by the isolated executor |
| Max Connections | `database.max_conns` | `DB_MAX_CONNS` | `--db-max-conns` | `25` | int | Maximum connections in pool |
| Min Connections | `database.min_conns` | `DB_MIN_CONNS` | `--db-min-conns` | `5` | int | Minimum connections in pool |
| Max Connection Lifetime | `database.max_conn_lifetime` | `DB_MAX_CONN_LIFETIME` | `--db-max-conn-lifetime` | `3600` | int | Max connection lifetime (seconds) |
| Max Connection Idle Time | `database.max_conn_idle_time` | `DB_MAX_CONN_IDLE_TIME` | `--db-max-conn-idle-time` | `300` | int | Max connection idle time (seconds) |
| Health Check Period | `database.health_check_period` | `DB_HEALTH_CHECK_PERIOD` | `--db-health-check-period` | `60` | int | Health check interval (seconds) |

### Braidplane Proof and GitHub App

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Proof Git Checks Enabled | `proof.git_checks_enabled` | `PROOF_GIT_CHECKS_ENABLED` | `--proof-git-checks-enabled` | `false` | bool | Publish informational Proof results for exact Git candidates: GitHub checks and GitLab merge-request notes. |
| App ID | `github_proof.app_id` | `GITHUB_PROOF_APP_ID` | `--github-proof-app-id` | `0` | int | Numeric identity of the deployment-owned GitHub App. |
| Client ID | `github_proof.client_id` | `GITHUB_PROOF_CLIENT_ID` | `--github-proof-client-id` | empty | string | OAuth client ID used only to verify installation authority. |
| Client Secret | `github_proof.client_secret` | `GITHUB_PROOF_CLIENT_SECRET` | `--github-proof-client-secret` | empty | string | Deployment-owned OAuth client secret; never persisted as tenant data. |
| Private Key File | `github_proof.private_key_file` | `GITHUB_PROOF_PRIVATE_KEY_FILE` | `--github-proof-private-key-file` | empty | string | Runtime file reference for the App private key used to mint short-lived installation tokens. |
| Callback URL | `github_proof.callback_url` | `GITHUB_PROOF_CALLBACK_URL` | `--github-proof-callback-url` | empty | string | Authenticated callback URL; organization and actor identity come from bounded install state. |
| Installation URL | `github_proof.installation_url` | `GITHUB_PROOF_INSTALLATION_URL` | `--github-proof-installation-url` | empty | string | GitHub-hosted App installation URL used by Connect GitHub. |
| Webhook Secret | `github_proof.webhook_secret` | `GITHUB_PROOF_WEBHOOK_SECRET` | `--github-proof-webhook-secret` | empty | string | Deployment-owned secret for authenticated App lifecycle and Proof intake; minimum 32 characters. |

Core Proof APIs and protected receipt pages are always available. The API and
worker start with an all-zero `github_proof` configuration; GitHub operations
then report unavailable. Setting any App value requires the complete
configuration and a readable, valid private key. Partial configuration stops
startup. Supply these settings to the API and worker processes.

For local or CI tests that do not contact GitHub, set `FRONTEND_URL` to the
local frontend origin and explicitly `source scripts/github-app-test-env.sh`
before starting the app or running runtime tests. Set
`BRAIDPLANE_GITHUB_TEST_KEY_FILE` to reuse a local test key; otherwise this
generates a disposable private key and exports dummy App credentials. These
cannot exercise real GitHub onboarding. Remove a disposable key directory
after stopping its test runtime; retain a worktree key until worktree cleanup.

Configure the GitHub App webhook URL as `/api/webhooks/github/pull-requests`.
Check publication and live observations retain their separate startup controls.
Use explicit environment policy disable and observation cancellation to stop
work. Starting a process does not revoke stored tenant policies. Retention stays
active independently of observation admission.

The temporary `proof.enabled` / `PROOF_ENABLED` / `--proof-enabled` setting has
been removed. Remove it from deployment configuration and launch arguments;
it is no longer a rollback or emergency-stop control.

### Operational contract evidence

The versioned operational policy controls admission of operational structural
receipts. Admission requires pipeline execute/read and data-source read
permission. It makes no provider calls and publishes nothing to Git. Run-history
retention and Proof's separate seven-day cap apply. Historical captures keep
their original identities.

The temporary composition and operational-evidence controls were removed after
the accepted rollout. Remove their old TOML, environment and CLI settings
from deployments. Rollback now requires a compatible application revision;
retain supported plan readers and never rewrite historical pins or effects.

### Live Proof observations

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Live observations enabled | `proof_observations.enabled` | `PROOF_OBSERVATIONS_ENABLED` | `--proof-observations-enabled` | `false` | bool | Expose explicit observation policy, preview, authorization, status, cancellation, and receipt compilation APIs. |
| Evidence key | `proof_observations.evidence_key_hex` | `PROOF_OBSERVATIONS_EVIDENCE_KEY_HEX` | `--proof-observations-evidence-key-hex` | empty | string | Independent 32-byte AES key encoded as 64 hexadecimal characters; surrounding whitespace is trimmed and comparisons ignore hexadecimal letter case. Must differ from credential and execution-envelope keys and must not be zero or the development placeholder. Supply through a protected process environment. |

Supply the evidence key through your deployment's protected process environment.
Provisioning it does not enable live observations. Disabled observation controls
return `404` while ordinary static/cached Proof compilation remains available.
Validate least-privilege provider access, revocation, cancellation, audit,
retention and alerting before enabling observations in your deployment.

Rollback restores `proof_observations.enabled=false`; data and schema remain
compatible. Stored live claims display as stale while their current authority
cannot be revalidated; stored receipts remain immutable. The independent River
maintenance job continues erasing expired
evidence without a decryption key. Controls load at process startup; environment
policy revocation and per-job cancellation apply during execution. Removing
this temporary deployment control requires acceptance of the full observation
workflow; tenant policy and user authorization remain mandatory. The shared connector executor
handles observations with the original reserved deadline and no provider retry.

### Backend Session Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Table Name | `session.table_name` | `SESSION_TABLE_NAME` | `--session-table-name` | `user_sessions` | string | Session storage table; must match the frontend and database grants |
| Cookie Secure | `session.cookie_secure` | `SESSION_COOKIE_SECURE` | `--session-cookie-secure` | `true` | bool | Set Secure flag on session cookies |

### Standalone Frontend Session Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Table Name | `frontend_session.table_name` | `FRONTEND_SESSION_TABLE_NAME` | `--frontend-session-table-name` | `user_sessions` | string | Session storage table shared with the backend; requires matching database grants |
| Cookie Secure | `frontend_session.cookie_secure` | `FRONTEND_SESSION_COOKIE_SECURE` | `--frontend-session-cookie-secure` | `true` | bool | Set Secure flag on standalone frontend session cookies |

Backend and frontend authentication cookies are host-only because the runtime
does not set a cookie `Domain`. They are also `Secure`, `HttpOnly`,
`SameSite=Lax`, and scoped to `/`, so staging and production application hosts
cannot share sessions.

### Security Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Argon2 Memory | `security.argon2_memory` | `ARGON2_MEMORY` | `--argon2-memory` | `65536` | uint32 | Memory usage in KB |
| Argon2 Iterations | `security.argon2_iterations` | `ARGON2_ITERATIONS` | `--argon2-iterations` | `3` | uint32 | Number of iterations |
| Argon2 Parallelism | `security.argon2_parallelism` | `ARGON2_PARALLELISM` | `--argon2-parallelism` | `2` | uint8 | Degree of parallelism |
| Argon2 Salt Length | `security.argon2_salt_length` | `ARGON2_SALT_LENGTH` | `--argon2-salt-length` | `16` | uint32 | Length of salt in bytes |
| Argon2 Key Length | `security.argon2_key_length` | `ARGON2_KEY_LENGTH` | `--argon2-key-length` | `32` | uint32 | Length of derived key in bytes |
| Encryption Key Hex | `security.encryption_key_hex` | `ENCRYPTION_KEY_HEX` | `--encryption-key-hex` | required | string | Deployment-specific AES-256 key (must be exactly 64 hex chars and must not use the repository default or all-zero placeholders) |
| Credential Current Key ID | `security.credential_current_key_id` | `CREDENTIAL_CURRENT_KEY_ID` | `--credential-current-key-id` | `initial` when both credential key fields are empty | string | Key ID used for new product ciphertext; must exist in the explicit keyring when configured |
| Credential Keys Hex | `security.credential_keys_hex` | `CREDENTIAL_KEYS_HEX` | `--credential-keys-hex` | original application key under `initial` | string | Protected JSON object mapping key IDs to 64-character AES-256 hex keys; retain historical keys for reads |
| Credential Disable Legacy | `security.credential_disable_legacy` | `CREDENTIAL_DISABLE_LEGACY` | `--credential-disable-legacy` | `false` | bool | Reject unversioned product ciphertext after all retained records have migrated; queued email and extension keys keep separate lifecycles |
| Execution Request Key Hex | `security.execution_request_key_hex` | `EXECUTION_REQUEST_KEY_HEX` | `--execution-request-key-hex` | required for `api`, `worker`, and `executor` roles | string | Dedicated AES-256 key for short-lived execution request/result envelopes; must differ from the application encryption key |
| Allow Plaintext Connectors | `security.allow_plaintext_connectors` | `ALLOW_PLAINTEXT_CONNECTORS` | `--allow-plaintext-connectors` | `false` | bool | Backend-only local development exception for plaintext connector fixtures. Startup rejects `true` in every non-local profile and backend role. Private host access does not enable this exception. |
| Allow Private Data Source Hosts | `security.allow_private_data_source_hosts` | `ALLOW_PRIVATE_DATA_SOURCE_HOSTS` | `--allow-private-data-source-hosts` | `false` | bool | Allows data source payloads to opt into private or loopback hosts with `allow_private_hosts: true`. Use for local fixtures. The self-hosted profile rejects this setting; use an explicit destination grant. |

Follow [credential encryption operations](doc/CREDENTIAL_ENCRYPTION.md) for key custody, rotation, rollback and recovery drills. Supply keys through protected configuration or environment variables; keep them out of command-line arguments.

For a private CA, add its public certificate to the runtime trust store.
Set `SSL_CERT_DIR` through the standard Go environment setting; it has no
TOML or CLI field. Keep the system roots and hostname verification enabled.
Restart processes after changing trust because Go caches the roots. Fix missing
trust certificates while keeping TLS verification enabled.

By default, create/update and draft connection tests reject `allow_private_hosts: true`
for PostgreSQL, MySQL, MongoDB, HTTP, FTP, Git/GitHub/GitLab and S3. Local
development fixtures may enable `security.allow_private_data_source_hosts`.
The self-hosted profile instead requires an explicit
[pinned PostgreSQL source](#pinned-private-postgresql-source); keep payload
`allow_private_hosts` false. Other private connector targets are not enabled by
that grant.

CLI flags/request files preserve omitted/false `allow_private_hosts`.
PostgreSQL UI/CLI preserve `ssl_mode` (URI: `sslmode`);
see [TLS policy](doc/data-sources/POSTGRES.md#transport-security).

### Authentication Configuration (OAuth/OIDC)

These Google settings configure user OIDC login. Data-source grants use separate
settings. Keep connector OAuth2 app credentials in backend deployment
configuration and out of data-source API payloads. Connector credentials entered
through the product are write-only application data.

`auth.self_service_registration_enabled` controls public account creation and
defaults to false. Enable it to allow signup. When disabled, it blocks
email/password signup and first-time OIDC account creation. Existing sessions
and accounts still work, including email/password and linked OIDC login,
password reset, email verification and MFA. The frontend hides signup links and
shows that registration is closed; the backend rejects new account requests.

Deployments that previously relied on the open default must explicitly set
`SELF_SERVICE_REGISTRATION_ENABLED=true` in both API and frontend environments
to retain public signup. The local `task dev` launcher supplies that opt-in.

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Google Client ID | `auth.google_client_id` | `GOOGLE_CLIENT_ID` | `--google-client-id` | `` | string | Google OAuth client ID |
| Google Client Secret | `auth.google_client_secret` | `GOOGLE_CLIENT_SECRET` | `--google-client-secret` | `` | string | Google OAuth client secret |
| Google Redirect URI | `auth.google_redirect_uri` | `GOOGLE_REDIRECT_URI` | `--google-redirect-uri` | `http://localhost:3002/api/auth/google/callback` | string | Google OAuth redirect URI. Non-local profiles require the exact application-origin `/api/auth/google/callback` URL. |
| Self-Service Registration Enabled | `auth.self_service_registration_enabled` | `SELF_SERVICE_REGISTRATION_ENABLED` | `--self-service-registration-enabled` | `false` | bool | Enable public email/password signup and first-time OIDC user auto-creation |

Configure the Google client ID and secret together to enable Google sign-in.
Without them, Google sign-in returns unavailable; email/password authentication
remains available. The backend discovers Google's OIDC endpoints when sign-in is
requested, so startup does not require Google network access. Configured Google
sign-in requires network access to Google for discovery, token exchange and keys.

### Customer HTTP OAuth

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| HTTP OAuth Redirect URI | `http_oauth.oauth_redirect_uri` | `HTTP_OAUTH_REDIRECT_URI` | `--http-oauth-redirect-uri` | `` | string | Trusted API callback URL ending in `/provider-connectors/http/callback`. HTTPS is required, with no user info, query, or fragment; non-local deployments require a public URL. Empty disables authorization-code setup while client-credentials remains available. |

Register this API callback on the customer app, separately from the frontend.
The callback `iss` must match the saved issuer. Encrypted app settings are
write-only source credentials. See
[HTTP OAuth setup](doc/data-sources/HTTP.md#oauth-setup).

### HubSpot Connector Overrides

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| HubSpot Base URL | `hubspot.base_url` | `HUBSPOT_BASE_URL` | `--hubspot-base-url` | `` | string | Optional runtime override for the HubSpot API base URL. Intended for local mock testing and development. |
| HubSpot Allow Private Hosts | `hubspot.allow_private_hosts` | `HUBSPOT_ALLOW_PRIVATE_HOSTS` | `--hubspot-allow-private-hosts` | `false` | bool | Allows the runtime HubSpot connector override to target private or loopback hosts for local mock testing. |
| HubSpot OAuth Client ID | `hubspot.oauth_client_id` | `HUBSPOT_OAUTH_CLIENT_ID` | `--hubspot-oauth-client-id` | `` | string | HubSpot app client ID for this deployment. Configure with the client secret and redirect URI in backend configuration. |
| HubSpot OAuth Client Secret | `hubspot.oauth_client_secret` | `HUBSPOT_OAUTH_CLIENT_SECRET` | `--hubspot-oauth-client-secret` | `` | string | HubSpot app client secret for this deployment. Keep it in the protected process environment and out of API responses and logs. |
| HubSpot OAuth Redirect URI | `hubspot.oauth_redirect_uri` | `HUBSPOT_OAUTH_REDIRECT_URI` | `--hubspot-oauth-redirect-uri` | `` | string | Exact developer-app callback URI ending in `/provider-connectors/hubspot/callback`. Non-local deployments require a public HTTPS URL. |
| HubSpot OAuth Base URL | `hubspot.oauth_base_url` | `HUBSPOT_OAUTH_BASE_URL` | `--hubspot-oauth-base-url` | `` | string | Optional override for deterministic local mock testing. Leave empty for `https://api.hubapi.com`; startup rejects a non-empty value in staging and production. |

### Slack notifications

Configure all three fields together to register the Slack notification OAuth provider.
All default to an empty string. Slack uses fixed OAuth and webhook endpoints.
The callback requires HTTPS, including for local installations using a public tunnel.

| TOML | Environment | Flag | Type | Behavior |
|------|-------------|------|------|----------|
| `slack.oauth_client_id` | `SLACK_OAUTH_CLIENT_ID` | `--slack-oauth-client-id` | string | Slack app client ID for this deployment. |
| `slack.oauth_client_secret` | `SLACK_OAUTH_CLIENT_SECRET` | `--slack-oauth-client-secret` | string | Deployment-owned app secret; never send it in API payloads. |
| `slack.oauth_redirect_uri` | `SLACK_OAUTH_REDIRECT_URI` | `--slack-oauth-redirect-uri` | string | Exact registered HTTPS callback on the authenticated app origin, ending in `/provider-connectors/slack/callback`. |

Request only `incoming-webhook`. Slack's installation screen chooses the channel;
no history or channel read permissions are needed. Workspace administrators may
need to approve the app. See Slack notification lifecycle.

### GitLab OAuth

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| GitLab OAuth Client ID | `gitlab.oauth_client_id` | `GITLAB_OAUTH_CLIENT_ID` | `--gitlab-oauth-client-id` | `` | string | Deployment-owned GitLab OAuth application ID. |
| GitLab OAuth Client Secret | `gitlab.oauth_client_secret` | `GITLAB_OAUTH_CLIENT_SECRET` | `--gitlab-oauth-client-secret` | `` | string | Deployment-owned GitLab OAuth application secret; never expose it in API responses or logs. |
| GitLab OAuth Redirect URI | `gitlab.oauth_redirect_uri` | `GITLAB_OAUTH_REDIRECT_URI` | `--gitlab-oauth-redirect-uri` | `` | string | Exact callback URI ending in `/provider-connectors/gitlab/callback`; non-local deployments require public HTTPS. |
| GitLab OAuth Base URL | `gitlab.oauth_base_url` | `GITLAB_OAUTH_BASE_URL` | `--gitlab-oauth-base-url` | `` | string | GitLab instance base URL. Leave empty for `https://gitlab.com`; non-local overrides require public HTTPS. |

### BigQuery Connector Configuration

Configure all three OAuth fields together to enable Connect with Google. Leaving
all three empty disables Google OAuth onboarding. The UI uses OAuth only. OAuth uses fixed
Google endpoints; service-account endpoint overrides do not alter connector
consent, refresh, or revocation. See [BigQuery authorization](doc/data-sources/BIGQUERY.md#credentials)
for consent-screen and reauthentication requirements. The client ID and secret may
reuse the application-login web client when its authorized redirects include the
BigQuery callback. Set the BigQuery fields explicitly; there is no login-config fallback.

Use the endpoint overrides for trusted deployment routing, including private
Google API endpoints, egress proxies and local test fixtures. Set them in
deployment configuration; BigQuery data source API payloads cannot supply them.

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| BigQuery OAuth Client ID | `bigquery.oauth_client_id` | `BIGQUERY_OAUTH_CLIENT_ID` | `--bigquery-oauth-client-id` | `` | string | Deployment-owned Google OAuth client for connector authorization; may reuse the login client with a separately registered BigQuery callback. |
| BigQuery OAuth Client Secret | `bigquery.oauth_client_secret` | `BIGQUERY_OAUTH_CLIENT_SECRET` | `--bigquery-oauth-client-secret` | `` | string | Deployment-owned Google OAuth client secret. |
| BigQuery OAuth Redirect URI | `bigquery.oauth_redirect_uri` | `BIGQUERY_OAUTH_REDIRECT_URI` | `--bigquery-oauth-redirect-uri` | `` | string | Exact registered callback ending in `/provider-connectors/bigquery/callback`; public HTTPS outside local deployments. |
| BigQuery Base URL | `bigquery.base_url` | `BIGQUERY_BASE_URL` | `--bigquery-base-url` | `` | string | Optional override for the BigQuery REST API base URL. Leave empty to use Google BigQuery's public endpoint. |
| BigQuery Token URL | `bigquery.token_url` | `BIGQUERY_TOKEN_URL` | `--bigquery-token-url` | `` | string | Optional override for service-account token exchange. Leave empty to use the service-account `token_uri` or Google OAuth token endpoint. |
| BigQuery Allow Private Hosts | `bigquery.allow_private_hosts` | `BIGQUERY_ALLOW_PRIVATE_HOSTS` | `--bigquery-allow-private-hosts` | `false` | bool | Allows BigQuery endpoint overrides to call private or loopback hosts. Enable only for trusted private routing or local verification. |

### Google Workspace

`google_workspace`: `GOOGLE_WORKSPACE_*` env vars; `--google-workspace-*` flags.
Strings default empty.

| Field | Behavior |
|-------|----------|
| `oauth_client_id`, `oauth_client_secret` | OAuth. |
| `oauth_redirect_uri` | Callback; nonlocal HTTPS. |
| `picker_api_key` | Public key restricted to Picker/Drive APIs and HTTP referrers. |
| `project_number` | Numeric client/Picker project number. |

OAuth needs Picker and GIS origins, separate from login/BigQuery.
Remove retired `google_workspace.enabled`, `GOOGLE_WORKSPACE_ENABLED` and
`--google-workspace-enabled` settings. Dispatch does not prove Google approval or
provider acceptance.
[Setup](doc/data-sources/GOOGLE_WORKSPACE.md).

### Snowflake Workload Identity

Customer WIF uses a local SPIRE Workload API socket and `wif_issuer` for its
database-managed lifecycle. Configure both or neither. An authorized saved
Snowflake connection selecting WIF records its exact identity intent; a separate
privileged reconciler provisions the registration. Pending saves return inactive
and defer their connection test until identity readiness. Each new provider session
requires a ready binding verified within the last 60 seconds. The readiness
lookup exposes no tenant tables to the executor. Configure the same issuer and
Workload API socket path for backend and executor; mount only the Workload API
socket into the executor. See
customer lifecycle.

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| WIF Socket Path | `snowflake.wif_socket_path` | `SNOWFLAKE_WIF_SOCKET_PATH` | `--snowflake-wif-socket-path` | `` | string | Absolute local SPIRE Workload API socket path. Required with the issuer. |
| WIF Issuer | `snowflake.wif_issuer` | `SNOWFLAKE_WIF_ISSUER` | `--snowflake-wif-issuer` | `` | string | HTTPS issuer origin without a port or path. Enables database-managed customer identities; requires the separate reconciler. |

See [Snowflake timeouts](doc/data-sources/SNOWFLAKE.md).

### CORS Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Allowed Origins | `cors.allowed_origins` | `CORS_ALLOWED_ORIGINS` | `--cors-allowed-origins` | `["http://localhost:3000"]` | []string | Allowed CORS origin URLs. Environment variables and flags use comma-separated values. |

CORS rules:

- At least one origin must be configured
- Each origin must be a valid URL with scheme (http:// or https://)
- Origins must not include a path, query string, or fragment
- Local deployments may configure multiple origins as a TOML array or comma-separated environment/flag value
- Non-local deployment profiles require exactly one HTTPS origin matching `frontend_url`; staging and production require a public host
- CORS does not allow credentials; browser sessions use the same-origin `/api` route

### SMTP Configuration

Mailpit skips reverse DNS: `MP_SMTP_DISABLE_RDNS=true`.

Configure your deployment's SMTP endpoint, sender and authentication through the
settings below. Keep passwords in the protected process environment. The
[Community walkthrough](COMMUNITY_BUILD.md) provides isolated local Mailpit setup.

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Host | `smtp.host` | `SMTP_HOST` | `--smtp-host` | `localhost:31025` | string | SMTP server host with port |
| User | `smtp.user` | `SMTP_USER` | `--smtp-user` | `` | string | SMTP username |
| Password | `smtp.password` | `SMTP_PASSWORD` | `--smtp-password` | `` | string | SMTP password |
| CA Certificate | `smtp.ca_cert` | `SMTP_CA_CERT` | `--smtp-ca-cert` | `` | string | SMTP CA certificate in PEM format |
| CA Certificate File | `smtp.ca_cert_file` | `SMTP_CA_CERT_FILE` | `--smtp-ca-cert-file` | `` | string | Path to SMTP CA certificate file in PEM format |
| Insecure Skip Verify | `smtp.insecure_skip_verify` | `SMTP_INSECURE_SKIP_VERIFY` | `--smtp-insecure-skip-verify` | `true` | bool | Skip SMTP TLS certificate verification |
| No-Reply Address | `smtp.no_reply_address` | `SMTP_NO_REPLY_ADDRESS` | `--smtp-no-reply-address` | `no-reply@braidplane.com` | string | No-reply email address |

### Logging Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Level | `logging.level` | `LOG_LEVEL` | `--log-level` | `debug` | string | Log level (none, debug, info, warn, error) |
| Backend File | `logging.backend_file` | `LOG_BACKEND_FILE` | `--log-backend-file` | `logs/backend.log` | string | Backend log file path. Owned by the backend process. Empty disables backend file logging. |
| Frontend File | `logging.frontend_file` | `LOG_FRONTEND_FILE` | `--log-frontend-file` | `logs/frontend.log` | string | Standalone frontend log file path. Owned by the standalone frontend process. Empty disables frontend file logging. |
| Rotation Interval | `logging.rotation_interval` | `LOG_ROTATION_INTERVAL` | `--log-rotation-interval` | `1h` | duration | Log file rotation interval (e.g., 1h, 24h) |
| Log HTTP Requests | `logging.log_http_requests` | `LOG_HTTP_REQUESTS` | `--log-http-requests` | `true` | bool | Log full HTTP request/response bodies |
| Max File Size (MB) | `logging.max_file_size_mb` | `LOG_MAX_FILE_SIZE_MB` | `--log-max-file-size-mb` | `100` | int | Maximum log file size in MB before rotation |
| Environment | `logging.environment` | `LOG_ENVIRONMENT` | `--log-environment` | `local` | string | Deployment environment included in structured log records |
| OTLP Logs Endpoint | `logging.otlp_logs_endpoint` | `LOG_OTLP_LOGS_ENDPOINT` | `--log-otlp-logs-endpoint` | `` | string | Full OTLP/HTTP logs endpoint (empty disables direct log export) |

Logging behavior:

- Logs are written to both stdout and file simultaneously
- Logs use structured JSON with `service.name`, `deployment.environment`, and `braidplane.component` fields.
- `logging.environment` controls only the structured log environment field; use `deployment.profile` for startup safety behavior
- When `logging.otlp_logs_endpoint` is set, logs are exported directly to the configured OTLP/HTTP collector endpoint
- Files rotate based on time interval OR file size (whichever comes first)
- File logging retains the active path and one previous file at `<path>.1`; the next rotation replaces that previous file. Use a distinct path per process. Rotation occurs before the next write after a time/size threshold, so one record can exceed the size threshold. Set a positive maximum size to bound file growth; stdout/container log retention must be configured separately.
- HTTP completion logs never include request or response bodies
- When `log_http_requests` is enabled, redacted request/response bodies and headers are logged at debug level
- Empty file path disables file logging for that component
- `logs/` directory is git-ignored but tracked with `.gitkeep`

### Tracing Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| OTLP Traces Endpoint | `tracing.otlp_traces_endpoint` | `TRACING_OTLP_TRACES_ENDPOINT` | `--tracing-otlp-traces-endpoint` | `` | string | Full OTLP/HTTP traces endpoint (empty disables trace export) |
| Sampling Ratio | `tracing.sampling_ratio` | `TRACING_SAMPLING_RATIO` | `--tracing-sampling-ratio` | `1.0` | float | Root trace sampling ratio from 0.0 to 1.0 |

Tracing behavior:

- Traces are pushed from Braidplane to the configured OTLP/HTTP collector endpoint
- Set `tracing.otlp_traces_endpoint` to your OTLP/HTTP collector's trace endpoint
- Empty `tracing.otlp_traces_endpoint` disables trace export
- Sampling uses parent-based sampling with `tracing.sampling_ratio` for root traces
- HTTP, service, repository, application database, and connector spans use safe attributes only
- `request_id`, `trace_id`, and `span_id` are kept as separate correlation fields

### Metrics Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Enabled | `metrics.enabled` | `METRICS_ENABLED` | `--metrics-enabled` | `false` | bool | Expose the Prometheus-compatible metrics endpoint |
| Path | `metrics.path` | `METRICS_PATH` | `--metrics-path` | `/metrics` | string | Prometheus-compatible metrics endpoint path |

Metrics behavior:

- Metrics are exposed as a Prometheus-compatible scrape endpoint only when `metrics.enabled` is true
- `metrics.path` must be an absolute, non-root path without query or fragment and must not use the reserved `/api` namespace
- The metrics endpoint should be reachable only from trusted local, private, proxy, or scraper networks
- API, worker, and standalone frontend processes expose separate metrics from their own HTTP servers
- The isolated executor exports queue depth/age, active/capacity saturation, terminal outcomes, and last-successful-poll liveness without tenant identifiers or envelope contents
- HTTP metrics use route templates and low-cardinality labels; raw paths, slugs, request IDs, trace IDs, and user identifiers are not metric labels

### Shared Abuse Controls Configuration

PostgreSQL enforces these budgets atomically across all API replicas and process
restarts. Every value must be positive. Authentication requests count against
both client and stable-subject budgets. Expensive connector, query and pipeline
operations count against client and tenant budgets. Credential mutations use the
narrower credential budget in both dimensions.

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Authentication Attempts | `abuse_controls.auth_attempts` | `ABUSE_CONTROLS_AUTH_ATTEMPTS` | `--abuse-controls-auth-attempts` | `5` | int | Maximum authentication attempts per client and stable subject in one window |
| Authentication Window | `abuse_controls.auth_window` | `ABUSE_CONTROLS_AUTH_WINDOW` | `--abuse-controls-auth-window` | `1m` | duration | Shared authentication throttle window |
| Expensive Client Attempts | `abuse_controls.expensive_client_attempts` | `ABUSE_CONTROLS_EXPENSIVE_CLIENT_ATTEMPTS` | `--abuse-controls-expensive-client-attempts` | `20` | int | Maximum connector, query, or pipeline operations per client in one window |
| Expensive Tenant Attempts | `abuse_controls.expensive_tenant_attempts` | `ABUSE_CONTROLS_EXPENSIVE_TENANT_ATTEMPTS` | `--abuse-controls-expensive-tenant-attempts` | `40` | int | Maximum connector, query, or pipeline operations per tenant in one window |
| Credential Attempts | `abuse_controls.credential_attempts` | `ABUSE_CONTROLS_CREDENTIAL_ATTEMPTS` | `--abuse-controls-credential-attempts` | `10` | int | Maximum credential mutations per client and tenant in one window |
| Expensive Window | `abuse_controls.expensive_window` | `ABUSE_CONTROLS_EXPENSIVE_WINDOW` | `--abuse-controls-expensive-window` | `1m` | duration | Shared expensive-operation throttle window |

Rate-limited requests return the same non-enumerating `429` response with a
`Retry-After` header. If the shared store is unavailable, protected requests
fail closed instead of falling back to process-local counters. To recover from
an accidental lockout, an operator can lower the window and restart the API;
the updated duration applies to each newly opened budget window.

### Execution Limits Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Query Timeout | `execution_limits.query_timeout` | `EXECUTION_LIMITS_QUERY_TIMEOUT` | `--execution-limits-query-timeout` | `30s` | duration | Maximum saved or inline query execution duration |
| Connector Timeout | `execution_limits.connector_timeout` | `EXECUTION_LIMITS_CONNECTOR_TIMEOUT` | `--execution-limits-connector-timeout` | `30s` | duration | Maximum connector operation duration |
| Max Result Rows | `execution_limits.max_result_rows` | `EXECUTION_LIMITS_MAX_RESULT_ROWS` | `--execution-limits-max-result-rows` | `1000` | int | Maximum rows or documents returned by one execution |
| Max Response Bytes | `execution_limits.max_response_bytes` | `EXECUTION_LIMITS_MAX_RESPONSE_BYTES` | `--execution-limits-max-response-bytes` | `10485760` | int64 | Maximum normalized execution response payload size in bytes |
| Connector Concurrency Per Data Source | `execution_limits.connector_concurrency_per_data_source` | `EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_PER_DATA_SOURCE` | `--execution-limits-connector-concurrency-per-data-source` | `4` | int | Maximum concurrent connector executions per organization/data source across the broker-backed deployment |
| Connector Concurrency Queue Timeout | `execution_limits.connector_concurrency_queue_timeout` | `EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_QUEUE_TIMEOUT` | `--execution-limits-connector-concurrency-queue-timeout` | `5s` | duration | Maximum time an execution waits for its organization/data-source slot |
| Connector Concurrency Max Queued Per Data Source | `execution_limits.connector_concurrency_max_queued_per_data_source` | `EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_MAX_QUEUED_PER_DATA_SOURCE` | `--execution-limits-connector-concurrency-max-queued-per-data-source` | `16` | int | Maximum queued executions per organization/data source before overload rejection |
| HTTP Timeout | `execution_limits.http_timeout` | `EXECUTION_LIMITS_HTTP_TIMEOUT` | `--execution-limits-http-timeout` | `10s` | duration | Maximum HTTP operation duration, including retry attempts, waits and response reads; a whole-drain deadline can be earlier |
| HTTP Max Redirects | `execution_limits.http_max_redirects` | `EXECUTION_LIMITS_HTTP_MAX_REDIRECTS` | `--execution-limits-http-max-redirects` | `3` | int | Maximum redirects followed by HTTP connector execution |
| HTTP Max Request Bytes | `execution_limits.http_max_request_bytes` | `EXECUTION_LIMITS_HTTP_MAX_REQUEST_BYTES` | `--execution-limits-http-max-request-bytes` | `1048576` | int64 | Maximum outbound HTTP connector request body size in bytes |
| HTTP Max Response Bytes | `execution_limits.http_max_response_bytes` | `EXECUTION_LIMITS_HTTP_MAX_RESPONSE_BYTES` | `--execution-limits-http-max-response-bytes` | `10485760` | int64 | Maximum outbound HTTP connector response body size in bytes |
| HTTP Retry Attempts | `execution_limits.http_retry_attempts` | `EXECUTION_LIMITS_HTTP_RETRY_ATTEMPTS` | `--execution-limits-http-retry-attempts` | `1` | int | Maximum HTTP connector attempts, including the first attempt |
| HTTP Retry Backoff | `execution_limits.http_retry_backoff` | `EXECUTION_LIMITS_HTTP_RETRY_BACKOFF` | `--execution-limits-http-retry-backoff` | `250ms` | duration | Minimum wait between attempts; Retry-After can increase it. Waits above 60s or beyond the operation deadline stop retries |
| Transform Timeout | `execution_limits.transform_timeout` | `EXECUTION_LIMITS_TRANSFORM_TIMEOUT` | `--execution-limits-transform-timeout` | `10s` | duration | Maximum SQL transform execution duration |
| Transform Max Work | `execution_limits.transform_max_work` | `EXECUTION_LIMITS_TRANSFORM_MAX_WORK` | `--execution-limits-transform-max-work` | `1000000` | int64 | Cumulative work: join keys, index builds/lookups and candidate pairs |
| Transform Max Intermediate Rows | `execution_limits.transform_max_intermediate_rows` | `EXECUTION_LIMITS_TRANSFORM_MAX_INTERMEDIATE_ROWS` | `--execution-limits-transform-max-intermediate-rows` | `10000` | int | Maximum cumulative rows materialized during one SQL transform |
| Transform Max Intermediate Bytes | `execution_limits.transform_max_intermediate_bytes` | `EXECUTION_LIMITS_TRANSFORM_MAX_INTERMEDIATE_BYTES` | `--execution-limits-transform-max-intermediate-bytes` | `33554432` | int64 | Maximum cumulative estimated bytes materialized during one SQL transform |
| Transform Max Output Rows | `execution_limits.transform_max_output_rows` | `EXECUTION_LIMITS_TRANSFORM_MAX_OUTPUT_ROWS` | `--execution-limits-transform-max-output-rows` | `1000` | int | Maximum rows returned by one SQL transform |
| Transform Max Output Bytes | `execution_limits.transform_max_output_bytes` | `EXECUTION_LIMITS_TRANSFORM_MAX_OUTPUT_BYTES` | `--execution-limits-transform-max-output-bytes` | `10485760` | int64 | Maximum serialized bytes returned by one SQL transform |
| Transform Max Depth | `execution_limits.transform_max_depth` | `EXECUTION_LIMITS_TRANSFORM_MAX_DEPTH` | `--execution-limits-transform-max-depth` | `16` | int | Maximum nested SQL transform relation and expression depth |

### Native file qualification profile

This profile defaults to disabled and changes no billing state. Enabling it
requires all five fields and an exact organization slug. Configure matching
writers and execution workers, apply core migration 2 and reapply runtime role
grants before restarting them. Executor preflight fails closed when retained
file capabilities are missing. These allowances do not establish qualified
capacity; see [native file transfers](doc/NATIVE_FILE_STREAMING.md).

| TOML under `execution_limits.native_files` | Environment | Flag | Default | Type | Behavior |
|---|---|---|---|---|---|
| `organization` | `NATIVE_FILE_PROFILE_ORGANIZATION` | `--native-file-profile-organization` | empty | string | Only organization eligible for this profile |
| `max_bytes` | `NATIVE_FILE_PROFILE_MAX_BYTES` | `--native-file-profile-max-bytes` | `0` | int64 | Maximum total bytes per native file, positive when enabled |
| `timeout` | `NATIVE_FILE_PROFILE_TIMEOUT` | `--native-file-profile-timeout` | `0s` | duration | Native file deadline measured from pipeline attempt start, or standalone operation start; includes preparation, queues, reads and writes |
| `transfers` | `NATIVE_FILE_PROFILE_TRANSFERS` | `--native-file-profile-transfers` | `0` | int | Maximum pipeline attempts using native files, or standalone file operations, held across preparation, reads and writes; positive |
| `connectors` | `NATIVE_FILE_PROFILE_CONNECTORS` | `--native-file-profile-connectors` | `0` | int | Maximum active connector queries, saved-source connection tests and schema introspections across the organization; positive |

The byte ceiling is 2,251,799,812,636,672 (2^31-1 chunks of 1 MiB), a storage
representation bound, not a supported capacity claim. Providers impose lower
limits. Explicit native file operations in this organization use the profile's
time allowance in place of ordinary query, connector, HTTP and broker wait
caps; saved query timeouts, caller deadlines and provider bounds can shorten
it. Structured queries, other organizations, transforms and normalized metadata
retain their ordinary limits. HTTP reserves a native-file slot conservatively
because its response media type is unknown before dispatch.

Admission is database coordinated across data sources and workers. Existing
per-source FIFO, queue capacity and queue timeout limits still apply. The
transfer budget counts a pipeline attempt once, even when it has several native
file steps; its connector operations still obey the separate connector budget.
A successful source read keeps transfer admission through destination delivery
and attempt completion. Standalone operations release on return. Lost callers
expire at the native-file deadline. Released scopes still count while cancelled
or ambiguous provider work remains within its lease plus ten-second cleanup
grace. Transfer and connector counts have independent positive bounds.
Startup and admission logs record effective settings; persisted request fields
and existing connector admission metrics support readback. See the diagnostics
in [native file transfers](doc/NATIVE_FILE_STREAMING.md#allowances-and-acceptance).

Production tuning:
- Keep `query_timeout` and `connector_timeout` below the shortest upstream reverse-proxy or load-balancer timeout so users receive Braidplane safety errors instead of dropped connections.
- Start with low `connector_concurrency_per_data_source` values for shared production targets, then raise them only after observing database pool usage and provider rate-limit behavior.
- Queues are FIFO within one organization/data source. Independent data sources make progress independently. A full queue is rejected immediately; cancellation or the shorter of the queue and query deadlines removes pending work without consuming a slot.
- Size row and byte limits for the largest result a user can inspect usefully in the UI, not for bulk export. Large exports should use a separate job path.
- Keep `http_retry_attempts = 1` unless the target API is known to tolerate idempotent retries. Read retries cover transient network errors, `429` and `5xx`. Saved HTTP pipeline sinks additionally require an explicit verified-provider `write_retry` policy, durable keyed effect and unexpired retention. See [HTTP retry behavior](doc/data-sources/HTTP.md#opt-in-keyed-write-retries).
- Treat transform limits as one cumulative per-execution budget. A final `LIMIT` does not reduce join candidate work or intermediate row and byte charges.

PostgreSQL and MySQL runtime connectors reuse process-owned pools keyed by
organization, data source, configuration digest, and credential version.
Each database pool allows at most five open and two idle connections, closes
connections idle for five minutes, and replaces connections after 30 minutes.
The process cache retains at most 128 pools, retires entries idle for ten
minutes or alive for one hour, and closes all entries during graceful shutdown.
A configuration or credential version change retires the previous entry after
its active leases finish. Connection tests always open and close a fresh
connector and never enter this runtime cache.

Operational metrics expose aggregate connector admission
(`braidplane_connector_concurrency_current` and
`braidplane_connector_admission_total`) and process-local SQL pool utilization
(`braidplane_sql_connector_pool_entries` and
`braidplane_sql_connector_pool_connections`), wait totals
(`braidplane_sql_connector_pool_wait_count` and
`braidplane_sql_connector_pool_wait_duration_seconds`), and connection
retirement totals (`braidplane_sql_connector_pool_closed_connections`). These
series do not include organization, data-source, credential, or DSN labels. Filter
`braidplane_connector_concurrency_current` by `scope`: `deployment` is the same
database-global snapshot on every executor replica, so dashboards must use
`max` rather than `sum` across replicas; `process` reports direct local
admission and may be summed only to observe process-local work, not to infer a
shared deployment limit. SQL pool series are process-local and may be summed.

### Worker Queue Configuration

Each value controls one River queue in one `worker` process. The `api` command
does not start consumers. All values must be greater than zero.

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Pipeline Git Max Workers | `worker.pipeline_git_max_workers` | `WORKER_PIPELINE_GIT_MAX_WORKERS` | `--worker-pipeline-git-max-workers` | `1` | int | Maximum concurrent pipeline Git jobs per process |
| Email Max Workers | `worker.email_max_workers` | `WORKER_EMAIL_MAX_WORKERS` | `--worker-email-max-workers` | `1` | int | Maximum concurrent email jobs per process |
| Notification Delivery Max Workers | `worker.notification_delivery_max_workers` | `WORKER_NOTIFICATION_DELIVERY_MAX_WORKERS` | `--worker-notification-delivery-max-workers` | `1` | int | Maximum concurrent notification delivery jobs per process |
| Events Max Workers | `worker.events_max_workers` | `WORKER_EVENTS_MAX_WORKERS` | `--worker-events-max-workers` | `1` | int | Maximum concurrent durable event jobs per process |
| Event Maintenance Max Workers | `worker.event_maintenance_max_workers` | `WORKER_EVENT_MAINTENANCE_MAX_WORKERS` | `--worker-event-maintenance-max-workers` | `1` | int | Maximum concurrent event maintenance jobs per process |
| Pipeline Schedule Max Workers | `worker.pipeline_schedule_max_workers` | `WORKER_PIPELINE_SCHEDULE_MAX_WORKERS` | `--worker-pipeline-schedule-max-workers` | `4` | int | Maximum concurrent schedule dispatch and execution jobs per process |
| Pipeline Retry Max Workers | `worker.pipeline_retry_max_workers` | `WORKER_PIPELINE_RETRY_MAX_WORKERS` | `--worker-pipeline-retry-max-workers` | `4` | int | Maximum concurrent pipeline retry jobs per process |
| Pipeline Run Max Workers | `worker.pipeline_run_max_workers` | `WORKER_PIPELINE_RUN_MAX_WORKERS` | `--worker-pipeline-run-max-workers` | `4` | int | Maximum concurrent pipeline run jobs per process |
| Data Source Schema Import Max Workers | `worker.data_source_schema_import_max_workers` | `WORKER_DATA_SOURCE_SCHEMA_IMPORT_MAX_WORKERS` | `--worker-data-source-schema-import-max-workers` | `2` | int | Maximum concurrent data-source schema import jobs per process |
| Execution Max Workers | `worker.execution_max_workers` | `WORKER_EXECUTION_MAX_WORKERS` | `--worker-execution-max-workers` | `4` | int | Maximum concurrent persisted execution jobs per process when `execution.backend=river` |

Adding worker replicas multiplies queue capacity approximately by the replica
count. The sum of queue limits is the process's upper bound before database pool
capacity, per-data-source connector concurrency, provider rate limits, or job
mix impose a lower effective throughput. Tune these values from observed load;
the defaults preserve the existing development limits and are not a production
capacity target.

### Execution Backend Configuration

`api` and `worker` use `river` (encrypted envelopes and opaque request-ID jobs
persisted atomically; bounded queue) or `poller` (envelopes only; isolated
`executor`). Invalid backends fail startup without fallback.

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Backend | `execution.backend` | `EXECUTION_BACKEND` | `--execution-backend` | `river` | string | Shared API/worker backend |
| Completion | `execution.completion_triggers_enabled` | `EXECUTION_COMPLETION_TRIGGERS_ENABLED` | `--execution-completion-triggers-enabled` | `false` | bool | Lifecycle |
| Request Timeout | `execution.request_timeout` | `EXECUTION_REQUEST_TIMEOUT` | `--execution-request-timeout` | `30s` | duration | Wait limit for encrypted results; shorter caller deadlines win |

### Backend Application Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Frontend URL | `frontend_url` | `FRONTEND_URL` | `--frontend-url` | `http://localhost:3002` | string | Canonical browser application URL used for links and authentication flows |
| Default Language | `default_language` | `DEFAULT_LANGUAGE` | `--default-language` | `en` | string | Default language for the application |

### Standalone Frontend Application Configuration

| Variable | TOML | Environment | Flag | Default | Type | Description |
|----------|------|-------------|------|---------|------|-------------|
| Frontend URL | `frontend_url` | `FRONTEND_URL` | `--frontend-url` | `http://localhost:3002` | string | Canonical browser application origin |
| Documentation URL | `documentation_url` | `DOCUMENTATION_URL` | `--documentation-url` | `""` | string | Documentation link; empty hides it. HTTPS is required outside local development. The self-hosted profile accepts private HTTPS origins. |
| Backend API URL | `backend_api_url` | `BACKEND_API_URL` | `--backend-api-url` | `http://localhost:3001` | string | Backend API base URL used by the standalone frontend server |

## Example Configuration File

See the partial local configuration example.

## Notes

- Configuration is applied in order: flags > environment variables > TOML > defaults
- String values in TOML should be quoted
- Duration values use Go syntax: `30s`, `1m`, `2h`
- String array environment variables and flags use comma-separated values. This includes `server.trusted_proxies`, `frontend_server.trusted_proxies`, and `cors.allowed_origins`.
- `hubspot.base_url` and `hubspot.allow_private_hosts` are runtime-only overrides for local development or mock testing. They do not change the persisted HubSpot data source contract.
- `bigquery.base_url`, `bigquery.token_url`, and `bigquery.allow_private_hosts` are backend endpoint overrides for trusted private routing or local verification. They do not change the persisted BigQuery data source contract and must not be supplied through data source API payloads.
- `security.allow_private_data_source_hosts` controls data source payloads. The self-hosted profile uses the pinned PostgreSQL grant below. HubSpot and BigQuery endpoint overrides are operator configuration.
- `server.proxy_header` is rejected unless `server.trusted_proxies` contains at least one valid IP address or CIDR range. The same rule applies to `frontend_server.proxy_header` and `frontend_server.trusted_proxies`.
- Standalone frontend sessions use the configured PostgreSQL session table so proxied API calls and frontend browser flows share session state.

### Pinned private PostgreSQL source

In `self-hosted` deployments, the operator can grant access to one private
PostgreSQL source. Source payloads cannot use that grant for other private hosts:

```toml
[private_postgres]
organization = "customer"
source = "warehouse"
host = "warehouse.customer.internal"
address = "10.20.0.8:5432"
```

The equivalent environment variables are `PRIVATE_POSTGRES_ORGANIZATION`,
`PRIVATE_POSTGRES_SOURCE`, `PRIVATE_POSTGRES_HOST` and
`PRIVATE_POSTGRES_ADDRESS`; flags use the same names with hyphens and lowercase.
All four fields are required together; empty fields disable the grant. The
address must be a private RFC1918 or ULA literal with a nonzero port. Loopback,
link-local/metadata, public addresses and broad network ranges are rejected.

Configure the same grant in the API, worker and executor processes that work
together. The pinned dialer accepts only the configured organization, data-source
slug, PostgreSQL type, hostname and port. DNS answers must match the pinned IP.
An address change blocks connections until the operator updates the grant. Keep
source `allow_private_hosts` false. TLS verification checks the source hostname;
install the customer PostgreSQL CA in the runtime's PostgreSQL trust store.
Each source pool is limited to one connection. Application permissions and
database privileges still apply. This setting supports one destination and the
PostgreSQL connector only.

### Customer help

Set `DOCUMENTATION_URL` to show a link to customer help or documentation. Leave
it empty to hide the link. These links use the host and browser trust stores for
TLS verification.

## Community operation admission

Community runs without an activation key, subscription or vendor network
connection. Core has no trial lifecycle, commercial plan limits or entitlement
table. Protected operations still require authentication, organization permissions
and an existing tenant. Consuming distributions can add their own admission
policies.

The execution-limits table above lists the technical capacity controls operators
can tune: query, connector and transform timeouts; result row and byte limits;
connector concurrency per data source; and queue depth.
`WORKER_PIPELINE_RUN_MAX_WORKERS` and `WORKER_EXECUTION_MAX_WORKERS` limit work per
process. `DB_MAX_CONNS` limits each database pool. Plan for the extra capacity
before raising these limits. Authorization and destination checks still apply.
