# Example: PostgreSQL to S3

For the reusable five-recipe collection, start with [Pipeline examples](../examples/index.md).
This walkthrough keeps its own tutorial assets; it is a separate workflow.

This CLI worked example is only about the PostgreSQL-to-S3 pipeline. Account setup,
CLI installation, and the specification format have their own
guides:

- Confirm that you can sign in to the deployment you intend to use.
- [Install and authenticate the CLI](cli.md)
- [Read the pipeline specification reference](pipeline-specifications.md)

The downloadable pipeline joins synthetic PostgreSQL orders and customers with
`sql_v1`, then writes JSON to S3. Braidplane does not provide the database,
bucket, or repository.

Choose the systems appropriate for your evaluation. Hosted execution cannot
reach your laptop's `localhost`, loopback, or private network addresses by
default. Do not disable TLS or make the bucket public as a shortcut.

## Before you start

You need an authenticated CLI, Bash, Git, `jq`, and:

- a Braidplane workspace where you can manage a project, environment, data
  sources, queries, bindings, deployments, and runs;
- your own reachable PostgreSQL database;
- your own private S3 bucket and dedicated tutorial prefix;
- a Git repository with an initialized branch you can push to.

Keep the same terminal throughout the example. Keep `BRAIDPLANE_SERVER_URL` set
to the installation API base URL you used to authenticate, including `/api`.
Choose a new project slug and an existing environment or a new one:

```bash
export BRAIDPLANE_ORG='<organization-slug>'
export BRAIDPLANE_PROJECT='<new-tutorial-project-slug>'
export BRAIDPLANE_ENVIRONMENT='<environment-slug>'
export TUTORIAL_GIT_URL='<your-repository-clone-url>'
export TUTORIAL_BRANCH='<your-tracked-branch>'
export TUTORIAL_GIT_SOURCE='tutorial-git'
export TUTORIAL_ORDERS_SOURCE='tutorial-postgres'
export TUTORIAL_CUSTOMERS_SOURCE='tutorial-postgres'
export TUTORIAL_SINK_SOURCE='tutorial-s3'
```

For a **new** tutorial project, create it, then enable your chosen environment.
Only create the organization-wide environment if it does not already exist:

```bash
braidplane project create --name 'My first pipeline' --slug "$BRAIDPLANE_PROJECT"
# Skip this command when reusing an existing environment.
braidplane environment create --name 'Tutorial' --slug "$BRAIDPLANE_ENVIRONMENT"
braidplane project-environment enable "$BRAIDPLANE_ENVIRONMENT" --default
braidplane project get "$BRAIDPLANE_PROJECT"
braidplane project-environment list
```

Confirm the selected project and enabled environment before continuing. You can
reuse an existing project by skipping project creation, but never delete that
project during tutorial cleanup.

## 1. Connect your systems and create operations

A **data source** is an organization-owned connection, including credentials.
A **saved operation** describes what to read or write through that connection.
The same PostgreSQL connection can serve both source operations in this example.

### Register connections

Download the [PostgreSQL](files/postgres.connection.json),
[S3](files/s3.connection.json), and [Git HTTPS](files/git.connection.json) request
templates into a folder named `tutorial-connections`, outside any Git repository.
Restrict that folder to your user (`chmod 700 tutorial-connections`). Replace all
angle-bracket placeholders with your own settings; JSON does not expand shell
variables. If you change a connection slug, update the corresponding
`TUTORIAL_*_SOURCE` variable too.

| Connection | Configuration and credential files |
|---|---|
| PostgreSQL | Your database host, port, database name, and dedicated read-only username. Put its password in `postgres-password` beside the JSON file. Keep TLS certificate verification enabled. Restrict the role to the tables you intend to read. |
| S3 | Your bucket, signing region, and a dedicated prefix such as `tutorial/first-pipeline`. Put credentials in `s3-access-key-id` and `s3-secret-access-key`. For S3-compatible storage, add the provider's HTTPS `endpoint` and its required `force_path_style` setting. Temporary credentials also need `session_token_file`. |
| Git | Your HTTPS clone URL, initialized branch, provider username, and a repository-scoped token in `git-token`. This connection needs repository read/write access for the project backend. Your local Git login is separate. |

Enter secret values using your editor or password manager, not literal shell
commands. Make the credential files readable only by your user (`chmod 600`),
and never commit or share the folder. Credential file paths resolve relative
to each request file. Restrict provider permissions to your tutorial resources;
Braidplane's `read_only` flag is an additional guard, not proof of least privilege.
For S3, allow the connection test's bucket access and required object operations
only in your tutorial scope; the bucket remains private.

