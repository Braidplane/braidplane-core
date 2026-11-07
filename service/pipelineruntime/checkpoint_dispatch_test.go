package pipelineruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestRepairInputsAreDurableBeforeSinkDispatch(t *testing.T) {
	store := &fakeRunStore{}
	req := progressTestRequest(t)
	sinkCalls := 0
	runner := progressTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		sinkCalls++
		checkpoint, ok := store.checkpoints[req.RunSlug]
		require.True(t, ok, "repair inputs must survive a crash inside the provider call")
		require.NotNil(t, checkpoint.Results["fetch"])
		require.Equal(t, slug.Slug("fetch"), checkpoint.StepSlug)
		require.Equal(t, "running", store.timelines[req.RunSlug].Status)
		return nil
	}})
	_, err := runner.Run(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, 1, sinkCalls)
}

func TestCheckpointFailurePreventsSinkDispatch(t *testing.T) {
	store := &fakeRunStore{saveCheckpointFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, string, pipelinerun.ReplayCheckpoint) error {
		return xerror.Unexpected().Detail("checkpoint storage unavailable")
	}}
	runner := progressTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		t.Fatal("checkpoint storage failure must prevent external dispatch")
		return nil
	}})
	_, err := runner.Run(t.Context(), progressTestRequest(t))
	require.ErrorContains(t, err, "checkpoint storage unavailable")
	timeline := store.timelines["checks-test"]
	require.Equal(t, "failed", timeline.Status)
	require.False(t, timeline.Failure.OutcomeUncertain, "a failed pre-dispatch save cannot imply an external effect")
	require.Equal(t, "Sink repair inputs could not be retained before dispatch.", timeline.Failure.Error)
	for _, event := range timeline.Events {
		require.False(t, event.Type == "step_started" && event.Step == "deliver")
	}
}

func TestTransformedRepairInputsAreRetainedBeforeDispatch(t *testing.T) {
	store := &fakeRunStore{}
	req := checkTestRequest(t)
	sinkCalls := 0
	runner := progressTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		sinkCalls++
		checkpoint := store.checkpoints[req.RunSlug]
		require.NotNil(t, checkpoint.Results["fetch"])
		require.NotNil(t, checkpoint.Results["project"])
		require.Equal(t, slug.Slug("project"), checkpoint.StepSlug)
		return nil
	}})
	runner.transformRuntime = &fakeTransformRuntime{executeFn: func(*pipelinespec.TransformSpec, *payload.Payload) (*payload.Output, error) {
		return &payload.Output{OutputKind: model.TransformOutputKindRows, RowCount: 1, ColumnNames: []string{"id"}, Rows: []map[string]any{{"id": 2}}}, nil
	}}
	_, err := runner.Run(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, 1, sinkCalls)
}

func TestOversizedRepairInputsAreExcludedFromEarlyRetention(t *testing.T) {
	store := &fakeRunStore{}
	req := progressTestRequest(t)
	// The bound includes the immutable pipeline as well as intermediate outputs.
	req.Pipeline.Metadata.Name = strings.Repeat("x", 10*1024*1024)
	runner := progressTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		require.Empty(t, store.checkpoints)
		return nil
	}})
	_, err := runner.Run(t.Context(), req)
	require.NoError(t, err)
	require.Contains(t, store.checkpoints, req.RunSlug, "terminal checkpointing keeps its existing size behavior")
}

func TestSecretBearingRunDoesNotRetainPreDispatchRepairInputs(t *testing.T) {
	store := &fakeRunStore{}
	req := progressTestRequest(t)
	req.Pipeline.Steps[0].Query.Parameters = map[string]any{"token": "{{runtime.secrets.TOKEN}}"}
	runner := progressTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		require.Empty(t, store.checkpoints, "redacted outputs cannot reproduce an exact interrupted write")
		return nil
	}})
	runner.environmentSecretResolver = fakeEnvironmentSecretResolver{resolveFn: func(context.Context, slug.Slug, slug.Slug) ([]model.RuntimeSecretValue, error) {
		return []model.RuntimeSecretValue{{Key: "TOKEN", Value: "synthetic-test-secret"}}, nil
	}}
	_, err := runner.Run(t.Context(), req)
	require.NoError(t, err)
	require.Contains(t, store.checkpoints, req.RunSlug, "terminal checkpointing keeps its existing redaction behavior")
}

func TestWorkerLossRecoveryRejectsUnsafeOrIncompleteInputs(t *testing.T) {
	for _, mode := range []string{"unsafe", "unknown", "attempt-target", "s3-no-overwrite", "missing-output", "missing-transform", "missing-checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			store := &fakeRunStore{}
			req := progressTestRequest(t)
			runner := progressTestRunner(t, store, &fakeSinkRuntime{})
			_, err := runner.Run(t.Context(), req)
			require.NoError(t, err)
			timeline := store.timelines[req.RunSlug]
			timeline.Status = "failed"
			timeline.Failure = &pipelinerun.FailureContext{ReasonCode: pipelinerun.FailureReasonCodeWorkerOwnershipLost}
			store.timelines[req.RunSlug] = timeline
			checkpoint := store.checkpoints[req.RunSlug]
			switch mode {
			case "unsafe":
				checkpoint.Pipeline.Steps[1].Sink.IdempotencyClass = pipelinespec.SinkIdempotencyClassUnsafe
			case "unknown":
				checkpoint.Pipeline.Steps[1].Sink.IdempotencyClass = pipelinespec.SinkIdempotencyClassUnknown
			case "attempt-target":
				checkpoint.Pipeline.Steps[1].Sink.Config = map[string]any{"target": "{{runtime.run.slug}}"}
			case "s3-no-overwrite":
				checkpoint.Pipeline.Steps[1].Sink.OperationType = "s3"
				checkpoint.Pipeline.Steps[1].Sink.Config = map[string]any{"key": "out.json", "overwrite": false}
			case "missing-transform":
				checkpoint.Pipeline.Steps[1].Sink.Input.FromStep = "project"
				checkpoint.Pipeline.Steps = append(checkpoint.Pipeline.Steps, pipelinespec.Step{Kind: "transform", Slug: "project", Transform: &pipelinespec.TransformSpec{SQL: "SELECT id FROM rows", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"rows": {FromStep: "fetch"}}, OutputContract: &payload.Contract{Name: "rows", Representation: model.ContractRepresentationRows}}})
			case "missing-output":
				delete(checkpoint.Results, "fetch")
			case "missing-checkpoint":
				delete(store.checkpoints, req.RunSlug)
			}
			if mode != "missing-checkpoint" {
				store.checkpoints[req.RunSlug] = checkpoint
			}
			runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
				t.Fatal("unsupported recovery must not dispatch")
				return nil
			}}
			_, err = runner.Recover(t.Context(), ReplayRequest{OrgSlug: req.OrgSlug, ProjectSlug: req.ProjectSlug, EnvironmentSlug: req.EnvironmentSlug, SourceRunSlug: req.RunSlug, ReplayRunSlug: "repair"})
			require.NotEmpty(t, xerror.PublicDetail(err), "operator refusal must explain how to proceed without re-reading sources")
			switch mode {
			case "unsafe", "unknown", "attempt-target", "s3-no-overwrite":
				require.ErrorIs(t, err, ErrReplayUnsafe)
			case "missing-transform":
				require.ErrorIs(t, err, ErrReplayUnsupportedMode)
			default:
				require.ErrorIs(t, err, ErrReplayRunNotFound)
			}
		})
	}
}
