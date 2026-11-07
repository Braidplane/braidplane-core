package pipelineruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
)

func disconnectedTestPipeline() *pipelinespec.Pipeline {
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "independent-flows", Name: "Independent flows"}}
	for _, name := range []slug.Slug{"a", "b"} {
		pipeline.Steps = append(pipeline.Steps,
			pipelinespec.Step{Kind: "query", Slug: name, Query: &pipelinespec.QueryRef{
				DataSourceSlug: "shared", OperationType: "sql", OperationRef: "read",
				OutputContract: incrementalCheckpointRowsContract(),
			}},
			pipelinespec.Step{Kind: "sink", Slug: slug.Slug("sink-" + name.String()), Sink: &pipelinespec.SinkSpec{
				Input: &pipelinespec.StepInput{FromStep: name}, DataSourceSlug: "shared", OperationType: "sql", OperationRef: "write",
				IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
				InputContract:    incrementalCheckpointRowsContract(),
			}},
		)
	}

	return pipeline
}

func TestRunnerRejectsDisconnectedFlowsBeforeDispatch(t *testing.T) {
	t.Parallel()
	pipeline := disconnectedTestPipeline()
	for _, mode := range []string{"run", "retained outputs", "checkpoint bootstrap", "replay", "recovery"} {
		t.Run(mode, func(t *testing.T) {
			// Unconfigured fakes fail on any connector call; invalid graphs must never reach them.
			runner := mustNewRunner(t, &testutil.FakeQueryExecutor{}, &testutil.FakeClock{})
			runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
				t.Fatal("invalid graph dispatched a sink")
				return nil
			}}
			req := RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "dev", Pipeline: pipeline}
			if mode == "retained outputs" {
				req.pinnedResults = map[slug.Slug]*pipelinerun.StepResult{"a": pipelinerun.NewStepResult(successfulSQLResult(1)), "b": pipelinerun.NewStepResult(successfulSQLResult(1))}
			}
			switch mode {
			case "replay", "recovery":
				runner.store = &fakeRunStore{
					timelines: map[string]pipelinerun.RunTimeline{"old-run": {ExecutionContext: testExecutionContext()}},
					checkpoints: map[string]pipelinerun.ReplayCheckpoint{"old-run": {
						Pipeline: pipeline, ProjectSlug: "default", EnvironmentSlug: "dev", StepSlug: "a",
						Results: map[slug.Slug]*pipelinerun.StepResult{"a": pipelinerun.NewStepResult(successfulSQLResult(1)), "b": pipelinerun.NewStepResult(successfulSQLResult(1))},
					}},
				}
				replay := ReplayRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "dev", SourceRunSlug: "old-run", InputMode: ReplayInputModePinned}
				var err error
				if mode == "replay" {
					_, err = runner.Replay(context.Background(), replay)
				} else {
					_, err = runner.Recover(context.Background(), replay)
				}
				require.ErrorIs(t, err, pipelinevalidate.ErrOrphanStep)
			case "checkpoint bootstrap":
				_, err := runner.PrepareCheckpointBootstrap(context.Background(), req)
				require.ErrorIs(t, err, pipelinevalidate.ErrOrphanStep)
			default:
				got, err := runner.Run(context.Background(), req)
				require.Nil(t, got)
				require.ErrorIs(t, err, pipelinevalidate.ErrOrphanStep)
			}
		})
	}
	require.Len(t, pipeline.Steps, 4)
}

func TestDisconnectedPlansRemainInspectableButCannotExecute(t *testing.T) {
	t.Parallel()
	pipeline := disconnectedTestPipeline()
	authored, err := json.Marshal(pipeline)
	require.NoError(t, err)
	operations := make([]contractplan.Operation, 0, len(pipeline.Steps))
	for _, step := range pipeline.Steps {
		role, operation := "query", "read"
		if step.Sink != nil {
			role, operation = "sink", "write"
		}
		operations = append(operations, contractplan.Operation{
			Step: step.Slug, Role: role, DataSource: "shared", Slug: slug.Slug(operation), Revision: "v1",
			Content: json.RawMessage(`{"data":{"statement":"{\"sql\":\"SELECT 1\"}"},"parameters":[]}`),
		})
	}
	compiledRaw, compiledDigest, err := contractplan.Compile(contractplan.Input{
		Source:   contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/independent-flows.json"},
		Authored: authored, Operations: operations,
	})
	require.NoError(t, err)
	compiled, err := contractplan.Read(compiledRaw, compiledDigest)
	require.NoError(t, err)
	portable, err := compiled.RuntimePipeline()
	require.NoError(t, err)
	resolution := deploymentplan.Plan{
		Organization: "acme", Project: "default", Environment: "dev",
		TemplateDigest: compiledDigest, CompiledDigest: compiledDigest, CompiledPlan: compiledRaw, Pipeline: portable,
	}
	for _, operation := range operations {
		resolution.Targets = append(resolution.Targets, deploymentplan.Target{
			Step: operation.Step, Role: operation.Role, Reference: "shared", DataSource: "shared",
			ConfigDigest: "sha256:" + strings.Repeat("b", 64), BindingRevision: "v1",
		})
	}
	resolutionRaw, resolutionDigest, err := deploymentplan.Encode(resolution)
	require.NoError(t, err)
	_, err = deploymentplan.Read(resolutionRaw, resolutionDigest)
	require.NoError(t, err)
	originalCompiled, originalResolution := append([]byte(nil), compiledRaw...), append([]byte(nil), resolutionRaw...)
	for _, mode := range []string{"compiled", "deployment", "retained recovery"} {
		t.Run(mode, func(t *testing.T) {
			runner := mustNewRunner(t, &testutil.FakeQueryExecutor{}, &testutil.FakeClock{})
			req := RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "dev"}
			if mode == "compiled" {
				req.CompiledPlan, req.CompiledPlanDigest = compiledRaw, compiledDigest
			} else {
				req.DeploymentPlan, req.DeploymentResolutionDigest = resolutionRaw, resolutionDigest
			}
			if mode == "retained recovery" {
				results := map[slug.Slug]*pipelinerun.StepResult{"a": pipelinerun.NewStepResult(successfulSQLResult(1)), "b": pipelinerun.NewStepResult(successfulSQLResult(1))}
				runner.store = &fakeRunStore{
					timelines: map[string]pipelinerun.RunTimeline{"old-run": {ExecutionContext: testExecutionContext()}},
					checkpoints: map[string]pipelinerun.ReplayCheckpoint{"old-run": {
						Pipeline: pipeline, ProjectSlug: "default", EnvironmentSlug: "dev", StepSlug: "a", Results: results,
						CompiledPlan: compiledRaw, CompiledPlanDigest: compiledDigest, DeploymentPlan: resolutionRaw, DeploymentResolutionDigest: resolutionDigest,
					}},
				}
				_, err = runner.Recover(context.Background(), ReplayRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "dev", SourceRunSlug: "old-run", InputMode: ReplayInputModePinned})
			} else {
				_, err = runner.Run(context.Background(), req)
			}
			require.ErrorIs(t, err, pipelinevalidate.ErrOrphanStep)
			require.ErrorContains(t, err, `["a", "sink-a"]; ["b", "sink-b"]`)
		})
	}
	require.Equal(t, originalCompiled, compiledRaw)
	require.Equal(t, originalResolution, resolutionRaw)
	_, err = contractplan.Read(compiledRaw, compiledDigest)
	require.NoError(t, err)
	_, err = deploymentplan.Read(resolutionRaw, resolutionDigest)
	require.NoError(t, err)
}
