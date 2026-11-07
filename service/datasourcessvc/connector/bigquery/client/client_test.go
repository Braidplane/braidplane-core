package client

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func fakeServiceAccountJSON(tokenURL string) string {
	data, err := os.ReadFile("../../../../../pkg/testutil/testdata/bigquery-service-account.json")
	if err != nil {
		panic(err)
	}
	var payload map[string]string
	if err := json.Unmarshal(data, &payload); err != nil {
		panic(err)
	}
	payload["token_uri"] = tokenURL
	data, err = json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestSchemaRequestsRejectExhaustedByteQuotaBeforeProviderRequest(t *testing.T) {
	t.Parallel()

	tokenServer := newTestServer(t, tokenHandler(t, "unused-token"))
	client := newTestClient(t, "http://127.0.0.1:1", tokenServer.URL+"/token")
	ctx := schemaquota.WithQuota(transportsecurity.WithPlaintext(t.Context()), 1, 0)

	for name, list := range map[string]func(context.Context) error{
		"Datasets": func(ctx context.Context) error {
			_, err := client.ListDatasets(ctx, ListDatasetsRequest{ProjectID: "analytics-prod"})
			return err
		},
		"Tables": func(ctx context.Context) error {
			_, err := client.ListTables(ctx, ListTablesRequest{ProjectID: "analytics-prod", DatasetID: "events"})
			return err
		},
		"TableMetadata": func(ctx context.Context) error {
			_, err := client.GetTable(ctx, GetTableRequest{ProjectID: "analytics-prod", DatasetID: "events", TableID: "clicks"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := list(ctx)
			require.Error(t, err)
			require.True(t, xerror.Is(err, xerror.ExecutionUnavailable()))
		})
	}
}

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func newTestClient(t *testing.T, baseURL, tokenURL string) *Client {
	t.Helper()

	client, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{
			BaseURL:           baseURL,
			TimeoutSeconds:    5,
			AllowPrivateHosts: true,
		},
		Credentials{ServiceAccountJSON: fakeServiceAccountJSON(tokenURL)},
	)
	require.NoError(t, err)
	return client
}

func tokenHandler(t *testing.T, accessToken string) http.HandlerFunc {
	t.Helper()
	var account serviceAccount
	require.NoError(t, json.Unmarshal([]byte(fakeServiceAccountJSON("")), &account))
	block, _ := pem.Decode([]byte(account.PrivateKey))
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err)

	return func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/token", r.URL.Path)
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if !assert.NoError(t, r.ParseForm()) {
			return
		}
		assert.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", r.Form.Get("grant_type"))
		assert.NotEmpty(t, r.Form.Get("assertion"))
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if !assert.Len(t, parts, 3) {
			return
		}
		signature, err := base64.RawURLEncoding.DecodeString(parts[2])
		if !assert.NoError(t, err) {
			return
		}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		assert.NoError(t, rsa.VerifyPKCS1v15(&key.(*rsa.PrivateKey).PublicKey, crypto.SHA256, digest[:], signature))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + accessToken + `","token_type":"Bearer","expires_in":3600}`))
	}
}

