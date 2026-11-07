package datasourcessvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	gitconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/git"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/githubappsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type fakeHTTPIntrospectorConnector struct {
	introspectFn func(ctx context.Context) (*httpconnector.HTTPSchema, error)
}

func testAdmissionService() admissionsvc.Service {
	return admissionsvc.New(testutil.FakeTransactionManager{}, &testutil.FakeOrgsRepo{})
}

func (f fakeHTTPIntrospectorConnector) Execute(
	_ context.Context,
	_ json.RawMessage,
	_ map[string]types.TypedParam,
	_ ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	return &result.ExecutionResult{}, nil
}

func (f fakeHTTPIntrospectorConnector) TestConnection(_ context.Context) error {
	return nil
}

func (f fakeHTTPIntrospectorConnector) Close() error {
	return nil
}

func (f fakeHTTPIntrospectorConnector) IntrospectSchema(
	ctx context.Context,
) (*httpconnector.HTTPSchema, error) {
	if f.introspectFn != nil {
		return f.introspectFn(ctx)
	}
	return &httpconnector.HTTPSchema{Operations: []httpconnector.Operation{}}, nil
}

func newTestService(
	repo repods.Repository,
	schemaRepo repods.SchemaMetadataRepository,
	_ crypto.AES256,
	ncf connector.NewConnectorFunc,
) Service {
	return newTestServiceWithPolicy(
		repo,
		schemaRepo,
		testutil.NewFakeEncryptor(),
		ncf,
		CustomerConfigPolicy{},
	)
}

func newTestServiceWithPolicy(
	repo repods.Repository,
	schemaRepo repods.SchemaMetadataRepository,
	_ crypto.AES256,
	ncf connector.NewConnectorFunc,
	policy CustomerConfigPolicy,
) Service {
	svc, err := New(
		testutil.FakeTransactionManager{},
		repo,
		schemaRepo,
		nil,
		newFakeCredentialService(),
		NewNoopGitHubAppManager(),
		NewNoopGitLabWebhookManager(),
		testAdmissionService(),
		ncf,
		eventtest.NewProducer(&fakeDataSourceEventRecorder{}),
		&fakeDataSourceSchemaImportProducer{},
		ServiceConfig{CustomerConfigPolicy: policy},
	)
	if err != nil {
		panic(err) // Should never happen in tests unless explicitly testing factory nil case
	}
	return svc
}

type fakeDataSourceEventRecorder struct {
	calls []eventssvc.CreateEventParams
	err   error
}

type fakeDataSourceSchemaImportProducer struct {
	calls  []jobs.DataSourceSchemaImportArgs
	queued queue.QueuedJob
	err    error
}

func (p *fakeDataSourceSchemaImportProducer) EnqueueDataSourceSchemaImport(
	_ context.Context,
	_ postgres.DBTX,
	args jobs.DataSourceSchemaImportArgs,
) (queue.QueuedJob, error) {
	p.calls = append(p.calls, args)
	if p.err != nil {
		return queue.QueuedJob{}, p.err
	}
	if p.queued != (queue.QueuedJob{}) {
		return p.queued, nil
	}
	return queue.QueuedJob{
		Kind:  args.Kind(),
		Queue: jobs.DataSourceSchemaImportQueue,
		State: "queued",
	}, nil
}

type fakeServiceDBTX struct {
	postgres.DBTX
}

type connectionTestExecutorFunc func(context.Context, ConnectionTestRequest) error

func TestUnexecutedConnectionTestPreservesHealthAndTrialState(t *testing.T) {
	t.Parallel()
	failure := xerror.Unavailable(xerror.DataSource)
	s := &service{connectionTester: connectionTestExecutorFunc(func(context.Context, ConnectionTestRequest) error { return executiondelivery.MarkNotExecuted(failure) })}
	ds := &repods.DataSource{Slug: "source", Type: model.DataSourceTypePostgres, IsActive: true, ConnectionTestRevision: 3}
	result, err := s.testConnection(t.Context(), "tenant", ds, nil)
	require.ErrorIs(t, err, failure)
	require.Same(t, ds, result)
	require.True(t, result.IsActive)
	require.EqualValues(t, 3, result.ConnectionTestRevision)
	require.Nil(t, result.TestError)
}

func (f connectionTestExecutorFunc) TestConnection(
	ctx context.Context,
	request ConnectionTestRequest,
) error {
	return f(ctx, request)
}

// resolveGitAuthTransactionManager records whether ResolveGitAuth is inside its database transaction.
type resolveGitAuthTransactionManager struct {
	inTx bool
}

// RunInTx marks the callback as transactional so tests can verify external work is deferred.
func (m *resolveGitAuthTransactionManager) RunInTx(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	m.inTx = true
	defer func() { m.inTx = false }()
	return fn(ctx, nil)
}

// RunRead executes test reads without opening a transaction.
func (m *resolveGitAuthTransactionManager) RunRead(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	return fn(ctx, nil)
}

type fakeCredentialService struct {
	ConnectHTTPOAuthFn                   func(context.Context, credentialssvc.StartOAuthInstallParams) (*credentialssvc.ProviderConnectorInstall, error)
	AcquireHTTPOAuthTestCredentialFn     func(context.Context, credentialssvc.HTTPOAuthApp) (credentialssvc.RuntimeCredential, error)
	ConfigureHTTPOAuthAppFn              func(context.Context, credentialssvc.ConfigureHTTPOAuthAppParams) error
	ConnectHTTPOAuthClientCredentialsFn  func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.CredentialMetadata, error)
	CreateOrganizationGitLabConnectionFn func(ctx context.Context, params credentialssvc.CreateOrganizationGitLabConnectionParams) (credentialssvc.OrganizationGitLabConnection, error)
	GetOrganizationGitLabConnectionFn    func(ctx context.Context, org slug.Slug) (credentialssvc.OrganizationGitLabConnection, error)
	ListOrganizationGitLabRepositoriesFn func(ctx context.Context, org slug.Slug) ([]credentialssvc.ProviderRepository, error)
	AttachOrganizationGitLabConnectionFn func(ctx context.Context, params credentialssvc.AttachOrganizationGitLabConnectionParams) error

	credentials                  map[slug.Slug][]byte
	createPendingOAuthFn         func(context.Context, credentialssvc.CreatePendingOAuthCredentialParams) (credentialssvc.CredentialMetadata, error)
	startOAuthInstallFn          func(context.Context, credentialssvc.StartOAuthInstallParams) (credentialssvc.ProviderConnectorInstall, error)
	findOAuthInstallFn           func(context.Context, credentialssvc.FindOAuthInstallParams) (credentialssvc.CredentialInstall, error)
	completeOAuthCallbackFn      func(context.Context, credentialssvc.CompleteOAuthCallbackParams) (credentialssvc.CompleteOAuthCallbackResult, error)
	resolveRuntimeFn             func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error)
	listRepositoriesFn           func(context.Context, credentialssvc.ListProviderRepositoriesParams) ([]credentialssvc.ProviderRepository, error)
	resolveRepositoryFn          func(context.Context, credentialssvc.ResolveProviderRepositoryParams) (credentialssvc.ProviderRepository, error)
	resolveRepositoryWithTokenFn func(context.Context, credentialssvc.ResolveProviderRepositoryWithTokenParams) (credentialssvc.ProviderRepository, error)
}

func (f *fakeCredentialService) ResolveProviderRepositoryWithToken(
	ctx context.Context,
	params credentialssvc.ResolveProviderRepositoryWithTokenParams,
) (credentialssvc.ProviderRepository, error) {
	if f.resolveRepositoryWithTokenFn == nil {
		return credentialssvc.ProviderRepository{}, fmt.Errorf("unexpected call to ResolveProviderRepositoryWithToken in fakeCredentialService")
	}
	return f.resolveRepositoryWithTokenFn(ctx, params)
}

func (f *fakeCredentialService) ListProviderRepositories(
	ctx context.Context,
	params credentialssvc.ListProviderRepositoriesParams,
) ([]credentialssvc.ProviderRepository, error) {
	if f.listRepositoriesFn == nil {
		return nil, fmt.Errorf("unexpected call to ListProviderRepositories in fakeCredentialService")
	}
	return f.listRepositoriesFn(ctx, params)
}

func (f *fakeCredentialService) ResolveProviderRepository(
	ctx context.Context,
	params credentialssvc.ResolveProviderRepositoryParams,
) (credentialssvc.ProviderRepository, error) {
	if f.resolveRepositoryFn == nil {
		return credentialssvc.ProviderRepository{}, fmt.Errorf("unexpected call to ResolveProviderRepository in fakeCredentialService")
	}
	return f.resolveRepositoryFn(ctx, params)
}

type recordingGitLabWebhookManager struct {
	calls           int
	credentialsJSON []byte
	err             error
}

type fakeGitHubAppManager struct {
	connection         *githubappsvc.Connection
	token              string
	contentsPermission string
	getErr             error
	tokenErr           error
}

func (m *fakeGitHubAppManager) Get(context.Context, slug.Slug) (*githubappsvc.Connection, error) {
	return m.connection, m.getErr
}

func (m *fakeGitHubAppManager) Token(_ context.Context, _ slug.Slug, repositoryIDs []int64) (githubappsvc.InstallationToken, error) {
	if len(repositoryIDs) != 1 || repositoryIDs[0] != 42 {
		return githubappsvc.InstallationToken{}, fmt.Errorf("unexpected repository IDs: %v", repositoryIDs)
	}
	return githubappsvc.InstallationToken{Token: m.token, ExpiresAt: time.Now().Add(time.Hour), ContentsPermission: m.contentsPermission}, m.tokenErr
}

func (m *recordingGitLabWebhookManager) Ensure(_ context.Context, _, _ slug.Slug, _, credentialsJSON []byte) error {
	m.calls++
	m.credentialsJSON = append([]byte(nil), credentialsJSON...)
	return m.err
}

func newFakeCredentialService() *fakeCredentialService {
	return &fakeCredentialService{credentials: make(map[slug.Slug][]byte)}
}

func (f *fakeCredentialService) CreateStaticSecret(
	_ context.Context,
	params credentialssvc.CreateStaticSecretParams,
) (credentialssvc.CredentialMetadata, error) {
	f.credentials[params.Owner.DataSourceSlug] = append([]byte(nil), params.PlaintextJSON...)
	return credentialssvc.CredentialMetadata{
		Slug:      must.Slug("credential-" + params.Owner.DataSourceSlug.String()),
		Kind:      credentialssvc.CredentialKindStaticSecret,
		Status:    credentialssvc.CredentialStatusActive,
		UpdatedAt: time.Now(),
	}, nil
}

func (f *fakeCredentialService) CreatePendingOAuthCredential(
	ctx context.Context,
	params credentialssvc.CreatePendingOAuthCredentialParams,
) (credentialssvc.CredentialMetadata, error) {
	if f.createPendingOAuthFn == nil {
		return credentialssvc.CredentialMetadata{}, fmt.Errorf("unexpected call to CreatePendingOAuthCredential in fakeCredentialService")
	}
	return f.createPendingOAuthFn(ctx, params)
}

func (f *fakeCredentialService) StartOAuthInstall(
	ctx context.Context,
	params credentialssvc.StartOAuthInstallParams,
) (credentialssvc.ProviderConnectorInstall, error) {
	if f.startOAuthInstallFn == nil {
		return credentialssvc.ProviderConnectorInstall{}, fmt.Errorf("unexpected call to StartOAuthInstall in fakeCredentialService")
	}
	return f.startOAuthInstallFn(ctx, params)
}

func (f *fakeCredentialService) FindOAuthInstall(
	ctx context.Context,
	params credentialssvc.FindOAuthInstallParams,
) (credentialssvc.CredentialInstall, error) {
	if f.findOAuthInstallFn == nil {
		return credentialssvc.CredentialInstall{}, fmt.Errorf("unexpected call to FindOAuthInstall in fakeCredentialService")
	}
	return f.findOAuthInstallFn(ctx, params)
}

func (f *fakeCredentialService) ConsumeOAuthInstall(
	context.Context,
	credentialssvc.ConsumeOAuthInstallParams,
) (credentialssvc.CredentialInstall, error) {
	return credentialssvc.CredentialInstall{}, fmt.Errorf("unexpected call to ConsumeOAuthInstall in fakeCredentialService")
}

func (f *fakeCredentialService) CompleteOAuthCallback(
	ctx context.Context,
	params credentialssvc.CompleteOAuthCallbackParams,
) (credentialssvc.CompleteOAuthCallbackResult, error) {
	if f.completeOAuthCallbackFn == nil {
		return credentialssvc.CompleteOAuthCallbackResult{}, fmt.Errorf("unexpected call to CompleteOAuthCallback in fakeCredentialService")
	}
	return f.completeOAuthCallbackFn(ctx, params)
}

func (f *fakeCredentialService) ResolveRuntimeCredential(
	ctx context.Context,
	params credentialssvc.ResolveRuntimeCredentialParams,
) (credentialssvc.RuntimeCredential, error) {
	if f.resolveRuntimeFn != nil {
		return f.resolveRuntimeFn(ctx, params)
	}
	if payload, ok := f.credentials[params.Owner.DataSourceSlug]; ok {
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindStaticSecret,
			PlaintextJSON: append([]byte(nil), payload...),
		}, nil
	}
	switch params.Owner.DataSourceSlug {
	case must.Slug("customer-api"), must.Slug("http-api"):
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindStaticSecret,
			PlaintextJSON: []byte(`{"bearer_token":"token-123"}`),
		}, nil
	case must.Slug("prod-db"):
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindStaticSecret,
			PlaintextJSON: []byte(`{"username":"admin","password":"secret"}`),
		}, nil
	case must.Slug("git-backend"):
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindStaticSecret,
			PlaintextJSON: []byte(`{"auth_type":"https","username":"alice","token":"tok123"}`),
		}, nil
	case must.Slug("git-repo"):
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindStaticSecret,
			PlaintextJSON: []byte(`{"auth_type":"https"}`),
		}, nil
	case must.Slug("github-backend"):
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindStaticSecret,
			PlaintextJSON: []byte(`{"auth_type":"https","token":"ghp_123"}`),
		}, nil
	case must.Slug("gitlab-backend"):
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindStaticSecret,
			PlaintextJSON: []byte(`{"auth_type":"https","token":"glpat-123"}`),
		}, nil
	}
	return credentialssvc.RuntimeCredential{
		Kind:          credentialssvc.CredentialKindStaticSecret,
		PlaintextJSON: []byte(`{"username":"user","password":"pass","auth_type":"https","token":"token","bearer_token":"token","private_app_token":"token"}`),
	}, nil
}

func (f *fakeCredentialService) Refresh(
	context.Context,
	credentialssvc.RefreshGrantParams,
) (credentialssvc.CredentialMetadata, error) {
	return credentialssvc.CredentialMetadata{}, nil
}

func (f *fakeCredentialService) Revoke(
	context.Context,
	credentialssvc.RevokeParams,
) (credentialssvc.CredentialMetadata, error) {
	return credentialssvc.CredentialMetadata{}, nil
}

func (f *fakeCredentialService) Rotate(
	context.Context,
	credentialssvc.RotateParams,
) (credentialssvc.ProviderConnectorInstall, error) {
	return credentialssvc.ProviderConnectorInstall{}, nil
}

func (f *fakeDataSourceEventRecorder) CreateEvent(
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

func newTestServiceWithEvents(
	t *testing.T,
	repo repods.Repository,
	schemaRepo repods.SchemaMetadataRepository,
	_ crypto.AES256,
	ncf connector.NewConnectorFunc,
	recorder *fakeDataSourceEventRecorder,
) Service {
	t.Helper()

	svc, err := New(
		testutil.FakeTransactionManager{},
		repo,
		schemaRepo,
		nil,
		newFakeCredentialService(),
		NewNoopGitHubAppManager(),
		NewNoopGitLabWebhookManager(),
		testAdmissionService(),
		ncf,
		eventtest.NewProducer(recorder),
		&fakeDataSourceSchemaImportProducer{},
		ServiceConfig{},
	)
	require.NoError(t, err)
	return svc
}

func assertDataSourceEvent(
	t *testing.T,
	got eventssvc.CreateEventParams,
	orgSlug, actorSlug, dsSlug slug.Slug,
	action model.EventAction,
	dsType model.DataSourceType,
) {
	t.Helper()

	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeDataSource, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, dsSlug, *got.EntitySlug)
	require.Equal(t, action, got.Action)
	require.Equal(t, map[string]any{
		"data_source_type": dsType.String(),
	}, got.Metadata)
}

func TestCreateEmitsCreateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("main-db")
	actorSlug := must.Slug("alice")
	events := &fakeDataSourceEventRecorder{}
	enc := testutil.NewFakeEncryptor()
	repo := testutil.NewFakeDataSourceRepo()
	repo.CreateFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, params.Slug)
		return &repods.DataSource{
			Slug:     params.Slug,
			Name:     params.Name,
			Type:     params.Type,
			Config:   params.ConfigJSON,
			IsActive: true,
		}, nil
	}
	repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{Slug: dsSlug, Name: "Main DB", Type: model.DataSourceTypePostgres, IsActive: true}, nil
	}
	cf := &testutil.FakeConnectorFactory{}
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return testutil.NewFakeSQLConnector(), nil
	}

	svc := newTestServiceWithEvents(t, repo, testutil.NewFakeSchemaMetadataRepo(), enc, cf.NewConnectorFn, events)

	_, err := svc.Create(context.Background(), orgSlug, model.UserEventActor(actorSlug), &CreateParams{
		Name:        "Main DB",
		Slug:        dsSlug,
		Type:        model.DataSourceTypePostgres,
		ConfigJSON:  []byte(`{"host":"localhost","port":5432,"database":"app","ssl_mode":"verify-full"}`),
		Credentials: map[string]any{"username": "app", "password": "secret"},
	})
	require.NoError(t, err)
	require.Len(t, events.calls, 1)
	assertDataSourceEvent(
		t,
		events.calls[0],
		orgSlug,
		actorSlug,
		dsSlug,
		model.EventActionCreate,
		model.DataSourceTypePostgres,
	)
	require.NotContains(t, events.calls[0].Metadata, "credentials")
}

func TestCreateReturnsRequiredAuditFailureAfterMutation(t *testing.T) {
	t.Parallel()

	auditErr := xerror.Unexpected().Detail("audit write failed")
	mutationCalled := false
	repo := testutil.NewFakeDataSourceRepo()
	repo.CreateFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_ slug.Slug,
		params *repods.CreateParams,
	) (*repods.DataSource, error) {
		mutationCalled = true
		return &repods.DataSource{
			Slug:     params.Slug,
			Name:     params.Name,
			Type:     params.Type,
			Config:   params.ConfigJSON,
			IsActive: true,
		}, nil
	}
	connectorFactory := &testutil.FakeConnectorFactory{
		NewConnectorFn: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
			return testutil.NewFakeSQLConnector(), nil
		},
	}
	svc := newTestServiceWithEvents(
		t,
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		testutil.NewFakeEncryptor(),
		connectorFactory.NewConnectorFn,
		&fakeDataSourceEventRecorder{err: auditErr},
	)

	got, err := svc.Create(
		context.Background(),
		must.Slug("acme"),
		model.UserEventActor(must.Slug("alice")),
		&CreateParams{
			Name:        "Main DB",
			Slug:        must.Slug("main-db"),
			Type:        model.DataSourceTypePostgres,
			ConfigJSON:  []byte(`{"host":"localhost","port":5432,"database":"app","ssl_mode":"verify-full"}`),
			Credentials: map[string]any{"username": "app", "password": "secret"},
		},
	)

	require.Nil(t, got)
	require.True(t, mutationCalled)
	require.ErrorIs(t, err, auditErr)
	require.ErrorContains(t, err, "record data source create event")
}

// TestStartProviderDataSourceInstallCreatesPendingHubSpotSource verifies OAuth-first bootstrap without static credentials.
func TestStartProviderDataSourceInstallCreatesPendingHubSpotSource(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("crm")
	actorSlug := must.Slug("alice")
	credentialSlug := must.Slug("oauth-credential-crm")
	events := &fakeDataSourceEventRecorder{}
	repo := testutil.NewFakeDataSourceRepo()
	repo.CreateFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		assert.Equal(t, dsSlug, params.Slug)
		assert.Equal(t, model.DataSourceTypeHubSpot, params.Type)
		assert.Equal(t, credentialSlug, params.CredentialSlug)
		assert.True(t, params.ReadOnly)
		return &repods.DataSource{
			Slug:     params.Slug,
			Name:     params.Name,
			Type:     params.Type,
			Config:   params.ConfigJSON,
			ReadOnly: params.ReadOnly,
		}, nil
	}
	credentials := newFakeCredentialService()
	credentials.createPendingOAuthFn = func(_ context.Context, params credentialssvc.CreatePendingOAuthCredentialParams) (credentialssvc.CredentialMetadata, error) {
		assert.Equal(t, credentialssvc.CredentialOwner{OrgSlug: orgSlug, DataSourceSlug: dsSlug}, params.Owner)
		assert.Equal(t, "hubspot", params.Provider)
		return credentialssvc.CredentialMetadata{
			Slug:   credentialSlug,
			Kind:   credentialssvc.CredentialKindOAuth2Grant,
			Status: credentialssvc.CredentialStatusPending,
		}, nil
	}
	credentials.startOAuthInstallFn = func(_ context.Context, params credentialssvc.StartOAuthInstallParams) (credentialssvc.ProviderConnectorInstall, error) {
		assert.Equal(t, credentialssvc.CredentialOwner{OrgSlug: orgSlug, DataSourceSlug: dsSlug}, params.Owner)
		assert.Equal(t, "hubspot", params.Provider)
		assert.Equal(t, credentialssvc.OAuthInstallIntentCreate, params.Intent)
		assert.False(t, params.IncludeWriteScopes)
		return credentialssvc.ProviderConnectorInstall{
			Slug:             must.Slug("oauth-install-crm"),
			Provider:         "hubspot",
			AuthorizationURL: "https://app.hubspot.com/oauth/authorize?state=opaque",
			Intent:           credentialssvc.OAuthInstallIntentCreate,
		}, nil
	}
	svc, err := New(
		testutil.FakeTransactionManager{},
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		nil,
		credentials,
		NewNoopGitHubAppManager(),
		NewNoopGitLabWebhookManager(),
		testAdmissionService(),
		nil,
		eventtest.NewProducer(events),
		&fakeDataSourceSchemaImportProducer{},
		ServiceConfig{},
	)
	require.NoError(t, err)

	got, err := svc.StartProviderDataSourceInstall(context.Background(), orgSlug, model.UserEventActor(actorSlug), "hubspot", &StartProviderDataSourceInstallParams{
		Name:       "CRM",
		Slug:       dsSlug,
		Type:       model.DataSourceTypeHubSpot,
		ConfigJSON: []byte(`{"timeout_seconds":5}`),
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, dsSlug, got.DataSource.Slug)
	assert.False(t, got.DataSource.IsActive)
	require.NotNil(t, got.DataSource.Credential)
	assert.Equal(t, credentialssvc.CredentialStatusPending, got.DataSource.Credential.Status)
	assert.Equal(t, "https://app.hubspot.com/oauth/authorize?state=opaque", got.Install.AuthorizationURL)
	assert.Empty(t, events.calls)
}

