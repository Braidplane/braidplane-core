package environmentssvc

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
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

var errUnexpectedEnvironmentRepoCall = fmt.Errorf("unexpected environment repository call")

type fakeEnvironmentsRepo struct {
	createFn                                  func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params *environments.CreateParams) (*environments.Environment, error)
	findBySlugFn                              func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug) (*environments.Environment, error)
	findAnyBySlugFn                           func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug) (*environments.Environment, error)
	listByOrgFn                               func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, req model.ListEnvironmentsRequest) ([]*environments.Environment, error)
	updateFn                                  func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug, params *environments.UpdateParams) (*environments.Environment, error)
	deleteFn                                  func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug, params *environments.DeleteParams) error
	enableProjectEnvironmentFn                func(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug, params *environments.EnableProjectParams) (*environments.ProjectEnvironment, error)
	listProjectEnvironmentsFn                 func(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug slug.Slug, req model.ListRequest) ([]*environments.ProjectEnvironment, error)
	findProjectEnvironmentProtectionFn        func(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug) (*model.ProjectEnvironmentProtection, error)
	findProjectEnvironmentCatalogProtectionFn func(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug) (*model.ProjectEnvironmentProtection, error)
	disableProjectEnvironmentFn               func(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug) error
	upsertRuntimeVariableFn                   func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug, params model.UpsertRuntimeVariable) (*model.RuntimeVariable, error)
	findRuntimeVariableFn                     func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug, key string) (*model.RuntimeVariable, error)
	listRuntimeVariablesFn                    func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug, req model.ListRequest) ([]*model.RuntimeVariable, error)
	deleteRuntimeVariableFn                   func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug, key string) error
	upsertEnvironmentSecretFn                 func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug, params model.UpsertEnvironmentSecret, credentialSlug slug.Slug) (*model.EnvironmentSecret, error)
	findEnvironmentSecretFn                   func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug, key string) (*model.EnvironmentSecret, error)
	listEnvironmentSecretsFn                  func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug, req model.ListRequest) ([]*model.EnvironmentSecret, error)
	listAllEnvironmentSecretsFn               func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug) ([]*model.EnvironmentSecret, error)
	deleteEnvironmentSecretsForEnvironmentFn  func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug) error
	deleteEnvironmentSecretFn                 func(ctx context.Context, tx postgres.DBTX, orgSlug, environmentSlug slug.Slug, key string) error
}

func (f *fakeEnvironmentsRepo) Create(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	params *environments.CreateParams,
) (*environments.Environment, error) {
	return f.createFn(ctx, tx, orgSlug, params)
}

func (f *fakeEnvironmentsRepo) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
) (*environments.Environment, error) {
	if f.findBySlugFn == nil {
		return &environments.Environment{
			Slug: environmentSlug,
			Name: environmentSlug.String(),
		}, nil
	}
	return f.findBySlugFn(ctx, tx, orgSlug, environmentSlug)
}

func (f *fakeEnvironmentsRepo) FindAnyBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
) (*environments.Environment, error) {
	if f.findAnyBySlugFn == nil {
		return &environments.Environment{
			Slug: environmentSlug,
			Name: environmentSlug.String(),
		}, nil
	}
	return f.findAnyBySlugFn(ctx, tx, orgSlug, environmentSlug)
}

func (f *fakeEnvironmentsRepo) ListByOrg(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListEnvironmentsRequest,
) ([]*environments.Environment, error) {
	return f.listByOrgFn(ctx, tx, orgSlug, req)
}

func (f *fakeEnvironmentsRepo) Update(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	params *environments.UpdateParams,
) (*environments.Environment, error) {
	return f.updateFn(ctx, tx, orgSlug, environmentSlug, params)
}

func (f *fakeEnvironmentsRepo) Delete(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	params *environments.DeleteParams,
) error {
	return f.deleteFn(ctx, tx, orgSlug, environmentSlug, params)
}

func (f *fakeEnvironmentsRepo) EnableProjectEnvironment(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params *environments.EnableProjectParams,
) (*environments.ProjectEnvironment, error) {
	return f.enableProjectEnvironmentFn(ctx, tx, orgSlug, projectSlug, environmentSlug, params)
}

