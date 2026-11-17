# Large SQL file transfer and recovery qualification

This procedure qualifies a finite synthetic PostgreSQL-to-S3 file transfer and
its failure boundaries. Run it on disposable fixtures with space for several
GiB of encrypted retained input, destination objects and PostgreSQL WAL.
It does not qualify native SQL transforms, other connectors or hosted capacity.

## Recorded local result

The [sanitized receipt](../comparison/evidence/149/receipt.json) identifies the
base revision, exact changed-source hashes, fixture images and measurement scope.
The [run log](../comparison/evidence/149/run.txt) records all assertions. The
[initial verdict](../comparison/evidence/149/verification.json) and
[recovered verdict](../comparison/evidence/149/recovery-verification.json) and
[reconciled verdict](../comparison/evidence/149/reconciliation-verification.json) each
check every record in the complete original downloaded object.

On the recorded shared M3 Pro host, one million 1,024-byte synthetic payloads
produced a 1,078,777,880-byte object. Transfer plus complete download/hash took
35.810 seconds. After a pre-dispatch failure and source mutation, retained-byte
recovery plus a confirmed repeat took 13.970 seconds. After SIGKILL, the broker
lease fence completed in 29.949 seconds; reconciliation plus a confirmed repeat
and full digest readbacks took 2.115 seconds, without another provider write.
All three outputs have SHA-256
`14a16998b6126c78a14fb1f56cafa1bf44bc5e9b9ce299682a93b31c1e19d77e`.

