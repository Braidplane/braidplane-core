# Data Sources

The shipping catalog below is not exhaustive. Add connectors for customer workflows
through the shared credential, introspection, operation, payload, source/sink and
runtime-safety contracts. Per-type pages own protocol details.

## Shipped types

See [support levels](../docs-public/connectors/support-levels.md) for classifications
and [evidence maintenance](CONNECTOR_SUPPORT_EVIDENCE.md) for contributor guidance.

| Type | Protocol | Use cases |
|---|---|---|
| [PostgreSQL](data-sources/POSTGRES.md) | SQL over TCP | Relational databases, transactional reads and writes |
| [MySQL](data-sources/MYSQL.md) | SQL over TCP | Relational databases, transactional reads and writes |
| [Google Workspace](data-sources/GOOGLE_WORKSPACE.md) | Native Google Sheets, Drive, Docs and Slides | Selected-file grants, Google Picker, spreadsheet/file operations, bounded authorized-file discovery and metadata-only Proof; Sheets/Drive Alpha acceptance complete; native Docs/Slides hosted acceptance pending |
| [BigQuery](data-sources/BIGQUERY.md) | BigQuery REST APIs over HTTPS | Analytical warehouse reads, row-normalized query results, and append-style table writes |
| [Snowflake](data-sources/SNOWFLAKE.md) | Snowflake SQL protocol over HTTPS | Parameterized warehouse reads and explicitly authored append-style SQL writes |
| [MongoDB](data-sources/MONGODB.md) | MongoDB wire protocol | Document stores, flexible schemas |
| [HTTP](data-sources/HTTP.md) | HTTP/HTTPS | REST APIs, webhooks, JSON/CSV endpoints, and opaque file responses |
| [HubSpot](data-sources/HUBSPOT.md) | HubSpot CRM APIs over HTTPS | SaaS CRM reads and key-based upserts for contacts, companies, deals, and discovered custom objects |
| [FTP](data-sources/FTP.md) | FTP over TCP | File-based data, CSV/JSON/Excel workbook ingestion/export, and opaque file pass-through |
| [Git](data-sources/GIT.md) | Git over SSH/HTTPS | Versioned file data, CSV/JSON/Excel workbook reads, raw file reads, and file writes in repositories |
| [GitHub](data-sources/GIT.md) | Git over SSH/HTTPS | GitHub-hosted repository data with the shared Git runtime connector |
| [GitLab](data-sources/GIT.md) | Git over SSH/HTTPS | GitLab-hosted repository data with the shared Git runtime connector |
| [S3](data-sources/S3.md) | S3-compatible object-store APIs over HTTP/HTTPS | Durable object routing, CSV/JSON/Excel workbook ingestion/export, and opaque file/blob pass-through |

