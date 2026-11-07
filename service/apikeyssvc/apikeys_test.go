package apikeyssvc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/apikeys"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

func TestService_CreateAPIKeyGeneratesSlugAndRetriesGeneratedCollision(t *testing.T) {
	t.Parallel()
	repo := &fakeAPIKeysRepository{}
	repo.createFn = func(_ context.Context, _ postgres.DBTX, params apikeys.CreateParams) (*apikeys.APIKey, error) {
		if repo.createCalls == 1 {
			return nil, apikeys.ErrSlugAlreadyExists
		}
		return &apikeys.APIKey{Slug: params.Slug, Name: params.Name, RoleSlug: params.RoleSlug}, nil
	}
	svc := New(testutil.FakeTransactionManager{}, repo, &fakes.RolesService{}, eventtest.NewProducer(&fakeAPIKeyEventRecorder{}))

	key, err := svc.CreateAPIKey(context.Background(), "acme", model.UserEventActor("owner"), model.NewAPIKey{Name: "Deploy key", Role: "viewer"})

	require.NoError(t, err)
	require.NotNil(t, key)
	require.Equal(t, 2, repo.createCalls)
	require.True(t, strings.HasPrefix(repo.createParams.Slug.String(), "deploy-key-"))
}

func TestService_CreateAPIKeyExplicitSlugCollisionDoesNotRetry(t *testing.T) {
	t.Parallel()
	repo := &fakeAPIKeysRepository{createFn: func(context.Context, postgres.DBTX, apikeys.CreateParams) (*apikeys.APIKey, error) {
		return nil, apikeys.ErrSlugAlreadyExists
	}}
	svc := New(testutil.FakeTransactionManager{}, repo, &fakes.RolesService{}, eventtest.NewProducer(&fakeAPIKeyEventRecorder{}))

	_, err := svc.CreateAPIKey(context.Background(), "acme", model.UserEventActor("owner"), model.NewAPIKey{Name: "Deploy key", Slug: "deploy-key", Role: "viewer"})

	require.ErrorIs(t, err, apikeys.ErrSlugAlreadyExists)
	require.Equal(t, 1, repo.createCalls)
}

func TestService_CreateAPIKeyRejectsInvalidExplicitSlug(t *testing.T) {
	t.Parallel()
	repo := &fakeAPIKeysRepository{}
	svc := New(testutil.FakeTransactionManager{}, repo, &fakes.RolesService{}, eventtest.NewProducer(&fakeAPIKeyEventRecorder{}))

	_, err := svc.CreateAPIKey(context.Background(), "acme", model.UserEventActor("owner"), model.NewAPIKey{Name: "Deploy key", Slug: "Bad Slug", Role: "viewer"})

	require.Error(t, err)
	require.False(t, repo.createCalled)
}

func TestService_CreateAPIKeyPersistsOptionalExpiration(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	repo := &fakeAPIKeysRepository{
		createFn: func(_ context.Context, _ postgres.DBTX, params apikeys.CreateParams) (*apikeys.APIKey, error) {
			return &apikeys.APIKey{
				Slug:              params.Slug,
				Name:              params.Name,
				Description:       params.Description,
				TokenPrefix:       params.TokenPrefix,
				RoleSlug:          params.RoleSlug,
				CreatedByUserSlug: params.CreatedByUserSlug,
				ExpiresAt:         params.ExpiresAt,
				CanManageAPIKeys:  params.CanManageAPIKeys,
			}, nil
		},
	}
	svc := New(
		testutil.FakeTransactionManager{},
		repo,
		&fakes.RolesService{},
		eventtest.NewProducer(&fakeAPIKeyEventRecorder{}),
	)

	key, err := svc.CreateAPIKey(
		context.Background(),
		"acme",
		model.UserEventActor("owner"),
		model.NewAPIKey{
			Name:             "Deploy key",
			Slug:             "deploy-key",
			Role:             "viewer",
			ExpiresAt:        &expiresAt,
			CanManageAPIKeys: true,
		},
	)

	require.NoError(t, err)
	require.NotNil(t, key)
	require.True(t, repo.createCalled)
	require.NotNil(t, repo.createParams.CreatedByUserSlug)
	require.Equal(t, slug.Slug("owner"), *repo.createParams.CreatedByUserSlug)
	require.True(t, repo.createParams.CanManageAPIKeys)
	require.True(t, key.CanManageAPIKeys)
	require.NotNil(t, repo.createParams.ExpiresAt)
	require.True(t, repo.createParams.ExpiresAt.Equal(expiresAt))
	require.NotNil(t, key.ExpiresAt)
	require.True(t, key.ExpiresAt.Equal(expiresAt))
}

