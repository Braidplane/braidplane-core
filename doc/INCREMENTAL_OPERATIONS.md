# Incremental Operator Controls

Braidplane Alpha supports initial checkpoint bootstrap and one bounded historical
backfill per request. The incremental execution model
defines the underlying source and sink contract.

## Initial checkpoint bootstrap

When no committed checkpoint exists, operators can seed initial committed progress
through `POST .../environments/{environment_slug}/pipelines/{pipeline_slug}:bootstrap-checkpoints`,
`braidplane pipeline bootstrap`, or the Builder's **Incremental controls**.
Bootstrap requires pipeline update permission, a request slug for audit provenance,
one seed envelope for every incremental source, and protected-environment
confirmation when applicable. Watermarks use `{"value": ...}` with a non-empty
string or number for SQL sources. HTTP timestamp watermarks require a `value`
string containing an RFC3339 timestamp with an explicit timezone and at most nine
fractional second digits, for example `{"value":"2026-05-24T10:00:00.123456789Z"}`.
Invalid explicit HTTP watermark seeds, including null, numbers and malformed
or over-precision timestamps, are rejected before checkpoint or audit persistence. Absent/null
normal execution state still means no initial boundary. Cursors use `{"cursor": "..."}`.
Source identities resolve from the deployed version, environment bindings/variables, and supplied non-secret
runtime inputs exactly as for execution. Seeds and the required security-audit
event commit atomically. Existing committed progress causes a conflict, including
repeated requests; bootstrap never resets progress or executes source/sink operations.
The checkpoint display metadata retains the bootstrap request slug, and later run
diagnostics identify bootstrapped previous progress. Seed values remain runtime
state and never enter the Git-authored spec.

## Bounded backfill

Alpha exposes one explicitly bounded backfill run per request through the normal
run-start endpoint's `backfill.source_windows`, CLI `pipeline run start
--backfill-json`, and Builder **Incremental controls**. The map must name every
incremental source exactly once and supply both `lower_bound` and `upper_bound`
envelopes. Watermark bounds must have the same JSON value type and increase
(numeric values/strings, RFC3339 timestamp, or lexical text ordering); opaque
cursor bounds are not ordered by Braidplane. Source operations must consume both
injected bounds. HTTP watermark lower and upper bounds both follow the timestamp
seed rule above and must increase by instant (timezone offsets are supported).
Invalid windows are rejected before a run or execution job is accepted. These
service validations apply to API, CLI and Builder requests for saved and inline
source operations.
Every sink must declare a replay-safe idempotency class. An explicit `run_slug`
provides duplicate protection: a repeated slug in the same project environment
returns a conflict and does not enqueue another run. Use a new slug only when
intentionally requesting another execution of the window.

Accepted records, failed-before-execution records, and completed runs retain the
`backfill` mode, series/run identity, one-chunk metadata, and redacted source-window
diagnostics. Exact request windows are persisted with the queued execution job;
request input values and raw cursor tokens stay out of audit metadata and user
visible summaries. A required `backfill_requested` security-audit event is atomic
with run acceptance. Backfill execution and retained-payload recovery never advance
normal committed checkpoints. Automatic chunk scheduling, checkpoint reset, and
backfill endpoint promotion remain deferred. The data model describes these
broader defaults separately from the available Alpha controls. Numeric bounds
follow the pipeline numeric contract. UI, CLI, and API requests preserve exact
numeric tokens when passing checkpoint values to connectors.
