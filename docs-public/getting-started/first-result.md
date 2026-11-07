# Get your first result in the UI

Connect one supported system and run one harmless standalone
read. The result can be rows, documents, records, or file content depending on
the connector. This proves the saved connection and operation work together; it
does not require the CLI, a Git repository, a pipeline, or a destination.

Before starting, sign in to the deployment you intend to use and choose your system in the
[connector directory](../connectors/index.md). Read its setup guide and use the
safe read example it identifies. Use least-privilege provider credentials.

Hosted execution cannot reach your laptop's `localhost`, loopback, or private
network addresses by default. Do not disable TLS or expose a system publicly as
a shortcut. If you have no reachable system, watch the
[synthetic demo](https://braidplane.com/#demo) instead.

## 1. Connect your system

1. Sign in, select your workspace, and open **Data Sources**.
2. Select **Add Data Source** and choose the connector.
3. Enter the settings from its guide and give the connection a recognizable
   name.
4. Follow the connector guide's authentication sequence.
5. For static credentials, test the connection and create the data source when
   it succeeds. OAuth and workload-identity connectors can save an inactive
   data source first; complete their connection or verification step, then test.

The detail page shows whether the data source is active and its last test.
Stored credentials are never displayed again. A successful saved connection
test confirms that the source can connect.

If the test fails, check the endpoint, authentication, TLS, provider
permissions, firewall, and hosted-connectivity requirements in the connector
guide.

## 2. Save one read

Saved connector operations currently appear under **Queries**.

1. Open **Queries** and select the data source you created.
2. Select **New query**.
3. Give the operation a recognizable name.
4. Enter the connector-specific fields from its safe read example.
5. Keep the row limit and timeout conservative, then select **Save query**.

## 3. Run and verify

Open the saved query and select **Run query**. Expect **success**. Compare the
returned rows, documents, records, or file content with the connector guide's
expected result and your source data.

A connection test proves access. This execution proves that the saved read and
its provider permissions also work. If it fails, confirm the selected data
source, statement fields, parameters, and permissions, then test the connection
and retry.

The dashboard marks **Get a result** complete when a retained successful saved
operation is recorded. Its query link returns to that operation. **Hide** keeps
the product available; **Resume setup** restores the guidance. Connection and
query work do not require a project. Project and repository setup follows when
you are ready to build a repeatable pipeline.

Progress uses retained execution metadata, not result payloads. It does not
certify read-only provider behavior or complete the pipeline-run milestone.

## 4. Repeat the read as a pipeline

Keep the saved query and connection if you want to repeat this read.

1. On the saved query, select **Build pipeline**. Select a project, or create
   one using the link and return to **Check readiness**.
2. If the project needs a repository, follow its repository setup link. Choose
   an existing repository or explicitly consent to creating a private GitHub or
   GitLab repository in your account or organization. Complete provider consent
   as the same Braidplane user, then return and check readiness.
3. Name the pipeline, supply any required query parameters, and select
   **Propose pipeline**. This references the current saved operation; it does
   not copy a previous execution or its parameter values.
4. Open **Review Git proposal** and review and merge it in your provider.
   Return to progress and select **Sync merged pipeline**. Git review and merge
   remain separate from deployment.
5. Once the exact proposed version is imported, select an enabled runtime
   target and **Configure source bindings**. Bind `source` to the connection
   containing your saved query, then return and **Deploy this version**.
6. Open the pipeline, then **Open Builder** for the same target. Select **Run**
   and inspect the resulting run's status, executed version and timeline.
   Deployment alone does not mean a run succeeded.

To resume after leaving, open **Pipelines → Creation proposals** in the same
project. A failed proposal can retry its recorded definition. A changed
definition or repository needs a new pipeline name or slug. Declining provider
consent preserves the repository setup; **Resume setup** starts fresh consent.
If an action is unavailable, ask an administrator for the permission identified
on that screen.

## Clean up

For standalone resources, delete the saved query before deleting the data source.
If you continued into pipeline setup, keep them while the pipeline references
them. Remove any local credential material you created for the connection.

For the equivalent terminal workflow, [use the CLI guide](first-result-cli.md).
To add a destination, read the
[pipeline specification reference](pipeline-specifications.md), or follow the
[PostgreSQL-to-S3 example](first-pipeline.md).
