# GitLab

For pipeline changes in a managed GitLab repository, optional
[Proof assessments](../getting-started/proof.md) publish informational MR notes
with exact-head results and protected receipt links. These are separate from
repository access and are not GitLab CI statuses or merge gates.

Read and write repository data through a managed GitLab connection. Select
one project after authentication; use its stable project identity for the
connection and the shared Git operation format for files.

Use the [shared connection workflow](connect-a-system.md) to create this data
source in your workspace. You can read this guide without an account or
provider resources. Running examples requires provider resources appropriate for your evaluation.

## Connect and authenticate

1. Choose GitLab when creating a data source.
2. Authorize the managed OAuth connection in the intended GitLab account.
3. Select one project visible through that authorization.
4. Choose the default branch and optional base path, test, save, and refresh.

Read-only OAuth grants request `read_user`, `read_api`, and `read_repository`.
Writable grants request `read_user`, `api`, and `write_repository`. Choosing a
project binds the Braidplane data source, but does not restrict the user-scoped
OAuth token to that project. Provider access and protected-branch rules still
apply. Reauthorization may be needed when changing requested access.

For strict project isolation, the advanced project-access-token flow accepts a
numeric project ID and a token belonging to that project. Braidplane verifies
the project identity before saving it. Use read or write permissions appropriate
to the intended operations; token availability depends on the GitLab account.

| Setting | Meaning |
|---|---|
| Authentication | Managed OAuth, or advanced project access token |
| Project | One verified stable project ID |
| `default_branch` | Branch used when an operation omits it |
| `base_path` | Optional scope for discovery and relative file paths |

Managed GitLab setup does not require a repository URL, generic HTTPS
credentials, or SSH key. Use [Generic Git](git.md) for manually authenticated
or self-managed URLs. Credentials remain encrypted and are not returned by the API.

## Test and discover

Test repository access and discover its branches. Refresh clones the repository
and recursively discovers regular files under the base path. It skips symlinks
and `.git`. Normal schema views use the cached result. Discovery keeps safe
metadata and warnings for unreadable or unsupported samples; a listing quota
failure leaves the previous cache unchanged. An explicit file operation can
still be valid when discovery reports a warning.

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
  "type": "git",
  "operation": "read",
  "path": "customers.csv",
  "format": "csv",
  "has_header": true,
  "branch": "main"
}
```

## Write example

This direct write example stores one synthetic record as JSON. It commits
and pushes to `main`; choose a writable test branch before running it. For a pipeline
sink, upstream input replaces the inline content; do not rely on inline sample
content being written when the pipeline supplies rows.

```json
{
  "type": "git",
  "operation": "write",
  "path": "exports/customers.json",
  "format": "json",
  "content": [
    {
      "id": 1,
      "email": "alex@example.com"
    }
  ],
  "branch": "main",
  "message": "Export synthetic customers"
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
| `branch` | Optional branch/ref override; writes need a writable branch |
| `message` | Commit message for a write |

Use `{{parameter_name}}` in supported paths and worksheet/range fields. Runtime
values such as `{{runtime.run.date}}` use UTC and are supplied by Braidplane;
do not declare your own query parameters under the reserved `runtime.` prefix.

## Pipeline and format behavior

Use `operation_type: git`. Structured reads normalize CSV, JSON, and Excel
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
  "type": "git",
  "operation": "read",
  "path": "customers.xlsx",
  "format": "spreadsheet",
  "sheet": "Customers",
  "range": "A1:B20",
  "has_header": true,
  "branch": "main"
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

## Repository data and pipeline definitions

Reading CSV/JSON files as data and using Git to distribute pipeline definitions
are separate uses of the connection. Project settings can import an existing
pipeline repository with an explicit branch and directory. This connector’s
project import flow does not create a new repository.

## Limits and troubleshooting

- No result pagination or incremental checkpoints.
- Repository operations use shallow clones, which adds overhead to frequent reads.
- Paths must be repository-relative; absolute paths and traversal are rejected.
- Missing repository/branch: check the selected identity, provider access, and
  branch discovery. Empty schema: inspect the base path and supported file types.
- Push rejection: check read-only mode, contents-write permission, and branch
  protection. File writes push directly; they do not automatically open a PR.
- Refresh metadata after repository files change. A successful clone does not
  guarantee that a push is allowed.

See [operation and pipeline basics](connect-a-system.md#use-the-examples) for
where to use these statements, parameters, environment bindings, and replay
safety. Return to the [connector directory](index.md).
