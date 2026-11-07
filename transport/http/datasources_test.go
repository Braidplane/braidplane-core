package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

// unsupportedSchemaResult forces schemaResultToHTTP through its unsupported-type path.
type unsupportedSchemaResult struct{}

// SchemaType reports an unsupported schema family for mapper failure coverage.
func (unsupportedSchemaResult) SchemaType() datasourcessvc.SchemaType {
	return datasourcessvc.SchemaTypeUnknown
}

// VendorType reports a placeholder vendor for mapper failure coverage.
func (unsupportedSchemaResult) VendorType() string {
	return "unsupported"
}

func TestCreateDataSource(t *testing.T) {
	t.Parallel()

	testDS := &datasourcessvc.DataSource{
		Slug:     must.Slug("prod-db"),
		Name:     "Production Database",
		Type:     model.DataSourceTypePostgres,
		Config:   json.RawMessage(`{"host":"localhost","port":5432,"database":"prod"}`),
		IsActive: true,
	}

	testMySQLDS := &datasourcessvc.DataSource{
		Slug:     must.Slug("mysql-db"),
		Name:     "MySQL Database",
		Type:     model.DataSourceTypeMySQL,
		Config:   json.RawMessage(`{"host":"mysql.local","port":3306,"database":"app"}`),
		IsActive: true,
	}

	testHTTPDS := &datasourcessvc.DataSource{
		Slug:     must.Slug("api-service"),
		Name:     "API Service",
		Type:     model.DataSourceTypeHTTP,
		Config:   json.RawMessage(`{"base_url":"https://api.example.com"}`),
		IsActive: true,
	}

	testHubSpotDS := &datasourcessvc.DataSource{
		Slug:     must.Slug("hubspot-crm"),
		Name:     "HubSpot CRM",
		Type:     model.DataSourceTypeHubSpot,
		Config:   json.RawMessage(`{"timeout_seconds":15}`),
		IsActive: true,
		ReadOnly: true,
	}

	testMongoDS := &datasourcessvc.DataSource{
		Slug:     must.Slug("mongo-db"),
		Name:     "MongoDB Database",
		Type:     model.DataSourceTypeMongoDB,
		Config:   json.RawMessage(`{"host":"localhost","port":27017,"database":"app"}`),
		IsActive: true,
	}

	testS3DS := &datasourcessvc.DataSource{
		Slug:     must.Slug("s3-fixtures"),
		Name:     "S3 Fixtures",
		Type:     model.DataSourceTypeS3,
		Config:   json.RawMessage(`{"region":"us-east-1","bucket":"braidplane-fixtures","prefix":"fixtures"}`),
		IsActive: true,
	}

	var createReq api.CreateDataSourceRequest
	must.FnErr(createReq.FromCreatePostgresDataSourceRequest(api.CreatePostgresDataSourceRequest{
		Name: "Production Database",
		Slug: apiSlugValuePtr("prod-db"),
		Config: api.PostgresConfig{
			Host:     "localhost",
			Port:     5432,
			Database: "prod",
		},
		Credentials: api.PostgresCredentials{
			Username: "postgres",
			Password: "secret",
		},
	}))

	tests := []struct {
		name       string
		body       any
		createFn   func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, params *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
		wantBody   *api.DataSource
	}{
		{
			name: "ClientCannotAssignDemoProvenance",
			body: map[string]any{
				"type": "postgres", "name": "Payment Ledger", "slug": "prod-db", "system_provided_demo": true,
				"config":      map[string]any{"host": "db.example", "port": 5432, "database": "app", "ssl_mode": "verify-full"},
				"credentials": map[string]any{"username": "test", "password": "test-only"},
			},
			createFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, params *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
				encoded, err := json.Marshal(params)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "SystemProvidedDemo")
				require.NotContains(t, string(encoded), "system_provided_demo")
				return testDS, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   new(dataSourceToHTTP(testDS)),
		},
		{
			name: "CreatesPostgresDataSourceAndReturns201",
			body: createReq,
			createFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
				return testDS, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   new(dataSourceToHTTP(testDS)),
		},
		{
			name: "CreatesPasswordlessMySQLDataSourceAndReturns201",
			body: func() api.CreateDataSourceRequest {
				var req api.CreateDataSourceRequest
				must.FnErr(req.FromCreateMySQLDataSourceRequest(api.CreateMySQLDataSourceRequest{
					Name: "MySQL Database",
					Slug: apiSlugValuePtr("mysql-db"),
					Config: api.MySQLConfig{
						Host:     "mysql.local",
						Port:     3306,
						Database: "app",
					},
					Credentials: api.MySQLCredentials{
						Username: "mysql",
						Password: "",
					},
				}))
				return req
			}(),
			createFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, params *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
				require.Equal(t, map[string]any{"username": "mysql", "password": ""}, params.Credentials)
				return testMySQLDS, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   new(dataSourceToHTTP(testMySQLDS)),
		},
		{
			name: "CreatesHTTPDataSourceAndReturns201",
			body: func() api.CreateDataSourceRequest {
				var req api.CreateDataSourceRequest
				must.FnErr(req.FromCreateHTTPDataSourceRequest(api.CreateHTTPDataSourceRequest{
					Name: "API Service",
					Slug: apiSlugValuePtr("api-service"),
					Config: api.HTTPConfig{
						BaseUrl: "https://api.example.com",
					},
					Credentials: api.HTTPCredentials{
						BearerToken: new("token123"),
					},
				}))
				return req
			}(),
			createFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
				return testHTTPDS, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   new(dataSourceToHTTP(testHTTPDS)),
		},
		{
			name: "CreatesHubSpotDataSourceAndReturns201",
			body: func() api.CreateDataSourceRequest {
				var req api.CreateDataSourceRequest
				timeoutSeconds := 15
				must.FnErr(req.FromCreateHubSpotDataSourceRequest(api.CreateHubSpotDataSourceRequest{
					Name: "HubSpot CRM",
					Slug: apiSlugValuePtr("hubspot-crm"),
					Config: api.HubSpotConfig{
						TimeoutSeconds: &timeoutSeconds,
					},
					Credentials: api.HubSpotCredentials{
						PrivateAppToken: "pat-na1-test-token",
					},
				}))
				return req
			}(),
			createFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, params *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
				require.Equal(t, model.DataSourceTypeHubSpot, params.Type)
				require.JSONEq(t, `{"timeout_seconds":15}`, string(params.ConfigJSON))
				require.Equal(t, "pat-na1-test-token", params.Credentials["private_app_token"])
				require.True(t, params.ReadOnly)
				return testHubSpotDS, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   new(dataSourceToHTTP(testHubSpotDS)),
		},
		{
			name: "CreatesMongoDBDataSourceAndReturns201",
			body: func() api.CreateDataSourceRequest {
				var req api.CreateDataSourceRequest
				must.FnErr(
					req.FromCreateMongoDBDataSourceRequest(api.CreateMongoDBDataSourceRequest{
						Name: "MongoDB Database",
						Slug: apiSlugValuePtr("mongo-db"),
						Config: api.MongoDBConfig{
							Host:     "localhost",
							Port:     27017,
							Database: "app",
						},
						Credentials: api.MongoDBCredentials{
							Username: "mongo",
							Password: "secret",
						},
					}),
				)
				return req
			}(),
			createFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
				return testMongoDS, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   new(dataSourceToHTTP(testMongoDS)),
		},
		{
			name: "CreatesS3DataSourceAndReturns201",
			body: func() api.CreateDataSourceRequest {
				var req api.CreateDataSourceRequest
				forcePathStyle := true
				allowPrivateHosts := true
				timeoutSeconds := 5
				endpoint := "http://localhost:39000"
				prefix := "fixtures"
				accessKeyID := "braidplane"
				secretAccessKey := "braidplanepass"
				must.FnErr(req.FromCreateS3DataSourceRequest(api.CreateS3DataSourceRequest{
					Name: "S3 Fixtures",
					Slug: apiSlugValuePtr("s3-fixtures"),
					Config: api.S3Config{
						Endpoint:          &endpoint,
						Region:            "us-east-1",
						Bucket:            "braidplane-fixtures",
						Prefix:            &prefix,
						ForcePathStyle:    &forcePathStyle,
						TimeoutSeconds:    &timeoutSeconds,
						AllowPrivateHosts: &allowPrivateHosts,
					},
					Credentials: api.S3Credentials{
						AccessKeyId:     &accessKeyID,
						SecretAccessKey: &secretAccessKey,
					},
				}))
				return req
			}(),
			createFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, params *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
				require.Equal(t, model.DataSourceTypeS3, params.Type)
				require.JSONEq(t, `{"allow_private_hosts":true,"bucket":"braidplane-fixtures","endpoint":"http://localhost:39000","force_path_style":true,"prefix":"fixtures","region":"us-east-1","timeout_seconds":5}`, string(params.ConfigJSON))
				require.Equal(t, "braidplane", params.Credentials["access_key_id"])
				require.Equal(t, "braidplanepass", params.Credentials["secret_access_key"])
				return testS3DS, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   new(dataSourceToHTTP(testS3DS)),
		},
		{
			name: "Returns409WhenSlugAlreadyExists",
			body: createReq,
			createFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
				return nil, datasourcessvc.ErrSlugTaken
			},
			wantStatus: http.StatusConflict,
			wantErr: apierror.New(
				409,
				"DATA_SOURCE_ALREADY_EXISTS",
				"data source already exists",
			),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			body: createReq,
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionCreate &&
					input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "create"),
		},
		{
			name:       "Returns400WhenScalarRequestBody",
			body:       "not-json",
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name:       "Returns400WhenArrayRequestBody",
			body:       []string{"not-an-object"},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name:       "Returns400WhenNullRequestBody",
			body:       nil,
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name: "Returns422WhenConnectionFailed",
			body: createReq,
			createFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
				return nil, datasourcessvc.ErrConnectionFailed
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr: apierror.New(
				422,
				"DATA_SOURCE_CONNECTION_FAILED",
				"data source connection failed",
			),
		},
		{
			name: "Returns422WhenConnectionFailedWithPublicDetail",
			body: createReq,
			createFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
				return nil, datasourcessvc.ErrConnectionFailed.
					WithPublicDetail("git ls-remote failed: repository not found").
					Detail("repository not found")
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr: apierror.New(
				422,
				"DATA_SOURCE_CONNECTION_FAILED",
				"data source connection failed",
			).WithDetailedError("git ls-remote failed: repository not found"),
		},
		{
			name: "Returns400WhenServiceReturnsInvalidInput",
			body: createReq,
			createFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
				return nil, fmt.Errorf("name too long: %w", datasourcessvc.ErrInvalidInput)
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "VALIDATION_FAILED", "validation failed"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{
					CreateFn: tc.createFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(t, http.MethodPost, "/organizations/acme-corp/data-sources", tc.body)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}

			if tc.wantBody != nil {
				assertResponse(t, resp, *tc.wantBody)
			}
		})
	}
}

