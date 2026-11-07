package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"

	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"

	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

const defaultProjectSlug = slug.Slug("default")

// testSinkEffectLedger supplies the same durable implementation used in production.
func testSinkEffectLedger(t *testing.T, txm postgres.TransactionManager) pipelinerun.SinkEffectLedger {
	t.Helper()

	ledger, err := pipelinesvc.NewSinkEffectLedger(txm, pipelinesinkeffects.New())
	require.NoError(t, err)
	return ledger
}

type ownershipGitSourceResolver struct {
	source       *pipelinegit.Source
	resolveCalls int
}

func (r *ownershipGitSourceResolver) Resolve(
	context.Context,
	slug.Slug,
	pipelinegit.SourceRequest,
) (*pipelinegit.Source, error) {
	r.resolveCalls++
	return r.source, nil
}

type ownershipRepoStore struct {
	existingPath string
	data         []byte
}

func (s ownershipRepoStore) ReadFile(_ context.Context, path string, maxBytes int64) ([]byte, error) {
	if path != s.existingPath {
		return nil, repostore.ErrFileNotFound.Detailf("%s", path)
	}
	if maxBytes > 0 && int64(len(s.data)) > maxBytes {
		return nil, xerror.ExecutionResponseSizeExceeded().Detailf(
			"file content exceeds %d byte limit",
			maxBytes,
		)
	}
	return s.data, nil
}

func TestOwnershipRepoStoreReadFileHonorsLimit(t *testing.T) {
	t.Parallel()

	store := ownershipRepoStore{existingPath: "pipelines/example.pipeline.yaml", data: []byte("content")}

	data, err := store.ReadFile(context.Background(), store.existingPath, int64(len(store.data))-1)
	require.Nil(t, data)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())

	data, err = store.ReadFile(context.Background(), "pipelines/missing.pipeline.yaml", int64(len(store.data)))
	require.Nil(t, data)
	require.ErrorIs(t, err, repostore.ErrFileNotFound)

	data, err = store.ReadFile(context.Background(), store.existingPath, int64(len(store.data)))
	require.NoError(t, err)
	require.Equal(t, store.data, data)
}

func (s ownershipRepoStore) WriteFile(context.Context, string, []byte, string) error {
	return nil
}

func (s ownershipRepoStore) DeleteFile(context.Context, string, string) error {
	return nil
}

func (s ownershipRepoStore) ListFiles(context.Context, string) ([]repostore.FileEntry, error) {
	return nil, nil
}

func (s ownershipRepoStore) FileExists(_ context.Context, path string) (bool, error) {
	return path == s.existingPath, nil
}

type noopQueryExecutor struct{}

func (noopQueryExecutor) FindBySlug(context.Context, slug.Slug, slug.Slug, slug.Slug) (*queriessvc.Query, error) {
	return nil, xerror.Unexpected().Detail("saved Workspace operations are not used by these tests")
}

func (noopQueryExecutor) Execute(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	map[string]any,
	...queriessvc.ExecuteOption,
) (*result.ExecutionResult, error) {
	return &result.ExecutionResult{}, nil
}

func (noopQueryExecutor) GetExpectedInputContract(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*payload.Contract, error) {
	return &payload.Contract{
		Name: "noop",
		Fields: []payload.ContractField{
			{Name: "id", Type: model.ContractFieldTypeValueString},
		},
	}, nil
}

func (noopQueryExecutor) ExecuteInline(
	context.Context,
	slug.Slug,
	slug.Slug,
	map[string]any,
	map[string]any,
) (*result.ExecutionResult, error) {
	return &result.ExecutionResult{}, nil
}

type noopPipelineRuntimeResolver struct{}

func (noopPipelineRuntimeResolver) ResolvePipelineDataSourceRefs(
	_ context.Context,
	_, _, _ slug.Slug,
	pipeline *pipelinespec.Pipeline,
) (*datasourcerefssvc.ResolvedPipeline, error) {
	return &datasourcerefssvc.ResolvedPipeline{Pipeline: pipeline}, nil
}

func (noopPipelineRuntimeResolver) SyncPipelineDataSourceRefs(
	context.Context,
	slug.Slug,
	slug.Slug,
	*pipelinespec.Pipeline,
	model.EventActor,
) (*datasourcerefssvc.PipelineDataSourceRefSyncResult, error) {
	return &datasourcerefssvc.PipelineDataSourceRefSyncResult{}, nil
}

func (noopPipelineRuntimeResolver) ReconcilePipelineDataSourceRefs(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	actor model.EventActor,
) (*datasourcerefssvc.PipelineDataSourceRefSyncResult, error) {
	return noopPipelineRuntimeResolver{}.SyncPipelineDataSourceRefs(ctx, orgSlug, projectSlug, pipeline, actor)
}

