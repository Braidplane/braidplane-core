package pipelineruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	executionruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

func TestRunner_RunIncrementalSourceUsesOnlyCommittedCheckpointAsLowerBound(t *testing.T) {
	t.Parallel()

	committedUpper := json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)
	observedUpper := json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`)
	store := newIncrementalCheckpointTestRunStore(&pipelinerun.IncrementalCheckpoint{
		PipelineSlug:        must.Slug("users-sync"),
		SourceStepSlug:      must.Slug("fetch-users"),
		CheckpointNamespace: "normal",
		CheckpointKind:      "watermark",
		ObservedUpperBound:  committedUpper,
		DisplayMetadata:     json.RawMessage(`{"summary":"up to 10:00","record_count":10}`),
		CommitStatus:        pipelinerun.IncrementalCheckpointStatusCommitted,
		CommittingRunSlug:   "run-previous",
		CommittedAt:         ptrTime(time.Date(2026, time.May, 24, 10, 1, 0, 0, time.UTC)),
	})
	executor := &incrementalCheckpointQueryExecutor{
		executeWithCheckpointFn: func(
			_ context.Context,
			_, _ slug.Slug,
			_ slug.Slug,
			_ map[string]any,
			checkpoint *pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			require.NotNil(t, checkpoint)
			require.JSONEq(t, string(committedUpper), string(checkpoint.LowerBound))
			return successfulSQLResult(3), &pipelinerun.IncrementalCheckpointObservation{
				SourceStepSlug:      must.Slug("fetch-users"),
				CheckpointNamespace: "normal",
				CheckpointKind:      "watermark",
				ObservedLowerBound:  checkpoint.LowerBound,
				ObservedUpperBound:  observedUpper,
				DisplayMetadata:     json.RawMessage(`{"summary":"up to 11:00","record_count":3}`),
				ObservedAt:          time.Date(2026, time.May, 24, 11, 0, 0, 0, time.UTC),
			}, nil
		},
	}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, runnerNoopSinkRuntime{})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-commits-checkpoint",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Len(t, executor.checkpointInputs, 1)
	require.JSONEq(t, string(committedUpper), string(executor.checkpointInputs[0].LowerBound))

	require.Len(t, store.persistedRuns, 1)
	require.Len(t, store.persistedRuns[0].IncrementalCheckpointTransitions, 1)
	transition := store.persistedRuns[0].IncrementalCheckpointTransitions[0]
	require.Equal(t, pipelinerun.IncrementalCheckpointTransitionCommitted, transition.Status)
	require.Equal(t, must.Slug("fetch-users"), transition.SourceStepSlug)
	require.Equal(t, "run-commits-checkpoint", transition.CommittingRunSlug)
	require.JSONEq(t, string(observedUpper), string(transition.ObservedUpperBound))
}

func TestRunner_RunIncrementalSourceWithoutCommittedCheckpointUsesNilLowerBound(t *testing.T) {
	t.Parallel()

	observedUpper := json.RawMessage(`{"value":"2026-05-24T12:00:00Z"}`)
	store := newIncrementalCheckpointTestRunStore(nil)
	executor := &incrementalCheckpointQueryExecutor{
		executeWithCheckpointFn: func(
			_ context.Context,
			_, _ slug.Slug,
			_ slug.Slug,
			_ map[string]any,
			checkpoint *pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			require.NotNil(t, checkpoint)
			require.Nil(t, checkpoint.LowerBound)
			return successfulSQLResult(5), &pipelinerun.IncrementalCheckpointObservation{
				SourceStepSlug:      must.Slug("fetch-users"),
				CheckpointNamespace: "normal",
				CheckpointKind:      "watermark",
				ObservedLowerBound:  nil,
				ObservedUpperBound:  observedUpper,
				DisplayMetadata:     json.RawMessage(`{"summary":"up to 12:00","record_count":5}`),
				ObservedAt:          time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC),
			}, nil
		},
	}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, runnerNoopSinkRuntime{})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-first-incremental",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Len(t, executor.checkpointInputs, 1)
	require.Nil(t, executor.checkpointInputs[0].LowerBound)
	require.Equal(t, pipelinerun.IncrementalCheckpointTransitionCommitted, store.persistedRuns[0].IncrementalCheckpointTransitions[0].Status)
}

func TestRunner_RunRedactsEnvironmentSecretValuesFromIncrementalSinkDiagnostics(t *testing.T) {
	t.Parallel()

	const secretValue = "ghp_incremental_sink_secret\nquoted\"slash\\"

	store := newIncrementalCheckpointTestRunStore(nil)
	observedUpperBound, err := json.Marshal(map[string]string{"value": secretValue})
	require.NoError(t, err)
	encodedSecret, err := json.Marshal(secretValue)
	require.NoError(t, err)
	escapedSecret := strings.Trim(string(encodedSecret), `"`)
	executor := &incrementalCheckpointQueryExecutor{
		executeWithCheckpointFn: func(
			_ context.Context,
			_, _ slug.Slug,
			_ slug.Slug,
			_ map[string]any,
			checkpoint *pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			require.NotNil(t, checkpoint)
			return successfulSQLResult(1), &pipelinerun.IncrementalCheckpointObservation{
				SourceStepSlug:      must.Slug("fetch-users"),
				CheckpointNamespace: "normal",
				CheckpointKind:      "watermark",
				ObservedUpperBound:  observedUpperBound,
				DisplayMetadata:     observedUpperBound,
			}, nil
		},
	}
	sinkRuntime := &fakeSinkRuntime{executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
		require.Equal(t, secretValue, input.SinkConfig["token"])
		return fmt.Errorf("warehouse rejected token %s", secretValue)
	}}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, sinkRuntime)
	runner.environmentSecretResolver = fakeEnvironmentSecretResolver{
		resolveFn: func(context.Context, slug.Slug, slug.Slug) ([]model.RuntimeSecretValue, error) {
			return []model.RuntimeSecretValue{{Key: "API_TOKEN", Value: secretValue}}, nil
		},
	}
	pipeline := incrementalCheckpointPipeline()
	pipeline.Steps[1].Sink.Config = map[string]any{
		"token": "{{runtime.secrets.API_TOKEN}}",
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-redacts-incremental-sink-diagnostics",
		PipelineVersionID: 42,
		Pipeline:          pipeline,
	})

	require.Error(t, err)
	require.NotContains(t, err.Error(), secretValue)
	require.NotNil(t, runResult)
	require.Len(t, store.persistedRuns, 1)

	runResultJSON, err := json.Marshal(runResult)
	require.NoError(t, err)
	require.NotContains(t, string(runResultJSON), secretValue)
	require.NotContains(t, string(runResultJSON), escapedSecret)
	require.Contains(t, string(runResultJSON), "[REDACTED]")

	persistedResultJSON, err := json.Marshal(store.persistedRuns[0])
	require.NoError(t, err)
	require.NotContains(t, string(persistedResultJSON), secretValue)
	require.NotContains(t, string(persistedResultJSON), escapedSecret)
	require.Contains(t, string(persistedResultJSON), "[REDACTED]")
	require.NotContains(t, runResult.Metadata.Incremental.ReplayBlockedReason, secretValue)
	require.NotContains(t, runResult.IncrementalDiagnostics.ReplaySafety.BlockedReason, secretValue)
}

