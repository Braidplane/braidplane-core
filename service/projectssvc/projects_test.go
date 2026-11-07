package projectssvc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

var errUnexpectedProjectRepoCall = fmt.Errorf("unexpected project repository call")

type fakeProjectsRepo struct {
	bindGitAuthorityFn func(context.Context, postgres.DBTX, projects.BindProposalAuthorityParams) error
	revokeAuthorityFn  func(context.Context, postgres.DBTX, projects.BindProposalAuthorityParams) error
	bindAuthorityFn    func(context.Context, postgres.DBTX, projects.BindProposalAuthorityParams) error
	createFn           func(ctx context.Context, tx postgres.DBTX, params *projects.CreateParams) (*projects.Project, error)
	findBySlugFn       func(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug slug.Slug) (*projects.Project, error)
	listByOrgFn        func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, req model.ListRequest) ([]*projects.Project, error)
	updateFn           func(ctx context.Context, tx postgres.DBTX, params *projects.UpdateParams) error
	setBackendFn       func(ctx context.Context, tx postgres.DBTX, params *projects.SetPipelineBackendParams) error
	clearBackendFn     func(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug slug.Slug) error
	deleteFn           func(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug slug.Slug) error
}

func (f *fakeProjectsRepo) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params *projects.CreateParams,
) (*projects.Project, error) {
	return f.createFn(ctx, tx, params)
}

func (f *fakeProjectsRepo) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
) (*projects.Project, error) {
	return f.findBySlugFn(ctx, tx, orgSlug, projectSlug)
}

func (f *fakeProjectsRepo) FindBySlugForUpdate(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
) (*projects.Project, error) {
	return f.findBySlugFn(ctx, tx, orgSlug, projectSlug)
}

func (f *fakeProjectsRepo) ListByOrg(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*projects.Project, error) {
	return f.listByOrgFn(ctx, tx, orgSlug, req)
}

func (f *fakeProjectsRepo) Update(
	ctx context.Context,
	tx postgres.DBTX,
	params *projects.UpdateParams,
) error {
	return f.updateFn(ctx, tx, params)
}

func (f *fakeProjectsRepo) SetPipelineBackend(
	ctx context.Context,
	tx postgres.DBTX,
	params *projects.SetPipelineBackendParams,
) error {
	if f.setBackendFn != nil {
		return f.setBackendFn(ctx, tx, params)
	}
	return errUnexpectedProjectRepoCall
}

func (f *fakeProjectsRepo) RevokeChangedProposalAuthority(ctx context.Context, tx postgres.DBTX, params projects.BindProposalAuthorityParams) error {
	if f.revokeAuthorityFn != nil {
		return f.revokeAuthorityFn(ctx, tx, params)
	}
	return nil
}

func (f *fakeProjectsRepo) BindGitBranchProposalAuthority(ctx context.Context, tx postgres.DBTX, params projects.BindProposalAuthorityParams) error {
	if f.bindGitAuthorityFn != nil {
		return f.bindGitAuthorityFn(ctx, tx, params)
	}
	return nil
}

func (f *fakeProjectsRepo) BindGitHubProposalAuthority(ctx context.Context, tx postgres.DBTX, params projects.BindProposalAuthorityParams) error {
	if f.bindAuthorityFn != nil {
		return f.bindAuthorityFn(ctx, tx, params)
	}
	return errUnexpectedProjectRepoCall
}

func (f *fakeProjectsRepo) ClearPipelineBackend(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
) error {
	if f.clearBackendFn != nil {
		return f.clearBackendFn(ctx, tx, orgSlug, projectSlug)
	}
	return errUnexpectedProjectRepoCall
}

func (f *fakeProjectsRepo) Delete(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
) error {
	return f.deleteFn(ctx, tx, orgSlug, projectSlug)
}

type fakeProjectEventRecorder struct {
	calls []eventssvc.CreateEventParams
}

func newTestService(repo *fakeProjectsRepo, eventProducer workerproducer.EventProducer) Service {
	return New(testutil.FakeTransactionManager{}, repo, nil, nil, nil, nil, nil, nil, eventProducer)
}

type fakeDefaultProjectEnvironmentRepo struct {
	findBySlugFn func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*environments.Environment, error)
	enableFn     func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, *environments.EnableProjectParams) (*environments.ProjectEnvironment, error)
}

func (f *fakeDefaultProjectEnvironmentRepo) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
) (*environments.Environment, error) {
	if f.findBySlugFn != nil {
		return f.findBySlugFn(ctx, tx, orgSlug, environmentSlug)
	}
	return nil, errUnexpectedProjectRepoCall
}

