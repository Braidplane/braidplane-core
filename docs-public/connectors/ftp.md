# FTP

Read and write files on an FTPS server. Use CSV, JSON, or spreadsheet mode
for tabular records, or raw-file mode to preserve bytes.

Use the [shared connection workflow](connect-a-system.md) to create this data
source in your workspace. You can read this guide without an account or
provider resources. Running examples requires provider resources appropriate for your evaluation.

## Connection settings

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `host` | yes | string | — | Hostname or IP of the FTP server |
| `port` | yes | integer | 21 | 1–65535 |
| `base_path` | no | string | — | Base directory for file listing and relative paths |
| `connect_timeout` | no | integer | 5 | Connection and TLS/login timeout in seconds (1–300); later transfers are not capped by this setting |
| `allow_private_hosts` | no | boolean | `false` | Requires private host access to be enabled for the deployment; hosted/default deployments reject `true` |

## Authentication and permissions

| Field | Required | Notes |
|---|:---:|---|
| `username` | yes | FTP user |
| `password` | yes | FTP password |

Credentials belong in the connection, never in an operation statement. They are
encrypted at rest and are not returned in API responses. Hosted/default
deployments block loopback and private network endpoints; your laptop’s
`localhost` is not the hosted worker’s network.

Use a read-restricted FTP account for sources and a separately scoped writer
for destinations. Explicit FTPS (AUTH TLS) protects both the control and passive
data channels. The server certificate must be trusted by the runtime and match
the configured hostname. Raw FTP is available only under an explicit local
development deployment policy. SFTP is unsupported.

## Test and discover

Test server access and refresh metadata to list and sample files under the
base path. CSV headers, JSON fields, and workbook samples provide field names.
Unreadable files may be omitted.

## Read example

Place a synthetic `customers.csv` file in the configured base path or prefix:

```csv
id,email
1,alex@example.com
```

This read returns one row with `id` and `email`. Adapt the path and, for Git,
the branch to your connection.

```json
{
  "type": "ftp",
  "operation": "read",
  "path": "customers.csv",
  "format": "csv",
  "has_header": true
}
```

## Write example

This direct write example stores one synthetic record as JSON. With
`overwrite: false`, an existing target causes the write to fail. For a pipeline
sink, upstream input replaces the inline content; do not rely on inline sample
content being written when the pipeline supplies rows.

```json
{
  "type": "ftp",
  "operation": "write",
  "path": "exports/customers.json",
  "format": "json",
  "content": [
    {
      "id": 1,
      "email": "alex@example.com"
    }
  ],
  "overwrite": false
}
```

## Operation reference

| Field | Meaning |
|---|---|
| `type` | Connector operation discriminator shown in the examples |
| `operation` | `read` or `write` |
| `path` | Relative file path or object key |
| `format` | `auto`, `csv`, `json`, `spreadsheet`, or `file` |
| `delimiter`, `has_header` | CSV parsing controls |
| `sheet`, `range`, `has_header` | Worksheet and A1 range controls |
| `content` | Direct write payload; pipeline input takes precedence |
| `overwrite` | Whether an existing file may be replaced; choose explicitly |

Use `{{parameter_name}}` in supported paths and worksheet/range fields. Runtime
values such as `{{runtime.run.date}}` use UTC and are supplied by Braidplane;
do not declare your own query parameters under the reserved `runtime.` prefix.

## Pipeline and format behavior

Use `operation_type: ftp`. Structured reads normalize CSV, JSON, and Excel
workbooks to rows. `format: file` preserves opaque bytes for pass-through sinks.
Sink operations receive upstream `input`; it takes precedence over saved inline
content. CSV input stays raw for CSV output. JSON and spreadsheet output require
structured input, so CSV-to-JSON or CSV-to-spreadsheet needs an explicit transform.

Reads are bounded full-file/full-object operations with no watermark or cursor
checkpoints. Results expose row counts and fields for structured reads, write
counts where available, and safe file metadata. Raw bytes are not retained in
historical run diagnostics for later download.

### Spreadsheet example

Given an Excel workbook with a sheet named `Customers` and header cells
`id,email`, this operation reads the selected table as rows:

```json
{
  "type": "ftp",
  "operation": "read",
  "path": "customers.xlsx",
  "format": "spreadsheet",
  "sheet": "Customers",
  "range": "A1:B20",
  "has_header": true
}
```

Supported workbook extensions are `.xlam`, `.xlsm`, `.xlsx`, `.xltm`, and `.xltx`.
When `sheet` is omitted, the first worksheet is used; worksheets are not merged.
`range` belongs to that sheet and does not include its name. Explicit
`has_header: true` makes the first selected row the column names. Without an
explicit choice, an all-numeric first row is treated as headless with `col_N`
column names. Empty selected cells become empty strings.

For writes, `range` is the top-left starting cell. Braidplane creates a new
workbook; it does not preserve existing formatting, charts, macros, or template
metadata and does not evaluate formulas. OpenDocument workbooks are unsupported.

## Limits and troubleshooting

- No pagination, incremental checkpoints, implicit FTPS, or SFTP.
- Missing files: check the base path, exact name, and FTP account permissions.
- Connection success but transfer failure: verify passive data connectivity
  and server permissions, not just the control port.
- Existing target: review overwrite behavior before repeating the write.

See [operation and pipeline basics](connect-a-system.md#use-the-examples) for
where to use these statements, parameters, environment bindings, and replay
safety. Return to the [connector directory](index.md).