func TestIncrementalCheckpointLookupForStep_InlineHashFailure(t *testing.T) {
	t.Parallel()

	pipeline := incrementalCheckpointPipeline()

	_, isIncrementalSource, err := incrementalCheckpointLookupForStep(
		pipeline.Metadata.Slug,
		42,
		pipelinerun.DefaultIncrementalCheckpointNamespace,
		pipeline.Steps[0],
		"",
		map[string]any{"bad": func() {}},
		true,
	)

	require.Error(t, err)
	require.True(t, isIncrementalSource)
}

func TestRunner_RunIncrementalSourceCheckpointLoadFailureSetsReasonCode(t *testing.T) {
	t.Parallel()

	store := newIncrementalCheckpointTestRunStore(nil)
	store.currentErr = xerror.Unexpected().Detail("checkpoint store unavailable")
	runner := mustNewIncrementalCheckpointRunner(
		t,
		&incrementalCheckpointQueryExecutor{},
		store,
		runnerNoopSinkRuntime{},
	)

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-checkpoint-load-failure",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
	})

	require.Error(t, err)
	require.NotNil(t, runResult)
	require.NotNil(t, runResult.Timeline.Failure)
	require.Equal(t, pipelinerun.FailureReasonCodeSourceQueryFailed, runResult.Timeline.Failure.ReasonCode)
	require.Equal(t, "failed", runResult.Metadata.Status)
}

