# Snowflake data source

## First-slice contract

Snowflake is a warehouse connector for parameterized SQL reads and explicitly
authored SQL writes. It uses the official Snowflake Go driver and the shared SQL
execution envelope. The initial sink path is append-oriented `INSERT`; staged
loads, `COPY INTO`, table creation, schema evolution, merge/upsert helpers, and
Snowpipe are not supported.

## Configuration and credentials

Configuration requires `account`, `warehouse`, `database`, and `schema`.
`role` and `connect_timeout` are optional. The connection timeout defaults to
30 seconds and bounds session startup and connection verification separately.
Explicit values from 1 to 300 seconds override this default; caller deadlines
can end an operation sooner. Authentication is selected with
`credentials.auth_type`:

- `workload_identity` requires a ready identity for the saved organization,
  connection, and exact Snowflake account. Braidplane provisions its registration asynchronously through the identity
  control plane. Customer connections obtain a fresh
  short-lived OIDC identity through SPIRE when opening a provider session.
  Neither the token nor its path is accepted in customer data-source config or
  persisted credentials.
- `password` requires write-only `username` and `password` values encrypted
  through the shared credential service.

Password authentication is generally available where the customer's Snowflake
policy permits it. Customer WIF needs deployment-level identity infrastructure,
but no per-customer operator binding or SPIRE registration.

## Self-service workload identity onboarding

1. Choose workload identity, the exact lowercase account identifier, warehouse,
   database, schema, and a dedicated least-privilege role. Select existing source
   tables for `SELECT`, or sink tables for `INSERT`. Use separate source and sink
   connections; set `read_only: false` for a sink. Names are exact and case-sensitive:
   use uppercase for ordinary unquoted Snowflake objects. Enter table names without SQL quoting or qualification;
   dots and spaces are treated as part of the exact identifier. No wildcard or future grants are created. Select between 1 and 100 distinct table grants.
2. Save the connection. It remains inactive. The onboarding panel shows the public
   issuer, connection-specific subject, account-specific audience, and provisioning
   state. Braidplane creates and reconciles the identity automatically.
3. **The one customer-admin action:** review and run the generated SQL script in
   the configured Snowflake account. The administrator needs authority to create
   the dedicated service user, role and authentication policy, and grant usage and
   the selected table privileges. Braidplane never requests ACCOUNTADMIN credentials
   or executes this administrative SQL.
4. Select **Retry status** while identity provisioning is pending. Once ready,
   select **Verify**. Verification checks authenticated access, role selection,
   warehouse/database/schema usage and every selected table grant. Only a successful
   verification activates the saved connection. Configuration and WIF credential
   changes require another explicit Verify.

The script uses stable connection-specific user and policy names, `IF NOT EXISTS`,
and explicit grants. Rerunning it preserves existing users, policies, and grants;
review collisions or conflicting existing trust with your Snowflake administrator.
Use a dedicated role: the script does not remove pre-existing or previously selected
privileges. Verification requires the script's direct table grants, rather than accepting
broader inherited privileges. It reads grant metadata and never inserts test rows.
After verification, run your intended parameterized read to check its results
and row-access policies. Test appends in a dedicated table with synthetic rows.
Check the written rows, then remove the test data before running the pipeline.

**Disable identity** requests revocation and prevents new logins. Disable the
Snowflake service user and terminate its already-open sessions when immediate
provider access removal is required. **Enable identity** requests provisioning
again; Verify is required before reactivation.

The same flow is available through API and CLI:

```bash
braidplane data-source create --file snowflake.json
braidplane data-source snowflake-onboarding warehouse-source
# Review the response's sql, run it in Snowflake, then:
braidplane data-source test-stored warehouse-source
braidplane data-source update snowflake warehouse-source --active=false
```

A source request file contains:

```json
{"name":"Warehouse source","slug":"warehouse-source","type":"snowflake","read_only":true,"config":{"account":"acme-account","warehouse":"COMPUTE","database":"ANALYTICS","schema":"PUBLIC","role":"BRAIDPLANE_READER","wif_objects":[{"name":"ORDERS","privilege":"SELECT"}]},"credentials":{"auth_type":"workload_identity"}}
```

