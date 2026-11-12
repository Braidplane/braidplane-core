# Connector support levels

Braidplane's connectors are in Alpha: preview, except experimental FTP. None
currently has a production-ready classification. Check the operation,
authentication mode and environment you intend to use in the table below.
Reads and writes can require different permissions and have different limits.

## Levels and qualifiers

| Level | Required evidence and intended use |
|---|---|
| Unclassified | No accepted classification. Treat the operation as unverified. |
| Experimental | Available for bounded evaluation; a readiness gate remains unresolved. Name the gap before use. |
| Preview | Documented bounded reads/writes, deterministic coverage, runtime limits and explicit accepted limitations. Suitable for an Alpha evaluation with disposable or approved data. Live acceptance may remain unverified. |
| Supported | Every applicable readiness gate has a reviewed answer; UI/CLI setup, execution and diagnosis work for the claimed scope; dated provider evidence covers the stated auth mode, operations and recovery. Maintenance ownership is explicit. |
| Production-ready | Supported criteria plus release-specific, repeatable live canaries, renewal/revocation, failure/replay and schema-change evidence in the intended deployment. Operational ownership, monitoring and recovery are accepted. Requires an explicit promotion; never inferred from test success. |
| Deprecated | Still available only within a documented existing scope, with a replacement or removal plan. No currently shipped connector has this level. |

Independent qualifiers describe scope, rather than raising a level:

| Qualifier | Meaning |
|---|---|
| Source-only | Reads are implemented; sink writes are unavailable. A read-only connection can also restrict a sink-capable connector. |
| Sink-capable | Specific writes are implemented. Load mode, permissions and replay policy still constrain each operation. |
| Provider-limited | Provider scopes, quotas, API behavior, app approval or deployment setup restrict the documented scope. |
| Unsupported | The operation/auth/payload/mode is unavailable. Selectors and validation must not offer it as executable. |
| Unverified | Implementation or historical evidence exists, but the specified provider/environment/version combination has no qualifying verification record. |
| Planned | No shipped native connector. Reads and writes are unsupported today; generic HTTP access is a separate customer-authored integration. |

## Shipped scope

These classifications describe build capabilities, not the status of a saved
connection. The transport descriptions were checked against the current Core
source on 6 November 2026; no connector was promoted by that review. All
listed types are sink-capable; none is source-only at the type level. FTP remains experimental; its current transport uses explicit FTPS.
Other types are preview with the limits below. The CLI's
`braidplane data-source capabilities --output text` and JSON output consume
the same catalog facts used for operation eligibility in the UI and CLI.
The command describes the installed CLI build, which can differ from a server.
Its JSON `connectors[].capabilities` contains read/write operations, load modes,
source payloads, sink payload binding modes and limits, pagination scope/methods,
throttling, authentication, renewal and recovery. Text output uses the same
facts. Capability presentation in creation, detail and Builder UI is deferred
to the creation UX rework. Unlisted operations or payloads
are unsupported. Load modes describe existing operation semantics; they are not
additional request parameters or automatic provider helpers.

