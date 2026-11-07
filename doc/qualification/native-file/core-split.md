# Core split native-file qualification

On 2026-11-03, the local S3 to encrypted PostgreSQL chunks to S3 pipeline
passed at core revision `647e8a4489e5afec860707e49300c67ab996d7c7`. The test
independently verified 1,073,741,841 bytes and SHA-256
`42e0ba806f9eea358c4b32b3a25fc39f633849b5f60359194d4c746cae21b364`.
The runner took 21.26 seconds, with a sampled Go heap peak of 61,057,544 bytes
and test-process RSS high-water mark of 121,733,120 bytes. Retained ciphertext
was 1,073,845,281 bytes; database growth was 1,116,069,888 bytes.

The test also passed byte-limit, broken source, lost completion, cancellation,
and real executor-loss scenarios. After executor loss, the request remained
ambiguous, the prior destination was preserved, and explicit multipart cleanup
removed the incomplete upload. The executor-loss payload was 20 MiB.

This is local service-level evidence using PostgreSQL and SeaweedFS. It excludes
API and browser memory and is not deployed capacity or provider-wide acceptance.
The [original monorepo evidence](README.md) is historical.

## Rebased Core qualification

On 2026-11-06, the complete harness passed on clean Core revision
`6e26f3aed183c5f6c2c881f5b721e9072525082b`, after the Core main rebase and
credential fixture update. The independent S3 readback verified 1,073,741,841
bytes and SHA-256 `42e0ba806f9eea358c4b32b3a25fc39f633849b5f60359194d4c746cae21b364`.
The pipeline took 22.20 seconds. Sampled Go heap peaked at 60,989,184 bytes;
the test process RSS high-water mark was 118,947,840 bytes. PostgreSQL retained
1,073,845,281 encrypted bytes and grew by 1,116,061,696 bytes, excluding WAL.
[core-rebased.json](core-rebased.json) records the measurements and profile.

The same run passed below/at/above byte limits, cut-source and lost-completion
faults, blocked-upload cancellation, and executor loss and replacement. The
executor-loss case used a 20 MiB payload: after one 8 MiB part and a killed
executor, recovery left the request ambiguous and preserved the prior object.
The fixture explicitly aborted the orphaned upload. These results do not install
or qualify a provider cleanup policy.

The runner, query and credential services, and broker pollers shared one Go
process. PostgreSQL and SeaweedFS ran in isolated containers; an independent
SDK seeded and read back the object. The run used Darwin arm64, Go 1.26.10,
Docker Engine 29.8.2, PostgreSQL 18.6, and SeaweedFS 4.48. A SeaweedFS memory
snapshot during the fault cases was 2.368 GiB; it is not a peak measurement.
API, browser, deployed services and aggregate provider memory remain outside
this qualification. No deployed capacity claim follows from this local test.

```sh
BP_NATIVE_FILE_S3_ENDPOINT=http://127.0.0.1:<fixture-port> \
BP_NATIVE_FILE_REVISION=6e26f3aed183c5f6c2c881f5b721e9072525082b \
bash scripts/go-toolchain.sh test ./service/pipelinesvc/e2e \
  -run '^TestNativeFileQualification$' -count=1 -v -timeout 15m
```

## Rebased Git and FTP provider checks

On 2026-11-06, Core revision `0b3ecf7a81d629586f3b8b990551ff9b9d589b91`
passed the Linux Git qualification with 1,073,741,841 bytes. An independent
full Git readback matched SHA-256
`59b2d4efbfc713bbec5caadf546d50f21c53ac89c86fbc169bca2f79902bda15`.
The clone, read, write and push took 79.27 seconds. The Linux arm64 fixture used
Go 1.26.10 and Git 2.47.3, with a 512 MiB per-process address-space limit and
a 16 MiB single Git allocation limit. The test process peaked at 19,939,328
bytes RSS; the largest reaped child also peaked at 19,939,328 bytes. These
figures include fixture work and are not aggregate memory measurements. The
separate TLS CGI Git server spooled requests to disk and retained packs; the
broker, retained-file store and API were outside this test.

On the same revision, Linux tests passed for inherited limits, file and
allocation limits, cancellation of transport children, and committing a
513 MiB worktree file without mapping the worktree. The isolated FTP provider
test passed streaming read and write, digest and size verification, preservation
of the previous destination after a failed checksum, and rejection of
unsupported no-overwrite writes. These are local fixture results, not hosted
provider acceptance.

## Qualification after Core reconciliation

On 2026-11-07, the complete S3 harness passed on clean revision
`de37013a8edcecdaf2f065b47c54cb4304cfa123`, after reconciling current Core
and its migration catalog. Independent readback verified 1,073,741,841 bytes
and SHA-256 `42e0ba806f9eea358c4b32b3a25fc39f633849b5f60359194d4c746cae21b364`.
The runner took 25.76 seconds; sampled Go heap peaked at 56,633,016 bytes and
the test process RSS high-water mark was 117,686,272 bytes. PostgreSQL retained
1,073,845,281 encrypted bytes and grew by 1,116,031,824 bytes, excluding WAL.
[The measurement record](core-current.json) includes the explicit profile,
storage and executor-loss evidence.

Below/at/above limits, source and completion faults, cancellation and executor
replacement passed in the same run. After a killed executor, recovery preserved
the prior object, marked the request ambiguous and refused redispatch; the
fixture explicitly aborted the orphaned upload. Darwin arm64, Go 1.26.10,
Docker Engine 29.8.2, PostgreSQL 18.6 and SeaweedFS 4.48 were used. The runner,
services and broker pollers shared one Go process; PostgreSQL and S3 were
separate containers. API, browser, aggregate provider memory, whole-host loss
and provider lifecycle policy remain outside this test.