func TestStartProviderDataSourceInstallRequiresOrganizationGitLab(t *testing.T) {
	t.Parallel()
	svc := newTestService(testutil.NewFakeDataSourceRepo(), testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), nil)
	result, err := svc.StartProviderDataSourceInstall(t.Context(), must.Slug("acme"), model.UserEventActor(must.Slug("alice")), "gitlab", &StartProviderDataSourceInstallParams{Name: "Pipelines", Type: model.DataSourceTypeGitlab})
	require.ErrorContains(t, err, "organization settings")
	require.Nil(t, result)
}

func TestCreateGitLabReusesOrganizationGrantAndVerifiedRepository(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("pipelines")
	repo := testutil.NewFakeDataSourceRepo()
	var stored *repods.DataSource
	repo.CreateFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
		stored = &repods.DataSource{
			Slug: dsSlug, Name: params.Name, Type: params.Type, Config: append([]byte(nil), params.ConfigJSON...),
			Credential: &repods.CredentialMetadata{
				Slug: must.Slug("credential-pipelines"), Kind: credentialssvc.CredentialKindOAuth2Grant.String(),
				Status: credentialssvc.CredentialStatusActive.String(), UpdatedAt: time.Now(),
			},
		}
		return stored, nil
	}
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		cloned := *stored
		return &cloned, nil
	}
	repo.MarkTestSuccessFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug, _ *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
		stored.IsActive = true
		stored.ConnectionTestRevision++
		cloned := *stored
		return &cloned, nil
	}
	credentials := newFakeCredentialService()
	credentials.GetOrganizationGitLabConnectionFn = func(context.Context, slug.Slug) (credentialssvc.OrganizationGitLabConnection, error) {
		return credentialssvc.OrganizationGitLabConnection{Credential: credentialssvc.CredentialMetadata{Slug: must.Slug("credential-pipelines"), Status: credentialssvc.CredentialStatusActive}}, nil
	}
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{Kind: credentialssvc.CredentialKindOAuth2Grant, PlaintextJSON: []byte(`{"access_token":"project-token"}`)}, nil
	}
	credentials.AttachOrganizationGitLabConnectionFn = func(_ context.Context, params credentialssvc.AttachOrganizationGitLabConnectionParams) error {
		require.Equal(t, "42", params.ExpectedRepositoryID)
		require.Equal(t, must.Slug("credential-pipelines"), params.ExpectedCredentialSlug)
		return nil
	}
	credentials.resolveRepositoryFn = func(_ context.Context, params credentialssvc.ResolveProviderRepositoryParams) (credentialssvc.ProviderRepository, error) {
		require.Equal(t, "42", params.RepositoryID)
		require.Equal(t, credentialssvc.CredentialOwnerKindOrganizationGitLab, params.Owner.Kind)
		return credentialssvc.ProviderRepository{
			ID: "42", Name: "Pipelines", FullName: "acme/pipelines",
			WebURL: "https://gitlab.com/acme/pipelines", CloneURL: "https://gitlab.com/acme/pipelines.git", DefaultBranch: "main",
		}, nil
	}
	executor := connectionTestExecutorFunc(func(_ context.Context, request ConnectionTestRequest) error {
		require.Equal(t, orgSlug, request.OrganizationSlug)
		require.Equal(t, dsSlug, request.DataSourceSlug)
		require.JSONEq(t, `{"repo_url":"https://gitlab.com/acme/pipelines.git","default_branch":"main","base_path":"data"}`, string(request.ConfigJSON))
		require.JSONEq(t, `{"auth_type":"https","username":"oauth2","token":"project-token"}`, string(request.CredentialsJSON))
		return nil
	})
	svc, err := New(
		testutil.FakeTransactionManager{}, repo, testutil.NewFakeSchemaMetadataRepo(), nil,
		credentials, NewNoopGitHubAppManager(), NewNoopGitLabWebhookManager(), testAdmissionService(), nil,
		eventtest.NewProducer(&fakeDataSourceEventRecorder{}), &fakeDataSourceSchemaImportProducer{},
		ServiceConfig{ConnectionTestExecutor: executor},
	)
	require.NoError(t, err)

	created, err := svc.Create(t.Context(), orgSlug, model.UserEventActor(must.Slug("alice")), &CreateParams{
		Name: "Pipelines", Slug: dsSlug, Type: model.DataSourceTypeGitlab,
		ConfigJSON: []byte(`{"project_id":"42","base_path":"data"}`),
		ReadOnly:   true,
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	require.JSONEq(t, `{"repository":{"id":"42","name":"Pipelines","full_name":"acme/pipelines","web_url":"https://gitlab.com/acme/pipelines","default_branch":"main"},"base_path":"data"}`, string(stored.Config))
	require.NotContains(t, string(stored.Config), "project-token")
}

func TestCreateGitHubUsesOrganizationAppTokenWithoutPersistingIt(t *testing.T) {
	t.Parallel()

	orgSlug, dsSlug := must.Slug("acme"), must.Slug("pipelines")
	repo := testutil.NewFakeDataSourceRepo()
	var stored *repods.DataSource
	repo.CreateFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
		stored = &repods.DataSource{
			Slug: dsSlug, Name: params.Name, Type: params.Type, Config: append([]byte(nil), params.ConfigJSON...),
			Credential: &repods.CredentialMetadata{Slug: params.CredentialSlug, Kind: credentialssvc.CredentialKindStaticSecret.String(), Status: credentialssvc.CredentialStatusActive.String(), UpdatedAt: time.Now()},
		}
		return stored, nil
	}
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		cloned := *stored
		return &cloned, nil
	}
	repo.MarkTestSuccessFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug, _ *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
		stored.IsActive = true
		stored.ConnectionTestRevision++
		cloned := *stored
		return &cloned, nil
	}
	credentials := newFakeCredentialService()
	githubApps := &fakeGitHubAppManager{token: "short-lived", connection: &githubappsvc.Connection{
		Organization: orgSlug,
		Repositories: []githubapps.RepositoryBinding{{ID: 42, FullName: "acme/pipelines", OwnerLogin: "acme", Name: "pipelines"}},
	}}
	executor := connectionTestExecutorFunc(func(_ context.Context, request ConnectionTestRequest) error {
		require.JSONEq(t, `{"repo_url":"https://github.com/acme/pipelines.git","base_path":"data"}`, string(request.ConfigJSON))
		require.JSONEq(t, `{"auth_type":"https","username":"x-access-token","token":"short-lived"}`, string(request.CredentialsJSON))
		return nil
	})
	svc, err := New(
		testutil.FakeTransactionManager{}, repo, testutil.NewFakeSchemaMetadataRepo(), nil,
		credentials, githubApps, NewNoopGitLabWebhookManager(), testAdmissionService(), nil,
		eventtest.NewProducer(&fakeDataSourceEventRecorder{}), &fakeDataSourceSchemaImportProducer{},
		ServiceConfig{ConnectionTestExecutor: executor},
	)
	require.NoError(t, err)

	created, err := svc.Create(t.Context(), orgSlug, model.UserEventActor(must.Slug("alice")), &CreateParams{
		Name: "Pipelines", Slug: dsSlug, Type: model.DataSourceTypeGithub,
		ConfigJSON: []byte(`{"repository_id":"42","base_path":"data"}`), Credentials: map[string]any{}, ReadOnly: true,
	})
	require.NoError(t, err)
	require.True(t, created.IsActive)
	require.JSONEq(t, `{"repository":{"id":"42","name":"pipelines","full_name":"acme/pipelines","web_url":"https://github.com/acme/pipelines"},"base_path":"data"}`, string(stored.Config))
	require.JSONEq(t, `{"auth_type":"https","username":"x-access-token"}`, string(credentials.credentials[dsSlug]))
}

func TestGitHubDraftConnectionUsesOrganizationAppToken(t *testing.T) {
	t.Parallel()

	githubApps := &fakeGitHubAppManager{token: "short-lived", connection: &githubappsvc.Connection{
		Repositories: []githubapps.RepositoryBinding{{ID: 42, FullName: "acme/pipelines", Name: "pipelines"}},
	}}
	svc, err := New(testutil.FakeTransactionManager{}, testutil.NewFakeDataSourceRepo(), testutil.NewFakeSchemaMetadataRepo(), nil,
		newFakeCredentialService(), githubApps, NewNoopGitLabWebhookManager(), testAdmissionService(), nil,
		eventtest.NewProducer(&fakeDataSourceEventRecorder{}), &fakeDataSourceSchemaImportProducer{},
		ServiceConfig{ConnectionTestExecutor: connectionTestExecutorFunc(func(_ context.Context, request ConnectionTestRequest) error {
			require.JSONEq(t, `{"repo_url":"https://github.com/acme/pipelines.git"}`, string(request.ConfigJSON))
			require.JSONEq(t, `{"auth_type":"https","username":"x-access-token","token":"short-lived"}`, string(request.CredentialsJSON))
			return nil
		})},
	)
	require.NoError(t, err)
	require.NoError(t, svc.TestConnection(t.Context(), must.Slug("acme"), &TestConnectionParams{
		Type: model.DataSourceTypeGithub, ConfigJSON: []byte(`{"repository_id":"42"}`), Credentials: map[string]any{},
	}))
}

func TestGitHubRepositoryRejectsInvalidOrUnavailableBindings(t *testing.T) {
	t.Parallel()

	now := time.Now()
	for _, tc := range []struct {
		name       string
		repository string
		connection *githubappsvc.Connection
		want       string
	}{
		{name: "invalid ID", repository: "invalid", connection: &githubappsvc.Connection{}, want: "positive integer"},
		{name: "missing connection", repository: "42", want: "active organization GitHub App connection"},
		{name: "suspended connection", repository: "42", connection: &githubappsvc.Connection{SuspendedAt: &now}, want: "active organization GitHub App connection"},
		{name: "deleted connection", repository: "42", connection: &githubappsvc.Connection{DeletedAt: &now}, want: "active organization GitHub App connection"},
		{name: "unbound repository", repository: "43", connection: &githubappsvc.Connection{Repositories: []githubapps.RepositoryBinding{{ID: 42}}}, want: "not bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &service{githubApps: &fakeGitHubAppManager{connection: tc.connection}}
			_, err := svc.githubRepository(t.Context(), must.Slug("acme"), tc.repository)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestCreateGitHubStopsBeforePersistenceWhenTokenMintingFails(t *testing.T) {
	t.Parallel()

	created := false
	repo := testutil.NewFakeDataSourceRepo()
	repo.CreateFn = func(context.Context, postgres.DBTX, slug.Slug, *repods.CreateParams) (*repods.DataSource, error) {
		created = true
		return nil, assert.AnError
	}
	credentials := newFakeCredentialService()
	githubApps := &fakeGitHubAppManager{tokenErr: assert.AnError, connection: &githubappsvc.Connection{
		Repositories: []githubapps.RepositoryBinding{{ID: 42, FullName: "acme/pipelines", Name: "pipelines"}},
	}}
	svc, err := New(testutil.FakeTransactionManager{}, repo, testutil.NewFakeSchemaMetadataRepo(), nil,
		credentials, githubApps, NewNoopGitLabWebhookManager(), testAdmissionService(), nil,
		eventtest.NewProducer(&fakeDataSourceEventRecorder{}), &fakeDataSourceSchemaImportProducer{}, ServiceConfig{},
	)
	require.NoError(t, err)

	_, err = svc.Create(t.Context(), must.Slug("acme"), model.UserEventActor(must.Slug("alice")), &CreateParams{
		Name: "Pipelines", Type: model.DataSourceTypeGithub,
		ConfigJSON: []byte(`{"repository_id":"42"}`), Credentials: map[string]any{},
	})
	require.ErrorContains(t, err, "mint GitHub repository token")
	require.False(t, created)
	require.Empty(t, credentials.credentials)
}

// TestStartProviderDataSourceInstallFailures verifies validation and dependency failures return no partial install or event.
func TestStartProviderDataSourceInstallFailures(t *testing.T) {
	t.Parallel()

	errPending := xerror.Unexpected().Detail("create pending credential failed")
	errCreate := repods.ErrSlugTaken
	errInstall := xerror.Unexpected().Detail("start oauth install failed")
	tests := []struct {
		name          string
		provider      string
		params        StartProviderDataSourceInstallParams
		pendingErr    error
		createErr     error
		installErr    error
		expectedErr   error
		expectedCalls [3]int
	}{
		{
			name:        "InvalidProvider",
			provider:    "salesforce",
			params:      StartProviderDataSourceInstallParams{Name: "CRM", Slug: must.Slug("crm"), Type: model.DataSourceTypeHubSpot, ConfigJSON: []byte(`{"timeout_seconds":5}`)},
			expectedErr: ErrInvalidInput,
		},
		{
			name:        "InvalidConfig",
			provider:    "hubspot",
			params:      StartProviderDataSourceInstallParams{Name: "CRM", Slug: must.Slug("crm"), Type: model.DataSourceTypeHubSpot, ConfigJSON: []byte(`{`)},
			expectedErr: ErrInvalidInput,
		},
		{
			name:        "BigQueryRejectsHubSpotConfig",
			provider:    "bigquery",
			params:      StartProviderDataSourceInstallParams{Name: "Warehouse", Slug: must.Slug("warehouse"), Type: model.DataSourceTypeBigQuery, ConfigJSON: []byte(`{"timeout_seconds":5}`)},
			expectedErr: ErrInvalidInput,
		},
		{
			name:        "HubSpotRejectsBigQueryConfig",
			provider:    "hubspot",
			params:      StartProviderDataSourceInstallParams{Name: "CRM", Slug: must.Slug("crm"), Type: model.DataSourceTypeHubSpot, ConfigJSON: []byte(`{"project_id":"fixture","default_dataset":"crm"}`)},
			expectedErr: ErrInvalidInput,
		},
		{
			name:          "PendingCredentialError",
			provider:      "hubspot",
			params:        StartProviderDataSourceInstallParams{Name: "CRM", Slug: must.Slug("crm"), Type: model.DataSourceTypeHubSpot, ConfigJSON: []byte(`{"timeout_seconds":5}`)},
			pendingErr:    errPending,
			expectedErr:   errPending,
			expectedCalls: [3]int{1, 0, 0},
		},
		{
			name:          "RepositoryCreateError",
			provider:      "hubspot",
			params:        StartProviderDataSourceInstallParams{Name: "CRM", Slug: must.Slug("crm"), Type: model.DataSourceTypeHubSpot, ConfigJSON: []byte(`{"timeout_seconds":5}`)},
			createErr:     errCreate,
			expectedErr:   errCreate,
			expectedCalls: [3]int{1, 1, 0},
		},
		{
			name:          "OAuthInstallError",
			provider:      "hubspot",
			params:        StartProviderDataSourceInstallParams{Name: "CRM", Slug: must.Slug("crm"), Type: model.DataSourceTypeHubSpot, ConfigJSON: []byte(`{"timeout_seconds":5}`)},
			installErr:    errInstall,
			expectedErr:   errInstall,
			expectedCalls: [3]int{1, 1, 1},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var calls [3]int
			events := &fakeDataSourceEventRecorder{}
			repo := testutil.NewFakeDataSourceRepo()
			repo.CreateFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
				calls[1]++
				if test.createErr != nil {
					return nil, test.createErr
				}
				return &repods.DataSource{Slug: params.Slug, Name: params.Name, Type: params.Type, Config: params.ConfigJSON, ReadOnly: params.ReadOnly}, nil
			}
			credentials := newFakeCredentialService()
			credentials.createPendingOAuthFn = func(_ context.Context, params credentialssvc.CreatePendingOAuthCredentialParams) (credentialssvc.CredentialMetadata, error) {
				calls[0]++
				if test.pendingErr != nil {
					return credentialssvc.CredentialMetadata{}, test.pendingErr
				}
				return credentialssvc.CredentialMetadata{Slug: must.Slug("oauth-credential-crm"), Kind: credentialssvc.CredentialKindOAuth2Grant, Status: credentialssvc.CredentialStatusPending}, nil
			}
			credentials.startOAuthInstallFn = func(_ context.Context, _ credentialssvc.StartOAuthInstallParams) (credentialssvc.ProviderConnectorInstall, error) {
				calls[2]++
				if test.installErr != nil {
					return credentialssvc.ProviderConnectorInstall{}, test.installErr
				}
				return credentialssvc.ProviderConnectorInstall{Slug: must.Slug("oauth-install-crm"), Provider: "hubspot", AuthorizationURL: "https://app.hubspot.com/oauth/authorize", Intent: credentialssvc.OAuthInstallIntentCreate}, nil
			}
			svc, err := New(testutil.FakeTransactionManager{}, repo, testutil.NewFakeSchemaMetadataRepo(), nil, credentials, NewNoopGitHubAppManager(), NewNoopGitLabWebhookManager(), testAdmissionService(), nil, eventtest.NewProducer(events), &fakeDataSourceSchemaImportProducer{}, ServiceConfig{})
			require.NoError(t, err)

			got, err := svc.StartProviderDataSourceInstall(context.Background(), must.Slug("acme"), model.UserEventActor(must.Slug("alice")), test.provider, &test.params)

			require.Error(t, err)
			require.ErrorIs(t, err, test.expectedErr)
			assert.Nil(t, got)
			assert.Equal(t, test.expectedCalls, calls)
			assert.Empty(t, events.calls)
		})
	}
}

// TestStartProviderDataSourceInstallSuffixesGeneratedReservedSlugOnce verifies active or deleted reservations are resolved before credential ownership is created.
func TestStartProviderDataSourceInstallSuffixesGeneratedReservedSlugOnce(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	baseSlug := must.Slug("crm")
	repo := testutil.NewFakeDataSourceRepo()
	repo.IsSlugReservedFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug) (bool, error) {
		assert.Equal(t, orgSlug, gotOrgSlug)
		assert.Equal(t, baseSlug, gotDSSlug)
		return true, nil
	}
	var createdSlug slug.Slug
	var credentialOwnerSlug slug.Slug
	repo.CreateFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
		createdSlug = params.Slug
		return &repods.DataSource{Slug: params.Slug, Name: params.Name, Type: params.Type, Config: params.ConfigJSON, ReadOnly: params.ReadOnly}, nil
	}
	credentials := newFakeCredentialService()
	credentials.createPendingOAuthFn = func(_ context.Context, params credentialssvc.CreatePendingOAuthCredentialParams) (credentialssvc.CredentialMetadata, error) {
		credentialOwnerSlug = params.Owner.DataSourceSlug
		return credentialssvc.CredentialMetadata{Slug: must.Slug("oauth-credential-crm"), Kind: credentialssvc.CredentialKindOAuth2Grant, Status: credentialssvc.CredentialStatusPending}, nil
	}
	credentials.startOAuthInstallFn = func(_ context.Context, params credentialssvc.StartOAuthInstallParams) (credentialssvc.ProviderConnectorInstall, error) {
		assert.Equal(t, credentialOwnerSlug, params.Owner.DataSourceSlug)
		return credentialssvc.ProviderConnectorInstall{Slug: must.Slug("oauth-install-crm"), Provider: "hubspot", AuthorizationURL: "https://app.hubspot.com/oauth/authorize", Intent: credentialssvc.OAuthInstallIntentCreate}, nil
	}
	svc, err := New(testutil.FakeTransactionManager{}, repo, testutil.NewFakeSchemaMetadataRepo(), nil, credentials, NewNoopGitHubAppManager(), NewNoopGitLabWebhookManager(), testAdmissionService(), nil, eventtest.NewProducer(&fakeDataSourceEventRecorder{}), &fakeDataSourceSchemaImportProducer{}, ServiceConfig{})
	require.NoError(t, err)

	got, err := svc.StartProviderDataSourceInstall(context.Background(), orgSlug, model.UserEventActor(must.Slug("alice")), "hubspot", &StartProviderDataSourceInstallParams{Name: "CRM", Type: model.DataSourceTypeHubSpot, ConfigJSON: []byte(`{"timeout_seconds":5}`)})

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Regexp(t, `^crm-[a-z0-9]{6}$`, createdSlug.String())
	assert.Equal(t, createdSlug, credentialOwnerSlug)
	assert.Equal(t, createdSlug, got.DataSource.Slug)
}

func TestUpdateEmitsUpdateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("main-db")
	actorSlug := must.Slug("alice")
	events := &fakeDataSourceEventRecorder{}
	enc := testutil.NewFakeEncryptor()
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:                 dsSlug,
			Name:                 "Main DB",
			Type:                 model.DataSourceTypePostgres,
			Config:               []byte(`{"host":"localhost","port":5432,"database":"app","ssl_mode":"verify-full"}`),
			EncryptedCredentials: []byte(`{"username":"app","password":"secret"}`),
			IsActive:             true,
		}, nil
	}
	repo.UpdateFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug, params *repods.UpdateParams) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.DataSource{
			Slug:                 dsSlug,
			Name:                 params.Name,
			Type:                 model.DataSourceTypePostgres,
			Config:               params.ConfigJSON,
			EncryptedCredentials: []byte(`{"username":"app","password":"secret"}`),
			IsActive:             true,
		}, nil
	}
	cf := &testutil.FakeConnectorFactory{}
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return testutil.NewFakeSQLConnector(), nil
	}

	svc := newTestServiceWithEvents(t, repo, testutil.NewFakeSchemaMetadataRepo(), enc, cf.NewConnectorFn, events)

	_, err := svc.Update(context.Background(), orgSlug, dsSlug, model.UserEventActor(actorSlug), &UpdateParams{
		Name:       "Primary DB",
		ConfigJSON: []byte(`{"host":"localhost","port":5432,"database":"app","ssl_mode":"verify-full"}`),
	})
	require.NoError(t, err)
	require.Len(t, events.calls, 1)
	assertDataSourceEvent(
		t,
		events.calls[0],
		orgSlug,
		actorSlug,
		dsSlug,
		model.EventActionUpdate,
		model.DataSourceTypePostgres,
	)
}

