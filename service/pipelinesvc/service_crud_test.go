package pipelinesvc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"

	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

type fakeServicePipelineStore struct {
	resolveContractsFn         func(context.Context, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*pipelinespec.Pipeline, []model.PinnedODCSPort, error)
	findSpecCalls              int
	getCalls                   int
	resolveProposalAuthorityFn func(
		context.Context, slug.Slug, slug.Slug,
	) (model.PipelineProposalAuthority, error)

	findSpecFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) (*pipelinesrepo.PipelineSpec, error)
	findSpecForUpdateFn     func(ctx context.Context, orgSlug, projectSlug, pipelineSlug slug.Slug) (*pipelinesrepo.PipelineSpec, error)
	getFn                   func(ctx context.Context, orgSlug, projectSlug, pipelineSlug slug.Slug) (*pipelinespec.Pipeline, error)
	existsFn                func(ctx context.Context, orgSlug, projectSlug, pipelineSlug slug.Slug) (bool, error)
	listFn                  func(ctx context.Context, orgSlug, projectSlug slug.Slug, req model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error)
	findDraftFn             func(ctx context.Context, orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug) (*pipelinesrepo.PipelineDraft, error)
	saveDraftFn             func(ctx context.Context, orgSlug, projectSlug, memberSlug slug.Slug, pipeline *pipelinespec.Pipeline, basedOnSpecHash *string) error
	publishDraftFn          func(ctx context.Context, orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug, expectedSpecHash *string, force bool) error
	deleteDraftFn           func(ctx context.Context, orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug) error
	listVersionsFn          func(ctx context.Context, orgSlug, projectSlug, pipelineSlug slug.Slug) ([]*pipelinesrepo.PipelineVersion, error)
	findVersionBySpecHashFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		specHash string,
	) (*pipelinesrepo.PipelineVersion, error)
	findVersionBySpecHashPrefixFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		specHashPrefix string,
	) (*pipelinesrepo.PipelineVersion, error)
	deployFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params pipelinesrepo.DeployParams,
	) (*pipelinesrepo.PipelineDeployment, error)
	findDeploymentFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	) (*pipelinesrepo.PipelineDeployment, error)
	listDeploymentsFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) ([]*pipelinesrepo.PipelineDeployment, error)
	projectEnvironmentEnabledFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) (bool, error)
	projectEnvironmentProtectionFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) (*model.ProjectEnvironmentProtection, error)
	reconcileFromGitFn func(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		repoStore repostore.RepoStore,
		params ReconcileFromGitParams,
	) (*ReconcileFromGitResult, error)
	prepareGitDraftPublishFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		baseRef, expectedSpecHash *string,
		force bool,
	) (*GitDraftPublishRequest, error)
	prepareGitProposalRefreshFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	) (*GitProposalRefreshRequest, error)
}

func (s *fakeServicePipelineStore) ResolvePipelineContracts(ctx context.Context, org, project slug.Slug, p *pipelinespec.Pipeline) (*pipelinespec.Pipeline, []model.PinnedODCSPort, error) {
	if s.resolveContractsFn != nil {
		return s.resolveContractsFn(ctx, org, project, p)
	}
	return p, nil, nil
}

func (f *fakeServicePipelineStore) ResolveProposalAuthority(
	ctx context.Context, orgSlug, projectSlug slug.Slug,
) (model.PipelineProposalAuthority, error) {
	if f.resolveProposalAuthorityFn != nil {
		return f.resolveProposalAuthorityFn(ctx, orgSlug, projectSlug)
	}
	return testProposalAuthority(), nil
}

func testProposalAuthority() model.PipelineProposalAuthority {
	return model.PipelineProposalAuthority{
		ID:                        1,
		PublicationMode:           model.PipelineProposalModeOrganizationService,
		ProviderHost:              "github.com",
		ProviderAccountID:         "braidplane",
		ProviderRepositoryID:      "github.com/Braidplane/acme-pipelines",
		ProviderCredentialID:      "credential-1",
		ExecutingPrincipalID:      "braidplane-service",
		ExecutingPrincipalDisplay: "Braidplane",
		AuthoritySource:           "organization service connection",
	}
}

func (f *fakeServicePipelineStore) PrepareGitProposalRefresh(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) (*GitProposalRefreshRequest, error) {
	if f.prepareGitProposalRefreshFn != nil {
		return f.prepareGitProposalRefreshFn(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	}
	return nil, ErrPipelineDraftNotFound
}

func (f *fakeServicePipelineStore) CompleteProposalRefresh(context.Context, pipelinegit.ProposalRefreshCompletion) error {
	return nil
}

func (f *fakeServicePipelineStore) FindSpec(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineSpec, error) {
	f.findSpecCalls++
	if f.findSpecFn != nil {
		return f.findSpecFn(ctx, orgSlug, projectSlug, pipelineSlug)
	}
	return nil, ErrPipelineNotFound
}

func (f *fakeServicePipelineStore) Get(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*pipelinespec.Pipeline, error) {
	f.getCalls++
	if f.getFn != nil {
		return f.getFn(ctx, orgSlug, projectSlug, pipelineSlug)
	}
	return nil, ErrPipelineNotFound
}

func (f *fakeServicePipelineStore) FindSpecForUpdate(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineSpec, error) {
	if f.findSpecForUpdateFn != nil {
		return f.findSpecForUpdateFn(ctx, orgSlug, projectSlug, pipelineSlug)
	}
	return f.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
}

func (f *fakeServicePipelineStore) List(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	req model.ListRequest,
) ([]*pipelinesrepo.PipelineSpec, error) {
	if f.listFn != nil {
		return f.listFn(ctx, orgSlug, projectSlug, req)
	}
	return nil, nil
}

func (f *fakeServicePipelineStore) FindDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) (*pipelinesrepo.PipelineDraft, error) {
	if f.findDraftFn != nil {
		return f.findDraftFn(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	}

	return nil, ErrPipelineDraftNotFound
}

func (f *fakeServicePipelineStore) SaveDraft(
	ctx context.Context,
	orgSlug, projectSlug, memberSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	basedOnSpecHash *string,
) error {
	if f.saveDraftFn != nil {
		return f.saveDraftFn(ctx, orgSlug, projectSlug, memberSlug, pipeline, basedOnSpecHash)
	}

	return nil
}

func (f *fakeServicePipelineStore) PublishDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	expectedSpecHash *string,
	force bool,
) error {
	if f.publishDraftFn != nil {
		return f.publishDraftFn(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug, expectedSpecHash, force)
	}

	return nil
}