func TestRunner_RunScheduledIncrementalUsesNormalCheckpointNamespace(t *testing.T) {
	t.Parallel()

	committedUpper := json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)
	store := newIncrementalCheckpointTestRunStore(&pipelinerun.IncrementalCheckpoint{
		ObservedUpperBound: committedUpper,
		CommitStatus:       pipelinerun.IncrementalCheckpointStatusCommitted,
	})
	executor := &incrementalCheckpointQueryExecutor{
		executeWithCheckpointFn: func(
			_ context.Context,
			_, _ slug.Slug,
			_ slug.Slug,
			_ map[string]any,
			checkpoint *pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			require.JSONEq(t, string(committedUpper), string(checkpoint.LowerBound))
			return successfulSQLResult(2), &pipelinerun.IncrementalCheckpointObservation{
				ObservedUpperBound: json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`),
			}, nil
		},
	}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, runnerNoopSinkRuntime{})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-scheduled-incremental",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
		Incremental: pipelinerun.IncrementalRunOptions{
			TriggerSource: "scheduled",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Equal(t, pipelinerun.DefaultIncrementalCheckpointNamespace, store.lastLookup.CheckpointNamespace)
	require.Equal(t, pipelinerun.DefaultIncrementalCheckpointNamespace, store.persistedRuns[0].IncrementalCheckpointTransitions[0].CheckpointNamespace)
}

func TestRunner_RunInlineIncrementalSourceUsesCheckpointExecutor(t *testing.T) {
	t.Parallel()

	observedUpper := json.RawMessage(`{"value":"inline-10"}`)
	store := newIncrementalCheckpointTestRunStore(nil)
	executor := &incrementalCheckpointQueryExecutor{
		executeInlineWithCheckpointFn: func(
			_ context.Context,
			_, _ slug.Slug,
			operationInline map[string]any,
			_ map[string]any,
			checkpoint *pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			require.Equal(t, map[string]any{"sql": "SELECT id FROM users WHERE updated_at > $checkpoint"}, operationInline)
			require.Nil(t, checkpoint.LowerBound)
			return successfulSQLResult(3), &pipelinerun.IncrementalCheckpointObservation{
				ObservedUpperBound: observedUpper,
			}, nil
		},
	}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, runnerNoopSinkRuntime{})
	pipeline := incrementalCheckpointPipeline()
	pipeline.Steps[0].Query.OperationRef = ""
	pipeline.Steps[0].Query.OperationInline = map[string]any{"sql": "SELECT id FROM users WHERE updated_at > $checkpoint"}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-inline-incremental",
		PipelineVersionID: 42,
		Pipeline:          pipeline,
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Len(t, executor.checkpointInputs, 1)
	require.Len(t, store.persistedRuns, 1)
	require.Len(t, store.persistedRuns[0].IncrementalCheckpointTransitions, 1)
	transition := store.persistedRuns[0].IncrementalCheckpointTransitions[0]
	require.Equal(t, pipelinerun.IncrementalCheckpointTransitionCommitted, transition.Status)
	require.JSONEq(t, string(observedUpper), string(transition.ObservedUpperBound))
	require.NotEmpty(t, transition.OperationInlineHash)
}

func TestRunner_RunIncrementalSourceFailsWhenCheckpointExecutorUnavailable(t *testing.T) {
	t.Parallel()

	store := newIncrementalCheckpointTestRunStore(&pipelinerun.IncrementalCheckpoint{
		PipelineSlug:        must.Slug("users-sync"),
		SourceStepSlug:      must.Slug("fetch-users"),
		CheckpointNamespace: "normal",
		CheckpointKind:      "watermark",
		ObservedUpperBound:  json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`),
		CommitStatus:        pipelinerun.IncrementalCheckpointStatusCommitted,
		CommittingRunSlug:   "run-previous",
	})
	executor := &legacyIncrementalCheckpointQueryExecutor{}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, runnerNoopSinkRuntime{})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-legacy-incremental",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "incremental query executor is required")
	require.NotNil(t, runResult)
	require.Equal(t, "failed", runResult.Metadata.Status)
	require.Len(t, store.persistedRuns, 1)
	require.Empty(t, store.persistedRuns[0].IncrementalCheckpointTransitions)
	require.NotNil(t, store.persistedRuns[0].Timeline.Failure)
	require.Contains(t, store.persistedRuns[0].Timeline.Failure.Error, "incremental query executor is required")
}

func TestRunner_RunInlineIncrementalSourceFailsWhenCheckpointExecutorUnavailable(t *testing.T) {
	t.Parallel()

	store := newIncrementalCheckpointTestRunStore(nil)
	operationInline := map[string]any{"sql": "SELECT id FROM users"}
	executor := &legacyIncrementalCheckpointQueryExecutor{}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, runnerNoopSinkRuntime{})
	pipeline := incrementalCheckpointPipeline()
	pipeline.Steps[0].Query.OperationRef = ""
	pipeline.Steps[0].Query.OperationInline = operationInline

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-legacy-inline-incremental",
		PipelineVersionID: 42,
		Pipeline:          pipeline,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "incremental inline query executor is required")
	require.NotNil(t, runResult)
	require.Equal(t, "failed", runResult.Metadata.Status)
	require.Len(t, store.persistedRuns, 1)
	require.Empty(t, store.persistedRuns[0].IncrementalCheckpointTransitions)
	require.NotNil(t, store.persistedRuns[0].Timeline.Failure)
	require.Contains(t, store.persistedRuns[0].Timeline.Failure.Error, "incremental inline query executor is required")
}