func TestUpdateCredentialsEmitsRotateEventWithoutCredentialMetadata(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("main-db")
	actorSlug := must.Slug("alice")
	events := &fakeDataSourceEventRecorder{}
	enc := testutil.NewFakeEncryptor()
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:                 dsSlug,
			Name:                 "Main DB",
			Type:                 model.DataSourceTypePostgres,
			Config:               []byte(`{"host":"localhost","port":5432,"database":"app","ssl_mode":"verify-full"}`),
			EncryptedCredentials: []byte(`{"username":"app","password":"old"}`),
			IsActive:             true,
		}, nil
	}
	repo.UpdateCredentialsFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug, params *repods.UpdateCredentialsParams) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		require.Equal(t, must.Slug("credential-main-db"), params.CredentialSlug)
		return &repods.DataSource{
			Slug:     dsSlug,
			Name:     "Main DB",
			Type:     model.DataSourceTypePostgres,
			Config:   []byte(`{"host":"localhost","port":5432,"database":"app","ssl_mode":"verify-full"}`),
			IsActive: true,
		}, nil
	}
	cf := &testutil.FakeConnectorFactory{}
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return testutil.NewFakeSQLConnector(), nil
	}

	svc := newTestServiceWithEvents(t, repo, testutil.NewFakeSchemaMetadataRepo(), enc, cf.NewConnectorFn, events)

	_, err := svc.UpdateCredentials(
		context.Background(),
		orgSlug,
		dsSlug,
		model.UserEventActor(actorSlug),
		&UpdateCredentialsParams{
			Credentials: map[string]any{"username": "app", "password": "new"},
		})
	require.NoError(t, err)
	require.Len(t, events.calls, 1)
	assertDataSourceEvent(
		t,
		events.calls[0],
		orgSlug,
		actorSlug,
		dsSlug,
		model.EventActionRotate,
		model.DataSourceTypePostgres,
	)
	require.NotContains(t, events.calls[0].Metadata, "credentials")
	require.NotContains(t, events.calls[0].Metadata, "password")
}

func TestValidateCredentialLifecycleTestsRuntimeCredentialAndRecordsEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("crm")
	actorSlug := must.Slug("alice")
	events := &fakeDataSourceEventRecorder{}
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:     dsSlug,
			Name:     "CRM",
			Type:     model.DataSourceTypeHubSpot,
			Config:   []byte(`{}`),
			IsActive: false,
		}, nil
	}
	repo.MarkTestSuccessFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug, _ *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.DataSource{Slug: dsSlug, Type: model.DataSourceTypeHubSpot, IsActive: true}, nil
	}
	cf := &testutil.FakeConnectorFactory{}
	cf.NewConnectorFn = func(_ context.Context, dsType model.DataSourceType, _, credentialsJSON []byte) (connector.Connector, error) {
		require.Equal(t, model.DataSourceTypeHubSpot, dsType)
		require.JSONEq(t, `{"username":"user","password":"pass","auth_type":"https","token":"token","bearer_token":"token","private_app_token":"token"}`, string(credentialsJSON))
		return testutil.NewFakeSQLConnector(), nil
	}
	svc := newTestServiceWithEvents(t, repo, testutil.NewFakeSchemaMetadataRepo(), nil, cf.NewConnectorFn, events)

	got, err := svc.ValidateCredentialLifecycle(
		context.Background(),
		orgSlug,
		dsSlug,
		model.UserEventActor(actorSlug),
		model.EventActionRotate,
	)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.IsActive)
	require.Len(t, events.calls, 1)
	assertDataSourceEvent(t, events.calls[0], orgSlug, actorSlug, dsSlug, model.EventActionRotate, model.DataSourceTypeHubSpot)
}

func TestValidateCredentialLifecycleStopsBeforeConnectorWhenCredentialResolutionFails(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("crm")
	actorSlug := must.Slug("alice")
	resolveErr := fmt.Errorf("runtime credential unavailable")
	events := &fakeDataSourceEventRecorder{}
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:     dsSlug,
			Name:     "CRM",
			Type:     model.DataSourceTypeHubSpot,
			Config:   []byte(`{}`),
			IsActive: true,
		}, nil
	}
	credentials := newFakeCredentialService()
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{}, resolveErr
	}
	connectorCalled := false
	cf := &testutil.FakeConnectorFactory{}
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		connectorCalled = true
		return testutil.NewFakeSQLConnector(), nil
	}
	svc, err := New(
		testutil.FakeTransactionManager{},
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		nil,
		credentials,
		NewNoopGitHubAppManager(),
		NewNoopGitLabWebhookManager(),
		testAdmissionService(),
		cf.NewConnectorFn,
		eventtest.NewProducer(events),
		&fakeDataSourceSchemaImportProducer{},
		ServiceConfig{},
	)
	require.NoError(t, err)

	got, err := svc.ValidateCredentialLifecycle(
		context.Background(),
		orgSlug,
		dsSlug,
		model.UserEventActor(actorSlug),
		model.EventActionRotate,
	)
	require.Error(t, err)
	require.Nil(t, got)
	assert.Contains(t, err.Error(), "runtime credential unavailable")
	assert.False(t, connectorCalled)
	assert.Empty(t, events.calls)
}

func TestRecordCredentialLifecycleEventAllowsRevoke(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("crm")
	actorSlug := must.Slug("alice")
	events := &fakeDataSourceEventRecorder{}
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug: dsSlug,
			Name: "CRM",
			Type: model.DataSourceTypeHubSpot,
		}, nil
	}
	svc := newTestServiceWithEvents(t, repo, testutil.NewFakeSchemaMetadataRepo(), nil, nil, events)

	err := svc.RecordCredentialLifecycleEvent(
		context.Background(),
		orgSlug,
		dsSlug,
		model.UserEventActor(actorSlug),
		model.EventActionRevoke,
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)
	assertDataSourceEvent(t, events.calls[0], orgSlug, actorSlug, dsSlug, model.EventActionRevoke, model.DataSourceTypeHubSpot)
}

func TestCompleteProviderOAuthCallbackValidatesBeforeSuccess(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("crm")
	actorSlug := must.Slug("alice")
	tx := &fakeServiceDBTX{}
	events := &fakeDataSourceEventRecorder{}
	credentials := newFakeCredentialService()
	callbackCtx, cancelCallback := context.WithCancel(context.Background())
	credentials.completeOAuthCallbackFn = func(_ context.Context, params credentialssvc.CompleteOAuthCallbackParams) (credentialssvc.CompleteOAuthCallbackResult, error) {
		require.Equal(t, "fakecrm", params.Provider)
		require.Equal(t, "state-1", params.State)
		require.Equal(t, "code-1", params.Code)
		cancelCallback()
		return credentialssvc.CompleteOAuthCallbackResult{
			InstallSlug: must.Slug("install-1"),
			Owner:       credentialssvc.CredentialOwner{OrgSlug: orgSlug, DataSourceSlug: dsSlug},
			Intent:      credentialssvc.OAuthInstallIntentRotate,
			Actor:       model.UserEventActor(actorSlug),
			Credential: credentialssvc.CredentialMetadata{
				Kind:   credentialssvc.CredentialKindOAuth2Grant,
				Status: credentialssvc.CredentialStatusActive,
			},
		}, nil
	}
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(ctx context.Context, gotTx postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug) (*repods.DataSource, error) {
		require.NoError(t, ctx.Err())
		require.Same(t, tx, gotTx)
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.DataSource{
			Slug:     dsSlug,
			Name:     "CRM",
			Type:     model.DataSourceTypeHubSpot,
			Config:   []byte(`{}`),
			IsActive: true,
		}, nil
	}
	repo.MarkTestSuccessFn = func(ctx context.Context, gotTx postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug, _ *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
		require.NoError(t, ctx.Err())
		require.Same(t, tx, gotTx)
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.DataSource{Slug: dsSlug, Type: model.DataSourceTypeHubSpot, IsActive: true}, nil
	}
	cf := &testutil.FakeConnectorFactory{}
	cf.NewConnectorFn = func(_ context.Context, dsType model.DataSourceType, _, credentialsJSON []byte) (connector.Connector, error) {
		require.Equal(t, model.DataSourceTypeHubSpot, dsType)
		require.Contains(t, string(credentialsJSON), "private_app_token")
		conn := testutil.NewFakeSQLConnector()
		conn.TestConnectionFn = func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			assert.Greater(t, time.Until(deadline), 10*time.Minute)
			return nil
		}
		return conn, nil
	}
	webhooks := &recordingGitLabWebhookManager{}
	svc, err := New(
		testutil.FakeTransactionManager{Tx: tx},
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		nil,
		credentials,
		NewNoopGitHubAppManager(),
		webhooks,
		testAdmissionService(),
		cf.NewConnectorFn,
		eventtest.NewProducer(events),
		&fakeDataSourceSchemaImportProducer{},
		ServiceConfig{},
	)
	require.NoError(t, err)

	completed, err := svc.CompleteProviderOAuthCallback(callbackCtx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    "state-1",
		Code:     "code-1",
	})
	require.NoError(t, err)
	require.ErrorIs(t, callbackCtx.Err(), context.Canceled)
	assert.Equal(t, must.Slug("install-1"), completed.InstallSlug)
	require.Len(t, events.calls, 1)
	assertDataSourceEvent(t, events.calls[0], orgSlug, actorSlug, dsSlug, model.EventActionRotate, model.DataSourceTypeHubSpot)
	assert.Zero(t, webhooks.calls)
}

func TestCompleteProviderOAuthCallbackDoesNotEnableProof(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hookErr error
	}{
		{name: "success"},
		{name: "failure", hookErr: xerror.Unexpected().Detail("hook unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orgSlug, dsSlug := must.Slug("acme"), must.Slug("repo")
			credentials := newFakeCredentialService()
			credentials.completeOAuthCallbackFn = func(context.Context, credentialssvc.CompleteOAuthCallbackParams) (credentialssvc.CompleteOAuthCallbackResult, error) {
				return credentialssvc.CompleteOAuthCallbackResult{
					Owner: credentialssvc.CredentialOwner{OrgSlug: orgSlug, DataSourceSlug: dsSlug},
					Credential: credentialssvc.CredentialMetadata{
						Kind: credentialssvc.CredentialKindOAuth2Grant, Status: credentialssvc.CredentialStatusActive,
						OAuth: credentialssvc.CredentialOAuthMetadata{Provider: "gitlab"},
					},
				}, nil
			}
			credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
				return credentialssvc.RuntimeCredential{Kind: credentialssvc.CredentialKindOAuth2Grant, PlaintextJSON: []byte(`{"access_token":"token"}`)}, nil
			}
			repo := testutil.NewFakeDataSourceRepo()
			repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
				return &repods.DataSource{Slug: dsSlug, Type: model.DataSourceTypeGitlab, Config: []byte(`{"repo_url":"https://gitlab.com/acme/repo.git"}`), IsActive: true}, nil
			}
			repo.MarkTestSuccessFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
				return &repods.DataSource{Slug: dsSlug, Type: model.DataSourceTypeGitlab, IsActive: true}, nil
			}
			factory := &testutil.FakeConnectorFactory{NewConnectorFn: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
				return testutil.NewFakeSQLConnector(), nil
			}}
			webhooks := &recordingGitLabWebhookManager{err: tc.hookErr}
			svc, err := New(testutil.FakeTransactionManager{}, repo, testutil.NewFakeSchemaMetadataRepo(), nil, credentials, NewNoopGitHubAppManager(), webhooks, testAdmissionService(), factory.NewConnectorFn, eventtest.NewProducer(&fakeDataSourceEventRecorder{}), &fakeDataSourceSchemaImportProducer{}, ServiceConfig{})
			require.NoError(t, err)

			_, err = svc.CompleteProviderOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "gitlab"})
			require.NoError(t, err)
			assert.Zero(t, webhooks.calls)
		})
	}
}

func TestRetryGitLabWebhookSetupUsesCurrentOAuthCredential(t *testing.T) {
	manager := &recordingGitLabWebhookManager{}
	credentials := newFakeCredentialService()
	svc := &service{credentials: credentials, gitLabWebhooks: manager}
	ds := &repods.DataSource{Slug: must.Slug("repo"), Type: model.DataSourceTypeGitlab, Config: []byte(`{"repo_url":"https://gitlab.com/acme/repo.git"}`)}

	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{Kind: credentialssvc.CredentialKindOAuth2Grant, PlaintextJSON: []byte(`{"access_token":"token"}`)}, nil
	}
	require.NoError(t, svc.retryGitLabWebhookSetup(t.Context(), must.Slug("acme"), ds))
	assert.Equal(t, 1, manager.calls)
	assert.JSONEq(t, `{"access_token":"token"}`, string(manager.credentialsJSON))
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{Kind: credentialssvc.CredentialKindStaticSecret, PlaintextJSON: []byte(`{"auth_type":"https","username":"oauth2","token":"token"}`)}, nil
	}
	require.NoError(t, svc.retryGitLabWebhookSetup(t.Context(), must.Slug("acme"), ds))
	assert.Equal(t, 2, manager.calls)
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{Kind: credentialssvc.CredentialKindStaticSecret, PlaintextJSON: []byte(`{"token":"pat"}`)}, nil
	}
	require.NoError(t, svc.retryGitLabWebhookSetup(t.Context(), must.Slug("acme"), ds))
	assert.Equal(t, 2, manager.calls)
}

func TestTestExistingConnectionDoesNotEnableProof(t *testing.T) {
	orgSlug, dsSlug := must.Slug("acme"), must.Slug("repo")
	credentials := newFakeCredentialService()
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{Kind: credentialssvc.CredentialKindOAuth2Grant, PlaintextJSON: []byte(`{"access_token":"token"}`)}, nil
	}
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{Slug: dsSlug, Type: model.DataSourceTypeGitlab, Config: []byte(`{"repo_url":"https://gitlab.com/acme/repo.git"}`), IsActive: true}, nil
	}
	repo.MarkTestSuccessFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
		return &repods.DataSource{Slug: dsSlug, Type: model.DataSourceTypeGitlab, Config: []byte(`{"repo_url":"https://gitlab.com/acme/repo.git"}`), IsActive: true}, nil
	}
	factory := &testutil.FakeConnectorFactory{NewConnectorFn: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return testutil.NewFakeSQLConnector(), nil
	}}
	webhooks := &recordingGitLabWebhookManager{}
	svc, err := New(testutil.FakeTransactionManager{}, repo, testutil.NewFakeSchemaMetadataRepo(), nil, credentials, NewNoopGitHubAppManager(), webhooks, testAdmissionService(), factory.NewConnectorFn, eventtest.NewProducer(&fakeDataSourceEventRecorder{}), &fakeDataSourceSchemaImportProducer{}, ServiceConfig{})
	require.NoError(t, err)

	require.NoError(t, svc.TestExistingConnection(t.Context(), orgSlug, dsSlug))
	assert.Zero(t, webhooks.calls)
}

func TestCompleteProviderOAuthCallbackReturnsInvalidInputWhenValidationFails(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("crm")
	events := &fakeDataSourceEventRecorder{}
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:     dsSlug,
			Name:     "CRM",
			Type:     model.DataSourceTypeHubSpot,
			Config:   []byte(`{}`),
			IsActive: true,
		}, nil
	}
	cf := &testutil.FakeConnectorFactory{}
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		conn := testutil.NewFakeSQLConnector()
		conn.TestConnectionFn = func(context.Context) error {
			return xerror.InvalidCredentials()
		}
		return conn, nil
	}
	credentials := newFakeCredentialService()
	credentials.completeOAuthCallbackFn = func(context.Context, credentialssvc.CompleteOAuthCallbackParams) (credentialssvc.CompleteOAuthCallbackResult, error) {
		return credentialssvc.CompleteOAuthCallbackResult{
			Owner:  credentialssvc.CredentialOwner{OrgSlug: orgSlug, DataSourceSlug: dsSlug},
			Intent: credentialssvc.OAuthInstallIntentCreate,
			Actor:  model.ExternalEventActor(),
		}, nil
	}
	svc, err := New(
		testutil.FakeTransactionManager{},
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		nil,
		credentials,
		NewNoopGitHubAppManager(),
		NewNoopGitLabWebhookManager(),
		testAdmissionService(),
		cf.NewConnectorFn,
		eventtest.NewProducer(events),
		&fakeDataSourceSchemaImportProducer{},
		ServiceConfig{},
	)
	require.NoError(t, err)

	_, err = svc.CompleteProviderOAuthCallback(context.Background(), credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    "state-1",
		Code:     "code-1",
	})
	require.ErrorIs(t, err, ErrInvalidInput)
	require.Len(t, events.calls, 1)
	assert.Equal(t, orgSlug, events.calls[0].OrgSlug)
	assert.Equal(t, model.EventActorTypeExternal, events.calls[0].ActorType)
	assert.Nil(t, events.calls[0].ActorSlug)
	require.NotNil(t, events.calls[0].EntitySlug)
	assert.Equal(t, dsSlug, *events.calls[0].EntitySlug)
	assert.Equal(t, model.EventActionCreate, events.calls[0].Action)
}

func TestDeleteRestoreAndRefreshEmitEvents(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("main-db")
	actorSlug := must.Slug("alice")
	events := &fakeDataSourceEventRecorder{}
	enc := testutil.NewFakeEncryptor()
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:                 dsSlug,
			Name:                 "Main DB",
			Type:                 model.DataSourceTypePostgres,
			Config:               []byte(`{"host":"localhost","port":5432,"database":"app","ssl_mode":"verify-full"}`),
			EncryptedCredentials: []byte(`{"username":"app","password":"secret"}`),
			IsActive:             true,
		}, nil
	}
	schemaRepo := testutil.NewFakeSchemaMetadataRepo()
	schemaRepo.UpsertByDataSourceSlugFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug, _ *repods.UpsertSchemaMetadataParams) (*repods.SchemaMetadata, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.SchemaMetadata{Slug: dsSlug, SchemaData: []byte(`{"tables":[]}`)}, nil
	}
	cf := &testutil.FakeConnectorFactory{}
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		conn := testutil.NewFakeSQLConnector()
		conn.IntrospectSchemaFn = func(context.Context) (*sqlconnector.Schema, error) {
			return &sqlconnector.Schema{Tables: []sqlconnector.Table{}}, nil
		}
		return conn, nil
	}

	svc := newTestServiceWithEvents(t, repo, schemaRepo, enc, cf.NewConnectorFn, events)

	require.NoError(t, svc.Delete(context.Background(), orgSlug, dsSlug, model.UserEventActor(actorSlug)))
	require.NoError(t, svc.Restore(context.Background(), orgSlug, dsSlug, model.UserEventActor(actorSlug)))
	_, err := svc.RefreshSchema(context.Background(), orgSlug, dsSlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)

	require.Len(t, events.calls, 3)
	assertDataSourceEvent(t, events.calls[0], orgSlug, actorSlug, dsSlug, model.EventActionDelete, model.DataSourceTypePostgres)
	assertDataSourceEvent(t, events.calls[1], orgSlug, actorSlug, dsSlug, model.EventActionRestore, model.DataSourceTypePostgres)
	assertDataSourceEvent(t, events.calls[2], orgSlug, actorSlug, dsSlug, model.EventActionRefresh, model.DataSourceTypePostgres)
}

func TestCreateReturnsPersistedConnectionAndCredentialState(t *testing.T) {
	t.Parallel()

	const credentialSlug = "credential-main-db"
	testedAt := time.Date(2026, time.October, 27, 12, 0, 0, 0, time.UTC)
	repo := testutil.NewFakeDataSourceRepo()
	repo.CreateFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug: params.Slug, Name: params.Name, Type: params.Type, Config: params.ConfigJSON,
		}, nil
	}
	repo.MarkTestSuccessFn = func(_ context.Context, _ postgres.DBTX, _, dsSlug slug.Slug, _ *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
		return &repods.DataSource{Slug: dsSlug, IsActive: true, LastTestedAt: &testedAt}, nil
	}
	repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _, dsSlug slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug: dsSlug, Name: "Main DB", Type: model.DataSourceTypePostgres, IsActive: true, LastTestedAt: &testedAt,
			Credential: &repods.CredentialMetadata{
				Slug: credentialSlug, Kind: credentialssvc.CredentialKindStaticSecret.String(), Status: credentialssvc.CredentialStatusActive.String(),
			},
		}, nil
	}
	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return testutil.NewFakeSQLConnector(), nil
	}
	svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), cf.NewConnectorFn)

	params := &CreateParams{
		Slug: "main-db", Name: "Main DB", Type: model.DataSourceTypePostgres,
		ConfigJSON:  []byte(`{"host":"db.example.com","port":5432,"database":"app"}`),
		Credentials: map[string]any{"username": "app", "password": "secret"},
	}
	created, err := svc.Create(t.Context(), must.Slug("acme"), model.SystemEventActor(), params)

	require.NoError(t, err)
	require.True(t, created.IsActive)
	require.Equal(t, testedAt, *created.LastTestedAt)
	require.NotNil(t, created.Credential)
	assert.Equal(t, credentialSlug, created.Credential.Slug.String())
	assert.Equal(t, credentialssvc.CredentialStatusActive, created.Credential.Status)

	t.Run("post-test lookup failure", func(t *testing.T) {
		lookupErr := xerror.Unexpected().Detail("private lookup diagnostic")
		repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
			return nil, lookupErr
		}
		created, err := svc.Create(t.Context(), must.Slug("acme"), model.SystemEventActor(), params)
		require.Nil(t, created)
		require.ErrorIs(t, err, lookupErr)
		assert.Contains(t, xerror.PublicDetail(err), "was created")
		assert.Contains(t, xerror.PublicDetail(err), "data-source get main-db")
		assert.NotContains(t, xerror.PublicDetail(err), "private lookup diagnostic")
	})
}

