package pipelineruntime

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/testutil"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	executionruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func testExecutionContext() *executionruntime.ExecutionContext {
	return &executionruntime.ExecutionContext{At: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
}

func TestExecutionSnapshotPinsHistoryAndReusesItForRetry(t *testing.T) {
	at := testExecutionContext().At
	prior := &executionruntime.SuccessfulRun{RunSlug: "previous", StartedAt: at.Add(-2 * time.Hour), FinishedAt: at.Add(-time.Hour)}
	calls := 0
	store := &fakeRunStore{previousSuccessFn: func(_ context.Context, org, project, environment, pipeline slug.Slug, version int64, before time.Time) (*executionruntime.SuccessfulRun, error) {
		calls++
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("default"), project)
		require.Equal(t, slug.Slug("prod"), environment)
		require.Equal(t, slug.Slug("orders"), pipeline)
		require.Equal(t, int64(42), version)
		require.Equal(t, at, before)
		return prior, nil
	}}
	runner := &Runner{store: store}
	pipeline := incrementalCheckpointPipeline()
	pipeline.Metadata.Slug = "orders"
	req := RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", Pipeline: pipeline, PipelineVersionID: 42, RunSlug: "original"}
	snapshot, err := runner.executionSnapshot(t.Context(), req, at)
	require.NoError(t, err)
	require.Equal(t, prior, snapshot.PreviousSuccess)
	store.timelines = map[string]pipelinerun.RunTimeline{"original": {ExecutionContext: snapshot}}
	// New successes appearing after selection cannot change this attempt or its retry.
	store.previousSuccessFn = func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, time.Time) (*executionruntime.SuccessfulRun, error) {
		t.Fatal("history must not be reread")
		return nil, pipelinerun.ErrTimelineNotFound
	}
	same, err := runner.executionSnapshot(t.Context(), req, at.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, snapshot, same)
	original := slug.Slug("original")
	req.RunSlug = "retry"
	req.Source.AttemptNumber = 2
	req.Source.RootRunSlug = original
	req.Source.RetryOfRunSlug = &original
	retry, err := runner.executionSnapshot(t.Context(), req, at.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, snapshot, retry)
	require.Equal(t, 1, calls)
}

func TestExecutionSnapshotFirstRunAndLegacyRetry(t *testing.T) {
	runner := &Runner{store: &fakeRunStore{}}
	req := RunRequest{Pipeline: incrementalCheckpointPipeline()}
	snapshot, err := runner.executionSnapshot(t.Context(), req, testExecutionContext().At)
	require.NoError(t, err)
	require.Nil(t, snapshot.PreviousSuccess)
	legacy := slug.Slug("legacy")
	req.Source.RetryOfRunSlug = &legacy
	_, err = runner.executionSnapshot(t.Context(), req, testExecutionContext().At)
	require.True(t, xerror.Is(err, xerror.InvalidInput(xerror.PipelineRun)))
	require.ErrorContains(t, err, "retained execution context")
}

func TestRunnerPinsMetadataBeforeDispatchAndThroughRetryAndRecovery(t *testing.T) {
	at := testExecutionContext().At
	now := at
	store := &fakeRunStore{}
	historyReads := 0
	store.previousSuccessFn = func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, time.Time) (*executionruntime.SuccessfulRun, error) {
		historyReads++
		return &executionruntime.SuccessfulRun{RunSlug: "previous", StartedAt: at.Add(-2 * time.Hour), FinishedAt: at.Add(-time.Hour)}, nil
	}
	pipeline := incrementalCheckpointPipeline()
	pipeline.Steps[0].Query.Incremental = nil
	pipeline.Steps[0].Query.Parameters = map[string]any{"since": "{{runtime.run.datetime | offset:-24h}}", "previous": "{{runtime.previous_success.run_slug}}", "attempt": "{{runtime.run.slug}}"}
	pipeline.Steps[1].Sink.Config = map[string]any{"as_of": "{{runtime.run.date}}", "attempt": "{{runtime.run.slug}}"}
	sourceCalls, sinkCalls := 0, 0
	executor := &testutil.FakeQueryExecutor{ExecuteFn: func(ctx context.Context, _, _, _ slug.Slug, params map[string]any) (*result.ExecutionResult, error) {
		sourceCalls++
		expectedAttempt := []string{"original", "retry"}[sourceCalls-1]
		require.Equal(t, expectedAttempt, params["attempt"])
		snapshot, ok := executionruntime.ExecutionContextFrom(ctx)
		require.True(t, ok)
		require.Equal(t, at, snapshot.At)
		require.Equal(t, expectedAttempt, snapshot.RunSlug)
		require.Equal(t, at.Add(-24*time.Hour).Format(time.RFC3339), params["since"])
		require.Equal(t, "previous", params["previous"])
		require.NotNil(t, store.timelines["original"].ExecutionContext, "metadata must be durable before the first provider call")
		now = now.Add(time.Hour)
		return successfulSQLResult(1), nil
	}}
	runner, err := NewRunner(adaptTestQueryExecutor(executor), &testutil.FakeClock{NowFn: func() time.Time { return now }}, store, incrementalCheckpointDataSourceService{}, &fakeSinkRuntime{executeFn: func(ctx context.Context, input *SinkRuntimeInput) error {
		sinkCalls++
		expectedAttempt := []string{"original", "retry", "recovered"}[sinkCalls-1]
		require.Equal(t, expectedAttempt, input.SinkConfig["attempt"])
		snapshot, ok := executionruntime.ExecutionContextFrom(ctx)
		require.True(t, ok)
		require.Equal(t, at, snapshot.At)
		require.Equal(t, expectedAttempt, snapshot.RunSlug)
		require.Equal(t, at.Format(time.DateOnly), input.SinkConfig["as_of"])
		if sinkCalls == 1 {
			return xerror.Unexpected().Detail("synthetic sink failure")
		}
		return nil
	}})
	require.NoError(t, err)
	req := RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", RunSlug: "original", Pipeline: pipeline, PipelineVersionID: 42}
	_, err = runner.Run(t.Context(), req)
	require.ErrorContains(t, err, "synthetic sink failure")
	original := slug.Slug("original")
	req.RunSlug = "retry"
	req.Source.AttemptNumber = 2
	req.Source.RootRunSlug = original
	req.Source.RetryOfRunSlug = &original
	_, err = runner.Run(t.Context(), req)
	require.NoError(t, err)
	_, err = runner.Recover(t.Context(), ReplayRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", SourceRunSlug: "original", ReplayRunSlug: "recovered", PipelineVersionID: 42, InputMode: ReplayInputModePinned})
	require.NoError(t, err)
	require.Equal(t, 1, historyReads)
	require.Equal(t, 2, sourceCalls)
	require.Equal(t, 3, sinkCalls)
	require.Equal(t, "original", store.timelines["original"].ExecutionContext.RunSlug)
}

func TestRunnerKeepsSuppliedRuntimeSyntaxLiteral(t *testing.T) {
	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(_ context.Context, _, _, _ slug.Slug, params map[string]any) (*result.ExecutionResult, error) {
		require.Equal(t, "{{runtime.run.date | add_days:-1}}", params["as_of"])
		return successfulSQLResult(1), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time { return testExecutionContext().At }})
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}, Steps: []pipelinespec.Step{{
		Kind: "query", Slug: "read", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationRef: "read", Parameters: map[string]any{
			"as_of": executionruntime.LiteralString("{{runtime.run.date | add_days:-1}}"),
		}},
	}}}
	_, err := runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", RunSlug: "literal-runtime", Pipeline: pipeline})
	require.NoError(t, err)
}
