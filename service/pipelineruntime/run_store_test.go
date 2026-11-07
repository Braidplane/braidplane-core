package pipelineruntime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
)

type fakeRunStorageService struct {
	createRunFn   func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, *createRunParams) error
	completeRunFn func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, *createRunParams) error

	createRunCalls   int
	completeRunCalls int

	lastCreateRunOrgSlug       slug.Slug
	lastCreateRunProjectSlug   slug.Slug
	lastCreateRunEnvironment   slug.Slug
	lastCreateRunParams        *createRunParams
	lastCompleteRunOrgSlug     slug.Slug
	lastCompleteRunProjectSlug slug.Slug
	lastCompleteRunEnvironment slug.Slug
	lastCompleteRunParams      *createRunParams
}

var errFakeDBTXMethodNotUsed = fmt.Errorf("fake dbtx method should not be used")

type fakeDBTX struct{}

func (f *fakeDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errFakeDBTXMethodNotUsed
}

func (f *fakeDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errFakeDBTXMethodNotUsed
}

func (f *fakeDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

func (f *fakeRunStorageService) CreateRun(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params *createRunParams,
) error {
	f.createRunCalls++
	f.lastCreateRunOrgSlug = orgSlug
	f.lastCreateRunProjectSlug = projectSlug
	f.lastCreateRunEnvironment = environmentSlug
	f.lastCreateRunParams = copyCreateRunParams(params)
	if f.createRunFn != nil {
		return f.createRunFn(ctx, tx, orgSlug, projectSlug, environmentSlug, params)
	}
	return nil
}

func (f *fakeRunStorageService) CompleteRun(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params *createRunParams,
) error {
	f.completeRunCalls++
	f.lastCompleteRunOrgSlug = orgSlug
	f.lastCompleteRunProjectSlug = projectSlug
	f.lastCompleteRunEnvironment = environmentSlug
	f.lastCompleteRunParams = copyCreateRunParams(params)
	if f.completeRunFn != nil {
		return f.completeRunFn(ctx, tx, orgSlug, projectSlug, environmentSlug, params)
	}
	return nil
}

func copyCreateRunParams(params *createRunParams) *createRunParams {
	if params == nil {
		return nil
	}
	copied := *params
	if params.RequestID != nil {
		requestID := *params.RequestID
		copied.RequestID = &requestID
	}
	if params.ErrorSummary != nil {
		errorSummary := *params.ErrorSummary
		copied.ErrorSummary = &errorSummary
	}
	if params.ReplayOfRunSlug != nil {
		replayOf := *params.ReplayOfRunSlug
		copied.ReplayOfRunSlug = &replayOf
	}
	if params.ReplayFromStep != nil {
		replayFrom := *params.ReplayFromStep
		copied.ReplayFromStep = &replayFrom
	}
	if params.EndedAt != nil {
		endedAt := *params.EndedAt
		copied.EndedAt = &endedAt
	}
	copied.Timeline = append([]byte(nil), params.Timeline...)
	return &copied
}

type fakeTransactionManager struct {
	runInTxFn func(context.Context, func(context.Context, postgres.DBTX) error) error
	runInTxs  int
}

func (f *fakeTransactionManager) RunInTx(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	f.runInTxs++
	if f.runInTxFn != nil {
		return f.runInTxFn(ctx, fn)
	}
	return fn(ctx, &fakeDBTX{})
}

func (f *fakeTransactionManager) RunRead(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	return fn(ctx, &fakeDBTX{})
}

func TestNewRunStore_ComposesRunWriterAndTimelineStore(t *testing.T) {
	t.Parallel()

	runs := pipelineruns.New()
	store := NewRunStore(&fakeTransactionManager{}, &fakeDBTX{}, nil, testutil.FakeRunSucceededNotifier{}, runs)
	runStore, ok := store.(*runStore)
	require.True(t, ok)
	require.NotNil(t, runStore.runWriter)
	require.NotNil(t, runStore.timelineStore)

	runWriter, ok := runStore.runWriter.(*runWriter)
	require.True(t, ok)
	require.NotNil(t, runWriter.clock)
	require.NotNil(t, runWriter.runStorageSvc)
	storage, ok := runWriter.runStorageSvc.(*persistedRunStorage)
	require.True(t, ok)
	require.Same(t, runs, storage.repo)
	require.PanicsWithValue(t, "pipelineruntime: pipeline run repository is required", func() {
		NewRunStore(&fakeTransactionManager{}, &fakeDBTX{}, nil, testutil.FakeRunSucceededNotifier{}, nil)
	})
}

func TestRunWriter_PersistRun_ValidatesInputs(t *testing.T) {
	t.Parallel()

	writer := &runWriter{
		notifier:      testutil.FakeRunSucceededNotifier{},
		txm:           &fakeTransactionManager{},
		clock:         &testutil.FakeClock{},
		runStorageSvc: &fakeRunStorageService{},
	}

	err := writer.PersistRun(
		context.Background(),
		must.Slug("acme"),
		must.Slug("default"),
		must.Slug("prod"),
		nil,
		&pipelinerun.RunResult{},
	)
	require.ErrorIs(t, err, pipelinerun.ErrNilPipeline)

	err = writer.PersistRun(
		context.Background(),
		must.Slug("acme"),
		must.Slug("default"),
		must.Slug("prod"),
		&pipelinespec.Pipeline{},
		nil,
	)
	require.ErrorContains(t, err, "run result is required")
}

func TestRunWriter_PersistRun_MapsPersistenceFields(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.March, 30, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Second)
	environmentSlug := must.Slug("prod")
	replayOf := "run-000"
	replayFrom := "fetch-users"
	failedStep := must.Slug("sink-users")
	requestID := "req-123"
	projectSlug := must.Slug("default")

	storage := &fakeRunStorageService{}
	writer := &runWriter{
		notifier: testutil.FakeRunSucceededNotifier{},
		txm:      &fakeTransactionManager{},
		clock: &testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
		}},
		runStorageSvc: storage,
	}

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: must.Slug("users-sync"), Name: "Users Sync"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("acme-postgres"),
					OperationType:  "sql",
					OperationRef:   must.Slug("list-users"),
				},
			},
			{
				Kind: "sink",
				Slug: must.Slug("sink-users"),
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
				},
			},
		},
	}

	runResult := &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{
			ProjectSlug:       projectSlug,
			EnvironmentSlug:   environmentSlug,
			RunSlug:           "run-001",
			Status:            "failed",
			DurationMs:        2000,
			StepCount:         2,
			PipelineVersionID: 42,
			FailedStep:        &failedStep,
			ReplayOfRunSlug:   &replayOf,
			ReplayFromStep:    &replayFrom,
		},
		Timeline: pipelinerun.RunTimeline{
			RunSlug:         "run-001",
			PipelineSlug:    must.Slug("users-sync"),
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
			Status:          "failed",
			Events: []pipelinerun.TimelineEvent{{At: start.Add(-5 * time.Second), Type: "run_queued", Status: "queued"}, {At: start, Type: "run_started", Status: "running"}, {
				At: end, Type: "run_completed", Status: "failed",
			}},
			Failure: &pipelinerun.FailureContext{Step: failedStep, Error: "sink failed", At: end},
		},
	}

	ctx := reqctx.WithRequestID(context.Background(), requestID)
	err := writer.PersistRun(
		ctx,
		must.Slug("acme"),
		projectSlug,
		environmentSlug,
		pipeline,
		runResult,
	)
	require.NoError(t, err)

	require.Equal(t, 1, storage.createRunCalls)
	require.Equal(t, must.Slug("acme"), storage.lastCreateRunOrgSlug)
	require.Equal(t, must.Slug("default"), storage.lastCreateRunProjectSlug)
	require.Equal(t, environmentSlug, storage.lastCreateRunEnvironment)
	require.NotNil(t, storage.lastCreateRunParams)
	require.Equal(t, "users-sync", storage.lastCreateRunParams.PipelineSlug)
	require.EqualValues(t, 42, storage.lastCreateRunParams.PipelineVersionID)
	require.Equal(t, "run-001", storage.lastCreateRunParams.RunSlug)
	require.NotNil(t, storage.lastCreateRunParams.RequestID)
	require.Equal(t, requestID, *storage.lastCreateRunParams.RequestID)
	require.Equal(t, "failed", storage.lastCreateRunParams.Status)
	require.EqualValues(t, 2000, storage.lastCreateRunParams.DurationMs)
	require.EqualValues(t, 2, storage.lastCreateRunParams.StepCount)
	require.Equal(t, "sink-users", storage.lastCreateRunParams.StepSlug)
	require.NotNil(t, storage.lastCreateRunParams.ErrorSummary)
	require.Equal(t, "sink failed", *storage.lastCreateRunParams.ErrorSummary)
	require.Equal(t, start, storage.lastCreateRunParams.StartedAt)
	require.NotNil(t, storage.lastCreateRunParams.EndedAt)
	require.Equal(t, end, *storage.lastCreateRunParams.EndedAt)
	require.NotNil(t, storage.lastCreateRunParams.ReplayOfRunSlug)
	require.Equal(t, replayOf, *storage.lastCreateRunParams.ReplayOfRunSlug)
	require.NotNil(t, storage.lastCreateRunParams.ReplayFromStep)
	require.Equal(t, replayFrom, *storage.lastCreateRunParams.ReplayFromStep)
	require.NotEmpty(t, storage.lastCreateRunParams.Timeline)
}