Check existing data-source slugs first. Reuse a compatible connection you are
authorized to use, or choose unused slugs and create new ones. Do not overwrite
someone else's connection. For new connections:

```bash
braidplane data-source list
braidplane data-source test --file tutorial-connections/postgres.connection.json
braidplane data-source create --file tutorial-connections/postgres.connection.json
braidplane data-source test --file tutorial-connections/s3.connection.json
braidplane data-source create --file tutorial-connections/s3.connection.json
braidplane data-source test --file tutorial-connections/git.connection.json
braidplane data-source create --file tutorial-connections/git.connection.json
```

A successful draft connection test exits zero and prints JSON such as
`{"type":"postgres","status":"succeeded"}`. Use `--output text` for a readable
confirmation. Creation returns connection metadata; it does not return the secret
values. After successful registration, remove local credential files when no longer
needed.

### Choose your sample data

Download these public files into `tutorial-inputs`, next to `tutorial-connections`:

- [Synthetic source data](files/source.sql), optional for the exact example
- [Orders query](files/orders-query.statement.json)
- [Customers query](files/customers-query.statement.json)
- [S3 write operation](files/sink-write.statement.json)
- [Expected destination rows](files/joined-orders.expected-rows.json)

For the exact example, use your database provider's SQL editor to run `source.sql`
in a fresh, non-production database you control. It creates and populates only
`etl_golden_orders` and `etl_golden_customers`; it fails if those names already
exist. Use an authorized setup identity for this step, then grant your separate
Braidplane read-only role access to those two tables. Do not use the setup
identity as the Braidplane connection credential.

Alternatively, adapt the query files to your own tables. Keep output
column names and types consistent with the pipeline's contracts, or update those
contracts and the transform together. The supplied expected rows apply only to
the supplied sample data and unmodified queries. For another connector, adapt
the saved operations and the spec's operation types as well as the bindings;
changing a saved connection alone does not translate SQL into another API.

```bash
braidplane data-source test-stored "$TUTORIAL_ORDERS_SOURCE"
braidplane data-source test-stored "$TUTORIAL_SINK_SOURCE"
braidplane data-source schema refresh "$TUTORIAL_ORDERS_SOURCE"
braidplane data-source schema get "$TUTORIAL_ORDERS_SOURCE"
```

Schema refresh is asynchronous. If import is still pending, read the schema
again after it completes. Confirm the tables your queries use appear in cached
metadata. If you use two separate PostgreSQL connections, test and refresh both.
Create these
operations once, on the dedicated tutorial connections. If a slug already exists,
stop and confirm ownership rather than updating someone else's
operation.

```bash
braidplane query create --data-source "$TUTORIAL_ORDERS_SOURCE" \
  --name 'Tutorial paid orders' --slug golden-path-orders \
  --statement-file tutorial-inputs/orders-query.statement.json \
  --row-limit 100 --timeout-seconds 30
braidplane query create --data-source "$TUTORIAL_CUSTOMERS_SOURCE" \
  --name 'Tutorial active customers' --slug golden-path-customers \
  --statement-file tutorial-inputs/customers-query.statement.json \
  --row-limit 100 --timeout-seconds 30
braidplane query create --data-source "$TUTORIAL_SINK_SOURCE" \
  --name 'Tutorial joined orders export' --slug golden-path-s3-write-joined-orders \
  --statement-file tutorial-inputs/sink-write.statement.json --timeout-seconds 30
braidplane query execute golden-path-orders --data-source "$TUTORIAL_ORDERS_SOURCE"
braidplane query execute golden-path-customers --data-source "$TUTORIAL_CUSTOMERS_SOURCE"
```

With the supplied sample data, expect successful SQL results with
`payload.row_count` of **3** and **4**
respectively. Compare these counts with the sample data you loaded.
Do not execute the saved S3 write operation directly: its input comes from the
pipeline transform.

## 2. Commit the complete pipeline and configure Git

Download the [complete pipeline specification](files/joined-orders.pipeline.yaml).
It contains two query steps, the SQL join, explicit row contracts, and the S3
sink. Its `data_source_ref` values are **logical names**, not credentials or your
saved connection slugs. Keep these names as supplied; the next step binds them.

From the folder containing `tutorial-inputs`, clone your repository and enter it:

```bash
git clone --branch "$TUTORIAL_BRANCH" "$TUTORIAL_GIT_URL" tutorial-repository
cd tutorial-repository
mkdir -p pipelines
```

