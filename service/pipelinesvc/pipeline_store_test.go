package pipelinesvc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
)

var (
	defaultProjectSlug = slug.Slug("default")
	// noEnvironmentSlug marks validation paths that intentionally bypass env-aware
	// data source ref resolution.
	noEnvironmentSlug = slug.Slug("")
)

// fakePipelineReferenceRepo implements pipelinesrepo.Repository for unit tests.
type fakePipelineReferenceRepo struct {
	upsertFn                      func(ctx context.Context, orgSlug slug.Slug, params pipelinesrepo.UpsertParams) (*pipelinesrepo.PipelineReference, error)
	deleteFn                      func(ctx context.Context, orgSlug, pipelineSlug slug.Slug) error
	setHasDraftFn                 func(ctx context.Context, orgSlug, pipelineSlug slug.Slug, hasDraft bool) error
	upsertDraftFn                 func(ctx context.Context, orgSlug, memberSlug slug.Slug, params pipelinesrepo.UpsertDraftParams) (*pipelinesrepo.PipelineDraft, error)
	findDraftFn                   func(ctx context.Context, orgSlug, pipelineSlug, memberSlug slug.Slug) (*pipelinesrepo.PipelineDraft, error)
	findAnyDraftFn                func(ctx context.Context, orgSlug, pipelineSlug slug.Slug) (*pipelinesrepo.PipelineDraft, error)
	queueDraftPublishFn           func(ctx context.Context, orgSlug slug.Slug, params pipelinesrepo.QueueDraftPublishParams) error
	completeDraftPublishFn        func(ctx context.Context, orgSlug slug.Slug, params pipelinesrepo.CompleteDraftPublishParams) error
	failDraftPublishFn            func(ctx context.Context, orgSlug slug.Slug, params pipelinesrepo.FailDraftPublishParams) error
	queueDraftProposalRefreshFn   func(ctx context.Context, orgSlug slug.Slug, params pipelinesrepo.QueueDraftProposalRefreshParams) error
	refreshDraftProposalFn        func(ctx context.Context, orgSlug slug.Slug, params pipelinesrepo.RefreshDraftProposalParams) error
	failDraftProposalRefreshFn    func(ctx context.Context, orgSlug slug.Slug, params pipelinesrepo.FailDraftProposalRefreshParams) error
	deleteDraftFn                 func(ctx context.Context, orgSlug, pipelineSlug, memberSlug slug.Slug) error
	upsertSpecFn                  func(ctx context.Context, orgSlug slug.Slug, params pipelinesrepo.UpsertSpecParams) (*pipelinesrepo.PipelineSpec, error)
	findSpecFn                    func(ctx context.Context, orgSlug, pipelineSlug slug.Slug) (*pipelinesrepo.PipelineSpec, error)
	findSpecForUpdateFn           func(ctx context.Context, orgSlug, pipelineSlug slug.Slug) (*pipelinesrepo.PipelineSpec, error)
	listSpecsFn                   func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error)
	listSpecsTxFn                 func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, req model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error)
	deleteSpecFn                  func(ctx context.Context, orgSlug, pipelineSlug, deletedBy slug.Slug) error
	restoreSpecFn                 func(ctx context.Context, orgSlug, pipelineSlug slug.Slug) error
	findSpecDeletedAtFn           func(ctx context.Context, orgSlug, pipelineSlug slug.Slug) (time.Time, bool, error)
	createVersionFn               func(ctx context.Context, orgSlug slug.Slug, params pipelinesrepo.CreateVersionParams) (*pipelinesrepo.PipelineVersion, error)
	findVersionBySpecHashFn       func(ctx context.Context, orgSlug, pipelineSlug slug.Slug, specHash string) (*pipelinesrepo.PipelineVersion, error)
	findVersionBySpecHashPrefixFn func(
		ctx context.Context,
		orgSlug, pipelineSlug slug.Slug,
		specHashPrefix string,
	) (*pipelinesrepo.PipelineVersion, error)
	findVersionByCommitSHAFn       func(ctx context.Context, orgSlug, pipelineSlug slug.Slug, commitSHA string) (*pipelinesrepo.PipelineVersion, error)
	findVersionByIDFn              func(ctx context.Context, orgSlug, pipelineSlug slug.Slug, versionID int64) (*pipelinesrepo.PipelineVersion, error)
	listVersionsFn                 func(ctx context.Context, orgSlug, pipelineSlug slug.Slug) ([]*pipelinesrepo.PipelineVersion, error)
	setCurrentVersionFn            func(ctx context.Context, orgSlug, pipelineSlug slug.Slug, versionID int64) error
	deployFn                       func(ctx context.Context, orgSlug, projectSlug, environmentSlug slug.Slug, params pipelinesrepo.DeployParams) (*pipelinesrepo.PipelineDeployment, error)
	findDeploymentFn               func(ctx context.Context, orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug) (*pipelinesrepo.PipelineDeployment, error)
	listDeploymentsFn              func(ctx context.Context, orgSlug, projectSlug, environmentSlug slug.Slug) ([]*pipelinesrepo.PipelineDeployment, error)
	projectEnvironmentEnabledFn    func(ctx context.Context, orgSlug, projectSlug, environmentSlug slug.Slug) (bool, error)
	projectEnvironmentProtectionFn func(ctx context.Context, orgSlug, projectSlug, environmentSlug slug.Slug) (*model.ProjectEnvironmentProtection, error)
}

func (*fakePipelineReferenceRepo) FindProposalAuthority(
	context.Context, postgres.DBTX, slug.Slug, slug.Slug,
) (*pipelinesrepo.ProposalAuthority, error) {
	authority := testProposalAuthority()
	return &pipelinesrepo.ProposalAuthority{
		ID:                        authority.ID,
		PublicationMode:           authority.PublicationMode,
		ProviderHost:              authority.ProviderHost,
		ProviderAccountID:         authority.ProviderAccountID,
		ProviderRepositoryID:      authority.ProviderRepositoryID,
		ProviderCredentialID:      authority.ProviderCredentialID,
		ExecutingPrincipalID:      authority.ExecutingPrincipalID,
		ExecutingPrincipalDisplay: authority.ExecutingPrincipalDisplay,
		AuthoritySource:           authority.AuthoritySource,
	}, nil
}

func (f *fakePipelineReferenceRepo) RefreshDraftProposal(
	ctx context.Context, _ postgres.DBTX, orgSlug, _ slug.Slug, params pipelinesrepo.RefreshDraftProposalParams,
) error {
	if f.refreshDraftProposalFn != nil {
		return f.refreshDraftProposalFn(ctx, orgSlug, params)
	}
	return nil
}

func (f *fakePipelineReferenceRepo) QueueDraftProposalRefresh(
	ctx context.Context, _ postgres.DBTX, orgSlug, _ slug.Slug, params pipelinesrepo.QueueDraftProposalRefreshParams,
) error {
	if f.queueDraftProposalRefreshFn != nil {
		return f.queueDraftProposalRefreshFn(ctx, orgSlug, params)
	}
	return nil
}

func (f *fakePipelineReferenceRepo) FailDraftProposalRefresh(
	ctx context.Context, _ postgres.DBTX, orgSlug, _ slug.Slug, params pipelinesrepo.FailDraftProposalRefreshParams,
) error {
	if f.failDraftProposalRefreshFn != nil {
		return f.failDraftProposalRefreshFn(ctx, orgSlug, params)
	}
	return nil
}

type fakeRepoStore struct {
	listFilesFn      func(ctx context.Context, dir string) ([]repostore.FileEntry, error)
	readFileFn       func(ctx context.Context, path string) ([]byte, error)
	readFileMaxBytes []int64
	writeFileFn      func(ctx context.Context, path string, data []byte, message string) error
	deleteFileFn     func(ctx context.Context, path, message string) error
	existsFn         func(ctx context.Context, path string) (bool, error)
}

func (f *fakeRepoStore) ReadFile(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	f.readFileMaxBytes = append(f.readFileMaxBytes, maxBytes)
	if f.readFileFn != nil {
		return f.readFileFn(ctx, path)
	}
	return nil, nil
}

func (f *fakeRepoStore) WriteFile(
	ctx context.Context,
	path string,
	data []byte,
	message string,
) error {
	if f.writeFileFn != nil {
		return f.writeFileFn(ctx, path, data, message)
	}
	return nil
}

func (f *fakeRepoStore) DeleteFile(ctx context.Context, path, message string) error {
	if f.deleteFileFn != nil {
		return f.deleteFileFn(ctx, path, message)
	}
	return nil
}

func (f *fakeRepoStore) ListFiles(ctx context.Context, dir string) ([]repostore.FileEntry, error) {
	if f.listFilesFn != nil {
		return f.listFilesFn(ctx, dir)
	}
	return nil, nil
}

func (f *fakeRepoStore) FileExists(ctx context.Context, path string) (bool, error) {
	if f.existsFn != nil {
		return f.existsFn(ctx, path)
	}
	return false, nil
}

func (f *fakePipelineReferenceRepo) Upsert(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _ slug.Slug,
	params pipelinesrepo.UpsertParams,
) (*pipelinesrepo.PipelineReference, error) {
	if f.upsertFn != nil {
		return f.upsertFn(ctx, orgSlug, params)
	}
	return &pipelinesrepo.PipelineReference{Slug: params.Slug, Name: params.Name}, nil
}

func (f *fakePipelineReferenceRepo) FindBySlug(
	_ context.Context,
	_ postgres.DBTX,
	_, _, _ slug.Slug,
) (*pipelinesrepo.PipelineReference, error) {
	return nil, pipelinesrepo.ErrPipelineReferenceNotFound
}

func (f *fakePipelineReferenceRepo) List(
	_ context.Context,
	_ postgres.DBTX,
	_, _ slug.Slug,
) ([]*pipelinesrepo.PipelineReference, error) {
	return nil, nil
}

func (f *fakePipelineReferenceRepo) Delete(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
) error {
	if f.deleteFn != nil {
		return f.deleteFn(ctx, orgSlug, pipelineSlug)
	}
	return nil
}

func (f *fakePipelineReferenceRepo) FindSpecDeletedAtForUpdate(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
) (time.Time, bool, error) {
	if f.findSpecDeletedAtFn != nil {
		return f.findSpecDeletedAtFn(ctx, orgSlug, pipelineSlug)
	}
	return time.Time{}, false, pipelinesrepo.ErrPipelineReferenceNotFound
}

