package frontend

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouter_LoadPipelineConnectionsBuildsCurrentComponent(t *testing.T) {
	t.Parallel()

	requests := 0
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/organizations/acme/projects/ops/environments/prod/pipeline-triggers", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("limit"))
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			_, _ = w.Write([]byte(`{"pipeline_triggers":[
				{"slug":"a-to-b","type":"completion","enabled":true,"revision":2,"pipeline_slug":"b","completion":{"upstream_pipeline_slug":"a","condition":"success","execution_grant_enabled":true}},
				{"slug":"b-to-c","type":"completion","enabled":true,"revision":4,"pipeline_slug":"c","completion":{"upstream_pipeline_slug":"b","condition":"final_failure","execution_grant_enabled":false}}
			],"has_more":true,"next_cursor":"second"}`))
			return
		}
		assert.Equal(t, "second", r.URL.Query().Get("cursor"))
		_, _ = w.Write([]byte(`{"pipeline_triggers":[],"has_more":false}`))
	}))
	t.Cleanup(apiServer.Close)

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	var gotNodes, gotEdges int
	var gotState, gotCondition string
	app.Get("/", func(c fiber.Ctx) error {
		connections, unauthorized := router.loadPipelineConnections(c, "acme", "ops", "prod", "b")
		require.False(t, unauthorized)
		require.False(t, connections.Partial)
		require.False(t, connections.Unavailable)
		gotNodes, gotEdges = len(connections.Nodes), len(connections.Edges)
		gotState, gotCondition = connections.Edges[1].State, connections.Edges[1].Condition
		require.Positive(t, connections.CanvasWidth)
		require.Positive(t, connections.CanvasHeight)
		return c.SendStatus(http.StatusNoContent)
	})

	response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.Equal(t, 2, requests)
	require.Equal(t, 3, gotNodes)
	require.Equal(t, 2, gotEdges)
	require.Equal(t, "Revoked", gotState)
	require.Equal(t, "final_failure", gotCondition)
}

func TestRouter_LoadPipelineConnectionsMarksRepeatedCursorPartial(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pipeline_triggers":[],"has_more":true,"next_cursor":"stuck"}`))
	}))
	t.Cleanup(apiServer.Close)

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	app.Get("/", func(c fiber.Ctx) error {
		connections, unauthorized := router.loadPipelineConnections(c, "acme", "ops", "prod", "b")
		require.False(t, unauthorized)
		require.True(t, connections.Partial)
		return c.SendStatus(http.StatusNoContent)
	})

	response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusNoContent, response.StatusCode)
}

func TestRouter_LoadPipelineConnectionsKeepsNearestNodesAtLimit(t *testing.T) {
	triggers := []map[string]any{}
	add := func(from, to, name string) {
		triggers = append(triggers, map[string]any{"slug": name, "type": "completion", "enabled": true, "revision": 1, "pipeline_slug": to, "completion": map[string]any{"upstream_pipeline_slug": from, "condition": "success", "execution_grant_enabled": true}})
	}
	add("zz-selected", "zz-neighbor", "direct")
	previous := "zz-neighbor"
	for i := 0; i < 45; i++ {
		next := fmt.Sprintf("a-%02d", i)
		add(previous, next, fmt.Sprintf("edge-%d", i))
		previous = next
	}
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"pipeline_triggers": triggers, "has_more": false}))
	}))
	t.Cleanup(apiServer.Close)
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	var result views.PipelineConnections
	app.Get("/", func(c fiber.Ctx) error {
		result, _ = router.loadPipelineConnections(c, "acme", "ops", "prod", "zz-selected")
		return c.SendStatus(http.StatusNoContent)
	})
	response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.True(t, result.Truncated)
	require.Len(t, result.Nodes, 40)
	slugs := []string{}
	for _, node := range result.Nodes {
		slugs = append(slugs, node.Slug)
	}
	require.Contains(t, slugs, "zz-selected")
	require.Contains(t, slugs, "zz-neighbor")
	require.Len(t, result.Edges, 39)
}
