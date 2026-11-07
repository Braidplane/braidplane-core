# HTTP

Read HTTP APIs or send results to an endpoint with explicit requests. HTTP
supports source and sink roles, with response/request media types determining
whether data is JSON, CSV, a raw file, or a form.

Use the [shared connection workflow](connect-a-system.md) to create this data
source in your workspace. You can read this guide without an account or
provider resources. Running examples requires provider resources appropriate for your evaluation.

## Connection settings

| Field | Required | Type | Default | Notes |
|---|:---:|---|---|---|
| `base_url` | yes | string (URI) | — | Base URL of the HTTP service; must be `http` or `https` |
| `health_path` | no | string | `/` | Relative path used for the connection test (GET request) |
| `timeout_seconds` | no | integer | 5 | Request timeout in seconds (1–300) |
| `openapi_url` | no | string (URI) | — | OpenAPI document URL used for schema import and operation browsing |
| `openapi_fetch_timeout_seconds` | no | integer | 10 | OpenAPI document fetch timeout in seconds (1–60) |
| `allow_private_hosts` | no | boolean | `false` | Requires private host access to be enabled for the deployment; hosted/default deployments reject `true` |

## Authentication and permissions

| Field | Required | Notes |
|---|:---:|---|
| `oauth` | no | Write-only OAuth app settings; cannot be combined with another authentication method |
| `api_key` | no | Write-only API key; requires `api_key_header` |
| `api_key_header` | no | `X-Api-Key` sends the key directly; `Authorization` sends `ApiKey <key>` |
| `bearer_token` | no | Added as `Authorization: Bearer <token>` header |
| `username` | no | HTTP Basic Auth username |
| `password` | no | HTTP Basic Auth password; required when `username` is set |

Credentials belong in the connection, never in an operation statement. They are
encrypted at rest and are not returned in API responses. Hosted/default
deployments block loopback and private network endpoints; your laptop’s
`localhost` is not the hosted worker’s network.

Use one authentication method: API key, bearer, Basic, or OAuth. Leave
credentials empty for a public endpoint. Do not put authentication tokens or
API keys in operation examples or saved query parameters. Register an OAuth app
with the API provider, save the connection, then use **Connect HTTP API** to
complete authorization. Use the [API reference](../api/index.md) for API and
CLI request fields.

## Test and discover

Connection testing sends GET to `health_path` relative to `base_url`. Choose a
path that permits GET. Schema refresh imports the optional OpenAPI document;
without `openapi_url`, an empty schema is expected. Imported operations can be
browsed but do not automatically create saved queries.

## Read example

Assume your endpoint exposes GET `/customers?email=...` and returns
`[{"email":"alex@example.com","name":"Alex"}]`. Define a text `email` parameter
with `alex@example.com`. The configured base URL supplies the host; this is an
example endpoint contract, not a Braidplane-hosted demo API.

```json
{
  "type": "http",
  "method": "GET",
  "path": "/customers",
  "query_params": {
    "email": "{{email}}"
  },
  "headers": {
    "Accept": "application/json"
  }
}
```

## Write example

Save this operation for a pipeline sink whose destination accepts a JSON
array. Upstream rows become the request body. Success and write semantics are
defined by your endpoint; a POST is not automatically safe to replay.

```json
{
  "type": "http",
  "method": "POST",
  "path": "/imports/customers",
  "headers": {
    "Content-Type": "application/json"
  },
  "body": "{{input}}"
}
```

## Operation reference

| Field | Meaning |
|---|---|
| `type` | `http` |
| `method` | `GET`, `POST`, `PUT`, `DELETE`, `PATCH`, or `HEAD` |
| `path` | Endpoint path relative to the configured base URL |
| `query_params` | Query string values |
| `headers` | Non-secret request headers |
| `body` | JSON value or supported native input binding |
| `pagination` | Optional bounded GET cursor or page-number drain, authored through API/CLI |

Parameters can appear in the path, query parameters, headers, and body. For
URL-encoded scalar forms use `Content-Type: application/x-www-form-urlencoded`;
for multipart forms use `multipart/form-data`. Braidplane supplies the multipart
boundary. Explicit null bodies are JSON-only.

## Pipeline behavior

Use `operation_type: http`. JSON and CSV source responses retain their payload
representations; other media types such as PDF or images can produce raw file
payloads. HTTP also supports multipart form payloads. This does not mean every
response can feed an SQL transform directly.

