# Native-file streaming

Native file operations stream through encrypted retained storage. They use the
ordinary query, connector, broker and provider deadlines and the existing
per-organization/data-source concurrency queue. No organization profile or
separate transfer-admission system is required.

Set `execution_limits.max_file_bytes` to bound each file (default: 128 MiB).
`max_response_bytes` still limits result metadata and structured responses.
Streaming does not turn CSV/JSON parsing or SQL row results into streaming work.

## Data path

The source writes through a 64 KiB copy buffer into encrypted PostgreSQL chunks
of at most 1 MiB. A manifest becomes readable only after full length and SHA-256
verification. The sink consumes the retained bytes with backpressure. It never
rereads a changing source to recover missing input.

Every retained read/write requires the organization and a live execution claim.
Opaque references travel inside encrypted broker messages and in-process file
bindings; public JSON and replay checkpoints contain only safe file metadata.
Cancelled, expired, incomplete or corrupt input cannot be used by a sink.

The complete source is retained before the sink starts. Maintenance runs every
minute and deletes expired retained files, including cancelled/ambiguous work.
Retention lasts until 15 minutes after the request deadline. PostgreSQL reuses
freed space; database files need not shrink. Size storage for files, indexes, WAL and backups;
concurrency limits do not bound completed files waiting for expiry.

## Supported operations

| Connector | Native-file operations | Restrictions |
|---|---|---|
| S3 | `read`, `write` with `format: file` | Sequential 8 MiB multipart parts; at most 10,000 parts (78.125 GiB). Completion requires exact length/digest. Conditional no-overwrite requires provider support. |
| FTP | `read`, `write` with `format: file` | Writes verify a temporary file before rename. Atomic no-overwrite is refused. Host loss can leave temporary files. |
| Git, GitHub, GitLab | `read`, `write` with `format: file` | Retained streaming requires Linux. Only the selected raw blob is checked out; repository filters/line-ending conversion are disabled. LFS pointers remain pointers. |
| HTTP | File responses and retained file request bodies | Existing content-type, network and idempotency rules apply. Success requires complete consumption of the current verified request body. |
| Google Workspace | Drive file read/create/update; Docs/Slides PDF export | Existing selected-file/folder authorization applies. Upload success requires matching target/length and full input consumption. |

Git helpers inherit a 512 MiB process address-space cap, a 16 MiB allocation
cap and a per-file write cap of twice the file allowance plus 64 MiB. These
are per-process/file bounds, not aggregate memory or scratch quotas. Unsupported
workers refuse retained Git operations before opening input.

Provider limits and shorter saved-query/caller deadlines still apply. Streaming
uses one file-size allowance; it does not raise HTTP metadata, parsing or
structured-result limits. Finite byte producers, including future SQL serializers,
can reuse the same retained-store and destination interfaces.

## Failures and installation

A consumed source does not prove destination delivery. Providers verify input
before publication; partial or uncertain writes never become success or authorize
blind replay. S3 aborts multipart state on ordinary failure/cancellation. FTP uses
bounded cleanup through a fresh connection. Lost completion/rename/push responses
require independent destination inspection. Host loss additionally requires
provider cleanup procedures or lifecycle rules.

Matching binaries, Core migration 3 and refreshed runtime/executor grants are
required. The installer backs up the previous database/configuration before
migration and applies grants from the matching candidate tools bundle. Never
roll only one side of the encrypted broker contract.

[Qualification evidence](qualification/native-file/README.md) records exact
revisions, tests, measurements and exclusions. Local results do not establish
hosted provider capacity or authorize deployment.
