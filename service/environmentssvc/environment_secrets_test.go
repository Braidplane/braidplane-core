package environmentssvc

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
)

var errUnexpectedCredentialServiceCall = fmt.Errorf("unexpected credential service call")

type fakeEnvironmentCredentialService struct {
	ConnectHTTPOAuthFn                  func(context.Context, credentialssvc.StartOAuthInstallParams) (*credentialssvc.ProviderConnectorInstall, error)
	AcquireHTTPOAuthTestCredentialFn    func(context.Context, credentialssvc.HTTPOAuthApp) (credentialssvc.RuntimeCredential, error)
	ConfigureHTTPOAuthAppFn             func(context.Context, credentialssvc.ConfigureHTTPOAuthAppParams) error
	ConnectHTTPOAuthClientCredentialsFn func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.CredentialMetadata, error)
	createStaticSecretFn                func(context.Context, credentialssvc.CreateStaticSecretParams) (credentialssvc.CredentialMetadata, error)
	resolveRuntimeCredentialFn          func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error)
	revokeFn                            func(context.Context, credentialssvc.RevokeParams) (credentialssvc.CredentialMetadata, error)
}

func (f *fakeEnvironmentCredentialService) CreateStaticSecret(
	ctx context.Context,
	params credentialssvc.CreateStaticSecretParams,
) (credentialssvc.CredentialMetadata, error) {
	if f.createStaticSecretFn == nil {
		return credentialssvc.CredentialMetadata{}, errUnexpectedCredentialServiceCall
	}
	return f.createStaticSecretFn(ctx, params)
}

// CreatePendingOAuthCredential rejects OAuth credential creation because environment secret tests do not support provider grants.
func (f *fakeEnvironmentCredentialService) CreatePendingOAuthCredential(
	context.Context,
	credentialssvc.CreatePendingOAuthCredentialParams,
) (credentialssvc.CredentialMetadata, error) {
	return credentialssvc.CredentialMetadata{}, errUnexpectedCredentialServiceCall
}

func (f *fakeEnvironmentCredentialService) StartOAuthInstall(
	context.Context,
	credentialssvc.StartOAuthInstallParams,
) (credentialssvc.ProviderConnectorInstall, error) {
	return credentialssvc.ProviderConnectorInstall{}, errUnexpectedCredentialServiceCall
}

func (f *fakeEnvironmentCredentialService) FindOAuthInstall(
	context.Context,
	credentialssvc.FindOAuthInstallParams,
) (credentialssvc.CredentialInstall, error) {
	return credentialssvc.CredentialInstall{}, errUnexpectedCredentialServiceCall
}

// ConsumeOAuthInstall rejects OAuth callback consumption because environment secret tests do not support provider installs.
func (f *fakeEnvironmentCredentialService) ConsumeOAuthInstall(
	context.Context,
	credentialssvc.ConsumeOAuthInstallParams,
) (credentialssvc.CredentialInstall, error) {
	return credentialssvc.CredentialInstall{}, errUnexpectedCredentialServiceCall
}

func (f *fakeEnvironmentCredentialService) CompleteOAuthCallback(
	context.Context,
	credentialssvc.CompleteOAuthCallbackParams,
) (credentialssvc.CompleteOAuthCallbackResult, error) {
	return credentialssvc.CompleteOAuthCallbackResult{}, errUnexpectedCredentialServiceCall
}

func (f *fakeEnvironmentCredentialService) ResolveRuntimeCredential(
	ctx context.Context,
	params credentialssvc.ResolveRuntimeCredentialParams,
) (credentialssvc.RuntimeCredential, error) {
	if f.resolveRuntimeCredentialFn == nil {
		return credentialssvc.RuntimeCredential{}, errUnexpectedCredentialServiceCall
	}
	return f.resolveRuntimeCredentialFn(ctx, params)
}

func (f *fakeEnvironmentCredentialService) Refresh(
	context.Context,
	credentialssvc.RefreshGrantParams,
) (credentialssvc.CredentialMetadata, error) {
	return credentialssvc.CredentialMetadata{}, errUnexpectedCredentialServiceCall
}

func (f *fakeEnvironmentCredentialService) Revoke(
	ctx context.Context,
	params credentialssvc.RevokeParams,
) (credentialssvc.CredentialMetadata, error) {
	if f.revokeFn == nil {
		return credentialssvc.CredentialMetadata{}, errUnexpectedCredentialServiceCall
	}
	return f.revokeFn(ctx, params)
}