func (noopPipelineRuntimeResolver) DeleteDataSourceRefsForPipeline(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	model.EventActor,
) error {
	return nil
}

func (noopPipelineRuntimeResolver) ResolveRuntimeSecrets(
	context.Context,
	slug.Slug,
	slug.Slug,
) ([]model.RuntimeSecretValue, error) {
	return nil, nil
}

type noopPipelineGitProducer struct{}

type noopPipelineRunProducer struct{}

func (noopPipelineRunProducer) EnqueuePipelineRunExecution(
	context.Context,
	postgres.DBTX,
	jobs.PipelineRunExecutionArgs,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (noopPipelineGitProducer) EnqueueWriteFile(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*string,
	string,
	[]byte,
	string,
	bool,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

type noopProjectBackendService struct{}

func (noopProjectBackendService) GetProject(
	_ context.Context,
	_, projectSlug slug.Slug,
) (*model.Project, error) {
	return &model.Project{
		Slug: projectSlug,
		Name: projectSlug.String(),
		PipelineBackend: &model.ProjectPipelineBackend{
			DataSourceSlug: slug.Slug("pipeline-ownership-git"),
			TrackedBranch:  "main",
		},
	}, nil
}

func (noopPipelineGitProducer) EnqueueDeleteFile(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*string,
	string,
	string,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (noopPipelineGitProducer) EnqueuePublishProposal(
	context.Context,
	postgres.DBTX,
	jobs.PipelineGitPublishProposalArgs,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (noopPipelineGitProducer) EnqueueRefreshProposal(
	context.Context, postgres.DBTX, jobs.PipelineGitRefreshProposalArgs,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

type recordingPublishPipelineGitProducer struct {
	writeCalls   int
	publishCalls int
	publishArgs  jobs.PipelineGitPublishProposalArgs
}

func (p *recordingPublishPipelineGitProducer) EnqueueWriteFile(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*string,
	string,
	[]byte,
	string,
	bool,
) (queue.QueuedJob, error) {
	p.writeCalls++
	return queue.QueuedJob{
		Kind:  "pipeline_git_write_file",
		Queue: "pipeline_git",
		State: "queued",
	}, nil
}

func (recordingPublishPipelineGitProducer) EnqueueDeleteFile(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*string,
	string,
	string,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (p *recordingPublishPipelineGitProducer) EnqueuePublishProposal(
	_ context.Context,
	_ postgres.DBTX,
	args jobs.PipelineGitPublishProposalArgs,
) (queue.QueuedJob, error) {
	p.publishCalls++
	p.publishArgs = args
	return queue.QueuedJob{
		Kind:  "pipeline_git_publish_proposal",
		Queue: "pipeline_git",
		State: "queued",
	}, nil
}

func (p *recordingPublishPipelineGitProducer) EnqueueRefreshProposal(
	context.Context, postgres.DBTX, jobs.PipelineGitRefreshProposalArgs,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func TestPipelineServiceUpdateQueuesGitProposalWithoutChangingCanonical(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres pipeline Git proposal e2e test in short mode")
	}

	ctx := context.Background()
	pool := openE2EPool(t)
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)

	unique := time.Now().UnixNano()
	orgSlug := must.Slug(fmt.Sprintf("pipeline-ownership-org-%d", unique))
	memberSlug := must.Slug(fmt.Sprintf("pipeline-ownership-user-%d", unique))
	seedE2EOrg(t, pool, orgSlug)
	seedE2EMember(t, pool, orgSlug, memberSlug)

	pipelineSlug := must.Slug("pipeline-ownership-transfer")
	sourceDataSourceSlug := must.Slug("pipeline-ownership-git")
	seedE2EDataSource(t, pool, orgSlug, sourceDataSourceSlug)
	seedE2EProposalAuthority(t, pool, orgSlug, sourceDataSourceSlug)
	sourcePath := "pipelines/pipeline-ownership-transfer.pipeline.json"
	pipeline := ownershipPipeline(pipelineSlug)
	sourceData, err := json.Marshal(pipeline)
	require.NoError(t, err)
	sourceRepo := "github.com/Braidplane/acme-pipelines"
	sourceRef := "refs/heads/main"
	lastAppliedCommit := "abc123def456"
	gitResolver := &ownershipGitSourceResolver{
		source: &pipelinegit.Source{
			RepoStore:         ownershipRepoStore{existingPath: sourcePath, data: sourceData},
			DataSourceSlug:    sourceDataSourceSlug,
			SourceRepo:        &sourceRepo,
			SourceRef:         &sourceRef,
			LastAppliedCommit: &lastAppliedCommit,
			Cleanup:           func() {},
		},
	}
	dataSourceService := &fakes.DataSourceService{
		FindBySlugFn: func(
			_ context.Context,
			gotOrgSlug slug.Slug,
			gotDataSourceSlug slug.Slug,
		) (*datasourcessvc.DataSource, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, sourceDataSourceSlug, gotDataSourceSlug)
			return &datasourcessvc.DataSource{
				Slug:     gotDataSourceSlug,
				Type:     model.DataSourceTypeGit,
				IsActive: true,
			}, nil
		},
	}
	producer := &recordingPublishPipelineGitProducer{}

	svc, err := pipelinesvc.New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		store,
		pool,
		txm,
		pipelineruns.New(), incrementalcheckpoints.New(),
		pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		driftimpactrepo.New(),
		noopQueryExecutor{},
		allowAuthorizationService{},
		&fakes.AdmissionService{},
		dataSourceService,
		noopProjectBackendService{},
		gitResolver,
		clock.New(),
		noopPipelineRunProducer{},
		producer,
		eventtest.NewProducer(nil),
		noopPipelineRuntimeResolver{},
		testSinkEffectLedger(t, txm),
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	materializePipelines(t, ctx, store, orgSlug, defaultProjectSlug, pipeline)
	spec, err := store.FindSpec(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
	require.NoError(t, err)
	require.Equal(t, pipelinesrepo.SyncStatusSynced, spec.SyncStatus)
	require.Equal(t, &sourcePath, spec.SourcePath)
	require.Equal(t, &sourceRef, spec.SourceRef)
	require.NotNil(t, spec.LastAppliedCommit)

	updatedPipeline := ownershipPipeline(pipelineSlug)
	updatedPipeline.Metadata.Name = "Proposed Pipeline Name"
	updated, err := svc.Update(
		ctx,
		orgSlug,
		defaultProjectSlug,
		pipelineSlug,
		memberSlug,
		model.UserEventActor(memberSlug),
		updatedPipeline,
	)
	require.NoError(t, err)
	require.Equal(t, "Integration Test Pipeline", updated.Spec.Metadata.Name)
	require.Equal(t, 1, producer.publishCalls)
	require.Equal(t, pipelineSlug, producer.publishArgs.PipelineSlug)
	require.Equal(t, sourceDataSourceSlug, producer.publishArgs.DataSourceSlug)
	require.Equal(t, sourcePath, producer.publishArgs.Path)
	require.Contains(t, string(producer.publishArgs.Content), "Proposed Pipeline Name")

	canonical, err := store.Get(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
	require.NoError(t, err)
	require.Equal(t, "Integration Test Pipeline", canonical.Metadata.Name)
}

func TestPipelineServiceGitManagedDraftPublish_PersistsQueuedProposalStateWithoutChangingCanonicalSpec(
	t *testing.T,
) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres Git proposal draft publish e2e test in short mode")
	}

	ctx := context.Background()
	pool := openE2EPool(t)
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)

	unique := time.Now().UnixNano()
	orgSlug := must.Slug(fmt.Sprintf("pipeline-git-publish-org-%d", unique))
	memberSlug := must.Slug(fmt.Sprintf("pipeline-git-publish-user-%d", unique))
	seedE2EOrg(t, pool, orgSlug)
	seedE2EMember(t, pool, orgSlug, memberSlug)

	pipelineSlug := must.Slug("pipeline-git-publish")
	sourceDataSourceSlug := must.Slug("pipeline-git-publish-source")
	seedE2EDataSource(t, pool, orgSlug, sourceDataSourceSlug)
	seedE2EProposalAuthority(t, pool, orgSlug, sourceDataSourceSlug)

	pipeline := ownershipPipeline(pipelineSlug)
	sourcePath := "pipelines/pipeline-git-publish.pipeline.json"
	sourceData, err := json.Marshal(pipeline)
	require.NoError(t, err)

	sourceRepo := "github.com/Braidplane/acme-pipelines"
	sourceRef := "refs/heads/main"
	lastAppliedCommit := "abc123def456"
	gitResolver := &ownershipGitSourceResolver{
		source: &pipelinegit.Source{
			RepoStore:         ownershipRepoStore{existingPath: sourcePath, data: sourceData},
			DataSourceSlug:    sourceDataSourceSlug,
			SourceRepo:        &sourceRepo,
			SourceRef:         &sourceRef,
			LastAppliedCommit: &lastAppliedCommit,
		},
	}
	dataSourceService := &fakes.DataSourceService{
		FindBySlugFn: func(
			_ context.Context,
			gotOrgSlug slug.Slug,
			gotDataSourceSlug slug.Slug,
		) (*datasourcessvc.DataSource, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, sourceDataSourceSlug, gotDataSourceSlug)
			return &datasourcessvc.DataSource{
				Slug:     gotDataSourceSlug,
				Type:     model.DataSourceTypeGit,
				IsActive: true,
			}, nil
		},
	}
	projectBackendService := &fakes.ProjectsService{
		GetProjectFn: func(
			_ context.Context,
			gotOrgSlug slug.Slug,
			gotProjectSlug slug.Slug,
		) (*model.Project, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, defaultProjectSlug, gotProjectSlug)
			return &model.Project{
				Slug: defaultProjectSlug,
				PipelineBackend: &model.ProjectPipelineBackend{
					DataSourceSlug: sourceDataSourceSlug,
					Repo:           sourceRepo,
					TrackedBranch:  "main",
				},
			}, nil
		},
	}
	producer := &recordingPublishPipelineGitProducer{}

	svc, err := pipelinesvc.New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		store,
		pool,
		txm,
		pipelineruns.New(), incrementalcheckpoints.New(),
		pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		driftimpactrepo.New(),
		noopQueryExecutor{},
		allowAuthorizationService{},
		&fakes.AdmissionService{},
		dataSourceService,
		projectBackendService,
		gitResolver,
		clock.New(),
		noopPipelineRunProducer{},
		producer,
		eventtest.NewProducer(nil),
		noopPipelineRuntimeResolver{},
		testSinkEffectLedger(t, txm),
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	actor := model.UserEventActor(memberSlug)
	materializePipelines(t, ctx, store, orgSlug, defaultProjectSlug, pipeline)
	created, err := svc.Get(ctx, orgSlug, defaultProjectSlug, pipelineSlug, memberSlug)
	require.NoError(t, err)

	draftPipeline := ownershipPipeline(pipelineSlug)
	draftPipeline.Metadata.Name = "Draft Pipeline"
	draft, err := svc.SaveDraft(
		ctx,
		orgSlug,
		defaultProjectSlug,
		pipelineSlug,
		memberSlug,
		actor,
		draftPipeline,
		created.Summary.CurrentVersionSpecHash,
	)
	require.NoError(t, err)
	require.Equal(t, "Draft Pipeline", draft.Spec.Metadata.Name)

	beforePublish, err := store.FindSpec(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
	require.NoError(t, err)
	require.NotNil(t, beforePublish.CurrentVersionID)
	beforeVersionID := *beforePublish.CurrentVersionID
	beforeSpecHash := beforePublish.SpecHash
	beforeSpecJSON := append([]byte(nil), beforePublish.SpecJSONB...)

	published, err := svc.PublishDraft(
		ctx,
		orgSlug,
		defaultProjectSlug,
		pipelineSlug,
		memberSlug,
		actor,
		beforePublish.CurrentVersionSpecHash,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, "Integration Test Pipeline", published.Spec.Metadata.Name)
	require.NotNil(t, published.Draft)
	require.Equal(t, "Draft Pipeline", published.Draft.Spec.Metadata.Name)
	require.Equal(t, 1, producer.publishCalls)
	require.Equal(t, pipelineSlug, producer.publishArgs.PipelineSlug)
	require.NotEmpty(t, producer.publishArgs.PublishAttemptID)
	require.Equal(
		t,
		fmt.Sprintf("braidplane/default/%s/%s-%s", pipelineSlug, memberSlug, producer.publishArgs.PublishAttemptID),
		producer.publishArgs.BranchName,
	)

	var draftState string
	var baseRef string
	var branchName string
	var queuedAt time.Time
	err = pool.QueryRow(
		ctx,
		`
		SELECT d.draft_state, d.base_ref, d.branch_name, d.queued_at
		FROM pipeline_drafts d
		JOIN projects p ON p.id = d.project_id
		JOIN organizations o ON o.id = p.organization_id
		JOIN organization_members om ON om.id = d.member_id
		JOIN users u ON u.id = om.user_id
		WHERE o.slug = $1 AND p.slug = $2 AND d.pipeline_slug = $3 AND u.slug = $4
		`,
		orgSlug.String(),
		defaultProjectSlug.String(),
		pipelineSlug.String(),
		memberSlug.String(),
	).Scan(&draftState, &baseRef, &branchName, &queuedAt)
	require.NoError(t, err)
	require.Equal(t, "publish_queued", draftState)
	require.Equal(t, sourceRef, baseRef)
	require.Equal(t, producer.publishArgs.BranchName, branchName)
	require.False(t, queuedAt.IsZero())

	afterPublish, err := store.FindSpec(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
	require.NoError(t, err)
	require.NotNil(t, afterPublish.CurrentVersionID)
	require.Equal(t, beforeVersionID, *afterPublish.CurrentVersionID)
	require.Equal(t, beforeSpecHash, afterPublish.SpecHash)
	require.JSONEq(t, string(beforeSpecJSON), string(afterPublish.SpecJSONB))
}

func ownershipPipeline(pipelineSlug slug.Slug) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Integration Test Pipeline",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-data"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("test-ds"),
					OperationType:  "sql",
					OperationRef:   must.Slug("list-items"),
				},
			},
		},
	}
}