func TestServiceAccountJWTKeyFormats(t *testing.T) {
	t.Parallel()
	var account serviceAccount
	require.NoError(t, json.Unmarshal([]byte(fakeServiceAccountJSON("")), &account))
	block, _ := pem.Decode([]byte(account.PrivateKey))
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err)
	key := parsed.(*rsa.PrivateKey)
	for name, encoded := range map[string][]byte{
		"PKCS8": block.Bytes,
		"PKCS1": x509.MarshalPKCS1PrivateKey(key),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tokenServer := newTestServer(t, tokenHandler(t, "key-format-token"))
			provider := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{}`) })
			client := newTestClient(t, provider.URL, tokenServer.URL+"/token")
			client.serviceAccount.PrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
			require.NoError(t, client.TestConnection(transportsecurity.WithPlaintext(t.Context()), TestConnectionRequest{ProjectID: "fixture"}))
		})
	}
}

func TestMalformedServiceAccountKeyRejectsBeforeProviderRequest(t *testing.T) {
	t.Parallel()
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecDER, err := x509.MarshalPKCS8PrivateKey(ecKey)
	require.NoError(t, err)
	for name, key := range map[string]string{
		"MissingPEM":   "malformed-key",
		"MalformedDER": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("malformed")})),
		"NonRSA":       string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER})),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				t.Error("invalid key reached provider")
				w.WriteHeader(http.StatusInternalServerError)
			})
			client := newTestClient(t, server.URL, server.URL+"/token")
			client.serviceAccount.PrivateKey = key
			_, err := client.ListDatasets(transportsecurity.WithPlaintext(t.Context()), ListDatasetsRequest{ProjectID: "analytics-prod"})
			require.Error(t, err)
			require.True(t, xerror.Is(err, xerror.InvalidCredentials()))
			require.NotContains(t, err.Error(), key)
		})
	}
}

func TestNewRejectsMissingServiceAccountJSON(t *testing.T) {
	t.Parallel()

	client, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: "https://bigquery.googleapis.com", TimeoutSeconds: 5},
		Credentials{},
	)

	require.Nil(t, client)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
}

func TestNewRejectsMalformedServiceAccountJSON(t *testing.T) {
	t.Parallel()

	client, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: "https://bigquery.googleapis.com", TimeoutSeconds: 5},
		Credentials{ServiceAccountJSON: `{"type":"service_account"}`},
	)

	require.Nil(t, client)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
	assert.ErrorContains(t, err, "client_email")
}

func TestClientTestConnectionUsesDatasetsEndpoint(t *testing.T) {
	t.Parallel()

	tokenServer := newTestServer(t, tokenHandler(t, "fake-access-token"))
	providerServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/bigquery/v2/projects/analytics-prod/datasets", r.URL.Path)
		assert.Equal(t, "1", r.URL.Query().Get("maxResults"))
		assert.Equal(t, "Bearer fake-access-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"datasets":[]}`))
	})

	client := newTestClient(t, providerServer.URL, tokenServer.URL+"/token")

	err := client.TestConnection(
		transportsecurity.WithPlaintext(context.Background()),
		TestConnectionRequest{ProjectID: "analytics-prod", Location: "US"},
	)

	require.NoError(t, err)
}

func TestClientGetTablePreservesObservedIdentityAndResourceType(t *testing.T) {
	t.Parallel()

	for _, resourceType := range []TableType{TableTypeTable, TableTypeView, TableTypeExternal, TableTypeMaterializedView, TableTypeSnapshot} {
		t.Run(resourceType.String(), func(t *testing.T) {
			t.Parallel()
			tokenServer := newTestServer(t, tokenHandler(t, "fake-metadata-token"))
			providerServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/bigquery/v2/projects/analytics-prod/datasets/analytics/tables/contacts", r.URL.Path)
				assert.Empty(t, r.URL.RawQuery)
				assert.Equal(t, "Bearer fake-metadata-token", r.Header.Get("Authorization"))
				_, _ = io.WriteString(w, `{"tableReference":{"projectId":"analytics-prod","datasetId":"analytics","tableId":"contacts"},"type":"`+resourceType.String()+`","schema":{"fields":[{"name":"hs_object_id","type":"STRING","mode":"REQUIRED"},{"name":"amount","type":"NUMERIC","precision":"18","scale":"0"},{"name":"default_amount","type":"NUMERIC"}]}}`)
			})
			client := newTestClient(t, providerServer.URL, tokenServer.URL+"/token")

			metadata, err := client.GetTable(transportsecurity.WithPlaintext(t.Context()), GetTableRequest{ProjectID: "analytics-prod", DatasetID: "analytics", TableID: "contacts"})

			require.NoError(t, err)
			require.Equal(t, &TableMetadata{
				ProjectID: "analytics-prod", DatasetID: "analytics", TableID: "contacts", Type: resourceType,
				Fields: []Field{{Name: "hs_object_id", Type: "STRING", Mode: "REQUIRED"}, {Name: "amount", Type: "NUMERIC", Precision: "18", Scale: "0"}, {Name: "default_amount", Type: "NUMERIC"}},
			}, metadata)
		})
	}
}

