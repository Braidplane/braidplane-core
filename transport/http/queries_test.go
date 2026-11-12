package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	qrepo "github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

var testQueryData = json.RawMessage(
	`{"statement":"\"SELECT * FROM users\"","row_limit":1000,"timeout_seconds":30}`,
)

type unavailableQueryConnectorExecutor struct{}

func (unavailableQueryConnectorExecutor) ExecuteConnector(
	context.Context,
	queriessvc.ConnectorExecutionRequest,
) (*result.ExecutionResult, error) {
	return nil, xerror.ExecutionUnavailable().Detail("connector execution is unavailable in this test")
}

var testQuery = &queriessvc.Query{
	Slug:           must.Slug("get-users"),
	Name:           "Get Users",
	Description:    new("Fetches all users"),
	DataSourceSlug: must.Slug("prod-db"),
	Data:           testQueryData,
	Parameters:     []*queriessvc.QueryParameter{},
	CreatedAt:      time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	UpdatedAt:      time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
}

var testQueryWithParams = &queriessvc.Query{
	Slug:           must.Slug("get-user-by-id"),
	Name:           "Get User By ID",
	Description:    new("Fetches a user by ID"),
	DataSourceSlug: must.Slug("prod-db"),
	Data: json.RawMessage(
		`{"statement":"\"SELECT * FROM users WHERE id = :id\"","row_limit":1,"timeout_seconds":30}`,
	),
	Parameters: []*queriessvc.QueryParameter{
		{
			Slug:     must.Slug("id-param"),
			Name:     "id",
			DataType: queriessvc.ParameterDataTypeInteger,
		},
	},
	CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	UpdatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
}