func (f *fakeEnvironmentCredentialService) Rotate(
	context.Context,
	credentialssvc.RotateParams,
) (credentialssvc.ProviderConnectorInstall, error) {
	return credentialssvc.ProviderConnectorInstall{}, errUnexpectedCredentialServiceCall
}

func TestService_UpsertEnvironmentSecretStoresPlaintextThroughCredentialsOnly(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	credentialSlug := must.Slug("cred-prod-api-token")
	description := "GitHub token"
	secretValue := "ghp_super_secret"
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	events := &fakeEnvironmentEventRecorder{}
	credentialCalls := 0
	svc := New(
		testutil.FakeTransactionManager{},
		&fakeEnvironmentsRepo{
			findBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug) (*environments.Environment, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, environmentSlug, gotEnvironmentSlug)
				return &environments.Environment{Slug: environmentSlug, Name: "Production"}, nil
			},
			upsertEnvironmentSecretFn: func(
				_ context.Context,
				_ postgres.DBTX,
				gotOrgSlug, gotEnvironmentSlug slug.Slug,
				params model.UpsertEnvironmentSecret,
				gotCredentialSlug slug.Slug,
			) (*model.EnvironmentSecret, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, environmentSlug, gotEnvironmentSlug)
				require.Equal(t, credentialSlug, gotCredentialSlug)
				require.Equal(t, "API_TOKEN", params.Key)
				require.Equal(t, secretValue, params.Value)
				return &model.EnvironmentSecret{
					EnvironmentSlug: environmentSlug,
					Key:             params.Key,
					Description:     params.Description,
					CreatedAt:       now,
					UpdatedAt:       now,
				}, nil
			},
		},
		eventtest.NewProducer(events),
		WithCredentialService(&fakeEnvironmentCredentialService{
			createStaticSecretFn: func(_ context.Context, params credentialssvc.CreateStaticSecretParams) (credentialssvc.CredentialMetadata, error) {
				credentialCalls++
				require.Equal(t, credentialssvc.CredentialOwnerKindEnvironmentSecret, params.Owner.Kind)
				require.Equal(t, orgSlug, params.Owner.OrgSlug)
				require.Equal(t, environmentSlug, params.Owner.EnvironmentSlug)
				require.Equal(t, "API_TOKEN", params.Owner.EnvironmentSecretKey)
				require.Equal(t, credentialssvc.StaticCredentialTypeEnvironmentSecret, params.CredentialType)
				assert.JSONEq(t, `{"value":"`+secretValue+`"}`, string(params.PlaintextJSON))
				return credentialssvc.CredentialMetadata{Slug: credentialSlug}, nil
			},
		}),
	)

	got, err := svc.UpsertEnvironmentSecret(
		context.Background(),
		orgSlug,
		environmentSlug,
		model.UpsertEnvironmentSecret{Key: "API_TOKEN", Value: secretValue, Description: &description},
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "API_TOKEN", got.Key)
	require.NotNil(t, got.Description)
	require.Equal(t, description, *got.Description)
	require.Equal(t, 1, credentialCalls)
	require.Len(t, events.calls, 1)
	require.Equal(t, "API_TOKEN", events.calls[0].Metadata["key_name"])
	encodedMetadata, err := json.Marshal(events.calls[0].Metadata)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedMetadata), secretValue)
	assert.Contains(t, string(encodedMetadata), "API_TOKEN")
}

func TestService_UpsertEnvironmentSecretRequiresProtectedConfirmation(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	svc := New(
		testutil.FakeTransactionManager{},
		&fakeEnvironmentsRepo{
			findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*environments.Environment, error) {
				return &environments.Environment{Slug: environmentSlug, Name: "Production", Protected: true}, nil
			},
		},
		eventtest.NewProducer(nil),
		WithCredentialService(&fakeEnvironmentCredentialService{
			createStaticSecretFn: func(context.Context, credentialssvc.CreateStaticSecretParams) (credentialssvc.CredentialMetadata, error) {
				return credentialssvc.CredentialMetadata{}, errUnexpectedCredentialServiceCall
			},
		}),
	)

	got, err := svc.UpsertEnvironmentSecret(
		context.Background(),
		orgSlug,
		environmentSlug,
		model.UpsertEnvironmentSecret{Key: "API_TOKEN", Value: "secret"},
		model.UserEventActor(must.Slug("alice")),
	)

	require.Nil(t, got)
	require.True(t, xerror.Is(err, protectedenv.ErrConfirmationRequired))
}