| Connector | Level | Reads and payloads | Writes / load modes | Incremental and pagination | Auth/setup and recovery limits |
|---|---|---|---|---|---|
| [PostgreSQL](postgresql.md) | Preview | Parameterized SQL → rows | Authored SQL insert/update/upsert; rows | Watermark; no automatic result-page traversal | Static database credentials; customer rotates and grants least privilege. Private hosted access requires an operator-pinned endpoint. |
| [MySQL](mysql.md) | Preview | Parameterized SQL → rows | Authored database-native SQL; rows | Watermark; no automatic result-page traversal | Static database credentials; customer rotates and grants least privilege. |
| [BigQuery](bigquery.md) | Preview, provider-limited | Query jobs → rows | `insert_rows` append, not merge/upsert or bulk load | Watermark; bounded internal query-result pagination | Service account or managed Google OAuth; IAM and billing required. Testing-mode OAuth expires after seven days; reconnect is required. |
| [Snowflake](snowflake.md) | Preview, provider-limited | Parameterized SQL → rows | Authored append-style `INSERT`; no `COPY INTO` or merge helpers | Watermark; no public result pagination | Password where policy permits, or saved-connection customer WIF. WIF needs operator infrastructure and customer-admin grants; unsaved draft WIF tests are unavailable. |
| [MongoDB](mongodb.md) | Preview | Find/aggregate → JSON document array; count metadata | Explicit insert/update/delete; structured bindings; no replace/upsert option | Watermark; no change streams or automatic pagination | Static database credentials; customer owns roles and rotation. |
| [HTTP](http.md) | Preview, provider-limited | Explicit requests; JSON/CSV/file/form payloads | Explicit request body; JSON/CSV/file/multipart; provider defines append/upsert semantics | GET cursor/page/next-link traversal; cursor or timestamp watermark | Static headers/API key/bearer/basic, or customer OAuth app. Customer owns endpoint, scopes, API version and operation semantics; reconnect after revocation. |
| [HubSpot](hubspot.md) | Preview, provider-limited | List/search contacts, companies, deals and discovered custom objects → rows | Unique-key upsert with explicit field mapping; no generic create/delete sink | Cursor; list/search pagination and bounded throttling | OAuth or private app token. More scopes require reconnect, not token refresh. Custom-object entitlement is account-specific. API `2026-03`. |
| [FTP](ftp.md) | Experimental, provider-limited | CSV/JSON/XLSX → rows; raw file | File writes; CSV/JSON/XLSX serialization or raw file; no multipart | Full read only; no incremental checkpoint | Username/password over explicit FTPS with verified control/data TLS. Plaintext requires an explicit local development policy. No implicit FTPS or SFTP. |
| [Git](git.md) | Preview | Repository CSV/JSON/XLSX → rows; raw file | Explicit repository file writes/commits | Full read only; no issues/PR extraction | Manual HTTPS/SSH credentials; customer rotates credentials and maintains host trust. HTTPS redirects are rejected. |
| [GitHub](github.md) | Preview, provider-limited | Shared Git repository-file runtime | Repository file writes where installation permissions allow | Full read only; no native issues/PR extraction | Organization GitHub App installation and selected repository; access removal requires explicit repair/reconnect. |
| [GitLab](gitlab.md) | Preview, provider-limited | Shared Git repository-file runtime | Repository file writes where grant permissions allow | Full read only; no native issues/merge-request extraction | Organization OAuth connection and verified project; grant expiry/revocation requires recovery. Manual project-token repositories use Generic Git. |
| [S3](s3.md) | Preview, provider-limited | CSV/JSON/XLSX → rows; raw object/file | Object put/overwrite, not record merge; no multipart form sink | Full read only; pagination for object listing only | Static access keys, optional session token. Customer rotates keys/session credentials; automatic AssumeRole renewal is unavailable. |
| [Google Workspace](google-workspace.md) | Preview, provider-limited | Selected-file Sheets ranges → rows; Drive JSON/CSV/file; native Docs/Slides structured reads/PDF | Exact-range RAW Sheets update; Drive create/update; blank native document creation and text replacement | Full read only; Drive discovery pages; no CDC | Managed selected-file `drive.file` OAuth and Picker. Folder selection does not grant existing children. Testing-mode grants expire after seven days. Native Docs/Slides have bounded historical staging acceptance; current-release acceptance is unverified. |

No listed connector implements general CDC. A watermark/cursor must be applied
by the authored operation; it is not a complete synchronization guarantee.
Deletes, late updates, ordering and schema drift require an explicit workflow
contract. A lookback can reread late updates; it does not deduplicate writes.
SQL upserts and MongoDB writes require operation-specific replay evidence.

For file-backed XLSX, sources select one worksheet/range; sinks create workbook
output. Formula evaluation, preservation of existing workbook artifacts,
OpenDocument editing and multi-sheet merge are unsupported.

Connection tests prove the configured probe only. Schema refresh imports bounded
metadata into a cache; opening a detail page does not call the provider.
Missing metadata never grants write permission. Each linked guide defines exact
operation fields and payload constraints.

