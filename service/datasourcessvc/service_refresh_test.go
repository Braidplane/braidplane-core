package datasourcessvc

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
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	hubspotconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type schemaIntrospectionExecutorFunc func(context.Context, SchemaIntrospectionRequest) ([]byte, error)

func (f schemaIntrospectionExecutorFunc) IntrospectSchema(
	ctx context.Context,
	request SchemaIntrospectionRequest,
) ([]byte, error) {
	return f(ctx, request)
}

func TestIntrospectSchemaUsesResolvedProviderRepositoryPayload(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	ds := &repods.DataSource{
		Slug:   must.Slug("managed-gitlab"),
		Type:   model.DataSourceTypeGitlab,
		Config: []byte(`{"repository":{"id":"42","full_name":"acme/repo"}}`),
	}
	credentials := newFakeCredentialService()
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindOAuth2Grant,
			PlaintextJSON: []byte(`{"access_token":"runtime-token"}`),
		}, nil
	}
	credentials.resolveRepositoryFn = func(_ context.Context, params credentialssvc.ResolveProviderRepositoryParams) (credentialssvc.ProviderRepository, error) {
		require.Equal(t, "42", params.RepositoryID)
		return credentialssvc.ProviderRepository{
			ID:            "42",
			CloneURL:      "https://gitlab.com/acme/repo.git",
			DefaultBranch: "main",
		}, nil
	}

	emptySchema, err := emptySchemaForType(model.DataSourceTypeGitlab)
	require.NoError(t, err)
	encodedSchema, err := json.Marshal(emptySchema)
	require.NoError(t, err)
	var request SchemaIntrospectionRequest
	svc := &service{
		credentials: credentials,
		schemaIntrospector: schemaIntrospectionExecutorFunc(func(_ context.Context, got SchemaIntrospectionRequest) ([]byte, error) {
			request = got
			return encodedSchema, nil
		}),
		schemaMaxBytes: 1024,
		schemaMaxItems: 10,
	}

	_, err = svc.introspectSchema(t.Context(), orgSlug, ds)
	require.NoError(t, err)
	require.Equal(t, model.DataSourceTypeGitlab, request.DataSourceType)
	require.Equal(t, orgSlug, request.OrganizationSlug)
	require.Equal(t, ds.Slug, request.DataSourceSlug)
	assert.JSONEq(t, `{"repo_url":"https://gitlab.com/acme/repo.git","default_branch":"main"}`, string(request.ConfigJSON))
	assert.JSONEq(t, `{"auth_type":"https","username":"oauth2","token":"runtime-token"}`, string(request.CredentialsJSON))
	assert.JSONEq(t, `{"repository":{"id":"42","full_name":"acme/repo"}}`, string(ds.Config))
}

func TestIntrospectSchemaMarksUnavailableWhenProviderRepositoryResolutionFails(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	ds := &repods.DataSource{
		Slug:                   must.Slug("managed-gitlab"),
		Type:                   model.DataSourceTypeGitlab,
		Config:                 []byte(`{"repository":{"id":"42","full_name":"acme/repo"}}`),
		ConnectionTestRevision: 7,
	}
	credentials := newFakeCredentialService()
	credentials.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{
			Kind:          credentialssvc.CredentialKindOAuth2Grant,
			PlaintextJSON: []byte(`{"access_token":"runtime-token"}`),
		}, nil
	}
	repositoryErr := fmt.Errorf("provider repository unavailable")
	credentials.resolveRepositoryFn = func(context.Context, credentialssvc.ResolveProviderRepositoryParams) (credentialssvc.ProviderRepository, error) {
		return credentialssvc.ProviderRepository{}, repositoryErr
	}
	repo := testutil.NewFakeDataSourceRepo()
	markCalls := 0
	repo.MarkTestFailureFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
		gotRevision int64,
		_ string,
		_ json.RawMessage,
	) (*repods.DataSource, error) {
		markCalls++
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, ds.Slug, gotDSSlug)
		require.Equal(t, ds.ConnectionTestRevision, gotRevision)
		return ds, nil
	}
	svc := &service{
		txm:         testutil.FakeTransactionManager{},
		repo:        repo,
		credentials: credentials,
	}

	_, err := svc.introspectSchema(t.Context(), orgSlug, ds)
	require.ErrorIs(t, err, repositoryErr)
	assert.Equal(t, 1, markCalls)
}