func TestCreateDataSourcePayloadFromRequest_HubSpotReadOnlyDefaultsSecurely(t *testing.T) {
	t.Parallel()

	request := func(readOnly *bool) api.CreateDataSourceRequest {
		var req api.CreateDataSourceRequest
		must.FnErr(req.FromCreateHubSpotDataSourceRequest(api.CreateHubSpotDataSourceRequest{
			Name:   "HubSpot CRM",
			Type:   api.CreateHubSpotDataSourceRequestTypeHubspot,
			Config: api.HubSpotConfig{},
			Credentials: api.HubSpotCredentials{
				PrivateAppToken: "pat-na1-test-token",
			},
			ReadOnly: readOnly,
		}))
		return req
	}

	payload, err := createDataSourcePayloadFromRequest(request(nil))
	require.NoError(t, err)
	assert.True(t, payload.ReadOnly)

	payload, err = createDataSourcePayloadFromRequest(request(new(false)))
	require.NoError(t, err)
	assert.False(t, payload.ReadOnly)
}

func TestSnowflakeRequestPayloadAndCredentialValidation(t *testing.T) {
	t.Parallel()

	newCredentials := func(t *testing.T, payload string) api.SnowflakeCredentials {
		t.Helper()
		var credentials api.SnowflakeCredentials
		require.NoError(t, json.Unmarshal([]byte(payload), &credentials))
		return credentials
	}

	validCredentials := newCredentials(t, `{"auth_type":"workload_identity"}`)
	var request api.CreateDataSourceRequest
	require.NoError(t, request.FromCreateSnowflakeDataSourceRequest(api.CreateSnowflakeDataSourceRequest{
		Name: "Warehouse",
		Config: api.SnowflakeConfig{
			Account: "acme", Warehouse: "WH", Database: "DB", Schema: "PUBLIC",
		},
		Credentials: validCredentials,
	}))
	payload, err := createDataSourcePayloadFromRequest(request)
	require.NoError(t, err)
	require.Equal(t, model.DataSourceTypeSnowflake, payload.Type)
	require.NoError(t, validateCredentialsByType(payload.Type, payload.Credentials))

	staticWIF := newCredentials(
		t, `{"auth_type":"workload_identity","username":"must-not-be-accepted"}`,
	)
	err = validateCredentialsByType(model.DataSourceTypeSnowflake, staticWIF)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
	require.ErrorContains(t, err, "must not include username or password")

	unsupported := newCredentials(
		t, `{"auth_type":"oauth2","username":"complete","password":"complete"}`,
	)
	err = validateCredentialsByType(model.DataSourceTypeSnowflake, unsupported)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
	require.ErrorContains(t, err, "auth_type")
}

func TestDataSourceToHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ds     *datasourcessvc.DataSource
		assert func(t *testing.T, got api.DataSource)
	}{
		{
			name: "BigQueryRedactsCredentials",
			ds: &datasourcessvc.DataSource{
				Slug: must.Slug("analytics-warehouse"),
				Name: "Analytics Warehouse",
				Type: model.DataSourceTypeBigQuery,
				Config: json.RawMessage(
					`{"project_id":"analytics-prod","location":"EU","default_dataset":"warehouse"}`,
				),
				IsActive: true,
			},
			assert: func(t *testing.T, got api.DataSource) {
				t.Helper()

				config, err := got.Config.AsBigQueryConfig()
				require.NoError(t, err)
				assert.Equal(t, "analytics-prod", config.ProjectId)
				require.NotNil(t, config.Location)
				assert.Equal(t, "EU", *config.Location)

				body, err := json.Marshal(got)
				require.NoError(t, err)
				payload := string(body)
				assert.Contains(t, payload, `"type":"bigquery"`)
				assert.Contains(t, payload, `"project_id":"analytics-prod"`)
				assert.NotContains(t, payload, "credentials")
				assert.NotContains(t, payload, "service_account_json")
				assert.NotContains(t, payload, "private_key")
				assert.NotContains(t, payload, "svc@analytics-prod.iam.gserviceaccount.com")
			},
		},
		{
			name: "ColdCacheSchemaStatusIsNotImported",
			ds: &datasourcessvc.DataSource{
				Slug:     must.Slug("prod-db"),
				Name:     "Production Database",
				Type:     model.DataSourceTypePostgres,
				Config:   json.RawMessage(`{"host":"localhost","port":5432,"database":"prod"}`),
				IsActive: true,
				Schema: datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            model.DataSourceTypePostgres.String(),
					Payload: datasourcessvc.SQLSchemaPayload{
						Tables: []datasourcessvc.SchemaTable{},
					},
				},
			},
			assert: func(t *testing.T, got api.DataSource) {
				t.Helper()

				assert.Equal(t, api.DataSourceSchemaStatusNotImported, got.SchemaStatus)
				sqlSchema, err := got.Schema.AsSQLSchemaResponse()
				require.NoError(t, err)
				assert.Empty(t, sqlSchema.Tables)
			},
		},
		{
			name: "CachedSchemaStatusIsReadyWithSchema",
			ds: &datasourcessvc.DataSource{
				Slug:         must.Slug("prod-db"),
				Name:         "Production Database",
				Type:         model.DataSourceTypePostgres,
				Config:       json.RawMessage(`{"host":"localhost","port":5432,"database":"prod"}`),
				IsActive:     true,
				SchemaCached: true,
				Schema: datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            model.DataSourceTypePostgres.String(),
					Payload: datasourcessvc.SQLSchemaPayload{
						Tables: []datasourcessvc.SchemaTable{{
							Name: "users",
							Columns: []datasourcessvc.SchemaColumn{
								{Name: "email", Type: "text"},
							},
						}},
					},
				},
			},
			assert: func(t *testing.T, got api.DataSource) {
				t.Helper()

				assert.Equal(t, api.DataSourceSchemaStatusReady, got.SchemaStatus)
				sqlSchema, err := got.Schema.AsSQLSchemaResponse()
				require.NoError(t, err)
				require.Len(t, sqlSchema.Tables, 1)
				assert.Equal(t, "users", sqlSchema.Tables[0].Name)
			},
		},
		{
			name: "CachedSchemaStatusIsFailedWhenSchemaMappingFails",
			ds: &datasourcessvc.DataSource{
				Slug:         must.Slug("prod-db"),
				Name:         "Production Database",
				Type:         model.DataSourceTypePostgres,
				Config:       json.RawMessage(`{"host":"localhost","port":5432,"database":"prod"}`),
				IsActive:     true,
				SchemaCached: true,
				Schema:       unsupportedSchemaResult{},
			},
			assert: func(t *testing.T, got api.DataSource) {
				t.Helper()

				assert.Equal(t, api.DataSourceSchemaStatusFailed, got.SchemaStatus)
				require.NotNil(t, got.SchemaDiagnostic)
				assert.Equal(t, "SCHEMA_RESPONSE_MAPPING_FAILED", got.SchemaDiagnostic.Code)
				assert.Equal(t, "cached schema could not be rendered", got.SchemaDiagnostic.Message)
				_, err := got.Schema.ValueByDiscriminator()
				require.Error(t, err)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := dataSourceToHTTP(tc.ds)
			tc.assert(t, got)
		})
	}
}

func TestTestDataSourceConnection(t *testing.T) {
	t.Parallel()

	var req api.TestDataSourceConnectionRequest
	must.FnErr(
		req.FromTestPostgresDataSourceConnectionRequest(api.TestPostgresDataSourceConnectionRequest{
			Config: api.PostgresConfig{
				Host:     "localhost",
				Port:     5432,
				Database: "prod",
			},
			Credentials: api.PostgresCredentials{
				Username: "postgres",
				Password: "secret",
			},
		}),
	)

	tests := []struct {
		name       string
		body       any
		testFn     func(ctx context.Context, orgSlug slug.Slug, params *datasourcessvc.TestConnectionParams) error
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
	}{
		{
			name: "Returns204WhenConnectionSucceeds",
			body: req,
			testFn: func(_ context.Context, _ slug.Slug, _ *datasourcessvc.TestConnectionParams) error {
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "Returns204WhenHubSpotConnectionSucceeds",
			body: func() api.TestDataSourceConnectionRequest {
				var hubspotReq api.TestDataSourceConnectionRequest
				timeoutSeconds := 20
				must.FnErr(
					hubspotReq.FromTestHubSpotDataSourceConnectionRequest(
						api.TestHubSpotDataSourceConnectionRequest{
							Config: api.HubSpotConfig{
								TimeoutSeconds: &timeoutSeconds,
							},
							Credentials: api.HubSpotCredentials{
								PrivateAppToken: "pat-na1-connection-test",
							},
						},
					),
				)
				return hubspotReq
			}(),
			testFn: func(_ context.Context, _ slug.Slug, params *datasourcessvc.TestConnectionParams) error {
				require.Equal(t, model.DataSourceTypeHubSpot, params.Type)
				require.JSONEq(t, `{"timeout_seconds":20}`, string(params.ConfigJSON))
				require.Equal(t, "pat-na1-connection-test", params.Credentials["private_app_token"])
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "Returns204WhenS3ConnectionSucceeds",
			body: func() api.TestDataSourceConnectionRequest {
				var s3Req api.TestDataSourceConnectionRequest
				forcePathStyle := true
				allowPrivateHosts := true
				endpoint := "http://localhost:39000"
				accessKeyID := "braidplane"
				secretAccessKey := "braidplanepass"
				must.FnErr(
					s3Req.FromTestS3DataSourceConnectionRequest(
						api.TestS3DataSourceConnectionRequest{
							Config: api.S3Config{
								Endpoint:          &endpoint,
								Region:            "us-east-1",
								Bucket:            "braidplane-fixtures",
								ForcePathStyle:    &forcePathStyle,
								AllowPrivateHosts: &allowPrivateHosts,
							},
							Credentials: api.S3Credentials{
								AccessKeyId:     &accessKeyID,
								SecretAccessKey: &secretAccessKey,
							},
						},
					),
				)
				return s3Req
			}(),
			testFn: func(_ context.Context, _ slug.Slug, params *datasourcessvc.TestConnectionParams) error {
				require.Equal(t, model.DataSourceTypeS3, params.Type)
				require.JSONEq(t, `{"allow_private_hosts":true,"bucket":"braidplane-fixtures","endpoint":"http://localhost:39000","force_path_style":true,"region":"us-east-1"}`, string(params.ConfigJSON))
				require.Equal(t, "braidplane", params.Credentials["access_key_id"])
				require.Equal(t, "braidplanepass", params.Credentials["secret_access_key"])
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "Returns422WhenConnectionFails",
			body: req,
			testFn: func(_ context.Context, _ slug.Slug, _ *datasourcessvc.TestConnectionParams) error {
				return datasourcessvc.ErrConnectionFailed
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr: apierror.New(
				422,
				"DATA_SOURCE_CONNECTION_FAILED",
				"data source connection failed",
			),
		},
		{
			name: "Returns422WhenConnectionFailsWithPublicDetail",
			body: req,
			testFn: func(_ context.Context, _ slug.Slug, _ *datasourcessvc.TestConnectionParams) error {
				return datasourcessvc.ErrConnectionFailed.
					WithPublicDetail("git ls-remote failed: repository not found").
					Detail("repository not found")
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr: apierror.New(
				422,
				"DATA_SOURCE_CONNECTION_FAILED",
				"data source connection failed",
			).WithDetailedError("git ls-remote failed: repository not found"),
		},
		{
			name:       "Returns400WhenScalarBody",
			body:       "not-json",
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name:       "Returns400WhenArrayBody",
			body:       []string{"not-an-object"},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name:       "Returns400WhenNullBody",
			body:       nil,
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			body: req,
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionCreate &&
					input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "create"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{TestConnectionFn: tc.testFn},
				authService:        fakeAuthService{CanFn: tc.authFn},
			})

			req := jsonRequest(
				t,
				http.MethodPost,
				"/organizations/acme-corp/data-sources/test-connection",
				tc.body,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}
		})
	}
}

func TestTestExistingDataSourceConnection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		testFn     func(ctx context.Context, orgSlug, dsSlug slug.Slug) error
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
	}{
		{
			name: "Returns204WhenConnectionSucceeds",
			testFn: func(_ context.Context, orgSlug, dsSlug slug.Slug) error {
				require.Equal(t, must.Slug("acme-corp"), orgSlug)
				require.Equal(t, must.Slug("prod-db"), dsSlug)
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "Returns404WhenDataSourceIsMissing",
			testFn: func(_ context.Context, _, _ slug.Slug) error {
				return datasourcessvc.ErrDataSourceNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr: apierror.New(
				404,
				"DATA_SOURCE_NOT_FOUND",
				"data source not found",
			),
		},
		{
			name: "Returns422WhenConnectionFails",
			testFn: func(_ context.Context, _, _ slug.Slug) error {
				return datasourcessvc.ErrConnectionFailed
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr: apierror.New(
				422,
				"DATA_SOURCE_CONNECTION_FAILED",
				"data source connection failed",
			),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionUpdate &&
					input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "update"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{TestExistingConnectionFn: tc.testFn},
				authService:        fakeAuthService{CanFn: tc.authFn},
			})

			req := jsonRequest(
				t,
				http.MethodPost,
				"/organizations/acme-corp/data-sources/prod-db/test-connection",
				nil,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}
		})
	}
}

func TestListDataSourceBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		listFn     func(ctx context.Context, orgSlug, dsSlug slug.Slug) ([]datasourcessvc.DataSourceBranch, error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
		wantBody   *api.DataSourceBranchListResponse
	}{
		{
			name: "ReturnsBranches",
			listFn: func(_ context.Context, orgSlug, dsSlug slug.Slug) ([]datasourcessvc.DataSourceBranch, error) {
				require.Equal(t, must.Slug("acme-corp"), orgSlug)
				require.Equal(t, must.Slug("pipelines-git"), dsSlug)
				return []datasourcessvc.DataSourceBranch{
					{Name: "main", IsDefault: true},
					{Name: "release"},
				}, nil
			},
			wantStatus: http.StatusOK,
			wantBody: &api.DataSourceBranchListResponse{
				Branches: []api.DataSourceBranch{
					{Name: "main", IsDefault: true},
					{Name: "release", IsDefault: false},
				},
			},
		},
		{
			name: "Returns400ForUnsupportedType",
			listFn: func(_ context.Context, _, _ slug.Slug) ([]datasourcessvc.DataSourceBranch, error) {
				return nil, datasourcessvc.ErrInvalidDataSourceType
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "DATA_SOURCE_INVALID_INPUT", "data source invalid input"),
		},
		{
			name: "Returns404WhenDataSourceIsMissing",
			listFn: func(_ context.Context, _, _ slug.Slug) ([]datasourcessvc.DataSourceBranch, error) {
				return nil, datasourcessvc.ErrDataSourceNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "DATA_SOURCE_NOT_FOUND", "data source not found"),
		},
		{
			name: "Returns422WhenRemoteCannotBeListed",
			listFn: func(_ context.Context, _, _ slug.Slug) ([]datasourcessvc.DataSourceBranch, error) {
				return nil, datasourcessvc.ErrConnectionFailed.
					WithPublicDetail("git ls-remote failed: repository not found").
					Detail("repository not found")
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr: apierror.New(
				422,
				"DATA_SOURCE_CONNECTION_FAILED",
				"data source connection failed",
			).WithDetailedError("git ls-remote failed: repository not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionRead && input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "read"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{ListBranchesFn: tc.listFn},
				authService:        fakeAuthService{CanFn: tc.authFn},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				"/organizations/acme-corp/data-sources/pipelines-git/branches",
				nil,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)
			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}
			if tc.wantBody != nil {
				assertResponse(t, resp, *tc.wantBody)
			}
		})
	}
}

func TestProviderRepositorySelectionEndpoints(t *testing.T) {
	t.Parallel()

	var selected string
	dataSourcesService := &fakeDataSourcesService{
		ListProviderRepositoriesFn: func(_ context.Context, orgSlug, dsSlug slug.Slug) ([]datasourcessvc.ProviderRepository, error) {
			require.Equal(t, must.Slug("acme-corp"), orgSlug)
			require.Equal(t, must.Slug("pipelines"), dsSlug)
			return []datasourcessvc.ProviderRepository{{
				ID: "42", Name: "Pipelines", FullName: "acme/pipelines",
				WebURL: "https://gitlab.com/acme/pipelines", DefaultBranch: "main",
			}}, nil
		},
		SelectProviderRepositoryFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, params datasourcessvc.SelectProviderRepositoryParams) (*datasourcessvc.DataSource, error) {
			selected = params.RepositoryID
			return &datasourcessvc.DataSource{
				Slug: must.Slug("pipelines"), Name: "Pipelines", Type: model.DataSourceTypeGitlab,
				Config: []byte(`{"repository":{"id":"42","name":"Pipelines","full_name":"acme/pipelines","web_url":"https://gitlab.com/acme/pipelines","default_branch":"main"}}`),
			}, nil
		},
	}
	app, sessionStore := newTestApp(t, testServerConfig{
		datasourcesService: datasourcessvc.NewServiceWithLogging(dataSourcesService, slog.Default()),
	})

	listReq := jsonRequest(t, http.MethodGet, "/organizations/acme-corp/data-sources/pipelines/provider-repositories", nil)
	listResp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), listReq)
	defer func() { require.NoError(t, listResp.Body.Close()) }()
	require.Equal(t, http.StatusOK, listResp.StatusCode)
	list := parseResponse[api.ProviderRepositoryListResponse](t, listResp)
	require.Len(t, list.Repositories, 1)
	require.Equal(t, "42", list.Repositories[0].Id)
	require.NotNil(t, list.Repositories[0].DefaultBranch)

	selectReq := jsonRequest(t, http.MethodPut, "/organizations/acme-corp/data-sources/pipelines/provider-repository", api.SelectProviderRepositoryRequest{RepositoryId: "42"})
	selectResp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), selectReq)
	defer func() { require.NoError(t, selectResp.Body.Close()) }()
	require.Equal(t, http.StatusOK, selectResp.StatusCode)
	require.Equal(t, "42", selected)
}

func TestListDataSources(t *testing.T) {
	t.Parallel()

	testDSList := []datasourcessvc.DataSource{
		{
			Slug:     must.Slug("prod-db"),
			Name:     "Production Database",
			Type:     model.DataSourceTypePostgres,
			Config:   json.RawMessage(`{"host":"localhost"}`),
			IsActive: true,
		},
		{
			Slug:     must.Slug("staging-db"),
			Name:     "Staging Database",
			Type:     model.DataSourceTypeMySQL,
			Config:   json.RawMessage(`{"host":"staging.local"}`),
			IsActive: true,
		},
	}

	tests := []struct {
		name         string
		listFn       func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) (model.ListResponse[datasourcessvc.DataSource], error)
		authFn       func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus   int
		wantCount    int
		wantErr      *apierror.Error
		assertResult func(*testing.T, []api.DataSource)
	}{
		{
			name: "ReturnsAllDataSourcesInOrg",
			listFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) (model.ListResponse[datasourcessvc.DataSource], error) {
				return model.ListResponse[datasourcessvc.DataSource]{Items: testDSList}, nil
			},
			wantStatus: http.StatusOK,
			wantCount:  2,
			assertResult: func(t *testing.T, result []api.DataSource) {
				assert.Equal(t, "prod-db", result[0].Slug)
				assert.Equal(t, "Production Database", result[0].Name)
				assert.Equal(t, api.DataSourceTypePostgres, result[0].Type)
				assert.Equal(t, "staging-db", result[1].Slug)
				assert.Equal(t, "Staging Database", result[1].Name)
				assert.Equal(t, api.DataSourceTypeMysql, result[1].Type)
			},
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionRead && input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "read"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{
					ListByOrgFn: tc.listFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(t, http.MethodGet, "/organizations/acme-corp/data-sources", nil)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}

			if tc.wantCount > 0 {
				result := parseResponse[api.DataSourceListResponse](t, resp)
				assert.Len(t, result.DataSources, tc.wantCount)
				if tc.assertResult != nil {
					tc.assertResult(t, result.DataSources)
				}
			}
		})
	}
}

