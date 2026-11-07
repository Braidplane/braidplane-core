package crm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

// newTestClient creates a private-host-enabled HubSpot client for local HTTP test servers.
func newTestClient(t *testing.T, baseURL, token string) *Client {
	t.Helper()

	client, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: baseURL, TimeoutSeconds: 5, AllowPrivateHosts: true},
		token,
	)
	require.NoError(t, err)

	return client
}

// TestNew_RejectsMissingAccessToken verifies client construction rejects empty portal tokens.
func TestNew_RejectsMissingAccessToken(t *testing.T) {
	t.Parallel()

	client, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: "https://api.hubapi.com", TimeoutSeconds: 5},
		"",
	)

	require.Nil(t, client)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
}

// TestNew_RejectsInvalidBaseURL verifies client construction rejects malformed and unsupported URLs.
func TestNew_RejectsInvalidBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		baseURL     string
		wantMessage string
	}{
		{
			name:        "unsupported scheme",
			baseURL:     "ftp://api.hubapi.com",
			wantMessage: "hubspot base_url must use http or https",
		},
		{
			name:        "malformed url",
			baseURL:     ":bad",
			wantMessage: "missing protocol scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := New(
				transportsecurity.WithPlaintext(context.Background()),
				Config{BaseURL: tt.baseURL, TimeoutSeconds: 5, AllowPrivateHosts: true},
				"pat-na1-test-token",
			)

			require.Nil(t, client)
			require.Error(t, err)
			require.ErrorIs(t, err, xerror.InvalidInput(xerror.DataSource))
			assert.ErrorContains(t, err, tt.wantMessage)
		})
	}
}

func TestSupportedObjects_ReturnsCommonCRMObjectsAndCopy(t *testing.T) {
	t.Parallel()

	objects := SupportedObjects()
	require.Len(t, objects, 3)
	assert.Equal(t, "contacts", objects[0].Name)
	assert.Equal(t, "Contacts", objects[0].Label)
	assert.Equal(t, "companies", objects[1].Name)
	assert.Equal(t, "deals", objects[2].Name)

	objects[0].Name = "mutated"

	freshObjects := SupportedObjects()
	assert.Equal(t, "contacts", freshObjects[0].Name)
}

func TestClient_Close_AllowsNilAndInitializedClients(t *testing.T) {
	t.Parallel()

	var nilClient *Client
	require.NoError(t, nilClient.Close())
	require.NoError(t, (&Client{}).Close())
	require.NoError(t, (&Client{client: &http.Client{}}).Close())
}

func TestClient_AccountDetails_UsesAccountDetailsEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/account-info/v3/details", r.URL.Path)
		assert.Equal(t, "Bearer pat-na1-test-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"portalId":12345,"companyName":"Acme Inc"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server.URL+"/", "  pat-na1-test-token  ")

	details, err := client.AccountDetails(transportsecurity.WithPlaintext(context.Background()))
	require.NoError(t, err)
	require.NotNil(t, details)
	assert.EqualValues(t, 12345, details.AccountID)
	assert.Equal(t, "Acme Inc", details.CompanyName)
}