func (f *fakePipelineReferenceRepo) SetHasDraft(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
	hasDraft bool,
) error {
	if f.setHasDraftFn != nil {
		return f.setHasDraftFn(ctx, orgSlug, pipelineSlug, hasDraft)
	}

	return nil
}

func (f *fakePipelineReferenceRepo) UpsertDraft(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _ slug.Slug,
	memberSlug slug.Slug,
	params pipelinesrepo.UpsertDraftParams,
) (*pipelinesrepo.PipelineDraft, error) {
	if f.upsertDraftFn != nil {
		return f.upsertDraftFn(ctx, orgSlug, memberSlug, params)
	}

	return &pipelinesrepo.PipelineDraft{PipelineSlug: params.PipelineSlug}, nil
}

func (f *fakePipelineReferenceRepo) FindDraft(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug, memberSlug slug.Slug,
) (*pipelinesrepo.PipelineDraft, error) {
	if f.findDraftFn != nil {
		return f.findDraftFn(ctx, orgSlug, pipelineSlug, memberSlug)
	}

	return nil, pipelinesrepo.ErrPipelineReferenceNotFound
}

func (f *fakePipelineReferenceRepo) FindAnyDraft(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineDraft, error) {
	if f.findAnyDraftFn != nil {
		return f.findAnyDraftFn(ctx, orgSlug, pipelineSlug)
	}

	return nil, pipelinesrepo.ErrPipelineReferenceNotFound
}

func (f *fakePipelineReferenceRepo) DeleteDraft(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug, memberSlug slug.Slug,
) error {
	if f.deleteDraftFn != nil {
		return f.deleteDraftFn(ctx, orgSlug, pipelineSlug, memberSlug)
	}

	return nil
}

func (f *fakePipelineReferenceRepo) QueueDraftPublish(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _ slug.Slug,
	params pipelinesrepo.QueueDraftPublishParams,
) error {
	if f.queueDraftPublishFn != nil {
		return f.queueDraftPublishFn(ctx, orgSlug, params)
	}

	return nil
}

func (f *fakePipelineReferenceRepo) CompleteDraftPublish(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _ slug.Slug,
	params pipelinesrepo.CompleteDraftPublishParams,
) error {
	if f.completeDraftPublishFn != nil {
		return f.completeDraftPublishFn(ctx, orgSlug, params)
	}

	return nil
}

func (f *fakePipelineReferenceRepo) FailDraftPublish(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _ slug.Slug,
	params pipelinesrepo.FailDraftPublishParams,
) error {
	if f.failDraftPublishFn != nil {
		return f.failDraftPublishFn(ctx, orgSlug, params)
	}

	return nil
}

func (f *fakePipelineReferenceRepo) UpsertSpec(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _ slug.Slug,
	params pipelinesrepo.UpsertSpecParams,
) (*pipelinesrepo.PipelineSpec, error) {
	if f.upsertSpecFn != nil {
		return f.upsertSpecFn(ctx, orgSlug, params)
	}
	return &pipelinesrepo.PipelineSpec{Slug: params.Slug, Name: params.Name}, nil
}

func (f *fakePipelineReferenceRepo) FindSpec(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineSpec, error) {
	if f.findSpecFn != nil {
		return f.findSpecFn(ctx, orgSlug, pipelineSlug)
	}
	return nil, pipelinesrepo.ErrPipelineReferenceNotFound
}

func (f *fakePipelineReferenceRepo) FindSpecForUpdate(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineSpec, error) {
	if f.findSpecForUpdateFn != nil {
		return f.findSpecForUpdateFn(ctx, orgSlug, pipelineSlug)
	}

	if f.findSpecFn != nil {
		return f.findSpecFn(ctx, orgSlug, pipelineSlug)
	}

	return nil, pipelinesrepo.ErrPipelineReferenceNotFound
}

func (f *fakePipelineReferenceRepo) ListSpecs(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, _ slug.Slug,
	request model.ListRequest,
) ([]*pipelinesrepo.PipelineSpec, error) {
	if f.listSpecsTxFn != nil {
		return f.listSpecsTxFn(ctx, tx, orgSlug, request)
	}
	if f.listSpecsFn != nil {
		return f.listSpecsFn(ctx, orgSlug, request)
	}
	return nil, nil
}

func (f *fakePipelineReferenceRepo) DeleteSpec(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug, deletedBy slug.Slug,
) error {
	if f.deleteSpecFn != nil {
		return f.deleteSpecFn(ctx, orgSlug, pipelineSlug, deletedBy)
	}
	return nil
}

func (f *fakePipelineReferenceRepo) RestoreSpec(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
) error {
	if f.restoreSpecFn != nil {
		return f.restoreSpecFn(ctx, orgSlug, pipelineSlug)
	}
	return nil
}

func (f *fakePipelineReferenceRepo) CreateVersion(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _ slug.Slug,
	params pipelinesrepo.CreateVersionParams,
) (*pipelinesrepo.PipelineVersion, error) {
	if f.createVersionFn != nil {
		return f.createVersionFn(ctx, orgSlug, params)
	}
	return &pipelinesrepo.PipelineVersion{ID: 1, PipelineSlug: params.PipelineSlug}, nil
}

func (f *fakePipelineReferenceRepo) FindVersionByCommitSHA(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
	commitSHA string,
) (*pipelinesrepo.PipelineVersion, error) {
	if f.findVersionByCommitSHAFn != nil {
		return f.findVersionByCommitSHAFn(ctx, orgSlug, pipelineSlug, commitSHA)
	}
	return nil, pipelinesrepo.ErrPipelineVersionNotFound
}

func (f *fakePipelineReferenceRepo) FindVersionBySpecHash(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
	specHash string,
) (*pipelinesrepo.PipelineVersion, error) {
	if f.findVersionBySpecHashFn != nil {
		return f.findVersionBySpecHashFn(ctx, orgSlug, pipelineSlug, specHash)
	}
	return nil, pipelinesrepo.ErrPipelineVersionNotFound
}

func (f *fakePipelineReferenceRepo) FindVersionBySpecHashPrefix(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
	specHashPrefix string,
) (*pipelinesrepo.PipelineVersion, error) {
	if f.findVersionBySpecHashPrefixFn != nil {
		return f.findVersionBySpecHashPrefixFn(ctx, orgSlug, pipelineSlug, specHashPrefix)
	}
	return nil, pipelinesrepo.ErrPipelineVersionNotFound
}

func (f *fakePipelineReferenceRepo) FindVersionByID(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
	versionID int64,
) (*pipelinesrepo.PipelineVersion, error) {
	if f.findVersionByIDFn != nil {
		return f.findVersionByIDFn(ctx, orgSlug, pipelineSlug, versionID)
	}
	return nil, pipelinesrepo.ErrPipelineVersionNotFound
}

func (f *fakePipelineReferenceRepo) ListVersions(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
) ([]*pipelinesrepo.PipelineVersion, error) {
	if f.listVersionsFn != nil {
		return f.listVersionsFn(ctx, orgSlug, pipelineSlug)
	}
	return nil, nil
}

func (f *fakePipelineReferenceRepo) SetCurrentVersion(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, _, pipelineSlug slug.Slug,
	versionID int64,
) error {
	if f.setCurrentVersionFn != nil {
		return f.setCurrentVersionFn(ctx, orgSlug, pipelineSlug, versionID)
	}
	return nil
}

func (f *fakePipelineReferenceRepo) Deploy(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params pipelinesrepo.DeployParams,
) (*pipelinesrepo.PipelineDeployment, error) {
	if f.deployFn != nil {
		return f.deployFn(ctx, orgSlug, projectSlug, environmentSlug, params)
	}
	return nil, pipelinesrepo.ErrPipelineDeploymentNotFound
}

func (f *fakePipelineReferenceRepo) FindDeployment(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineDeployment, error) {
	if f.findDeploymentFn != nil {
		return f.findDeploymentFn(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug)
	}
	return nil, pipelinesrepo.ErrPipelineDeploymentNotFound
}

func (f *fakePipelineReferenceRepo) ListDeployments(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) ([]*pipelinesrepo.PipelineDeployment, error) {
	if f.listDeploymentsFn != nil {
		return f.listDeploymentsFn(ctx, orgSlug, projectSlug, environmentSlug)
	}
	return nil, nil
}

func (f *fakePipelineReferenceRepo) ProjectEnvironmentEnabled(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (bool, error) {
	if f.projectEnvironmentEnabledFn != nil {
		return f.projectEnvironmentEnabledFn(ctx, orgSlug, projectSlug, environmentSlug)
	}
	return true, nil
}

func (f *fakePipelineReferenceRepo) FindProjectEnvironmentProtection(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	if f.projectEnvironmentProtectionFn != nil {
		return f.projectEnvironmentProtectionFn(ctx, orgSlug, projectSlug, environmentSlug)
	}
	return &model.ProjectEnvironmentProtection{EnvironmentSlug: environmentSlug}, nil
}

func TestPipelineStore_List_UsesTransactionManager(t *testing.T) {
	t.Parallel()

	expectedTx := &fakeDBTX{}
	dbtx := &fakeDBTX{}
	expectedSpecs := []*pipelinesrepo.PipelineSpec{{Slug: slug.Slug("users-sync")}}
	repo := &fakePipelineReferenceRepo{
		listSpecsTxFn: func(_ context.Context, gotTx postgres.DBTX, gotOrgSlug slug.Slug, req model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
			require.Same(t, expectedTx, gotTx)
			require.Equal(t, slug.Slug("acme"), gotOrgSlug)
			require.Equal(t, model.ListRequest{Limit: 10}, req)
			return expectedSpecs, nil
		},
	}

	store := &pipelineStore{
		dbtx: dbtx,
		txm:  testutil.FakeTransactionManager{Tx: expectedTx},
		repo: repo,
	}

	got, err := store.List(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		model.ListRequest{Limit: 10},
	)

	require.NoError(t, err)
	require.Equal(t, expectedSpecs, got)
}

func TestPipelineStore_Get_ReturnsNotFoundWhenSpecMissingFromDB(t *testing.T) {
	t.Parallel()

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecFn: func(_ context.Context, _, _ slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
				return nil, pipelinesrepo.ErrPipelineReferenceNotFound
			},
		},
	}

	_, err := store.Get(context.Background(), slug.Slug("acme"), defaultProjectSlug, slug.Slug("acme-users-sync"))
	require.ErrorIs(t, err, ErrPipelineNotFound)
}

