package queriessvc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	executionruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	qrepo "github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

type testConnectorExecutor struct {
	factory connector.NewConnectorFunc
}

type errorConnectorExecutor struct {
	output *result.ExecutionResult
	err    error
}

func (e errorConnectorExecutor) ExecuteConnector(
	context.Context,
	queriessvc.ConnectorExecutionRequest,
) (*result.ExecutionResult, error) {
	return e.output, e.err
}

func (e testConnectorExecutor) ExecuteConnector(
	ctx context.Context,
	request queriessvc.ConnectorExecutionRequest,
) (*result.ExecutionResult, error) {
	factory := e.factory
	if factory == nil {
		factory = connector.NewConnectorWithTracing(connector.NewConnector)
	}
	conn, err := factory(ctx, request.DataSourceType, request.ConfigJSON, request.CredentialsJSON)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	return conn.Execute(ctx, request.StatementJSON, request.Parameters, request.Limits)
}

func newTestService(
	queryRepo qrepo.Repository,
	dsService datasourcessvc.Service,
	txm postgres.TransactionManager,
	connectorFactory connector.NewConnectorFunc,
) (queriessvc.Service, error) {
	if txm == nil {
		txm = &testutil.FakeTransactionManager{}
	}

	return queriessvc.NewWithExecutionLimits(
		txm,
		queryRepo,
		dsService,
		testConnectorExecutor{factory: connectorFactory},
		newFakeCredentialService(),
		&testutil.FakeClock{},
		testExecutionLimits(),
		eventtest.NewProducer(&fakeQueryEventRecorder{}),
	)
}

func TestNewWithExecutionLimitsRequiresConnectorExecutor(t *testing.T) {
	t.Parallel()

	svc, err := queriessvc.NewWithExecutionLimits(
		&testutil.FakeTransactionManager{},
		nil,
		nil,
		nil,
		newFakeCredentialService(),
		&testutil.FakeClock{},
		testExecutionLimits(),
		nil,
	)
	require.Nil(t, svc)
	require.ErrorContains(t, err, "connector executor is required")
}

func newTestServiceWithEvents(
	queryRepo qrepo.Repository,
	dsService datasourcessvc.Service,
	txm postgres.TransactionManager,
	connectorFactory connector.NewConnectorFunc,
	recorder *fakeQueryEventRecorder,
) (queriessvc.Service, error) {
	if txm == nil {
		txm = &testutil.FakeTransactionManager{}
	}
	if recorder == nil {
		recorder = &fakeQueryEventRecorder{}
	}

	return queriessvc.NewWithExecutionLimits(
		txm,
		queryRepo,
		dsService,
		testConnectorExecutor{factory: connectorFactory},
		newFakeCredentialService(),
		&testutil.FakeClock{},
		testExecutionLimits(),
		eventtest.NewProducer(recorder),
	)
}

func testExecutionLimits() queriessvc.ExecutionLimits {
	return queriessvc.ExecutionLimits{
		QueryTimeout:                      30 * time.Second,
		ConnectorTimeout:                  30 * time.Second,
		MaxResultRows:                     1000,
		MaxResponseBytes:                  10 * 1024 * 1024,
		ConnectorConcurrencyPerDataSource: 4,
		HTTPTimeout:                       10 * time.Second,
		HTTPMaxRedirects:                  3,
		HTTPMaxRequestBytes:               1024 * 1024,
		HTTPMaxResponseBytes:              10 * 1024 * 1024,
		HTTPRetryAttempts:                 1,
		HTTPRetryBackoff:                  250 * time.Millisecond,
	}
}

type fakeCredentialService struct {
	ConnectHTTPOAuthFn                  func(context.Context, credentialssvc.StartOAuthInstallParams) (*credentialssvc.ProviderConnectorInstall, error)
	AcquireHTTPOAuthTestCredentialFn    func(context.Context, credentialssvc.HTTPOAuthApp) (credentialssvc.RuntimeCredential, error)
	ConfigureHTTPOAuthAppFn             func(context.Context, credentialssvc.ConfigureHTTPOAuthAppParams) error
	ConnectHTTPOAuthClientCredentialsFn func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.CredentialMetadata, error)
	mu                                  sync.Mutex
	defaultRuntime                      credentialssvc.RuntimeCredential
	runtimeByOwner                      map[credentialssvc.CredentialOwner]credentialssvc.RuntimeCredential
	requestedOwners                     []credentialssvc.CredentialOwner
}

func newFakeCredentialService() *fakeCredentialService {
	return &fakeCredentialService{
		defaultRuntime: credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindStaticSecret,
			PlaintextJSON: []byte(`{"username":"svc-user","password":"secret-password"}`),
		},
		runtimeByOwner: map[credentialssvc.CredentialOwner]credentialssvc.RuntimeCredential{},
	}
}

func (f *fakeCredentialService) CreateStaticSecret(
	context.Context,
	credentialssvc.CreateStaticSecretParams,
) (credentialssvc.CredentialMetadata, error) {
	return credentialssvc.CredentialMetadata{}, nil
}

func (f *fakeCredentialService) CreatePendingOAuthCredential(
	context.Context,
	credentialssvc.CreatePendingOAuthCredentialParams,
) (credentialssvc.CredentialMetadata, error) {
	return credentialssvc.CredentialMetadata{}, nil
}

func (f *fakeCredentialService) StartOAuthInstall(
	context.Context,
	credentialssvc.StartOAuthInstallParams,
) (credentialssvc.ProviderConnectorInstall, error) {
	return credentialssvc.ProviderConnectorInstall{}, nil
}

func (f *fakeCredentialService) FindOAuthInstall(
	context.Context,
	credentialssvc.FindOAuthInstallParams,
) (credentialssvc.CredentialInstall, error) {
	return credentialssvc.CredentialInstall{}, nil
}

func (f *fakeCredentialService) ConsumeOAuthInstall(
	context.Context,
	credentialssvc.ConsumeOAuthInstallParams,
) (credentialssvc.CredentialInstall, error) {
	return credentialssvc.CredentialInstall{}, nil
}

func (f *fakeCredentialService) CompleteOAuthCallback(
	context.Context,
	credentialssvc.CompleteOAuthCallbackParams,
) (credentialssvc.CompleteOAuthCallbackResult, error) {
	return credentialssvc.CompleteOAuthCallbackResult{}, nil
}

func (f *fakeCredentialService) ResolveRuntimeCredential(
	_ context.Context,
	params credentialssvc.ResolveRuntimeCredentialParams,
) (credentialssvc.RuntimeCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requestedOwners = append(f.requestedOwners, params.Owner)
	if runtime, ok := f.runtimeByOwner[params.Owner]; ok {
		runtime.PlaintextJSON = append([]byte(nil), runtime.PlaintextJSON...)
		return runtime, nil
	}
	runtime := f.defaultRuntime
	runtime.PlaintextJSON = append([]byte(nil), runtime.PlaintextJSON...)
	return runtime, nil
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

func newTestServiceWithLimits(
	queryRepo qrepo.Repository,
	dsService datasourcessvc.Service,
	txm postgres.TransactionManager,
	connectorFactory connector.NewConnectorFunc,
	limits queriessvc.ExecutionLimits,
) (queriessvc.Service, error) {
	if txm == nil {
		txm = &testutil.FakeTransactionManager{}
	}

	limits = mergeTestExecutionLimits(limits)

	return queriessvc.NewWithExecutionLimits(
		txm,
		queryRepo,
		dsService,
		testConnectorExecutor{factory: connectorFactory},
		newFakeCredentialService(),
		&testutil.FakeClock{},
		limits,
		eventtest.NewProducer(&fakeQueryEventRecorder{}),
	)
}

func newTestServiceAtTime(
	queryRepo qrepo.Repository,
	dsService datasourcessvc.Service,
	txm postgres.TransactionManager,
	connectorFactory connector.NewConnectorFunc,
	now time.Time,
) (queriessvc.Service, error) {
	if txm == nil {
		txm = &testutil.FakeTransactionManager{}
	}

	return queriessvc.NewWithExecutionLimits(
		txm,
		queryRepo,
		dsService,
		testConnectorExecutor{factory: connectorFactory},
		newFakeCredentialService(),
		&testutil.FakeClock{NowFn: func() time.Time { return now }},
		testExecutionLimits(),
		eventtest.NewProducer(&fakeQueryEventRecorder{}),
	)
}

func mustEncryptTestCredentials(t *testing.T, raw string) []byte {
	t.Helper()

	dummyKey := make([]byte, 32)
	enc, err := crypto.NewAES256(dummyKey)
	require.NoError(t, err)

	encrypted, err := enc.Encrypt([]byte(raw))
	require.NoError(t, err)

	return encrypted
}

type txTrackingManager struct {
	mu           sync.Mutex
	inTx         bool
	runInTxCalls int
}

type fakeQueryEventRecorder struct {
	calls []eventssvc.CreateEventParams
}

func (f *fakeQueryEventRecorder) CreateEvent(
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

func (t *txTrackingManager) RunInTx(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	t.mu.Lock()
	t.runInTxCalls++
	t.inTx = true
	t.mu.Unlock()

	err := fn(ctx, nil)

	t.mu.Lock()
	t.inTx = false
	t.mu.Unlock()

	return err
}

func (t *txTrackingManager) RunRead(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	return fn(ctx, nil)
}

func (t *txTrackingManager) InTx() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inTx
}

func (t *txTrackingManager) RunInTxCalls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.runInTxCalls
}

func TestCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		orgSlug          slug.Slug
		dsSlug           slug.Slug
		params           *queriessvc.CreateParams
		repoSetupFn      func(r *testutil.FakeQueryRepository)
		dsServiceSetupFn func(d *fakes.DataSourceService)
		wantErrContains  string
		validateResultFn func(t *testing.T, q *queriessvc.Query)
	}{
		{
			name:    "CreatesQueryWithValidParams",
			orgSlug: must.Slug("org-1"),
			dsSlug:  must.Slug("postgres-main"),
			params: &queriessvc.CreateParams{
				Name: "Get Users",
				Slug: must.Slug("get-users"),
				Data: []byte(`{"statement":"SELECT * FROM users"}`),
			},
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				// Return nil for FindBySlug to indicate slug doesn't exist (no collision)
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return nil, nil, qrepo.ErrQueryNotFound
				}
			},
			validateResultFn: func(t *testing.T, q *queriessvc.Query) {
				assert.Equal(t, "Get Users", q.Name)
				assert.Equal(t, must.Slug("get-users"), q.Slug)
			},
		},
		{
			name:    "CreatesQueryWithoutExpectedInputContractAsNull",
			orgSlug: must.Slug("org-1"),
			dsSlug:  must.Slug("postgres-main"),
			params: &queriessvc.CreateParams{
				Name: "Get Users Without Contract",
				Slug: must.Slug("get-users-without-contract"),
				Data: []byte(`{"statement":"SELECT * FROM users"}`),
			},
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return nil, nil, qrepo.ErrQueryNotFound
				}
				r.CreateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *qrepo.CreateParams) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					require.Nil(t, params.ExpectedInputContract)
					return &qrepo.Query{
						Slug:                  params.Slug,
						Name:                  params.Name,
						Data:                  params.Data,
						DataSourceSlug:        dsSlug,
						ExpectedInputContract: params.ExpectedInputContract,
					}, nil, nil
				}
			},
			validateResultFn: func(t *testing.T, q *queriessvc.Query) {
				assert.Equal(t, "Get Users Without Contract", q.Name)
				require.Nil(t, q.ExpectedInputContract)
			},
		},
		{
			name:    "CreatesQueryWithExpectedInputContract",
			orgSlug: must.Slug("org-1"),
			dsSlug:  must.Slug("postgres-main"),
			params: &queriessvc.CreateParams{
				Name: "Upload Users",
				Slug: must.Slug("upload-users"),
				Data: []byte(`{"statement":"SELECT * FROM users"}`),
				ExpectedInputContract: &payload.Contract{
					Name:           "users_rows",
					Representation: model.ContractRepresentationRows,
					Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					}},
				},
			},
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return nil, nil, qrepo.ErrQueryNotFound
				}
				r.CreateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *qrepo.CreateParams) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					require.JSONEq(
						t,
						`{"name":"users_rows","representation":"rows","fields":[{"name":"id","type":"integer","required":true}]}`,
						string(params.ExpectedInputContract),
					)
					return &qrepo.Query{
						Slug:                  params.Slug,
						Name:                  params.Name,
						Data:                  params.Data,
						DataSourceSlug:        dsSlug,
						ExpectedInputContract: params.ExpectedInputContract,
					}, nil, nil
				}
			},
			validateResultFn: func(t *testing.T, q *queriessvc.Query) {
				require.NotNil(t, q.ExpectedInputContract)
				assert.Equal(t, "users_rows", q.ExpectedInputContract.Name)
				assert.Equal(
					t,
					model.ContractRepresentationRows,
					q.ExpectedInputContract.Representation,
				)
				require.Len(t, q.ExpectedInputContract.Fields, 1)
				assert.Equal(t, "id", q.ExpectedInputContract.Fields[0].Name)
			},
		},
		{
			name:    "ReturnsValidationErrorOnEmptyName",
			orgSlug: must.Slug("org-1"),
			dsSlug:  must.Slug("postgres-main"),
			params: &queriessvc.CreateParams{
				Name: "",
				Slug: must.Slug("get-users"),
				Data: []byte(`{"statement":"SELECT * FROM users"}`),
			},
			wantErrContains: "Name",
		},
		{
			name:    "RejectsReservedRuntimeParameterNamespace",
			orgSlug: must.Slug("org-1"),
			dsSlug:  must.Slug("postgres-main"),
			params: &queriessvc.CreateParams{
				Name: "Get Users",
				Slug: must.Slug("get-users"),
				Data: []byte(`{"statement":"SELECT * FROM users"}`),
				Parameters: []*queriessvc.QueryParameter{{
					Name:       "runtime.run.date",
					DataType:   queriessvc.ParameterDataTypeText,
					IsRequired: true,
				}},
			},
			wantErrContains: "runtime.",
		},
		{
			name:    "RejectsUnknownRuntimeParameterBeforeSaving",
			orgSlug: must.Slug("org-1"),
			dsSlug:  must.Slug("postgres-main"),
			params: &queriessvc.CreateParams{
				Name: "Get Users",
				Slug: must.Slug("get-users"),
				Data: []byte(`{"statement":"SELECT {{runtime.run.datetim}}"}`),
			},
			wantErrContains: "unknown runtime parameter runtime.run.datetim",
		},
		{
			name:    "FailsClosedWhenSlugLookupReturnsUnexpectedError",
			orgSlug: must.Slug("org-1"),
			dsSlug:  must.Slug("postgres-main"),
			params: &queriessvc.CreateParams{
				Name: "Get Users",
				Slug: must.Slug("get-users"),
				Data: []byte(`{"statement":"SELECT * FROM users"}`),
			},
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return nil, nil, xerror.Unexpected().Detail("query slug lookup failed")
				}
				r.CreateFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *qrepo.CreateParams) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return nil, nil, xerror.Unexpected().Detail("unexpected create query call")
				}
			},
			wantErrContains: "query slug lookup failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{}
			dsService := &fakes.DataSourceService{}

			if tc.repoSetupFn != nil {
				tc.repoSetupFn(queryRepo)
			}
			if tc.dsServiceSetupFn != nil {
				tc.dsServiceSetupFn(dsService)
			}

			svc, err := newTestService(queryRepo, dsService, nil, nil)
			require.NoError(t, err)

			ctx := context.Background()

			q, err := svc.Create(
				ctx,
				tc.orgSlug,
				tc.dsSlug,
				model.UserEventActor(must.Slug("member-1")),
				tc.params,
			)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
			if tc.validateResultFn != nil {
				tc.validateResultFn(t, q)
			}
		})
	}
}