func TestService_ListEnvironmentSecretsReturnsSafeMetadata(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	description := "CI token"
	svc := newTestService(
		&fakeEnvironmentsRepo{
			listEnvironmentSecretsFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug, req model.ListRequest) ([]*model.EnvironmentSecret, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, environmentSlug, gotEnvironmentSlug)
				require.Equal(t, 2, req.Limit)
				return []*model.EnvironmentSecret{
					{EnvironmentSlug: environmentSlug, Key: "API_TOKEN", Description: &description, CreatedAt: now, UpdatedAt: now},
					{EnvironmentSlug: environmentSlug, Key: "WEBHOOK_SECRET", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)},
				}, nil
			},
		},
		eventtest.NewProducer(nil),
	)

	got, err := svc.ListEnvironmentSecrets(
		context.Background(),
		orgSlug,
		environmentSlug,
		model.ListRequest{Limit: 1},
	)

	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.True(t, got.HasMore)
	require.Equal(t, "API_TOKEN", got.Items[0].Key)
	require.NotNil(t, got.Items[0].Description)
	require.Equal(t, description, *got.Items[0].Description)
}

func TestService_ListEnvironmentSecretsReturnsMissingEnvironment(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	svc := newTestService(
		&fakeEnvironmentsRepo{
			findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*environments.Environment, error) {
				return nil, environments.ErrNotFound
			},
		},
		eventtest.NewProducer(nil),
	)

	got, err := svc.ListEnvironmentSecrets(
		context.Background(),
		orgSlug,
		environmentSlug,
		model.ListRequest{Limit: 1},
	)

	require.Empty(t, got.Items)
	require.True(t, xerror.Is(err, ErrEnvironmentNotFound))
}

func TestService_DeleteEnvironmentSecretRequiresProtectedConfirmationAndRecordsSafeEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	events := &fakeEnvironmentEventRecorder{}
	deleted := false
	revoked := false
	svc := New(
		testutil.FakeTransactionManager{},
		&fakeEnvironmentsRepo{
			findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*environments.Environment, error) {
				return &environments.Environment{Slug: environmentSlug, Name: "Production", Protected: true}, nil
			},
			findEnvironmentSecretFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug, key string) (*model.EnvironmentSecret, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, environmentSlug, gotEnvironmentSlug)
				require.Equal(t, "API_TOKEN", key)
				return &model.EnvironmentSecret{EnvironmentSlug: environmentSlug, Key: key}, nil
			},
			deleteEnvironmentSecretFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug, key string) error {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, environmentSlug, gotEnvironmentSlug)
				require.Equal(t, "API_TOKEN", key)
				require.True(t, revoked)
				deleted = true
				return nil
			},
		},
		eventtest.NewProducer(events),
		WithCredentialService(&fakeEnvironmentCredentialService{
			revokeFn: func(_ context.Context, params credentialssvc.RevokeParams) (credentialssvc.CredentialMetadata, error) {
				require.Equal(t, credentialssvc.CredentialOwnerKindEnvironmentSecret, params.Owner.Kind)
				require.Equal(t, orgSlug, params.Owner.OrgSlug)
				require.Equal(t, environmentSlug, params.Owner.EnvironmentSlug)
				require.Equal(t, "API_TOKEN", params.Owner.EnvironmentSecretKey)
				revoked = true
				return credentialssvc.CredentialMetadata{Status: credentialssvc.CredentialStatusRevoked}, nil
			},
		}),
	)

	err := svc.DeleteEnvironmentSecret(
		context.Background(),
		orgSlug,
		environmentSlug,
		"API_TOKEN",
		&environmentSlug,
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.True(t, deleted)
	require.True(t, revoked)
	require.Len(t, events.calls, 1)
	require.Equal(t, model.EventActionDelete, events.calls[0].Action)
	encodedMetadata, err := json.Marshal(events.calls[0].Metadata)
	require.NoError(t, err)
	assert.Contains(t, string(encodedMetadata), "API_TOKEN")
	assert.Contains(t, string(encodedMetadata), "protected_environment_confirmed")
}