func TestPipelineStore_Get_ReturnsDeserializedPipelineFromDB(t *testing.T) {
	t.Parallel()

	original := testPipelineSpec()
	original.Steps[0].Query.Parameters = map[string]any{"id": int64(9007199254740993)}
	specJSON, err := json.Marshal(original)
	require.NoError(t, err)

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecFn: func(_ context.Context, _, _ slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
				return &pipelinesrepo.PipelineSpec{
					Slug:      original.Metadata.Slug,
					SpecJSONB: specJSON,
				}, nil
			},
		},
	}

	got, err := store.Get(context.Background(), slug.Slug("acme"), defaultProjectSlug, original.Metadata.Slug)
	require.NoError(t, err)
	require.Equal(t, original.Metadata.Slug, got.Metadata.Slug)
	require.Equal(t, original.Metadata.Name, got.Metadata.Name)
	require.Len(t, got.Steps, len(original.Steps))
	require.Equal(t, int64(9007199254740993), got.Steps[0].Query.Parameters["id"])
}

func TestPipelineStore_FindDraft_EmptyMemberSlugReturnsNotFound(t *testing.T) {
	t.Parallel()

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findAnyDraftFn: func(
				_ context.Context,
				_, _ slug.Slug,
			) (*pipelinesrepo.PipelineDraft, error) {
				return nil, fmt.Errorf("FindAnyDraft must not be called when memberSlug is empty")
			},
			findDraftFn: func(
				_ context.Context,
				_, _, _ slug.Slug,
			) (*pipelinesrepo.PipelineDraft, error) {
				return nil, fmt.Errorf("FindDraft must not be called when memberSlug is empty")
			},
		},
	}

	_, err := store.FindDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
		slug.Slug(""),
	)
	require.ErrorIs(t, err, ErrPipelineDraftNotFound)
}

func TestPipelineStore_SaveDraft_EmptyMemberSlugReturnsInvalidRequired(t *testing.T) {
	t.Parallel()

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			upsertDraftFn: func(
				_ context.Context,
				_, _ slug.Slug,
				_ pipelinesrepo.UpsertDraftParams,
			) (*pipelinesrepo.PipelineDraft, error) {
				return nil, fmt.Errorf("UpsertDraft must not be called when memberSlug is empty")
			},
		},
	}

	err := store.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug(""),
		testPipelineSpec(),
		nil,
	)
	require.ErrorIs(t, err, xerror.InvalidRequired(xerror.Member))
}

func TestPipelineStore_SaveDraft_AllowsGitManagedPipelines(t *testing.T) {
	t.Parallel()

	currentVersionID := int64(26)
	var gotBasedOnVersionID *int64
	var hasDraftValue bool

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecFn: func(
				_ context.Context,
				_, pipelineSlug slug.Slug,
			) (*pipelinesrepo.PipelineSpec, error) {
				return &pipelinesrepo.PipelineSpec{
					Slug:             pipelineSlug,
					CurrentVersionID: &currentVersionID,
				}, nil
			},
			findDraftFn: func(
				_ context.Context,
				_, _, _ slug.Slug,
			) (*pipelinesrepo.PipelineDraft, error) {
				return nil, pipelinesrepo.ErrPipelineReferenceNotFound
			},
			upsertDraftFn: func(
				_ context.Context,
				_, memberSlug slug.Slug,
				params pipelinesrepo.UpsertDraftParams,
			) (*pipelinesrepo.PipelineDraft, error) {
				require.Equal(t, slug.Slug("member-a"), memberSlug)
				gotBasedOnVersionID = params.BasedOnVersionID
				return &pipelinesrepo.PipelineDraft{PipelineSlug: params.PipelineSlug}, nil
			},
			setHasDraftFn: func(
				_ context.Context,
				_, pipelineSlug slug.Slug,
				hasDraft bool,
			) error {
				require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
				hasDraftValue = hasDraft
				return nil
			},
		},
	}

	err := store.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-a"),
		testPipelineSpec(),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, &currentVersionID, gotBasedOnVersionID)
	require.True(t, hasDraftValue)
}

func TestPipelineStore_SaveDraft_PassesMemberSlugToRepository(t *testing.T) {
	t.Parallel()

	var gotMemberSlug slug.Slug
	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecFn: func(
				_ context.Context,
				_, _ slug.Slug,
			) (*pipelinesrepo.PipelineSpec, error) {
				return &pipelinesrepo.PipelineSpec{}, nil
			},
			upsertDraftFn: func(
				_ context.Context,
				_, memberSlug slug.Slug,
				params pipelinesrepo.UpsertDraftParams,
			) (*pipelinesrepo.PipelineDraft, error) {
				gotMemberSlug = memberSlug
				return &pipelinesrepo.PipelineDraft{PipelineSlug: params.PipelineSlug}, nil
			},
		},
	}

	err := store.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-a"),
		testPipelineSpec(),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, slug.Slug("member-a"), gotMemberSlug)
}

func TestPipelineStore_SaveDraft_DefaultsBasedOnVersionIDToCurrentVersion(t *testing.T) {
	t.Parallel()

	currentVersionID := int64(26)
	var gotBasedOnVersionID *int64

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecFn: func(
				_ context.Context,
				_, _ slug.Slug,
			) (*pipelinesrepo.PipelineSpec, error) {
				return &pipelinesrepo.PipelineSpec{
					CurrentVersionID: &currentVersionID,
				}, nil
			},
			findDraftFn: func(
				_ context.Context,
				_, _, _ slug.Slug,
			) (*pipelinesrepo.PipelineDraft, error) {
				return nil, pipelinesrepo.ErrPipelineReferenceNotFound
			},
			upsertDraftFn: func(
				_ context.Context,
				_, _ slug.Slug,
				params pipelinesrepo.UpsertDraftParams,
			) (*pipelinesrepo.PipelineDraft, error) {
				gotBasedOnVersionID = params.BasedOnVersionID
				return &pipelinesrepo.PipelineDraft{PipelineSlug: params.PipelineSlug}, nil
			},
		},
	}

	err := store.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-a"),
		testPipelineSpec(),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, &currentVersionID, gotBasedOnVersionID)
}

func TestPipelineStore_SaveDraft_PreservesExistingDraftBasedOnVersionIDWhenClientOmits(
	t *testing.T,
) {
	t.Parallel()

	existingBasedOnVersionID := int64(18)
	var gotBasedOnVersionID *int64

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecFn: func(
				_ context.Context,
				_, _ slug.Slug,
			) (*pipelinesrepo.PipelineSpec, error) {
				return &pipelinesrepo.PipelineSpec{}, nil
			},
			findDraftFn: func(
				_ context.Context,
				_, pipelineSlug, _ slug.Slug,
			) (*pipelinesrepo.PipelineDraft, error) {
				return &pipelinesrepo.PipelineDraft{
					PipelineSlug:     pipelineSlug,
					BasedOnVersionID: &existingBasedOnVersionID,
				}, nil
			},
			upsertDraftFn: func(
				_ context.Context,
				_, _ slug.Slug,
				params pipelinesrepo.UpsertDraftParams,
			) (*pipelinesrepo.PipelineDraft, error) {
				gotBasedOnVersionID = params.BasedOnVersionID
				return &pipelinesrepo.PipelineDraft{PipelineSlug: params.PipelineSlug}, nil
			},
		},
	}

	err := store.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-a"),
		testPipelineSpec(),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, &existingBasedOnVersionID, gotBasedOnVersionID)
}

func TestPipelineStore_SaveDraft_ReturnsConflictWhenExpectedVersionIsStale(t *testing.T) {
	t.Parallel()

	expectedSpecHash := "stale-spec-hash"
	currentSpecHash := "current-spec-hash"
	currentVersionID := int64(13)
	var upsertDraftCalled bool

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecFn: func(
				_ context.Context,
				_, pipelineSlug slug.Slug,
			) (*pipelinesrepo.PipelineSpec, error) {
				return &pipelinesrepo.PipelineSpec{
					Slug:                   pipelineSlug,
					CurrentVersionID:       &currentVersionID,
					CurrentVersionSpecHash: &currentSpecHash,
				}, nil
			},
			upsertDraftFn: func(
				_ context.Context,
				_, _ slug.Slug,
				_ pipelinesrepo.UpsertDraftParams,
			) (*pipelinesrepo.PipelineDraft, error) {
				upsertDraftCalled = true
				return &pipelinesrepo.PipelineDraft{}, nil
			},
		},
	}

	err := store.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-a"),
		testPipelineSpec(),
		&expectedSpecHash,
	)
	require.ErrorIs(t, err, ErrPipelineDraftConflict)
	require.False(t, upsertDraftCalled)

	var conflict *PipelineDraftConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, slug.Slug("acme-users-sync"), conflict.PipelineSlug)
	require.Equal(t, &expectedSpecHash, conflict.ExpectedSpecHash)
	require.Equal(t, &currentSpecHash, conflict.CurrentSpecHash)
	require.Nil(t, conflict.DraftBasedOnVersionID)
	require.False(t, conflict.DraftPreserved)
	require.False(t, conflict.CanOverwrite)
}

func TestPipelineStore_SaveDraft_RebasesStaleExpectedVersionWhenSpecMatchesCanonical(t *testing.T) {
	t.Parallel()

	expectedSpecHash := "stale-spec-hash"
	currentVersionID := int64(13)
	specJSON, err := json.Marshal(testPipelineSpec())
	require.NoError(t, err)

	var gotBasedOnVersionID *int64

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecFn: func(
				_ context.Context,
				_, pipelineSlug slug.Slug,
			) (*pipelinesrepo.PipelineSpec, error) {
				currentSpecHash := fmt.Sprintf("%x", sha256.Sum256(specJSON))
				return &pipelinesrepo.PipelineSpec{
					Slug:                   pipelineSlug,
					CurrentVersionID:       &currentVersionID,
					CurrentVersionSpecHash: &currentSpecHash,
					SpecJSONB:              specJSON,
					SpecHash:               currentSpecHash,
				}, nil
			},
			upsertDraftFn: func(
				_ context.Context,
				_, _ slug.Slug,
				params pipelinesrepo.UpsertDraftParams,
			) (*pipelinesrepo.PipelineDraft, error) {
				gotBasedOnVersionID = params.BasedOnVersionID
				return &pipelinesrepo.PipelineDraft{PipelineSlug: params.PipelineSlug}, nil
			},
		},
	}

	err = store.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-a"),
		testPipelineSpec(),
		&expectedSpecHash,
	)
	require.NoError(t, err)
	require.Equal(t, &currentVersionID, gotBasedOnVersionID)
}