func testQueryServiceExecutionLimits() queriessvc.ExecutionLimits {
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

func newCreateQueryReq() api.NewQuery {
	var stmt api.QueryStatement
	must.FnErr(stmt.FromPostgresStatement(api.PostgresStatement{
		Sql:  "SELECT * FROM users",
		Type: api.PostgresStatementTypePostgres,
	}))
	return api.NewQuery{
		Name:           "Get Users",
		Description:    new("Fetches all users"),
		Slug:           new("get-users"),
		QueryStatement: stmt,
	}
}

func newUpdateQueryReq() api.UpdateQuery {
	var stmt api.QueryStatement
	must.FnErr(stmt.FromPostgresStatement(api.PostgresStatement{
		Sql:  "SELECT * FROM users WHERE active = true",
		Type: api.PostgresStatementTypePostgres,
	}))
	return api.UpdateQuery{
		Name:           "Active Users",
		Description:    new("Active users only"),
		QueryStatement: stmt,
	}
}

func invalidExpectedInputContractNewQueryReq() api.NewQuery {
	req := newCreateQueryReq()
	validationMin := api.PipelineSchemaContractValidationBound{}
	must.FnErr(validationMin.FromPipelineSchemaContractValidationBound0(10))
	representation := api.PipelineSchemaContractRepresentation("rows")
	fields := []api.PipelineSchemaContractField{{
		Name:     "email",
		Type:     api.PipelineSchemaContractFieldType("string"),
		Required: true,
		Validations: &api.PipelineSchemaContractFieldValidations{
			Min: &validationMin,
		},
	}}
	req.ExpectedInputContract = &api.PipelineSchemaContract{
		Name:           "users_rows",
		Representation: &representation,
		Fields:         &fields,
	}
	return req
}

func invalidExpectedInputContractUpdateQueryReq() api.UpdateQuery {
	req := newUpdateQueryReq()
	validationMin := api.PipelineSchemaContractValidationBound{}
	must.FnErr(validationMin.FromPipelineSchemaContractValidationBound0(10))
	representation := api.PipelineSchemaContractRepresentation("rows")
	fields := []api.PipelineSchemaContractField{{
		Name:     "email",
		Type:     api.PipelineSchemaContractFieldType("string"),
		Required: true,
		Validations: &api.PipelineSchemaContractFieldValidations{
			Min: &validationMin,
		},
	}}
	req.ExpectedInputContract = &api.PipelineSchemaContract{
		Name:           "users_rows",
		Representation: &representation,
		Fields:         &fields,
	}
	return req
}

func unknownValidationKeyQueryBody(t *testing.T, body any) map[string]any {
	t.Helper()

	data, err := json.Marshal(body)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	raw["expected_input_contract"] = map[string]any{
		"name":           "users_rows",
		"representation": "rows",
		"fields": []map[string]any{{
			"name": "id",
			"type": "integer",
			"validations": map[string]any{
				"minimum": 1,
			},
		}},
	}
	return raw
}

func nullValidationRuleQueryBody(t *testing.T, body any) map[string]any {
	t.Helper()

	data, err := json.Marshal(body)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	raw["expected_input_contract"] = map[string]any{
		"name":           "users_rows",
		"representation": "rows",
		"fields": []map[string]any{{
			"name": "id",
			"type": "integer",
			"validations": map[string]any{
				"min": nil,
			},
		}},
	}
	return raw
}

func TestListQueries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		requestURL string
		listFn     func(ctx context.Context, orgSlug, dsSlug slug.Slug, req model.ListRequest, includeDeleted bool) (model.ListResponse[*queriessvc.Query], error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantCount  int
		wantErr    *apierror.Error
	}{
		{
			name: "ReturnsQueriesListAndStatus200",
			listFn: func(_ context.Context, _, _ slug.Slug, _ model.ListRequest, includeDeleted bool) (model.ListResponse[*queriessvc.Query], error) {
				if includeDeleted {
					return model.ListResponse[*queriessvc.Query]{}, fmt.Errorf("includeDeleted = true")
				}
				return model.ListResponse[*queriessvc.Query]{
					Items:      []*queriessvc.Query{testQuery, testQueryWithParams},
					HasMore:    false,
					NextCursor: nil,
				}, nil
			},
			wantStatus: http.StatusOK,
			wantCount:  2,
		},
		{
			name: "ReturnsEmptyListWhenNoQueries",
			listFn: func(_ context.Context, _, _ slug.Slug, _ model.ListRequest, _ bool) (model.ListResponse[*queriessvc.Query], error) {
				return model.ListResponse[*queriessvc.Query]{
					Items:      []*queriessvc.Query{},
					HasMore:    false,
					NextCursor: nil,
				}, nil
			},
			wantStatus: http.StatusOK,
			wantCount:  0,
		},
		{
			name:       "IncludesDeletedQueriesWhenRequested",
			requestURL: "/organizations/acme-corp/data-sources/prod-db/queries?include_deleted=true",
			listFn: func(_ context.Context, _, _ slug.Slug, _ model.ListRequest, includeDeleted bool) (model.ListResponse[*queriessvc.Query], error) {
				if !includeDeleted {
					return model.ListResponse[*queriessvc.Query]{}, fmt.Errorf("includeDeleted = false")
				}
				deletedAt := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
				deletedQuery := *testQuery
				deletedQuery.DeletedAt = &deletedAt
				return model.ListResponse[*queriessvc.Query]{
					Items:      []*queriessvc.Query{&deletedQuery},
					HasMore:    false,
					NextCursor: nil,
				}, nil
			},
			wantStatus: http.StatusOK,
			wantCount:  1,
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.Read && input.Resource == model.ResourceQueries {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("queries", "read"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				queriesService: &fakeQueriesService{
					ListByDataSourceFn: tc.listFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			requestURL := tc.requestURL
			if requestURL == "" {
				requestURL = "/organizations/acme-corp/data-sources/prod-db/queries"
			}
			req := jsonRequest(
				t,
				http.MethodGet,
				requestURL,
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

			if tc.wantCount >= 0 && tc.wantErr == nil {
				result := parseResponse[api.QueryListResponse](t, resp)
				assert.Len(t, result.Queries, tc.wantCount)
			}
		})
	}
}

func TestListQueries_Returns400WhenCursorMalformed(t *testing.T) {
	t.Parallel()

	queriesService, err := queriessvc.NewWithExecutionLimits(
		&testutil.FakeTransactionManager{},
		&testutil.FakeQueryRepository{
			ListByDataSourceFn: func(
				_ context.Context,
				_ postgres.DBTX,
				_ slug.Slug,
				_ slug.Slug,
				req model.ListRequest,
				_ bool,
			) ([]*qrepo.Query, error) {
				_, _, _, err := postgres.BuildCursorValues(req.Cursor, req.SortBy)
				if err != nil {
					return nil, err
				}
				return []*qrepo.Query{}, nil
			},
		},
		nil,
		unavailableQueryConnectorExecutor{},
		&fakeCredentialsService{},
		&testutil.FakeClock{},
		testQueryServiceExecutionLimits(),
		nil,
	)
	require.NoError(t, err)

	app, sessionStore := newTestApp(t, testServerConfig{
		queriesService: queriesService,
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme-corp/data-sources/prod-db/queries?cursor=not-a-timestamp_run-001",
		nil,
	)
	resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assertErrorResponse(
		t,
		resp,
		apierror.New(400, "CURSOR_INVALID_INPUT", "cursor invalid input"),
	)
}

func TestCreateQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       any
		createFn   func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, params *queriessvc.CreateParams) (*queriessvc.Query, error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
	}{
		{
			name: "CreatesQueryAndReturns201",
			body: newCreateQueryReq(),
			createFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.CreateParams) (*queriessvc.Query, error) {
				return testQuery, nil
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "Returns409WhenSlugAlreadyExists",
			body: newCreateQueryReq(),
			createFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.CreateParams) (*queriessvc.Query, error) {
				return nil, queriessvc.ErrSlugTaken
			},
			wantStatus: http.StatusConflict,
			wantErr:    apierror.New(409, "QUERY_ALREADY_EXISTS", "query already exists"),
		},
		{
			name: "Returns400WhenInvalidInput",
			body: newCreateQueryReq(),
			createFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.CreateParams) (*queriessvc.Query, error) {
				return nil, queriessvc.ErrInvalidInput
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "VALIDATION_FAILED", "validation failed"),
		},
		{
			name: "Returns400WhenExpectedInputContractValidationIsInvalid",
			body: invalidExpectedInputContractNewQueryReq(),
			createFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.CreateParams) (*queriessvc.Query, error) {
				t.Fatal("query service must not be called for invalid expected input contract validations")
				return nil, errFakeQueriesServiceFnNotSet
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "Returns400WhenExpectedInputContractValidationKeyIsUnknown",
			body: unknownValidationKeyQueryBody(t, newCreateQueryReq()),
			createFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.CreateParams) (*queriessvc.Query, error) {
				t.Fatal("query service must not be called for unknown expected input contract validation keys")
				return nil, errFakeQueriesServiceFnNotSet
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "Returns400WhenExpectedInputContractValidationRuleIsNull",
			body: nullValidationRuleQueryBody(t, newCreateQueryReq()),
			createFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.CreateParams) (*queriessvc.Query, error) {
				t.Fatal("query service must not be called for null expected input contract validation rules")
				return nil, errFakeQueriesServiceFnNotSet
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "Returns400WhenInvalidRequestBody",
			body:       "not-json",
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			body: newCreateQueryReq(),
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.Create && input.Resource == model.ResourceQueries {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("queries", "create"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				queriesService: &fakeQueriesService{
					CreateFn: tc.createFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodPost,
				"/organizations/acme-corp/data-sources/prod-db/queries",
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

			if tc.wantStatus == http.StatusCreated {
				result := parseResponse[api.Query](t, resp)
				assert.Equal(t, "get-users", result.Slug)
				assert.Equal(t, "Get Users", result.Name)
			}
		})
	}
}

func TestCreateQuery_MapsExpectedInputContract(t *testing.T) {
	t.Parallel()

	reqBody := newCreateQueryReq()
	representation := api.PipelineSchemaContractRepresentation("rows")
	validationMin := api.PipelineSchemaContractValidationBound{}
	require.NoError(t, validationMin.FromPipelineSchemaContractValidationBound0(1))
	activeLiteral := api.PipelineSchemaContractValidationLiteral{}
	require.NoError(t, activeLiteral.FromPipelineSchemaContractValidationLiteral0("active"))
	disabledLiteral := api.PipelineSchemaContractValidationLiteral{}
	require.NoError(t, disabledLiteral.FromPipelineSchemaContractValidationLiteral0("disabled"))
	enumValues := []api.PipelineSchemaContractValidationLiteral{activeLiteral, disabledLiteral}
	uniqueValidation := true
	fields := []api.PipelineSchemaContractField{{
		Name:     "status",
		Type:     api.PipelineSchemaContractFieldType("string"),
		Required: true,
		Validations: &api.PipelineSchemaContractFieldValidations{
			Enum:   &enumValues,
			Unique: &uniqueValidation,
		},
	}, {
		Name:     "id",
		Type:     api.PipelineSchemaContractFieldType("integer"),
		Required: true,
		Validations: &api.PipelineSchemaContractFieldValidations{
			Min: &validationMin,
		},
	}}
	reqBody.ExpectedInputContract = &api.PipelineSchemaContract{
		Name:           "users_rows",
		Representation: &representation,
		Fields:         &fields,
	}

	returnedQuery := &queriessvc.Query{
		Slug:           must.Slug("get-users"),
		Name:           "Get Users",
		Description:    new("Fetches all users"),
		DataSourceSlug: must.Slug("prod-db"),
		Data:           testQueryData,
		ExpectedInputContract: &payload.Contract{
			Name:           "users_rows",
			Representation: model.ContractRepresentationRows,
			Fields: []payload.ContractField{{
				Name:     "status",
				Type:     model.ContractFieldTypeValueString,
				Required: true,
				Validations: &payload.ContractFieldValidations{
					Enum:   []any{"active", "disabled"},
					Unique: &uniqueValidation,
				},
			}, {
				Name:     "id",
				Type:     model.ContractFieldTypeValueInteger,
				Required: true,
				Validations: &payload.ContractFieldValidations{
					Min: float64(1),
				},
			}},
		},
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	app, sessionStore := newTestApp(t, testServerConfig{
		queriesService: &fakeQueriesService{
			CreateFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, params *queriessvc.CreateParams) (*queriessvc.Query, error) {
				require.NotNil(t, params.ExpectedInputContract)
				assert.Equal(t, "users_rows", params.ExpectedInputContract.Name)
				assert.Equal(
					t,
					model.ContractRepresentationRows,
					params.ExpectedInputContract.Representation,
				)
				require.Len(t, params.ExpectedInputContract.Fields, 2)
				assert.Equal(t, "status", params.ExpectedInputContract.Fields[0].Name)
				require.NotNil(t, params.ExpectedInputContract.Fields[0].Validations)
				assert.Equal(t, []any{"active", "disabled"}, params.ExpectedInputContract.Fields[0].Validations.Enum)
				require.NotNil(t, params.ExpectedInputContract.Fields[0].Validations.Unique)
				assert.True(t, *params.ExpectedInputContract.Fields[0].Validations.Unique)
				assert.Equal(t, "id", params.ExpectedInputContract.Fields[1].Name)
				require.NotNil(t, params.ExpectedInputContract.Fields[1].Validations)
				assert.Equal(t, json.Number("1"), params.ExpectedInputContract.Fields[1].Validations.Min)
				return returnedQuery, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPost,
		"/organizations/acme-corp/data-sources/prod-db/queries",
		reqBody,
	)
	resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	body := parseResponse[api.Query](t, resp)
	require.NotNil(t, body.ExpectedInputContract)
	require.NotNil(t, body.ExpectedInputContract.Fields)
	assert.Equal(t, "users_rows", body.ExpectedInputContract.Name)
	assert.Equal(
		t,
		api.PipelineSchemaContractFieldType("string"),
		(*body.ExpectedInputContract.Fields)[0].Type,
	)
	require.NotNil(t, (*body.ExpectedInputContract.Fields)[0].Validations)
	require.NotNil(t, (*body.ExpectedInputContract.Fields)[0].Validations.Enum)
	require.Len(t, *(*body.ExpectedInputContract.Fields)[0].Validations.Enum, 2)
	gotEnumValue, err := (*(*body.ExpectedInputContract.Fields)[0].Validations.Enum)[0].AsPipelineSchemaContractValidationLiteral0()
	require.NoError(t, err)
	assert.Equal(t, "active", gotEnumValue)
	require.NotNil(t, (*body.ExpectedInputContract.Fields)[0].Validations.Unique)
	assert.True(t, *(*body.ExpectedInputContract.Fields)[0].Validations.Unique)
	require.NotNil(t, (*body.ExpectedInputContract.Fields)[1].Validations)
	require.NotNil(t, (*body.ExpectedInputContract.Fields)[1].Validations.Min)
	gotMin, err := (*body.ExpectedInputContract.Fields)[1].Validations.Min.AsPipelineSchemaContractValidationBound0()
	require.NoError(t, err)
	assert.Equal(t, int64(1), gotMin)
}

func TestAPISchemaContractToPipeline_AllowsFileWithoutFields(t *testing.T) {
	t.Parallel()

	representation := api.PipelineSchemaContractRepresentation("file")
	contract, ok, err := apiSchemaContractToPipeline(&api.PipelineSchemaContract{
		Name:           "file_input",
		Representation: &representation,
	})

	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, contract)
	require.Equal(t, model.ContractRepresentationFile, contract.Representation)
	require.Empty(t, contract.Fields)
}

func TestAPISchemaContractToPipeline_PreservesIntegerValidationPrecision(t *testing.T) {
	t.Parallel()

	minimum := api.PipelineSchemaContractValidationBound{}
	require.NoError(t, minimum.UnmarshalJSON([]byte(`9007199254740993`)))
	enumLiteral := api.PipelineSchemaContractValidationLiteral{}
	require.NoError(t, enumLiteral.UnmarshalJSON([]byte(`9007199254740993`)))
	enumValues := []api.PipelineSchemaContractValidationLiteral{enumLiteral}
	fields := []api.PipelineSchemaContractField{{
		Name: "id",
		Type: api.PipelineSchemaContractFieldType("integer"),
		Validations: &api.PipelineSchemaContractFieldValidations{
			Enum: &enumValues,
			Min:  &minimum,
		},
	}}

	contract, ok, err := apiSchemaContractToPipeline(&api.PipelineSchemaContract{
		Name:   "users_rows",
		Fields: &fields,
	})

	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, contract.Fields, 1)
	require.NotNil(t, contract.Fields[0].Validations)
	require.Equal(t, json.Number("9007199254740993"), contract.Fields[0].Validations.Min)
	require.Equal(t, []any{json.Number("9007199254740993")}, contract.Fields[0].Validations.Enum)
}

func TestAPISchemaContractToPipeline_RejectsNonFileWithoutFields(t *testing.T) {
	t.Parallel()

	representation := api.PipelineSchemaContractRepresentation("rows")
	_, _, err := apiSchemaContractToPipeline(&api.PipelineSchemaContract{
		Name:           "rows_input",
		Representation: &representation,
	})

	require.Error(t, err)
	require.ErrorIs(t, err, xerror.InvalidInput(xerror.Query))
	require.ErrorContains(t, err, "fields are required")
}

func TestGetQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		findFn     func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug) (*queriessvc.Query, error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
	}{
		{
			name: "ReturnsQueryAndStatus200",
			findFn: func(_ context.Context, _, _, _ slug.Slug) (*queriessvc.Query, error) {
				return testQuery, nil
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "Returns404WhenQueryNotFound",
			findFn: func(_ context.Context, _, _, _ slug.Slug) (*queriessvc.Query, error) {
				return nil, queriessvc.ErrQueryNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "QUERY_NOT_FOUND", "query not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.Read && input.Resource == model.ResourceQueries {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("queries", "read"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				queriesService: &fakeQueriesService{
					FindBySlugFn: tc.findFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				"/organizations/acme-corp/data-sources/prod-db/queries/get-users",
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

			if tc.wantStatus == http.StatusOK {
				result := parseResponse[api.Query](t, resp)
				assert.Equal(t, "get-users", result.Slug)
				assert.Equal(t, "Get Users", result.Name)
				assert.Equal(t, "prod-db", result.DataSourceSlug)
			}
		})
	}
}

func TestUpdateQuery(t *testing.T) {
	t.Parallel()

	updatedQuery := &queriessvc.Query{
		Slug:           must.Slug("get-users"),
		Name:           "Active Users",
		Description:    new("Active users only"),
		DataSourceSlug: must.Slug("prod-db"),
		Data: json.RawMessage(
			`{"statement":"\"SELECT * FROM users WHERE active = true\"","row_limit":1000,"timeout_seconds":30}`,
		),
		Parameters: []*queriessvc.QueryParameter{},
		CreatedAt:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name       string
		body       any
		updateFn   func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor, params *queriessvc.UpdateParams) (*queriessvc.Query, error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
	}{
		{
			name: "UpdatesQueryAndReturns200",
			body: newUpdateQueryReq(),
			updateFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.UpdateParams) (*queriessvc.Query, error) {
				return updatedQuery, nil
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "Returns404WhenQueryNotFound",
			body: newUpdateQueryReq(),
			updateFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.UpdateParams) (*queriessvc.Query, error) {
				return nil, queriessvc.ErrQueryNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "QUERY_NOT_FOUND", "query not found"),
		},
		{
			name: "Returns400WhenInvalidInput",
			body: newUpdateQueryReq(),
			updateFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.UpdateParams) (*queriessvc.Query, error) {
				return nil, queriessvc.ErrInvalidInput
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "VALIDATION_FAILED", "validation failed"),
		},
		{
			name: "Returns400WhenExpectedInputContractValidationIsInvalid",
			body: invalidExpectedInputContractUpdateQueryReq(),
			updateFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.UpdateParams) (*queriessvc.Query, error) {
				t.Fatal("query service must not be called for invalid expected input contract validations")
				return nil, errFakeQueriesServiceFnNotSet
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "Returns400WhenExpectedInputContractValidationKeyIsUnknown",
			body: unknownValidationKeyQueryBody(t, newUpdateQueryReq()),
			updateFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.UpdateParams) (*queriessvc.Query, error) {
				t.Fatal("query service must not be called for unknown expected input contract validation keys")
				return nil, errFakeQueriesServiceFnNotSet
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "Returns400WhenExpectedInputContractValidationRuleIsNull",
			body: nullValidationRuleQueryBody(t, newUpdateQueryReq()),
			updateFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ *queriessvc.UpdateParams) (*queriessvc.Query, error) {
				t.Fatal("query service must not be called for null expected input contract validation rules")
				return nil, errFakeQueriesServiceFnNotSet
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "Returns400WhenInvalidRequestBody",
			body:       "not-json",
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			body: newUpdateQueryReq(),
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.Update && input.Resource == model.ResourceQueries {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("queries", "update"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				queriesService: &fakeQueriesService{
					UpdateFn: tc.updateFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodPut,
				"/organizations/acme-corp/data-sources/prod-db/queries/get-users",
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

			if tc.wantStatus == http.StatusOK {
				result := parseResponse[api.Query](t, resp)
				assert.Equal(t, "Active Users", result.Name)
			}
		})
	}
}

func TestUpdateQuery_MapsExpectedInputContract(t *testing.T) {
	t.Parallel()

	reqBody := newUpdateQueryReq()
	representation := api.PipelineSchemaContractRepresentation("rows")
	fields := []api.PipelineSchemaContractField{{
		Name:     "id",
		Type:     api.PipelineSchemaContractFieldType("integer"),
		Required: true,
	}}
	reqBody.ExpectedInputContract = &api.PipelineSchemaContract{
		Name:           "users_rows",
		Representation: &representation,
		Fields:         &fields,
	}

	updatedQuery := &queriessvc.Query{
		Slug:           must.Slug("get-users"),
		Name:           "Active Users",
		Description:    new("Active users only"),
		DataSourceSlug: must.Slug("prod-db"),
		Data: json.RawMessage(
			`{"statement":"\"SELECT * FROM users WHERE active = true\"","row_limit":1000,"timeout_seconds":30}`,
		),
		ExpectedInputContract: &payload.Contract{
			Name:           "users_rows",
			Representation: model.ContractRepresentationRows,
			Fields: []payload.ContractField{{
				Name:     "id",
				Type:     model.ContractFieldTypeValueInteger,
				Required: true,
			}},
		},
		Parameters: []*queriessvc.QueryParameter{},
		CreatedAt:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
	}

	app, sessionStore := newTestApp(t, testServerConfig{
		queriesService: &fakeQueriesService{
			UpdateFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, params *queriessvc.UpdateParams) (*queriessvc.Query, error) {
				require.NotNil(t, params.ExpectedInputContract)
				assert.Equal(t, "users_rows", params.ExpectedInputContract.Name)
				assert.Equal(
					t,
					model.ContractRepresentationRows,
					params.ExpectedInputContract.Representation,
				)
				require.Len(t, params.ExpectedInputContract.Fields, 1)
				assert.Equal(t, "id", params.ExpectedInputContract.Fields[0].Name)
				return updatedQuery, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/organizations/acme-corp/data-sources/prod-db/queries/get-users",
		reqBody,
	)
	resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := parseResponse[api.Query](t, resp)
	require.NotNil(t, body.ExpectedInputContract)
	require.NotNil(t, body.ExpectedInputContract.Fields)
	assert.Equal(t, "users_rows", body.ExpectedInputContract.Name)
	assert.Equal(
		t,
		api.PipelineSchemaContractFieldType("integer"),
		(*body.ExpectedInputContract.Fields)[0].Type,
	)
}

func TestDeleteQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		deleteFn   func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
	}{
		{
			name: "SoftDeletesQueryWithAuthenticatedActorAndReturns204",
			deleteFn: func(_ context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error {
				assert.Equal(t, must.Slug("acme-corp"), orgSlug)
				assert.Equal(t, must.Slug("prod-db"), dsSlug)
				assert.Equal(t, must.Slug("get-users"), querySlug)
				assert.Equal(t, model.EventActorTypeUser, actor.Type)
				require.NotNil(t, actor.Slug)
				assert.Equal(t, must.Slug("test-user"), *actor.Slug)
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "Returns404WhenQueryNotFound",
			deleteFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor) error {
				return queriessvc.ErrQueryNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "QUERY_NOT_FOUND", "query not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.Delete && input.Resource == model.ResourceQueries {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("queries", "delete"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				queriesService: &fakeQueriesService{
					DeleteFn: tc.deleteFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodDelete,
				"/organizations/acme-corp/data-sources/prod-db/queries/get-users",
				nil,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				assertErrorResponse(t, resp, tc.wantErr)
				if tc.name == "Returns422WhenQueryExecutionFails" {
					errResp := parseResponse[apierror.ErrorResponse](t, resp)
					require.NotNil(t, errResp.Error)
					require.NotEmpty(t, errResp.Error.DetailedError)
					assert.Contains(t, errResp.Error.DetailedError, "syntax error at or near FROM")
				}
			}
		})
	}
}

func TestRestoreQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		restoreFn  func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
	}{
		{
			name: "RestoresQueryWithAuthenticatedActorAndReturns204",
			restoreFn: func(_ context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error {
				assert.Equal(t, must.Slug("acme-corp"), orgSlug)
				assert.Equal(t, must.Slug("prod-db"), dsSlug)
				assert.Equal(t, must.Slug("get-users"), querySlug)
				assert.Equal(t, model.EventActorTypeUser, actor.Type)
				require.NotNil(t, actor.Slug)
				assert.Equal(t, must.Slug("test-user"), *actor.Slug)
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "Returns404WhenQueryNotFound",
			restoreFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor) error {
				return queriessvc.ErrQueryNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "QUERY_NOT_FOUND", "query not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionRestore && input.Resource == model.ResourceQueries {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("queries", "restore"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				queriesService: &fakeQueriesService{
					RestoreFn: tc.restoreFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodPost,
				"/organizations/acme-corp/data-sources/prod-db/queries/get-users/restore",
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

func TestPurgeQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		purgeFn    func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
	}{
		{
			name: "PurgesDeletedQueryWithAuthenticatedActorAndReturns204",
			purgeFn: func(_ context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error {
				assert.Equal(t, must.Slug("acme-corp"), orgSlug)
				assert.Equal(t, must.Slug("prod-db"), dsSlug)
				assert.Equal(t, must.Slug("get-users"), querySlug)
				assert.Equal(t, model.EventActorTypeUser, actor.Type)
				require.NotNil(t, actor.Slug)
				assert.Equal(t, must.Slug("test-user"), *actor.Slug)
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "Returns409WhenQueryIsActive",
			purgeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor) error {
				return queriessvc.ErrQueryNotDeleted
			},
			wantStatus: http.StatusConflict,
			wantErr: apierror.New(
				409,
				"QUERY_NOT_DELETED",
				"query must be deleted before purge",
			),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.ActionPurge && input.Resource == model.ResourceQueries {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("queries", "purge"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				queriesService: &fakeQueriesService{
					PurgeFn: tc.purgeFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodDelete,
				"/organizations/acme-corp/data-sources/prod-db/queries/get-users/purge",
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

func TestExecuteQuery(t *testing.T) {
	t.Parallel()

	sqlResult := result.NewSQLResult(
		"postgres",
		&result.SQLResult{
			Rows:        []map[string]any{{"id": float64(1), "name": "Alice"}},
			ColumnNames: []string{"id", "name"},
			RowCount:    1,
		},
		42,
		nil,
	)

	tests := []struct {
		name       string
		body       any
		executeFn  func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor, paramValues map[string]any) (*result.ExecutionResult, error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
	}{
		{
			name: "ExecutesQueryAndReturns200",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return sqlResult, nil
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "ExecutesQueryWithParametersAndReturns200",
			body: api.ExecuteQueryRequest{
				Parameters: map[string]any{"id": float64(1)},
			},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, params map[string]any) (*result.ExecutionResult, error) {
				return sqlResult, nil
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "Returns404WhenQueryNotFound",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, queriessvc.ErrQueryNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "QUERY_NOT_FOUND", "query not found"),
		},
		{
			name: "Returns400WhenParameterValidationFails",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, queriessvc.ErrParameterValidationFailed
			},
			wantStatus: http.StatusBadRequest,
			wantErr: apierror.New(
				400,
				"PARAMETER_VALIDATION_FAILED",
				"parameter validation failed",
			),
		},
		{
			name: "Returns408WhenQueryTimesOut",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, queriessvc.ErrQueryTimeout
			},
			wantStatus: http.StatusRequestTimeout,
			wantErr:    apierror.New(408, "QUERY_TIMEOUT", "query timeout"),
		},
		{
			name: "Returns400WhenHTTPAcknowledgementRejected",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, xerror.HTTPAcknowledgementRejected().WithPublicDetail("Rejection does not prove rollback; verify remote state before retrying.")
			},
			wantStatus: http.StatusBadRequest,
			wantErr: apierror.New(400, "HTTP_ACKNOWLEDGEMENT_REJECTED", "HTTP acknowledgement rejected").
				WithDetailedError("Rejection does not prove rollback; verify remote state before retrying."),
		},
		{
			name: "Returns422WhenHTTPAcknowledgementUncertain",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, xerror.HTTPAcknowledgementUncertain().WithPublicDetail("Missing evidence; verify remote state before retrying a write.")
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr: apierror.New(422, "HTTP_ACKNOWLEDGEMENT_UNCERTAIN", "HTTP acknowledgement uncertain").
				WithDetailedError("Missing evidence; verify remote state before retrying a write."),
		},
		{
			name: "Returns422WhenDataSourceUnavailable",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, queriessvc.ErrDataSourceUnavailable
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr:    apierror.New(422, "DATA_SOURCE_UNAVAILABLE", "data source unavailable"),
		},
		{
			name: "Returns400WhenHTTPRowLimitExceeded",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, xerror.ExecutionRowLimitExceeded().WithPublicDetail("HTTP request completed with status 201; verify remote state before retrying a write.")
			},
			wantStatus: http.StatusBadRequest,
			wantErr: apierror.New(400, "EXECUTION_ROW_LIMIT_EXCEEDED", "execution row limit exceeded").
				WithDetailedError("HTTP request completed with status 201; verify remote state before retrying a write."),
		},

		{
			name: "Returns422WhenQueryExecutionFails",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, xerror.Join(
					queriessvc.ErrQueryExecutionFailed,
					fmt.Errorf("syntax error at or near FROM"),
				)
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr:    apierror.New(422, "QUERY_EXECUTION_FAILED", "Query execution failed."),
		},
		{
			name: "ReturnsSafeWorksheetHeaderDiagnosticFromIsolatedWorker",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, xerror.Wrapf("%w: connector execution failed: %w", queriessvc.ErrDataSourceUnavailable,
					xerror.ExecutionUnavailable().WithPublicDetail("Headers must be nonblank unique strings"))
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr: apierror.New(422, "DATA_SOURCE_UNAVAILABLE", "data source unavailable").
				WithDetailedError("Headers must be nonblank unique strings"),
		},
		{
			name: "ReturnsSafeWorksheetHeaderDiagnosticWhenExecutionFails",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, queriessvc.ErrQueryExecutionFailed.With(
					xerror.ValidationFailed().WithPublicDetail("Headers must be nonblank unique strings"),
				)
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr:    apierror.New(422, "QUERY_EXECUTION_FAILED", "Headers must be nonblank unique strings"),
		},
		{
			name: "Returns422WhenUnsupportedDataSourceType",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, queriessvc.ErrUnsupportedDataSourceType
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantErr:    apierror.New(422, "DATA_SOURCE_UNSUPPORTED", "data source unsupported"),
		},
		{
			name: "Returns403WhenDataSourceReadOnly",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				return nil, datasourcessvc.ErrDataSourceReadOnly
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.New(403, "DATA_SOURCE_READ_ONLY", "data source is read-only"),
		},
		{
			name: "Returns500WhenExecutionResultIsNil",
			body: api.ExecuteQueryRequest{},
			executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any) (*result.ExecutionResult, error) {
				//nolint:nilnil // intentionally simulate invalid nil-result contract coverage
				return nil, nil
			},
			wantStatus: http.StatusInternalServerError,
			wantErr:    apierror.InternalServerError(),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			body: api.ExecuteQueryRequest{},
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.Execute && input.Resource == model.ResourceQueries {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("queries", "execute"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				queriesService: &fakeQueriesService{
					ExecuteFn: tc.executeFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodPost,
				"/organizations/acme-corp/data-sources/prod-db/queries/get-users/execute",
				tc.body,
			)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantErr != nil {
				if tc.name == "Returns422WhenQueryExecutionFails" {
					errResp := parseResponse[apierror.ErrorResponse](t, resp)
					require.NotNil(t, errResp.Error)
					assert.Equal(t, tc.wantErr.Code, errResp.Error.Code)
					assert.Equal(t, tc.wantErr.Message, errResp.Error.Message)
					assert.Empty(t, errResp.Error.DetailedError)
				} else {
					assertErrorResponse(t, resp, tc.wantErr)
				}
			}
		})
	}
}

func TestListQueryExecutions(t *testing.T) {
	t.Parallel()

	buildExecutionResultJSON := func(t *testing.T, rowID float64, name string) json.RawMessage {
		t.Helper()

		sqlResult := api.SQLExecutionResult{
			Type:       "postgres",
			DurationMs: 12,
			Success:    true,
		}
		sqlResult.Payload.Rows = []map[string]any{{"id": rowID, "name": name}}
		sqlResult.Payload.RowCount = 1
		sqlResult.Payload.ColumnNames = &[]string{"id", "name"}

		var executionResult api.QueryExecutionResult
		require.NoError(t, executionResult.FromSQLExecutionResult(sqlResult))

		resultBytes, err := json.Marshal(executionResult)
		require.NoError(t, err)

		return resultBytes
	}

	tests := []struct {
		name       string
		listFn     func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, req model.ListRequest) (model.ListResponse[*queriessvc.QueryExecution], error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
		wantCount  int
	}{
		{
			name: "ReturnsExecutionsAndStatus200",
			listFn: func(_ context.Context, _, _, _ slug.Slug, _ model.ListRequest) (model.ListResponse[*queriessvc.QueryExecution], error) {
				return model.ListResponse[*queriessvc.QueryExecution]{
					Items: []*queriessvc.QueryExecution{
						{
							Slug:       must.Slug("exec-1"),
							Status:     "success",
							Result:     buildExecutionResultJSON(t, float64(1), "Alice"),
							ExecutedAt: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC),
						},
						{
							Slug:       must.Slug("exec-2"),
							Status:     "success",
							Result:     buildExecutionResultJSON(t, float64(2), "Bob"),
							ExecutedAt: time.Date(2025, 1, 3, 10, 1, 0, 0, time.UTC),
						},
					},
					HasMore:    false,
					NextCursor: nil,
				}, nil
			},
			wantStatus: http.StatusOK,
			wantCount:  2,
		},
		{
			name: "Returns404WhenQueryNotFound",
			listFn: func(_ context.Context, _, _, _ slug.Slug, _ model.ListRequest) (model.ListResponse[*queriessvc.QueryExecution], error) {
				return model.ListResponse[*queriessvc.QueryExecution]{}, queriessvc.ErrQueryNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "QUERY_NOT_FOUND", "query not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.Read && input.Resource == model.ResourceQueries {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("queries", "read"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				queriesService: &fakeQueriesService{
					ListExecutionsFn: tc.listFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				"/organizations/acme-corp/data-sources/prod-db/queries/get-users/executions",
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

			if tc.wantStatus == http.StatusOK {
				result := parseResponse[api.QueryExecutionListResponse](t, resp)
				assert.Len(t, result.Executions, tc.wantCount)
			}
		})
	}
}

func TestGetQueryExecution(t *testing.T) {
	t.Parallel()

	buildExecutionResultJSON := func(t *testing.T) json.RawMessage {
		t.Helper()

		sqlResult := api.SQLExecutionResult{
			Type:       "postgres",
			DurationMs: 8,
			Success:    true,
		}
		sqlResult.Payload.Rows = []map[string]any{{"id": float64(1), "name": "Alice"}}
		sqlResult.Payload.RowCount = 1
		sqlResult.Payload.ColumnNames = &[]string{"id", "name"}

		var executionResult api.QueryExecutionResult
		require.NoError(t, executionResult.FromSQLExecutionResult(sqlResult))

		resultBytes, err := json.Marshal(executionResult)
		require.NoError(t, err)

		return resultBytes
	}

	tests := []struct {
		name       string
		getFn      func(ctx context.Context, orgSlug, dsSlug, querySlug, executionSlug slug.Slug) (*queriessvc.QueryExecution, error)
		authFn     func(ctx context.Context, input authorization.Input) (bool, error)
		wantStatus int
		wantErr    *apierror.Error
	}{
		{
			name: "ReturnsExecutionAndStatus200",
			getFn: func(_ context.Context, _, _, _, _ slug.Slug) (*queriessvc.QueryExecution, error) {
				return &queriessvc.QueryExecution{
					Slug:       must.Slug("exec-1"),
					Status:     "success",
					Result:     buildExecutionResultJSON(t),
					ExecutedAt: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC),
				}, nil
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "Returns404WhenQueryNotFound",
			getFn: func(_ context.Context, _, _, _, _ slug.Slug) (*queriessvc.QueryExecution, error) {
				return nil, queriessvc.ErrQueryNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "QUERY_NOT_FOUND", "query not found"),
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.Read && input.Resource == model.ResourceQueries {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
			wantErr:    apierror.Forbidden("queries", "read"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				queriesService: &fakeQueriesService{
					GetExecutionFn: tc.getFn,
				},
				authService: fakeAuthService{
					CanFn: tc.authFn,
				},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				"/organizations/acme-corp/data-sources/prod-db/queries/get-users/executions/exec-1",
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

			if tc.wantStatus == http.StatusOK {
				result := parseResponse[api.QueryExecutionRecord](t, resp)
				assert.Equal(t, "exec-1", result.Slug)
				assert.Equal(t, "success", result.Status)
			}
		})
	}
}

func TestGetQueryExecutionSummary(t *testing.T) {
	t.Parallel()

	executedAt := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		summaryFn  func(context.Context, slug.Slug) (*queriessvc.QueryExecutionIdentity, error)
		authFn     func(context.Context, authorization.Input) (bool, error)
		wantStatus int
	}{
		{
			name: "ReturnsRecordedSuccessMetadata",
			summaryFn: func(_ context.Context, gotOrg slug.Slug) (*queriessvc.QueryExecutionIdentity, error) {
				require.Equal(t, must.Slug("acme-corp"), gotOrg)
				return &queriessvc.QueryExecutionIdentity{
					ExecutionSlug:  must.Slug("orders-exec-1"),
					DataSourceSlug: must.Slug("warehouse"),
					QuerySlug:      must.Slug("orders"),
					ExecutedAt:     executedAt,
				}, nil
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "ReturnsNullWhenNoRecordedSuccess",
			summaryFn: func(context.Context, slug.Slug) (*queriessvc.QueryExecutionIdentity, error) {
				return nil, nil //nolint:nilnil // Exercise the valid empty-history response.
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "Returns403WhenAuthorizationDenied",
			authFn: func(_ context.Context, input authorization.Input) (bool, error) {
				if input.Action == model.Read && input.Resource == model.ResourceQueries {
					return false, nil
				}
				return true, nil
			},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, sessionStore := newTestApp(t, testServerConfig{
				queriesService: &fakeQueriesService{GetExecutionSummaryFn: tc.summaryFn},
				authService:    fakeAuthService{CanFn: tc.authFn},
			})

			req := jsonRequest(t, http.MethodGet, "/organizations/acme-corp/query-execution-summary", nil)
			resp := authenticatedRequest(t, app, sessionStore, must.Slug("test-user"), req)
			defer func() { require.NoError(t, resp.Body.Close()) }()

			require.Equal(t, tc.wantStatus, resp.StatusCode)
			if tc.wantStatus == http.StatusForbidden {
				assertErrorResponse(t, resp, apierror.Forbidden("queries", "read"))
				return
			}

			body := parseResponse[api.QueryExecutionSummary](t, resp)
			if tc.name == "ReturnsNullWhenNoRecordedSuccess" {
				require.Nil(t, body.SuccessfulExecution)
				return
			}
			require.NotNil(t, body.SuccessfulExecution)
			assert.Equal(t, "orders-exec-1", body.SuccessfulExecution.ExecutionSlug)
			assert.Equal(t, "warehouse", body.SuccessfulExecution.DataSourceSlug)
			assert.Equal(t, "orders", body.SuccessfulExecution.QuerySlug)
			assert.Equal(t, executedAt, body.SuccessfulExecution.ExecutedAt)
		})
	}
}

func TestApiParamsToService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		params    *[]api.QueryParameter
		wantLen   int
		wantCheck func(t *testing.T, result []*queriessvc.QueryParameter)
	}{
		{
			name:    "NilParamsReturnsNil",
			params:  nil,
			wantLen: 0,
			wantCheck: func(t *testing.T, result []*queriessvc.QueryParameter) {
				assert.Nil(t, result)
			},
		},
		{
			name: "ConvertsAllFields",
			params: &[]api.QueryParameter{
				{
					Slug:         new("user-id"),
					Name:         "User ID",
					DataType:     api.QueryParameterDataTypeText,
					IsRequired:   new(true),
					DefaultValue: new("default-val"),
					Description:  new("The user identifier"),
				},
			},
			wantLen: 1,
			wantCheck: func(t *testing.T, result []*queriessvc.QueryParameter) {
				p := result[0]
				assert.Equal(t, "user-id", p.Slug.String())
				assert.Equal(t, "User ID", p.Name)
				assert.Equal(t, queriessvc.ParameterDataTypeText, p.DataType)
				assert.True(t, p.IsRequired)
				require.NotNil(t, p.DefaultValue)
				assert.Equal(t, "default-val", *p.DefaultValue)
				require.NotNil(t, p.Description)
				assert.Equal(t, "The user identifier", *p.Description)
			},
		},
		{
			name: "IsRequiredDefaultsToFalse",
			params: &[]api.QueryParameter{
				{
					Name:     "Optional Param",
					DataType: api.QueryParameterDataTypeInteger,
				},
			},
			wantLen: 1,
			wantCheck: func(t *testing.T, result []*queriessvc.QueryParameter) {
				assert.False(t, result[0].IsRequired)
			},
		},
		{
			name: "InvalidSlugIsIgnored",
			params: &[]api.QueryParameter{
				{
					Slug:     new("!!!invalid"),
					Name:     "Bad Slug Param",
					DataType: api.QueryParameterDataTypeText,
				},
			},
			wantLen: 1,
			wantCheck: func(t *testing.T, result []*queriessvc.QueryParameter) {
				assert.Empty(t, result[0].Slug.String())
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := apiParamsToService(tc.params)
			if tc.params == nil {
				tc.wantCheck(t, result)
				return
			}
			assert.Len(t, result, tc.wantLen)
			if tc.wantCheck != nil {
				tc.wantCheck(t, result)
			}
		})
	}
}

func TestExecutionResultToHTTP_MongoDB(t *testing.T) {
	t.Parallel()

	mongoResult := result.NewMongoDBResult(
		&result.MongoDBResult{
			OperationType: "find",
			Documents:     []map[string]any{{"_id": "abc123", "name": "Alice"}},
			DocumentCount: new(1),
			MatchedCount:  new(int64(1)),
		},
		15,
		nil,
	)

	apiResult, err := executionResultToHTTP(mongoResult)
	require.NoError(t, err)

	mongoResp, err := apiResult.AsMongoDBExecutionResult()
	require.NoError(t, err)
	assert.Equal(
		t,
		api.MongoDBExecutionResultPayloadOperationType("find"),
		mongoResp.Payload.OperationType,
	)
	require.NotNil(t, mongoResp.Payload.Documents)
	assert.Len(t, *mongoResp.Payload.Documents, 1)
	require.NotNil(t, mongoResp.Payload.DocumentCount)
	assert.Equal(t, 1, *mongoResp.Payload.DocumentCount)
	assert.Equal(t, 15, mongoResp.DurationMs)
	assert.True(t, mongoResp.Success)
}

func TestExecutionResultToHTTP_HTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      json.RawMessage
		wantCheck func(t *testing.T, httpResp api.HTTPExecutionResult)
	}{
		{
			name: "JSONBody",
			body: json.RawMessage(`{"key":"value"}`),
			wantCheck: func(t *testing.T, httpResp api.HTTPExecutionResult) {
				// Body should be unmarshaled as JSON object
				bodyMap, ok := httpResp.Payload.Body.(map[string]any)
				require.True(t, ok, "expected JSON object body")
				assert.Equal(t, "value", bodyMap["key"])
			},
		},
		{
			name: "NonJSONBody",
			body: json.RawMessage(`"plain text response"`),
			wantCheck: func(t *testing.T, httpResp api.HTTPExecutionResult) {
				assert.Equal(t, "plain text response", httpResp.Payload.Body)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			httpRes := result.NewHTTPResult(
				&result.HTTPResult{
					StatusCode: 200,
					Headers:    map[string]string{"Content-Type": "application/json"},
					Body:       tc.body,
				},
				25,
				nil,
			)

			apiResult, err := executionResultToHTTP(httpRes)
			require.NoError(t, err)

			httpResp, err := apiResult.AsHTTPExecutionResult()
			require.NoError(t, err)
			assert.Equal(t, 200, httpResp.Payload.StatusCode)
			assert.Equal(t, 25, httpResp.DurationMs)
			assert.True(t, httpResp.Success)
			tc.wantCheck(t, httpResp)
		})
	}
}

func TestSQLStreamHTTPMetadataAndReferencePrivacy(t *testing.T) {
	t.Parallel()
	metadata := filepayload.NewFilePayloadMetadata("query-result.json", "application/json", []byte("{}"))
	execution := result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{}, RowCount: 100, File: &metadata, Reference: &filepayload.Reference{ID: "private-retained-id"}}, 25, nil)
	stored, err := json.Marshal(execution)
	require.NoError(t, err)
	for _, storedResult := range []bool{false, true} {
		var mapped api.QueryExecutionResult
		if storedResult {
			mapped, err = storedExecutionResultToHTTP(stored)
		} else {
			mapped, err = executionResultToHTTP(execution)
		}
		require.NoError(t, err)
		output, err := mapped.AsSQLExecutionResult()
		require.NoError(t, err)
		require.Equal(t, 100, output.Payload.RowCount)
		require.NotNil(t, output.Payload.File)
		require.EqualValues(t, 2, output.Payload.File.SizeBytes)
		raw, err := json.Marshal(mapped)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "private-retained-id")
	}
}

