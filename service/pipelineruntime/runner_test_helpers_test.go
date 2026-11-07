package pipelineruntime

import (
	"context"
	"sync"
	"time"

	executionruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

type testQueryExecutorAdapter struct {
	*testutil.FakeQueryExecutor
	getExpectedInputContractFn func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
	) (*payload.Contract, error)
}

func (a *testQueryExecutorAdapter) GetExpectedInputContract(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
) (*payload.Contract, error) {
	if a == nil {
		return &payload.Contract{}, nil
	}
	if a.getExpectedInputContractFn != nil {
		return a.getExpectedInputContractFn(ctx, orgSlug, dsSlug, querySlug)
	}
	return &payload.Contract{}, nil
}

func adaptTestQueryExecutor(executor *testutil.FakeQueryExecutor) queryExecutor {
	if executor == nil {
		return nil
	}

	return &testQueryExecutorAdapter{FakeQueryExecutor: executor}
}

func newTestQueryExecutorAdapter(
	executor *testutil.FakeQueryExecutor,
	getExpectedInputContractFn func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
	) (*payload.Contract, error),
) queryExecutor {
	if executor == nil {
		executor = &testutil.FakeQueryExecutor{}
	}

	return &testQueryExecutorAdapter{
		FakeQueryExecutor:          executor,
		getExpectedInputContractFn: getExpectedInputContractFn,
	}
}

type noopRunStore struct {
	mu          sync.RWMutex
	timelines   map[string]pipelinerun.RunTimeline
	checkpoints map[string]pipelinerun.ReplayCheckpoint
}

func (s *noopRunStore) PersistRun(
	_ context.Context,
	_ slug.Slug,
	_ slug.Slug,
	_ slug.Slug,
	_ *pipelinespec.Pipeline,
	runResult *pipelinerun.RunResult,
) error {
	if runResult == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.timelines[runResult.Metadata.RunSlug] = runResult.Timeline
	return nil
}

func (s *noopRunStore) Get(_ context.Context, _, _, _ slug.Slug, runSlug string) (*pipelinerun.RunTimeline, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	timeline, ok := s.timelines[runSlug]
	if !ok {
		return nil, pipelinerun.ErrTimelineNotFound
	}

	return &timeline, nil
}

func (s *noopRunStore) SaveCheckpoint(
	_ context.Context,
	_ slug.Slug,
	_ slug.Slug,
	_ slug.Slug,
	runSlug string,
	checkpoint pipelinerun.ReplayCheckpoint,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoints[runSlug] = checkpoint
	return nil
}

func (s *noopRunStore) GetCheckpoint(
	_ context.Context,
	_ slug.Slug,
	_ slug.Slug,
	_ slug.Slug,
	runSlug string,
) (*pipelinerun.ReplayCheckpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	checkpoint, ok := s.checkpoints[runSlug]
	if !ok {
		return nil, pipelinerun.ErrReplayCheckpointNotFound
	}

	return &checkpoint, nil
}

func (s *noopRunStore) GetCurrentIncrementalCheckpoint(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	pipelinerun.IncrementalCheckpointLookup,
) (*pipelinerun.IncrementalCheckpoint, error) {
	return nil, pipelinerun.ErrIncrementalCheckpointNotFound
}

func NewNoopRunStore() pipelinerun.RunStore {
	return &noopRunStore{
		timelines:   make(map[string]pipelinerun.RunTimeline),
		checkpoints: make(map[string]pipelinerun.ReplayCheckpoint),
	}
}

type runnerNoopSinkRuntime struct{}

func (runnerNoopSinkRuntime) Execute(context.Context, *SinkRuntimeInput) error {
	return nil
}

type runnerTestDataSourceLookup struct {
	datasourcessvc.Service
}

func (runnerTestDataSourceLookup) FindBySlug(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
	return &datasourcessvc.DataSource{Type: model.DataSourceTypeHTTP}, nil
}

func mustNewRunnerWithStore(
	executor *testutil.FakeQueryExecutor,
	clk clock.Clock,
	store pipelinerun.RunStore,
	dataSourceService datasourcessvc.Service,
) *Runner {
	runner, err := NewRunner(
		adaptTestQueryExecutor(executor),
		clk,
		store,
		dataSourceService,
		runnerNoopSinkRuntime{},
	)
	if err != nil {
		panic(err)
	}

	return runner
}

func (s *noopRunStore) PreviousSuccess(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, time.Time) (*executionruntime.SuccessfulRun, error) {
	return nil, pipelinerun.ErrTimelineNotFound
}

func (s *noopRunStore) ObserveWorker(context.Context, slug.Slug, slug.Slug, slug.Slug, string, time.Time) error {
	return nil
}