func (f *fakeServicePipelineStore) PrepareGitDraftPublish(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	baseRef, expectedSpecHash *string,
	force bool,
) (*GitDraftPublishRequest, error) {
	if f.prepareGitDraftPublishFn != nil {
		return f.prepareGitDraftPublishFn(
			ctx,
			orgSlug,
			projectSlug,
			pipelineSlug,
			memberSlug,
			baseRef,
			expectedSpecHash,
			force,
		)
	}
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeServicePipelineStore) CompleteProposal(
	context.Context,
	pipelinegit.ProposalCompletion,
) error {
	return nil
}

func (f *fakeServicePipelineStore) FailProposal(
	context.Context,
	pipelinegit.ProposalFailure,
) error {
	return nil
}

func (f *fakeServicePipelineStore) FailProposalRefresh(
	context.Context, pipelinegit.ProposalRefreshFailure,
) error {
	return nil
}

func (f *fakeServicePipelineStore) DeleteDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) error {
	if f.deleteDraftFn != nil {
		return f.deleteDraftFn(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	}

	return nil
}

func (f *fakeServicePipelineStore) Exists(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (bool, error) {
	if f.existsFn != nil {
		return f.existsFn(ctx, orgSlug, projectSlug, pipelineSlug)
	}
	return false, nil
}

func (f *fakeServicePipelineStore) ListVersions(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) ([]*pipelinesrepo.PipelineVersion, error) {
	if f.listVersionsFn != nil {
		return f.listVersionsFn(ctx, orgSlug, projectSlug, pipelineSlug)
	}
	return nil, nil
}

func (f *fakeServicePipelineStore) FindVersionBySpecHash(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHash string,
) (*pipelinesrepo.PipelineVersion, error) {
	if f.findVersionBySpecHashFn != nil {
		return f.findVersionBySpecHashFn(ctx, orgSlug, projectSlug, pipelineSlug, specHash)
	}
	return nil, ErrPipelineVersionNotFound
}

func (f *fakeServicePipelineStore) FindVersionBySpecHashPrefix(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHashPrefix string,
) (*pipelinesrepo.PipelineVersion, error) {
	if f.findVersionBySpecHashPrefixFn != nil {
		return f.findVersionBySpecHashPrefixFn(ctx, orgSlug, projectSlug, pipelineSlug, specHashPrefix)
	}
	return nil, ErrPipelineVersionNotFound
}

func (f *fakeServicePipelineStore) Deploy(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params pipelinesrepo.DeployParams,
) (*pipelinesrepo.PipelineDeployment, error) {
	if f.deployFn != nil {
		return f.deployFn(ctx, orgSlug, projectSlug, environmentSlug, params)
	}
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeServicePipelineStore) FindDeployment(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineDeployment, error) {
	if f.findDeploymentFn != nil {
		return f.findDeploymentFn(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug)
	}
	return nil, ErrPipelineDeploymentNotFound
}

func (f *fakeServicePipelineStore) ListDeployments(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) ([]*pipelinesrepo.PipelineDeployment, error) {
	if f.listDeploymentsFn != nil {
		return f.listDeploymentsFn(ctx, orgSlug, projectSlug, environmentSlug)
	}
	return nil, nil
}

func (f *fakeServicePipelineStore) ProjectEnvironmentEnabled(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (bool, error) {
	if f.projectEnvironmentEnabledFn != nil {
		return f.projectEnvironmentEnabledFn(ctx, orgSlug, projectSlug, environmentSlug)
	}
	return true, nil
}

func (f *fakeServicePipelineStore) ProjectEnvironmentProtection(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	if f.projectEnvironmentProtectionFn != nil {
		return f.projectEnvironmentProtectionFn(ctx, orgSlug, projectSlug, environmentSlug)
	}
	return &model.ProjectEnvironmentProtection{EnvironmentSlug: environmentSlug}, nil
}

func (f *fakeServicePipelineStore) ReconcileFromGit(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	repoStore repostore.RepoStore,
	params ReconcileFromGitParams,
) (*ReconcileFromGitResult, error) {
	if f.reconcileFromGitFn != nil {
		return f.reconcileFromGitFn(ctx, orgSlug, projectSlug, repoStore, params)
	}
	return &ReconcileFromGitResult{}, nil
}

func newCRUDTestService(t *testing.T, store PipelineStore) Service {
	t.Helper()

	return newCRUDTestServiceWithProducer(t, store, &recordingPipelineGitProducer{})
}

func newCRUDTestServiceWithProducer(
	t *testing.T,
	store PipelineStore,
	producer workerproducer.PipelineGitProducer,
) Service {
	t.Helper()

	return newCRUDTestServiceWithProducerAndGitSource(t, store, producer, fakeGitSourceResolver{})
}

func newCRUDTestServiceWithProducerAndGitSource(
	t *testing.T,
	store PipelineStore,
	producer workerproducer.PipelineGitProducer,
	gitSourceResolver pipelinegit.SourceResolver,
) Service {
	t.Helper()

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		store,
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		&fakeQueryExecutor{},
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{},
		fakeProjectBackendService{},
		gitSourceResolver,
		clock.New(),
		&recordingPipelineRunProducer{},
		producer,
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)
	return svc
}

func newCRUDTestServiceWithDataSourceRefResolver(
	t *testing.T,
	store PipelineStore,
	resolver pipelineRuntimeResolver,
) Service {
	t.Helper()
	if resolver == nil {
		resolver = &fakeDataSourceRefResolver{}
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		store,
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
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
		resolver,
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)
	return svc
}

type fakePipelineEventRecorder struct {
	calls []eventssvc.CreateEventParams
	err   error
}

func (f *fakePipelineEventRecorder) CreateEvent(
	_ context.Context,
	params eventssvc.CreateEventParams,
) (*model.Event, error) {
	f.calls = append(f.calls, params)
	if f.err != nil {
		return nil, f.err
	}
	return &model.Event{
		Slug:       params.Slug,
		ActorType:  params.ActorType,
		ActorSlug:  params.ActorSlug,
		EntityType: params.EntityType,
		EntitySlug: params.EntitySlug,
		Action:     params.Action,
		Metadata:   map[string]any{},
	}, nil
}

func newCRUDTestServiceWithEvents(
	t *testing.T,
	store PipelineStore,
	events *fakePipelineEventRecorder,
) Service {
	t.Helper()

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		store,
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
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
		eventtest.NewProducer(events),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)
	return svc
}

func assertPipelineEventWithMetadata(
	t *testing.T,
	got eventssvc.CreateEventParams,
	orgSlug, actorSlug, pipelineSlug slug.Slug,
	action model.EventAction,
	wantMetadata map[string]any,
) {
	t.Helper()

	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypePipeline, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, pipelineSlug, *got.EntitySlug)
	require.Equal(t, action, got.Action)
	require.Equal(t, wantMetadata, got.Metadata)
}