func TestExecutionResultToHTTP_HTTPFileOmitsRawBody(t *testing.T) {
	t.Parallel()

	content := []byte{0, 1, 2}
	metadata := filepayload.NewFilePayloadMetadata("users.bin", "application/octet-stream", content)
	httpRes := result.NewHTTPResult(
		&result.HTTPResult{
			StatusCode: 200,
			Headers:    map[string]string{"Content-Type": "application/octet-stream"},
			File:       &metadata,
			RawBody:    content,
		},
		25,
		nil,
	)

	apiResult, err := executionResultToHTTP(httpRes)
	require.NoError(t, err)

	httpResp, err := apiResult.AsHTTPExecutionResult()
	require.NoError(t, err)
	require.Equal(t, 200, httpResp.Payload.StatusCode)
	require.Nil(t, httpResp.Payload.Body)
	require.NotNil(t, httpResp.Payload.File)
	require.NotNil(t, httpResp.Payload.File.Filename)
	require.Equal(t, "users.bin", *httpResp.Payload.File.Filename)
	require.Equal(t, "application/octet-stream", httpResp.Payload.File.MimeType)
	require.EqualValues(t, len(content), httpResp.Payload.File.SizeBytes)
}

func TestExecutionResultToHTTP_FTPNilPayloadOnError(t *testing.T) {
	t.Parallel()

	ftpRes := result.NewFTPResult(nil, 33, fmt.Errorf("ftp read failed"))

	apiResult, err := executionResultToHTTP(ftpRes)
	require.NoError(t, err)

	ftpResp, err := apiResult.AsFTPExecutionResult()
	require.NoError(t, err)
	assert.False(t, ftpResp.Success)
	require.NotNil(t, ftpResp.Error)
	assert.Equal(t, "ftp read failed", *ftpResp.Error)
	assert.Equal(t, api.FTPExecutionResultPayloadOperationRead, ftpResp.Payload.Operation)
	assert.Equal(t, api.FTPExecutionResultPayloadFormatJson, ftpResp.Payload.Format)
}