func TestRunner_RunIncrementalSourceBlocksObservationWhenDependentSinkFails(t *testing.T) {
	t.Parallel()

	committedUpper := json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)
	observedUpper := json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`)
	store := newIncrementalCheckpointTestRunStore(&pipelinerun.IncrementalCheckpoint{
		PipelineSlug:        must.Slug("users-sync"),
		SourceStepSlug:      must.Slug("fetch-users"),
		CheckpointNamespace: "normal",
		CheckpointKind:      "watermark",
		ObservedUpperBound:  committedUpper,
		CommitStatus:        pipelinerun.IncrementalCheckpointStatusCommitted,
		CommittingRunSlug:   "run-previous",
	})
	executor := &incrementalCheckpointQueryExecutor{
		executeWithCheckpointFn: func(
			_ context.Context,
			_, _ slug.Slug,
			_ slug.Slug,
			_ map[string]any,
			checkpoint *pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			return successfulSQLResult(3), &pipelinerun.IncrementalCheckpointObservation{
				SourceStepSlug:      must.Slug("fetch-users"),
				CheckpointNamespace: "normal",
				CheckpointKind:      "watermark",
				ObservedLowerBound:  checkpoint.LowerBound,
				ObservedUpperBound:  observedUpper,
				DisplayMetadata:     json.RawMessage(`{"summary":"blocked up to 11:00","record_count":3}`),
				ObservedAt:          time.Date(2026, time.May, 24, 11, 0, 0, 0, time.UTC),
			}, nil
		},
	}
	sinkErr := xerror.Unexpected().Detail("sink write failed")
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, &fakeSinkRuntime{
		executeFn: func(context.Context, *SinkRuntimeInput) error {
			return sinkErr
		},
	})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-blocks-checkpoint",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
	})
	require.Error(t, err)
	require.NotNil(t, runResult)
	require.Equal(t, "failed", runResult.Metadata.Status)
	require.Len(t, store.persistedRuns, 1)
	require.Len(t, store.persistedRuns[0].IncrementalCheckpointTransitions, 1)
	transition := store.persistedRuns[0].IncrementalCheckpointTransitions[0]
	require.Equal(t, pipelinerun.IncrementalCheckpointTransitionBlocked, transition.Status)
	require.Equal(t, must.Slug("fetch-users"), transition.SourceStepSlug)
	require.Equal(t, must.Slug("persist-users"), transition.BlockedByStep)
	require.Contains(t, transition.BlockedReason, "sink write failed")
	require.Empty(t, transition.CommittingRunSlug)
	require.JSONEq(t, string(observedUpper), string(transition.ObservedUpperBound))
}

func TestRunner_RunMultipleIncrementalSourcesCommitAndBlockIndependently(t *testing.T) {
	t.Parallel()

	store := newIncrementalCheckpointTestRunStore(nil)
	executor := &incrementalCheckpointQueryExecutor{
		executeWithCheckpointFn: func(
			_ context.Context,
			_, _ slug.Slug,
			querySlug slug.Slug,
			_ map[string]any,
			checkpoint *pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			require.Nil(t, checkpoint.LowerBound)
			sourceStep := must.Slug("fetch-users")
			upper := json.RawMessage(`{"value":"users-10"}`)
			if querySlug == must.Slug("list-orders") {
				sourceStep = must.Slug("fetch-orders")
				upper = json.RawMessage(`{"value":"orders-10"}`)
			}
			return successfulSQLResult(2), &pipelinerun.IncrementalCheckpointObservation{
				SourceStepSlug:      sourceStep,
				CheckpointNamespace: "normal",
				CheckpointKind:      checkpoint.Lookup.CheckpointKind,
				ObservedUpperBound:  upper,
				DisplayMetadata:     json.RawMessage(`{"record_count":2}`),
				ObservedAt:          time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC),
			}, nil
		},
	}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, &fakeSinkRuntime{
		executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
			if input.SourceStep == must.Slug("fetch-orders") {
				return xerror.Unexpected().Detail("orders sink failed")
			}
			return nil
		},
	})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-partial-commit",
		PipelineVersionID: 42,
		Pipeline:          multiSourceIncrementalCheckpointPipeline(),
	})
	require.Error(t, err)
	require.NotNil(t, runResult)
	require.Equal(t, "failed", runResult.Metadata.Status)
	require.Len(t, store.persistedRuns, 1)
	require.Len(t, store.persistedRuns[0].IncrementalCheckpointTransitions, 2)

	transitionsByStep := map[slug.Slug]pipelinerun.IncrementalCheckpointTransition{}
	for _, transition := range store.persistedRuns[0].IncrementalCheckpointTransitions {
		transitionsByStep[transition.SourceStepSlug] = transition
	}
	require.Equal(t, pipelinerun.IncrementalCheckpointTransitionCommitted, transitionsByStep[must.Slug("fetch-users")].Status)
	require.Equal(t, pipelinerun.IncrementalCheckpointTransitionBlocked, transitionsByStep[must.Slug("fetch-orders")].Status)
	require.Equal(t, must.Slug("persist-orders"), transitionsByStep[must.Slug("fetch-orders")].BlockedByStep)
}

func TestRunner_RunBackfillChunkUsesExplicitWindowWithoutNormalCheckpointPromotion(t *testing.T) {
	t.Parallel()

	normalUpper := json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)
	backfillLower := json.RawMessage(`{"value":"2026-05-01T00:00:00Z"}`)
	backfillUpper := json.RawMessage(`{"value":"2026-05-02T00:00:00Z"}`)
	store := newIncrementalCheckpointTestRunStore(&pipelinerun.IncrementalCheckpoint{
		ObservedUpperBound: normalUpper,
		CommitStatus:       pipelinerun.IncrementalCheckpointStatusCommitted,
	})
	executor := &incrementalCheckpointQueryExecutor{
		executeWithCheckpointFn: func(
			_ context.Context,
			_, _ slug.Slug,
			_ slug.Slug,
			_ map[string]any,
			checkpoint *pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			require.NotNil(t, checkpoint)
			require.JSONEq(t, string(backfillLower), string(checkpoint.LowerBound))
			require.JSONEq(t, string(backfillUpper), string(checkpoint.UpperBound))
			require.NotNil(t, checkpoint.Window)
			return successfulSQLResult(3), &pipelinerun.IncrementalCheckpointObservation{}, nil
		},
	}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, runnerNoopSinkRuntime{})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-backfill-chunk-001",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
		Incremental: pipelinerun.IncrementalRunOptions{
			CheckpointNamespace:  "backfill:series-001",
			SkipCheckpointCommit: true,
			BackfillSeriesSlug:   must.Slug("series-001"),
			BackfillChunkIndex:   1,
			BackfillChunkCount:   10,
			SourceWindows: map[slug.Slug]pipelinerun.IncrementalSourceWindow{
				must.Slug("fetch-users"): {
					LowerBound: backfillLower,
					UpperBound: backfillUpper,
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Len(t, store.persistedRuns, 1)
	require.Len(t, store.persistedRuns[0].IncrementalCheckpointTransitions, 1)
	transition := store.persistedRuns[0].IncrementalCheckpointTransitions[0]
	require.Equal(t, pipelinerun.IncrementalCheckpointTransitionBlocked, transition.Status)
	require.Equal(t, "backfill:series-001", transition.CheckpointNamespace)
	require.Contains(t, transition.BlockedReason, "checkpoint promotion skipped")
	require.Empty(t, transition.CommittingRunSlug)
	require.JSONEq(t, string(backfillLower), string(transition.ObservedLowerBound))
	require.JSONEq(t, string(backfillUpper), string(transition.ObservedUpperBound))
}

func TestRunner_RunBackfillPromotionCommitsCompletedEndpointToNormalCheckpoint(t *testing.T) {
	t.Parallel()

	backfillLower := json.RawMessage(`{"value":"2026-05-01T00:00:00Z"}`)
	backfillUpper := json.RawMessage(`{"value":"2026-05-02T00:00:00Z"}`)
	store := newIncrementalCheckpointTestRunStore(nil)
	executor := &incrementalCheckpointQueryExecutor{
		executeWithCheckpointFn: func(
			_ context.Context,
			_, _ slug.Slug,
			_ slug.Slug,
			_ map[string]any,
			checkpoint *pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			return successfulSQLResult(3), &pipelinerun.IncrementalCheckpointObservation{
				ObservedLowerBound: checkpoint.LowerBound,
				ObservedUpperBound: checkpoint.UpperBound,
			}, nil
		},
	}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, runnerNoopSinkRuntime{})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-backfill-promote",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
		Incremental: pipelinerun.IncrementalRunOptions{
			CheckpointNamespace:  "backfill:series-001",
			SkipCheckpointCommit: true,
			PromoteCheckpoint:    true,
			SourceWindows: map[slug.Slug]pipelinerun.IncrementalSourceWindow{
				must.Slug("fetch-users"): {
					LowerBound: backfillLower,
					UpperBound: backfillUpper,
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	transition := store.persistedRuns[0].IncrementalCheckpointTransitions[0]
	require.Equal(t, pipelinerun.IncrementalCheckpointTransitionCommitted, transition.Status)
	require.Equal(t, pipelinerun.DefaultIncrementalCheckpointNamespace, transition.CheckpointNamespace)
	require.Equal(t, "run-backfill-promote", transition.CommittingRunSlug)
}

func TestRunner_RunPassesStableSinkIdempotencyMetadata(t *testing.T) {
	t.Parallel()

	observedUpper := json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`)
	store := newIncrementalCheckpointTestRunStore(nil)
	executor := &incrementalCheckpointQueryExecutor{
		executeWithCheckpointFn: func(
			_ context.Context,
			_, _ slug.Slug,
			_ slug.Slug,
			_ map[string]any,
			_ *pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			return successfulSQLResult(3), &pipelinerun.IncrementalCheckpointObservation{
				ObservedUpperBound: observedUpper,
			}, nil
		},
	}
	var firstKey string
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, &fakeSinkRuntime{
		executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
			require.Equal(t, pipelinespec.SinkIdempotencyClassDeduplicatedByLedger, input.IdempotencyClass)
			require.NotEmpty(t, input.IdempotencyKey)
			require.Contains(t, input.IdempotencyKey, "sink:v1:")
			firstKey = input.IdempotencyKey
			return nil
		},
	})

	_, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-idempotency",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
	})
	require.NoError(t, err)

	runner = mustNewIncrementalCheckpointRunner(t, executor, store, &fakeSinkRuntime{
		executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
			require.Equal(t, firstKey, input.IdempotencyKey)
			return nil
		},
	})
	_, err = runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-idempotency",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
	})
	require.ErrorIs(t, err, errAttemptInterrupted, "a completed attempt cannot dispatch effects again")
}

