# Pipeline specifications

A pipeline specification is the reviewable YAML or JSON definition of a
pipeline. It names the steps and their data contracts, but never contains
connection credentials. Git is the source of truth. When you apply a commit,
Braidplane stores an immutable version for validation and deployment.

Use filenames ending in `.pipeline.yaml`, `.pipeline.yml`, or `.pipeline.json`.
The examples below use YAML.

## Top-level shape

```yaml
metadata:
  slug: customer-export
  name: Customer export
steps:
  - kind: query
    slug: read-customers
    query:
      data_source_ref: customers-db
      operation_type: sql
      operation_ref: active-customers
      output_contract:
        representation: rows
        fields:
          - name: customer_id
            type: string
            required: true
```

`metadata.slug` and every step slug use lowercase letters, digits, and hyphens.
Slugs are stable identifiers; names are display labels. `steps` must contain at
least one step and form an acyclic graph.

Before deployment or execution, every step must belong to one connected data
flow. Multiple sources may join downstream, and one producer may feed multiple
sinks. Explicit query-parameter step references also connect the graph. Sharing
a connection, project, schedule or trigger does not connect independent flows.
Validation lists disconnected groups and their step slugs: split those flows
into separate pipelines or connect them through an actual data dependency.
Connectivity does not impose a step-count or resource limit.

Draft save and formatting preserve incomplete or disconnected work. Existing
deployed or pinned versions must pass the rule at their next deployment or run,
including replay and recovery. Stored versions, run history and retained
artifacts remain unchanged; remediation requires new reviewed definitions.

## Step kinds

| Kind | Purpose | Required block |
|---|---|---|
| `input` | Accept one run-scoped JSON payload | `input.output_contract` |
| `query` | Read from a connector | `query` |
| `transform` | Transform earlier results with `sql_v1` | `transform` |
| `sink` | Write one earlier result to a connector | `sink` |

Query steps are graph roots. A transform uses a map of SQL table aliases to
earlier step slugs. A sink consumes exactly one earlier step:

```yaml
- kind: transform
  slug: select-customer-ids
  transform:
    inputs:
      customers: read-customers
    sql: SELECT customer_id FROM customers
    output_kind: rows

- kind: sink
  slug: write-customers
  sink:
    input: select-customer-ids
    data_source_ref: archive
    operation_type: s3
    operation_ref: write-customer-export
    config:
      key: exports/customers.json
      overwrite: true
    idempotency_class: naturally_idempotent
```

S3 terminal sinks require a non-empty string at `sink.config.key`, even when
the saved or inline operation already contains a key. CLI and API-backed
definition validation check this requirement. A connection test checks the
connection separately; it does not validate the sink definition.

This example overwrites a stable object key.
Choose another replay class if the operation does not satisfy that guarantee.

Every transform or sink reference must point to an earlier input, query, or
transform step. Query-parameter step references must point to an earlier query
or transform step. Multiple query roots are allowed when they converge
downstream.

## Connections and operations

`data_source_ref` is a logical name, not the slug of a saved connection.
Bind each logical reference to a compatible data source separately for every
project environment. That keeps one specification portable across runtime
targets.

Set `operation_type` to the connector operation family:

- Query: `sql`, `bigquery`, `snowflake`, `http`, `hubspot`, `ftp`, `mongodb`,
  `git`, or `s3`
- Sink: `sql`, `bigquery`, `snowflake`, `http`, `hubspot`, `ftp`, `mongodb`,
  `git`, or `s3`

`google_workspace` is available only where the deployment enables that
connector. Check the connection options in your workspace before using it in a
pipeline.

Define exactly one operation target:

- `operation_ref`: slug of a saved connector operation; recommended when the
  operation is managed and tested independently.
- `operation_inline`: connector-specific statement embedded in the spec.

Connector guides define the accepted statement fields.

## Parameters and `{{placeholders}}`

