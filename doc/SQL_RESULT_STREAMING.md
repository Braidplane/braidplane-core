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

Configure `execution_limits.stream_max_rows` (`0` by default: no row quota),
positive `max_file_bytes` and positive `max_response_bytes`.
Materialized results retain their separate `max_result_rows` limit. The file limit bounds the entire serialized object. The
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

## Limits through the transfer path

| Stage | Effective protection |
|---|---|
| Saved-query API/editor | `row_limit=0` defaults to operator policy and round-trips as zero. Positive quotas accept 1–10,000 (HubSpot editor: 200); saved `timeout_seconds` accepts 1–300 seconds, defaults to 30. Inline pipeline operations can use longer positive operator budgets. |
| Query service | Positive saved `row_limit` caps streamed rows; otherwise `stream_max_rows` (0 means no quota). Materialized results use `max_result_rows`. Earliest query/connector/saved/caller deadline wins. |
| PostgreSQL/MySQL/Snowflake serialization | One row at a time. `max_response_bytes` bounds row and column metadata; drivers may allocate the largest field before validation. `stream_max_rows` rejects overflow without publishing a partial file. |
| BigQuery | One original SELECT job, pages requested at at most 1,000 rows even with no quota. Each page is bounded by the smaller positive `max_response_bytes` and `http_max_response_bytes`; provider timeout and billed-byte limits also apply. |
| File serializer and encrypted retained storage | `max_file_bytes` bounds the whole JSON object. Storage uses at most 1 MiB plaintext chunks. Format maximum is `(2^31-1) * 1 MiB` bytes, independent of available disk. |
| Broker | `execution.request_timeout` is an additional finite deadline. Envelopes carry metadata and private references, not full file bytes; `max_response_bytes` bounds these envelopes. Claims remain organization-scoped. |
| S3 sink | Opens the complete retained reference under `max_file_bytes`, uploads sequential 8 MiB parts, at most 10,000 (78.125 GiB), and checks exact length/SHA-256 before completion. No materialized row cap is reapplied. |
| Cleanup | Retained files expire 15 minutes after their source request deadline; maintenance runs every minute. Cancellation releases execution slots but can leave unreadable chunks until expiry. |

Other native destinations have their own concrete provider/platform bounds; see
[native-file streaming](NATIVE_FILE_STREAMING.md). Startup logs expose loaded
settings. Limit diagnostics name `execution_limits.stream_max_rows`,
`execution_limits.max_file_bytes` or `execution_limits.max_response_bytes`.
See [configuration and upgrade guidance](../CONFIG.md#streamed-transfer-policy-and-upgrades)
for defaults, precedence and preserving an existing row quota.

## Recovery boundaries

| Interruption | Behavior |
|---|---|
| SQL scanning or serialization fails | No reference is published. Ordinary failure discards unsealed input. A new run starts a new snapshot. |
| SQL execution is cancelled | Database rows and connector slots are released; BigQuery job cancellation is attempted. Cancellation invalidates the storage claim; partial chunks can remain unreadable until expiry and maintenance cleanup. |
| Source worker is lost | No offset resume or continuation of the lost snapshot is supported. Claim fencing and expiry apply to incomplete input. |
| Destination write is partial or its acknowledgement is lost | Native-file sink guards apply. Missing acknowledgement is not success and cannot authorize automatic replay. Inspect the destination and clean provider remnants before recovery. |
| A checkpoint is replayed | Public checkpoints retain metadata, not a reusable private file reference. SQL file replay is refused before source reread or sink dispatch. |

## Independent destination verification

See the [comparison verifier guide](../comparison/README.md) for standalone
setup, destination-object verification and synthetic regression checks.

## Local operator-policy acceptance (17 November 2026)

The #147 working tree based on `93dbeb660a1b43212679c56637900a48dcfeae18`
passed the package-owned PostgreSQL/S3 fixture test:

```sh
bash scripts/go-toolchain.sh test ./service/pipelinesvc/e2e \
  -run '^TestNativeFileStreaming$' -timeout 5m -v
```

Effective SQL settings were `stream_max_rows=0`, `max_result_rows=1000`,
`max_file_bytes=134217728`, `max_response_bytes=1048576`, `query_timeout=30s`
and `connector_timeout=30s`. The shared fault-test broker used a 5-minute request
budget; the S3 connection used 300 seconds. The complete run had a 3-minute
caller budget. These longer fixture budgets do not establish a requirement to
raise production defaults.

Independent AWS SDK readback decoded every one of the **1,000,000** destination
records, checking ordered IDs, amounts, payloads, envelope counts and EOF.
The object contained **86,777,880 bytes**, matching 83 retained plaintext chunks
and SHA-256 `0adfe994282020d04fb1333f727f27462d6f0adfe282ee1cc94e2863eab55ac4`.
The same suite passed configured row/byte boundaries, prior-output preservation,
SQL cancellation and admission release, multipart cancellation, lost completion
acknowledgement and executor-loss fencing. This is local synthetic acceptance;
it does not establish hosted CI, publication, deployment or live provider capacity.