func TestRunner_ReplayBlocksAutomaticReplayForUnsafeSiblingSink(t *testing.T) {
	t.Parallel()

	pipeline := fanOutIncrementalCheckpointPipeline()
	store := newIncrementalCheckpointTestRunStore(nil)
	store.timelines["run-partial-sink-failure"] = pipelinerun.RunTimeline{
		ExecutionContext: testExecutionContext(),
		RunSlug:          "run-partial-sink-failure",
		PipelineSlug:     pipeline.Metadata.Slug,
		Status:           "failed",
		Events: []pipelinerun.TimelineEvent{
			{At: time.Date(2026, time.May, 24, 11, 0, 0, 0, time.UTC), Type: "step_succeeded", Step: must.Slug("fetch-users"), Status: "succeeded"},
			{At: time.Date(2026, time.May, 24, 11, 1, 0, 0, time.UTC), Type: "step_succeeded", Step: must.Slug("append-audit"), Status: "succeeded"},
			{At: time.Date(2026, time.May, 24, 11, 2, 0, 0, time.UTC), Type: "step_failed", Step: must.Slug("persist-users"), Status: "failed"},
		},
		Failure: &pipelinerun.FailureContext{
			Step:  must.Slug("persist-users"),
			Error: "warehouse write failed",
			At:    time.Date(2026, time.May, 24, 11, 2, 0, 0, time.UTC),
			Kind:  failureKindDataSource,
			Side:  failureSideSink,
		},
	}
	store.checkpoints["run-partial-sink-failure"] = pipelinerun.ReplayCheckpoint{
		RunSlug:  "run-partial-sink-failure",
		OrgSlug:  must.Slug("acme"),
		StepSlug: must.Slug("fetch-users"),
		Pipeline: pipeline,
		Results: map[slug.Slug]*pipelinerun.StepResult{
			must.Slug("fetch-users"): pipelinerun.NewStepResult(successfulSQLResult(3)),
		},
		IncrementalSourceWindows: map[slug.Slug]pipelinerun.IncrementalSourceWindow{
			must.Slug("fetch-users"): {
				LowerBound: json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`),
				UpperBound: json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`),
			},
		},
	}
	runner := mustNewIncrementalCheckpointRunner(t, &incrementalCheckpointQueryExecutor{}, store, runnerNoopSinkRuntime{})

	runResult, err := runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		ProjectSlug:   must.Slug("default"),
		SourceRunSlug: "run-partial-sink-failure",
		ReplayRunSlug: "run-partial-sink-failure-replay",
		InputMode:     ReplayInputModePinned,
	})
	require.ErrorIs(t, err, ErrReplayUnsafe)
	require.Contains(t, err.Error(), "append-audit")
	require.Nil(t, runResult)
}