Use `--active=true` to request provisioning again. Update request files must include
`type`, `slug`, and `is_active`; the API accepts `{"is_active":false}` directly. The onboarding endpoint is
`GET /organizations/{slug}/data-sources/{ds_slug}/snowflake-onboarding`.
Verification uses the existing stored `test-connection` endpoint. API responses
contain only public claims, generated SQL, and safe diagnostics.

Customer bindings are checked before provider access. A missing binding, changed
account, unavailable identity, invalid claims, or invalid signature fails closed;
there is no password or shared-fixture fallback. Tokens remain in process memory
and are renewed for each new physical session, including sessions opened during
long operations. The staging token-file fixture remains a separate deployment
mode. See Snowflake WIF operations for onboarding,
rotation, revocation, and live validation.
External OAuth, Snowflake OAuth, and key-pair JWT are future architecture and
remain deferred.

Use separate least-privilege Snowflake users and roles for sources and sinks.
A read-only Braidplane data source should use a Snowflake role that can only use
the selected warehouse and read the selected database schemas. Sink roles should
only have the target-table privileges needed by their saved and inline `INSERT`
operations.

## Connection testing and schema refresh

Password/fixture connection testing opens the configured Snowflake session and pings it within
the configured timeout. Customer WIF verification additionally checks the selected direct grants.
A verification timeout leaves the connection inactive and does not establish that
grants are missing. Retry Verify or increase the connection timeout. Schema refresh reads visible columns from
`information_schema.columns` and exposes tables as `schema.table`.

## Queries and writes

Saved and inline operations use `operation_type: snowflake` and a statement:

```json
{"type":"snowflake","sql":"SELECT * FROM ANALYTICS.ORDERS WHERE REGION = {{region}}"}
```

Placeholder values are bound through `database/sql`; Braidplane never
interpolates values into SQL text. Identifiers cannot be parameterized.
Source results use the shared row-normalized SQL payload. Sink statements must
be deliberately authored `INSERT INTO ... SELECT ... FROM input` operations and
must follow the shared sink replay policy. Braidplane materializes `input` as a
Snowflake CTE between the `INSERT` target and its `SELECT` query.

Source steps support catalog-declared watermark checkpoints. Author the SQL
predicate with `{{runtime.incremental.lower_bound.value}}` and
`{{runtime.incremental.upper_bound.value}}`, and return the configured watermark
field. Checkpoints do not provide automatic pagination, CDC, deletes or late-update
completeness; the authored predicate and workflow define those semantics.

## Known limitations

Row and response-size limits stop result collection. Queries that exceed either
limit fail without returning rows. The response cap bounds retained connector
output; the Snowflake driver may allocate its own buffers before that check.

- Customer WIF requires pre-provisioned SPIRE infrastructure and a separate
  privileged reconciler.
  The customer administrator runs the generated script to configure Snowflake trust and grants. Accounts in bindings use exact lowercase identifiers (letters, digits,
  hyphens, and dots), not URLs.
- Customer WIF live acceptance covers isolated source and sink identities,
  renewal, independent revocation, and signing-key retirement.
- OAuth and key-pair JWT are not implemented.
- No live Snowflake dependency is required by normal local or CI tests.
- No public provider pagination or resumable partial-result contract.
- No staged file loads, bulk-copy helpers, merge/upsert helpers, DDL helpers, or
  automatic table/schema management.
- PrivateLink and custom endpoint routing are not exposed in the first slice.

Run the generated SQL as one complete block in a Snowsight worksheet. The administrator needs authority to create the user, role, and schema-scoped authentication policy, and issue the selected grants. Inspecting an existing service user requires user `MONITOR` and visibility of its type through user `OWNERSHIP` or account `MANAGE GRANTS`. Assigning the policy separately requires `APPLY AUTHENTICATION POLICY` on the user or account; `MANAGE GRANTS` alone does not authorize that assignment. See [Snowflake authentication-policy privileges](https://docs.snowflake.com/en/user-guide/authentication-policies). Before assigning its authentication policy or role, the block checks an existing user against the exact service type, issuer, subject, and audience; a conflict stops the block without changing that user. Existing roles and policies are preserved.
