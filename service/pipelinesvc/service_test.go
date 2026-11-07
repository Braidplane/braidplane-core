package pipelinesvc

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	executionruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"

	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"

	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

var errFakeRepositoryMethodNotUsed = fmt.Errorf("fake repository method should not be used")

func testAdmissionService() *fakes.AdmissionService {
	return &fakes.AdmissionService{}
}

type fakeDBTX struct{}

func (f *fakeDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errFakeRepositoryMethodNotUsed
}

func (f *fakeDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	return fakeRow{}
}

type fakeRow struct{}

func (fakeRow) Scan(...any) error {
	return errFakeRepositoryMethodNotUsed
}

type fakeRunRepository struct {
	lockRecoveryFenceFn             func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) error
	listInterruptedFn               func(context.Context, postgres.DBTX, time.Time) ([]pipelineruns.InterruptedRun, error)
	inspectLogicalRunFn             func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.PipelineRunCompletion, error)
	listCompletionDecisionsFn       func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) ([]model.PipelineCompletionDecision, error)
	updateRunSourceEventCalls       int
	lastUpdateRunSourceEventRunSlug string
	lastUpdateRunSourceEventSlug    slug.Slug
	createRunFn                     func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params *pipelineruns.CreateRunParams,
	) (*pipelineruns.PipelineRun, error)
	findRunBySlugFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		runSlug string,
	) (*pipelineruns.PipelineRun, error)
	findLatestRunByPipelineSlugFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	) (*pipelineruns.PipelineRun, error)
	listRunsFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		filters *pipelineruns.RunListFilters,
		req model.ListRequest,
	) ([]*pipelineruns.PipelineRun, error)
	updateRunSourceEventFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		runSlug string,
		sourceEventSlug slug.Slug,
	) error
	markRunRunningFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		pipelineSlug slug.Slug,
		runSlug string,
		startedAt time.Time,
		timeline json.RawMessage,
	) (*pipelineruns.PipelineRun, error)
	completeRunFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		runSlug string,
		params *pipelineruns.CompleteRunParams,
	) (*pipelineruns.PipelineRun, error)
}

type fakeQueryExecutor struct {
	findBySlugFn func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*queriessvc.Query, error)
	executeFn    func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		actor model.EventActor,
		paramValues map[string]any,
	) (*result.ExecutionResult, error)
	executeWithOptionsFn func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		actor model.EventActor,
		paramValues map[string]any,
		opts ...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error)
	getExpectedInputContractFn func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
	) (*payload.Contract, error)
	executeInlineFn func(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		operationInline map[string]any,
		paramValues map[string]any,
	) (*result.ExecutionResult, error)
	executeInlineIncrementalFn func(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		operationInline map[string]any,
		paramValues map[string]any,
		checkpoint *pipelinerun.IncrementalCheckpointInput,
	) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
}

func (f *fakeQueryExecutor) FindBySlug(ctx context.Context, org, source, query slug.Slug) (*queriessvc.Query, error) {
	if f.findBySlugFn != nil {
		return f.findBySlugFn(ctx, org, source, query)
	}
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeQueryExecutor) Execute(
	ctx context.Context,
	orgSlug slug.Slug,
	dsSlug slug.Slug,
	querySlug slug.Slug,
	actor model.EventActor,
	paramValues map[string]any,
	opts ...queriessvc.ExecuteOption,
) (*result.ExecutionResult, error) {
	if f.executeWithOptionsFn != nil {
		return f.executeWithOptionsFn(ctx, orgSlug, dsSlug, querySlug, actor, paramValues, opts...)
	}

	if f.executeFn != nil {
		return f.executeFn(ctx, orgSlug, dsSlug, querySlug, actor, paramValues)
	}

	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeQueryExecutor) GetExpectedInputContract(
	ctx context.Context,
	orgSlug slug.Slug,
	dsSlug slug.Slug,
	querySlug slug.Slug,
) (*payload.Contract, error) {
	if f.getExpectedInputContractFn != nil {
		return f.getExpectedInputContractFn(ctx, orgSlug, dsSlug, querySlug)
	}

	return &payload.Contract{}, nil
}

func (f *fakeQueryExecutor) ExecuteInline(
	ctx context.Context,
	orgSlug slug.Slug,
	dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	if f.executeInlineFn != nil {
		return f.executeInlineFn(ctx, orgSlug, dsSlug, operationInline, paramValues)
	}

	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeQueryExecutor) ExecuteInlineIncremental(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	if f.executeInlineIncrementalFn != nil {
		return f.executeInlineIncrementalFn(ctx, orgSlug, dsSlug, operationInline, paramValues, checkpoint)
	}

	return nil, nil, errFakeRepositoryMethodNotUsed
}

type legacyQueryExecutor struct {
	executeFn func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		actor model.EventActor,
		paramValues map[string]any,
		opts ...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error)
	executeInlineFn func(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		operationInline map[string]any,
		paramValues map[string]any,
	) (*result.ExecutionResult, error)
}

func (f *legacyQueryExecutor) FindBySlug(context.Context, slug.Slug, slug.Slug, slug.Slug) (*queriessvc.Query, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *legacyQueryExecutor) Execute(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
	paramValues map[string]any,
	opts ...queriessvc.ExecuteOption,
) (*result.ExecutionResult, error) {
	if f.executeFn != nil {
		return f.executeFn(ctx, orgSlug, dsSlug, querySlug, actor, paramValues, opts...)
	}

	return nil, errFakeRepositoryMethodNotUsed
}

func (f *legacyQueryExecutor) GetExpectedInputContract(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*payload.Contract, error) {
	return &payload.Contract{}, nil
}

func (f *legacyQueryExecutor) ExecuteInline(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	if f.executeInlineFn != nil {
		return f.executeInlineFn(ctx, orgSlug, dsSlug, operationInline, paramValues)
	}

	return nil, errFakeRepositoryMethodNotUsed
}

func TestRunnerQueryExecutorAdapterExecuteInlineForwardsCall(t *testing.T) {
	t.Parallel()

	want := &result.ExecutionResult{}
	operationInline := map[string]any{"sql": "select 1"}
	paramValues := map[string]any{"limit": 10}
	executor := &fakeQueryExecutor{
		executeInlineFn: func(
			_ context.Context,
			orgSlug, dsSlug slug.Slug,
			gotOperationInline map[string]any,
			gotParamValues map[string]any,
		) (*result.ExecutionResult, error) {
			require.Equal(t, slug.Slug("acme"), orgSlug)
			require.Equal(t, slug.Slug("warehouse"), dsSlug)
			require.Equal(t, operationInline, gotOperationInline)
			require.Equal(t, paramValues, gotParamValues)
			return want, nil
		},
	}

	got, err := runnerQueryExecutorAdapter{executor: executor}.ExecuteInline(
		context.Background(),
		slug.Slug("acme"),
		slug.Slug("warehouse"),
		operationInline,
		paramValues,
	)

	require.NoError(t, err)
	require.Same(t, want, got)
}

