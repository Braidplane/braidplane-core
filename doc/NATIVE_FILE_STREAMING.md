# Bounded file transfer boundary

Native file transfers cover FTP, Git, S3, HTTP and Google Workspace file
operations. Streaming SQL rows and incremental serialization are separate work
that can use the same byte-transfer boundary described here.

The shared transport, retained-input layer and provider adapters are implemented.
The default still uses bounded in-memory file payloads. An explicit, disabled-by-default
organization profile selects retained streaming and database-coordinated admission.
The [local qualification report](qualification/native-file/core-split.md) records
core split evidence. Historical monorepo tests and local runs do not establish
deployed Custom capacity.

## Producers and consumers

A byte producer can read an existing object or serialize records incrementally.
Neither transport nor destinations should depend on the producer's connector
type. Readers and writers use synchronous I/O so a slow consumer stops upstream
reads. Each operation carries a context and a positive total byte limit. Blocked
provider I/O must observe that context; checking cancellation between reads is
insufficient to interrupt a blocked socket.

`pkg/connector/filestream.Copy` copies with a 64 KiB buffer, calculates the
full SHA-256 and byte count, and rejects input beyond the limit without writing
an extra byte. A failed copy leaves invalid partial output. Its caller must
prevent publication and clean up that output.

The SQL producer additionally owns typed row scanning, row limits, a consistent
source view and serialization. CSV headers must come from declared query
columns before rows are emitted; discovering the union of all row keys requires
materialization. JSON must preserve existing type/decimal rules. SQL transforms
and structured file parsing do not become streaming simply by using a byte
writer at the final destination.

## Retained input and broker transport