func TestGetDataSource(t *testing.T) {
	t.Parallel()

	testDS := &datasourcessvc.DataSource{
		Slug:     must.Slug("prod-db"),
		Name:     "Production Database",
		Type:     model.DataSourceTypePostgres,
		Config:   json.RawMessage(`{"host":"localhost","port":5432,"database":"prod"}`),
		IsActive: true,
	}

	tests := []struct {
		name       string
		findFn     func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
		wantBody   *api.DataSource
	}{
		{
			name: "ReturnsDataSource",
			findFn: func(_ context.Context, _, _ slug.Slug) (*datasourcessvc.DataSource, error) {
				return testDS, nil
			},
			wantStatus: http.StatusOK,
			wantBody:   new(dataSourceToHTTP(testDS)),
		},
		{
			name: "Returns404WhenDataSourceNotFound",
			findFn: func(_ context.Context, _, _ slug.Slug) (*datasourcessvc.DataSource, error) {
				return nil, datasourcessvc.ErrDataSourceNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "DATA_SOURCE_NOT_FOUND", "data source not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionRead && input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "read"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{
					FindBySlugFn: tc.findFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				"/organizations/acme-corp/data-sources/prod-db",
				nil,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}

			if tc.wantBody != nil {
				assertResponse(t, resp, *tc.wantBody)
			}
		})
	}
}

func TestUpdateDataSource(t *testing.T) {
	t.Parallel()

	testDS := &datasourcessvc.DataSource{
		Slug:     must.Slug("prod-db"),
		Name:     "Updated Database",
		Type:     model.DataSourceTypePostgres,
		Config:   json.RawMessage(`{"host":"newhost"}`),
		IsActive: false,
	}

	tests := []struct {
		name       string
		body       any
		updateFn   func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, params *datasourcessvc.UpdateParams) (*datasourcessvc.DataSource, error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
		wantBody   *api.DataSource
	}{
		{
			name: "UpdatesDataSourceAndReturns200",
			body: func() api.UpdateDataSourceRequest {
				var req api.UpdateDataSourceRequest
				req.Name = ptrToName("Updated Database")
				req.Config = &api.DataSourceConfig{}
				must.FnErr(req.Config.FromPostgresConfig(api.PostgresConfig{
					Host:     "newhost",
					Port:     5432,
					Database: "prod",
				}))
				return req
			}(),
			updateFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, params *datasourcessvc.UpdateParams) (*datasourcessvc.DataSource, error) {
				require.True(t, params.ConfigFromCustomer)
				require.JSONEq(t, `{"host":"newhost","port":5432,"database":"prod"}`, string(params.ConfigJSON))
				return testDS, nil
			},
			wantStatus: http.StatusOK,
			wantBody:   new(dataSourceToHTTP(testDS)),
		},
		{
			name: "Returns404WhenDataSourceNotFound",
			body: func() api.UpdateDataSourceRequest {
				var req api.UpdateDataSourceRequest
				req.Name = ptrToName("Updated Database")
				return req
			}(),
			updateFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ *datasourcessvc.UpdateParams) (*datasourcessvc.DataSource, error) {
				return nil, datasourcessvc.ErrDataSourceNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "DATA_SOURCE_NOT_FOUND", "data source not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			body: func() api.UpdateDataSourceRequest {
				var req api.UpdateDataSourceRequest
				req.Name = ptrToName("Updated Database")
				return req
			}(),
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionUpdate &&
					input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "update"),
		},
		{
			name:       "Returns400WhenInvalidRequestBody",
			body:       "not-json",
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name: "Returns400WhenServiceReturnsInvalidInput",
			body: func() api.UpdateDataSourceRequest {
				var req api.UpdateDataSourceRequest
				req.Name = ptrToName("Updated Database")
				return req
			}(),
			updateFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ *datasourcessvc.UpdateParams) (*datasourcessvc.DataSource, error) {
				return nil, fmt.Errorf("name too long: %w", datasourcessvc.ErrInvalidInput)
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "VALIDATION_FAILED", "validation failed"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{
					UpdateFn: tc.updateFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodPatch,
				"/organizations/acme-corp/data-sources/prod-db",
				tc.body,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}

			if tc.wantBody != nil {
				assertResponse(t, resp, *tc.wantBody)
			}
		})
	}
}

func TestDeleteDataSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		deleteFn   func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) error
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
	}{
		{
			name: "DeletesDataSourceAndReturns204",
			deleteFn: func(_ context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) error {
				assert.Equal(t, must.Slug("acme-corp"), orgSlug)
				assert.Equal(t, must.Slug("prod-db"), dsSlug)
				assert.Equal(t, model.EventActorTypeUser, actor.Type)
				require.NotNil(t, actor.Slug)
				assert.Equal(t, must.Slug("test-user"), *actor.Slug)
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "Returns404WhenDataSourceNotFound",
			deleteFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor) error {
				return datasourcessvc.ErrDataSourceNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "DATA_SOURCE_NOT_FOUND", "data source not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionDelete &&
					input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "delete"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{
					DeleteFn: tc.deleteFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodDelete,
				"/organizations/acme-corp/data-sources/prod-db",
				nil,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}
		})
	}
}

func TestRestoreDataSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		restoreFn      func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) error
		authFn         func(ctx context.Context, input authorization.Input) (bool, error)
		wantQuotaCheck bool
		wantStatus     int
		wantErr        *apierror.Error
	}{
		{
			name: "RestoresDataSourceAndReturns204",
			restoreFn: func(_ context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) error {
				assert.Equal(t, must.Slug("acme-corp"), orgSlug)
				assert.Equal(t, must.Slug("prod-db"), dsSlug)
				assert.Equal(t, model.EventActorTypeUser, actor.Type)
				require.NotNil(t, actor.Slug)
				assert.Equal(t, must.Slug("test-user"), *actor.Slug)
				return nil
			},
			wantQuotaCheck: true,
			wantStatus:     http.StatusNoContent,
		},
		{
			name: "Returns404WhenDataSourceNotFound",
			restoreFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor) error {
				return datasourcessvc.ErrDataSourceNotFound
			},
			wantQuotaCheck: true,
			wantStatus:     http.StatusNotFound,
			wantErr:        apierror.New(404, "DATA_SOURCE_NOT_FOUND", "data source not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionRestore &&
					input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "restore"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			quotaChecked := false

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{
					RestoreFn: tc.restoreFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
				admissionService: &fakes.AdmissionService{CheckDataSourceAdmissionFn: func(
					_ context.Context,
					orgSlug slug.Slug,
				) error {
					assert.Equal(t, must.Slug("acme-corp"), orgSlug)
					quotaChecked = true
					return nil
				}},
			})

			req := jsonRequest(
				t,
				http.MethodPost,
				"/organizations/acme-corp/data-sources/prod-db/restore",
				nil,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)
			assert.Equal(t, tc.wantQuotaCheck, quotaChecked)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}
		})
	}
}