func TestRunnerQueryExecutorAdapterExecuteInlineIncrementalForwardsCheckpointCall(t *testing.T) {
	t.Parallel()

	wantResult := &result.ExecutionResult{}
	wantObservation := &pipelinerun.IncrementalCheckpointObservation{
		ObservedUpperBound: json.RawMessage(`{"value":"next"}`),
	}
	checkpoint := &pipelinerun.IncrementalCheckpointInput{
		LowerBound: json.RawMessage(`{"value":"previous"}`),
	}
	operationInline := map[string]any{"sql": "select 1"}
	paramValues := map[string]any{"limit": 10}
	executor := &fakeQueryExecutor{
		executeInlineIncrementalFn: func(
			_ context.Context,
			orgSlug, dsSlug slug.Slug,
			gotOperationInline map[string]any,
			gotParamValues map[string]any,
			gotCheckpoint *pipelinerun.IncrementalCheckpointInput,
		) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
			require.Equal(t, slug.Slug("acme"), orgSlug)
			require.Equal(t, slug.Slug("warehouse"), dsSlug)
			require.Equal(t, operationInline, gotOperationInline)
			require.Equal(t, paramValues, gotParamValues)
			require.Same(t, checkpoint, gotCheckpoint)
			return wantResult, wantObservation, nil
		},
	}

	gotResult, gotObservation, err := runnerQueryExecutorAdapter{executor: executor}.ExecuteInlineIncremental(
		context.Background(),
		slug.Slug("acme"),
		slug.Slug("warehouse"),
		operationInline,
		paramValues,
		checkpoint,
	)

	require.NoError(t, err)
	require.Same(t, wantResult, gotResult)
	require.Same(t, wantObservation, gotObservation)
}

func TestRunnerQueryExecutorAdapterExecuteIncrementalSurfacesUnsupportedExecutor(t *testing.T) {
	t.Parallel()

	checkpoint := &pipelinerun.IncrementalCheckpointInput{
		LowerBound: json.RawMessage(`{"value":"previous"}`),
	}
	paramValues := map[string]any{"limit": 10}
	executor := &legacyQueryExecutor{}

	gotResult, gotObservation, err := runnerQueryExecutorAdapter{executor: executor}.ExecuteIncremental(
		context.Background(),
		slug.Slug("acme"),
		slug.Slug("warehouse"),
		must.Slug("list-users"),
		paramValues,
		checkpoint,
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "incremental query executor is required")
	require.Nil(t, gotResult)
	require.Nil(t, gotObservation)
}

func TestRunnerQueryExecutorAdapterExecuteInlineIncrementalSurfacesUnsupportedExecutor(t *testing.T) {
	t.Parallel()

	checkpoint := &pipelinerun.IncrementalCheckpointInput{
		LowerBound: json.RawMessage(`{"value":"previous"}`),
	}
	operationInline := map[string]any{"sql": "select 1"}
	paramValues := map[string]any{"limit": 10}
	executor := &legacyQueryExecutor{}

	gotResult, gotObservation, err := runnerQueryExecutorAdapter{executor: executor}.ExecuteInlineIncremental(
		context.Background(),
		slug.Slug("acme"),
		slug.Slug("warehouse"),
		operationInline,
		paramValues,
		checkpoint,
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "incremental inline query executor is required")
	require.Nil(t, gotResult)
	require.Nil(t, gotObservation)
}

type fakeRunStore struct{}

type directoryPipelineStore struct {
	root string
}

func (*directoryPipelineStore) ResolvePipelineContracts(_ context.Context, _, _ slug.Slug, p *pipelinespec.Pipeline) (*pipelinespec.Pipeline, []model.PinnedODCSPort, error) {
	return p, nil, nil
}

func (f *fakeRunStore) PersistRun(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*pipelinespec.Pipeline,
	*pipelinerun.RunResult,
) error {
	return nil
}

func (f *fakeRunStore) Get(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*pipelinerun.RunTimeline, error) {
	return nil, pipelinerun.ErrTimelineNotFound
}

func (f *fakeRunStore) SaveCheckpoint(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	string,
	pipelinerun.ReplayCheckpoint,
) error {
	return nil
}

func (f *fakeRunStore) GetCheckpoint(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	string,
) (*pipelinerun.ReplayCheckpoint, error) {
	return nil, pipelinerun.ErrReplayCheckpointNotFound
}

func (f *fakeRunStore) GetCurrentIncrementalCheckpoint(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	pipelinerun.IncrementalCheckpointLookup,
) (*pipelinerun.IncrementalCheckpoint, error) {
	return nil, pipelinerun.ErrIncrementalCheckpointNotFound
}

func newDirectoryPipelineStore(root string) PipelineStore {
	return &directoryPipelineStore{root: root}
}

func (*directoryPipelineStore) ResolveProposalAuthority(
	context.Context,
	slug.Slug,
	slug.Slug,
) (model.PipelineProposalAuthority, error) {
	return model.PipelineProposalAuthority{}, errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) FindSpec(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineSpec, error) {
	_, err := s.Get(ctx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		return nil, err
	}

	return &pipelinesrepo.PipelineSpec{
		Slug:                   pipelineSlug,
		SyncStatus:             pipelinesrepo.SyncStatusSynced,
		CurrentVersionID:       int64Ptr(1),
		CurrentVersionSpecHash: strPtr("example"),
	}, nil
}

func (s *directoryPipelineStore) FindSpecForUpdate(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineSpec, error) {
	return s.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
}

func (s *directoryPipelineStore) Get(
	ctx context.Context,
	_ slug.Slug,
	_ slug.Slug,
	pipelineSlug slug.Slug,
) (*pipelinespec.Pipeline, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var found *pipelinespec.Pipeline
	targetBasePrefix := pipelineSlug.String() + ".pipeline."
	walkErr := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !pipelinespec.IsCanonicalFile(path) {
			return nil
		}

		spec, loadErr := pipelinevalidate.LoadFile(path)
		if loadErr != nil {
			if strings.HasPrefix(filepath.Base(path), targetBasePrefix) {
				return loadErr
			}
			return nil
		}
		if spec.Metadata.Slug == pipelineSlug {
			found = spec
			return fs.SkipAll
		}

		return nil
	})
	if walkErr != nil && !xerror.Is(walkErr, fs.SkipAll) {
		return nil, walkErr
	}
	if found == nil {
		return nil, ErrPipelineNotFound
	}

	return found, nil
}