Save the downloaded specification as `pipelines/joined-orders.pipeline.yaml`.
If the tracked branch requires a pull request, create a working branch now and
use your provider's normal push/review process instead of pushing the tracked
branch directly. Otherwise, validate, commit, and push only this file:

```bash
braidplane pipeline validate --local --file pipelines/joined-orders.pipeline.yaml
git add pipelines/joined-orders.pipeline.yaml
git commit -m 'Add synthetic joined-orders tutorial'
git push origin "$TUTORIAL_BRANCH"
```

If the tracked branch requires a pull request, use your Git provider's normal
review process and wait until this exact file is merged into that branch.

For a fresh tutorial project whose backend has not yet been configured, adopt
the repository after confirming the selected Git connection targets it:

```bash
braidplane project pipeline-backend set "$BRAIDPLANE_PROJECT" \
  --data-source "$TUTORIAL_GIT_SOURCE" --tracked-branch "$TUTORIAL_BRANCH" \
  --pipeline-dir pipelines --mode adopt_existing
braidplane project get "$BRAIDPLANE_PROJECT"
```

If the project already has this backend, skip `set` and inspect it with `get`.
Do not replace an existing backend. Check its repository, branch, and pipeline
directory against the values you just pushed.

### Sync and deploy from the UI

After configuring the project Git backend, open its pipeline catalog and choose
**Sync from Git**. **Preview Git changes** shows the source commit and the
pipelines that would be synced, skipped, deleted, or reset, plus any failures.
Review the report and choose **Apply Git changes**. If the source changed after
preview, preview again before applying.

For a successfully synced pipeline, choose **Deploy version**, select an enabled
runtime environment, and choose **Review target**. The form shows the exact
version and current deployment. Confirm protected environments when prompted.
Open **Resolve selected version bindings** to see missing or invalid references
for that environment before the full expert matrix. Binding controls edit current
configuration explicitly; protected targets still require confirmation. Choose
**Return to selected pipeline** to review the same full version hash and environment
before retrying deployment. Validate that selected version and start a run after
deployment to check its behavior. Existing pipeline details also provide a
targeted **Sync from Git** action.

## 3. Apply, bind, validate, and deploy

Run from `tutorial-repository`:

```bash
braidplane pipeline apply --dir pipelines --pipeline golden-path-joined-orders
```

`apply` reads the committed remote Git branch and stores its definitions and
logical refs. It needs no environment bindings and never deploys. Review its
source commit and imported version hash, then bind and explicitly deploy that
version below. Stop and investigate any apply error or a missing pipeline.

```bash
braidplane pipeline get golden-path-joined-orders > tutorial-pipeline.json
TUTORIAL_SPEC_HASH="$(jq -er '.current_version_spec_hash | select(length > 0)' tutorial-pipeline.json)"
```

Verify the stored specification and Git details match the file and commit you
pushed. Local uncommitted files are not applied. Bind the logical refs to the
saved connections in your selected environment:

```bash
braidplane data-source-binding bind golden-path-orders-postgres \
  --pipeline golden-path-joined-orders --data-source "$TUTORIAL_ORDERS_SOURCE"
braidplane data-source-binding bind golden-path-customers-postgres \
  --pipeline golden-path-joined-orders --data-source "$TUTORIAL_CUSTOMERS_SOURCE"
braidplane data-source-binding bind golden-path-s3-sink \
  --pipeline golden-path-joined-orders --data-source "$TUTORIAL_SINK_SOURCE"
braidplane data-source-binding list
braidplane pipeline validate --file pipelines/joined-orders.pipeline.yaml
braidplane pipeline deploy golden-path-joined-orders --spec-hash "$TUTORIAL_SPEC_HASH"
```

Server validation should print `pipeline validation succeeded` and exit zero.
It checks contracts, bindings, and cached schema; it does not execute connectors.
Deployment selects an immutable version for this environment and does not start
a run.

## 4. Run and verify the destination

```bash
TUTORIAL_RUN="tutorial-$(date -u +%Y%m%d%H%M%S)"
braidplane pipeline run start golden-path-joined-orders --run-slug "$TUTORIAL_RUN"
braidplane pipeline run watch "$TUTORIAL_RUN" --timeout-seconds 120 --output json \
  > tutorial-progress.ndjson
braidplane pipeline run get "$TUTORIAL_RUN" --output json > tutorial-run.json
jq -e --arg hash "$TUTORIAL_SPEC_HASH" \
  '.metadata.status == "succeeded" and .metadata.pipeline_version_spec_hash == $hash' \
  tutorial-run.json
braidplane pipeline run get "$TUTORIAL_RUN" --output text
```

