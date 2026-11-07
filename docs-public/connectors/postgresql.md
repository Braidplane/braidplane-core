# PostgreSQL

Read relational data with parameterized SQL and write pipeline output using
explicit inserts, updates, or database-native upserts. Sources return rows that
can feed SQL transforms; destinations use the database SQL dialect.

Use the [shared connection workflow](connect-a-system.md) to create this data
source in your workspace. You can read this guide without an account or
provider resources. Running examples requires provider resources appropriate for your evaluation.

## Connection settings

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `host` | yes | string | — | Hostname or IP of the PostgreSQL server |
| `port` | yes | integer | 5432 | 1–65535 |
| `database` | yes | string | — | Database name |
| `connect_timeout` | no | integer | 5 | Connection timeout in seconds (1–300) |
| `allow_private_hosts` | no | boolean | `false` | Requires private host access to be enabled for the deployment; hosted/default deployments reject `true` |

## Authentication and permissions

| Field | Required | Notes |
|---|:---:|---|
| `username` | yes | Database user |
| `password` | yes | Database password |

Credentials belong in the connection, never in an operation statement. They are
encrypted at rest and are not returned in API responses. Hosted/default
deployments block loopback and private network endpoints; your laptop’s
`localhost` is not the hosted worker’s network.

Use a dedicated source identity with access only to the required tables or
views. Set `read_only: true` for reads. Braidplane checks the statement and
executes it in a native read-only transaction; provider privileges remain an
independent boundary. Use a separately scoped writable connection for sinks.

For a source, set `read_only: true` and use a dedicated PostgreSQL role with
`CONNECT`, `USAGE` on only the required schemas, and `SELECT` on only the
required tables or views. Do not grant write, DDL, routine-execution, large
object, file, replication, role-management, or superuser capabilities unless a
separate destination workflow explicitly requires them. Remove unnecessary
`TEMPORARY` access, audit and revoke default `PUBLIC EXECUTE` access to
side-effecting or security-definer routines, and set owner default privileges
so future routines are not callable by the source role without review.

## Test and discover

Test the connection, save it, and refresh schema metadata. Discovery reads
`information_schema` for visible tables and columns. A failed refresh does not
invalidate an otherwise usable connection. Check database/schema permissions
when the test succeeds but expected tables are missing.

## Read example

Assume `customers` contains `id=1, email=alex@example.com`. Define an integer
parameter named `customer_id` with value `1`. This statement returns one row
with `id` and `email`; an unmatched ID returns no rows.

```json
{
  "type": "postgres",
  "sql": "SELECT id, email FROM public.customers WHERE id = {{customer_id}}"
}
```

## Write example

Create the destination table `customer_export` with compatible `id` and `email`
columns first. Save this as a sink operation. With upstream rows
`[{"id":1,"email":"alex@example.com"}]`, it inserts that row. The `input` relation
is supplied by the pipeline; this example is not a standalone database script.
An append can duplicate rows on replay: choose the sink replay policy before
running it.

```json
{
  "type": "postgres",
  "sql": "INSERT INTO public.customer_export (id, email) SELECT id, email FROM input"
}
```

## Operation reference

| Field | Meaning |
|---|---|
| `type` | `postgres` for saved/API statements; pipeline `operation_type` remains `sql` |
| `sql` | SQL text in the target database dialect |

Use `{{parameter_name}}` for values and declare their types in the saved query.
Do not quote or interpolate placeholders yourself. Table and column names must
be authored as SQL identifiers, not supplied as value parameters.

## Pipeline behavior

Use `operation_type: sql`. Reads emit rows. SQL sinks can refer to upstream
rows through `FROM input` or `JOIN input`. Structured JSON objects/arrays and
headered CSV can also be normalized into this relation; CSV values remain
strings. Do not assume scalar JSON, malformed CSV, or raw file bytes become
SQL rows. Cast fields explicitly when the destination requires different types.

Watermark sources must return the chosen field and apply checkpoint bounds in
the SQL predicate. The runtime values are
`{{runtime.incremental.lower_bound.value}}` and
`{{runtime.incremental.upper_bound.value}}`. Handle an empty initial checkpoint
in the query and configure the watermark contract; a placeholder alone does
not enable incremental execution. Positive lookback windows can overlap reads
for late-arriving records. This is not CDC.

## Results, limitations, and troubleshooting

- Query results include `column_names`, `rows`, and `row_count`. Use `RETURNING` for generated values; `last_insert_id` is not populated.
- There is no public result pagination; executions are bounded by row, time,
  and payload limits. Narrow a query instead of expecting partial continuation.
- Authentication failures: verify the database and dedicated account. Missing
  tables: refresh metadata and check schema permissions.
- Rejected writes: check `read_only`, provider write grants, destination column
  types, and the selected operation.
- Source roles should not have access to side-effecting or security-definer
  routines; a read-only transaction does not replace least privilege.


See [operation and pipeline basics](connect-a-system.md#use-the-examples) for
where to use these statements, parameters, environment bindings, and replay
safety. Return to the [connector directory](index.md).