func (s *directoryPipelineStore) Save(
	context.Context,
	slug.Slug,
	slug.Slug,
	*pipelinespec.Pipeline,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) Delete(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) Restore(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) Purge(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) List(
	ctx context.Context,
	orgSlug slug.Slug,
	_ slug.Slug,
	_ model.ListRequest,
) ([]*pipelinesrepo.PipelineSpec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	specs := make([]*pipelinesrepo.PipelineSpec, 0)
	walkErr := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !pipelinespec.IsCanonicalFile(path) {
			return nil
		}

		spec, loadErr := pipelinevalidate.LoadFile(path)
		if loadErr != nil {
			return loadErr
		}
		specs = append(specs, &pipelinesrepo.PipelineSpec{
			Slug:       spec.Metadata.Slug,
			Name:       spec.Metadata.Name,
			SyncStatus: pipelinesrepo.SyncStatusSynced,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	return specs, nil
}

func (s *directoryPipelineStore) Exists(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (bool, error) {
	_, err := s.Get(ctx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		if xerror.Is(err, ErrPipelineNotFound) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (s *directoryPipelineStore) ReconcileFromGit(
	context.Context,
	slug.Slug,
	slug.Slug,
	repostore.RepoStore,
	ReconcileFromGitParams,
) (*ReconcileFromGitResult, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) ListVersions(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) ([]*pipelinesrepo.PipelineVersion, error) {
	if _, err := s.Get(ctx, orgSlug, projectSlug, pipelineSlug); err != nil {
		return nil, err
	}

	return []*pipelinesrepo.PipelineVersion{{
		ID:           1,
		PipelineSlug: pipelineSlug,
		CreatedAt:    time.Now().UTC(),
		SpecHash:     "example",
	}}, nil
}

func (s *directoryPipelineStore) FindVersionByID(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	versionID int64,
) (*pipelinesrepo.PipelineVersion, error) {
	pipeline, err := s.Get(ctx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		return nil, err
	}

	specJSON, err := json.Marshal(pipeline)
	if err != nil {
		return nil, err
	}

	return &pipelinesrepo.PipelineVersion{
		ID:           versionID,
		PipelineSlug: pipelineSlug,
		CreatedAt:    time.Now().UTC(),
		SpecJSONB:    specJSON,
		SpecHash:     "example",
	}, nil
}

func (s *directoryPipelineStore) FindVersionBySpecHash(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHash string,
) (*pipelinesrepo.PipelineVersion, error) {
	pipeline, err := s.Get(ctx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		return nil, err
	}

	specJSON, err := json.Marshal(pipeline)
	if err != nil {
		return nil, err
	}

	return &pipelinesrepo.PipelineVersion{
		ID:           1,
		PipelineSlug: pipelineSlug,
		CreatedAt:    time.Now().UTC(),
		SpecJSONB:    specJSON,
		SpecHash:     specHash,
	}, nil
}

func (s *directoryPipelineStore) FindVersionBySpecHashPrefix(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHashPrefix string,
) (*pipelinesrepo.PipelineVersion, error) {
	return s.FindVersionBySpecHash(ctx, orgSlug, projectSlug, pipelineSlug, specHashPrefix)
}

func (s *directoryPipelineStore) Deploy(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	pipelinesrepo.DeployParams,
) (*pipelinesrepo.PipelineDeployment, error) {
	return nil, ErrPipelineDeploymentNotFound
}

func (s *directoryPipelineStore) FindDeployment(
	ctx context.Context,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	environmentSlug slug.Slug,
	pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineDeployment, error) {
	spec, err := s.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		return nil, err
	}
	if spec.CurrentVersionID == nil || spec.CurrentVersionSpecHash == nil {
		return nil, ErrPipelineDeploymentNotFound
	}

	return &pipelinesrepo.PipelineDeployment{
		ProjectSlug:             projectSlug,
		EnvironmentSlug:         environmentSlug,
		PipelineSlug:            pipelineSlug,
		CurrentVersionID:        *spec.CurrentVersionID,
		CurrentVersionSpecHash:  *spec.CurrentVersionSpecHash,
		DeployedAt:              time.Time{},
		UpdatedAt:               time.Time{},
		DeployedByActorType:     model.EventActorTypeSystem.String(),
		DeployedByActorSlug:     nil,
		PreviousVersionID:       nil,
		PreviousVersionSpecHash: nil,
	}, nil
}

func (s *directoryPipelineStore) ListDeployments(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) ([]*pipelinesrepo.PipelineDeployment, error) {
	return nil, nil
}

func (s *directoryPipelineStore) ProjectEnvironmentEnabled(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (bool, error) {
	return false, nil
}

func (s *directoryPipelineStore) ProjectEnvironmentProtection(
	_ context.Context,
	_, _, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	return &model.ProjectEnvironmentProtection{EnvironmentSlug: environmentSlug}, nil
}

func (s *directoryPipelineStore) FindDraft(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*pipelinesrepo.PipelineDraft, error) {
	return nil, ErrPipelineDraftNotFound
}

func (s *directoryPipelineStore) SaveDraft(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*pipelinespec.Pipeline,
	*string,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) PublishDraft(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*string,
	bool,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) PrepareGitDraftPublish(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*string,
	*string,
	bool,
) (*GitDraftPublishRequest, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) CompleteProposal(
	context.Context,
	pipelinegit.ProposalCompletion,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) PrepareGitProposalRefresh(
	context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug,
) (*GitProposalRefreshRequest, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) CompleteProposalRefresh(
	context.Context, pipelinegit.ProposalRefreshCompletion,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *directoryPipelineStore) FailProposal(
	context.Context,
	pipelinegit.ProposalFailure,
) error {
	return nil
}

func (s *directoryPipelineStore) FailProposalRefresh(
	context.Context, pipelinegit.ProposalRefreshFailure,
) error {
	return nil
}

func (s *directoryPipelineStore) DeleteDraft(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) error {
	return errFakeRepositoryMethodNotUsed
}

type fakeDataSourceService struct {
	ConnectHTTPOAuthFn func(context.Context, slug.Slug, slug.Slug, model.EventActor) (*datasourcessvc.HTTPOAuthConnection, error)
	schemas            map[slug.Slug]datasourcessvc.SchemaResult
	findBySlugFn       func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error)
	findBySlugErr      error
}

func (*fakeDataSourceService) AttachOrganizationGitLabConnection(context.Context, slug.Slug, slug.Slug, model.EventActor) error {
	return fmt.Errorf("unexpected organization GitLab connection attachment")
}

func (f *fakeDataSourceService) CreateProvisioned(context.Context, slug.Slug, model.EventActor, *datasourcessvc.CreateParams, datasourcessvc.SourceProvisioner) (*datasourcessvc.DataSource, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) Create(
	context.Context,
	slug.Slug,
	model.EventActor,
	*datasourcessvc.CreateParams,
) (*datasourcessvc.DataSource, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) StartProviderDataSourceInstall(
	context.Context,
	slug.Slug,
	model.EventActor,
	string,
	*datasourcessvc.StartProviderDataSourceInstallParams,
) (*datasourcessvc.ProviderDataSourceInstall, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) ListProviderRepositories(
	context.Context,
	slug.Slug,
	slug.Slug,
) ([]datasourcessvc.ProviderRepository, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) SelectProviderRepository(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	datasourcessvc.SelectProviderRepositoryParams,
) (*datasourcessvc.DataSource, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) TestConnection(
	context.Context,
	slug.Slug,
	*datasourcessvc.TestConnectionParams,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) GetSnowflakeOnboarding(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.SnowflakeOnboarding, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) TestExistingConnection(
	context.Context,
	slug.Slug,
	slug.Slug,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) FindBySlug(
	ctx context.Context,
	orgSlug slug.Slug,
	dsSlug slug.Slug,
) (*datasourcessvc.DataSource, error) {
	if f.findBySlugFn != nil {
		return f.findBySlugFn(ctx, orgSlug, dsSlug)
	}
	if f.findBySlugErr != nil {
		return nil, f.findBySlugErr
	}
	if schema, ok := f.schemas[dsSlug]; ok {
		dsType, err := model.DataSourceTypeString(schema.VendorType())
		if err != nil {
			return nil, err
		}

		return &datasourcessvc.DataSource{
			Slug:   dsSlug,
			Type:   dsType,
			Schema: schema,
		}, nil
	}

	// Return a default non-read-only data source for slugs not explicitly
	// registered in schemas (e.g. sink data sources looked up for ReadOnly check).
	return &datasourcessvc.DataSource{Slug: dsSlug}, nil
}

func (f *fakeDataSourceService) ListByOrg(
	context.Context,
	slug.Slug,
	model.ListRequest,
) (model.ListResponse[datasourcessvc.DataSource], error) {
	return model.ListResponse[datasourcessvc.DataSource]{}, nil
}

func (f *fakeDataSourceService) Update(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	*datasourcessvc.UpdateParams,
) (*datasourcessvc.DataSource, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) UpdateCredentials(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	*datasourcessvc.UpdateCredentialsParams,
) (*datasourcessvc.DataSource, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) CompleteProviderOAuthCallback(
	context.Context,
	credentialssvc.CompleteOAuthCallbackParams,
) (credentialssvc.CompleteOAuthCallbackResult, error) {
	return credentialssvc.CompleteOAuthCallbackResult{}, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) ValidateCredentialLifecycle(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	model.EventAction,
) (*datasourcessvc.DataSource, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) RecordCredentialLifecycleEvent(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	model.EventAction,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) ListBranches(
	context.Context,
	slug.Slug,
	slug.Slug,
) ([]datasourcessvc.DataSourceBranch, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) IntrospectSchema(
	context.Context,
	slug.Slug,
	slug.Slug,
) (datasourcessvc.SchemaResult, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) RefreshSchema(
	_ context.Context,
	_ slug.Slug,
	_ slug.Slug,
	_ model.EventActor,
) (*datasourcessvc.SchemaImportResult, error) {
	return &datasourcessvc.SchemaImportResult{
		Status:   model.DataSourceSchemaImportStatusImporting,
		Enqueued: true,
	}, nil
}

func (f *fakeDataSourceService) ExecuteQueuedSchemaImport(context.Context, jobs.DataSourceSchemaImportArgs) (*datasourcessvc.SchemaImportResult, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) Delete(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) Restore(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) ResolveGitAuth(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.GitDataSourceAuth, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDataSourceService) ResolveGitExecutionAuth(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.GitExecutionAuth, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

type fakeGitSourceResolver struct {
	source *pipelinegit.Source
	err    error
}

type fakeProjectBackendService struct {
	project *model.Project
	err     error
}

func (f fakeProjectBackendService) GetProject(
	_ context.Context,
	_, projectSlug slug.Slug,
) (*model.Project, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.project != nil {
		return f.project, nil
	}
	return &model.Project{
		Slug: projectSlug,
		Name: projectSlug.String(),
		PipelineBackend: &model.ProjectPipelineBackend{
			DataSourceSlug: slug.Slug("pipeline-git"),
			Repo:           "https://github.com/acme/pipelines.git",
			TrackedBranch:  "main",
		},
	}, nil
}

func (f fakeGitSourceResolver) Resolve(
	context.Context,
	slug.Slug,
	pipelinegit.SourceRequest,
) (*pipelinegit.Source, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.source != nil {
		return f.source, nil
	}
	return &pipelinegit.Source{
		DataSourceSlug: slug.Slug("pipeline-git"),
		RepoStore: &fakeRepoStore{
			readFileFn: func(context.Context, string) ([]byte, error) {
				return nil, repostore.ErrFileNotFound
			},
		},
	}, nil
}

func newTestService(t *testing.T, repo *fakeRunRepository) Service {
	t.Helper()

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore("../../examples/pipelines"),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		repo, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		&fakeQueryExecutor{},
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	return svc
}

func (f *fakeRunRepository) CreateRun(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	environmentSlug slug.Slug,
	params *pipelineruns.CreateRunParams,
) (*pipelineruns.PipelineRun, error) {
	if f.createRunFn != nil {
		return f.createRunFn(ctx, tx, orgSlug, projectSlug, environmentSlug, params)
	}

	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeRunRepository) MarkRunRunning(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	pipelineSlug slug.Slug,
	runSlug string,
	startedAt time.Time,
	timeline json.RawMessage,
) (*pipelineruns.PipelineRun, error) {
	if f.markRunRunningFn != nil {
		return f.markRunRunningFn(ctx, tx, orgSlug, projectSlug, environmentSlug, pipelineSlug, runSlug, startedAt, timeline)
	}

	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeRunRepository) CompleteRun(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
	params *pipelineruns.CompleteRunParams,
) (*pipelineruns.PipelineRun, error) {
	if f.completeRunFn != nil {
		return f.completeRunFn(ctx, tx, orgSlug, projectSlug, environmentSlug, runSlug, params)
	}

	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeRunRepository) UpdateRunSourceEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
	sourceEventSlug slug.Slug,
) error {
	f.updateRunSourceEventCalls++
	f.lastUpdateRunSourceEventRunSlug = runSlug
	f.lastUpdateRunSourceEventSlug = sourceEventSlug
	if f.updateRunSourceEventFn != nil {
		return f.updateRunSourceEventFn(ctx, tx, orgSlug, projectSlug, environmentSlug, runSlug, sourceEventSlug)
	}

	return nil
}

func (f *fakeRunRepository) FindRunBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
) (*pipelineruns.PipelineRun, error) {
	if f.findRunBySlugFn != nil {
		return f.findRunBySlugFn(ctx, tx, orgSlug, projectSlug, environmentSlug, runSlug)
	}

	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeRunRepository) FindLatestRunByPipelineSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
) (*pipelineruns.PipelineRun, error) {
	if f.findLatestRunByPipelineSlugFn != nil {
		return f.findLatestRunByPipelineSlugFn(ctx, tx, orgSlug, projectSlug, environmentSlug, pipelineSlug)
	}

	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeRunRepository) ListRuns(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	filters *pipelineruns.RunListFilters,
	req model.ListRequest,
) ([]*pipelineruns.PipelineRun, error) {
	if f.listRunsFn != nil {
		return f.listRunsFn(ctx, tx, orgSlug, projectSlug, environmentSlug, filters, req)
	}

	return nil, errFakeRepositoryMethodNotUsed
}

func int64Ptr(v int64) *int64 {
	return &v
}

func strPtr(v string) *string {
	return &v
}

func TestNew_RequiresAllDependencies(t *testing.T) {
	t.Parallel()

	repo := &fakeRunRepository{}
	runStore := &fakeRunStore{}
	driftReports := &fakeDriftImpactRepository{}
	queryExec := &fakeQueryExecutor{}
	dsService := &fakeDataSourceService{}
	gitResolver := fakeGitSourceResolver{}

	testCases := []struct {
		name            string
		pipelineStore   PipelineStore
		dbtx            postgres.DBTX
		repo            pipelineruns.Repository
		runStore        pipelinerun.RunStore
		driftReports    driftimpactrepo.Repository
		queryExec       queryExecutor
		dsService       datasourcessvc.Service
		gitResolver     pipelinegit.SourceResolver
		runProducer     workerproducer.PipelineRunProducer
		eventProducer   workerproducer.EventProducer
		txm             postgres.TransactionManager
		omitCheckpoints bool
		omitResolver    bool
		omitAuthz       bool
		omitEntitlement bool
		omitLedger      bool
		omitProofLinks  bool
		wantErr         string
	}{
		{
			name:          "MissingPipelineStore",
			pipelineStore: nil,
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			wantErr:       "pipeline store is required",
		},
		{
			name:          "MissingDBTX",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          nil,
			repo:          repo,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			wantErr:       "dbtx is required",
		},
		{
			name:          "MissingRunRepository",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          nil,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			wantErr:       "run repository is required",
		},
		{
			name:            "MissingCheckpointRepository",
			pipelineStore:   newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:            &fakeDBTX{},
			repo:            repo,
			runStore:        runStore,
			driftReports:    driftReports,
			queryExec:       queryExec,
			dsService:       dsService,
			gitResolver:     gitResolver,
			runProducer:     &recordingPipelineRunProducer{},
			eventProducer:   eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:             testutil.FakeTransactionManager{},
			omitCheckpoints: true,
			wantErr:         "pipeline checkpoint is required",
		},
		{
			name:          "MissingRunStore",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      nil,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			wantErr:       "run store is required",
		},
		{
			name:          "MissingDriftImpactRepository",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      runStore,
			driftReports:  nil,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			wantErr:       "drift impact repository is required",
		},
		{
			name:          "MissingQueryExecutor",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     nil,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			wantErr:       "query executor is required",
		},
		{
			name:          "MissingDataSourceService",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     nil,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			wantErr:       "datasource service is required",
		},
		{
			name:          "MissingAuthorizationService",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			omitAuthz:     true,
			wantErr:       "authorization service is required",
		},
		{
			name:          "MissingGitSourceResolver",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   nil,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			wantErr:       "git source resolver is required",
		},
		{
			name:            "MissingAdmissionService",
			pipelineStore:   newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:            &fakeDBTX{},
			repo:            repo,
			runStore:        runStore,
			driftReports:    driftReports,
			queryExec:       queryExec,
			dsService:       dsService,
			gitResolver:     gitResolver,
			runProducer:     &recordingPipelineRunProducer{},
			eventProducer:   eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:             testutil.FakeTransactionManager{},
			omitEntitlement: true,
			wantErr:         "admission service is required",
		},
		{
			name:          "MissingPipelineRunProducer",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   nil,
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			wantErr:       "pipeline run producer is required",
		},
		{
			name:          "MissingEventProducer",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: nil,
			txm:           testutil.FakeTransactionManager{},
			wantErr:       "event producer is required",
		},
		{
			name:          "MissingTransactionManager",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           nil,
			wantErr:       "txm is required",
		},
		{
			name:          "MissingRuntimeResolver",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			omitResolver:  true,
			wantErr:       "pipeline runtime resolver is required",
		},
		{
			name:          "MissingSinkEffectLedger",
			pipelineStore: newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:          &fakeDBTX{},
			repo:          repo,
			runStore:      runStore,
			driftReports:  driftReports,
			queryExec:     queryExec,
			dsService:     dsService,
			gitResolver:   gitResolver,
			runProducer:   &recordingPipelineRunProducer{},
			eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:           testutil.FakeTransactionManager{},
			omitLedger:    true,
			wantErr:       "sink effect ledger is required",
		},
		{
			name:           "MissingProofLifecycleRepository",
			pipelineStore:  newDirectoryPipelineStore("../../examples/pipelines"),
			dbtx:           &fakeDBTX{},
			repo:           repo,
			runStore:       runStore,
			driftReports:   driftReports,
			queryExec:      queryExec,
			dsService:      dsService,
			gitResolver:    gitResolver,
			runProducer:    &recordingPipelineRunProducer{},
			eventProducer:  eventtest.NewProducer(&fakePipelineEventRecorder{}),
			txm:            testutil.FakeTransactionManager{},
			omitProofLinks: true,
			wantErr:        "proof lifecycle repository is required",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var authz authorization.Service = allowAuthorizationService{}
			if tc.omitAuthz {
				authz = nil
			}
			var admission admissionsvc.Service = testAdmissionService()
			if tc.omitEntitlement {
				admission = nil
			}
			checkpoints := incrementalcheckpoints.New()
			if tc.omitCheckpoints {
				checkpoints = nil
			}
			var resolver pipelineRuntimeResolver = &fakeDataSourceRefResolver{}
			if tc.omitResolver {
				resolver = nil
			}
			var ledger pipelinerun.SinkEffectLedger = noopSinkEffectLedger{}
			if tc.omitLedger {
				ledger = nil
			}
			proofLinks := newTestProofLinks()
			if tc.omitProofLinks {
				proofLinks = nil
			}
			_, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
				tc.pipelineStore,
				tc.dbtx,
				tc.txm,
				tc.repo, checkpoints,
				tc.runStore,
				tc.driftReports,
				tc.queryExec,
				authz,
				admission,
				tc.dsService,
				fakeProjectBackendService{},
				tc.gitResolver,
				clock.New(),
				tc.runProducer,
				&recordingPipelineGitProducer{},
				tc.eventProducer,
				resolver,
				ledger,
				proofLinks,
				testutil.FakeRunSucceededNotifier{})
			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestService_Execute_ReturnsExecutionFailedOnRunnerError(t *testing.T) {
	t.Parallel()

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore("../../examples/pipelines"),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		&fakeQueryExecutor{executeFn: func(
			_ context.Context,
			_ slug.Slug,
			_ slug.Slug,
			_ slug.Slug,
			_ model.EventActor,
			_ map[string]any,
		) (*result.ExecutionResult, error) {
			return nil, fmt.Errorf("execute boom")
		}},
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	orgSlug, err := slug.New("acme")
	require.NoError(t, err)
	pipelineSlug, err := slug.New("acme-users-sync")
	require.NoError(t, err)

	actor := model.UserEventActor(must.Slug("alice"))
	_, err = svc.Execute(
		context.Background(),
		orgSlug,
		defaultProjectSlug,
		must.Slug("prod"),
		pipelineSlug,
		nil,
		nil,
		actor,
		testRunSource(actor),
		nil,
	)
	require.ErrorIs(t, err, ErrPipelineExecutionFailed)
}

func TestService_Execute_ReplayFromCheckpoint(t *testing.T) {
	t.Parallel()

	t.Run("ReplayCheckpointSavesAndRetrieves", func(t *testing.T) {
		t.Parallel()

		// Verify that checkpoints can be saved and retrieved for replay scenarios
		runStore := &fakeRunStoreWithCheckpoint{
			checkpoints: make(map[string]*pipelinerun.ReplayCheckpoint),
		}

		ctx := context.Background()
		orgSlug := must.Slug("acme")
		runSlug := "run-001"
		stepSlug := must.Slug("fetch-users")

		checkpoint := pipelinerun.ReplayCheckpoint{
			RunSlug:  runSlug,
			OrgSlug:  orgSlug,
			StepSlug: stepSlug,
			Pipeline: nil, // Simplified test
			Results:  make(map[slug.Slug]*pipelinerun.StepResult),
		}

		// Save checkpoint
		err := runStore.SaveCheckpoint(ctx, orgSlug, defaultProjectSlug, must.Slug("prod"), runSlug, checkpoint)
		require.NoError(t, err)

		// Retrieve checkpoint
		retrieved, err := runStore.GetCheckpoint(ctx, orgSlug, defaultProjectSlug, must.Slug("prod"), runSlug)
		require.NoError(t, err)
		require.NotNil(t, retrieved)
		require.Equal(t, "run-001", retrieved.RunSlug)
		require.Equal(t, "fetch-users", retrieved.StepSlug.String())
	})
}

type fakeRunStoreWithCheckpoint struct {
	*fakeRunStore
	checkpoints map[string]*pipelinerun.ReplayCheckpoint
}

func (f *fakeRunStoreWithCheckpoint) SaveCheckpoint(
	_ context.Context,
	_ slug.Slug,
	_ slug.Slug,
	_ slug.Slug,
	runSlug string,
	cp pipelinerun.ReplayCheckpoint,
) error {
	f.checkpoints[runSlug] = &cp
	return nil
}

func (f *fakeRunStoreWithCheckpoint) GetCheckpoint(
	_ context.Context,
	_ slug.Slug,
	_ slug.Slug,
	_ slug.Slug,
	runSlug string,
) (*pipelinerun.ReplayCheckpoint, error) {
	if cp, ok := f.checkpoints[runSlug]; ok {
		return cp, nil
	}
	return nil, pipelinerun.ErrReplayCheckpointNotFound
}

func TestService_ListRuns(t *testing.T) {
	t.Parallel()

	t.Run("MapsFiltersAndBuildsPaginationResponse", func(t *testing.T) {
		t.Parallel()

		now := time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC)
		environmentSlug := must.Slug("prod")
		status := "failed"
		pipelineSlug := must.Slug("acme-users-sync")

		repo := &fakeRunRepository{
			listRunsFn: func(
				_ context.Context,
				_ postgres.DBTX,
				orgSlug slug.Slug,
				_ slug.Slug,
				_ slug.Slug,
				filters *pipelineruns.RunListFilters,
				req model.ListRequest,
			) ([]*pipelineruns.PipelineRun, error) {
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.NotNil(t, filters)
				require.NotNil(t, filters.Status)
				require.Equal(t, "failed", *filters.Status)
				require.NotNil(t, filters.PipelineSlug)
				require.Equal(t, "acme-users-sync", *filters.PipelineSlug)
				require.Equal(t, 3, req.Limit)
				require.Equal(t, model.ListSortCreatedAt, req.SortBy)
				require.Equal(t, model.ListOrderDesc, req.Order)

				return []*pipelineruns.PipelineRun{
					{
						EnvironmentSlug: "prod",
						PipelineSlug:    "acme-users-sync",
						RunSlug:         "run-003",
						Status:          "failed",
						CreatedAt:       now,
					},
					{
						EnvironmentSlug: "prod",
						PipelineSlug:    "acme-users-sync",
						RunSlug:         "run-002",
						Status:          "failed",
						CreatedAt:       now.Add(-time.Minute),
					},
					{
						EnvironmentSlug: "prod",
						PipelineSlug:    "acme-users-sync",
						RunSlug:         "run-001",
						Status:          "failed",
						CreatedAt:       now.Add(-2 * time.Minute),
					},
				}, nil
			},
		}

		svc := newTestService(t, repo)

		response, err := svc.ListRuns(
			context.Background(),
			must.Slug("acme"),
			defaultProjectSlug,
			environmentSlug,
			&RunFilters{Status: &status, PipelineSlug: &pipelineSlug},
			model.ListRequest{
				Limit:  2,
				SortBy: model.ListSortCreatedAt,
				Order:  model.ListOrderDesc,
			},
		)
		require.NoError(t, err)
		require.Len(t, response.Items, 2)
		require.True(t, response.HasMore)
		require.Equal(t, "prod", response.Items[0].EnvironmentSlug)
		require.Equal(t, "run-003", response.Items[0].RunSlug)
		require.Equal(t, "run-002", response.Items[1].RunSlug)
		require.NotNil(t, response.NextCursor)
		require.Equal(
			t,
			*model.EncodeCursor(now.Add(-time.Minute).Format(time.RFC3339Nano), "run-002"),
			*response.NextCursor,
		)
		require.Nil(t, response.PrevCursor)
	})

	t.Run("HandlesPreviousDirectionAndBuildsPrevCursor", func(t *testing.T) {
		t.Parallel()

		now := time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC)
		repo := &fakeRunRepository{
			listRunsFn: func(
				_ context.Context,
				_ postgres.DBTX,
				_ slug.Slug,
				_ slug.Slug,
				_ slug.Slug,
				_ *pipelineruns.RunListFilters,
				req model.ListRequest,
			) ([]*pipelineruns.PipelineRun, error) {
				require.Equal(t, model.ListDirectionPrev, req.Direction)
				require.Equal(t, model.ListOrderAsc, req.Order)
				require.Equal(t, 3, req.Limit)

				return []*pipelineruns.PipelineRun{
					{RunSlug: "run-001", Status: "succeeded", CreatedAt: now.Add(-2 * time.Minute)},
					{RunSlug: "run-002", Status: "succeeded", CreatedAt: now.Add(-time.Minute)},
					{RunSlug: "run-003", Status: "succeeded", CreatedAt: now},
				}, nil
			},
		}

		svc := newTestService(t, repo)

		response, err := svc.ListRuns(
			context.Background(),
			must.Slug("acme"),
			defaultProjectSlug,
			must.Slug("prod"),
			nil,
			model.ListRequest{
				Limit:     2,
				SortBy:    model.ListSortCreatedAt,
				Order:     model.ListOrderDesc,
				Direction: model.ListDirectionPrev,
				Cursor:    "2026-04-01T10:00:00Z_run-003",
			},
		)
		require.NoError(t, err)
		require.Len(t, response.Items, 2)
		require.Equal(t, "run-002", response.Items[0].RunSlug)
		require.Equal(t, "run-001", response.Items[1].RunSlug)
		require.NotNil(t, response.PrevCursor)
		require.NotNil(t, response.NextCursor)
	})

	t.Run("WrapsRepositoryErrors", func(t *testing.T) {
		t.Parallel()

		repo := &fakeRunRepository{
			listRunsFn: func(
				_ context.Context,
				_ postgres.DBTX,
				_ slug.Slug,
				_ slug.Slug,
				_ slug.Slug,
				_ *pipelineruns.RunListFilters,
				_ model.ListRequest,
			) ([]*pipelineruns.PipelineRun, error) {
				return nil, fmt.Errorf("db exploded")
			},
		}

		svc := newTestService(t, repo)
		_, err := svc.ListRuns(context.Background(), must.Slug("acme"), defaultProjectSlug, must.Slug("prod"), nil, model.ListRequest{})
		require.ErrorContains(t, err, "list pipeline runs")
	})

	t.Run("MapsIncrementalFiltersAndSummary", func(t *testing.T) {
		t.Parallel()

		mode := "backfill"
		backfillSeriesSlug := "series-001"
		repo := &fakeRunRepository{
			listRunsFn: func(
				_ context.Context,
				_ postgres.DBTX,
				_ slug.Slug,
				_ slug.Slug,
				_ slug.Slug,
				filters *pipelineruns.RunListFilters,
				_ model.ListRequest,
			) ([]*pipelineruns.PipelineRun, error) {
				require.NotNil(t, filters)
				require.NotNil(t, filters.Mode)
				require.Equal(t, "backfill", *filters.Mode)
				require.NotNil(t, filters.BackfillSeriesSlug)
				require.Equal(t, "series-001", *filters.BackfillSeriesSlug)

				now := time.Date(2026, time.May, 24, 11, 0, 0, 0, time.UTC)
				return []*pipelineruns.PipelineRun{{
					PipelineSlug: "users-sync",
					RunSlug:      "run-backfill-001",
					Status:       "succeeded",
					StartedAt:    now,
					CreatedAt:    now,
					Incremental: pipelinerun.IncrementalRunSummary{
						Mode:               "backfill",
						CheckpointStatus:   "skipped",
						SourceStepSlug:     "fetch-users",
						ReplaySafetyStatus: "safe",
						BackfillSeriesSlug: "series-001",
						BackfillChunkIndex: 1,
						BackfillChunkCount: 10,
					},
				}}, nil
			},
		}

		svc := newTestService(t, repo)
		response, err := svc.ListRuns(
			context.Background(),
			must.Slug("acme"),
			defaultProjectSlug,
			must.Slug("prod"),
			&RunFilters{
				Mode:               &mode,
				BackfillSeriesSlug: &backfillSeriesSlug,
			},
			model.ListRequest{},
		)
		require.NoError(t, err)
		require.Len(t, response.Items, 1)
		require.Equal(t, "backfill", response.Items[0].Incremental.Mode)
		require.Equal(t, "skipped", response.Items[0].Incremental.CheckpointStatus)
		require.Equal(t, "series-001", response.Items[0].Incremental.BackfillSeriesSlug)
		require.Equal(t, 1, response.Items[0].Incremental.BackfillChunkIndex)
		require.Equal(t, 10, response.Items[0].Incremental.BackfillChunkCount)
	})
}

func TestService_GetRun(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsRunDetailWithTimeline", func(t *testing.T) {
		t.Parallel()

		now := time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC)
		timelineJSON, err := json.Marshal(pipelinerun.RunTimeline{
			RunSlug:         "run-001",
			PipelineSlug:    must.Slug("acme-users-sync"),
			EnvironmentSlug: must.Slug("prod"),
			Status:          "failed",
			Events: []pipelinerun.TimelineEvent{{
				At:     now,
				Type:   "run_started",
				Status: "running",
			}},
		})
		require.NoError(t, err)

		repo := &fakeRunRepository{
			findRunBySlugFn: func(
				_ context.Context,
				_ postgres.DBTX,
				orgSlug slug.Slug,
				_ slug.Slug,
				_ slug.Slug,
				runSlug string,
			) (*pipelineruns.PipelineRun, error) {
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.Equal(t, "run-001", runSlug)
				return &pipelineruns.PipelineRun{
					APIVersion:      "braidplane.com/pipeline/v0alpha1",
					EnvironmentSlug: "prod",
					PipelineSlug:    "acme-users-sync",
					RunSlug:         "run-001",
					Status:          "failed",
					Timeline:        timelineJSON,
					CreatedAt:       now,
					StartedAt:       now,
				}, nil
			},
		}

		svc := newTestService(t, repo)
		run, err := svc.GetRun(context.Background(), must.Slug("acme"), defaultProjectSlug, must.Slug("prod"), "run-001")
		require.NoError(t, err)
		require.Equal(t, "prod", run.Metadata.EnvironmentSlug)
		require.Equal(t, "run-001", run.Metadata.RunSlug)
		require.Equal(t, "failed", run.Metadata.Status)
		require.Equal(t, "run_started", run.Timeline.Events[0].Type)
	})

	t.Run("ReturnsIncrementalDiagnostics", func(t *testing.T) {
		t.Parallel()

		now := time.Date(2026, time.May, 24, 11, 0, 0, 0, time.UTC)
		timelineJSON, err := json.Marshal(pipelinerun.RunTimeline{
			RunSlug:      "run-incremental-001",
			PipelineSlug: must.Slug("users-sync"),
			Status:       "succeeded",
			Events: []pipelinerun.TimelineEvent{
				{At: now, Type: "checkpoint_observed", Step: must.Slug("fetch-users"), Status: "observed"},
				{At: now, Type: "checkpoint_committed", Step: must.Slug("fetch-users"), Status: "committed"},
			},
		})
		require.NoError(t, err)

		repo := &fakeRunRepository{
			findRunBySlugFn: func(
				_ context.Context,
				_ postgres.DBTX,
				_ slug.Slug,
				_ slug.Slug,
				_ slug.Slug,
				runSlug string,
			) (*pipelineruns.PipelineRun, error) {
				require.Equal(t, "run-incremental-001", runSlug)
				return &pipelineruns.PipelineRun{
					APIVersion:   "braidplane.com/pipeline/v0alpha1",
					PipelineSlug: "users-sync",
					RunSlug:      "run-incremental-001",
					Status:       "succeeded",
					Timeline:     timelineJSON,
					CreatedAt:    now,
					StartedAt:    now,
					Incremental: pipelinerun.IncrementalRunSummary{
						Mode:               "incremental",
						CheckpointStatus:   "committed",
						SourceStepSlug:     "fetch-users",
						ReplaySafetyStatus: "safe",
					},
					IncrementalDiagnostics: pipelinerun.IncrementalRunDiagnostics{
						Sources: []pipelinerun.IncrementalSourceDiagnostic{{
							SourceStepSlug: "fetch-users",
							CheckpointKind: "watermark",
							PreviousCheckpoint: pipelinerun.IncrementalCheckpointSummary{
								Summary: "up to 10:00",
							},
							ObservedCheckpoint: pipelinerun.IncrementalCheckpointSummary{
								Summary: "up to 11:00",
							},
							CommitResult: pipelinerun.IncrementalCheckpointCommitResult{
								Status: "committed",
							},
						}},
					},
				}, nil
			},
		}

		svc := newTestService(t, repo)
		run, err := svc.GetRun(context.Background(), must.Slug("acme"), defaultProjectSlug, must.Slug("prod"), "run-incremental-001")
		require.NoError(t, err)
		require.Equal(t, "incremental", run.Metadata.Incremental.Mode)
		require.Equal(t, "committed", run.Metadata.Incremental.CheckpointStatus)
		require.Len(t, run.IncrementalDiagnostics.Sources, 1)
		require.Equal(t, "fetch-users", run.IncrementalDiagnostics.Sources[0].SourceStepSlug)
		require.Equal(t, "up to 10:00", run.IncrementalDiagnostics.Sources[0].PreviousCheckpoint.Summary)
		require.Equal(t, "up to 11:00", run.IncrementalDiagnostics.Sources[0].ObservedCheckpoint.Summary)
		require.Equal(t, "committed", run.IncrementalDiagnostics.Sources[0].CommitResult.Status)
	})

	t.Run("MapsRunNotFound", func(t *testing.T) {
		t.Parallel()

		repo := &fakeRunRepository{
			findRunBySlugFn: func(
				_ context.Context,
				_ postgres.DBTX,
				_ slug.Slug,
				_ slug.Slug,
				_ slug.Slug,
				_ string,
			) (*pipelineruns.PipelineRun, error) {
				return nil, pipelineruns.ErrRunNotFound
			},
		}

		svc := newTestService(t, repo)
		_, err := svc.GetRun(context.Background(), must.Slug("acme"), defaultProjectSlug, must.Slug("prod"), "missing-run")
		require.ErrorIs(t, err, ErrPipelineRunNotFound)
	})

	t.Run("ReturnsErrorWhenTimelineIsInvalid", func(t *testing.T) {
		t.Parallel()

		repo := &fakeRunRepository{
			findRunBySlugFn: func(
				_ context.Context,
				_ postgres.DBTX,
				_ slug.Slug,
				_ slug.Slug,
				_ slug.Slug,
				_ string,
			) (*pipelineruns.PipelineRun, error) {
				return &pipelineruns.PipelineRun{Timeline: []byte("{"), RunSlug: "run-001"}, nil
			},
		}

		svc := newTestService(t, repo)
		_, err := svc.GetRun(context.Background(), must.Slug("acme"), defaultProjectSlug, must.Slug("prod"), "run-001")
		require.ErrorContains(t, err, "decode pipeline run timeline")
	})

	t.Run("SuccessfulRetrievalWithFailureContext", func(t *testing.T) {
		t.Parallel()

		now := time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC)
		failureStep, err := slug.New("fetch-users")
		require.NoError(t, err)
		failureContext := pipelinerun.FailureContext{
			Step:       failureStep,
			Error:      "Query execution failed: connection timeout",
			At:         now,
			ReasonCode: pipelinerun.FailureReasonCodeDataSourcePermissionDenied,
		}
		timelineJSON, err := json.Marshal(pipelinerun.RunTimeline{
			RunSlug:      "run-failed-001",
			PipelineSlug: must.Slug("acme-users-sync"),
			Status:       "failed",
			Events: []pipelinerun.TimelineEvent{{
				At:     now,
				Type:   "step_failed",
				Status: "failed",
			}},
			Failure: &failureContext,
		})
		require.NoError(t, err)

		repo := &fakeRunRepository{
			findRunBySlugFn: func(
				_ context.Context,
				_ postgres.DBTX,
				orgSlug slug.Slug,
				_ slug.Slug,
				_ slug.Slug,
				runSlug string,
			) (*pipelineruns.PipelineRun, error) {
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.Equal(t, "run-failed-001", runSlug)
				return &pipelineruns.PipelineRun{
					APIVersion:   "braidplane.com/pipeline/v0alpha1",
					PipelineSlug: "acme-users-sync",
					RunSlug:      "run-failed-001",
					Status:       "failed",
					Timeline:     timelineJSON,
					CreatedAt:    now,
					StartedAt:    now,
				}, nil
			},
		}

		svc := newTestService(t, repo)
		run, err := svc.GetRun(context.Background(), must.Slug("acme"), defaultProjectSlug, must.Slug("prod"), "run-failed-001")
		require.NoError(t, err)
		require.Equal(t, "run-failed-001", run.Metadata.RunSlug)
		require.Equal(t, "failed", run.Metadata.Status)
		require.NotNil(t, run.Timeline.Failure)
		require.Equal(t, "Query execution failed: connection timeout", run.Timeline.Failure.Error)
		require.Equal(t, "fetch-users", run.Timeline.Failure.Step.String())
		require.Equal(t, now, run.Timeline.Failure.At)
	})
}

