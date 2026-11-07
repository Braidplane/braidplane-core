package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/as"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func baseURL() string {
	return depsports.HTTPConnectorBaseURL()
}

func openAPIURL() string {
	return depsports.OpenAPIServerSchemaURL()
}

func getTestConfig() httpconnector.Config {
	return httpconnector.Config{
		BaseURL:           baseURL(),
		HealthPath:        "/health",
		TimeoutSeconds:    5,
		AllowPrivateHosts: true,
	}
}

func executeHTTP(
	ctx context.Context,
	conn *httpconnector.Connector,
	query map[string]any,
	params map[string]any,
) (any, error) {
	return executeHTTPWithLimits(ctx, conn, query, params, types.ExecutionLimits{})
}

func executeHTTPWithLimits(
	ctx context.Context,
	conn *httpconnector.Connector,
	query map[string]any,
	params map[string]any,
	limits types.ExecutionLimits,
) (any, error) {
	queryData, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}

	return executeHTTPRawWithLimits(ctx, conn, queryData, params, limits)
}

func executeHTTPRaw(
	ctx context.Context,
	conn *httpconnector.Connector,
	queryData []byte,
	params map[string]any,
) (any, error) {
	return executeHTTPRawWithLimits(ctx, conn, queryData, params, types.ExecutionLimits{})
}

func executeHTTPRawWithLimits(
	ctx context.Context,
	conn *httpconnector.Connector,
	queryData []byte,
	params map[string]any,
	limits types.ExecutionLimits,
) (any, error) {
	// Convert plain params to TypedParam map (use "text" for E2E tests)
	typedParams := make(map[string]types.TypedParam, len(params))
	for name, value := range params {
		typedParams[name] = types.TypedParam{
			Value:    value,
			DataType: "text", // E2E tests don't test normalization
		}
	}

	execResult, err := conn.Execute(ctx, queryData, typedParams, limits)
	if err != nil {
		return nil, err
	}

	if execResult.Error() != nil {
		return nil, execResult.Error()
	}

	httpResult, ok := execResult.HTTP()
	if !ok {
		return nil, fmt.Errorf("expected HTTP result, got: %s", execResult.Type())
	}

	// Unmarshal the body - could be array or object
	var body any
	if err := json.Unmarshal(httpResult.Body, &body); err != nil {
		return nil, fmt.Errorf("failed to unmarshal HTTP result body: %w", err)
	}

	return body, nil
}

func TestNew(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	tests := []struct {
		name      string
		config    httpconnector.Config
		creds     httpconnector.Credentials
		assertErr func(t *testing.T, err error)
	}{
		{
			name:   "ValidConfig",
			config: getTestConfig(),
		},
		{
			name: "MissingBaseURL",
			config: httpconnector.Config{
				HealthPath:     "/health",
				TimeoutSeconds: 5,
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, httpconnector.ErrInvalidConfig)
			},
		},
		{
			name: "InvalidScheme",
			config: httpconnector.Config{
				BaseURL:        "ftp://example.com",
				HealthPath:     "/health",
				TimeoutSeconds: 5,
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, httpconnector.ErrInvalidConfig)
			},
		},
		{
			name:   "InvalidBasicAuthPair",
			config: getTestConfig(),
			creds: httpconnector.Credentials{
				Username: "user",
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, httpconnector.ErrInvalidCredentials)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn, err := httpconnector.New(transportsecurity.WithPlaintext(context.Background()), tt.config, tt.creds)

			if tt.assertErr != nil {
				tt.assertErr(t, err)
				require.Nil(t, conn, "connector should be nil")
				return
			}

			require.NoError(t, err)
			require.NotNil(t, conn, "connector should be created")
		})
	}
}

func TestConnector_TestConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := httpconnector.New(transportsecurity.WithPlaintext(ctx), getTestConfig(), httpconnector.Credentials{})
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")

	err = conn.TestConnection(ctx)
	require.NoError(t, err)
}

func TestConnector_TestConnection_ErrorStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	config := getTestConfig()
	config.HealthPath = "/items/999999"

	conn, err := httpconnector.New(transportsecurity.WithPlaintext(ctx), config, httpconnector.Credentials{})
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")

	err = conn.TestConnection(ctx)
	require.ErrorIs(t, err, xerror.NotFound(xerror.DataSource))
	require.Equal(t, "Endpoint not found (HTTP status 404). Check the health endpoint.", xerror.PublicDetail(err))
}

