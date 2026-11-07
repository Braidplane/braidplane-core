package pipelineruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	executionruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
)

func TestPersistedRunStorage_CreateRunCompletesAcceptedRun(t *testing.T) {
	t.Parallel()

	repo := &fakePipelineRunRepository{
		completeRunFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			string,
			*pipelineruns.CompleteRunParams,
		) (*pipelineruns.PipelineRun, error) {
			return &pipelineruns.PipelineRun{}, nil
		},
	}
	storage := &persistedRunStorage{repo: repo}

	err := storage.CreateRun(
		context.Background(),
		&fakeDBTX{},
		must.Slug("acme"),
		must.Slug("default"),
		must.Slug("prod"),
		testCreateRunParams(),
	)

	require.NoError(t, err)
	require.Equal(t, 1, repo.completeRunCalls)
	require.Equal(t, 0, repo.createRunCalls)
	require.Equal(t, "run-001", repo.lastCompleteRunSlug)
	require.Equal(t, "users-sync", repo.lastCompleteRunParams.PipelineSlug)
	require.Equal(t, "succeeded", repo.lastCompleteRunParams.Status)
	require.EqualValues(t, 2, repo.lastCompleteRunParams.StepCount)
	require.JSONEq(t, `{"status":"succeeded"}`, string(repo.lastCompleteRunParams.Timeline))
}

func TestPersistedRunStorage_CreateRunFallsBackToInsertWhenNoAcceptedRunExists(t *testing.T) {
	t.Parallel()

	repo := &fakePipelineRunRepository{
		completeRunFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			string,
			*pipelineruns.CompleteRunParams,
		) (*pipelineruns.PipelineRun, error) {
			return nil, pipelineruns.ErrRunNotFound
		},
		createRunFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			*pipelineruns.CreateRunParams,
		) (*pipelineruns.PipelineRun, error) {
			return &pipelineruns.PipelineRun{}, nil
		},
	}
	storage := &persistedRunStorage{repo: repo}

	err := storage.CreateRun(
		context.Background(),
		&fakeDBTX{},
		must.Slug("acme"),
		must.Slug("default"),
		must.Slug("prod"),
		testCreateRunParams(),
	)

	require.NoError(t, err)
	require.Equal(t, 1, repo.completeRunCalls)
	require.Equal(t, 1, repo.createRunCalls)
	require.Equal(t, "run-001", repo.lastCreateRunParams.RunSlug)
	require.Equal(t, "succeeded", repo.lastCreateRunParams.Status)
}

func TestPersistedRunStorage_CreateRunReturnsCompletionErrors(t *testing.T) {
	t.Parallel()

	expectedErr := xerror.Unexpected().Detail("complete failed")
	repo := &fakePipelineRunRepository{
		completeRunFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			string,
			*pipelineruns.CompleteRunParams,
		) (*pipelineruns.PipelineRun, error) {
			return nil, expectedErr
		},
	}
	storage := &persistedRunStorage{repo: repo}

	err := storage.CreateRun(
		context.Background(),
		&fakeDBTX{},
		must.Slug("acme"),
		must.Slug("default"),
		must.Slug("prod"),
		testCreateRunParams(),
	)

	require.ErrorIs(t, err, expectedErr)
	require.ErrorContains(t, err, "complete accepted pipeline run")
	require.Equal(t, 1, repo.completeRunCalls)
	require.Equal(t, 0, repo.createRunCalls)
}

func testCreateRunParams() *createRunParams {
	now := time.Date(2026, time.July, 3, 10, 0, 0, 0, time.UTC)
	endedAt := now.Add(250 * time.Millisecond)
	return &createRunParams{
		PipelineVersionID: 42,
		PipelineSlug:      "users-sync",
		RunSlug:           "run-001",
		Source: pipelinerun.RunSourceSummary{
			Type:       model.PipelineRunSourceTypeManual,
			Summary:    "Manual run",
			Entrypoint: model.PipelineRunSourceEntrypointUI,
		},
		Status:                 "succeeded",
		DurationMs:             250,
		StepCount:              2,
		StepSlug:               "sink-users",
		StartedAt:              now,
		EndedAt:                &endedAt,
		Incremental:            pipelinerun.IncrementalRunSummary{Mode: "full", CheckpointStatus: "not_applicable"},
		IncrementalDiagnostics: json.RawMessage(`{"sources":[]}`),
		Timeline:               json.RawMessage(`{"status":"succeeded"}`),
	}
}

