# File/object schema discovery

FTP and Git schema imports recursively discover regular files below `base_path`;
S3 recursively lists objects below `prefix`. Git skips symbolic links and the
`.git` directory. FTP skips directory links and other non-regular entries. Git and FTP
schema imports fail if discovery would descend beyond 32 directories. Future
file connectors use this same discovery and result contract. S3 keys ending in
`/`, including directory markers, remain raw object entries.

Discovered entries include the provider-relative path/name, extension-derived MIME
type, and size when available. Raw entries support explicit `format: file` reads.
CSV, JSON, and Excel workbooks retain field inference; extensionless samples use
content inference only when detected as UTF-8 plain text. Detected binary, PDF, HTML,
and other opaque content remains a raw-file entry. Raw bytes never appear in schema responses.

Each discovered entry is retained even when it is opaque, unreadable, or its
sample cannot be parsed. Opaque entries have no inferred fields. Failed samples also have no inferred
fields and include a warning diagnostic describing the failure. Supported CSV fields use
the header names and the field type `string`; JSON and Excel samples may infer
their supported structural fields. A failed sample never causes the raw bytes
to be stored in schema metadata.

Discovery and sampling use the schema import worker's execution limits. The
current defaults are 1,000 items, 10 MiB of schema bytes, and a 30-second
connector timeout (`execution_limits.max_result_rows`,
`execution_limits.max_response_bytes`, and `execution_limits.connector_timeout`).
Provider listing and sampling consume the same item and byte budgets. If a
listing exceeds a budget, the import fails and the previous schema cache is
left unchanged; it is not replaced by a partial result.