func TestClientGetTableRejectsUnobservedOrMismatchedTargetFacts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, project, dataset, table, resourceType string
	}{
		{"missing project", "", "analytics", "contacts", "TABLE"},
		{"missing dataset", "analytics-prod", "", "contacts", "TABLE"},
		{"missing table", "analytics-prod", "analytics", "", "TABLE"},
		{"different project", "provider-private-value", "analytics", "contacts", "TABLE"},
		{"different dataset", "analytics-prod", "provider-private-value", "contacts", "TABLE"},
		{"different table", "analytics-prod", "analytics", "provider-private-value", "TABLE"},
		{"missing type", "analytics-prod", "analytics", "contacts", ""},
		{"unknown type", "analytics-prod", "analytics", "contacts", "unknown"},
		{"unsupported type", "analytics-prod", "analytics", "contacts", "provider-private-value"},
		{"noncanonical type", "analytics-prod", "analytics", "contacts", "table"},
		{"empty response", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tokenServer := newTestServer(t, tokenHandler(t, "fake-metadata-token"))
			providerServer := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"tableReference": map[string]string{"projectId": tc.project, "datasetId": tc.dataset, "tableId": tc.table},
					"type":           tc.resourceType,
					"schema":         map[string]any{"fields": []any{}},
				}))
			})
			client := newTestClient(t, providerServer.URL, tokenServer.URL+"/token")

			metadata, err := client.GetTable(transportsecurity.WithPlaintext(t.Context()), GetTableRequest{ProjectID: "analytics-prod", DatasetID: "analytics", TableID: "contacts"})

			require.Nil(t, metadata)
			require.ErrorIs(t, err, xerror.Unavailable(xerror.DataSource))
			require.ErrorContains(t, err, "metadata is incomplete, mismatched, or has an unsupported resource type")
			assert.NotContains(t, err.Error(), "provider-private-value")
		})
	}
}

func TestClientGetTableRejectsMissingRequestIdentityBeforeDispatch(t *testing.T) {
	t.Parallel()

	client := &Client{}
	for _, req := range []GetTableRequest{
		{DatasetID: "analytics", TableID: "contacts"},
		{ProjectID: "analytics-prod", TableID: "contacts"},
		{ProjectID: "analytics-prod", DatasetID: "analytics"},
	} {
		metadata, err := client.GetTable(transportsecurity.WithPlaintext(t.Context()), req)
		require.Nil(t, metadata)
		require.ErrorIs(t, err, xerror.InvalidInput(xerror.DataSource))
	}
}