The sampled application RSS sum peaked at 218,864 KiB (213.73 MiB). At the crash
boundary, retained input occupied 4,324,548,721 logical plaintext bytes and
4,324,965,103 ciphertext bytes; the chunk relation, including indexes and TOAST,
occupied 4,495,491,072 bytes. The whole rehearsal took 217.94 seconds, including
fixture startup, fault cases, lease waits and cleanup. These figures describe
one local run stacked on [PR #151](https://github.com/Braidplane/braidplane-core/pull/151)
(revision `a129d76de12598a8bb6659f90071bf2e7381f420`) under the explicit profile below; they establish no throughput
ranking or production capacity. See the receipt for inclusions and exclusions.

## Recovery boundary

Pipeline checkpoints privately pin sealed file inputs. Recovery restores those
references only within the original organization, project, environment and run,
then reads the original encrypted chunks under a new live sink claim when no
provider effect has started. An uncertain S3 overwrite is reconciled through
complete destination readback; it cannot issue another write. Confirmed effects
are skipped. Recovery does not revive a dead source claim or reread changed SQL. Public checkpoint and run
JSON continue to contain metadata only.

The pin lasts for the organization's replay-checkpoint retention window, measured
from checkpoint creation (seven days by default). Existing pins follow changes to the current policy; increasing it cannot restore bytes already removed by maintenance. Ordinary query/file expiry can
pass while the pin remains live. Deleting or expiring the checkpoint releases
the pin; normal minute maintenance can then delete the source request and chunks.
Each recovered attempt has its own checkpoint and retention window. File bytes
stay outside the 10 MiB serialized checkpoint limit, so storage planning must
include all pinned inputs across concurrent and completed runs.

Recovery refuses missing, expired or mismatched input. Chunk authentication,
length and SHA-256 checks still protect sink publication. Secret-redacted
checkpoints do not pin original file bytes. Source/transform failure, unsafe
sink effects and unfenced pipeline worker ownership keep their existing refusals.

## Runnable procedure

Use the pinned toolchain, Docker and the setup in
[Community build](../COMMUNITY_BUILD.md). The test owns its database, S3 service,
network, synthetic credentials and executor children. It never targets an existing
application. The fixture reaper must remain enabled. The environment variable
below selects the explicit large profile; ordinary E2E keeps its small fixture.

```sh
(
set -e
mkdir -p .worktree/qualification-149
python3 -m venv .worktree/comparison-venv
.worktree/comparison-venv/bin/pip install -r comparison/requirements.txt
bash scripts/go-toolchain.sh test -c \
  -o .worktree/large-sql.test ./service/pipelinesvc/e2e
)
```

For a local Docker context backed by a Unix socket, export its endpoint as
`DOCKER_HOST`, following Community build. Run the binary directly so compilation
is excluded from measurements:

```sh
(
set -e
export BP_SQL_QUALIFICATION_OUTPUT="$PWD/.worktree/qualification-149/destination.json"
/usr/bin/time -l .worktree/large-sql.test \
  -test.run '^TestNativeFileStreaming$' -test.timeout 35m -test.v \
  > .worktree/qualification-149/run.log 2>&1 &
measurement_pid=$!
# On macOS /usr/bin/time remains the parent of the application process.
python3 comparison/sample_process_memory.py "$measurement_pid" \
  > .worktree/qualification-149/memory.jsonl &
sampler_pid=$!
run_status=0
wait "$measurement_pid" || run_status=$?
sampler_status=0
wait "$sampler_pid" || sampler_status=$?
if [ "$run_status" -ne 0 ]; then exit "$run_status"; fi
if [ "$sampler_status" -ne 0 ]; then exit "$sampler_status"; fi
.worktree/comparison-venv/bin/python comparison/verify_large_output.py \
  --encoding sql-object-envelope --rows 1000000 --payload-bytes 1024 \
  .worktree/qualification-149/destination.json \
  > .worktree/qualification-149/verification.json
.worktree/comparison-venv/bin/python comparison/verify_large_output.py \
  --encoding sql-object-envelope --rows 1000000 --payload-bytes 1024 \
  .worktree/qualification-149/destination.json.recovered \
  > .worktree/qualification-149/recovery-verification.json
.worktree/comparison-venv/bin/python comparison/verify_large_output.py \
  --encoding sql-object-envelope --rows 1000000 --payload-bytes 1024 \
  .worktree/qualification-149/destination.json.reconciled \
  > .worktree/qualification-149/reconciliation-verification.json
(cd comparison && ../.worktree/comparison-venv/bin/python -m unittest \
  test_verify_large_output test_sample_process_memory)
)
```

On platforms with different `time` flags, collect equivalent elapsed time and
peak RSS explicitly. The sampler uses `ps` RSS in KiB with a 250 ms delay between calls; the
observed interval also includes each `ps` invocation.
Shared pages may be counted once per process. Its sum includes the selected
process and transitive children: the test harness,
application broker and executor children, plus the small `time` parent when using
the command above. It excludes Docker services, their daemon, OS cache and the
sampler itself. It is a sampled peak, not a hard upper bound. `time -l` supplies
additional kernel accounting; do not equate its RSS field to the sampled sum.

Keep the original downloaded object unchanged until independent verification
finishes. The verifier streams all original bytes, hashes them and checks every
record against the frozen formula in [datasets.sql](../comparison/datasets.sql).
It requires exactly one million distinct IDs, exact field types and values,
matching columns and row count, complete JSON and no trailing data. No output
normalization or intermediate row-array conversion is used.

## Effective capacity profile

| Control | Large fixture setting |
|---|---|
| Streamed row policy | Unlimited (`StreamMaxRows=0`); fixture emits 1,000,000 rows |
| Materialized result row limit | 1,000 (`MaxResultRows`); separate from streamed results |
| Maximum retained file | 2,147,483,648 bytes |
| Per-row and metadata response allowance | 1,048,576 bytes |
| Query, connector and HTTP deadlines | Five minutes each |
| SQL startup connection timeout | Five seconds |
| SQL connection pool | Five open, two idle |
| Control-plane maintenance isolation | READ COMMITTED |
| Owned executor test-process timeout | Ten minutes |
| Broker request deadline | Five minutes |
| Broker executors | Two |
| Per-data-source connector admission | Two active, four queued, one-minute queue wait |
| Enclosing test context | 30 minutes |
| Go test timeout | 35 minutes |
| Multipart writes | Sequential 8 MiB parts |

The fixture uses direct loopback SQL access without a deployment-private
PostgreSQL authorization policy. Policy-routed sources with their shorter
authorization deadline are outside this measured envelope.

The source generates integer IDs from 1 through 1,000,000, integer
`amount_cents = order_id % 100000` and a 1,024-byte string of repeated lowercase
MD5 digits of the decimal ID. MD5 defines synthetic bytes; SHA-256 verifies output
integrity. The test SQL generates rows directly instead of loading a source table;
a one-row source-state table allows mutation after executor loss.

These are fixture settings, not proposed defaults. The run does not determine
whether five-minute operation deadlines are necessary. Self-hosted controls are
defined by [#147](https://github.com/Braidplane/braidplane-core/issues/147).
The broker's executor lease is distinct from pipeline worker rescue in
[#148](https://github.com/Braidplane/braidplane-core/issues/148). This rehearsal
kills a real broker executor and waits for its existing lease fence; it does not
change or measure the production River rescue threshold.

## Failure assertions and cleanup

| Boundary | Required observation |
|---|---|
| Concurrent pin and expiry | A pin transaction is held across source expiry while maintenance waits on its lock. A fresh maintenance snapshot preserves both the committed pin and the source request. Cleanup requires READ COMMITTED isolation; other levels are refused. |
| Retained storage capacity | A fixture-only trigger raises PostgreSQL `53100` after two persisted chunks. The run fails, no unsealed stream or active request remains, and the complete prior destination keeps its length and SHA-256. This simulates a database capacity error; it does not fill a disk or qualify recovery of PostgreSQL itself. |
| Destination backpressure | The second multipart part blocks after one accepted 8 MiB part. No further part or completion is sent while blocked. |
| Cancellation at that boundary | The caller returns an error, active/pending requests drain, the multipart upload is aborted and the prior complete destination retains its length and digest. Source cancellation also proves a subsequent single-slot SQL request can run. |
| Failure before provider dispatch | A synthetic credential-decryption failure leaves a sealed, pinned SQL input and no provider effect. After changing SQL source state and restoring the fixture credential, recovery uploads the retained original bytes. A confirmed repeat performs no additional upload; no SQL request is issued. |
| Executor death at that boundary | SIGKILL terminates only the fixture child. Source state changes before replacement. The broker reports ambiguous execution, the original pipeline remains failed. After ordinary source expiry is advanced, supported recovery confirms the prior matching destination by complete readback with no new SQL requests, parts or completion. A repeated recovery skips the confirmed effect. Expired pins are explicitly refused. The prior destination remains complete throughout. |
| Provider cleanup after death | Exactly one open multipart upload with one 8 MiB part remains. The procedure lists and explicitly aborts it. Real installations need an operator cleanup or an incomplete-upload lifecycle rule. |
| Retained cleanup | The fixture advances its SQL stream and checkpoint-pin expiries and calls the ordinary maintenance query, then requires zero SQL retained files. Unpinned files retain deadline-plus-15-minute expiry. Pinned pipeline files follow replay-checkpoint retention with minute maintenance. PostgreSQL file size need not shrink. |

The retained-storage observation logs logical plaintext and ciphertext sizes at
the crash boundary and `pg_total_relation_size('retained_file_chunks')`. The
relation figure includes indexes and TOAST for the fixture table, including its
other small test files. It excludes WAL, backups, SQL temporary files, other relations and destination
storage. The test does not run broker maintenance automatically during transfer;
several complete retained inputs accumulate before the explicit expiry check.
This boundary measurement is not a continuous peak of all database storage.

Upgrade through forward-only migration 5 (`000005_checkpoint_file_inputs.up.sql`)
and apply the normal runtime grants for its new private table and retention view. Executor function
signatures and the encrypted file-reference protocol are unchanged. Upgrade all
processes that run maintenance before enabling file recovery: older maintenance
queries can delete pinned inputs using the previous request-expiry rules.
Older metadata-only file checkpoints remain ineligible; the migration does not
guess or backfill file associations. When integrating concurrent catalog changes, resequence unpublished migrations
and regenerate the catalog before applying them. Never renumber an applied
migration on an installed database.

See [native-file streaming](NATIVE_FILE_STREAMING.md) for 64 KiB copy buffers,
1 MiB encrypted chunks, provider implementation bounds and retention obligations.
A configured 2 GiB allowance alone proves no capacity. Use the measured receipt
and complete verifier verdict when assessing the tested envelope.
