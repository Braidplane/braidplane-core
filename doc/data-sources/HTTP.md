# HTTP data source

## Purpose and best-fit use cases

Use the HTTP connector to integrate with REST APIs, webhooks, or any HTTP/HTTPS endpoint that returns JSON. It is the right choice when you need to read from a third-party API, forward data to a webhook, or chain external service calls into a pipeline.

## Transport security

The base URL and OpenAPI URL require HTTPS. HTTPS redirects cannot downgrade to HTTP, including unauthenticated execution and schema import.
See the shared [transport encryption policy](../DATA_SOURCES.md#transport-encryption) for the local fixture exception.

## Shared capabilities

HTTP supports the shared data-source lifecycle, parameterized request execution,
and pipeline source/sink roles. See the [capability matrix](../DATA_SOURCES.md#capability-matrix)
and [payload compatibility matrix](../DATA_SOURCES.md#pipeline-payload-compatibility-matrix)
for the canonical cross-connector contract.

## Required config fields

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `base_url` | yes | string (URI) | None | Base URL of the HTTP service; must be `http` or `https` |
| `health_path` | no | string | `/` | Relative path used for the connection test (GET request) |
| `timeout_seconds` | no | integer | 5 | Request timeout in seconds (1 to 300) |
| `openapi_url` | no | string (URI) | None | OpenAPI document URL used for schema import and query draft authoring |
| `openapi_fetch_timeout_seconds` | no | integer | 10 | OpenAPI document fetch timeout in seconds (1 to 60) |
| `allow_private_hosts` | no | boolean | `false` | Requires private host access to be enabled for the deployment; hosted/default deployments reject `true` |

## Credentials

| Field | Required | Notes |
|---|:---:|---|
| `oauth` | no | Write-only customer OAuth app; mutually exclusive with API key, bearer and basic auth |
| `secret_headers` | no | Map of 1 to 8 named secret headers; names up to 128 bytes, values up to 4096 bytes each; encrypted and write-only |
| `api_key` | no | Write-only API key; requires `api_key_header` |
| `api_key_header` | no | `X-Api-Key` sends the key directly; `Authorization` sends `ApiKey <key>` |
| `bearer_token` | no | Added as `Authorization: Bearer <token>` header |
| `username` | no | HTTP Basic Auth username |
| `password` | no | HTTP Basic Auth password; required when `username` is set |

Configure at most one authentication method: named secret headers, legacy API key, Bearer, Basic, or OAuth. Mixed methods and incomplete key/header or username/password pairs are rejected. API keys belong in credentials, never saved operation headers. Create, pre-save test, and credential rotation forms accept API keys. The CLI accepts `--http-api-key` with `--http-api-key-header`; JSON request files also accept `api_key_file` relative to the request file. Authenticated requests, including Bearer and Basic, reject redirects to a different origin (scheme, host, or port). Credentials are encrypted at rest and never returned in API responses. See [DATA_SOURCES.md - Shared credential lifecycle](../DATA_SOURCES.md#shared-credential-lifecycle).

### Named secret headers

Use the same encrypted credential lifecycle for APIs requiring multiple keys:

```json
{
  "credentials": {
    "secret_headers": {
      "X-Api-Key": "<api-key>",
      "X-Api-Secret": "<api-secret>",
      "X-App-Key": "<application-key>"
    }
  }
}
```

Names are case-insensitive and must be unique HTTP header names. Values are sent
verbatim; `Authorization` needs the complete scheme and value, such as
`Bearer <token>` or `ApiKey <key>`. Empty values, line breaks and invalid header
characters are rejected. Transport, proxy, cookie, `Sec-*`, `Content-*`, `Accept`,
`Accept-Encoding`, `User-Agent` and `Expect` headers cannot be credential headers.
Configure ordinary headers such as `Content-Type` and `Accept` on the operation.
Operation headers cannot overlap named secret headers, regardless of case.
Authenticated execution, connection tests and same-origin OpenAPI imports use
the complete header set and refuse cross-origin redirects.

The UI uses one secret-header editor for API keys and custom authentication
headers. Header-name suggestions in the operation editor include standard names
from the [MDN HTTP header reference](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers);
secret-header suggestions include authentication names. Both allow custom names.
Stored secrets are never prefilled. Creation, draft testing and replacement use
the same fields; replacement requires the complete set. Inspection returns
credential lifecycle metadata, never values. If a provider echoes a configured
secret into a response header or body, execution withholds the response and
reports an uncertain acknowledgement; verify remote state before retrying.
The check also covers legacy API keys, bearer/OAuth tokens and Basic credentials.
OpenAPI imports inspect decoded JSON and YAML values. Short or common secrets
can match unrelated response text; other transformed representations are not detected.

For CLI create, test and credential replacement, use the same JSON request-file
`secret_headers`. To read secrets from files, use `secret_header_files` with
header names mapped to paths relative to the request file. Do not supply the
same header in both maps. Files contain the exact header value; line breaks,
including a trailing newline, are rejected with an actionable diagnostic. Existing `api_key`/`api_key_header` payloads and CLI
flags remain supported.

## OAuth setup

First register an OAuth app with the API provider. Choose the grant that fits:

| Method | Use for | App settings |
|---|---|---|
| Authorization code + PKCE | A person signs in and approves access | Issuer, authorization/token endpoints, client ID, scopes; secret for confidential apps |
| Client credentials | Service-to-service access without user consent | Issuer, token endpoint, client ID, secret and scopes |

### Connect in the UI

1. For authorization code, obtain Braidplane's callback URL from your deployment
   administrator and register it on the provider app. The administrator must
   configure it first; the data-source form does not display it.
2. Create an **HTTP** data source with the HTTPS API base URL and a GET health
   path inside it that tests the access you need.
3. Under **HTTP authentication**, select the OAuth method and enter the app
   settings. See the [field reference](HTTP_OAUTH.md#app-settings) for optional
   audience and revocation settings.
4. Save, then click **Connect HTTP API** in Settings. Saving alone leaves the
   source inactive and makes no provider request.
5. For authorization code, sign in and approve access on the provider's consent
   page. Client credentials obtains a token directly. Braidplane tests API
   connectivity and activates the source on success.

Client-credentials apps can use **Test connection** before saving; code-flow apps
must be saved and connected first. Re-enter the secret after a draft test.
Connecting requires permission to update data sources.

### Maintain the connection

Queries and pipelines renew expiring tokens automatically. Code-flow apps need a
refresh token for unattended renewal; request the provider's offline-access scope
when needed. Without a refresh token, authorize again after expiry.

Use **Reconnect HTTP API** to authorize again with the saved app. Replacing app
settings requires re-entering the complete settings and any required secret,
then reconnecting; stored values are never prefilled. Replacement retires the old
grant locally without revoking its provider tokens.

See the [HTTP OAuth reference](HTTP_OAUTH.md) for API/CLI setup, revocation and
provider compatibility.

## Schema introspection

When an OpenAPI spec URL is configured in the connector runtime settings,
schema refresh/import queues a worker job that fetches the spec and parses it
into a list of operations. Normal data-source detail pages and `GET /schema`
read cached metadata only; they do not fetch the OpenAPI document. Each
operation includes `id`, `path`, `method`, and `description`, plus optional
`request_body` and `responses`. The result format is `HTTPSchemaResponse`
(schema_type: `http`).

The SSR data-source create, pre-save connection test, and update flows expose `openapi_url` and `openapi_fetch_timeout_seconds`. The data-source detail page can refresh the imported schema, browse operation methods, paths, descriptions, request body metadata, and response metadata, and show diagnostics for parser failures, unreachable specs, unsafe URLs, and empty operation sets.

Same-origin OpenAPI documents are supported, including documents below or outside
the API base path. Same origin means identical scheme, hostname, and effective
port. Static API-key, Bearer, and Basic credentials are sent only when the
configured document URL has that origin. Authenticated spec redirects must stay
on that origin; an unrelated document origin is fetched anonymously. Private-host,
DNS pinning, redirect count, byte quota, timeout, and external-reference protections
still apply. URL user information is rejected; external `$ref` reads are disabled.
Generic OAuth support retains its tighter destination binding.

Schema imports also limit expansion of local references and YAML aliases,
summary data and generated drafts. Nesting beyond 128 levels is rejected.
These conservative checks use the remaining schema byte budget, capped at
10 MiB, and can reject a document whose operation output would fit. Unused
definitions can count toward the checks.

If no OpenAPI spec URL is configured, refresh/import returns an empty schema.
The connection test still works independently of schema availability.

### Create a saved query from an operation

In the data source's **Schema** section, choose **Create query draft** beside a supported operation. Review the prefilled method, relative path, query parameters, headers, JSON body and typed parameter definitions, then choose **Save query**. Opening a draft does not save or execute it. Existing imports need a schema refresh before draft metadata appears. The API and CLI expose the same `operations[].draft` and `diagnostics` in cached schema responses; submit its `statement` as `query_statement` and its `parameters` with the normal create-query request.

The configured data source base URL and credentials remain authoritative; root OpenAPI servers are informational. Parameter placeholders are generated without examples, defaults or security credentials. Path values are escaped as path components, query values use URL encoding, and body values use JSON encoding. Review optional fields before execution: remove unused fields and placeholders from the draft. Every retained placeholder needs a value; an explicit null body value is serialized as JSON null rather than removing the field. Expected response metadata remains available in the schema browser; it is not an enforced output contract.

Pipelines reference the saved query slug through the normal `operation_ref` field. Draft authoring does not create or modify pipeline specs. A query saved as `get-user` can be used in a query step with `data_source_ref: api`, `operation_type: http` and `operation_ref: get-user`, using the pipeline's normal parameter bindings.

## Query and operation model

A query defines an HTTP request: method, path (relative to `base_url`), query parameters, headers, and request body. Parameters use the `{{param_name}}` template syntax and can appear in any of these fields. Request bodies default to JSON encoding, but a saved operation that sets `Content-Type: application/x-www-form-urlencoded` now URL-encodes flat object bodies instead, and `Content-Type: multipart/form-data` now emits multipart fields for flat scalar object bodies.

Execution returns `status_code`, `headers`, and the response `body` as JSON. Non-JSON text is preserved as a JSON string; opaque responses return native `file` metadata instead. HTTP results are not normalized into `rows` or `column_names`.

For row-limit enforcement, each element of a top-level JSON array counts as one row, including scalar or nested-array elements. Objects and scalar/text responses count as one document; arrays nested inside an object are not counted separately. JSON `null` and opaque file responses contribute no JSON rows. Braidplane rejects arrays exceeding the effective row limit with `EXECUTION_ROW_LIMIT_EXCEEDED`, without returning a truncated success. The effective limit is the lower of the saved query's `row_limit` and the server's `execution_limits.max_result_rows`; inline operations use the server limit.

Row and response-byte limit errors include a safe detail stating the completed HTTP request's status. A response rejected by a limit can follow a completed write; verify remote state before retrying it. Pipeline triggers skip automatic retries after row or response-byte limit failures, even when their retry policy includes data-source failures.

Response-byte limits apply independently to every response shape. The connector caps the downloaded body using `execution_limits.http_max_response_bytes`, and the shared query runtime also caps the serialized execution response using `execution_limits.max_response_bytes`.

## Response acknowledgement

Saved and inline operations can opt into a declarative acknowledgement contract:

```json
{
  "type": "http",
  "method": "POST",
  "path": "/records",
  "body": "{{input}}",
  "acknowledgement": {
    "status_codes": [200, 201],
    "success": [{"path": "status.code", "equals": "OK"}],
    "empty_fields": ["errors"]
  }
}
```

In the UI, open **Response acknowledgement (optional)** when creating or editing
an HTTP operation. Select **Require response acknowledgement**, enter the accepted
status codes separated by commas, and add success checks with an object path and
an expected JSON scalar. Quote strings (`"OK"`); enter booleans (`true`) and numbers
(`1.0`) without quotes. Add empty error field names to check them recursively.
Save the operation to persist the contract. Reopening the definition shows its
checks; clear **Require response acknowledgement** and save to remove it.
Invalid fields retain the draft and show a correction message.

API and CLI operation JSON use the same contract shown above. All checks apply
to each response, including every page of a paginated GET. Existing operations without this object retain their
status-and-body behavior; HTTP sinks still require a 2xx status.

`status_codes` requires 1 to 16 distinct 2xx codes. `success` requires 1 to 8 checks.
Each path traverses JSON objects only, with at most 16 dot-separated segments
and 256 bytes; arrays, wildcards and templates are unsupported. `equals` is a
non-null string, boolean or number. Types must match. Numbers require the same
JSON number representation, so `1` and `1.0` differ. `empty_fields` optionally
names up to 8 distinct fields (up to 64 bytes each), checked recursively across
objects and arrays. Every occurrence must be null, an empty object or an empty
array. Absent error fields are allowed. Nonempty containers or scalar values
fail. This catches record-level errors under provider-specific resource keys
without embedding business mapping in the connector.

Unexpected status, unequal success fields, and nonempty error fields report
`HTTP_ACKNOWLEDGEMENT_REJECTED` (HTTP 400 for direct saved-query execution).
Missing, malformed, duplicate-key or excessive-depth JSON evidence reports
`HTTP_ACKNOWLEDGEMENT_UNCERTAIN` (HTTP 422 for direct saved-query execution).
The UI and CLI retain these distinct outcomes. Provider body values are
withheld from failure diagnostics. A write timeout or incomplete response also
reports uncertainty. Neither result confirms the sink effect or commits a
source checkpoint dependent on that sink. Automatic pipeline retries are
prohibited for both outcomes. A rejected acknowledgement may follow a partial
write; it does not prove rollback. Verify remote state before an operator retry.
The ledger retains its existing unconfirmed state until a successful response;
this contract adds neither per-record recovery nor an exactly-once guarantee.

## Cursor pagination

Saved and inline GET operations may add an explicit `pagination` object. Author
it through the API or CLI; the query form preserves existing settings.

```json
{
  "type": "http",
  "method": "GET",
  "path": "/items",
  "query_params": {"limit": 100},
  "pagination": {
    "cursor_param": "after",
    "records_path": "data.items",
    "next_cursor_path": "paging.next",
    "max_pages": 20,
    "timeout_seconds": 30
  }
}
```

The supported response shape is `{"data":{"items":[{"id":1}]},"paging":{"next":"opaque-token"}}`.
The next request uses the same endpoint, headers, and credentials, adding
`after=opaque-token`. An existing `query_params.after` seeds the first page.
Cursors are opaque strings, never endpoint URLs or parameter templates. Object-only
dot paths do not index arrays or transform nested records. Array-index and
wildcard notation is rejected. `records_path` must
select an array on every page. Missing, null, or empty `next_cursor_path` ends
the drain; other cursor types fail. Empty continuing pages and repeated cursors
(including the initial cursor) fail without a partial result.

The final response envelope retains the last page's other fields, with all
selected records concatenated at `records_path`. Status and headers come from
the last page. Selected records count toward the effective execution row limit;
ordinary non-paginated envelope counting remains unchanged. Downloaded successful
page bodies share one byte budget: the lower of the HTTP response limit and the
execution response limit. The combined envelope must also fit that budget.
Retry responses remain bounded per attempt by the request runtime.

`max_pages` is required (1 to 1000); reaching it with another cursor fails.
`timeout_seconds` is required (1 to 300) and bounds the whole drain, response reads,
retries, and backoff. Earlier execution deadlines win. Cancellation stops the
drain, including response selection and aggregation. The existing retry policy
applies to each GET: one attempt by default;
configured attempts retry transient failures, 429, and 5xx. Waits honor the
configured backoff and `Retry-After` within the operation deadline and the
60-second delay limit. Exhausted non-2xx responses fail
pagination, while ordinary requests retain their status-and-body behavior.
All requests retain destination validation; pagination redirects must stay on the
original origin, including scheme and port, to protect authored credentials.

Pagination cursors move between pages within one execution. Incremental
checkpoints move between successful pipeline runs. Configure a separate stable
checkpoint field in the final envelope when the API provides one; do not use a
terminal pagination cursor as a durable checkpoint. Failed drains expose no
partial result or checkpoint observation. Existing pipeline commit rules advance
checkpoints only after successful pipeline completion. Missing final checkpoint
values retain the previous lower bound.

## Page-number pagination

Use the same GET-only `pagination` object with page-number fields instead of
`cursor_param` and `next_cursor_path`:

```json
{
  "type": "http",
  "method": "GET",
  "path": "/items",
  "pagination": {
    "page_param": "page",
    "page_size_param": "pageSize",
    "page_size": 100,
    "records_path": "data.items",
    "total_pages_path": "totalPages",
    "page_number_path": "page",
    "max_pages": 20,
    "timeout_seconds": 30
  }
}
```

Traversal starts at page 1 and increments by 1. The connector owns both traversal
query parameters, overriding authored values. `page_size` is required (1 to 10000).
`total_pages_path` must select a nonnegative integer on every response; it is the
explicit termination signal. Zero is accepted only for an empty first page.
`page_number_path` must select an integer equal to the requested page. A repeated,
missing, or mismatched response page fails. Duplicate records across different
numbered pages are preserved. Changing totals, empty pages with a positive total, or
responses larger than the requested page size fail without a partial result.
Stable ordering and a stable page count during the read are provider requirements;
this does not provide a snapshot guarantee for a changing dataset.

All cursor-drain aggregation, source enforcement, limits, retries, cancellation,
redirect protection, and checkpoint rules above also apply. Page traversal never
advances a durable incremental checkpoint by itself. The final envelope retains
the last page's metadata and the combined selected array. Mixing cursor and
page-number options is rejected. Author pagination through API/CLI; the query
form preserves the authored object.

## Timestamp watermarks

HTTP sources support `query.incremental.checkpoint_kind: watermark` alongside
opaque cursor checkpoints. Set `field` to the timestamp's shared JSON contract
path: `updated_at` for a top-level array of objects or a single object,
`items[].updated_at` for an envelope containing an `items` array. Declare that
same field in the effective JSON output contract. Each selected record must
contain a non-null RFC3339 timestamp with an explicit timezone and at most nine
fractional second digits. Observation compares instants and stores the maximum
as UTC RFC3339 with nanosecond precision; offsets and equal timestamps are valid.
CSV, file, scalar, missing fields, malformed timestamps, non-2xx responses and
incomplete pagination fail without a checkpoint observation. Diagnostics describe
the expected shape without including provider field values.

Bind the provider's filters explicitly:

```yaml
operation_inline:
  method: GET
  path: /items
  query_params:
    updated_since: "{{runtime.incremental.lower_bound.value}}"
    updated_until: "{{runtime.incremental.upper_bound.value}}"
  pagination:
    cursor_param: after
    records_path: items
    next_cursor_path: next
    max_pages: 20
    timeout_seconds: 30
incremental:
  checkpoint_kind: watermark
  field: items[].updated_at
  lookback_window: 5m
output_contract:
  name: items-response
  representation: json
  root_shape: object
  fields:
    - name: items[].updated_at
      type: datetime
      required: true
```

Saved operations use the same placeholders. In the UI builder, select Watermark
and enter the field path and optional lookback window. API and CLI pipeline
validation use the same catalog capabilities and contract checks.

The first run has no lower bound unless initialized through
[bootstrap](../INCREMENTAL_OPERATIONS.md). Null query filters are omitted. Positive
`lookback_window` shifts the request lower bound backward, retaining the original
boundary for checkpoint observation. Bounds stay fixed across cursor or numbered
pages. Normal runs have no upper bound; backfills can supply one, which must be
an RFC3339 `value` envelope. Bootstrap seeds and both backfill bounds require an
explicit timezone and at most nine fractional second digits; numbers, null,
malformed strings and excess precision are rejected before state is committed or
a backfill is queued. Lower bounds must precede upper bounds by instant. A successful bounded backfill validates every record
before observing its explicit upper bound; records above it fail observation.

Use an inclusive lower filter (`updated_at >= updated_since`) to reread records
sharing the last timestamp. Choose an inclusive upper filter (`<= updated_until`)
for this example. Confirm your provider's actual filter semantics. An exclusive
lower filter needs a provider guarantee that no later record can share the
committed timestamp. Equal timestamps do not establish a unique record order.
`ordering` and `dedupe_key` document the source contract; they do not add a remote
sort or a destination upsert automatically.

Open-ended reads observe the maximum returned timestamp after the entire drain.
Empty results retain the committed lower boundary. Overlap-only results never
move it backward. Pagination tokens remain local traversal state; neither a
terminal token nor wall-clock completion advances a timestamp checkpoint.
Failed traversal, exhausted retries, cancellation, invalid records or a failed
dependent sink block promotion. A subsequent run uses the last committed
boundary and applies lookback again.

Providers must offer stable ordering and consistent traversal for the requested
window. In a changing dataset, use a provider snapshot or explicit backfill bounds
with guarantees against updates moving between pages. Lookback handles only
late arrivals inside its duration; records arriving with older timestamps can be
missed. Use an idempotent target/upsert or deduplication to absorb overlaps and
retries. HTTP filters and pagination alone do not prove exactly-once delivery or
universal no-loss behavior. Timestamp observation uses only a completed drain,
independently of cursor, numbered or next-link traversal.

See the [local synthetic recipe](../examples/http-timestamp-watermark/README.md)
for repeatable ingestion into an idempotent target using local synthetic data.

## Pipeline relevance

**As a source**: the pipeline step uses `operation_type: http` with method, path, query parameters, headers, and body in `operation_inline`. JSON responses enter the graph as `json`, CSV responses enter as `csv`, and non-JSON/non-CSV response media types enter as native `file` payloads with safe metadata only in persisted diagnostics.

HTTP source steps support incremental `cursor` contracts when the operation
accepts a previous cursor token and returns the next cursor token in the
response envelope. The checkpoint value is opaque to cross-connector pipeline
code. Saved and inline source operations can reference
`{{runtime.incremental.lower_bound.cursor}}` and
`{{runtime.incremental.upper_bound.cursor}}` in path, query parameters, headers,
or body fields. Nil cursor values are omitted from query parameters. Braidplane
observes the next checkpoint from the configured response path, such as
`next_page_token` or `paging.next_page_token`, and keeps cursor values out of
public run diagnostics.

**As a sink**: the pipeline step uses `operation_type: http`. Upstream row data is serialized and sent as the request body to the configured endpoint. HTTP operation bodies may now be any JSON value, so `body: "{{input}}"` can send a top-level array, document, or explicit `null` payload directly instead of wrapping it in an object or rewriting `null` to `{}`. The same preserved `null` behavior also applies to a literal saved-query `body: null`. The same contextual `input` placeholder also carries CSV and file sink payloads: when the upstream payload representation is CSV and the body is exactly `{{input}}`, the connector writes the CSV bytes directly and defaults `Content-Type` to `text/csv`; when the representation is `file`, it writes the raw file bytes directly and defaults `Content-Type` from file metadata or `application/octet-stream`. Saved operation headers still win, so callers can override the default media type when an endpoint expects a vendor JSON media type or a different raw text content type. When the resolved body is a string and the saved operation explicitly sets a non-JSON `Content-Type` such as `text/plain`, the connector now sends that string as raw bytes instead of JSON-quoting it. When the resolved body is a flat object and the saved operation sets `Content-Type: application/x-www-form-urlencoded`, the connector URL-encodes those scalar fields instead of sending JSON. When the resolved body is a flat object and the saved operation sets `Content-Type: multipart/form-data`, the connector now emits multipart scalar fields and replaces the caller header with the generated boundary-bearing content type required by the request. Explicit `null` bodies remain JSON-only; the connector now rejects `body: null` when the saved operation sets a non-JSON content type. Likewise, write-like methods that omit `body` entirely now reject explicit non-JSON content types instead of synthesizing `{}` under the wrong media type.

## Security

Private IP ranges and loopback addresses are blocked by default to prevent SSRF attacks. Hosted/default Braidplane rejects customer HTTP data source payloads that set `allow_private_hosts: true`. The `self-hosted` profile rejects this broad switch and currently supports public destinations for this connector; local fixtures may opt into private hosts. See the [private-host policy](../DATA_SOURCES.md#private-host-access).

## Next-link pagination

Select a JSON response string using `pagination.next_link_path`, with
`records_path`, `max_pages` and `timeout_seconds`. For example:

```json
{"method":"GET","path":"/items","pagination":{"next_link_path":"paging.next","records_path":"items","max_pages":10,"timeout_seconds":30}}
```

Choose exactly one pagination pattern. Missing, null or empty next links end the
drain. Relative URLs resolve against the last response URL, including same-origin
redirects. Absolute links must retain the original scheme, hostname and effective
port. User information, fragments, cross-origin destinations and HTTPS downgrades
are rejected before forwarding credentials. Existing pinned dialing, private-host
policy, OAuth destination restrictions and transport encryption still apply to
every request. URLs are used literally, without template substitution.

A link query replaces the original query parameters. The provider must retain
fixed incremental window filters in every link; the connector does not invent or
merge provider URL parameters. Retries repeat the exact page URL. Repeated URLs,
empty continuing pages, malformed envelopes and page/row/cumulative byte limits
fail the entire drain without returning partial results. Pagination never commits
a durable checkpoint; source observation and dependent sink confirmation still
control checkpoint promotion. `Link` response headers are not parsed.

## Runtime safety limits

HTTP execution inherits the shared query limits documented in [DATA_SOURCES.md](../DATA_SOURCES.md#runtime-safety-limits). In addition, HTTP requests are capped by:

- `execution_limits.http_timeout` for outbound request duration
- `execution_limits.http_max_redirects` for redirects
- `execution_limits.http_max_request_bytes` for request bodies
- `execution_limits.http_max_response_bytes` for response bodies
- `execution_limits.http_retry_attempts` and `execution_limits.http_retry_backoff` for retry behavior

Retries default to one attempt. With an increased attempt budget, read methods
retry timeouts, connection reset/refusal, unexpected connection closure, `429`
and `5xx`. Cancellation, destination/redirect validation and other failures stop
retries. Each operation deadline covers attempts, backoff and response reads;
an earlier caller or whole-drain deadline wins.

`Retry-After` accepts delay-seconds and HTTP-date. The wait is the larger of the
configured backoff and the provider delay. Malformed values fall back to the
configured backoff; past dates contribute zero delay. Delays above 60 seconds
(including integer overflow), or waits that cannot fit the remaining deadline,
stop repetition. Cancellation interrupts waits. Attempts never reset the deadline.

### Opt-in keyed write retries

Default mutating requests have no connector retry. A saved pipeline sink operation
may explicitly attest a verified provider contract with:

```json
{
  "type": "http",
  "method": "POST",
  "path": "/records",
  "headers": {"Idempotency-Key": "{{runtime.sink.idempotency_key}}"},
  "body": "{{input}}",
  "write_retry": {
    "verified": true,
    "provider_contract_url": "https://provider.example/idempotency",
    "key_header": "Idempotency-Key",
    "retention_seconds": 86400,
    "payload_conflict": "reject"
  }
}
```

The operator must verify the documented provider guarantee for the configured
account and destination: the same method, key and payload returns the original
outcome, while a changed payload under the same key is rejected. The URL records
that verification reference; execution does not fetch it or certify the provider.
An arbitrary header or replay-class declaration does not enable retries.

This policy requires `idempotent_with_key`, a durable ledger claim, a nonempty
runtime key bound exactly to the configured header, and a replayable body.
Reserved protocol/authentication key headers and `Connection` overrides are refused.
Only `input` and `runtime.sink.idempotency_key` template bindings are permitted;
other bindings can change across pipeline attempts or operator recovery. Static
path, query and header values remain supported. Redirects are refused so attempts
retain the same destination, method, headers and encoded payload bytes. Transient
transport errors, `429` and `5xx` share the read attempt/backoff machinery.
Application/key conflicts and acknowledgement rejection are not retried.

`retention_seconds` is the verified minimum provider retention (1 to 604800 seconds),
measured from the original durable claim, including on operator recovery. An
expired claim refuses dispatch. Retention caps the operation deadline; recovery
cannot refresh it. The pipeline recovery contract preserves input, immutable
operation and effect key. An unrelated manual run creates a new effect. Provider
configuration/account changes require operator review before recovery.

Transport timeout or connection loss can follow a committed provider write.
Unconfirmed delivery leaves the ledger in progress and blocks dependent source
checkpoints; only confirmed success marks it succeeded. Unsafe or ledger-only
unknown effects remain blocked by the existing recovery policy. No status polling
or provider reconciliation is added. An exhausted retry budget never proves the
provider write failed. Check the destination and your provider's idempotency
contract before repeating an uncertain write.

API and CLI statements use these same fields. Query create/edit forms expose
pagination and write policy JSON under **Pagination and write retries**. Generic
HTTP sinks require a saved operation; inline HTTP source operations do not imply
inline HTTP sink support.

## Known limitations

- Connection tests always use GET against `health_path`. Method customization and fallback behavior (HEAD/OPTIONS) remain future work.
- Draft authoring supports scalar path, query and header parameters and JSON object bodies with scalar leaves. Arrays, composition/recursive schemas, non-JSON or ambiguous body media types, cookie/complex parameters, API-key security schemes, unsafe paths and path/operation server overrides produce diagnostics and no draft. Authors can still create manual HTTP queries for supported runtime operations. OpenAPI `number` fields need manual authoring because saved query parameters have no decimal type.
- Explicit GET cursor, page-number and JSON next-link pagination are supported. Link-header parsing, short-page-only termination and asynchronous job polling are not supported.
- Spreadsheet-specific parsing and workbook export remain future work. Use FTP or Git spreadsheet mode when the workflow needs sheet and range semantics.

See [connector support levels](../../docs-public/connectors/support-levels.md) for available operations, connector status and provider requirements.