func TestCreateUsesSingleWriteTransactionWhenSlugPrecheckMisses(t *testing.T) {
	t.Parallel()

	txm := &txTrackingManager{}
	queryRepo := &testutil.FakeQueryRepository{
		FindBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
			return nil, nil, qrepo.ErrQueryNotFound
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, _, dsSlug slug.Slug, params *qrepo.CreateParams) (*qrepo.Query, []*qrepo.QueryParameter, error) {
			return &qrepo.Query{
				Slug:           params.Slug,
				Name:           params.Name,
				Data:           params.Data,
				DataSourceSlug: dsSlug,
			}, nil, nil
		},
	}

	svc, err := newTestService(queryRepo, &fakes.DataSourceService{}, txm, nil)
	require.NoError(t, err)

	_, err = svc.Create(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("postgres-main"),
		model.UserEventActor(must.Slug("member-1")),
		&queriessvc.CreateParams{
			Name: "Get Users",
			Slug: must.Slug("get-users"),
			Data: []byte(`{"statement":"SELECT * FROM users"}`),
		},
	)
	require.NoError(t, err)
	require.Equal(t, 1, txm.RunInTxCalls())
}

func TestCreateEnforcesHubSpotUpsertAccessMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		readOnly bool
		wantErr  bool
	}{
		{name: "ReadOnlyRejectsUpsert", readOnly: true, wantErr: true},
		{name: "WritableAllowsUpsert", readOnly: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{
				FindBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return nil, nil, qrepo.ErrQueryNotFound
				},
				CreateFn: func(_ context.Context, _ postgres.DBTX, _, dsSlug slug.Slug, params *qrepo.CreateParams) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					require.False(t, tc.wantErr, "read-only upsert must not reach the repository")
					return &qrepo.Query{
						Slug:           params.Slug,
						Name:           params.Name,
						Data:           params.Data,
						DataSourceSlug: dsSlug,
					}, nil, nil
				},
			}
			dsService := &fakes.DataSourceService{
				FindBySlugFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Type:     model.DataSourceTypeHubSpot,
						ReadOnly: tc.readOnly,
					}, nil
				},
			}
			svc, err := newTestService(queryRepo, dsService, nil, nil)
			require.NoError(t, err)

			query, err := svc.Create(
				t.Context(),
				must.Slug("org-1"),
				must.Slug("hubspot-crm"),
				model.UserEventActor(must.Slug("member-1")),
				&queriessvc.CreateParams{
					Name: "Upsert Contacts",
					Slug: must.Slug("upsert-contacts"),
					Data: json.RawMessage(`{"statement":"{\"type\":\"hubspot\",\"object_type\":\"contacts\",\"operation\":\"upsert\"}"}`),
				},
			)
			if tc.wantErr {
				require.Nil(t, query)
				require.ErrorIs(t, err, datasourcessvc.ErrDataSourceReadOnly)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, query)
		})
	}
}

func TestCreateRejectsUnsupportedQueryParameterDataType(t *testing.T) {
	t.Parallel()

	svc, err := newTestService(&testutil.FakeQueryRepository{}, &fakes.DataSourceService{}, nil, nil)
	require.NoError(t, err)

	_, err = svc.Create(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("postgres-main"),
		model.UserEventActor(must.Slug("member-1")),
		&queriessvc.CreateParams{
			Name: "Get Users",
			Slug: must.Slug("get-users"),
			Data: []byte(`{"statement":"SELECT * FROM users WHERE id = {{id}}"}`),
			Parameters: []*queriessvc.QueryParameter{
				{
					Name:     "id",
					DataType: queriessvc.ParameterDataTypeUnknown,
				},
			},
		},
	)
	require.ErrorIs(t, err, queriessvc.ErrInvalidInput)
	require.ErrorContains(t, err, "queryparamdatatype")
}

func TestCreateRejectsNilQueryParameter(t *testing.T) {
	t.Parallel()

	svc, err := newTestService(&testutil.FakeQueryRepository{}, &fakes.DataSourceService{}, nil, nil)
	require.NoError(t, err)

	_, err = svc.Create(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("postgres-main"),
		model.UserEventActor(must.Slug("member-1")),
		&queriessvc.CreateParams{
			Name:       "Get Users",
			Slug:       must.Slug("get-users"),
			Data:       []byte(`{"statement":"SELECT * FROM users WHERE id = {{id}}"}`),
			Parameters: []*queriessvc.QueryParameter{nil},
		},
	)
	require.ErrorIs(t, err, queriessvc.ErrInvalidInput)
	require.ErrorContains(t, err, "required")
}

func TestCreateEmitsCreateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("org-1")
	dsSlug := must.Slug("postgres-main")
	querySlug := must.Slug("get-users")
	actorSlug := must.Slug("member-1")
	events := &fakeQueryEventRecorder{}

	queryRepo := &testutil.FakeQueryRepository{
		FindBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
			return nil, nil, qrepo.ErrQueryNotFound
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug slug.Slug, params *qrepo.CreateParams) (*qrepo.Query, []*qrepo.QueryParameter, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, dsSlug, gotDSSlug)
			require.Equal(t, querySlug, params.Slug)
			return &qrepo.Query{
				Slug:           params.Slug,
				Name:           params.Name,
				Data:           params.Data,
				DataSourceSlug: gotDSSlug,
			}, nil, nil
		},
	}
	svc, err := newTestServiceWithEvents(queryRepo, &fakes.DataSourceService{}, nil, nil, events)
	require.NoError(t, err)

	_, err = svc.Create(
		context.Background(),
		orgSlug,
		dsSlug,
		model.UserEventActor(actorSlug),
		&queriessvc.CreateParams{
			Name: "Get Users",
			Slug: querySlug,
			Data: []byte(`{"statement":"SELECT * FROM users"}`),
		})
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeQuery, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, querySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionCreate, got.Action)
	require.Equal(t, map[string]any{
		"data_source_slug": dsSlug.String(),
	}, got.Metadata)
}

func mergeTestExecutionLimits(overrides queriessvc.ExecutionLimits) queriessvc.ExecutionLimits {
	limits := testExecutionLimits()
	if overrides.QueryTimeout != 0 {
		limits.QueryTimeout = overrides.QueryTimeout
	}
	if overrides.ConnectorTimeout != 0 {
		limits.ConnectorTimeout = overrides.ConnectorTimeout
	}
	if overrides.MaxResultRows != 0 {
		limits.MaxResultRows = overrides.MaxResultRows
	}
	if overrides.MaxResponseBytes != 0 {
		limits.MaxResponseBytes = overrides.MaxResponseBytes
	}
	if overrides.ConnectorConcurrencyPerDataSource != 0 {
		limits.ConnectorConcurrencyPerDataSource = overrides.ConnectorConcurrencyPerDataSource
	}
	if overrides.ConnectorConcurrencyQueueTimeout != 0 {
		limits.ConnectorConcurrencyQueueTimeout = overrides.ConnectorConcurrencyQueueTimeout
	}
	if overrides.ConnectorConcurrencyMaxQueued != 0 {
		limits.ConnectorConcurrencyMaxQueued = overrides.ConnectorConcurrencyMaxQueued
	}
	if overrides.HTTPTimeout != 0 {
		limits.HTTPTimeout = overrides.HTTPTimeout
	}
	if overrides.HTTPMaxRedirects != 0 {
		limits.HTTPMaxRedirects = overrides.HTTPMaxRedirects
	}
	if overrides.HTTPMaxRequestBytes != 0 {
		limits.HTTPMaxRequestBytes = overrides.HTTPMaxRequestBytes
	}
	if overrides.HTTPMaxResponseBytes != 0 {
		limits.HTTPMaxResponseBytes = overrides.HTTPMaxResponseBytes
	}
	if overrides.HTTPRetryAttempts != 0 {
		limits.HTTPRetryAttempts = overrides.HTTPRetryAttempts
	}
	if overrides.HTTPRetryBackoff != 0 {
		limits.HTTPRetryBackoff = overrides.HTTPRetryBackoff
	}
	return limits
}

func TestFindBySlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		dsSlug          slug.Slug
		querySlug       slug.Slug
		repoSetupFn     func(r *testutil.FakeQueryRepository)
		wantErrContains string
		validateFn      func(t *testing.T, q *queriessvc.Query)
	}{
		{
			name:      "FindsQueryBySlug",
			orgSlug:   must.Slug("org-1"),
			dsSlug:    must.Slug("postgres-main"),
			querySlug: must.Slug("get-users"),
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return &qrepo.Query{
						Slug: querySlug,
						Name: "Get Users",
						Data: []byte(`{"statement":"SELECT * FROM users"}`),
					}, nil, nil
				}
			},
			validateFn: func(t *testing.T, q *queriessvc.Query) {
				assert.Equal(t, "Get Users", q.Name)
				assert.Equal(t, must.Slug("get-users"), q.Slug)
			},
		},
		{
			name:      "FindsQueryBySlugWithExpectedInputContract",
			orgSlug:   must.Slug("org-1"),
			dsSlug:    must.Slug("postgres-main"),
			querySlug: must.Slug("upload-users"),
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return &qrepo.Query{
						Slug: querySlug,
						Name: "Upload Users",
						Data: []byte(`{"statement":"SELECT * FROM users"}`),
						ExpectedInputContract: []byte(
							`{"name":"users_rows","representation":"rows","fields":[{"name":"id","type":"integer","required":true}]}`,
						),
					}, nil, nil
				}
			},
			validateFn: func(t *testing.T, q *queriessvc.Query) {
				require.NotNil(t, q.ExpectedInputContract)
				assert.Equal(t, "users_rows", q.ExpectedInputContract.Name)
				assert.Equal(
					t,
					model.ContractRepresentationRows,
					q.ExpectedInputContract.Representation,
				)
				require.Len(t, q.ExpectedInputContract.Fields, 1)
				assert.Equal(
					t,
					model.ContractFieldTypeValueInteger,
					q.ExpectedInputContract.Fields[0].Type,
				)
			},
		},
		{
			name:            "ReturnsErrorWhenExpectedInputContractIsMalformed",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			querySlug:       must.Slug("upload-users"),
			wantErrContains: "decode expected input contract",
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return &qrepo.Query{
						Slug:                  querySlug,
						Name:                  "Upload Users",
						Data:                  []byte(`{"statement":"SELECT * FROM users"}`),
						ExpectedInputContract: []byte(`{"name":"users_rows",`),
					}, nil, nil
				}
			},
		},
		{
			name:            "ReturnsErrorWhenNotFound",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			querySlug:       must.Slug("nonexistent"),
			wantErrContains: "not found",
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return nil, nil, qrepo.ErrQueryNotFound
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{}
			if tc.repoSetupFn != nil {
				tc.repoSetupFn(queryRepo)
			}

			svc, err := newTestService(queryRepo, nil, nil, nil)
			require.NoError(t, err)

			ctx := context.Background()

			q, err := svc.FindBySlug(ctx, tc.orgSlug, tc.dsSlug, tc.querySlug)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
			if tc.validateFn != nil {
				tc.validateFn(t, q)
			}
		})
	}
}

