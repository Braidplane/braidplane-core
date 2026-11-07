package projectssvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
)

func TestManagedPipelineBackendPreservesRuntimeCloneURL(t *testing.T) {
	t.Parallel()
	for _, provider := range []model.DataSourceType{model.DataSourceTypeGithub, model.DataSourceTypeGitlab} {
		t.Run(provider.String(), func(t *testing.T) {
			t.Parallel()
			repoURL := "https://" + provider.String() + ".com/acme/pipelines.git"
			executor := &projectBackendSnapshotExecutor{snapshot: &pipelinegit.RemoteSourceSnapshot{SourceRepo: &repoURL}}
			svc := &service{remoteGitExecutor: executor, remoteGitMaxBytes: 1024, dataSourcesService: &fakes.DataSourceService{
				FindBySlugFn: func(_ context.Context, org, source slug.Slug) (*datasourcessvc.DataSource, error) {
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("pipelines"), source)
					return &datasourcessvc.DataSource{
						Type: provider, IsActive: true,
						Config: []byte(`{"repository":{"id":"42","web_url":"https://` + provider.String() + `.com/acme/pipelines"}}`),
					}, nil
				},
				ResolveGitExecutionAuthFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitExecutionAuth, error) {
					return &datasourcessvc.GitExecutionAuth{DataSourceType: provider, ConfigJSON: []byte(`{"repo_url":"` + repoURL + `","allow_private_hosts":false}`), GitHubProposalAuthority: &datasourcessvc.GitHubProposalAuthority{Authority: model.PipelineProposalAuthority{ProviderAccountID: "2", ExecutingPrincipalDisplay: "app[bot]"}}}, nil
				},
			}}
			backend, err := svc.resolvePipelineBackend(t.Context(), "acme", "pipelines", "main", "pipelines")
			require.NoError(t, err)
			require.Equal(t, provider.String()+".com/acme/pipelines", backend.repo)
			_, _, cleanup, err := svc.openProjectBackendRepo(t.Context(), "acme", backend)
			require.NoError(t, err)
			t.Cleanup(cleanup)
			require.JSONEq(t, `{"repo_url":"`+repoURL+`","allow_private_hosts":false}`, string(executor.request.Auth.ConfigJSON))
		})
	}
}