func TestClient_ListProperties_ReturnsPropertiesForRequestedObject(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer pat-na1-schema-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/crm/properties/2026-03/contacts":
			_, _ = w.Write([]byte(`{"results":[{"name":"email","label":"Email","type":"string","fieldType":"text","formField":true},{"name":"firstname","label":"First name","type":"string","fieldType":"text","formField":false}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server.URL, "pat-na1-schema-token")

	properties, err := client.ListProperties(transportsecurity.WithPlaintext(context.Background()), "contacts")
	require.NoError(t, err)
	require.Len(t, properties, 2)
	assert.Equal(t, "email", properties[0].Name)
	assert.Equal(t, "Email", properties[0].Label)
	assert.Equal(t, "string", properties[0].Type)
}

func TestClient_ListCustomObjectSchemas_ReturnsActiveSchemasAndProperties(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/crm-object-schemas/2026-03/schemas", r.URL.Path)
		assert.Equal(t, "true", r.URL.Query().Get("includePropertyDefinitions"))
		assert.Equal(t, "Bearer pat-na1-schema-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{
					"archived": false,
					"name": "cars",
					"objectTypeId": "2-7282133",
					"labels": {"singular": "Car", "plural": "Cars"},
					"properties": [
						{
							"name": "vin",
							"label": "VIN",
							"type": "string",
							"hasUniqueValue": true,
							"readOnlyValue": false
						}
					]
				},
				{
					"archived": true,
					"name": "old",
					"objectTypeId": "2-99",
					"labels": {"plural": "Old"}
				}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server.URL, "pat-na1-schema-token")
	schemas, err := client.ListCustomObjectSchemas(transportsecurity.WithPlaintext(context.Background()))
	require.NoError(t, err)
	require.Len(t, schemas, 1)
	assert.Equal(t, "2-7282133", schemas[0].ObjectTypeID)
	assert.Equal(t, "cars", schemas[0].Name)
	assert.Equal(t, "Cars", schemas[0].Label)
	require.Len(t, schemas[0].Properties, 1)
	assert.Equal(t, "vin", schemas[0].Properties[0].Name)
	assert.True(t, schemas[0].Properties[0].HasUniqueValue)
}

func TestClient_List_BuildsProviderQueryAndReturnsPage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/crm/objects/2026-03/companies", r.URL.Path)
		assert.Equal(t, "cursor-1", r.URL.Query().Get("after"))
		assert.Equal(t, "100", r.URL.Query().Get("limit"))
		assert.Equal(t, "name,domain", r.URL.Query().Get("properties"))
		assert.Equal(t, "Bearer pat-na1-list-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{
					"id": "201",
					"properties": {
						"name": "Acme",
						"domain": "acme.example"
					}
				}
			],
			"paging": {
				"next": {
					"after": "cursor-2"
				}
			}
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server.URL, "pat-na1-list-token")

	page, err := client.List(
		transportsecurity.WithPlaintext(context.Background()),
		ListRequest{
			ObjectType: "companies",
			Properties: []string{"name", "domain"},
			After:      "cursor-1",
		},
	)
	require.NoError(t, err)
	require.NotNil(t, page)
	assert.Equal(t, "companies", page.ObjectType)
	require.Len(t, page.Records, 1)
	assert.Equal(t, "201", page.Records[0].ID)
	assert.Equal(t, "Acme", page.Records[0].Properties["name"])
	require.NotNil(t, page.NextAfter)
	assert.Equal(t, "cursor-2", *page.NextAfter)
	assert.True(t, page.HasMore)
}

func TestClient_List_RejectsUnsupportedObjectType(t *testing.T) {
	t.Parallel()

	page, err := (&Client{}).List(
		transportsecurity.WithPlaintext(context.Background()),
		ListRequest{ObjectType: "owners"},
	)

	require.Nil(t, page)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.InvalidInput(xerror.Query))
	assert.ErrorContains(t, err, `unsupported HubSpot object type "owners"`)
}

func TestClient_List_AcceptsCustomObjectTypeID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/crm/objects/2026-03/2-7282133", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server.URL, "pat-na1-custom-token")
	page, err := client.List(transportsecurity.WithPlaintext(context.Background()), ListRequest{ObjectType: "2-7282133"})
	require.NoError(t, err)
	assert.Equal(t, "2-7282133", page.ObjectType)
}

func TestClient_List_RespectsMaxResponseBytes(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"1","properties":{"email":"alice@example.com","firstname":"Alice"}}]}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server.URL, "pat-na1-size-token")

	page, err := client.List(
		transportsecurity.WithPlaintext(context.Background()),
		ListRequest{
			ObjectType:       "contacts",
			MaxResponseBytes: 32,
		},
	)

	require.Nil(t, page)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestClient_AccountDetails_MapsProviderErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		statusCode    int
		retryAfter    string
		responseBody  string
		wantErr       error
		wantSubstring string
	}{
		{
			name:          "unauthorized",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"message":"bad token"}`,
			wantErr:       xerror.InvalidCredentials(),
			wantSubstring: "bad token",
		},
		{
			name:         "forbidden",
			statusCode:   http.StatusForbidden,
			responseBody: `{}`,
			wantErr:      xerror.Forbidden(xerror.DataSource),
		},
		{
			name:          "rate limited",
			statusCode:    http.StatusTooManyRequests,
			retryAfter:    "7",
			responseBody:  `{"message":"slow down"}`,
			wantErr:       xerror.Unavailable(xerror.DataSource),
			wantSubstring: "retry after 7 seconds",
		},
		{
			name:          "bad request",
			statusCode:    http.StatusBadRequest,
			responseBody:  `{"message":"bad filter"}`,
			wantErr:       xerror.InvalidInput(xerror.Query),
			wantSubstring: "bad filter",
		},
		{
			name:          "server error",
			statusCode:    http.StatusInternalServerError,
			responseBody:  `{}`,
			wantErr:       xerror.Unavailable(xerror.DataSource),
			wantSubstring: "status 500",
		},
		{
			name:         "invalid json success body",
			statusCode:   http.StatusOK,
			responseBody: `{"accountId":`,
			wantErr:      xerror.Unexpected(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.retryAfter != "" {
					w.Header().Set("Retry-After", tt.retryAfter)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			t.Cleanup(server.Close)

			client := newTestClient(t, server.URL, "pat-na1-error-token")

			details, err := client.AccountDetails(transportsecurity.WithPlaintext(context.Background()))
			require.Nil(t, details)
			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
			if tt.wantSubstring != "" {
				assert.ErrorContains(t, err, tt.wantSubstring)
			}
		})
	}
}

func TestClient_AccountDetails_MapsTimeoutToDataSourceTimeout(t *testing.T) {
	t.Parallel()

	client := &Client{
		baseURL: "https://api.hubapi.com",
		token:   "pat-na1-timeout-token",
		client: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return nil, &url.Error{
					Op:  http.MethodGet,
					URL: "https://api.hubapi.com/account-info/v3/details",
					Err: timeoutNetError{},
				}
			}),
		},
	}

	details, err := client.AccountDetails(transportsecurity.WithPlaintext(context.Background()))
	require.Nil(t, details)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.Timeout(xerror.DataSource))
}