func (f *fakeEnvironmentsRepo) ListProjectEnvironments(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	req model.ListRequest,
) ([]*environments.ProjectEnvironment, error) {
	return f.listProjectEnvironmentsFn(ctx, tx, orgSlug, projectSlug, req)
}

func (f *fakeEnvironmentsRepo) FindProjectEnvironmentProtection(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	if f.findProjectEnvironmentProtectionFn != nil {
		return f.findProjectEnvironmentProtectionFn(ctx, tx, orgSlug, projectSlug, environmentSlug)
	}
	return &model.ProjectEnvironmentProtection{EnvironmentSlug: environmentSlug}, nil
}

func (f *fakeEnvironmentsRepo) FindProjectEnvironmentCatalogProtection(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	if f.findProjectEnvironmentCatalogProtectionFn != nil {
		return f.findProjectEnvironmentCatalogProtectionFn(ctx, tx, orgSlug, projectSlug, environmentSlug)
	}
	return &model.ProjectEnvironmentProtection{EnvironmentSlug: environmentSlug}, nil
}

func (f *fakeEnvironmentsRepo) DisableProjectEnvironment(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) error {
	return f.disableProjectEnvironmentFn(ctx, tx, orgSlug, projectSlug, environmentSlug)
}

func (f *fakeEnvironmentsRepo) UpsertRuntimeVariable(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	params model.UpsertRuntimeVariable,
) (*model.RuntimeVariable, error) {
	return f.upsertRuntimeVariableFn(ctx, tx, orgSlug, environmentSlug, params)
}

func (f *fakeEnvironmentsRepo) FindRuntimeVariable(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	key string,
) (*model.RuntimeVariable, error) {
	return f.findRuntimeVariableFn(ctx, tx, orgSlug, environmentSlug, key)
}

func (f *fakeEnvironmentsRepo) ListRuntimeVariables(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) ([]*model.RuntimeVariable, error) {
	return f.listRuntimeVariablesFn(ctx, tx, orgSlug, environmentSlug, req)
}

func (f *fakeEnvironmentsRepo) DeleteRuntimeVariable(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	key string,
) error {
	return f.deleteRuntimeVariableFn(ctx, tx, orgSlug, environmentSlug, key)
}

func (f *fakeEnvironmentsRepo) UpsertEnvironmentSecret(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	params model.UpsertEnvironmentSecret,
	credentialSlug slug.Slug,
) (*model.EnvironmentSecret, error) {
	if f.upsertEnvironmentSecretFn == nil {
		return nil, errUnexpectedEnvironmentRepoCall
	}
	return f.upsertEnvironmentSecretFn(ctx, tx, orgSlug, environmentSlug, params, credentialSlug)
}

func (f *fakeEnvironmentsRepo) FindEnvironmentSecret(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	key string,
) (*model.EnvironmentSecret, error) {
	if f.findEnvironmentSecretFn == nil {
		return nil, errUnexpectedEnvironmentRepoCall
	}
	return f.findEnvironmentSecretFn(ctx, tx, orgSlug, environmentSlug, key)
}

func (f *fakeEnvironmentsRepo) ListEnvironmentSecrets(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) ([]*model.EnvironmentSecret, error) {
	if f.listEnvironmentSecretsFn == nil {
		return nil, errUnexpectedEnvironmentRepoCall
	}
	return f.listEnvironmentSecretsFn(ctx, tx, orgSlug, environmentSlug, req)
}

func (f *fakeEnvironmentsRepo) ListAllEnvironmentSecrets(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
) ([]*model.EnvironmentSecret, error) {
	if f.listAllEnvironmentSecretsFn == nil {
		return nil, errUnexpectedEnvironmentRepoCall
	}
	return f.listAllEnvironmentSecretsFn(ctx, tx, orgSlug, environmentSlug)
}

func (f *fakeEnvironmentsRepo) DeleteEnvironmentSecret(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	key string,
) error {
	if f.deleteEnvironmentSecretFn == nil {
		return errUnexpectedEnvironmentRepoCall
	}
	return f.deleteEnvironmentSecretFn(ctx, tx, orgSlug, environmentSlug, key)
}