func TestRunWriter_PersistRun_MapsIncrementalVisibilityFields(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 24, 11, 0, 0, 0, time.UTC)
	storage := &fakeRunStorageService{}
	writer := &runWriter{
		notifier:      testutil.FakeRunSucceededNotifier{},
		txm:           &fakeTransactionManager{},
		clock:         &testutil.FakeClock{NowFn: func() time.Time { return now }},
		runStorageSvc: storage,
	}
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: must.Slug("users-sync"), Name: "Users Sync"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("acme-postgres"),
					OperationType:  "sql",
					OperationRef:   must.Slug("list-users"),
				},
			},
		},
	}
	runResult := &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{
			RunSlug:           "run-incremental",
			Status:            "succeeded",
			DurationMs:        12,
			StepCount:         1,
			PipelineVersionID: 42,
			Incremental: pipelinerun.IncrementalRunSummary{
				Mode:               "incremental",
				CheckpointStatus:   "committed",
				SourceStepSlug:     "fetch-users",
				ReplaySafetyStatus: "safe",
			},
		},
		Timeline: pipelinerun.RunTimeline{
			RunSlug:      "run-incremental",
			PipelineSlug: must.Slug("users-sync"),
			Status:       "succeeded",
			Events: []pipelinerun.TimelineEvent{
				{At: now, Type: "run_started", Status: "running"},
				{At: now, Type: "checkpoint_committed", Step: must.Slug("fetch-users"), Status: "committed"},
			},
		},
		IncrementalDiagnostics: pipelinerun.IncrementalRunDiagnostics{
			Sources: []pipelinerun.IncrementalSourceDiagnostic{{
				SourceStepSlug: "fetch-users",
				CheckpointKind: "watermark",
				PreviousCheckpoint: pipelinerun.IncrementalCheckpointSummary{
					Summary:         "up to 10:00",
					RawValueExposed: false,
				},
				ObservedCheckpoint: pipelinerun.IncrementalCheckpointSummary{
					Summary:         "up to 11:00",
					RawValueExposed: false,
				},
				CommitResult: pipelinerun.IncrementalCheckpointCommitResult{
					Status: "committed",
				},
			}},
		},
	}

	err := writer.PersistRun(context.Background(), must.Slug("acme"), must.Slug("default"), must.Slug("prod"), pipeline, runResult)
	require.NoError(t, err)

	require.Equal(t, "incremental", storage.lastCreateRunParams.Incremental.Mode)
	require.Equal(t, "committed", storage.lastCreateRunParams.Incremental.CheckpointStatus)
	require.Equal(t, "fetch-users", storage.lastCreateRunParams.Incremental.SourceStepSlug)
	require.JSONEq(
		t,
		`{"sources":[{"source_step_slug":"fetch-users","checkpoint_kind":"watermark","previous_checkpoint":{"summary":"up to 10:00","raw_value_exposed":false},"window":{"summary":"","raw_value_exposed":false},"observed_checkpoint":{"summary":"up to 11:00","raw_value_exposed":false},"commit_result":{"status":"committed"}}],"replay_safety":{"status":""}}`,
		string(storage.lastCreateRunParams.IncrementalDiagnostics),
	)
}