func TestFindBySlugDoesNotOpenWriteTransaction(t *testing.T) {
	t.Parallel()

	txm := &txTrackingManager{}
	queryRepo := &testutil.FakeQueryRepository{
		FindBySlugFn: func(_ context.Context, _ postgres.DBTX, _, _, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
			return &qrepo.Query{
				Slug: querySlug,
				Name: "Get Users",
				Data: []byte(`{"statement":"SELECT * FROM users"}`),
			}, nil, nil
		},
	}

	svc, err := newTestService(queryRepo, &fakes.DataSourceService{}, txm, nil)
	require.NoError(t, err)

	q, err := svc.FindBySlug(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("postgres-main"),
		must.Slug("get-users"),
	)

	require.NoError(t, err)
	require.Equal(t, must.Slug("get-users"), q.Slug)
	require.Equal(t, 0, txm.RunInTxCalls())
}

func TestUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		dsSlug          slug.Slug
		querySlug       slug.Slug
		params          *queriessvc.UpdateParams
		repoSetupFn     func(r *testutil.FakeQueryRepository)
		wantErrContains string
		validateFn      func(t *testing.T, q *queriessvc.Query)
	}{
		{
			name:      "UpdatesQuerySuccessfully",
			orgSlug:   must.Slug("org-1"),
			dsSlug:    must.Slug("postgres-main"),
			querySlug: must.Slug("get-users"),
			params: &queriessvc.UpdateParams{
				Name: "Get All Users",
				Data: []byte(`{"statement":"SELECT * FROM users LIMIT 100"}`),
			},
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.UpdateFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug, params *qrepo.UpdateParams) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return &qrepo.Query{
						Slug:           querySlug,
						Name:           params.Name,
						Data:           params.Data,
						DataSourceSlug: dsSlug,
					}, nil, nil
				}
			},
			validateFn: func(t *testing.T, q *queriessvc.Query) {
				assert.Equal(t, "Get All Users", q.Name)
			},
		},
		{
			name:      "RejectsReservedRuntimeParameterNamespace",
			orgSlug:   must.Slug("org-1"),
			dsSlug:    must.Slug("postgres-main"),
			querySlug: must.Slug("get-users"),
			params: &queriessvc.UpdateParams{
				Name: "Get All Users",
				Data: []byte(`{"statement":"SELECT * FROM users LIMIT 100"}`),
				Parameters: []*queriessvc.QueryParameter{{
					Name:       "runtime.run.datetime",
					DataType:   queriessvc.ParameterDataTypeText,
					IsRequired: true,
				}},
			},
			wantErrContains: "runtime.",
		},
		{
			name:      "RejectsUnknownRuntimeParameterBeforeUpdating",
			orgSlug:   must.Slug("org-1"),
			dsSlug:    must.Slug("postgres-main"),
			querySlug: must.Slug("get-users"),
			params: &queriessvc.UpdateParams{
				Name: "Get Users",
				Data: []byte(`{"statement":"SELECT {{runtime.previous_success.unknown}}"}`),
			},
			wantErrContains: "unknown runtime parameter runtime.previous_success.unknown",
		},
		{
			name:      "RejectsNilQueryParameter",
			orgSlug:   must.Slug("org-1"),
			dsSlug:    must.Slug("postgres-main"),
			querySlug: must.Slug("get-users"),
			params: &queriessvc.UpdateParams{
				Name:       "Get All Users",
				Data:       []byte(`{"statement":"SELECT * FROM users LIMIT 100"}`),
				Parameters: []*queriessvc.QueryParameter{nil},
			},
			wantErrContains: "required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{}
			if tc.repoSetupFn != nil {
				tc.repoSetupFn(queryRepo)
			}

			svc, err := newTestService(queryRepo, nil, nil, nil)
			require.NoError(t, err)

			ctx := context.Background()

			q, err := svc.Update(
				ctx,
				tc.orgSlug,
				tc.dsSlug,
				tc.querySlug,
				model.UserEventActor(must.Slug("member-1")),
				tc.params,
			)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
			if tc.validateFn != nil {
				tc.validateFn(t, q)
			}
		})
	}
}

func TestUpdateEmitsUpdateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("org-1")
	dsSlug := must.Slug("postgres-main")
	querySlug := must.Slug("get-users")
	actorSlug := must.Slug("member-1")
	events := &fakeQueryEventRecorder{}

	queryRepo := &testutil.FakeQueryRepository{
		UpdateFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotDSSlug, gotQuerySlug slug.Slug, params *qrepo.UpdateParams) (*qrepo.Query, []*qrepo.QueryParameter, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, dsSlug, gotDSSlug)
			require.Equal(t, querySlug, gotQuerySlug)
			return &qrepo.Query{
				Slug:           gotQuerySlug,
				Name:           params.Name,
				Data:           params.Data,
				DataSourceSlug: gotDSSlug,
			}, nil, nil
		},
	}
	svc, err := newTestServiceWithEvents(queryRepo, &fakes.DataSourceService{}, nil, nil, events)
	require.NoError(t, err)

	_, err = svc.Update(
		context.Background(),
		orgSlug,
		dsSlug,
		querySlug,
		model.UserEventActor(actorSlug),
		&queriessvc.UpdateParams{
			Name: "Get All Users",
			Data: []byte(`{"statement":"SELECT * FROM users LIMIT 100"}`),
		})
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeQuery, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, querySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionUpdate, got.Action)
	require.Equal(t, map[string]any{
		"data_source_slug": dsSlug.String(),
	}, got.Metadata)
}