Use `{{name}}` in a connector operation when part of the operation must receive
a value at execution time. Names are case-sensitive. For example, a saved SQL
query might contain:

```sql
SELECT customer_id, email
FROM customers
WHERE customer_id = {{customer_id}}
```

A saved query defines each ordinary parameter's name, type, whether it is
required, and any default. Supported types are `text`, `integer`, `boolean`,
`date`, and `uuid`. The pipeline query step supplies the value under the same
name. An inline operation has no saved parameter definition, so put every
ordinary placeholder value in the query step's `parameters` map and use the
connector's supported value types. Reserved `{{runtime.*}}` expressions may
also appear directly in supported inline-operation fields.

```yaml
- kind: query
  slug: read-customer
  query:
    data_source_ref: customers-db
    operation_type: sql
    operation_ref: customer-by-id
    parameters:
      customer_id: "{{runtime.input.customer_id}}"
```

Here, `{{customer_id}}` belongs to the saved operation, while
`{{runtime.input.customer_id}}` tells the pipeline where its value comes from.
Start the run with a non-secret input value:

```bash
braidplane pipeline start customer-export \
  --input-json '{"customer_id": 42}'
```

A query parameter value can be:

- a literal JSON-compatible value, such as `customer_id: 42`;
- a per-run non-secret value, `{{runtime.input.customer_id}}`;
- an environment value, `{{runtime.env.batch_size}}`;
- a write-only environment secret, `{{runtime.secrets.API_TOKEN}}`; or
- an earlier query or transform result, such as `$steps.read-customers.rows`.

The `runtime.*` namespaces are reserved and supplied by Braidplane. `{{input}}`
is also reserved for the upstream payload consumed by a sink; it is not an
ordinary query parameter. Keep connection credentials on the data source, not
in pipeline literals or per-run input.

### Current runtime parameter catalog

These values are supplied at execution time. They are not saved query
parameters, and the `runtime.` prefix is reserved.

| Expression | Value and format | Resolution, missing values, and exposure |
|---|---|---|
| `{{runtime.run.date}}` | Current UTC date, `YYYY-MM-DD` | Pinned once before pipeline execution; reused by all operations, retries, and recovery. Standalone query execution uses its own current time. |
| `{{runtime.run.datetime}}` | Current UTC timestamp, RFC 3339 (for example, `2026-05-14T22:30:00Z`) | Pinned once before pipeline execution; reused by all operations, retries, and recovery. Standalone query execution uses its own current time. |
| `{{runtime.previous_success.exists}}` | Boolean. | Pipeline parameter/config fields and supported connector bindings. Unavailable in standalone queries. False when no qualifying success remains. Selected once with the run timestamp; never changes during retries or recovery. |
| `{{runtime.previous_success.run_slug}}` | Prior attempt's slug, or null. | Same pinned selection and locations as `exists`; never a sink replay identity. |
| `{{runtime.previous_success.started_at}}` | RFC 3339 UTC text, or null. | Same pinned selection and locations as `exists`; supports date operations when non-null. |
| `{{runtime.previous_success.finished_at}}` | RFC 3339 UTC text, or null. | Same pinned selection and locations as `exists`; supports date operations when non-null. |
| `{{runtime.input.<key>}}` | Non-secret JSON value supplied for this run. Keys match `[A-Za-z_][A-Za-z0-9_]*` and cannot be secret-like. | Resolved on the execution copy before validation. A missing key fails the run. Values are request-local: run records, events, logs, and UI retain only input key names. |
| `{{runtime.env.<key>}}` | Environment scalar: text, integer, boolean, date (`YYYY-MM-DD`), or UUID string. | Resolved on the execution copy before validation. A missing key fails the run. |
| `{{runtime.secrets.<key>}}` | Write-only environment secret. | Resolved only inside the runtime boundary before execution. A missing key fails the run; plaintext is redacted and is never persisted, returned, logged, or rendered. |
| `{{runtime.incremental.lower_bound}}` and `.value` / `.cursor` | The decoded prior checkpoint, or its comparable watermark value / opaque cursor. | Injected for incremental source execution. On the first run each bound is `null`; the source must handle that case. |
| `{{runtime.incremental.upper_bound}}` and `.value` / `.cursor` | The bounded upper checkpoint, or its comparable watermark value / opaque cursor. | Injected for incremental source execution. It is `null` until a bound is available; successful execution observes the next watermark or cursor. |
| `{{runtime.run.slug}}` | Current attempt's run slug. | Available throughout pipeline execution in parameter/config fields and supported connector bindings. Standalone queries have no pipeline run slug. Retries and recovery use their own attempt slug; use `runtime.sink.replay_identity` when the key must retain the original effect identity. |
| `{{runtime.sink.idempotency_key}}` | Resolved sink idempotency key. | Injected only while a sink operation executes, when a non-empty key is configured. |
| `{{runtime.sink.idempotency_class}}` | Declared sink idempotency class as text. | Injected only while a sink operation executes, when the class is known. It describes the declared delivery contract, not proof of provider behavior. |
| `{{runtime.sink.replay_identity}}` | Stable sink effect identity, normally the run slug. | Injected only while a sink operation executes. Authorized recovery reuses the original run's identity, so a deterministic sink key can target the same external effect. It is not a general source or transform parameter. |