func TestCreateConnectionFailureReportsRetainedInactiveDataSource(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	repo.CreateFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
		return &repods.DataSource{Slug: params.Slug, Name: params.Name, Type: params.Type, Config: params.ConfigJSON}, nil
	}
	repo.MarkTestFailureFn = func(_ context.Context, _ postgres.DBTX, _, dsSlug slug.Slug, _ int64, _ string, _ json.RawMessage) (*repods.DataSource, error) {
		return &repods.DataSource{Slug: dsSlug, IsActive: false}, nil
	}
	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		conn := testutil.NewFakeSQLConnector()
		conn.TestConnectionFn = func(context.Context) error {
			return ErrConnectionFailed.WithPublicDetail("database endpoint unavailable")
		}
		return conn, nil
	}
	svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), cf.NewConnectorFn)

	created, err := svc.Create(t.Context(), must.Slug("acme"), model.SystemEventActor(), &CreateParams{
		Slug: "main-db", Name: "Main DB", Type: model.DataSourceTypePostgres,
		ConfigJSON:  []byte(`{"host":"db.example.com","port":5432,"database":"app"}`),
		Credentials: map[string]any{"username": "app", "password": "secret"},
	})

	require.Nil(t, created)
	require.ErrorIs(t, err, ErrInvalidInput)
	detail := xerror.PublicDetail(err)
	assert.Contains(t, detail, "data-source get main-db")
	assert.Contains(t, detail, "data-source update --file <request.json>")
	assert.Contains(t, detail, "data-source credentials update --file <request.json>")
	assert.Contains(t, detail, "data-source test-stored main-db")
	assert.NotContains(t, detail, "secret")
}

func TestCreateDataSource(t *testing.T) {
	t.Parallel()

	pgCreds := map[string]any{
		"username": "admin",
		"password": "secret",
	}

	mysqlCreds := map[string]any{
		"username": "app",
		"password": "mysqlpass",
	}

	httpCreds := map[string]any{
		"bearer_token": "token-123",
	}

	hubspotCreds := map[string]any{
		"private_app_token": "pat-na1-test-token",
	}

	tests := []struct {
		name            string
		setup           func(repo *testutil.FakeDataSourceRepo, enc *testutil.FakeEncryptor, cf *testutil.FakeConnectorFactory)
		orgSlug         slug.Slug
		params          *CreateParams
		wantDataSource  *DataSource
		wantErr         error
		wantErrContains string
	}{
		{
			name: "CreatesDataSourceWithValidCredentials",
			setup: func(repo *testutil.FakeDataSourceRepo, enc *testutil.FakeEncryptor, cf *testutil.FakeConnectorFactory) {
				// Mock successful data source creation
				repo.CreateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
					encCreds, _ := enc.Encrypt(func() []byte {
						b, _ := json.Marshal(pgCreds)
						return b
					}())
					return &repods.DataSource{
						Slug:                 params.Slug,
						Name:                 params.Name,
						Type:                 params.Type,
						Config:               params.ConfigJSON,
						EncryptedCredentials: encCreds,
						IsActive:             true,
					}, nil
				}

				// Mock successful TestConnection
				repo.MarkTestSuccessFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, _ *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
					now := time.Now()
					return &repods.DataSource{
						Slug:         dsSlug,
						Name:         "Test DB",
						Type:         model.DataSourceTypePostgres,
						IsActive:     true,
						LastTestedAt: &now,
						TestError:    nil,
						Schema:       nil,
					}, nil
				}

				// Mock connector that succeeds TestConnection
				cf.NewConnectorFn = func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
					mockConnector := testutil.NewFakeSQLConnector()
					mockConnector.TestConnectionFn = func(ctx context.Context) error {
						return nil // Connection succeeds
					}
					return mockConnector, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			params: &CreateParams{
				Name:        "DB",
				Type:        model.DataSourceTypePostgres,
				ConfigJSON:  []byte(`{"host":"localhost","port":5432,"database":"testdb"}`),
				Credentials: pgCreds,
			},
			wantDataSource: &DataSource{
				Name: "DB",
				Type: model.DataSourceTypePostgres,
			},
		},
		{
			name: "CreatesMySQLDataSourceWithValidCredentials",
			setup: func(repo *testutil.FakeDataSourceRepo, enc *testutil.FakeEncryptor, cf *testutil.FakeConnectorFactory) {
				repo.CreateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
					encCreds, _ := enc.Encrypt(func() []byte {
						b, _ := json.Marshal(mysqlCreds)
						return b
					}())
					return &repods.DataSource{
						Slug:                 params.Slug,
						Name:                 params.Name,
						Type:                 params.Type,
						Config:               params.ConfigJSON,
						EncryptedCredentials: encCreds,
						IsActive:             true,
					}, nil
				}

				repo.MarkTestSuccessFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, _ *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
					now := time.Now()
					return &repods.DataSource{
						Slug:         dsSlug,
						Name:         "MySQL DB",
						Type:         model.DataSourceTypeMySQL,
						IsActive:     true,
						LastTestedAt: &now,
						TestError:    nil,
						Schema:       nil,
					}, nil
				}

				cf.NewConnectorFn = func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
					mockConnector := testutil.NewFakeSQLConnector()
					mockConnector.TestConnectionFn = func(ctx context.Context) error {
						return nil
					}
					return mockConnector, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			params: &CreateParams{
				Name:        "MySQL DB",
				Type:        model.DataSourceTypeMySQL,
				ConfigJSON:  []byte(`{"host":"localhost","port":3306,"database":"mysqldb"}`),
				Credentials: mysqlCreds,
			},
			wantDataSource: &DataSource{
				Name: "MySQL DB",
				Type: model.DataSourceTypeMySQL,
			},
		},
		{
			name: "CreatesHTTPDataSourceWithValidCredentials",
			setup: func(repo *testutil.FakeDataSourceRepo, enc *testutil.FakeEncryptor, cf *testutil.FakeConnectorFactory) {
				repo.CreateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
					encCreds, _ := enc.Encrypt(func() []byte {
						b, _ := json.Marshal(httpCreds)
						return b
					}())
					return &repods.DataSource{
						Slug:                 params.Slug,
						Name:                 params.Name,
						Type:                 params.Type,
						Config:               params.ConfigJSON,
						EncryptedCredentials: encCreds,
						IsActive:             true,
					}, nil
				}

				repo.MarkTestSuccessFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, _ *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
					now := time.Now()
					return &repods.DataSource{
						Slug:         dsSlug,
						Name:         "HTTP API",
						Type:         model.DataSourceTypeHTTP,
						IsActive:     true,
						LastTestedAt: &now,
						TestError:    nil,
						Schema:       nil,
					}, nil
				}

				cf.NewConnectorFn = func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
					mockConnector := testutil.NewFakeSQLConnector()
					mockConnector.TestConnectionFn = func(ctx context.Context) error {
						return nil
					}
					return mockConnector, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			params: &CreateParams{
				Name: "HTTP API",
				Type: model.DataSourceTypeHTTP,
				ConfigJSON: []byte(
					`{"base_url":"https://api.example.com","health_path":"/health"}`,
				),
				Credentials: httpCreds,
			},
			wantDataSource: &DataSource{
				Name: "HTTP API",
				Type: model.DataSourceTypeHTTP,
			},
		},
		{
			name: "CreatesHubSpotDataSourceWithValidCredentials",
			setup: func(repo *testutil.FakeDataSourceRepo, enc *testutil.FakeEncryptor, cf *testutil.FakeConnectorFactory) {
				repo.CreateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
					require.Equal(t, model.DataSourceTypeHubSpot, params.Type)
					require.True(t, params.ReadOnly)

					encCreds, _ := enc.Encrypt(func() []byte {
						b, _ := json.Marshal(hubspotCreds)
						return b
					}())
					return &repods.DataSource{
						Slug:                 params.Slug,
						Name:                 params.Name,
						Type:                 params.Type,
						Config:               params.ConfigJSON,
						EncryptedCredentials: encCreds,
						IsActive:             true,
						ReadOnly:             true,
					}, nil
				}

				repo.MarkTestSuccessFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, _ *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
					now := time.Now()
					return &repods.DataSource{
						Slug:         dsSlug,
						Name:         "HubSpot CRM",
						Type:         model.DataSourceTypeHubSpot,
						IsActive:     true,
						ReadOnly:     true,
						LastTestedAt: &now,
						TestError:    nil,
						Schema:       nil,
					}, nil
				}

				cf.NewConnectorFn = func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
					require.Equal(t, model.DataSourceTypeHubSpot, dsType)
					require.JSONEq(t, `{"timeout_seconds":15}`, string(configJSON))
					require.JSONEq(t, `{"private_app_token":"pat-na1-test-token"}`, string(credentialsJSON))

					mockConnector := testutil.NewFakeSQLConnector()
					mockConnector.TestConnectionFn = func(ctx context.Context) error {
						return nil
					}
					return mockConnector, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			params: &CreateParams{
				Name:        "HubSpot CRM",
				Type:        model.DataSourceTypeHubSpot,
				ConfigJSON:  []byte(`{"timeout_seconds":15}`),
				Credentials: hubspotCreds,
				ReadOnly:    true,
			},
			wantDataSource: &DataSource{
				Name: "HubSpot CRM",
				Type: model.DataSourceTypeHubSpot,
			},
		},
		{
			name: "ReturnsErrWhenRepositoryFails",
			setup: func(repo *testutil.FakeDataSourceRepo, enc *testutil.FakeEncryptor, cf *testutil.FakeConnectorFactory) {
				repo.CreateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
					return nil, repods.ErrDataSourceNotFound
				}
			},
			orgSlug: must.Slug("acme-corp"),
			params: &CreateParams{
				Name:        "DB",
				Type:        model.DataSourceTypePostgres,
				ConfigJSON:  []byte(`{"host":"localhost","port":5432,"database":"testdb"}`),
				Credentials: pgCreds,
			},
			wantErr: repods.ErrDataSourceNotFound,
		},
		{
			name: "ReturnsErrWhenConnectionTestFailsWithBadCredentials",
			setup: func(repo *testutil.FakeDataSourceRepo, enc *testutil.FakeEncryptor, cf *testutil.FakeConnectorFactory) {
				repo.CreateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
					encCreds, _ := enc.Encrypt(func() []byte {
						b, _ := json.Marshal(pgCreds)
						return b
					}())
					return &repods.DataSource{
						Slug:                 params.Slug,
						Name:                 params.Name,
						Type:                 params.Type,
						Config:               params.ConfigJSON,
						EncryptedCredentials: encCreds,
						IsActive:             true,
					}, nil
				}
				// Mock connector creation to fail with invalid credentials
				cf.NewConnectorFn = func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
					return nil, ErrInvalidInput // Simulate invalid credentials
				}
			},
			orgSlug: must.Slug("acme-corp"),
			params: &CreateParams{
				Name:        "Bad DB",
				Type:        model.DataSourceTypePostgres,
				ConfigJSON:  []byte(`{"host":"localhost","port":5432,"database":"testdb"}`),
				Credentials: pgCreds,
			},
			wantErrContains: "failed to create connector",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			enc := testutil.NewFakeEncryptor()
			cf := testutil.NewFakeConnectorFactory()
			if tc.setup != nil {
				tc.setup(repo, enc, cf)
			}
			repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _, dsSlug slug.Slug) (*repods.DataSource, error) {
				return &repods.DataSource{Slug: dsSlug, Name: tc.params.Name, Type: tc.params.Type, ReadOnly: tc.params.ReadOnly}, nil
			}
			schemaRepo := testutil.NewFakeSchemaMetadataRepo()
			svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

			result, err := svc.Create(
				context.Background(),
				tc.orgSlug,
				model.UserEventActor(must.Slug("alice")),
				tc.params,
			)

			if tc.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, result)
				return
			}
			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.wantErrContains)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tc.wantDataSource.Name, result.Name)
			assert.Equal(t, tc.wantDataSource.Type, result.Type)
		})
	}
}

func TestTestConnection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		setup            func(cf *testutil.FakeConnectorFactory)
		params           *TestConnectionParams
		wantErr          error
		wantErrContains  string
		wantPublicDetail string
	}{
		{
			name: "ReturnsNilWhenConnectorConnectionSucceeds",
			setup: func(cf *testutil.FakeConnectorFactory) {
				cf.NewConnectorFn = func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
					mockConnector := testutil.NewFakeSQLConnector()
					mockConnector.TestConnectionFn = func(ctx context.Context) error {
						return nil
					}
					return mockConnector, nil
				}
			},
			params: &TestConnectionParams{
				Type:        model.DataSourceTypePostgres,
				ConfigJSON:  []byte(`{"host":"localhost","port":5432,"database":"app"}`),
				Credentials: map[string]any{"username": "admin", "password": "secret"},
			},
		},
		{
			name: "ReturnsErrConnectionFailedWhenConnectorFactoryFails",
			setup: func(cf *testutil.FakeConnectorFactory) {
				cf.NewConnectorFn = func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
					return nil, fmt.Errorf("dial failed")
				}
			},
			params: &TestConnectionParams{
				Type:        model.DataSourceTypePostgres,
				ConfigJSON:  []byte(`{"host":"localhost","port":5432,"database":"app"}`),
				Credentials: map[string]any{"username": "admin", "password": "secret"},
			},
			wantErr:         ErrConnectionFailed,
			wantErrContains: "failed to create connector",
		},
		{
			name: "ReturnsErrConnectionFailedWhenConnectorTestFails",
			setup: func(cf *testutil.FakeConnectorFactory) {
				cf.NewConnectorFn = func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
					mockConnector := testutil.NewFakeSQLConnector()
					mockConnector.TestConnectionFn = func(ctx context.Context) error {
						return fmt.Errorf("timeout")
					}
					return mockConnector, nil
				}
			},
			params: &TestConnectionParams{
				Type:        model.DataSourceTypePostgres,
				ConfigJSON:  []byte(`{"host":"localhost","port":5432,"database":"app"}`),
				Credentials: map[string]any{"username": "admin", "password": "secret"},
			},
			wantErr:         ErrConnectionFailed,
			wantErrContains: "timeout",
		},
		{
			name: "ReturnsErrConnectionFailedWithConnectorPublicDetail",
			setup: func(cf *testutil.FakeConnectorFactory) {
				cf.NewConnectorFn = func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
					mockConnector := testutil.NewFakeSQLConnector()
					mockConnector.TestConnectionFn = func(ctx context.Context) error {
						return xerror.Unavailable(xerror.RepoFile).
							WithPublicDetail("git ls-remote failed: repository not found").
							Detail("repository not found")
					}
					return mockConnector, nil
				}
			},
			params: &TestConnectionParams{
				Type:        model.DataSourceTypePostgres,
				ConfigJSON:  []byte(`{"host":"localhost","port":5432,"database":"app"}`),
				Credentials: map[string]any{"username": "admin", "password": "secret"},
			},
			wantErr:          ErrConnectionFailed,
			wantErrContains:  "repository not found",
			wantPublicDetail: "git ls-remote failed: repository not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			schemaRepo := testutil.NewFakeSchemaMetadataRepo()
			enc := testutil.NewFakeEncryptor()
			cf := testutil.NewFakeConnectorFactory()
			if tc.setup != nil {
				tc.setup(cf)
			}

			svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)
			err := svc.TestConnection(context.Background(), must.Slug("acme-corp"), tc.params)

			if tc.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.wantErr)
				if tc.wantErrContains != "" {
					require.ErrorContains(t, err, tc.wantErrContains)
				}
				if tc.wantPublicDetail != "" {
					require.Equal(t, tc.wantPublicDetail, xerror.PublicDetail(err))
				}
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestPrivateDataSourceHostPolicyRejectsCustomerOptInByDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		dsType      model.DataSourceType
		configJSON  []byte
		credentials map[string]any
	}{
		{
			name:       "Postgres",
			dsType:     model.DataSourceTypePostgres,
			configJSON: []byte(`{"host":"db.example.com","port":5432,"database":"app","allow_private_hosts":true}`),
			credentials: map[string]any{
				"username": "pg-user",
				"password": "secret",
			},
		},
		{
			name:       "MySQL",
			dsType:     model.DataSourceTypeMySQL,
			configJSON: []byte(`{"host":"db.example.com","port":3306,"database":"app","allow_private_hosts":true}`),
			credentials: map[string]any{
				"username": "my-user",
				"password": "secret",
			},
		},
		{
			name:       "MongoDB",
			dsType:     model.DataSourceTypeMongoDB,
			configJSON: []byte(`{"host":"db.example.com","port":27017,"database":"app","allow_private_hosts":true}`),
			credentials: map[string]any{
				"username": "mongo-user",
				"password": "secret",
			},
		},
		{
			name:       "HTTP",
			dsType:     model.DataSourceTypeHTTP,
			configJSON: []byte(`{"base_url":"https://api.example.com","allow_private_hosts":true}`),
			credentials: map[string]any{
				"bearer_token": "token-123",
			},
		},
		{
			name:       "FTP",
			dsType:     model.DataSourceTypeFTP,
			configJSON: []byte(`{"host":"ftp.example.com","port":21,"allow_private_hosts":true}`),
			credentials: map[string]any{
				"username": "ftp-user",
				"password": "secret",
			},
		},
		{
			name:       "Git",
			dsType:     model.DataSourceTypeGit,
			configJSON: []byte(`{"repo_url":"https://github.com/acme/repo.git","allow_private_hosts":true}`),
			credentials: map[string]any{
				"auth_type": "https",
			},
		},
		{
			name:       "GitLab",
			dsType:     model.DataSourceTypeGitlab,
			configJSON: []byte(`{"repo_url":"https://gitlab.com/acme/repo.git","allow_private_hosts":true}`),
			credentials: map[string]any{
				"auth_type": "https",
			},
		},
		{
			name:       "S3",
			dsType:     model.DataSourceTypeS3,
			configJSON: []byte(`{"endpoint":"https://s3.example.com","region":"us-east-1","bucket":"fixtures","allow_private_hosts":true}`),
			credentials: map[string]any{
				"access_key_id":     "access-key",
				"secret_access_key": "secret-key",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cf := testutil.NewFakeConnectorFactory()
			cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
				t.Fatalf("connector factory should not run when private host opt-in is disabled")
				return nil, assert.AnError
			}
			svc := newTestService(
				testutil.NewFakeDataSourceRepo(),
				testutil.NewFakeSchemaMetadataRepo(),
				testutil.NewFakeEncryptor(),
				cf.NewConnectorFn,
			)

			err := svc.TestConnection(context.Background(), must.Slug("acme-corp"), &TestConnectionParams{
				Type:        tc.dsType,
				ConfigJSON:  tc.configJSON,
				Credentials: tc.credentials,
			})

			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalidInput)
			if tc.dsType == model.DataSourceTypeGitlab {
				require.ErrorContains(t, err, "Use Generic Git")
			} else {
				require.ErrorContains(t, err, "allow_private_hosts is disabled for this deployment")
			}
		})
	}
}

func TestPrivateDataSourceHostPolicyAllowsCustomerOptInWhenEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		dsType      model.DataSourceType
		configJSON  []byte
		credentials map[string]any
	}{
		{
			name:       "Postgres",
			dsType:     model.DataSourceTypePostgres,
			configJSON: []byte(`{"host":"db.example.com","port":5432,"database":"app","allow_private_hosts":true}`),
			credentials: map[string]any{
				"username": "pg-user",
				"password": "secret",
			},
		},
		{
			name:       "MySQL",
			dsType:     model.DataSourceTypeMySQL,
			configJSON: []byte(`{"host":"db.example.com","port":3306,"database":"app","allow_private_hosts":true}`),
			credentials: map[string]any{
				"username": "my-user",
				"password": "secret",
			},
		},
		{
			name:       "MongoDB",
			dsType:     model.DataSourceTypeMongoDB,
			configJSON: []byte(`{"host":"db.example.com","port":27017,"database":"app","allow_private_hosts":true}`),
			credentials: map[string]any{
				"username": "mongo-user",
				"password": "secret",
			},
		},
		{
			name:       "HTTP",
			dsType:     model.DataSourceTypeHTTP,
			configJSON: []byte(`{"base_url":"https://api.example.com","allow_private_hosts":true}`),
			credentials: map[string]any{
				"bearer_token": "token-123",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			called := false
			cf := testutil.NewFakeConnectorFactory()
			cf.NewConnectorFn = func(
				_ context.Context,
				dsType model.DataSourceType,
				configJSON []byte,
				_ []byte,
			) (connector.Connector, error) {
				called = true
				require.Equal(t, tc.dsType, dsType)
				require.JSONEq(t, string(tc.configJSON), string(configJSON))
				return testutil.NewFakeSQLConnector(), nil
			}
			svc := newTestServiceWithPolicy(
				testutil.NewFakeDataSourceRepo(),
				testutil.NewFakeSchemaMetadataRepo(),
				testutil.NewFakeEncryptor(),
				cf.NewConnectorFn,
				CustomerConfigPolicy{AllowPrivateDataSourceHosts: true},
			)

			err := svc.TestConnection(context.Background(), must.Slug("acme-corp"), &TestConnectionParams{
				Type:        tc.dsType,
				ConfigJSON:  tc.configJSON,
				Credentials: tc.credentials,
			})

			require.NoError(t, err)
			require.True(t, called)
		})
	}
}

