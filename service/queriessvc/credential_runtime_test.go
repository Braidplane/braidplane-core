package queriessvc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repoq "github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type runtimeCredentialExecutor struct {
	called      *bool
	config      *[]byte
	credentials *[]byte
}

func (e runtimeCredentialExecutor) ExecuteConnector(
	ctx context.Context,
	request ConnectorExecutionRequest,
) (*result.ExecutionResult, error) {
	if e.called != nil {
		*e.called = true
	}
	if e.config != nil {
		*e.config = append([]byte(nil), request.ConfigJSON...)
	}
	if e.credentials != nil {
		*e.credentials = append([]byte(nil), request.CredentialsJSON...)
	}
	return (&runtimeCredentialConnector{}).Execute(
		ctx,
		request.StatementJSON,
		request.Parameters,
		request.Limits,
	)
}

func TestExecuteUsesResolvedProviderRepositoryPayload(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("managed-gitlab")
	querySlug := must.Slug("read-readme")
	credentials := &recordingCredentialService{
		resolveErr: fmt.Errorf("generic credential resolution must not be used"),
	}
	dsService := &runtimeCredentialDataSourceService{
		ds: &datasourcessvc.DataSource{
			Slug:     dsSlug,
			Type:     model.DataSourceTypeGitlab,
			Config:   []byte(`{"repository":{"id":"42","full_name":"acme/repo"}}`),
			IsActive: true,
		},
		gitExecutionAuth: &datasourcessvc.GitExecutionAuth{
			DataSourceType:  model.DataSourceTypeGitlab,
			ConfigJSON:      []byte(`{"repo_url":"https://gitlab.com/acme/repo.git","default_branch":"main"}`),
			CredentialsJSON: []byte(`{"auth_type":"https","username":"oauth2","token":"runtime-token"}`),
		},
	}
	queryRepo := &runtimeCredentialQueryRepo{
		query: &repoq.Query{
			Slug: querySlug,
			Name: "Read README",
			Data: []byte(`{"statement":"{\"type\":\"git\",\"operation\":\"read\",\"path\":\"README.md\"}"}`),
		},
	}

	var connectorConfig, connectorCredentials []byte
	svc, err := NewWithExecutionLimits(
		testutil.FakeTransactionManager{},
		queryRepo,
		dsService,
		runtimeCredentialExecutor{config: &connectorConfig, credentials: &connectorCredentials},
		credentials,
		fixedCredentialRuntimeClock{now: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)},
		ExecutionLimits{
			QueryTimeout:                      time.Minute,
			ConnectorTimeout:                  time.Minute,
			MaxResultRows:                     100,
			MaxResponseBytes:                  1024 * 1024,
			ConnectorConcurrencyPerDataSource: 1,
			HTTPTimeout:                       time.Minute,
			HTTPMaxRedirects:                  3,
			HTTPMaxRequestBytes:               1024 * 1024,
			HTTPMaxResponseBytes:              1024 * 1024,
			HTTPRetryAttempts:                 1,
			HTTPRetryBackoff:                  time.Millisecond,
		},
		nil,
	)
	require.NoError(t, err)

	_, err = svc.Execute(
		t.Context(),
		orgSlug,
		dsSlug,
		querySlug,
		model.SystemEventActor(),
		nil,
		WithoutExecutionRecording(),
	)
	require.NoError(t, err)

	assert.Zero(t, credentials.resolveCalls)
	assert.JSONEq(t, `{"repo_url":"https://gitlab.com/acme/repo.git","default_branch":"main"}`, string(connectorConfig))
	assert.JSONEq(t, `{"auth_type":"https","username":"oauth2","token":"runtime-token"}`, string(connectorCredentials))
}