All fields are reserved bindings rather than saved-query parameter declarations.
Use whole-value placeholders to retain their declared type; interpolation produces
text and rejects null. Run dates may appear in pipeline query parameters, sink
config, and connector fields with named-parameter support. Plain previous-success fields are available through supported connector bindings;
date operations belong in pipeline query parameters and sink config. Environment, input, and
secret values may appear in pipeline query parameters, inline connector fields,
and sink config; the selected connector must support the resulting field type.
Environment values may also supply an incremental lookback duration. Sink metadata
is available in sink config and sink connector bindings only. Incremental bounds
are available in incremental query parameters and source connector bindings only.
Runtime bindings are unavailable in transform SQL, data-source identities, and
pipeline metadata.

Retained-input recovery can repair terminal sink failures and explicit worker-loss
attempts after queue ownership is fenced. It reuses the recorded version, source
and transform outputs, and effect identity. Worker-loss recovery requires complete
retained inputs and repeat-safe sinks; it refuses targets or inputs that change
with `runtime.run.slug`. Use `runtime.sink.replay_identity` for a deterministic
S3 key with `overwrite: true`.

Bounded eligible checkpoints are saved before sink dispatch, with a 10 MiB total
limit and the organization's retention policy.
Event payloads and runs with resolved environment secrets are excluded from this
early retention. Missing inputs, unknown ledger-only attempts and unsafe effects
remain refused; an unknown S3 write can converge through deterministic overwrite.
The UI recovery action, API and `pipeline run recover` CLI share these checks;
a new evaluation reads current sources again.

Run dates and safe run/sink/history identities may appear in user-authored output
paths or requests. Environment values are non-secret and may be retained in pinned
deployment materialization. Input values and secret plaintext are never runtime
history fields; only safe input key names are retained. Checkpoint values follow
the connector checkpoint's persistence and inspection policy. Missing sink metadata
fails if referenced; it does not become an empty string. Standalone queries supply
run dates but require explicit pipeline/sink/incremental context for other runtime
bindings. Connector field restrictions still apply: for example, a SQL value uses
a named bound parameter, not SQL identifier or statement interpolation.

Incremental bounds are connector-defined JSON: an object bound exposes each
of its top-level fields through `.field`; scalar bounds also appear as `.value`
and `.cursor`. Missing `.value` or `.cursor` is null. Object field types follow
the connector's checkpoint schema; watermark timestamps use the source's
serialized timestamp format rather than the run's wall clock. Incremental bounds
are supplied only to query execution, never directly to transform SQL.

