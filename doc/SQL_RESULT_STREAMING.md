# SQL result streaming

PostgreSQL statements can select `format: file` to serialize one finite query
as a retained JSON object containing `column_names`, `rows` and `row_count`.
The default `rows` format retains its existing behavior. File output passes
through the native-file broker and sink path; it does not expose retained
references in public JSON or replay checkpoints.

The query runs in one read-only repeatable-read transaction. Duplicate column
names and multiple result sets are refused. Integer JSON numbers preserve their
digits; exact PostgreSQL numerics remain strings, bytea uses base64, timestamps
use RFC 3339, and SQL NULL becomes JSON null. Consumers must preserve JSON number
precision when decoding. Existing PostgreSQL driver normalization applies.

Configure positive `execution_limits.max_result_rows`, `max_file_bytes` and
`max_response_bytes`. The file limit bounds the entire serialized object. The
response limit bounds each serialized row and result metadata independently;
rows are scanned and encoded one at a time. Driver allocations still depend on
the largest source row. Connector and request deadlines bound each operation;
the caller's run deadline bounds the complete transfer. Existing tenant-scoped
per-data-source queues and worker concurrency remain in force. This path adds
no organization-wide commercial allowance policy.
Operator-authorized private sources retain their existing shorter authorization
deadline.

Use a file output contract and connect it directly to a native-file sink with
`format: file`. Row transforms and row contracts do not consume this opaque
file. Saved queries require an explicit file output contract. This path does
not implement pagination, CDC, offset resume or incremental backfill.

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
| SQL execution is cancelled | The transaction and connector slot are released. Cancellation invalidates the storage claim; partial chunks can remain unreadable until expiry and maintenance cleanup. |
| Source worker is lost | No offset resume or continuation of the lost snapshot is supported. Claim fencing and expiry apply to incomplete input. |
| Destination write is partial or its acknowledgement is lost | Native-file sink guards apply. Missing acknowledgement is not success and cannot authorize automatic replay. Inspect the destination and clean provider remnants before recovery. |
| A checkpoint is replayed | Public checkpoints retain metadata, not a reusable private file reference. SQL file replay is refused before source reread or sink dispatch. |

## Local qualification

The [recorded profiles](qualification/sql-streaming.json) transferred the frozen
synthetic business records through credentials, the encrypted broker, PostgreSQL
chunks, the pipeline runner, the sink ledger and one S3-compatible object. An
independent streaming oracle verified every ID, integer type, amount, payload,
record count and complete-object SHA-256. SQL generated the same frozen records
directly; these runs do not replace the historical comparison or measure its
physical source tables.

With disposable Docker fixtures available, run each profile in a separate test
process, using `100`, `100000`, or `1000000`:

```sh
BP_SQL_STREAM_QUALIFICATION_ROWS=1000000 bash scripts/go-toolchain.sh test ./service/pipelinesvc/e2e -run '^TestNativeFileStreaming$/^SQL_snapshot_crosses_retained_broker_and_S3_sink$' -count=1 -v
```

| Records | Serialized bytes | Execution and verification | Sampled heap peak | Process RSS peak |
|---|---:|---:|---:|---:|
| 100 | 107,264 | 0.290 s | 59.2 MB | 114.4 MB |
| 100,000 | 107,777,868 | 2.890 s | 73.7 MB | 149.7 MB |
| 1,000,000 | 1,078,777,880 | 29.685 s | 76.0 MB | 143.1 MB |

Memory figures cover the application and bounded verifier process, including
fixture initialization. They exclude PostgreSQL/S3 processes, database/WAL
storage and kernel caches. These measurements demonstrate bounded application
memory for the recorded profiles, not a universal memory cap or hosted capacity.

Focused PostgreSQL tests verify exact large integers, decimals, binary values,
nulls, timestamps, a concurrent-update snapshot, read-only write-CTE refusal and
connection release after cancellation. Pipeline tests cover row/byte boundaries,
prior-object preservation, refused replay, cancellation after persisted chunks,
slot release and expiry cleanup. The cleanup test advances only its synthetic
retained-file expiry before invoking real maintenance cleanup.

Shared native-file fault tests cover slow destinations, interrupted writes,
lost completion acknowledgement and executor loss without redispatch. They
qualify the shared transport and sink behavior; they do not establish resuming a
SQL snapshot after a source process is killed. That recovery remains unsupported.