func (f *fakeEnvironmentsRepo) DeleteEnvironmentSecretsForEnvironment(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
) error {
	if f.deleteEnvironmentSecretsForEnvironmentFn == nil {
		return errUnexpectedEnvironmentRepoCall
	}
	return f.deleteEnvironmentSecretsForEnvironmentFn(ctx, tx, orgSlug, environmentSlug)
}

type fakeEnvironmentEventRecorder struct {
	calls []eventssvc.CreateEventParams
	err   error
}

func newTestService(
	repo *fakeEnvironmentsRepo,
	eventProducer workerproducer.EventProducer,
) Service {
	return New(testutil.FakeTransactionManager{}, repo, eventProducer)
}

func (f *fakeEnvironmentEventRecorder) CreateEvent(
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
		Metadata:   params.Metadata,
	}, nil
}

func TestService_CreateDefaultEnvironmentsSeedsConfiguredDefaults(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	var created []environments.CreateParams
	svc := newTestService(&fakeEnvironmentsRepo{
		findAnyBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, _ slug.Slug) (*environments.Environment, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			return nil, environments.ErrNotFound
		},
		createFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug slug.Slug, params *environments.CreateParams) (*environments.Environment, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			created = append(created, *params)
			return &environments.Environment{Slug: params.Slug, Name: params.Name}, nil
		},
	}, nil)

	require.NoError(t, svc.CreateDefaultEnvironments(context.Background(), orgSlug))
	require.Len(t, created, 3)
	require.Equal(t, model.DefaultDevelopmentEnvironmentSlug, created[0].Slug)
	require.Equal(t, "Development", created[0].Name)
	require.Equal(t, model.EnvironmentColorBlue, created[0].Color)
	require.NotNil(t, created[0].Description)
	require.Equal(t, model.DefaultStagingEnvironmentSlug, created[1].Slug)
	require.Equal(t, "Staging", created[1].Name)
	require.Equal(t, model.EnvironmentColorAmber, created[1].Color)
	require.NotNil(t, created[1].Description)
	require.Equal(t, model.DefaultProductionEnvironmentSlug, created[2].Slug)
	require.Equal(t, "Production", created[2].Name)
	require.Equal(t, model.EnvironmentColorRed, created[2].Color)
	require.NotNil(t, created[2].Description)
}

func TestService_CreateDefaultEnvironmentsSkipsExistingRows(t *testing.T) {
	t.Parallel()

	created := false
	svc := newTestService(&fakeEnvironmentsRepo{
		findAnyBySlugFn: func(_ context.Context, _ postgres.DBTX, _, environmentSlug slug.Slug) (*environments.Environment, error) {
			return &environments.Environment{Slug: environmentSlug}, nil
		},
		createFn: func(context.Context, postgres.DBTX, slug.Slug, *environments.CreateParams) (*environments.Environment, error) {
			created = true
			return nil, fmt.Errorf("unexpected create")
		},
	}, nil)

	require.NoError(t, svc.CreateDefaultEnvironments(context.Background(), must.Slug("acme")))
	require.False(t, created)
}

func TestService_CreateDefaultEnvironmentsWrapsRepositoryError(t *testing.T) {
	t.Parallel()

	svc := newTestService(&fakeEnvironmentsRepo{
		findAnyBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*environments.Environment, error) {
			return nil, fmt.Errorf("seed failed")
		},
	}, nil)

	err := svc.CreateDefaultEnvironments(context.Background(), must.Slug("acme"))

	require.ErrorContains(t, err, "check default environment")
	require.ErrorContains(t, err, "seed failed")
}