`{{input}}` has a different role: it is the exact upstream payload binding for
a sink operation, such as `rows: "{{input}}"` or `body: "{{input}}"`, and is
resolved when the sink dispatches. `$steps.<step-slug>.<field>` is also
different: it is a typed reference to a completed earlier step result, resolved
just before a later query or transform runs. Supported fields depend on the
result type; common fields are `rows`, `column_names`, and `row_count`, while
HTTP and cursor results also expose fields such as `body`, `headers`, `file`,
and `next_cursor`. A missing, unfinished, or unsupported step field fails the
step. A string beginning with `$steps.` inside a resolved runtime input or
environment value remains a literal string rather than becoming a new step
reference.

### Date expressions and previous success

Use date-operation expressions in pipeline `query.parameters` and `sink.config` values.
They work in the shared CLI/backend validator and in Builder-authored specs.
Pass them through named query parameters, including for SQL; the connector
continues to bind parameter values safely. Expressions are not evaluated inside
SQL text, saved operation definitions, transform SQL, runtime inputs, or secrets.
Plain run and previous-success bindings are also available in connector fields
that support named parameter substitution.
Date operations on `runtime.env.<key>` require a date-typed environment variable.
Operations on `runtime.input.<key>` require an ISO date (`YYYY-MM-DD`) or RFC 3339
timestamp string. Timestamps normalize to UTC before calendar operations.
Text-typed environment variables, numbers, booleans, objects, arrays, null, and
secrets cannot supply date operations. Resolved strings remain data and cannot
introduce new expressions.

```yaml
parameters:
  since: "{{runtime.run.datetime | offset:-24h}}"
  report_date: "{{runtime.run.date | add_days:-1}}"
  prior_finished: "{{runtime.previous_success.finished_at}}"
  has_previous: "{{runtime.previous_success.exists}}"
  reporting_month: "{{runtime.env.reporting_date | add_months:-1}}"
  requested_day: "{{runtime.input.requested_at | format:date}}"
```

| Operation | Behavior |
|---|---|
| `offset:DURATION` | Fixed elapsed duration, such as `-24h`, `30m`, or `1h30m`. Go duration units apply (`ns`, `us`/`µs`, `ms`, `s`, `m`, `h`); `d` is unsupported. |
| `add_days:N` | Integer calendar-day offset in UTC. |
| `add_months:N` | Integer calendar-month offset, clamped to the target month's final day. March 31 minus one month becomes February 28, or February 29 in a leap year. The UTC time of day is preserved. |
| `format:date` | `YYYY-MM-DD` string. |
| `format:datetime` | RFC 3339 UTC string. |
| `format:unix` | Integer Unix seconds. |

Chain at most four operations with `|`; put `format` last. Calendar offsets
are bounded to integers from -120000 to 120000, and results must remain within
years 0001 through 9999. The default output preserves the date/datetime kind.
UTC has no daylight-saving changes. Calendar arithmetic in other timezones, custom format strings,
`.addDate(...)`, `runtime.date`, scripting, and general evaluation are unsupported.
Invalid syntax, unknown fields, invalid durations, and operations on null values
fail validation or execution. A whole-value reference preserves booleans, null,
and integer Unix seconds; interpolation produces text. Null cannot be interpolated.

Unknown namespaces, misspelled built-ins, unsupported metadata fields, and
malformed expressions fail authoring validation. Sink metadata requires sink
execution; incremental bounds require incremental source execution. Dynamic
environment, input, and secret keys resolve against the selected context rather
than a fixed built-in list. Missing keys fail before connector dispatch.
Connector-defined checkpoint fields remain supported. A known null bound or
absent previous success differs from an unknown parameter, which never becomes
null or literal text.

Runtime fields use an entity namespace followed by a field name. The replacement
names are `runtime.run.date`, `runtime.run.datetime`, `runtime.run.slug`,
`runtime.sink.idempotency_key`, `runtime.sink.idempotency_class`, and
`runtime.sink.replay_identity`. Flat run/sink names have no aliases for new
authoring. Historical inspection retains the exact executed definition.