func TestClientQueryBuildsParameterizedJobsQueryRequest(t *testing.T) {
	t.Parallel()

	tokenServer := newTestServer(t, tokenHandler(t, "fake-query-token"))
	providerServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/bigquery/v2/projects/analytics-prod/queries", r.URL.Path)
		assert.Equal(t, "Bearer fake-query-token", r.Header.Get("Authorization"))

		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}

		var payload map[string]any
		if !assert.NoError(t, json.Unmarshal(body, &payload)) {
			return
		}
		assert.Equal(t, false, payload["useLegacySql"])
		assert.Equal(t, "NAMED", payload["parameterMode"])
		assert.Equal(t, "EU", payload["location"])
		assert.Equal(t, "500000", payload["maximumBytesBilled"])
		assert.InDelta(t, 1000, payload["timeoutMs"], 0.000001)
		assert.InDelta(t, 25, payload["maxResults"], 0.000001)
		assert.Equal(t, "SELECT * FROM `analytics.orders` WHERE region = @region", payload["query"])
		if !assert.Len(t, payload["queryParameters"], 5) {
			return
		}

		values := []any{"north-america", "9007199254740993", "false", "", nil}
		for i, parameter := range payload["queryParameters"].([]any) {
			value := parameter.(map[string]any)["parameterValue"].(map[string]any)
			assert.Equal(t, values[i], value["value"])
			assert.Contains(t, value, "value")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"jobComplete": true,
			"schema": {"fields": [
				{"name": "order_id", "type": "STRING", "mode": "REQUIRED"},
				{"name": "total_amount", "type": "NUMERIC", "mode": "NULLABLE"}
			]},
			"rows": [
				{"f": [{"v": "o-100"}, {"v": 12345678901234567890.123456789}]}
			],
			"totalRows": "1"
		}`))
	})

	client := newTestClient(t, providerServer.URL, tokenServer.URL+"/token")

	got, err := client.Query(
		transportsecurity.WithPlaintext(context.Background()),
		QueryRequest{
			ProjectID:          "analytics-prod",
			Location:           "EU",
			SQL:                "SELECT * FROM `analytics.orders` WHERE region = @region",
			MaximumBytesBilled: 500_000,
			Timeout:            time.Second,
			MaxResultRows:      25,
			Parameters: []QueryParameter{{
				Name:  "region",
				Type:  "STRING",
				Value: "north-america",
			}, {Name: "count", Type: "INT64", Value: int64(9007199254740993)}, {Name: "active", Type: "BOOL", Value: false}, {Name: "empty", Type: "STRING", Value: ""}, {Name: "missing", Type: "STRING", Value: nil}},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []Field{
		{Name: "order_id", Type: "STRING", Mode: "REQUIRED"},
		{Name: "total_amount", Type: "NUMERIC", Mode: "NULLABLE"},
	}, got.Schema)
	assert.Equal(t, []map[string]any{{"order_id": "o-100", "total_amount": json.Number("12345678901234567890.123456789")}}, got.Rows)
}

func TestClientQueryMapsMissingRemoteRoutinePermissionToForbidden(t *testing.T) {
	t.Parallel()

	tokenServer := newTestServer(t, tokenHandler(t, "fake-read-only-token"))
	providerServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/bigquery/v2/projects/analytics-prod/queries", r.URL.Path)
		assert.Equal(t, "Bearer fake-read-only-token", r.Header.Get("Authorization"))

		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}
		var payload map[string]any
		if !assert.NoError(t, json.Unmarshal(body, &payload)) {
			return
		}
		assert.Equal(t, "SELECT `analytics.remote_routine`(1)", payload["query"])

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(
			`{"error":{"message":"Access Denied: Permission bigquery.connections.use denied"}}`,
		))
	})

	client := newTestClient(t, providerServer.URL, tokenServer.URL+"/token")

	got, err := client.Query(
		transportsecurity.WithPlaintext(context.Background()),
		QueryRequest{
			ProjectID: "analytics-prod",
			Location:  "US",
			SQL:       "SELECT `analytics.remote_routine`(1)",
		},
	)

	require.Nil(t, got)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.Forbidden(xerror.DataSource))
	assert.NotContains(t, err.Error(), "bigquery.connections.use denied")
}

func TestClientQueryFollowsProviderResultPagesWithinRowLimit(t *testing.T) {
	t.Parallel()

	tokenServer := newTestServer(t, tokenHandler(t, "fake-page-token"))
	providerServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer fake-page-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/bigquery/v2/projects/analytics-prod/queries":
			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				return
			}

			var payload map[string]any
			if !assert.NoError(t, json.Unmarshal(body, &payload)) {
				return
			}
			assert.InDelta(t, 2, payload["maxResults"], 0.000001)

			_, _ = w.Write([]byte(`{
				"jobComplete": true,
				"jobReference": {"projectId": "analytics-prod", "jobId": "job-123", "location": "EU"},
				"schema": {"fields": [{"name": "order_id", "type": "STRING"}]},
				"rows": [{"f": [{"v": "o-100"}]}],
				"pageToken": "next-page",
				"totalRows": "2"
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/bigquery/v2/projects/analytics-prod/queries/job-123":
			assert.Equal(t, "next-page", r.URL.Query().Get("pageToken"))
			assert.Equal(t, "EU", r.URL.Query().Get("location"))
			assert.Equal(t, "1", r.URL.Query().Get("maxResults"))

			_, _ = w.Write([]byte(`{
				"jobComplete": true,
				"schema": {"fields": [{"name": "order_id", "type": "STRING"}]},
				"rows": [{"f": [{"v": "o-101"}]}],
				"totalRows": "2"
			}`))
		default:
			t.Fatalf("unexpected provider request: %s %s", r.Method, r.URL.String())
		}
	})

	client := newTestClient(t, providerServer.URL, tokenServer.URL+"/token")

	got, err := client.Query(
		transportsecurity.WithPlaintext(context.Background()),
		QueryRequest{
			ProjectID:     "analytics-prod",
			Location:      "EU",
			SQL:           "SELECT order_id FROM `analytics.orders`",
			MaxResultRows: 2,
		},
	)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []Field{{Name: "order_id", Type: "STRING"}}, got.Schema)
	assert.Equal(t, []map[string]any{
		{"order_id": "o-100"},
		{"order_id": "o-101"},
	}, got.Rows)
}

