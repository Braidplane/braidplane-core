# Connector package inventory

The [registry](../service/datasourcessvc/connector/registry.go) constructs
the 13 connector kinds below. The [catalog](../pkg/connector/catalog/catalog.go)
defines operation identifiers, payload and binding modes, replay strategies,
schema families and supported Proof observations. The
[package boundary](PACKAGE_BOUNDARIES.md#connector-boundary) assigns ownership.

| Registered kind | Provider client and connection configuration | Resolved credential inputs | Pipeline source and sink payloads | Schema discovery and selected-resource Proof | Provider-specific limits and typed error examples |
|---|---|---|---|---|---|
| PostgreSQL | `pgx/v5`, host/port/database, SSL mode and connect timeout. Trusted runtime can supply a private dialer and pool settings. | Username and password | Authored SQL reads rows or exports a file. SQL sink binds native rows, normalizes JSON/CSV or uses explicit file binding. | SQL catalog. `postgres.table_metadata` | Pool and statement deadlines, read-only execution, row and file budgets. See `SourceSchemaReferenceInvalid` and `DestinationConstraintViolation`. |
| MySQL | `go-sql-driver/mysql`, host/port/database and connect timeout | Username and password field, which can be empty | Authored SQL reads rows or exports a file. SQL sink binds native rows, normalizes JSON/CSV or uses explicit file binding. | SQL catalog. `mysql.table_metadata` | Connection and execution deadlines, read-only execution, row and file budgets. See `InvalidCredentials` and `ConnectionFailed`. |
| BigQuery | Local client wraps `google.golang.org/api/bigquery/v2`. Project, location, default dataset, endpoint, timeout and maximum billed bytes. | Service-account JSON or resolved access token | Query reads rows or exports a file. `insert_rows` appends rows, including normalized JSON/CSV. | Dataset/table/field catalog. `bigquery.table_metadata` supports an append-capability claim. | Billed-byte ceiling, deadline, cumulative response and row/stream budgets. See `InvalidCredentials` and `ExecutionResponseSizeExceeded`. |
| Snowflake | Local client uses `gosnowflake/v2`. Account, warehouse, database, schema, role, timeout and WIF object grants. | Username/password or trusted WIF token source | SQL reads rows or exports a file. Authored `INSERT SELECT FROM input` consumes rows or normalized JSON/CSV. | SQL catalog. No selected-resource Proof observer. | WIF object and statement restrictions, deadline, row and file budgets. See `InvalidCredentials` and `ExecutionUnavailable`. |
| HTTP | `net/http` and `kin-openapi`, base URL, health path, OpenAPI URL and request/schema timeouts | Optional API key and header, bearer token (with destination when OAuth-resolved), basic auth or secret headers | Reads JSON, CSV, file or multipart form. Acknowledgement can require specific 2xx codes and JSON checks. Sink binds rows/JSON, raw CSV/file or form fields to authored requests. Bounded HubSpot company updates PATCH mapped properties and reconcile uncertain effects by reading the same company. | Optional OpenAPI operations. No selected-resource Proof observer. | Egress and redirect policy, request/response bytes, pagination, deadlines and conditional retries. See `ExecutionRedirectLimitExceeded` and `HTTPAcknowledgementUncertain`. |
| MongoDB | `mongo-driver/v2`, host/port/database, auth database and connect timeout | Username and password | Find, aggregate and counts return JSON. Writes use explicit document/filter/update bindings. | Collections and field schema. `mongodb.collection_metadata` | Read-only execution, cursor deadline, document/response and row limits. See `InvalidCredentials` and `ExecutionRowLimitExceeded`. |
| HubSpot | `pkg/hubspot/crm` HTTP client, optional base URL and timeout. Operations select the CRM object and properties. | Private-app token or resolved access token and scopes | List/search produce rows. Keyed upsert consumes mapped rows or normalized JSON/CSV. | CRM object/property catalog. `hubspot.contact_metadata` supports a source live-identity claim. | Request deadline, bounded pages and response size, keyed-write requirements. See `InvalidInput` and `ExecutionRowLimitExceeded`. |
| FTP | `jlaffaye/ftp`, host/port, base path and connect timeout | Username and password | File reads produce rows or raw file bytes. Writes serialize rows/JSON/CSV to compatible formats or stream native files. | File discovery. No selected-resource Proof observer. | Path scope, operation deadline, file/sample budgets and bounded spreadsheet parsing. See `ConnectionFailed` and `ExecutionFailed`. |
| Git | `pkg/repostore` with Git/OpenSSH, repository URL, branch and base path | HTTPS credentials or SSH key, optional passphrase and host keys | Reads repository files as rows or raw bytes. Writes serialize compatible payloads or commit native files. | File discovery. No selected-resource Proof observer. | Shallow clone, path and branch scope, file/byte and time limits. See `InvalidCredentials` and `ValidationFailed`. |
| GitHub | Same Git adapter. Core resolves installation repository identity to the runtime URL, branch and path. | Resolved GitHub App installation token for managed access | Same repository file reads/writes as Git. | File discovery. No selected-resource Proof observer. | Git limits and repository/branch permissions. Uses the Git error mapping. |
| GitLab | Same Git adapter. Core resolves the selected project to the runtime URL, branch and path. | Resolved GitLab OAuth token for managed access | Same repository file reads/writes as Git. | File discovery. No selected-resource Proof observer. | Git limits and project/branch permissions. Uses the Git error mapping. |
| S3 | Local client wraps AWS SDK for Go v2. Endpoint, bucket, region, prefix, path style and timeout. | Access key, secret and optional session token | Object reads return rows or native file bytes. Object puts serialize compatible payloads or stream native files. | Object listing/file discovery. `s3.object_metadata` | Bucket/prefix confinement, object and sample byte budgets, bounded listing and deadlines. See `NotFound` and `ExecutionResponseSizeExceeded`. |
| Google Workspace | `pkg/googleworkspace` wraps official Drive, Sheets, Docs and Slides clients. Connection config selects the selected-files access mode. | Resolved OAuth access token and granted scopes | Sheets rows, Drive files and Docs/Slides reads/exports. Sinks update Sheets ranges, create/update Drive files or create/update native text content. | File discovery. `google_workspace.worksheet_metadata` and `google_workspace.file_metadata` | Selected-file scope, bounded lists, cells, file bytes and deadlines. See `DataSourceReadOnly` and `ReplayUnsafe`. |

All adapters implement [Connector](../service/datasourcessvc/connector/types.go).
Operation JSON and typed parameters enter the adapter, and
[`ExecutionResult`](../pkg/connector/result/result.go) carries normalized data and safe
metadata. Adapters use [`xerror`](../pkg/xerror/xerror.go) for typed failures such as invalid
operations, connection failures and limit violations. Callers check both the
returned error and `ExecutionResult.Error()` according to the existing contract.

Core supplies [execution limits](../service/datasourcessvc/connector/types/param.go)
and trusted transport policy. Adapters apply the relevant fields and honor context
cancellation. Shared [schema quotas](../pkg/connector/schemaquota/quota.go) bound discovery,
[file streams](../pkg/connector/filestream/retained.go) carry bounded transfers, and
[sink bindings](../pkg/sinkbinding/doc.go) and [write guards](../pkg/connector/writegate/gate.go)
carry sink metadata and the runtime guard to the write boundary. Core owns
admission and retry orchestration. HTTP's adapter-level write retries require its existing sink
marker and idempotency conditions.

Proof observers implement [`MetadataObserver`](../pkg/proofevidence/metadata.go) through
the [connector observation contract](../service/datasourcessvc/connector/observation.go).
Adapters own selector matching, provider metadata reads and typed assessments
using `ProofVerdict` and `ProofDiagnostic`. Core owns grant admission, protected
transport, the durable ledger and receipts. Schema discovery and Proof use
distinct contracts.