func openE2EPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connString := "postgres://postgres:postgres@localhost:35432/braidplane?sslmode=disable"
	if envConnString := os.Getenv("DATABASE_URL"); envConnString != "" {
		connString = envConnString
	}

	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("required real-Postgres pipeline ownership e2e test: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("required real-Postgres pipeline ownership e2e test: %v", err)
	}

	t.Cleanup(pool.Close)
	return pool
}

func seedE2EOrg(t *testing.T, pool *pgxpool.Pool, orgSlug slug.Slug) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(
		ctx,
		`INSERT INTO organizations (name, slug) VALUES ($1, $2)`,
		"Pipeline Ownership E2E Org",
		orgSlug.String(),
	)
	require.NoError(t, err)

	_, err = pool.Exec(
		ctx,
		`INSERT INTO projects (organization_id, name, slug)
		SELECT id, 'Default project', 'default'
		FROM organizations
		WHERE slug = $1`,
		orgSlug.String(),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()

		_, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug.String())
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug.String())
		require.NoError(t, cleanupErr)
	})
}

func seedE2EMember(t *testing.T, pool *pgxpool.Pool, orgSlug, memberSlug slug.Slug) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `
		WITH org_row AS (
			SELECT id FROM organizations WHERE slug = $1
		),
		user_row AS (
			INSERT INTO users (slug, email, full_name, role, locale)
			VALUES ($2, $3, 'Pipeline Ownership E2E User', 'user', 'en')
			RETURNING id
		),
		role_row AS (
			INSERT INTO roles (organization_id, slug, name, is_default)
			SELECT org_row.id, 'owner', 'Owner', true FROM org_row
			RETURNING id
		)
		INSERT INTO organization_members (organization_id, user_id, role_id)
		SELECT org_row.id, user_row.id, role_row.id
		FROM org_row, user_row, role_row
	`, orgSlug.String(), memberSlug.String(), fmt.Sprintf("%s@example.com", memberSlug))
	require.NoError(t, err)
}