type incrementalCheckpointQueryExecutor struct {
	checkpointInputs        []*pipelinerun.IncrementalCheckpointInput
	executeWithCheckpointFn func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		map[string]any,
		*pipelinerun.IncrementalCheckpointInput,
	) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
	executeInlineWithCheckpointFn func(
		context.Context,
		slug.Slug,
		slug.Slug,
		map[string]any,
		map[string]any,
		*pipelinerun.IncrementalCheckpointInput,
	) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
}

func (e *incrementalCheckpointQueryExecutor) Execute(
	_ context.Context,
	_ slug.Slug,
	_ slug.Slug,
	querySlug slug.Slug,
	_ map[string]any,
) (*result.ExecutionResult, error) {
	if querySlug == "select-scope" {
		return successfulSQLResult(1), nil
	}
	return nil, xerror.Unexpected().Detail("plain Execute must not be used for incremental source steps")
}

func (e *incrementalCheckpointQueryExecutor) ExecuteIncremental(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	e.checkpointInputs = append(e.checkpointInputs, checkpoint)
	if e.executeWithCheckpointFn != nil {
		return e.executeWithCheckpointFn(ctx, orgSlug, dsSlug, querySlug, paramValues, checkpoint)
	}
	return successfulSQLResult(1), nil, nil
}

func (e *incrementalCheckpointQueryExecutor) ExecuteInlineIncremental(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	e.checkpointInputs = append(e.checkpointInputs, checkpoint)
	if e.executeInlineWithCheckpointFn != nil {
		return e.executeInlineWithCheckpointFn(ctx, orgSlug, dsSlug, operationInline, paramValues, checkpoint)
	}
	return successfulSQLResult(1), nil, nil
}

func (e *incrementalCheckpointQueryExecutor) GetExpectedInputContract(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*payload.Contract, error) {
	return &payload.Contract{}, nil
}

func (e *incrementalCheckpointQueryExecutor) ExecuteInline(
	context.Context,
	slug.Slug,
	slug.Slug,
	map[string]any,
	map[string]any,
) (*result.ExecutionResult, error) {
	return nil, xerror.Unexpected().Detail("inline Execute must not be used for incremental source steps")
}

type legacyIncrementalCheckpointQueryExecutor struct {
	executeFn func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error)
	executeInlineFn func(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		operationInline map[string]any,
		paramValues map[string]any,
	) (*result.ExecutionResult, error)
}

func (e *legacyIncrementalCheckpointQueryExecutor) Execute(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	if e.executeFn != nil {
		return e.executeFn(ctx, orgSlug, dsSlug, querySlug, paramValues)
	}
	return nil, xerror.Unexpected().Detail("plain Execute was not expected")
}

