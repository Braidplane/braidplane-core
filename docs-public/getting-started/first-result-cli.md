# Get your first result with the CLI

Run one saved read and check that it returns data. The PostgreSQL sample returns
one synthetic row with ID `1` and email `alex@example.com`. It creates no tables
and reads no stored customer data.

You need an [authenticated CLI](cli.md), a workspace where you can create data
sources and queries, and least-privilege credentials for one system your
deployment can reach. A standalone read needs no project, Git repository, or
destination. For another system, use the safe read in its
[connector guide](../connectors/index.md).

Hosted execution blocks loopback and private network addresses by default.
Keep TLS verification enabled. If you have no reachable system, use the
[reading tour](overview.md).

## 1. Connect your system

For OAuth or workload identity, follow your connector's setup guide to connect
and test the data source. Then [save one read](#2-save-one-read).

For static credentials, prepare `connection.json` using the connector guide and
[`CreateDataSourceRequest` schema]({{ public_openapi_url }}#/components/schemas/CreateDataSourceRequest).
The schema defines the complete request envelope for every connector.

For PostgreSQL, save the [connection request](files/postgres.connection.json)
as `connection.json`. Replace every angle-bracket placeholder and keep
`read_only: true`. Put only the database password in `postgres-password` next
to the request, without a trailing newline. The inline sample only needs a role
that can connect.
For reads from your own tables, see the
[PostgreSQL role requirements](../connectors/postgresql.md#authentication-and-permissions).

Keep `connection.json` and referenced credential files outside Git with
permissions restricted to your user.

```bash
braidplane data-source test --file connection.json
```

A successful draft test exits zero and prints JSON such as
`{"type":"postgres","status":"succeeded"}`. Use `--output text` for a readable
confirmation.

### Optional: try one failed connection and recover

To practice recovery before creating the connection, use a test PostgreSQL
database you control. In `connection.json`, replace only `config.database` with
a name you know is absent, such as `braidplane_tutorial_missing`. Run the draft
test again:

```bash
braidplane data-source test --file connection.json
```

Expect a nonzero exit and `DATA_SOURCE_CONNECTION_FAILED` on stderr. For this
failure, compare `config.database` with the name of the database you intended
to use. Restore its name and repeat the test. Confirm exit zero and
`"status":"succeeded"`.
If the corrected name still fails, see the
[PostgreSQL connection guide](../connectors/postgresql.md#authentication-and-permissions).

For static credentials, create the data source after a successful draft test:

```bash
braidplane data-source create --file connection.json
```

To test a connection you already saved, run:

```bash
braidplane data-source test-stored '<data-source-slug>'
```

`test-stored` makes a live connection attempt using that data source's saved
configuration and stored credentials. It records the success or failure on the
data source and prints nothing when successful. It does not run a saved query or
write pipeline data.

## 2. Save one read

For PostgreSQL, save this as `operation.statement.json`. For another system,
use the statement in its connector guide.

```json
{
  "type": "postgres",
  "sql": "SELECT id, email FROM (VALUES (1, 'alex@example.com')) AS customers(id, email) WHERE id = {{customer_id}}"
}
```

Define the sample's `customer_id` parameter in `parameters.json`:

```json
[
  {
    "name": "customer_id",
    "data_type": "integer",
    "is_required": true
  }
]
```

Replace `<data-source-slug>` with the slug of your saved connection, such as
`tutorial-postgres`. Omit `--parameters-file` if your statement has no parameters.

```bash
braidplane query create --data-source '<data-source-slug>' \
  --name 'First read' --slug first-read \
  --statement-file operation.statement.json \
  --parameters-file parameters.json --row-limit 10 --timeout-seconds 10
```

The placeholder name and parameter name are case-sensitive and must match.
See [parameters and placeholders](pipeline-specifications.md#parameters-and-placeholders)
for pipeline literals, per-run input, environment values, secrets, and earlier
step results.

## 3. Run and verify

For the parameterized example above:

```bash
braidplane query execute first-read --data-source '<data-source-slug>' \
  --parameters-json '{"customer_id": 1}'
```

For a query without parameters:

```bash
braidplane query execute first-read --data-source '<data-source-slug>'
```

For the sample, expect `success: true`, `payload.row_count: 1`, and the row
`{"id":1,"email":"alex@example.com"}` in `payload.rows`. For another connector,
compare the result with its guide's read example.

To resume later, inspect recorded first-result progress without retrieving
result payloads:

```bash
braidplane query execution summary
```

`successful_execution` identifies the latest retained successful saved operation
by source, query, execution and timestamp. Execution history follows workspace
retention and is recorded on a best-effort basis, so a null value can also mean
the record expired or was not saved. Inspect pipeline runs separately in their
run history.

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
  --slug repeat-first-read --parameters-json '{"customer_id": 1}'
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