func TestIntrospectDataSourceSchema(t *testing.T) {
	t.Parallel()

	sqlSchema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{
			Tables: []datasourcessvc.SchemaTable{
				{
					Name: "users",
					Columns: []datasourcessvc.SchemaColumn{
						{Name: "id", Type: "integer"},
						{Name: "email", Type: "text"},
					},
				},
			},
		},
	}

	httpSchema := datasourcessvc.Schema[datasourcessvc.HTTPSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeHTTP,
		Type:            "http",
		Payload: datasourcessvc.HTTPSchemaPayload{
			Operations: []datasourcessvc.SchemaOperation{
				{
					ID:     "get-user",
					Path:   "/users/{id}",
					Method: "GET",
					RequestBody: json.RawMessage(`{
						"description": "Lookup payload",
						"required": true,
						"content": {
							"application/json": {
								"schema_summary": {
									"type": "object",
									"properties": {
										"id": {
											"type": "string"
										}
									}
								}
							}
						}
					}`),
					Responses: json.RawMessage(`{
						"200": {
							"description": "Success",
							"content": {
								"application/json": {
									"schema_summary": {
										"type": "object"
									}
								}
							}
						},
						"404": {
							"description": "Not found"
						}
					}`),
				},
			},
		},
	}

	documentSchema := datasourcessvc.Schema[datasourcessvc.DocumentSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeDocument,
		Type:            "mongodb",
		Payload: datasourcessvc.DocumentSchemaPayload{
			Collections: []datasourcessvc.SchemaCollection{
				{
					Name: "users",
					Fields: []datasourcessvc.SchemaField{
						{Name: "_id", Type: "objectId"},
						{Name: "email", Type: "string"},
					},
				},
			},
		},
	}

	hubspotSchema := datasourcessvc.Schema[datasourcessvc.ObjectAPISchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeObjectAPI,
		Type:            "hubspot",
		Payload: datasourcessvc.ObjectAPISchemaPayload{
			Objects: []datasourcessvc.SchemaObjectAPIObject{
				{
					Name:  "contacts",
					Label: "Contacts",
					Fields: []datasourcessvc.SchemaObjectAPIField{
						{
							Name:  "hs_object_id",
							Label: "HubSpot record ID",
							Type:  "string",
							ProviderMetadata: map[string]any{
								"source": "record_id",
							},
						},
						{Name: "email", Label: "Email", Type: "string"},
						{Name: "firstname", Label: "First name", Type: "string"},
					},
					Operations: []datasourcessvc.SchemaObjectAPIOperation{
						{Name: "list", Label: "List"},
						{Name: "search", Label: "Search"},
					},
				},
			},
		},
	}

	fileSchema := datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeFile,
		Type:            "github",
		Payload: datasourcessvc.FileSchemaPayload{
			Files: []datasourcessvc.SchemaFile{
				{
					Name: "orders.xlsx",
					Fields: []datasourcessvc.SchemaField{
						{Name: "email", Type: "string"},
						{Name: "total", Type: "string"},
					},
				},
			},
			Diagnostics: []datasourcessvc.SchemaFileDiagnostic{
				{
					Name:     "bad.json",
					Target:   "imports/bad.json",
					Severity: "warning",
					Code:     "parse_failed",
					Message:  "Skipped file because fields could not be inferred from the sample.",
				},
			},
		},
	}

	tests := []struct {
		name           string
		introspectFn   func(ctx context.Context, orgSlug, dsSlug slug.Slug) (datasourcessvc.SchemaResult, error)
		authFn         func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus     int
		wantSchemaType string
		wantErr        *apierror.Error
	}{
		{
			name: "ReturnsSQLSchema",
			introspectFn: func(_ context.Context, _, _ slug.Slug) (datasourcessvc.SchemaResult, error) {
				return sqlSchema, nil
			},
			wantStatus:     http.StatusOK,
			wantSchemaType: "sql",
		},
		{
			name: "ReturnsHTTPSchema",
			introspectFn: func(_ context.Context, _, _ slug.Slug) (datasourcessvc.SchemaResult, error) {
				return httpSchema, nil
			},
			wantStatus:     http.StatusOK,
			wantSchemaType: "http",
		},
		{
			name: "ReturnsDocumentSchema",
			introspectFn: func(_ context.Context, _, _ slug.Slug) (datasourcessvc.SchemaResult, error) {
				return documentSchema, nil
			},
			wantStatus:     http.StatusOK,
			wantSchemaType: "document",
		},
		{
			name: "ReturnsHubSpotSchema",
			introspectFn: func(_ context.Context, _, _ slug.Slug) (datasourcessvc.SchemaResult, error) {
				return hubspotSchema, nil
			},
			wantStatus:     http.StatusOK,
			wantSchemaType: "object_api",
		},
		{
			name: "ReturnsGitHubFileSchema",
			introspectFn: func(_ context.Context, _, _ slug.Slug) (datasourcessvc.SchemaResult, error) {
				return fileSchema, nil
			},
			wantStatus:     http.StatusOK,
			wantSchemaType: "file",
		},
		{
			name: "Returns404WhenDataSourceNotFound",
			introspectFn: func(_ context.Context, _, _ slug.Slug) (datasourcessvc.SchemaResult, error) {
				return nil, datasourcessvc.ErrDataSourceNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "DATA_SOURCE_NOT_FOUND", "data source not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionRead && input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "read"),
		},
		{
			name: "Returns422WhenConnectionFailed",
			introspectFn: func(_ context.Context, _, _ slug.Slug) (datasourcessvc.SchemaResult, error) {
				return nil, datasourcessvc.ErrConnectionFailed
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr: apierror.New(
				422,
				"DATA_SOURCE_CONNECTION_FAILED",
				"data source connection failed",
			),
		},
		{
			name: "Returns422WhenSchemaIntrospectionUnsupported",
			introspectFn: func(_ context.Context, _, _ slug.Slug) (datasourcessvc.SchemaResult, error) {
				return nil, datasourcessvc.ErrSchemaIntrospectionUnsupported
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr: apierror.New(
				422,
				"DATA_SOURCE_SCHEMA_INTROSPECTION_UNSUPPORTED",
				"data source schema introspection unsupported",
			),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{
					IntrospectSchemaFn: tc.introspectFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				"/organizations/acme-corp/data-sources/prod-db/schema",
				nil,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}

			if tc.wantSchemaType != "" {
				result := parseResponse[api.IntrospectSchemaResponse](t, resp)
				// Verify discriminator field
				schemaType, err := result.Discriminator()
				require.NoError(t, err)
				assert.Equal(t, tc.wantSchemaType, schemaType)

				// Verify schema content
				switch tc.wantSchemaType {
				case "sql":
					sqlResp, err := result.AsSQLSchemaResponse()
					require.NoError(t, err)
					assert.NotNil(t, sqlResp)
					assert.Len(t, sqlResp.Tables, 1)
					assert.Equal(t, "users", sqlResp.Tables[0].Name)
					assert.Len(t, sqlResp.Tables[0].Columns, 2)
				case "http":
					httpResp, err := result.AsHTTPSchemaResponse()
					require.NoError(t, err)
					assert.NotNil(t, httpResp)
					assert.Len(t, httpResp.Operations, 1)
					assert.Equal(t, "get-user", httpResp.Operations[0].Id)
					require.NotNil(t, httpResp.Operations[0].RequestBody)
					requestBody := httpResp.Operations[0].RequestBody
					require.NotNil(t, requestBody.Content)
					requestContent := *requestBody.Content
					require.Contains(t, requestContent, "application/json")
					requestJSON := requestContent["application/json"]
					require.NotNil(t, requestJSON.SchemaSummary)
					requestSchemaSummary := *requestJSON.SchemaSummary
					assert.Equal(t, "object", requestSchemaSummary["type"])
					requestProperties, ok := requestSchemaSummary["properties"].(map[string]any)
					require.True(t, ok)
					assert.Contains(t, requestProperties, "id")
					require.NotNil(t, httpResp.Operations[0].Responses)
					responses := *httpResp.Operations[0].Responses
					require.Contains(t, responses, "200")
					require.Contains(t, responses, "404")
					response200 := responses["200"]
					require.NotNil(t, response200.Description)
					assert.Equal(t, "Success", *response200.Description)
					require.NotNil(t, response200.Content)
					content := *response200.Content
					require.Contains(t, content, "application/json")
					applicationJSON := content["application/json"]
					require.NotNil(t, applicationJSON.SchemaSummary)
					schemaSummary := *applicationJSON.SchemaSummary
					assert.Equal(t, "object", schemaSummary["type"])
				case "document":
					docResp, err := result.AsDocumentSchemaResponse()
					require.NoError(t, err)
					assert.NotNil(t, docResp)
					assert.Len(t, docResp.Collections, 1)
					assert.Equal(t, "users", docResp.Collections[0].Name)
				case "object_api":
					hubspotResp, err := result.AsObjectAPISchemaResponse()
					require.NoError(t, err)
					assert.NotNil(t, hubspotResp)
					assert.Equal(t, "hubspot", hubspotResp.Type)
					assert.Len(t, hubspotResp.Objects, 1)
					assert.Equal(t, "contacts", hubspotResp.Objects[0].Name)
					require.NotNil(t, hubspotResp.Objects[0].Label)
					assert.Equal(t, "Contacts", *hubspotResp.Objects[0].Label)
					require.Len(t, hubspotResp.Objects[0].Fields, 3)
					assert.Equal(t, "hs_object_id", hubspotResp.Objects[0].Fields[0].Name)
					assert.Equal(t, "string", hubspotResp.Objects[0].Fields[0].Type)
					require.NotNil(t, hubspotResp.Objects[0].Fields[0].ProviderMetadata)
					assert.Equal(t, "record_id", (*hubspotResp.Objects[0].Fields[0].ProviderMetadata)["source"])
					require.NotNil(t, hubspotResp.Objects[0].Operations)
					require.Len(t, *hubspotResp.Objects[0].Operations, 2)
					assert.Equal(t, "search", (*hubspotResp.Objects[0].Operations)[1].Name)
				case "file":
					fileResp, err := result.AsFileSchemaResponse()
					require.NoError(t, err)
					assert.Equal(t, api.FileSchemaResponseTypeGithub, fileResp.Type)
					require.Len(t, fileResp.Files, 1)
					assert.Equal(t, "orders.xlsx", fileResp.Files[0].Name)
					require.Len(t, fileResp.Files[0].Fields, 2)
					assert.Equal(t, "email", fileResp.Files[0].Fields[0].Name)
					require.NotNil(t, fileResp.Diagnostics)
					require.Len(t, *fileResp.Diagnostics, 1)
					diagnostic := (*fileResp.Diagnostics)[0]
					assert.Equal(t, "bad.json", diagnostic.Name)
					assert.Equal(t, "imports/bad.json", diagnostic.Target)
					assert.Equal(t, "warning", diagnostic.Severity)
					assert.Equal(t, "parse_failed", diagnostic.Code)
				}
			}
		})
	}
}

func TestRefreshDataSourceSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		refreshFn  func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) (*datasourcessvc.SchemaImportResult, error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
		wantBody   *api.DataSourceSchemaImportResponse
	}{
		{
			name: "QueuesSchemaImportAndReturns202",
			refreshFn: func(_ context.Context, _, dsSlug slug.Slug, _ model.EventActor) (*datasourcessvc.SchemaImportResult, error) {
				return &datasourcessvc.SchemaImportResult{
					DataSourceSlug: dsSlug,
					Status:         model.DataSourceSchemaImportStatusImporting,
					Enqueued:       true,
					Job: &datasourcessvc.QueuedSchemaImportJob{
						Kind:  "data_source_schema_import",
						Queue: "data_source_schema_import",
						State: "queued",
					},
				}, nil
			},
			wantStatus: http.StatusAccepted,
			wantBody: &api.DataSourceSchemaImportResponse{
				DataSourceSlug: "prod-db",
				SchemaStatus:   api.DataSourceSchemaStatusImporting,
				Enqueued:       true,
				Job: &api.DataSourceSchemaImportJob{
					Kind:  "data_source_schema_import",
					Queue: "data_source_schema_import",
					State: api.DataSourceSchemaImportJobStateQueued,
				},
			},
		},
		{
			name: "Returns202WhenImportAlreadyInFlight",
			refreshFn: func(_ context.Context, _, dsSlug slug.Slug, _ model.EventActor) (*datasourcessvc.SchemaImportResult, error) {
				return &datasourcessvc.SchemaImportResult{
					DataSourceSlug: dsSlug,
					Status:         model.DataSourceSchemaImportStatusImporting,
					Enqueued:       false,
				}, nil
			},
			wantStatus: http.StatusAccepted,
			wantBody: &api.DataSourceSchemaImportResponse{
				DataSourceSlug: "prod-db",
				SchemaStatus:   api.DataSourceSchemaStatusImporting,
				Enqueued:       false,
			},
		},
		{
			name: "Returns404WhenDataSourceNotFound",
			refreshFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor) (*datasourcessvc.SchemaImportResult, error) {
				return nil, datasourcessvc.ErrDataSourceNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "DATA_SOURCE_NOT_FOUND", "data source not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionUpdate &&
					input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "update"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{
					RefreshSchemaFn: tc.refreshFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodPost,
				"/organizations/acme-corp/data-sources/prod-db/schema",
				nil,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}
			if tc.wantBody != nil {
				assertResponse(t, resp, *tc.wantBody)
			}
		})
	}
}

