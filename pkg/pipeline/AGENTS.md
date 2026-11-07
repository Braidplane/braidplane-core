# Pipeline contributor guide

This guide applies to `pkg/pipeline/`. Start here for pipeline changes elsewhere,
then follow that directory's scoped guide. Use [contribution rules](../../CONTRIBUTING.md),
[setup](../../COMMUNITY_BUILD.md), [quality gates](../../doc/QUALITY_GATES.md)
and [package boundaries](../../doc/PACKAGE_BOUNDARIES.md) as the canonical rules.

## Core pipeline packages

| Concern | Location |
|---|---|
| YAML/JSON contract, loading and CUE schema | `pkg/pipeline/spec/` (`types.go`, `schema.cue`); shared enums in `pkg/model/` |
| Structural and compatibility validation | `pkg/pipeline/validate/`, `pkg/pipeline/graph/`; tags in `pkg/structvalidate/` |
| Payloads, run contracts and neutral execution helpers | `pkg/pipeline/payload/`, `pkg/pipeline/run/`, `pkg/pipeline/runtime/` |
| SQL transform validation, inference and evaluation | `pkg/pipeline/transform/sql/` |

## Related integration and consumers

These locations sit outside `pkg/pipeline/`. Follow their scoped guides when
a feature changes them; pipeline packages must not import these layers.

| Concern | Location |
|---|---|
| Deployment, saved bindings, admission and run inspection | `service/pipelinesvc/` |
| Execution orchestration and sinks | `service/pipelineruntime/`; dispatch in `service/executionbroker/` |
| Queues and triggers | `service/worker/`, `service/pipelineschedulesvc/`, `service/pipelinewebhooksvc/`, `service/pipelinetriggersvc/` |
| Connector integration and provider adapters | `service/datasourcessvc/connector/`; neutral contracts and catalog in `pkg/connector/` |
| Public HTTP, CLI and UI | `transport/http/`, `api-schema/schema.yml`, `cli/cmd/braidplane/`, `frontend/` |

Keep `pkg/pipeline/` runtime-neutral: no service, persistence, HTTP, frontend or
generated API imports. Backend domain aliases live in `pkg/domain/model/`.
Connector adapters implement provider I/O; pipeline orchestration owns execution
order and policy. Keep persisted identity, tenant authorization and credential
storage/refresh in backend integration. Use the current connector paths above;
follow any connector-scoped guide present when editing them.

Preserve organization-scoped lookups and HTTP authorization. Follow the service
guide for its explicit reauthorization exceptions. Preserve cancellation,
budgets, resource scopes, host protections, read-only enforcement, safe typed
errors and sink write/replay guards. Never log credentials or row payloads.

## Change a feature

1. Trace an existing spec through loading, validation, inference and execution.
   Change the owning layer and its failure cases rather than adding a parallel path.
2. For spec fields, update Go types, `schema.cue`, loading/validation tests and
   YAML/JSON examples together. Check compatibility, graph references and output
   contracts. For SQL, keep validation, inferred contracts and runtime semantics aligned.
3. Trace affected consumers: service admission, persisted run state, queued jobs,
   connector capability metadata, CLI mapping and UI. Update only those affected.
   Follow [configuration](../../CONFIG.md) for configuration changes and
   [quality gates](../../doc/QUALITY_GATES.md) for immutable migrations and generation.
4. API changes start in `api-schema/schema.yml`; `task generate-api` updates
   server/client and `cli/api/` projections. Use `task generate-templ` for Templ
   and the source's `go:generate` directive for enums/wrappers. Include generated
   output. SQLC and migration checks require a disposable core-only database.
5. Update [pipeline specifications](../../docs-public/getting-started/pipeline-specifications.md)
   and affected `examples/pipelines/` or getting-started examples for user-visible behavior.

## Verify

Run from the repository root with the pinned wrapper; the CLI shares this module.
Select affected packages, then broader checks from the quality gates:

```sh
bash scripts/go-toolchain.sh test -short ./pkg/pipeline/...
bash scripts/go-toolchain.sh test -short ./service/pipelinesvc ./service/pipelineruntime
task test-cli
```

Use synthetic fixtures in `pkg/pipeline/spec/testdata/`, `pkg/pipeline/fixture/`,
`pkg/testutil/pipelinefixture/` and the owning package's tests. Provider/database
integration belongs in existing `e2e` packages, including `service/pipelinesvc/e2e/`,
`service/queriessvc/e2e/` and `postgres/tenantboundaries/e2e/`. Inspect their fixture
setup before running them; unit success does not prove integration behavior.
For changed browser journeys, follow [frontend E2E](../../frontend/e2e/README.md).
No private distribution or live provider credentials are required for hermetic tests.

### Existing-feature walkthrough: SQL HAVING

`pkg/pipeline/transform/sql/having.go` validates grouped references and evaluates
post-group predicates. `having_test.go` exercises validation, inferred output
contracts and execution together, including aliases, NULLs, exact decimals,
budgets and cancellation. The SQL remains a transform string, so this feature
needs no new spec field or API projection. Its public contract is documented in
the pipeline specifications' SQL section. Trace service orchestration separately
in `service/pipelineruntime/runner_transform_tdd_test.go`.

These focused hermetic checks need no database or application process:

```sh
bash scripts/go-toolchain.sh test -short ./pkg/pipeline/transform/sql -run TestHaving
bash scripts/go-toolchain.sh test -short ./service/pipelineruntime -run TestRunner_RunTransform_AggregateRuntime
```