func TestPipelineStore_SaveDraft_RebasesStaleExpectedVersionWhenCanonicalBytesMatch(
	t *testing.T,
) {
	t.Parallel()

	expectedSpecHash := "stale-spec-hash"
	currentVersionID := int64(13)
	specJSON, err := json.Marshal(testPipelineSpec())
	require.NoError(t, err)

	var gotBasedOnVersionID *int64

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecFn: func(
				_ context.Context,
				_, pipelineSlug slug.Slug,
			) (*pipelinesrepo.PipelineSpec, error) {
				currentVersionSpecHash := "different-current-version-hash"
				return &pipelinesrepo.PipelineSpec{
					Slug:                   pipelineSlug,
					CurrentVersionID:       &currentVersionID,
					SpecJSONB:              specJSON,
					SpecHash:               "different-spec-hash",
					CurrentVersionSpecHash: &currentVersionSpecHash,
				}, nil
			},
			upsertDraftFn: func(
				_ context.Context,
				_, _ slug.Slug,
				params pipelinesrepo.UpsertDraftParams,
			) (*pipelinesrepo.PipelineDraft, error) {
				gotBasedOnVersionID = params.BasedOnVersionID
				return &pipelinesrepo.PipelineDraft{PipelineSlug: params.PipelineSlug}, nil
			},
		},
	}

	err = store.SaveDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-a"),
		testPipelineSpec(),
		&expectedSpecHash,
	)
	require.NoError(t, err)
	require.Equal(t, &currentVersionID, gotBasedOnVersionID)
}

func TestPipelineStore_DeleteDraft_KeepsHasDraftWhenOtherMemberDraftsRemain(t *testing.T) {
	t.Parallel()

	var hasDraftValue bool
	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			deleteDraftFn: func(_ context.Context, _, _, _ slug.Slug) error {
				return nil
			},
			findAnyDraftFn: func(
				_ context.Context,
				_, pipelineSlug slug.Slug,
			) (*pipelinesrepo.PipelineDraft, error) {
				return &pipelinesrepo.PipelineDraft{PipelineSlug: pipelineSlug}, nil
			},
			setHasDraftFn: func(
				_ context.Context,
				_, _ slug.Slug,
				hasDraft bool,
			) error {
				hasDraftValue = hasDraft
				return nil
			},
		},
	}

	err := store.DeleteDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
		slug.Slug("member-a"),
	)
	require.NoError(t, err)
	require.True(t, hasDraftValue)
}

func TestPipelineStore_DeleteDraft_ClearsHasDraftForGitManagedPipelineWhenLastDraftIsDiscarded(
	t *testing.T,
) {
	t.Parallel()

	var hasDraftValue bool
	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			deleteDraftFn: func(_ context.Context, _, pipelineSlug, memberSlug slug.Slug) error {
				require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
				require.Equal(t, slug.Slug("member-a"), memberSlug)
				return nil
			},
			findAnyDraftFn: func(
				_ context.Context,
				_, pipelineSlug slug.Slug,
			) (*pipelinesrepo.PipelineDraft, error) {
				require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
				return nil, pipelinesrepo.ErrPipelineReferenceNotFound
			},
			setHasDraftFn: func(
				_ context.Context,
				_, pipelineSlug slug.Slug,
				hasDraft bool,
			) error {
				require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
				hasDraftValue = hasDraft
				return nil
			},
		},
	}

	err := store.DeleteDraft(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("acme-users-sync"),
		slug.Slug("member-a"),
	)
	require.NoError(t, err)
	require.False(t, hasDraftValue)
}

func TestPipelineStore_PrepareGitDraftPublishTx_QueuesProposalPublishWithProjectBackendFallbackRef(t *testing.T) {
	t.Parallel()

	orgSlug := slug.Slug("acme")
	projectSlug := defaultProjectSlug
	pipelineSlug := slug.Slug("acme-users-sync")
	memberSlug := slug.Slug("member-a")
	sourcePath := " pipelines/acme-users-sync.pipeline.yaml "
	fallbackBaseRef := "refs/heads/main"
	currentSpecHash := "current-spec-hash"
	currentVersionID := int64(12)
	lastAppliedCommit := "abc123"
	draftJSON, err := json.Marshal(testPipelineSpec())
	require.NoError(t, err)

	var queued pipelinesrepo.QueueDraftPublishParams
	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecForUpdateFn: func(
				_ context.Context,
				gotOrgSlug, gotPipelineSlug slug.Slug,
			) (*pipelinesrepo.PipelineSpec, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, pipelineSlug, gotPipelineSlug)
				return &pipelinesrepo.PipelineSpec{
					Slug:                   pipelineSlug,
					SourcePath:             &sourcePath,
					CurrentVersionID:       &currentVersionID,
					CurrentVersionSpecHash: &currentSpecHash,
					LastAppliedCommit:      &lastAppliedCommit,
				}, nil
			},
			findDraftFn: func(
				_ context.Context,
				gotOrgSlug, gotPipelineSlug, gotMemberSlug slug.Slug,
			) (*pipelinesrepo.PipelineDraft, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, pipelineSlug, gotPipelineSlug)
				require.Equal(t, memberSlug, gotMemberSlug)
				return &pipelinesrepo.PipelineDraft{
					PipelineSlug:     pipelineSlug,
					SpecJSONB:        draftJSON,
					BasedOnVersionID: &currentVersionID,
					BasedOnSpecHash:  &currentSpecHash,
				}, nil
			},
			queueDraftPublishFn: func(
				_ context.Context,
				gotOrgSlug slug.Slug,
				params pipelinesrepo.QueueDraftPublishParams,
			) error {
				require.Equal(t, orgSlug, gotOrgSlug)
				queued = params
				return nil
			},
		},
	}

	req, err := store.PrepareGitDraftPublish(
		context.Background(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		memberSlug,
		&fallbackBaseRef,
		&currentSpecHash,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, "braidplane/default/acme-users-sync/member-a-"+req.PublishAttemptID, req.BranchName)
	require.Equal(t, "pipelines/acme-users-sync.pipeline.yaml", req.SourcePath)
	require.Equal(t, "propose pipeline acme-users-sync", req.CommitMessage)
	require.Contains(t, string(req.Content), "slug: acme-users-sync")
	require.Equal(t, pipelineSlug, queued.PipelineSlug)
	require.Equal(t, memberSlug, queued.MemberSlug)
	require.Equal(t, pipelineDraftStatePublishQueued, queued.DraftState)
	require.NotNil(t, queued.BaseRef)
	require.Equal(t, fallbackBaseRef, *queued.BaseRef)
	require.NotNil(t, queued.BaseCommitSHA)
	require.Equal(t, lastAppliedCommit, *queued.BaseCommitSHA)
	require.Equal(t, req.BranchName, queued.BranchName)

	// The fake returns another fresh draft, as after merging and discarding the old one.
	next, err := store.PrepareGitDraftPublish(context.Background(), orgSlug, projectSlug, pipelineSlug, memberSlug, &fallbackBaseRef, &currentSpecHash, false)
	require.NoError(t, err)
	require.NotEqual(t, req.BranchName, next.BranchName)
}

func TestPipelineStore_PrepareGitDraftPublishReusesQueuedAttemptForSameDraft(t *testing.T) {
	t.Parallel()
	orgSlug, projectSlug := slug.Slug("acme"), defaultProjectSlug
	pipelineSlug, memberSlug := slug.Slug("acme-users-sync"), slug.Slug("member-a")
	sourcePath, sourceRef, attemptID := "pipelines/acme-users-sync.pipeline.yaml", "refs/heads/main", "attempt-existing"
	draftState := pipelineDraftStatePublishQueued
	branchName := "braidplane/existing-proposal"
	var headSHA *string
	versionID := int64(12)
	specHash := "current-spec-hash"
	draftJSON, err := json.Marshal(testPipelineSpec())
	require.NoError(t, err)
	var queued pipelinesrepo.QueueDraftPublishParams
	store := &pipelineStore{dbtx: &fakeDBTX{}, txm: testutil.FakeTransactionManager{}, repo: &fakePipelineReferenceRepo{
		findSpecForUpdateFn: func(context.Context, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
			return &pipelinesrepo.PipelineSpec{SourcePath: &sourcePath, SourceRef: &sourceRef, CurrentVersionID: &versionID, CurrentVersionSpecHash: &specHash}, nil
		},
		findDraftFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineDraft, error) {
			return &pipelinesrepo.PipelineDraft{SpecJSONB: draftJSON, BasedOnVersionID: &versionID, DraftState: &draftState, PublishAttemptID: &attemptID, BranchName: &branchName, BranchHeadSHA: headSHA}, nil
		},
		queueDraftPublishFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.QueueDraftPublishParams) error {
			queued = params
			return nil
		},
	}}

	req, err := store.PrepareGitDraftPublish(context.Background(), orgSlug, projectSlug, pipelineSlug, memberSlug, nil, &specHash, false)

	require.NoError(t, err)
	require.Equal(t, branchName, req.BranchName)
	require.Equal(t, branchName, queued.BranchName)
	require.Equal(t, attemptID, req.PublishAttemptID)
	require.Equal(t, attemptID, queued.PublishAttemptID)

	draftState = pipelineDraftStatePublishFailed
	retry, err := store.PrepareGitDraftPublish(context.Background(), orgSlug, projectSlug, pipelineSlug, memberSlug, nil, &specHash, false)
	require.NoError(t, err)
	require.NotEqual(t, branchName, retry.BranchName)
	require.NotEqual(t, attemptID, retry.PublishAttemptID)
	require.Equal(t, req.Content, retry.Content)
	require.Nil(t, retry.ExpectedHeadSHA)

	recordedHead := "published-head"
	headSHA = &recordedHead
	publishedRetry, err := store.PrepareGitDraftPublish(context.Background(), orgSlug, projectSlug, pipelineSlug, memberSlug, nil, &specHash, false)
	require.NoError(t, err)
	require.Equal(t, branchName, publishedRetry.BranchName)
	require.Equal(t, &recordedHead, publishedRetry.ExpectedHeadSHA)
}