The legacy file path serializes entire byte sidecars into one encrypted request
or response, and runtime bindings clone those bytes. The new broker protocol
can instead carry retained-file references, and runtime file bindings forward
those references. References are excluded from public result JSON and replay
checkpoints. The [native file profile](../CONFIG.md#native-file-qualification-profile) selects
this path for the configured organization and supported native operations. PostgreSQL limits an individual field
to 1 GiB, so the legacy path cannot carry arbitrarily large objects.

The retained-input layer now provides an opaque reference and bounded encrypted
chunks, with a manifest sealed only after complete source consumption. The explicit profile is required to select this layer.
`service/filetransfersvc` owns encryption, hashing and streaming I/O;
`postgres/retainedfiles` owns SQLC-backed persistence. Each encrypted chunk is
at most 1 MiB of content plus its authenticated identity and encryption overhead.
PostgreSQL remains the canonical owner, so workers do not require a shared local
filesystem. A manifest records complete length, digest and expiry; incomplete
manifests must never be usable as sink inputs. Each chunk must authenticate its
organization, artifact identity and sequence as well as its bytes. Retrieval
must verify sequence, final length and digest rather than trusting a manifest
alone.

Executor access must retain the existing request/claim capability boundary:
no general tenant-table reads, no cross-organization artifact references, and
no writes from cancelled, expired or replaced claims. Runtime and broker
serialization must carry references without reconstructing the full body.
Diagnostics and public API responses continue to expose only safe file metadata.

The source execution request owns retained input lifetime; its expiration caps
the requested retention and deletion cascades through the manifest and chunks.
Failed producers attempt immediate discard; stale or cancelled claims cannot
mutate storage, so expiry cleanup remains necessary after cancellation or host
loss.

Retention is explicit and finite. Missing, expired, incomplete or corrupt input
must refuse replay. A retry must not silently reread a changing source and
claim it recovered the original input. Cleanup must cover abandoned unsealed
input, expired retained input and provider-side partial uploads.

## Destination safety

A destination consumes verified retained input with a known length and digest.
Successful source consumption does not imply successful destination delivery.
Publication must stay behind existing sink-effect preparation and dispatch
checks. Unknown write outcomes remain unknown until independently resolved;
there is no general exactly-once claim.

The S3 provider primitive uses sequential 8 MiB multipart parts and publishes
only after checking the full input length and SHA-256. No-overwrite uses a
conditional completion request. Completion has one application SDK attempt;
a failed acknowledgement is returned as an unknown outcome. Failed uploads
attempt abort with a separate bounded cleanup context. Host loss, a lost upload
creation acknowledgement or failed abort additionally require provider lifecycle
cleanup; in-process abort cannot prove cleanup after a crash.

The shared remote-file adapter selects streaming for explicit `format: file`
reads with a positive file allowance, and for retained-reference writes. Missing
storage or provider support fails explicitly; it never falls back to buffering
a retained file. Small inline file writes remain bounded by their file allowance.

| Connector | Streaming boundary | Provider restrictions |
|---|---|---|
| S3 | SDK response body to retention; retained input to sequential multipart upload | Completion requires exact length/digest. Atomic no-overwrite depends on conditional-completion support. Provider lifecycle cleanup remains necessary after host loss or an unknown creation acknowledgement. |
| FTP | Passive data streams; verified temporary upload followed by rename | Streaming writes require overwrite and server rename/delete permissions. FTP has no atomic conditional rename. Failure cleanup reconnects with a bounded context. Host loss can leave temporary files; recovery must not assume the rename outcome or silently resume them. |
| Git | Selected raw-blob checkout, descriptor-pinned I/O, verified replacement, commit and push | Bounded retained streaming requires a Linux executor with process limits. Atomic no-replace applies within the private checkout. Failed push acknowledgement does not prove remote rollback. |
| Google Workspace | Drive download and native PDF export to retention; bounded SDK file-upload chunks | Selected-file/folder authorization remains required. File creation retains its unsafe replay policy. Upload acknowledgement must match the target and byte count, with complete input consumption and EOF verification. Google provider limits still apply; source body failures do not resume a partial retained file. |
| HTTP | Native-file response bodies to retention; retained `input` bodies with repeatable reads for authorized keyed retries | Existing media-type selection determines file output. Streaming file responses do not support JSON acknowledgement checks or pagination. File writes forbid redirects and require 2xx status after complete input verification; an early success response is uncertain. Configured JSON acknowledgement checks still apply to ordinary receipt bodies. Endpoint atomicity and idempotency remain provider-specific. |

Supported native sources and sinks compose through the `file` representation,
subject to those provider restrictions. FTP, Git, S3, Workspace and HTTP results
all preserve the retained reference through broker transport and runtime binding;
no connector-pair-specific transport is required. This interface support is
broader than the recorded capacity evidence: the complete pipeline capacity and
executor-loss runs use S3 to S3. The qualification report distinguishes each
connector's component/provider tests from full pipeline and hosted-provider proof.

On Linux and Darwin, Git commands use a separate process group so cancellation
also kills transport and pack helpers. Waiting for inherited output pipes is
bounded to one additional second. Ordinary commands retain their ten-second
timeout and bounded output capture. Positive native-file allowances require a
finite caller deadline and a Linux executor. Native commands share that transfer
deadline, including clone, commit and push; capability probes keep their shorter
connection-test bound. Unsupported workers refuse before opening retained input.

Explicit Git `format: file` operations prepare a private checkout without
line-ending, ident, encoding or clean/smudge conversions. Local
[attribute overrides](https://git-scm.com/docs/gitattributes) are installed before
checkout, so reads use stored blob bytes and commits preserve the verified input.
Only the selected file is materialized; other tracked paths stay in the index
and object database. A positive file allowance is checked against the blob size
before checkout. Directory, symbolic-link and Git metadata targets are rejected.
File paths are literal during checkout, staging and commit, including wildcard
characters. A missing target can be created, including in an empty repository.
Git LFS pointers remain pointer files; this path does not fetch LFS objects.
Ordinary CSV/JSON/spreadsheet checkouts retain repository attribute behavior.

The private native checkout sets `core.bigFileThreshold` to 1 MiB so Git selects
its [large-file handling](https://git-scm.com/docs/git-config) earlier. Pack mmap
windows are 1 MiB, the mapped-pack budget is 8 MiB, the delta-base cache is 8 MiB
per thread, pack window memory is 8 MiB and packing uses one thread. Automatic GC
and maintenance are disabled
in that checkout. These settings persist for its commit and push, and reduce
common sources of file-sized buffering and background work.
Native clones use the Git transport even for local fixtures, retain fetched
objects in packs, omit tags and do not recurse into submodules.
Staged-change checks disable external diff and text conversion; commits suppress
unused diff summaries. This keeps Git from mapping an entire working file to
perform content-based diff processing. Publication still requires verified
replacement, staging, commit and push.

For an explicit native-file allowance, each Git process and its children inherit
a 512 MiB address-space limit and a Git allocation limit of 16 MiB per allocation.
Each Git-written file is limited to twice the file allowance plus 64 MiB, rounded
up to a 512-byte boundary. Existing stricter kernel limits are retained. A fixed
shell wrapper installs limits before executing the resolved Git binary; setup
failure cannot fall back to an unlimited command. Requested limits and the
deadline are logged without file contents or credentials. Oversized delta objects
or repository metadata can be refused when Git cannot process them within these
bounds. There is no full-body buffering fallback.

These are per-process and per-file controls, not aggregate filesystem/cgroup
quotas. The worker still needs sufficient scratch storage and its operator-owned
container limits. The private checkout uses `TMPDIR`; selected file, pack and
new object storage add to the retained-input database footprint. No deployed
worker limits or scratch mounts are changed by qualification.

HTTP credential-echo protection scans raw and JSON-escaped credential material
across chunks without retaining the complete body. It conservatively examines
quoted strings even in non-JSON files. Basic, bearer, API-key and secret-header
values remain outside returned headers and file payloads when detected.

These boundaries preserve provider-specific safety rather than claim general
atomic delivery or resumability. Missing retained references in checkpoints
refuse native-file replay. A destination error after dispatch must retain the
existing sink-effect and provider acknowledgement classification.

## Pipeline specs

The [FTP to S3 example](../examples/pipelines/yaml/native-file-transfer.pipeline.yaml)
uses the existing query/sink shape, explicit `format: file` operations and
`representation: file` contracts. The source writes and seals encrypted retained
chunks; the sink then reads that same input. An operator's organization profile
selects bounded retained streaming and its allowances. There is no new pipeline
`streaming` flag, and a pipeline cannot grant itself larger limits. Without the
profile, ordinary small-file limits still apply.

The example declares `idempotency_class: unsafe`, so it authorizes no automatic
sink replay. Streaming does not grant a replay policy. SQL row streaming,
serialization and export authoring syntax are not defined here.

## Allowances and acceptance

The explicit profile separately bounds total bytes, a native-file deadline
measured from pipeline-attempt start (or standalone operation start), active
pipeline attempts using native files, and active datasource-bound connector
operations. One pipeline attempt holds one transfer slot across source reads,
S3 preparation, gaps with retained input, destination writes and attempt
completion. Multiple native-file branches in that attempt share its transfer
slot; each active operation still consumes a connector slot. A standalone file
operation holds its transfer slot through its complete operation. Transfer and
connector counts are independent budgets.

HTTP conservatively claims transfer admission before its media type is known.
Existing per-source queue deadlines and provider timeouts still apply. Fixed
buffer sizes are not capacity allowances. Database claim admission serializes
decisions across processes and data sources. A successful connector operation
releases its I/O slot but does not finish the pipeline's transfer scope. Attempt
completion releases that scope. Caller loss has a finite native-file deadline;
released scopes still count while running or ambiguous provider work may remain
within its lease and ten-second cleanup grace. Admission metadata expires after
the deadline plus fifteen minutes; unresolved execution-history obligations can
outlive admission metadata without retaining a slot or permitting dispatch.

Startup logs expose the configured profile; admission logs expose each request's
file classification, effective concurrency caps and deadlines. Ordinary connector
admission metrics still apply. Operators with request-table authority can inspect
one organization's live state using the canonical request fields, for example:

```sql
SELECT request.state, request.file_transfer,
       request.org_connector_limit, request.org_file_limit,
       count(*) AS requests, min(request.deadline_at) AS earliest_deadline,
       min(request.lease_expires_at) AS earliest_lease
FROM execution_requests request
JOIN organizations organization ON organization.id = request.organization_id
WHERE organization.slug = 'synthetic-file-fixture'
GROUP BY request.state, request.file_transfer,
         request.org_connector_limit, request.org_file_limit;
```

```sql
SELECT count(*) AS active_transfers,
       min(scope.concurrency_limit) AS minimum_admitted_limit,
       min(scope.deadline_at) AS earliest_transfer_deadline
FROM file_transfer_scopes scope
JOIN organizations organization ON organization.id = scope.organization_id
WHERE organization.slug = 'synthetic-file-fixture' AND scope.claimed_at IS NOT NULL
  AND ((scope.released_at IS NULL AND scope.deadline_at > clock_timestamp())
    OR EXISTS (SELECT 1 FROM execution_requests request
      WHERE request.file_transfer_scope_id = scope.scope_id
        AND request.organization_id = scope.organization_id
        AND request.state IN ('running','ambiguous')
        AND request.lease_expires_at + INTERVAL '10 seconds' > clock_timestamp()));
```

The executor cannot run this diagnostic table query; it retains capability-only
access. Matching binaries, core migration 3 and refreshed runtime grants are required.
The self-hosted installer applies [executor grants](../scripts/installation/execution-grants.sql)
after migration. Other distributions must grant the same retained-file capabilities.
Do not roll only one side of the encrypted request contract.
No deployed tenant limits change as part of synthetic qualification.

Acceptance requires isolated end-to-end transfer and independent verification
of one object exceeding 1 GiB, with revision, effective settings, checksum,
length, elapsed time, peak memory, storage use and component scope recorded.
It also requires per-connector behavior, below/at/above limits, slow consumers,
cancellation and slot release, interruption/restart, prior-good destination
handling, partial cleanup and uncertain acknowledgements. Provider fixture
unit tests alone do not satisfy those requirements. Operations capacity and SQL
workload qualification remain separate.

## Local full-path qualification

`TestNativeFileQualification` in `service/pipelinesvc/e2e` runs the actual query
and credential services, broker pollers, pipeline runner, PostgreSQL run store,
sink ledger and encrypted retained chunks against a separately started local
S3-compatible fixture. The query/API caller and executor run in one Go test
process during the capacity measurement. Datasource lookup uses isolated fixture
metadata matching the real persisted source; this does not exercise the public
HTTP authorization boundary or browser. A later fault phase uses separate
executor processes. The package starts its own disposable PostgreSQL.

Start the repository's `s3-connector-test` and setup services on an isolated
worktree Compose network, then use their loopback port. The test accepts only a
loopback HTTP endpoint, uses the repository's synthetic fixture credentials and
bucket, creates unique object keys, and removes only its objects and tenant.
Run from a clean committed checkout so the declared revision matches the source:

```sh
export BP_NATIVE_FILE_S3_ENDPOINT=http://localhost:<fixture-port>
export BP_NATIVE_FILE_REVISION=$(git rev-parse HEAD)
bash scripts/go-toolchain.sh test ./service/pipelinesvc/e2e \
  -run '^TestNativeFileQualification$' -v -timeout 15m
```

The default object is 1 GiB + 17 bytes. For a wiring check, set
`BP_NATIVE_FILE_BYTES=3145745`; this changes both the generated object and the
explicit profile byte limit. The five-minute transfer profile, one file slot,
two connector slots, one-minute queue timeout, ordinary one-second request caps
and provider's 300-second timeout are recorded or fixed in the harness. No live
configuration is changed.

The source is a disk file containing deterministic AES-CTR output, seeded by
an independent SDK client. Destination readback uses that client and checks the
complete length and SHA-256 against the source. Evidence records elapsed runner
time, sampled Go heap (10 ms), the test process's RSS high-water mark, retained
ciphertext and relation size, database growth, seed disk size, logical S3 object
bytes, largest encrypted chunk/envelope, checkpoint size, profile and revision.
RSS includes fixture initialization and independent seed/readback work. Database
growth excludes WAL and provider storage overhead; database/server memory and
crash recovery need separate evidence. A successful run proves only its stated
component scope and object size; it does not complete the remaining acceptance
scenarios by itself.

The [core local evidence](qualification/native-file/core-split.md) records the
independently verified 1 GiB + 17 byte run and its exact revision and scope.

The current harness additionally exercises 15/16/17-byte inputs under an explicit
16-byte per-operation allowance below the operator ceiling. Below/at succeed;
above fails with the typed byte-limit error and preserves an independently seeded
prior-good destination. It checks that no unsealed input or active request remains.
A checkpoint-recovery case requires the explicit missing-file-input refusal and
verifies that neither source nor sink is redispatched. The qualification size
must therefore be at least 17 bytes. These small scenarios supplement the recorded
large-object result; they do not simulate a process dying during provider I/O.

A loopback reverse proxy also cuts a source response after partial consumption,
drops an otherwise successful multipart-completion acknowledgement, and blocks
an upload part until cancellation. Fault payloads use up to 20 MiB, independent
of the capacity sample. The checks require source failure to discard unsealed
chunks and preserve the prior destination; completion uncertainty to leave the
sink ledger unresolved without another publication; and a blocked upload to
hold its transfer slot without dispatching subsequent parts. Cancellation must
abort the multipart upload, finish the running request, close transfer admission
and preserve the previous complete destination. Independent SDK readback and
multipart listing bypass the proxy.

The final fault phase stops in-process pollers and starts an owned executor
process. With a fault payload over 8 MiB, the provider accepts one 8 MiB part
before the next part is blocked. The test kills the executor, changes the source
object and starts a replacement against the same retained-input database. It
keeps the real thirty-second claim lease: the replacement must wait for expiry,
mark the interrupted query ambiguous and leave the sink ledger unresolved.
Neither automatic processing nor refused checkpoint replay may reread the
changed source or redispatch the write. Independent readback verifies the prior
complete destination. Multipart listing must expose the part left by process
death, followed by explicit fixture abort and a clean listing. This exercises
executor replacement with a surviving caller and database, not whole-host or
database failure. Provider lifecycle policies remain operational prerequisites;
the test does not install or qualify a provider's scheduled cleanup policy.
