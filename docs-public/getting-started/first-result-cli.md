# Get your first result with the CLI

Connect one supported system, save one harmless standalone read,
and run it. Exact connection fields, statement shapes, and results depend on the
connector.

Before starting, sign in to the deployment you intend to use,
[install and authenticate the CLI](cli.md), and choose your system in the
[connector directory](../connectors/index.md). Use the connector guide's safe
read example and least-privilege provider credentials.

Hosted execution cannot reach your laptop's `localhost`, loopback, or private
network addresses by default. Do not disable TLS or expose a system publicly as
a shortcut.

## 1. Connect your system

Prepare `connection.json` from the connector guide and the
[`CreateDataSourceRequest` schema]({{ public_openapi_url }}#/components/schemas/CreateDataSourceRequest).
The schema defines the complete request envelope for every connector.

For example, download the complete
[PostgreSQL connection request](files/postgres.connection.json), replace every
angle-bracket placeholder, and keep `read_only: true`. The database role needs
`CONNECT` on the database, `USAGE` on the required schemas, and `SELECT` on the
required tables or views. This PostgreSQL request is an example; use the request
fields and least-privilege permissions from the guide for the connector you
choose.

Keep `connection.json` and referenced credential files outside Git with
permissions restricted to your user.

```bash
braidplane data-source test --file connection.json
braidplane data-source create --file connection.json
```

A successful draft test exits zero and prints JSON such as
`{"type":"postgres","status":"succeeded"}`. Use `--output text` for a readable
confirmation. Creation returns safe metadata, never credentials.
Static-credential connections can be tested before creation. OAuth
and workload-identity connectors can create an inactive data source, then
require the connection or verification step in their connector guide before a
successful saved test.

To check the saved connection again later, run:

```bash
braidplane data-source test-stored '<data-source-slug>'
```

`test-stored` makes a live connection attempt using that data source's saved
configuration and stored credentials. It records the success or failure on the
data source and prints nothing when successful. It does not run a saved query or
write pipeline data.

## 2. Save one read

Save the connector guide's standalone read statement as
`operation.statement.json`:

For a query without parameters:

```bash
braidplane query create --data-source '<data-source-slug>' \
  --name 'First read' --slug first-read \
  --statement-file operation.statement.json
```

If the statement contains a placeholder such as `{{customer_id}}`, define it in
`parameters.json`:

```json
[
  {
    "name": "customer_id",
    "data_type": "integer",
    "is_required": true
  }
]
```

Pass the definitions when creating the query:

```bash
braidplane query create --data-source '<data-source-slug>' \
  --name 'First read' --slug first-read \
  --statement-file operation.statement.json \
  --parameters-file parameters.json
```

The placeholder name and parameter name are case-sensitive and must match.
See [parameters and placeholders](pipeline-specifications.md#parameters-and-placeholders)
for pipeline literals, per-run input, environment values, secrets, and earlier
step results.

## 3. Run and verify

For the parameterized example above:

```bash
braidplane query execute first-read --data-source '<data-source-slug>' \
  --parameters-json '{"customer_id": 42}'
```

For a query without parameters:

```bash
braidplane query execute first-read --data-source '<data-source-slug>'
```

Expect `success`. Compare the returned rows, documents, records, or file content
with the connector guide's expected result and your source data. A
connection test proves access; execution proves the saved read and its
permissions work.

If execution fails, confirm the data-source slug, statement shape, parameters,
and provider permissions, then test the saved connection and retry.

To resume later, inspect recorded first-result progress without retrieving
result payloads:

```bash
braidplane query execution summary
```

`successful_execution` identifies the latest retained successful saved operation
by source, query, execution, and timestamp. A null value means no retained success
is recorded; it does not prove an operation never ran. Execution history is
recorded on a best-effort basis and follows workspace retention. This summary
does not prove pipeline deployment, a successful pipeline run, or read-only
provider behavior.

## 4. Repeat the read as a pipeline

Keep the saved query and connection. Use an authenticated user PAT for proposing
pipeline creation and provider consent; an organization API key cannot stand in
for a user's consent. Reuse a project with a connected repository, or create
one and explicitly authorize a private repository in your provider account:

```bash
braidplane project create --name 'First pipeline' --slug first-pipeline
braidplane project repository create first-pipeline --provider github \
  --owner '<your-owner-or-organization>' --repository-name '<unused-name>' \
  --create-private-repository --wait
```

For GitLab, use `--provider gitlab` and your personal or group namespace as
`--owner`. Follow the consent URL in your browser while signed into Braidplane
as the same user. A timeout or declined consent preserves progress; inspect it
with `project repository get first-pipeline`, correct access and repeat the same
create command. Do not change the target to retry an interrupted setup.

Propose the current saved operation without writing a pipeline file:

```bash
braidplane pipeline create --project first-pipeline --from-query first-read \
  --data-source '<data-source-slug>' --name 'Repeat first read' \
  --slug repeat-first-read --parameters-json '{"customer_id": 42}'
braidplane pipeline creation list --project first-pipeline
braidplane pipeline creation get '<creation-slug>' --project first-pipeline
```

Omit `--parameters-json` for an operation without parameters. Supply required
values explicitly; the command does not reuse an earlier execution's values.
The response records a creation `slug`, publication `status` and proposed
`spec_hash`. Inspect `proposal_url` when available and review and merge it in
GitHub or GitLab. If publication failed, `pipeline creation retry
<creation-slug> --project first-pipeline` retries the exact recorded definition.
Changing its definition or repository requires a new pipeline name or slug.

After merging, import the pipeline and enable an existing
environment for this project:

```bash
braidplane pipeline apply --project first-pipeline --pipeline repeat-first-read
braidplane project-environment enable '<environment-slug>' --project first-pipeline
braidplane data-source-binding bind source --project first-pipeline \
  --pipeline repeat-first-read --environment '<environment-slug>' \
  --data-source '<data-source-slug>'
braidplane pipeline deploy repeat-first-read --project first-pipeline \
  --environment '<environment-slug>' --spec-hash '<imported-spec-hash>'
braidplane pipeline run start repeat-first-read --project first-pipeline \
  --environment '<environment-slug>'
braidplane pipeline run watch '<run-slug>' --project first-pipeline \
  --environment '<environment-slug>' --output text
braidplane pipeline run get '<run-slug>' --project first-pipeline \
  --environment '<environment-slug>' --output text
```

Use the imported hash returned by `pipeline apply`, and the run slug returned by
`pipeline run start`. Deployment validates the selected version and bindings.
Starting a run only queues it; expect terminal `succeeded` from watch and inspect
its executed version and timeline. Failed or canceled runs return a nonzero
watch result. Correct the reported cause before starting another run.

## Clean up

```bash
braidplane query delete first-read --data-source '<data-source-slug>'
braidplane data-source delete '<data-source-slug>'
```

Delete local request and credential files when no longer needed.

If you continued into pipeline setup, preserve its query and connection while
they remain referenced. The delete commands above apply to standalone resources.

To add a destination, read the [pipeline specification reference](pipeline-specifications.md)
or follow the [PostgreSQL-to-S3 example](first-pipeline.md).
