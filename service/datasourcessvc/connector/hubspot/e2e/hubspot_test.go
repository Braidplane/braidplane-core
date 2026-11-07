package hubspot_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	hubspotclient "github.com/Braidplane/braidplane-core/pkg/hubspot/crm"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	hubspotconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func hubSpotMockBaseURL() string {
	return depsports.HubSpotMockBaseURL()
}

func testConfig() hubspotclient.Config {
	return hubspotclient.Config{
		BaseURL:           hubSpotMockBaseURL(),
		TimeoutSeconds:    5,
		AllowPrivateHosts: true,
	}
}

// testCredentials returns test-only HubSpot static credentials for the mock provider.
func testCredentials() hubspotconnector.Credentials {
	return hubspotconnector.Credentials{PrivateAppToken: "pat-na1-test-token"}
}

func resetMock(t *testing.T, ctx context.Context) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hubSpotMockBaseURL()+"/reset", nil)
	require.NoError(t, err)

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func seedMock(t *testing.T, ctx context.Context, payload map[string]any) {
	t.Helper()

	bodyBytes, err := json.Marshal(payload)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		hubSpotMockBaseURL()+"/seed",
		strings.NewReader(string(bodyBytes)),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func executeHubSpot(
	ctx context.Context,
	conn *hubspotconnector.Connector,
	query map[string]any,
	params map[string]types.TypedParam,
) (*result.ObjectRowsResult, error) {
	return executeHubSpotWithLimits(ctx, conn, query, params, types.ExecutionLimits{})
}

func executeHubSpotWithLimits(
	ctx context.Context,
	conn *hubspotconnector.Connector,
	query map[string]any,
	params map[string]types.TypedParam,
	limits types.ExecutionLimits,
) (*result.ObjectRowsResult, error) {
	queryData, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}

	execResult, err := conn.Execute(ctx, queryData, params, limits)
	if err != nil {
		return nil, err
	}
	if execResult.Error() != nil {
		return nil, execResult.Error()
	}

	hubspotResult, ok := execResult.ObjectRows()
	if !ok {
		return nil, xerror.Unexpected().Detailf("expected object rows result, got %s", execResult.Type())
	}

	return hubspotResult, nil
}

// Shares a mutable mock service instance across tests.
func TestConnector_TestConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := hubspotconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(), testCredentials())
	require.NoError(t, err)

	err = conn.TestConnection(ctx)
	require.NoError(t, err)
}

// Shares a mutable mock service instance across tests.
func TestConnector_TestConnection_InvalidCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := hubspotconnector.New(
		transportsecurity.WithPlaintext(ctx),
		testConfig(),
		hubspotconnector.Credentials{PrivateAppToken: "pat-na1-invalid-token"},
	)
	require.NoError(t, err)

	err = conn.TestConnection(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
}

// Shares a mutable mock service instance across tests.
func TestConnector_IntrospectSchema_ReturnsSeededAccountProperties(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resetMock(t, ctx)
	seedMock(t, ctx, map[string]any{
		"properties": map[string]any{
			"contacts": []map[string]any{
				{"name": "email", "label": "Email", "type": "string", "formField": true},
				{"name": "firstname", "label": "First name", "type": "string", "formField": false},
				{"name": "lifecyclestage", "label": "Lifecycle stage", "type": "enumeration", "formField": false},
			},
		},
	})

	conn, err := hubspotconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(), testCredentials())
	require.NoError(t, err)

	schema, err := conn.IntrospectSchema(ctx)
	require.NoError(t, err)
	require.Len(t, schema.Objects, 3)
	require.Equal(t, "contacts", schema.Objects[0].Name)
	require.Len(t, schema.Objects[0].Properties, 3)
	require.Equal(t, "lifecyclestage", schema.Objects[0].Properties[2].Name)
	require.Equal(t, "enumeration", schema.Objects[0].Properties[2].Type)
}

// Shares a mutable mock service instance across tests.
func TestConnector_CustomObjectDiscoveryAndSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resetMock(t, ctx)
	seedMock(t, ctx, map[string]any{
		"custom_objects": []map[string]any{
			{
				"object_type_id": "2-7282133",
				"name":           "cars",
				"label":          "Cars",
				"properties": []map[string]any{
					{
						"name":           "vin",
						"label":          "VIN",
						"type":           "string",
						"hasUniqueValue": true,
					},
					{"name": "model", "label": "Model", "type": "string"},
				},
				"records": []map[string]any{
					{
						"id": "501",
						"properties": map[string]any{
							"vin":   "VIN-001",
							"model": "Roadster",
						},
					},
				},
			},
		},
	})

	conn, err := hubspotconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(), testCredentials())
	require.NoError(t, err)

	schema, err := conn.IntrospectSchema(ctx)
	require.NoError(t, err)
	require.Len(t, schema.Objects, 4)
	require.Equal(t, "2-7282133", schema.Objects[3].Name)
	require.Equal(t, "Cars", schema.Objects[3].Label)
	require.Equal(t, true, schema.Objects[3].ProviderMetadata["custom"])
	require.Equal(t, true, schema.Objects[3].Properties[0].ProviderMetadata["has_unique_value"])

	hubspotResult, err := executeHubSpot(
		ctx,
		conn,
		map[string]any{
			"type":        "hubspot",
			"object_type": "2-7282133",
			"operation":   "search",
			"properties":  []string{"vin", "model"},
			"query":       "Roadster",
		},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "2-7282133", hubspotResult.ObjectType)
	require.Equal(t, 1, hubspotResult.RowCount)
	require.Equal(t, "VIN-001", hubspotResult.Rows[0]["vin"])
}

