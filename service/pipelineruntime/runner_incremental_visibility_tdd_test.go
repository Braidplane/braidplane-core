package pipelineruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestRunner_RunIncrementalVisibilitySummarizesCommittedCheckpoint(t *testing.T) {
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
	require.Equal(t, "incremental", runResult.Metadata.Incremental.Mode)
	require.Equal(t, "committed", runResult.Metadata.Incremental.CheckpointStatus)
	require.Equal(t, "fetch-users", runResult.Metadata.Incremental.SourceStepSlug)
	require.Equal(t, "safe", runResult.Metadata.Incremental.ReplaySafetyStatus)
	require.Empty(t, runResult.Metadata.Incremental.BlockedReason)
	require.Empty(t, runResult.Metadata.Incremental.ReplayBlockedReason)
	require.Contains(t, runTimelineEventTypes(runResult.Timeline), "checkpoint_observed")
	require.Contains(t, runTimelineEventTypes(runResult.Timeline), "checkpoint_committed")

	require.Len(t, runResult.IncrementalDiagnostics.Sources, 1)
	source := runResult.IncrementalDiagnostics.Sources[0]
	require.Equal(t, "fetch-users", source.SourceStepSlug)
	require.Equal(t, "watermark", source.CheckpointKind)
	require.Equal(t, "up to 10:00", source.PreviousCheckpoint.Summary)
	require.Equal(t, "up to 11:00", source.ObservedCheckpoint.Summary)
	require.Equal(t, "committed", source.CommitResult.Status)
	require.Empty(t, source.CommitResult.BlockedReason)
	require.False(t, source.PreviousCheckpoint.RawValueExposed)
	require.False(t, source.ObservedCheckpoint.RawValueExposed)
}

func TestRunner_RunIncrementalVisibilityRedactsCursorCheckpointTokens(t *testing.T) {
	t.Parallel()

	committedUpper := json.RawMessage(`{"cursor":"secret-cursor-previous"}`)
	observedUpper := json.RawMessage(`{"cursor":"secret-cursor-next"}`)
	store := newIncrementalCheckpointTestRunStore(&pipelinerun.IncrementalCheckpoint{
		PipelineSlug:        must.Slug("users-sync"),
		SourceStepSlug:      must.Slug("fetch-users"),
		CheckpointNamespace: "normal",
		CheckpointKind:      "cursor",
		ObservedUpperBound:  committedUpper,
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
			return successfulSQLResult(1), &pipelinerun.IncrementalCheckpointObservation{
				SourceStepSlug:      must.Slug("fetch-users"),
				CheckpointNamespace: "normal",
				CheckpointKind:      "cursor",
				ObservedLowerBound:  checkpoint.LowerBound,
				ObservedUpperBound:  observedUpper,
			}, nil
		},
	}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, runnerNoopSinkRuntime{})
	pipeline := incrementalCheckpointPipeline()
	pipeline.Steps[0].Query.OperationType = "http"
	pipeline.Steps[0].Query.Incremental.CheckpointKind = incrementalCheckpointTestKind(model.IncrementalCheckpointKindCursor)
	pipeline.Steps[0].Query.Incremental.Field = ""
	pipeline.Steps[0].Query.Incremental.Cursor = "next_page_token"
	pipeline.Steps[0].Query.Incremental.LookbackWindow = ""
	pipeline.Steps[0].Query.Incremental.Ordering = nil
	pipeline.Steps[0].Query.Incremental.DedupeKey = ""

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-redacts-cursor-checkpoint",
		PipelineVersionID: 42,
		Pipeline:          pipeline,
	})

	require.NoError(t, err)
	require.Len(t, runResult.IncrementalDiagnostics.Sources, 1)
	source := runResult.IncrementalDiagnostics.Sources[0]
	require.False(t, source.PreviousCheckpoint.RawValueExposed)
	require.False(t, source.Window.RawValueExposed)
	require.False(t, source.ObservedCheckpoint.RawValueExposed)

	diagnostics := fmt.Sprint(runResult.Metadata.Incremental) + " " + fmt.Sprint(source)
	require.NotContains(t, diagnostics, "secret-cursor-previous")
	require.NotContains(t, diagnostics, "secret-cursor-next")
	require.Contains(t, strings.ToLower(source.PreviousCheckpoint.Summary), "redacted")
	require.Contains(t, strings.ToLower(source.ObservedCheckpoint.Summary), "redacted")
}

