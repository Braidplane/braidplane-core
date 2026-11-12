# Operator sink recovery

Run details show a delivery outcome for each attempted sink, regardless of
run success: `confirmed` (ledger completion), `failed` (pre-dispatch failure),
`unknown` (an attempt may have reached the provider), or `no_op` (empty input
skipped without dispatch). Old runs without this evidence show outcome
unavailable. A connector error or lost acknowledgement does not prove absence
of external writes. Unvisited sinks have no delivery evidence for that run.

`POST /organizations/{slug}/projects/{project_slug}/environments/{environment_slug}/runs/{run_slug}:recover`
and **Attempts & recovery → Recover sink delivery** recover terminal sink
failures with retained outputs for every source and transform. Terminal attempts
with the explicit `worker_ownership_lost` cause are also eligible when a retained
queue row proves ownership has ended and every sink declares a repeat-safe
strategy. Running ownership, absent queue records and unrelated platform failures
remain refused. Native queue rescue and terminal reconciliation must finish
before recovery; worker silence alone does not establish a fence. Execute permission
and protected-environment confirmation are required. The original immutable
version, resolved datasource references, retained inputs, incremental source
windows and effect identity are reused. Source query parameters supplied through
`runtime.input` are not resolved again because their retained outputs replace
source execution. Those request-local values remain unpersisted. Recovery does
not read newer source rows or advance incremental checkpoints. Recovery of a
recovery keeps the same original effect identity. Intent and terminal outcome are durable security audit
events linked to the original run and version; unavailable intent auditing
prevents dispatch.

The native CLI exposes the same operation as `braidplane pipeline run recover
<run-slug> --project <project> --environment <environment>`, with
`--confirm-environment <environment>` for protected environments. JSON output
is the returned run record; `--output text` shows the recovery run, original
effect identity and sink replay decisions. A terminal failed recovery prints
its record and exits nonzero. Source-failure reruns use `pipeline run start`
after repair and evaluate current source data; recovery retains the failed
history and never substitutes a new evaluation. See the
[pipeline specifications](../docs-public/getting-started/pipeline-specifications.md#sink-replay-safety).

This endpoint is synchronous. A persisted failed recovery is returned as a run
record with its failure diagnostics. After a response timeout, inspect run
history and provider state before another attempt. Confirmed repeat-safe effects
are skipped with `skip_ledger_succeeded`; their completed ledger records stay
immutable. An unknown S3 write can converge through the same deterministic
object overwrite. Unknown `deduplicated_by_ledger` attempts and attempted `unsafe`
operations remain refused; there is no force-retry or manual success-marking API.

The initial representative workflow uses row input and S3 JSON export with
`overwrite: true` and a key based on `runtime.sink.replay_identity`. Prerequisites
are retained, non-secret input checkpoints, unchanged saved operations and
provider target/serialization, and a documented deterministic overwrite
contract. Source/transform failures, missing or pruned checkpoints, event-input
runs without payload checkpoints, and sink runtime-input substitutions not
retained in the checkpoint are unsupported. Recovery refuses a missing sink
input rather than accepting new values or guessing a provider destination.
File metadata alone is not a retained file. When nonempty file bytes are
unavailable, recovery refuses the request before creating another run or
dispatching a sink. Reconcile the destination before a new evaluation, which
reads the source again.
You can repair credentials through the existing datasource lifecycle. Changing
the operation or target starts a new evaluation, not recovery of the original
effect. No cross-system transaction is promised.

Eligible source/transform outputs are checkpointed before sink dispatch, using
the existing checkpoint store and organization retention policy. The complete
serialized pre-dispatch checkpoint, including plans and intermediate outputs,
is limited to 10 MiB. Event-input runs, runs with resolved environment secrets and outputs
that cannot be serialized are excluded from pre-dispatch retention. Existing
terminal checkpointing keeps its size behavior and value-based redaction.
Redacted outputs do not establish exact worker-loss repair inputs. A checkpoint-store failure stops
eligible dispatch. Missing, excluded, oversized or expired repair inputs require
provider reconciliation; a new evaluation reads sources again.

Worker-loss recovery preserves the platform failure and uncertain delivery in
the original run. It does not relabel that history as a sink failure or success.
Naturally idempotent S3 recovery requires `overwrite: true` and the same pinned
key, target and serialization. Sink values that change with `runtime.run.slug`
are refused for worker-loss repair; use `runtime.sink.replay_identity` instead.
Confirmed safe effects are skipped, while unknown
ledger-only and unsafe effects remain blocked by the effect ledger. This adds
no arbitrary mid-graph resume, queue policy change or exactly-once guarantee.

A row-input sink may set `empty_input_policy: skip` for an intentional
zero-row/no-change result or `reject` for unexpected empty data. `skip` records
`no_op`, creates no sink-effect claim, and makes no connector call. Input-contract
validation still runs first. Omission preserves connector behavior: SQL sinks skip empty rows,
while connector minimum-row checks remain in force elsewhere. Apply `reject`
when a missing source dataset must fail rather than silently skip delivery.
The policy applies to row payloads; a raw header-only CSV export remains an
intentional object write under its consumer contract. Row-key upsert,
deterministic object overwrite and append are distinct provider contracts.
BigQuery append and best-effort `insertId` suppression do not establish
exactly-once delivery.