### Runtime and delivery limits

Default shared limits are 30 seconds, 1,000 normalized rows, 10 MB result size
and four active operations per organization/data source. Effective deployment,
query and provider limits can be lower. HTTP request/response caps and
connector-specific limits apply independently; exceeding a limit is a failure,
not successful truncation. Cancellation after a write can leave an external
effect even when Braidplane reports failure.

The catalog declares permitted replay mechanisms, not proof that every operation
is idempotent. A saved sink must choose a permitted strategy and supply the
required evidence. `unsafe` permits no replay guarantee; a ledger skips known
confirmed deliveries but does not prove an uncertain delivery absent. Verify
remote state before repeating ambiguous writes. Provider rate-limit retries
must remain within the execution deadline; they never imply unlimited retry.

## Evidence record

The source review above used existing test records without running live provider
tests. The table below lists those records and their scope. Exact provider
versions are not recorded here except for HubSpot's pinned API.

| Scope | Available evidence | Last provider verification: date / environment / exact version |
|---|---|---|
| PostgreSQL, MySQL, MongoDB | Hermetic statement/policy tests and local dependency-backed connector suites | Unverified here; local database fixtures do not prove a hosted customer deployment. |
| BigQuery | Owned fake-provider query, normalization, pagination, auth and failure tests | Unverified here; no live BigQuery acceptance claimed. |
| HTTP | Owned fake-server request, OAuth, next-link, throttling and retry tests | Unverified for each customer's API and authored operation. |
| HubSpot | Owned fake-provider discovery, list/search/upsert, auth and throttling tests | Unverified here for live CRM reads/upserts. Metadata-only Proof is narrower evidence. |
| FTP, Git/GitHub/GitLab, S3 | Local/fake file/repository/object-store suites, payload and safety tests | Unverified here for exact provider, auth and workflow. Repository authorization or object metadata alone cannot qualify writes. |
| Google Workspace Sheets/Drive | Recorded synthetic selected-file Alpha acceptance and cleanup | Historical scope: selected-file reads/writes, scheduling, read-only enforcement, revocation/recovery and metadata-only Proof. Exact last run date, candidate, environment and provider version are unverified in this matrix. |
| Google Workspace Docs/Slides | Deterministic native-operation coverage and recorded selected-file acceptance for both native types | Historical acceptance completed 17 October 2026 UTC: structured reads, blank creation, exact text replacement and PDF export under selected-file `drive.file` OAuth. Exact candidate, environment and provider versions are unverified in this matrix. Current-release acceptance remains unverified. |
| Snowflake | Deterministic SQL/onboarding coverage; recorded isolated live WIF acceptance and hosted source/sink acceptance | Historical isolated/hosted scope includes source/sink identities, renewal, independent revocation and signing-key retirement. Exact last run date, candidate and provider version are unverified in this matrix. |

Use the recorded operation, authentication mode and version when comparing test
results. Metadata-only Proof checks metadata; test extraction, delivery and
recovery through the corresponding operations.

A promotion record must include the exact Braidplane commit/release, provider
API/protocol version (or an explicit non-versioned service), UTC test date,
environment, objects/operations, auth/renewal/revocation scope, pagination and
throttling, full/incremental/CDC behavior, deletes/late updates/schema changes,
load/replay/recovery behavior, evidence links and the remaining limitations.
An absent field means **unverified**, not an implied pass. Store detailed
evidence in the owning issue or authorized artifact; keep secrets and customer
payloads out of public records.

## Maintenance ownership

