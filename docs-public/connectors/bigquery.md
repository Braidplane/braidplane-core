# BigQuery

[Proof](../getting-started/proof.md#demonstrated-alpha-connector-scope) can use
explicitly authorized read-only metadata for one selected table. These
observations do not read rows, start query jobs, insert data, or establish
BigQuery write permission.

Run GoogleSQL query jobs and append rows to existing BigQuery tables. Both
pipeline sources and sinks use rows; dataset/table metadata supports schema
discovery and contract authoring.

Use the [shared connection workflow](connect-a-system.md) to create this data
source in your workspace. You can read this guide without an account or
provider resources. Running examples requires provider resources appropriate for your evaluation.

## Connection settings

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `project_id` | yes | string | none | BigQuery project used for jobs, dataset discovery, and writes |
| `location` | no | string | provider default | Job location, such as `US` or `EU`; set it for regional or multi-regional datasets so jobs and paged result reads stay in the same location |
| `default_dataset` | no | string | none | Optional BigQuery dataset namespace used when an operation omits `dataset` |
| `timeout_seconds` | no | integer | 5 | Request/job timeout in seconds, capped by configured execution limits |
| `maximum_bytes_billed` | no | integer | configured limit | BigQuery-native `maximumBytesBilled` query cost guard, capped by configured execution limits |

## Authentication and permissions

| Field | Required | Notes |
|---|:---:|---|
| Google connection | yes in the UI | Authorize through **Connect with Google** with an account that has the required BigQuery access |

Credentials belong in the connection, never in an operation statement. They are
encrypted at rest and are not returned in API responses. Hosted/default
deployments block loopback and private network endpoints; your laptop’s
`localhost` is not the hosted worker’s network.

In the UI, enter the project ID and selected dataset, choose read-only or
write-enabled access, then select **Connect with Google**. Google IAM remains
the provider boundary. For source access, use BigQuery Job User on the execution
project and Data Viewer scoped to the required tables or views. Avoid write
permissions and unreviewed routines. `read_only: true` restricts operations in
Braidplane. Use a separate destination identity with only the needed table
append permissions.

Advanced API and CLI connections can use static service-account credentials.
Keep those credentials out of operation statements and source control.

## Test and discover

The connection test reads metadata for the selected dataset. Refresh discovers
its tables and fields, naming tables as `dataset.table`. A successful metadata
read does not establish write permission. Use the correct dataset location for
both discovery and query jobs.

## Read example

Replace `example_project.analytics.customers` with an existing table containing
`id` and `email`. Define an integer `customer_id` parameter. A matching ID returns
that customer as one row. Table names are authored identifiers, not parameters.

```json
{
  "type": "bigquery",
  "operation": "query",
  "sql": "SELECT id, email FROM `example_project.analytics.customers` WHERE id = {{customer_id}}"
}
```

## Write example

Create `analytics.customer_export` with compatible columns first. Save this
operation as a pipeline sink. Upstream rows such as
`[{"id":1,"email":"alex@example.com"}]` are appended to that table. An append
without an appropriate replay policy can duplicate rows.

```json
{
  "type": "bigquery",
  "operation": "insert_rows",
  "dataset": "analytics",
  "table": "customer_export",
  "rows": "{{input}}"
}
```

## Operation reference

| Field | Meaning |
|---|---|
| `type` | `bigquery` |
| `operation` | `query` or `insert_rows`; set it explicitly |
| `sql` | GoogleSQL for `query` |
| `timeout_seconds` | Optional narrower timeout, 1–300 seconds |
| `maximum_bytes_billed` | Optional narrower provider bytes-processed cost cap |
| `dataset` | Write dataset, or the configured default dataset |
| `table` | Existing write target table |
| `rows` | Object/row array, or `{{input}}` for a pipeline sink |
| `insert_id_prefix` | Optional prefix for best-effort provider duplicate suppression |

`{{name}}` placeholders are bound as named parameters. Supported scalar
parameter types include text, integer, boolean, date, and UUID. Identifiers
cannot be supplied as value parameters. `insert_id_prefix` alone does not
guarantee exactly-once writes.

## Results and pipeline behavior

Use `operation_type: bigquery`. Query results return rows; provider pages are
fetched internally until completion or a configured limit. Watermark sources
must apply checkpoint filters and return the watermark field.

| BigQuery value | Result value |
|---|---|
| STRING, date/time types, GEOGRAPHY | String |
| INT64 / FLOAT64 / BOOL | Integer / number / boolean |
| NUMERIC, BIGNUMERIC | String preserving precision |
| JSON | Parsed JSON when valid |
| BYTES | Base64 string |
| RECORD, repeated fields | Object or array |

Sinks accept rows natively; JSON and CSV require normalization into row objects.
File and multipart payloads are unsupported. Required fields are non-nullable;
nested schema flattening is not available.

## Limits and troubleshooting

- No public continuation tokens, resumable partial results, load jobs, exports,
  table creation, schema evolution, or merge/upsert helpers.
- The bytes-billed cap controls provider query cost; row and response-size caps
  are separate. Operation limits cannot enlarge deployment limits.
- Authentication/access errors: check the service account, execution project,
  dataset visibility, and table-specific permissions.
- Location errors: match the dataset region. Limit errors: narrow the query or
  select fewer fields rather than expecting unrestricted pagination.
- Missing or incompatible target columns: prepare the table and explicit field
  mapping before inserting.

See [operation and pipeline basics](connect-a-system.md#use-the-examples) for
where to use these statements, parameters, environment bindings, and replay
safety. Return to the [connector directory](index.md).
