package hubspot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	hubspotclient "github.com/Braidplane/braidplane-core/pkg/hubspot/crm"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestNew_RejectsMissingPrivateAppToken(t *testing.T) {
	t.Parallel()

	conn, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		hubspotclient.Config{BaseURL: "https://api.hubapi.com", TimeoutSeconds: 5},
		Credentials{},
	)

	require.Nil(t, conn)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
}

func TestConnector_TestConnection_UsesAccountDetailsEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/account-info/v3/details", r.URL.Path)
		assert.Equal(t, "Bearer pat-na1-test-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"portalId":12345,"companyName":"Acme Inc"}`))
	}))
	t.Cleanup(server.Close)

	conn, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		hubspotclient.Config{BaseURL: server.URL, TimeoutSeconds: 5, AllowPrivateHosts: true},
		Credentials{PrivateAppToken: "pat-na1-test-token"},
	)
	require.NoError(t, err)

	err = conn.TestConnection(transportsecurity.WithPlaintext(context.Background()))
	require.NoError(t, err)
}

func TestConnector_TestConnection_MapsUnauthorizedToInvalidCredentials(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":"error","message":"Authentication credentials not found"}`))
	}))
	t.Cleanup(server.Close)

	conn, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		hubspotclient.Config{BaseURL: server.URL, TimeoutSeconds: 5, AllowPrivateHosts: true},
		Credentials{PrivateAppToken: "pat-na1-invalid"},
	)
	require.NoError(t, err)

	err = conn.TestConnection(transportsecurity.WithPlaintext(context.Background()))
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
}

func TestConnector_IntrospectSchema_ReturnsCommonCRMObjects(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer pat-na1-schema-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/crm/properties/2026-03/contacts":
			_, _ = w.Write([]byte(`{"results":[{"name":"email","label":"Email","type":"string","fieldType":"text","formField":true},{"name":"firstname","label":"First name","type":"string","fieldType":"text","formField":false}]}`))
		case "/crm/properties/2026-03/companies":
			_, _ = w.Write([]byte(`{"results":[{"name":"name","label":"Company name","type":"string","fieldType":"text","formField":true}]}`))
		case "/crm/properties/2026-03/deals":
			_, _ = w.Write([]byte(`{"results":[{"name":"dealname","label":"Deal name","type":"string","fieldType":"text","formField":true}]}`))
		case "/crm-object-schemas/2026-03/schemas":
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	conn, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		hubspotclient.Config{BaseURL: server.URL, TimeoutSeconds: 5, AllowPrivateHosts: true},
		Credentials{PrivateAppToken: "pat-na1-schema-token"},
	)
	require.NoError(t, err)

	schema, err := conn.IntrospectSchema(transportsecurity.WithPlaintext(context.Background()))
	require.NoError(t, err)
	require.NotNil(t, schema)
	require.Len(t, schema.Objects, 3)

	assert.Equal(t, "contacts", schema.Objects[0].Name)
	assert.Equal(t, "Contacts", schema.Objects[0].Label)
	require.Len(t, schema.Objects[0].Properties, 2)
	assert.Equal(t, "email", schema.Objects[0].Properties[0].Name)
	assert.Equal(t, "Email", schema.Objects[0].Properties[0].Label)
	assert.Equal(t, "string", schema.Objects[0].Properties[0].Type)
	require.Len(t, schema.Objects[0].Operations, 3)
	assert.Equal(t, "list", schema.Objects[0].Operations[0].Name)
	assert.Equal(t, "search", schema.Objects[0].Operations[1].Name)
	assert.Equal(t, "upsert", schema.Objects[0].Operations[2].Name)
}

