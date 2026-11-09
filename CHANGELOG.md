# Changelog

## Unreleased

- Bound spreadsheet expansion during reads and schema inference, including
  sparse worksheets and repeated shared-string copies.
- Bound OpenAPI reference expansion, schema summaries and generated query drafts
  before retaining their output.
- Enforce Snowflake row and response budgets while collecting results, and omit
  rows from results that exceed the response limit.
- Keep upstream MySQL column names out of generated JSON-path literals.
- Interrupt established FTP operations when their caller cancels or times out.
- Bound BigQuery pagination and row expansion before retaining results.
- Withhold reflected HTTP credentials for every authentication mode, including
  JSON and YAML OpenAPI responses and normalized non-file response text.
- Reject Git administration paths in repository file operations and schema discovery.
- Require pipeline execution permission to create or update schedule and webhook
  trigger definitions, including disabled definitions.
- Use the installation's `/api` base in Community API and CLI examples, and
  correct connector and operating guidance.
- Simplify Community support guidance and remove commercial planning.
- Remove billing addresses from Community user profiles and the public API.
- Declare protected API authentication, public sign-in exceptions and signed
  pipeline webhook authentication in OpenAPI and generated clients.
- Publish the [Alpha API contract](docs-public/api/alpha.md),
  including matching application/CLI versions, migration limits and execution retries.
- Remove private tracker references from source, documentation and synthetic fixture names.