func seedE2EDataSource(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, dataSourceSlug slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(
		ctx,
		`WITH credential AS (
			INSERT INTO credentials (
				organization_id,
				slug,
				kind,
				credential_type,
				status,
				encrypted_payload
			)
			SELECT id, $2 || '-credential', 'static_secret', $3, 'active', ''::bytea
			FROM organizations
			WHERE slug = $1
			RETURNING id, organization_id
		)
		INSERT INTO data_sources (
			organization_id,
			credential_id,
			name,
			slug,
			type,
			config,
			is_active
		)
		SELECT organization_id, id, 'Pipeline ownership git', $2, $3, '{}'::jsonb, true
		FROM credential`,
		orgSlug.String(),
		dataSourceSlug.String(),
		model.DataSourceTypeGit.String(),
	)
	require.NoError(t, err)
}

func seedE2EProposalAuthority(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, dataSourceSlug slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `
		WITH bound AS (
			UPDATE projects p
			SET pipeline_backend_data_source_id = ds.id
			FROM organizations o, data_sources ds
			WHERE p.organization_id = o.id
				AND ds.organization_id = o.id
				AND o.slug = $1
				AND p.slug = 'default'
				AND ds.slug = $2
			RETURNING o.id AS organization_id, p.id AS project_id, ds.id AS data_source_id, ds.credential_id
		)
		INSERT INTO pipeline_proposal_authorities (
			organization_id, project_id, data_source_id, credential_id, publication_mode,
			provider_host, provider_account_id, provider_repository_id, provider_credential_id,
			executing_principal_id, executing_principal_display, authority_source
		)
		SELECT organization_id, project_id, data_source_id, credential_id, 'organization_service',
			'github.com', 'braidplane', 'github.com/Braidplane/acme-pipelines', 'e2e-credential',
			'braidplane-service', 'Braidplane', 'organization service connection'
		FROM bound`, orgSlug.String(), dataSourceSlug.String())
	require.NoError(t, err)
}