func TestPrivateDataSourceHostPolicyPersistsCustomerOptInWhenEnabled(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	repo.CreateFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
		require.JSONEq(t, `{"host":"db.example.com","port":5432,"database":"app","allow_private_hosts":true}`, string(params.ConfigJSON))
		return &repods.DataSource{
			Slug:     params.Slug,
			Name:     params.Name,
			Type:     params.Type,
			Config:   params.ConfigJSON,
			IsActive: true,
		}, nil
	}
	repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _, dsSlug slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{Slug: dsSlug, Name: "Main DB", Type: model.DataSourceTypePostgres, Config: []byte(`{"host":"db.example.com","port":5432,"database":"app","allow_private_hosts":true}`), IsActive: true}, nil
	}
	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return testutil.NewFakeSQLConnector(), nil
	}
	svc := newTestServiceWithPolicy(
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		testutil.NewFakeEncryptor(),
		cf.NewConnectorFn,
		CustomerConfigPolicy{AllowPrivateDataSourceHosts: true},
	)

	result, err := svc.Create(
		context.Background(),
		must.Slug("acme-corp"),
		model.SystemEventActor(),
		&CreateParams{
			Name:       "Main DB",
			Type:       model.DataSourceTypePostgres,
			ConfigJSON: []byte(`{"host":"db.example.com","port":5432,"database":"app","allow_private_hosts":true}`),
			Credentials: map[string]any{
				"username": "pg-user",
				"password": "secret",
			},
		},
	)

	require.NoError(t, err)
	require.JSONEq(t, `{"host":"db.example.com","port":5432,"database":"app","allow_private_hosts":true}`, string(result.Config))
}

func TestPrivateDataSourceHostPolicyRejectsCreateBeforePersisting(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	repo.CreateFn = func(context.Context, postgres.DBTX, slug.Slug, *repods.CreateParams) (*repods.DataSource, error) {
		t.Fatalf("repo create should not run when private host opt-in is disabled")
		return nil, assert.AnError
	}
	svc := newTestService(
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		testutil.NewFakeEncryptor(),
		nil,
	)

	_, err := svc.Create(
		context.Background(),
		must.Slug("acme-corp"),
		model.SystemEventActor(),
		&CreateParams{
			Name:       "Customer API",
			Type:       model.DataSourceTypeHTTP,
			ConfigJSON: []byte(`{"base_url":"https://api.example.com","allow_private_hosts":true}`),
			Credentials: map[string]any{
				"bearer_token": "token-123",
			},
		},
	)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidInput)
	require.ErrorContains(t, err, "allow_private_hosts is disabled for this deployment")
}

func TestPrivateDataSourceHostPolicyRejectsUpdateBeforePersisting(t *testing.T) {
	t.Parallel()

	dsSlug := must.Slug("customer-api")
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(
		context.Context,
		postgres.DBTX,
		slug.Slug,
		slug.Slug,
	) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:     dsSlug,
			Name:     "Customer API",
			Type:     model.DataSourceTypeHTTP,
			Config:   []byte(`{"base_url":"https://api.example.com"}`),
			IsActive: true,
		}, nil
	}
	repo.UpdateFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.UpdateParams) (*repods.DataSource, error) {
		t.Fatalf("repo update should not run when private host opt-in is disabled")
		return nil, assert.AnError
	}
	svc := newTestService(
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		testutil.NewFakeEncryptor(),
		nil,
	)

	_, err := svc.Update(
		context.Background(),
		must.Slug("acme-corp"),
		dsSlug,
		model.SystemEventActor(),
		&UpdateParams{
			Name:       "Customer API",
			ConfigJSON: []byte(`{"base_url":"https://api.example.com","allow_private_hosts":true}`),
		},
	)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidInput)
	require.ErrorContains(t, err, "allow_private_hosts is disabled for this deployment")
}

func TestTestConnection_GitFailureFromRealConnectorHasPublicDetail(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	schemaRepo := testutil.NewFakeSchemaMetadataRepo()
	svc := newTestServiceWithPolicy(
		repo,
		schemaRepo,
		testutil.NewFakeEncryptor(),
		nil,
		CustomerConfigPolicy{AllowPrivateDataSourceHosts: true},
	)

	err := svc.TestConnection(context.Background(), must.Slug("acme-corp"), &TestConnectionParams{
		Type:       model.DataSourceTypeGit,
		ConfigJSON: []byte(`{"repo_url":"ssh://git@127.0.0.1:1/acme/repo.git","allow_private_hosts":true}`),
		Credentials: map[string]any{
			"auth_type": gitconnector.AuthTypeHTTPS,
		},
	})

	require.Error(t, err)
	require.ErrorIs(t, err, ErrConnectionFailed)
	require.Contains(t, xerror.PublicDetail(err), "git ls-remote failed")
}

func TestTestExistingConnectionUsesPersistedConfigAndStoredCredentials(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("prod-db")
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.DataSource{
			Slug:     dsSlug,
			Name:     "Production DB",
			Type:     model.DataSourceTypePostgres,
			Config:   []byte(`{"host":"db.internal","port":5432,"database":"app"}`),
			IsActive: true,
		}, nil
	}
	markedSuccess := false
	var testedAt time.Time
	repo.MarkTestSuccessFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug, params *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		require.False(t, params.TestedAt.IsZero())
		testedAt = params.TestedAt
		markedSuccess = true
		return &repods.DataSource{Slug: dsSlug, Type: model.DataSourceTypePostgres, IsActive: true}, nil
	}
	activatedTrial := false
	admission := &activationPolicy{activate: func(_ context.Context, gotOrg, gotSource slug.Slug, at time.Time) error {
		require.Equal(t, orgSlug, gotOrg)
		require.Equal(t, dsSlug, gotSource)
		require.Equal(t, testedAt, at)
		activatedTrial = true
		return nil
	}}

	repo.MarkTestFailureFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, int64, string, json.RawMessage) (*repods.DataSource, error) {
		t.Fatal("did not expect connection test failure to be recorded")
		return nil, xerror.Unexpected().Detail("unexpected connection test failure")
	}
	credentials := newFakeCredentialService()
	credentials.resolveRuntimeFn = func(_ context.Context, params credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		require.Equal(t, orgSlug, params.Owner.OrgSlug)
		require.Equal(t, dsSlug, params.Owner.DataSourceSlug)
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindStaticSecret,
			PlaintextJSON: []byte(`{"username":"app","password":"secret"}`),
		}, nil
	}
	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(_ context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
		require.Equal(t, model.DataSourceTypePostgres, dsType)
		require.JSONEq(t, `{"host":"db.internal","port":5432,"database":"app"}`, string(configJSON))
		require.JSONEq(t, `{"username":"app","password":"secret"}`, string(credentialsJSON))
		return testutil.NewFakeSQLConnector(), nil
	}
	svc, err := New(
		testutil.FakeTransactionManager{},
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		nil,
		credentials,
		NewNoopGitHubAppManager(),
		NewNoopGitLabWebhookManager(),
		admission,
		cf.NewConnectorFn,
		eventtest.NewProducer(&fakeDataSourceEventRecorder{}),
		&fakeDataSourceSchemaImportProducer{},
		ServiceConfig{},
	)
	require.NoError(t, err)

	err = svc.TestExistingConnection(context.Background(), orgSlug, dsSlug)
	require.NoError(t, err)
	require.True(t, markedSuccess)
	require.True(t, activatedTrial)
}

func TestTestExistingConnectionRejectsPendingProviderRepositorySelection(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("pending-gitlab")
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:   dsSlug,
			Type:   model.DataSourceTypeGitlab,
			Config: []byte(`{"provider":"gitlab"}`),
		}, nil
	}
	connectorCreated := false
	svc := newTestService(
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		testutil.NewFakeEncryptor(),
		func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
			connectorCreated = true
			return testutil.NewFakeSQLConnector(), nil
		},
	)

	err := svc.TestExistingConnection(context.Background(), orgSlug, dsSlug)

	require.ErrorIs(t, err, ErrInvalidInput)
	require.Equal(t, "Select a provider repository before using this data source.", xerror.PublicDetail(err))
	require.False(t, connectorCreated)
}

func TestTestExistingConnectionRetriesSnapshotAfterCredentialRefresh(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("oauth-source")
	repo := testutil.NewFakeDataSourceRepo()
	refreshed := false
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		revision := int64(7)
		if refreshed {
			revision = 8
		}
		return &repods.DataSource{
			Slug:                   dsSlug,
			Type:                   model.DataSourceTypePostgres,
			Config:                 []byte(`{"host":"db.internal","port":5432,"database":"app"}`),
			ConnectionTestRevision: revision,
		}, nil
	}
	credentials := newFakeCredentialService()
	resolveCalls := 0
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		resolveCalls++
		refreshed = true
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindOAuth2Grant,
			PlaintextJSON: []byte(`{"access_token":"refreshed"}`),
		}, nil
	}
	repo.MarkTestSuccessFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_ slug.Slug,
		_ slug.Slug,
		params *repods.MarkTestSuccessParams,
	) (*repods.DataSource, error) {
		assert.Equal(t, int64(8), params.ExpectedRevision)
		return &repods.DataSource{
			Slug:                   dsSlug,
			Type:                   model.DataSourceTypePostgres,
			IsActive:               true,
			ConnectionTestRevision: 9,
		}, nil
	}
	repo.MarkTestFailureFn = func(
		context.Context,
		postgres.DBTX,
		slug.Slug,
		slug.Slug,
		int64,
		string,
		json.RawMessage,
	) (*repods.DataSource, error) {
		return nil, xerror.Unexpected().Detail("did not expect connection test failure")
	}
	executor := connectionTestExecutorFunc(func(context.Context, ConnectionTestRequest) error {
		return nil
	})
	svc, err := New(
		testutil.FakeTransactionManager{},
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		nil,
		credentials,
		NewNoopGitHubAppManager(),
		NewNoopGitLabWebhookManager(),
		testAdmissionService(),
		nil,
		eventtest.NewProducer(&fakeDataSourceEventRecorder{}),
		&fakeDataSourceSchemaImportProducer{},
		ServiceConfig{ConnectionTestExecutor: executor},
	)
	require.NoError(t, err)

	err = svc.TestExistingConnection(context.Background(), orgSlug, dsSlug)

	require.NoError(t, err)
	assert.Equal(t, 2, resolveCalls)
}

func TestTestExistingConnectionPreservesConnectionFailureType(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("prod-db")
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.DataSource{
			Slug:     dsSlug,
			Name:     "Production DB",
			Type:     model.DataSourceTypePostgres,
			Config:   []byte(`{"host":"db.internal","port":5432,"database":"app"}`),
			IsActive: true,
		}, nil
	}
	markedFailure := false
	repo.MarkTestFailureFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug, _ int64, errMsg string, schema json.RawMessage) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		require.Equal(t, "Connection test failed. Check the connection settings and service availability. If the problem persists, contact an administrator.", errMsg)
		require.Nil(t, schema)
		markedFailure = true
		return &repods.DataSource{Slug: dsSlug, Type: model.DataSourceTypePostgres, IsActive: false}, nil
	}
	repo.MarkTestSuccessFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
		t.Fatal("did not expect connection test success to be recorded")
		return nil, xerror.Unexpected().Detail("unexpected connection test success")
	}
	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		conn := testutil.NewFakeSQLConnector()
		conn.TestConnectionFn = func(context.Context) error {
			return fmt.Errorf("dial timeout")
		}
		return conn, nil
	}
	svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), cf.NewConnectorFn)

	err := svc.TestExistingConnection(context.Background(), orgSlug, dsSlug)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrConnectionFailed)
	require.NotErrorIs(t, err, ErrInvalidInput)
	require.ErrorContains(t, err, "dial timeout")
	require.True(t, markedFailure)
}

func TestTestExistingConnectionRunsNetworkOutsideTransactionsAndFinalizesAfterTimeout(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("prod-db")
	txm := &resolveGitAuthTransactionManager{}
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:                   dsSlug,
			Name:                   "Production DB",
			Type:                   model.DataSourceTypePostgres,
			Config:                 []byte(`{"host":"db.internal","port":5432,"database":"app"}`),
			ConnectionTestRevision: 7,
		}, nil
	}
	finalized := false
	repo.MarkTestFailureFn = func(
		ctx context.Context,
		_ postgres.DBTX,
		_ slug.Slug,
		_ slug.Slug,
		expectedRevision int64,
		diagnostic string,
		_ json.RawMessage,
	) (*repods.DataSource, error) {
		require.True(t, txm.inTx)
		require.NoError(t, ctx.Err())
		assert.Equal(t, int64(7), expectedRevision)
		assert.Contains(t, diagnostic, "Connection test timed out.")
		finalized = true
		return &repods.DataSource{ConnectionTestRevision: 8}, nil
	}
	credentials := newFakeCredentialService()
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{PlaintextJSON: []byte(`{"username":"app","password":"secret"}`)}, nil
	}
	executor := connectionTestExecutorFunc(func(ctx context.Context, request ConnectionTestRequest) error {
		require.False(t, txm.inTx)
		assert.Equal(t, orgSlug, request.OrganizationSlug)
		<-ctx.Done()
		return ErrConnectionFailed.With(ctx.Err())
	})
	svc, err := New(
		txm,
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		nil,
		credentials,
		NewNoopGitHubAppManager(),
		NewNoopGitLabWebhookManager(),
		testAdmissionService(),
		testutil.NewFakeConnectorFactory().NewConnectorFn,
		eventtest.NewProducer(&fakeDataSourceEventRecorder{}),
		&fakeDataSourceSchemaImportProducer{},
		ServiceConfig{ConnectionTestExecutor: executor},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err = svc.TestExistingConnection(ctx, orgSlug, dsSlug)

	require.ErrorIs(t, err, ErrConnectionFailed)
	assert.True(t, finalized)
	assert.False(t, txm.inTx)
}

func TestFindBySlug(t *testing.T) {
	t.Parallel()
	credentialUpdatedAt := time.Date(2026, time.July, 17, 9, 30, 0, 0, time.UTC)
	accessTokenExpiresAt := credentialUpdatedAt.Add(time.Hour)
	lastRefreshedAt := credentialUpdatedAt.Add(-time.Minute)

	tests := []struct {
		name           string
		setup          func(repo *testutil.FakeDataSourceRepo, schemaRepo *testutil.FakeSchemaMetadataRepo)
		orgSlug        slug.Slug
		dsSlug         slug.Slug
		wantDataSource *DataSource
		wantSchema     SchemaResult // nil means no schema expected
		wantCached     bool
		wantErr        error
	}{
		{
			name: "FindsDataSourceBySlug",
			setup: func(repo *testutil.FakeDataSourceRepo, schemaRepo *testutil.FakeSchemaMetadataRepo) {
				repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
					if dsSlug == must.Slug("prod-db") {
						return &repods.DataSource{
							Slug: dsSlug,
							Name: "Production DB",
							Type: model.DataSourceTypePostgres,
							Credential: &repods.CredentialMetadata{
								Slug:                 must.Slug("credential-prod-db"),
								Kind:                 "oauth2_grant",
								Status:               "pending",
								Provider:             "hubspot",
								UpdatedAt:            credentialUpdatedAt,
								AccessTokenExpiresAt: &accessTokenExpiresAt,
								Scopes:               []string{"crm.objects.contacts.read"},
								Account: repods.ProviderAccount{
									ID: "acct-1", Name: "Acme CRM", Email: "ops@example.com",
								},
								LastRefreshedAt: &lastRefreshedAt,
								FailureCode:     "refresh_failed",
								FailureMessage:  "Reconnect HubSpot",
							},
						}, nil
					}
					return nil, repods.ErrDataSourceNotFound
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("prod-db"),
			wantDataSource: &DataSource{
				Name: "Production DB",
				Type: model.DataSourceTypePostgres,
				Credential: &credentialssvc.CredentialMetadata{
					Slug:      must.Slug("credential-prod-db"),
					Kind:      credentialssvc.CredentialKindOAuth2Grant,
					Status:    credentialssvc.CredentialStatusPending,
					UpdatedAt: credentialUpdatedAt,
					OAuth: credentialssvc.CredentialOAuthMetadata{
						Provider:             "hubspot",
						Scopes:               []string{"crm.objects.contacts.read"},
						Account:              credentialssvc.ProviderAccount{ID: "acct-1", Name: "Acme CRM", Email: "ops@example.com"},
						AccessTokenExpiresAt: accessTokenExpiresAt,
						LastRefreshedAt:      &lastRefreshedAt,
						FailureCode:          "refresh_failed",
						FailureMessage:       "Reconnect HubSpot",
					},
				},
			},
		},
		{
			name: "LoadsCachedSQLSchemaWhenPresent",
			setup: func(repo *testutil.FakeDataSourceRepo, schemaRepo *testutil.FakeSchemaMetadataRepo) {
				repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug: must.Slug("sql-db"),
						Name: "SQL Database",
						Type: model.DataSourceTypePostgres,
					}, nil
				}
				schemaRepo.FindByDataSourceSlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.SchemaMetadata, error) {
					return &repods.SchemaMetadata{
						Slug: must.Slug("schema-sql-db"),
						SchemaData: json.RawMessage(`{
							"schema_type": "sql",
							"type": "postgres",
							"payload": {
								"tables": [
									{"name": "users", "columns": [{"name": "id", "type": "integer"}]}
								]
							}
						}`),
					}, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("sql-db"),
			wantDataSource: &DataSource{
				Name: "SQL Database",
				Type: model.DataSourceTypePostgres,
			},
			wantSchema: Schema[SQLSchemaPayload]{
				SchemaTypeValue: SchemaTypeSQL,
				Type:            "postgres",
				Payload: SQLSchemaPayload{
					Tables: []SchemaTable{
						{
							Name: "users",
							Columns: []SchemaColumn{
								{Name: "id", Type: "integer"},
							},
						},
					},
				},
			},
			wantCached: true,
		},
		{
			name: "ReturnsEmptySQLSchemaWhenCacheMissing",
			setup: func(repo *testutil.FakeDataSourceRepo, schemaRepo *testutil.FakeSchemaMetadataRepo) {
				repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug: must.Slug("uncached-db"),
						Name: "Uncached Database",
						Type: model.DataSourceTypePostgres,
					}, nil
				}
				// No schema metadata - schemaRepo returns ErrSchemaMetadataNotFound by default
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("uncached-db"),
			wantDataSource: &DataSource{
				Name: "Uncached Database",
				Type: model.DataSourceTypePostgres,
			},
			wantSchema: Schema[SQLSchemaPayload]{
				SchemaTypeValue: SchemaTypeSQL,
				Type:            "postgres",
				Payload: SQLSchemaPayload{
					Tables: []SchemaTable{},
				},
			},
		},
		{
			name: "ReturnsEmptySQLSchemaWhenCacheMalformed",
			setup: func(repo *testutil.FakeDataSourceRepo, schemaRepo *testutil.FakeSchemaMetadataRepo) {
				repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug: must.Slug("bad-cache-db"),
						Name: "Bad Cache Database",
						Type: model.DataSourceTypePostgres,
					}, nil
				}
				schemaRepo.FindByDataSourceSlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.SchemaMetadata, error) {
					return &repods.SchemaMetadata{
						Slug:       must.Slug("schema-bad-cache-db"),
						SchemaData: json.RawMessage(`{"schema_type":"unknown","type":"postgres","payload":{}}`),
					}, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("bad-cache-db"),
			wantDataSource: &DataSource{
				Name: "Bad Cache Database",
				Type: model.DataSourceTypePostgres,
			},
			wantSchema: Schema[SQLSchemaPayload]{
				SchemaTypeValue: SchemaTypeSQL,
				Type:            "postgres",
				Payload: SQLSchemaPayload{
					Tables: []SchemaTable{},
				},
			},
		},
		{
			name:    "ReturnsErrNotFoundWhenDataSourceDoesNotExist",
			setup:   func(repo *testutil.FakeDataSourceRepo, schemaRepo *testutil.FakeSchemaMetadataRepo) { /* no setup */ },
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("nonexistent"),
			wantErr: repods.ErrDataSourceNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			schemaRepo := testutil.NewFakeSchemaMetadataRepo()
			if tc.setup != nil {
				tc.setup(repo, schemaRepo)
			}
			enc := testutil.NewFakeEncryptor()
			cf := testutil.NewFakeConnectorFactory()
			svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

			result, err := svc.FindBySlug(context.Background(), tc.orgSlug, tc.dsSlug)

			if tc.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tc.wantDataSource.Name, result.Name)
			assert.Equal(t, tc.wantDataSource.Type, result.Type)
			assert.Equal(t, tc.wantDataSource.Credential, result.Credential)
			assert.Equal(t, tc.wantCached, result.SchemaCached)

			if tc.wantSchema != nil {
				require.NotNil(t, result.Schema, "expected schema to be populated")
				assert.Equal(t, tc.wantSchema.SchemaType(), result.Schema.SchemaType())
				assert.Equal(t, tc.wantSchema.VendorType(), result.Schema.VendorType())

				// Type-assert to access payload
				if expected, ok := tc.wantSchema.(Schema[SQLSchemaPayload]); ok {
					actual, ok := result.Schema.(Schema[SQLSchemaPayload])
					require.True(t, ok, "expected result schema to be Schema[SQLSchemaPayload]")
					assert.Equal(t, expected.Payload, actual.Payload)
				}
			}
		})
	}
}

func TestListByOrg(t *testing.T) {
	t.Parallel()
	credentialUpdatedAt := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		setup     func(repo *testutil.FakeDataSourceRepo)
		orgSlug   slug.Slug
		wantCount int
		wantErr   error
	}{
		{
			name: "ReturnsAllDataSourcesInOrg",
			setup: func(repo *testutil.FakeDataSourceRepo) {
				repo.ListByOrgFn = func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, _ model.ListRequest) ([]*repods.DataSource, error) {
					return []*repods.DataSource{
						{
							Slug: must.Slug("db1"), Type: model.DataSourceTypePostgres,
							Credential: &repods.CredentialMetadata{
								Slug:      must.Slug("credential-db1"),
								Kind:      "static_secret",
								Status:    "active",
								UpdatedAt: credentialUpdatedAt,
							},
						},
						{Slug: must.Slug("db2"), Type: model.DataSourceTypeMySQL},
					}, nil
				}
			},
			orgSlug:   must.Slug("acme-corp"),
			wantCount: 2,
		},
		{
			name: "ReturnsEmptyListWhenNoDataSources",
			setup: func(repo *testutil.FakeDataSourceRepo) {
				repo.ListByOrgFn = func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, _ model.ListRequest) ([]*repods.DataSource, error) {
					return []*repods.DataSource{}, nil
				}
			},
			orgSlug:   must.Slug("empty-corp"),
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			if tc.setup != nil {
				tc.setup(repo)
			}
			enc := testutil.NewFakeEncryptor()
			cf := testutil.NewFakeConnectorFactory()
			schemaRepo := testutil.NewFakeSchemaMetadataRepo()
			svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

			results, err := svc.ListByOrg(context.Background(), tc.orgSlug, model.ListRequest{})

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, results.Items)
				return
			}

			require.NoError(t, err)
			assert.Len(t, results.Items, tc.wantCount)
			if tc.name == "ReturnsAllDataSourcesInOrg" {
				require.NotNil(t, results.Items[0].Credential)
				assert.Equal(t, credentialssvc.CredentialKindStaticSecret, results.Items[0].Credential.Kind)
				assert.Equal(t, credentialssvc.CredentialStatusActive, results.Items[0].Credential.Status)
				assert.Equal(t, credentialUpdatedAt, results.Items[0].Credential.UpdatedAt)
			}
		})
	}
}