func TestService_CreateEnvironmentAddsSuffixForGeneratedSlugCollision(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	baseSlug := must.Slug("production")
	now := time.Date(2026, 5, 25, 10, 30, 0, 0, time.UTC)
	events := &fakeEnvironmentEventRecorder{}
	svc := newTestService(&fakeEnvironmentsRepo{
		findAnyBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug) (*environments.Environment, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, baseSlug, gotEnvironmentSlug)
			return &environments.Environment{Slug: baseSlug}, nil
		},
		createFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug slug.Slug, params *environments.CreateParams) (*environments.Environment, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.NotEqual(t, baseSlug, params.Slug)
			assert.True(t, strings.HasPrefix(params.Slug.String(), "production-"))
			return &environments.Environment{
				Slug:      params.Slug,
				Name:      params.Name,
				Color:     params.Color,
				CreatedAt: now,
				UpdatedAt: now,
			}, nil
		},
	}, eventtest.NewProducer(events))

	got, err := svc.CreateEnvironment(
		context.Background(),
		orgSlug,
		model.NewEnvironment{Name: "Production", Color: model.EnvironmentColorRed},
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, strings.HasPrefix(got.Slug.String(), "production-"))
	assert.Equal(t, model.EnvironmentColorRed, got.Color)
	require.Len(t, events.calls, 1)
	assert.Equal(t, model.EventEntityTypeEnvironment, events.calls[0].EntityType)
	require.NotNil(t, events.calls[0].EntitySlug)
	assert.Equal(t, got.Slug, *events.calls[0].EntitySlug)
	assert.Equal(t, model.EventActionCreate, events.calls[0].Action)
}

func TestService_CreateEnvironmentFailsClosedWhenSlugLookupReturnsUnexpectedError(t *testing.T) {
	t.Parallel()

	svc := newTestService(&fakeEnvironmentsRepo{
		findAnyBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*environments.Environment, error) {
			return nil, xerror.Unexpected().Detail("environment slug lookup failed")
		},
		createFn: func(context.Context, postgres.DBTX, slug.Slug, *environments.CreateParams) (*environments.Environment, error) {
			return nil, errUnexpectedEnvironmentRepoCall
		},
	}, eventtest.NewProducer(nil))

	_, err := svc.CreateEnvironment(
		context.Background(),
		must.Slug("acme"),
		model.NewEnvironment{Name: "Production", Color: model.EnvironmentColorRed},
		model.UserEventActor(must.Slug("alice")),
	)
	require.ErrorContains(t, err, "environment slug lookup failed")
}

func TestService_GetEnvironmentReturnsCatalogEntry(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	now := time.Date(2026, 5, 25, 10, 30, 0, 0, time.UTC)
	svc := newTestService(&fakeEnvironmentsRepo{
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug) (*environments.Environment, error) {
			assert.Equal(t, orgSlug, gotOrgSlug)
			assert.Equal(t, environmentSlug, gotEnvironmentSlug)
			return &environments.Environment{
				Slug:      environmentSlug,
				Name:      "Production",
				Color:     model.EnvironmentColorRed,
				CreatedAt: now,
				UpdatedAt: now,
			}, nil
		},
	}, eventtest.NewProducer(nil))

	got, err := svc.GetEnvironment(context.Background(), orgSlug, environmentSlug)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, environmentSlug, got.Slug)
	assert.Equal(t, "Production", got.Name)
	assert.Equal(t, model.EnvironmentColorRed, got.Color)
}

func TestService_ListEnvironmentsKeepsIncludeDeletedAndPaginates(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	now := time.Date(2026, 5, 25, 10, 30, 0, 0, time.UTC)
	svc := newTestService(&fakeEnvironmentsRepo{
		listByOrgFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug slug.Slug, req model.ListEnvironmentsRequest) ([]*environments.Environment, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			assert.True(t, req.IncludeDeleted)
			assert.Equal(t, 3, req.Limit)
			return []*environments.Environment{
				{Slug: must.Slug("prod"), Name: "Production", Color: model.EnvironmentColorRed, CreatedAt: now, UpdatedAt: now},
				{Slug: must.Slug("staging"), Name: "Staging", Color: model.EnvironmentColorAmber, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)},
				{Slug: must.Slug("dev"), Name: "Development", Color: model.EnvironmentColorBlue, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)},
			}, nil
		},
	}, eventtest.NewProducer(nil))

	got, err := svc.ListEnvironments(
		context.Background(),
		orgSlug,
		model.ListEnvironmentsRequest{
			ListRequest:    model.ListRequest{Limit: 2},
			IncludeDeleted: true,
		},
	)
	require.NoError(t, err)
	require.Len(t, got.Items, 2)
	assert.True(t, got.HasMore)
	require.NotNil(t, got.NextCursor)
	assert.Contains(t, *got.NextCursor, "staging")
}