func TestUpdateDataSourceCredentials(t *testing.T) {
	t.Parallel()

	testDS := &datasourcessvc.DataSource{
		Slug:     must.Slug("prod-db"),
		Name:     "Production Database",
		Type:     model.DataSourceTypePostgres,
		Config:   json.RawMessage(`{"host":"localhost","port":5432,"database":"prod"}`),
		IsActive: true,
	}

	tests := []struct {
		name         string
		body         any
		findBySlugFn func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error)
		updateCredFn func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, params *datasourcessvc.UpdateCredentialsParams) (*datasourcessvc.DataSource, error)
		authFn       func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus   int
		wantErr      *apierror.Error
		wantBody     *api.DataSource
	}{
		{
			name: "UpdatesCredentialsAndReturns200",
			body: func() api.UpdateDataSourceCredentialsRequest {
				var req api.UpdateDataSourceCredentialsRequest
				must.FnErr(req.Credentials.FromPostgresCredentials(api.PostgresCredentials{
					Username: "admin",
					Password: "newpass",
				}))
				return req
			}(),
			findBySlugFn: func(_ context.Context, _, _ slug.Slug) (*datasourcessvc.DataSource, error) {
				return testDS, nil
			},
			updateCredFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ *datasourcessvc.UpdateCredentialsParams) (*datasourcessvc.DataSource, error) {
				return testDS, nil
			},
			wantStatus: http.StatusOK,
			wantBody:   new(dataSourceToHTTP(testDS)),
		},
		{
			name: "Returns404WhenDataSourceNotFoundOnLookup",
			body: func() api.UpdateDataSourceCredentialsRequest {
				var req api.UpdateDataSourceCredentialsRequest
				must.FnErr(req.Credentials.FromPostgresCredentials(api.PostgresCredentials{
					Username: "admin",
					Password: "newpass",
				}))
				return req
			}(),
			findBySlugFn: func(_ context.Context, _, _ slug.Slug) (*datasourcessvc.DataSource, error) {
				return nil, datasourcessvc.ErrDataSourceNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "DATA_SOURCE_NOT_FOUND", "data source not found"),
		},
		{
			name: "Returns404WhenDataSourceNotFoundOnUpdate",
			body: func() api.UpdateDataSourceCredentialsRequest {
				var req api.UpdateDataSourceCredentialsRequest
				must.FnErr(req.Credentials.FromPostgresCredentials(api.PostgresCredentials{
					Username: "admin",
					Password: "newpass",
				}))
				return req
			}(),
			findBySlugFn: func(_ context.Context, _, _ slug.Slug) (*datasourcessvc.DataSource, error) {
				return testDS, nil
			},
			updateCredFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ *datasourcessvc.UpdateCredentialsParams) (*datasourcessvc.DataSource, error) {
				return nil, datasourcessvc.ErrDataSourceNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "DATA_SOURCE_NOT_FOUND", "data source not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			body: func() api.UpdateDataSourceCredentialsRequest {
				var req api.UpdateDataSourceCredentialsRequest
				must.FnErr(req.Credentials.FromPostgresCredentials(api.PostgresCredentials{
					Username: "admin",
					Password: "newpass",
				}))
				return req
			}(),
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionUpdate &&
					input.Resource == model.ResourceDataSources {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("datasources", "update"),
		},
		{
			name: "Returns400WhenInvalidRequestBody",
			body: "not-json",
			findBySlugFn: func(_ context.Context, _, _ slug.Slug) (*datasourcessvc.DataSource, error) {
				return testDS, nil
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{
					FindBySlugFn:        tc.findBySlugFn,
					UpdateCredentialsFn: tc.updateCredFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodPatch,
				"/organizations/acme-corp/data-sources/prod-db/credentials",
				tc.body,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
			}

			if tc.wantBody != nil {
				assertResponse(t, resp, *tc.wantBody)
			}
		})
	}
}