func TestExecutionResultToHTTP_S3FilePayload(t *testing.T) {
	t.Parallel()

	writtenRows := 2
	writtenBytes := 75
	s3Res := result.NewS3Result(&result.FileResult{
		Rows:         []map[string]any{{"id": "1"}, {"id": "2"}},
		ColumnNames:  []string{"id"},
		RowCount:     2,
		Format:       "json",
		Operation:    "write",
		TargetPath:   "fixtures/exports/users.json",
		WrittenRows:  &writtenRows,
		WrittenBytes: &writtenBytes,
	}, 41, nil)

	apiResult, err := executionResultToHTTP(s3Res)
	require.NoError(t, err)

	s3Resp, err := apiResult.AsS3ExecutionResult()
	require.NoError(t, err)
	assert.True(t, s3Resp.Success)
	assert.Equal(t, "s3", s3Resp.Type)
	assert.Equal(t, api.S3ExecutionResultPayloadOperationWrite, s3Resp.Payload.Operation)
	assert.Equal(t, api.S3ExecutionResultPayloadFormatJson, s3Resp.Payload.Format)
	require.NotNil(t, s3Resp.Payload.TargetPath)
	assert.Equal(t, "fixtures/exports/users.json", *s3Resp.Payload.TargetPath)
	require.NotNil(t, s3Resp.Payload.WrittenRows)
	assert.Equal(t, writtenRows, *s3Resp.Payload.WrittenRows)
	require.NotNil(t, s3Resp.Payload.WrittenBytes)
	assert.Equal(t, writtenBytes, *s3Resp.Payload.WrittenBytes)
}