Start is asynchronous. Watch must reach **`succeeded`**; an accepted start is not
completion. On timeout, use `pipeline run get` and watch the same run again. Do
not start another run just because the wait timed out. On `failed` or `canceled`,
inspect the safe diagnostic and contact the team before retrying.

With the supplied sample data, inspect the sink's `file_write` details for
`written_rows: 3` and a
`target_path`. The object key uses the runtime sink replay identity; do not guess
it from the run slug. In your storage provider's console,
open the reported bucket/key and download that exact object as
`tutorial-output.json` in this repository folder.

```bash
jq -S . tutorial-output.json > tutorial-output.sorted.json
jq -S . ../tutorial-inputs/joined-orders.expected-rows.json > tutorial-expected.sorted.json
diff -u tutorial-expected.sorted.json tutorial-output.sorted.json
```

For the unmodified sample, the diff must produce no output and exit zero.
For an adapted pipeline, compare against the expected result you defined. The file is a raw JSON array,
not a query response envelope. The full expected file checks every field;
these are the three order totals you should recognize:

| Order | Customer | Net total |
|---|---|---:|
| `ord-1001` | Atlas Retail | 1199 |
| `ord-1002` | Atlas Retail | 349.99 |
| `ord-1004` | Northstar Logistics | 800 |

## 5. Inspect the same run in the app

Open the app and select the same organization, project, and environment
as the CLI. Open **Pipelines**, then **Golden Path Joined Orders**. Check Git
provenance, the current version, the deployment for your environment, the spec
and contracts, and all three data-source bindings.

Open **Runs**, select your `TUTORIAL_RUN`, and inspect its terminal status,
execution timeline, version, and **Sink output metadata**. Deployment state and
run state are separate. The app displays destination metadata; the independent
object comparison above proves the actual bytes.

## 6. Trigger a safe failure and recover

After the successful output comparison, remove only this pipeline's sink
binding in the selected tutorial environment:

```bash
braidplane data-source-binding delete golden-path-s3-sink --pipeline golden-path-joined-orders
braidplane pipeline validate --file pipelines/joined-orders.pipeline.yaml
```

Expect a non-zero validation exit with **`DATA_SOURCE_BINDING_NOT_FOUND`**,
identifying `write-joined-orders`, `golden-path-s3-sink`, and your environment.
This is a validation failure; no new run or output object is created. The
previous successful run remains valid. On the pipeline detail page, the sink
binding for this environment should be missing.

Restore the binding and validate again:

```bash
braidplane data-source-binding bind golden-path-s3-sink \
  --pipeline golden-path-joined-orders --data-source "$TUTORIAL_SINK_SOURCE"
braidplane pipeline validate --file pipelines/joined-orders.pipeline.yaml
```

Expect validation success. Do not change the spec or rotate credentials to fix
a missing binding.

## 7. Clean up and get help

Using the storage console, delete only the output object whose exact path you
verified. Do not delete the bucket, shared prefixes, or source tables.

Once you no longer need the run history, and only after confirming this project
is the dedicated tutorial project, delete it and its owned runtime resources:

```bash
braidplane project delete "$BRAIDPLANE_PROJECT"
```

This does not remove the Git file or organization-owned connections and queries.
Remove the three operations you created on the dedicated tutorial connections:

```bash
braidplane query delete golden-path-orders --data-source "$TUTORIAL_ORDERS_SOURCE"
braidplane query delete golden-path-customers --data-source "$TUTORIAL_CUSTOMERS_SOURCE"
braidplane query delete golden-path-s3-write-joined-orders --data-source "$TUTORIAL_SINK_SOURCE"
```

Use a normal Git commit/review to remove only `pipelines/joined-orders.pipeline.yaml`
from the tutorial repository. If you created the connections solely for this
tutorial, remove them after deleting their queries and project bindings:

```bash
braidplane data-source delete "$TUTORIAL_ORDERS_SOURCE"
braidplane data-source delete "$TUTORIAL_SINK_SOURCE"
braidplane data-source delete "$TUTORIAL_GIT_SOURCE"
```

The example uses one PostgreSQL connection for both queries, so delete it once.
Skip deletion for reused connections. Remove the two sample tables through your
database provider only if you created them here and no longer need them. Keep
shared environments, databases, buckets, repositories, and organization resources. Delete downloaded output and local JSON evidence
when no longer needed; keep them out of Git. `braidplane logout` removes the saved
CLI login when you finish using it.

For help, contact your deployment operator with the
step number, CLI version, run slug if one exists, terminal
status, and diagnostic code. Do not send tokens, passwords, connection request
files, raw records, or screenshots containing secrets.