func TestRefreshSchema_EnqueuesSchemaImportWithoutConnectorIntrospection(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("prod-db")
	configJSON := []byte(`{"host":"localhost","port":5432,"database":"app"}`)
	actorSlug := must.Slug("alice")

	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
	) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.DataSource{
			Slug:                 dsSlug,
			Name:                 "Production DB",
			Type:                 model.DataSourceTypePostgres,
			Config:               configJSON,
			EncryptedCredentials: []byte(`{"username":"admin","password":"secret"}`),
		}, nil
	}

	schemaRepo := testutil.NewFakeSchemaMetadataRepo()

	connectorCalls := 0
	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(
		_ context.Context,
		_ model.DataSourceType,
		_, _ []byte,
	) (connector.Connector, error) {
		connectorCalls++
		return testutil.NewFakeSQLConnector(), nil
	}

	producer := &fakeDataSourceSchemaImportProducer{}
	svc := newTestService(repo, schemaRepo, testutil.NewFakeEncryptor(), cf.NewConnectorFn).(*service)
	svc.schemaImportProducer = producer

	result, err := svc.RefreshSchema(
		context.Background(),
		orgSlug,
		dsSlug,
		model.UserEventActor(actorSlug),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, model.DataSourceSchemaImportStatusImporting, result.Status)
	assert.True(t, result.Enqueued)
	require.NotNil(t, result.Job)
	assert.Equal(t, "data_source_schema_import", result.Job.Kind)
	assert.Equal(t, jobs.DataSourceSchemaImportQueue, result.Job.Queue)
	assert.Equal(t, "queued", result.Job.State)
	assert.Zero(t, connectorCalls)
	require.Len(t, producer.calls, 1)
	assert.Equal(t, orgSlug, producer.calls[0].OrganizationSlug)
	assert.Equal(t, dsSlug, producer.calls[0].DataSourceSlug)
	assert.NotEmpty(t, producer.calls[0].AttemptID)
	assert.Equal(t, model.EventActorTypeUser, producer.calls[0].Actor.Type)
	require.NotNil(t, producer.calls[0].Actor.Slug)
	assert.Equal(t, actorSlug, *producer.calls[0].Actor.Slug)
	assert.False(t, producer.calls[0].RequestedAt.IsZero())
}

func TestRefreshSchema_CoalescesDuplicateInFlightImport(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("prod-db")

	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:     dsSlug,
			Name:     "Production DB",
			Type:     model.DataSourceTypePostgres,
			Config:   []byte(`{"host":"localhost","port":5432,"database":"app"}`),
			IsActive: true,
		}, nil
	}

	schemaRepo := testutil.NewFakeSchemaMetadataRepo()
	schemaRepo.StartImportBySlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
		attemptID string,
		_ time.Time,
	) (*repods.SchemaImportState, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		require.NotEmpty(t, attemptID)
		return &repods.SchemaImportState{
			DataSourceSlug: dsSlug,
			Status:         model.DataSourceSchemaImportStatusImporting,
			Started:        false,
		}, nil
	}

	producer := &fakeDataSourceSchemaImportProducer{
		err: assert.AnError,
	}
	svc := newTestService(
		repo,
		schemaRepo,
		testutil.NewFakeEncryptor(),
		testutil.NewFakeConnectorFactory().NewConnectorFn,
	).(*service)
	svc.schemaImportProducer = producer

	result, err := svc.RefreshSchema(
		context.Background(),
		orgSlug,
		dsSlug,
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, model.DataSourceSchemaImportStatusImporting, result.Status)
	assert.False(t, result.Enqueued)
	assert.Nil(t, result.Job)
	assert.Empty(t, producer.calls)
}