func TestService_UpdateEnvironmentPassesMetadataAndRecordsEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	description := "Protected production target"
	now := time.Date(2026, 5, 25, 10, 30, 0, 0, time.UTC)
	events := &fakeEnvironmentEventRecorder{}
	svc := newTestService(&fakeEnvironmentsRepo{
		updateFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug, gotEnvironmentSlug slug.Slug,
			params *environments.UpdateParams,
		) (*environments.Environment, error) {
			assert.Equal(t, orgSlug, gotOrgSlug)
			assert.Equal(t, environmentSlug, gotEnvironmentSlug)
			assert.Equal(t, "Production", params.Name)
			require.NotNil(t, params.Description)
			assert.Equal(t, description, *params.Description)
			assert.Equal(t, model.EnvironmentColorRed, params.Color)
			return &environments.Environment{
				Slug:        environmentSlug,
				Name:        params.Name,
				Description: params.Description,
				Color:       params.Color,
				CreatedAt:   now,
				UpdatedAt:   now,
			}, nil
		},
	}, eventtest.NewProducer(events))

	got, err := svc.UpdateEnvironment(
		context.Background(),
		orgSlug,
		environmentSlug,
		model.UpdateEnvironment{Name: "Production", Description: &description, Color: model.EnvironmentColorRed},
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, description, *got.Description)
	assert.Equal(t, model.EnvironmentColorRed, got.Color)
	require.Len(t, events.calls, 1)
	assert.Equal(t, model.EventEntityTypeEnvironment, events.calls[0].EntityType)
	assert.Equal(t, model.EventActionUpdate, events.calls[0].Action)
}

func TestService_DeleteEnvironmentPassesReasonAndRecordsEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	reason := "Superseded by production"
	events := &fakeEnvironmentEventRecorder{}
	var deletedSecrets bool
	svc := newTestService(&fakeEnvironmentsRepo{
		listAllEnvironmentSecretsFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug, gotEnvironmentSlug slug.Slug,
		) ([]*model.EnvironmentSecret, error) {
			assert.Equal(t, orgSlug, gotOrgSlug)
			assert.Equal(t, environmentSlug, gotEnvironmentSlug)
			return nil, nil
		},
		deleteEnvironmentSecretsForEnvironmentFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug, gotEnvironmentSlug slug.Slug,
		) error {
			assert.Equal(t, orgSlug, gotOrgSlug)
			assert.Equal(t, environmentSlug, gotEnvironmentSlug)
			deletedSecrets = true
			return nil
		},
		deleteFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug, gotEnvironmentSlug slug.Slug,
			params *environments.DeleteParams,
		) error {
			assert.Equal(t, orgSlug, gotOrgSlug)
			assert.Equal(t, environmentSlug, gotEnvironmentSlug)
			assert.True(t, deletedSecrets)
			require.NotNil(t, params.Reason)
			assert.Equal(t, reason, *params.Reason)
			return nil
		},
	}, eventtest.NewProducer(events))

	err := svc.DeleteEnvironment(
		context.Background(),
		orgSlug,
		environmentSlug,
		model.DeleteEnvironment{Reason: &reason},
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)
	assert.True(t, deletedSecrets)
	require.Len(t, events.calls, 1)
	assert.Equal(t, model.EventEntityTypeEnvironment, events.calls[0].EntityType)
	require.NotNil(t, events.calls[0].EntitySlug)
	assert.Equal(t, environmentSlug, *events.calls[0].EntitySlug)
	assert.Equal(t, model.EventActionDelete, events.calls[0].Action)
}