func TestRunWriter_PersistRun_WrapsCreateRunErrors(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("boom")
	writer := &runWriter{
		notifier: testutil.FakeRunSucceededNotifier{},
		txm:      &fakeTransactionManager{},
		clock:    clock.New(),
		runStorageSvc: &fakeRunStorageService{createRunFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			*createRunParams,
		) error {
			return expectedErr
		}},
	}

	err := writer.PersistRun(context.Background(), must.Slug("acme"), must.Slug("default"), must.Slug("prod"), &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: must.Slug("users-sync"), Name: "Users Sync"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("acme-postgres"),
					OperationType:  "sql",
					OperationRef:   must.Slug("list-users"),
				},
			},
			{
				Kind: "sink",
				Slug: must.Slug("sink-users"),
				Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")}},
			},
		},
	}, &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{
			ProjectSlug:     must.Slug("default"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-001",
			Status:          "succeeded",
			StepCount:       1,
		},
		Timeline: pipelinerun.RunTimeline{
			RunSlug:         "run-001",
			PipelineSlug:    must.Slug("users-sync"),
			ProjectSlug:     must.Slug("default"),
			EnvironmentSlug: must.Slug("prod"),
			Status:          "succeeded",
		},
	})

	require.ErrorIs(t, err, expectedErr)
	require.ErrorContains(t, err, "create pipeline run")
}