func (f *fakeDefaultProjectEnvironmentRepo) EnableProjectEnvironment(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params *environments.EnableProjectParams,
) (*environments.ProjectEnvironment, error) {
	if f.enableFn != nil {
		return f.enableFn(ctx, tx, orgSlug, projectSlug, environmentSlug, params)
	}
	return nil, errUnexpectedProjectRepoCall
}

func (f *fakeProjectEventRecorder) CreateEvent(
	_ context.Context,
	params eventssvc.CreateEventParams,
) (*model.Event, error) {
	f.calls = append(f.calls, params)
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

func TestService_CreateProject(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	baseSlug := must.Slug("ops-workspace")
	now := time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name       string
		newProject model.NewProject
		findBySlug func(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug slug.Slug) (*projects.Project, error)
		create     func(ctx context.Context, tx postgres.DBTX, params *projects.CreateParams) (*projects.Project, error)
		assertErr  func(t *testing.T, err error)
		assertGot  func(t *testing.T, project *model.Project)
	}{
		{
			name: "CreatesProjectWithGeneratedSlug",
			newProject: model.NewProject{
				Name: "Ops Workspace",
			},
			findBySlug: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotProjectSlug slug.Slug) (*projects.Project, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, baseSlug, gotProjectSlug)
				return nil, projects.ErrNotFound
			},
			create: func(_ context.Context, _ postgres.DBTX, params *projects.CreateParams) (*projects.Project, error) {
				require.Equal(t, orgSlug, params.OrgSlug)
				require.Equal(t, baseSlug, params.Slug)
				require.Equal(t, "Ops Workspace", params.Name)
				return &projects.Project{
					Slug:      params.Slug,
					Name:      params.Name,
					CreatedAt: now,
					UpdatedAt: now,
				}, nil
			},
			assertGot: func(t *testing.T, project *model.Project) {
				require.NotNil(t, project)
				assert.Equal(t, baseSlug, project.Slug)
				assert.Equal(t, "Ops Workspace", project.Name)
			},
		},
		{
			name: "AddsRandomSuffixForGeneratedSlugCollision",
			newProject: model.NewProject{
				Name: "Ops Workspace",
			},
			findBySlug: func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*projects.Project, error) {
				return &projects.Project{Slug: baseSlug}, nil
			},
			create: func(_ context.Context, _ postgres.DBTX, params *projects.CreateParams) (*projects.Project, error) {
				require.Equal(t, orgSlug, params.OrgSlug)
				require.NotEqual(t, baseSlug, params.Slug)
				assert.True(t, strings.HasPrefix(params.Slug.String(), "ops-workspace-"))
				return &projects.Project{
					Slug:      params.Slug,
					Name:      params.Name,
					CreatedAt: now,
					UpdatedAt: now,
				}, nil
			},
			assertGot: func(t *testing.T, project *model.Project) {
				require.NotNil(t, project)
				assert.True(t, strings.HasPrefix(project.Slug.String(), "ops-workspace-"))
			},
		},
		{
			name: "FailsClosedWhenSlugLookupReturnsUnexpectedError",
			newProject: model.NewProject{
				Name: "Ops Workspace",
			},
			findBySlug: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*projects.Project, error) {
				return nil, xerror.Unexpected().Detail("project slug lookup failed")
			},
			create: func(context.Context, postgres.DBTX, *projects.CreateParams) (*projects.Project, error) {
				return nil, xerror.Unexpected().Detail("unexpected create project call")
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "project slug lookup failed")
			},
		},
		{
			name:       "ReturnsValidationErrorWhenNameIsEmpty",
			newProject: model.NewProject{},
			findBySlug: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*projects.Project, error) {
				return nil, errUnexpectedProjectRepoCall
			},
			create: func(context.Context, postgres.DBTX, *projects.CreateParams) (*projects.Project, error) {
				return nil, errUnexpectedProjectRepoCall
			},
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorContains(t, err, "validation failed")
			},
		},
		{
			name: "PropagatesDuplicateSlugFromRepository",
			newProject: model.NewProject{
				Name: "Ops Workspace",
				Slug: baseSlug,
			},
			findBySlug: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*projects.Project, error) {
				return nil, projects.ErrNotFound
			},
			create: func(context.Context, postgres.DBTX, *projects.CreateParams) (*projects.Project, error) {
				return nil, projects.ErrSlugTaken
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, projects.ErrSlugTaken)
				require.ErrorContains(t, err, "create project")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newTestService(&fakeProjectsRepo{
				createFn:     tt.create,
				findBySlugFn: tt.findBySlug,
				listByOrgFn: func(context.Context, postgres.DBTX, slug.Slug, model.ListRequest) ([]*projects.Project, error) {
					return nil, errUnexpectedProjectRepoCall
				},
				updateFn: func(context.Context, postgres.DBTX, *projects.UpdateParams) error {
					return errUnexpectedProjectRepoCall
				},
			}, eventtest.NewProducer(&fakeProjectEventRecorder{}))

			got, err := svc.CreateProject(
				context.Background(),
				orgSlug,
				tt.newProject,
				model.UserEventActor(must.Slug("alice")),
			)
			if tt.assertErr != nil {
				tt.assertErr(t, err)
				return
			}

			require.NoError(t, err)
			tt.assertGot(t, got)
		})
	}
}