func TestService_CreateAPIKeyRecordsAPIKeyActorWithoutDuplicatingLineage(t *testing.T) {
	t.Parallel()

	repo := &fakeAPIKeysRepository{
		createFn: func(_ context.Context, _ postgres.DBTX, params apikeys.CreateParams) (*apikeys.APIKey, error) {
			return &apikeys.APIKey{
				Slug:             params.Slug,
				Name:             params.Name,
				TokenPrefix:      params.TokenPrefix,
				RoleSlug:         params.RoleSlug,
				CanManageAPIKeys: params.CanManageAPIKeys,
			}, nil
		},
	}
	events := &fakeAPIKeyEventRecorder{}
	svc := New(
		testutil.FakeTransactionManager{},
		repo,
		&fakes.RolesService{},
		eventtest.NewProducer(events),
	)

	key, err := svc.CreateAPIKey(
		context.Background(),
		"acme",
		model.APIKeyEventActor("manager-key"),
		model.NewAPIKey{Name: "Child key", Slug: "child-key", Role: "viewer"},
	)

	require.NoError(t, err)
	require.Nil(t, repo.createParams.CreatedByUserSlug)
	require.NotNil(t, key)
	require.Len(t, events.calls, 1)
	require.Equal(t, model.EventActorTypeAPIKey, events.calls[0].ActorType)
	require.NotNil(t, events.calls[0].ActorSlug)
	require.Equal(t, slug.Slug("manager-key"), *events.calls[0].ActorSlug)
	require.Equal(t, model.EventEntityTypeAPIKey, events.calls[0].EntityType)
	require.NotNil(t, events.calls[0].EntitySlug)
	require.Equal(t, slug.Slug("child-key"), *events.calls[0].EntitySlug)
}

func TestService_UpdateAPIKeyPersistsOptionalExpiration(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	repo := &fakeAPIKeysRepository{}
	repo.findBySlugFn = func(_ context.Context, _ postgres.DBTX, _, apiKeySlug slug.Slug) (*apikeys.APIKey, error) {
		ret := &apikeys.APIKey{
			Slug:     apiKeySlug,
			Name:     "Deploy key",
			RoleSlug: "viewer",
		}
		if repo.updateCalled {
			ret.Name = repo.updateParams.Name
			ret.Description = repo.updateParams.Description
			ret.ExpiresAt = repo.updateParams.ExpiresAt
		}
		return ret, nil
	}
	repo.updateFn = func(context.Context, postgres.DBTX, apikeys.UpdateParams) error {
		return nil
	}
	svc := New(
		testutil.FakeTransactionManager{},
		repo,
		nil,
		eventtest.NewProducer(&fakeAPIKeyEventRecorder{}),
	)

	key, err := svc.UpdateAPIKey(
		context.Background(),
		"acme",
		"deploy-key",
		model.UserEventActor("owner"),
		model.UpdateAPIKey{
			Name:      "Deploy key renamed",
			ExpiresAt: &expiresAt,
		},
	)

	require.NoError(t, err)
	require.NotNil(t, key)
	require.True(t, repo.updateCalled)
	require.NotNil(t, repo.updateParams.ExpiresAt)
	require.True(t, repo.updateParams.ExpiresAt.Equal(expiresAt))
	require.NotNil(t, key.ExpiresAt)
	require.True(t, key.ExpiresAt.Equal(expiresAt))
}