func testPipelineSpecWithSlug(pipelineSlug slug.Slug) *pipelinespec.Pipeline {
	pipeline := testPipelineSpec()
	pipeline.Metadata.Slug = pipelineSlug
	return pipeline
}

func TestService_UpdateGitAuthoredPipelineQueuesProposalWithoutSavingCanonicalSpec(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("default")
	actorSlug := must.Slug("alice")
	pipelineSlug := must.Slug("users-sync")
	sourcePath := "pipelines/users-sync.pipeline.yaml"
	sourceRef := "refs/heads/release"
	lastCommit := "abc123"
	pipeline := testPipelineSpecWithSlug(pipelineSlug)
	producer := &recordingPipelineGitProducer{}
	store := &fakeServicePipelineStore{}
	store.findSpecFn = func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
		return &pipelinesrepo.PipelineSpec{
			SpecJSONB:         must.Fn(json.Marshal(testPipelineSpecWithSlug(pipelineSlug))),
			Slug:              pipelineSlug,
			Name:              pipeline.Metadata.Name,
			SyncStatus:        pipelinesrepo.SyncStatusSynced,
			SourcePath:        &sourcePath,
			SourceRef:         &sourceRef,
			LastAppliedCommit: &lastCommit,
		}, nil
	}
	store.getFn = func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinespec.Pipeline, error) {
		return pipeline, nil
	}
	draftJSON, err := json.Marshal(pipeline)
	require.NoError(t, err)
	store.findDraftFn = func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineDraft, error) {
		return &pipelinesrepo.PipelineDraft{PipelineSlug: pipelineSlug, SpecJSONB: draftJSON}, nil
	}
	store.prepareGitDraftPublishFn = func(
		context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, *string, *string, bool,
	) (*GitDraftPublishRequest, error) {
		return &GitDraftPublishRequest{
			ProjectSlug:      projectSlug,
			PipelineSlug:     pipelineSlug,
			MemberSlug:       actorSlug,
			BaseRef:          &sourceRef,
			BaseCommitSHA:    &lastCommit,
			BranchName:       "braidplane/default/users-sync/alice",
			SourcePath:       sourcePath,
			Content:          []byte("metadata:\n  slug: users-sync\n"),
			PublishAttemptID: "attempt-1",
			Authority:        testProposalAuthority(),
		}, nil
	}

	svc := newCRUDTestServiceWithProducer(t, store, producer)
	_, err = svc.Update(
		context.Background(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		actorSlug,
		model.UserEventActor(actorSlug),
		pipeline,
	)

	require.NoError(t, err)
	require.Equal(t, orgSlug, producer.publishArgs.OrganizationSlug)
	require.Equal(t, projectSlug, producer.publishArgs.ProjectSlug)
	require.Equal(t, slug.Slug("pipeline-git"), producer.publishArgs.DataSourceSlug)
	require.Equal(t, pipelineSlug, producer.publishArgs.PipelineSlug)
	require.Equal(t, &sourceRef, producer.publishArgs.BaseRef)
	require.Equal(t, &lastCommit, producer.publishArgs.BaseCommitSHA)
	require.Equal(t, sourcePath, producer.publishArgs.Path)
	require.Equal(t, "braidplane/default/users-sync/alice", producer.publishArgs.BranchName)
	require.Equal(t, []byte("metadata:\n  slug: users-sync\n"), producer.publishArgs.Content)
	require.Equal(t, actorSlug, producer.publishArgs.MemberSlug)
	require.Equal(t, testProposalAuthority().Payload(), producer.publishArgs.Authority)
}

func TestService_UpdateRejectsAPIKeyBeforeSavingDraft(t *testing.T) {
	t.Parallel()

	saveCalls := 0
	store := &fakeServicePipelineStore{saveDraftFn: func(
		context.Context, slug.Slug, slug.Slug, slug.Slug, *pipelinespec.Pipeline, *string,
	) error {
		saveCalls++
		return nil
	}}
	pipelineSlug := must.Slug("users-sync")
	_, err := newCRUDTestService(t, store).Update(
		context.Background(),
		must.Slug("acme"),
		must.Slug("default"),
		pipelineSlug,
		must.Slug("alice"),
		model.APIKeyEventActor(must.Slug("automation")),
		testPipelineSpecWithSlug(pipelineSlug),
	)

	require.ErrorIs(t, err, ErrPipelineProposalAuthorityDenied)
	require.Zero(t, saveCalls)
}