func TestExecuteResolvesRuntimeCredentialBeforeConnectorConstruction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("crm")
	querySlug := must.Slug("list-contacts")
	credentials := &recordingCredentialService{
		payload: []byte(`{"access_token":"runtime-access-token"}`),
	}
	dsService := &runtimeCredentialDataSourceService{
		ds: &datasourcessvc.DataSource{
			Slug:                 dsSlug,
			Name:                 "CRM",
			Type:                 model.DataSourceTypeHubSpot,
			Config:               []byte(`{"timeout_seconds":5}`),
			EncryptedCredentials: []byte("legacy-credential-blob-must-not-be-used"),
			IsActive:             true,
		},
	}
	queryRepo := &runtimeCredentialQueryRepo{
		query: &repoq.Query{
			Slug: querySlug,
			Name: "List contacts",
			Data: []byte(`{"statement":"{\"type\":\"hubspot\",\"object_type\":\"contacts\",\"operation\":\"list\"}"}`),
		},
		params: []*repoq.QueryParameter{},
	}

	var connectorCredentials []byte
	svc, err := NewWithExecutionLimits(
		testutil.FakeTransactionManager{},
		queryRepo,
		dsService,
		runtimeCredentialExecutor{credentials: &connectorCredentials},
		credentials,
		fixedCredentialRuntimeClock{now: time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)},
		ExecutionLimits{
			QueryTimeout:                      time.Minute,
			ConnectorTimeout:                  time.Minute,
			MaxResultRows:                     100,
			MaxResponseBytes:                  1024 * 1024,
			ConnectorConcurrencyPerDataSource: 1,
			HTTPTimeout:                       time.Minute,
			HTTPMaxRedirects:                  3,
			HTTPMaxRequestBytes:               1024 * 1024,
			HTTPMaxResponseBytes:              1024 * 1024,
			HTTPRetryAttempts:                 1,
			HTTPRetryBackoff:                  time.Millisecond,
		},
		nil,
	)
	require.NoError(t, err)

	_, err = svc.Execute(
		ctx,
		orgSlug,
		dsSlug,
		querySlug,
		model.SystemEventActor(),
		nil,
		WithoutExecutionRecording(),
	)
	require.NoError(t, err)

	assert.Equal(t, credentialssvc.CredentialOwner{
		OrgSlug:        orgSlug,
		DataSourceSlug: dsSlug,
	}, credentials.lastOwner)
	assert.JSONEq(t, `{"access_token":"runtime-access-token"}`, string(connectorCredentials))
	assert.NotContains(t, string(connectorCredentials), "legacy-credential-blob-must-not-be-used")
}

func TestExecuteStopsBeforeConnectorConstructionWhenRuntimeCredentialResolutionFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("crm")
	querySlug := must.Slug("list-contacts")
	credentials := &recordingCredentialService{
		resolveErr: fmt.Errorf("runtime credential unavailable"),
	}
	dsService := &runtimeCredentialDataSourceService{
		ds: &datasourcessvc.DataSource{
			Slug:                 dsSlug,
			Name:                 "CRM",
			Type:                 model.DataSourceTypeHubSpot,
			Config:               []byte(`{"timeout_seconds":5}`),
			EncryptedCredentials: []byte("legacy-credential-blob-must-not-be-used"),
			IsActive:             true,
		},
	}
	queryRepo := &runtimeCredentialQueryRepo{
		query: &repoq.Query{
			Slug: querySlug,
			Name: "List contacts",
			Data: []byte(`{"statement":"{\"type\":\"hubspot\",\"object_type\":\"contacts\",\"operation\":\"list\"}"}`),
		},
		params: []*repoq.QueryParameter{},
	}

	connectorCalled := false
	svc, err := NewWithExecutionLimits(
		testutil.FakeTransactionManager{},
		queryRepo,
		dsService,
		runtimeCredentialExecutor{called: &connectorCalled},
		credentials,
		fixedCredentialRuntimeClock{now: time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)},
		ExecutionLimits{
			QueryTimeout:                      time.Minute,
			ConnectorTimeout:                  time.Minute,
			MaxResultRows:                     100,
			MaxResponseBytes:                  1024 * 1024,
			ConnectorConcurrencyPerDataSource: 1,
			HTTPTimeout:                       time.Minute,
			HTTPMaxRedirects:                  3,
			HTTPMaxRequestBytes:               1024 * 1024,
			HTTPMaxResponseBytes:              1024 * 1024,
			HTTPRetryAttempts:                 1,
			HTTPRetryBackoff:                  time.Millisecond,
		},
		nil,
	)
	require.NoError(t, err)

	_, err = svc.Execute(
		ctx,
		orgSlug,
		dsSlug,
		querySlug,
		model.SystemEventActor(),
		nil,
		WithoutExecutionRecording(),
	)
	require.Error(t, err)

	assert.Equal(t, credentialssvc.CredentialOwner{
		OrgSlug:        orgSlug,
		DataSourceSlug: dsSlug,
	}, credentials.lastOwner)
	assert.Contains(t, err.Error(), "runtime credential unavailable")
	assert.False(t, connectorCalled)
}

type recordingCredentialService struct {
	ConnectHTTPOAuthFn                  func(context.Context, credentialssvc.StartOAuthInstallParams) (*credentialssvc.ProviderConnectorInstall, error)
	AcquireHTTPOAuthTestCredentialFn    func(context.Context, credentialssvc.HTTPOAuthApp) (credentialssvc.RuntimeCredential, error)
	ConfigureHTTPOAuthAppFn             func(context.Context, credentialssvc.ConfigureHTTPOAuthAppParams) error
	ConnectHTTPOAuthClientCredentialsFn func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.CredentialMetadata, error)
	lastOwner                           credentialssvc.CredentialOwner
	payload                             []byte
	resolveErr                          error
	resolveCalls                        int
}