func TestExecuteQueuedSchemaImport_CachesFreshConnectorSchema(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("prod-db")
	attemptID := "attempt-success"
	configJSON := []byte(`{"host":"localhost","port":5432,"database":"app"}`)
	credentialsJSON := []byte(`{"username":"admin","password":"secret"}`)

	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
	) (*repods.DataSource, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		return &repods.DataSource{
			Slug:                 dsSlug,
			Name:                 "Production DB",
			Type:                 model.DataSourceTypePostgres,
			Config:               configJSON,
			EncryptedCredentials: credentialsJSON,
		}, nil
	}

	schemaRepo := testutil.NewFakeSchemaMetadataRepo()
	var upsertedSchema json.RawMessage
	var markedReady bool
	schemaRepo.UpsertByDataSourceSlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
		params *repods.UpsertSchemaMetadataParams,
	) (*repods.SchemaMetadata, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		upsertedSchema = append(json.RawMessage(nil), params.SchemaData...)
		return &repods.SchemaMetadata{SchemaData: params.SchemaData}, nil
	}
	schemaRepo.MarkImportReadyBySlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
		gotAttemptID string,
		finishedAt time.Time,
	) (*repods.SchemaImportState, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		require.Equal(t, attemptID, gotAttemptID)
		require.False(t, finishedAt.IsZero())
		markedReady = true
		return &repods.SchemaImportState{
			DataSourceSlug: dsSlug,
			Status:         model.DataSourceSchemaImportStatusReady,
			FinishedAt:     &finishedAt,
		}, nil
	}

	connectorCalls := 0
	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(
		_ context.Context,
		gotType model.DataSourceType,
		gotConfigJSON, gotCredentialsJSON []byte,
	) (connector.Connector, error) {
		connectorCalls++
		require.Equal(t, model.DataSourceTypePostgres, gotType)
		require.JSONEq(t, string(configJSON), string(gotConfigJSON))
		require.JSONEq(t, string(credentialsJSON), string(gotCredentialsJSON))

		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.IntrospectSchemaFn = func(context.Context) (*sqlconnector.Schema, error) {
			return &sqlconnector.Schema{
				Tables: []sqlconnector.Table{
					{
						Name: "users",
						Columns: []sqlconnector.Column{
							{Name: "id", Type: "integer"},
						},
					},
				},
			}, nil
		}
		return mockConnector, nil
	}

	svc := newTestService(repo, schemaRepo, testutil.NewFakeEncryptor(), cf.NewConnectorFn)

	result, err := svc.ExecuteQueuedSchemaImport(
		context.Background(),
		jobs.DataSourceSchemaImportArgs{
			OrganizationSlug: orgSlug,
			DataSourceSlug:   dsSlug,
			AttemptID:        attemptID,
			Actor:            model.UserEventActor(must.Slug("alice")),
		},
	)
	require.NoError(t, err)
	require.Equal(t, model.DataSourceSchemaImportStatusReady, result.Status)
	require.Equal(t, 1, connectorCalls)
	require.NotNil(t, upsertedSchema)
	require.True(t, markedReady)

	decoded, err := decodeSchemaJSON(upsertedSchema)
	require.NoError(t, err)
	actual, ok := decoded.(Schema[SQLSchemaPayload])
	require.True(t, ok)
	assert.Equal(t, SchemaTypeSQL, actual.SchemaTypeValue)
	assert.Equal(t, model.DataSourceTypePostgres.String(), actual.Type)
	require.Len(t, actual.Payload.Tables, 1)
	assert.Equal(t, "users", actual.Payload.Tables[0].Name)
}