func TestService_SaveAndDeleteDraftEmitPipelineUpdateEvents(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("default")
	actorSlug := must.Slug("alice")
	memberSlug := must.Slug("member-1")
	pipelineSlug := must.Slug("users-sync")
	baseSpecHash := "abc123"
	events := &fakePipelineEventRecorder{}
	store := &fakeServicePipelineStore{
		findDraftFn: func(
			context.Context,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			slug.Slug,
		) (*pipelinesrepo.PipelineDraft, error) {
			return &pipelinesrepo.PipelineDraft{
				PipelineSlug:     pipelineSlug,
				BasedOnSpecHash:  &baseSpecHash,
				BasedOnVersionID: int64Ptr(12),
				CurrentVersionID: int64Ptr(12),
				SpecJSONB:        mustMarshalPipelineForMergeTest(t, testPipelineSpecWithSlug(pipelineSlug)),
				CreatedAt:        time.Now().UTC(),
				UpdatedAt:        time.Now().UTC(),
			}, nil
		},
	}
	svc := newCRUDTestServiceWithEvents(t, store, events)

	_, err := svc.SaveDraft(
		context.Background(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		memberSlug,
		model.UserEventActor(actorSlug),
		testPipelineSpecWithSlug(pipelineSlug),
		&baseSpecHash,
	)
	require.NoError(t, err)
	require.NoError(t, svc.DeleteDraft(
		context.Background(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		memberSlug,
		model.UserEventActor(actorSlug),
	))

	require.Len(t, events.calls, 2)
	assertPipelineEventWithMetadata(
		t,
		events.calls[0],
		orgSlug,
		actorSlug,
		pipelineSlug,
		model.EventActionUpdate,
		map[string]any{
			"project_slug": projectSlug.String(),
			"mutation":     "draft_saved",
		},
	)
	assertPipelineEventWithMetadata(
		t,
		events.calls[1],
		orgSlug,
		actorSlug,
		pipelineSlug,
		model.EventActionUpdate,
		map[string]any{
			"project_slug": projectSlug.String(),
			"mutation":     "draft_deleted",
		},
	)
}

func TestService_PublishDraftEmitsGitProposalUpdateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("default")
	actorSlug := must.Slug("alice")
	memberSlug := actorSlug
	pipelineSlug := must.Slug("users-sync")
	sourcePath := "pipelines/users-sync.pipeline.yaml"
	baseSpecHash := "abc123"
	events := &fakePipelineEventRecorder{}
	store := &fakeServicePipelineStore{
		prepareGitDraftPublishFn: func(
			_ context.Context,
			gotOrgSlug, gotProjectSlug, gotPipelineSlug, gotMemberSlug slug.Slug,
			baseRef, expectedSpecHash *string,
			force bool,
		) (*GitDraftPublishRequest, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			require.Equal(t, pipelineSlug, gotPipelineSlug)
			require.Equal(t, memberSlug, gotMemberSlug)
			require.Equal(t, "refs/heads/main", *baseRef)
			require.Equal(t, *expectedSpecHash, baseSpecHash)
			require.False(t, force)
			return &GitDraftPublishRequest{
				ProjectSlug:   projectSlug,
				PipelineSlug:  pipelineSlug,
				MemberSlug:    memberSlug,
				SourcePath:    sourcePath,
				DraftSpecHash: "draft-spec-hash",
				Authority:     testProposalAuthority(),
			}, nil
		},
		findSpecFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
			return &pipelinesrepo.PipelineSpec{
				SpecJSONB:  must.Fn(json.Marshal(testPipelineSpecWithSlug(pipelineSlug))),
				Slug:       pipelineSlug,
				Name:       "Users Sync",
				SyncStatus: pipelinesrepo.SyncStatusSynced,
			}, nil
		},
		getFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinespec.Pipeline, error) {
			return testPipelineSpecWithSlug(pipelineSlug), nil
		},
	}
	svc := newCRUDTestServiceWithEvents(t, store, events)

	_, err := svc.PublishDraft(
		context.Background(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		memberSlug,
		model.UserEventActor(actorSlug),
		&baseSpecHash,
		false,
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)
	assertPipelineEventWithMetadata(
		t,
		events.calls[0],
		orgSlug,
		actorSlug,
		pipelineSlug,
		model.EventActionUpdate,
		map[string]any{
			"project_slug":        projectSlug.String(),
			"mutation":            "git_update_proposed",
			"source_path":         sourcePath,
			"publication_mode":    model.PipelineProposalModeOrganizationService,
			"provider_host":       "github.com",
			"provider_repository": "github.com/Braidplane/acme-pipelines",
			"provider_actor":      "Braidplane",
			"authority_source":    "organization service connection",
		},
	)
}

func TestService_PublishDraftRejectsDifferentInitiatingMember(t *testing.T) {
	t.Parallel()

	svc := newCRUDTestService(t, &fakeServicePipelineStore{})
	_, err := svc.PublishDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("users-sync"),
		slug.Slug("member-a"),
		model.UserEventActor(slug.Slug("member-b")),
		nil,
		false,
	)

	require.ErrorIs(t, err, ErrPipelineProposalAuthorityDenied)
}

func TestService_AuthorizeProposalPublishRejectsRevokedAuthority(t *testing.T) {
	t.Parallel()

	store := &fakeServicePipelineStore{
		resolveProposalAuthorityFn: func(
			context.Context, slug.Slug, slug.Slug,
		) (model.PipelineProposalAuthority, error) {
			return model.PipelineProposalAuthority{}, ErrPipelineProposalAuthorityDenied.Detail("revoked")
		},
	}
	svc := newCRUDTestService(t, store)
	err := svc.AuthorizeProposalPublish(context.Background(), jobs.PipelineGitPublishProposalArgs{
		OrganizationSlug: slug.Slug("acme"),
		ProjectSlug:      defaultProjectSlug,
		DataSourceSlug:   slug.Slug("pipeline-git"),
		PipelineSlug:     slug.Slug("users-sync"),
		MemberSlug:       slug.Slug("member-a"),
		Authority:        testProposalAuthority().Payload(),
	})

	require.ErrorIs(t, err, ErrPipelineProposalAuthorityDenied)
	require.ErrorContains(t, err, "revoked")
}