func TestConnector_IntrospectSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	config := httpconnector.Config{
		BaseURL:                    baseURL(),
		HealthPath:                 "/health",
		TimeoutSeconds:             10,
		OpenAPIURL:                 openAPIURL(),
		OpenAPIFetchTimeoutSeconds: 10,
		AllowPrivateHosts:          true,
	}

	conn, err := httpconnector.New(transportsecurity.WithPlaintext(ctx), config, httpconnector.Credentials{})
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")

	schema, err := conn.IntrospectSchema(ctx)
	require.NoError(t, err)
	require.NotNil(t, schema, "schema should be returned")
	assert.Greater(t, len(schema.Operations), 10, "schema should include many operations")

	operations := make(map[string]httpconnector.Operation)
	for _, op := range schema.Operations {
		operations[op.OperationID] = op
	}

	_, hasCreateOrganization := operations["createOrganization"]
	assert.True(t, hasCreateOrganization, "Should have createOrganization operation")
	_, hasListOrganizations := operations["listOrganizations"]
	assert.True(t, hasListOrganizations, "Should have listOrganizations operation")
	_, hasCreateRole := operations["createRole"]
	assert.True(t, hasCreateRole, "Should have createRole operation")
	_, hasListRoles := operations["listRoles"]
	assert.True(t, hasListRoles, "Should have listRoles operation")
	_, hasCreateMember := operations["createMember"]
	assert.True(t, hasCreateMember, "Should have createMember operation")
	_, hasListMembers := operations["listMembers"]
	assert.True(t, hasListMembers, "Should have listMembers operation")
	_, hasCreateDataSource := operations["createDataSource"]
	assert.True(t, hasCreateDataSource, "Should have createDataSource operation")
	_, hasListDataSources := operations["listDataSources"]
	assert.True(t, hasListDataSources, "Should have listDataSources operation")

	listOrganizations := operations["listOrganizations"]
	require.NotEmpty(t, listOrganizations.Responses)

	decodedResponses := make(map[string]any)
	require.NoError(t, json.Unmarshal(listOrganizations.Responses, &decodedResponses))
	require.Contains(t, decodedResponses, "200")
	assert.GreaterOrEqual(t, len(decodedResponses), 1)

	response200, ok := decodedResponses["200"].(map[string]any)
	require.True(t, ok)
	content, ok := response200["content"].(map[string]any)
	require.True(t, ok)
	_, hasJSON := content["application/json"]
	assert.True(t, hasJSON)

	_, hasRefreshDataSourceSchema := operations["refreshDataSourceSchema"]
	assert.True(t, hasRefreshDataSourceSchema, "Should have refreshDataSourceSchema operation")
	refreshSchema := operations["refreshDataSourceSchema"]
	require.NotEmpty(t, refreshSchema.Responses)
	decodedRefresh := make(map[string]any)
	require.NoError(t, json.Unmarshal(refreshSchema.Responses, &decodedRefresh))
	require.Contains(t, decodedRefresh, "202")

	createDataSource := operations["createDataSource"]
	require.NotEmpty(t, createDataSource.RequestBody)

	decodedRequestBody := make(map[string]any)
	require.NoError(t, json.Unmarshal(createDataSource.RequestBody, &decodedRequestBody))

	requestContent, ok := decodedRequestBody["content"].(map[string]any)
	require.True(t, ok)
	requestJSON, ok := requestContent["application/json"].(map[string]any)
	require.True(t, ok)
	requestSchemaSummary, ok := requestJSON["schema_summary"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, requestSchemaSummary, "$ref")

	requestProperties, ok := requestSchemaSummary["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, requestProperties, "name")
	assert.Contains(t, requestProperties, "type")
	assert.Contains(t, requestProperties, "config")

	require.NotEmpty(t, createDataSource.Responses)
	decodedCreateResponses := make(map[string]any)
	require.NoError(t, json.Unmarshal(createDataSource.Responses, &decodedCreateResponses))
	require.Contains(t, decodedCreateResponses, "201")

	response201, ok := decodedCreateResponses["201"].(map[string]any)
	require.True(t, ok)
	responseContent, ok := response201["content"].(map[string]any)
	require.True(t, ok)
	responseJSON, ok := responseContent["application/json"].(map[string]any)
	require.True(t, ok)
	responseSchemaSummary, ok := responseJSON["schema_summary"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, responseSchemaSummary, "$ref")

	responseProperties, ok := responseSchemaSummary["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, responseProperties, "slug")
	assert.Contains(t, responseProperties, "name")
	assert.Contains(t, responseProperties, "type")
	assert.Contains(t, responseProperties, "config")
}

func TestConnector_IntrospectSchema_InvalidOpenAPIURL(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	config := httpconnector.Config{
		BaseURL:                    baseURL(),
		HealthPath:                 "/health",
		TimeoutSeconds:             5,
		OpenAPIURL:                 "http://localhost:99999/nonexistent.yml",
		OpenAPIFetchTimeoutSeconds: 5,
		AllowPrivateHosts:          true,
	}

	conn, err := httpconnector.New(transportsecurity.WithPlaintext(ctx), config, httpconnector.Credentials{})
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")

	schema, err := conn.IntrospectSchema(ctx)
	require.ErrorContains(t, err, "failed to fetch OpenAPI spec")
	require.Nil(t, schema, "schema should be nil")
}

func TestConnector_IntrospectSchema_MissingOpenAPIURLReturnsEmptySchema(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	config := getTestConfig()

	conn, err := httpconnector.New(transportsecurity.WithPlaintext(ctx), config, httpconnector.Credentials{})
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")

	schema, err := conn.IntrospectSchema(ctx)
	require.NoError(t, err)
	require.NotNil(t, schema, "schema should be returned")
	assert.Empty(t, schema.Operations, "schema should have no operations")
}

func TestConnector_ExecuteLifecycle(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := httpconnector.New(transportsecurity.WithPlaintext(ctx), getTestConfig(), httpconnector.Credentials{})
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")

	resetResult, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodPost,
		"path":   "/reset",
	}, nil)
	require.NoError(t, err)
	resetMap := as.Must[map[string]any](t, resetResult)
	assert.True(t, as.Must[bool](t, resetMap["reset"]))

	createOneResult, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodPost,
		"path":   "/items",
		"body":   map[string]any{"name": "alpha", "status": "new"},
	}, nil)
	require.NoError(t, err)
	createOneMap := as.Must[map[string]any](t, createOneResult)
	id1 := int(createOneMap["id"].(float64))
	assert.Equal(t, "alpha", createOneMap["name"])
	assert.Equal(t, "new", createOneMap["status"])

	createTwoResult, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodPost,
		"path":   "/items",
		"body":   map[string]any{"name": "beta", "status": "pending"},
	}, nil)
	require.NoError(t, err)
	createTwoMap := as.Must[map[string]any](t, createTwoResult)
	id2 := int(createTwoMap["id"].(float64))
	assert.Greater(t, id2, id1, "second id should be greater")

	updateResult, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodPut,
		"path":   fmt.Sprintf("/items/%d", id1),
		"body":   map[string]any{"name": "alpha-up", "status": "active"},
	}, nil)
	require.NoError(t, err)
	updateMap := as.Must[map[string]any](t, updateResult)
	assert.Equal(t, id1, int(updateMap["id"].(float64)))
	assert.Equal(t, "alpha-up", updateMap["name"])
	assert.Equal(t, "active", updateMap["status"])

	getResult, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodGet,
		"path":   fmt.Sprintf("/items/%d", id1),
	}, nil)
	require.NoError(t, err)
	getMap := as.Must[map[string]any](t, getResult)
	assert.Equal(t, id1, int(getMap["id"].(float64)))
	assert.Equal(t, "alpha-up", getMap["name"])
	assert.Equal(t, "active", getMap["status"])

	listResult, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodGet,
		"path":   "/items",
	}, nil)
	require.NoError(t, err)
	list := as.Must[[]any](t, listResult)
	assert.Len(t, list, 2)
	first := as.Must[map[string]any](t, list[0])
	second := as.Must[map[string]any](t, list[1])
	assert.Equal(t, id1, int(first["id"].(float64)))
	assert.Equal(t, id2, int(second["id"].(float64)))

	filterResult, err := executeHTTP(ctx, conn, map[string]any{
		"method":       http.MethodGet,
		"path":         "/items",
		"query_params": map[string]any{"status": "active"},
	}, nil)
	require.NoError(t, err)
	filtered := as.Must[[]any](t, filterResult)
	assert.Len(t, filtered, 1)
	filteredItem := as.Must[map[string]any](t, filtered[0])
	assert.Equal(t, id1, int(filteredItem["id"].(float64)))

	deleteOneResult, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodDelete,
		"path":   fmt.Sprintf("/items/%d", id1),
	}, nil)
	require.NoError(t, err)
	deleteOneMap := as.Must[map[string]any](t, deleteOneResult)
	assert.True(t, as.Must[bool](t, deleteOneMap["deleted"]))

	notFoundResult, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodGet,
		"path":   fmt.Sprintf("/items/%d", id1),
	}, nil)
	require.NoError(t, err)
	notFoundMap := as.Must[map[string]any](t, notFoundResult)
	assert.Equal(t, "not found", notFoundMap["error"])

	deleteTwoResult, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodDelete,
		"path":   fmt.Sprintf("/items/%d", id2),
	}, nil)
	require.NoError(t, err)
	deleteTwoMap := as.Must[map[string]any](t, deleteTwoResult)
	assert.True(t, as.Must[bool](t, deleteTwoMap["deleted"]))

	finalListResult, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodGet,
		"path":   "/items",
	}, nil)
	require.NoError(t, err)
	finalList := as.Must[[]any](t, finalListResult)
	assert.Empty(t, finalList)

	resetAgainResult, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodPost,
		"path":   "/reset",
	}, nil)
	require.NoError(t, err)
	resetAgainMap := as.Must[map[string]any](t, resetAgainResult)
	assert.True(t, as.Must[bool](t, resetAgainMap["reset"]))
}