func (e *legacyIncrementalCheckpointQueryExecutor) GetExpectedInputContract(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*payload.Contract, error) {
	return &payload.Contract{}, nil
}

func (e *legacyIncrementalCheckpointQueryExecutor) ExecuteInline(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	if e.executeInlineFn != nil {
		return e.executeInlineFn(ctx, orgSlug, dsSlug, operationInline, paramValues)
	}
	return nil, xerror.Unexpected().Detail("inline Execute was not expected")
}

type incrementalCheckpointTestRunStore struct {
	current               *pipelinerun.IncrementalCheckpoint
	currentErr            error
	lastLookup            pipelinerun.IncrementalCheckpointLookup
	lastLookupEnvironment slug.Slug
	persistedRuns         []*pipelinerun.RunResult
	persistedEnvironments []slug.Slug
	timelines             map[string]pipelinerun.RunTimeline
	checkpoints           map[string]pipelinerun.ReplayCheckpoint
}

func newIncrementalCheckpointTestRunStore(
	current *pipelinerun.IncrementalCheckpoint,
) *incrementalCheckpointTestRunStore {
	return &incrementalCheckpointTestRunStore{
		current:     current,
		timelines:   make(map[string]pipelinerun.RunTimeline),
		checkpoints: make(map[string]pipelinerun.ReplayCheckpoint),
	}
}

func (s *incrementalCheckpointTestRunStore) PersistRun(
	_ context.Context,
	_, _, environmentSlug slug.Slug,
	_ *pipelinespec.Pipeline,
	runResult *pipelinerun.RunResult,
) error {
	// Keep terminal outcomes separate from intermediate context/check records.
	if runResult.Metadata.Status != "running" {
		s.persistedRuns = append(s.persistedRuns, runResult)
		s.persistedEnvironments = append(s.persistedEnvironments, environmentSlug)
	}
	s.timelines[runResult.Metadata.RunSlug] = runResult.Timeline
	return nil
}

func (s *incrementalCheckpointTestRunStore) Get(
	_ context.Context,
	_, _, _ slug.Slug,
	runSlug string,
) (*pipelinerun.RunTimeline, error) {
	timeline, ok := s.timelines[runSlug]
	if !ok {
		return nil, pipelinerun.ErrTimelineNotFound
	}
	return &timeline, nil
}

func (s *incrementalCheckpointTestRunStore) SaveCheckpoint(
	_ context.Context,
	_, _, _ slug.Slug,
	runSlug string,
	checkpoint pipelinerun.ReplayCheckpoint,
) error {
	s.checkpoints[runSlug] = checkpoint
	return nil
}

func (s *incrementalCheckpointTestRunStore) GetCheckpoint(
	_ context.Context,
	_, _, _ slug.Slug,
	runSlug string,
) (*pipelinerun.ReplayCheckpoint, error) {
	checkpoint, ok := s.checkpoints[runSlug]
	if !ok {
		return nil, pipelinerun.ErrReplayCheckpointNotFound
	}
	return &checkpoint, nil
}

func (s *incrementalCheckpointTestRunStore) GetCurrentIncrementalCheckpoint(
	_ context.Context,
	_, _, environmentSlug slug.Slug,
	lookup pipelinerun.IncrementalCheckpointLookup,
) (*pipelinerun.IncrementalCheckpoint, error) {
	s.lastLookup = lookup
	s.lastLookupEnvironment = environmentSlug
	if s.currentErr != nil {
		return nil, s.currentErr
	}
	return s.current, nil
}

func mustNewIncrementalCheckpointRunner(
	t *testing.T,
	executor queryExecutor,
	store *incrementalCheckpointTestRunStore,
	sinkRuntime SinkRuntime,
) *Runner {
	t.Helper()
	runner, err := NewRunner(
		executor,
		nil,
		store,
		incrementalCheckpointDataSourceService{},
		sinkRuntime,
	)
	require.NoError(t, err)
	return runner
}

type incrementalCheckpointDataSourceService struct {
	datasourcessvc.Service
}

func (incrementalCheckpointDataSourceService) FindBySlug(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.DataSource, error) {
	return &datasourcessvc.DataSource{
		Type:   model.DataSourceTypePostgres,
		Schema: incrementalCheckpointSQLSchema(),
	}, nil
}

func incrementalCheckpointPipeline() *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: must.Slug("users-sync"), Name: "Users Sync"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("acme-postgres"),
					OperationType:  "sql",
					OperationRef:   must.Slug("list-users"),
					Incremental: &pipelinespec.IncrementalSourceContract{
						CheckpointKind: incrementalCheckpointTestKind(model.IncrementalCheckpointKindWatermark),
						Field:          "updated_at",
						LookbackWindow: "5m",
						Ordering:       []string{"updated_at", "id"},
						DedupeKey:      "id",
					},
					OutputContract: incrementalCheckpointRowsContract(),
				},
			},
			{
				Kind: "sink",
				Slug: must.Slug("persist-users"),
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
					DataSourceSlug:   must.Slug("warehouse"),
					OperationType:    "sql",
					OperationRef:     must.Slug("upsert-users"),
					InputContract:    incrementalCheckpointRowsContract(),
				},
			},
		},
	}
}

