# Pipeline worker recovery

Core detects abandoned River work automatically and fences new sink dispatches.
The default target is three minutes from worker loss to the original attempt's
terminal worker-ownership failure, with PostgreSQL reachable, a healthy River
leader and available worker capacity. Supported recovery uses retained input;
it never silently evaluates changed sources.

## Queue timing and detection

The previous path used River's one-hour rescue threshold, despite a one-minute
job timeout. A pipeline worker reports its observation every five seconds; after
15 seconds without successful persistence its execution context is cancelled.
These observations diagnose connectivity and staleness. They do not prove that
a provider write stopped.

Core now configures a one-minute job timeout and two-minute rescue threshold.
River scans roughly every 30 seconds and schedules eligible jobs for redelivery.
Redelivery finalizes the original attempt as `worker_ownership_lost` without
rerunning its source queries or effects. Terminal queue states can also be
reconciled by the existing interrupted-run sweep. Recovery still requires a
retained, attempted queue record in a non-running state and a failed run.
Silence, an unstarted job or an absent queue record is insufficient.

The target allows a minute after normal cancellation, then maintenance and retry
scheduling. The safety fence below does not depend on cancellation completing in
that minute. The existing job runtime allowance is unchanged. For longer jobs,
set `worker.job_timeout` and `worker.rescue_stuck_jobs_after` on every worker;
rescue must exceed the positive timeout by at least one minute. See
[configuration](../CONFIG.md#worker-queue-configuration). Longer worker-specific
timeouts, backlog, database outages or unavailable workers extend the delay.

## Exclusive writes across worker loss

The durable sink effect ledger is the write gate. Before creating a claim,
runtime dispatch locks and verifies its tenant-scoped owning run is still
running. Terminalization prevents a returning worker from obtaining a new claim.
An effect's unique identity admits one initial writer. The claim survives
process loss and database session loss.

An existing unresolved claim never authorizes another runtime writer, including
recovery of an ordinary sink failure caused by a connectivity interruption.
Elapsed time, cancellation and declared idempotency cannot release it. Confirmed
effects are skipped. Unclaimed effects can be written by the new recovery run.

For naturally idempotent S3 sinks with deterministic `overwrite: true` output,
recovery can instead verify the prepared object's complete digest, length,
content type and user metadata. Readback uses bounded memory and sends no PUT.
A match confirms the ledger; an absent or different object, failed read, or
unsupported verification path preserves refusal. Retained file references use
their recorded digest and length. Read permission is needed in addition to write
permission. The encrypted executor protocol requires an explicit readback result;
an older executor's preparation acknowledgement cannot authorize dispatch.

Keep the original retained version and inputs. The recovery target or input must
not change through `runtime.run.slug`; use `runtime.sink.replay_identity` for the
stable effect identity. Missing inputs and unsafe effects remain refused.
An already accepted provider request can settle after its worker disappears;
this design prevents replacement writers and does not promise general exactly-once
delivery. Provider-side changes by other actors remain outside this fence.

Drain and replace old worker and executor binaries before adopting the shorter
timing. The previous binaries do not enforce these claim and verification rules.
Apply the pending Core migration catalog before starting the upgraded roles.

## Operator actions

Wait for the original attempt to report its terminal worker-ownership failure,
then use the existing UI recovery action, API or `pipeline run recover`. No
stopped-worker attestation is required. Recovery preserves the recorded version,
source and transform outputs, execution context and effect identity. Repeating it
skips confirmed writes. See [recovery eligibility](../docs-public/getting-started/pipeline-specifications.md#retained-input-recovery).

If recovery is refused, inspect its missing-input or provider-outcome explanation.
Do not treat a new evaluation as retained-input recovery: it reads current sources.
An unresolved effect stays blocked until its original result can be confirmed;
Core does not automatically overwrite it to clear the refusal.

## Bounded synthetic qualification

`TestWorkerLossNativeCrashRehearsal` uses production queue timing. It kills real
worker processes before claim and after provider acceptance, and pauses another
worker until after replacement. It changes the source data, recovers the original
snapshot, repeats recovery and checks exactly one provider write per effect.
It measures fencing against three minutes and each bounded recovery request
against 30 seconds. Larger-payload recovery qualification is separate.

`TestSinkClaimFencesSlowAndDisconnectedWriter` holds a provider write while
ignoring cancellation. Recovery is refused both before and after terminating the
original PostgreSQL session. The original write can then finish; readback confirms
it without another write. A returning failed owner cannot obtain a fresh claim.

A local run on 17 November 2026 measured queue detection at 138.8–139.2 seconds
and ownership fencing at 140.0–140.2 seconds after loss. The six recovery requests
completed in 25–153 milliseconds. Across the three original runs, source reads
remained at three and provider writes at one per effect, including after the
paused worker returned. These are bounded fixture observations, not hosted
latency guarantees.

These fixtures use synthetic localhost identities and disposable databases and
object storage. They do not authorize interruption of a live worker or qualify
hosted deployment behavior.