func TestConnector_ExecuteMultipartFormRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := httpconnector.New(transportsecurity.WithPlaintext(ctx), getTestConfig(), httpconnector.Credentials{})
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")

	got, err := executeHTTP(ctx, conn, map[string]any{
		"method": http.MethodPost,
		"path":   "/multipart-echo",
		"headers": map[string]any{
			"Content-Type": "multipart/form-data",
		},
		"body": map[string]any{
			"name": "Alice",
			"tag":  []any{"urgent", "customer"},
		},
	}, nil)
	require.NoError(t, err)

	resultMap := as.Must[map[string]any](t, got)
	require.True(t, as.Must[bool](t, resultMap["accepted"]))

	fields := as.Must[map[string]any](t, resultMap["fields"])
	require.Equal(t, []any{"Alice"}, fields["name"])
	require.Equal(t, []any{"urgent", "customer"}, fields["tag"])
}

func TestConnector_ExecuteEnforcesByteLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := httpconnector.New(transportsecurity.WithPlaintext(ctx), getTestConfig(), httpconnector.Credentials{})
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")

	_, err = executeHTTPWithLimits(
		ctx,
		conn,
		map[string]any{
			"method": http.MethodPost,
			"path":   "/items",
			"body":   map[string]any{"name": "alpha", "status": "new"},
		},
		nil,
		types.ExecutionLimits{HTTPMaxRequestBytes: 8, HTTPMaxResponseBytes: 1024},
	)
	require.ErrorIs(t, err, xerror.ExecutionRequestSizeExceeded())

	_, err = executeHTTPWithLimits(
		ctx,
		conn,
		map[string]any{
			"method": http.MethodGet,
			"path":   "/items",
		},
		nil,
		types.ExecutionLimits{HTTPMaxResponseBytes: 1},
	)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