func TestExecuteQueuedSchemaImport_CachesFileBackedConnectorSchema(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("repo-source")
	attemptID := "attempt-file"

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
			Name:   "Repository Source",
			Type:   model.DataSourceTypeGit,
			Config: []byte(`{"repo_url":"https://example.com/acme/orders.git","default_branch":"main"}`),
		}, nil
	}

	schemaRepo := testutil.NewFakeSchemaMetadataRepo()
	var upsertedSchema json.RawMessage
	schemaRepo.UpsertByDataSourceSlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
		params *repods.UpsertSchemaMetadataParams,
	) (*repods.SchemaMetadata, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		upsertedSchema = append(json.RawMessage(nil), params.SchemaData...)
		return &repods.SchemaMetadata{SchemaData: params.SchemaData}, nil
	}

	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return fakeFileIntrospectorConnector{
			schema: &filedata.Schema{
				Files: []filedata.File{{Name: "nested/report.pdf", MIMEType: "application/pdf", SizeBytes: new(int64(42)), Fields: []filedata.Field{}}, {
					Name: "orders.csv",
					Fields: []filedata.Field{
						{Name: "order_id", Type: "string"},
						{Name: "total", Type: "number"},
					},
				}},
			},
		}, nil
	}

	svc := newTestService(repo, schemaRepo, testutil.NewFakeEncryptor(), cf.NewConnectorFn)

	result, err := svc.ExecuteQueuedSchemaImport(
		context.Background(),
		jobs.DataSourceSchemaImportArgs{
			OrganizationSlug: orgSlug,
			DataSourceSlug:   dsSlug,
			AttemptID:        attemptID,
			Actor:            model.UserEventActor(must.Slug("alice")),
		},
	)
	require.NoError(t, err)
	require.Equal(t, model.DataSourceSchemaImportStatusReady, result.Status)
	require.NotNil(t, upsertedSchema)

	decoded, err := decodeSchemaJSON(upsertedSchema)
	require.NoError(t, err)
	actual, ok := decoded.(Schema[FileSchemaPayload])
	require.True(t, ok)
	assert.Equal(t, SchemaTypeFile, actual.SchemaTypeValue)
	assert.Equal(t, model.DataSourceTypeGit.String(), actual.Type)
	require.Len(t, actual.Payload.Files, 2)
	require.Equal(t, "nested/report.pdf", actual.Payload.Files[0].Name)
	require.Empty(t, actual.Payload.Files[0].Fields)
	require.Equal(t, "application/pdf", actual.Payload.Files[0].MIMEType)
	require.Equal(t, int64(42), *actual.Payload.Files[0].SizeBytes)
	assert.Equal(t, "orders.csv", actual.Payload.Files[1].Name)
	require.Len(t, actual.Payload.Files[1].Fields, 2)
	assert.Equal(t, "order_id", actual.Payload.Files[1].Fields[0].Name)
}