func TestService_AuthenticateTreatsZeroRowMarkUsedAsInvalidToken(t *testing.T) {
	t.Parallel()

	repo := &fakeAPIKeysRepository{
		principal: &apikeys.Principal{
			Slug:    slug.Slug("automation-key"),
			OrgSlug: slug.Slug("acme"),
			Role:    slug.Slug("viewer"),
		},
		markUsedErr: apikeys.ErrNotFound,
	}
	svc := New(
		testutil.FakeTransactionManager{},
		repo,
		nil,
		eventtest.NewProducer(&fakeAPIKeyEventRecorder{}),
	)

	principal, err := svc.Authenticate(context.Background(), "token")

	require.Nil(t, principal)
	require.Error(t, err)
	require.True(t, xerror.Is(err, ErrInvalidToken))
	require.True(t, repo.markUsedCalled)
}

func TestService_AuthenticatePropagatesAPIKeyManagementCapability(t *testing.T) {
	t.Parallel()

	repo := &fakeAPIKeysRepository{principal: &apikeys.Principal{
		Slug:             "automation-key",
		OrgSlug:          "acme",
		Role:             "viewer",
		CanManageAPIKeys: true,
	}}
	svc := New(
		testutil.FakeTransactionManager{},
		repo,
		nil,
		eventtest.NewProducer(&fakeAPIKeyEventRecorder{}),
	)

	principal, err := svc.Authenticate(context.Background(), "token")

	require.NoError(t, err)
	require.NotNil(t, principal)
	require.True(t, principal.CanManageAPIKeys)
	require.True(t, repo.markUsedCalled)
}

func TestService_CreateAPIKeyEmitsCreateEventWithoutTokenMetadata(t *testing.T) {
	t.Parallel()

	orgSlug := slug.Slug("acme")
	apiKeySlug := slug.Slug("deploy-key")
	actorSlug := slug.Slug("owner")
	events := &fakeAPIKeyEventRecorder{}
	repo := &fakeAPIKeysRepository{
		createFn: func(_ context.Context, _ postgres.DBTX, params apikeys.CreateParams) (*apikeys.APIKey, error) {
			return &apikeys.APIKey{
				Slug:              params.Slug,
				Name:              params.Name,
				Description:       params.Description,
				TokenPrefix:       params.TokenPrefix,
				RoleSlug:          params.RoleSlug,
				CreatedByUserSlug: params.CreatedByUserSlug,
			}, nil
		},
	}
	svc := New(
		testutil.FakeTransactionManager{},
		repo,
		&fakes.RolesService{},
		eventtest.NewProducer(events),
	)

	key, err := svc.CreateAPIKey(
		context.Background(),
		orgSlug,
		model.UserEventActor(actorSlug),
		model.NewAPIKey{Name: "Deploy key", Slug: apiKeySlug, Role: "viewer"},
	)
	require.NoError(t, err)
	require.NotEmpty(t, key.Token)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeAPIKey, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, apiKeySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionCreate, got.Action)
	require.Equal(t, "viewer", got.Metadata["role_slug"])
	require.NotContains(t, got.Metadata, "token")
	require.NotContains(t, got.Metadata, "token_hash")
	require.NotContains(t, got.Metadata, "token_prefix")
}

func TestService_UpdateAPIKeyEmitsUpdateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := slug.Slug("acme")
	apiKeySlug := slug.Slug("deploy-key")
	actorSlug := slug.Slug("owner")
	events := &fakeAPIKeyEventRecorder{}
	repo := &fakeAPIKeysRepository{}
	repo.findBySlugFn = func(_ context.Context, _ postgres.DBTX, _, gotAPIKeySlug slug.Slug) (*apikeys.APIKey, error) {
		return &apikeys.APIKey{Slug: gotAPIKeySlug, Name: "Deploy key", RoleSlug: "viewer"}, nil
	}
	repo.updateFn = func(context.Context, postgres.DBTX, apikeys.UpdateParams) error {
		return nil
	}
	svc := New(testutil.FakeTransactionManager{}, repo, nil, eventtest.NewProducer(events))

	_, err := svc.UpdateAPIKey(
		context.Background(),
		orgSlug,
		apiKeySlug,
		model.UserEventActor(actorSlug),
		model.UpdateAPIKey{Name: "Deploy key renamed"},
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventClassSecurityAudit, got.EventClass)
	require.Equal(t, model.CurrentEventSchemaVersion, got.SchemaVersion)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeAPIKey, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, apiKeySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionUpdate, got.Action)
}