func TestService_CreateProjectEnablesDevelopmentDefaultEnvironment(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	now := time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC)
	enabled := false

	svc := New(
		testutil.FakeTransactionManager{},
		&fakeProjectsRepo{
			findBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotProjectSlug slug.Slug) (*projects.Project, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, projectSlug, gotProjectSlug)
				return nil, projects.ErrNotFound
			},
			createFn: func(_ context.Context, _ postgres.DBTX, params *projects.CreateParams) (*projects.Project, error) {
				return &projects.Project{
					Slug:      params.Slug,
					Name:      params.Name,
					CreatedAt: now,
					UpdatedAt: now,
				}, nil
			},
		},
		nil,
		&fakeDefaultProjectEnvironmentRepo{
			findBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug) (*environments.Environment, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, model.DefaultDevelopmentEnvironmentSlug, gotEnvironmentSlug)
				return &environments.Environment{
					Slug:      gotEnvironmentSlug,
					Name:      "Development",
					CreatedAt: now,
					UpdatedAt: now,
				}, nil
			},
			enableFn: func(
				_ context.Context,
				_ postgres.DBTX,
				gotOrgSlug, gotProjectSlug, gotEnvironmentSlug slug.Slug,
				params *environments.EnableProjectParams,
			) (*environments.ProjectEnvironment, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, projectSlug, gotProjectSlug)
				require.Equal(t, model.DefaultDevelopmentEnvironmentSlug, gotEnvironmentSlug)
				require.True(t, params.IsDefault)
				enabled = true
				return &environments.ProjectEnvironment{}, nil
			},
		},
		nil,
		nil,
		nil,
		nil,
		eventtest.NewProducer(&fakeProjectEventRecorder{}),
	)

	_, err := svc.CreateProject(
		context.Background(),
		orgSlug,
		model.NewProject{Name: "Ops", Slug: projectSlug},
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.True(t, enabled)
}

func TestService_CreateProjectSkipsMissingDevelopmentDefaultEnvironment(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	now := time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC)

	svc := New(
		testutil.FakeTransactionManager{},
		&fakeProjectsRepo{
			findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*projects.Project, error) {
				return nil, projects.ErrNotFound
			},
			createFn: func(_ context.Context, _ postgres.DBTX, params *projects.CreateParams) (*projects.Project, error) {
				return &projects.Project{
					Slug:      params.Slug,
					Name:      params.Name,
					CreatedAt: now,
					UpdatedAt: now,
				}, nil
			},
		},
		nil,
		&fakeDefaultProjectEnvironmentRepo{
			findBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug) (*environments.Environment, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, model.DefaultDevelopmentEnvironmentSlug, gotEnvironmentSlug)
				return nil, environments.ErrNotFound
			},
			enableFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, *environments.EnableProjectParams) (*environments.ProjectEnvironment, error) {
				return nil, fmt.Errorf("default project environment should not be enabled")
			},
		},
		nil,
		nil,
		nil,
		nil,
		eventtest.NewProducer(&fakeProjectEventRecorder{}),
	)

	got, err := svc.CreateProject(
		context.Background(),
		orgSlug,
		model.NewProject{Name: "Ops", Slug: projectSlug},
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.Equal(t, projectSlug, got.Slug)
}