func TestService_AuthorizeProposalPublishRejectsTamperedTarget(t *testing.T) {
	t.Parallel()

	pipelineSlug := slug.Slug("users-sync")
	pipeline := testPipelineSpecWithSlug(pipelineSlug)
	draftJSON, err := json.Marshal(pipeline)
	require.NoError(t, err)
	sourcePath := "pipelines/users-sync.pipeline.json"
	content, err := renderPipelineSource(sourcePath, pipeline)
	require.NoError(t, err)
	baseRef := "refs/heads/main"
	branchName := "braidplane/default/users-sync/member-a"
	attemptID := "attempt-1"
	authorityID := testProposalAuthority().ID
	store := &fakeServicePipelineStore{
		findDraftFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineDraft, error) {
			return &pipelinesrepo.PipelineDraft{
				PipelineSlug:        pipelineSlug,
				SpecJSONB:           draftJSON,
				BaseRef:             &baseRef,
				BranchName:          &branchName,
				PublishAttemptID:    &attemptID,
				ProposalAuthorityID: &authorityID,
			}, nil
		},
		findSpecFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
			return &pipelinesrepo.PipelineSpec{SourcePath: &sourcePath}, nil
		},
	}
	svc := newCRUDTestService(t, store)
	args := jobs.PipelineGitPublishProposalArgs{
		OrganizationSlug: slug.Slug("acme"),
		ProjectSlug:      defaultProjectSlug,
		DataSourceSlug:   slug.Slug("pipeline-git"),
		PipelineSlug:     pipelineSlug,
		MemberSlug:       slug.Slug("member-a"),
		BaseRef:          &baseRef,
		BranchName:       branchName,
		Path:             sourcePath,
		Content:          content,
		DraftSpecHash:    fmt.Sprintf("%x", sha256.Sum256(draftJSON)),
		PublishAttemptID: attemptID,
		Authority:        testProposalAuthority().Payload(),
	}
	require.NoError(t, svc.AuthorizeProposalPublish(context.Background(), args))

	args.Content = []byte("tampered")
	err = svc.AuthorizeProposalPublish(context.Background(), args)
	require.ErrorIs(t, err, ErrPipelineProposalAuthorityDenied)
}

func TestService_RefreshDraftProposalQueuesExactProposal(t *testing.T) {
	t.Parallel()
	orgSlug, projectSlug := must.Slug("acme"), must.Slug("default")
	pipelineSlug, memberSlug := must.Slug("users-sync"), must.Slug("member-1")
	store := &fakeServicePipelineStore{
		prepareGitProposalRefreshFn: func(
			_ context.Context,
			_, _, _, _ slug.Slug,
		) (*GitProposalRefreshRequest, error) {
			return &GitProposalRefreshRequest{
				ProjectSlug: projectSlug, PipelineSlug: pipelineSlug, MemberSlug: memberSlug,
				ProposalID: "42", RefreshAttemptID: "refresh-42",
			}, nil
		},
		findSpecFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
			return &pipelinesrepo.PipelineSpec{SpecJSONB: must.Fn(json.Marshal(testPipelineSpecWithSlug(pipelineSlug))), Slug: pipelineSlug, Name: "Users Sync", SyncStatus: pipelinesrepo.SyncStatusSynced}, nil
		},
		getFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinespec.Pipeline, error) {
			return testPipelineSpecWithSlug(pipelineSlug), nil
		},
	}
	producer := &recordingPipelineGitProducer{}
	svc := newCRUDTestServiceWithProducer(t, store, producer)

	_, err := svc.RefreshDraftProposal(context.Background(), orgSlug, projectSlug, pipelineSlug, memberSlug,
		model.UserEventActor(must.Slug("alice")))

	require.NoError(t, err)
	require.Equal(t, "42", producer.refreshArgs.ProposalID)
	require.Equal(t, memberSlug, producer.refreshArgs.MemberSlug)
	require.Equal(t, "refresh-42", producer.refreshArgs.RefreshAttemptID)
}

func TestService_Get_UsesSingleSpecLookup(t *testing.T) {
	t.Parallel()

	store := &fakeServicePipelineStore{}
	currentVersionID := int64(13)
	store.findSpecFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
	) (*pipelinesrepo.PipelineSpec, error) {
		return &pipelinesrepo.PipelineSpec{
			SpecJSONB:        must.Fn(json.Marshal(testPipelineSpec())),
			Slug:             slug.Slug("acme-users-sync"),
			Name:             "ACME Users Sync",
			SyncStatus:       pipelinesrepo.SyncStatusSynced,
			CurrentVersionID: &currentVersionID,
		}, nil
	}

	svc := newCRUDTestService(t, store)

	detail, err := svc.Get(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
		"",
	)
	require.NoError(t, err)
	require.Equal(t, "ACME Users Sync", detail.Summary.Name)
	require.Equal(t, pipelinesrepo.SyncStatusSynced, detail.Summary.SyncStatus)
	require.Equal(t, &currentVersionID, detail.Summary.CurrentVersionID)
	require.Equal(t, 1, store.findSpecCalls)
	require.Equal(t, 0, store.getCalls)
	require.Equal(t, testPipelineSpec(), detail.Spec)
}

func TestService_Get_PropagatesGitSourceMetadata(t *testing.T) {
	t.Parallel()

	sourcePath := "pipelines/acme-users-sync.pipeline.yaml"
	sourceRef := "refs/heads/main"
	lastAppliedCommit := "abc123def456"

	store := &fakeServicePipelineStore{}
	store.findSpecFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
	) (*pipelinesrepo.PipelineSpec, error) {
		return &pipelinesrepo.PipelineSpec{
			SpecJSONB:         must.Fn(json.Marshal(testPipelineSpec())),
			Slug:              slug.Slug("acme-users-sync"),
			Name:              "ACME Users Sync",
			SyncStatus:        pipelinesrepo.SyncStatusDrifted,
			SourcePath:        &sourcePath,
			SourceRef:         &sourceRef,
			LastAppliedCommit: &lastAppliedCommit,
		}, nil
	}

	svc := newCRUDTestService(t, store)

	detail, err := svc.Get(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
		"",
	)
	require.NoError(t, err)
	require.Equal(t, &sourcePath, detail.Summary.SourcePath)
	require.Equal(t, &sourceRef, detail.Summary.SourceRef)
	require.Equal(t, &lastAppliedCommit, detail.Summary.LastAppliedCommit)
}