func TestService_RevokeAPIKeyEmitsRevokeEvent(t *testing.T) {
	t.Parallel()

	orgSlug := slug.Slug("acme")
	apiKeySlug := slug.Slug("deploy-key")
	actorSlug := slug.Slug("owner")
	events := &fakeAPIKeyEventRecorder{}
	repo := &fakeAPIKeysRepository{}
	repo.findBySlugFn = func(_ context.Context, _ postgres.DBTX, _, gotAPIKeySlug slug.Slug) (*apikeys.APIKey, error) {
		return &apikeys.APIKey{Slug: gotAPIKeySlug, Name: "Deploy key", RoleSlug: "viewer"}, nil
	}
	repo.revokeFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotAPIKeySlug slug.Slug) error {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, apiKeySlug, gotAPIKeySlug)
		return nil
	}
	svc := New(testutil.FakeTransactionManager{}, repo, nil, eventtest.NewProducer(events))

	err := svc.RevokeAPIKey(context.Background(), orgSlug, apiKeySlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	require.True(t, repo.revokeCalled)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeAPIKey, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, apiKeySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionRevoke, got.Action)
}

func TestService_DeleteRevokedAPIKeyDeletesMetadataAndEmitsDeleteEvent(t *testing.T) {
	t.Parallel()

	orgSlug := slug.Slug("acme")
	apiKeySlug := slug.Slug("deploy-key")
	actorSlug := slug.Slug("owner")
	revokedAt := time.Now().UTC()
	events := &fakeAPIKeyEventRecorder{}
	repo := &fakeAPIKeysRepository{}
	repo.findBySlugFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotAPIKeySlug slug.Slug) (*apikeys.APIKey, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, apiKeySlug, gotAPIKeySlug)
		return &apikeys.APIKey{Slug: gotAPIKeySlug, Name: "Deploy key", RoleSlug: "viewer", RevokedAt: &revokedAt}, nil
	}
	repo.deleteRevokedFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotAPIKeySlug slug.Slug) error {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, apiKeySlug, gotAPIKeySlug)
		return nil
	}
	svc := New(testutil.FakeTransactionManager{}, repo, nil, eventtest.NewProducer(events))

	err := svc.DeleteRevokedAPIKey(context.Background(), orgSlug, apiKeySlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	require.True(t, repo.deleteRevokedCalled)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeAPIKey, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, apiKeySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionDelete, got.Action)
	require.NotContains(t, got.Metadata, "token")
	require.NotContains(t, got.Metadata, "token_hash")
	require.NotContains(t, got.Metadata, "token_prefix")
}

func TestService_DeleteRevokedAPIKeyRejectsActiveKey(t *testing.T) {
	t.Parallel()

	repo := &fakeAPIKeysRepository{}
	repo.findBySlugFn = func(_ context.Context, _ postgres.DBTX, _, gotAPIKeySlug slug.Slug) (*apikeys.APIKey, error) {
		return &apikeys.APIKey{Slug: gotAPIKeySlug, Name: "Deploy key", RoleSlug: "viewer"}, nil
	}
	svc := New(testutil.FakeTransactionManager{}, repo, nil, eventtest.NewProducer(&fakeAPIKeyEventRecorder{}))

	err := svc.DeleteRevokedAPIKey(context.Background(), "acme", "deploy-key", model.UserEventActor("owner"))

	require.Error(t, err)
	require.True(t, xerror.Is(err, ErrAPIKeyNotRevoked))
	require.False(t, repo.deleteRevokedCalled)
}

func TestService_RotateAPIKeyEmitsRotateEventWithoutTokenMetadata(t *testing.T) {
	t.Parallel()

	orgSlug := slug.Slug("acme")
	apiKeySlug := slug.Slug("deploy-key")
	actorSlug := slug.Slug("owner")
	events := &fakeAPIKeyEventRecorder{}
	repo := &fakeAPIKeysRepository{}
	repo.rotateFn = func(_ context.Context, _ postgres.DBTX, params apikeys.RotateParams) (*apikeys.APIKey, error) {
		require.Equal(t, orgSlug, params.OrgSlug)
		require.Equal(t, apiKeySlug, params.Slug)
		return &apikeys.APIKey{
			Slug:        params.Slug,
			Name:        "Deploy key",
			TokenPrefix: params.TokenPrefix,
			RoleSlug:    "viewer",
		}, nil
	}
	svc := New(testutil.FakeTransactionManager{}, repo, nil, eventtest.NewProducer(events))

	key, err := svc.RotateAPIKey(context.Background(), orgSlug, apiKeySlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	require.NotEmpty(t, key.Token)
	require.True(t, repo.rotateCalled)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeAPIKey, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, apiKeySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionRotate, got.Action)
	require.NotContains(t, got.Metadata, "token")
	require.NotContains(t, got.Metadata, "token_hash")
	require.NotContains(t, got.Metadata, "token_prefix")
}