func TestService_ListProjectsScopesByOrganization(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	now := time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC)

	svc := newTestService(&fakeProjectsRepo{
		createFn: func(context.Context, postgres.DBTX, *projects.CreateParams) (*projects.Project, error) {
			return nil, errUnexpectedProjectRepoCall
		},
		findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*projects.Project, error) {
			return nil, errUnexpectedProjectRepoCall
		},
		listByOrgFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug slug.Slug, req model.ListRequest) ([]*projects.Project, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, 3, req.Limit)
			return []*projects.Project{
				{Slug: "default", Name: "Default project", CreatedAt: now, UpdatedAt: now},
				{Slug: "ops", Name: "Ops", CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)},
				{Slug: "support", Name: "Support", CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-2 * time.Minute)},
			}, nil
		},
		updateFn: func(context.Context, postgres.DBTX, *projects.UpdateParams) error {
			return errUnexpectedProjectRepoCall
		},
	}, nil)

	got, err := svc.ListProjects(context.Background(), orgSlug, model.ListRequest{Limit: 2})

	require.NoError(t, err)
	require.Len(t, got.Items, 2)
	assert.True(t, got.HasMore)
	assert.Equal(t, must.Slug("default"), got.Items[0].Slug)
	assert.NotNil(t, got.NextCursor)
}

func TestService_CreateProjectEmitsCreateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	actorSlug := must.Slug("etl-worker")
	actor := model.APIKeyEventActor(actorSlug)
	now := time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC)
	events := &fakeProjectEventRecorder{}

	svc := newTestService(&fakeProjectsRepo{
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotProjectSlug slug.Slug) (*projects.Project, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			return nil, projects.ErrNotFound
		},
		createFn: func(_ context.Context, _ postgres.DBTX, params *projects.CreateParams) (*projects.Project, error) {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, projectSlug, params.Slug)
			return &projects.Project{
				Slug:      params.Slug,
				Name:      params.Name,
				CreatedAt: now,
				UpdatedAt: now,
			}, nil
		},
	}, eventtest.NewProducer(events))

	_, err := svc.CreateProject(
		context.Background(),
		orgSlug,
		model.NewProject{Name: "Ops", Slug: projectSlug},
		actor,
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeAPIKey, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeProject, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, projectSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionCreate, got.Action)
	require.Empty(t, got.Metadata)
}

func TestService_ListProjectsPropagatesRepositoryError(t *testing.T) {
	t.Parallel()

	svc := newTestService(&fakeProjectsRepo{
		listByOrgFn: func(context.Context, postgres.DBTX, slug.Slug, model.ListRequest) ([]*projects.Project, error) {
			return nil, xerror.Unexpected().Detail("list projects failed")
		},
	}, nil)

	_, err := svc.ListProjects(context.Background(), must.Slug("acme"), model.ListRequest{Limit: 2})

	require.ErrorContains(t, err, "list projects failed")
}

func TestService_GetProjectScopesByOrganization(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("default")
	now := time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC)

	svc := newTestService(&fakeProjectsRepo{
		createFn: func(context.Context, postgres.DBTX, *projects.CreateParams) (*projects.Project, error) {
			return nil, errUnexpectedProjectRepoCall
		},
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotProjectSlug slug.Slug) (*projects.Project, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			return &projects.Project{Slug: projectSlug, Name: "Default project", CreatedAt: now, UpdatedAt: now}, nil
		},
		listByOrgFn: func(context.Context, postgres.DBTX, slug.Slug, model.ListRequest) ([]*projects.Project, error) {
			return nil, errUnexpectedProjectRepoCall
		},
		updateFn: func(context.Context, postgres.DBTX, *projects.UpdateParams) error {
			return errUnexpectedProjectRepoCall
		},
	}, nil)

	got, err := svc.GetProject(context.Background(), orgSlug, projectSlug)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, projectSlug, got.Slug)
}

func TestService_GetProjectPropagatesRepositoryError(t *testing.T) {
	t.Parallel()

	svc := newTestService(&fakeProjectsRepo{
		findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*projects.Project, error) {
			return nil, xerror.Unexpected().Detail("get project failed")
		},
	}, nil)

	_, err := svc.GetProject(context.Background(), must.Slug("acme"), must.Slug("default"))

	require.ErrorContains(t, err, "get project failed")
}