func TestPipelineStore_CompleteProposal_PersistsSafeProposalMetadataWithoutDeploying(t *testing.T) {
	t.Parallel()

	orgSlug := slug.Slug("acme")
	projectSlug := defaultProjectSlug
	pipelineSlug := slug.Slug("acme-users-sync")
	memberSlug := slug.Slug("member-a")

	upsertSpecCalled := false
	createVersionCalled := false
	setCurrentVersionCalled := false
	deleteDraftCalled := false
	var completed pipelinesrepo.CompleteDraftPublishParams
	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			completeDraftPublishFn: func(
				_ context.Context,
				gotOrgSlug slug.Slug,
				params pipelinesrepo.CompleteDraftPublishParams,
			) error {
				require.Equal(t, orgSlug, gotOrgSlug)
				completed = params
				return nil
			},
			upsertSpecFn: func(
				context.Context,
				slug.Slug,
				pipelinesrepo.UpsertSpecParams,
			) (*pipelinesrepo.PipelineSpec, error) {
				upsertSpecCalled = true
				return &pipelinesrepo.PipelineSpec{}, nil
			},
			createVersionFn: func(
				context.Context,
				slug.Slug,
				pipelinesrepo.CreateVersionParams,
			) (*pipelinesrepo.PipelineVersion, error) {
				createVersionCalled = true
				return &pipelinesrepo.PipelineVersion{}, nil
			},
			setCurrentVersionFn: func(context.Context, slug.Slug, slug.Slug, int64) error {
				setCurrentVersionCalled = true
				return nil
			},
			deleteDraftFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) error {
				deleteDraftCalled = true
				return nil
			},
		},
	}

	err := store.CompleteProposal(
		context.Background(),
		pipelinegit.ProposalCompletion{
			OrganizationSlug: orgSlug,
			ProjectSlug:      projectSlug,
			PipelineSlug:     pipelineSlug,
			MemberSlug:       memberSlug,
			Provider:         "github",
			ProposalID:       "42",
			ProposalURL:      "https://github.com/acme/pipelines/pull/42",
			ProposalState:    "open",
			BranchName:       "braidplane/default/acme-users-sync/member-a",
			BranchHeadSHA:    "def456",
			DraftSpecHash:    "draft-hash",
			PublishAttemptID: "attempt-42",
		},
	)

	require.NoError(t, err)
	require.Equal(t, pipelineSlug, completed.PipelineSlug)
	require.Equal(t, memberSlug, completed.MemberSlug)
	require.Equal(t, "proposal_open", completed.DraftState)
	require.Equal(t, "github", completed.ProposalProvider)
	require.Equal(t, "42", completed.ProposalID)
	require.Equal(t, "https://github.com/acme/pipelines/pull/42", completed.ProposalURL)
	require.Equal(t, "open", completed.ProposalState)
	require.Equal(t, "braidplane/default/acme-users-sync/member-a", completed.BranchName)
	require.Equal(t, "def456", completed.BranchHeadSHA)
	require.Equal(t, "draft-hash", completed.LastProposedDraftHash)
	require.Equal(t, "attempt-42", completed.PublishAttemptID)
	require.False(t, upsertSpecCalled)
	require.False(t, createVersionCalled)
	require.False(t, setCurrentVersionCalled)
	require.False(t, deleteDraftCalled)
}

func TestPipelineStore_FailProposal_PersistsRetryableStateWithSafeDiagnostics(t *testing.T) {
	t.Parallel()

	var failed pipelinesrepo.FailDraftPublishParams
	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			failDraftPublishFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				params pipelinesrepo.FailDraftPublishParams,
			) error {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				failed = params
				return nil
			},
		},
	}

	err := store.FailProposal(context.Background(), pipelinegit.ProposalFailure{
		OrganizationSlug: slug.Slug("acme"),
		ProjectSlug:      defaultProjectSlug,
		PipelineSlug:     slug.Slug("acme-users-sync"),
		MemberSlug:       slug.Slug("member-a"),
		PublishAttemptID: "attempt-1",
		FailureCode:      "provider_unavailable",
		FailureMessage:   "The Git provider could not be reached. Retry the proposal.",
	})

	require.NoError(t, err)
	require.Equal(t, slug.Slug("acme-users-sync"), failed.PipelineSlug)
	require.Equal(t, slug.Slug("member-a"), failed.MemberSlug)
	require.Equal(t, pipelineDraftStatePublishFailed, failed.DraftState)
	require.Equal(t, "attempt-1", failed.PublishAttemptID)
	require.Equal(t, "provider_unavailable", failed.FailureCode)
	require.Equal(t, "The Git provider could not be reached. Retry the proposal.", failed.FailureMessage)
}

func TestPipelineStore_PrepareGitProposalRefreshQueuesDistinctAttempt(t *testing.T) {
	t.Parallel()
	proposalID := "42"
	var queued pipelinesrepo.QueueDraftProposalRefreshParams
	store := &pipelineStore{dbtx: &fakeDBTX{}, txm: testutil.FakeTransactionManager{}, repo: &fakePipelineReferenceRepo{
		findDraftFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineDraft, error) {
			return &pipelinesrepo.PipelineDraft{ProposalID: &proposalID}, nil
		},
		queueDraftProposalRefreshFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.QueueDraftProposalRefreshParams) error {
			queued = params
			return nil
		},
	}}

	req, err := store.PrepareGitProposalRefresh(context.Background(), slug.Slug("acme"), defaultProjectSlug,
		slug.Slug("acme-users-sync"), slug.Slug("member-a"))

	require.NoError(t, err)
	require.Equal(t, proposalID, req.ProposalID)
	require.NotEmpty(t, req.RefreshAttemptID)
	require.Equal(t, req.RefreshAttemptID, queued.RefreshAttemptID)
	require.Equal(t, proposalID, queued.ProposalID)
}

func TestPipelineStore_FailProposalRefreshPreservesProposalLifecycleState(t *testing.T) {
	t.Parallel()
	for _, draftState := range []string{pipelineDraftStateProposalOpen, pipelineDraftStateProposalClosed} {
		t.Run(draftState, func(t *testing.T) {
			t.Parallel()
			var failed pipelinesrepo.FailDraftProposalRefreshParams
			store := &pipelineStore{dbtx: &fakeDBTX{}, txm: testutil.FakeTransactionManager{}, repo: &fakePipelineReferenceRepo{
				failDraftProposalRefreshFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.FailDraftProposalRefreshParams) error {
					failed = params
					return nil
				},
			}}

			err := store.FailProposalRefresh(context.Background(), pipelinegit.ProposalRefreshFailure{
				OrganizationSlug: slug.Slug("acme"), ProjectSlug: defaultProjectSlug,
				PipelineSlug: slug.Slug("acme-users-sync"), MemberSlug: slug.Slug("member-a"),
				ProposalID: "42", RefreshAttemptID: "refresh-1",
				FailureCode: "provider_unavailable", FailureMessage: "Refresh failed safely.",
			})

			require.NoError(t, err)
			require.Equal(t, "42", failed.ProposalID)
			require.Equal(t, "refresh-1", failed.RefreshAttemptID)
			require.Equal(t, "provider_unavailable", failed.FailureCode)
		})
	}
}

func TestPipelineStore_CompleteProposalRefreshPersistsTerminalStateForExactProposal(t *testing.T) {
	t.Parallel()
	var refreshed pipelinesrepo.RefreshDraftProposalParams
	store := &pipelineStore{dbtx: &fakeDBTX{}, txm: testutil.FakeTransactionManager{}, repo: &fakePipelineReferenceRepo{
		refreshDraftProposalFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.RefreshDraftProposalParams) error {
			refreshed = params
			return nil
		},
	}}

	err := store.CompleteProposalRefresh(context.Background(), pipelinegit.ProposalRefreshCompletion{
		OrganizationSlug: slug.Slug("acme"), ProjectSlug: defaultProjectSlug,
		PipelineSlug: slug.Slug("acme-users-sync"), MemberSlug: slug.Slug("member-a"),
		Provider: "github", ProposalID: "42", ProposalState: "merged", BranchName: "bp/member", BranchHeadSHA: "head-42",
		RefreshAttemptID: "refresh-42",
	})

	require.NoError(t, err)
	require.NotNil(t, refreshed.ProposalID)
	require.Equal(t, "42", *refreshed.ProposalID)
	require.Equal(t, pipelineDraftStateProposalClosed, refreshed.DraftState)
	require.NotNil(t, refreshed.BranchHeadSHA)
	require.Equal(t, "head-42", *refreshed.BranchHeadSHA)
	require.Equal(t, "refresh-42", refreshed.RefreshAttemptID)
}