func TestService_APIKeyMutationsFailClosedWhenAuditEventCannotBeRecorded(t *testing.T) {
	t.Parallel()

	orgSlug := slug.Slug("acme")
	apiKeySlug := slug.Slug("deploy-key")

	tests := []struct {
		name string
		run  func(t *testing.T, svc Service, repo *fakeAPIKeysRepository) error
	}{
		{
			name: "Create",
			run: func(t *testing.T, svc Service, repo *fakeAPIKeysRepository) error {
				_, err := svc.CreateAPIKey(
					context.Background(),
					orgSlug,
					model.UserEventActor("owner"),
					model.NewAPIKey{Name: "Deploy key", Slug: apiKeySlug, Role: "viewer"},
				)
				require.True(t, repo.createCalled)
				return err
			},
		},
		{
			name: "Update",
			run: func(t *testing.T, svc Service, repo *fakeAPIKeysRepository) error {
				_, err := svc.UpdateAPIKey(
					context.Background(),
					orgSlug,
					apiKeySlug,
					model.UserEventActor("owner"),
					model.UpdateAPIKey{Name: "Deploy key renamed"},
				)
				require.True(t, repo.updateCalled)
				return err
			},
		},
		{
			name: "Revoke",
			run: func(t *testing.T, svc Service, repo *fakeAPIKeysRepository) error {
				err := svc.RevokeAPIKey(
					context.Background(),
					orgSlug,
					apiKeySlug,
					model.UserEventActor("owner"),
				)
				require.True(t, repo.revokeCalled)
				return err
			},
		},
		{
			name: "DeleteRevoked",
			run: func(t *testing.T, svc Service, repo *fakeAPIKeysRepository) error {
				err := svc.DeleteRevokedAPIKey(
					context.Background(),
					orgSlug,
					apiKeySlug,
					model.UserEventActor("owner"),
				)
				require.True(t, repo.deleteRevokedCalled)
				return err
			},
		},
		{
			name: "Rotate",
			run: func(t *testing.T, svc Service, repo *fakeAPIKeysRepository) error {
				_, err := svc.RotateAPIKey(
					context.Background(),
					orgSlug,
					apiKeySlug,
					model.UserEventActor("owner"),
				)
				require.True(t, repo.rotateCalled)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events := &fakeAPIKeyEventRecorder{err: xerror.Unexpected().Detail("event store down")}
			repo := &fakeAPIKeysRepository{}
			repo.createFn = func(_ context.Context, _ postgres.DBTX, params apikeys.CreateParams) (*apikeys.APIKey, error) {
				return &apikeys.APIKey{
					Slug:              params.Slug,
					Name:              params.Name,
					Description:       params.Description,
					TokenPrefix:       params.TokenPrefix,
					RoleSlug:          params.RoleSlug,
					CreatedByUserSlug: params.CreatedByUserSlug,
				}, nil
			}
			repo.findBySlugFn = func(_ context.Context, _ postgres.DBTX, _, gotAPIKeySlug slug.Slug) (*apikeys.APIKey, error) {
				revokedAt := time.Now().UTC()
				return &apikeys.APIKey{Slug: gotAPIKeySlug, Name: "Deploy key", RoleSlug: "viewer", RevokedAt: &revokedAt}, nil
			}
			repo.updateFn = func(context.Context, postgres.DBTX, apikeys.UpdateParams) error {
				return nil
			}
			repo.revokeFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error {
				return nil
			}
			repo.deleteRevokedFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error {
				return nil
			}
			repo.rotateFn = func(_ context.Context, _ postgres.DBTX, params apikeys.RotateParams) (*apikeys.APIKey, error) {
				return &apikeys.APIKey{
					Slug:        params.Slug,
					Name:        "Deploy key",
					TokenPrefix: params.TokenPrefix,
					RoleSlug:    "viewer",
				}, nil
			}
			svc := New(testutil.FakeTransactionManager{}, repo, &fakes.RolesService{}, eventtest.NewProducer(events))

			err := tt.run(t, svc, repo)
			require.ErrorContains(t, err, "enqueue required API key audit event")
			require.ErrorContains(t, err, "event store down")
			require.Len(t, events.calls, 1)
		})
	}
}

