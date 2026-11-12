# PostgreSQL data source

## Purpose and best-fit use cases

Use the PostgreSQL connector when your data lives in a PostgreSQL database and you need SQL-based read or write access from Braidplane queries or pipeline steps. It is well-suited for transactional workloads, structured reporting queries, and ETL sink operations that require INSERT, UPDATE, or UPSERT semantics.

## Transport security

The UI and CLI preserve `ssl_mode` during creation, draft connection tests and
settings updates. URI import preserves `sslmode`.

Connections default to `ssl_mode: verify-full`. Other modes are rejected unless the explicit local development policy is enabled. Certificates must be trusted by the runtime and match the configured hostname.
See the shared [transport encryption policy](../DATA_SOURCES.md#transport-encryption) for the local fixture exception.

## Shared capabilities

PostgreSQL supports the shared data-source lifecycle, SQL query execution, and
pipeline source/sink roles. See the [capability matrix](../DATA_SOURCES.md#capability-matrix)
and [payload compatibility matrix](../DATA_SOURCES.md#pipeline-payload-compatibility-matrix)
for the canonical cross-connector contract.

## Required config fields

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `host` | yes | string | None | Hostname or IP of the PostgreSQL server |
| `port` | yes | integer | 5432 | 1 to 65535 |
| `database` | yes | string | None | Database name |
| `ssl_mode` | no | string | `verify-full` | SSL mode; alternatives require the local development transport exception |
| `connect_timeout` | no | integer | 5 | Connection timeout in seconds (1 to 300) |
| `allow_private_hosts` | no | boolean | `false` | Requires private host access to be enabled for the deployment; hosted/default deployments reject `true` |

## Private host access

Private IP ranges and loopback addresses are blocked by default to reduce SSRF
risk. Hosted/default Braidplane rejects customer PostgreSQL data source payloads
that set `allow_private_hosts: true`. For the `self-hosted` profile, configure the
[exact operator-pinned source](../../CONFIG.md#pinned-private-postgresql-source)
and keep `allow_private_hosts` false. The broad switch is reserved for local
development fixtures.

## Credentials

| Field | Required | Notes |
|---|:---:|---|
| `username` | yes | Database user |
| `password` | yes | Database password |

Credentials are encrypted at rest and never returned in API responses. See [DATA_SOURCES.md - Shared credential lifecycle](../DATA_SOURCES.md#shared-credential-lifecycle).

## Operation modes and least privilege

For a source, set `read_only: true` and use a dedicated PostgreSQL role with
`CONNECT`, `USAGE` on only the required schemas, and `SELECT` on only the
required tables or views. Do not grant write, DDL, routine-execution, large
object, file, replication, role-management, or superuser capabilities unless a
separate destination workflow explicitly requires them. Remove unnecessary
`TEMPORARY` access, audit and revoke default `PUBLIC EXECUTE` access to
side-effecting or security-definer routines, and set owner default privileges
so future routines are not callable by the source role without review.

In enforced read-only mode, Braidplane classifies the statement and then runs it
inside a PostgreSQL `READ ONLY` transaction through the driver. The classifier
also rejects known function, locking, file-output, and multi-statement forms
that a transaction setting alone may not make harmless. PostgreSQL read-only
transactions are a database-native boundary, but PostgreSQL documents them as a
high-level restriction rather than a guarantee that every possible side effect
is impossible; least-privilege grants remain mandatory.

Use `read_only: false` only with a separately scoped write role for intentional
sink operations.

## Schema introspection

The connector queries `information_schema.tables` and `information_schema.columns` to enumerate tables and their columns. Each column includes name and data type. The result format is `SQLSchemaResponse` (schema_type: `sql`, type: `postgres`).

Introspection can be refreshed at any time via the introspect endpoint. A failed introspection does not invalidate the connection.

## Query and operation model

Queries use standard SQL with `{{param_name}}` placeholders in Braidplane. Before execution, the PostgreSQL connector converts those markers to positional `$1, $2, ...` parameters and passes values as typed parameters.

Query execution returns:
- `column_names`: ordered list of column names from the result set
- `rows`: array of objects (column name → value)
- `row_count`: number of rows affected or returned
- `last_insert_id`: not populated for PostgreSQL

For PostgreSQL, generated IDs are not exposed via `last_insert_id`. If an INSERT needs to return generated values, write the statement with `RETURNING` and read those values from the returned rows.

## Pipeline relevance

**As a source**: the pipeline step uses `operation_type: sql` with a SELECT statement in `operation_inline.sql`. The result set flows into downstream transform steps.

PostgreSQL source steps support incremental `watermark` contracts. The
operation must accept the previous checkpoint as an explicit query bound and
return the source field used to compute the next observed checkpoint.
Saved and inline source operations can reference
`{{runtime.incremental.lower_bound.value}}` and
`{{runtime.incremental.upper_bound.value}}`; the connector binds them as
ordinary SQL parameters before execution. Braidplane observes the next checkpoint by
selecting the maximum returned value from the configured `field`.
Watermark contracts may declare a positive duration `lookback_window` when the
query should intentionally overlap reads for late-arriving rows.

**As a sink**: the pipeline step uses `operation_type: sql`. Supported write operations: `insert`, `update`, `upsert`. The sink maps upstream row fields to target table columns using the sink input contract.

SQL sink operations can use `FROM input` or `JOIN input` to reference the upstream payload directly as a native CTE relation. The connector materializes it as a `WITH input AS (SELECT * FROM jsonb_to_recordset(...))` block automatically. This works with all three upstream payload types:

- **Rows** (from SQL or transform steps): each row becomes a record in the relation.
- **JSON** (from HTTP steps returning `application/json`): arrays produce one row per element; a single object produces one row. Scalars and null fall back to the public `{{input}}` binding.
- **CSV** (from HTTP steps returning `text/csv`): the first row is treated as the header; each subsequent row becomes a record. Values are always strings. Malformed CSV falls back to the public `{{input}}` binding.

Column types in the generated CTE are inferred from the Go value types: integers → `bigint`, floating-point → `double precision`, booleans → `boolean`, nested objects and arrays → `jsonb`, everything else → `text`. Use the `->>` operator to extract text from `jsonb` columns inline in the same query.

## Known limitations

- Pagination is not supported. Queries return all matching rows in a single execution.

See [connector support levels](../../docs-public/connectors/support-levels.md) for available operations, connector status and provider requirements.

## Exact decimal values

PostgreSQL NUMERIC results retain driver decimal text. Qualified numeric columns
retain `numeric(p,s)` metadata; declarations within precision 38 and nonnegative
scale map to bounded decimal pipeline fields. Unqualified/wider/negative-scale
native types keep their prior mapping and require an explicit bounded cast or
contract. Decimal and JSON-number SQL sink input relations use PostgreSQL
`numeric`, with no float conversion. PostgreSQL target column rules still apply.
See decimal compatibility.