func TestExecuteQueuedSchemaImport_CreatesSourceDriftReportForChangedCachedSchema(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("prod-db")
	attemptID := "attempt-drift"
	configJSON := []byte(`{"host":"localhost","port":5432,"database":"app"}`)
	credentialsJSON := []byte(`{"username":"admin","password":"secret"}`)
	previousSchema := Schema[SQLSchemaPayload]{
		SchemaTypeValue: SchemaTypeSQL,
		Type:            model.DataSourceTypePostgres.String(),
		Payload: SQLSchemaPayload{
			Tables: []SchemaTable{{
				Name: "users",
				Columns: []SchemaColumn{
					{Name: "id", Type: "integer"},
					{Name: "email", Type: "text"},
				},
			}},
		},
	}
	previousSchemaJSON, err := json.Marshal(previousSchema)
	require.NoError(t, err)

	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_, _ slug.Slug,
	) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:                 dsSlug,
			Name:                 "Production DB",
			Type:                 model.DataSourceTypePostgres,
			Config:               configJSON,
			EncryptedCredentials: credentialsJSON,
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
		return &repods.SchemaMetadata{SchemaData: previousSchemaJSON}, nil
	}
	schemaRepo.UpsertByDataSourceSlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_ slug.Slug,
		_ slug.Slug,
		params *repods.UpsertSchemaMetadataParams,
	) (*repods.SchemaMetadata, error) {
		return &repods.SchemaMetadata{SchemaData: params.SchemaData}, nil
	}

	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.IntrospectSchemaFn = func(context.Context) (*sqlconnector.Schema, error) {
			return &sqlconnector.Schema{
				Tables: []sqlconnector.Table{{
					Name: "users",
					Columns: []sqlconnector.Column{
						{Name: "id", Type: "integer"},
					},
				}},
			}, nil
		}
		return mockConnector, nil
	}

	reports := &fakeDriftImpactRepository{}
	svc := newTestService(repo, schemaRepo, testutil.NewFakeEncryptor(), cf.NewConnectorFn).(*service)
	svc.driftReports = reports

	result, err := svc.ExecuteQueuedSchemaImport(
		context.Background(),
		jobs.DataSourceSchemaImportArgs{
			OrganizationSlug: orgSlug,
			DataSourceSlug:   dsSlug,
			AttemptID:        attemptID,
			Actor:            model.UserEventActor(must.Slug("alice")),
		},
	)
	require.NoError(t, err)
	require.Equal(t, model.DataSourceSchemaImportStatusReady, result.Status)
	require.Equal(t, 1, reports.supersedeOpenCalls)
	require.Equal(t, 1, reports.createCalls)
	require.Equal(t, "source_schema", reports.created.ReportType.String())
	require.Equal(t, "breaking", reports.created.Severity.String())
	require.Equal(t, dsSlug, *reports.created.DataSourceSlug)
	require.JSONEq(
		t,
		`[{"kind":"removal","path":"tables.users.email","before":"text(nullable=false)","after":"<none>","severity":"breaking","reason_code":"removal"}]`,
		string(reports.created.ChangesJSONB),
	)
}

func TestExecuteQueuedSchemaImport_SkipsStaleSuccessAttempt(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("prod-db")
	attemptID := "attempt-stale-success"

	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:                 dsSlug,
			Name:                 "Production DB",
			Type:                 model.DataSourceTypePostgres,
			Config:               []byte(`{"host":"localhost","port":5432,"database":"app"}`),
			EncryptedCredentials: []byte(`{"username":"admin","password":"secret"}`),
		}, nil
	}

	schemaRepo := testutil.NewFakeSchemaMetadataRepo()
	schemaRepo.FindCurrentAttemptFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
		gotAttemptID string,
	) (*repods.SchemaImportState, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		require.Equal(t, attemptID, gotAttemptID)
		return nil, repods.ErrSchemaImportAttemptNotCurrent
	}
	schemaRepo.UpsertByDataSourceSlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.UpsertSchemaMetadataParams) (*repods.SchemaMetadata, error) {
		t.Fatal("stale schema import success must not write cache metadata")
		return nil, assert.AnError
	}
	schemaRepo.MarkImportReadyBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, string, time.Time) (*repods.SchemaImportState, error) {
		t.Fatal("stale schema import success must not mark ready")
		return nil, assert.AnError
	}

	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.IntrospectSchemaFn = func(context.Context) (*sqlconnector.Schema, error) {
			return &sqlconnector.Schema{Tables: []sqlconnector.Table{{Name: "users"}}}, nil
		}
		return mockConnector, nil
	}

	svc := newTestService(repo, schemaRepo, testutil.NewFakeEncryptor(), cf.NewConnectorFn)

	result, err := svc.ExecuteQueuedSchemaImport(
		context.Background(),
		jobs.DataSourceSchemaImportArgs{
			OrganizationSlug: orgSlug,
			DataSourceSlug:   dsSlug,
			AttemptID:        attemptID,
			Actor:            model.UserEventActor(must.Slug("alice")),
		},
	)
	require.NoError(t, err)
	require.True(t, result.Skipped)
}

