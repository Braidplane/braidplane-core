# Connector support evidence

The public [support matrix](../docs-public/connectors/support-levels.md) owns
classification criteria, maintenance ownership and promotion records.

Support levels and structured capability details flow through the existing catalog/projection into
`braidplane data-source capabilities` (offline, installed-build facts).
Capability presentation in creation, detail and Builder UI is deferred to
a future creation UX rework. No new HTTP API fields or
capability endpoint are required by this classification change. Existing API
operation validation continues to enforce catalog capabilities; evidence,
maintenance ownership and promotion records remain documentation, not new
mandatory persisted storage fields. Separate/version-skewed clients must not infer that
their local catalog describes a different server build.

`Descriptor.Capabilities` owns read/write operation identifiers, load semantics,
source payloads, per-representation sink binding modes, pagination scope and
methods, throttling, auth modes, renewal, recovery and limitations. Shared neutral
types live in `pkg/model`; `spec.AllConnectorCapabilities` deep-copies them.
CLI JSON exposes these as `connectors[].capabilities`; terminal output uses
the same projection. Load modes
describe existing operation semantics, not new request fields or provider helpers.
An unlisted operation/payload is unsupported. Binding fallback never establishes
native binary ingestion, automatic row conversion or a provider-level load API.

Tests must retain provider-specific distinctions: BigQuery append versus keyed
upsert, S3 listing versus extraction pagination, MongoDB JSON output and exposed
statement operations, Workspace resource-specific writes and managed versus
manual Git authentication. Capability facts establish neither provider acceptance
nor permission for a configured connection.

## Deterministic coverage

The [public matrix](../docs-public/connectors/support-levels.md#evidence-record)
records provider verification separately from deterministic coverage. Relevant
source-owned coverage includes:

| Scope | Deterministic evidence entrypoints |
|---|---|
| SQL / database | [shared SQL harness](../service/datasourcessvc/connector/sql/harness_test.go), [PostgreSQL E2E](../service/datasourcessvc/connector/sql/postgres/e2e/postgres_test.go), [MySQL E2E](../service/datasourcessvc/connector/sql/mysql/e2e/mysql_test.go), [MongoDB E2E](../service/datasourcessvc/connector/mongo/e2e/mongo_test.go) |
| BigQuery | [adapter normalization](../service/datasourcessvc/connector/bigquery/adapter_test.go), [fake client](../service/datasourcessvc/connector/bigquery/client/client_test.go) |
| Snowflake | [SQL connector](../service/datasourcessvc/connector/sql/snowflake/connector_test.go), [identity onboarding](../service/datasourcessvc/connector/sql/snowflake/client/onboarding_test.go) |
| HTTP | [next-link policy](../service/datasourcessvc/connector/http/next_link_test.go), [numbered pages](../service/datasourcessvc/connector/http/page_number_test.go) |
| HubSpot | [adapter](../service/datasourcessvc/connector/hubspot/adapter_test.go), [connector](../service/datasourcessvc/connector/hubspot/connector_test.go) |
| Workspace | [Sheets](../service/datasourcessvc/connector/googleworkspace/connector_test.go), [Drive](../service/datasourcessvc/connector/googleworkspace/file_test.go), [Docs/Slides](../service/datasourcessvc/connector/googleworkspace/native_test.go) |
| Files / objects | [FTP writes](../service/datasourcessvc/connector/ftp/connector_write_test.go), [Git reads](../service/datasourcessvc/connector/git/connector_read_test.go), [S3 client](../service/datasourcessvc/connector/s3/client/client_test.go) |

These are coverage entrypoints, not a claim that every readiness failure is
tested or that this source review reran the connector suites. Ordinary tests
use owned fakes; dependency-backed tests remain in `e2e`. Review the applicable
tests and the exact candidate's results before promoting a scope.