func TestService_Get_ReturnsMemberScopedDraftOnly(t *testing.T) {
	t.Parallel()

	pipeline := testPipelineSpec()
	specJSON := must.Fn(json.Marshal(pipeline))
	draftBasedOnVersionID := int64(12)
	currentVersionID := int64(13)

	store := &fakeServicePipelineStore{}
	store.findSpecFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
	) (*pipelinesrepo.PipelineSpec, error) {
		return &pipelinesrepo.PipelineSpec{
			SpecJSONB:  specJSON,
			Slug:       pipeline.Metadata.Slug,
			Name:       pipeline.Metadata.Name,
			SyncStatus: pipelinesrepo.SyncStatusSynced,
		}, nil
	}
	store.findDraftFn = func(
		_ context.Context,
		orgSlug, _, pipelineSlug, memberSlug slug.Slug,
	) (*pipelinesrepo.PipelineDraft, error) {
		require.Equal(t, slug.Slug("acme"), orgSlug)
		require.Equal(t, pipeline.Metadata.Slug, pipelineSlug)
		require.Equal(t, slug.Slug("member-a"), memberSlug)
		return &pipelinesrepo.PipelineDraft{
			PipelineSlug:     pipeline.Metadata.Slug,
			SpecJSONB:        specJSON,
			BasedOnVersionID: &draftBasedOnVersionID,
			CurrentVersionID: &currentVersionID,
		}, nil
	}

	svc := newCRUDTestService(t, store)
	detail, err := svc.Get(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		pipeline.Metadata.Slug,
		slug.Slug("member-a"),
	)

	require.NoError(t, err)
	require.NotNil(t, detail.Draft)
	require.True(t, detail.Summary.HasDraft)
	require.Equal(t, pipeline.Metadata.Name, detail.Draft.Spec.Metadata.Name)
	require.Equal(t, &draftBasedOnVersionID, detail.Draft.DraftBasedOnVersionID)
	require.Equal(t, &currentVersionID, detail.Draft.CurrentVersionID)
}

func TestService_List_SetsHasDraftForCurrentMemberOnly(t *testing.T) {
	t.Parallel()

	store := &fakeServicePipelineStore{}
	store.listFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
		model.ListRequest,
	) ([]*pipelinesrepo.PipelineSpec, error) {
		return []*pipelinesrepo.PipelineSpec{
			{Slug: slug.Slug("pipeline-with-draft"), Name: "Pipeline With Draft"},
			{Slug: slug.Slug("pipeline-without-draft"), Name: "Pipeline Without Draft"},
		}, nil
	}
	store.findDraftFn = func(
		_ context.Context,
		orgSlug, _, pipelineSlug, memberSlug slug.Slug,
	) (*pipelinesrepo.PipelineDraft, error) {
		require.Equal(t, slug.Slug("acme"), orgSlug)
		require.Equal(t, slug.Slug("member-a"), memberSlug)
		if pipelineSlug == slug.Slug("pipeline-with-draft") {
			return &pipelinesrepo.PipelineDraft{PipelineSlug: pipelineSlug}, nil
		}
		return nil, ErrPipelineDraftNotFound
	}

	svc := newCRUDTestService(t, store)
	response, err := svc.List(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-a"),
		model.ListRequest{},
	)

	require.NoError(t, err)
	require.Len(t, response.Items, 2)
	require.True(t, response.Items[0].HasDraft)
	require.False(t, response.Items[1].HasDraft)
	require.False(t, response.HasMore)
	require.Nil(t, response.NextCursor)
	require.Nil(t, response.PrevCursor)
}

func TestService_List_PaginatesBeforeDraftLookups(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 5, 12, 0, 0, 0, time.UTC)
	requestedDrafts := make([]slug.Slug, 0, 2)

	store := &fakeServicePipelineStore{}
	store.listFn = func(
		_ context.Context,
		orgSlug slug.Slug,
		_ slug.Slug,
		req model.ListRequest,
	) ([]*pipelinesrepo.PipelineSpec, error) {
		require.Equal(t, slug.Slug("acme"), orgSlug)
		require.Equal(t, model.ListRequest{
			Limit:     3,
			Cursor:    "cursor-1",
			SortBy:    model.ListSortCreatedAt,
			Order:     model.ListOrderDesc,
			Direction: model.ListDirectionNext,
		}, req)

		return []*pipelinesrepo.PipelineSpec{
			{
				Slug:      slug.Slug("pipeline-3"),
				Name:      "Pipeline 3",
				CreatedAt: now.Add(2 * time.Minute),
			},
			{
				Slug:      slug.Slug("pipeline-2"),
				Name:      "Pipeline 2",
				CreatedAt: now.Add(1 * time.Minute),
			},
			{
				Slug:      slug.Slug("pipeline-1"),
				Name:      "Pipeline 1",
				CreatedAt: now,
			},
		}, nil
	}
	store.findDraftFn = func(
		_ context.Context,
		orgSlug, _, pipelineSlug, memberSlug slug.Slug,
	) (*pipelinesrepo.PipelineDraft, error) {
		require.Equal(t, slug.Slug("acme"), orgSlug)
		require.Equal(t, slug.Slug("member-a"), memberSlug)
		requestedDrafts = append(requestedDrafts, pipelineSlug)
		return nil, ErrPipelineDraftNotFound
	}

	svc := newCRUDTestService(t, store)
	response, err := svc.List(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-a"),
		model.ListRequest{Limit: 2, Cursor: "cursor-1"},
	)

	require.NoError(t, err)
	require.Len(t, response.Items, 2)
	require.True(t, response.HasMore)
	require.Equal(t, []slug.Slug{slug.Slug("pipeline-3"), slug.Slug("pipeline-2")}, requestedDrafts)
	require.NotNil(t, response.NextCursor)
	require.Equal(t, "2026-03-05T12:01:00Z_pipeline-2", *response.NextCursor)
	require.NotNil(t, response.PrevCursor)
	require.Equal(t, "2026-03-05T12:02:00Z_pipeline-3", *response.PrevCursor)
}

func TestService_List_UsesNanosecondCursorPrecision(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.March, 5, 12, 0, 0, 0, time.UTC)
	store := &fakeServicePipelineStore{}
	store.listFn = func(
		_ context.Context,
		_ slug.Slug,
		_ slug.Slug,
		_ model.ListRequest,
	) ([]*pipelinesrepo.PipelineSpec, error) {
		return []*pipelinesrepo.PipelineSpec{
			{
				Slug:      slug.Slug("pipeline-newer"),
				Name:      "Pipeline Newer",
				CreatedAt: base.Add(900 * time.Millisecond),
			},
			{
				Slug:      slug.Slug("pipeline-older"),
				Name:      "Pipeline Older",
				CreatedAt: base.Add(100 * time.Millisecond),
			},
		}, nil
	}

	svc := newCRUDTestService(t, store)
	response, err := svc.List(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-a"),
		model.ListRequest{Limit: 1},
	)

	require.NoError(t, err)
	require.True(t, response.HasMore)
	require.NotNil(t, response.NextCursor)
	require.Equal(t, "2026-03-05T12:00:00.9Z_pipeline-newer", *response.NextCursor)
}