func TestRefreshSchema_ReturnsRepositoryErrors(t *testing.T) {
	t.Parallel()

	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_, _ slug.Slug,
	) (*repods.DataSource, error) {
		return nil, repods.ErrDataSourceNotFound
	}

	svc := newTestService(
		repo,
		testutil.NewFakeSchemaMetadataRepo(),
		testutil.NewFakeEncryptor(),
		testutil.NewFakeConnectorFactory().NewConnectorFn,
	)

	result, err := svc.RefreshSchema(
		context.Background(),
		must.Slug("acme-corp"),
		must.Slug("missing"),
		model.UserEventActor(must.Slug("alice")),
	)
	require.Error(t, err)
	require.ErrorIs(t, err, repods.ErrDataSourceNotFound)
	assert.Nil(t, result)
}

func TestExecuteQueuedSchemaImport_RecordsSafeFailureDiagnostics(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("customer-api")
	attemptID := "attempt-failure"

	tests := []struct {
		name           string
		dataSourceType model.DataSourceType
		gotErr         error
		wantCode       string
		wantMessage    string
	}{
		{
			name:        "InvalidSchemaURL",
			gotErr:      ErrInvalidSchemaURL.WithPublicDetail("OpenAPI URL failed safety validation"),
			wantCode:    ErrInvalidSchemaURL.Code(),
			wantMessage: "OpenAPI URL failed safety validation",
		},
		{
			name:        "SchemaFetchFailed",
			gotErr:      ErrSchemaFetchFailed.WithPublicDetail("failed to fetch OpenAPI spec"),
			wantCode:    ErrSchemaFetchFailed.Code(),
			wantMessage: "failed to fetch OpenAPI spec",
		},
		{
			name:        "InvalidOpenAPI",
			gotErr:      ErrInvalidOpenAPI.WithPublicDetail("failed to parse OpenAPI spec"),
			wantCode:    ErrInvalidOpenAPI.Code(),
			wantMessage: "failed to parse OpenAPI spec",
		},
		{
			name:        "GenericConnectorErrorIsSanitized",
			gotErr:      fmt.Errorf("provider returned token=secret-token-123"),
			wantCode:    "DATA_SOURCE_CONNECTION_FAILED",
			wantMessage: "data source connection failed",
		},
		{
			name:           "HubSpotProviderFailureIsSanitized",
			dataSourceType: model.DataSourceTypeHubSpot,
			gotErr:         fmt.Errorf("HubSpot response included access_token=secret-hubspot-token"),
			wantCode:       "DATA_SOURCE_CONNECTION_FAILED",
			wantMessage:    "data source connection failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			repo.FindBySlugFn = func(
				_ context.Context,
				_ postgres.DBTX,
				gotOrgSlug, gotDSSlug slug.Slug,
			) (*repods.DataSource, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, dsSlug, gotDSSlug)
				dataSourceType := tc.dataSourceType
				if dataSourceType == model.DataSourceTypeUnknown {
					dataSourceType = model.DataSourceTypeHTTP
				}
				return &repods.DataSource{
					Slug:                 dsSlug,
					Name:                 "Customer API",
					Type:                 dataSourceType,
					Config:               []byte(`{"base_url":"https://api.example.com","openapi_url":"https://api.example.com/openapi.yml"}`),
					EncryptedCredentials: []byte(`{"bearer_token":"secret"}`),
				}, nil
			}

			schemaRepo := testutil.NewFakeSchemaMetadataRepo()
			schemaRepo.UpsertByDataSourceSlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.UpsertSchemaMetadataParams) (*repods.SchemaMetadata, error) {
				t.Fatal("schema diagnostics must not write cache entries")
				return nil, assert.AnError
			}
			var failureParams *repods.MarkSchemaImportFailedParams
			schemaRepo.MarkImportFailedBySlugFn = func(
				_ context.Context,
				_ postgres.DBTX,
				gotOrgSlug, gotDSSlug slug.Slug,
				params *repods.MarkSchemaImportFailedParams,
			) (*repods.SchemaImportState, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, dsSlug, gotDSSlug)
				require.Equal(t, attemptID, params.AttemptID)
				failureParams = params
				return &repods.SchemaImportState{
					DataSourceSlug: dsSlug,
					Status:         model.DataSourceSchemaImportStatusFailed,
					Diagnostic: &repods.SchemaImportDiagnostic{
						Code:    params.ErrorCode,
						Message: params.ErrorMessage,
					},
					FinishedAt: &params.FinishedAt,
				}, nil
			}

			cf := testutil.NewFakeConnectorFactory()
			cf.NewConnectorFn = func(_ context.Context, dataSourceType model.DataSourceType, _, _ []byte) (connector.Connector, error) {
				if dataSourceType == model.DataSourceTypeHubSpot {
					return fakeHubSpotIntrospectorConnector{err: tc.gotErr}, nil
				}
				return fakeHTTPIntrospectorConnector{
					introspectFn: func(context.Context) (*httpconnector.HTTPSchema, error) {
						return nil, tc.gotErr
					},
				}, nil
			}

			svc := newTestService(repo, schemaRepo, testutil.NewFakeEncryptor(), cf.NewConnectorFn)

			result, err := svc.ExecuteQueuedSchemaImport(
				context.Background(),
				jobs.DataSourceSchemaImportArgs{
					OrganizationSlug: orgSlug,
					DataSourceSlug:   dsSlug,
					AttemptID:        attemptID,
					Actor:            model.UserEventActor(must.Slug("alice")),
				},
			)
			require.NoError(t, err)
			require.Equal(t, model.DataSourceSchemaImportStatusFailed, result.Status)
			require.Equal(t, tc.wantCode, result.Diagnostic.Code)
			require.Equal(t, tc.wantMessage, result.Diagnostic.Message)
			require.NotNil(t, failureParams)
			assert.Equal(t, tc.wantCode, failureParams.ErrorCode)
			assert.Equal(t, tc.wantMessage, failureParams.ErrorMessage)
			assert.NotContains(t, failureParams.ErrorMessage, "secret-token-123")
			assert.False(t, failureParams.FinishedAt.IsZero())
		})
	}
}