func TestExecutionResultToHTTP_S3NilPayloadOnError(t *testing.T) {
	t.Parallel()

	s3Res := result.NewS3Result(nil, 33, fmt.Errorf("s3 read failed"))

	apiResult, err := executionResultToHTTP(s3Res)
	require.NoError(t, err)

	s3Resp, err := apiResult.AsS3ExecutionResult()
	require.NoError(t, err)
	assert.False(t, s3Resp.Success)
	require.NotNil(t, s3Resp.Error)
	assert.Equal(t, "s3 read failed", *s3Resp.Error)
	assert.Equal(t, api.S3ExecutionResultPayloadOperationRead, s3Resp.Payload.Operation)
	assert.Equal(t, api.S3ExecutionResultPayloadFormatJson, s3Resp.Payload.Format)
}

func TestExecutionResultToHTTP_ObjectRowsNilPayloadOnError(t *testing.T) {
	t.Parallel()

	objectRowsRes := result.NewObjectRowsResult("hubspot", nil, 33, fmt.Errorf("hubspot request failed"))

	apiResult, err := executionResultToHTTP(objectRowsRes)
	require.NoError(t, err)

	objectRowsResp, err := apiResult.AsObjectRowsExecutionResult()
	require.NoError(t, err)
	assert.False(t, objectRowsResp.Success)
	assert.Equal(t, "hubspot", objectRowsResp.Type)
	require.NotNil(t, objectRowsResp.Error)
	assert.Equal(t, "hubspot request failed", *objectRowsResp.Error)
	assert.Empty(t, objectRowsResp.Payload.ObjectType)
	assert.Empty(t, objectRowsResp.Payload.ColumnNames)
	assert.Empty(t, objectRowsResp.Payload.Rows)
	assert.Equal(t, 0, objectRowsResp.Payload.RowCount)
	assert.False(t, objectRowsResp.Payload.HasMore)
}