// Shares a mutable mock service instance across tests.
func TestConnector_UpsertCreatesAndUpdatesByUniqueProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resetMock(t, ctx)
	seedMock(t, ctx, map[string]any{
		"properties": map[string]any{
			"contacts": []map[string]any{
				{"name": "external_customer_id", "label": "External customer ID", "type": "string", "hasUniqueValue": true},
				{"name": "email", "label": "Email", "type": "string"},
			},
		},
	})

	conn, err := hubspotconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(), testCredentials())
	require.NoError(t, err)
	query := map[string]any{
		"type":             "hubspot",
		"object_type":      "contacts",
		"operation":        "upsert",
		"id_property":      "external_customer_id",
		"property_mapping": map[string]string{"customer_id": "external_customer_id", "email": "email"},
		"rows":             "{{input}}",
	}

	created, err := executeHubSpot(ctx, conn, query, map[string]types.TypedParam{
		"input": {Value: []any{map[string]any{"customer_id": "customer-1", "email": "first@example.com"}}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, created.RowCount)
	require.Equal(t, true, created.Rows[0]["created"])
	recordID := created.Rows[0][hubspotconnector.RecordIDFieldName]

	updated, err := executeHubSpot(ctx, conn, query, map[string]types.TypedParam{
		"input": {Value: []any{map[string]any{"customer_id": "customer-1", "email": "updated@example.com"}}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, updated.RowCount)
	require.Equal(t, false, updated.Rows[0]["created"])
	require.Equal(t, recordID, updated.Rows[0][hubspotconnector.RecordIDFieldName])
}

// Shares a mutable mock service instance across tests.
func TestConnector_Execute_ListSupportsPagination(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resetMock(t, ctx)
	seedMock(t, ctx, map[string]any{
		"contacts": []map[string]any{
			{"id": "101", "properties": map[string]any{"email": "alice@example.com", "firstname": "Alice"}},
			{"id": "102", "properties": map[string]any{"email": "bob@example.com", "firstname": "Bob"}},
			{"id": "103", "properties": map[string]any{"email": "carol@example.com", "firstname": "Carol"}},
		},
	})

	conn, err := hubspotconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(), testCredentials())
	require.NoError(t, err)

	hubspotResult, err := executeHubSpot(
		ctx,
		conn,
		map[string]any{
			"type":        "hubspot",
			"object_type": "contacts",
			"operation":   "list",
			"properties":  []string{"email", "firstname"},
			"limit":       2,
		},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "contacts", hubspotResult.ObjectType)
	require.Equal(t, "list", hubspotResult.Operation)
	require.Equal(t, 2, hubspotResult.RowCount)
	require.Equal(t, []string{"hs_object_id", "email", "firstname"}, hubspotResult.ColumnNames)
	require.NotNil(t, hubspotResult.NextCursor)
	require.Equal(t, "2", *hubspotResult.NextCursor)
	require.True(t, hubspotResult.HasMore)
	require.Equal(t, "101", hubspotResult.Rows[0]["hs_object_id"])
	require.Equal(t, "alice@example.com", hubspotResult.Rows[0]["email"])
}

// Shares a mutable mock service instance across tests.
func TestConnector_Execute_ReturnsRowLimitViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resetMock(t, ctx)
	seedMock(t, ctx, map[string]any{
		"contacts": []map[string]any{
			{"id": "301", "properties": map[string]any{"email": "alpha@example.com"}},
			{"id": "302", "properties": map[string]any{"email": "beta@example.com"}},
		},
	})

	conn, err := hubspotconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(), testCredentials())
	require.NoError(t, err)

	_, err = executeHubSpotWithLimits(
		ctx,
		conn,
		map[string]any{
			"type":        "hubspot",
			"object_type": "contacts",
			"operation":   "list",
			"properties":  []string{"email"},
			"limit":       100,
		},
		nil,
		types.ExecutionLimits{MaxResultRows: 1, MaxResponseBytes: 1024 * 1024},
	)
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
}

// Shares a mutable mock service instance across tests.
func TestConnector_Execute_SearchFiltersAndSorts(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resetMock(t, ctx)
	seedMock(t, ctx, map[string]any{
		"contacts": []map[string]any{
			{"id": "201", "properties": map[string]any{"email": "bob@example.com", "firstname": "Bob"}},
			{"id": "202", "properties": map[string]any{"email": "alice@example.com", "firstname": "Alice"}},
			{"id": "203", "properties": map[string]any{"email": "bob@other.com", "firstname": "Bobby"}},
		},
	})

	conn, err := hubspotconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(), testCredentials())
	require.NoError(t, err)

	hubspotResult, err := executeHubSpot(
		ctx,
		conn,
		map[string]any{
			"type":        "hubspot",
			"object_type": "contacts",
			"operation":   "search",
			"properties":  []string{"email", "firstname"},
			"query":       "{{domain}}",
			"sorts":       []string{"email"},
			"filter_groups": []map[string]any{
				{
					"filters": []map[string]any{
						{
							"property_name": "email",
							"operator":      "EQ",
							"value":         "{{target_email}}",
						},
					},
				},
			},
			"limit": 10,
		},
		map[string]types.TypedParam{
			"domain":       {Value: "example.com", DataType: "text"},
			"target_email": {Value: "bob@example.com", DataType: "text"},
		},
	)
	require.NoError(t, err)
	require.Equal(t, 1, hubspotResult.RowCount)
	require.False(t, hubspotResult.HasMore)
	require.Nil(t, hubspotResult.NextCursor)
	require.Equal(t, "201", hubspotResult.Rows[0]["hs_object_id"])
	require.Equal(t, "bob@example.com", hubspotResult.Rows[0]["email"])
	require.Equal(t, "Bob", hubspotResult.Rows[0]["firstname"])
}
