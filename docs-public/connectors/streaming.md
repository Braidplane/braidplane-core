# Streaming files and SQL results

Use the [connector matrix](index.md) to choose a source and destination.
Native-file sources preserve bytes; SQL sources serialize one finite query into
a JSON file. Connect a file output contract directly to a file sink. SQL transforms
consume rows and cannot consume this opaque file.

## SQL exports

PostgreSQL, MySQL, BigQuery and Snowflake support `format: file`:

```json
{"type":"postgres","sql":"SELECT id, amount FROM orders ORDER BY id","format":"file"}
```

The file contains `column_names`, `rows` and `row_count`. Duplicate column names
and multiple result sets are refused. PostgreSQL/MySQL use a read-only
repeatable-read snapshot; BigQuery reads bounded pages from one SELECT job.
For Snowflake, select a read-only source role. Decimals stay exact strings;
consumers must preserve integer JSON precision.

For example, an operator can keep the default streamed row policy and explicitly
bound files and operation duration in TOML:

```toml
[execution_limits]
stream_max_rows = 0
max_result_rows = 1000
max_file_bytes = 134217728
query_timeout = "30s"
connector_timeout = "30s"

[execution]
request_timeout = "30s"
```

The matching environment variable is `EXECUTION_LIMITS_STREAM_MAX_ROWS`; the
server flag is `--execution-limits-stream-max-rows`. Flags override environment,
which overrides TOML. A hosted distribution can set a positive streamed quota.

## Native files

S3 and FTP read/write with `format: file`. Git, GitHub and GitLab support raw
file read/write on Linux workers; repository filters and line-ending conversion
are disabled and LFS pointers stay pointers. HTTP supports file responses and
retained request bodies. Google Workspace supports Drive file read/create/update
and Docs/Slides PDF export within its selected-file authorization.

## Operator controls

Streamed SQL files have no default row quota: `execution_limits.stream_max_rows=0`.
A positive value or saved-query `row_limit` rejects overflow rather than truncating
the file. Materialized rows and SQL transforms keep their own limits. New saved queries
and the editor default to `row_limit=0` (use operator policy); existing positive
saved limits remain active. Clear an old saved quota with `row_limit=0`.

The default per-file allowance is 128 MiB (`max_file_bytes`). The query,
connector and broker budgets default to 30 seconds. File bytes and deadlines must
be positive; zero is rejected. Operators can raise them for available storage
and throughput. The earliest caller, saved-operation, connector, broker or
private-network authorization deadline wins. Raising one budget does not override
another. These are Core resource controls; hosted distributions set explicit
policies of their own.

Sources stage complete files in encrypted 1 MiB chunks, so disk/WAL usage grows
with file size. SQL keeps one row at a time; `max_response_bytes` bounds a row,
metadata and each BigQuery page (also capped by `http_max_response_bytes`). S3 uses sequential 8 MiB multipart parts with
a 10,000-part bound (78.125 GiB). Provider limits also apply. Completed files wait
for expiry and maintenance cleanup; active concurrency does not bound that storage.

Cancellation releases source connections and execution slots. Partial source
files are never published as complete. A lost destination acknowledgement needs
independent destination inspection. Checkpoint replay cannot recover missing
private file references or resume a SQL snapshot.

After upgrading, an old `max_result_rows` setting continues to bound materialized
results. To keep that quota for SQL file exports, explicitly set `stream_max_rows`
to the old allowance. Upgrade API, worker and executor binaries together.