One lookup selects the most recently finished successful attempt strictly before
the new run starts, within the same organization, project, environment, pipeline,
and exact immutable pipeline version. Equal completion times use the later
stored run record to break ties. Failed, running, recovery replay, and backfill
runs are excluded; successful retry attempts qualify. A new version starts with
no prior success. The metadata does not attest to unchanged external data or
mutable saved-operation definitions. Retention can remove otherwise qualifying
history, which is treated as absent.

The selected run identity and timestamps are saved in the run timeline before
connector dispatch. Concurrent successes cannot change that run's selection.
A conflicting context for the same run identity fails before dispatch. Retries
and sink recovery reuse the saved time and selection; their own lifecycle
start/end timestamps still describe the new attempt. Legacy runs without
retained context cannot be retried or recovered through this path: start a new
run. No runtime input values or secrets are read from prior history. This lookup
creates no trigger, dependency, or ordering between pipelines.

Do not quote `{{name}}` inside SQL or use it for table or column names. SQL
connectors bind it as a typed value. Other connectors substitute parameters only
in the fields documented by their connector guide. A missing required value or
a value that cannot be converted to its declared type stops the operation with
a validation or execution error.

## SQL transforms

`sql_v1` runs one `SELECT` over named, row-normalized inputs. It supports
projections, aliases, comparisons and boolean filters, `IS NULL`, grouped
aggregates, `ORDER BY`, literal `LIMIT`/`OFFSET`, non-recursive CTEs, and
`JSON_OBJECT(...)` or `JSON_ARRAY(...)` projection helpers.

It rejects writes, multiple statements, recursive CTEs, unsupported subqueries,
and expressions outside the validated subset. `HAVING` supports post-group
predicates over bounded groups and aggregates. Input aliases must match the
names under `transform.inputs`; derived projections should have explicit aliases.
Set `output_kind` to `rows`, `json`, or `csv` to control serialization. Local
validation reports unsupported syntax before the spec is committed. The
[API reference](../api/index.md) defines the pipeline contract.

## Contracts

Contracts describe the payload crossing a step boundary. Declare an upstream
`output_contract`, a downstream `input_contract`, or both when you want both
sides documented and checked.

```yaml
output_contract:
  name: customers
  representation: rows
  fields:
    - name: customer_id
      type: string
      required: true
      validations:
        min_length: 1
    - name: created_at
      type: datetime
      nullable: true
```

Representations are `rows`, `json`, `csv`, `file`, and `multipart_form`.
Transforms support `rows`, `json`, and `csv`; multipart form is limited to HTTP
boundaries. Field types are `string`, `integer`, `number`, `boolean`, `object`,
`array`, `datetime`, and `decimal`.

Fields can set `required`, `nullable`, and `description`. A `decimal` field
requires `precision` from 1 to 38 and `scale` from 0 through
the precision; Braidplane rejects inexact values. Applicable validation rules
include `enum`, `min`, `max`, `min_length`, `max_length`, `format` (`email`,
`uuid`, or `url`), and `unique`. Validation rejects incompatible contracts
before a run starts.

## Sink replay safety

Every sink declares what Braidplane may do when a write might have happened but
the run did not finish cleanly. This matters after a timeout or worker crash: a
retry could otherwise write the same records twice.

| Class | What it means on retry |
|---|---|
| `naturally_idempotent` | Repeating the exact write has the same visible result. A deterministic file overwrite or an upsert by a stable business key can qualify; a plain insert does not. |
| `idempotent_with_key` | Braidplane sends the same stable idempotency key again, and the provider guarantees that the repeated key does not create another effect. |
| `deduplicated_by_ledger` | Braidplane records the attempt before calling the provider. It skips a recorded success, but refuses to guess when an attempt is still in progress because the remote outcome is unknown. |
| `unsafe` | There is no repeat-safety guarantee. After an attempt starts, Braidplane refuses automatic retry and operator replay. |