func TestValidateCredentialsByType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dsType  model.DataSourceType
		creds   func() any
		wantErr bool
	}{
		{
			name:   "ReturnsNilForNonHTTPType",
			dsType: model.DataSourceTypePostgres,
			creds: func() any {
				return api.PostgresCredentials{
					Username: "postgres",
					Password: "secret",
				}
			},
			wantErr: false,
		},
		{
			name:   "ReturnsNilForHTTPWithBearerToken",
			dsType: model.DataSourceTypeHTTP,
			creds: func() any {
				return api.HTTPCredentials{
					BearerToken: new("token123"),
				}
			},
			wantErr: false,
		},
		{
			name:   "ReturnsNilForHTTPWithValidBasicAuth",
			dsType: model.DataSourceTypeHTTP,
			creds: func() any {
				return api.HTTPCredentials{
					Username: new("user"),
					Password: new("pass"),
				}
			},
			wantErr: false,
		},
		{
			name:   "ReturnsErrorForHTTPWithUsernameOnly",
			dsType: model.DataSourceTypeHTTP,
			creds: func() any {
				return api.HTTPCredentials{
					Username: new("user"),
				}
			},
			wantErr: true,
		},
		{
			name:   "ReturnsErrorForHTTPWithEmptyUsername",
			dsType: model.DataSourceTypeHTTP,
			creds: func() any {
				return api.HTTPCredentials{
					Username: new(""),
					Password: new("pass"),
				}
			},
			wantErr: true,
		},
		{
			name:   "ReturnsErrorForHTTPWithEmptyPassword",
			dsType: model.DataSourceTypeHTTP,
			creds: func() any {
				return api.HTTPCredentials{
					Username: new("user"),
					Password: new(""),
				}
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			creds := tc.creds()
			err := validateCredentialsByType(tc.dsType, creds)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func apiSlugValuePtr(value string) *api.Slug {
	return &value
}

func ptrToName(value string) *api.Name {
	return &value
}

func (f *fakeDataSourcesService) GetSnowflakeOnboarding(ctx context.Context, org, ds slug.Slug) (*datasourcessvc.SnowflakeOnboarding, error) {
	if f.GetSnowflakeOnboardingFn != nil {
		return f.GetSnowflakeOnboardingFn(ctx, org, ds)
	}
	return nil, datasourcessvc.ErrInvalidInput
}

func TestSnowflakeOnboardingPermissionAndScope(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			calls := 0
			app, sessions := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{GetSnowflakeOnboardingFn: func(_ context.Context, org, ds slug.Slug) (*datasourcessvc.SnowflakeOnboarding, error) {
					calls++
					require.Equal(t, slug.Slug("acme-corp"), org)
					require.Equal(t, slug.Slug("warehouse"), ds)
					return &datasourcessvc.SnowflakeOnboarding{IdentityState: "pending", Issuer: "https://issuer.test", Subject: "public-subject", Audience: "public-audience", SQL: "reviewable SQL", Diagnostic: "Provisioning"}, nil
				}},
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) { return allowed, nil }},
			})
			req := jsonRequest(t, http.MethodGet, "/organizations/acme-corp/data-sources/warehouse/snowflake-onboarding", nil)
			resp := authenticatedRequest(t, app, sessions, must.Slug("test-user"), req)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			if !allowed {
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
				require.Zero(t, calls)
				return
			}
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, 1, calls)
			var body api.SnowflakeOnboarding
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			require.Equal(t, "reviewable SQL", body.Sql)
			require.Equal(t, api.SnowflakeOnboardingIdentityStatePending, body.IdentityState)
			require.False(t, body.Active)
		})
	}
}

func TestPublicActiveUpdateIsSnowflakeOnly(t *testing.T) {
	for _, kind := range []model.DataSourceType{model.DataSourceTypePostgres, model.DataSourceTypeSnowflake} {
		t.Run(kind.String(), func(t *testing.T) {
			calls := 0
			ds := &datasourcessvc.DataSource{Slug: "warehouse", Type: kind, Config: json.RawMessage(`{}`)}
			app, sessions := newTestApp(t, testServerConfig{datasourcesService: &fakeDataSourcesService{
				FindBySlugFn: func(_ context.Context, org, source slug.Slug) (*datasourcessvc.DataSource, error) {
					require.Equal(t, slug.Slug("acme-corp"), org)
					require.Equal(t, ds.Slug, source)
					return ds, nil
				},
				UpdateFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, params *datasourcessvc.UpdateParams) (*datasourcessvc.DataSource, error) {
					calls++
					require.NotNil(t, params.IsActive)
					require.False(t, *params.IsActive)
					return ds, nil
				},
			}})
			request := jsonRequest(t, http.MethodPatch, "/organizations/acme-corp/data-sources/warehouse", map[string]bool{"is_active": false})
			response := authenticatedRequest(t, app, sessions, must.Slug("test-user"), request)
			defer func() { require.NoError(t, response.Body.Close()) }()
			if kind == model.DataSourceTypeSnowflake {
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.Equal(t, 1, calls)
			} else {
				require.Equal(t, http.StatusBadRequest, response.StatusCode)
				require.Zero(t, calls)
			}
		})
	}
}

func TestHTTPCredentialsRejectOAuthWithAPIKey(t *testing.T) {
	key := "synthetic-key"
	header := api.HTTPCredentialsApiKeyHeader("X-Api-Key")
	err := validateCredentialsByType(model.DataSourceTypeHTTP, api.HTTPCredentials{Oauth: &api.HTTPOAuthCredentials{}, ApiKey: &key, ApiKeyHeader: &header})
	require.ErrorContains(t, err, "cannot be combined")
	require.NotContains(t, err.Error(), key)
}

func TestGetHTTPDraftTenantIsolation(t *testing.T) {
	draft := json.RawMessage(`{"statement":{"type":"http","method":"GET","path":"/users/{{id}}"},"parameters":[{"name":"id","data_type":"text","is_required":true}]}`)
	for _, org := range []string{"acme-corp", "other-org"} {
		t.Run(org, func(t *testing.T) {
			app, store := newTestApp(t, testServerConfig{
				datasourcesService: &fakeDataSourcesService{FindBySlugFn: func(_ context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
					require.Equal(t, "acme-corp", orgSlug.String())
					require.Equal(t, "api-http", dsSlug.String())
					return &datasourcessvc.DataSource{Slug: dsSlug, Type: model.DataSourceTypeHTTP, Config: json.RawMessage(`{"base_url":"https://api.example.com"}`), Schema: datasourcessvc.Schema[datasourcessvc.HTTPSchemaPayload]{SchemaTypeValue: datasourcessvc.SchemaTypeHTTP, Type: "http", Payload: datasourcessvc.HTTPSchemaPayload{Operations: []datasourcessvc.SchemaOperation{{ID: "getUser", Method: "GET", Path: "/users/{id}", Draft: draft}}}}}, nil
				}},
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					return input.OrgSlug != nil && input.OrgSlug.String() == "acme-corp", nil
				}},
			})
			req := jsonRequest(t, http.MethodGet, "/organizations/"+org+"/data-sources/api-http", nil)
			resp := authenticatedRequest(t, app, store, must.Slug("test-user"), req)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			if org != "acme-corp" {
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
				return
			}
			require.Equal(t, http.StatusOK, resp.StatusCode)
			source := parseResponse[api.DataSource](t, resp)
			schema, err := source.Schema.AsHTTPSchemaResponse()
			require.NoError(t, err)
			require.NotNil(t, schema.Operations[0].Draft)
			require.Equal(t, "/users/{{id}}", schema.Operations[0].Draft.Statement.Path)
			require.True(t, *schema.Operations[0].Draft.Parameters[0].IsRequired)
		})
	}
}