func TestPipelineFromJSONRejectsUnknownCanonicalFields(t *testing.T) {
	t.Parallel()

	_, err := pipelineFromJSON([]byte(`{
		"metadata": {"slug": "acme-users-sync", "name": "ACME Users Sync"},
		"steps": [],
		"unexpected": true
	}`))

	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown field")
}

func (*fakeDataSourceService) EnsureProofWebhook(context.Context, slug.Slug, slug.Slug) error {
	return fmt.Errorf("unexpected Proof webhook setup")
}

func (f *fakeDataSourceService) ListGoogleWorkspaceFiles(context.Context, slug.Slug, slug.Slug, workspace.ListOptions) (*workspace.Discovery, error) {
	return nil, fmt.Errorf("unexpected call to fakeDataSourceService.ListGoogleWorkspaceFiles")
}

func (f *fakeDataSourceService) VerifyGoogleWorkspaceFiles(context.Context, slug.Slug, slug.Slug, model.EventActor, datasourcessvc.WorkspaceVerificationParams) (*workspace.Discovery, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (f *fakeDataSourceService) GetGoogleWorkspaceFile(context.Context, slug.Slug, slug.Slug, string) (*workspace.File, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (f *fakeDataSourceService) GetGoogleWorkspaceWorksheets(context.Context, slug.Slug, slug.Slug, string) (*workspace.Metadata, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (*fakeDataSourceService) GetGoogleWorkspacePicker(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.WorkspacePicker, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (f *fakeDataSourceService) ConnectHTTPOAuth(ctx context.Context, org, source slug.Slug, actor model.EventActor) (*datasourcessvc.HTTPOAuthConnection, error) {
	if f.ConnectHTTPOAuthFn == nil {
		return nil, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuth call")
	}
	return f.ConnectHTTPOAuthFn(ctx, org, source, actor)
}

func TestExecutionBeforeRuntimeIsMarkedNotStarted(t *testing.T) {
	svc := &service{}
	_, err := svc.executeResolvedVersion(t.Context(), must.Slug("acme"), must.Slug("project"), must.Slug("local"), &pipelinesrepo.PipelineVersion{SpecJSONB: json.RawMessage(`{invalid`)}, nil, nil, model.SystemEventActor(), pipelinerun.RunSourceSummary{}, nil, false, nil, pipelinerun.IncrementalRunOptions{}, nil)
	require.ErrorIs(t, err, ErrPipelineExecutionNotStarted)
}

func (f *fakeRunRepository) InspectLogicalRun(ctx context.Context, db postgres.DBTX, org, project, environment, run slug.Slug) (*model.PipelineRunCompletion, error) {
	if f.inspectLogicalRunFn == nil {
		panic("unexpected logical run inspection")
	}
	return f.inspectLogicalRunFn(ctx, db, org, project, environment, run)
}

func (f *fakeRunRepository) ListCompletionDecisions(ctx context.Context, db postgres.DBTX, org, project, environment, run slug.Slug) ([]model.PipelineCompletionDecision, error) {
	if f.listCompletionDecisionsFn == nil {
		panic("unexpected completion decisions inspection")
	}
	return f.listCompletionDecisionsFn(ctx, db, org, project, environment, run)
}

func (s *fakeRunStore) PreviousSuccess(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, time.Time) (*executionruntime.SuccessfulRun, error) {
	return nil, pipelinerun.ErrTimelineNotFound
}

func (s *fakeRunRepository) PreviousSuccess(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, time.Time) (*executionruntime.SuccessfulRun, error) {
	panic("unexpected PreviousSuccess")
}

func (s *fakeRunStore) ObserveWorker(context.Context, slug.Slug, slug.Slug, slug.Slug, string, time.Time) error {
	return nil
}

func (f *fakeRunRepository) BindWorkerJob(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string, int64) error {
	return nil
}

func (f *fakeRunRepository) LockRecoveryFence(ctx context.Context, db postgres.DBTX, org, project, environment slug.Slug, run string) error {
	if f.lockRecoveryFenceFn != nil {
		return f.lockRecoveryFenceFn(ctx, db, org, project, environment, run)
	}
	return pipelineruns.ErrRunNotFound
}

func (f *fakeRunRepository) ListInterruptedForSystem(ctx context.Context, db postgres.DBTX, cutoff time.Time) ([]pipelineruns.InterruptedRun, error) {
	if f.listInterruptedFn != nil {
		return f.listInterruptedFn(ctx, db, cutoff)
	}
	return nil, nil
}
