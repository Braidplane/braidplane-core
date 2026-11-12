# SQL result streaming

PostgreSQL, MySQL, BigQuery and Snowflake statements can select `format: file`
to serialize one finite query as a retained JSON object containing `column_names`, `rows` and `row_count`.
The default `rows` format retains its existing behavior. File output passes
through the native-file broker and sink path; it does not expose retained
references in public JSON or replay checkpoints.

PostgreSQL and MySQL use a read-only repeatable-read transaction. BigQuery
validates a single SELECT with a dry run and reads bounded pages from one job.
Snowflake streams one driver result using the source account's permissions;
use a read-only role because file output does not add a database permission boundary.
Duplicate column names and multiple result sets are refused. Existing connector
normalization applies: exact decimals remain strings, SQL NULL becomes JSON null,
and integer JSON numbers preserve their digits. Consumers must preserve JSON
number precision when decoding. PostgreSQL bytea uses base64; MySQL and Snowflake
retain their existing byte-to-string conversion.

Configure positive `execution_limits.max_result_rows`, `max_file_bytes` and
`max_response_bytes`. The file limit bounds the entire serialized object. The
response limit bounds each serialized row and result metadata independently,
and each BigQuery provider page. Rows are scanned and encoded one at a time. Driver allocations still depend on
the largest source row. Connector and request deadlines bound each operation;
the caller's run deadline bounds the complete transfer. Existing tenant-scoped
per-data-source queues and worker concurrency remain in force. This path adds
no organization-wide commercial allowance policy.
Operator-authorized private sources retain their existing shorter authorization
deadline.

Use a file output contract and connect it directly to a native-file sink with
`format: file`. Row transforms and row contracts do not consume this opaque
file. Saved queries require an explicit file output contract. This path does
not implement independently paginated queries, CDC, offset resume or incremental
backfill. BigQuery pagination stays within the original query job.

The complete result is staged in encrypted PostgreSQL chunks before destination
writing. Storage grows with serialized result size, including completed results
awaiting expiry, WAL and backup overhead. Native-file retention, claim scoping,
cleanup, sink acknowledgement and replay restrictions apply; see
[native-file streaming](NATIVE_FILE_STREAMING.md). An interrupted source is not
a resumable snapshot. Uncertain sink writes require destination inspection.

## Recovery boundaries

| Interruption | Behavior |
|---|---|
| SQL scanning or serialization fails | No reference is published. Ordinary failure discards unsealed input. A new run starts a new snapshot. |
| SQL execution is cancelled | Database rows and connector slots are released; BigQuery job cancellation is attempted. Cancellation invalidates the storage claim; partial chunks can remain unreadable until expiry and maintenance cleanup. |
| Source worker is lost | No offset resume or continuation of the lost snapshot is supported. Claim fencing and expiry apply to incomplete input. |
| Destination write is partial or its acknowledgement is lost | Native-file sink guards apply. Missing acknowledgement is not success and cannot authorize automatic replay. Inspect the destination and clean provider remnants before recovery. |
| A checkpoint is replayed | Public checkpoints retain metadata, not a reusable private file reference. SQL file replay is refused before source reread or sink dispatch. |