func TestDelete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		dsSlug          slug.Slug
		querySlug       slug.Slug
		actorSlug       slug.Slug
		repoSetupFn     func(t *testing.T, r *testutil.FakeQueryRepository)
		wantErrContains string
	}{
		{
			name:      "DeletesQuerySuccessfully",
			orgSlug:   must.Slug("org-1"),
			dsSlug:    must.Slug("postgres-main"),
			querySlug: must.Slug("get-users"),
			actorSlug: must.Slug("member-1"),
			repoSetupFn: func(t *testing.T, r *testutil.FakeQueryRepository) {
				r.DeleteFn = func(
					_ context.Context,
					_ postgres.DBTX,
					orgSlug, dsSlug, querySlug slug.Slug,
				) error {
					assert.Equal(t, must.Slug("org-1"), orgSlug)
					assert.Equal(t, must.Slug("postgres-main"), dsSlug)
					assert.Equal(t, must.Slug("get-users"), querySlug)
					return nil
				}
			},
		},
		{
			name:            "ReturnsErrorWhenNotFound",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			querySlug:       must.Slug("nonexistent"),
			actorSlug:       must.Slug("member-1"),
			wantErrContains: "not found",
			repoSetupFn: func(_ *testing.T, r *testutil.FakeQueryRepository) {
				r.DeleteFn = func(
					_ context.Context,
					_ postgres.DBTX,
					_, _, _ slug.Slug,
				) error {
					return qrepo.ErrQueryNotFound
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{}
			if tc.repoSetupFn != nil {
				tc.repoSetupFn(t, queryRepo)
			}

			svc, err := newTestService(queryRepo, nil, nil, nil)
			require.NoError(t, err)

			ctx := context.Background()

			err = svc.Delete(
				ctx,
				tc.orgSlug,
				tc.dsSlug,
				tc.querySlug,
				model.UserEventActor(tc.actorSlug),
			)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestDeleteEmitsDeleteEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("org-1")
	dsSlug := must.Slug("postgres-main")
	querySlug := must.Slug("get-users")
	actorSlug := must.Slug("member-1")
	events := &fakeQueryEventRecorder{}
	queryRepo := &testutil.FakeQueryRepository{}

	svc, err := newTestServiceWithEvents(queryRepo, &fakes.DataSourceService{}, nil, nil, events)
	require.NoError(t, err)

	err = svc.Delete(context.Background(), orgSlug, dsSlug, querySlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeQuery, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, querySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionDelete, got.Action)
	require.Equal(t, map[string]any{
		"data_source_slug": dsSlug.String(),
	}, got.Metadata)
}

func TestRestore(t *testing.T) {
	t.Parallel()

	queryRepo := &testutil.FakeQueryRepository{
		RestoreFn: func(
			_ context.Context,
			_ postgres.DBTX,
			orgSlug, dsSlug, querySlug slug.Slug,
		) error {
			assert.Equal(t, must.Slug("org-1"), orgSlug)
			assert.Equal(t, must.Slug("postgres-main"), dsSlug)
			assert.Equal(t, must.Slug("get-users"), querySlug)
			return nil
		},
	}

	svc, err := newTestService(queryRepo, nil, nil, nil)
	require.NoError(t, err)

	err = svc.Restore(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("postgres-main"),
		must.Slug("get-users"),
		model.UserEventActor(must.Slug("member-1")),
	)

	require.NoError(t, err)
}

func TestRestoreEmitsRestoreEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("org-1")
	dsSlug := must.Slug("postgres-main")
	querySlug := must.Slug("get-users")
	actorSlug := must.Slug("member-1")
	events := &fakeQueryEventRecorder{}
	queryRepo := &testutil.FakeQueryRepository{}

	svc, err := newTestServiceWithEvents(queryRepo, &fakes.DataSourceService{}, nil, nil, events)
	require.NoError(t, err)

	err = svc.Restore(context.Background(), orgSlug, dsSlug, querySlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeQuery, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, querySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionRestore, got.Action)
	require.Equal(t, map[string]any{
		"data_source_slug": dsSlug.String(),
	}, got.Metadata)
}

func TestPurge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		repoSetupFn     func(r *testutil.FakeQueryRepository)
		wantErrContains string
	}{
		{
			name: "PurgesDeletedQuerySuccessfully",
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.PurgeFn = func(
					_ context.Context,
					_ postgres.DBTX,
					orgSlug, dsSlug, querySlug slug.Slug,
				) error {
					assert.Equal(t, must.Slug("org-1"), orgSlug)
					assert.Equal(t, must.Slug("postgres-main"), dsSlug)
					assert.Equal(t, must.Slug("get-users"), querySlug)
					return nil
				}
			},
		},
		{
			name:            "ReturnsConflictWhenQueryIsActive",
			wantErrContains: "query must be deleted before purge",
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.PurgeFn = func(
					_ context.Context,
					_ postgres.DBTX,
					_, _, _ slug.Slug,
				) error {
					return queriessvc.ErrQueryNotDeleted
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{}
			tc.repoSetupFn(queryRepo)

			svc, err := newTestService(queryRepo, nil, nil, nil)
			require.NoError(t, err)

			err = svc.Purge(
				context.Background(),
				must.Slug("org-1"),
				must.Slug("postgres-main"),
				must.Slug("get-users"),
				model.UserEventActor(must.Slug("member-1")),
			)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestPurgeEmitsPurgeEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("org-1")
	dsSlug := must.Slug("postgres-main")
	querySlug := must.Slug("get-users")
	actorSlug := must.Slug("member-1")
	events := &fakeQueryEventRecorder{}
	queryRepo := &testutil.FakeQueryRepository{}

	svc, err := newTestServiceWithEvents(queryRepo, &fakes.DataSourceService{}, nil, nil, events)
	require.NoError(t, err)

	err = svc.Purge(context.Background(), orgSlug, dsSlug, querySlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeQuery, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, querySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionPurge, got.Action)
	require.Equal(t, map[string]any{
		"data_source_slug": dsSlug.String(),
	}, got.Metadata)
}

func TestExecute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		orgSlug            slug.Slug
		dsSlug             slug.Slug
		querySlug          slug.Slug
		paramValues        map[string]any
		repoSetupFn        func(r *testutil.FakeQueryRepository)
		dsServiceSetupFn   func(d *fakes.DataSourceService)
		connectorExecuteFn func(
			ctx context.Context,
			dsType model.DataSourceType,
			queryData json.RawMessage,
			params map[string]types.TypedParam,
		) (*result.ExecutionResult, error)
		wantErrContains string
		validateFn      func(t *testing.T, result *result.ExecutionResult)
	}{
		{
			name:        "ExecutesQuerySuccessfully",
			orgSlug:     must.Slug("org-1"),
			dsSlug:      must.Slug("postgres-main"),
			querySlug:   must.Slug("get-users"),
			paramValues: map[string]any{},
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return &qrepo.Query{
						Slug:           querySlug,
						Name:           "Get Users",
						Data:           []byte(`{"statement":"SELECT * FROM users"}`),
						DataSourceSlug: dsSlug,
					}, nil, nil
				}
			},
			dsServiceSetupFn: func(d *fakes.DataSourceService) {
				// Create dummy encrypted credentials with username and password
				dummyKey := make([]byte, 32)
				enc, _ := crypto.NewAES256(dummyKey)
				encryptedCreds, _ := enc.Encrypt(
					[]byte(`{"username":"testuser","password":"testpass"}`),
				)

				d.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Slug:     dsSlug,
						Type:     model.DataSourceTypePostgres,
						IsActive: true,
						Config: json.RawMessage(
							`{"host":"localhost","port":5432,"database":"testdb"}`,
						),
						EncryptedCredentials: encryptedCreds,
					}, nil
				}
			},
			validateFn: func(t *testing.T, res *result.ExecutionResult) {
				require.NotNil(t, res)
			},
		},
		{
			name:            "ReturnsErrorOnInactiveDataSource",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			querySlug:       must.Slug("get-users"),
			paramValues:     map[string]any{},
			wantErrContains: "unavailable",
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return &qrepo.Query{
						Slug:           querySlug,
						Name:           "Get Users",
						Data:           []byte(`{"statement":"SELECT * FROM users"}`),
						DataSourceSlug: dsSlug,
					}, nil, nil
				}
			},
			dsServiceSetupFn: func(d *fakes.DataSourceService) {
				d.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Slug:     dsSlug,
						IsActive: false,
						Type:     model.DataSourceTypePostgres,
						Config:   json.RawMessage(`{}`),
					}, nil
				}
			},
		},
		{
			name:            "ReturnsErrorOnMissingRequiredParameter",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			querySlug:       must.Slug("get-user-by-id"),
			paramValues:     map[string]any{}, // user_id is missing
			wantErrContains: "missing required parameter \"user_id\"",
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return &qrepo.Query{
						Slug: querySlug,
						Name: "Get User by ID",
						Data: []byte(
							`{"statement":"SELECT * FROM users WHERE id = {{user_id}}"}`,
						),
						DataSourceSlug: dsSlug,
					}, nil, nil
				}
			},
			dsServiceSetupFn: func(d *fakes.DataSourceService) {
				dummyKey := make([]byte, 32)
				enc, err := crypto.NewAES256(dummyKey)
				require.NoError(t, err)

				encryptedCreds, err := enc.Encrypt(
					[]byte(`{"username":"testuser","password":"testpass"}`),
				)
				require.NoError(t, err)

				d.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Slug:     dsSlug,
						Type:     model.DataSourceTypePostgres,
						IsActive: true,
						Config: json.RawMessage(
							`{"host":"localhost","port":5432,"database":"testdb"}`,
						),
						EncryptedCredentials: encryptedCreds,
					}, nil
				}
			},
		},
		{
			name:        "SucceedsWithAllRequiredParametersProvided",
			orgSlug:     must.Slug("org-1"),
			dsSlug:      must.Slug("postgres-main"),
			querySlug:   must.Slug("get-user-by-id"),
			paramValues: map[string]any{"user_id": 123},
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return &qrepo.Query{
						Slug: querySlug,
						Name: "Get User by ID",
						Data: []byte(
							`{"statement":"SELECT * FROM users WHERE id = {{user_id}}"}`,
						),
						DataSourceSlug: dsSlug,
					}, nil, nil
				}
			},
			dsServiceSetupFn: func(d *fakes.DataSourceService) {
				dummyKey := make([]byte, 32)
				enc, err := crypto.NewAES256(dummyKey)
				require.NoError(t, err)

				encryptedCreds, err := enc.Encrypt(
					[]byte(`{"username":"testuser","password":"testpass"}`),
				)
				require.NoError(t, err)

				d.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Slug:     dsSlug,
						Type:     model.DataSourceTypePostgres,
						IsActive: true,
						Config: json.RawMessage(
							`{"host":"localhost","port":5432,"database":"testdb"}`,
						),
						EncryptedCredentials: encryptedCreds,
					}, nil
				}
			},
			validateFn: func(t *testing.T, res *result.ExecutionResult) {
				require.NotNil(t, res)
			},
		},
		{
			name:      "CoercesFloat64IntegerParameterBeforeExecution",
			orgSlug:   must.Slug("org-1"),
			dsSlug:    must.Slug("mysql-main"),
			querySlug: must.Slug("get-users-by-email"),
			paramValues: map[string]any{
				"email": "test.user@example.com",
				"limit": float64(5),
			},
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return &qrepo.Query{
							Slug: querySlug,
							Name: "Get users by email",
							Data: []byte(
								`{"statement":"SELECT * FROM users WHERE email = {{email}} LIMIT {{limit}}"}`,
							),
							DataSourceSlug: dsSlug,
						}, []*qrepo.QueryParameter{
							{
								Slug:     must.Slug("email"),
								Name:     "email",
								DataType: "text",
							},
							{
								Slug:     must.Slug("limit"),
								Name:     "limit",
								DataType: "integer",
							},
						}, nil
				}
			},
			dsServiceSetupFn: func(d *fakes.DataSourceService) {
				dummyKey := make([]byte, 32)
				enc, _ := crypto.NewAES256(dummyKey)
				encryptedCreds, _ := enc.Encrypt(
					[]byte(`{"username":"testuser","password":"testpass"}`),
				)

				d.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Slug:     dsSlug,
						Type:     model.DataSourceTypeMySQL,
						IsActive: true,
						Config: json.RawMessage(
							`{"host":"localhost","port":33307,"database":"braidplane"}`,
						),
						EncryptedCredentials: encryptedCreds,
					}, nil
				}
			},
			validateFn: func(t *testing.T, res *result.ExecutionResult) {
				require.NotNil(t, res)
			},
		},
		{
			name:            "ReturnsQueryTimeoutWhenConnectorRespectsContextDeadline",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			querySlug:       must.Slug("slow-query"),
			paramValues:     map[string]any{},
			wantErrContains: "query timeout",
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return &qrepo.Query{
						Slug: querySlug,
						Name: "Slow Query",
						Data: []byte(
							`{"statement":"SELECT pg_sleep(2)","timeout_seconds":1}`,
						),
						DataSourceSlug: dsSlug,
					}, nil, nil
				}
			},
			dsServiceSetupFn: func(d *fakes.DataSourceService) {
				dummyKey := make([]byte, 32)
				enc, _ := crypto.NewAES256(dummyKey)
				encryptedCreds, _ := enc.Encrypt(
					[]byte(`{"username":"testuser","password":"testpass"}`),
				)

				d.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Slug:     dsSlug,
						Type:     model.DataSourceTypePostgres,
						IsActive: true,
						Config: json.RawMessage(
							`{"host":"localhost","port":5432,"database":"testdb"}`,
						),
						EncryptedCredentials: encryptedCreds,
					}, nil
				}
			},
			connectorExecuteFn: func(ctx context.Context, dsType model.DataSourceType, queryData json.RawMessage, params map[string]types.TypedParam) (*result.ExecutionResult, error) {
				<-ctx.Done()
				return result.NewSQLResult(
					"postgres",
					&result.SQLResult{
						Rows:        []map[string]any{},
						ColumnNames: []string{},
						RowCount:    0,
					},
					1,
					ctx.Err(),
				), nil
			},
		},
		{
			name:        "UsesStoredDefaultForOptionalParameterDuringExecution",
			orgSlug:     must.Slug("org-1"),
			dsSlug:      must.Slug("postgres-main"),
			querySlug:   must.Slug("default-limit-query"),
			paramValues: map[string]any{},
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					defaultValue := "100"
					return &qrepo.Query{
							Slug: querySlug,
							Name: "Default Limit Query",
							Data: []byte(
								`{"statement":"SELECT * FROM users LIMIT {{limit}}"}`,
							),
							DataSourceSlug: dsSlug,
						}, []*qrepo.QueryParameter{
							{
								Slug:         must.Slug("limit"),
								Name:         "limit",
								DataType:     "integer",
								DefaultValue: &defaultValue,
							},
						}, nil
				}
			},
			dsServiceSetupFn: func(d *fakes.DataSourceService) {
				dummyKey := make([]byte, 32)
				enc, _ := crypto.NewAES256(dummyKey)
				encryptedCreds, _ := enc.Encrypt(
					[]byte(`{"username":"testuser","password":"testpass"}`),
				)

				d.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Slug:     dsSlug,
						Type:     model.DataSourceTypePostgres,
						IsActive: true,
						Config: json.RawMessage(
							`{"host":"localhost","port":5432,"database":"testdb"}`,
						),
						EncryptedCredentials: encryptedCreds,
					}, nil
				}
			},
			connectorExecuteFn: func(ctx context.Context, dsType model.DataSourceType, queryData json.RawMessage, params map[string]types.TypedParam) (*result.ExecutionResult, error) {
				limitParam, ok := params["limit"]
				require.True(t, ok)
				require.Equal(t, "integer", limitParam.DataType)
				require.Equal(t, "100", limitParam.Value)

				return result.NewSQLResult(
					"postgres",
					&result.SQLResult{
						Rows:        []map[string]any{},
						ColumnNames: []string{},
						RowCount:    0,
					},
					1,
					nil,
				), nil
			},
		},
		{
			name:      "ReturnsExecutionFailureWhenConnectorParameterCoercionFails",
			orgSlug:   must.Slug("org-1"),
			dsSlug:    must.Slug("mysql-main"),
			querySlug: must.Slug("coercion-fail-query"),
			paramValues: map[string]any{
				"limit": "abc",
			},
			wantErrContains: "connector execution failed",
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return &qrepo.Query{
							Slug:           querySlug,
							Name:           "Coercion Fail Query",
							Data:           []byte(`{"statement":"SELECT {{limit}}"}`),
							DataSourceSlug: dsSlug,
						}, []*qrepo.QueryParameter{
							{
								Slug:     must.Slug("limit"),
								Name:     "limit",
								DataType: "integer",
							},
						}, nil
				}
			},
			dsServiceSetupFn: func(d *fakes.DataSourceService) {
				dummyKey := make([]byte, 32)
				enc, _ := crypto.NewAES256(dummyKey)
				encryptedCreds, _ := enc.Encrypt(
					[]byte(`{"username":"testuser","password":"testpass"}`),
				)

				d.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Slug:     dsSlug,
						Type:     model.DataSourceTypeMySQL,
						IsActive: true,
						Config: json.RawMessage(
							`{"host":"localhost","port":33307,"database":"braidplane"}`,
						),
						EncryptedCredentials: encryptedCreds,
					}, nil
				}
			},
			connectorExecuteFn: func(ctx context.Context, dsType model.DataSourceType, queryData json.RawMessage, params map[string]types.TypedParam) (*result.ExecutionResult, error) {
				return nil, fmt.Errorf("invalid integer parameter value")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{}
			dsService := &fakes.DataSourceService{}

			if tc.repoSetupFn != nil {
				tc.repoSetupFn(queryRepo)
			}
			if tc.dsServiceSetupFn != nil {
				tc.dsServiceSetupFn(dsService)
			}

			// Create mock connector factory for Execute tests
			mockConnectorFactory := func(_ context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
				// Return a mock SQL connector that succeeds
				mockConnector := testutil.NewFakeSQLConnector()
				mockConnector.ExecuteFn = func(ctx context.Context, queryData json.RawMessage, params map[string]types.TypedParam) (*result.ExecutionResult, error) {
					if tc.connectorExecuteFn != nil {
						return tc.connectorExecuteFn(ctx, dsType, queryData, params)
					}

					// Verify parameter normalization happens in connector layer, not service layer
					if limitParam, ok := params["limit"]; ok {
						require.Equal(
							t,
							"integer",
							limitParam.DataType,
							"limit parameter should have integer data type",
						)
						// The actual value will still be float64 from JSON unmarshaling
						// Connectors are responsible for coercing based on DataType
					}

					switch dsType {
					case model.DataSourceTypePostgres:
						fallthrough
					case model.DataSourceTypeMySQL:
						fallthrough
					case model.DataSourceTypeBigQuery:
						fallthrough
					case model.DataSourceTypeSnowflake:
						vendorType := "postgres"
						if dsType == model.DataSourceTypeMySQL {
							vendorType = "mysql"
						}
						if dsType == model.DataSourceTypeBigQuery {
							vendorType = "bigquery"
						}
						if dsType == model.DataSourceTypeSnowflake {
							vendorType = "snowflake"
						}

						return result.NewSQLResult(
							vendorType,
							&result.SQLResult{
								Rows:        []map[string]any{},
								ColumnNames: []string{},
								RowCount:    0,
							},
							1,
							nil,
						), nil
					case model.DataSourceTypeMongoDB:
						docCount := 0
						return result.NewMongoDBResult(
							&result.MongoDBResult{
								OperationType: "find",
								Documents:     []map[string]any{},
								DocumentCount: &docCount,
							},
							1,
							nil,
						), nil
					case model.DataSourceTypeHTTP:
						return result.NewHTTPResult(
							&result.HTTPResult{
								StatusCode: 200,
								Headers:    map[string]string{},
								Body:       json.RawMessage(`{}`),
							},
							1,
							nil,
						), nil
					case model.DataSourceTypeHubSpot:
						return result.NewObjectRowsResult(
							"hubspot",
							&result.ObjectRowsResult{
								ObjectType:  "contacts",
								Operation:   "list",
								Rows:        []map[string]any{},
								ColumnNames: []string{"hs_object_id"},
								RowCount:    0,
								NextCursor:  nil,
								HasMore:     false,
							},
							1,
							nil,
						), nil
					case model.DataSourceTypeFTP:
						return result.NewFTPResult(
							&result.FileResult{
								Rows:        []map[string]any{},
								ColumnNames: []string{},
								RowCount:    0,
								Format:      "json",
								Operation:   "read",
							},
							1,
							nil,
						), nil
					case model.DataSourceTypeS3:
						return result.NewS3Result(
							&result.FileResult{
								Rows:        []map[string]any{},
								ColumnNames: []string{},
								RowCount:    0,
								Format:      "json",
								Operation:   "read",
							},
							1,
							nil,
						), nil
					case model.DataSourceTypeGit, model.DataSourceTypeGithub,
						model.DataSourceTypeGitlab:
						return result.NewGitResult(
							&result.FileResult{
								Rows:        []map[string]any{},
								ColumnNames: []string{},
								RowCount:    0,
								Format:      "json",
								Operation:   "read",
							},
							1,
							nil,
						), nil
					case model.DataSourceTypeUnknown:
						return result.NewHTTPResult(nil, 1, nil), nil
					}

					return result.NewHTTPResult(nil, 1, nil), nil
				}
				return mockConnector, nil
			}

			svc, err := newTestService(queryRepo, dsService, nil, mockConnectorFactory)
			require.NoError(t, err)

			ctx := context.Background()

			res, err := svc.Execute(
				ctx,
				tc.orgSlug,
				tc.dsSlug,
				tc.querySlug,
				model.UserEventActor(must.Slug("member-1")),
				tc.paramValues,
			)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
			if tc.validateFn != nil {
				tc.validateFn(t, res)
			}
		})
	}
}