func TestClient_Search_BuildsProviderPayloadAndReturnsPage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/crm/objects/2026-03/contacts/search", r.URL.Path)
		assert.Equal(t, "Bearer pat-na1-search-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

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
		assert.Equal(t, "cursor-9", payload["after"])
		assert.Equal(t, []any{"-createdate"}, payload["sorts"])

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

	client := newTestClient(t, server.URL, "pat-na1-search-token")

	page, err := client.Search(
		transportsecurity.WithPlaintext(context.Background()),
		SearchRequest{
			ObjectType: "contacts",
			Properties: []string{"email", "firstname"},
			Query:      "alice@example.com",
			Sorts:      []string{"-createdate"},
			After:      "cursor-9",
			Limit:      2,
			FilterGroups: []FilterGroup{{
				Filters: []Filter{{
					PropertyName: "email",
					Operator:     "EQ",
					Value:        "alice@example.com",
				}},
			}},
		},
	)
	require.NoError(t, err)
	require.NotNil(t, page)
	assert.Equal(t, "contacts", page.ObjectType)
	require.Len(t, page.Records, 2)
	assert.Equal(t, "101", page.Records[0].ID)
	assert.Equal(t, "alice@example.com", page.Records[0].Properties["email"])
	require.NotNil(t, page.NextAfter)
	assert.Equal(t, "200", *page.NextAfter)
	assert.True(t, page.HasMore)
}

func TestClient_Upsert_UsesBatchEndpointAndRejectsMixedResults(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/crm/objects/2026-03/contacts/batch/upsert", r.URL.Path)
		var body struct {
			Inputs []struct {
				ID         string         `json:"id"`
				IDProperty string         `json:"idProperty"`
				Properties map[string]any `json:"properties"`
			} `json:"inputs"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		if !assert.Len(t, body.Inputs, 1) {
			return
		}
		assert.Equal(t, "customer-1", body.Inputs[0].ID)
		assert.Equal(t, "external_customer_id", body.Inputs[0].IDProperty)
		assert.Equal(t, "alice@example.com", body.Inputs[0].Properties["email"])
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			_, _ = w.Write([]byte(`{"status":"COMPLETE","results":[{"id":"101","new":true}]}`))
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`{"status":"COMPLETE","results":[],"errors":[{"message":"conflict"}]}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server.URL, "pat-na1-upsert-token")
	request := UpsertRequest{
		ObjectType: "contacts",
		Inputs: []UpsertInput{{
			ID:         "customer-1",
			IDProperty: "external_customer_id",
			Properties: map[string]any{"email": "alice@example.com"},
		}},
	}
	got, err := client.Upsert(transportsecurity.WithPlaintext(t.Context()), request)
	require.NoError(t, err)
	require.Len(t, got.Records, 1)
	assert.Equal(t, "101", got.Records[0].ID)
	assert.True(t, got.Records[0].Created)

	got, err = client.Upsert(transportsecurity.WithPlaintext(t.Context()), request)
	require.Nil(t, got)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.Unavailable(xerror.DataSource))
}

func TestProviderFailurePublicDetailExcludesResponseData(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`{"message":"rejected: [{\"value\":\"customer@example.test: test-token\",\"name\":\"email\"}]"}`,
		`{"message": malformed: customer@example.test: test-token`,
	} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			client := newTestClient(t, "https://api.hubapi.com", "test-token")
			client.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			_, err := client.Upsert(transportsecurity.WithPlaintext(context.Background()), UpsertRequest{ObjectType: "contacts", Inputs: []UpsertInput{{ID: "customer@example.test", IDProperty: "email"}}})
			require.ErrorIs(t, err, xerror.InvalidInput(xerror.Query))
			require.Equal(t, "HubSpot request failed: query invalid input", xerror.PublicDetail(err))
		})
	}
}