func TestService_ListProjectEnvironmentsPaginatesEnabledEnvironments(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	now := time.Date(2026, 5, 25, 10, 30, 0, 0, time.UTC)
	stagingEnvironmentCreatedAt := now.Add(-48 * time.Hour)
	stagingEnabledAt := now.Add(-time.Hour)
	svc := newTestService(&fakeEnvironmentsRepo{
		listProjectEnvironmentsFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotProjectSlug slug.Slug, req model.ListRequest) ([]*environments.ProjectEnvironment, error) {
			assert.Equal(t, orgSlug, gotOrgSlug)
			assert.Equal(t, projectSlug, gotProjectSlug)
			assert.Equal(t, 3, req.Limit)
			return []*environments.ProjectEnvironment{
				{
					Environment: environments.Environment{Slug: must.Slug("prod"), Name: "Production", CreatedAt: now, UpdatedAt: now},
					IsDefault:   true,
					EnabledAt:   now,
					UpdatedAt:   now,
				},
				{
					Environment: environments.Environment{Slug: must.Slug("staging"), Name: "Staging", CreatedAt: stagingEnvironmentCreatedAt, UpdatedAt: stagingEnvironmentCreatedAt},
					EnabledAt:   stagingEnabledAt,
					UpdatedAt:   stagingEnabledAt,
				},
				{
					Environment: environments.Environment{Slug: must.Slug("dev"), Name: "Development", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)},
					EnabledAt:   now.Add(-2 * time.Hour),
					UpdatedAt:   now.Add(-2 * time.Hour),
				},
			}, nil
		},
	}, eventtest.NewProducer(nil))

	got, err := svc.ListProjectEnvironments(
		context.Background(),
		orgSlug,
		projectSlug,
		model.ListRequest{Limit: 2},
	)
	require.NoError(t, err)
	require.Len(t, got.Items, 2)
	assert.True(t, got.HasMore)
	assert.True(t, got.Items[0].IsDefault)
	require.NotNil(t, got.NextCursor)
	assert.Contains(t, *got.NextCursor, "staging")
	assert.Contains(t, *got.NextCursor, stagingEnvironmentCreatedAt.Format(time.RFC3339Nano))
	assert.NotContains(t, *got.NextCursor, stagingEnabledAt.Format(time.RFC3339Nano))
}

func TestService_ListProjectEnvironmentsUpdatedCursorUsesEnvironmentUpdatedAt(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	now := time.Date(2026, 5, 25, 10, 30, 0, 0, time.UTC)
	stagingEnvironmentUpdatedAt := now.Add(-48 * time.Hour)
	stagingBindingUpdatedAt := now.Add(-time.Hour)
	svc := newTestService(&fakeEnvironmentsRepo{
		listProjectEnvironmentsFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotProjectSlug slug.Slug, req model.ListRequest) ([]*environments.ProjectEnvironment, error) {
			assert.Equal(t, orgSlug, gotOrgSlug)
			assert.Equal(t, projectSlug, gotProjectSlug)
			assert.Equal(t, model.ListSortUpdatedAt, req.SortBy)
			assert.Equal(t, 3, req.Limit)
			return []*environments.ProjectEnvironment{
				{
					Environment: environments.Environment{Slug: must.Slug("prod"), Name: "Production", CreatedAt: now, UpdatedAt: now},
					EnabledAt:   now,
					UpdatedAt:   now,
				},
				{
					Environment: environments.Environment{Slug: must.Slug("staging"), Name: "Staging", CreatedAt: now, UpdatedAt: stagingEnvironmentUpdatedAt},
					EnabledAt:   stagingBindingUpdatedAt,
					UpdatedAt:   stagingBindingUpdatedAt,
				},
				{
					Environment: environments.Environment{Slug: must.Slug("dev"), Name: "Development", CreatedAt: now, UpdatedAt: now.Add(-2 * time.Hour)},
					EnabledAt:   now.Add(-2 * time.Hour),
					UpdatedAt:   now.Add(-2 * time.Hour),
				},
			}, nil
		},
	}, eventtest.NewProducer(nil))

	got, err := svc.ListProjectEnvironments(
		context.Background(),
		orgSlug,
		projectSlug,
		model.ListRequest{Limit: 2, SortBy: model.ListSortUpdatedAt},
	)
	require.NoError(t, err)
	require.Len(t, got.Items, 2)
	require.NotNil(t, got.NextCursor)
	assert.Contains(t, *got.NextCursor, "staging")
	assert.Contains(t, *got.NextCursor, stagingEnvironmentUpdatedAt.Format(time.RFC3339Nano))
	assert.NotContains(t, *got.NextCursor, stagingBindingUpdatedAt.Format(time.RFC3339Nano))
}