func TestClientInsertRowsBuildsInsertAllRequest(t *testing.T) {
	t.Parallel()

	tokenServer := newTestServer(t, tokenHandler(t, "fake-insert-token"))
	providerServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/bigquery/v2/projects/analytics-prod/datasets/analytics/tables/orders/insertAll", r.URL.Path)
		assert.Equal(t, "Bearer fake-insert-token", r.Header.Get("Authorization"))

		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}

		var payload map[string]any
		if !assert.NoError(t, json.Unmarshal(body, &payload)) {
			return
		}
		rows, ok := payload["rows"].([]any)
		if !assert.True(t, ok) {
			return
		}
		if !assert.Len(t, rows, 2) {
			return
		}
		assert.Equal(t, false, payload["skipInvalidRows"])
		assert.Equal(t, false, payload["ignoreUnknownValues"])

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	client := newTestClient(t, providerServer.URL, tokenServer.URL+"/token")

	got, err := client.InsertRows(
		transportsecurity.WithPlaintext(context.Background()),
		InsertRowsRequest{
			ProjectID:      "analytics-prod",
			DatasetID:      "analytics",
			TableID:        "orders",
			InsertIDPrefix: "run-abc",
			Rows: []map[string]any{
				{"order_id": "o-100"},
				{"order_id": "o-101"},
			},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 2, got.InsertedRows)
}