func TestOpenProjectBackendRepoUsesIsolatedSnapshotExecutor(t *testing.T) {
	t.Parallel()

	const repo = "https://example.com/acme/pipelines.git"
	commit := "0123456789abcdef"
	executor := &projectBackendSnapshotExecutor{
		snapshot: &pipelinegit.RemoteSourceSnapshot{
			Files:             map[string][]byte{"pipelines/sync.yaml": []byte("name: sync")},
			SourceRepo:        pointerTo(repo),
			LastAppliedCommit: &commit,
		},
	}
	svc := &service{
		dataSourcesService: &projectBackendDataSources{},
		remoteGitExecutor:  executor,
		remoteGitMaxBytes:  1024,
	}

	store, lastCommit, cleanup, err := svc.openProjectBackendRepo(t.Context(), "acme", resolvedProjectPipelineBackend{
		dataSourceSlug: "pipelines",
		repo:           repo,
		trackedBranch:  "main",
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.Equal(t, commit, *lastCommit)
	require.Equal(t, slug.Slug("acme"), executor.request.OrganizationSlug)
	require.Equal(t, slug.Slug("pipelines"), executor.request.DataSourceSlug)
	require.Equal(t, "main", executor.request.Branch)
	require.Equal(t, int64(1024), executor.request.MaxBytes)
	require.Equal(t, datasourcessvc.GitExecutionAuth{
		DataSourceType:  model.DataSourceTypeGit,
		ConfigJSON:      []byte(`{"repo_url":"https://example.com/acme/pipelines.git"}`),
		CredentialsJSON: []byte(`{"token":"test"}`),
	}, executor.request.Auth)
	data, err := store.ReadFile(t.Context(), "pipelines/sync.yaml", 1024)
	require.NoError(t, err)
	require.Equal(t, "name: sync", string(data))
}

func TestGitExecutionConfigForRepositoryPreservesConnectionPolicy(t *testing.T) {
	t.Parallel()

	got, err := gitExecutionConfigForRepository(
		[]byte(`{"repo_url":"https://github.com/acme/auth-source.git","allow_private_hosts":false}`),
		"https://github.com/acme/new-pipelines.git",
	)

	require.NoError(t, err)
	require.JSONEq(t, `{"repo_url":"https://github.com/acme/new-pipelines.git","allow_private_hosts":false}`, string(got))
}

func TestRemotePipelineBackendRepositorySelection(t *testing.T) {
	for _, createdURL := range []string{"", "https://github.com/acme/new-pipelines.git"} {
		t.Run(createdURL, func(t *testing.T) {
			executor := &projectBackendSnapshotExecutor{snapshot: &pipelinegit.RemoteSourceSnapshot{SourceRepo: &createdURL}}
			svc := &service{dataSourcesService: &projectBackendDataSources{}, remoteGitExecutor: executor, remoteGitMaxBytes: 1024}
			_, _, cleanup, err := svc.openProjectBackendRepo(t.Context(), "acme", resolvedProjectPipelineBackend{
				dataSourceSlug: "pipelines", repo: "github.com/acme/new-pipelines", createdRepoURL: createdURL,
			})
			if createdURL == "" {
				require.ErrorContains(t, err, "repo changed during attach")
				require.Empty(t, executor.request.DataSourceSlug)
				return
			}
			require.NoError(t, err)
			t.Cleanup(cleanup)
			require.JSONEq(t, `{"repo_url":"`+createdURL+`"}`, string(executor.request.Auth.ConfigJSON))
		})
	}
}

func TestValidateProjectBackendAuthRepoRejectsMismatchedGitHubRepository(t *testing.T) {
	t.Parallel()

	err := validateProjectBackendAuthRepo(resolvedProjectPipelineBackend{
		provider: model.DataSourceTypeGithub,
		repo:     "github.com/acme/pipelines",
	}, &datasourcessvc.GitDataSourceAuth{RepoURL: "https://github.com/acme/other.git"})

	require.ErrorContains(t, err, "repo changed during attach")
}

func TestCreateProjectPipelineBackendRetryReturnsRegisteredRepository(t *testing.T) {
	t.Parallel()

	dataSourceSlug := slug.Slug("github-connection")
	repo := "github.com/Acme/Pipelines"
	branch := "main"
	pipelineDir := "pipelines"
	firstCommit := "0123456789abcdef"
	projectsRepo := &fakeProjectsRepo{
		findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*projects.Project, error) {
			return &projects.Project{
				Slug:                          "default",
				Name:                          "Default",
				PipelineBackendDataSourceSlug: &dataSourceSlug,
				PipelineBackendRepo:           &repo,
				PipelineBackendTrackedBranch:  &branch,
				PipelineBackendPipelineDir:    &pipelineDir,
				PipelineBackendFirstCommitSHA: &firstCommit,
			}, nil
		},
	}
	manager := &recordingRepositoryManager{}
	svc := New(
		testutil.FakeTransactionManager{},
		projectsRepo,
		nil,
		nil,
		&fixedProjectBackendDataSource{},
		nil,
		nil,
		manager,
		nil,
	)

	got, err := svc.SetProjectPipelineBackend(t.Context(), "acme", "default", model.SetProjectPipelineBackend{
		DataSourceSlug: dataSourceSlug,
		TrackedBranch:  branch,
		PipelineDir:    pipelineDir,
		ProviderOwner:  "acme",
		RepositoryName: "pipelines",
		Mode:           model.SetProjectPipelineBackendModeCreateFromTemplate,
	})

	require.NoError(t, err)
	require.Equal(t, firstCommit, *got.PipelineBackend.FirstCommitSHA)
	require.Zero(t, manager.bootstrapCalls)
}

func TestAdoptGitHubPipelineBackendFailsClosedWithoutRepositoryManager(t *testing.T) {
	t.Parallel()

	svc := New(
		testutil.FakeTransactionManager{}, &fakeProjectsRepo{}, nil, nil,
		&fixedProjectBackendDataSource{}, nil, nil, nil, nil,
	)
	_, err := svc.SetProjectPipelineBackend(t.Context(), "acme", "default", model.SetProjectPipelineBackend{
		DataSourceSlug: "github-connection", TrackedBranch: "main", PipelineDir: "pipelines",
		Mode: model.SetProjectPipelineBackendModeAdoptExisting,
	})

	require.ErrorContains(t, err, "repository validation is unavailable")
}

type projectBackendSnapshotExecutor struct {
	request  pipelinegit.RemoteSourceRequest
	snapshot *pipelinegit.RemoteSourceSnapshot
}

func (e *projectBackendSnapshotExecutor) ExecuteGitSourceSnapshot(
	_ context.Context,
	request pipelinegit.RemoteSourceRequest,
) (*pipelinegit.RemoteSourceSnapshot, error) {
	e.request = request
	return e.snapshot, nil
}

type projectBackendDataSources struct{}

func (*projectBackendDataSources) FindBySlug(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.DataSource, error) {
	return &datasourcessvc.DataSource{}, nil
}

func (*projectBackendDataSources) ResolveGitAuth(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.GitDataSourceAuth, error) {
	panic("control-plane Git auth must not be resolved")
}

func (*projectBackendDataSources) ResolveGitExecutionAuth(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.GitExecutionAuth, error) {
	return &datasourcessvc.GitExecutionAuth{
		DataSourceType:  model.DataSourceTypeGit,
		ConfigJSON:      []byte(`{"repo_url":"https://example.com/acme/pipelines.git"}`),
		CredentialsJSON: []byte(`{"token":"test"}`),
	}, nil
}

type fixedProjectBackendDataSource struct{}

func (*fixedProjectBackendDataSource) FindBySlug(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.DataSource, error) {
	return &datasourcessvc.DataSource{
		Type:     model.DataSourceTypeGithub,
		Config:   []byte(`{"repository":{"web_url":"https://github.com/acme/authorization-source"}}`),
		IsActive: true,
	}, nil
}

func (*fixedProjectBackendDataSource) ResolveGitAuth(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.GitDataSourceAuth, error) {
	panic("idempotent retry must not resolve credentials")
}

func (*fixedProjectBackendDataSource) ResolveGitExecutionAuth(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.GitExecutionAuth, error) {
	panic("idempotent retry must not execute git")
}

type recordingRepositoryManager struct {
	bootstrapCalls int
	result         *pipelinegit.RepositoryBootstrapResult
	deleted        *pipelinegit.RepositoryBootstrapOperation
}

func (*recordingRepositoryManager) ValidateRepository(context.Context, slug.Slug, slug.Slug, string) error {
	return nil
}

func (m *recordingRepositoryManager) BootstrapRepository(
	context.Context,
	pipelinegit.RepositoryBootstrapOperation,
) (*pipelinegit.RepositoryBootstrapResult, error) {
	m.bootstrapCalls++
	if m.result != nil {
		return m.result, nil
	}
	return nil, errUnexpectedProjectRepoCall
}

func (m *recordingRepositoryManager) DeleteRepository(
	_ context.Context,
	operation pipelinegit.RepositoryBootstrapOperation,
) error {
	m.deleted = &operation
	return nil
}

func pointerTo[T any](value T) *T { return &value }

func TestManagedGitHubBackendRejectsBootstrapBeforeProviderMutation(t *testing.T) {
	t.Parallel()
	manager := &recordingRepositoryManager{}
	svc := &service{repositoryManager: manager}
	_, err := svc.createProjectPipelineBackend(t.Context(), "acme", "default", model.SetProjectPipelineBackend{}, resolvedProjectPipelineBackend{
		provider: model.DataSourceTypeGithub, proposalAuthority: &datasourcessvc.GitHubProposalAuthority{},
	})
	require.ErrorIs(t, err, pipelinegit.ErrRepositoryBootstrapUnsupported)
	require.Zero(t, manager.bootstrapCalls)
}

func TestBootstrapFailureCarriesCreatedRepositoryIdentityIntoCleanup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, repositoryID, remoteURL, errorText string }{
		{"wrong repository", "42", "https://github.com/other/repository.git", "inconsistent repository provenance"},
		{"old executor missing identity", "", "https://github.com/acme/pipelines.git", "inconsistent repository provenance"},
		{"canonical casing reaches attachment", "42", "https://github.com/Acme/Pipelines.git", "attachment credential resolution failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &recordingRepositoryManager{result: &pipelinegit.RepositoryBootstrapResult{
				RepositoryID: tc.repositoryID, RemoteURL: tc.remoteURL, FirstCommitSHA: "first-sha",
			}}
			svc := &service{
				txm: testutil.FakeTransactionManager{},
				projectsRepo: &fakeProjectsRepo{findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*projects.Project, error) {
					return &projects.Project{Slug: "default"}, nil
				}},
				repositoryManager: manager,
				dataSourcesService: &fakes.DataSourceService{ResolveGitAuthFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error) {
					return nil, xerror.InvalidCredentials().Detail("attachment credential resolution failed")
				}},
			}
			_, err := svc.createProjectPipelineBackend(t.Context(), "acme", "default", model.SetProjectPipelineBackend{ProviderOwner: "acme", RepositoryName: "pipelines"}, resolvedProjectPipelineBackend{provider: model.DataSourceTypeGithub, dataSourceSlug: "github-connection", trackedBranch: "main", pipelineDir: "pipelines"})
			require.ErrorContains(t, err, tc.errorText)
			if tc.repositoryID == "" {
				require.ErrorIs(t, err, gitprovider.ErrRepositoryCleanupFailed)
				require.Nil(t, manager.deleted)
				return
			}
			require.NotNil(t, manager.deleted)
			require.Equal(t, "42", manager.deleted.RepositoryID)
			require.Equal(t, "acme", manager.deleted.Owner)
			require.Equal(t, "pipelines", manager.deleted.Name)
		})
	}
}