func TestService_EnableProjectEnvironmentSetsDefaultAndRecordsProjectContext(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	environmentSlug := must.Slug("prod")
	now := time.Date(2026, 5, 25, 10, 30, 0, 0, time.UTC)
	events := &fakeEnvironmentEventRecorder{}
	svc := newTestService(&fakeEnvironmentsRepo{
		enableProjectEnvironmentFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug, gotProjectSlug, gotEnvironmentSlug slug.Slug,
			params *environments.EnableProjectParams,
		) (*environments.ProjectEnvironment, error) {
			assert.Equal(t, orgSlug, gotOrgSlug)
			assert.Equal(t, projectSlug, gotProjectSlug)
			assert.Equal(t, environmentSlug, gotEnvironmentSlug)
			assert.True(t, params.IsDefault)
			return &environments.ProjectEnvironment{
				Environment: environments.Environment{
					Slug:      environmentSlug,
					Name:      "Production",
					CreatedAt: now,
					UpdatedAt: now,
				},
				IsDefault: true,
				EnabledAt: now,
				UpdatedAt: now,
			}, nil
		},
	}, eventtest.NewProducer(events))

	got, err := svc.EnableProjectEnvironment(
		context.Background(),
		orgSlug,
		projectSlug,
		environmentSlug,
		model.EnableProjectEnvironment{IsDefault: true},
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.IsDefault)
	require.Len(t, events.calls, 1)
	assert.Equal(t, model.EventEntityTypeProjectEnvironment, events.calls[0].EntityType)
	entitySlug, err := projectEnvironmentEntitySlug(projectSlug, environmentSlug)
	require.NoError(t, err)
	require.NotNil(t, events.calls[0].EntitySlug)
	assert.Equal(t, entitySlug, *events.calls[0].EntitySlug)
	assert.Equal(t, model.EventActionEnable, events.calls[0].Action)
	assert.Equal(t, projectSlug.String(), events.calls[0].Metadata["project_slug"])
	assert.Equal(t, environmentSlug.String(), events.calls[0].Metadata["environment_slug"])
}

func TestService_EnableProtectedProjectEnvironmentAsDefaultRequiresCatalogConfirmation(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	environmentSlug := must.Slug("prod")
	svc := newTestService(&fakeEnvironmentsRepo{
		findProjectEnvironmentProtectionFn: func(
			_ context.Context,
			_ postgres.DBTX,
			_, _, _ slug.Slug,
		) (*model.ProjectEnvironmentProtection, error) {
			return nil, environments.ErrProjectEnvironmentNotFound
		},
		findProjectEnvironmentCatalogProtectionFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug, gotProjectSlug, gotEnvironmentSlug slug.Slug,
		) (*model.ProjectEnvironmentProtection, error) {
			assert.Equal(t, orgSlug, gotOrgSlug)
			assert.Equal(t, projectSlug, gotProjectSlug)
			assert.Equal(t, environmentSlug, gotEnvironmentSlug)
			return &model.ProjectEnvironmentProtection{
				EnvironmentSlug: environmentSlug,
				Protected:       true,
			}, nil
		},
		enableProjectEnvironmentFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			*environments.EnableProjectParams,
		) (*environments.ProjectEnvironment, error) {
			t.Fatal("enable should not run without protected confirmation")
			return nil, assert.AnError
		},
	}, eventtest.NewProducer(nil))

	got, err := svc.EnableProjectEnvironment(
		context.Background(),
		orgSlug,
		projectSlug,
		environmentSlug,
		model.EnableProjectEnvironment{IsDefault: true},
		model.UserEventActor(must.Slug("alice")),
	)

	require.Nil(t, got)
	require.True(t, xerror.Is(err, protectedenv.ErrConfirmationRequired))
}