// Sequential execution required: shares ctx and conn across subtests
func TestConnector_ExecuteErrorPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := httpconnector.New(transportsecurity.WithPlaintext(ctx), getTestConfig(), httpconnector.Credentials{})
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")

	tests := []struct {
		name         string
		queryData    []byte
		assertErr    func(t *testing.T, err error)
		assertResult func(t *testing.T, got any)
	}{
		{
			name:      "InvalidJSON",
			queryData: []byte(`{"method":"GET",`),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "invalid HTTP query data")
			},
		},
		{
			name:      "MissingMethod",
			queryData: []byte(`{"path":"/items"}`),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "must specify a method")
			},
		},
		{
			name:      "MissingPath",
			queryData: []byte(`{"method":"GET"}`),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "must specify a path")
			},
		},
		{
			name:      "MethodNotAllowed",
			queryData: []byte(`{"method":"PATCH","path":"/items"}`),
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
			assertResult: func(t *testing.T, got any) {
				resultMap := as.Must[map[string]any](t, got)
				require.Equal(t, "method not allowed", resultMap["error"])
			},
		},
		{
			name:      "PostInvalidBody",
			queryData: []byte(`{"method":"POST","path":"/items","body":{}}`),
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
			assertResult: func(t *testing.T, got any) {
				resultMap := as.Must[map[string]any](t, got)
				require.Equal(t, "name and status required", resultMap["error"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, execErr := executeHTTPRaw(ctx, conn, tt.queryData, nil)
			tt.assertErr(t, execErr)
			if tt.assertResult != nil {
				tt.assertResult(t, got)
			}
		})
	}
}