The declaration does not make an operation safe by itself. Choose a class the
connector supports and that the specific operation actually satisfies. For
example, the S3 walkthrough uses `naturally_idempotent` because it overwrites a
deterministic object key. If a sink cannot be replayed safely, recover it
manually and verify the destination before starting another write.

## Incremental query sources

An incremental query reads from the last successfully committed position instead
of reading the full source every time. The checkpoint belongs to the deployed
pipeline version and source step. Braidplane commits a new checkpoint only after
all dependent sinks succeed, so a failed run reads the same window again.

Use `checkpoint_kind: watermark` for a comparable source value such as an
`updated_at` timestamp or increasing ID:

```yaml
incremental:
  checkpoint_kind: watermark
  field: updated_at
  lookback_window: 15m
  ordering:
    - updated_at
    - id
  dedupe_key: id
```

The source operation must use `{{runtime.incremental.lower_bound.value}}` in its
filter and return the configured `field`. Braidplane observes the greatest
returned value as the next checkpoint. A `lookback_window` moves the lower bound
back to include late records; that overlap can return records again, so use a
stable `ordering` and make the transform or sink tolerate repeats. `dedupe_key`
names the record identity available for that deduplication; it does not remove
duplicates by itself.

Use `checkpoint_kind: cursor` for an opaque continuation token from an API:

```yaml
incremental:
  checkpoint_kind: cursor
  cursor: next_page_token
```

The operation accepts `{{runtime.incremental.lower_bound.cursor}}` and returns
the named next cursor. Braidplane stores the token without interpreting it.
Cursors cannot use a lookback window.

These concepts are distinct: a rolling window is a deliberate overlap around a
watermark, a prior timestamp is metadata about an earlier run, and an
incremental lower bound is the committed source checkpoint used to start the
next source read. For example, `runtime.run.datetime | offset:-24h` means a
rolling 24-hour window; `runtime.previous_success.finished_at` means when the
prior attempt finished; `runtime.incremental.lower_bound.value` means the last
committed data watermark, adjusted by `lookback_window` when configured. Extraction
can precede completion by minutes, so `finished_at` cannot replace a checkpoint.

On the first run there is no lower bound. Do not put an initial checkpoint in
the specification; checkpoint seeds and backfill ranges are runtime settings.
Only add `incremental` to a query step when its connector guide documents the
checkpoint kind. PostgreSQL, MySQL, BigQuery, Snowflake, and MongoDB support
watermarks; HTTP and HubSpot support cursors. FTP, Git, and S3 currently perform
full reads.

## Validate before committing

```bash
braidplane pipeline format --file pipelines/customer-export.pipeline.yaml --format yaml
braidplane pipeline validate --local --file pipelines/customer-export.pipeline.yaml
```

The format command sends the complete file to the configured API and prints the
formatted result to stdout; it does not edit the file. The formatter rejects
extra YAML documents and JSON values. It requires pipeline read permission.

Local validation checks syntax, step ordering, references, operation types,
contracts, transform SQL, and graph semantics without contacting Braidplane.
After configuring a project and environment, server validation additionally
checks bindings and cached connector schema:

```bash
braidplane pipeline validate \
  --project '<project-slug>' \
  --environment '<environment-slug>' \
  --file pipelines/customer-export.pipeline.yaml
```

Use `--output json` or `--output yaml` for machine-readable results. A server
response includes structured diagnostics and exits nonzero when invalid. Local
success reports a file count; local failures and request errors remain on stderr.
The output option does not change what validation does.

The CLI reads only the first YAML document or JSON value from a file. Additional
content is not validated or sent for server validation. Ensure the file contains
one document or value before relying on the result.

Start from the downloadable
[PostgreSQL-to-S3 specification](files/joined-orders.pipeline.yaml) for a
complete query-transform-sink example.
