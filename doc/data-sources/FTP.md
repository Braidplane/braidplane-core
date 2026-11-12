# FTP data source

## Purpose and best-fit use cases

Use the FTP connector to read from or write to files hosted on an FTP server. It is suited for file-based data exchange scenarios such as ingesting CSV exports from external systems, reading JSON snapshots, importing Excel workbooks, passing through opaque files, or writing pipeline output back to a shared file store.

## Transport security

Connections use explicit FTPS (AUTH TLS) with verified certificates for control and passive data channels. Servers must support FTPS and present a certificate trusted by the runtime. Raw FTP requires the explicit local development policy; SFTP remains a separate connector.
See the shared [transport encryption policy](../DATA_SOURCES.md#transport-encryption) for the local fixture exception.

## Shared capabilities

FTP supports the shared data-source lifecycle, parameterized file reads/writes,
and pipeline source/sink roles. See the [capability matrix](../DATA_SOURCES.md#capability-matrix)
and [payload compatibility matrix](../DATA_SOURCES.md#pipeline-payload-compatibility-matrix)
for the canonical cross-connector contract.

## Required config fields

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `host` | yes | string | None | Hostname or IP of the FTP server |
| `port` | yes | integer | 21 | 1 to 65535 |
| `base_path` | no | string | None | Base directory for file listing and relative paths |
| `connect_timeout` | no | integer | 5 | Connection and TLS/login timeout in seconds (1 to 300); later transfers are not capped by this setting |
| `allow_private_hosts` | no | boolean | `false` | Requires private host access to be enabled for the deployment; hosted/default deployments reject `true` |

Operation cancellation and deadlines interrupt control commands, passive data
dials, transfers and final replies. Operations sharing a connection run one at a
time. Canceling an active operation closes that connection; the next operation
needs a new one. A read or existence probe with a failed final reply also closes
its connection. Completed operations can reuse their connection. Canceling a
write does not undo bytes already sent, so check the remote file before retrying.

## Credentials

| Field | Required | Notes |
|---|:---:|---|
| `username` | yes | FTP user |
| `password` | yes | FTP password |

Credentials are encrypted at rest and never returned in API responses. See [DATA_SOURCES.md - Shared credential lifecycle](../DATA_SOURCES.md#shared-credential-lifecycle).

## Schema introspection

See the shared [file discovery contract](FILE_DISCOVERY.md) for object metadata,
raw-file entries, inference diagnostics, and import budgets.

Schema refresh/import queues a worker job that recursively discovers regular
files below `base_path` and samples them. Directory links and other non-regular
FTP entries are skipped. Discovery fails the import if it would descend beyond
32 directories. Normal data-source detail pages and `GET /schema` read cached
metadata only; they do not list or sample remote files.

Every discovered file is retained in the `FileSchemaResponse` result
(`schema_type: file`, `type: ftp`) with safe MIME type and size metadata when
known. CSV, JSON, and Excel workbook samples are used for field inference;
opaque files have no fields and raw bytes are never stored as schema. CSV field
names come from the header row and all inferred CSV fields use type `string`.
JSON fields retain supported structural types, and workbook fields come from
the selected first worksheet header behavior.

Unreadable files and samples that cannot be parsed remain as entries with no
fields and a warning diagnostic. Unsupported or opaque formats follow the same
retention rule. The import uses the shared schema item, byte, and time quotas;
the current defaults are 1,000 entries, 10 MiB of schema bytes, and a 30-second
connector timeout. Exceeding a listing quota fails the import and leaves the
previous schema cache unchanged rather than persisting a partial result.

## Query and operation model

A query defines a file operation:

**Read** (`operation: read`): retrieves the file at the given path and returns its contents as rows for `csv`, `json`, and `spreadsheet`, or as a native `file` payload for `format: file`. Format is detected from the file extension unless specified explicitly. CSV supports a configurable delimiter and an optional header row flag. Spreadsheet mode supports Excel workbook files, selects one worksheet, optionally crops to an A1 range, and normalizes the selected cells into rows.

**Write** (`operation: write`): for pipeline sinks, serializes the runtime-provided `input` payload to the file at the given path; direct (non-pipeline) saved queries may alternatively provide inline `content`. When reserved runtime `path` or `input` bindings are present they override the saved query's inline path/content values. CSV output includes a header row and preserves raw `input` bytes when the upstream payload is already CSV. JSON output accepts structured payloads only: row arrays and document payloads are written as JSON, while scalar and null content are rejected. Spreadsheet output accepts row-object arrays and writes an Excel workbook with a stable header order. Native file output uses `format: file` and writes bytes without CSV/JSON/spreadsheet parsing. In pipeline terms, CSV-to-JSON or CSV-to-spreadsheet requires an explicit transform instead of a hidden sink-side conversion. An `overwrite` flag controls whether the file is replaced or the operation fails if the file exists.

Supported operation fields include:

- `path` (required)
- `operation` (`read` or `write`, default is `read`)
- `format` (`auto`, `csv`, `json`, `file`, or `spreadsheet`)
- `delimiter` and `has_header` for CSV handling
- `sheet`, `range`, and `has_header` for spreadsheet handling
- `overwrite` for writes

The `{{param_name}}` template syntax is supported in file paths to inject dynamic values, including reserved runtime placeholders such as `{{runtime.run.date}}` and `{{runtime.run.datetime}}`. Spreadsheet `sheet` and `range` fields also support placeholders. Those runtime values are injected automatically for query execution and sink path overrides. `runtime.run.date` uses the current UTC date in `YYYY-MM-DD` format. `runtime.run.datetime` uses the current UTC timestamp in RFC 3339 format. Users do not define these values manually, and saved query parameters may not use the reserved `runtime.` prefix.

Spreadsheet behavior:

- Supported workbook extensions are `.xlam`, `.xlsm`, `.xlsx`, `.xltm`, and `.xltx`.
- Spreadsheet mode exists separately from CSV because it exposes workbook-native worksheet and A1 range controls while still returning pipeline `rows`.
- If `sheet` is omitted on read, the first workbook sheet is selected. Multi-sheet files are not merged.
- `range` uses A1 notation inside the selected sheet, such as `A1:D200`. Put the sheet name in `sheet`, not in the range.
- `has_header` can force header handling. When omitted, the connector treats a first row made entirely of numeric values as headless, creates synthetic `col_N` names, and includes the original cell values in `spreadsheet_row`.
- Empty cells inside the selected range are returned as empty strings.
- Formula cells depend on the spreadsheet library's stored cell values. Braidplane does not evaluate formulas.
- Write mode uses `sheet` for the target worksheet name and treats `range` as the top-left start cell.
- Write mode creates a new workbook from rows. It does not preserve existing macros, add-in behavior, template metadata, formatting, or charts.

Execution returns:
- `column_names`: field names from the file header (CSV/spreadsheet) or JSON keys
- `rows`: array of objects (field name → value)
- `row_count`: number of rows read
- `written_rows` and `written_bytes` for writes when the connector can report them
- `file`: safe metadata for native file reads/writes; raw bytes are omitted from API responses and persisted run diagnostics

## Pipeline relevance

**As a source**: the pipeline step uses `operation_type: ftp` with path, format, delimiter, `sheet`, `range`, and `has_header` in `operation_inline`. CSV, JSON, and spreadsheet file contents are normalized to rows before downstream transform steps see them; `format: file` produces a native file payload for pass-through sinks.

FTP source steps do not support incremental checkpoint contracts in the first
incremental slice. They are full-file reads during pipeline validation and
runtime execution. Pipeline validation rejects `watermark` and `cursor`
incremental contracts for `operation_type: ftp`.

**As a sink**: the pipeline step uses `operation_type: ftp`. Upstream payloads are serialized to the specified file path in CSV, JSON, or spreadsheet format, or written byte-for-byte when the sink operation uses `format: file`. CSV payloads stay raw for CSV targets. JSON and spreadsheet targets expect rows or structured JSON payloads; if the upstream payload is CSV, add an explicit transform before the sink.

## Security

Private IP ranges and loopback addresses are blocked by default to prevent SSRF. Hosted/default Braidplane rejects customer FTP data source payloads that set `allow_private_hosts: true`. The `self-hosted` profile rejects this broad switch and currently supports public destinations for this connector; local fixtures may opt into private hosts. See the [private-host policy](../DATA_SOURCES.md#private-host-access). Both control and passive data sockets dial a policy-vetted literal IP; passive-server host replies are never passed through as unresolved hostnames.

## Known limitations

- Pagination is not supported. File reads return the full file contents in a single execution.
- Schema introspection retains opaque files without fields. Failed samples
  include warning diagnostics.
- Explicit FTPS is required by default. SFTP is a separate, unimplemented connector.
- OpenDocument spreadsheets, workbook rendering, spreadsheet editing UI, multi-sheet merge behavior, formula evaluation, and preservation of existing workbook artifacts are not supported.

See [connector support levels](../../docs-public/connectors/support-levels.md) for available operations, connector status and provider requirements.