func TestExecuteInjectsReservedRuntimeParamsForSavedGitPathTemplate(t *testing.T) {
	t.Parallel()

	queryRepo := &testutil.FakeQueryRepository{}
	queryRepo.FindBySlugFn = func(
		context.Context,
		postgres.DBTX,
		slug.Slug,
		slug.Slug,
		slug.Slug,
	) (*qrepo.Query, []*qrepo.QueryParameter, error) {
		return &qrepo.Query{
			Slug:           must.Slug("git-users"),
			Name:           "Git Users",
			DataSourceSlug: must.Slug("git-main"),
			Data: []byte(
				`{"statement":"{\"type\":\"file\",\"operation\":\"read\",\"path\":\"reports/{{runtime.run.date}}/users.csv\"}"}`,
			),
		}, nil, nil
	}

	dsService := &fakes.DataSourceService{}
	dsService.FindBySlugFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
	) (*datasourcessvc.DataSource, error) {
		return &datasourcessvc.DataSource{
			Slug:                 must.Slug("git-main"),
			Type:                 model.DataSourceTypeGit,
			IsActive:             true,
			Config:               json.RawMessage(`{"repo_url":"https://example.com/acme/reports.git"}`),
			EncryptedCredentials: mustEncryptTestCredentials(t, `{"auth_type":"https"}`),
		}, nil
	}

	fixedNow := time.Date(2026, 5, 15, 0, 30, 0, 0, time.FixedZone("+02", 2*60*60))
	expectedNow := fixedNow
	connectorFactory := func(
		_ context.Context,
		dsType model.DataSourceType,
		_,
		_ []byte,
	) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			_ context.Context,
			queryData json.RawMessage,
			params map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			require.Equal(t, model.DataSourceTypeGit, dsType)
			require.JSONEq(
				t,
				`{"type":"file","operation":"read","path":"reports/{{runtime.run.date}}/users.csv"}`,
				string(queryData),
			)
			require.Equal(t, expectedNow.UTC().Format(time.DateOnly), params["runtime.run.date"].Value)
			require.Equal(t, expectedNow.UTC().Format(time.RFC3339), params["runtime.run.datetime"].Value)

			return result.NewGitResult(
				&result.FileResult{
					Operation:  "read",
					TargetPath: "reports/2026-05-14/users.csv",
				},
				0,
				nil,
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestServiceAtTime(queryRepo, dsService, nil, connectorFactory, fixedNow)
	require.NoError(t, err)

	execResult, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		must.Slug("git-main"),
		must.Slug("git-users"),
		model.UserEventActor(must.Slug("member-1")),
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	expectedNow = fixedNow.Add(-48 * time.Hour)
	pinned := executionruntime.WithExecutionContext(t.Context(), executionruntime.ExecutionContext{At: expectedNow})
	_, err = svc.Execute(pinned, "acme", "git-main", "git-users", model.UserEventActor("member-1"), nil)
	require.NoError(t, err)
}

func TestExecuteRejectsReadOnlyMutatingDirectOperationsBeforeConnectorDispatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dsType    model.DataSourceType
		dsSlug    slug.Slug
		querySlug slug.Slug
		statement string
		result    *result.ExecutionResult
	}{
		{
			name:      "SavedGitWrite",
			dsType:    model.DataSourceTypeGit,
			dsSlug:    must.Slug("git-main"),
			querySlug: must.Slug("write-users"),
			statement: `{"type":"file","operation":"write","path":"exports/users.json","format":"json","content":[{"id":1}]}`,
			result: result.NewGitResult(
				&result.FileResult{Operation: "write", TargetPath: "exports/users.json"},
				0,
				nil,
			),
		},
		{
			name:      "SavedHTTPPost",
			dsType:    model.DataSourceTypeHTTP,
			dsSlug:    must.Slug("http-main"),
			querySlug: must.Slug("post-users"),
			statement: `{"method":"POST","path":"/users","body":{"id":1}}`,
			result: result.NewHTTPResult(
				&result.HTTPResult{
					StatusCode: 202,
					Headers:    map[string]string{},
					Body:       json.RawMessage(`{"accepted":true}`),
				},
				0,
				nil,
			),
		},
		{
			name:      "UnknownGitOperationFailsClosed",
			dsType:    model.DataSourceTypeGit,
			dsSlug:    must.Slug("git-main"),
			querySlug: must.Slug("archive-users"),
			statement: `{"type":"file","operation":"archive","path":"exports/users.json","format":"json"}`,
			result: result.NewGitResult(
				&result.FileResult{Operation: "archive", TargetPath: "exports/users.json"},
				0,
				nil,
			),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{}
			queryRepo.FindBySlugFn = func(
				context.Context,
				postgres.DBTX,
				slug.Slug,
				slug.Slug,
				slug.Slug,
			) (*qrepo.Query, []*qrepo.QueryParameter, error) {
				return &qrepo.Query{
					Slug:           tc.querySlug,
					Name:           "Mutating Query",
					DataSourceSlug: tc.dsSlug,
					Data:           []byte(fmt.Sprintf(`{"statement":%q}`, tc.statement)),
				}, nil, nil
			}

			executionRecordCalls := 0
			queryRepo.CreateExecutionFn = func(
				context.Context,
				postgres.DBTX,
				slug.Slug,
				slug.Slug,
				slug.Slug,
				*qrepo.CreateExecutionParams,
			) (*qrepo.QueryExecution, error) {
				executionRecordCalls++
				return &qrepo.QueryExecution{Slug: must.Slug("exec-1")}, nil
			}

			dsService := &fakes.DataSourceService{}
			dsService.FindBySlugFn = func(
				context.Context,
				slug.Slug,
				slug.Slug,
			) (*datasourcessvc.DataSource, error) {
				return &datasourcessvc.DataSource{
					Slug:                 tc.dsSlug,
					Type:                 tc.dsType,
					IsActive:             true,
					ReadOnly:             true,
					Config:               json.RawMessage(`{}`),
					EncryptedCredentials: mustEncryptTestCredentials(t, `{}`),
				}, nil
			}

			connectorCalls := 0
			connectorFactory := func(
				context.Context,
				model.DataSourceType,
				[]byte,
				[]byte,
			) (connector.Connector, error) {
				connectorCalls++
				mockConnector := testutil.NewFakeSQLConnector()
				mockConnector.ExecuteFn = func(
					context.Context,
					json.RawMessage,
					map[string]types.TypedParam,
				) (*result.ExecutionResult, error) {
					return tc.result, nil
				}
				return mockConnector, nil
			}

			svc, err := newTestService(queryRepo, dsService, nil, connectorFactory)
			require.NoError(t, err)

			execResult, err := svc.Execute(
				context.Background(),
				must.Slug("acme"),
				tc.dsSlug,
				tc.querySlug,
				model.UserEventActor(must.Slug("member-1")),
				nil,
			)

			assert.True(t, xerror.Is(err, datasourcessvc.ErrDataSourceReadOnly), "got %v", err)
			assert.Nil(t, execResult)
			assert.Zero(t, connectorCalls, "read-only mutations must be blocked before connector dispatch")
			assert.Zero(t, executionRecordCalls, "blocked writes must not record successful executions")
		})
	}
}

func TestExecuteInlineRejectsReadOnlyGitWriteBeforeConnectorDispatch(t *testing.T) {
	t.Parallel()

	dsSlug := must.Slug("git-main")
	dsService := &fakes.DataSourceService{}
	dsService.FindBySlugFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
	) (*datasourcessvc.DataSource, error) {
		return &datasourcessvc.DataSource{
			Slug:                 dsSlug,
			Type:                 model.DataSourceTypeGit,
			IsActive:             true,
			ReadOnly:             true,
			Config:               json.RawMessage(`{}`),
			EncryptedCredentials: mustEncryptTestCredentials(t, `{}`),
		}, nil
	}

	connectorCalls := 0
	connectorFactory := func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (connector.Connector, error) {
		connectorCalls++
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			context.Context,
			json.RawMessage,
			map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			return result.NewGitResult(
				&result.FileResult{Operation: "write", TargetPath: "exports/users.json"},
				0,
				nil,
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestService(&testutil.FakeQueryRepository{}, dsService, nil, connectorFactory)
	require.NoError(t, err)

	execResult, err := svc.ExecuteInline(
		context.Background(),
		must.Slug("acme"),
		dsSlug,
		map[string]any{
			"type":      "file",
			"operation": "write",
			"path":      "exports/users.json",
			"format":    "json",
			"content":   []any{map[string]any{"id": 1}},
		},
		nil,
	)

	assert.True(t, xerror.Is(err, datasourcessvc.ErrDataSourceReadOnly), "got %v", err)
	assert.Nil(t, execResult)
	assert.Zero(t, connectorCalls, "read-only inline writes must be blocked before connector dispatch")
}

func TestExecuteAllowsReadOnlyGitRead(t *testing.T) {
	t.Parallel()

	dsSlug := must.Slug("git-main")
	querySlug := must.Slug("read-users")

	queryRepo := &testutil.FakeQueryRepository{}
	queryRepo.FindBySlugFn = func(
		context.Context,
		postgres.DBTX,
		slug.Slug,
		slug.Slug,
		slug.Slug,
	) (*qrepo.Query, []*qrepo.QueryParameter, error) {
		return &qrepo.Query{
			Slug:           querySlug,
			Name:           "Read Users",
			DataSourceSlug: dsSlug,
			Data: []byte(
				`{"statement":"{\"type\":\"file\",\"operation\":\"read\",\"path\":\"exports/users.json\",\"format\":\"json\"}"}`,
			),
		}, nil, nil
	}

	dsService := &fakes.DataSourceService{}
	dsService.FindBySlugFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
	) (*datasourcessvc.DataSource, error) {
		return &datasourcessvc.DataSource{
			Slug:                 dsSlug,
			Type:                 model.DataSourceTypeGit,
			IsActive:             true,
			ReadOnly:             true,
			Config:               json.RawMessage(`{}`),
			EncryptedCredentials: mustEncryptTestCredentials(t, `{}`),
		}, nil
	}

	connectorCalls := 0
	connectorFactory := func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (connector.Connector, error) {
		connectorCalls++
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			context.Context,
			json.RawMessage,
			map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			return result.NewGitResult(
				&result.FileResult{
					Rows:        []map[string]any{{"id": float64(1)}},
					ColumnNames: []string{"id"},
					RowCount:    1,
					Operation:   "read",
					TargetPath:  "exports/users.json",
				},
				0,
				nil,
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestService(queryRepo, dsService, nil, connectorFactory)
	require.NoError(t, err)

	execResult, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		dsSlug,
		querySlug,
		model.UserEventActor(must.Slug("member-1")),
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, execResult)
	assert.Equal(t, 1, connectorCalls)
	fileResult, ok := execResult.Git()
	require.True(t, ok)
	require.NotNil(t, fileResult)
	assert.Equal(t, "read", fileResult.Operation)
}

func TestExecuteInlinePropagatesReadOnlyModeToConnector(t *testing.T) {
	t.Parallel()

	dsSlug := must.Slug("postgres-main")
	dsService := &fakes.DataSourceService{}
	dsService.FindBySlugFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
	) (*datasourcessvc.DataSource, error) {
		return &datasourcessvc.DataSource{
			Slug:                 dsSlug,
			Type:                 model.DataSourceTypePostgres,
			IsActive:             true,
			ReadOnly:             true,
			Config:               json.RawMessage(`{}`),
			EncryptedCredentials: mustEncryptTestCredentials(t, `{}`),
		}, nil
	}

	var gotLimits types.ExecutionLimits
	connectorFactory := func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteWithLimitsFn = func(
			_ context.Context,
			_ json.RawMessage,
			_ map[string]types.TypedParam,
			limits types.ExecutionLimits,
		) (*result.ExecutionResult, error) {
			gotLimits = limits
			return result.NewSQLResult(
				"postgres",
				&result.SQLResult{
					Rows:        []map[string]any{{"value": int64(1)}},
					ColumnNames: []string{"value"},
					RowCount:    1,
				},
				0,
				nil,
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestService(&testutil.FakeQueryRepository{}, dsService, nil, connectorFactory)
	require.NoError(t, err)

	execResult, err := svc.ExecuteInline(
		context.Background(),
		must.Slug("acme"),
		dsSlug,
		map[string]any{"type": "postgres", "sql": "SELECT 1 AS value"},
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.True(t, gotLimits.ReadOnly)
}

func TestExecute_AppliesConfiguredTimeoutCap(t *testing.T) {
	t.Parallel()

	queryRepo, dsService := safetyLimitQueryFixtures(t, []byte(
		`{"statement":"SELECT pg_sleep(300)","timeout_seconds":300}`,
	))

	connectorFactory := func(
		_ context.Context,
		_ model.DataSourceType,
		_, _ []byte,
	) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			ctx context.Context,
			queryData json.RawMessage,
			params map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "execution context must carry the configured timeout")
			require.LessOrEqual(t, time.Until(deadline), 200*time.Millisecond)

			<-ctx.Done()
			return result.NewSQLResult(
				"postgres",
				&result.SQLResult{Rows: []map[string]any{}, ColumnNames: []string{}, RowCount: 0},
				1,
				ctx.Err(),
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestServiceWithLimits(
		queryRepo,
		dsService,
		nil,
		connectorFactory,
		queriessvc.ExecutionLimits{
			QueryTimeout:                      25 * time.Millisecond,
			ConnectorTimeout:                  time.Second,
			MaxResultRows:                     1000,
			MaxResponseBytes:                  1024 * 1024,
			ConnectorConcurrencyPerDataSource: 1,
		},
	)
	require.NoError(t, err)

	_, err = svc.Execute(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("postgres-main"),
		must.Slug("limited-query"),
		model.UserEventActor(must.Slug("member-1")),
		map[string]any{},
	)

	require.Error(t, err)
	require.True(t, xerror.Is(err, queriessvc.ErrQueryTimeout), "got %v", err)
}

func TestExecute_CancelsConnectorWhenRequestContextIsCancelled(t *testing.T) {
	t.Parallel()

	queryRepo, dsService := safetyLimitQueryFixtures(t, []byte(`{"statement":"SELECT * FROM users"}`))
	connectorEntered := make(chan struct{})

	connectorFactory := func(
		_ context.Context,
		_ model.DataSourceType,
		_, _ []byte,
	) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			ctx context.Context,
			queryData json.RawMessage,
			params map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			close(connectorEntered)
			<-ctx.Done()
			return result.NewSQLResult(
				"postgres",
				&result.SQLResult{Rows: []map[string]any{}, ColumnNames: []string{}, RowCount: 0},
				1,
				ctx.Err(),
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestServiceWithLimits(
		queryRepo,
		dsService,
		nil,
		connectorFactory,
		queriessvc.ExecutionLimits{
			QueryTimeout:                      time.Second,
			ConnectorTimeout:                  time.Second,
			MaxResultRows:                     1000,
			MaxResponseBytes:                  1024 * 1024,
			ConnectorConcurrencyPerDataSource: 1,
		},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, execErr := svc.Execute(
			ctx,
			must.Slug("org-1"),
			must.Slug("postgres-main"),
			must.Slug("limited-query"),
			model.UserEventActor(must.Slug("member-1")),
			map[string]any{},
		)
		errCh <- execErr
	}()

	<-connectorEntered
	cancel()

	require.ErrorIs(t, <-errCh, context.Canceled)
}

func TestExecute_EnforcesHTTPResultLimits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		body        string
		savedLimit  int
		serverLimit int
		maxBytes    int64
		wantErr     error
	}{
		{"saved limit", `[1,2]`, 1, 10, 1024, xerror.ExecutionRowLimitExceeded()},
		{"server limit", `[1,2]`, 10, 1, 1024, xerror.ExecutionRowLimitExceeded()},
		{"at effective limit", `[1,2]`, 2, 10, 1024, nil},
		{"object with nested array", `{"items":[1,2,3]}`, 1, 10, 1024, nil},
		{"empty array", `[]`, 1, 10, 1024, nil},
		{"null", `null`, 1, 10, 1024, nil},
		{"array bytes independent", `[1]`, 1, 10, 1, xerror.ExecutionResponseSizeExceeded()},
		{"object bytes independent", `{"items":[1,2,3]}`, 1, 10, 1, xerror.ExecutionResponseSizeExceeded()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			queryData := json.RawMessage(fmt.Sprintf(`{"statement":%q,"row_limit":%d}`, `{"method":"GET","path":"/posts"}`, tc.savedLimit))
			queryRepo, dsService := safetyLimitQueryFixtures(t, queryData)
			factory := func(_ context.Context, _ model.DataSourceType, _, _ []byte) (connector.Connector, error) {
				fake := testutil.NewFakeSQLConnector()
				fake.ExecuteFn = func(context.Context, json.RawMessage, map[string]types.TypedParam) (*result.ExecutionResult, error) {
					return result.NewHTTPResult(&result.HTTPResult{StatusCode: 200, Body: json.RawMessage(tc.body)}, 1, nil), nil
				}
				return fake, nil
			}
			svc, err := newTestServiceWithLimits(queryRepo, dsService, nil, factory,
				queriessvc.ExecutionLimits{MaxResultRows: tc.serverLimit, MaxResponseBytes: tc.maxBytes})
			require.NoError(t, err)
			got, err := svc.Execute(t.Context(), must.Slug("org-1"), must.Slug("postgres-main"),
				must.Slug("limited-query"), model.UserEventActor(must.Slug("member-1")), nil)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.Contains(t, xerror.PublicDetail(err), "HTTP request completed with status 200")
				require.Contains(t, xerror.PublicDetail(err), "Verify remote state before retrying a write")
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				payload, ok := got.HTTP()
				require.True(t, ok)
				require.JSONEq(t, tc.body, string(payload.Body))
			}
		})
	}
}