func TestPipelineStore_ReconcileFromGit_UpsertsGitSpecsAndDeletesStaleGitRows(t *testing.T) {
	t.Parallel()

	gitSpec := testPipelineSpec()
	gitSpecYAML := []byte(`
metadata:
  slug: acme-users-sync
  name: ACME Users Sync
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref:  list-active-users
`)

	var upsertSpecParams []pipelinesrepo.UpsertSpecParams
	var upsertRefParams []pipelinesrepo.UpsertParams
	var createdVersions []pipelinesrepo.CreateVersionParams
	var currentVersionCalls int
	var deletedSpec []slug.Slug

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			upsertSpecFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.UpsertSpecParams) (*pipelinesrepo.PipelineSpec, error) {
				upsertSpecParams = append(upsertSpecParams, params)
				return &pipelinesrepo.PipelineSpec{Slug: params.Slug, Name: params.Name}, nil
			},
			upsertFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.UpsertParams) (*pipelinesrepo.PipelineReference, error) {
				upsertRefParams = append(upsertRefParams, params)
				return &pipelinesrepo.PipelineReference{Slug: params.Slug, Name: params.Name}, nil
			},
			findVersionByCommitSHAFn: func(_ context.Context, _, _ slug.Slug, _ string) (*pipelinesrepo.PipelineVersion, error) {
				return nil, pipelinesrepo.ErrPipelineVersionNotFound
			},
			createVersionFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.CreateVersionParams) (*pipelinesrepo.PipelineVersion, error) {
				require.NotNil(t, params.SourcePath)
				require.NotEmpty(t, *params.SourcePath)
				createdVersions = append(createdVersions, params)
				require.NotEmpty(t, params.SpecJSONB)
				require.NotEmpty(t, params.SpecHash)
				return &pipelinesrepo.PipelineVersion{
					ID:           7,
					PipelineSlug: params.PipelineSlug,
					GitCommitSHA: params.GitCommitSHA,
				}, nil
			},
			setCurrentVersionFn: func(_ context.Context, _, _ slug.Slug, _ int64) error {
				currentVersionCalls++
				return nil
			},
			listSpecsFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
				return []*pipelinesrepo.PipelineSpec{
					{Slug: slug.Slug("stale-git")},
				}, nil
			},
			deleteSpecFn: func(_ context.Context, _, pipelineSlug, _ slug.Slug) error {
				deletedSpec = append(deletedSpec, pipelineSlug)
				return nil
			},
			deleteFn: func(_ context.Context, _, _ slug.Slug) error {
				return nil
			},
		},
	}

	repoStore := &fakeRepoStore{
		listFilesFn: func(_ context.Context, dir string) ([]repostore.FileEntry, error) {
			require.Equal(t, "pipelines", dir)
			return []repostore.FileEntry{
				{Path: "pipelines/acme-users-sync.pipeline.yaml", Size: int64(len(gitSpecYAML))},
			}, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			require.Equal(t, "pipelines/acme-users-sync.pipeline.yaml", path)
			return gitSpecYAML, nil
		},
	}

	sourceRef := "main"
	commit := "abc123"

	result, err := store.ReconcileFromGit(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		repoStore,
		ReconcileFromGitParams{
			PipelineDir:       "pipelines",
			SourceRef:         &sourceRef,
			LastAppliedCommit: &commit,
		},
	)
	require.NoError(t, err)
	require.Equal(t, 1, result.Scanned)
	require.Equal(t, 1, result.Upserted)
	require.Equal(t, 1, result.Deleted)
	require.Equal(t, 0, result.Failed)

	require.Len(t, upsertSpecParams, 1)
	require.Equal(t, gitSpec.Metadata.Slug, upsertSpecParams[0].Slug)
	require.Equal(t, pipelinesrepo.SyncStatusSynced, upsertSpecParams[0].SyncStatus)
	require.Equal(t, sourceRef, *upsertSpecParams[0].SourceRef)
	require.Equal(t, commit, *upsertSpecParams[0].LastAppliedCommit)
	require.Equal(t, "pipelines/acme-users-sync.pipeline.yaml", *upsertSpecParams[0].SourcePath)

	require.Len(t, upsertRefParams, 1)
	require.Equal(t, gitSpec.Metadata.Slug, upsertRefParams[0].Slug)
	require.Equal(t, gitSpec.Metadata.Name, upsertRefParams[0].Name)
	require.Len(t, createdVersions, 1)
	require.Equal(t, gitSpec.Metadata.Slug, createdVersions[0].PipelineSlug)
	storedPipeline, err := pipelineFromJSON(createdVersions[0].SpecJSONB)
	require.NoError(t, err)
	validationHash, err := validationSpecHash(storedPipeline)
	require.NoError(t, err)
	require.Equal(t, createdVersions[0].SpecHash, *validationHash)
	require.NotNil(t, createdVersions[0].GitCommitSHA)
	require.Equal(t, commit, *createdVersions[0].GitCommitSHA)
	require.Equal(t, 1, currentVersionCalls)
	require.Equal(t, []slug.Slug{slug.Slug("stale-git")}, deletedSpec)
	require.Equal(t, []int64{maxReconcilePipelineSpecBytes}, repoStore.readFileMaxBytes)
}

func TestPipelineStore_ReconcileFromGit_AllowsNonDryRunUpsertsWithoutBackendOwnerFields(t *testing.T) {
	t.Parallel()

	gitSpecYAML := []byte(`
metadata:
  slug: acme-users-sync
  name: ACME Users Sync
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
`)

	var upsertSpecCalls int
	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			upsertSpecFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.UpsertSpecParams) (*pipelinesrepo.PipelineSpec, error) {
				upsertSpecCalls++
				return &pipelinesrepo.PipelineSpec{Slug: params.Slug, Name: params.Name}, nil
			},
		},
	}

	repoStore := &fakeRepoStore{
		listFilesFn: func(_ context.Context, dir string) ([]repostore.FileEntry, error) {
			require.Equal(t, "pipelines", dir)
			return []repostore.FileEntry{
				{Path: "pipelines/acme-users-sync.pipeline.yaml", Size: int64(len(gitSpecYAML))},
			}, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			require.Equal(t, "pipelines/acme-users-sync.pipeline.yaml", path)
			return gitSpecYAML, nil
		},
	}

	result, err := store.ReconcileFromGit(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		repoStore,
		testReconcileFromGitParams(),
	)

	require.NoError(t, err)
	require.Equal(t, 1, result.Scanned)
	require.Equal(t, 1, upsertSpecCalls)
}

func TestPipelineStore_ReconcileFromGit_ReportsWhenDriftedSpecIsReset(t *testing.T) {
	t.Parallel()

	gitSpecYAML := []byte(`
metadata:
  slug: acme-users-sync
  name: ACME Users Sync
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
`)
	sourcePath := "pipelines/acme-users-sync.pipeline.yaml"

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecForUpdateFn: func(
				_ context.Context,
				orgSlug, pipelineSlug slug.Slug,
			) (*pipelinesrepo.PipelineSpec, error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
				return &pipelinesrepo.PipelineSpec{
					Slug:       pipelineSlug,
					Name:       "ACME Users Sync",
					SpecHash:   "ui-drifted-hash",
					SyncStatus: pipelinesrepo.SyncStatusDrifted,
				}, nil
			},
			upsertSpecFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.UpsertSpecParams) (*pipelinesrepo.PipelineSpec, error) {
				require.Equal(t, pipelinesrepo.SyncStatusSynced, params.SyncStatus)
				return &pipelinesrepo.PipelineSpec{Slug: params.Slug, Name: params.Name}, nil
			},
			upsertFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.UpsertParams) (*pipelinesrepo.PipelineReference, error) {
				return &pipelinesrepo.PipelineReference{Slug: params.Slug, Name: params.Name}, nil
			},
			createVersionFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.CreateVersionParams) (*pipelinesrepo.PipelineVersion, error) {
				return &pipelinesrepo.PipelineVersion{ID: 7, PipelineSlug: params.PipelineSlug}, nil
			},
			listSpecsFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
				return []*pipelinesrepo.PipelineSpec{
					{Slug: slug.Slug("acme-users-sync")},
				}, nil
			},
		},
	}

	repoStore := &fakeRepoStore{
		listFilesFn: func(_ context.Context, dir string) ([]repostore.FileEntry, error) {
			require.Equal(t, "pipelines", dir)
			return []repostore.FileEntry{{Path: sourcePath, Size: int64(len(gitSpecYAML))}}, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			require.Equal(t, sourcePath, path)
			return gitSpecYAML, nil
		},
	}

	result, err := store.ReconcileFromGit(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		repoStore,
		testReconcileFromGitParams(),
	)

	require.NoError(t, err)
	require.Equal(t, 1, result.Upserted)
	require.Equal(t, 1, result.ResetDrifted)
	require.Len(t, result.ResetDriftedPipelines, 1)
	require.Equal(t, "acme-users-sync", result.ResetDriftedPipelines[0].PipelineSlug)
	require.Equal(t, sourcePath, result.ResetDriftedPipelines[0].SourcePath)
	require.Equal(t, "ui-drifted-hash", result.ResetDriftedPipelines[0].PreviousSpecHash)
}

func TestPipelineStore_ReconcileFromGit_RestoresSoftDeletedSlugAndContinues(t *testing.T) {
	t.Parallel()

	deletedSpecYAML := []byte(`
metadata:
  slug: deleted-git
  name: Deleted Git
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
`)
	activeSpecYAML := []byte(`
metadata:
  slug: active-git
  name: Active Git
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
`)

	var upserted []slug.Slug
	var restored []slug.Slug
	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecForUpdateFn: func(
				_ context.Context,
				_ slug.Slug,
				pipelineSlug slug.Slug,
			) (*pipelinesrepo.PipelineSpec, error) {
				require.Contains(t, []slug.Slug{slug.Slug("deleted-git"), slug.Slug("active-git")}, pipelineSlug)
				return nil, pipelinesrepo.ErrPipelineReferenceNotFound
			},
			findSpecDeletedAtFn: func(
				_ context.Context,
				_ slug.Slug,
				pipelineSlug slug.Slug,
			) (time.Time, bool, error) {
				switch pipelineSlug {
				case slug.Slug("deleted-git"):
					return time.Date(2026, time.May, 14, 12, 0, 0, 0, time.UTC), true, nil
				case slug.Slug("active-git"):
					return time.Time{}, false, pipelinesrepo.ErrPipelineReferenceNotFound
				default:
					t.Fatalf("unexpected pipeline slug %s", pipelineSlug)
					return time.Time{}, false, nil
				}
			},
			restoreSpecFn: func(_ context.Context, _, pipelineSlug slug.Slug) error {
				restored = append(restored, pipelineSlug)
				return nil
			},
			upsertSpecFn: func(
				_ context.Context,
				_ slug.Slug,
				params pipelinesrepo.UpsertSpecParams,
			) (*pipelinesrepo.PipelineSpec, error) {
				upserted = append(upserted, params.Slug)
				return &pipelinesrepo.PipelineSpec{Slug: params.Slug, Name: params.Name}, nil
			},
			upsertFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.UpsertParams) (*pipelinesrepo.PipelineReference, error) {
				return &pipelinesrepo.PipelineReference{Slug: params.Slug, Name: params.Name}, nil
			},
			listSpecsFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
				return []*pipelinesrepo.PipelineSpec{
					{Slug: slug.Slug("deleted-git")},
					{Slug: slug.Slug("active-git")},
				}, nil
			},
		},
	}

	repoStore := &fakeRepoStore{
		listFilesFn: func(_ context.Context, dir string) ([]repostore.FileEntry, error) {
			require.Equal(t, "pipelines", dir)
			return []repostore.FileEntry{
				{Path: "pipelines/deleted-git.pipeline.yaml", Size: int64(len(deletedSpecYAML))},
				{Path: "pipelines/active-git.pipeline.yaml", Size: int64(len(activeSpecYAML))},
			}, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			switch path {
			case "pipelines/deleted-git.pipeline.yaml":
				return deletedSpecYAML, nil
			case "pipelines/active-git.pipeline.yaml":
				return activeSpecYAML, nil
			default:
				return nil, fmt.Errorf("unexpected path %s", path)
			}
		},
	}

	result, err := store.ReconcileFromGit(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		repoStore,
		testReconcileFromGitParams(),
	)

	require.NoError(t, err)
	require.Equal(t, 2, result.Scanned)
	require.Equal(t, 2, result.Upserted)
	require.Equal(t, 0, result.Deleted)
	require.Equal(t, 0, result.Failed)
	require.Equal(t, []slug.Slug{slug.Slug("deleted-git"), slug.Slug("active-git")}, upserted)
	require.Equal(t, []slug.Slug{slug.Slug("deleted-git")}, restored)
	require.Empty(t, result.Failures)
}