func TestRunner_RunIncrementalVisibilitySummarizesBlockedCheckpoint(t *testing.T) {
	t.Parallel()

	observedUpper := json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`)
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
	sinkErr := xerror.Unexpected().WithPublicDetail("sink write failed").Detail("sink write failed")
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
	require.Equal(t, "incremental", runResult.Metadata.Incremental.Mode)
	require.Equal(t, "blocked", runResult.Metadata.Incremental.CheckpointStatus)
	require.Equal(t, "fetch-users", runResult.Metadata.Incremental.SourceStepSlug)
	require.Contains(t, runResult.Metadata.Incremental.BlockedReason, "sink write failed")
	require.Equal(t, "blocked", runResult.Metadata.Incremental.ReplaySafetyStatus)
	require.Contains(t, runResult.Metadata.Incremental.ReplayBlockedReason, "sink write failed")
	require.Contains(t, runTimelineEventTypes(runResult.Timeline), "checkpoint_blocked")

	require.Len(t, runResult.IncrementalDiagnostics.Sources, 1)
	source := runResult.IncrementalDiagnostics.Sources[0]
	require.Equal(t, "blocked", source.CommitResult.Status)
	require.Equal(t, "persist-users", source.CommitResult.BlockedByStep)
	require.Contains(t, source.CommitResult.BlockedReason, "sink write failed")
}

func TestRunner_RunIncrementalVisibilityBlocksReplayForUnsafeSiblingSink(t *testing.T) {
	t.Parallel()

	observedUpper := json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`)
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
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, &fakeSinkRuntime{
		executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
			if input.SinkOperationRef == must.Slug("upsert-users") {
				return xerror.Unexpected().Detail("warehouse write failed")
			}
			return nil
		},
	})
	pipeline := fanOutIncrementalCheckpointPipeline()
	pipeline.Steps[1], pipeline.Steps[2] = pipeline.Steps[2], pipeline.Steps[1]

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-blocks-replay-for-unsafe-sibling",
		PipelineVersionID: 42,
		Pipeline:          pipeline,
	})

	require.Error(t, err)
	require.NotNil(t, runResult)
	require.Equal(t, "blocked", runResult.Metadata.Incremental.ReplaySafetyStatus)
	require.Equal(t, "append-audit", runResult.IncrementalDiagnostics.ReplaySafety.BlockedSink)
	require.Contains(t, runResult.Metadata.Incremental.ReplayBlockedReason, "successful sibling sink")
	require.Contains(t, runResult.Metadata.Incremental.ReplayBlockedReason, "unsafe")
}

func TestRunner_RunIncrementalVisibilitySummarizesUnchangedCheckpoint(t *testing.T) {
	t.Parallel()

	lower := json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)
	store := newIncrementalCheckpointTestRunStore(&pipelinerun.IncrementalCheckpoint{
		ObservedUpperBound: lower,
		DisplayMetadata:    json.RawMessage(`{"summary":"up to 10:00","record_count":10}`),
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
			return successfulSQLResult(0), &pipelinerun.IncrementalCheckpointObservation{
				SourceStepSlug:      must.Slug("fetch-users"),
				CheckpointNamespace: "normal",
				CheckpointKind:      "watermark",
				ObservedLowerBound:  checkpoint.LowerBound,
				ObservedUpperBound:  checkpoint.LowerBound,
				DisplayMetadata:     json.RawMessage(`{"summary":"no new rows","record_count":0}`),
			}, nil
		},
	}
	ledger := &fakeSinkEffectLedger{}
	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{}, ledger)
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, sinkRuntime)

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-unchanged-checkpoint",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
	})

	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Equal(t, "incremental", runResult.Metadata.Incremental.Mode)
	require.Equal(t, "unchanged", runResult.Metadata.Incremental.CheckpointStatus)
	require.Equal(t, "safe", runResult.Metadata.Incremental.ReplaySafetyStatus)
	require.Zero(t, ledger.beginCalls)
	require.Zero(t, ledger.markCalls)
	require.Equal(t, "skip_empty_input", runResult.Timeline.Snapshots[1].Input.SinkReplayDecision)
	require.Len(t, runResult.IncrementalDiagnostics.Sources, 1)
	require.Equal(t, "unchanged", runResult.IncrementalDiagnostics.Sources[0].CommitResult.Status)
	require.Equal(t, "no new rows", runResult.IncrementalDiagnostics.Sources[0].ObservedCheckpoint.Summary)
}

func TestRunner_RunIncrementalVisibilitySummarizesBackfillChunk(t *testing.T) {
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
				DisplayMetadata:    json.RawMessage(`{"summary":"2026-05-01 to 2026-05-02","record_count":3}`),
			}, nil
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
	require.Equal(t, "backfill", runResult.Metadata.Incremental.Mode)
	require.Equal(t, "skipped", runResult.Metadata.Incremental.CheckpointStatus)
	require.Equal(t, "series-001", runResult.Metadata.Incremental.BackfillSeriesSlug)
	require.Equal(t, 1, runResult.Metadata.Incremental.BackfillChunkIndex)
	require.Equal(t, 10, runResult.Metadata.Incremental.BackfillChunkCount)
	require.Equal(t, "safe", runResult.Metadata.Incremental.ReplaySafetyStatus)
	require.Empty(t, runResult.Metadata.Incremental.ReplayBlockedReason)
	require.Len(t, runResult.IncrementalDiagnostics.Sources, 1)
	require.Equal(t, "2026-05-01 to 2026-05-02", runResult.IncrementalDiagnostics.Sources[0].Window.Summary)
	require.Equal(t, "skipped", runResult.IncrementalDiagnostics.Sources[0].CommitResult.Status)
	require.Empty(t, runResult.IncrementalDiagnostics.Sources[0].CommitResult.BlockedByStep)
	require.Empty(t, runResult.IncrementalDiagnostics.Sources[0].CommitResult.BlockedReason)
	require.Contains(t, runTimelineEventTypes(runResult.Timeline), "checkpoint_skipped")
	require.NotContains(t, runTimelineEventTypes(runResult.Timeline), "checkpoint_blocked")
}