type fakePipelineRunRepository struct {
	createRunFn func(
		context.Context,
		postgres.DBTX,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		*pipelineruns.CreateRunParams,
	) (*pipelineruns.PipelineRun, error)
	completeRunFn func(
		context.Context,
		postgres.DBTX,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		string,
		*pipelineruns.CompleteRunParams,
	) (*pipelineruns.PipelineRun, error)

	createRunCalls        int
	completeRunCalls      int
	lastCreateRunParams   *pipelineruns.CreateRunParams
	lastCompleteRunSlug   string
	lastCompleteRunParams *pipelineruns.CompleteRunParams
}

func (f *fakePipelineRunRepository) CreateRun(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params *pipelineruns.CreateRunParams,
) (*pipelineruns.PipelineRun, error) {
	f.createRunCalls++
	f.lastCreateRunParams = params
	if f.createRunFn != nil {
		return f.createRunFn(ctx, tx, orgSlug, projectSlug, environmentSlug, params)
	}
	return &pipelineruns.PipelineRun{}, nil
}

func (f *fakePipelineRunRepository) MarkRunRunning(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	string,
	time.Time,
	json.RawMessage,
) (*pipelineruns.PipelineRun, error) {
	panic("MarkRunRunning should not be called")
}

func (f *fakePipelineRunRepository) CompleteRun(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
	params *pipelineruns.CompleteRunParams,
) (*pipelineruns.PipelineRun, error) {
	f.completeRunCalls++
	f.lastCompleteRunSlug = runSlug
	f.lastCompleteRunParams = params
	if f.completeRunFn != nil {
		return f.completeRunFn(ctx, tx, orgSlug, projectSlug, environmentSlug, runSlug, params)
	}
	return &pipelineruns.PipelineRun{}, nil
}

func (f *fakePipelineRunRepository) InspectLogicalRun(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*model.PipelineRunCompletion, error) {
	panic("InspectLogicalRun should not be called")
}

func (f *fakePipelineRunRepository) ListCompletionDecisions(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) ([]model.PipelineCompletionDecision, error) {
	panic("ListCompletionDecisions should not be called")
}

func (f *fakePipelineRunRepository) FindRunBySlug(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	string,
) (*pipelineruns.PipelineRun, error) {
	panic("FindRunBySlug should not be called")
}

func (f *fakePipelineRunRepository) ListRuns(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*pipelineruns.RunListFilters,
	model.ListRequest,
) ([]*pipelineruns.PipelineRun, error) {
	panic("ListRuns should not be called")
}

func (f *fakePipelineRunRepository) UpdateRunSourceEvent(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	string,
	slug.Slug,
) error {
	panic("UpdateRunSourceEvent should not be called")
}

func (f *fakePipelineRunRepository) FindLatestRunByPipelineSlug(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*pipelineruns.PipelineRun, error) {
	panic("FindLatestRunByPipelineSlug should not be called")
}

func (f *fakePipelineRunRepository) PreviousSuccess(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, time.Time) (*executionruntime.SuccessfulRun, error) {
	panic("unexpected PreviousSuccess")
}

func (f *fakePipelineRunRepository) BindWorkerJob(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string, int64) error {
	return errFakeDBTXMethodNotUsed
}

func (f *fakePipelineRunRepository) ListInterruptedForSystem(context.Context, postgres.DBTX, time.Time) ([]pipelineruns.InterruptedRun, error) {
	return nil, errFakeDBTXMethodNotUsed
}

func (f *fakePipelineRunRepository) LockRecoveryFence(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) error {
	return pipelineruns.ErrRunNotFound
}
