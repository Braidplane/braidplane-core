# Snowflake

Execute parameterized warehouse reads and explicitly authored append-style
INSERT operations. Source results are rows; destinations must already exist.

Use the [shared connection workflow](connect-a-system.md) to create this data
source in your workspace. You can read this guide without an account or
provider resources. Running examples requires provider resources appropriate for your evaluation.

## Connection settings

| Field | Required | Meaning |
|---|---|---|
| `account` | Yes | Snowflake account identifier |
| `warehouse` | Yes | Warehouse used for execution |
| `database` | Yes | Database namespace |
| `schema` | Yes | Schema namespace |
| `role` | WIF | Dedicated session role |
| `wif_objects` | WIF | Explicit existing table names and SELECT or INSERT privileges |
| `connect_timeout` | No | Connection timeout |

## Authentication and permissions

Password authentication (`auth_type: password`) is available with a username
and password wherever your Snowflake policy permits it.

For workload identity (`auth_type: workload_identity`), save the connection with
an exact account identifier, a dedicated role, and explicit source `SELECT` or
sink `INSERT` table selections. Use uppercase for ordinary unquoted Snowflake
object names. Enter names without SQL quoting or qualification; dots and spaces are treated
as part of the exact identifier. Wildcards and future grants are unavailable.

Braidplane provisions the identity automatically. The saved connection stays
inactive until verification. Open the onboarding panel, review its public issuer,
connection-specific subject, account-specific audience, and generated SQL script.
Your Snowflake administrator performs the one external setup action: run that
script in the configured account. Braidplane never requests administrator
credentials or runs administrative SQL.

After identity readiness, select **Verify**. Failures distinguish identity
provisioning, Snowflake trust, role/warehouse usage, and selected table grants.
Use **Retry status** to refresh provisioning. Changing the configuration requires
another Verify. The script preserves existing users and grants on rerun; review
any existing-name conflicts, and use a dedicated role without broader privileges.

**Disable identity** revokes access for new logins. Your Snowflake administrator
must terminate already-open sessions for immediate access removal. **Enable
identity** provisions again and requires another successful Verify.

Each saved connection has its own subject. Account identifiers must be lowercase
letters, digits, hyphens, and dots, not URLs or `snowflakecomputing.com` hostnames.
An unsaved draft cannot use WIF. Tokens and token-file paths are never customer
inputs. OAuth and key-pair JWT remain unavailable.

Use a source role with warehouse usage and read access only to the selected
schemas/tables. Set `read_only: true`. Use a separate writable connection with
only the destination INSERT privileges. Credentials are encrypted at rest and
are not returned in API responses.

## Test and discover

Customer WIF verification authenticates and checks the selected direct role and
table grants without writing test rows. It does not validate every query or row
policy; run your intended read or append after activation. Password tests ping
the configured session. Refresh reads
visible column metadata and exposes tables as `schema.table`. Missing metadata
usually requires checking the selected role, database, and schema visibility.

## Read example

Create or choose a `PUBLIC.CUSTOMERS` table containing `ID` and `EMAIL`. Define
an integer `customer_id` parameter. A matching record returns one row;
unquoted Snowflake identifiers normally appear in uppercase.

```json
{
  "type": "snowflake",
  "sql": "SELECT ID, EMAIL FROM PUBLIC.CUSTOMERS WHERE ID = {{customer_id}}"
}
```

## Write example

Create `PUBLIC.CUSTOMER_EXPORT` with compatible columns. Save this as a pipeline
sink receiving rows with `ID` and `EMAIL`. The operation appends those rows.
Select a ledger-backed replay policy or the `unsafe` classification, which
refuses replay after an attempt is claimed;
this connector does not offer native merge/upsert helpers.

```json
{
  "type": "snowflake",
  "sql": "INSERT INTO PUBLIC.CUSTOMER_EXPORT (ID, EMAIL) SELECT ID, EMAIL FROM input"
}
```

## Operation reference and pipeline behavior

The statement fields are `type: snowflake` and `sql`. Pipeline steps use
`operation_type: snowflake`. Values use `{{parameter_name}}` placeholders;
identifiers cannot be parameterized. An INSERT sink reads upstream rows through
`input`, which Braidplane supplies between the INSERT target and SELECT.

Watermark source contracts can bind
`{{runtime.incremental.lower_bound.value}}` and
`{{runtime.incremental.upper_bound.value}}`. The SQL must filter on those bounds
and return the configured field, including handling an empty first checkpoint.

## Limits and troubleshooting

- No staged loads, `COPY INTO`, Snowpipe, DDL/schema management, merge/upsert
  helpers, or public result pagination.
- PrivateLink/custom endpoint routing is not exposed. Workload identity is
  deployment-dependent, not automatically available to every organization.
- Authentication failure: verify the selected authentication mode and account.
  A workload-identity error may require coordinated deployment setup.
- Execution failure: check warehouse availability, role privileges, identifier
  casing, table existence, and upstream column types.
- Operations are bounded by execution time, rows, and payload size. A successful
  connection test does not prove an INSERT will succeed.

See [operation and pipeline basics](connect-a-system.md#use-the-examples) for
where to use these statements, parameters, environment bindings, and replay
safety. Return to the [connector directory](index.md).

Run the generated SQL as one complete block in a Snowsight worksheet. The administrator needs authority to create the user, role, and schema-scoped authentication policy, and issue the selected grants. Inspecting an existing service user requires user `MONITOR` and visibility of its type through user `OWNERSHIP` or account `MANAGE GRANTS`. Assigning the policy separately requires `APPLY AUTHENTICATION POLICY` on the user or account; `MANAGE GRANTS` alone does not authorize that assignment. See [Snowflake authentication-policy privileges](https://docs.snowflake.com/en/user-guide/authentication-policies). Before assigning its authentication policy or role, the block checks an existing user against the exact service type, issuer, subject, and audience; a conflict stops the block without changing that user. Existing roles and policies are preserved.
