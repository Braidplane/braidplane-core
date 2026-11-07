# Historical native-file qualification

These are historical measurements from the original monorepo. They do not
qualify the adapted core or managed revisions.

On 2026-11-03, the isolated S3 → retained encrypted PostgreSQL chunks → S3
pipeline passed at both recorded sizes on clean application revision
`3a417f25fb05502a0042e4a665965b7e999e6854`. This is a service-level local
qualification, not a deployed tenant capacity claim or completed acceptance.

| Measurement | Small object | Object over 1 GiB |
|---|---:|---:|
| Independently verified bytes | 3,145,745 | 1,073,741,841 |
| Runner elapsed seconds | 0.441591 | 28.729943 |
| Sampled Go heap peak, bytes | 26,052,296 | 62,074,640 |
| Test process RSS high-water mark, bytes | 84,279,296 | 123,518,976 |
| Retained encrypted bytes | 3,146,141 | 1,073,845,281 |
| Retained chunk relation, bytes including TOAST/indexes | 3,342,336 | 1,115,701,248 |
| Database size growth, bytes | 3,678,208 | 1,116,007,248 |
| Largest encrypted broker envelope, bytes | 3,908 | 3,929 |
| Persisted replay checkpoint, bytes | 1,754 | 1,763 |

The large object's independently calculated SHA-256 was
`42e0ba806f9eea358c4b32b3a25fc39f633849b5f60359194d4c746cae21b364`.
The exact machine-readable results are [small.json](small.json) and
[over-1gib.json](over-1gib.json). The data was deterministic AES-CTR output rather
than a short repeated block; full destination readback checked length and hash
against the independently seeded source. Provider checksum headers were absent;
the independent SHA-256 check did not rely on those headers.

The effective profile allowed exactly the object's byte count, five minutes
from attempt start, one active file operation and two active connector operations.
Per-source concurrency was two, queue capacity four and queue timeout one minute.
Ordinary query/connector/HTTP/broker request caps were one second; the explicit
native-file profile selected the longer allowance. S3 provider timeout was
300 seconds, with sequential 8 MiB multipart parts. Metadata and ordinary HTTP
body limits stayed at 1 MiB. No live configuration or billing state changed.

The runner, real query/credential services and broker pollers shared one Go
process. PostgreSQL held encrypted file chunks, run/checkpoint state and the sink
ledger. Datasource lookup used isolated fixture metadata matching the persisted
source. The S3 endpoint was the repository's pinned SeaweedFS 4.48 image
(`sha256:4e61d15fd35994cb1e43e1e553dff106794841fd9a99ade2fc8c8bfce4d7872d`).
The package started a disposable PostgreSQL 18.6 container. Host was Darwin arm64,
Go 1.26.10 and Docker Engine 29.8.2.

Go heap was sampled every 10 ms. RSS is the test process high-water mark, including
fixture initialization and independent seed/readback. Database size growth excludes
WAL. Source disk and logical S3 sizes are in each JSON report; physical provider
storage can remain allocated after object deletion. SeaweedFS memory observed just
after the large run was 2.868 GiB, versus 115.4 MiB before qualification; these are
snapshots, not provider peak measurements or a bounded-memory claim for SeaweedFS.
The Go process did not materialize a whole 1 GiB object. These two samples alone
are not a general capacity curve.

Commands, after starting the isolated repository S3 fixture on its selected
loopback port:

```sh
export BP_NATIVE_FILE_S3_ENDPOINT=http://localhost:<fixture-port>
export BP_NATIVE_FILE_REVISION=3a417f25fb05502a0042e4a665965b7e999e6854
BP_NATIVE_FILE_BYTES=3145745 bash scripts/go-toolchain.sh test \
  ./service/pipelinesvc/e2e -run '^TestNativeFileQualification$' -v
bash scripts/go-toolchain.sh test ./service/pipelinesvc/e2e \
  -run '^TestNativeFileQualification$' -v -timeout 15m
```

The harness independently deletes its two S3 keys and synthetic tenant; package
fixture teardown removes its PostgreSQL container. The worktree's S3 service and
volume remain available for subsequent failure qualification. This record does
not prove public API authorization, separate executor processes, slow-destination
behavior, cancellation, interruption/restart, uncertain acknowledgements, prior-good
object preservation, or provider-side crash cleanup. Those scenarios and final
review/delivery gates remain outstanding.

## Transfer-lifetime admission requalification