func TestRunner_RunIncrementalVisibilityKeepsSourceInputWhenObservationMissing(t *testing.T) {
	t.Parallel()

	lower := json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)
	store := newIncrementalCheckpointTestRunStore(&pipelinerun.IncrementalCheckpoint{
		ObservedUpperBound: lower,
		DisplayMetadata:    json.RawMessage(`{"summary":"up to 10:00"}`),
		CommitStatus:       pipelinerun.IncrementalCheckpointStatusCommitted,
	})
	executor := &incrementalCheckpointQueryExecutor{
		executeWithCheckpointFn: func(
			context.Context,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			map[string]any,
			*pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			return nil, nil, xerror.Unexpected().Detail("source query failed before observation")
		},
	}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, runnerNoopSinkRuntime{})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-source-fails-before-observation",
		PipelineVersionID: 42,
		Pipeline:          incrementalCheckpointPipeline(),
	})

	require.Error(t, err)
	require.NotNil(t, runResult)
	require.Equal(t, "incremental", runResult.Metadata.Incremental.Mode)
	require.Equal(t, "skipped", runResult.Metadata.Incremental.CheckpointStatus)
	require.Equal(t, "fetch-users", runResult.Metadata.Incremental.SourceStepSlug)
	require.Len(t, runResult.IncrementalDiagnostics.Sources, 1)
	source := runResult.IncrementalDiagnostics.Sources[0]
	require.Equal(t, "fetch-users", source.SourceStepSlug)
	require.Equal(t, "up to 10:00", source.PreviousCheckpoint.Summary)
	require.Equal(t, "after 2026-05-24T10:00:00Z", source.Window.Summary)
	require.Equal(t, "not observed", source.ObservedCheckpoint.Summary)
	require.Equal(t, "skipped", source.CommitResult.Status)
}

func TestRunner_RunIncrementalVisibilityUsesSourceForHighestPriorityStatus(t *testing.T) {
	t.Parallel()

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
				SourceStepSlug:      checkpoint.Lookup.SourceStepSlug,
				CheckpointNamespace: "normal",
				CheckpointKind:      "watermark",
				ObservedLowerBound:  checkpoint.LowerBound,
				ObservedUpperBound:  json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`),
				DisplayMetadata:     json.RawMessage(`{"summary":"up to 11:00"}`),
			}, nil
		},
	}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, &fakeSinkRuntime{
		executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
			if input.SinkOperationRef == must.Slug("upsert-orders") {
				return xerror.Unexpected().Detail("orders sink failed")
			}
			return nil
		},
	})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-multi-source-summary",
		PipelineVersionID: 42,
		Pipeline:          multiSourceIncrementalCheckpointPipeline(),
	})

	require.Error(t, err)
	require.NotNil(t, runResult)
	require.Equal(t, "blocked", runResult.Metadata.Incremental.CheckpointStatus)
	require.Equal(t, "fetch-orders", runResult.Metadata.Incremental.SourceStepSlug)
	require.Contains(t, runResult.Metadata.Incremental.BlockedReason, "orders sink failed")
}

func TestRunner_RunFullVisibilityMarksCheckpointNotApplicable(t *testing.T) {
	t.Parallel()

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{
		ExecuteFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, map[string]any) (*result.ExecutionResult, error) {
			return successfulSQLResult(1), nil
		},
	}, nil)

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:           must.Slug("acme"),
		ProjectSlug:       must.Slug("default"),
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           "run-full",
		PipelineVersionID: 42,
		Pipeline:          fullVisibilityPipeline(),
	})

	require.NoError(t, err)
	require.Equal(t, "full", runResult.Metadata.Incremental.Mode)
	require.Equal(t, "not_applicable", runResult.Metadata.Incremental.CheckpointStatus)
	require.Empty(t, runResult.Metadata.Incremental.SourceStepSlug)
	require.Empty(t, runResult.IncrementalDiagnostics.Sources)
}

func runTimelineEventTypes(timeline pipelinerun.RunTimeline) []string {
	types := make([]string, 0, len(timeline.Events))
	for _, event := range timeline.Events {
		types = append(types, event.Type)
	}
	slices.Sort(types)
	return types
}

func fullVisibilityPipeline() *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: must.Slug("users-full-sync"), Name: "Users Full Sync"},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: must.Slug("fetch-users"),
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: must.Slug("acme-postgres"),
				OperationType:  "sql",
				OperationRef:   must.Slug("list-users"),
			},
		}},
	}
}
