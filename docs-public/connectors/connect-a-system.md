# Connect a system

A data source stores the connection settings for a system your organization
uses. You can explore the [connector guide](index.md) first; configuration is
needed only when you want to use that system in Braidplane.

## Before connecting

For an actual connection, you need a Braidplane account with permission
to manage data sources and access to the chosen provider. Choose the systems and data appropriate for your evaluation.

Braidplane's runtime must be able to reach the endpoint. A service on your
laptop's `localhost` is not reachable as that service from a hosted worker.
Hosted/default deployments block loopback and private network destinations.

## Create and test the connection

1. Select your organization and open its data sources.
2. Choose the connector for your system.
3. Enter its connection settings and complete the supported authentication
   flow. The connector guides below explain what information you need.
4. Test the connection and save it. A successful test checks connectivity and
   access, not every operation you might later perform.
5. Refresh schema metadata where useful, then inspect the discovered tables,
   properties, files, or API operations.

Credentials are encrypted at rest and are not returned in API responses. Keep
credentials out of pipeline definitions and run inputs. A source should use
provider credentials restricted to the required reads; a destination needs
only the writes its operation performs.

## Define an operation

Choose what to read or write: a SQL query, HTTP request, CRM operation, or file
action. Use parameters for changing values. Review the operation's output shape
and the next step's input requirements before connecting them in a pipeline.

Refreshing metadata and running an operation are different actions. A refresh
updates cached information; it does not automatically change or execute a
pipeline. A write operation can change the connected system.

## Use it in a pipeline

Bind the pipeline's logical data-source reference to this connection in the
selected project environment. Validate the pipeline, deploy a version, and
inspect its run when you execute it. See [How Braidplane works](../getting-started/overview.md)
for the full sequence.

## Use the examples

The JSON blocks on connector pages are **operation statements**, not complete
connection requests or complete pipeline definitions. In the app, choose the
connection's query editor and fill in the operation fields shown in the guide.
When using the API, put the statement in the request’s `statement` field and
supply the other required fields described by the [API reference](../api/index.md).
The `type` discriminator identifies the operation format; GitHub and GitLab
file operations both use `type: git`.

A placeholder such as `{{customer_id}}` is a parameter value. Declare that name
and its type in the saved query and provide a value when executing it. Do not
supply passwords or tokens as query parameters. `{{input}}` is the pipeline’s
upstream payload, and `{{runtime.*}}` values are runtime-provided context, not
ordinary user-defined query parameters.

For a standalone read, supply the example’s parameters and inspect the returned
records. For a standalone write, use the explicit synthetic inline content or
parameters shown on its page. A statement that reads `FROM input` or binds
`{{input}}` requires pipeline context unless you explicitly provide an equivalent
supported binding through the API. These examples do not create destination
tables, buckets, unique CRM properties, or provider permissions.

For a pipeline, save the operation with a recognizable name, refer to its slug
using `operation_ref`, and set the documented `operation_type`. Bind its logical
`data_source_ref` in the target project environment. Declare input/output
contracts matching the example fields, then validate and deploy the pipeline.
The [optional worked example](../getting-started/first-pipeline.md) demonstrates
the complete definition and execution lifecycle with explicit prerequisites.

### Decide write and replay behavior

Read examples leave the provider unchanged. Write examples can insert records,
replace files, or push commits. Test them where the provider-side effects are acceptable and can be verified.
Before using a write as a sink, select the replay behavior supported by that
connector and operation:

- `naturally_idempotent`: repeating the operation has the same intended effect;
  this requires an operation-specific guarantee, not just a connector flag.
- `idempotent_with_key`: stable keys identify repeated writes, as required by
  HubSpot upserts.
- `deduplicated_by_ledger`: a recorded success is skipped on replay; an
  in-progress attempt has an unknown outcome and is refused. The ledger does
  not make an arbitrary remote write atomic.
- `unsafe`: after an attempt is claimed, automatic retry and operator replay
  are refused because repeat safety is unproven.

| Connector | Supported sink replay classifications |
|---|---|
| PostgreSQL, MySQL, MongoDB, Generic Git, S3 | naturally_idempotent, deduplicated_by_ledger, unsafe |
| BigQuery | naturally_idempotent, idempotent_with_key, deduplicated_by_ledger, unsafe |
| Snowflake, FTP, GitHub, GitLab | deduplicated_by_ledger, unsafe |
| HTTP | idempotent_with_key, deduplicated_by_ledger, unsafe |
| HubSpot | idempotent_with_key |

Support in this table permits a classification; it does not prove that your
particular SQL, request, or file write satisfies it. For example, a plain SQL
INSERT is not naturally idempotent. Check the destination independently after
execution rather than treating validation as proof that data was written.

## Connector setup guides

Choose a system in the [connector directory](index.md) for its complete guide.

## Transport encryption

HTTP, OpenAPI, and S3 endpoints require HTTPS. MySQL and MongoDB require verified
TLS; PostgreSQL requires `ssl_mode: verify-full`. FTP uses explicit FTPS for both
control and data channels. The configured hostname must match a certificate
trusted by the runtime. HTTPS redirects cannot downgrade to HTTP. Git remotes
use HTTPS or SSH with certificate or host-key verification.

Private-host access does not relax encryption. Plaintext local fixtures require
an explicit operator development policy, which staging and production reject.
Update existing insecure endpoints or SSL modes before testing or running them.