func (r *recordingCredentialService) CreateStaticSecret(
	context.Context,
	credentialssvc.CreateStaticSecretParams,
) (credentialssvc.CredentialMetadata, error) {
	return credentialssvc.CredentialMetadata{}, nil
}

func (r *recordingCredentialService) CreatePendingOAuthCredential(
	context.Context,
	credentialssvc.CreatePendingOAuthCredentialParams,
) (credentialssvc.CredentialMetadata, error) {
	return credentialssvc.CredentialMetadata{}, nil
}

func (r *recordingCredentialService) StartOAuthInstall(
	context.Context,
	credentialssvc.StartOAuthInstallParams,
) (credentialssvc.ProviderConnectorInstall, error) {
	return credentialssvc.ProviderConnectorInstall{}, nil
}

func (r *recordingCredentialService) FindOAuthInstall(
	context.Context,
	credentialssvc.FindOAuthInstallParams,
) (credentialssvc.CredentialInstall, error) {
	return credentialssvc.CredentialInstall{}, nil
}

func (r *recordingCredentialService) ConsumeOAuthInstall(
	context.Context,
	credentialssvc.ConsumeOAuthInstallParams,
) (credentialssvc.CredentialInstall, error) {
	return credentialssvc.CredentialInstall{}, nil
}

func (r *recordingCredentialService) CompleteOAuthCallback(
	context.Context,
	credentialssvc.CompleteOAuthCallbackParams,
) (credentialssvc.CompleteOAuthCallbackResult, error) {
	return credentialssvc.CompleteOAuthCallbackResult{}, nil
}

type fixedCredentialRuntimeClock struct {
	now time.Time
}

func (c fixedCredentialRuntimeClock) Now() time.Time {
	return c.now
}

func (r *recordingCredentialService) ResolveRuntimeCredential(
	_ context.Context,
	params credentialssvc.ResolveRuntimeCredentialParams,
) (credentialssvc.RuntimeCredential, error) {
	r.resolveCalls++
	r.lastOwner = params.Owner
	if r.resolveErr != nil {
		return credentialssvc.RuntimeCredential{}, r.resolveErr
	}
	return credentialssvc.RuntimeCredential{
		Kind:          credentialssvc.CredentialKindOAuth2Grant,
		PlaintextJSON: append([]byte(nil), r.payload...),
	}, nil
}

func (r *recordingCredentialService) Refresh(
	context.Context,
	credentialssvc.RefreshGrantParams,
) (credentialssvc.CredentialMetadata, error) {
	return credentialssvc.CredentialMetadata{}, nil
}

func (r *recordingCredentialService) Revoke(
	context.Context,
	credentialssvc.RevokeParams,
) (credentialssvc.CredentialMetadata, error) {
	return credentialssvc.CredentialMetadata{}, nil
}

func (r *recordingCredentialService) Rotate(
	context.Context,
	credentialssvc.RotateParams,
) (credentialssvc.ProviderConnectorInstall, error) {
	return credentialssvc.ProviderConnectorInstall{}, nil
}

type runtimeCredentialConnector struct{}

func (c *runtimeCredentialConnector) Execute(
	context.Context,
	json.RawMessage,
	map[string]types.TypedParam,
	...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	return result.NewObjectRowsResult("hubspot", &result.ObjectRowsResult{
		ObjectType:  "contacts",
		Operation:   "list",
		Rows:        []map[string]any{},
		ColumnNames: []string{"hs_object_id"},
		RowCount:    0,
	}, 1, nil), nil
}

func (c *runtimeCredentialConnector) TestConnection(context.Context) error {
	return nil
}

func (c *runtimeCredentialConnector) Close() error {
	return nil
}

type runtimeCredentialDataSourceService struct {
	datasourcessvc.Service
	ds               *datasourcessvc.DataSource
	gitExecutionAuth *datasourcessvc.GitExecutionAuth
}

func (s *runtimeCredentialDataSourceService) FindBySlug(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.DataSource, error) {
	return s.ds, nil
}

func (s *runtimeCredentialDataSourceService) ResolveGitExecutionAuth(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.GitExecutionAuth, error) {
	return s.gitExecutionAuth, nil
}

type runtimeCredentialQueryRepo struct {
	repoq.Repository
	query  *repoq.Query
	params []*repoq.QueryParameter
}