func multiSourceIncrementalCheckpointPipeline() *pipelinespec.Pipeline {
	pipeline := incrementalCheckpointPipeline()
	// Both incremental branches consume a shared scope without depending on each other's deliveries.
	pipeline.Steps[0].Query.Parameters = map[string]any{"scope": "$steps.scope.rows"}
	pipeline.Steps = append(pipeline.Steps,
		pipelinespec.Step{
			Kind: "query",
			Slug: must.Slug("fetch-orders"),
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: must.Slug("acme-postgres"),
				OperationType:  "sql",
				OperationRef:   must.Slug("list-orders"),
				Parameters:     map[string]any{"scope": "$steps.scope.rows"},
				Incremental: &pipelinespec.IncrementalSourceContract{
					CheckpointKind: incrementalCheckpointTestKind(model.IncrementalCheckpointKindWatermark),
					Field:          "cursor",
					Ordering:       []string{"cursor"},
					DedupeKey:      "id",
				},
				OutputContract: incrementalCheckpointOrdersRowsContract(),
			},
		},
		pipelinespec.Step{
			Kind: "sink",
			Slug: must.Slug("persist-orders"),
			Sink: &pipelinespec.SinkSpec{
				IdempotencyClass: pipelinespec.SinkIdempotencyClassUnsafe,
				Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-orders")},
				DataSourceSlug:   must.Slug("warehouse"),
				OperationType:    "sql",
				OperationRef:     must.Slug("upsert-orders"),
				InputContract:    incrementalCheckpointOrdersRowsContract(),
			},
		},
	)

	pipeline.Steps = append([]pipelinespec.Step{{
		Kind: "query", Slug: "scope", Query: &pipelinespec.QueryRef{
			DataSourceSlug: "acme-postgres", OperationType: "sql", OperationRef: "select-scope",
			OutputContract: incrementalCheckpointRowsContract(),
		},
	}}, pipeline.Steps...)
	return pipeline
}

func fanOutIncrementalCheckpointPipeline() *pipelinespec.Pipeline {
	pipeline := incrementalCheckpointPipeline()
	pipeline.Steps[1].Sink.IdempotencyClass = pipelinespec.SinkIdempotencyClassIdempotentWithKey
	pipeline.Steps = append(pipeline.Steps,
		pipelinespec.Step{
			Kind: "sink",
			Slug: must.Slug("append-audit"),
			Sink: &pipelinespec.SinkSpec{
				IdempotencyClass: pipelinespec.SinkIdempotencyClassUnsafe,
				Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
				DataSourceSlug:   must.Slug("warehouse"),
				OperationType:    "sql",
				OperationRef:     must.Slug("append-users-audit"),
				InputContract:    incrementalCheckpointRowsContract(),
			},
		},
	)
	return pipeline
}

func incrementalCheckpointTestKind(value model.IncrementalCheckpointKind) *model.IncrementalCheckpointKind {
	return &value
}

func incrementalCheckpointSQLSchema() datasourcessvc.SchemaResult {
	return datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
			{
				Name: "users",
				Columns: []datasourcessvc.SchemaColumn{
					{Name: "id", Type: "integer"},
					{Name: "updated_at", Type: "timestamp"},
				},
			},
			{
				Name: "orders",
				Columns: []datasourcessvc.SchemaColumn{
					{Name: "id", Type: "integer"},
					{Name: "cursor", Type: "text"},
				},
			},
		}},
	}
}

func successfulSQLResult(rowCount int) *result.ExecutionResult {
	rows := make([]map[string]any, rowCount)
	for i := range rows {
		rows[i] = map[string]any{
			"id":         i + 1,
			"updated_at": fmt.Sprintf("2026-05-24T10:%02d:00Z", i),
			"cursor":     fmt.Sprintf("cursor-%d", i+1),
		}
	}
	return result.NewSQLResult("postgres", &result.SQLResult{
		Rows:        rows,
		ColumnNames: []string{"id", "updated_at", "cursor"},
		RowCount:    rowCount,
	}, 25, nil)
}

func incrementalCheckpointRowsContract() *payload.Contract {
	return &payload.Contract{
		Name:           "rows",
		Representation: model.ContractRepresentationRows,
		Fields: []payload.ContractField{
			{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
			{Name: "updated_at", Type: model.ContractFieldTypeValueDatetime, Required: true},
		},
	}
}

func incrementalCheckpointOrdersRowsContract() *payload.Contract {
	return &payload.Contract{
		Name:           "rows",
		Representation: model.ContractRepresentationRows,
		Fields: []payload.ContractField{
			{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
			{Name: "cursor", Type: model.ContractFieldTypeValueString, Required: true},
		},
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func (s *incrementalCheckpointTestRunStore) PreviousSuccess(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, time.Time) (*executionruntime.SuccessfulRun, error) {
	return nil, pipelinerun.ErrTimelineNotFound
}

func (s *incrementalCheckpointTestRunStore) ObserveWorker(context.Context, slug.Slug, slug.Slug, slug.Slug, string, time.Time) error {
	return nil
}