func TestPipelineStore_ReconcileFromGit_ReturnsInvalidRequiredWhenRepoStoreNil(t *testing.T) {
	t.Parallel()

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{},
	}

	_, err := store.ReconcileFromGit(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		nil,
		ReconcileFromGitParams{},
	)
	require.ErrorContains(t, err, "reconcile from git requires repo store")
}

func TestPipelineStore_ReconcileFromGit_ContinuesOnPipelineFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, stage, message string }{
		{"read error", "read", "boom"},
		{"disconnected flows", "load", `["a", "x"]; ["b", "y"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validSpecYAML := []byte(`
metadata:
  slug: acme-users-sync
  name: ACME Users Sync
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref:  list-active-users
`)

			store := &pipelineStore{
				dbtx: &fakeDBTX{},
				txm:  testutil.FakeTransactionManager{},
				repo: &fakePipelineReferenceRepo{
					upsertSpecFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.UpsertSpecParams) (*pipelinesrepo.PipelineSpec, error) {
						return &pipelinesrepo.PipelineSpec{Slug: params.Slug, Name: params.Name}, nil
					},
					upsertFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.UpsertParams) (*pipelinesrepo.PipelineReference, error) {
						return &pipelinesrepo.PipelineReference{Slug: params.Slug, Name: params.Name}, nil
					},
					listSpecsFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
						return []*pipelinesrepo.PipelineSpec{
							{Slug: slug.Slug("acme-users-sync")},
						}, nil
					},
				},
			}

			repoStore := &fakeRepoStore{
				listFilesFn: func(_ context.Context, dir string) ([]repostore.FileEntry, error) {
					require.Equal(t, "pipelines", dir)
					return []repostore.FileEntry{
						{Path: "pipelines/invalid.pipeline.yaml", Size: 12},
						{Path: "pipelines/acme-users-sync.pipeline.yaml", Size: int64(len(validSpecYAML))},
					}, nil
				},
				readFileFn: func(_ context.Context, path string) ([]byte, error) {
					switch path {
					case "pipelines/invalid.pipeline.yaml":
						if tc.stage == "read" {
							return nil, fmt.Errorf("boom")
						}
						return []byte(`metadata:
  slug: disconnected
  name: Disconnected
steps:
  - kind: query
    slug: a
    query:
      data_source_ref: shared
      operation_type: sql
      operation_ref: read-a
  - kind: sink
    slug: x
    sink:
      input: a
      data_source_ref: shared
      operation_type: sql
      operation_ref: write-x
      idempotency_class: idempotent_with_key
  - kind: query
    slug: b
    query:
      data_source_ref: shared
      operation_type: sql
      operation_ref: read-b
  - kind: sink
    slug: y
    sink:
      input: b
      data_source_ref: shared
      operation_type: sql
      operation_ref: write-y
      idempotency_class: idempotent_with_key
`), nil
					case "pipelines/acme-users-sync.pipeline.yaml":
						return validSpecYAML, nil
					default:
						return nil, fmt.Errorf("unexpected path")
					}
				},
			}

			result, err := store.ReconcileFromGit(
				context.Background(),
				slug.Slug("acme"),
				defaultProjectSlug,
				repoStore,
				testReconcileFromGitParams(),
			)
			require.NoError(t, err)
			require.Equal(t, 2, result.Scanned)
			require.Equal(t, 1, result.Upserted)
			require.Equal(t, 0, result.Deleted)
			require.Equal(t, 1, result.Failed)
			require.Len(t, result.Failures, 1)
			require.Equal(t, "pipelines/invalid.pipeline.yaml", result.Failures[0].PipelinePath)
			require.Equal(t, tc.stage, result.Failures[0].Stage)
			require.Contains(t, result.Failures[0].Message, tc.message)
		})
	}
}

func testPipelineSpec() *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: slug.Slug("acme-users-sync"),
			Name: "ACME Users Sync",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: slug.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: slug.Slug("acme-postgres"),
					OperationType:  "sql",
					OperationRef:   slug.Slug("list-active-users"),
				},
			},
		},
	}
}

func TestPipelineStore_ReconcileFromGit_DryRunSkipsDeletesButReportsCounts(t *testing.T) {
	t.Parallel()

	var deleteSpecCallCount int
	var deleteRefCallCount int

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			listSpecsFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
				return []*pipelinesrepo.PipelineSpec{
					{Slug: slug.Slug("stale-git-1")},
					{Slug: slug.Slug("stale-git-2")},
				}, nil
			},
			deleteSpecFn: func(_ context.Context, _, _, _ slug.Slug) error {
				deleteSpecCallCount++
				return nil
			},
			deleteFn: func(_ context.Context, _, _ slug.Slug) error {
				deleteRefCallCount++
				return nil
			},
		},
	}

	repoStore := &fakeRepoStore{
		listFilesFn: func(_ context.Context, _ string) ([]repostore.FileEntry, error) {
			return []repostore.FileEntry{}, nil
		},
	}

	result, err := store.ReconcileFromGit(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		repoStore,
		ReconcileFromGitParams{
			PipelineDir: "pipelines",
			DryRun:      true,
		},
	)

	require.NoError(t, err)
	require.Equal(t, 0, result.Scanned)
	require.Equal(t, 0, result.Upserted)
	require.Equal(t, 2, result.Deleted)
	require.Len(t, result.DeletedPipelines, 2)
	require.Equal(t, "stale-git-1", result.DeletedPipelines[0].PipelineSlug)
	require.Equal(t, "stale-git-2", result.DeletedPipelines[1].PipelineSlug)
	require.Equal(t, 0, deleteSpecCallCount, "dry-run must NOT call deleteSpec")
	require.Equal(t, 0, deleteRefCallCount, "dry-run must NOT call delete")
}

func TestPipelineStore_ReconcileFromGit_DryRunSkipsUpsertsButReportsCounts(t *testing.T) {
	t.Parallel()

	validSpecYAML := []byte(`
metadata:
  slug: acme-users-sync
  name: ACME Users Sync
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
`)

	var upsertSpecCallCount int
	var upsertRefCallCount int
	var createVersionCallCount int
	var setCurrentVersionCallCount int
	var deleteSpecCallCount int
	var deleteRefCallCount int

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			upsertSpecFn: func(_ context.Context, _ slug.Slug, _ pipelinesrepo.UpsertSpecParams) (*pipelinesrepo.PipelineSpec, error) {
				upsertSpecCallCount++
				return &pipelinesrepo.PipelineSpec{}, nil
			},
			upsertFn: func(_ context.Context, _ slug.Slug, _ pipelinesrepo.UpsertParams) (*pipelinesrepo.PipelineReference, error) {
				upsertRefCallCount++
				return &pipelinesrepo.PipelineReference{}, nil
			},
			createVersionFn: func(_ context.Context, _ slug.Slug, _ pipelinesrepo.CreateVersionParams) (*pipelinesrepo.PipelineVersion, error) {
				createVersionCallCount++
				return &pipelinesrepo.PipelineVersion{ID: 1}, nil
			},
			setCurrentVersionFn: func(_ context.Context, _, _ slug.Slug, _ int64) error {
				setCurrentVersionCallCount++
				return nil
			},
			listSpecsFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
				return []*pipelinesrepo.PipelineSpec{
					{Slug: slug.Slug("acme-users-sync")},
					{Slug: slug.Slug("stale-git")},
				}, nil
			},
			deleteSpecFn: func(_ context.Context, _, _, _ slug.Slug) error {
				deleteSpecCallCount++
				return nil
			},
			deleteFn: func(_ context.Context, _, _ slug.Slug) error {
				deleteRefCallCount++
				return nil
			},
		},
	}

	repoStore := &fakeRepoStore{
		listFilesFn: func(_ context.Context, dir string) ([]repostore.FileEntry, error) {
			require.Equal(t, "pipelines", dir)
			return []repostore.FileEntry{
				{Path: "pipelines/acme-users-sync.pipeline.yml", Size: int64(len(validSpecYAML))},
			}, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			require.Equal(t, "pipelines/acme-users-sync.pipeline.yml", path)
			return validSpecYAML, nil
		},
	}

	result, err := store.ReconcileFromGit(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		repoStore,
		ReconcileFromGitParams{
			PipelineDir: "pipelines",
			DryRun:      true,
		},
	)

	require.NoError(t, err)
	require.Equal(t, 1, result.Scanned)
	require.Equal(t, 0, result.Upserted)
	require.Equal(t, 1, result.Synced)
	require.Equal(t, 1, result.Deleted)
	require.Equal(t, 0, result.Failed)
	require.Len(t, result.SyncedPipelines, 1)
	require.Equal(t, "acme-users-sync", result.SyncedPipelines[0].PipelineSlug)
	require.Len(t, result.DeletedPipelines, 1)
	require.Equal(t, "stale-git", result.DeletedPipelines[0].PipelineSlug)
	require.Equal(t, 0, upsertSpecCallCount)
	require.Equal(t, 0, upsertRefCallCount)
	require.Equal(t, 0, createVersionCallCount)
	require.Equal(t, 0, setCurrentVersionCallCount)
	require.Equal(t, 0, deleteSpecCallCount)
	require.Equal(t, 0, deleteRefCallCount)
}

func TestPipelineStore_ReconcileFromGit_DryRunReportsUnchangedWithoutLocking(t *testing.T) {
	t.Parallel()

	validSpecYAML := []byte(`
metadata:
  slug: acme-users-sync
  name: ACME Users Sync
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
`)

	var findSpecCallCount int
	var findSpecForUpdateCallCount int
	specJSON, err := json.Marshal(testPipelineSpec())
	require.NoError(t, err)
	unchangedHash := fmt.Sprintf("%x", sha256.Sum256(specJSON))

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			findSpecFn: func(_ context.Context, _, pipelineSlug slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
				findSpecCallCount++
				require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
				return &pipelinesrepo.PipelineSpec{
					Slug:     pipelineSlug,
					SpecHash: unchangedHash,
				}, nil
			},
			findSpecForUpdateFn: func(_ context.Context, _, _ slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
				findSpecForUpdateCallCount++
				return nil, xerror.Unexpected().Detail("dry-run must not acquire FOR UPDATE locks")
			},
			listSpecsFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
				return []*pipelinesrepo.PipelineSpec{
					{Slug: slug.Slug("acme-users-sync")},
				}, nil
			},
		},
	}

	repoStore := &fakeRepoStore{
		listFilesFn: func(_ context.Context, _ string) ([]repostore.FileEntry, error) {
			return []repostore.FileEntry{
				{Path: "pipelines/acme-users-sync.pipeline.yml", Size: int64(len(validSpecYAML))},
			}, nil
		},
		readFileFn: func(_ context.Context, _ string) ([]byte, error) {
			return validSpecYAML, nil
		},
	}

	result, err := store.ReconcileFromGit(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		repoStore,
		ReconcileFromGitParams{
			PipelineDir:   "pipelines",
			PipelineSlugs: []slug.Slug{"acme-users-sync"},
			DryRun:        true,
		},
	)

	require.NoError(t, err)
	require.Equal(t, 1, result.Synced)
	require.Equal(t, []ReconcileFromGitSynced{{
		PipelineSlug: "acme-users-sync",
		SourcePath:   "pipelines/acme-users-sync.pipeline.yml",
		SpecHash:     unchangedHash,
	}}, result.SyncedPipelines)
	require.Equal(t, 1, findSpecCallCount)
	require.Equal(t, 0, findSpecForUpdateCallCount)
}

func TestPipelineStore_ReconcileFromGit_DryRunReportsWouldSyncAndDeleteWithoutMutating(t *testing.T) {
	t.Parallel()

	validSpecYAML := []byte(`
metadata:
  slug: acme-users-sync
  name: ACME Users Sync
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
`)

	var upsertSpecCallCount int
	var upsertRefCallCount int
	var deleteSpecCallCount int
	var deleteRefCallCount int
	legacySourcePath := "pipelines/legacy-sync.pipeline.yaml"

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			upsertSpecFn: func(_ context.Context, _ slug.Slug, _ pipelinesrepo.UpsertSpecParams) (*pipelinesrepo.PipelineSpec, error) {
				upsertSpecCallCount++
				return &pipelinesrepo.PipelineSpec{}, nil
			},
			upsertFn: func(_ context.Context, _ slug.Slug, _ pipelinesrepo.UpsertParams) (*pipelinesrepo.PipelineReference, error) {
				upsertRefCallCount++
				return &pipelinesrepo.PipelineReference{}, nil
			},
			listSpecsFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
				return []*pipelinesrepo.PipelineSpec{
					{Slug: slug.Slug("legacy-sync"), SourcePath: &legacySourcePath},
				}, nil
			},
			deleteSpecFn: func(_ context.Context, _, _, _ slug.Slug) error {
				deleteSpecCallCount++
				return nil
			},
			deleteFn: func(_ context.Context, _, _ slug.Slug) error {
				deleteRefCallCount++
				return nil
			},
		},
	}

	repoStore := &fakeRepoStore{
		listFilesFn: func(_ context.Context, dir string) ([]repostore.FileEntry, error) {
			require.Equal(t, "pipelines", dir)
			return []repostore.FileEntry{
				{Path: "pipelines/acme-users-sync.pipeline.yaml", Size: int64(len(validSpecYAML))},
			}, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			require.Equal(t, "pipelines/acme-users-sync.pipeline.yaml", path)
			return validSpecYAML, nil
		},
	}

	result, err := store.ReconcileFromGit(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		repoStore,
		ReconcileFromGitParams{
			PipelineDir: "pipelines",
			DryRun:      true,
		},
	)

	require.NoError(t, err)
	require.Equal(t, 1, result.Scanned)
	require.Equal(t, 1, result.Synced)
	require.Equal(t, 1, result.Deleted)
	require.Len(t, result.SyncedPipelines, 1)
	require.Equal(t, "acme-users-sync", result.SyncedPipelines[0].PipelineSlug)
	require.Equal(t, "pipelines/acme-users-sync.pipeline.yaml", result.SyncedPipelines[0].SourcePath)
	require.NotEmpty(t, result.SyncedPipelines[0].SpecHash)
	require.Len(t, result.DeletedPipelines, 1)
	require.Equal(t, "legacy-sync", result.DeletedPipelines[0].PipelineSlug)
	require.Equal(t, legacySourcePath, *result.DeletedPipelines[0].PreviousSourcePath)
	require.Equal(t, 0, upsertSpecCallCount)
	require.Equal(t, 0, upsertRefCallCount)
	require.Equal(t, 0, deleteSpecCallCount)
	require.Equal(t, 0, deleteRefCallCount)
}

func TestPipelineStore_ReconcileFromGit_FilterReportsSkippedAndDoesNotDeleteUnfilteredGitRows(t *testing.T) {
	t.Parallel()

	selectedSpecYAML := []byte(`
metadata:
  slug: acme-users-sync
  name: ACME Users Sync
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
`)
	otherSpecYAML := []byte(`
metadata:
  slug: other-sync
  name: Other Sync
steps:
  - kind: query
    slug: fetch-other
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
`)

	var upserted []slug.Slug
	var deleted []slug.Slug

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			upsertSpecFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.UpsertSpecParams) (*pipelinesrepo.PipelineSpec, error) {
				upserted = append(upserted, params.Slug)
				return &pipelinesrepo.PipelineSpec{Slug: params.Slug, Name: params.Name}, nil
			},
			upsertFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.UpsertParams) (*pipelinesrepo.PipelineReference, error) {
				return &pipelinesrepo.PipelineReference{Slug: params.Slug, Name: params.Name}, nil
			},
			createVersionFn: func(_ context.Context, _ slug.Slug, params pipelinesrepo.CreateVersionParams) (*pipelinesrepo.PipelineVersion, error) {
				return &pipelinesrepo.PipelineVersion{ID: 7, PipelineSlug: params.PipelineSlug}, nil
			},
			listSpecsFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
				return []*pipelinesrepo.PipelineSpec{
					{Slug: slug.Slug("acme-users-sync")},
					{Slug: slug.Slug("stale-git")},
				}, nil
			},
			deleteSpecFn: func(_ context.Context, _, pipelineSlug, _ slug.Slug) error {
				deleted = append(deleted, pipelineSlug)
				return nil
			},
			deleteFn: func(_ context.Context, _, _ slug.Slug) error {
				return nil
			},
		},
	}

	repoStore := &fakeRepoStore{
		listFilesFn: func(_ context.Context, dir string) ([]repostore.FileEntry, error) {
			require.Equal(t, "pipelines", dir)
			return []repostore.FileEntry{
				{Path: "pipelines/acme-users-sync.pipeline.yaml", Size: int64(len(selectedSpecYAML))},
				{Path: "pipelines/other-sync.pipeline.yaml", Size: int64(len(otherSpecYAML))},
			}, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			switch path {
			case "pipelines/acme-users-sync.pipeline.yaml":
				return selectedSpecYAML, nil
			case "pipelines/other-sync.pipeline.yaml":
				return otherSpecYAML, nil
			default:
				return nil, fmt.Errorf("unexpected path %s", path)
			}
		},
	}

	result, err := store.ReconcileFromGit(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		repoStore,
		withReconcilePipelineSlugs(slug.Slug("acme-users-sync")),
	)

	require.NoError(t, err)
	require.Equal(t, []slug.Slug{slug.Slug("acme-users-sync")}, upserted)
	require.Empty(t, deleted, "filtered reconcile must not delete unrequested materialized rows")
	require.Equal(t, 1, result.Synced)
	require.Equal(t, 0, result.Deleted)
	require.Equal(t, 1, result.Skipped)
	require.Len(t, result.SkippedPipelines, 1)
	require.Equal(t, "other-sync", result.SkippedPipelines[0].PipelineSlug)
	require.Equal(t, "pipelines/other-sync.pipeline.yaml", *result.SkippedPipelines[0].SourcePath)
	require.Equal(t, "filtered_out", result.SkippedPipelines[0].Reason)
}

func TestPipelineStore_ReconcileFromGit_WithoutDryRunDeletesOrphans(t *testing.T) {
	t.Parallel()

	var deleteSpecCallCount int
	var deleteRefCallCount int

	store := &pipelineStore{
		dbtx: &fakeDBTX{},
		txm:  testutil.FakeTransactionManager{},
		repo: &fakePipelineReferenceRepo{
			listSpecsFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
				return []*pipelinesrepo.PipelineSpec{
					{Slug: slug.Slug("stale-git-1")},
					{Slug: slug.Slug("stale-git-2")},
				}, nil
			},
			deleteSpecFn: func(_ context.Context, _, _, _ slug.Slug) error {
				deleteSpecCallCount++
				return nil
			},
			deleteFn: func(_ context.Context, _, _ slug.Slug) error {
				deleteRefCallCount++
				return nil
			},
		},
	}

	repoStore := &fakeRepoStore{
		listFilesFn: func(_ context.Context, _ string) ([]repostore.FileEntry, error) {
			return []repostore.FileEntry{}, nil
		},
	}

	result, err := store.ReconcileFromGit(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		repoStore,
		testReconcileFromGitParams(),
	)

	require.NoError(t, err)
	require.Equal(t, 0, result.Scanned)
	require.Equal(t, 0, result.Upserted)
	require.Equal(t, 2, result.Deleted, "should delete 2 orphans")
	require.Equal(t, 2, deleteSpecCallCount, "must call deleteSpec for each orphan")
	require.Equal(t, 2, deleteRefCallCount, "must call delete for each orphan")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func testReconcileFromGitParams() ReconcileFromGitParams {
	sourceRef := "refs/heads/main"
	commit := "0123456789abcdef0123456789abcdef01234567"
	return ReconcileFromGitParams{
		PipelineDir:       "pipelines",
		SourceRef:         &sourceRef,
		LastAppliedCommit: &commit,
	}
}

func withReconcilePipelineSlugs(pipelineSlugs ...slug.Slug) ReconcileFromGitParams {
	params := testReconcileFromGitParams()
	params.PipelineSlugs = pipelineSlugs
	return params
}