| Responsibility | Maintained native connector | Customer-authored HTTP operation |
|---|---|---|
| Credential storage and safe runtime injection | Braidplane | Braidplane |
| Managed token refresh and failure diagnostics | Braidplane for implemented OAuth/WIF modes; provider controls expiry/revocation | Braidplane for supported OAuth grants; customer registers and maintains the provider app |
| Consent, resource grants, static rotation and reconnect | Customer administrator; provider enforces grants; deployment operator owns configured provider app | Customer administrator and provider |
| Provider API-version changes | Braidplane updates its native client and fixtures; provider owns API policy | Customer updates paths, request shapes, version headers and response interpretation |
| Pagination and throttling | Braidplane within the documented connector policy; provider owns quotas | Customer selects correct extraction/pagination policy; Braidplane enforces implemented bounds and retries |
| Schema drift | Braidplane refreshes/maps supported metadata and reports failures; customer reviews affected operations/contracts | Customer maintains schema/operation mapping; Braidplane validates supported shapes |
| Business mappings, keys and duplicate semantics | Customer authors and approves; Braidplane enforces declared contracts/replay policy | Customer authors and qualifies provider semantics; Braidplane enforces declared policy |
| Ambiguous writes and recovery | Customer verifies remote effects and authorizes replay; Braidplane supplies retained evidence and supported recovery | Same; a generic request cannot infer provider rollback or deduplication |
| Deployment, egress and monitoring | Deployment operator; hosted Braidplane operators own hosted infrastructure | Same |

## Planned portfolio

These connectors are planned and are not available yet. The table lists the
proposed first operations for each system.

| Candidate | Intended first scope |
|---|---|
| SQL Server | Source and sink |
| SFTP | Source and sink. FTP and Git SSH do not supply SFTP. |
| Salesforce | CRM reads and keyed upsert |
| Stripe | Revenue data; qualify writes per workflow |
| Shopify | Commerce data; qualify writes per workflow |
| Google Analytics 4 | Reporting source-only; configuration writes excluded |
| Zendesk | Support data |
| Intercom | Contacts, companies and conversations |
| Jira Cloud | Projects/issues/bounded changelogs |
| Notion | Structured source |
| Pipedrive, Zoho CRM | CRM reads and approved updates/upserts |
| Microsoft Dynamics 365 Sales | Dataverse reads and approved updates |
| Oracle NetSuite, SAP S/4HANA | ERP reads and bounded approved writes |
| Odoo, Attio, monday CRM | Reads and bounded approved updates |
| OpenMercato | Discovery through shared HTTP, not a shipped native connector |
| Google Ads, Meta Ads, QuickBooks, Xero | Discovery candidates only; no accepted native operation contract |

## Gaps and promotion

Existing preview operations can be implemented and evaluated without qualifying
the entire portfolio. Promotion applies only to the stated operation/auth scope.

* Every preview scope needs a current, exact-candidate provider record and a
  reviewed readiness checklist before moving to supported. Historical evidence
  above has missing dates/versions; link or rerun bounded acceptance to fill them.
* Workspace public OAuth approval is separate. Docs/Slides need their
  own current-candidate hosted acceptance; the historical native record and
  Sheets/Drive acceptance cannot substitute.
* FTP's explicit FTPS implementation still needs provider acceptance for each
  claimed workflow. SFTP is a separate planned connector; do not represent it
  as an FTP option.
* S3 automatic AssumeRole lifecycle and advanced object options remain future work.
  Static credentials remain the current documented scope.
* BigQuery additional auth/load helpers remain future work. Snowflake bulk loads,
  merge helpers, OAuth/key-pair modes and public pagination remain unsupported.
* Advanced file spreadsheet behavior, HTTP health and spreadsheet modes,
  and per-record HTTP delivery/recovery remain future work.
* CDC strategy remains future work. Shared checkpoints alone cannot qualify CDC,
  delete propagation, late-update completeness or automatic synchronization.

For each connector PR, review connection tests (including draft and stored
credential changes), introspection/cache failure behavior, parameter binding,
credential encryption/redaction and renewal/revocation, source/sink enforcement,
payload compatibility, pagination/throttling, cancellation/size/time/concurrency
limits and replay/recovery. Require meaningful deterministic success and failure
coverage. Each applicable item needs implementation/test evidence or an explicit
accepted limitation. Unresolved safety, auth lifecycle or write semantics keep
that scope experimental or unsupported. Update catalog, UI/CLI projections,
connector guide and this matrix together when their public facts change.