A sink with `body: "{{input}}"` can forward rows, JSON, CSV, files, or forms in
the corresponding supported representation. Raw file output uses the file media
type; explicit compatible headers can override the default.

Cursor checkpoints require an API that accepts a previous cursor and returns a
next cursor. Bind `{{runtime.incremental.lower_bound.cursor}}` into the request
and configure a separate checkpoint response path. Optional `pagination` drains
pages within one execution; its next-page cursor is distinct from the durable
incremental checkpoint. Failed drains do not advance checkpoints.

For an envelope such as `{"data":{"items":[{"id":1}]},"paging":{"next":"token"}}`,
add `pagination: {"cursor_param":"after","records_path":"data.items",
"next_cursor_path":"paging.next","max_pages":20,"timeout_seconds":30}` to a GET
statement. Each next cursor becomes the same endpoint's `after` query parameter.
Missing, null, or empty next cursors end the drain. The final envelope contains
all selected records and retains other fields from the last page. Repeated cursors,
empty continuing pages, non-2xx responses, and exceeded limits fail without partial
success. Pagination redirects stay on the original origin.

## Limits and troubleshooting

- Requests, responses, time, redirects, and row counts are bounded.
- Retries default to one attempt. Read retries handle transient network failures, HTTP 429 and 5xx.
  Retry-After accepts seconds or HTTP dates, with at most 60 seconds per wait
  and the operation deadline as a total budget. Malformed values use configured
  backoff; past dates add no delay; excessive waits stop retries. Mutating
  requests require an explicit verified provider idempotency contract and a
  durable keyed pipeline sink effect to retry.
- A health test returning 405 usually needs a different GET-compatible path.
- Empty metadata without an OpenAPI URL is expected; failed imports require
  checking URL reachability and document validity.
- A 401/403 response calls for checking provider credentials and permissions.
- Non-2xx sink responses report the numeric status and standard HTTP label in
  the run diagnostic, without copying provider response bodies or headers. Verify
  remote state before retrying a write; an error response does not establish rollback.
- Explicit GET cursor, page-number and JSON next-link pagination are supported.
  Link-header parsing, short-page-only termination and asynchronous job polling
  are unsupported.
  A page-number drain needs distinct `page_param` and `page_size_param`, a
  `page_size` from 1 to 10,000, `page_number_path`, `total_pages_path`, and
  bounded `max_pages` and `timeout_seconds`. It starts at page 1 and fails on
  changed totals, missing or mismatched page numbers, or an empty continuing
  page. No spreadsheet parsing/export. Use a file
  connector when worksheet and range selection are required.

See [operation and pipeline basics](connect-a-system.md#use-the-examples) for
where to use these statements, parameters, environment bindings, and replay
safety. Return to the [connector directory](index.md).

## Next links and keyed write retries

`pagination.next_link_path` selects a JSON next URL string. Use it with
`records_path`, `max_pages` and `timeout_seconds`, without cursor/page options.
Relative URLs resolve against the last response URL. Links must remain on the
original origin and pass destination and OAuth checks. Link queries replace the
original query; providers must retain fixed incremental window filters. Missing,
null or empty links end traversal; cycles or limits fail without partial output.

Saved pipeline sinks can opt into `write_retry` with `verified: true`, an HTTPS
`provider_contract_url`, `key_header`, `retention_seconds` (1–604800), and
`payload_conflict: "reject"`. Verify that the provider deduplicates the same
account, destination, method, key and payload and rejects changed payloads. Bind
that header exactly to `{{runtime.sink.idempotency_key}}`, select
`idempotent_with_key`, and configure an attempt budget above one. Only `input`
and `runtime.sink.idempotency_key` templates are allowed with this policy.
Redirects are refused. Query create/edit exposes both JSON policies under
**Pagination and write retries**; API and CLI use the same statement fields.

Retention starts at the original durable ledger claim and caps all attempts and
recovery. Expired claims refuse dispatch. Timeout can follow a committed write;
unconfirmed delivery leaves the ledger in progress and blocks dependent source
checkpoints. Operator recovery preserves effect identity; an unrelated new run is
a new effect. Generic HTTP inline sinks are unsupported. Fixture validation does
not verify a real provider guarantee or establish universal exactly-once delivery.
