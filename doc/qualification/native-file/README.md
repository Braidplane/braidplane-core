# Native-file qualification

The implementation and provider limits are in [Native-file streaming](../../NATIVE_FILE_STREAMING.md).
Raw JSON records retain exact revisions and settings. The S3 record below covers the simplified implementation. Earlier profile/admission
measurements remain historical.

## Recorded capacity samples

| Revision | Scope | Evidence |
|---|---|---|
| `e6e77ebd086766231bb9805e507059ff46e0c9d3` | Simplified S3 → encrypted PostgreSQL chunks → S3; 1,073,741,841 bytes, independent length/SHA-256, 24.13 s | [S3](core-2026-11-12.json), [20 MiB executor-loss sample](executor-loss-2026-11-12.json) |
| `67b837de7c60ee03f2bce2df235d41c34fb522c9` | Separate Linux Git clone/read/write/push of 1,073,741,841 bytes; independent full readback; 111.92 s client time, 20,226,048-byte process RSS | [Git](git-2026-11-12.json) |
| `e6e77ebd086766231bb9805e507059ff46e0c9d3` | Schema 2 → 3 image upgrade; post-migration grant failure and isolated recovery | [Upgrade/recovery](upgrade-2026-11-12.json) |
| Earlier Core revisions | Previous S3 and Git samples | [S3](core-current.json), [Git](git-core-current.json), [rebased S3](core-rebased.json) |
| Historical implementation | Original capacity and fault samples | [S3](over-1gib.json), [faults](fault-qualified-over-1gib.json), [executor loss](executor-loss.json), [Git](git-over-1gib.json) |

The S3 harness uses real query/credential services, broker pollers, checkpoints,
sink ledger and encrypted chunks. It checks below/at/above byte limits, broken
source bodies, lost completion responses, slow uploads, cancellation, prior-good
output and executor replacement. Independent SDK reads verify length and digest.
The Git fixture uses a separate loopback TLS CGI provider and kernel process limits.

Local `task ci`, focused service race tests, retained-storage/admission/FTP E2E
suites, and the documentation gate passed for this candidate.

## Focused checks

```sh
bash scripts/go-toolchain.sh test -race -short ./pkg/connector/filestream ./pkg/googleworkspace ./pkg/repostore ./service/filetransfersvc ./service/executionbroker ./service/pipelineruntime ./service/datasourcessvc/connector/...
# Docker: each package starts and removes its own fixtures.
bash scripts/go-toolchain.sh test -count=1 -p 1 ./postgres/retainedfiles/e2e ./postgres/executionrequests/e2e ./service/datasourcessvc/connector/ftp/e2e
```

For the large S3 run, start the repository's S3 fixture on an isolated Compose
project and loopback port, then run from the recorded clean revision:

```sh
BP_NATIVE_FILE_S3_ENDPOINT=http://127.0.0.1:<fixture-port> \
BP_NATIVE_FILE_REVISION=<full-commit-hash> \
bash scripts/go-toolchain.sh test -tags nativefilequalification ./service/pipelinesvc/e2e \
  -run '^TestNativeFileQualification$' -count=1 -v -timeout 15m
```

Default size is 1 GiB + 17 bytes; `BP_NATIVE_FILE_BYTES` selects a smaller sample.
The harness configures ordinary file-byte and execution-time limits. Linux Git
capacity is opt-in through `TestNativeGitLargeFileQualification`; it requires
`BP_NATIVE_GIT_QUALIFICATION=1`, a full `BP_NATIVE_FILE_REVISION`, private scratch
and optionally `BP_NATIVE_GIT_BYTES`.

## Scope

Provider SDK/socket tests cover S3, FTP, HTTP, Drive and native PDF exports.
Git tests cover raw bytes, unsafe paths, limits and helper cancellation. Storage
and broker tests cover claim/tenant isolation, corrupt input, metadata/reference
agreement and omission from public JSON/checkpoints. Installed-upgrade tests
cover matching candidate tools, backups, schema/grant changes and recovery holds.

Process RSS includes fixture/oracle work. Child RSS is the largest reaped child,
not aggregate concurrent memory. Database growth excludes WAL; Git checkout
sizes are logical snapshots, not peak physical scratch. API/browser memory,
whole-host/database loss, hosted authentication/capacity and provider cleanup
policy installation remain outside these fixtures. No general capacity claim
or deployed limit change follows from them.