The same large-object command passed on clean revision
`2325234c8d2626b3e8f8ca3833d92e2136560e64`, after transfer admission was extended
through the complete pipeline attempt and poller/release lock races were fixed.
[admission-over-1gib.json](admission-over-1gib.json) records the exact result:
1,073,741,841 bytes with the same independently verified SHA-256, 21.438302208
runner seconds, 56,652,960 bytes sampled heap peak and 120,373,248 bytes process
RSS high-water mark. Component scope, measurement limits and fixture versions
remain as described above. Below/at/above request limits and checkpoint recovery
refusal also passed in this invocation. This adds evidence without replacing
the earlier revision's measurements or establishing the remaining fault cases.

## Socket-fault and cancellation qualification

Clean revision `a45febf080b5c53f1fca2c968e26ae9555b8cab5` passed the full harness,
including a 1,073,741,841-byte independently verified transfer and separate
20 MiB fault payloads. [fault-qualified-over-1gib.json](fault-qualified-over-1gib.json)
records the capacity sample: 26.674733958 runner seconds, 59,549,832 bytes sampled
heap peak and 118,603,776 bytes process RSS high-water mark, with the same hash
as the previous large samples. Measurements are taken before the fault cases.

The live-worker fault cases use a loopback reverse proxy in front of the same
real S3 fixture. A source response ends after 2 MiB despite declaring the full
length: the run fails, unsealed chunks are discarded, the prior destination is
unchanged, and no sink publication occurs. An explicit new run rereads the
source. The proxy then lets S3 commit all multipart data and drops the successful
completion response. Independent full length/hash readback proves the object
exists, while the pipeline reports failure and the sink ledger remains
`in_progress`. Recovery refuses unavailable retained checkpoint input without
another source read or publication; there is no automatic whole-file retry.

Finally, a blocked upload part prevents subsequent part dispatch and publication
while holding one transfer slot. Cancellation stops the run, aborts the open
multipart upload, finishes the running request and closes admission. Full
independent readback verifies that the previous complete object survives.
The 20 MiB payload exercises multiple 8 MiB parts. This qualifies a stalled
provider request and cancellation, not executor process death or restart.
Those process-loss cases, Git subprocess/resource bounds, the remaining
per-connector qualification, final review and delivery gates remain outstanding.

The same revision also passed with a 3,145,745-byte sample under `-race`; its
report is [fault-qualified-small-race.json](fault-qualified-small-race.json).
The command adds `BP_NATIVE_FILE_BYTES=3145745` and `-race` to the invocation
above. That run uses a smaller fault payload and includes race-detector overhead,
so its memory and timing are not directly comparable to the non-race samples.

## Executor loss and replacement

Clean revision `8dc99ee68aeeab4184105e29eeb4ae7b8d65b13a` passed the full harness
under the race detector with a 20 MiB + 17-byte initial sample and 20 MiB fault
payloads. [executor-loss.json](executor-loss.json) records the separate
process-loss result. The Go toolchain was 1.26.10 on Darwin arm64; fixture
versions and the surviving runner/database component scope remain as above.

After the socket/cancellation cases, the test stopped in-process pollers and
started a real broker executor in an owned child process. The S3 fixture accepted
one 8 MiB part before the proxy blocked the second part. The test verified that
the executor died under SIGKILL, replaced the source with different bytes, and
started another executor against the same database. It kept the real thirty-second
claim lease; ambiguity was resolved about 29.89 seconds after replacement readiness.

The persisted pipeline run was `failed`, the request was `ambiguous` with no
result ciphertext, and the sink effect remained `in_progress`. Source and part
request counters showed no redispatch by the replacement or refused checkpoint
replay. Independent full 20 MiB readback matched the prior complete destination's
SHA-256 `b9185b15757f27d70445347bf25e92aac76c0e8b38ceee5b88fa7efdb3ada2c5`.
Provider inspection found one incomplete upload containing the accepted 8 MiB
part. Explicit fixture abort removed it, followed by a clean multipart listing.
The test does not install a scheduled provider cleanup policy.

The process-loss subtest took 70.67 seconds, including the earlier cancelled
attempt's admission grace and the dead executor's unchanged lease. It does not
measure executor memory or simulate whole-host, caller or database loss. The
earlier large-object records retain their original revision and measurement
scope; this smaller race run adds process-loss evidence rather than replacing
those capacity samples.

```sh
export BP_NATIVE_FILE_S3_ENDPOINT=http://localhost:<fixture-port>
export BP_NATIVE_FILE_REVISION=8dc99ee68aeeab4184105e29eeb4ae7b8d65b13a
BP_NATIVE_FILE_BYTES=20971537 bash scripts/go-toolchain.sh test \
  ./service/pipelinesvc/e2e -run '^TestNativeFileQualification$' -race -v -timeout 15m
```

Git clone/pack resource bounds, remaining per-connector qualification and final
review/delivery gates are still outstanding. No publication or deployment was
performed.