func TestService_UpdateProjectUsesFullReplacementAndOrgScope(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("default")
	now := time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC)

	svc := newTestService(&fakeProjectsRepo{
		createFn: func(context.Context, postgres.DBTX, *projects.CreateParams) (*projects.Project, error) {
			return nil, errUnexpectedProjectRepoCall
		},
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotProjectSlug slug.Slug) (*projects.Project, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			return &projects.Project{Slug: projectSlug, Name: "Renamed project", CreatedAt: now, UpdatedAt: now}, nil
		},
		listByOrgFn: func(context.Context, postgres.DBTX, slug.Slug, model.ListRequest) ([]*projects.Project, error) {
			return nil, errUnexpectedProjectRepoCall
		},
		updateFn: func(_ context.Context, _ postgres.DBTX, params *projects.UpdateParams) error {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, projectSlug, params.Slug)
			require.Equal(t, "Renamed project", params.Name)
			return nil
		},
	}, eventtest.NewProducer(&fakeProjectEventRecorder{}))

	got, err := svc.UpdateProject(
		context.Background(),
		orgSlug,
		projectSlug,
		model.UpdateProject{
			Name: "Renamed project",
		},
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Renamed project", got.Name)
}

func TestService_UpdateProjectReturnsValidationErrorWhenNameIsEmpty(t *testing.T) {
	t.Parallel()

	svc := newTestService(&fakeProjectsRepo{
		updateFn: func(context.Context, postgres.DBTX, *projects.UpdateParams) error {
			return errUnexpectedProjectRepoCall
		},
	}, nil)

	_, err := svc.UpdateProject(
		context.Background(),
		must.Slug("acme"),
		must.Slug("default"),
		model.UpdateProject{},
		model.UserEventActor(must.Slug("alice")),
	)

	require.ErrorContains(t, err, "validation failed")
}

func TestService_UpdateProjectPropagatesRepositoryError(t *testing.T) {
	t.Parallel()

	svc := newTestService(&fakeProjectsRepo{
		updateFn: func(context.Context, postgres.DBTX, *projects.UpdateParams) error {
			return xerror.Unexpected().Detail("update project failed")
		},
	}, nil)

	_, err := svc.UpdateProject(
		context.Background(),
		must.Slug("acme"),
		must.Slug("default"),
		model.UpdateProject{Name: "Renamed project"},
		model.UserEventActor(must.Slug("alice")),
	)

	require.ErrorContains(t, err, "update project failed")
}

func TestService_UpdateProjectEmitsUpdateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	actorSlug := must.Slug("alice")
	now := time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC)
	events := &fakeProjectEventRecorder{}

	svc := newTestService(&fakeProjectsRepo{
		updateFn: func(_ context.Context, _ postgres.DBTX, params *projects.UpdateParams) error {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, projectSlug, params.Slug)
			return nil
		},
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotProjectSlug slug.Slug) (*projects.Project, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			return &projects.Project{
				Slug:      projectSlug,
				Name:      "Renamed project",
				CreatedAt: now,
				UpdatedAt: now,
			}, nil
		},
	}, eventtest.NewProducer(events))

	_, err := svc.UpdateProject(
		context.Background(),
		orgSlug,
		projectSlug,
		model.UpdateProject{Name: "Renamed project"},
		model.UserEventActor(actorSlug),
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeProject, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, projectSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionUpdate, got.Action)
	require.Empty(t, got.Metadata)
}

func TestService_DeleteProjectHardDeletesByOrganizationScope(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")

	svc := newTestService(&fakeProjectsRepo{
		deleteFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotProjectSlug slug.Slug) error {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			return nil
		},
	}, eventtest.NewProducer(&fakeProjectEventRecorder{}))

	err := svc.DeleteProject(
		context.Background(),
		orgSlug,
		projectSlug,
		model.DeleteProjectOptions{},
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
}

func TestService_DeleteProjectEmitsDeleteEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	actorSlug := must.Slug("alice")
	events := &fakeProjectEventRecorder{}

	svc := newTestService(&fakeProjectsRepo{
		deleteFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotProjectSlug slug.Slug) error {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			return nil
		},
	}, eventtest.NewProducer(events))

	err := svc.DeleteProject(
		context.Background(),
		orgSlug,
		projectSlug,
		model.DeleteProjectOptions{},
		model.UserEventActor(actorSlug),
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeProject, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, projectSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionDelete, got.Action)
	require.Empty(t, got.Metadata)
}

func TestService_DeleteProjectPropagatesRepositoryError(t *testing.T) {
	t.Parallel()

	svc := newTestService(&fakeProjectsRepo{
		deleteFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error {
			return xerror.Unexpected().Detail("delete project failed")
		},
	}, nil)

	err := svc.DeleteProject(
		context.Background(),
		must.Slug("acme"),
		must.Slug("ops"),
		model.DeleteProjectOptions{},
		model.UserEventActor(must.Slug("alice")),
	)

	require.ErrorContains(t, err, "delete project failed")
}