func TestClientMapsTokenInvalidGrantToInvalidCredentials(t *testing.T) {
	t.Parallel()

	tokenServer := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`))
	})
	providerServer := newTestServer(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("provider should not be called when token exchange fails")
	})
	client := newTestClient(t, providerServer.URL, tokenServer.URL+"/token")

	err := client.TestConnection(
		transportsecurity.WithPlaintext(context.Background()),
		TestConnectionRequest{ProjectID: "analytics-prod"},
	)

	require.Error(t, err)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
	assert.NotContains(t, err.Error(), "Invalid JWT Signature")
}

func TestClientMapsProviderErrorsSafely(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		statusCode   int
		responseBody string
		retryAfter   string
		wantErr      error
		wantPublic   string
	}{
		{
			name:         "unauthorized",
			statusCode:   http.StatusUnauthorized,
			responseBody: `{"error":{"message":"Request had invalid authentication credentials. token=private-runtime-token"}}`,
			wantErr:      xerror.InvalidCredentials(),
		},
		{
			name:         "forbidden",
			statusCode:   http.StatusForbidden,
			responseBody: `{"error":{"message":"Access Denied: Project analytics-prod"}}`,
			wantErr:      xerror.Forbidden(xerror.DataSource),
		},
		{
			name:         "streamingBillingDisabled",
			statusCode:   http.StatusForbidden,
			responseBody: `{"error":{"message":"Access Denied: analytics-prod: Streaming insert is not allowed in the free tier token=private-runtime-token"}}`,
			wantErr:      xerror.Forbidden(xerror.DataSource),
			wantPublic:   streamingBillingPublicDetail,
		},
		{
			name:         "rateLimited",
			statusCode:   http.StatusTooManyRequests,
			responseBody: `{"error":{"message":"Quota exceeded"}}`,
			retryAfter:   "token=private-runtime-token",
			wantErr:      xerror.Unavailable(xerror.DataSource),
		},
		{
			name:         "serverUnavailable",
			statusCode:   http.StatusServiceUnavailable,
			responseBody: `{}`,
			wantErr:      xerror.Unavailable(xerror.DataSource),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tokenServer := newTestServer(t, tokenHandler(t, "fake-error-token"))
			providerServer := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				if tt.retryAfter != "" {
					w.Header().Set("Retry-After", tt.retryAfter)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			})
			client := newTestClient(t, providerServer.URL, tokenServer.URL+"/token")

			err := client.TestConnection(
				transportsecurity.WithPlaintext(context.Background()),
				TestConnectionRequest{ProjectID: "analytics-prod"},
			)

			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
			require.Equal(t, tt.wantPublic, xerror.PublicDetail(err))
			assert.NotContains(t, err.Error(), "private-runtime-token")
			assert.NotContains(t, err.Error(), "analytics-prod")
			assert.NotContains(t, err.Error(), "Quota exceeded")
			assert.NotContains(t, err.Error(), "Access Denied")
		})
	}
}

func TestClientQueryRespectsResponseSizeLimit(t *testing.T) {
	t.Parallel()

	tokenServer := newTestServer(t, tokenHandler(t, "fake-size-token"))
	providerServer := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema":{"fields":[{"name":"id","type":"STRING"}]},"rows":[{"f":[{"v":"too-large"}]}]}`))
	})
	client := newTestClient(t, providerServer.URL, tokenServer.URL+"/token")

	got, err := client.Query(
		transportsecurity.WithPlaintext(context.Background()),
		QueryRequest{
			ProjectID:        "analytics-prod",
			SQL:              "SELECT id FROM `analytics.orders`",
			MaxResponseBytes: 16,
		},
	)

	require.Nil(t, got)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestClientTestConnectionHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	tokenServer := newTestServer(t, tokenHandler(t, "fake-canceled-token"))
	providerServer := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"datasets":[]}`))
	})
	client := newTestClient(t, providerServer.URL, tokenServer.URL+"/token")

	ctx, cancel := context.WithCancel(transportsecurity.WithPlaintext(context.Background()))
	cancel()

	err := client.TestConnection(ctx, TestConnectionRequest{ProjectID: "analytics-prod"})

	require.Error(t, err)
	require.ErrorIs(t, err, xerror.Timeout(xerror.DataSource))
}

func TestClientTestConnectionMapsUnreachableProvider(t *testing.T) {
	t.Parallel()

	tokenServer := newTestServer(t, tokenHandler(t, "fake-unreachable-token"))
	providerURL := (&url.URL{Scheme: "http", Host: "127.0.0.1:1"}).String()
	client := newTestClient(t, providerURL, tokenServer.URL+"/token")

	err := client.TestConnection(
		transportsecurity.WithPlaintext(context.Background()),
		TestConnectionRequest{ProjectID: "analytics-prod"},
	)

	require.Error(t, err)
	require.ErrorIs(t, err, xerror.Unavailable(xerror.DataSource))
	require.NotContains(t, strings.ToLower(err.Error()), "private key")
}

func TestJSONResponsesRejectTrailingContent(t *testing.T) {
	for _, body := range []string{`{"datasets":[]} {}`, `{"datasets":[]} trailing`} {
		t.Run(body, func(t *testing.T) {
			server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
			tokenServer := newTestServer(t, tokenHandler(t, "test-token"))
			client := newTestClient(t, server.URL, tokenServer.URL+"/token")
			_, err := client.ListDatasets(transportsecurity.WithPlaintext(t.Context()), ListDatasetsRequest{ProjectID: "analytics-prod"})
			require.Error(t, err)
		})
	}
}

func TestSDKInsertRejectsSerializedRequestOverBudgetBeforeDispatch(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("oversized SDK request reached provider")
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := newTestClient(t, server.URL, server.URL+"/token")
	_, err := client.InsertRows(transportsecurity.WithPlaintext(t.Context()), InsertRowsRequest{ProjectID: "fixture", DatasetID: "crm", TableID: "contacts", MaxRequestBytes: 1, Rows: []map[string]any{{"id": "1"}}})
	require.ErrorIs(t, err, xerror.ExecutionRequestSizeExceeded())
}

func TestOAuthJWTRejectsOversizedTokenResponse(t *testing.T) {
	t.Parallel()
	tokenServer := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"`+strings.Repeat("x", 1<<20)+`"}`)
	})
	client := newTestClient(t, "http://127.0.0.1:1", tokenServer.URL+"/token")
	err := client.TestConnection(transportsecurity.WithPlaintext(t.Context()), TestConnectionRequest{ProjectID: "fixture"})
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestStreamingBillingFailureHasSafePublicRemediation(t *testing.T) {
	t.Parallel()
	err := mapProviderError(&http.Response{StatusCode: http.StatusForbidden}, []byte(`{"error":{"message":"Access Denied: private-project: Streaming insert is not allowed in the free tier"}}`))
	require.ErrorIs(t, err, xerror.Forbidden(xerror.DataSource))
	require.Equal(t, "BigQuery streaming inserts require billing to be enabled for this Google Cloud project.", xerror.PublicDetail(err))
	require.Empty(t, xerror.PublicDetail(mapProviderError(&http.Response{StatusCode: http.StatusForbidden}, []byte(`{"error":{"message":"private provider detail"}}`))))
}