func TestService_List_PrevDirectionKeepsForwardNavigationOnNewestPage(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, time.March, 5, 12, 0, 0, 900000000, time.UTC)
	store := &fakeServicePipelineStore{}
	store.listFn = func(
		_ context.Context,
		orgSlug slug.Slug,
		_ slug.Slug,
		req model.ListRequest,
	) ([]*pipelinesrepo.PipelineSpec, error) {
		require.Equal(t, slug.Slug("acme"), orgSlug)
		require.Equal(t, model.ListRequest{
			Limit:     2,
			Cursor:    "2026-03-05T12:00:00.1Z_pipeline-older",
			SortBy:    model.ListSortCreatedAt,
			Order:     model.ListOrderAsc,
			Direction: model.ListDirectionPrev,
		}, req)

		return []*pipelinesrepo.PipelineSpec{{
			Slug:      slug.Slug("pipeline-newer"),
			Name:      "Pipeline Newer",
			CreatedAt: createdAt,
		}}, nil
	}

	svc := newCRUDTestService(t, store)
	response, err := svc.List(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-a"),
		model.ListRequest{
			Limit:     1,
			Cursor:    "2026-03-05T12:00:00.1Z_pipeline-older",
			Direction: model.ListDirectionPrev,
		},
	)

	require.NoError(t, err)
	require.Len(t, response.Items, 1)
	require.True(t, response.HasMore)
	require.NotNil(t, response.NextCursor)
	require.Equal(t, "2026-03-05T12:00:00.9Z_pipeline-newer", *response.NextCursor)
	require.Nil(t, response.PrevCursor)
}

func TestService_ListVersions_ReturnsVersionSummaries(t *testing.T) {
	t.Parallel()

	store := &fakeServicePipelineStore{}
	store.findSpecFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
	) (*pipelinesrepo.PipelineSpec, error) {
		return &pipelinesrepo.PipelineSpec{Slug: slug.Slug("acme-users-sync")}, nil
	}
	store.listVersionsFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
	) ([]*pipelinesrepo.PipelineVersion, error) {
		return []*pipelinesrepo.PipelineVersion{{
			ID:           12,
			PipelineSlug: slug.Slug("acme-users-sync"),
			SpecHash:     "abc123",
		}}, nil
	}

	svc := newCRUDTestService(t, store)

	versions, err := svc.ListVersions(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
	)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, "abc123", versions[0].SpecHash)
	require.Equal(t, slug.Slug("acme-users-sync"), versions[0].PipelineSlug)
}

func TestService_GetVersion_ReturnsDecodedSnapshot(t *testing.T) {
	t.Parallel()

	pipeline := testPipelineSpec()
	specJSON := must.Fn(json.Marshal(pipeline))

	store := &fakeServicePipelineStore{}
	store.findSpecFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
	) (*pipelinesrepo.PipelineSpec, error) {
		return &pipelinesrepo.PipelineSpec{Slug: slug.Slug("acme-users-sync")}, nil
	}
	store.findVersionBySpecHashFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		string,
	) (*pipelinesrepo.PipelineVersion, error) {
		return &pipelinesrepo.PipelineVersion{
			ID:           12,
			PipelineSlug: slug.Slug("acme-users-sync"),
			SpecJSONB:    specJSON,
			SpecHash:     "abc123",
		}, nil
	}

	svc := newCRUDTestService(t, store)

	version, err := svc.GetVersion(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
		"abc123",
	)
	require.NoError(t, err)
	require.Equal(t, "abc123", version.Summary.SpecHash)
	require.Equal(t, pipeline.Metadata.Slug, version.Spec.Metadata.Slug)
	require.Equal(t, pipeline.Metadata.Name, version.Spec.Metadata.Name)
}

func TestService_Update_ReturnsNotFoundWhenStoreReportsMissing(t *testing.T) {
	t.Parallel()

	store := &fakeServicePipelineStore{}
	store.findSpecFn = func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
		return nil, ErrPipelineNotFound
	}

	svc := newCRUDTestService(t, store)

	_, err := svc.Update(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
		slug.Slug("member-a"),
		model.UserEventActor(slug.Slug("member-a")),
		testPipelineSpec(),
	)
	require.ErrorIs(t, err, ErrPipelineNotFound)
}

func TestService_UpdateGitAuthoredPipelineRequiresSourcePath(t *testing.T) {
	t.Parallel()

	pipeline := testPipelineSpec()
	store := &fakeServicePipelineStore{
		findSpecFn: func(
			_ context.Context,
			orgSlug, _, pipelineSlug slug.Slug,
		) (*pipelinesrepo.PipelineSpec, error) {
			require.Equal(t, slug.Slug("acme"), orgSlug)
			require.Equal(t, pipeline.Metadata.Slug, pipelineSlug)
			return &pipelinesrepo.PipelineSpec{
				Slug:       pipelineSlug,
				SyncStatus: pipelinesrepo.SyncStatusSynced,
			}, nil
		},
	}

	svc := newCRUDTestService(t, store)
	_, err := svc.Update(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		pipeline.Metadata.Slug,
		slug.Slug("member-a"),
		model.UserEventActor(slug.Slug("member-a")),
		pipeline,
	)

	require.ErrorIs(t, err, ErrPipelineGitSourceRequired)
	require.Equal(t, 1, store.findSpecCalls)
}

