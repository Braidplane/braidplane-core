package frontend

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuilderSchemaDatasets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, source string
		count        int
		warning      bool
	}{
		{"sql", `{"type":"postgres","schema":{"schema_type":"sql","type":"postgres","tables":[{"name":"payments","columns":[{"name":"amount","type":"numeric(12,2)"}]}]}}`, 1, false},
		{"unknown", `{"type":"postgres","schema":{"schema_type":"sql","type":"postgres","tables":[{"name":"payments","columns":[{"name":"amount","type":"mystery"}]}]}}`, 0, true},
		{"s3 csv", `{"type":"s3","schema":{"schema_type":"file","type":"s3","files":[{"name":"customers.csv","fields":[{"name":"id","type":"string"},{"name":"name","type":"string"},{"name":"email","type":"string"}]}]}}`, 3, false},
		{"s3 unknown", `{"type":"s3","schema":{"schema_type":"file","type":"s3","files":[{"name":"customers.json","fields":[{"name":"value","type":"mystery"}]}]}}`, 0, true},
		{"document", `{"type":"mongodb","schema":{"schema_type":"document","type":"mongodb","collections":[{"name":"orders","fields":[{"name":"items","type":"array"}]}]}}`, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ds api.DataSource
			require.NoError(t, json.Unmarshal([]byte(tc.source), &ds))
			datasets := builderSchemaDatasets(&ds)
			require.Len(t, datasets, 1)
			require.Len(t, datasets[0].Fields, tc.count)
			require.Equal(t, tc.warning, datasets[0].Warning != "")
			if tc.count > 0 {
				require.True(t, datasets[0].Fields[0].Nullable)
				require.False(t, datasets[0].Fields[0].Required)
			}
			if tc.name == "s3 csv" {
				for _, field := range datasets[0].Fields {
					require.Equal(t, "string", field.Type.String())
				}
			}
			if tc.name == "sql" {
				require.Equal(t, 12, *datasets[0].Fields[0].Precision)
				require.Equal(t, 2, *datasets[0].Fields[0].Scale)
			}
		})
	}
}

func TestBuilderSchemaPreservesAPIAuthorization(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/api/organizations/acme/data-sources/warehouse", req.URL.Path)
		assert.Equal(t, "session=fixture", req.Header.Get("Cookie"))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer backend.Close()
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	app.Get("/builder/schema", router.handleBuilderSchema)
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"?org_slug=acme&data_source_slug=warehouse", http.StatusForbidden},
		{"?org_slug=bad%20slug&data_source_slug=warehouse", http.StatusBadRequest},
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/builder/schema"+tc.query, nil)
		req.Header.Set("Cookie", "session=fixture")
		response, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, tc.status, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestBuilderSchemaS3OperationKeyIsRelativeToConfiguredPrefix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ prefix, name, want string }{
		{"exports/", "exports/nested/orders.json", "nested/orders.json"},
		{" /exports/ ", "exports/orders.json", "orders.json"},
		{"", "nested/orders.json", "nested/orders.json"},
		{"exports", "exports-other/orders.json", ""},
	} {
		t.Run(tc.name+tc.prefix, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{
				"type": "s3", "config": map[string]any{"type": "s3", "prefix": tc.prefix},
				"schema": map[string]any{"schema_type": "file", "type": "s3", "files": []any{map[string]any{"name": tc.name, "fields": []any{}}}},
			})
			require.NoError(t, err)
			var ds api.DataSource
			require.NoError(t, json.Unmarshal(data, &ds))
			datasets := builderSchemaDatasets(&ds)
			require.Len(t, datasets, 1)
			require.Equal(t, tc.want, datasets[0].SourceKey)
		})
	}
}