## Git resource qualification

Clean revision `f6cf58f886cefc2fa4a5ba091a6661a2059f2dab` passed native Git
clone/read/write/commit/push with independently checked full source and destination
length/SHA-256. The source was disk-backed cryptographic random data; the write
changed its final byte and published a separate destination path. The large
sample contained 1,073,741,841 bytes. This supplements the pipeline-level S3
qualification above; it does not include the broker, retained database, API,
provider authentication or deployed GitHub/GitLab services.

| Measurement | Small object | Object over 1 GiB |
|---|---:|---:|
| Independently verified bytes | 20,971,537 | 1,073,741,841 |
| Client clone/read/write/push seconds | 1.702814671 | 93.976381572 |
| Go test-process RSS high-water mark, bytes | 19,349,504 | 20,705,280 |
| Largest reaped child RSS high-water mark, bytes | 19,218,432 | 20,705,280 |
| Read checkout logical bytes | 41,978,569 | 2,147,840,332 |
| Write checkout logical bytes | 62,958,353 | 3,221,911,555 |
| Largest checkout file, bytes | 20,978,237 | 1,074,069,677 |
| Individual Git file-write limit, bytes | 109,052,416 | 2,214,593,024 |

Exact measurements are [git-small.json](git-small.json) and
[git-over-1gib.json](git-over-1gib.json). The large destination SHA-256 was
`9a5717713a93e78c4587e7c2afe958a2f99c24f987ac26be18e1603cf59bfac3`.
Each native Git process and its children inherited a 512 MiB address-space limit,
a 16 MiB Git single-allocation limit and the individual file-write cap shown
above. The total fixture deadline was five minutes, including seed setup;
reported client time excludes seed setup and independent destination readback.
Go RSS includes that fixture/readback work. Child RSS includes seed, server and
client helpers before the oracle, and is the largest reaped child, not the sum
of simultaneous processes. Neither measurement is an aggregate container peak.
Logical checkout sizes exclude provider/seed storage and are not physical or
peak scratch measurements. Limits are per process/file, not aggregate scratch
quotas; the worker still needs operator-owned container and scratch controls.

The isolated provider was a separate loopback TLS Git CGI server, with its own
test CA. It retained received packs and disk-spooled chunked requests because
CGI requires a known content length. Provider/seed helpers did not inherit the
client's per-process limits. Native Git used the private raw-blob checkout and
cache/packing settings in [NATIVE_FILE_STREAMING.md](../../NATIVE_FILE_STREAMING.md).
Linux kernel inheritance, file-size refusal, allocation refusal, cancellation,
and the 513 MiB sparse-file commit regression passed in the same fixture image.
The complete Git connector suite also passed on Linux; repository and Git
connector race suites passed on Darwin, where the retained profile explicitly
refuses unsupported process limits.

Host: Darwin arm64, Docker Engine 29.8.2, Go 1.26.10. The native Linux arm64
fixture used Git 2.47.3 in a local test image with image ID
`sha256:a2970feb685fbc0c53cfd6c04e49ada2554d905bb5eda978951f76c98d4b5a15`.
It was built by installing Git in the pinned PostgreSQL 18.6 base image
`postgres:18.6@sha256:edb0c08dc33befd0262ab3b5cf1c4f3f8cfd8b09e88c05b45adee29d6761ddb6`;
this is a disposable test tool, not an application image change. Runs used no
external network, a read-only root, all capabilities dropped, no new privileges,
128 PIDs and two CPUs. The small run used 1 GiB container memory and a 512 MiB
`/tmp` tmpfs; the large run used 2 GiB memory, a 64 MiB `/tmp` tmpfs and a private
host scratch bind mount as `TMPDIR`. Neither provider data nor credentials were
from a live tenant.

Build from the recorded revision to reproduce these measurements. For another
build, declare its matching revision. Use a native Linux arm64 Git fixture image
and an empty private scratch directory mounted at `/qualification`:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 bash scripts/go-toolchain.sh test -c \
  ./internal/repostore -o /tmp/bp1379-git-resource.test
# In the isolated container, with the compiled binary mounted read-only:
TMPDIR=/qualification BP_NATIVE_GIT_QUALIFICATION=1 \
BP_NATIVE_FILE_REVISION=f6cf58f886cefc2fa4a5ba091a6661a2059f2dab \
BP_NATIVE_GIT_BYTES=1073741841 /bp1379-git-resource.test \
  -test.run '^TestNativeGitLargeFileQualification$' -test.v -test.timeout 8m