func TestExecute_ReturnsRowLimitViolation(t *testing.T) {
	t.Parallel()

	queryRepo, dsService := safetyLimitQueryFixtures(t, []byte(`{"statement":"SELECT * FROM users"}`))
	connectorFactory := func(
		_ context.Context,
		_ model.DataSourceType,
		_, _ []byte,
	) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			context.Context,
			json.RawMessage,
			map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult(
				"postgres",
				&result.SQLResult{
					Rows: []map[string]any{
						{"id": 1},
						{"id": 2},
					},
					ColumnNames: []string{"id"},
					RowCount:    2,
				},
				1,
				nil,
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestServiceWithLimits(
		queryRepo,
		dsService,
		nil,
		connectorFactory,
		queriessvc.ExecutionLimits{
			QueryTimeout:                      time.Second,
			ConnectorTimeout:                  time.Second,
			MaxResultRows:                     1,
			MaxResponseBytes:                  1024 * 1024,
			ConnectorConcurrencyPerDataSource: 1,
		},
	)
	require.NoError(t, err)

	_, err = svc.Execute(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("postgres-main"),
		must.Slug("limited-query"),
		model.UserEventActor(must.Slug("member-1")),
		map[string]any{},
	)

	require.Error(t, err)
	require.True(t, xerror.Is(err, xerror.ExecutionRowLimitExceeded()), "got %v", err)
}

func TestExecute_ReturnsResponseSizeViolation(t *testing.T) {
	t.Parallel()

	queryRepo, dsService := safetyLimitQueryFixtures(t, []byte(`{"statement":"GET /users"}`))
	connectorFactory := func(
		_ context.Context,
		_ model.DataSourceType,
		_, _ []byte,
	) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			context.Context,
			json.RawMessage,
			map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			return result.NewHTTPResult(
				&result.HTTPResult{
					StatusCode: 200,
					Headers:    map[string]string{},
					Body:       json.RawMessage(`{"payload":"too-large"}`),
				},
				1,
				nil,
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestServiceWithLimits(
		queryRepo,
		dsService,
		nil,
		connectorFactory,
		queriessvc.ExecutionLimits{
			QueryTimeout:                      time.Second,
			ConnectorTimeout:                  time.Second,
			MaxResultRows:                     1000,
			MaxResponseBytes:                  int64(len(`{"ok":true}`)),
			ConnectorConcurrencyPerDataSource: 1,
		},
	)
	require.NoError(t, err)

	_, err = svc.Execute(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("postgres-main"),
		must.Slug("limited-query"),
		model.UserEventActor(must.Slug("member-1")),
		map[string]any{},
	)

	require.Error(t, err)
	require.True(t, xerror.Is(err, xerror.ExecutionResponseSizeExceeded()), "got %v", err)
}

func TestExecute_DoesNotHoldTransactionDuringConnectorExecution(t *testing.T) {
	t.Parallel()

	queryRepo, dsService := safetyLimitQueryFixtures(t, []byte(`{"statement":"SELECT 1"}`))
	txm := &txTrackingManager{}

	connectorFactory := func(
		_ context.Context,
		_ model.DataSourceType,
		_, _ []byte,
	) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			context.Context,
			json.RawMessage,
			map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			require.False(t, txm.InTx(), "connector execution must happen outside app DB tx")
			return result.NewSQLResult(
				"postgres",
				&result.SQLResult{Rows: []map[string]any{}, ColumnNames: []string{}, RowCount: 0},
				1,
				nil,
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestServiceWithLimits(
		queryRepo,
		dsService,
		txm,
		connectorFactory,
		queriessvc.ExecutionLimits{
			QueryTimeout:                      time.Second,
			ConnectorTimeout:                  time.Second,
			MaxResultRows:                     1000,
			MaxResponseBytes:                  1024 * 1024,
			ConnectorConcurrencyPerDataSource: 1,
		},
	)
	require.NoError(t, err)

	_, err = svc.Execute(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("postgres-main"),
		must.Slug("limited-query"),
		model.UserEventActor(must.Slug("member-1")),
		map[string]any{},
	)
	require.NoError(t, err)
}

func TestExecute_PreservesConnectorConcurrencyLimitError(t *testing.T) {
	t.Parallel()

	queryRepo, dsService := safetyLimitQueryFixtures(t, []byte(`{"statement":"SELECT 1"}`))
	concurrencyErr := xerror.ExecutionConcurrencyLimitExceeded().Detail(
		"connector execution queue is full",
	)
	svc, err := queriessvc.NewWithExecutionLimits(
		&testutil.FakeTransactionManager{},
		queryRepo,
		dsService,
		errorConnectorExecutor{err: concurrencyErr},
		newFakeCredentialService(),
		&testutil.FakeClock{},
		testExecutionLimits(),
		eventtest.NewProducer(&fakeQueryEventRecorder{}),
	)
	require.NoError(t, err)

	_, err = svc.Execute(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("postgres-main"),
		must.Slug("limited-query"),
		model.UserEventActor(must.Slug("member-1")),
		map[string]any{},
	)

	require.ErrorIs(t, err, xerror.ExecutionConcurrencyLimitExceeded())
	require.NotErrorIs(t, err, queriessvc.ErrDataSourceUnavailable)
}

func TestExecutePreservesRecognizedProviderOutcomes(t *testing.T) {
	t.Parallel()
	for _, outcome := range []*xerror.Error{xerror.HTTPAcknowledgementRejected(), xerror.HTTPAcknowledgementUncertain(), xerror.DestinationConstraintViolation()} {
		t.Run(outcome.Code(), func(t *testing.T) {
			outcome := outcome.WithPublicDetail("Verify remote state before retrying a write.")
			for _, execution := range []struct {
				name     string
				executor errorConnectorExecutor
			}{
				{"ConnectorError", errorConnectorExecutor{err: outcome}},
				{"ResultError", errorConnectorExecutor{output: result.NewHTTPResult(nil, 1, outcome)}},
			} {
				for _, mode := range []string{"Saved", "Inline"} {
					t.Run(execution.name+mode, func(t *testing.T) {
						queryRepo, sources := safetyLimitQueryFixtures(t, []byte(`{"statement":"SELECT 1"}`))
						svc, err := queriessvc.NewWithExecutionLimits(&testutil.FakeTransactionManager{}, queryRepo, sources,
							execution.executor, newFakeCredentialService(), &testutil.FakeClock{}, testExecutionLimits(), eventtest.NewProducer(&fakeQueryEventRecorder{}))
						require.NoError(t, err)
						if mode == "Saved" {
							_, err = svc.Execute(t.Context(), must.Slug("org-1"), must.Slug("postgres-main"), must.Slug("limited-query"), model.UserEventActor(must.Slug("member-1")), nil)
						} else {
							_, err = svc.ExecuteInline(t.Context(), must.Slug("org-1"), must.Slug("postgres-main"), map[string]any{"type": "postgres", "sql": "SELECT 1"}, nil)
						}
						require.ErrorIs(t, err, outcome)
						require.NotErrorIs(t, err, queriessvc.ErrDataSourceUnavailable)
						require.NotErrorIs(t, err, queriessvc.ErrQueryExecutionFailed)
						require.Equal(t, outcome.PublicDetail(), xerror.PublicDetail(err))
					})
				}
			}
		})
	}
}

func safetyLimitQueryFixtures(
	t *testing.T,
	queryData json.RawMessage,
) (*testutil.FakeQueryRepository, *fakes.DataSourceService) {
	t.Helper()

	queryRepo := &testutil.FakeQueryRepository{}
	queryRepo.FindBySlugFn = func(
		context.Context,
		postgres.DBTX,
		slug.Slug,
		slug.Slug,
		slug.Slug,
	) (*qrepo.Query, []*qrepo.QueryParameter, error) {
		return &qrepo.Query{
			Slug:           must.Slug("limited-query"),
			Name:           "Limited Query",
			Data:           queryData,
			DataSourceSlug: must.Slug("postgres-main"),
		}, nil, nil
	}

	dummyKey := make([]byte, 32)
	enc, err := crypto.NewAES256(dummyKey)
	require.NoError(t, err)

	encryptedCreds, err := enc.Encrypt([]byte(`{"username":"testuser","password":"testpass"}`))
	require.NoError(t, err)

	dsService := &fakes.DataSourceService{}
	dsService.FindBySlugFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
	) (*datasourcessvc.DataSource, error) {
		return &datasourcessvc.DataSource{
			Slug:                 must.Slug("postgres-main"),
			Type:                 model.DataSourceTypePostgres,
			IsActive:             true,
			Config:               json.RawMessage(`{"host":"localhost","port":5432,"database":"testdb"}`),
			EncryptedCredentials: encryptedCreds,
		}, nil
	}

	return queryRepo, dsService
}

func TestExecuteInline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		orgSlug            slug.Slug
		dsSlug             slug.Slug
		operationInline    map[string]any
		paramValues        map[string]any
		repoSetupFn        func(r *testutil.FakeQueryRepository)
		dsServiceSetupFn   func(d *fakes.DataSourceService)
		connectorExecuteFn func(
			ctx context.Context,
			dsType model.DataSourceType,
			queryData json.RawMessage,
			params map[string]types.TypedParam,
		) (*result.ExecutionResult, error)
		wantErrContains string
		validateFn      func(t *testing.T, res *result.ExecutionResult)
	}{
		{
			name:            "ExecutesInlineQueryWithoutLoadingStoredQuery",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			operationInline: map[string]any{"sql": "SELECT * FROM users WHERE id = {{user_id}}"},
			paramValues:     map[string]any{"user_id": 123},
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
					return nil, nil, fmt.Errorf("unexpected FindBySlug call")
				}
				r.CreateExecutionFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug, params *qrepo.CreateExecutionParams) (*qrepo.QueryExecution, error) {
					return nil, fmt.Errorf("unexpected CreateExecution call")
				}
			},
			dsServiceSetupFn: func(d *fakes.DataSourceService) {
				dummyKey := make([]byte, 32)
				enc, err := crypto.NewAES256(dummyKey)
				require.NoError(t, err)

				encryptedCreds, err := enc.Encrypt(
					[]byte(`{"username":"testuser","password":"testpass"}`),
				)
				require.NoError(t, err)

				d.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Slug:     dsSlug,
						Type:     model.DataSourceTypePostgres,
						IsActive: true,
						Config: json.RawMessage(
							`{"host":"localhost","port":5432,"database":"testdb"}`,
						),
						EncryptedCredentials: encryptedCreds,
					}, nil
				}
			},
			connectorExecuteFn: func(ctx context.Context, dsType model.DataSourceType, queryData json.RawMessage, params map[string]types.TypedParam) (*result.ExecutionResult, error) {
				require.Equal(t, model.DataSourceTypePostgres, dsType)
				require.JSONEq(
					t,
					`{"sql":"SELECT * FROM users WHERE id = {{user_id}}"}`,
					string(queryData),
				)
				userIDParam, ok := params["user_id"]
				require.True(t, ok)
				require.Equal(t, "text", userIDParam.DataType)
				require.Equal(t, 123, userIDParam.Value)

				return result.NewSQLResult(
					"postgres",
					&result.SQLResult{
						Rows:        []map[string]any{{"id": 123}},
						ColumnNames: []string{"id"},
						RowCount:    1,
					},
					1,
					nil,
				), nil
			},
			validateFn: func(t *testing.T, res *result.ExecutionResult) {
				require.NotNil(t, res)
				require.Equal(t, result.ExecutionResultTypeSQL, res.Type())
			},
		},
		{
			name:            "ReturnsErrorOnInactiveDataSource",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			operationInline: map[string]any{"sql": "SELECT 1"},
			paramValues:     map[string]any{},
			wantErrContains: "unavailable",
			dsServiceSetupFn: func(d *fakes.DataSourceService) {
				d.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Slug:     dsSlug,
						Type:     model.DataSourceTypePostgres,
						IsActive: false,
						Config:   json.RawMessage(`{}`),
					}, nil
				}
			},
		},
		{
			name:            "ReturnsErrorOnMissingInlinePayload",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			operationInline: nil,
			paramValues:     map[string]any{},
			wantErrContains: "inline operation payload is required",
			connectorExecuteFn: func(ctx context.Context, dsType model.DataSourceType, queryData json.RawMessage, params map[string]types.TypedParam) (*result.ExecutionResult, error) {
				return nil, fmt.Errorf("unexpected connector execution")
			},
		},
		{
			name:            "ReturnsErrorOnUnmarshalableInlinePayload",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			operationInline: map[string]any{"body": make(chan int)},
			paramValues:     map[string]any{},
			wantErrContains: "marshal inline operation",
			connectorExecuteFn: func(ctx context.Context, dsType model.DataSourceType, queryData json.RawMessage, params map[string]types.TypedParam) (*result.ExecutionResult, error) {
				return nil, fmt.Errorf("unexpected connector execution")
			},
		},
		{
			name:            "ReturnsErrorOnConnectorExecutionFailure",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			operationInline: map[string]any{"sql": "SELECT 1"},
			paramValues:     map[string]any{},
			wantErrContains: "connector execution failed",
			dsServiceSetupFn: func(d *fakes.DataSourceService) {
				dummyKey := make([]byte, 32)
				enc, err := crypto.NewAES256(dummyKey)
				require.NoError(t, err)

				encryptedCreds, err := enc.Encrypt([]byte(`{}`))
				require.NoError(t, err)

				d.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{
						Slug:                 dsSlug,
						Type:                 model.DataSourceTypePostgres,
						IsActive:             true,
						Config:               json.RawMessage(`{}`),
						EncryptedCredentials: encryptedCreds,
					}, nil
				}
			},
			connectorExecuteFn: func(ctx context.Context, dsType model.DataSourceType, queryData json.RawMessage, params map[string]types.TypedParam) (*result.ExecutionResult, error) {
				return nil, fmt.Errorf("connection refused")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{}
			dsService := &fakes.DataSourceService{}

			if tc.repoSetupFn != nil {
				tc.repoSetupFn(queryRepo)
			}
			if tc.dsServiceSetupFn != nil {
				tc.dsServiceSetupFn(dsService)
			}

			mockConnectorFactory := func(_ context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
				mockConnector := testutil.NewFakeSQLConnector()
				mockConnector.ExecuteFn = func(ctx context.Context, queryData json.RawMessage, params map[string]types.TypedParam) (*result.ExecutionResult, error) {
					if tc.connectorExecuteFn != nil {
						return tc.connectorExecuteFn(ctx, dsType, queryData, params)
					}

					return result.NewSQLResult(
						"postgres",
						&result.SQLResult{
							Rows:        []map[string]any{},
							ColumnNames: []string{},
							RowCount:    0,
						},
						1,
						nil,
					), nil
				}
				return mockConnector, nil
			}

			svc, err := newTestService(queryRepo, dsService, nil, mockConnectorFactory)
			require.NoError(t, err)

			res, err := svc.ExecuteInline(
				context.Background(),
				tc.orgSlug,
				tc.dsSlug,
				tc.operationInline,
				tc.paramValues,
			)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
			if tc.validateFn != nil {
				tc.validateFn(t, res)
			}
		})
	}
}