func TestExecuteQueuedSchemaImport_SkipsStaleFailureAttempt(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("customer-api")
	attemptID := "attempt-stale-failure"

	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{
			Slug:                 dsSlug,
			Name:                 "Customer API",
			Type:                 model.DataSourceTypeHTTP,
			Config:               []byte(`{"base_url":"https://api.example.com","openapi_url":"https://api.example.com/openapi.yml"}`),
			EncryptedCredentials: []byte(`{"bearer_token":"secret"}`),
		}, nil
	}

	schemaRepo := testutil.NewFakeSchemaMetadataRepo()
	schemaRepo.MarkImportFailedBySlugFn = func(
		_ context.Context,
		_ postgres.DBTX,
		gotOrgSlug, gotDSSlug slug.Slug,
		params *repods.MarkSchemaImportFailedParams,
	) (*repods.SchemaImportState, error) {
		require.Equal(t, orgSlug, gotOrgSlug)
		require.Equal(t, dsSlug, gotDSSlug)
		require.Equal(t, attemptID, params.AttemptID)
		return nil, repods.ErrSchemaImportAttemptNotCurrent
	}

	cf := testutil.NewFakeConnectorFactory()
	cf.NewConnectorFn = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return fakeHTTPIntrospectorConnector{
			introspectFn: func(context.Context) (*httpconnector.HTTPSchema, error) {
				return nil, ErrInvalidOpenAPI.WithPublicDetail("failed to parse OpenAPI spec")
			},
		}, nil
	}

	svc := newTestService(repo, schemaRepo, testutil.NewFakeEncryptor(), cf.NewConnectorFn)

	result, err := svc.ExecuteQueuedSchemaImport(
		context.Background(),
		jobs.DataSourceSchemaImportArgs{
			OrganizationSlug: orgSlug,
			DataSourceSlug:   dsSlug,
			AttemptID:        attemptID,
			Actor:            model.UserEventActor(must.Slug("alice")),
		},
	)
	require.NoError(t, err)
	require.True(t, result.Skipped)
}

