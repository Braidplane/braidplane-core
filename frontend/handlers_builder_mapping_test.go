package frontend

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestBuilderMappingUsesSharedAuthoringBehavior(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	app.Post("/builder/mapping", (&Router{}).handleBuilderMapping)
	mapping := &transformsql.FieldMapping{Input: "input", Fields: []transformsql.MappingField{
		{Name: "sku", Expression: "item_code"}, {Name: "currency", Expression: "'PLN'"},
	}}
	canonical, err := transformsql.WriteFieldMapping(*mapping)
	require.NoError(t, err)
	source := &payload.Contract{Name: "supplier", Fields: []payload.ContractField{
		{Name: "item_code", Type: model.ContractFieldTypeValueString},
	}}
	destination := &payload.Contract{Name: "catalog", Fields: []payload.ContractField{
		{Name: "sku", Type: model.ContractFieldTypeValueString, Required: true},
		{Name: "currency", Type: model.ContractFieldTypeValueString, Required: true},
	}}
	for _, test := range []struct {
		request builderMappingRequest
		status  int
	}{
		{builderMappingRequest{Action: "render", Mapping: mapping}, http.StatusOK},
		{builderMappingRequest{Action: "inspect", SQL: canonical}, http.StatusOK},
		{builderMappingRequest{Action: "inspect", SQL: "SELECT * FROM input WHERE id > 0"}, http.StatusUnprocessableEntity},
		{builderMappingRequest{Action: "check", SQL: canonical, Source: source, Destination: destination}, http.StatusOK},
		{builderMappingRequest{Action: "check", SQL: "SELECT item_code AS sku FROM input", Source: source, Destination: destination}, http.StatusUnprocessableEntity},
		{builderMappingRequest{Action: "suggest", Mapping: mapping, Source: source}, http.StatusUnprocessableEntity},
	} {
		body, err := json.Marshal(test.request)
		require.NoError(t, err)
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/builder/mapping", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response, err := app.Test(request)
		require.NoError(t, err)
		require.Equal(t, test.status, response.StatusCode, string(body))
		require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		var result struct {
			SQL         string                     `json:"sql"`
			Mapping     *transformsql.FieldMapping `json:"mapping"`
			Valid       bool                       `json:"valid"`
			Diagnostics []map[string]string        `json:"diagnostics"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		require.NoError(t, response.Body.Close())
		switch {
		case test.status != http.StatusOK:
			require.False(t, result.Valid)
			require.NotEmpty(t, result.Diagnostics[0]["code"])
		case test.request.Action == "render":
			require.Equal(t, canonical, result.SQL)
		case test.request.Action == "inspect":
			expected, err := transformsql.ReadFieldMapping(canonical)
			require.NoError(t, err)
			require.Equal(t, expected, result.Mapping)
		}
	}
}

func TestBuilderMappingResolvesExactODCSConstraintsBeforeFixtureChecks(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, "/api/organizations/acme/projects/catalog/contracts/products/revisions/v1/inspection", req.URL.Path)
		assert.Equal(t, "product", req.URL.Query().Get("schema_object_id"))
		assert.Equal(t, "session=fixture", req.Header.Get("Cookie"))
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"revision":{"slug":"v1","digest":"sha256:exact","document":{}},"schema_object_id":"product","document_valid":true,"projection_available":true,"compiler_id":"decimal/v1","shape":{"name":"product","fields":[{"name":"price","type":"decimal","precision":20,"scale":2,"required":true,"validations":{"min":9007199254740993.01}}]}}`))
		assert.NoError(t, err)
	}))
	defer backend.Close()
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	app.Post("/builder/mapping", router.handleBuilderMapping)
	for _, test := range []struct {
		value  string
		passed bool
	}{{"9007199254740993.01", true}, {"9007199254740993.00", false}} {
		body, err := json.Marshal(builderMappingRequest{
			Action: "fixture", Org: "acme", Project: "catalog", Step: "map-products", SQL: "SELECT CAST(price AS DECIMAL(20,2)) AS price FROM input", OutputKind: model.TransformOutputKindRows,
			Source:         &payload.Contract{Name: "supplier", Fields: []payload.ContractField{{Name: "price", Type: model.ContractFieldTypeValueString}}},
			DestinationRef: &pipelinespec.ODCSContractRef{ContractSlug: "products", RevisionSlug: "v1", SchemaObjectID: "product"},
			FixtureJSON:    `{"inputs":{"input":[{"price":"` + test.value + `"}]},"expected":[{"price":` + test.value + `}]}`,
		})
		require.NoError(t, err)
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/builder/mapping", bytes.NewReader(body))
		request.Header.Set("Cookie", "session=fixture")
		response, err := app.Test(request)
		require.NoError(t, err)
		var result struct {
			Passed     bool   `json:"passed"`
			OutputJSON string `json:"output_json"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		require.NoError(t, response.Body.Close())
		require.Equal(t, test.passed, result.Passed)
		if test.passed {
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Contains(t, result.OutputJSON, test.value)
		} else {
			require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
			require.Empty(t, result.OutputJSON)
		}
	}
}

func TestBuilderMappingRejectsMalformedRequests(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	app.Post("/builder/mapping", (&Router{}).handleBuilderMapping)
	for _, body := range []string{`{`, `{"action":"inspect","unknown":true}`, `{"action":"inspect"} {}`, `{"action":"run"}`} {
		response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/builder/mapping", bytes.NewBufferString(body)))
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
}

func TestBuilderMappingContractDenialDoesNotFallBackToBrowserShape(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, err := w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"denied"},"document":"private-provider-detail"}`))
		assert.NoError(t, err)
	}))
	defer backend.Close()
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	app.Post("/builder/mapping", router.handleBuilderMapping)
	request := builderMappingRequest{
		Action: "check", Org: "acme", Project: "catalog", SQL: "SELECT price FROM input",
		DestinationRef: &pipelinespec.ODCSContractRef{ContractSlug: "products", RevisionSlug: "v1", SchemaObjectID: "product"},
	}
	for _, test := range []struct {
		revision string
		inline   bool
		status   int
	}{
		{"v1", false, http.StatusForbidden}, {"latest", false, http.StatusBadRequest}, {"v1", true, http.StatusUnprocessableEntity},
	} {
		request.DestinationRef.RevisionSlug = slug.Slug(test.revision)
		if test.inline {
			request.Destination = &payload.Contract{Name: "browser-copy"}
		}
		body, err := json.Marshal(request)
		require.NoError(t, err)
		response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/builder/mapping", bytes.NewReader(body)))
		require.NoError(t, err)
		require.Equal(t, test.status, response.StatusCode)
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "private-provider-detail")
		require.NoError(t, response.Body.Close())
	}
	require.Equal(t, int32(1), calls.Load())
}