func TestLastExecutedStepSlug(t *testing.T) {
	t.Parallel()

	t.Run("UsesFailedStepWhenPresent", func(t *testing.T) {
		t.Parallel()

		failedStep := must.Slug("transform-users")
		value := lastExecutedStepSlug(&pipelinespec.Pipeline{
			Steps: []pipelinespec.Step{
				{Slug: must.Slug("fetch-users")},
				{Slug: must.Slug("sink-users")},
			},
		}, &pipelinerun.RunResult{Metadata: pipelinerun.RunMetadata{FailedStep: &failedStep}})

		require.Equal(t, "transform-users", value)
	})

	t.Run("UsesLastPipelineStepOnSuccess", func(t *testing.T) {
		t.Parallel()

		value := lastExecutedStepSlug(&pipelinespec.Pipeline{
			Steps: []pipelinespec.Step{
				{Slug: must.Slug("fetch-users")},
				{Slug: must.Slug("sink-users")},
			},
		}, &pipelinerun.RunResult{})

		require.Equal(t, "sink-users", value)
	})

	t.Run("IgnoresCheckpointEventsAfterSink", func(t *testing.T) {
		t.Parallel()
		run := &pipelinerun.RunResult{Timeline: pipelinerun.RunTimeline{Events: []pipelinerun.TimelineEvent{
			{Type: "step_succeeded", Step: "sink-users"},
			{Type: "checkpoint_committed", Step: "fetch-users"},
			{Type: "run_completed", Status: "succeeded"},
		}}}
		require.Equal(t, "sink-users", lastExecutedStepSlug(nil, run))
	})

	t.Run("ReturnsEmptyWhenPipelineHasNoSteps", func(t *testing.T) {
		t.Parallel()

		value := lastExecutedStepSlug(&pipelinespec.Pipeline{}, &pipelinerun.RunResult{})
		require.Empty(t, value)
	})
}

func TestRunWriter_ActiveCheckEvidenceHasNoRetentionEndTime(t *testing.T) {
	storage := &fakeRunStorageService{}
	writer := &runWriter{notifier: testutil.FakeRunSucceededNotifier{}, txm: &fakeTransactionManager{}, clock: clock.New(), runStorageSvc: storage}
	now := time.Now().UTC()
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}}
	run := &pipelinerun.RunResult{Metadata: pipelinerun.RunMetadata{RunSlug: "attempt", Status: "running"}, Timeline: pipelinerun.RunTimeline{RunSlug: "attempt", PipelineSlug: "orders", Status: "running", Events: []pipelinerun.TimelineEvent{{At: now, Type: "run_started", Status: "running"}}, StructuralChecks: []pipelinerun.StructuralCheck{{ID: "check", Status: pipelinerun.CheckStatusUnavailable, ReasonCode: "evaluating"}}}}
	require.NoError(t, writer.PersistRun(t.Context(), "acme", "etl", "staging", pipeline, run))
	require.Nil(t, storage.lastCreateRunParams.EndedAt)
	run.Metadata.Status = "failed"
	run.Timeline.Status = "failed"
	require.NoError(t, writer.PersistRun(t.Context(), "acme", "etl", "staging", pipeline, run))
	require.NotNil(t, storage.lastCreateRunParams.EndedAt)
}
