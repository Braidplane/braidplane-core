# BigQuery data source

## Purpose and best-fit use cases

Use the BigQuery connector when Braidplane needs warehouse reads or writes against
Google BigQuery datasets. It is intended for analytical ETL flows where schemas
are dataset/table shaped, query execution is job based, results are normalized
to rows, and writes target warehouse tables rather than transactional database
rows.

## Transport security

Provider endpoints require HTTPS and provider clients refuse redirects. Operator plaintext mock endpoints require the explicit [local development policy](../DATA_SOURCES.md#transport-encryption); private-host access alone does not permit plaintext.

## Shared capabilities

BigQuery supports the shared data-source lifecycle, warehouse query execution,
append-style row inserts, internal provider pagination, and pipeline source/sink
roles. See the [capability matrix](../DATA_SOURCES.md#capability-matrix) and
[payload compatibility matrix](../DATA_SOURCES.md#pipeline-payload-compatibility-matrix)
for the canonical cross-connector contract. Public continuation tokens for
partially consumed query results remain follow-up work.

Append-style `insert_rows` uses BigQuery streaming inserts and requires billing
to be enabled for the Google Cloud project. A successful connection test verifies
dataset access; it does not establish permission or billing readiness for writes.
[BigQuery sandbox](https://docs.cloud.google.com/bigquery/docs/sandbox) does not
support streaming data. Braidplane exposes a billing remediation message when
Google returns that restriction.

## Required config fields

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `project_id` | yes | string | none | BigQuery project used for jobs, dataset discovery, and writes |
| `location` | no | string | provider default | Job location, such as `US` or `EU`; set it for regional or multi-regional datasets so jobs and paged result reads stay in the same location |
| `default_dataset` | no | string | none | Optional BigQuery dataset namespace used when an operation omits `dataset` |
| `timeout_seconds` | no | integer | 5 | Request/job timeout in seconds, capped by configured execution limits |
| `maximum_bytes_billed` | no | integer | configured limit | BigQuery-native `maximumBytesBilled` query cost guard, capped by configured execution limits |

`maximum_bytes_billed` uses BigQuery's own billing terminology: query jobs are
charged by bytes processed/scanned, not by response payload size. Response-size
limits remain separate Braidplane execution limits.

Trusted deployments may override the BigQuery REST API and token endpoints
through backend operator configuration for private Google API routing, egress
proxying, or local black-box verification. Public API payloads do not accept
provider base URLs or token URLs.

## Credentials

Choose **Connect with Google** to authorize a deployment-owned OAuth application.
Enter the project ID and dataset explicitly, select read-only or write-enabled
access, and approve consent with the Google account that has access. Braidplane
verifies connectivity after the callback and shows the connected account's email.
Google IAM remains authoritative; authorizing Braidplane does not grant the account
new project or dataset permissions. Application login is a separate authorization.

The deployment configures `bigquery.oauth_client_id`, `bigquery.oauth_client_secret`,
and `bigquery.oauth_redirect_uri` together. The redirect must match the Google
application registration exactly and end in `/provider-connectors/bigquery/callback`.
Hosted deployments require public HTTPS. The existing Google login web client may
be reused: register the additional BigQuery callback, retain the login callback,
and explicitly populate all three BigQuery settings. Login and connector consent
remain separate flows; request BigQuery access only when connecting the source.
Google revocation removes the user's grants across clients within the same Google
Cloud project, so a second client in that project does not isolate revocation.
See [Google token revocation](https://developers.google.com/identity/protocols/oauth2/web-server#tokenrevoke).

The OAuth consent screen must describe
BigQuery access, publish the required privacy policy and authorized domains, and
complete Google's applicable verification requirements before general availability.
External applications in testing are limited to configured test users and their
refresh tokens can expire after seven days; reconnect when Google requires consent.
See [Google's web-server OAuth guide](https://developers.google.com/identity/protocols/oauth2/web-server)
and [OAuth verification requirements](https://developers.google.com/identity/protocols/oauth2/production-readiness/sensitive-scope-verification).

Consent requests `openid`, `email`, and
`https://www.googleapis.com/auth/bigquery`. The BigQuery scope is required for the
connector's query jobs and supported inserts; `bigquery.readonly` alone does not
cover job creation. Braidplane's read-only enforcement still applies. Use Google
IAM to restrict the connected identity to required resources and operations.

Grants use the shared `oauth2_grant` lifecycle: encrypted tokens, write-only
credentials, tenant-scoped resolution, and automatic refresh. **Reconnect Google**
replaces the grant or migrates an existing service-account connection. API and CLI
lifecycle operations also support explicit refresh and revocation. Expired or
revoked refresh tokens require reconnect. Safe metadata includes the provider,
account, granted scopes, status, expiry, and last refresh time, never token values.

The UI uses Google OAuth only; it does not request service-account JSON keys.
Existing API-managed service-account credentials continue through the
`static_secret` lifecycle with encryption at rest and the same read-only checks.
Service-account requests require a PEM-encoded RSA private key in PKCS#8 or
PKCS#1 format. Malformed keys and non-RSA keys fail with invalid credentials
before any token request.
Workload identity federation is not implemented.

Proof resolves only the admitted credential version and never refreshes within
an observation. Refresh or reconnect changes that version, so old evidence cannot
be reused for the new credential; immutable observation history is retained.
BigQuery Proof reads table metadata only and does not prove append permission or
write success. No query job, row read, insert, or pipeline execution is part of
that acceptance check.

## Operation modes and least privilege

BigQuery has no read-only transaction or session setting. For a source with
`read_only: true`, Braidplane's enforced read-only classifier permits supported
GoogleSQL reads and blocks `insert_rows`, DML, DDL, export, procedure, and
multi-statement forms. Configured read-only IAM is the independent
provider-enforced boundary.

Use a Google account or dedicated service account with BigQuery Job User on the execution project
and BigQuery Data Viewer only on the required datasets, tables, or views.
Prefer table/view scope when practical. Dataset-level Data Viewer can permit
routine invocation; do not grant BigQuery Connection User or
`bigquery.connections.use`, and allow callable routines only after explicitly
reviewing them as side-effect free. Do not grant Data Editor, Data Owner, Admin,
write-session, routine-management, or object-storage write permissions to a
source identity.

Use `read_only: false` only with a separate destination identity scoped
to the exact tables and append operations required by `insert_rows`.

## Connection testing

Connection testing uses the connected Google identity or service account to read
the selected dataset metadata. If no default dataset is configured for an advanced
service-account source, it lists datasets in the configured project instead.
Failed draft tests must not persist credentials.
Provider authentication failures map to invalid credentials. Network,
provider-unavailable, timeout, and cancellation failures map to typed data-source
errors without logging credential values.

## Schema introspection

Schema introspection lists datasets, lists tables in each dataset, and fetches
table metadata for fields. BigQuery uses the BigQuery-native GoogleSQL dialect;
Braidplane still maps its metadata into the existing SQL table shape because the
connector is table/row shaped:

- `schema_type: "sql"`
- `type: "bigquery"`
- tables named as `dataset.table`
- columns with BigQuery field name, BigQuery type, and nullable state

In BigQuery terms, a project contains datasets, and a dataset is the namespace
that contains tables. A fully qualified table is usually written as
`project.dataset.table`; Braidplane stores `project_id` on the connector config and
uses `dataset` plus `table` on write operations.

Table metadata must return the requested project, dataset, and table identity
and an explicit supported resource type. Incomplete, mismatched, or unknown
metadata fails introspection; Braidplane does not infer missing identity or
assume that an omitted type is a writable table. The provider client preserves
the resource type separately from column shape. This metadata alone does not
establish insert permission, successful writes, or live Proof acceptance.

The separate, currently disabled
Proof metadata observer
uses exact-table reads with a restricted transport. Service-account observations
request a read-only OAuth scope; connected Google grants retain their authorized
scopes, while the sealed observation permits only the exact metadata read.

BigQuery `REQUIRED` fields are non-nullable. `NULLABLE` and `REPEATED` fields
are represented as nullable in the shared schema shape. Nested `RECORD` fields
remain BigQuery-native column types in the first connector slice; flattening
nested schemas for transform assistance is deferred.

## Query and operation model

BigQuery saved and inline pipeline operations use `operation_type: bigquery`.
The `operation` field inside `BigQueryStatement` is a Braidplane adapter selector,
not a field BigQuery receives directly. Braidplane uses it to choose between the
provider `jobs.query` and `insertAll` requests. Query statements may omit it and
default to `query`; sink writes must set `operation: insert_rows`.
`insert_rows` is BigQuery-specific. The shared Braidplane concept is the `rows`
payload shape; the BigQuery adapter consumes those rows and turns them into a
BigQuery `insertAll` request.

The `query` operation executes BigQuery-native GoogleSQL through BigQuery jobs:

- `type` must be `bigquery`.
- `operation` may be `query`, or omitted to use the default query mode.
- `sql` contains GoogleSQL text with Braidplane `{{param_name}}` placeholders.
- `timeout_seconds` optionally narrows the configured execution timeout.
- `maximum_bytes_billed` optionally narrows the configured BigQuery
  bytes-processed cost guard.

The adapter converts placeholders into BigQuery named parameters such as
`@customer_id`; it does not interpolate values into SQL text. Parameter values
use the shared Braidplane scalar parameter types: `text`, `integer`, `boolean`,
`date`, and `uuid`. Identifier substitution for project, dataset, table, or
column names is not supported.

Query execution returns the existing SQL execution result envelope with vendor
type `bigquery`. Result rows are normalized as follows:

| BigQuery type | Braidplane value |
|---|---|
| `STRING`, `DATE`, `DATETIME`, `TIME`, `TIMESTAMP`, `GEOGRAPHY` | string |
| `INT64` | integer |
| `FLOAT64` | number |
| `BOOL` | boolean |
| `NUMERIC`, `BIGNUMERIC` | string to preserve precision |
| `JSON` | JSON object, array, scalar, or null when valid JSON; otherwise string |
| `BYTES` | base64 string |
| `RECORD`, repeated fields | JSON-compatible object or array |

BigQuery's native `JSON` column type is supported as a value inside normalized
rows. The pipeline boundary still emits and consumes `rows` for BigQuery source
and sink steps; a full JSON payload is accepted by a BigQuery sink only when the
shared sink runtime can normalize it into one or more row objects before
invoking `insert_rows`.

The connector reads paginated query results through the provider API until the
job is exhausted or a Braidplane runtime limit stops execution. It does not expose
provider page tokens or resumable partial query reads in the first connector
slice; that public continuation contract remains future work.

The `insert_rows` operation writes row payloads to a configured table:

- `type` must be `bigquery`.
- `operation` must be `insert_rows`.
- `dataset` identifies the target dataset, or falls back to `default_dataset`.
- `table` identifies the target table.
- `rows` must resolve to Braidplane row objects, usually the standardized pipeline
  `input` rows.
- `insert_id_prefix` optionally prefixes BigQuery `insertId` values for
  insertAll. BigQuery uses `insertId` as a best-effort duplicate suppression key
  for streaming inserts; omitted values send rows without insert IDs.

The first connector slice supports append-style row inserts. Destructive writes,
table creation, schema evolution, load jobs, and merge/upsert helpers are
outside the first BigQuery connector.

## Runtime limits

BigQuery operations inherit `[execution_limits]` timeouts, row caps, response
size caps, request size caps, and connector concurrency limits. The BigQuery
adapter also enforces the effective `maximum_bytes_billed` value before starting
query jobs. The effective timeout, row limit, response-size limit, and
bytes-billed limit are the lower of operation-local values and server
configuration. Internal query-result pagination remains bounded by these same
limits.

The response-size cap applies to all query-result pages combined, including
repeated schema and page envelopes. A separate check bounds retained raw row
values before normalization, and the normalized result must also fit the cap.
Queries can exceed either earlier budget even when their final output would fit.
The client stops fetching once it has enough rows to report a row-limit error.

The bytes-billed limit is provider-side BigQuery cost control. It does not
replace Braidplane's response-size or row-count limits.

When a limit is exceeded, Braidplane returns the shared typed execution errors such
as `EXECUTION_ROW_LIMIT_EXCEEDED`, `EXECUTION_RESPONSE_SIZE_EXCEEDED`, or a
BigQuery query-limit error mapped to the data-source execution failure context.
Raw query parameter values, service-account JSON, access tokens, provider
headers, and provider response bodies are not stored in run metadata.

## Pipeline relevance

**As a source**: the pipeline step uses `operation_type: bigquery` with a saved
or inline `query` operation. Source output is normalized to `rows`.

BigQuery source steps support incremental `watermark` contracts. Operations can
reference `{{runtime.incremental.lower_bound.value}}` and
`{{runtime.incremental.upper_bound.value}}` as normal query parameters. Braidplane
observes the next checkpoint from the configured returned field.

**As a sink**: the pipeline step uses `operation_type: bigquery` with a saved
BigQuery-specific `insert_rows` operation. Rows from upstream query or transform
steps are native Braidplane row payloads consumed through the operation `rows` field.
JSON and CSV payloads can be consumed only when the shared sink runtime can
normalize them into rows before invoking the saved BigQuery operation. File and
multipart form payloads are not supported.

## Testing approach

Automated local and CI coverage must not call live BigQuery. We are not using a
third-party BigQuery emulator for the first connector slice. BigQuery connector
tests use a Braidplane-owned fake BigQuery-compatible HTTP provider boundary that
mimics the provider surface the connector owns:

- service-account token exchange;
- dataset and table metadata APIs;
- job query execution and query-result pagination;
- table row insertion;
- provider invalid credentials, permission, quota, timeout, cancellation, and
  unavailable responses.

The fake boundary is part of Braidplane's connector test fixture ownership, not a
third-party service or a full GoogleSQL execution engine. Manual live-provider
validation can be performed by an operator after implementation, but it is not a
required local or CI gate.

## Implementation boundary

The reusable connector ownership rule is documented in
[DATA_SOURCES.md - Connector implementation boundary](../DATA_SOURCES.md#connector-implementation-boundary).

The BigQuery implementation is split into two layers:

- `service/datasourcessvc/connector/bigquery/client` owns provider-facing
  protocol and provider-shaped primitives: service-account token exchange,
  auth headers, BigQuery REST requests, response decoding, provider pagination
  primitives where needed, and provider error mapping.
- `service/datasourcessvc/connector/bigquery` is the Braidplane adapter layer. It
  owns connection testing, schema mapping, `BigQueryStatement` decoding,
  `{{param_name}}` to named-parameter substitution, runtime-limit coordination,
  result normalization, and source/sink enforcement.

Provider clients must not import persistence repositories, HTTP transport,
frontend rendering, generated API DTOs, query editor code, credential storage,
or pipeline runtime packages.

### Shared Google client policies

`internal/googleclient` is the neutral foundation consumed by Workspace and
BigQuery execution. It owns explicit HTTP client construction, redirect rejection,
the common default response budget, bounded response reads,
and status/transport error mapping that excludes provider bodies, headers and
request URLs. It neither discovers credentials nor retries operations. Private
hosts remain an explicit BigQuery runtime opt-in for fake providers; Workspace
uses fixed Google endpoints and public hosts. Restricted Proof clients are
supplied by the observation boundary and are never replaced by this factory.

Resource policy stays with the resource: BigQuery owns transport cloning and
public-host validation at construction and dial time. Workspace owns its
operation deadline,
schema-quota coordination, request cap, bounded read attempts and Retry-After
waits. Sheets RAW updates and Drive uploads each have one attempt and preserve
ambiguous outcomes. BigQuery owns its connection timeout, service-account JWT
scope/token cache, job/cost/row limits, pagination and single-attempt requests.
Both receive only runtime credentials; static Workspace tokens never refresh in
an SDK, and resolved BigQuery OAuth tokens bypass service-account exchange.
Consent/refresh/revocation remain in `internal/googleoauth` and credential
adapters. Neutral `internal/oauthflow` helpers are shared with OIDC;
provider discovery, ID-token verification, profile mapping and sessions remain
in the provider-agnostic authentication service. Login does not consume a
Google-resource transport or credential adapter.

Workspace uses `google.golang.org/api/drive/v3` and `sheets/v4` instead of maintaining
REST endpoint construction and wire types in Braidplane. These [officially supported
clients](https://github.com/googleapis/google-api-go-client#status) are in maintenance
mode, with critical bug and security fixes maintained by Google. The backend's
`go.mod` owns the pinned SDK version and its required shared dependency upgrades;
updates must pass the connector tests and normal backend gates. The SDK receives
an explicit HTTP client with the resolved runtime token, with no ambient credential
discovery or SDK body logging. Braidplane retains byte limits, deadline and retry
policy, safe error mapping, and the prohibition on automatic update retries.

### SDK ownership and BigQuery client decision

BigQuery uses the [official generated REST SDK](https://pkg.go.dev/google.golang.org/api/bigquery/v2)
for dataset/table operations, jobs.query, result pagination and insertAll. The
SDK owns endpoint construction, request/response types and wire serialization;
Braidplane retains resource normalization and the bounded transport. Its default
JSON cell decoding uses float64, so bounded responses are decoded into the same
SDK types with `UseNumber` to preserve exact decimals. Schema normalization also
preserves absent versus explicit zero precision/scale for observations. SDK
formatting query parameters are removed before dispatch to preserve Proof's exact
URL allowlist. The SDK logger is discarded so rows and credentials cannot enter
provider debug logs.

The [high-level client](https://pkg.go.dev/cloud.google.com/go/bigquery) is not
needed for the supported REST operations; its job/iterator and insert retry
behavior would require additional adaptation. Service-account assertions and
token exchange use the pinned OAuth library's JWT client through the restricted,
cancellable transport. Token responses have the OAuth library's 1 MiB bound,
with oversized responses rejected before its truncating read. Resolved access
tokens still bypass exchange and SDK credential discovery. Read and write
operations dispatch once; Workspace owns its bounded read retries separately.
No dependency is added for this refactor. The backend `go.mod` remains the sole
runtime Google SDK and OAuth version owner. `tools/go.mod` owns tool-only
transitive versions independently; `cli/` carries neutral contracts without a
Google resource SDK dependency. Upgrades validate both resource clients and
credential lifecycle tests. BigQuery retains query-specific invalid-input errors while
sharing authentication, permission and availability mapping; provider response
text is never used as an error detail.

## Known limitations

- Google OAuth requires deployment-owned application configuration and provider consent.
  Domain-wide delegation and workload identity federation are not implemented.
- Hosted OAuth Proof requires separate acceptance; local provider tests do not establish deployed acceptance.
- Query result pagination is provider-internal only. Public continuation tokens
  and resumable partial query reads remain future work.
- Nested `RECORD` schema flattening is not implemented.
- Destructive writes, merge/upsert helpers, load jobs, exports, and table
  creation are not supported.
- The BigQuery Storage API is not used; the connector uses the REST APIs covered
  by the fake provider boundary.
- Live BigQuery tests are not automated in local or CI validation.

See [connector support levels](../../docs-public/connectors/support-levels.md) for shipped connector scope, limitations and acceptance boundaries.

## Decimal metadata and pipeline conversion

NUMERIC/BIGNUMERIC values remain exact text. Introspection retains declared
precision/scale; ordinary NUMERIC supplies its native 38-digit, scale-9 default.
NUMERIC and explicitly parameterized BIGNUMERIC declarations within precision 38
map to decimal contracts. BIGNUMERIC's wider native
range is not a bounded 38-digit promise: explicitly cast/validate the selected
values into a bounded decimal field. FLOAT64 remains binary. Native target
precision, scale and rounding rules still apply when loading rows.
See decimal compatibility.
