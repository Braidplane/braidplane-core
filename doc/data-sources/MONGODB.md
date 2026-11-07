# MongoDB data source

## Purpose and best-fit use cases

Use the MongoDB connector when your data lives in a MongoDB database. It supports flexible document schemas and is suited for read-heavy workloads, document aggregation pipelines, and pipeline sink operations that insert or update documents in a collection.

## Transport security

Connections require certificate-verified TLS with the configured hostname. Servers must present a certificate trusted by the runtime.
See the shared [transport encryption policy](../DATA_SOURCES.md#transport-encryption) for the local fixture exception.

## Shared capabilities

MongoDB supports the shared data-source lifecycle, document query execution, and
pipeline source/sink roles. See the [capability matrix](../DATA_SOURCES.md#capability-matrix)
and [payload compatibility matrix](../DATA_SOURCES.md#pipeline-payload-compatibility-matrix)
for the canonical cross-connector contract.

## Required config fields

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `host` | yes | string | — | Hostname or IP of the MongoDB server |
| `port` | yes | integer | 27017 | 1–65535 |
| `database` | yes | string | — | Database name |
| `auth_database` | no | string | `admin` | Authentication database |
| `connect_timeout` | no | integer | 5 | Connection timeout in seconds (1–300) |
| `allow_private_hosts` | no | boolean | `false` | Requires private host access to be enabled for the deployment; hosted/default deployments reject `true` |

## Private host access

Private IP ranges and loopback addresses are blocked by default to reduce SSRF
risk. Hosted/default Braidplane rejects customer MongoDB data source payloads
that set `allow_private_hosts: true`. The `self-hosted` profile rejects the broad switch
and currently supports public destinations for this connector. See the
[private-host policy](../DATA_SOURCES.md#private-host-access) for scope and local
fixture exceptions.

## Credentials

| Field | Required | Notes |
|---|:---:|---|
| `username` | yes | MongoDB user |
| `password` | yes | MongoDB password |

Credentials are encrypted at rest and never returned in API responses. See [DATA_SOURCES.md - Shared credential lifecycle](../DATA_SOURCES.md#shared-credential-lifecycle).

## Schema introspection

The connector samples documents from each collection to infer field types. The result format is `DocumentSchemaResponse` (schema_type: `document`, type: `mongodb`). Each collection entry includes field names and inferred BSON types: `string`, `int`, `double`, `boolean`, `objectId`, `date`, `timestamp`, `object`, `array`, `binary`, `decimal`, `regex`, `null`, or `mixed` (when multiple types are seen for the same field).

Sampling is best-effort: collections or documents that cannot be read are skipped silently.

## Query and operation model

Queries can be expressed as MongoDB aggregation pipelines or find operations. Parameters use `{{param_name}}` template syntax. Exact whole-field placeholders resolve before statement unmarshaling, so saved operations can bind native objects or arrays directly into fields such as `documents`, `document`, `filter`, `update`, or `pipeline` instead of relying on raw JSON string injection. Structured fields in those positions must resolve to non-null object or array values; `null` is rejected before execution.

Execution returns:
- `column_names` — keys from the top-level fields of returned documents
- `rows` — array of document objects (field name → value)
- `row_count` — number of documents returned

Nested documents and arrays are returned as-is in the row values.

## Pipeline relevance

**As a source**: the pipeline step uses `operation_type: mongodb` with the query (aggregation pipeline or find) in `operation_inline`. The runtime payload is a JSON document array; row-based consumers normalize that array to rows. Count results are metadata rather than an extraction payload.

MongoDB source steps support incremental `watermark` contracts. The operation
must accept the previous checkpoint in its filter or aggregation pipeline and
return the document field used to compute the next observed checkpoint.
Saved and inline source operations can reference
`{{runtime.incremental.lower_bound.value}}` and
`{{runtime.incremental.upper_bound.value}}` inside `filter` or `pipeline`
documents. Exact placeholders resolve before BSON encoding, so object and array
statement fields keep their native structure. Braidplane observes the next checkpoint
by selecting the maximum returned value from the configured `field`.
Watermark contracts may declare a positive duration `lookback_window` when the
query should intentionally overlap reads for late-arriving documents.

**As a sink**: the pipeline step uses `operation_type: mongodb`. The public statement exposes `insert_one`, `update_one`, `update_many`, `delete_one` and `delete_many`. It exposes no replace or upsert option. Saved operations bind runtime payloads through structured placeholders such as `document: "{{input}}"`, `filter: "{{input}}"` or `update: "{{input}}"`; match the operation's object/array shape. Contextual CSV/file bindings do not provide automatic CSV-to-document or native binary ingestion.

## Known limitations

- Transform paths that require JSON-native transform execution are outside the
  production-launch path. Transform steps operate on rows.
- Pagination is not supported.
- Find and aggregate operations count JSON response bytes from raw BSON before
  decoding each document. Later batches contain one document, so multi-document
  results can require a `getMore` round trip per additional document and increase
  latency against the execution timeout. See [runtime safety limits](../DATA_SOURCES.md#runtime-safety-limits)
  for encoding differences and driver-buffer limits.

See [connector support levels](../../docs-public/connectors/support-levels.md) for shipped connector scope, limitations and acceptance boundaries.
