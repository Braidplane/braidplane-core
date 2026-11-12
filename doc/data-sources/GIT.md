# Git data source

## Purpose and best-fit use cases

Use the Git connector to read and write files stored in a Git repository. It is suited for versioned file workflows such as consuming CSV/JSON snapshots from a repo, importing Excel workbooks, passing through opaque files, updating generated datasets, and emitting pipeline output into tracked files.

## Shared capabilities

Git supports the shared data-source lifecycle, parameterized repository file
reads/writes, and pipeline source/sink roles. See the [capability matrix](../DATA_SOURCES.md#capability-matrix)
and [payload compatibility matrix](../DATA_SOURCES.md#pipeline-payload-compatibility-matrix)
for the canonical cross-connector contract.

## Generic Git configuration

Generic Git is the URL-and-credentials escape hatch for self-hosted Git,
GitHub Enterprise, GitLab Self-Managed, deploy keys, and providers without a
native Braidplane onboarding flow.

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `repo_url` | yes | string | None | Git repository URL over HTTPS or SSH; plain HTTP is unsupported |
| `default_branch` | no | string | None | Branch used when operation-level branch is omitted |
| `base_path` | no | string | None | Base directory for listing files and resolving relative query paths |
| `allow_private_hosts` | no | boolean | `false` | Requires private host access to be enabled for the deployment; hosted/default deployments reject `true` |

### Generic Git credentials

| Field | Required | Notes |
|---|:---:|---|
| `auth_type` | yes | `https` or `ssh` |
| `username` | required for `https` | HTTPS username |
| `token` | required for `https` | HTTPS token or password |
| `ssh_private_key` | required for `ssh` | PEM/OpenSSH private key |
| `ssh_passphrase` | no | Passphrase for encrypted SSH private key |
| `ssh_known_hosts` | required for `ssh` | OpenSSH `known_hosts` entries used to verify the Git server host key |

Credentials are encrypted at rest and never returned in API responses. See [DATA_SOURCES.md - Shared credential lifecycle](../DATA_SOURCES.md#shared-credential-lifecycle).

## Managed provider connections

Open **Organization → Connections** to manage repository authorization. Managed
GitHub sources reuse the organization's verified GitHub App installation.
Select one bound repository when creating a source; Braidplane stores its stable
identity and resolves a repository-scoped installation token only when Git runs.

Managed GitLab sources reuse the organization's OAuth connection. Connect the
account first, then select a verified repository for each source. The source
stores the stable GitLab project ID and safe display metadata; GitLab resolves
the canonical HTTPS clone URL at runtime. Selecting a repository does not make
the shared OAuth grant project-scoped. Read-only grants request `read_user`,
`read_api`, and `read_repository`; write authorization requests `read_user`,
`api`, and `write_repository`. Each source's read-only restriction still applies.
Reconnection affects explicitly attached sources, which must pass a new
connection test. Disconnection prevents their repository operations and new
Proof assessments. Organization management permission is required to change the
shared grant. Data-source permission alone cannot rotate or revoke it.

Managed forms accept repository selection and source settings. Use Generic Git
for manual HTTPS credentials, project access tokens, PATs, and SSH keys. Existing
manual GitHub/GitLab sources are migrated to Generic Git in place, preserving
source IDs, references, credential bytes, and read-only restrictions. Existing
source-specific GitLab OAuth grants are not silently combined: use **Use
organization GitLab connection** on the source after authorizing the shared
connection. Attachment verifies repository access and rejects stale repository
or credential selections before changing ownership.

The CLI uses the same managed repository selection: after connecting the provider
in organization settings, use `data-source create gitlab --project-id <id>` or
`data-source create github --repository-id <id>` with the source name and settings.
JSON request files use `config.project_id` or `config.repository_id` and omit
`credentials`. `data-source provider onboard gitlab` directs users to organization
setup; source-level grant commands cannot replace the shared connection.

Proof starts disabled for new sources. Its assessment and GitHub publication
controls are separate from repository authorization; live connector evidence
requires separate environment authorization. See Proof configuration.

Plain `http://` repository URLs are not supported. Use `https://`, `ssh://`, or SCP-style SSH syntax such as `git@github.com:org/repo.git`. SSH connections require a trusted host key entry; the connector does not disable host key verification.

## Migration inventory and recovery

Before applying migration 39, inventory affected sources with a read-only query.
It returns source identity and credential kind, never credential material:

```sql
SELECT ds.organization_id, ds.slug, ds.type, c.kind
FROM data_sources ds
JOIN credentials c ON c.organization_id = ds.organization_id AND c.id = ds.credential_id
WHERE ds.type IN ('github', 'gitlab')
ORDER BY ds.organization_id, ds.slug;
```

Static credentials with a repository URL (or a GitLab repository web URL) move
to Generic Git. Managed GitHub installation records and GitLab OAuth grants
retain their provider type. Review any static record without either URL before
rollout: the migration cannot infer its repository. Keep the deployment's normal
database backup and validate affected sources after migration. Repair unexpected
records through a forward migration; do not rewrite migration 39 or revert
credential storage. Historical Proof receipts remain available even when a
source's targets are retired. Local migration tests are not a hosted inventory
or evidence that the deployment has been migrated.

## Runtime dependencies

Git data sources execute raw Git operations with the runtime `git` command.
HTTPS remotes also require Git's HTTPS remote helper, CA certificates, and a
POSIX shell for the in-memory credential helper. SSH remotes require the runtime `ssh` client,
a POSIX shell for passphrase askpass helpers, a writable runtime home, and
passwd data for the process UID. Validate these dependencies in the release
image before tagging or pushing it. This core repository does not define a
staging-image validation task.

## Branch discovery

Git-compatible data sources expose remote branch discovery for callers that need repository branch choices.

- The connector reads remote heads and returns branch records with explicit fields such as `name` and `is_default`.
- When the remote advertises a default branch, the connector marks that branch with `is_default: true` and may include `default_branch` on the response.
- When the remote does not expose HEAD metadata, the connector does not set `is_default`; it returns the configured `default_branch` value when one is available.
- Branch-list failures are surfaced through defined diagnostics such as an `error` or `diagnostics` object so callers can present connection problems without silently hiding the remote issue.

## Pipeline repository onboarding

Project settings support importing an existing repository and the resumable
repository-setup flow for private GitHub or GitLab creation. Repository setup
requires the operator/user's provider consent, creates a managed data source,
and attaches the exact created repository to the project's canonical Git backend.
GitHub creation requires a user session or personal access token; an organization
API key cannot consent for a person. GitLab setup can resume with an organization
API key after the organization connection has been authorized. Generic Git can
adopt an existing repository and does not create a provider repository.

A managed GitHub pipeline backend requires the App installation's `Contents:
read/write` grant for the selected repository, including when importing an
existing repository. Validation uses the permissions of its freshly minted
installation token and checks the selected branch without writing to it.
A connection test can pass with a read-only grant. Pipeline backend writes
require read/write access and must follow repository selection and branch rules.
Metadata-only Proof can use a read-only Contents grant.

The created repository contains only a template-version marker and a README in
the selected pipeline directory. Connector credentials, environment bindings,
production identifiers, provider tokens, and raw data stay in Braidplane. The
project records the canonical repository, connection, branch, directory, and
first validated commit. Braidplane-owned sample repositories and sample
environments remain demo fixtures; they are not attached as a persistent
customer backend and their Proof does not describe customer systems.

## Schema introspection

See the shared [file discovery contract](FILE_DISCOVERY.md) for object metadata,
raw-file entries, inference diagnostics, and import budgets.

Schema refresh/import queues a worker job that performs a shallow clone, then
recursively discovers regular files under `base_path`. Git skips symbolic links
and the `.git` directory. Discovery fails the import if it would descend beyond
32 directories. Normal data-source detail pages and `GET /schema` read cached
metadata only; they do not clone the repository.

Every discovered file is retained in the `FileSchemaResponse` result
(`schema_type: file`, `type: git`). The entry includes safe MIME type and size
metadata when known. CSV, JSON, and Excel workbook samples are used for field
inference; opaque files have no fields and raw bytes are never stored as schema.
CSV field names come from the header row and all inferred CSV fields use type
`string`. JSON fields retain supported structural types, and workbook fields
come from the selected first worksheet header behavior.

Unreadable files and samples that cannot be parsed remain as entries with no
fields and a warning diagnostic. Unsupported or opaque formats follow the same
retention rule. The import uses the shared schema item, byte, and time quotas;
the current defaults are 1,000 entries, 10 MiB of schema bytes, and a 30-second
connector timeout. Exceeding a listing quota fails the import and leaves the
previous schema cache unchanged rather than persisting a partial result.

## Query and operation model

A query defines a file operation:

**Read** (`operation: read`): reads the file at `path` and returns rows for `csv`, `json`, and `spreadsheet`, or a native file payload for `format: file`. Supported formats: `csv`, `json`, `file`, `spreadsheet`, or `auto` (detected from extension). Spreadsheet mode supports Excel workbook files, selects one worksheet, optionally crops to an A1 range, and normalizes the selected cells into rows.

**Write** (`operation: write`): for pipeline sinks, serializes the runtime-provided `input` payload into `path`; direct (non-pipeline) saved queries may alternatively provide inline `content`. The write commits the change in a shallow clone and pushes to the configured remote repository. When reserved runtime `path` or `input` bindings are present they override the saved query's inline path/content values. Non-empty runtime `branch` and `message` overrides also replace the saved query values when provided; empty values fall back to the saved query or connector defaults. CSV-format writes preserve raw `input` bytes when the upstream payload is already CSV. JSON-format writes accept structured payloads only: row arrays and document payloads are written as JSON, while scalar and null content are rejected. Spreadsheet writes accept row-object arrays and write an Excel workbook with a stable header order. Native file writes use `format: file` and preserve raw bytes without CSV/JSON/spreadsheet parsing. In pipeline terms, that means CSV-to-JSON or CSV-to-spreadsheet requires an explicit transform instead of a hidden sink-side conversion.

Explicit `format: file` operations preserve stored Git blob bytes through reads,
commits and pushes. Their private checkout disables line-ending, ident,
working-tree encoding and clean/smudge conversions, including required filters.
Only the selected file is checked out; commits preserve other tracked files.
With a positive native-file allowance, an oversized existing blob fails before
checkout. Git metadata, directory and symbolic-link targets are refused.
Git LFS pointer files are transferred as pointers; LFS object download is not
part of this operation. Structured formats retain repository attribute behavior.
See [native-file streaming](../NATIVE_FILE_STREAMING.md) for storage and resource
limits. Retained streaming requires Linux workers with finite transfer deadlines
and process resource limits. macOS workers refuse that path. Ordinary small-file
operations keep their existing response limits. Large delta objects or repository
metadata can exceed the Git process or allocation limits.

When the Git, GitHub, or GitLab data source has `read_only: true`, Braidplane allows direct and pipeline `read` operations but blocks direct saved-query writes, direct inline writes, incremental direct writes, and pipeline sink writes before connector dispatch. Unknown Git file operations fail closed for read-only data sources.

Supported operation fields include:
- `path` (required)
- `operation` (`read` or `write`, default is `read`)
- `format` (`auto`, `csv`, `json`, `file`, or `spreadsheet`)
- `delimiter` and `has_header` for CSV handling
- `sheet`, `range`, and `has_header` for spreadsheet handling
- `branch` (optional override of connector default branch)
- `message` (optional commit message for writes)

The `{{param_name}}` template syntax is supported in `path`, including reserved runtime placeholders such as `{{runtime.run.date}}` and `{{runtime.run.datetime}}`. Spreadsheet `sheet` and `range` fields also support placeholders. Those runtime values are injected automatically for query execution and sink path overrides. `runtime.run.date` uses the current UTC date in `YYYY-MM-DD` format. `runtime.run.datetime` uses the current UTC timestamp in RFC 3339 format. Users do not define these values manually, and saved query parameters may not use the reserved `runtime.` prefix.

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

Pipeline sinks supply write payload content through the reserved runtime `input` binding. Direct write queries may still provide inline `content`, but runtime sink bindings override inline content when present.

Execution returns:
- `column_names`: derived from CSV/spreadsheet headers or JSON object keys
- `rows`: array of objects (field name -> value)
- `row_count`: number of rows read
- `written_rows` and `written_bytes` for writes when the connector can report them
- `file`: safe metadata for native file reads/writes; raw bytes are omitted from API responses and persisted run diagnostics

## Pipeline relevance

**As a source**: the pipeline step uses `operation_type: git` with read operation details in `operation_inline`. CSV, JSON, and spreadsheet file contents are normalized to rows before downstream transform steps see them; `format: file` produces a native file payload for pass-through sinks.

Git source steps do not support incremental checkpoint contracts in the first
incremental slice. They are full-file reads during pipeline validation and
runtime execution. Pipeline validation rejects `watermark` and `cursor`
incremental contracts for `operation_type: git`, including GitHub and GitLab
data sources that share the Git runtime operation type.

**As a sink**: the pipeline step uses `operation_type: git`. Upstream payloads are serialized to CSV, JSON, or spreadsheet format and written to the target repository path, or written byte-for-byte when the sink operation uses `format: file`. CSV payloads stay raw for CSV targets. JSON and spreadsheet targets expect rows or structured JSON payloads; if the upstream payload is CSV, add an explicit transform before the sink.

## Security

Private IP ranges and loopback addresses are blocked by default to reduce SSRF risk. Hosted/default Braidplane rejects customer Git, GitHub, and GitLab data source payloads that set `allow_private_hosts: true`. The `self-hosted` profile rejects this broad switch and currently supports public destinations for this connector; local fixtures may opt into private hosts. See the [private-host policy](../DATA_SOURCES.md#private-host-access). Even for internal hosts, Git repository URLs must use HTTPS or SSH rather than plain HTTP. Native Git HTTPS uses a vetted-IP curl resolve entry while retaining the configured hostname for TLS; it fails closed unless Git is at least 2.37 and reports libcurl 7.57.0 or newer. GitLab.com browser-style HTTPS repository URLs are canonicalized to their direct `.git` endpoint before pinning. SSH uses the vetted IP as `HostName` and the configured hostname as its known-host identity. HTTPS credentials are supplied by a Git credential helper from the child process environment, so no executable helper is created under the runtime's no-exec temporary directory and credentials are not embedded in repository arguments. Redirects and inherited URL rewrite rules are rejected to prevent a hidden effective destination change.

Repository contents and operation paths are tenant-controlled. Read, write, delete, existence checks, and schema file listing accept repository-relative paths only. Git file reads are bounded before content is loaded into memory. Diagnostics do not disclose resolved host paths or file bytes.

Read and write paths, including runtime overrides and substituted parameters,
resolve beneath `base_path` (the repository root when omitted). Absolute paths
and `..` segments are rejected in both the configured base and operation path.
Slash and backslash separators follow the same validation rules.

## Known limitations

- Pagination is not supported.
- CSV, JSON, spreadsheet, and explicit native file reads/writes are supported. Introspection remains schema-oriented and does not infer fields from opaque files.
- OpenDocument spreadsheets, workbook rendering, spreadsheet editing UI, multi-sheet merge behavior, formula evaluation, and preservation of existing workbook artifacts are not supported.
- The connector is stateless and uses shallow clone per operation, which can increase overhead for high-frequency workloads.
- Introspection recursively lists regular files under the configured base path;
  opaque files remain without fields, and failed samples include warnings.

See [connector support levels](../../docs-public/connectors/support-levels.md) for available operations, connector status and provider requirements.