func TestListByOrg_BackwardPagination(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	repo.ListByOrgFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_ slug.Slug,
		req model.ListRequest,
	) ([]*repods.DataSource, error) {
		assert.Equal(t, model.ListOrderAsc, req.Order)
		assert.Equal(t, 3, req.Limit)

		baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		t1 := baseTime.Add(-2 * time.Hour)
		t2 := baseTime.Add(-1 * time.Hour)
		t3 := baseTime

		return []*repods.DataSource{
			{
				Slug:      must.Slug("db-a"),
				Name:      "A",
				Type:      model.DataSourceTypePostgres,
				CreatedAt: t1,
				UpdatedAt: t1,
			},
			{
				Slug:      must.Slug("db-b"),
				Name:      "B",
				Type:      model.DataSourceTypePostgres,
				CreatedAt: t2,
				UpdatedAt: t2,
			},
			{
				Slug:      must.Slug("db-c"),
				Name:      "C",
				Type:      model.DataSourceTypePostgres,
				CreatedAt: t3,
				UpdatedAt: t3,
			},
		}, nil
	}

	enc := testutil.NewFakeEncryptor()
	cf := testutil.NewFakeConnectorFactory()
	schemaRepo := testutil.NewFakeSchemaMetadataRepo()
	svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

	result, err := svc.ListByOrg(context.Background(), must.Slug("acme-corp"), model.ListRequest{
		Limit:     2,
		Cursor:    "2026-01-01T00:00:00Z_ds-z",
		SortBy:    model.ListSortCreatedAt,
		Order:     model.ListOrderDesc,
		Direction: model.ListDirectionPrev,
	})

	require.NoError(t, err)
	require.Len(t, result.Items, 2)
	assert.Equal(t, must.Slug("db-b"), result.Items[0].Slug)
	assert.Equal(t, must.Slug("db-a"), result.Items[1].Slug)
	assert.True(t, result.HasMore)
	assert.NotNil(t, result.NextCursor)
	assert.NotNil(t, result.PrevCursor)
}

func TestIntrospectSchemaReturnsEmptyHTTPSchemaWithoutOpenAPIURL(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("http-api")
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
	) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.DataSource{
			Slug:   dsSlug,
			Name:   "HTTP API",
			Type:   model.DataSourceTypeHTTP,
			Config: []byte(`{"base_url":"https://api.example.com"}`),
		}, nil
	}

	schemaRepo := testutil.NewFakeSchemaMetadataRepo()
	schemaRepo.UpsertByDataSourceSlugFn = func(
		context.Context,
		postgres.DBTX,
		slug.Slug,
		slug.Slug,
		*repods.UpsertSchemaMetadataParams,
	) (*repods.SchemaMetadata, error) {
		t.Fatal("cache-only schema reads must not write schema metadata")
		return nil, fmt.Errorf("unexpected schema metadata write")
	}

	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(
		_ context.Context,
		_ model.DataSourceType,
		_ []byte,
		_ []byte,
	) (connector.Connector, error) {
		t.Fatal("cache-only schema reads must not construct connectors")
		return nil, fmt.Errorf("unexpected connector construction")
	}

	svc := newTestService(
		repo,
		schemaRepo,
		testutil.NewFakeEncryptor(),
		cf.NewConnectorFn,
	)

	result, err := svc.IntrospectSchema(context.Background(), orgSlug, dsSlug)
	require.NoError(t, err)

	actual, ok := result.(Schema[HTTPSchemaPayload])
	require.True(t, ok)
	assert.Equal(t, SchemaTypeHTTP, actual.SchemaTypeValue)
	assert.Equal(t, model.DataSourceTypeHTTP.String(), actual.Type)
	assert.Empty(t, actual.Payload.Operations)
}

func TestIntrospectSchemaReturnsCachedSQLSchemaWithoutConnector(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("prod-db")
	cachedSchema := Schema[SQLSchemaPayload]{
		SchemaTypeValue: SchemaTypeSQL,
		Type:            model.DataSourceTypePostgres.String(),
		Payload: SQLSchemaPayload{
			Tables: []SchemaTable{{
				Name: "users",
				Columns: []SchemaColumn{
					{Name: "id", Type: "integer"},
				},
			}},
		},
	}
	cachedSchemaJSON, err := json.Marshal(cachedSchema)
	require.NoError(t, err)

	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(
		context.Context,
		postgres.DBTX,
		slug.Slug,
		slug.Slug,
	) (*repods.DataSource, error) {
		t.Fatal("cached schema reads must not load the data source")
		return nil, fmt.Errorf("unexpected data source load")
	}

	schemaRepo := testutil.NewFakeSchemaMetadataRepo()
	schemaRepo.FindByDataSourceSlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
	) (*repods.SchemaMetadata, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.SchemaMetadata{SchemaData: cachedSchemaJSON}, nil
	}

	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (connector.Connector, error) {
		t.Fatal("cached schema reads must not construct connectors")
		return nil, fmt.Errorf("unexpected connector construction")
	}

	svc := newTestService(
		repo,
		schemaRepo,
		testutil.NewFakeEncryptor(),
		cf.NewConnectorFn,
	)

	result, err := svc.IntrospectSchema(context.Background(), orgSlug, dsSlug)
	require.NoError(t, err)

	actual, ok := result.(Schema[SQLSchemaPayload])
	require.True(t, ok)
	assert.Equal(t, cachedSchema, actual)
}

func TestIntrospectSchemaReturnsEmptySQLSchemaWhenCacheMalformed(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("prod-db")
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
	) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.DataSource{
			Slug:   dsSlug,
			Name:   "Production Database",
			Type:   model.DataSourceTypePostgres,
			Config: []byte(`{"host":"db","port":5432,"database":"prod"}`),
		}, nil
	}

	schemaRepo := testutil.NewFakeSchemaMetadataRepo()
	schemaRepo.FindByDataSourceSlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
	) (*repods.SchemaMetadata, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.SchemaMetadata{
			SchemaData: json.RawMessage(`{"schema_type":"unknown","type":"postgres","payload":{}}`),
		}, nil
	}

	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (connector.Connector, error) {
		t.Fatal("malformed cache fallback must not construct connectors")
		return nil, fmt.Errorf("unexpected connector construction")
	}

	svc := newTestService(
		repo,
		schemaRepo,
		testutil.NewFakeEncryptor(),
		cf.NewConnectorFn,
	)

	result, err := svc.IntrospectSchema(context.Background(), orgSlug, dsSlug)
	require.NoError(t, err)

	actual, ok := result.(Schema[SQLSchemaPayload])
	require.True(t, ok)
	assert.Equal(t, SchemaTypeSQL, actual.SchemaTypeValue)
	assert.Equal(t, model.DataSourceTypePostgres.String(), actual.Type)
	assert.Empty(t, actual.Payload.Tables)
}

func TestIntrospectSchemaReturnsEmptyFileSchemaForColdGitHubCacheWithoutConnector(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("repo-source")
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_, _ slug.Slug,
	) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:   dsSlug,
			Name:   "Repository Source",
			Type:   model.DataSourceTypeGithub,
			Config: []byte(`{"repo_url":"git@github.com:Braidplane/braidplane.git","default_branch":"main"}`),
		}, nil
	}

	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (connector.Connector, error) {
		t.Fatal("cold cache schema reads must not clone repositories or construct connectors")
		return nil, fmt.Errorf("unexpected connector construction")
	}

	svc := newTestService(
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		testutil.NewFakeEncryptor(),
		cf.NewConnectorFn,
	)

	result, err := svc.IntrospectSchema(context.Background(), orgSlug, dsSlug)
	require.NoError(t, err)

	actual, ok := result.(Schema[FileSchemaPayload])
	require.True(t, ok)
	assert.Equal(t, SchemaTypeFile, actual.SchemaTypeValue)
	assert.Equal(t, model.DataSourceTypeGithub.String(), actual.Type)
	assert.Empty(t, actual.Payload.Files)
}

func TestUpdateDataSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setup          func(repo *testutil.FakeDataSourceRepo)
		orgSlug        slug.Slug
		dsSlug         slug.Slug
		params         *UpdateParams
		wantDataSource *DataSource
		wantErr        error
	}{
		{
			name: "UpdatesDataSourceMetadata",
			setup: func(repo *testutil.FakeDataSourceRepo) {
				repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:     dsSlug,
						Name:     "Old Name",
						Type:     model.DataSourceTypePostgres,
						Config:   []byte(`{"host":"oldhost","port":5432,"database":"old"}`),
						IsActive: true,
					}, nil
				}
				repo.UpdateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.UpdateParams) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:                 dsSlug,
						Name:                 params.Name,
						Type:                 model.DataSourceTypePostgres,
						Config:               params.ConfigJSON,
						EncryptedCredentials: []byte(`{"username":"user","password":"pass"}`),
					}, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("prod-db"),
			params: &UpdateParams{
				Name:       "Updated Name",
				ConfigJSON: []byte(`{"host":"localhost","port":5432,"database":"testdb"}`),
			},
			wantDataSource: &DataSource{
				Name: "Updated Name",
			},
		},
		{
			name: "UpdatesMySQLDataSourceMetadata",
			setup: func(repo *testutil.FakeDataSourceRepo) {
				repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:     dsSlug,
						Name:     "Old MySQL",
						Type:     model.DataSourceTypeMySQL,
						Config:   []byte(`{"host":"oldhost","port":3306,"database":"old"}`),
						IsActive: true,
					}, nil
				}
				repo.UpdateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.UpdateParams) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:   dsSlug,
						Name:   params.Name,
						Type:   model.DataSourceTypeMySQL,
						Config: params.ConfigJSON,
						EncryptedCredentials: []byte(
							`{"username":"user","password":"pass"}`,
						),
					}, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("mysql-db"),
			params: &UpdateParams{
				Name:       "Updated MySQL",
				ConfigJSON: []byte(`{"host":"localhost","port":3306,"database":"mysqldb"}`),
			},
			wantDataSource: &DataSource{
				Name: "Updated MySQL",
			},
		},
		{
			name: "UpdatesHTTPDataSourceMetadata",
			setup: func(repo *testutil.FakeDataSourceRepo) {
				repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:     dsSlug,
						Name:     "Old HTTP",
						Type:     model.DataSourceTypeHTTP,
						Config:   []byte(`{"base_url":"https://old.example.com"}`),
						IsActive: true,
					}, nil
				}
				repo.UpdateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.UpdateParams) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:   dsSlug,
						Name:   params.Name,
						Type:   model.DataSourceTypeHTTP,
						Config: params.ConfigJSON,
						EncryptedCredentials: []byte(
							`{"bearer_token":"token-123"}`,
						),
					}, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("http-api"),
			params: &UpdateParams{
				Name: "Updated HTTP",
				ConfigJSON: []byte(
					`{"base_url":"https://api.example.com","health_path":"/health"}`,
				),
			},
			wantDataSource: &DataSource{
				Name: "Updated HTTP",
			},
		},
		{
			name: "ReturnsErrNotFoundWhenDataSourceDoesNotExist",
			setup: func(repo *testutil.FakeDataSourceRepo) {
				repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
					return nil, repods.ErrDataSourceNotFound
				}
				repo.UpdateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.UpdateParams) (*repods.DataSource, error) {
					return nil, repods.ErrDataSourceNotFound
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("nonexistent"),
			params: &UpdateParams{
				Name:       "Updated",
				ConfigJSON: []byte(`{"host":"localhost","port":5432,"database":"testdb"}`),
			},
			wantErr: repods.ErrDataSourceNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			if tc.setup != nil {
				tc.setup(repo)
			}
			enc := testutil.NewFakeEncryptor()
			cf := testutil.NewFakeConnectorFactory()
			schemaRepo := testutil.NewFakeSchemaMetadataRepo()
			svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

			result, err := svc.Update(
				context.Background(),
				tc.orgSlug,
				tc.dsSlug,
				model.UserEventActor(must.Slug("alice")),
				tc.params,
			)

			if tc.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tc.wantDataSource.Name, result.Name)
		})
	}
}

func TestUpdateDataSourceRejectsInternalProviderOverrideConfigKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		dsType        model.DataSourceType
		currentConfig string
		updateConfig  string
		credentials   []byte
	}{
		{
			name:          "BigQueryBaseURL",
			dsType:        model.DataSourceTypeBigQuery,
			currentConfig: `{"project_id":"analytics-prod"}`,
			updateConfig:  `{"project_id":"analytics-prod","base_url":"https://bigquery.example.com"}`,
			credentials:   []byte(`{"service_account_json":"{\"type\":\"service_account\"}"}`),
		},
		{
			name:          "BigQueryTokenURL",
			dsType:        model.DataSourceTypeBigQuery,
			currentConfig: `{"project_id":"analytics-prod"}`,
			updateConfig:  `{"project_id":"analytics-prod","token_url":"https://oauth.example.com/token"}`,
			credentials:   []byte(`{"service_account_json":"{\"type\":\"service_account\"}"}`),
		},
		{
			name:          "BigQueryAllowPrivateHosts",
			dsType:        model.DataSourceTypeBigQuery,
			currentConfig: `{"project_id":"analytics-prod"}`,
			updateConfig:  `{"project_id":"analytics-prod","allow_private_hosts":true}`,
			credentials:   []byte(`{"service_account_json":"{\"type\":\"service_account\"}"}`),
		},
		{
			name:          "HubSpotBaseURL",
			dsType:        model.DataSourceTypeHubSpot,
			currentConfig: `{}`,
			updateConfig:  `{"base_url":"https://api.hubapi.com"}`,
			credentials:   []byte(`{"private_app_token":"pat-na1-secret"}`),
		},
		{
			name:          "HubSpotAllowPrivateHosts",
			dsType:        model.DataSourceTypeHubSpot,
			currentConfig: `{}`,
			updateConfig:  `{"allow_private_hosts":true}`,
			credentials:   []byte(`{"private_app_token":"pat-na1-secret"}`),
		},
		{
			name:          "PostgresCredentials",
			dsType:        model.DataSourceTypePostgres,
			currentConfig: `{"host":"db.example.com","port":5432,"database":"app"}`,
			updateConfig:  `{"host":"db.example.com","port":5432,"database":"app","user":"admin","password":"secret"}`,
			credentials:   []byte(`{"username":"pg-user","password":"pg-pass"}`),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
				return &repods.DataSource{
					Slug:                 must.Slug("provider-ds"),
					Name:                 "Provider DS",
					Type:                 tc.dsType,
					Config:               []byte(tc.currentConfig),
					EncryptedCredentials: tc.credentials,
					IsActive:             true,
				}, nil
			}
			repo.UpdateFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.UpdateParams) (*repods.DataSource, error) {
				t.Fatalf("repo update should not run when config contains internal override keys")
				return nil, assert.AnError
			}

			cf := testutil.NewFakeConnectorFactory()
			cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
				t.Fatalf("connector factory should not run when config contains internal override keys")
				return nil, assert.AnError
			}
			svc := newTestService(
				repo,
				testutil.NewFakeSchemaMetadataRepo(),
				testutil.NewFakeEncryptor(),
				cf.NewConnectorFn,
			)

			result, err := svc.Update(
				context.Background(),
				must.Slug("acme-corp"),
				must.Slug("provider-ds"),
				model.UserEventActor(must.Slug("alice")),
				&UpdateParams{
					ConfigJSON:         []byte(tc.updateConfig),
					ConfigFromCustomer: true,
				},
			)

			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalidInput)
			require.ErrorContains(t, err, "unknown field")
			require.Nil(t, result)
		})
	}
}

func TestUpdateDataSourceInvalidatesSchemaWhenHTTPOpenAPIConfigChanges(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("customer-api")

	tests := []struct {
		name          string
		currentConfig string
		updatedConfig string
		wantDeleted   bool
	}{
		{
			name:          "OpenAPIURLChanged",
			currentConfig: `{"base_url":"https://api.example.com","openapi_url":"https://api.example.com/openapi-v1.yaml","openapi_fetch_timeout_seconds":10}`,
			updatedConfig: `{"base_url":"https://api.example.com","openapi_url":"https://api.example.com/openapi-v2.yaml","openapi_fetch_timeout_seconds":10}`,
			wantDeleted:   true,
		},
		{
			name:          "OpenAPITimeoutChanged",
			currentConfig: `{"base_url":"https://api.example.com","openapi_url":"https://api.example.com/openapi.yaml","openapi_fetch_timeout_seconds":10}`,
			updatedConfig: `{"base_url":"https://api.example.com","openapi_url":"https://api.example.com/openapi.yaml","openapi_fetch_timeout_seconds":20}`,
			wantDeleted:   true,
		},
		{
			name:          "OpenAPIURLCleared",
			currentConfig: `{"base_url":"https://api.example.com","openapi_url":"https://api.example.com/openapi.yaml","openapi_fetch_timeout_seconds":10}`,
			updatedConfig: `{"base_url":"https://api.example.com"}`,
			wantDeleted:   true,
		},
		{
			name:          "BaseURLChanged",
			currentConfig: `{"base_url":"https://api-v1.example.com","openapi_url":"https://api.example.com/openapi.yaml","openapi_fetch_timeout_seconds":10}`,
			updatedConfig: `{"base_url":"https://api-v2.example.com","openapi_url":"https://api.example.com/openapi.yaml","openapi_fetch_timeout_seconds":10}`,
			wantDeleted:   true,
		},
		{
			name:          "AllowPrivateHostsChanged",
			currentConfig: `{"base_url":"https://internal-api.local","openapi_url":"https://internal-api.local/openapi.yaml","openapi_fetch_timeout_seconds":10,"allow_private_hosts":true}`,
			updatedConfig: `{"base_url":"https://internal-api.local","openapi_url":"https://internal-api.local/openapi.yaml","openapi_fetch_timeout_seconds":10}`,
			wantDeleted:   true,
		},
		{
			name:          "OnlyHealthPathChanged",
			currentConfig: `{"base_url":"https://api.example.com","health_path":"/health","openapi_url":"https://api.example.com/openapi.yaml","openapi_fetch_timeout_seconds":10}`,
			updatedConfig: `{"base_url":"https://api.example.com","health_path":"/ready","openapi_url":"https://api.example.com/openapi.yaml","openapi_fetch_timeout_seconds":10}`,
			wantDeleted:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
				return &repods.DataSource{
					Slug:                 dsSlug,
					Name:                 "Customer API",
					Type:                 model.DataSourceTypeHTTP,
					Config:               []byte(tc.currentConfig),
					EncryptedCredentials: []byte(`{"bearer_token":"token-123"}`),
					IsActive:             true,
				}, nil
			}
			repo.UpdateFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug, params *repods.UpdateParams) (*repods.DataSource, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, dsSlug, gotDSSlug)
				return &repods.DataSource{
					Slug:                 dsSlug,
					Name:                 params.Name,
					Type:                 model.DataSourceTypeHTTP,
					Config:               params.ConfigJSON,
					EncryptedCredentials: []byte(`{"bearer_token":"token-123"}`),
					IsActive:             true,
				}, nil
			}

			deleteCalls := 0
			schemaRepo := testutil.NewFakeSchemaMetadataRepo()
			schemaRepo.DeleteByDataSourceSlugFn = func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug) error {
				deleteCalls++
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, dsSlug, gotDSSlug)
				return nil
			}

			cf := testutil.NewFakeConnectorFactory()
			svc := newTestService(repo, schemaRepo, testutil.NewFakeEncryptor(), cf.NewConnectorFn)

			result, err := svc.Update(
				context.Background(),
				orgSlug,
				dsSlug,
				model.UserEventActor(must.Slug("alice")),
				&UpdateParams{
					Name:       "Customer API",
					ConfigJSON: []byte(tc.updatedConfig),
				},
			)
			require.NoError(t, err)
			require.NotNil(t, result)

			if tc.wantDeleted {
				require.Equal(t, 1, deleteCalls)
				return
			}
			require.Zero(t, deleteCalls)
		})
	}
}

func TestUpdateDataSourceRejectsRepoURLChangeWhenReferencedByProjectBackend(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	enc := testutil.NewFakeEncryptor()
	cf := testutil.NewFakeConnectorFactory()
	schemaRepo := testutil.NewFakeSchemaMetadataRepo()

	repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug: dsSlug,
			Name: "Git Repo",
			Type: model.DataSourceTypeGit,
			Config: []byte(
				`{"repo_url":"https://github.com/acme/repo.git","default_branch":"main"}`,
			),
			EncryptedCredentials: []byte(`{"auth_type":"https","token":"secret"}`),
			IsActive:             true,
		}, nil
	}
	repo.DataSourceHasProjectBackendReferencesFn = func(
		_ context.Context,
		_ postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
	) (bool, error) {
		require.Equal(t, must.Slug("acme-corp"), orgSlug)
		require.Equal(t, must.Slug("git-repo"), dsSlug)
		return true, nil
	}
	repo.UpdateFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.UpdateParams) (*repods.DataSource, error) {
		t.Fatal("update repository must not be called when a project-owned git backend attempts to change repo_url")
		return nil, xerror.Unexpected().Detail("unexpected datasource update")
	}

	svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

	result, err := svc.Update(
		context.Background(),
		must.Slug("acme-corp"),
		must.Slug("git-repo"),
		model.UserEventActor(must.Slug("admin")),
		&UpdateParams{
			ConfigJSON: []byte(
				`{"repo_url":"https://github.com/acme/other-repo.git","default_branch":"main"}`,
			),
		},
	)

	require.ErrorIs(t, err, ErrDataSourceUsedAsProjectBackend)
	assert.Nil(t, result)
}