func TestStoredExecutionResultToHTTP_FTPNilPayloadOnError(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(result.NewFTPResult(nil, 44, fmt.Errorf("ftp write failed")))
	require.NoError(t, err)

	apiResult, err := storedExecutionResultToHTTP(raw)
	require.NoError(t, err)

	ftpResp, err := apiResult.AsFTPExecutionResult()
	require.NoError(t, err)
	assert.False(t, ftpResp.Success)
	require.NotNil(t, ftpResp.Error)
	assert.Equal(t, "ftp write failed", *ftpResp.Error)
	assert.Equal(t, api.FTPExecutionResultPayloadOperationRead, ftpResp.Payload.Operation)
	assert.Equal(t, api.FTPExecutionResultPayloadFormatJson, ftpResp.Payload.Format)
}

func TestStoredExecutionResultToHTTP_S3NilPayloadOnError(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(result.NewS3Result(nil, 44, fmt.Errorf("s3 write failed")))
	require.NoError(t, err)

	apiResult, err := storedExecutionResultToHTTP(raw)
	require.NoError(t, err)

	s3Resp, err := apiResult.AsS3ExecutionResult()
	require.NoError(t, err)
	assert.False(t, s3Resp.Success)
	require.NotNil(t, s3Resp.Error)
	assert.Equal(t, "s3 write failed", *s3Resp.Error)
	assert.Equal(t, api.S3ExecutionResultPayloadOperationRead, s3Resp.Payload.Operation)
	assert.Equal(t, api.S3ExecutionResultPayloadFormatJson, s3Resp.Payload.Format)
}