type fakeFileIntrospectorConnector struct {
	schema *filedata.Schema
	err    error
}

type fakeHubSpotIntrospectorConnector struct {
	err error
}

func (f fakeHubSpotIntrospectorConnector) Execute(
	context.Context,
	json.RawMessage,
	map[string]types.TypedParam,
	...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	return &result.ExecutionResult{}, nil
}

func (f fakeHubSpotIntrospectorConnector) TestConnection(context.Context) error { return nil }
func (f fakeHubSpotIntrospectorConnector) Close() error                         { return nil }

func (f fakeHubSpotIntrospectorConnector) IntrospectSchema(context.Context) (*hubspotconnector.Schema, error) {
	return nil, f.err
}

func (f fakeFileIntrospectorConnector) Execute(
	context.Context,
	json.RawMessage,
	map[string]types.TypedParam,
	...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	return &result.ExecutionResult{}, nil
}

func (f fakeFileIntrospectorConnector) TestConnection(context.Context) error {
	return nil
}

func (f fakeFileIntrospectorConnector) Close() error {
	return nil
}

func (f fakeFileIntrospectorConnector) IntrospectSchema(ctx context.Context) (*filedata.Schema, error) {
	_ = ctx
	if f.err != nil {
		return nil, f.err
	}
	if f.schema != nil {
		return f.schema, nil
	}
	return &filedata.Schema{Files: []filedata.File{}}, nil
}

type fakeDriftImpactRepository struct {
	supersedeOpenCalls int
	createCalls        int
	created            *driftimpactrepo.Report
}

func (f *fakeDriftImpactRepository) SupersedeOpen(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	driftimpactrepo.CreateParams,
) error {
	f.supersedeOpenCalls++
	return nil
}

func (f *fakeDriftImpactRepository) Create(
	_ context.Context,
	_ postgres.DBTX,
	_ slug.Slug,
	params driftimpactrepo.CreateParams,
) (*driftimpactrepo.Report, error) {
	f.createCalls++
	f.created = &driftimpactrepo.Report{
		Slug:           params.Slug,
		ReportType:     params.ReportType,
		Status:         params.Status,
		Severity:       params.Severity,
		Summary:        params.Summary,
		ChangesJSONB:   params.ChangesJSONB,
		ImpactsJSONB:   params.ImpactsJSONB,
		ProjectSlug:    params.ProjectSlug,
		PipelineSlug:   params.PipelineSlug,
		DataSourceSlug: params.DataSourceSlug,
	}
	return f.created, nil
}

func (f *fakeDriftImpactRepository) FindBySlug(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*driftimpactrepo.Report, error) {
	return nil, assert.AnError
}

func (f *fakeDriftImpactRepository) List(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	driftimpactrepo.ListFilters,
) ([]*driftimpactrepo.Report, error) {
	return nil, assert.AnError
}

func TestIntrospectSchemaReturnsCachedGitFamilyFilesWithoutConnector(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	dsSlug := must.Slug("prod-db")
	for _, vendor := range []model.DataSourceType{model.DataSourceTypeGit, model.DataSourceTypeGithub, model.DataSourceTypeGitlab} {
		t.Run(vendor.String(), func(t *testing.T) {
			cachedSchema := Schema[FileSchemaPayload]{SchemaTypeValue: SchemaTypeFile, Type: vendor.String(), Payload: FileSchemaPayload{Files: []SchemaFile{{Name: "nested/report.pdf", MIMEType: "application/pdf", Fields: []SchemaField{}}}}}
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

			actual, ok := result.(Schema[FileSchemaPayload])
			require.True(t, ok)
			assert.Equal(t, cachedSchema, actual)
		})
	}
}