func TestExecuteInlineInjectsReservedRuntimeParamsForFTPPathTemplate(t *testing.T) {
	t.Parallel()

	dsService := &fakes.DataSourceService{}
	dsService.FindBySlugFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
	) (*datasourcessvc.DataSource, error) {
		return &datasourcessvc.DataSource{
			Slug:                 must.Slug("ftp-main"),
			Type:                 model.DataSourceTypeFTP,
			IsActive:             true,
			Config:               json.RawMessage(`{"host":"ftp.example.com","port":21}`),
			EncryptedCredentials: mustEncryptTestCredentials(t, `{"username":"ftp-user","password":"ftp-pass"}`),
		}, nil
	}

	fixedNow := time.Date(2026, 5, 15, 0, 30, 0, 0, time.FixedZone("+02", 2*60*60))
	connectorFactory := func(
		_ context.Context,
		dsType model.DataSourceType,
		_,
		_ []byte,
	) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			_ context.Context,
			queryData json.RawMessage,
			params map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			require.Equal(t, model.DataSourceTypeFTP, dsType)
			require.JSONEq(
				t,
				`{"type":"file","operation":"read","path":"exports/{{runtime.run.datetime}}/orders.json"}`,
				string(queryData),
			)
			require.Equal(t, "2026-05-14", params["runtime.run.date"].Value)
			require.Equal(t, "2026-05-14T22:30:00Z", params["runtime.run.datetime"].Value)

			return result.NewFTPResult(
				&result.FileResult{
					Operation:  "read",
					TargetPath: "exports/2026-05-14T22:30:00Z/orders.json",
				},
				0,
				nil,
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestServiceAtTime(
		&testutil.FakeQueryRepository{},
		dsService,
		nil,
		connectorFactory,
		fixedNow,
	)
	require.NoError(t, err)

	execResult, err := svc.ExecuteInline(
		context.Background(),
		must.Slug("acme"),
		must.Slug("ftp-main"),
		map[string]any{
			"type":      "file",
			"operation": "read",
			"path":      "exports/{{runtime.run.datetime}}/orders.json",
		},
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())
}

