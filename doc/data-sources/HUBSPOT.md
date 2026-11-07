# HubSpot data source

## Purpose and best-fit use cases

Use the HubSpot connector when Braidplane needs read access or key-based upsert access
to standard or account-defined CRM records from a HubSpot account. It is intended for
SaaS-style account integrations where authentication, schema discovery,
pagination, and provider error handling must be handled with HubSpot-specific
behavior rather than a generic HTTP request definition.

## Transport security

Provider endpoints require HTTPS and provider clients refuse redirects. Operator plaintext mock endpoints require the explicit [local development policy](../DATA_SOURCES.md#transport-encryption); private-host access alone does not permit plaintext.

## Shared capabilities

HubSpot supports the shared data-source lifecycle, CRM read and upsert
execution, cursor pagination, pipeline source roles, and
`idempotent_with_key` pipeline sink roles. See the [capability matrix](../DATA_SOURCES.md#capability-matrix)
and [payload compatibility matrix](../DATA_SOURCES.md#pipeline-payload-compatibility-matrix)
for the canonical cross-connector contract.

## Required config fields

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `timeout_seconds` | no | integer | 5 | Request timeout in seconds (1–300) |

The connector targets HubSpot's latest stable date-versioned API at the time of implementation: `2026-03`. This version is pinned internally and is not user-configurable in v1.

## Credentials

| Credential mode | Required | Notes |
|---|:---:|---|
| `private_app_token` | one of | Static HubSpot private app token used as a bearer token |
| `oauth2_grant` | one of | Product-managed OAuth2 grant installed through the HubSpot provider flow |

HubSpot private app tokens are `static_secret` credentials owned by
`service/credentialssvc`. They are encrypted at rest, resolved only as
short-lived in-memory connector credentials, and never returned in API
responses. See [DATA_SOURCES.md — Shared credential lifecycle](../DATA_SOURCES.md#shared-credential-lifecycle).

HubSpot OAuth2 is the recommended product-managed `oauth2_grant` credential.
The Braidplane deployment owns the developer-app client ID, client secret, and
exact callback URL; the customer installs that app in their HubSpot portal. The
operator source for the client secret is the Proton-primary
`hubspot.oauth.staging` capability;
the deployment secret workflow materializes it without adding it to source
control.
Use `hubspot.oauth_redirect_uri` ending in `/provider-connectors/hubspot/callback`.
Non-local deployments require a public HTTPS URL; localhost HTTP is only for
local testing. The first install requests HubSpot's required `oauth` scope and
contacts read access. Companies, deals, and custom-object reads plus contacts,
companies, deals, and custom-object writes are optional. Braidplane stores
HubSpot's returned scope list as safe grant
metadata, so a connected account can remain usable for contacts when optional
CRM object scopes were not granted. Reinstall or rotate the grant to request
the current scope plan again.

New HubSpot sources can start with OAuth2 directly. Braidplane first persists
the inactive source with a real `oauth2_grant` credential owner in `pending`
state and stores no encrypted credential payload or static-secret substitute.
It then returns the HubSpot authorization URL. Only a callback that passes the
single-use state, authenticated-session, organization, permission, provider,
and connection checks activates the grant and source. Cancellation, denial,
expiry, or callback failure leaves the source inactive and recoverable; the
operator can restart authorization without entering a private-app token.

Private app tokens remain an explicit static-secret alternative. They are not
used to bootstrap the OAuth2 path.

The connector uses a private app token only when no OAuth grant record exists.
Once an OAuth grant exists, its access token takes precedence and the connector
does not fall back to the static token if that grant is revoked; reinstall or
rotate the OAuth grant to restore HubSpot access. The static credential remains
stored until it is explicitly replaced or revoked.

## Connection testing

Connection testing uses a low-risk HubSpot account details endpoint. Static
token creation verifies the token before saving. OAuth-first creation saves an
inactive pending source, then verifies the exchanged token during the callback
before activating that source.

## Schema introspection

The connector does not crawl the entire public HubSpot API surface. It exposes
a fixed operation catalog for the supported standard CRM objects and discovers
account-specific property metadata for:

- `contacts`
- `companies`
- `deals`

The introspection result uses the shared `object_api` schema family. It includes
the object name, object label, supported operations, field names, field labels,
and HubSpot property types. The HubSpot adapter also exposes the provider record
identifier as the `hs_object_id` field because execution rows always include it.
This lets Braidplane surface account-specific fields without treating the whole
provider API surface as dynamic schema.

Schema introspection also discovers active account-defined custom objects from
HubSpot's schema endpoint. Each custom object is identified by its immutable
provider `objectTypeId` (for example `2-7282133`) and includes its current
plural label, provider name, property metadata, unique-property flags, and the
operations allowed by the active grant. Archived schemas are omitted.

## Query and operation model

Queries use a dedicated `HubSpotStatement` with these core fields:

- `object_type` — `contacts`, `companies`, `deals`, or a discovered custom
  object `objectTypeId` such as `2-7282133`
- `operation` — `list`, `search`, or `upsert`
- `properties` — projected property names
- `query` — optional free-text search input
- `filter_groups` — optional HubSpot search filters
- `sorts` — optional HubSpot search sorts
- `limit` — page size, maximum `200`
- `after` — pagination cursor for the next page
- `id_property` — the stable HubSpot unique property for `upsert`
- `property_mapping` — explicit input-field to HubSpot-property mapping for
  `upsert`
- `rows` — row object or row array, normally `{{input}}` in a pipeline sink

Parameter placeholders use the shared `{{param_name}}` syntax and can be used
in projected properties, search query text, pagination cursors, filter values,
sort expressions, and the upsert row binding.

Execution returns the shared `object_rows` result family with:

- `hs_object_id`
- requested HubSpot properties
- `row_count`
- `column_names`
- `next_cursor`
- `has_more`

Upsert execution returns `hs_object_id`, `created`, and the aggregate row count.
It never returns submitted property values.

## Pipeline relevance

**As a source**: the pipeline step uses `operation_type: hubspot`. Query execution returns rows that can flow into downstream transform steps.

HubSpot source steps support incremental `cursor` contracts for operations that
accept and return the HubSpot pagination cursor. The checkpoint token remains
opaque to cross-connector pipeline code. Saved and inline HubSpot operations can
set `after: "{{runtime.incremental.lower_bound.cursor}}"`; Braidplane observes the
next checkpoint from the normalized `next_cursor` result field and keeps cursor
values out of public run diagnostics.

Standalone connector YAML examples live in:

- `examples/pipelines/yaml/hubspot-to-mysql.pipeline.yaml`
- `examples/pipelines/yaml/postgres-to-hubspot.pipeline.yaml`

These examples remain separate from the execution-qualified public recipe collection.
See example ownership before copying or updating them.

The MySQL example uses an inline HubSpot source operation and a saved SQL sink
query. The Postgres example uses an inline SQL source operation and a saved
HubSpot upsert sink operation.

**As a sink**: use a saved HubSpot `upsert` operation, a writable HubSpot data
source, and `idempotency_class: idempotent_with_key`. The runtime binds upstream
rows to the operation's `rows` field and rejects read-only destinations before
connector dispatch.

## Read-only behavior

HubSpot data sources may be created as read-only sources or writable
destinations. Read-only sources permit `list` and `search` and reject `upsert`
before provider dispatch. Writable destinations still require the matching
provider object read and write scopes; setting `read_only: false` never grants
provider access.

To change an existing OAuth source from read-only to writable, select
**Write-enabled upsert destination** in its connection settings and save, then
use **Reconnect HubSpot** to request the expanded provider scopes. Changing the
stored access mode does not elevate the existing OAuth grant by itself.

Custom object IDs are validated against the provider's active schema list
before record dispatch. A syntactically valid but undiscovered, removed, or
inaccessible ID fails without becoming an arbitrary provider path.

## Expansion contract

HubSpot supports custom-object `list` and `search` reads plus CRM record upsert.
Schema mutation,
record archive/delete, association mutation, and arbitrary provider requests
remain unsupported.

### Object identity and discovery

- `contacts`, `companies`, and `deals` keep their existing stable names.
- Additional standard objects are added only through the fixed connector
  catalog after their required scopes, API availability, fixtures, and product
  use case are documented. Tickets, products, and line items are candidates,
  not supported objects.
- Custom objects are discovered from the account's CRM schema endpoint.
- A custom object is represented by its provider `objectTypeId`, not its
  mutable display label or short name. The ID is the operation-facing identity
  used in saved statements and cached schema metadata.
- Introspection records the object type ID, current labels, property metadata,
  unique-property flags, and supported Braidplane operations. It does not
  expose provider-internal responses or infer access to an object that the
  active credential could not inspect.
- Refresh replaces the discoverable custom-object set for that data source.
  Saved operations for a removed or inaccessible object remain stored but fail
  validation or execution with a typed unsupported/not-found error; they are
  never silently redirected by label.

Custom objects support the existing `list` and `search` statement
shape, pagination, parameter substitution, row normalization, and runtime
limits. Object type IDs are validated against refreshed schema metadata before
dispatch. A caller cannot use the custom-object field as an arbitrary HubSpot
path segment.

### Authentication and authorization

Static private-app tokens remain supported. OAuth2 remains the recommended
credential mode and uses the shared credential lifecycle.

Read and write access is derived from the granted credential, not from object
discovery alone. A source should use read-only scopes. A destination uses a
separate write-capable data source and least-privilege credential. Adding
required scopes to the connector never mutates an existing grant; the user must
explicitly reinstall or rotate OAuth authorization.

Static credentials can use reads and writes when the private app has the
required access. OAuth reads require the matching
`crm.objects.<object>.read` scope and upserts require both that read scope and
the matching `crm.objects.<object>.write` scope; custom objects use
`crm.objects.custom.read` and `crm.objects.custom.write`. Existing grants that do not contain a required scope
report a safe missing-scope error rather than falling back to a stored static
token. The OAuth install scope plan requests custom-object read and all
supported write scopes as optional. Existing OAuth sources must be reauthorized
to add them; token refresh does not elevate a grant's scopes.

### Write and sink behavior

The first write operation is `upsert`. It consumes row input and targets one
supported standard or discovered custom object.

Each saved upsert operation declares:

- the target object identity;
- an `id_property` that HubSpot marks unique for that object;
- an explicit mapping from input fields to writable HubSpot properties;
- `idempotent_with_key` as its sink replay strategy.

The identifier value comes from each input row. It is never generated
implicitly from row order, run ID, or mutable display fields. Contacts may use
`email` only where HubSpot supports it, but operations that require partial
upsert must use a custom unique identifier. Read-only, unknown, unmapped, and
provider-managed properties are rejected before dispatch.

The adapter batches at most the provider's documented maximum and applies
Braidplane request, response, row, concurrency, cancellation, and timeout
limits. It normalizes only safe record IDs and per-row outcomes. Submitted
property values, access tokens, raw provider bodies, and provider correlation
identifiers do not enter logs, traces, run metadata, or durable events.

HubSpot batch writes are not assumed atomic. A response containing any row
failure fails the sink step and records only a safe aggregate outcome. Replays
use the same unique keys and property mapping, so both confirmed and ambiguous
attempts are safe to repeat as `idempotent_with_key`; replay may update the same
records but cannot create a second record for the same declared unique key.

Create-only, update-by-record-ID, association changes, schema changes, merge,
archive, and delete are excluded. Each requires a separate contract because
its replay and destructive-action properties differ.

### Rate limits and provider failures

The provider client maps authentication, missing-scope, validation, not-found,
rate-limit, lock, timeout, and server failures into typed `xerror` values.

For reads, bounded retries may honor provider retry guidance while remaining
inside the effective execution deadline. For writes, `429`, `423`, `5xx`,
timeout, and transport failures fail the current connector attempt with a typed
error. The pipeline runtime may replay the sink using the same stable unique
keys. Daily or otherwise long retry windows fail with an actionable rate-limit
error rather than holding a worker.

### Testing and rollout gates

No automated test calls live HubSpot. Deterministic fake-provider coverage for
the expansion includes:

- custom schema discovery, refresh, rename, removal, and object type ID path
  validation;
- list/search success, pagination, missing scopes, authentication failure,
  rate limiting, cancellation, provider errors, and response bounds;
- upsert success, duplicate input keys, invalid or non-unique identifiers,
  unwritable properties, mixed batch results, ambiguous transport failure, and
  replay with stable keys;
- redaction of credentials, row values, raw provider errors, and correlation
  identifiers;
- organization-scoped data-source, schema, operation, and execution access.

Capability metadata declares HubSpot as both a source and an HTTP-family sink,
allows only `idempotent_with_key`, and does not force data sources read-only.

## Testing approach

Automated tests should not call the live HubSpot service. Connector coverage uses fake HTTP or connector-level integration tests against local mock servers so auth failures, pagination, rate limiting, and schema discovery remain deterministic.

## Implementation boundary

The reusable connector ownership rule is documented in
[DATA_SOURCES.md — Connector implementation boundary](../DATA_SOURCES.md#connector-implementation-boundary).
HubSpot is the first concrete application of that rule.

The HubSpot implementation is intentionally split across a neutral provider
boundary and a Braidplane adapter:

- `internal/hubspot/oauth` owns provider-owned consent, token exchange, refresh, and revocation. It receives the deployment-owned app client ID and secret, never a data-source credential record.
- `service/credentialssvc/provider/hubspot` is a thin lifecycle bridge that maps those OAuth protocol results into Braidplane credential grants.
- `internal/hubspot/crm` owns provider-facing CRM transport and primitives: bearer authentication, request construction, supported object metadata, account details fetches, per-object property fetches, paging, and provider error mapping. It receives only a portal access token.
- `service/datasourcessvc/connector/hubspot` is the Braidplane adapter layer. It assembles Braidplane connection testing and schema introspection from CRM primitives, and keeps `HubSpotStatement` decoding, `{{param_name}}` substitution, execution-limit coordination, and normalization into the shared Braidplane object-row execution result.

This keeps shared HubSpot protocol ownership reusable without leaking the OAuth
app secret into datasource runtime or pulling Braidplane query-editor and
result-envelope concerns into the provider transport layer.

## Published schema evaluation

Braidplane should keep the current handwritten narrow client for now.

Do not generate a raw internal HubSpot client from HubSpot's published OpenAPI specs.

Generated internal wire types are only worth revisiting later if the upstream spec usage question is cleared and the maintenance tradeoff becomes worth paying.

## Extraction path

If the provider client is later published or moved into a standalone module, the intended path is to keep Braidplane-specific behavior in the adapter package and lift the neutral `internal/hubspot` packages with the smallest possible surface: OAuth lifecycle, account details, supported objects, object property discovery, list, search, and batch upsert.

The main remaining Braidplane couplings that still block a clean external module split are:

- Braidplane's internal `xerror` typed error model
- Braidplane's internal `hostguard` SSRF protection helper
- request-level response-size guard fields that currently reflect Braidplane runtime limits
- repository-local mock and E2E wiring that currently assume Braidplane package paths

The provider client is separated from the Braidplane adapter. See the
[connector implementation boundary](../DATA_SOURCES.md#connector-implementation-boundary)
for the shared ownership rules.
Treat the bullets above as external-module extraction constraints, not active
architecture debt.

## Known limitations

- The connector is pinned internally to HubSpot API version `2026-03`; upgrading to a newer stable version requires a code change.
- Standard-object support remains limited to `contacts`, `companies`, and
  `deals`; additional standard objects are not inferred from the custom schema
  catalog.
- Custom-object reads and writes require a static credential with access or an
  OAuth grant containing `crm.objects.custom.read` and, for upsert,
  `crm.objects.custom.write`. Existing OAuth grants must be reauthorized to add
  newly optional scopes.
- `read_only` defaults to `true` when omitted. Set it explicitly to `false` for
  a writable HubSpot data source; writable OAuth installs request the matching
  optional CRM write scopes, while read-only installs omit them.
- Upsert is the only write operation. Create-only, update-by-record-ID,
  associations, merge, schema mutation, archive, and delete remain unsupported.
- The remaining staged behavior is documented in
  [Expansion contract](#expansion-contract).

See [connector support levels](../../docs-public/connectors/support-levels.md) for shipped connector scope, limitations and acceptance boundaries.