func TestService_DeleteEnvironmentRevokesEnvironmentSecretsBeforeDeletingMetadata(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	revokedKeys := make([]string, 0, 2)
	deletedSecrets := false
	svc := New(
		testutil.FakeTransactionManager{},
		&fakeEnvironmentsRepo{
			listAllEnvironmentSecretsFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug) ([]*model.EnvironmentSecret, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, environmentSlug, gotEnvironmentSlug)
				return []*model.EnvironmentSecret{
					{EnvironmentSlug: environmentSlug, Key: "API_TOKEN"},
					{EnvironmentSlug: environmentSlug, Key: "WEBHOOK_SECRET"},
				}, nil
			},
			deleteEnvironmentSecretsForEnvironmentFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug) error {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, environmentSlug, gotEnvironmentSlug)
				require.ElementsMatch(t, []string{"API_TOKEN", "WEBHOOK_SECRET"}, revokedKeys)
				deletedSecrets = true
				return nil
			},
			deleteFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug, _ *environments.DeleteParams) error {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, environmentSlug, gotEnvironmentSlug)
				require.True(t, deletedSecrets)
				return nil
			},
		},
		eventtest.NewProducer(nil),
		WithCredentialService(&fakeEnvironmentCredentialService{
			revokeFn: func(_ context.Context, params credentialssvc.RevokeParams) (credentialssvc.CredentialMetadata, error) {
				require.Equal(t, credentialssvc.CredentialOwnerKindEnvironmentSecret, params.Owner.Kind)
				require.Equal(t, orgSlug, params.Owner.OrgSlug)
				require.Equal(t, environmentSlug, params.Owner.EnvironmentSlug)
				revokedKeys = append(revokedKeys, params.Owner.EnvironmentSecretKey)
				return credentialssvc.CredentialMetadata{Status: credentialssvc.CredentialStatusRevoked}, nil
			},
		}),
	)

	err := svc.DeleteEnvironment(
		context.Background(),
		orgSlug,
		environmentSlug,
		model.DeleteEnvironment{},
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.True(t, deletedSecrets)
	require.ElementsMatch(t, []string{"API_TOKEN", "WEBHOOK_SECRET"}, revokedKeys)
}

func TestService_ResolveRuntimeSecretsUsesCredentialLifecycle(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	resolvedOwners := make([]credentialssvc.CredentialOwner, 0, 2)
	svc := New(
		testutil.FakeTransactionManager{},
		&fakeEnvironmentsRepo{
			listAllEnvironmentSecretsFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEnvironmentSlug slug.Slug) ([]*model.EnvironmentSecret, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, environmentSlug, gotEnvironmentSlug)
				return []*model.EnvironmentSecret{
					{EnvironmentSlug: environmentSlug, Key: "API_TOKEN"},
					{EnvironmentSlug: environmentSlug, Key: "WEBHOOK_SECRET"},
				}, nil
			},
		},
		eventtest.NewProducer(nil),
		WithCredentialService(&fakeEnvironmentCredentialService{
			resolveRuntimeCredentialFn: func(_ context.Context, params credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
				require.Equal(t, credentialssvc.CredentialOwnerKindEnvironmentSecret, params.Owner.Kind)
				require.Equal(t, orgSlug, params.Owner.OrgSlug)
				require.Equal(t, environmentSlug, params.Owner.EnvironmentSlug)
				resolvedOwners = append(resolvedOwners, params.Owner)
				return credentialssvc.RuntimeCredential{
					Kind:          credentialssvc.CredentialKindStaticSecret,
					PlaintextJSON: []byte(`{"value":"resolved-` + params.Owner.EnvironmentSecretKey + `"}`),
				}, nil
			},
		}),
	)

	got, err := svc.ResolveRuntimeSecrets(context.Background(), orgSlug, environmentSlug)

	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Len(t, resolvedOwners, 2)
	assert.Equal(t, model.RuntimeSecretValue{Key: "API_TOKEN", Value: "resolved-API_TOKEN"}, got[0])
	assert.Equal(t, model.RuntimeSecretValue{Key: "WEBHOOK_SECRET", Value: "resolved-WEBHOOK_SECRET"}, got[1])
}

func TestService_ResolveRuntimeSecretsReturnsMissingEnvironment(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	svc := New(
		testutil.FakeTransactionManager{},
		&fakeEnvironmentsRepo{
			findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*environments.Environment, error) {
				return nil, environments.ErrNotFound
			},
		},
		eventtest.NewProducer(nil),
		WithCredentialService(&fakeEnvironmentCredentialService{}),
	)

	got, err := svc.ResolveRuntimeSecrets(context.Background(), orgSlug, environmentSlug)

	require.Nil(t, got)
	require.True(t, xerror.Is(err, ErrEnvironmentNotFound))
}