func TestUpdateDataSourceRejectsReadOnlyTransitionWhenReferencedByProjectBackend(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	enc := testutil.NewFakeEncryptor()
	cf := testutil.NewFakeConnectorFactory()
	schemaRepo := testutil.NewFakeSchemaMetadataRepo()

	repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug: dsSlug,
			Name: "Git Repo",
			Type: model.DataSourceTypeGit,
			Config: []byte(
				`{"repo_url":"https://github.com/acme/repo.git","default_branch":"main"}`,
			),
			EncryptedCredentials: []byte(`{"auth_type":"ssh","private_key":"secret"}`),
			IsActive:             true,
			ReadOnly:             false,
		}, nil
	}
	repo.DataSourceHasProjectBackendReferencesFn = func(
		_ context.Context,
		_ postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
	) (bool, error) {
		require.Equal(t, must.Slug("acme-corp"), orgSlug)
		require.Equal(t, must.Slug("git-repo"), dsSlug)
		return true, nil
	}
	repo.UpdateFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.UpdateParams) (*repods.DataSource, error) {
		t.Fatal("update repository must not be called when a project-owned git backend attempts to become read-only")
		return nil, xerror.Unexpected().Detail("unexpected datasource update")
	}

	svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

	result, err := svc.Update(
		context.Background(),
		must.Slug("acme-corp"),
		must.Slug("git-repo"),
		model.UserEventActor(must.Slug("admin")),
		&UpdateParams{
			ReadOnly:    true,
			ReadOnlySet: true,
		},
	)

	require.ErrorIs(t, err, ErrDataSourceUsedAsProjectBackend)
	assert.Nil(t, result)
}

func TestUpdateDataSourceCredentials(t *testing.T) {
	t.Parallel()

	validCreds := map[string]any{
		"username": "admin",
		"password": "secret",
	}

	configJSON := []byte(`{"host":"localhost","port":5432,"database":"testdb"}`)

	tests := []struct {
		name            string
		setup           func(repo *testutil.FakeDataSourceRepo, enc *testutil.FakeEncryptor, cf *testutil.FakeConnectorFactory)
		orgSlug         slug.Slug
		dsSlug          slug.Slug
		params          *UpdateCredentialsParams
		wantDataSource  *DataSource
		wantErr         error
		wantErrContains string
	}{
		{
			name: "UpdatesCredentialsAndTestsConnection",
			setup: func(repo *testutil.FakeDataSourceRepo, enc *testutil.FakeEncryptor, cf *testutil.FakeConnectorFactory) {
				repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:   dsSlug,
						Name:   "Production DB",
						Type:   model.DataSourceTypePostgres,
						Config: configJSON,
					}, nil
				}

				repo.UpdateCredentialsFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.UpdateCredentialsParams) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:   dsSlug,
						Name:   "Production DB",
						Type:   model.DataSourceTypePostgres,
						Config: configJSON,
					}, nil
				}

				cf.NewConnectorFn = func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
					mockConnector := testutil.NewFakeSQLConnector()
					mockConnector.TestConnectionFn = func(ctx context.Context) error {
						return nil
					}
					return mockConnector, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("prod-db"),
			params: &UpdateCredentialsParams{
				Credentials: validCreds,
			},
			wantDataSource: &DataSource{
				Name: "Production DB",
				Type: model.DataSourceTypePostgres,
			},
		},
		{
			name:    "ReturnsErrWhenCredentialsMissing",
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("prod-db"),
			params:  &UpdateCredentialsParams{},
			wantErr: ErrInvalidInput,
		},
		{
			name:    "ReturnsErrWhenCredentialsMarshalFails",
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("prod-db"),
			params: &UpdateCredentialsParams{
				Credentials: map[string]any{
					"username": func() {},
				},
			},
			wantErr: ErrInvalidInput,
		},
		{
			name: "ReturnsErrWhenDataSourceDoesNotExist",
			setup: func(repo *testutil.FakeDataSourceRepo, enc *testutil.FakeEncryptor, cf *testutil.FakeConnectorFactory) {
				repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
					return nil, repods.ErrDataSourceNotFound
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("missing"),
			params: &UpdateCredentialsParams{
				Credentials: validCreds,
			},
			wantErr: repods.ErrDataSourceNotFound,
		},
		{
			name: "RejectsManagedProviderCredentialReplacement",
			setup: func(repo *testutil.FakeDataSourceRepo, _ *testutil.FakeEncryptor, _ *testutil.FakeConnectorFactory) {
				repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug: must.Slug("pipelines"), Type: model.DataSourceTypeGitlab,
						Config: []byte(`{"repository":{"id":"42","name":"repo","full_name":"acme/repo","web_url":"https://gitlab.com/acme/repo"}}`),
					}, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("pipelines"),
			params: &UpdateCredentialsParams{
				Credentials: map[string]any{"auth_type": "https", "token": "replacement"},
			},
			wantErr:         ErrInvalidInput,
			wantErrContains: "managed provider credentials must be changed through provider authorization",
		},
		{
			name: "ReturnsErrWhenConnectionTestFails",
			setup: func(repo *testutil.FakeDataSourceRepo, enc *testutil.FakeEncryptor, cf *testutil.FakeConnectorFactory) {
				repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:   dsSlug,
						Name:   "Production DB",
						Type:   model.DataSourceTypePostgres,
						Config: configJSON,
					}, nil
				}

				repo.UpdateCredentialsFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.UpdateCredentialsParams) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:   dsSlug,
						Name:   "Production DB",
						Type:   model.DataSourceTypePostgres,
						Config: configJSON,
					}, nil
				}

				cf.NewConnectorFn = func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
					mockConnector := testutil.NewFakeSQLConnector()
					mockConnector.TestConnectionFn = func(ctx context.Context) error {
						return fmt.Errorf("connection failed")
					}
					return mockConnector, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("prod-db"),
			params: &UpdateCredentialsParams{
				Credentials: validCreds,
			},
			wantErr:         ErrInvalidInput,
			wantErrContains: "failed to establish connection with updated credentials",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			enc := testutil.NewFakeEncryptor()
			cf := testutil.NewFakeConnectorFactory()
			if tc.setup != nil {
				tc.setup(repo, enc, cf)
			}
			schemaRepo := testutil.NewFakeSchemaMetadataRepo()
			svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

			result, err := svc.UpdateCredentials(
				context.Background(),
				tc.orgSlug,
				tc.dsSlug,
				model.UserEventActor(must.Slug("alice")),
				tc.params,
			)

			if tc.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.wantErr)
				if tc.wantErrContains != "" {
					require.ErrorContains(t, err, tc.wantErrContains)
				}
				assert.Nil(t, result)
				return
			}
			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.wantErrContains)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tc.wantDataSource.Name, result.Name)
			assert.Equal(t, tc.wantDataSource.Type, result.Type)
		})
	}
}

func TestCreateDataSource_ForcesReadOnlyForCredentiallessHTTPSGit(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	enc := testutil.NewFakeEncryptor()
	cf := testutil.NewFakeConnectorFactory()
	schemaRepo := testutil.NewFakeSchemaMetadataRepo()

	createCalledWithReadOnly := false
	repo.CreateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
		createCalledWithReadOnly = params.ReadOnly
		encCreds, _ := enc.Encrypt([]byte(`{"auth_type":"https"}`))
		return &repods.DataSource{
			Slug:                 params.Slug,
			Name:                 params.Name,
			Type:                 params.Type,
			Config:               params.ConfigJSON,
			EncryptedCredentials: encCreds,
			ReadOnly:             params.ReadOnly,
		}, nil
	}
	repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _, dsSlug slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{Slug: dsSlug, Name: "Git Repo", Type: model.DataSourceTypeGit, ReadOnly: true, IsActive: true}, nil
	}

	svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

	result, err := svc.Create(
		context.Background(),
		must.Slug("acme-corp"),
		model.UserEventActor(must.Slug("alice")),
		&CreateParams{
			Name: "Git Repo",
			Type: model.DataSourceTypeGit,
			ConfigJSON: []byte(
				`{"repo_url":"https://github.com/acme/repo.git","default_branch":"main"}`,
			),
			Credentials: map[string]any{
				"auth_type": "https",
			},
			ReadOnly: false,
		})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, createCalledWithReadOnly)
	assert.True(t, result.ReadOnly)
}

func TestCreateDataSource_AllowsWritableHubSpot(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	enc := testutil.NewFakeEncryptor()
	cf := testutil.NewFakeConnectorFactory()
	schemaRepo := testutil.NewFakeSchemaMetadataRepo()

	createCalledWithReadOnly := false
	repo.CreateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
		createCalledWithReadOnly = params.ReadOnly
		encCreds, _ := enc.Encrypt([]byte(`{"private_app_token":"pat-na1-force-read-only"}`))
		return &repods.DataSource{
			Slug:                 params.Slug,
			Name:                 params.Name,
			Type:                 params.Type,
			Config:               params.ConfigJSON,
			EncryptedCredentials: encCreds,
			ReadOnly:             params.ReadOnly,
		}, nil
	}
	repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _, dsSlug slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{Slug: dsSlug, Name: "HubSpot CRM", Type: model.DataSourceTypeHubSpot, IsActive: true}, nil
	}

	svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

	result, err := svc.Create(
		context.Background(),
		must.Slug("acme-corp"),
		model.UserEventActor(must.Slug("alice")),
		&CreateParams{
			Name:        "HubSpot CRM",
			Type:        model.DataSourceTypeHubSpot,
			ConfigJSON:  []byte(`{"timeout_seconds":10}`),
			Credentials: map[string]any{"private_app_token": "pat-na1-write-enabled"},
			ReadOnly:    false,
		})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, createCalledWithReadOnly)
	assert.False(t, result.ReadOnly)
}

func TestUpdateDataSource_ForcesReadOnlyForCredentiallessHTTPSGit(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	enc := testutil.NewFakeEncryptor()
	cf := testutil.NewFakeConnectorFactory()
	schemaRepo := testutil.NewFakeSchemaMetadataRepo()

	setReadOnlyCalled := false
	setReadOnlyValue := false
	repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug: dsSlug,
			Name: "Git Repo",
			Type: model.DataSourceTypeGit,
			Config: []byte(
				`{"repo_url":"https://github.com/acme/repo.git","default_branch":"main"}`,
			),
			IsActive: true,
			ReadOnly: false,
		}, nil
	}
	repo.UpdateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.UpdateParams) (*repods.DataSource, error) {
		encCreds, _ := enc.Encrypt([]byte(`{"auth_type":"https"}`))
		return &repods.DataSource{
			Slug: dsSlug,
			Name: params.Name,
			Type: model.DataSourceTypeGit,
			Config: []byte(
				`{"repo_url":"https://github.com/acme/repo.git","default_branch":"main"}`,
			),
			EncryptedCredentials: encCreds,
			ReadOnly:             false,
		}, nil
	}
	repo.SetReadOnlyFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, readOnly bool) error {
		setReadOnlyCalled = true
		setReadOnlyValue = readOnly
		return nil
	}

	svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

	result, err := svc.Update(
		context.Background(),
		must.Slug("acme-corp"),
		must.Slug("git-repo"),
		model.UserEventActor(must.Slug("alice")),
		&UpdateParams{
			Name: "Git Repo Updated",
			ConfigJSON: []byte(
				`{"repo_url":"https://github.com/acme/repo.git","default_branch":"main"}`,
			),
			ReadOnly: false,
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, setReadOnlyCalled)
	assert.True(t, setReadOnlyValue)
	assert.True(t, result.ReadOnly)
}

func TestUpdateDataSourceCredentials_ForcesReadOnlyForCredentiallessHTTPSGit(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	enc := testutil.NewFakeEncryptor()
	cf := testutil.NewFakeConnectorFactory()
	schemaRepo := testutil.NewFakeSchemaMetadataRepo()

	setReadOnlyCalled := false
	setReadOnlyValue := false
	repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug: dsSlug,
			Name: "Git Repo",
			Type: model.DataSourceTypeGit,
			Config: []byte(
				`{"repo_url":"https://github.com/acme/repo.git","default_branch":"main"}`,
			),
		}, nil
	}
	repo.UpdateCredentialsFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.UpdateCredentialsParams) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug: dsSlug,
			Name: "Git Repo",
			Type: model.DataSourceTypeGit,
			Config: []byte(
				`{"repo_url":"https://github.com/acme/repo.git","default_branch":"main"}`,
			),
			ReadOnly: false,
		}, nil
	}
	repo.SetReadOnlyFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, readOnly bool) error {
		setReadOnlyCalled = true
		setReadOnlyValue = readOnly
		return nil
	}

	svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

	result, err := svc.UpdateCredentials(
		context.Background(),
		must.Slug("acme-corp"),
		must.Slug("git-repo"),
		model.UserEventActor(must.Slug("alice")),
		&UpdateCredentialsParams{
			Credentials: map[string]any{
				"auth_type": "https",
			},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, setReadOnlyCalled)
	assert.True(t, setReadOnlyValue)
	assert.True(t, result.ReadOnly)
}

func TestUpdateDataSourceCredentialsRejectsForcedReadOnlyWhenReferencedByProjectBackend(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	enc := testutil.NewFakeEncryptor()
	cf := testutil.NewFakeConnectorFactory()
	schemaRepo := testutil.NewFakeSchemaMetadataRepo()

	repo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug: dsSlug,
			Name: "Git Repo",
			Type: model.DataSourceTypeGit,
			Config: []byte(
				`{"repo_url":"https://github.com/acme/repo.git","default_branch":"main"}`,
			),
			EncryptedCredentials: []byte(`{"auth_type":"ssh","private_key":"secret"}`),
			IsActive:             true,
			ReadOnly:             false,
		}, nil
	}
	repo.DataSourceHasProjectBackendReferencesFn = func(
		_ context.Context,
		_ postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
	) (bool, error) {
		require.Equal(t, must.Slug("acme-corp"), orgSlug)
		require.Equal(t, must.Slug("git-repo"), dsSlug)
		return true, nil
	}
	repo.UpdateCredentialsFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.UpdateCredentialsParams) (*repods.DataSource, error) {
		t.Fatal("credentials update must not persist when it would force a referenced project backend read-only")
		return nil, xerror.Unexpected().Detail("unexpected datasource credentials update")
	}
	repo.SetReadOnlyFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, bool) error {
		t.Fatal("referenced project backend must not be marked read-only")
		return xerror.Unexpected().Detail("unexpected read-only update")
	}

	svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

	result, err := svc.UpdateCredentials(
		context.Background(),
		must.Slug("acme-corp"),
		must.Slug("git-repo"),
		model.UserEventActor(must.Slug("admin")),
		&UpdateCredentialsParams{
			Credentials: map[string]any{
				"auth_type": "https",
			},
		},
	)

	require.ErrorIs(t, err, ErrDataSourceUsedAsProjectBackend)
	assert.Nil(t, result)
}

func TestDeleteDataSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(repo *testutil.FakeDataSourceRepo)
		orgSlug slug.Slug
		dsSlug  slug.Slug
		actor   slug.Slug
		wantErr error
	}{
		{
			name: "DeletesDataSourceSuccessfully",
			setup: func(repo *testutil.FakeDataSourceRepo) {
				repo.FindBySlugFn = func(
					context.Context,
					postgres.DBTX,
					slug.Slug,
					slug.Slug,
				) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug: must.Slug("prod-db"),
						Type: model.DataSourceTypePostgres,
					}, nil
				}
				repo.DeleteFn = func(
					ctx context.Context,
					tx postgres.DBTX,
					orgSlug, dsSlug slug.Slug,
				) error {
					assert.Equal(t, must.Slug("acme-corp"), orgSlug)
					assert.Equal(t, must.Slug("prod-db"), dsSlug)
					return nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("prod-db"),
			actor:   must.Slug("alice"),
			wantErr: nil,
		},
		{
			name: "ReturnsErrNotFoundWhenDataSourceDoesNotExist",
			setup: func(repo *testutil.FakeDataSourceRepo) {
				repo.FindBySlugFn = func(
					context.Context,
					postgres.DBTX,
					slug.Slug,
					slug.Slug,
				) (*repods.DataSource, error) {
					return nil, repods.ErrDataSourceNotFound
				}
				repo.DeleteFn = func(
					ctx context.Context,
					tx postgres.DBTX,
					orgSlug, dsSlug slug.Slug,
				) error {
					return repods.ErrDataSourceNotFound
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("nonexistent"),
			actor:   must.Slug("alice"),
			wantErr: repods.ErrDataSourceNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			if tc.setup != nil {
				tc.setup(repo)
			}
			enc := testutil.NewFakeEncryptor()
			cf := testutil.NewFakeConnectorFactory()
			schemaRepo := testutil.NewFakeSchemaMetadataRepo()
			svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

			err := svc.Delete(
				context.Background(),
				tc.orgSlug,
				tc.dsSlug,
				model.UserEventActor(tc.actor),
			)

			if tc.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestRestoreDataSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(repo *testutil.FakeDataSourceRepo)
		orgSlug slug.Slug
		dsSlug  slug.Slug
		actor   slug.Slug
		wantErr error
	}{
		{
			name: "RestoresDataSourceSuccessfully",
			setup: func(repo *testutil.FakeDataSourceRepo) {
				repo.RestoreFn = func(
					ctx context.Context,
					tx postgres.DBTX,
					orgSlug, dsSlug slug.Slug,
				) error {
					assert.Equal(t, must.Slug("acme-corp"), orgSlug)
					assert.Equal(t, must.Slug("prod-db"), dsSlug)
					return nil
				}
				repo.FindBySlugFn = func(
					context.Context,
					postgres.DBTX,
					slug.Slug,
					slug.Slug,
				) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug: must.Slug("prod-db"),
						Type: model.DataSourceTypePostgres,
					}, nil
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("prod-db"),
			actor:   must.Slug("alice"),
		},
		{
			name: "ReturnsErrNotFoundWhenDataSourceDoesNotExist",
			setup: func(repo *testutil.FakeDataSourceRepo) {
				repo.RestoreFn = func(
					ctx context.Context,
					tx postgres.DBTX,
					orgSlug, dsSlug slug.Slug,
				) error {
					return repods.ErrDataSourceNotFound
				}
			},
			orgSlug: must.Slug("acme-corp"),
			dsSlug:  must.Slug("missing-db"),
			actor:   must.Slug("alice"),
			wantErr: repods.ErrDataSourceNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			if tc.setup != nil {
				tc.setup(repo)
			}
			enc := testutil.NewFakeEncryptor()
			cf := testutil.NewFakeConnectorFactory()
			schemaRepo := testutil.NewFakeSchemaMetadataRepo()
			svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn)

			err := svc.Restore(
				context.Background(),
				tc.orgSlug,
				tc.dsSlug,
				model.UserEventActor(tc.actor),
			)

			if tc.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestResolveGitAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupRepo  func(repo *testutil.FakeDataSourceRepo)
		setupEnc   func(enc *testutil.FakeEncryptor)
		assertErr  func(t *testing.T, err error)
		assertAuth func(t *testing.T, auth *GitDataSourceAuth)
		orgSlug    slug.Slug
		dsSlug     slug.Slug
	}{
		{
			name: "ResolvesHTTPSAuthWithPinnedCredentialFreeURL",
			setupRepo: func(repo *testutil.FakeDataSourceRepo) {
				cfg := gitconnector.Config{
					RepoURL:       "https://github.com/acme/repo.git",
					DefaultBranch: "main",
				}
				cfgJSON, err := json.Marshal(cfg)
				if err != nil {
					panic(err)
				}

				repo.FindBySlugFn = func(
					ctx context.Context,
					tx postgres.DBTX,
					orgSlug, dsSlug slug.Slug,
				) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:                 dsSlug,
						Type:                 model.DataSourceTypeGit,
						Config:               cfgJSON,
						EncryptedCredentials: []byte("ciphertext"),
					}, nil
				}
			},
			setupEnc: func(enc *testutil.FakeEncryptor) {
				enc.DecryptFn = func(ciphertext []byte) ([]byte, error) {
					if string(ciphertext) != "ciphertext" {
						return nil, fmt.Errorf("unexpected ciphertext payload")
					}
					creds := gitconnector.Credentials{
						AuthType: gitconnector.AuthTypeHTTPS,
						Username: "alice",
						Token:    "tok123",
					}
					payload, err := json.Marshal(creds)
					if err != nil {
						panic(err)
					}
					return payload, nil
				}
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
			assertAuth: func(t *testing.T, auth *GitDataSourceAuth) {
				t.Helper()
				require.NotNil(t, auth)
				require.Equal(t, "main", auth.DefaultBranch)
				require.Equal(t, "https://github.com/acme/repo.git", auth.RepoURL)
				requireGitCredentialHelper(t, auth.Env)
				require.Contains(t, auth.Env, "GIT_TERMINAL_PROMPT=0")
				require.Contains(t, auth.Env, "GIT_CONFIG_KEY_0=http.curloptResolve")
				require.Contains(t, auth.Env, "GIT_CONFIG_KEY_1=http.followRedirects")
				require.NotNil(t, auth.Cleanup)
				auth.Cleanup()
			},
			orgSlug: must.Slug("acme-org"),
			dsSlug:  must.Slug("git-backend"),
		},
		{
			name: "ResolvesGitHubPATAuthViaProviderEnv",
			setupRepo: func(repo *testutil.FakeDataSourceRepo) {
				cfg := gitconnector.Config{
					RepoURL:       "https://github.com/acme/repo.git",
					DefaultBranch: "main",
				}
				cfgJSON, err := json.Marshal(cfg)
				if err != nil {
					panic(err)
				}

				repo.FindBySlugFn = func(
					ctx context.Context,
					tx postgres.DBTX,
					orgSlug, dsSlug slug.Slug,
				) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:                 dsSlug,
						Type:                 model.DataSourceTypeGithub,
						Config:               cfgJSON,
						EncryptedCredentials: []byte("ciphertext"),
					}, nil
				}
			},
			setupEnc: func(enc *testutil.FakeEncryptor) {
				enc.DecryptFn = func(ciphertext []byte) ([]byte, error) {
					if string(ciphertext) != "ciphertext" {
						return nil, fmt.Errorf("unexpected ciphertext payload")
					}
					creds := gitconnector.Credentials{
						AuthType: gitconnector.AuthTypeHTTPS,
						Token:    "ghp_123",
					}
					payload, err := json.Marshal(creds)
					if err != nil {
						panic(err)
					}
					return payload, nil
				}
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
			assertAuth: func(t *testing.T, auth *GitDataSourceAuth) {
				t.Helper()
				require.NotNil(t, auth)
				require.Equal(t, "main", auth.DefaultBranch)
				require.Equal(t, "https://github.com/acme/repo.git", auth.RepoURL)
				require.Contains(t, auth.Env, "GITHUB_TOKEN=ghp_123")
				requireGitCredentialHelper(t, auth.Env)
				require.Contains(t, auth.Env, "BRAIDPLANE_GIT_USERNAME=x-access-token")
				require.Contains(t, auth.Env, "BRAIDPLANE_GIT_PASSWORD=ghp_123")
				require.Contains(t, auth.Env, "GIT_TERMINAL_PROMPT=0")
				require.NotNil(t, auth.Cleanup)
				auth.Cleanup()
			},
			orgSlug: must.Slug("acme-org"),
			dsSlug:  must.Slug("github-backend"),
		},
		{
			name: "ResolvesGitLabPATAuthViaProviderEnv",
			setupRepo: func(repo *testutil.FakeDataSourceRepo) {
				cfg := gitconnector.Config{
					RepoURL: "https://gitlab.com/acme/repo.git",
				}
				cfgJSON, err := json.Marshal(cfg)
				if err != nil {
					panic(err)
				}

				repo.FindBySlugFn = func(
					ctx context.Context,
					tx postgres.DBTX,
					orgSlug, dsSlug slug.Slug,
				) (*repods.DataSource, error) {
					return &repods.DataSource{
						Slug:                 dsSlug,
						Type:                 model.DataSourceTypeGitlab,
						Config:               cfgJSON,
						EncryptedCredentials: []byte("ciphertext"),
					}, nil
				}
			},
			setupEnc: func(enc *testutil.FakeEncryptor) {
				enc.DecryptFn = func(ciphertext []byte) ([]byte, error) {
					if string(ciphertext) != "ciphertext" {
						return nil, fmt.Errorf("unexpected ciphertext payload")
					}
					creds := gitconnector.Credentials{
						AuthType: gitconnector.AuthTypeHTTPS,
						Token:    "glpat-123",
					}
					payload, err := json.Marshal(creds)
					if err != nil {
						panic(err)
					}
					return payload, nil
				}
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
			assertAuth: func(t *testing.T, auth *GitDataSourceAuth) {
				t.Helper()
				require.NotNil(t, auth)
				require.Equal(t, "https://gitlab.com/acme/repo.git", auth.RepoURL)
				require.Contains(t, auth.Env, "GITLAB_TOKEN=glpat-123")
				requireGitCredentialHelper(t, auth.Env)
				require.Contains(t, auth.Env, "BRAIDPLANE_GIT_USERNAME=oauth2")
				require.Contains(t, auth.Env, "BRAIDPLANE_GIT_PASSWORD=glpat-123")
				require.Contains(t, auth.Env, "GIT_TERMINAL_PROMPT=0")
				require.NotNil(t, auth.Cleanup)
				auth.Cleanup()
			},
			orgSlug: must.Slug("acme-org"),
			dsSlug:  must.Slug("gitlab-backend"),
		},
		{
			name: "ReturnsErrInvalidTypeForNonGitDataSource",
			setupRepo: func(repo *testutil.FakeDataSourceRepo) {
				repo.FindBySlugFn = func(
					ctx context.Context,
					tx postgres.DBTX,
					orgSlug, dsSlug slug.Slug,
				) (*repods.DataSource, error) {
					return &repods.DataSource{Slug: dsSlug, Type: model.DataSourceTypePostgres}, nil
				}
			},
			setupEnc: func(enc *testutil.FakeEncryptor) {
				enc.DecryptFn = func(ciphertext []byte) ([]byte, error) {
					return nil, fmt.Errorf(
						"decrypt should not be called for non-git data source type",
					)
				}
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				require.ErrorContains(t, err, "data source must be of type git, github, or gitlab")
			},
			assertAuth: func(t *testing.T, auth *GitDataSourceAuth) {
				t.Helper()
				require.Nil(t, auth)
			},
			orgSlug: must.Slug("acme-org"),
			dsSlug:  must.Slug("postgres-backend"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			if tc.setupRepo != nil {
				tc.setupRepo(repo)
			}

			enc := testutil.NewFakeEncryptor()
			if tc.setupEnc != nil {
				tc.setupEnc(enc)
			}

			cf := testutil.NewFakeConnectorFactory()
			schemaRepo := testutil.NewFakeSchemaMetadataRepo()
			svc := newTestService(repo, schemaRepo, enc, cf.NewConnectorFn).(*service)
			svc.prepareGitRemote = func(
				_ context.Context,
				repoURL string,
				env []string,
				_ hostguard.DestinationPolicy,
			) (string, []string, error) {
				return repoURL, append(env,
					"GIT_CONFIG_KEY_0=http.curloptResolve",
					"GIT_CONFIG_KEY_1=http.followRedirects",
				), nil
			}

			auth, err := svc.ResolveGitAuth(context.Background(), tc.orgSlug, tc.dsSlug)
			tc.assertErr(t, err)
			tc.assertAuth(t, auth)
		})
	}
}

func TestResolveGitAuthResolvesCredentialsAndPreparesRemoteAfterRead(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-org")
	dsSlug := must.Slug("git-backend")
	txm := &resolveGitAuthTransactionManager{}
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:   dsSlug,
			Type:   model.DataSourceTypeGit,
			Config: []byte(`{"repo_url":"https://github.com/acme/repo.git"}`),
		}, nil
	}
	credentials := newFakeCredentialService()
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		require.False(t, txm.inTx, "credential resolution must not hold a database transaction")
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindStaticSecret,
			PlaintextJSON: []byte(`{"auth_type":"https","username":"alice","token":"tok123"}`),
		}, nil
	}

	serviceInterface, err := New(
		txm,
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		nil,
		credentials,
		NewNoopGitHubAppManager(),
		NewNoopGitLabWebhookManager(),
		testAdmissionService(),
		nil,
		eventtest.NewProducer(&fakeDataSourceEventRecorder{}),
		&fakeDataSourceSchemaImportProducer{},
		ServiceConfig{},
	)
	require.NoError(t, err)
	svc := serviceInterface.(*service)
	svc.prepareGitRemote = func(
		_ context.Context,
		repoURL string,
		env []string,
		_ hostguard.DestinationPolicy,
	) (string, []string, error) {
		require.False(t, txm.inTx, "destination preparation must not hold a database transaction")
		return repoURL, env, nil
	}

	auth, err := svc.ResolveGitAuth(context.Background(), orgSlug, dsSlug)
	require.NoError(t, err)
	require.NotNil(t, auth)
	auth.Cleanup()
}