func TestService_EnableProtectedProjectEnvironmentRequiresCatalogConfirmation(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	environmentSlug := must.Slug("prod")
	svc := newTestService(&fakeEnvironmentsRepo{
		findProjectEnvironmentProtectionFn: func(
			_ context.Context,
			_ postgres.DBTX,
			_, _, _ slug.Slug,
		) (*model.ProjectEnvironmentProtection, error) {
			return nil, environments.ErrProjectEnvironmentNotFound
		},
		findProjectEnvironmentCatalogProtectionFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug, gotProjectSlug, gotEnvironmentSlug slug.Slug,
		) (*model.ProjectEnvironmentProtection, error) {
			assert.Equal(t, orgSlug, gotOrgSlug)
			assert.Equal(t, projectSlug, gotProjectSlug)
			assert.Equal(t, environmentSlug, gotEnvironmentSlug)
			return &model.ProjectEnvironmentProtection{
				EnvironmentSlug: environmentSlug,
				Protected:       true,
			}, nil
		},
		enableProjectEnvironmentFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			*environments.EnableProjectParams,
		) (*environments.ProjectEnvironment, error) {
			t.Fatal("enable should not run without protected confirmation")
			return nil, assert.AnError
		},
	}, eventtest.NewProducer(nil))

	got, err := svc.EnableProjectEnvironment(
		context.Background(),
		orgSlug,
		projectSlug,
		environmentSlug,
		model.EnableProjectEnvironment{},
		model.UserEventActor(must.Slug("alice")),
	)

	require.Nil(t, got)
	require.True(t, xerror.Is(err, protectedenv.ErrConfirmationRequired))
}

func TestService_DisableProjectEnvironmentRecordsProjectContext(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	environmentSlug := must.Slug("prod")
	events := &fakeEnvironmentEventRecorder{}
	svc := newTestService(&fakeEnvironmentsRepo{
		disableProjectEnvironmentFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug, gotProjectSlug, gotEnvironmentSlug slug.Slug,
		) error {
			assert.Equal(t, orgSlug, gotOrgSlug)
			assert.Equal(t, projectSlug, gotProjectSlug)
			assert.Equal(t, environmentSlug, gotEnvironmentSlug)
			return nil
		},
	}, eventtest.NewProducer(events))

	err := svc.DisableProjectEnvironment(
		context.Background(),
		orgSlug,
		projectSlug,
		environmentSlug,
		nil,
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)
	assert.Equal(t, model.EventEntityTypeProjectEnvironment, events.calls[0].EntityType)
	entitySlug, err := projectEnvironmentEntitySlug(projectSlug, environmentSlug)
	require.NoError(t, err)
	require.NotNil(t, events.calls[0].EntitySlug)
	assert.Equal(t, entitySlug, *events.calls[0].EntitySlug)
	assert.Equal(t, model.EventActionDisable, events.calls[0].Action)
	assert.Equal(t, projectSlug.String(), events.calls[0].Metadata["project_slug"])
	assert.Equal(t, environmentSlug.String(), events.calls[0].Metadata["environment_slug"])
}

func TestProjectEnvironmentEntitySlugDistinguishesProjectBindings(t *testing.T) {
	t.Parallel()

	first, err := projectEnvironmentEntitySlug(must.Slug("ops"), must.Slug("prod"))
	require.NoError(t, err)
	second, err := projectEnvironmentEntitySlug(must.Slug("analytics"), must.Slug("prod"))
	require.NoError(t, err)
	third, err := projectEnvironmentEntitySlug(must.Slug("ops"), must.Slug("staging"))
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
	assert.NotEqual(t, first, third)
	assert.Contains(t, first.String(), "ops-prod")
}

func TestProjectEnvironmentEntitySlugFitsSlugLimit(t *testing.T) {
	t.Parallel()

	entitySlug, err := projectEnvironmentEntitySlug(
		must.Slug(strings.Repeat("a", 100)),
		must.Slug(strings.Repeat("b", 100)),
	)
	require.NoError(t, err)

	assert.LessOrEqual(t, len(entitySlug.String()), 100)
}