func TestStoredExecutionResultToHTTP_ObjectRowsNilPayloadOnError(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(
		result.NewObjectRowsResult("hubspot", nil, 44, fmt.Errorf("hubspot rate limit exceeded")),
	)
	require.NoError(t, err)

	apiResult, err := storedExecutionResultToHTTP(raw)
	require.NoError(t, err)

	objectRowsResp, err := apiResult.AsObjectRowsExecutionResult()
	require.NoError(t, err)
	assert.False(t, objectRowsResp.Success)
	assert.Equal(t, "hubspot", objectRowsResp.Type)
	require.NotNil(t, objectRowsResp.Error)
	assert.Equal(t, "hubspot rate limit exceeded", *objectRowsResp.Error)
	assert.Empty(t, objectRowsResp.Payload.ObjectType)
	assert.Empty(t, objectRowsResp.Payload.ColumnNames)
	assert.Empty(t, objectRowsResp.Payload.Rows)
	assert.Equal(t, 0, objectRowsResp.Payload.RowCount)
	assert.False(t, objectRowsResp.Payload.HasMore)
}

func TestQueryExecutionToHTTP_ConvertsStoredInternalFTPEnvelope(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(result.NewFTPResult(&result.FileResult{
		Rows:                  []map[string]any{{"id": "1"}},
		ColumnNames:           []string{"id"},
		RowCount:              1,
		Format:                "spreadsheet",
		IsHeadlessSpreadsheet: true,
		Sheet:                 "Orders",
		Range:                 "A1:D20",
		Operation:             "read",
	}, 44, nil))
	require.NoError(t, err)

	record, err := queryExecutionToHTTP(&queriessvc.QueryExecution{
		Slug:       must.Slug("exec-1"),
		Status:     "success",
		Result:     raw,
		ExecutedAt: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	ftpResp, err := record.Result.AsFTPExecutionResult()
	require.NoError(t, err)
	assert.Equal(t, "ftp", ftpResp.Type)
	assert.True(t, ftpResp.Success)
	assert.Equal(t, api.FTPExecutionResultPayloadFormatSpreadsheet, ftpResp.Payload.Format)
	require.NotNil(t, ftpResp.Payload.RowCount)
	assert.Equal(t, 1, *ftpResp.Payload.RowCount)
	require.NotNil(t, ftpResp.Payload.ColumnNames)
	assert.Equal(t, []string{"id"}, *ftpResp.Payload.ColumnNames)
	require.NotNil(t, ftpResp.Payload.IsHeadlessSpreadsheet)
	assert.True(t, *ftpResp.Payload.IsHeadlessSpreadsheet)
	require.NotNil(t, ftpResp.Payload.Sheet)
	assert.Equal(t, "Orders", *ftpResp.Payload.Sheet)
	require.NotNil(t, ftpResp.Payload.Range)
	assert.Equal(t, "A1:D20", *ftpResp.Payload.Range)
}

func TestStoredExecutionResultToHTTP_GitResultUsesGitStorageShape(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(result.NewGitResult(&result.FileResult{
		Rows:        []map[string]any{{"id": 1}},
		ColumnNames: []string{"id"},
		RowCount:    1,
		Format:      "json",
		Operation:   "write",
		TargetPath:  "exports/users.json",
	}, 44, nil))
	require.NoError(t, err)

	var stored map[string]any
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.Equal(t, "Git", stored["result_type"])
	require.Equal(t, "git", stored["vendor_type"])
	_, hasGit := stored["git"]
	require.True(t, hasGit)
	_, hasFTP := stored["ftp"]
	require.False(t, hasFTP)

	apiResult, err := storedExecutionResultToHTTP(raw)
	require.NoError(t, err)

	gitResp, err := apiResult.AsGitExecutionResult()
	require.NoError(t, err)
	assert.True(t, gitResp.Success)
	assert.Equal(t, "git", gitResp.Type)
	assert.Equal(t, api.GitExecutionResultPayloadOperationWrite, gitResp.Payload.Operation)
	assert.Equal(t, api.GitExecutionResultPayloadFormatJson, gitResp.Payload.Format)
	require.NotNil(t, gitResp.Payload.TargetPath)
	assert.Equal(t, "exports/users.json", *gitResp.Payload.TargetPath)
}

func TestStoredExecutionResultToHTTP_S3ResultUsesS3StorageShape(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(result.NewS3Result(&result.FileResult{
		Rows:        []map[string]any{{"id": "1"}},
		ColumnNames: []string{"id"},
		RowCount:    1,
		Format:      "csv",
		Operation:   "read",
		TargetPath:  "fixtures/customers.csv",
	}, 44, nil))
	require.NoError(t, err)

	var stored map[string]any
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.Equal(t, "S3", stored["result_type"])
	require.Equal(t, "s3", stored["vendor_type"])
	_, hasS3 := stored["s3"]
	require.True(t, hasS3)
	_, hasFTP := stored["ftp"]
	require.False(t, hasFTP)
	_, hasGit := stored["git"]
	require.False(t, hasGit)

	apiResult, err := storedExecutionResultToHTTP(raw)
	require.NoError(t, err)

	s3Resp, err := apiResult.AsS3ExecutionResult()
	require.NoError(t, err)
	assert.True(t, s3Resp.Success)
	assert.Equal(t, "s3", s3Resp.Type)
	assert.Equal(t, api.S3ExecutionResultPayloadOperationRead, s3Resp.Payload.Operation)
	assert.Equal(t, api.S3ExecutionResultPayloadFormatCsv, s3Resp.Payload.Format)
	require.NotNil(t, s3Resp.Payload.TargetPath)
	assert.Equal(t, "fixtures/customers.csv", *s3Resp.Payload.TargetPath)
	require.NotNil(t, s3Resp.Payload.RowCount)
	assert.Equal(t, 1, *s3Resp.Payload.RowCount)
}

func TestExecutionResultToHTTP_WorkspaceNilPayload(t *testing.T) {
	t.Parallel()
	_, err := executionResultToHTTP(result.NewWorkspaceFileResult(nil, 1, nil))
	require.Error(t, err)
	apiResult, err := executionResultToHTTP(result.NewWorkspaceFileResult(nil, 1, fmt.Errorf("file read failed")))
	require.NoError(t, err)
	payload, err := apiResult.AsWorkspaceFileExecutionResult()
	require.NoError(t, err)
	require.False(t, payload.Success)
	require.Equal(t, "file read failed", *payload.Error)
}

func TestAPIDecimalContractRoundTrip(t *testing.T) {
	precision, scale := 38, 2
	minimum := api.PipelineSchemaContractValidationBound{}
	require.NoError(t, minimum.UnmarshalJSON([]byte(`9007199254740993.10`)))
	fields := []api.PipelineSchemaContractField{{Name: "price", Type: api.PipelineSchemaContractFieldType("decimal"), Precision: &precision, Scale: &scale, Validations: &api.PipelineSchemaContractFieldValidations{Min: &minimum}}}
	input := &api.PipelineSchemaContract{Name: "prices", Fields: &fields}
	contract, ok, err := apiSchemaContractToPipeline(input)
	require.NoError(t, err)
	require.True(t, ok)
	output := pipelineSchemaContractToAPI(contract)
	require.Equal(t, precision, *(*output.Fields)[0].Precision)
	require.Equal(t, scale, *(*output.Fields)[0].Scale)
	encoded, err := (*output.Fields)[0].Validations.Min.MarshalJSON()
	require.NoError(t, err)
	require.Equal(t, "9007199254740993.10", string(encoded))
}

func TestExecutionResultToHTTP_WorkspacePDFExport(t *testing.T) {
	t.Parallel()
	for _, executionError := range []error{nil, xerror.Forbidden(xerror.DataSource)} {
		apiResult, err := executionResultToHTTP(result.NewWorkspaceFileResult(&result.FileResult{Operation: "export", Format: "file", ResourceID: "native", MIMEType: "application/pdf"}, 1, executionError))
		require.NoError(t, err)
		payload, err := apiResult.AsWorkspaceFileExecutionResult()
		require.NoError(t, err)
		require.Equal(t, api.WorkspaceFileExecutionResultPayloadOperationExport, payload.Payload.Operation)
		require.Equal(t, api.WorkspaceFileExecutionResultPayloadFormatFile, payload.Payload.Format)
		require.Equal(t, executionError == nil, payload.Success)
		require.NotNil(t, payload.Payload.FileId)
		require.Equal(t, "native", *payload.Payload.FileId)
	}
}
