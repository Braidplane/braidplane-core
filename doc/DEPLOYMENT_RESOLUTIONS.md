# Deployment resolutions and attempt snapshots

A composition deployment selects an exact stored portable compiled-plan digest
and a complete environment-specific resolution. Authored spec hash, template
digest, executable compiled digest and deployment resolution digest are separate
identities. Selecting one compilation never searches by spec hash or `latest`.

The deployment API accepts `compiled_plan_digest` with `expected_revision`.
Zero requires an absent deployment; a positive revision must match the current
pointer. Every successful pointer update advances the revision, including
same-spec changes. The database compares and swaps the pointer atomically.
Concurrent stale candidates fail rather than replacing the winner. A same-version
update cannot remove a recorded resolution and silently return to flat execution.

`resolution_inputs` supplies explicit non-secret string values and source
revisions for names declared by the portable ODCS projection template. The
compiler validates the complete specialized projection and retains defaults,
relationships and source provenance. Unknown names, missing revisions, nested
expansion and runtime secret expressions fail. Deployment cannot select another
operation, contract revision, mapping or policy through an input. Runtime input
values never change executable shapes.

Resolution captures one consistent database view of the environment bindings,
selected typed runtime variables and their revisions, resolved executable
pipeline, saved-operation snapshots, fixed resource scopes and public connector
configuration identity. Configuration identity uses the public connector
contract; embedded URL credentials, query strings, fragments, unsupported
configuration and late substitutions fail before any digest is produced.
Raw connector configuration and credential values are not stored as provenance.
Configuration identity alone does not add a confinement claim to an operation
without a supported [resource mapping](RESOURCE_SCOPES.md).

An accepted queued run carries its selected resolution identity. Run start
requires current bindings, selected variables and safe configuration to still
match. Drift requires explicit validated resolution. Once execution begins,
each dispatch uses the captured physical source and operation; a concurrent
binding edit cannot switch later steps to another source. The connector validates
its final selector against any captured scope before I/O. Every dispatch checks
current resource usability and credentials. The worker repeats authority checks
for a claimed, unexpired request, including deactivation, revocation and newly
read-only sources. Its UUID-scoped database capability returns only a boolean;
it grants no tenant-table or credential-store reads.

Credential values stay in the existing encrypted execution channel. Rotation
is independent of configuration identity. Each dispatch resolves current
credentials; a queued request whose admitted credential identity or revision changed
fails before I/O and needs fresh admission. Older standard envelopes retain their
lifecycle-reference reader. Older Git envelopes with only a credential hash
require fresh admission; a hash cannot establish current credential authority. Recorded attempt admissions contain
only step/source identities and protected credential lifecycle references,
never secret plaintext or a hash of a secret value. Admissions do not prove
provider execution or delivery.

Run records retain the selected resolution digest. Recovery checkpoints retain
its exact target bytes and identity together with the existing captured outputs.
Reload verifies stored digests, scope, compiler support and the matching portable
plan. Missing, corrupt or unsupported material fails before effects. Historical
flat deployments and checkpoints keep their existing reader; no historical pins
or outcomes are backfilled.

Rollback supplies `resolution_digest` with the version hash and current deployment
revision. It reloads the old executable bytes and inputs, then revalidates the
same resources, configuration and permissions. Changed targets, shape-affecting
values, inactive resources or revoked credentials reject rollback. A fresh
resolution records the current binding/input revisions and links the original
resolution in `revalidated_from`. Rollback cannot supply replacement inputs or
reuse a static spec-only Proof receipt as proof of the richer target plan.

Rollback selects future work only. Existing attempts retain their snapshots.
Delivered or partially delivered writes remain delivered; committed source
checkpoints remain committed. Recovery keeps the original effect/replay identity
and idempotency key and uses the existing sink ledger. Selecting a prior version
or resolution neither undoes effects nor makes an unsafe retry safe.

Resolution selection and execution are available without a rollout control.
Authorization, validation, worker authority checks and redaction remain mandatory.
The expand migration and executor capability grants must precede upgraded worker
startup. Supported flat and rich readers remain available through retained-version
and rollback windows. Rollback requires a compatible application revision and
preserves the original effects, targets and checkpoints.
