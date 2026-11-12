# Braidplane documentation

Braidplane connects systems through pipelines: what to read, how to transform
it, and where to write the result. You can review each definition, deploy a
version, and see what happened in every run.

## Explore the platform

Start with the question your team needs to answer: which system holds the
records, what needs to change or move, and where should the result go? Then
choose the [UI tutorial](getting-started/first-result.md) or
[CLI tutorial](getting-started/first-result-cli.md) to connect a system you
already use and run one connector-appropriate safe read.

If you do not have infrastructure, [How Braidplane works](getting-started/overview.md)
is a reading tour that needs no account, CLI, database, or cloud bucket.

Then [explore the connectors](connectors/index.md): see which systems you can
connect, what each connector reads and writes, and its current limitations.
When you are ready to connect a system, follow
[Connect a system](connectors/connect-a-system.md).

Choose a complete synthetic workflow from [Pipeline examples](examples/index.md).
The collection links canonical specs, fixtures, bindings, expected results and
separate validation/execution evidence. Check its CLI compatibility status first.

## Common workflow patterns

These are starting points for planning a workflow. They describe supported
connector and pipeline shapes; choose systems and data appropriate for your evaluation.

- **Database orders → mapped HubSpot properties:** read order or customer
  records from a database such as [MongoDB](connectors/mongodb.md), normalize
  document objects to rows, then use a HubSpot upsert with a stable unique key and an explicit
  input-field-to-property mapping. The [HubSpot guide](connectors/hubspot.md)
  shows the required property setup and mapping.
- **Joined JSON → S3:** join compatible database rows or CSV files from
  [GitHub](connectors/github.md), then write the result as a JSON object.
  The [synthetic PostgreSQL-to-S3 walkthrough](getting-started/first-pipeline.md)
  includes the pipeline file, expected rows, and prerequisites.
- **HTTP JSON → scheduled CSV in FTP:** read an [HTTP API](connectors/http.md)
  response containing an object or array of objects, normalize it to rows,
  and attach an enabled schedule trigger to a deployed pipeline whose sink
  writes CSV to FTP. Schedule resources are managed for a
  project environment; the [FTP guide](connectors/ftp.md) documents CSV output
  and the connector's plain-FTP limits.
- **Snowflake ledger rows → CSV in S3:** query the period's rows in
  [Snowflake](connectors/snowflake.md), keep the output
  contract row-shaped, and write CSV to a dedicated S3 key. The [S3 guide](connectors/s3.md)
  documents structured row output and the explicit write format.

Each pattern still needs its own provider connection, permissions, bindings,
deployment, and destination check. The [pipeline specification reference](getting-started/pipeline-specifications.md)
explains those shared steps.

See the [Core issue tracker](https://github.com/Braidplane/braidplane-core/issues)
for planned work and proposals. Open issues are not delivery commitments.

The [API reference](api/index.md) describes the API available in this environment.

## Before you connect anything

Use appropriate provider permissions, keep your source data and verify outputs.

Read the [pipeline specification reference](getting-started/pipeline-specifications.md)
before writing your own pipeline. The
[PostgreSQL-to-S3 example](getting-started/first-pipeline.md) is a separate,
complete walkthrough.

Community has no hosted-plan or trial restrictions.

## Help and contributions

Use [GitHub Issues](https://github.com/Braidplane/braidplane-core/issues/new/choose)
for bugs, questions and connector proposals. See the
[contribution guide](https://github.com/Braidplane/braidplane-core/blob/main/CONTRIBUTING.md)
for setup, checks and review, and
[support and maintenance](https://github.com/Braidplane/braidplane-core/blob/main/SUPPORT.md)
for current version coverage and conduct guidance. Report security concerns
privately to [contact@braidplane.com](mailto:contact@braidplane.com), following the
[security policy](https://github.com/Braidplane/braidplane-core/blob/main/SECURITY.md).
Remove credentials, tenant data and private URLs from public reports.