func TestService_UpsertEnvironmentSecretRejectsInvalidKeys(t *testing.T) {
	t.Parallel()

	svc := newTestService(&fakeEnvironmentsRepo{}, eventtest.NewProducer(nil))

	for _, key := range []string{"1TOKEN", "api-token", ""} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			got, err := svc.UpsertEnvironmentSecret(
				context.Background(),
				must.Slug("acme"),
				must.Slug("prod"),
				model.UpsertEnvironmentSecret{Key: key, Value: "secret"},
				model.UserEventActor(must.Slug("alice")),
			)

			require.Nil(t, got)
			require.True(t, xerror.Is(err, ErrInvalidEnvironmentSecret) || xerror.Is(err, xerror.ValidationFailed()))
		})
	}
}

func (*fakeEnvironmentCredentialService) CreateOrganizationGitLabConnection(context.Context, credentialssvc.CreateOrganizationGitLabConnectionParams) (credentialssvc.OrganizationGitLabConnection, error) {
	return credentialssvc.OrganizationGitLabConnection{}, errUnexpectedCredentialServiceCall
}

func (*fakeEnvironmentCredentialService) GetOrganizationGitLabConnection(context.Context, slug.Slug) (credentialssvc.OrganizationGitLabConnection, error) {
	return credentialssvc.OrganizationGitLabConnection{}, errUnexpectedCredentialServiceCall
}

func (*fakeEnvironmentCredentialService) ListOrganizationGitLabRepositories(context.Context, slug.Slug) ([]credentialssvc.ProviderRepository, error) {
	return nil, errUnexpectedCredentialServiceCall
}

func (*fakeEnvironmentCredentialService) AttachOrganizationGitLabConnection(context.Context, credentialssvc.AttachOrganizationGitLabConnectionParams) error {
	return errUnexpectedCredentialServiceCall
}

func (f *fakeEnvironmentCredentialService) ConfigureHTTPOAuthApp(ctx context.Context, params credentialssvc.ConfigureHTTPOAuthAppParams) error {
	if f.ConfigureHTTPOAuthAppFn == nil {
		return xerror.Unexpected().Detail("unexpected ConfigureHTTPOAuthApp call")
	}
	return f.ConfigureHTTPOAuthAppFn(ctx, params)
}

func (f *fakeEnvironmentCredentialService) ConnectHTTPOAuthClientCredentials(ctx context.Context, params credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.CredentialMetadata, error) {
	if f.ConnectHTTPOAuthClientCredentialsFn == nil {
		return credentialssvc.CredentialMetadata{}, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuthClientCredentials call")
	}
	return f.ConnectHTTPOAuthClientCredentialsFn(ctx, params)
}

func (f *fakeEnvironmentCredentialService) ConnectHTTPOAuth(ctx context.Context, p credentialssvc.StartOAuthInstallParams) (*credentialssvc.ProviderConnectorInstall, error) {
	if f.ConnectHTTPOAuthFn == nil {
		return nil, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuth call")
	}
	return f.ConnectHTTPOAuthFn(ctx, p)
}

func (f *fakeEnvironmentCredentialService) AcquireHTTPOAuthTestCredential(ctx context.Context, p credentialssvc.HTTPOAuthApp) (credentialssvc.RuntimeCredential, error) {
	if f.AcquireHTTPOAuthTestCredentialFn == nil {
		return credentialssvc.RuntimeCredential{}, xerror.Unexpected().Detail("unexpected AcquireHTTPOAuthTestCredential call")
	}
	return f.AcquireHTTPOAuthTestCredentialFn(ctx, p)
}

func (*fakeEnvironmentCredentialService) CreateNotificationConnection(context.Context, credentialssvc.CreateNotificationConnectionParams) (credentialssvc.NotificationConnection, error) {
	return credentialssvc.NotificationConnection{}, credentialssvc.ErrCredentialNotFound
}

func (*fakeEnvironmentCredentialService) ListNotificationConnections(context.Context, slug.Slug) ([]credentialssvc.NotificationConnection, error) {
	return nil, credentialssvc.ErrCredentialNotFound
}

func (*fakeEnvironmentCredentialService) ConnectNotificationConnection(context.Context, credentialssvc.CredentialOwner, model.EventActor) (credentialssvc.ProviderConnectorInstall, error) {
	return credentialssvc.ProviderConnectorInstall{}, credentialssvc.ErrCredentialNotFound
}
