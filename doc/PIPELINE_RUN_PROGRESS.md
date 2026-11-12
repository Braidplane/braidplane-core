# Run execution progress

Manual/API and completion starts retain their selected artifact at enqueue.
Schedule and verified webhook intake (including retries) records a queued run
and private queue job ownership in the same transaction as enqueue. When the
version policy is resolved by the worker, authored version and deployment
identity remain unavailable until the first runtime admission write binds them
atomically. A queued timestamp never substitutes for the worker-start timestamp.
Historical jobs without an intake row retain only their first recorded fact.

The scheduler maintenance pass reconciles at most 100 abandoned attempts while
locking both the run and its owning queue job. It requires a stale observation
and a terminal queue job, or a previously attempted job returned to a waiting
state. Missing jobs and silence alone leave physical liveness unknown.
Reconciliation retains committed evidence, closes the attempt as failed, and
prohibits automatic replay when admission made external effects possible.
Source-owner logical finalization stays unresolved for uncertain effects.
Synchronous execution without queue ownership reports stale observations but
does not infer worker loss from a timeout.

The runtime persists its sanitized timeline at admission, before each step,
after each step, and at terminal finalization. This applies even when the
pipeline has no structural checks. Step completion is committed before the
next step starts. Retained replay outputs record `step_reused`; they do not
invent a new provider execution. Existing queued and worker-started timestamps
remain in the timeline when the runtime takes over. Synchronous paths without
a queued phase do not synthesize queue events.

An attempt-local timeline revision fences writes in the existing run record.
Updates must advance it by one and retain every previously committed event as
an ordered prefix. Duplicate, skipped, stale and concurrent revisions cannot
overwrite accepted progress. Existing terminal-state guards still own terminal
immutability; the existing logical-run closure remains separate from attempt
progress. Historical timelines without revisions remain readable.

A progress write failure stops execution before dispatching the next effect.
If persistence fails after sink dispatch, failure finalization retains an
uncertain outcome rather than asserting that the provider did nothing or
reporting success. If finalization also cannot be committed, the last durable
running state remains authoritative. Repeating an admitted attempt cannot
rerun its effects; recovery requires the existing recovery path or a new
attempt identity. Earlier failure evidence survives pre-runtime refusals.

Writes occur at boundaries and structural checks, never per row. This uses
cumulative sanitized timeline snapshots in the existing run storage and its
retention policy. Boundary timestamps describe durable execution observations;
a quiet step alone supplies no worker heartbeat or proof of worker loss.
The admitted runtime records a worker observation every five seconds. The
single latest timestamp is retained independently of boundary revisions;
heartbeat writes neither append events nor erase a newer observation. Transient
heartbeat failures are tolerated within the fifteen-second stale window. Continued reporting failure after that window cancels execution;
a rejected running record cancels it immediately because ownership is lost.
Reports within fifteen seconds are recent; older reports are stale, and future
or absent reports leave liveness unavailable. A stale report does not prove
worker death and never authorizes an effect replay. Terminal attempts reject
late reports. Loss reconciliation still requires authoritative worker/job
state, not an observation timeout.

The tenant-authorized `GET .../runs/{run_slug}/progress` endpoint returns at
most 100 ordered events, step counts, exact attempt metadata and reporting
state. `after_event` is the number of consumed events; continue from
`next_after_event`, and resynchronize from zero after an invalid cursor. An
expired run returns 404 under the existing run-retention policy; the API does
not distinguish never-recorded from retained-away history. Boundary revisions
can remain unchanged while worker observations advance.

For CLI inspection and watching, see [CLI pipeline runs](../docs-public/getting-started/cli.md#use-the-cli-with-an-agent).