Managed GitHub and GitLab sources select a verified repository using the
organization's shared GitHub App or GitLab OAuth connection. Generic Git owns
manual HTTPS/SSH credentials and customer-supplied repository URLs. Existing
manual provider sources migrate in place to Generic Git without changing source
identity, references, credentials, or read-only restrictions. Proof is optional
and configured on the data source. See [managed provider connections](data-sources/GIT.md#managed-provider-connections)
and Proof configuration.

## Transport encryption

See the [policy](data-sources/TRANSPORT_SECURITY.md).

## Private host access

PostgreSQL, MySQL, MongoDB, HTTP, FTP, Git/GitHub/GitLab, and S3 retain
`allow_private_hosts` for explicitly enabled local fixtures. Hosted/default
Braidplane rejects `true` on create, update and pre-save connection tests unless
distribution policy grants it. The `self-hosted` profile rejects the broad
switch entirely. Its supported private route is an
[operator-pinned PostgreSQL source](../CONFIG.md#pinned-private-postgresql-source)
bound to an exact organization, source, hostname and IP:port. Keep the source
payload flag false. Other private targets remain denied; broader connector
certificate/connectivity support is separately qualified. A private-origin UI
setting does not authorize a private data source.

FTP control and passive data channels, HTTP-family requests (including OpenAPI
discovery), custom S3 endpoints, and native Git transports resolve their
destination through the shared destination policy and dial a vetted literal IP
rather than re-resolving the configured hostname later. The protocol still
retains the configured hostname for TLS SNI, certificate validation, HTTP Host,
request signing, and SSH known-host identity. Native Git HTTPS redirects are
rejected rather than followed because its redirect locations cannot be exposed
for per-hop validation; Git URL rewrite configuration is isolated for the same
reason. Deployment egress allowlists remain defense in depth, not a replacement
for this per-dial policy.

Operator-only provider runtime overrides, such as HubSpot or BigQuery endpoint
overrides, are separate deployment configuration and are not accepted through
customer data source payloads.

## Schema cache and refresh

Data-source detail pages, concrete-source reads and `GET /schema` are cache-only:
no provider calls, repository clones, object listings, file sampling or OpenAPI
fetches. Detail responses separate schema state from connection metadata:
`not_imported` before any import, `ready` for a populated cache, and non-blocking
failure diagnostics for refresh/import errors. Without a cache, `GET /schema`
returns the connector-appropriate empty shape.

Explicit refresh (`POST /schema` or `braidplane data-source schema refresh`)
persists `importing`, enqueues a River `data_source_schema_import` job and returns
the persisted state. Introspection runs in the worker, not the request path.
Duplicate refreshes while `importing` return the in-flight state without another
job.

The worker updates cached `schema_metadata` and marks `ready` on success, or
`failed` with a safe diagnostic code/message. Operator diagnostics exclude credentials,
tokens, other secrets and raw provider payloads. Updates that invalidate an
in-flight import prevent stale attempts from rewriting its cache or diagnostics.

Every attempt reports `succeeded`, `failed_persisted`, `worker_failed` or
`skipped`. Persisted provider failures are warning-level and are not retried;
telemetry includes only safe slugs, attempt ID and the persisted diagnostic
code/message, never an unqualified completion.

Sink-only inspection and configuration require no introspection. Pipeline
validation and runs also use cached schemas only, without live introspection.

### PostgreSQL run query diagnostics

PostgreSQL runtime failures retain bounded reasons through worker execution,
run timelines, CLI output and run details. `source_schema_reference_invalid`
identifies missing columns, relations or schemas; `source_query_invalid` identifies
supported syntax, ambiguity, grouping, type and function-resolution failures.
Hints direct the user to query/schema references or query expressions. Permission,
authentication, connection and timeout failures keep their distinct reasons.
Unrecognized failures retain `source_query_failed`; their cause is not inferred.
Public diagnostics exclude SQL, object names, driver detail and connection secrets.
These runtime classifications do not validate arbitrary SQL before execution.

### Connection-test diagnostics

Connection checks preserve safe provider guidance across the execution worker,
API, stored connection status, and UI. Otherwise, typed failures distinguish
rejected credentials, denied permissions, missing resources, invalid settings,
timeouts, DNS, network reachability and TLS errors. Unknown failures remain
explicitly unclassified; raw provider messages, response bodies and credentials
are never returned as diagnostic detail.

### Schema diagnostics

Schema diagnostics use the same stable application error-code namespace as API
error responses. Import failures persist diagnostics. Local cached-schema
rendering failures can also attach a response diagnostic without changing the
persisted import state. The message is sanitized for operator display and can be
more specific than the code when the connector provides safe public detail.

Current schema diagnostic codes:

- `DATA_SOURCE_SCHEMA_IMPORT_FAILED`: fallback for an uncategorized import
  failure.
- `DATA_SOURCE_CONNECTION_FAILED`: connector setup, credential resolution, or
  provider connection failed before schema metadata could be imported.
- `DATA_SOURCE_SCHEMA_INTROSPECTION_UNSUPPORTED`: the connector does not support
  schema introspection for the requested data source.
- `INVALID_SCHEMA_URL`: HTTP OpenAPI import URL failed safety validation.
- `SCHEMA_FETCH_FAILED`: HTTP OpenAPI document could not be fetched.
- `INVALID_OPENAPI`: HTTP OpenAPI document could not be parsed.
- `SCHEMA_RESPONSE_MAPPING_FAILED`: cached schema metadata could not be rendered
  into the API response shape.

## Connector expansion strategy

Expand for customer workflows, prioritizing foundations over connector count.
Requested providers are product work, not a reason to narrow positioning. Reuse
shared authentication, introspection, payload, runtime-limit and testing contracts
without a separate orchestration model or repeated cross-layer design.

BigQuery and S3 are the first warehouse and object-store slices. Expand in order:

1. Foundations: HTTP OpenAPI draft coverage, OAuth2/provider credential lifecycle,
   shared pagination/rate limits, webhook/event intake and source/sink payload compatibility.
2. BigQuery: add read/write helpers, auth modes and diagnostics as workflows need
   them. Local and CI tests use a Braidplane-owned fake provider, not live BigQuery.
3. S3: non-static auth, retention policy and durable artifact routing, with
   deterministic fake/local S3-compatible coverage such as MinIO.
4. SaaS APIs, after shared OAuth2 lifecycle and pagination/rate limits are ready.
5. Provider-native Git/webhooks, after explicit event intake and verification.
   Tests use Braidplane-owned API/webhook fixtures, not live GitHub/GitLab or SaaS calls.
6. Other file stores, after S3 proves payload, retention and safety behavior.

## New connector readiness gate

A connector is not ready to be marked supported until each gate below has an
explicit answer in implementation, documentation, tests, or accepted known
limitations.

Promotion to supported also requires dated provider evidence for the claimed
operations and auth mode. Review each applicable gate:

- Connection testing works for create, update, credential update, and pre-save
  test paths, durably records safe outcomes for persisted sources, and never
  exposes credential values.
- Introspection maps provider metadata into stable Braidplane schema shapes, and
  schema refresh records non-blocking failures consistently.
- Query execution uses parameterized operations only; user input is never
  interpolated directly into protocol requests, SQL, paths, or file names.
- Pipeline source and sink roles are declared in `pkg/connector/catalog`,
  reflected in this capability matrix, and enforced for read-only connectors.
- Sink replay mechanisms are declared in the catalog. Every saved sink
  operation must declare `naturally_idempotent`, `idempotent_with_key`,
  `deduplicated_by_ledger`, or `unsafe`, with provider or operation evidence
  for every class except `unsafe`.
- Package ownership is granular: provider client code, Braidplane adapter behavior,
  capability metadata, and fake/local test fixtures have clear package
  boundaries instead of one cross-layer connector package.
- Credentials are encrypted at rest, write-only in APIs, omitted from logs and
  diagnostics, and covered by response/redaction tests.
- Runtime limits cover timeouts, row/result size, request/response size,
  concurrency, cancellation, redirects, and connector-specific retry behavior
  where applicable.
- Pagination, rate-limit handling, and retry behavior are either implemented
  through a documented connector policy or called out as unsupported.
- Payload compatibility is documented in the pipeline payload compatibility
  matrix for `rows`, `json`, `csv`, `file`, and `multipart_form` as applicable.
  Connector-native spreadsheet modes such as worksheet/range selection must be
  documented on the per-type page even when the pipeline boundary normalizes
  them to `rows` or `file`.
- Deterministic fake-provider or local-container tests cover success, invalid
  credentials, unreachable dependency or provider failure, timeout or
  cancellation, introspection mapping, parameter substitution, result
  normalization, runtime limits, and write-only credential responses.
  A connector without this coverage remains experimental or unsupported, even
  if the happy path works manually.
- The per-type page under `doc/data-sources/`, this capability matrix,
  known-limitations entries, API schema/generated code, and package-boundary
  docs are updated whenever the public contract, connector metadata, or import
  graph changes.

Do not mark a connector ready when its auth lifecycle, pagination or rate-limit
behavior, payload representation, or fake/local test strategy is unresolved.

## Connector-facing behavior wiring

`pkg/connector/catalog` owns schema family, query operation type, sink backend,
source/sink support, sink replay strategies, incremental checkpoint kinds and
forced read-only behavior. Frontend and builder code use the layer-safe
`pkg/pipeline/spec.AllConnectorCapabilities()` projection, not direct
connector imports while both ship in one Go module. Independently versioned or
separate frontend/backend repositories should use OpenAPI or generated API
metadata for these descriptors.

Public-shape changes require:

- OpenAPI schemas and oneOf variants for create, update, credential update,
  connection test, schema, query execution, and result responses.
- Generated API clients and server types after OpenAPI changes.
- HTTP transport, SSR form handlers, and CLI request builders that construct or
  parse those public variants.
- Per-type documentation, known-limitations updates, and fake/local validation
  coverage for the connector contract.

Update catalog descriptors first; UI/builder switches must not duplicate them.
The builder uses SSR-rendered descriptor JSON for source/sink eligibility,
read-only handling, query operation type, sink backend, schema-family payload
inference and incremental checkpoints. Separate wiring is needed only for new
public DTO shapes or connector-specific form fields.

Catalog, registry, pipeline-spec projection, frontend JSON serialization and
builder JavaScript tests guard against missing descriptors or builder metadata
that differs from the supported connector set.


## Capability matrix

| Capability | PostgreSQL | MySQL | BigQuery | MongoDB | HTTP | HubSpot | FTP | Git | S3 | Google Workspace |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:| :---: |
| Test connection | yes | yes | yes | yes | yes | yes | yes | yes | yes | yes |
| Schema introspection | yes | yes | yes | yes | yes | yes | yes | yes | yes | yes |
| Schema refresh | yes | yes | yes | yes | yes | yes | yes | yes | yes | yes |
| Query execution | yes | yes | yes | yes | yes | yes | yes | yes | yes | yes |
| Parameterized queries | yes | yes | yes | yes | yes | yes | yes | yes | yes | exact update `{{input}}` binding |
| `read_only: true` enforcement | classifier + native transaction | classifier + native transaction | classifier | adapter classifier | method classifier | operation classifier | operation classifier | operation classifier | operation classifier | read/list/export versus create/update classifier |
| Required source credential boundary | configured read-only | configured read-only | configured IAM | configured read-only | configured read-only or unauthenticated | configured scopes | configured read-only | configured read-only or public read | configured read-only | managed OAuth; Braidplane read-only enforcement |
| Pipeline source | yes | yes | yes | yes | yes | yes | yes | yes | yes | yes |
| Pipeline sink | yes | yes | yes | yes | yes | yes (`idempotent_with_key`) | yes | yes | yes | yes |
| Pagination | no | no | yes | no | [GET cursor/page/next-link](data-sources/HTTP.md#next-link-pagination) | yes | no | no | provider listing only | Drive discovery only |
| Incremental source checkpoints | watermark | watermark | watermark | watermark | timestamp watermark, cursor | cursor | no | no | no | no |

Incremental source checkpoint support is static connector capability metadata
used by pipeline spec validation. Validation does not call live connectors.
`watermark` means the operation can accept a previous comparable source field
value, optionally apply a positive duration `lookback_window`, and report the
next observed value. `cursor` means the operation can accept and report an
opaque connector-native cursor token. File-backed FTP and Git source steps are
full-read only in the first incremental slice.

During incremental source execution, Braidplane injects reserved runtime parameters
for both saved and inline operations:

- `runtime.incremental.lower_bound`: full decoded lower-bound checkpoint object
- `runtime.incremental.lower_bound.value`: comparable watermark lower bound
- `runtime.incremental.lower_bound.cursor`: opaque cursor lower bound
- `runtime.incremental.upper_bound`: full decoded upper-bound checkpoint object
- `runtime.incremental.upper_bound.value`: comparable watermark upper bound
- `runtime.incremental.upper_bound.cursor`: opaque cursor upper bound

PostgreSQL, MySQL, BigQuery, and MongoDB operations use the `value` fields for
watermark filters and must return the configured watermark field so Braidplane can
observe the maximum returned value. HTTP uses `value` for timestamps and `cursor` for opaque tokens; see
[HTTP](data-sources/HTTP.md#timestamp-watermarks).
HubSpot operations bind the cursor to `after` and observe the shared object-row
`next_cursor` result field. S3
source steps are full-object reads and do not support incremental checkpoints in
the first object-store slice. Opaque
cursor values are execution state, not display data; run diagnostics expose only
safe summaries. The connector contract verifies parameter binding and
observation mechanics; it does not prove that a user-authored predicate, filter,
or request applies the declared watermark or cursor to the intended remote
dataset.

## Pipeline payload compatibility matrix

This matrix tracks pipeline runtime payload representations, not scalar query parameter types such as `text`, `integer`, or `pkg/uuid`.

Legend:
- `native` — the connector/runtime can consume or emit that payload shape directly.
- `serialized` — the connector converts the runtime payload into the target file or request format.
- `binding fallback` — saved sink operations consume the standardized contextual `input` binding explicitly.
- `row-normalized` — the remote system may return JSON or CSV externally, but the pipeline source result entering the graph is normalized to rows.

| Data source | Source output into pipeline | Rows sink input | JSON sink input | CSV sink input | File sink input | Multipart form sink input |
|---|---|---|---|---|---|---|
| [PostgreSQL](data-sources/POSTGRES.md) | `rows` (`native`) | `native` via SQL `input` relation | `binding fallback` via `input` | `binding fallback` via `input` | `binding fallback` via `input` | `not supported` |
| [MySQL](data-sources/MYSQL.md) | `rows` (`native`) | `native` via SQL `input` relation | `binding fallback` via `input` | `binding fallback` via `input` | `binding fallback` via `input` | `not supported` |
| [Google Workspace](data-sources/GOOGLE_WORKSPACE.md) | `rows` from spreadsheet/native reads, selected file format, or PDF `file` from native export | exact-range spreadsheet `update` via `rows: "{{input}}"`; native text replacement consumes encoded string `content: "{{input}}"` | file create/update with `format: json` | file create/update with `format: csv` | file create/update with `format: file` | not supported |
| [BigQuery](data-sources/BIGQUERY.md) | `rows` (`native`) | `native` via `insert_rows` `rows: "{{input}}"` | `row-normalized fallback` via `input` when object/array payloads can become rows | `row-normalized fallback` via `input` when CSV can become rows | `not supported` | `not supported` |
| [MongoDB](data-sources/MONGODB.md) | `json` (`native` document array) | `binding fallback` via `input` | `binding fallback` via `input` | `binding fallback` via `input` | `binding fallback` via `input` | `not supported` |
| [HTTP](data-sources/HTTP.md) | `json`, `csv`, `file`, or `multipart_form` by response media type | `native` body via `body: "{{input}}"` | `native` body via `body: "{{input}}"` | `native` raw body via `body: "{{input}}"` | `native` raw body via `body: "{{input}}"` | `native` generated form body via `body: "{{input}}"` |
| [HubSpot](data-sources/HUBSPOT.md) | `rows` (`row-normalized` from HubSpot CRM objects) | `native` via upsert `rows: "{{input}}"` | `row-normalized fallback` via `input` when object/array payloads can become rows | `row-normalized fallback` via `input` when CSV can become rows | `not supported` | `not supported` |
| [FTP](data-sources/FTP.md) | `rows` for CSV/JSON/Excel workbooks, `file` for `format: file` | `serialized` to the configured file format | `serialized` to JSON file output | `serialized` to CSV file output | `native` raw write with `format: file` | `not supported` |
| [Git](data-sources/GIT.md) | `rows` for CSV/JSON/Excel workbooks, `file` for `format: file` | `serialized` to the configured file format | `serialized` to JSON file output | `serialized` to CSV file output | `native` raw write with `format: file` | `not supported` |
| [S3](data-sources/S3.md) | `rows` for CSV/JSON/Excel workbooks, `file` for `format: file` | `serialized` to the configured object format | `serialized` to JSON object output | `serialized` to CSV object output | `native` raw write with `format: file` | `not supported` |

Notes:
- File payloads expose safe metadata (`filename`, `mime_type`, `size_bytes`, `digest_algorithm`, `digest`) and keep raw bytes out of persisted run metadata, API responses, and diagnostics.
- File/object sink run detail surfaces may show the resolved output path or object key captured by FTP, Git, and S3 write execution results. This location is execution metadata only; raw object or file bodies, credentials, access keys, API keys, and database passwords remain excluded from run metadata and SSR diagnostics.
- Sink snapshots also expose only the declared replay strategy, selected method,
  decision, and durable effect state. Runtime idempotency keys, payloads, and
  credentials remain write-only execution material.
- HTTP file source output is selected for non-JSON and non-CSV response media types such as `application/octet-stream`, `application/pdf`, and image/video types.
- Source connectors usually normalize pipeline source output to rows, but HTTP preserves JSON, CSV, file, and form payload representations when the response `Content-Type` is explicit.
- BigQuery source output always enters the pipeline graph as rows. BigQuery
  sinks consume rows natively through `insert_rows`; JSON and CSV inputs require
  the shared sink runtime to normalize the upstream payload to rows before the
  saved operation runs. BigQuery's native `JSON` type is supported as a value
  inside a row, not as a separate pipeline payload representation.
- HubSpot sinks consume row-normalized input through a saved `upsert` operation
  and require `idempotent_with_key`. File and multipart payloads are unsupported.
- HTTP sink native-body support currently covers `body: "{{input}}"` for JSON arrays/documents, raw CSV bytes, raw file bytes, and `multipart_form` payloads. `multipart_form` is most important for future HTTP push/webhook trigger ingress and upload-style sink calls; HTTP multipart responses are supported for completeness but are uncommon. Form payloads carry named scalar fields, repeated fields, and file parts with content type, size, filename, and digest metadata; runtime field values and file bytes are not serialized into stored run metadata. Saved operation headers can override the default `Content-Type` for JSON, CSV, and file native-body paths. Form sinks generate a boundary-bearing `multipart/form-data` header when no form content type is provided, and may use `application/x-www-form-urlencoded` only for scalar field-only forms. Multipart requests always replace the caller header with the generated boundary-bearing value.
- Mongo sink binding fallback via `input` now covers exact whole-field placeholders not just for `documents` and `document`, but also for `filter`, `update`, and `pkg/pipeline` when the target field expects a native object or array.
- FTP, Git, and S3 spreadsheet support is a file-backed connector mode, not a new pipeline contract representation. Spreadsheet sources select one worksheet and optional A1 range, normalize the resulting table to `rows`, and spreadsheet sinks serialize row objects into a workbook starting at the configured sheet/range.
- Spreadsheet reads apply the byte budget to decompression, parsing work and row output. The checks include the whole worksheet; sparse coordinates, repeated shared strings or custom formats can exceed the budget even when the selected range is small. Schema inference applies its sample budget and leaves fields uninferred when parsing exceeds it.
- Spreadsheet mode exists separately from CSV because CSV has one flat table and no native worksheet or cell-range address space. The pipeline payload still becomes `rows`; the workbook-specific controls stay at the connector operation boundary.
- FTP, Git, and S3 sinks do not implicitly convert CSV `input` payloads into JSON or spreadsheet file output; JSON and spreadsheet targets expect rows or structured JSON payloads, so CSV-to-JSON or CSV-to-spreadsheet requires an explicit transform. File payload targets must use `format: file` to preserve bytes without CSV/JSON/spreadsheet parsing.
- SQL transforms accept row-normalized inputs only. The current SQL transform
  representation matrix is documented in
  Pipeline Data Model.
  Broader native JSON/CSV transform runtime behavior remains deferred;
  see KNOWN_LIMITATIONS.md.

## Shared behaviors

### Connector implementation boundary

Live Proof metadata uses a separate
grant-bound selected-resource observer.
Ordinary connection testing and broad schema introspection do not establish
Proof authorization or live evidence. The observer is not enabled for runtime
dispatch while the default-off `proof_observations.enabled` control and
explicit API-only preview/policy controls remain disabled.

The Proof metadata contract
defines typed targets, supported operations, evidence boundaries, restricted I/O
and SQL resource-binding restrictions.

Connector code should follow the same ownership discipline as pipeline code:
contracts should be easy to find, boundaries should be enforced where possible,
and new connector PRs should not need to touch unrelated layers to add one
provider.

The reusable connector contract lives at the Braidplane adapter boundary:
`service/datasourcessvc/connector` exposes connection testing, schema
introspection, query execution, pipeline source behavior, and pipeline sink
behavior through Braidplane-owned interfaces and result shapes.
`pkg/connector/catalog` is the discoverable domain catalog for connector
families, query operation types, pipeline source/sink support, sink backend
routing, and forced read-only behavior. `doc/PACKAGE_BOUNDARIES.md` defines the
Go import graph, and `.golangci.yml` depguard rules enforce that connector
packages do not import persistence repositories, HTTP transport, frontend
rendering, or generated API DTOs.

Connector construction is kept in an explicit service-layer registry under
`service/datasourcessvc/connector`. That registry owns construction-time
details: config JSON decoding, credential JSON decoding, connector constructors,
and any connector-local preparation such as merging credentials into SQL config
or applying construction-time defaults. It complements, but does not replace,
`pkg/connector/catalog`; catalog descriptors remain the source of truth for
capability and pipeline-routing facts. New connector types should update both
the catalog descriptor and the construction registry in the same reviewed slice.

Relational SQL connectors share only the adapter mechanics that are genuinely
common. `service/datasourcessvc/connector/sql` owns username/password merge
helpers, connection timeout and pool defaults, `database/sql` startup
lifecycle, shared `TestConnection`/`Close` error mapping, row scanning, affected
row and insert-ID normalization, row-limit enforcement, and wrapping normalized
SQL payloads into Braidplane execution results. Dialect packages such as
PostgreSQL, MySQL, and future SQL Server still own DSN construction, driver
selection, startup error classification, statement parsing, parameter binding,
native `input` relation generation, statement read/write classification, and
introspection SQL. Warehouse/provider connectors such as BigQuery and future
Snowflake may reuse the shared SQL schema/result shapes where they match
Braidplane's row contract, but provider auth, job/load APIs, pagination, cost
limits, and warehouse-specific write behavior remain connector-owned.

Provider client code and Braidplane adapter code have different owners:

| Area | Provider client owns | Braidplane connector adapter owns |
|---|---|---|
| Transport | Provider URLs, auth headers, request construction, response decoding, pagination primitives, provider status/error mapping with `pkg/xerror`, and provider-specific host or protocol safety checks. | Connector lifecycle, `TestConnection`, `Execute`, `Close`, context propagation, execution-limit coordination, and connector-level error context around provider failures. |
| Credentials | In-memory provider credential structs needed to authenticate one runtime operation. The client must not persist, log, or expose credential values. | Credential lifecycle orchestration through `service/credentialssvc`, including write-only API behavior, validation, encryption at rest, runtime handoff, refresh/revocation/rotation state, and redaction in responses, logs, tests, docs, and operational metadata. |
| Introspection | Provider primitives for listing raw remote metadata such as tables, collections, operations, properties, files, or fields. | Mapping raw metadata into Braidplane schema response shapes, cache/refresh semantics, non-blocking introspection failure behavior, and frontend-stable schema assistance. |
| Query execution | Provider-native read/write calls, paging tokens, protocol request bodies, and provider response envelopes. | Statement decoding, `{{param_name}}` substitution, typed parameter normalization, read-only and source/sink role enforcement, runtime guardrails, and normalization into Braidplane execution results or pipeline payloads. |
| Metadata | Provider constants that are part of the remote contract, such as supported object names or API versions. | Connector capability metadata, UI field metadata, OpenAPI schema contracts, docs capability matrices, operation type registration, and pipeline source/sink compatibility. |

Bounded native-file adapters share retained-input and stream contracts described
in [the file-transfer boundary](NATIVE_FILE_STREAMING.md). This implementation
is awaiting explicit Custom configuration and full qualification; existing
connector capability claims do not imply qualified large-transfer capacity.
Profile-selected Git streaming additionally requires a Linux executor with the
process resource controls described in that boundary.

Remote file connectors use a shared Braidplane adapter for file-shaped
behavior. FTP, Git, S3, and future SFTP-style connectors keep protocol
mechanics in their provider boundary: authentication, connection setup,
host/prefix/path safety, raw read/write/list operations, provider metadata, and
provider error mapping. The shared adapter owns statement decoding, path/key
parameter substitution hooks, common read/write operation dispatch, CSV, JSON,
spreadsheet, and native-file format handling, schema sampling, runtime row/byte
limits, file payload metadata, and normalized FTP/Git/S3 execution results.
File schema sampling keeps parse/read/size failures non-fatal and surfaces safe
per-file diagnostics with the listed name and provider target.
HTTP intentionally stays outside this adapter: it can reuse lower-level
`filedata` parsing/serialization and file payload helpers where useful, but its
method, status, header, multipart, and OpenAPI operation semantics remain
owned by the HTTP connector.

Provider clients should be narrow. A provider client is not a Braidplane service,
repository, HTTP handler, API DTO mapper, SSR view helper, query editor model,
or pipeline runtime. If a provider client needs Braidplane result envelopes,
generated API types, data-source slugs, organization context, encrypted
credential storage, or pipeline step semantics, that logic belongs in the
adapter or a higher Braidplane layer instead.

Provider clients are still Braidplane application code while they live in this
repository. They should wrap provider, network, timeout, auth, and validation
failures with the repository-standard `pkg/xerror` constructors at the
point where the provider failure is understood. The adapter should preserve
those typed errors and add connector-operation context only where it helps
reviewers or operators.

New connector work should add or update:

- the per-type page under `doc/data-sources/`;
- the new connector readiness gate in this document, with every unsupported or
  deferred gate mirrored in `doc/KNOWN_LIMITATIONS.md`;
- the capability and payload matrices in this document when behavior changes;
- `pkg/connector/catalog` whenever connector family, operation type,
  source/sink support, sink backend, or forced read-only behavior changes;
- `service/datasourcessvc/connector` construction registry whenever a supported
  data source type needs config decoding, credential decoding, connector
  construction, construction-time preparation, or customer-private-host policy
  detection through connector-specific construction-time validation hooks;
- `service/credentialssvc` integration whenever the connector uses static
  secrets, OAuth2 grants, provider account metadata, token refresh, provider
  revocation, or credential rotation;
- API schema and generated code only when the public contract changes;
- package-boundary docs and depguard rules when a new import direction is
  required;
- connector tests that cover connection success, invalid credentials,
  unreachable dependency or provider failure, timeout or cancellation where
  applicable, introspection mapping, parameter substitution, result
  normalization, runtime limits, and write-only credential responses;
- deterministic fake-provider or local-container coverage instead of live
  third-party calls.

The HubSpot implementation is the first concrete application of a shared
provider boundary for the `object_api` schema and `object_rows` execution-result
family: `internal/hubspot/crm` is the portal-token CRM provider client and
`service/datasourcessvc/connector/hubspot` is the Braidplane adapter.
`internal/hubspot/oauth` owns HubSpot's app-owned OAuth protocol, while
`service/credentialssvc/provider/hubspot` is its thin credential-lifecycle
bridge. Adapter-owned fields such as HubSpot's `hs_object_id` record identifier
are added by the HubSpot mapping rather than by the generic `object_api` runtime
branch. GitHub and GitLab provider API clients live in `internal/gitprovider`
because Git proposal publish, status refresh, and future webhook
handling need provider metadata without coupling to the Git data-source adapter.
`internal/repostore` stays limited to raw git operations and provider-specific
PAT URL adaptation for `git` commands.

### Shared credential lifecycle

`service/credentialssvc` owns the shared encrypted credential lifecycle.
Data-source services own data-source behavior such as create, update, test
connection, schema introspection, query execution, and deletion, but they do
not own active secret persistence. Data-source runtime paths resolve
credentials through `credentialssvc`, receive a short-lived in-memory
credential payload, and pass that payload to the connector factory. Connector
clients and adapters must not read credential tables, decrypt storage blobs
directly, or persist refreshed token material.

The same lifecycle also backs organization environment secrets. Environment
secret metadata is owned by runtime environment services, not connector
services, and secret plaintext resolves only inside the pipeline runtime
boundary for the selected organization environment. Environment secrets do not
replace concrete data-source credentials; connector authentication still
belongs on concrete data sources.

S3 currently supports static access-key credentials with optional session tokens;
non-static authorization modes remain future work.

Snowflake password credentials and its secret-free `workload_identity` selection
use the same owner-scoped credential lifecycle, including revocation. For customer
WIF, the executor obtains a token only after selecting the exact authorized
organization/connection/account binding. Braidplane persists desired identity
state with connection changes and requires recently reconciled readiness before
each login; the separate control plane owns SPIRE registration. The historical operator
file is replaced by database state. Tokens remain runtime-only and are not
stored in the credential payload. See [Snowflake onboarding](data-sources/SNOWFLAKE.md).

The credential service supports two credential kinds:

- `static_secret`: database usernames/passwords, HTTP bearer or basic auth,
  FTP credentials, Generic Git HTTPS tokens (including PATs and GitLab project
  tokens) or SSH keys, and HubSpot private app tokens.
- `oauth2_grant`: provider access and refresh tokens, expiry, granted scopes,
  provider account metadata, refresh diagnostics, revocation state, and rotation
  state.

OAuth-first creation may persist an inactive data source with an attached
`oauth2_grant` credential in `pending` state before provider authorization.
That credential has no encrypted payload and cannot run.
See [HTTP OAuth](data-sources/HTTP.md#oauth-setup). A validated callback activates
the same credential in place and stores the encrypted grant. The service tests
the connection outside the transaction
and applies the outcome through a short revision-checked transaction. Denied,
expired, invalid, or failed callbacks leave the source inactive with safe
diagnostics and remain safe to retry; they must not manufacture a static
credential or expose callback detail.

All secret payloads are encrypted at rest with AES-GCM. Credentials are
write-only through API and SSR flows: callers can create, rotate, revoke, or
replace credentials, but read responses expose only safe metadata such as
credential kind, status, provider, granted scopes, provider account id/name/email
when supplied by the provider, access-token expiry, last refresh time, revoked
time, and safe failure code/message. API responses, durable events, logs,
traces, run metadata, and tests must not contain plaintext secrets, OAuth
authorization codes, OAuth state values, access tokens, refresh tokens, token
hashes, encrypted blobs, raw provider headers, or raw provider responses.
Store connector credentials and environment secrets through the application's
encrypted credential flows. Use the authenticated API and SSR controls to
create, rotate, revoke or replace them.

OAuth2 provider app client IDs and secrets are backend deployment
configuration, not data-source API payloads. Provider adapters use that
configuration to build authorization URLs, exchange callback codes, refresh
tokens, and revoke grants. Provider scopes and account identity are safe
metadata only; they do not become Braidplane authorization principals.

OAuth2 differs from existing Google OIDC login in purpose and ownership. OIDC
authenticates a human Braidplane user and creates or resumes a browser session.
Connector OAuth2 authorizes Braidplane to access an external provider API for an
organization-owned resource. Shared helper code may cover OAuth URL/exchange,
state hashing, expiry, and redaction, but user creation, ID-token verification,
and session creation remain in the authentication/OIDC flow rather than
`credentialssvc`.

Runtime resolution policy:

- Static credentials decrypt to an in-memory connector credential payload and do
  not mutate credential state.
- Active OAuth2 grants with non-expired access tokens decrypt to an in-memory
  connector credential payload.
- Expired or near-expiry OAuth2 grants refresh before connector construction.
  On success, `credentialssvc` replaces the encrypted token set and updates
  safe metadata in the same credential lifecycle.
- Provider `invalid_grant` or equivalent revoked-grant failures mark the grant
  revoked, clear active runtime usability, mark the owning data source inactive
  where applicable, emit a safe `data_source/revoke` event, and return a typed
  provider-grant error without leaking token values.
- Transient refresh failures persist safe diagnostics and return a typed refresh
  error. Existing encrypted grant material remains stored for later retry, but
  no connector operation receives stale expired credentials.
- Manual revoke calls the provider revoke endpoint when supported, replaces
  encrypted token material with an encrypted non-secret tombstone, marks the
  credential revoked, and emits a safe `data_source/revoke` event.
- Rotation creates a new install flow for OAuth2 grants or replaces the static
  secret payload for static credentials. Successful rotation emits a safe
  `data_source/rotate` event.

The first implementation slice proved OAuth2 with a deterministic fake provider
adapter. HubSpot OAuth2 uses the shared credential lifecycle alongside its
static private-app-token fallback. GitLab OAuth2 uses the same lifecycle with an organization-owned connection for
Git-over-HTTPS and provider-native merge-request operations. Each explicitly
attached source binds one verified project identity. Generic Git owns manual
PAT, project-token, and SSH flows; GitHub uses the separate GitHub App model.

### Organization scoping

Every data source belongs to exactly one organization. All queries and operations are scoped to the active organization. Cross-organization data access is not possible.

Projects are the workspace scope for MVP pipelines, but data sources remain
organization-scoped. This allows multiple projects in the same organization to
reuse the same configured connection while credentials stay managed once at the
organization boundary.

First-class runtime environments do not move credentials into projects or
pipeline specs. The accepted model in
ADR-009 keeps concrete
data sources organization-owned. Pipeline-scoped logical data-source references
are the portable names used by pipeline specs through the `data_source_ref`
field. Each environment binding maps
`project + pipeline + environment + data_source_ref` to one organization-owned
concrete data source. Project-level ref views aggregate the refs owned by the
project's pipelines and include pipeline provenance.

New organizations start with normal environment catalog rows for `dev`, `stg`,
and `prod`. New projects automatically enable `dev` as the default project
environment when that catalog row is active. Operators can update or delete
these seeded rows through the same environment APIs as any manually created
environment; deleting one prevents it from being auto-enabled for later
projects unless it is recreated.

Logical references declare required capabilities such as `pipeline_source` or
`pipeline_sink`. They may also declare an expected connector type when an
operator predeclares a typed contract or the pipeline spec requirement is
unambiguous. Git reconciliation syncs refs for the target pipeline from the
materialized spec and prunes refs that spec no longer
declares. Deploy preflight for an immutable version only creates or expands
refs required by that version before binding resolution; it does not prune other
refs owned by the same pipeline. Server-backed validation is read-only: it may
resolve existing pipeline refs and bindings, but it does not create or update
refs. Ambiguous operation families such as `sql` are synced as
name-and-capability refs without guessing a connector type. Bindings must point
to active, non-deleted concrete data sources that satisfy the ref capabilities;
typed refs also require the same connector type. Sink-capable refs cannot bind
to read-only concrete data sources. Binding APIs return concrete data-source
identity and status only;
config and credentials remain write-only through the existing concrete
data-source APIs.

Pipeline deployment state is also environment-scoped, but it remains separate
from concrete credentials. A deployment points `project + environment +
pipeline` at an existing immutable pipeline version. Promotion deploys the same
version hash to another environment; the selected environment's bindings
continue to decide which concrete organization-owned data sources satisfy the
pipeline's logical `data_source_ref` references. Runtime execution consumes
the environment-scoped deployment pointer and resolves those logical references
before connector execution.

### Connection testing

Every usable data source must pass a connection test before it becomes active.
Static-credential creation saves the source before testing it. OAuth-first
creation saves an inactive source and payload-free pending credential before
redirecting to the provider. Provider-backed resources such as Git repositories
must be selected before the completed authorization is tested. Resource
selection tests the complete data source automatically; an explicit
post-creation recheck remains available for later permission, resource, or
provider changes. Credentials can be updated later via the credentials
endpoint, which re-runs the test.

Connector network work runs without holding a data-source transaction or row
lock. The service snapshots the tested configuration, performs the connection
test, then stores success or failure in a short revision-checked transaction.
Create, update, credential update, OAuth callback, and explicit test failures
therefore leave the source inactive with a durable safe diagnostic even though
the operation returns a connection error. If the source changes while a test is
running, the stale result is rejected instead of overwriting the newer state.

Connection-test failures should return the stable typed API error as the primary
message and use `detailed_error` only for sanitized, client-facing diagnostics
that help an operator act on the failure. Connector and provider code must opt
in to public detail explicitly after redacting credential-bearing URLs, tokens,
passwords, keys, and other secrets. SSR forms show the stable message and safe
detail together so operators can distinguish validation, authentication,
network, and remote-provider failures without exposing credentials.

### Schema introspection

Introspection queries the remote system to discover tables (SQL), collections (MongoDB), operations (HTTP via OpenAPI), object/API fields and operations (HubSpot and future object/API connectors), or files/objects and their fields (FTP, Git, S3). Results are cached and can be refreshed on demand. A successful connection test does not guarantee that introspection succeeds — introspection failures are non-blocking and recorded separately. HTTP OpenAPI import settings are available in API and SSR create, test, and update flows; schema browsing surfaces successful operation imports, fetch/parser failures, unsafe URL rejections, and empty operation sets.

The introspection result format varies by type:

- SQL and warehouse (PostgreSQL, MySQL, BigQuery): tables with columns that
  include `name` and `type`
- Document (MongoDB): collections with field names and inferred BSON types
- HTTP: operations with `id`, `path`, `method`, `description`, optional `request_body`, and optional `responses`
- Object API (HubSpot and future object/API connectors): supported provider
  objects with fields/properties, supported operations, and provider-safe
  metadata
- File/object (FTP, Git, S3): file metadata with optional inferred fields.
  See [file discovery](data-sources/FILE_DISCOVERY.md) for raw entries and limits.

### Parameterized queries

All connector types support parameterized queries using the `{{param_name}}`
template syntax. Placeholder names may also use dotted namespaces such as
`{{runtime.run.date}}` or `{{runtime.input.customer_id}}`. Parameters are
typed: `text`, `integer`, `boolean`, `date`, `pkg/uuid`. Each connector normalizes
values to the appropriate native format (SQL placeholders, BigQuery named query
parameters, URL substitution, JSON values, BSON types, file path fragments).

The [runtime parameter catalog](../docs-public/getting-started/pipeline-specifications.md#current-runtime-parameter-catalog)
owns reserved keys, types, exposure rules and Git/FTP path placeholders. It also
defines bounded date operations and pinned prior-success metadata for retries
and recovery. Prior completion time is not incremental progress.

Saved query parameters cannot use the reserved `runtime.` prefix. Run records,
events, logs, and UI expose runtime input key names only, never their values.

### Runtime safety limits

Query execution, pipeline source steps, and pipeline sink steps all inherit the
same runtime safety limits from `[execution_limits]` in `config.toml`. Defaults
cap execution at 30 seconds, normalized results at 1,000 rows and 10 MB, and
connector execution at four active operations per organization/data source.
Broker-backed API/worker execution coordinates that concurrency limit across
the deployment.

Connector admission is FIFO within one organization/data source and independent
across data sources. Up to 16 executions wait for five seconds by default. A
full queue is rejected immediately; caller cancellation and the shorter queue
or query deadline remove pending work without consuming capacity. In
broker-backed execution, active slots are leased and heartbeated through the
persisted execution boundary so a lost replica cannot hold capacity
indefinitely.

PostgreSQL and MySQL query execution reuses bounded process-owned connection
pools keyed by organization, data source, configuration digest, and credential
version. Configuration or credential changes retire the old pool after active
leases finish; idle and maximum lifetimes bound stale connections, and graceful
shutdown closes the cache. Pre-save and stored connection tests deliberately
bypass runtime reuse so every test is fresh and cannot contaminate query pools.

PostgreSQL and MySQL charge response bytes while scanning driver values, before
copying or normalizing oversized fields. MongoDB find and aggregate fetch one
document per batch and count its JSON representation directly from BSON before
decoding it, including base64 and escaping expansion. The final SQL and MongoDB
response check includes envelope metadata and encodes one row or document at a
time. These limits bound retained connector output, not driver-owned protocol
buffers or the process heap: a driver can receive a complete row or wire message
before the connector can inspect it.

Saved query `timeout_seconds` and `row_limit` values are still part of the query contract, but server configuration is authoritative: the effective timeout and row limit are the lower of the saved query value and the configured maximum. Inline pipeline operations use the configured defaults directly.

BigQuery query jobs also enforce an effective `maximum_bytes_billed` value when
configured. This mirrors BigQuery's `maximumBytesBilled` field and caps
provider-side bytes processed/scanned for query cost control; response payload
size is still governed by Braidplane response-size limits. Operation-local values may
only narrow the server-configured limit. The fake-provider test boundary must
cover both row/result limits and BigQuery-specific query-limit mapping without
calling live BigQuery. BigQuery query result pagination is provider-internal in
the first warehouse connector: the connector fetches result pages until
configured row, response-size, timeout, or cancellation limits stop execution.
Public page-token or continuation APIs remain follow-up work.

When a limit is exceeded, Braidplane returns a typed execution error such as `EXECUTION_ROW_LIMIT_EXCEEDED`, `EXECUTION_RESPONSE_SIZE_EXCEEDED`, `EXECUTION_REQUEST_SIZE_EXCEEDED`, `EXECUTION_REDIRECT_LIMIT_EXCEEDED`, or `EXECUTION_CONCURRENCY_LIMIT_EXCEEDED`. Pipeline run failure context remains `kind=data_source`, includes the affected data source slug, and gives an action hint naming the violated limit.

### Pipeline integration

A data source can be used as a pipeline source (reads data for upstream steps) or as a pipeline sink (writes data as the terminal step). Not all operation types are symmetric — for example, a SQL source uses SELECT statements while a SQL sink uses INSERT, UPDATE, or UPSERT operations. See the per-type pages for details.

Git-compatible data sources back persistent pipeline authoring through a
project pipeline backend. That authoring backend is
separate from runtime pipeline source/sink execution:

- Git source/sink steps move runtime payload files during a pipeline run.
- Draft publish creates or updates a branch for the pipeline spec. GitHub and
  GitLab backends also create or update a provider-native PR/MR proposal; a raw
  Git backend performs the branch write only.
- Deploying a pipeline requires an inbound apply/reconcile step that
  materializes the exact Git content into database-canonical runtime storage.

Saving a UI draft must not test, execute, or mutate the data-source
connector. Publishing the draft queues the provider operation described in
PIPELINE_DATA_MODEL.md
instead of running a pipeline or updating `pipeline_specs` directly.

Branch and PR/MR status is provider capability, not generic git capability. The
GitHub and GitLab provider clients live in `internal/gitprovider` and own
provider-native proposal API calls, safe proposal metadata, and provider error
mapping. The same boundary owns provider event parsing and webhook verification,
while service-layer code remains responsible for deciding what an event means
for Proof candidates, drafts, reconcile, or deployment. Provider-native
callbacks remain separate from customer-configured pipeline webhook triggers.
A generic git remote can support branch reads/writes, but it cannot report
PR/MR closure or merge state without a provider-specific API integration.

Generic `git` data sources do not have pull request or merge request semantics.
Git itself only stores refs and commits; PR/MR objects belong to hosting
providers such as GitHub and GitLab. Draft publish therefore creates
provider-native proposals only for `github` and `pkg/gitlab` data sources. A raw
`git` data source may still be used for runtime file source/sink operations and
ordinary branch writes. Draft publication does not simulate or persist a
synthetic pull request for raw git remotes.

### Read-only data sources

A data source selects a Braidplane execution mode independently from its
credential profile:

- **Enforced read-only execution**: `read_only: true` enables Braidplane's fail-closed
  operation classifier. PostgreSQL and MySQL additionally execute accepted SQL
  inside a driver/database-native read-only transaction. Known side-effecting
  functions, locking reads, output-file extensions, stacked statements, and
  unsupported statement forms are rejected before connector dispatch.
- **Read/write execution**: `read_only: false` permits connector write
  operations. Use it only for an intentional destination operation.

Every authenticated source independently requires a **configured read-only
credential**: the customer supplies a dedicated provider identity
  whose database grants, IAM roles, filesystem permissions, or API scopes allow
  only the required reads. This is required for every source credential,
  including connectors with an enforced read-only mode. BigQuery relies on this
  mode because its query API has no read-only transaction or session setting.
An unauthenticated source has no credential boundary. Set `read_only: true`, use
only an intentionally public read endpoint, and rely on the connector classifier
plus the remote endpoint's own behavior. Credentialless public HTTPS Git sources
are forced read-only.
Read/write destinations use a separate write-capable credential scoped to only
the destination objects and operations required by the pipeline.

Read-only data sources are blocked from Braidplane-initiated mutation paths.
Pipeline sinks fail at runtime with a `403 DATA_SOURCE_READ_ONLY` error before
sink dispatch. Direct saved-query, inline-query, and incremental query execution
also fail with `403 DATA_SOURCE_READ_ONLY` before credential resolution and
connector dispatch when the operation is mutating, unsupported in read-only
mode, malformed, or cannot be classified confidently. Diagnostics identify the
unsupported operation class without returning the submitted statement.

Statement classification remains defense in depth, not a substitute for the
native database boundary or least-privilege credentials. Database read-only
transactions are also not a universal no-side-effects sandbox: vendor behavior
can retain temporary-object or session-level exceptions. Source identities must
therefore omit write, DDL, file, routine-execution, and administrative grants
that the source does not need.

Direct execution intent is connector-aware and fail-closed for read-only data sources. File-backed connectors such as FTP, Git, GitHub, GitLab, and S3 allow `read` operations and block `write` or unknown operations. HTTP allows `GET`, `HEAD`, and `OPTIONS`, and blocks other methods such as `POST`, `PUT`, `PATCH`, `DELETE`, and `TRACE`. MongoDB allows read operations such as `find`, `aggregate`, and `count_documents`, and blocks insert, update, delete, and unknown operations. BigQuery allows classified GoogleSQL reads and blocks `insert_rows` plus unsupported SQL forms, while the service account's IAM grants provide the provider-enforced read boundary. PostgreSQL and MySQL use statement classification before their native read-only transaction boundary.

HubSpot may be configured as a read-only source or writable upsert destination.
Its read-only classifier permits `list` and `search` and blocks `upsert` before
credential resolution or connector dispatch.

The `read_only` flag must be paired with configured read-only credentials when
connecting a source, especially a production system.

## Data source API endpoints

| Operation | Method | Path |
|---|---|---|
| Create | POST | `/organizations/{slug}/data-sources` |
| Create and start provider OAuth | POST | `/organizations/{slug}/provider-connectors/{provider}/data-source-installs` |
| List repositories visible through a provider grant | GET | `/organizations/{slug}/data-sources/{ds_slug}/provider-repositories` |
| Select and verify one provider repository | PUT | `/organizations/{slug}/data-sources/{ds_slug}/provider-repository` |
| List | GET | `/organizations/{slug}/data-sources` |
| Get | GET | `/organizations/{slug}/data-sources/{ds_slug}` |
| Update mutable fields (partial) | PATCH | `/organizations/{slug}/data-sources/{ds_slug}` |
| Delete (soft delete) | DELETE | `/organizations/{slug}/data-sources/{ds_slug}` |
| Restore | POST | `/organizations/{slug}/data-sources/{ds_slug}/restore` |
| Test connection (pre-save) | POST | `/organizations/{slug}/data-sources/test-connection` |
| Test stored connection | POST | `/organizations/{slug}/data-sources/{ds_slug}/test-connection` |
| Update credentials | PATCH | `/organizations/{slug}/data-sources/{ds_slug}/credentials` |
| List Git branches | GET | `/organizations/{slug}/data-sources/{ds_slug}/branches` |
| Introspect schema | GET | `/organizations/{slug}/data-sources/{ds_slug}/schema` |
| Refresh schema | POST | `/organizations/{slug}/data-sources/{ds_slug}/schema` |

Deleting a data source records a tombstone and retains connector configuration, encrypted credentials, cached schema metadata, queries, query parameters, and query execution history. Normal list, get, update, credential update, schema, query creation, query execution, and execution history paths treat a deleted data source as not found or unavailable until it is restored.

Data-source soft delete does not propagate tombstones into child queries or executions. Normal child reads are filtered through the active data source; restore clears only the data-source tombstone. Existing database cascades from data sources to schema metadata and queries remain reserved for a future permanent purge path. Permanent data-source purge is intentionally deferred until product semantics and cascade tests are defined.

## Related documentation

- PIPELINE_DATA_MODEL.md — how data sources integrate with pipeline steps and contracts
- KNOWN_LIMITATIONS.md — accepted constraints and deferred capabilities
