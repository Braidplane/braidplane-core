# Export logs and traces to your collector

Core exports structured logs and traces to a customer-controlled OpenTelemetry
Collector using OTLP/HTTP protobuf. Configure full signal URLs, including
`/v1/logs` and `/v1/traces`. There is no OTLP/gRPC or OTLP metrics exporter.
Existing Prometheus metrics remain available through the configured metrics
endpoint. See [configuration](../CONFIG.md#metrics-configuration).

## Disposable local example

From the repository root, start the digest-pinned collector:

```sh
docker compose -p core-otel-example -f examples/otel/compose.yaml up -d
```

The [collector configuration](../examples/otel/collector.yaml) accepts logs and
traces on loopback port 4318. It writes diagnostic output to container logs and
JSON files in ephemeral `/tmp`. It requires no vendor account or credentials.
Use this example only with synthetic data: its detailed debug exporter prints
every received record. The image is a fixed interoperability fixture, not a
recommendation to deploy that version in production.

Set these variables before starting Core, for example with `task dev`:

```sh
export LOG_OTLP_LOGS_ENDPOINT=http://127.0.0.1:4318/v1/logs
export TRACING_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces
export TRACING_SAMPLING_RATIO=1
export LOG_HTTP_REQUESTS=false
task dev
```

Run a synthetic pipeline through the application, then inspect the collector:

```sh
docker compose -p core-otel-example -f examples/otel/compose.yaml logs collector
docker compose -p core-otel-example -f examples/otel/compose.yaml down
```

Containerized Core must use a collector address reachable from its own network;
`127.0.0.1` means that container. For a remote collector, use HTTPS with a trusted
certificate and configure access and retention under your control. Both HTTP
exporter SDKs also consume their supported `OTEL_EXPORTER_OTLP_*` settings,
including headers and TLS configuration. Keep credentials out of endpoint URLs.
Empty application endpoints disable the corresponding exporter even when
ambient SDK export variables are set. `LOG_LEVEL=none` disables all log handlers.

## Signals and identities

| Signal | Current instrumentation and attributes |
|---|---|
| HTTP server traces | API and SSR frontend request method, normalized route, status, request ID; no raw URL or body in span attributes |
| HTTP client traces | Frontend-to-API method and status; connector operation boundaries carry connector type and operation |
| Internal traces | Generated service/repository method and layer; application pgx operation/database system without SQL text or arguments |
| Queue traces and logs | River consumer kind, queue, attempt and bounded outcome; enqueue/start/completion logs |
| Logs | Configured slog records, source location and severity; shared completion/error helpers add correlation fields |
| Metrics | Prometheus HTTP/client, connector/query/transform, pipeline, broker, SQL pool, River, Proof and Go/process metrics; no OTLP metrics |

Both exported signals carry `service.name=braidplane`, `deployment.environment`
from logging configuration, and `braidplane.component` from process wiring
(`backend` or `frontend`). Traces also carry `service.namespace=braidplane`.
Core does not add a resource instance ID, service version or host identity.
Backend roles share the backend component; these attributes do not identify an
individual worker or executor. Log instrumentation scope is `braidplane`.

Logs emitted with an active context have OTLP trace/span IDs. The shared context
helpers also add separate `trace_id`, `span_id` and `request_id` attributes.
Allowlisted context attributes are organization, user, pipeline, pipeline-run,
data-source and query slugs, plus data-source type. These identifiers remain
customer data; scope access and retention accordingly.

Traces use parent-based sampling. The configured ratio applies to root spans;
a sampled incoming parent remains sampled even when the root ratio is zero.
Logs are filtered by log level, independently of trace sampling, so a log's
trace identity does not guarantee a retained trace.

## Context propagation boundaries

| Boundary | What survives |
|---|---|
| Incoming API/SSR request | W3C `traceparent`, `tracestate` and baggage are extracted; request ID remains a separate identity |
| SSR frontend to API | Trace context and baggage are injected by the instrumented API client |
| Synchronous service/repository/connector calls | The caller's Go context |
| River enqueue to worker | Persisted `request_id` and `traceparent`; consumer span continues the parent. Tracestate and baggage are dropped |
| Broker poller / separately dispatched executor | Persisted execution requests do not retain the originating trace context; processing uses its own context |

Browser JavaScript has no browser tracing SDK. Arbitrary outbound provider
clients are not promised W3C injection. Baggage is not automatically copied into
span attributes. Scheduling, retries, poller delivery and portable execution do
not establish one complete trace from browser to sink. Missing async propagation
is tracked in [issue #165](https://github.com/Braidplane/braidplane-core/issues/165);
this qualification preserves the current boundary.

## Failure, buffering and loss

Export runs asynchronously. With the pinned SDK defaults, traces buffer 2,048
spans, export batches of up to 512, and schedule every five seconds. Logs buffer
2,048 records, export up to 512, and schedule every second. SDK `OTEL_BSP_*` and
`OTEL_BLRP_*` variables can change these settings. Trace queue overflow drops
new spans; log queue overflow drops the oldest queued records. Neither processor
is configured to block application execution on a full queue.

Each HTTP export has a five-second timeout, including exporter retry work.
Each provider shutdown has a separate five-second allowance. Closing both
providers sequentially can therefore take approximately ten seconds. Local
stdout/file output is synchronous and has separate filesystem/container
backpressure; an OTLP timeout does not bound that output.

An absent, rejecting or stalled collector can lose batches. Retryable failures
are retried within the export deadline; permanent rejection is discarded.
Buffers are memory-only and are not a durable spool. Collector acceptance is
not proof that a downstream backend retained a record. Restart restores delivery
of new records; it does not guarantee replay of lost records. Export failures
are reported through SDK diagnostics or provider close errors and do not change
the runner's success/failure result. Telemetry cannot serve as the run ledger.

## Confidentiality and qualification

The tests reuse shared error classification, redaction and allowlisted context
helpers covered by [issue #29](https://github.com/Braidplane/braidplane-core/issues/29).
Synthetic row values and credential canaries must stay out of exported logs,
span attributes and exporter failure diagnostics. Shared span error helpers
record bounded type/kind rather than raw error strings.

The logging fanout does not sanitize arbitrary caller-provided slog fields.
Optional HTTP debug body logging is also a separate surface; disable it for this
example. This suite qualifies the exercised instrumentation, not every possible
log call or all of #29. Review sensitive application logging and collector SDK
diagnostics before exporting production data.

Run the repeatable collector and failure qualification without application
configuration, databases or provider accounts:

```sh
bash scripts/go-toolchain.sh test -count=1 -timeout=3m ./service/pipelineruntime/e2e
```

It runs an event-input-to-sink pipeline with synthetic I/O through the production
runner, exports production shared-method logs/spans, decodes collector output,
checks resource and log/span correlation, scans canaries, stops/restarts the
collector with the same providers, and exercises refusal, rejection, stalled responses and disabled
export. It checks successful and failing sink outcomes and execution/shutdown
budgets. A held receiver and small SDK queues verify overflow drops records
without blocking pipeline execution. Existing HTTP/frontend/River tests cover
their propagation contracts:

```sh
bash scripts/go-toolchain.sh test -short ./pkg/logging ./pkg/tracing ./pkg/queue ./frontend
```

Local fixture results qualify this transport and boundary only. They do not
establish a deployed customer collector's TLS, authorization, downstream storage
or retention policy.