```

The preceding failure was traced to a file-sized mmap of the working file during
`git diff --cached --quiet`. Disabling external diff/text conversion and unused
commit summaries resolved it without increasing limits. A direct sparse-file
regression fails before the change and passes afterward. The attempted explicit
pack import was removed; Git's existing large-file staging remains in use.
Large delta objects, excessive metadata or stricter executor limits can still
cause explicit refusal. These samples do not qualify every repository shape,
SSH transport, host-loss recovery or provider-side cleanup policy.

## Acceptance evidence map

The recorded revisions above own their measurements. The following tests cover
contracts and failure handling; small fake-provider tests do not qualify hosted
capacity or all cross-provider pairs. Every existing file connector uses the
same retained-reference contract, with provider-specific restrictions in
[NATIVE_FILE_STREAMING.md](../../NATIVE_FILE_STREAMING.md).

| Requirement | Evidence and scope |
|---|---|
| One complete object over 1 GiB through the pipeline | S3 capacity reports above: real query/credential services, broker, encrypted PostgreSQL chunks, runtime, checkpoint and sink ledger; independent full length/SHA-256 readback. Git has separate provider/tool qualification above. |
| Finite allowances and cross-worker admission | `TestNativeFileProfileConfiguration`, `TestNativeFileProfileDisabledByDefault`, `TestNativeFileAdmissionAndProfileBoundary`, `TestNativeFilePipelineScopeOutlivesConnectorOperations`; PostgreSQL `TestOrganizationFileAndConnectorAdmission` and `TestDeniedPollerCandidatesReleaseLocks` cover independent pollers, continuation admission, cancellation, expiry and lock release. |
| Tenant/claim isolation, encryption, retention and corruption | PostgreSQL `TestRetainedFileCapabilities` and `TestRetainedFileRestrictedExecutor`; file-transfer service `TestRetainedFileRefusesAlteredInput` and `TestRetainedFileFailureDiscardsUnsealedInput` cover authenticated chunks, length/digest, missing/unsealed input and failed-source discard. |
| References across every file connector | Broker `TestRetainedFileResponseRoundTripAndPrivacy` and runtime `TestRetainedFileReferenceRemainsRuntimeOnly` cover FTP, Git, S3, Workspace and HTTP. They verify private reference transport, cloning, no body reconstruction and public/checkpoint exclusion. |
| Below/at/above bytes, backpressure, cancellation and slots | Full-path harness exercises 15/16/17 bytes under a 16-byte request allowance, a blocked multipart part, absence of subsequent parts, cancellation, multipart abort, admission release and prior-good destination readback. |
| Source interruption, uncertain completion and executor restart | Full-path socket-fault and executor-loss reports above: cut source discards unsealed input; lost completion remains unresolved; SIGKILL/replacement preserves the prior destination, does not reread a changed source or redispatch, and refuses checkpoint replay. Independent fixture abort demonstrates orphan cleanup without claiming an installed lifecycle policy. |
| SQL reuse and authoring | Neutral `filestream.Producer`/Store contracts and retained references are independent of file-store providers. The locally validated FTP-to-S3 example uses existing file contracts; SQL row scanning, source consistency, row limits and serialization remain separate work. |

| Connector | Additional provider/component evidence |
|---|---|
| FTP | Isolated provider round-trip, verified temporary upload/rename, checksum-failure preservation of prior output and explicit no-overwrite refusal in `service/datasourcessvc/connector/ftp/e2e/stream_test.go`. Socket cancellation test interrupts blocked I/O and refuses sockets registered after cancellation. |
| Git/GitHub/GitLab | Shared Git adapter: raw blob/attribute and literal-path tests, selected-only checkout, pre-checkout size refusal, regular-file/path guards and group cancellation. Linux process/allocation/file limits and the independently verified large Git run are recorded above. Hosted-provider authentication and SSH capacity are not established by the loopback TLS fixture. |
| S3 | Sequential multipart size/hash verification, abort and conditional-completion checks in SDK/component tests; full pipeline capacity and real-provider fault evidence above. |
| HTTP | Real loopback source truncation returns no file reference. A 200 response after one byte of an 8 MiB upload is uncertain. Verified empty input, digest refusal, authorized keyed body reopening, redirect refusal, rejection and cross-chunk credential scanning have hermetic coverage. |
| Google Workspace | Official Drive SDK bounded upload chunks, short/oversized/premature-completion refusal, real TLS source truncation and blocked-download cancellation. Connector tests cover selected-file read, retained-file create/update and native PDF export. SDK fixtures do not establish live Google quotas, sessions or hosted capacity. |

No deployed tenant limits, provider lifecycle policy, billing state or historical
file-download API are changed. Missing checkpoint references explicitly refuse
recovery; there is no general offset resume or exactly-once guarantee. Publication
and deployment remain outside this local evidence; Operations capacity requires
independent qualification.