func (r *runtimeCredentialQueryRepo) FindBySlug(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*repoq.Query, []*repoq.QueryParameter, error) {
	return r.query, r.params, nil
}

func (*recordingCredentialService) CreateOrganizationGitLabConnection(context.Context, credentialssvc.CreateOrganizationGitLabConnectionParams) (credentialssvc.OrganizationGitLabConnection, error) {
	return credentialssvc.OrganizationGitLabConnection{}, fmt.Errorf("unexpected organization connection call")
}

func (*recordingCredentialService) GetOrganizationGitLabConnection(context.Context, slug.Slug) (credentialssvc.OrganizationGitLabConnection, error) {
	return credentialssvc.OrganizationGitLabConnection{}, fmt.Errorf("unexpected organization connection call")
}

func (*recordingCredentialService) ListOrganizationGitLabRepositories(context.Context, slug.Slug) ([]credentialssvc.ProviderRepository, error) {
	return nil, fmt.Errorf("unexpected organization connection call")
}

func (*recordingCredentialService) AttachOrganizationGitLabConnection(context.Context, credentialssvc.AttachOrganizationGitLabConnectionParams) error {
	return fmt.Errorf("unexpected organization connection call")
}

func (f *recordingCredentialService) ConfigureHTTPOAuthApp(ctx context.Context, params credentialssvc.ConfigureHTTPOAuthAppParams) error {
	if f.ConfigureHTTPOAuthAppFn == nil {
		return xerror.Unexpected().Detail("unexpected ConfigureHTTPOAuthApp call")
	}
	return f.ConfigureHTTPOAuthAppFn(ctx, params)
}

func (f *recordingCredentialService) ConnectHTTPOAuthClientCredentials(ctx context.Context, params credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.CredentialMetadata, error) {
	if f.ConnectHTTPOAuthClientCredentialsFn == nil {
		return credentialssvc.CredentialMetadata{}, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuthClientCredentials call")
	}
	return f.ConnectHTTPOAuthClientCredentialsFn(ctx, params)
}

func (f *recordingCredentialService) ConnectHTTPOAuth(ctx context.Context, p credentialssvc.StartOAuthInstallParams) (*credentialssvc.ProviderConnectorInstall, error) {
	if f.ConnectHTTPOAuthFn == nil {
		return nil, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuth call")
	}
	return f.ConnectHTTPOAuthFn(ctx, p)
}

func (f *recordingCredentialService) AcquireHTTPOAuthTestCredential(ctx context.Context, p credentialssvc.HTTPOAuthApp) (credentialssvc.RuntimeCredential, error) {
	if f.AcquireHTTPOAuthTestCredentialFn == nil {
		return credentialssvc.RuntimeCredential{}, xerror.Unexpected().Detail("unexpected AcquireHTTPOAuthTestCredential call")
	}
	return f.AcquireHTTPOAuthTestCredentialFn(ctx, p)
}

func TestExecutionPayloadSeparatesGitCredentialReferenceFromPoolHash(t *testing.T) {
	t.Parallel()
	reference := "secret/2026-10-18T10:00:00Z"
	ds := &datasourcessvc.DataSource{Type: model.DataSourceTypeGitlab}
	svc := &service{datasourcesSvc: &runtimeCredentialDataSourceService{gitExecutionAuth: &datasourcessvc.GitExecutionAuth{CredentialReference: reference, ConfigJSON: []byte(`{}`), CredentialsJSON: []byte(`{"token":"private"}`)}}}
	_, _, _, poolVersion, admitted, err := svc.resolveExecutionPayload(t.Context(), "acme", "repo", ds)
	require.NoError(t, err)
	require.Equal(t, reference, admitted)
	require.NotEqual(t, poolVersion, admitted)
	require.NotContains(t, admitted, "private")
}

func (*recordingCredentialService) CreateNotificationConnection(context.Context, credentialssvc.CreateNotificationConnectionParams) (credentialssvc.NotificationConnection, error) {
	return credentialssvc.NotificationConnection{}, credentialssvc.ErrCredentialNotFound
}

func (*recordingCredentialService) ListNotificationConnections(context.Context, slug.Slug) ([]credentialssvc.NotificationConnection, error) {
	return nil, credentialssvc.ErrCredentialNotFound
}

func (*recordingCredentialService) ConnectNotificationConnection(context.Context, credentialssvc.CredentialOwner, model.EventActor) (credentialssvc.ProviderConnectorInstall, error) {
	return credentialssvc.ProviderConnectorInstall{}, credentialssvc.ErrCredentialNotFound
}