func TestResolveGitAuthUsesGitLabOAuthAccessToken(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-org")
	dsSlug := must.Slug("gitlab-backend")
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{Slug: dsSlug, Type: model.DataSourceTypeGitlab, Config: []byte(`{"repository":{"id":"42","name":"repo","full_name":"acme/repo","web_url":"https://gitlab.com/acme/repo"}}`)}, nil
	}
	credentials := newFakeCredentialService()
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{Kind: credentialssvc.CredentialKindOAuth2Grant, PlaintextJSON: []byte(`{"access_token":"oauth-access"}`)}, nil
	}
	credentials.resolveRepositoryFn = func(_ context.Context, params credentialssvc.ResolveProviderRepositoryParams) (credentialssvc.ProviderRepository, error) {
		require.Equal(t, "42", params.RepositoryID)
		return credentialssvc.ProviderRepository{ID: "42", CloneURL: "https://gitlab.com/acme/renamed.git", DefaultBranch: "trunk"}, nil
	}
	svcInterface, err := New(testutil.FakeTransactionManager{}, repo, testutil.NewFakeSchemaMetadataRepo(), nil, credentials, NewNoopGitHubAppManager(), NewNoopGitLabWebhookManager(), testAdmissionService(), nil, eventtest.NewProducer(&fakeDataSourceEventRecorder{}), &fakeDataSourceSchemaImportProducer{}, ServiceConfig{})
	require.NoError(t, err)
	svc := svcInterface.(*service)
	svc.prepareGitRemote = func(_ context.Context, repoURL string, env []string, _ hostguard.DestinationPolicy) (string, []string, error) {
		return repoURL, env, nil
	}

	auth, err := svc.ResolveGitAuth(context.Background(), orgSlug, dsSlug)
	require.NoError(t, err)
	t.Cleanup(auth.Cleanup)
	require.Equal(t, "https://gitlab.com/acme/renamed.git", auth.RepoURL)
	require.Equal(t, "trunk", auth.DefaultBranch)
	assert.Contains(t, auth.Env, "GITLAB_TOKEN=oauth-access")
	requireGitCredentialHelper(t, auth.Env)
	require.Contains(t, auth.Env, "BRAIDPLANE_GIT_USERNAME=oauth2")
	require.Contains(t, auth.Env, "BRAIDPLANE_GIT_PASSWORD=oauth-access")
}

func TestResolveGitExecutionAuthUsesGitHubAppToken(t *testing.T) {
	t.Parallel()

	orgSlug, dsSlug := must.Slug("acme"), must.Slug("pipelines")
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug: dsSlug, Type: model.DataSourceTypeGithub,
			Config:     []byte(`{"repository":{"id":"42","name":"pipelines","full_name":"acme/pipelines","web_url":"https://github.com/acme/pipelines"}}`),
			Credential: &repods.CredentialMetadata{Slug: must.Slug("credential-pipelines"), Kind: credentialssvc.CredentialKindStaticSecret.String(), Status: credentialssvc.CredentialStatusActive.String()},
		}, nil
	}
	credentials := newFakeCredentialService()
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{Version: "secret/2026-10-18T10:00:00Z", Kind: credentialssvc.CredentialKindStaticSecret, PlaintextJSON: []byte(`{"auth_type":"https","username":"x-access-token"}`)}, nil
	}
	githubApps := &fakeGitHubAppManager{token: "short-lived", contentsPermission: "write", connection: &githubappsvc.Connection{
		Organization: orgSlug,
		Repositories: []githubapps.RepositoryBinding{{ID: 42, FullName: "acme/pipelines", OwnerLogin: "acme", Name: "pipelines"}},
	}}
	svc, err := New(testutil.FakeTransactionManager{}, repo, testutil.NewFakeSchemaMetadataRepo(), nil,
		credentials, githubApps, NewNoopGitLabWebhookManager(), testAdmissionService(), nil,
		eventtest.NewProducer(&fakeDataSourceEventRecorder{}), &fakeDataSourceSchemaImportProducer{}, ServiceConfig{},
	)
	require.NoError(t, err)

	auth, err := svc.ResolveGitExecutionAuth(t.Context(), orgSlug, dsSlug)
	require.NoError(t, err)
	require.JSONEq(t, `{"repo_url":"https://github.com/acme/pipelines.git"}`, string(auth.ConfigJSON))
	require.JSONEq(t, `{"auth_type":"https","username":"x-access-token","token":"short-lived"}`, string(auth.CredentialsJSON))
	require.Equal(t, "secret/2026-10-18T10:00:00Z", auth.CredentialReference)
	require.NotNil(t, auth.GitHubContentsPermission)
	require.Equal(t, "write", *auth.GitHubContentsPermission)
}

func TestPrepareGitExecutionAuthDoesNotReadGitHubContentsPermissionFromTenantPayload(t *testing.T) {
	t.Parallel()

	// A literal public IP keeps this permission test independent of external DNS.
	auth, err := PrepareGitExecutionAuth(t.Context(), GitExecutionAuth{
		DataSourceType:  model.DataSourceTypeGithub,
		ConfigJSON:      []byte(`{"repo_url":"https://93.184.216.34/acme/pipelines.git","github_contents_permission":"write"}`),
		CredentialsJSON: []byte(`{"auth_type":"https","token":"tenant-token","github_contents_permission":"write"}`),
	})
	require.NoError(t, err)
	t.Cleanup(auth.Cleanup)
	require.Nil(t, auth.GitHubContentsPermission)
}

func TestGitExecutionAuthPreservesInstallationContentsPermissionThroughJSON(t *testing.T) {
	t.Parallel()

	permission := ""
	encoded, err := json.Marshal(GitExecutionAuth{GitHubContentsPermission: &permission})
	require.NoError(t, err)
	var decoded GitExecutionAuth
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.NotNil(t, decoded.GitHubContentsPermission)
	require.Empty(t, *decoded.GitHubContentsPermission)
}

func TestResolveGitExecutionAuthResolvesCredentialsAfterRead(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-org")
	dsSlug := must.Slug("git-backend")
	txm := &resolveGitAuthTransactionManager{}
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:   dsSlug,
			Type:   model.DataSourceTypeGit,
			Config: []byte(`{"repo_url":"https://github.com/acme/repo.git"}`),
		}, nil
	}
	credentials := newFakeCredentialService()
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		require.False(t, txm.inTx, "credential resolution must not hold a database transaction")
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindStaticSecret,
			PlaintextJSON: []byte(`{"auth_type":"https","username":"alice","token":"tok123"}`),
		}, nil
	}
	svc, err := New(
		txm,
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		nil,
		credentials,
		NewNoopGitHubAppManager(),
		NewNoopGitLabWebhookManager(),
		testAdmissionService(),
		nil,
		eventtest.NewProducer(&fakeDataSourceEventRecorder{}),
		&fakeDataSourceSchemaImportProducer{},
		ServiceConfig{},
	)
	require.NoError(t, err)

	auth, err := svc.ResolveGitExecutionAuth(context.Background(), orgSlug, dsSlug)

	require.NoError(t, err)
	require.NotNil(t, auth)
	assert.Equal(t, model.DataSourceTypeGit, auth.DataSourceType)
	assert.JSONEq(t, `{"repo_url":"https://github.com/acme/repo.git"}`, string(auth.ConfigJSON))
	assert.JSONEq(t, `{"auth_type":"https","username":"alice","token":"tok123"}`, string(auth.CredentialsJSON))
}

func requireGitCredentialHelper(t *testing.T, env []string) {
	t.Helper()

	for _, value := range env {
		if strings.HasPrefix(value, "GIT_CONFIG_VALUE_") && strings.Contains(value, "BRAIDPLANE_GIT_PASSWORD") {
			require.NotContains(t, value, "tok123")
			require.NotContains(t, value, "ghp_123")
			require.NotContains(t, value, "glpat-123")
			return
		}
	}

	require.Fail(t, "expected a Git credential helper")
}

func TestDataSourceBranchesFromRemote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		remoteBranches    []repostore.RemoteBranch
		configuredDefault string
		want              []DataSourceBranch
	}{
		{
			name: "UsesRemoteDefaultWhenPresent",
			remoteBranches: []repostore.RemoteBranch{
				{Name: "feature"},
				{Name: "main", IsDefault: true},
				{Name: "release"},
			},
			configuredDefault: "release",
			want: []DataSourceBranch{
				{Name: "main", IsDefault: true},
				{Name: "feature"},
				{Name: "release"},
			},
		},
		{
			name: "FallsBackToConfiguredDefault",
			remoteBranches: []repostore.RemoteBranch{
				{Name: "feature"},
				{Name: "main"},
				{Name: "release"},
			},
			configuredDefault: "release",
			want: []DataSourceBranch{
				{Name: "release", IsDefault: true},
				{Name: "feature"},
				{Name: "main"},
			},
		},
		{
			name: "ReturnsLexicalBranchesWithoutDefault",
			remoteBranches: []repostore.RemoteBranch{
				{Name: "release"},
				{Name: "main"},
			},
			want: []DataSourceBranch{
				{Name: "main"},
				{Name: "release"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := dataSourceBranchesFromRemote(tc.remoteBranches, tc.configuredDefault)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestStartProviderDataSourceInstallAcceptsBigQuery(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	repo.CreateFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
		return &repods.DataSource{Slug: params.Slug, Name: params.Name, Type: params.Type, Config: params.ConfigJSON, ReadOnly: params.ReadOnly}, nil
	}
	credentials := newFakeCredentialService()
	credentials.createPendingOAuthFn = func(_ context.Context, params credentialssvc.CreatePendingOAuthCredentialParams) (credentialssvc.CredentialMetadata, error) {
		assert.Equal(t, "bigquery", params.Provider)
		return credentialssvc.CredentialMetadata{Slug: must.Slug("google-grant"), Kind: credentialssvc.CredentialKindOAuth2Grant, Status: credentialssvc.CredentialStatusPending}, nil
	}
	credentials.startOAuthInstallFn = func(_ context.Context, params credentialssvc.StartOAuthInstallParams) (credentialssvc.ProviderConnectorInstall, error) {
		assert.Equal(t, "bigquery", params.Provider)
		assert.True(t, params.IncludeWriteScopes)
		return credentialssvc.ProviderConnectorInstall{Slug: must.Slug("google-install"), Provider: "bigquery", AuthorizationURL: "https://accounts.google.com/o/oauth2/v2/auth"}, nil
	}
	svc, err := New(testutil.FakeTransactionManager{}, repo, testutil.NewFakeSchemaMetadataRepo(), nil, credentials, NewNoopGitHubAppManager(), NewNoopGitLabWebhookManager(), testAdmissionService(), nil, eventtest.NewProducer(&fakeDataSourceEventRecorder{}), &fakeDataSourceSchemaImportProducer{}, ServiceConfig{})
	require.NoError(t, err)
	readOnly := false

	result, err := svc.StartProviderDataSourceInstall(t.Context(), must.Slug("acme"), model.UserEventActor(must.Slug("alice")), "bigquery", &StartProviderDataSourceInstallParams{Name: "Pipelines", Slug: must.Slug("pipelines"), Type: model.DataSourceTypeBigQuery, ConfigJSON: []byte(`{"project_id":"fixture","default_dataset":"crm"}`), ReadOnly: &readOnly})
	require.NoError(t, err)
	assert.Equal(t, model.DataSourceTypeBigQuery, result.DataSource.Type)
	assert.Equal(t, "bigquery", result.Install.Provider)
}

func (f *fakeCredentialService) CreateOrganizationGitLabConnection(ctx context.Context, params credentialssvc.CreateOrganizationGitLabConnectionParams) (credentialssvc.OrganizationGitLabConnection, error) {
	if f.CreateOrganizationGitLabConnectionFn == nil {
		return credentialssvc.OrganizationGitLabConnection{}, credentialssvc.ErrCredentialNotFound
	}
	return f.CreateOrganizationGitLabConnectionFn(ctx, params)
}

func (f *fakeCredentialService) GetOrganizationGitLabConnection(ctx context.Context, org slug.Slug) (credentialssvc.OrganizationGitLabConnection, error) {
	if f.GetOrganizationGitLabConnectionFn == nil {
		return credentialssvc.OrganizationGitLabConnection{}, credentialssvc.ErrCredentialNotFound
	}
	return f.GetOrganizationGitLabConnectionFn(ctx, org)
}

func (f *fakeCredentialService) ListOrganizationGitLabRepositories(ctx context.Context, org slug.Slug) ([]credentialssvc.ProviderRepository, error) {
	if f.ListOrganizationGitLabRepositoriesFn == nil {
		return nil, credentialssvc.ErrCredentialNotFound
	}
	return f.ListOrganizationGitLabRepositoriesFn(ctx, org)
}

func (f *fakeCredentialService) AttachOrganizationGitLabConnection(ctx context.Context, params credentialssvc.AttachOrganizationGitLabConnectionParams) error {
	if f.AttachOrganizationGitLabConnectionFn == nil {
		return credentialssvc.ErrCredentialNotFound
	}
	return f.AttachOrganizationGitLabConnectionFn(ctx, params)
}

func (f *fakeCredentialService) ConfigureHTTPOAuthApp(ctx context.Context, params credentialssvc.ConfigureHTTPOAuthAppParams) error {
	if f.ConfigureHTTPOAuthAppFn == nil {
		return xerror.Unexpected().Detail("unexpected ConfigureHTTPOAuthApp call")
	}
	return f.ConfigureHTTPOAuthAppFn(ctx, params)
}

func (f *fakeCredentialService) ConnectHTTPOAuthClientCredentials(ctx context.Context, params credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.CredentialMetadata, error) {
	if f.ConnectHTTPOAuthClientCredentialsFn == nil {
		return credentialssvc.CredentialMetadata{}, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuthClientCredentials call")
	}
	return f.ConnectHTTPOAuthClientCredentialsFn(ctx, params)
}

func (f *fakeCredentialService) ConnectHTTPOAuth(ctx context.Context, p credentialssvc.StartOAuthInstallParams) (*credentialssvc.ProviderConnectorInstall, error) {
	if f.ConnectHTTPOAuthFn == nil {
		return nil, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuth call")
	}
	return f.ConnectHTTPOAuthFn(ctx, p)
}

func (f *fakeCredentialService) AcquireHTTPOAuthTestCredential(ctx context.Context, p credentialssvc.HTTPOAuthApp) (credentialssvc.RuntimeCredential, error) {
	if f.AcquireHTTPOAuthTestCredentialFn == nil {
		return credentialssvc.RuntimeCredential{}, xerror.Unexpected().Detail("unexpected AcquireHTTPOAuthTestCredential call")
	}
	return f.AcquireHTTPOAuthTestCredentialFn(ctx, p)
}

func (*fakeCredentialService) CreateNotificationConnection(context.Context, credentialssvc.CreateNotificationConnectionParams) (credentialssvc.NotificationConnection, error) {
	return credentialssvc.NotificationConnection{}, credentialssvc.ErrCredentialNotFound
}

func (*fakeCredentialService) ListNotificationConnections(context.Context, slug.Slug) ([]credentialssvc.NotificationConnection, error) {
	return nil, credentialssvc.ErrCredentialNotFound
}

func (*fakeCredentialService) ConnectNotificationConnection(context.Context, credentialssvc.CredentialOwner, model.EventActor) (credentialssvc.ProviderConnectorInstall, error) {
	return credentialssvc.ProviderConnectorInstall{}, credentialssvc.ErrCredentialNotFound
}
