# MySQL data source

## Purpose and best-fit use cases

Use the MySQL connector when your data lives in a MySQL (or compatible) database. It covers the same SQL-based read and write scenarios as the PostgreSQL connector with MySQL-native syntax. Suitable for transactional reads, structured reports, and pipeline sink operations requiring INSERT, UPDATE, or UPSERT.

## Transport security

Connections require certificate-verified TLS with the configured hostname. Servers must support TLS and present a certificate trusted by the runtime.
See the shared [transport encryption policy](../DATA_SOURCES.md#transport-encryption) for the local fixture exception.

## Shared capabilities

MySQL supports the shared data-source lifecycle, SQL query execution, and
pipeline source/sink roles. See the [capability matrix](../DATA_SOURCES.md#capability-matrix)
and [payload compatibility matrix](../DATA_SOURCES.md#pipeline-payload-compatibility-matrix)
for the canonical cross-connector contract.

## Required config fields

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `host` | yes | string | — | Hostname or IP of the MySQL server |
| `port` | yes | integer | 3306 | 1–65535 |
| `database` | yes | string | — | Database name |
| `connect_timeout` | no | integer | 5 | Connection timeout in seconds (1–300) |
| `allow_private_hosts` | no | boolean | `false` | Requires private host access to be enabled for the deployment; hosted/default deployments reject `true` |

## Private host access

Private IP ranges and loopback addresses are blocked by default to reduce SSRF
risk. Hosted/default Braidplane rejects customer MySQL data source payloads that
set `allow_private_hosts: true`. The `self-hosted` profile rejects the broad switch
and currently supports public destinations for this connector. See the
[private-host policy](../DATA_SOURCES.md#private-host-access) for scope and local
fixture exceptions.

## Credentials

| Field | Required | Notes |
|---|:---:|---|
| `username` | yes | Database user |
| `password` | yes | Database password; an explicit empty string is accepted for accounts configured without a password |

For passwordless accounts, omit `--password` in the CLI or leave the MySQL
password field blank in the create, test, or credential replacement form.

Credentials are encrypted at rest and never returned in API responses. See [DATA_SOURCES.md - Shared credential lifecycle](../DATA_SOURCES.md#shared-credential-lifecycle).

## Operation modes and least privilege

For a source, set `read_only: true` and use a dedicated MySQL account with
`SELECT` on only the required schemas, tables, or views. Do not grant write,
DDL, `FILE`, `EXECUTE`, administrative, or global privileges unless a separate
destination workflow explicitly requires them.

In enforced read-only mode, Braidplane classifies the statement and then starts
a MySQL `READ ONLY` transaction through the driver. The classifier also rejects
known locking reads, session-lock functions, output-file clauses, session
variable assignments, and multi-statement forms. MySQL permits some operations
on temporary tables inside read-only transactions, so least-privilege grants
remain mandatory.

Read-only classification rejects string literals that use backslash escapes
because their meaning changes with the connection's `NO_BACKSLASH_ESCAPES` SQL
mode. Use standard doubled-quote escaping in read-only statements.

Use `read_only: false` only with a separately scoped write account for
intentional sink operations.

## Schema introspection

The connector queries `information_schema.tables` and `information_schema.columns` for the configured database. Each column includes name and data type. The result format is `SQLSchemaResponse` (schema_type: `sql`, type: `mysql`).

Introspection can be refreshed at any time via the introspect endpoint. A failed introspection does not invalidate the connection.

## Query and operation model

Queries use standard SQL with Braidplane `{{param_name}}` placeholders. The connector converts these markers to MySQL `?` positional placeholders internally before execution and passes values as typed parameters.

Query execution returns:
- `column_names` — ordered list of column names from the result set
- `rows` — array of objects (column name → value)
- `row_count` — number of rows affected or returned
- `last_insert_id` — for INSERT statements that return a generated ID

## Pipeline relevance

**As a source**: the pipeline step uses `operation_type: sql` with a SELECT statement in `operation_inline.sql`. The result flows into downstream transform steps.

MySQL source steps support incremental `watermark` contracts. The operation
must accept the previous checkpoint as an explicit query bound and return the
source field used to compute the next observed checkpoint. Watermark contracts
can reference `{{runtime.incremental.lower_bound.value}}` and
`{{runtime.incremental.upper_bound.value}}`; the connector binds them as MySQL
positional parameters before execution. Braidplane observes the next checkpoint by
selecting the maximum returned value from the configured `field`. Watermark
contracts may declare a positive duration `lookback_window` when the query
should intentionally overlap reads for late-arriving rows.

**As a sink**: the pipeline step uses `operation_type: sql`. Supported write operations: `insert`, `update`, `upsert`. The sink maps upstream row fields to target table columns using the sink input contract.

SQL sink operations can use `FROM input` or `JOIN input` to reference the upstream payload directly as a native CTE relation — the connector materializes it as a `WITH input AS (SELECT * FROM JSON_TABLE(...))` block automatically. This works with all three upstream payload types:

- **Rows** (from SQL or transform steps): each row becomes a record in the relation.
- **JSON** (from HTTP steps returning `application/json`): arrays produce one row per element; a single object produces one row. Scalars and null fall back to the public `{{input}}` binding.
- **CSV** (from HTTP steps returning `text/csv`): the first row is treated as the header; each subsequent row becomes a record. Values are always strings. Malformed CSV falls back to the public `{{input}}` binding.

The generated relation quotes column names as SQL identifiers and uses fixed
internal JSON paths. It preserves names containing quotes, backslashes or
`{{field}}`, subject to the database's identifier rules. Missing and null cells
both produce SQL nulls.

Column types in the generated CTE are inferred from the Go value types: integers → `BIGINT`, floating-point → `DOUBLE`, booleans → `BOOLEAN`, nested objects and arrays → `JSON`, everything else → `TEXT`. Use the MySQL inline JSON path syntax (`->>'$."key"'`) to extract values from `JSON` columns in the same query.

## Known limitations

- Pagination is not supported. Queries return all matching rows in a single execution.

See [connector support levels](../../docs-public/connectors/support-levels.md) for shipped connector scope, limitations and acceptance boundaries.

## Exact decimal values

MySQL DECIMAL results retain exact text and `decimal(p,s)` introspection metadata.
Declarations within precision 38 map to bounded decimal pipeline fields. SQL sink
input relations preserve decimal and JSON-number values using a declaration
large enough for every row. Values outside MySQL's native DECIMAL precision/scale
bounds reject before JSON_TABLE conversion rather than rounding silently.
Decimal cells enter the intermediate JSON relation as text so MySQL's JSON
number parser cannot round them before DECIMAL conversion.
See decimal compatibility.