func TestPlainGitBackendBindsRepositoryCredentialsAndPropagatesFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		bound := false
		repo := &fakeProjectsRepo{
			setBackendFn: func(context.Context, postgres.DBTX, *projects.SetPipelineBackendParams) error { return nil },
			bindGitAuthorityFn: func(_ context.Context, _ postgres.DBTX, p projects.BindProposalAuthorityParams) error {
				bound = true
				require.Equal(t, slug.Slug("acme"), p.OrgSlug)
				require.Equal(t, slug.Slug("ops"), p.ProjectSlug)
				require.Equal(t, slug.Slug("git-source"), p.DataSourceSlug)
				require.Empty(t, p.Authority.ProviderAccountID)
				if fail {
					return xerror.Forbidden(xerror.RepoFile)
				}
				return nil
			},
			bindAuthorityFn: func(context.Context, postgres.DBTX, projects.BindProposalAuthorityParams) error {
				t.Fatal("plain Git must not bind a provider principal")
				return nil
			},
		}
		svc := &service{txm: testutil.FakeTransactionManager{}, projectsRepo: repo}
		err := svc.persistProjectPipelineBackendIdentity(t.Context(), "acme", "ops", resolvedProjectPipelineBackend{provider: model.DataSourceTypeGit, dataSourceSlug: "git-source", repo: "ssh://git.example.test/pipelines", trackedBranch: "main", pipelineDir: "pipelines"})
		require.True(t, bound)
		if fail {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
}

func TestPipelineBackendConflictExplainsRepositoryBranchOwnership(t *testing.T) {
	t.Parallel()
	repo := &fakeProjectsRepo{setBackendFn: func(context.Context, postgres.DBTX, *projects.SetPipelineBackendParams) error {
		return projects.ErrPipelineBackendTaken
	}}
	svc := &service{txm: testutil.FakeTransactionManager{}, projectsRepo: repo}
	err := svc.persistProjectPipelineBackendIdentity(t.Context(), "acme", "ops", resolvedProjectPipelineBackend{
		provider: model.DataSourceTypeGit, dataSourceSlug: "git-source", repo: "ssh://git.example.test/pipelines", trackedBranch: "main", pipelineDir: "separate-folder",
	})
	require.ErrorIs(t, err, ErrPipelineBackendTaken)
	require.Contains(t, xerror.PublicDetail(err), "already connected to another project")
	require.Contains(t, xerror.PublicDetail(err), "changing the pipeline path does not create a separate backend")
}