func TestExecuteInjectsReservedRuntimeParamsForFileSinkPathOverrides(t *testing.T) {
	t.Parallel()

	fixedNow := time.Date(2026, 5, 15, 0, 30, 0, 0, time.FixedZone("+02", 2*60*60))

	for _, tc := range []struct {
		name            string
		dsSlug          slug.Slug
		dsType          model.DataSourceType
		querySlug       slug.Slug
		queryData       string
		credentialsJSON string
	}{
		{
			name:            "git",
			dsSlug:          must.Slug("git-main"),
			dsType:          model.DataSourceTypeGit,
			querySlug:       must.Slug("git-write-users"),
			queryData:       `{"statement":"{\"type\":\"file\",\"operation\":\"write\",\"path\":\"stale/users.json\",\"format\":\"json\"}"}`,
			credentialsJSON: `{"auth_type":"https"}`,
		},
		{
			name:            "ftp",
			dsSlug:          must.Slug("ftp-main"),
			dsType:          model.DataSourceTypeFTP,
			querySlug:       must.Slug("ftp-write-users"),
			queryData:       `{"statement":"{\"type\":\"file\",\"operation\":\"write\",\"path\":\"stale/users.json\",\"format\":\"json\"}"}`,
			credentialsJSON: `{"username":"ftp-user","password":"ftp-pass"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{}
			queryRepo.FindBySlugFn = func(
				context.Context,
				postgres.DBTX,
				slug.Slug,
				slug.Slug,
				slug.Slug,
			) (*qrepo.Query, []*qrepo.QueryParameter, error) {
				return &qrepo.Query{
					Slug:           tc.querySlug,
					Name:           "Write Users",
					DataSourceSlug: tc.dsSlug,
					Data:           []byte(tc.queryData),
				}, nil, nil
			}

			dsService := &fakes.DataSourceService{}
			dsService.FindBySlugFn = func(
				context.Context,
				slug.Slug,
				slug.Slug,
			) (*datasourcessvc.DataSource, error) {
				return &datasourcessvc.DataSource{
					Slug:                 tc.dsSlug,
					Type:                 tc.dsType,
					IsActive:             true,
					Config:               json.RawMessage(`{}`),
					EncryptedCredentials: mustEncryptTestCredentials(t, tc.credentialsJSON),
				}, nil
			}

			connectorFactory := func(
				_ context.Context,
				dsType model.DataSourceType,
				_,
				_ []byte,
			) (connector.Connector, error) {
				mockConnector := testutil.NewFakeSQLConnector()
				mockConnector.ExecuteFn = func(
					_ context.Context,
					_ json.RawMessage,
					params map[string]types.TypedParam,
				) (*result.ExecutionResult, error) {
					require.Equal(t, tc.dsType, dsType)
					require.Equal(t, "exports/{{runtime.run.date}}/users.json", params["path"].Value)
					require.Equal(t, "2026-05-14", params["runtime.run.date"].Value)
					require.Equal(t, "2026-05-14T22:30:00Z", params["runtime.run.datetime"].Value)

					switch dsType {
					case model.DataSourceTypeGit:
						return result.NewGitResult(&result.FileResult{Operation: "write"}, 0, nil), nil
					case model.DataSourceTypeFTP:
						return result.NewFTPResult(&result.FileResult{Operation: "write"}, 0, nil), nil
					default:
						return nil, fmt.Errorf("unexpected data source type %s", dsType)
					}
				}
				return mockConnector, nil
			}

			svc, err := newTestServiceAtTime(
				queryRepo,
				dsService,
				nil,
				connectorFactory,
				fixedNow,
			)
			require.NoError(t, err)

			execResult, err := svc.Execute(
				context.Background(),
				must.Slug("acme"),
				tc.dsSlug,
				tc.querySlug,
				model.UserEventActor(must.Slug("member-1")),
				map[string]any{
					"path":               "exports/{{runtime.run.date}}/users.json",
					sinkbinding.Input:    []any{map[string]any{"id": 1}},
					"overwrite":          true,
					"unused_runtime_arg": "ok",
				},
			)
			require.NoError(t, err)
			require.NotNil(t, execResult)
			require.NoError(t, execResult.Error())
		})
	}
}

func TestExecuteRecordsQueryExecutionEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("org-1")
	dsSlug := must.Slug("postgres-main")
	querySlug := must.Slug("get-users")
	actorSlug := must.Slug("alice")
	events := &fakeQueryEventRecorder{}

	queryRepo := &testutil.FakeQueryRepository{}
	queryRepo.FindBySlugFn = func(
		context.Context,
		postgres.DBTX,
		slug.Slug,
		slug.Slug,
		slug.Slug,
	) (*qrepo.Query, []*qrepo.QueryParameter, error) {
		return &qrepo.Query{
				Slug:           querySlug,
				Name:           "Get Users",
				DataSourceSlug: dsSlug,
				Data:           []byte(`{"statement":"SELECT id FROM users WHERE customer_id = :customer_id"}`),
			}, []*qrepo.QueryParameter{
				{
					Slug:     must.Slug("customer-id"),
					Name:     "customer_id",
					DataType: "integer",
				},
			}, nil
	}

	var executionSlug slug.Slug
	queryRepo.CreateExecutionFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug, gotQuerySlug slug.Slug,
		params *qrepo.CreateExecutionParams,
	) (*qrepo.QueryExecution, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		require.Equal(t, querySlug, gotQuerySlug)
		require.NotEmpty(t, params.Slug)
		executionSlug = params.Slug
		return &qrepo.QueryExecution{Slug: params.Slug}, nil
	}

	dsService := &fakes.DataSourceService{}
	dsService.FindBySlugFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
	) (*datasourcessvc.DataSource, error) {
		return &datasourcessvc.DataSource{
			Slug:                 dsSlug,
			Type:                 model.DataSourceTypePostgres,
			IsActive:             true,
			Config:               json.RawMessage(`{}`),
			EncryptedCredentials: mustEncryptTestCredentials(t, `{}`),
		}, nil
	}

	connectorFactory := func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			context.Context,
			json.RawMessage,
			map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult(
				"postgres",
				&result.SQLResult{
					Rows:        []map[string]any{{"id": int64(7)}, {"id": int64(8)}},
					ColumnNames: []string{"id"},
					RowCount:    2,
				},
				1,
				nil,
			), nil
		}
		return mockConnector, nil
	}

	svc, err := queriessvc.NewWithExecutionLimits(
		&testutil.FakeTransactionManager{},
		queryRepo,
		dsService,
		testConnectorExecutor{factory: connectorFactory},
		newFakeCredentialService(),
		&testutil.FakeClock{},
		testExecutionLimits(),
		eventtest.NewProducer(events),
	)
	require.NoError(t, err)

	res, err := svc.Execute(
		context.Background(),
		orgSlug,
		dsSlug,
		querySlug,
		model.UserEventActor(actorSlug),
		map[string]any{"customer_id": 123},
	)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotEmpty(t, executionSlug)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeQueryExecution, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, executionSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionExecute, got.Action)
	require.Equal(t, querySlug.String(), got.Metadata["query_slug"])
	require.Equal(t, dsSlug.String(), got.Metadata["data_source_slug"])
	require.Equal(t, "success", got.Metadata["status"])
	require.Equal(t, 2, got.Metadata["row_count"])
	require.Equal(t, []string{"customer_id"}, got.Metadata["parameter_keys"])
	require.NotContains(t, fmt.Sprint(got.Metadata), "123")
}

func TestExecuteWithoutExecutionRecording(t *testing.T) {
	t.Parallel()

	queryRepo := &testutil.FakeQueryRepository{}
	queryRepo.FindBySlugFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
		return &qrepo.Query{
				Slug: querySlug,
				Name: "Post Users",
				Data: []byte(
					`{"statement":"{\"type\":\"http\",\"method\":\"POST\",\"path\":\"/users\",\"body\":\"{{payload}}\"}"}`,
				),
				DataSourceSlug: dsSlug,
			}, []*qrepo.QueryParameter{
				{
					Slug:     must.Slug("payload"),
					Name:     "payload",
					DataType: "integer",
				},
			}, nil
	}
	queryRepo.CreateExecutionFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug, params *qrepo.CreateExecutionParams) (*qrepo.QueryExecution, error) {
		return nil, fmt.Errorf("unexpected CreateExecution call")
	}

	dsService := &fakes.DataSourceService{}
	dummyKey := make([]byte, 32)
	enc, err := crypto.NewAES256(dummyKey)
	require.NoError(t, err)

	encryptedCreds, err := enc.Encrypt([]byte(`{"token":"test"}`))
	require.NoError(t, err)

	dsService.FindBySlugFn = func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
		return &datasourcessvc.DataSource{
			Slug:                 dsSlug,
			Type:                 model.DataSourceTypeHTTP,
			IsActive:             true,
			Config:               json.RawMessage(`{"base_url":"https://example.com"}`),
			EncryptedCredentials: encryptedCreds,
		}, nil
	}

	mockConnectorFactory := func(_ context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(ctx context.Context, queryData json.RawMessage, params map[string]types.TypedParam) (*result.ExecutionResult, error) {
			require.Equal(t, model.DataSourceTypeHTTP, dsType)
			require.JSONEq(
				t,
				`{"type":"http","method":"POST","path":"/users","body":"{{payload}}"}`,
				string(queryData),
			)

			payloadParam, ok := params["payload"]
			require.True(t, ok)
			require.Equal(t, "integer", payloadParam.DataType)
			require.Equal(t, 123, payloadParam.Value)

			return result.NewHTTPResult(
				&result.HTTPResult{
					StatusCode: 202,
					Headers:    map[string]string{"X-Request-Id": "req-1"},
					Body:       json.RawMessage(`{"accepted":true}`),
				},
				1,
				nil,
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestService(queryRepo, dsService, nil, mockConnectorFactory)
	require.NoError(t, err)

	res, err := svc.Execute(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("http-main"),
		must.Slug("post-users"),
		model.UserEventActor(must.Slug("member-1")),
		map[string]any{"payload": 123},
		queriessvc.WithoutExecutionRecording(),
	)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, result.ExecutionResultTypeHTTP, res.Type())
}

func TestListExecutions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		dsSlug          slug.Slug
		querySlug       slug.Slug
		repoSetupFn     func(r *testutil.FakeQueryRepository)
		wantErrContains string
		validateFn      func(t *testing.T, executions model.ListResponse[*queriessvc.QueryExecution])
	}{
		{
			name:      "ReturnsTwoExecutionsWithExpectedSlugs",
			orgSlug:   must.Slug("org-1"),
			dsSlug:    must.Slug("postgres-main"),
			querySlug: must.Slug("get-users"),
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.ListExecutionsFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug, req model.ListRequest) ([]*qrepo.QueryExecution, error) {
					return []*qrepo.QueryExecution{
						{Slug: must.Slug("exec-1")},
						{Slug: must.Slug("exec-2")},
					}, nil
				}
			},
			validateFn: func(t *testing.T, executions model.ListResponse[*queriessvc.QueryExecution]) {
				require.Len(t, executions.Items, 2)
				assert.Equal(t, must.Slug("exec-1"), executions.Items[0].Slug)
				assert.Equal(t, must.Slug("exec-2"), executions.Items[1].Slug)
			},
		},
		{
			name:            "ReturnsWrappedErrorWhenRepoFails",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			querySlug:       must.Slug("get-users"),
			wantErrContains: "repo list executions failed",
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.ListExecutionsFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug, req model.ListRequest) ([]*qrepo.QueryExecution, error) {
					return nil, fmt.Errorf("repo list executions failed")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{}
			if tc.repoSetupFn != nil {
				tc.repoSetupFn(queryRepo)
			}

			svc, err := newTestService(queryRepo, nil, nil, nil)
			require.NoError(t, err)

			ctx := context.Background()

			executions, err := svc.ListExecutions(
				ctx,
				tc.orgSlug,
				tc.dsSlug,
				tc.querySlug,
				model.ListRequest{},
			)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), "list query executions")
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
			if tc.validateFn != nil {
				tc.validateFn(t, executions)
			}
		})
	}
}

func TestListExecutions_BackwardPagination(t *testing.T) {
	t.Parallel()

	queryRepo := &testutil.FakeQueryRepository{}
	queryRepo.ListExecutionsFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_ slug.Slug,
		_ slug.Slug,
		_ slug.Slug,
		req model.ListRequest,
	) ([]*qrepo.QueryExecution, error) {
		assert.Equal(t, model.ListOrderAsc, req.Order)
		assert.Equal(t, 3, req.Limit)

		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		return []*qrepo.QueryExecution{
			{Slug: must.Slug("exec-a"), ExecutedAt: base.Add(-2 * time.Hour)},
			{Slug: must.Slug("exec-b"), ExecutedAt: base.Add(-1 * time.Hour)},
			{Slug: must.Slug("exec-c"), ExecutedAt: base},
		}, nil
	}

	svc, err := newTestService(queryRepo, nil, nil, nil)
	require.NoError(t, err)

	result, err := svc.ListExecutions(
		context.Background(),
		must.Slug("org-1"),
		must.Slug("postgres-main"),
		must.Slug("get-users"),
		model.ListRequest{
			Limit:     2,
			Cursor:    "2026-01-01T00:00:00Z_exec-z",
			SortBy:    model.ListSortExecutedAt,
			Order:     model.ListOrderDesc,
			Direction: model.ListDirectionPrev,
		},
	)

	require.NoError(t, err)
	require.Len(t, result.Items, 2)
	assert.Equal(t, must.Slug("exec-b"), result.Items[0].Slug)
	assert.Equal(t, must.Slug("exec-a"), result.Items[1].Slug)
	assert.True(t, result.HasMore)
	assert.NotNil(t, result.NextCursor)
	assert.NotNil(t, result.PrevCursor)
}

func TestGetExecution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		dsSlug          slug.Slug
		querySlug       slug.Slug
		executionSlug   slug.Slug
		repoSetupFn     func(r *testutil.FakeQueryRepository)
		wantErrContains string
		validateFn      func(t *testing.T, execution *queriessvc.QueryExecution)
	}{
		{
			name:          "ReturnsExecutionBySlug",
			orgSlug:       must.Slug("org-1"),
			dsSlug:        must.Slug("postgres-main"),
			querySlug:     must.Slug("get-users"),
			executionSlug: must.Slug("exec-1"),
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.GetExecutionFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug, executionSlug slug.Slug) (*qrepo.QueryExecution, error) {
					return &qrepo.QueryExecution{Slug: executionSlug}, nil
				}
			},
			validateFn: func(t *testing.T, execution *queriessvc.QueryExecution) {
				require.NotNil(t, execution)
				assert.Equal(t, must.Slug("exec-1"), execution.Slug)
			},
		},
		{
			name:            "ReturnsNotFoundError",
			orgSlug:         must.Slug("org-1"),
			dsSlug:          must.Slug("postgres-main"),
			querySlug:       must.Slug("get-users"),
			executionSlug:   must.Slug("missing-exec"),
			wantErrContains: "not found",
			repoSetupFn: func(r *testutil.FakeQueryRepository) {
				r.GetExecutionFn = func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug, executionSlug slug.Slug) (*qrepo.QueryExecution, error) {
					return nil, qrepo.ErrQueryNotFound
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{}
			if tc.repoSetupFn != nil {
				tc.repoSetupFn(queryRepo)
			}

			svc, err := newTestService(queryRepo, nil, nil, nil)
			require.NoError(t, err)

			ctx := context.Background()

			execution, err := svc.GetExecution(
				ctx,
				tc.orgSlug,
				tc.dsSlug,
				tc.querySlug,
				tc.executionSlug,
			)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), "get query execution")
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
			if tc.validateFn != nil {
				tc.validateFn(t, execution)
			}
		})
	}
}

func TestGetExecutionSummary(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("org-1")
	executedAt := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		repoResult *qrepo.QueryExecutionIdentity
		repoErr    error
		want       *queriessvc.QueryExecutionIdentity
	}{
		{
			name: "ReturnsRecordedSuccessMetadata",
			repoResult: &qrepo.QueryExecutionIdentity{
				ExecutionSlug:  must.Slug("orders-exec-1"),
				DataSourceSlug: must.Slug("warehouse"),
				QuerySlug:      must.Slug("orders"),
				ExecutedAt:     executedAt,
			},
			want: &queriessvc.QueryExecutionIdentity{
				ExecutionSlug:  must.Slug("orders-exec-1"),
				DataSourceSlug: must.Slug("warehouse"),
				QuerySlug:      must.Slug("orders"),
				ExecutedAt:     executedAt,
			},
		},
		{name: "ReturnsNilWhenNoRecordedSuccess"},
		{name: "WrapsRepositoryFailure", repoErr: fmt.Errorf("repository unavailable")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryRepo := &testutil.FakeQueryRepository{
				FindLatestSuccessfulExecutionFn: func(_ context.Context, _ postgres.DBTX, gotOrg slug.Slug) (*qrepo.QueryExecutionIdentity, error) {
					require.Equal(t, orgSlug, gotOrg)
					return tc.repoResult, tc.repoErr
				},
			}
			svc, err := newTestService(queryRepo, nil, nil, nil)
			require.NoError(t, err)

			got, err := svc.GetExecutionSummary(context.Background(), orgSlug)
			if tc.repoErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), "get query execution summary")
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func (*fakeCredentialService) CreateOrganizationGitLabConnection(context.Context, credentialssvc.CreateOrganizationGitLabConnectionParams) (credentialssvc.OrganizationGitLabConnection, error) {
	return credentialssvc.OrganizationGitLabConnection{}, fmt.Errorf("unexpected organization connection call")
}

func (*fakeCredentialService) GetOrganizationGitLabConnection(context.Context, slug.Slug) (credentialssvc.OrganizationGitLabConnection, error) {
	return credentialssvc.OrganizationGitLabConnection{}, fmt.Errorf("unexpected organization connection call")
}

func (*fakeCredentialService) ListOrganizationGitLabRepositories(context.Context, slug.Slug) ([]credentialssvc.ProviderRepository, error) {
	return nil, fmt.Errorf("unexpected organization connection call")
}

func (*fakeCredentialService) AttachOrganizationGitLabConnection(context.Context, credentialssvc.AttachOrganizationGitLabConnectionParams) error {
	return fmt.Errorf("unexpected organization connection call")
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

func TestExecute_PreservesOnlySafeResourceScopeRejection(t *testing.T) {
	t.Parallel()
	sensitive := "https://private.example/secret?password=hidden"
	for _, inResult := range []bool{false, true} {
		t.Run(fmt.Sprintf("result=%t", inResult), func(t *testing.T) {
			queryRepo, dsService := safetyLimitQueryFixtures(t, []byte(`{"statement":"SELECT 1"}`))
			executor := errorConnectorExecutor{err: resourcescope.ErrInvalid.Detail(sensitive)}
			if inResult {
				executor.output = result.NewSQLResult("postgres", nil, 0, executor.err)
				executor.err = nil
			}
			svc, err := queriessvc.NewWithExecutionLimits(&testutil.FakeTransactionManager{}, queryRepo, dsService, executor, newFakeCredentialService(), &testutil.FakeClock{}, testExecutionLimits(), eventtest.NewProducer(&fakeQueryEventRecorder{}))
			require.NoError(t, err)
			_, err = svc.Execute(t.Context(), "org-1", "postgres-main", "limited-query", model.SystemEventActor(), nil)
			require.ErrorIs(t, err, resourcescope.ErrInvalid)
			require.NotErrorIs(t, err, queriessvc.ErrDataSourceUnavailable)
			require.Equal(t, "execute query: "+resourcescope.ErrInvalid.Error(), err.Error())
		})
	}
}