func TestService_GetDraft_ReturnsDecodedDraft(t *testing.T) {
	t.Parallel()

	pipeline := testPipelineSpec()
	specJSON, err := json.Marshal(pipeline)
	require.NoError(t, err)
	draftState := pipelineDraftStateProposalOpen
	lastProposedHash := "older-draft-hash"
	proposalProvider := "github"
	proposalState := "open"
	proposalUpdatedAt := time.Date(2026, time.August, 13, 8, 30, 0, 0, time.UTC)

	store := &fakeServicePipelineStore{
		findDraftFn: func(
			_ context.Context,
			_, _, _, _ slug.Slug,
		) (*pipelinesrepo.PipelineDraft, error) {
			return &pipelinesrepo.PipelineDraft{
				PipelineSlug:          pipeline.Metadata.Slug,
				SpecJSONB:             specJSON,
				DraftState:            &draftState,
				ProposalProvider:      &proposalProvider,
				ProposalState:         &proposalState,
				LastProposedDraftHash: &lastProposedHash,
				ProposalUpdatedAt:     &proposalUpdatedAt,
			}, nil
		},
	}

	svc := newCRUDTestService(t, store)
	detail, err := svc.GetDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		pipeline.Metadata.Slug,
		slug.Slug("member-a"),
	)

	require.NoError(t, err)
	require.NotNil(t, detail)
	require.Equal(t, pipeline.Metadata.Slug, detail.PipelineSlug)
	require.Equal(t, pipeline.Metadata.Name, detail.Spec.Metadata.Name)
	require.Equal(t, pipelineDraftStateProposalOpen, detail.DraftState)
	require.Len(t, detail.DraftSpecHash, 64)
	require.Equal(t, "https://github.com/acme/pipelines.git", detail.Repository)
	require.Equal(t, proposalProvider, *detail.ProposalProvider)
	require.Equal(t, proposalState, *detail.ProposalState)
	require.True(t, detail.ProposalIsStale)
	require.Equal(t, proposalUpdatedAt, *detail.ProposalUpdatedAt)
}

func TestService_GetDraft_PropagatesNotFound(t *testing.T) {
	t.Parallel()

	store := &fakeServicePipelineStore{
		findDraftFn: func(
			_ context.Context,
			_, _, _, _ slug.Slug,
		) (*pipelinesrepo.PipelineDraft, error) {
			return nil, ErrPipelineDraftNotFound
		},
	}

	svc := newCRUDTestService(t, store)
	_, err := svc.GetDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
		slug.Slug("member-a"),
	)

	require.ErrorIs(t, err, ErrPipelineDraftNotFound)
}

func TestService_SaveDraft_RejectsNilPipeline(t *testing.T) {
	t.Parallel()

	svc := newCRUDTestService(t, &fakeServicePipelineStore{})
	_, err := svc.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
		slug.Slug("member-a"),
		model.UserEventActor(slug.Slug("member-a")),
		nil,
		nil,
	)

	require.Error(t, err)
}

func TestService_SaveDraft_RejectsSlugMismatch(t *testing.T) {
	t.Parallel()

	pipeline := testPipelineSpec()
	svc := newCRUDTestService(t, &fakeServicePipelineStore{})

	// pipeline.Metadata.Slug = "acme-users-sync" but pipelineSlug = "other-slug"
	_, err := svc.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("other-slug"),
		slug.Slug("member-a"),
		model.UserEventActor(slug.Slug("member-a")),
		pipeline,
		nil,
	)

	require.Error(t, err)
}

func TestService_SaveDraft_PropagatesPipelineNotFound(t *testing.T) {
	t.Parallel()

	pipeline := testPipelineSpec()
	store := &fakeServicePipelineStore{
		saveDraftFn: func(
			_ context.Context,
			_ slug.Slug,
			_ slug.Slug,
			_ slug.Slug,
			_ *pipelinespec.Pipeline,
			_ *string,
		) error {
			return ErrPipelineNotFound
		},
	}

	svc := newCRUDTestService(t, store)
	_, err := svc.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		pipeline.Metadata.Slug,
		slug.Slug("member-a"),
		model.UserEventActor(slug.Slug("member-a")),
		pipeline,
		nil,
	)

	require.ErrorIs(t, err, ErrPipelineNotFound)
}

func TestService_SaveDraft_ReturnsDraftDetailOnSuccess(t *testing.T) {
	t.Parallel()

	pipeline := testPipelineSpec()
	// Drafts preserve independent and incomplete steps until executable validation.
	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "query", Slug: "unfinished"})
	specJSON, marshalErr := json.Marshal(pipeline)
	require.NoError(t, marshalErr)

	store := &fakeServicePipelineStore{
		findSpecFn: func(_ context.Context, _, _, _ slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
			return &pipelinesrepo.PipelineSpec{Slug: pipeline.Metadata.Slug}, nil
		},
		findDraftFn: func(
			_ context.Context,
			_, _, _, _ slug.Slug,
		) (*pipelinesrepo.PipelineDraft, error) {
			return &pipelinesrepo.PipelineDraft{
				PipelineSlug: pipeline.Metadata.Slug,
				SpecJSONB:    specJSON,
			}, nil
		},
	}

	svc := newCRUDTestService(t, store)
	detail, err := svc.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		pipeline.Metadata.Slug,
		slug.Slug("member-a"),
		model.UserEventActor(slug.Slug("member-a")),
		pipeline,
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, detail)
	require.Equal(t, pipeline.Metadata.Slug, detail.PipelineSlug)
	require.Equal(t, pipeline, detail.Spec)
}

func TestService_DeleteDraft_DelegatesSuccess(t *testing.T) {
	t.Parallel()

	var called bool
	store := &fakeServicePipelineStore{
		deleteDraftFn: func(_ context.Context, _, _, _, _ slug.Slug) error {
			called = true
			return nil
		},
	}

	svc := newCRUDTestService(t, store)
	err := svc.DeleteDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
		slug.Slug("member-a"),
		model.UserEventActor(slug.Slug("member-a")),
	)

	require.NoError(t, err)
	require.True(t, called)
}

func TestService_DeleteDraft_PropagatesNotFound(t *testing.T) {
	t.Parallel()

	store := &fakeServicePipelineStore{
		deleteDraftFn: func(_ context.Context, _, _, _, _ slug.Slug) error {
			return ErrPipelineDraftNotFound
		},
	}

	svc := newCRUDTestService(t, store)
	err := svc.DeleteDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
		slug.Slug("member-a"),
		model.UserEventActor(slug.Slug("member-a")),
	)

	require.ErrorIs(t, err, ErrPipelineDraftNotFound)
}

func TestService_Get_RejectsInvalidCanonicalSpec(t *testing.T) {
	t.Parallel()
	store := &fakeServicePipelineStore{findSpecFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
		return &pipelinesrepo.PipelineSpec{SpecJSONB: []byte("invalid")}, nil
	}}
	detail, err := newCRUDTestService(t, store).Get(t.Context(), "acme", defaultProjectSlug, "pipeline", "")
	require.Error(t, err)
	require.Nil(t, detail)
	require.Equal(t, 1, store.findSpecCalls)
	require.Zero(t, store.getCalls)
}