type fakeAPIKeyEventRecorder struct {
	err   error
	calls []eventssvc.CreateEventParams
}

func (f *fakeAPIKeyEventRecorder) CreateEvent(
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

type fakeAPIKeysRepository struct {
	principal           *apikeys.Principal
	findErr             error
	markUsedErr         error
	markUsedCalled      bool
	createFn            func(context.Context, postgres.DBTX, apikeys.CreateParams) (*apikeys.APIKey, error)
	createCalled        bool
	createCalls         int
	createParams        apikeys.CreateParams
	findBySlugFn        func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*apikeys.APIKey, error)
	updateFn            func(context.Context, postgres.DBTX, apikeys.UpdateParams) error
	updateCalled        bool
	updateParams        apikeys.UpdateParams
	revokeFn            func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error
	revokeCalled        bool
	deleteRevokedFn     func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error
	deleteRevokedCalled bool
	rotateFn            func(context.Context, postgres.DBTX, apikeys.RotateParams) (*apikeys.APIKey, error)
	rotateCalled        bool
	rotateParams        apikeys.RotateParams
}

func (f *fakeAPIKeysRepository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params apikeys.CreateParams,
) (*apikeys.APIKey, error) {
	f.createCalled = true
	f.createCalls++
	f.createParams = params
	if f.createFn != nil {
		return f.createFn(ctx, tx, params)
	}
	return nil, apikeys.ErrNotFound
}

func (f *fakeAPIKeysRepository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	apiKeySlug slug.Slug,
) (*apikeys.APIKey, error) {
	if f.findBySlugFn != nil {
		return f.findBySlugFn(ctx, tx, orgSlug, apiKeySlug)
	}
	return nil, apikeys.ErrNotFound
}

func (f *fakeAPIKeysRepository) ListByOrganization(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	*slug.Slug,
	model.ListRequest,
) ([]*apikeys.APIKey, error) {
	return nil, apikeys.ErrNotFound
}

func (f *fakeAPIKeysRepository) Update(
	ctx context.Context,
	tx postgres.DBTX,
	params apikeys.UpdateParams,
) error {
	f.updateCalled = true
	f.updateParams = params
	if f.updateFn != nil {
		return f.updateFn(ctx, tx, params)
	}
	return apikeys.ErrNotFound
}

func (f *fakeAPIKeysRepository) Revoke(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	apiKeySlug slug.Slug,
) error {
	f.revokeCalled = true
	if f.revokeFn != nil {
		return f.revokeFn(ctx, tx, orgSlug, apiKeySlug)
	}
	return apikeys.ErrNotFound
}

func (f *fakeAPIKeysRepository) DeleteRevoked(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	apiKeySlug slug.Slug,
) error {
	f.deleteRevokedCalled = true
	if f.deleteRevokedFn != nil {
		return f.deleteRevokedFn(ctx, tx, orgSlug, apiKeySlug)
	}
	return apikeys.ErrNotFound
}

func (f *fakeAPIKeysRepository) Rotate(
	ctx context.Context,
	tx postgres.DBTX,
	params apikeys.RotateParams,
) (*apikeys.APIKey, error) {
	f.rotateCalled = true
	f.rotateParams = params
	if f.rotateFn != nil {
		return f.rotateFn(ctx, tx, params)
	}
	return nil, apikeys.ErrNotFound
}

func (f *fakeAPIKeysRepository) FindActivePrincipalByTokenHash(
	context.Context,
	postgres.DBTX,
	string,
) (*apikeys.Principal, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.principal, nil
}

func (f *fakeAPIKeysRepository) MarkUsed(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
) error {
	f.markUsedCalled = true
	return f.markUsedErr
}