// TestConnector_OAuthScopesLimitSchemaAndQueries verifies granted OAuth scopes filter CRM schemas and queries.
func TestConnector_OAuthScopesLimitSchemaAndQueries(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, "/crm/properties/2026-03/contacts", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"name":"email","label":"Email","type":"string"}]}`))
	}))
	t.Cleanup(server.Close)
	conn, err := New(transportsecurity.WithPlaintext(context.Background()), hubspotclient.Config{BaseURL: server.URL, TimeoutSeconds: 5, AllowPrivateHosts: true}, Credentials{AccessToken: "oauth-token", GrantedScopes: []string{"crm.objects.contacts.read"}})
	require.NoError(t, err)

	schema, err := conn.IntrospectSchema(transportsecurity.WithPlaintext(context.Background()))
	require.NoError(t, err)
	require.Len(t, schema.Objects, 1)
	assert.Equal(t, "contacts", schema.Objects[0].Name)

	result, err := conn.Execute(transportsecurity.WithPlaintext(context.Background()), json.RawMessage(`{"object_type":"companies","operation":"list"}`), nil)
	require.NoError(t, err)
	require.ErrorContains(t, result.Error(), "crm.objects.companies.read")
	assert.Equal(t, 1, requests)
}

func TestConnector_Execute_SearchNormalizesRowsAndPagination(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/crm/objects/2026-03/contacts/search", r.URL.Path)
		assert.Equal(t, "Bearer pat-na1-search-token", r.Header.Get("Authorization"))

		bodyBytes, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}

		var payload map[string]any
		if !assert.NoError(t, json.Unmarshal(bodyBytes, &payload)) {
			return
		}
		limitValue, ok := payload["limit"].(float64)
		if assert.True(t, ok) {
			assert.InDelta(t, 2, limitValue, 0.000001)
		}
		assert.Equal(t, "alice@example.com", payload["query"])
		assert.Equal(t, []any{"email", "firstname"}, payload["properties"])

		filterGroups, ok := payload["filterGroups"].([]any)
		if assert.True(t, ok) {
			assert.Len(t, filterGroups, 1)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total": 2,
			"results": [
				{
					"id": "101",
					"properties": {
						"email": "alice@example.com",
						"firstname": "Alice"
					}
				},
				{
					"id": "102",
					"properties": {
						"email": "bob@example.com",
						"firstname": "Bob"
					}
				}
			],
			"paging": {
				"next": {
					"after": "200"
				}
			}
		}`))
	}))
	t.Cleanup(server.Close)

	conn, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		hubspotclient.Config{BaseURL: server.URL, TimeoutSeconds: 5, AllowPrivateHosts: true},
		Credentials{PrivateAppToken: "pat-na1-search-token"},
	)
	require.NoError(t, err)

	queryData := json.RawMessage(`{
		"type": "hubspot",
		"object_type": "contacts",
		"operation": "search",
		"properties": ["email", "firstname"],
		"query": "{{search_term}}",
		"limit": 2,
		"filter_groups": [
			{
				"filters": [
					{
						"property_name": "email",
						"operator": "EQ",
						"value": "{{search_term}}"
					}
				]
			}
		]
	}`)

	execResult, err := conn.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		queryData,
		map[string]types.TypedParam{
			"search_term": {Value: "alice@example.com", DataType: "text"},
		},
	)
	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.Equal(t, result.ExecutionResultTypeObjectRows, execResult.Type())
	require.Equal(t, "hubspot", execResult.VendorType())

	hubspotResult, ok := execResult.ObjectRows()
	require.True(t, ok)
	require.NotNil(t, hubspotResult)
	assert.Equal(t, "contacts", hubspotResult.ObjectType)
	assert.Equal(t, "search", hubspotResult.Operation)
	assert.Equal(t, []string{"hs_object_id", "email", "firstname"}, hubspotResult.ColumnNames)
	assert.Equal(t, 2, hubspotResult.RowCount)
	require.Len(t, hubspotResult.Rows, 2)
	assert.Equal(t, "101", hubspotResult.Rows[0]["hs_object_id"])
	assert.Equal(t, "alice@example.com", hubspotResult.Rows[0]["email"])
	require.NotNil(t, hubspotResult.NextCursor)
	assert.Equal(t, "200", *hubspotResult.NextCursor)
	assert.True(t, hubspotResult.HasMore)
}

func TestConnector_Execute_UsesConfiguredResponseSizeLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/crm/objects/2026-03/contacts", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"101","properties":{"email":"alice@example.com"}}]}`))
	}))
	t.Cleanup(server.Close)

	conn, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		hubspotclient.Config{BaseURL: server.URL, TimeoutSeconds: 5, AllowPrivateHosts: true},
		Credentials{PrivateAppToken: "pat-na1-size-token"},
	)
	require.NoError(t, err)

	execResult, err := conn.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		json.RawMessage(`{
			"type": "hubspot",
			"object_type": "contacts",
			"operation": "list",
			"properties": ["email"]
		}`),
		map[string]types.TypedParam{},
		types.ExecutionLimits{MaxResponseBytes: int64(len(`{"results":[]}`))},
	)

	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.Error(t, execResult.Error())
	require.ErrorIs(t, execResult.Error(), xerror.ExecutionResponseSizeExceeded())
}

func TestConnector_Execute_RateLimitIncludesRetryAfter(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"status":"error","message":"You have reached your daily limit."}`))
	}))
	t.Cleanup(server.Close)

	conn, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		hubspotclient.Config{BaseURL: server.URL, TimeoutSeconds: 5, AllowPrivateHosts: true},
		Credentials{PrivateAppToken: "pat-na1-rate-limit-token"},
	)
	require.NoError(t, err)

	execResult, err := conn.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		json.RawMessage(`{"type":"hubspot","object_type":"contacts","operation":"search"}`),
		map[string]types.TypedParam{},
	)
	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.Equal(t, result.ExecutionResultTypeObjectRows, execResult.Type())
	require.Error(t, execResult.Error())
	require.ErrorIs(t, execResult.Error(), xerror.Unavailable(xerror.DataSource))
	require.ErrorContains(t, execResult.Error(), "retry after 7")

	hubspotResult, ok := execResult.ObjectRows()
	require.True(t, ok)
	require.NotNil(t, hubspotResult)
	assert.Equal(t, "contacts", hubspotResult.ObjectType)
	assert.Equal(t, "search", hubspotResult.Operation)
	assert.Equal(t, []string{"hs_object_id"}, hubspotResult.ColumnNames)
	assert.Equal(t, 0, hubspotResult.RowCount)
	assert.Empty(t, hubspotResult.Rows)
}
