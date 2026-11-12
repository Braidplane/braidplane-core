# Native-file streaming

Native file operations retain encrypted bytes in PostgreSQL before sending them
to a destination. They use the existing execution deadlines and concurrency queue.
Set `execution_limits.max_file_bytes` to limit each file; the default is 128 MiB.
`max_response_bytes` still limits metadata and structured responses. CSV/JSON
parsing and SQL row results keep their existing limits. PostgreSQL can also
[serialize a finite SQL result as a retained file](SQL_RESULT_STREAMING.md).

## Storage and cleanup

The source uses a 64 KiB copy buffer and encrypted chunks of at most 1 MiB.
A file becomes readable after its full length and SHA-256 digest are verified.
The sink reads those retained bytes with backpressure. It cannot reread a changed
source to recover missing input.

Retained reads and writes require the organization and a live execution claim.
File references stay inside encrypted broker messages and in-process bindings.
Public JSON and replay checkpoints contain file metadata. Sinks reject cancelled,
expired, incomplete or corrupt input.

Maintenance runs every minute and deletes expired retained files, including
cancelled or ambiguous work. Files expire 15 minutes after the request deadline.
PostgreSQL can reuse the freed space, but its database files may not shrink.
Allow storage for completed files awaiting expiry, indexes, WAL and backups;
active-request concurrency limits do not bound that storage.

## Supported operations

| Connector | Native-file operations | Restrictions |
|---|---|---|
| S3 | `read`, `write` with `format: file` | Sequential 8 MiB multipart parts; at most 10,000 parts (78.125 GiB). Completion requires exact length/digest. Conditional no-overwrite requires provider support. |
| FTP | `read`, `write` with `format: file` | Writes verify a temporary file before rename. Atomic no-overwrite is refused. Host loss can leave temporary files. |
| Git, GitHub, GitLab | `read`, `write` with `format: file` | Retained streaming requires Linux. Only the selected raw blob is checked out; repository filters and line-ending conversion are disabled. LFS pointers remain pointers. |
| HTTP | File responses and retained file request bodies | Existing content-type, network and idempotency rules apply. Success requires complete consumption of the current verified request body. |
| Google Workspace | Drive file read/create/update; Docs/Slides PDF export | Existing selected-file/folder authorization applies. Upload success requires matching target/length and full input consumption. |

Git helpers inherit a 512 MiB process address-space cap, a 16 MiB allocation cap
and a per-file write cap of twice the file allowance plus 64 MiB. These limits
apply per process or file; they do not cap aggregate memory or scratch space.
Unsupported workers refuse retained Git operations before opening input.
Provider limits and shorter saved-query or caller deadlines also apply.

## Failed writes and upgrades

Consuming the source does not prove delivery. Partial or uncertain writes fail
and cannot authorize automatic replay. S3 aborts multipart uploads on ordinary
failure or cancellation. FTP attempts cleanup through a fresh connection.
Lost completion, rename or push responses require independent destination
inspection. Host loss requires provider cleanup procedures or lifecycle rules.

Use matching binaries, Core migration 3 and refreshed runtime/executor grants.
The installer backs up the previous database and configuration before migration
and applies grants from the candidate tools bundle. Upgrade both sides of the
encrypted broker contract together.

A local S3-to-S3 process test transferred 1 GiB plus 17 bytes and verified the
checksum independently. That test showed the path can handle larger files;
capacity planning is still required before raising the 128 MiB default.
