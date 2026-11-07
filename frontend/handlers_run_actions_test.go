package frontend

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/runlineage"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"
)

func TestRunStepActionsRequireCurrentAuthorizedResources(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		status                int
		permissions           []string
		wantSource, wantQuery bool
		message               string
	}{
		{"available", http.StatusOK, []string{"datasources:read", "queries:read"}, true, true, ""},
		{"restricted", http.StatusOK, nil, false, false, "restricted"},
		{"query restricted", http.StatusOK, []string{"datasources:read"}, true, false, "query is restricted"},
		{"failure before snapshot", http.StatusOK, []string{"datasources:read", "queries:read"}, true, true, ""},
		{"deleted", http.StatusNotFound, []string{"datasources:read"}, false, false, "unavailable or deleted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if r.URL.Path == "/api/organizations/acme/data-sources/source/queries/read" {
					_, _ = w.Write([]byte(`{"slug":"read"}`))
				} else {
					_, _ = w.Write([]byte(`{"slug":"source","is_active":true}`))
				}
			}))
			defer backend.Close()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			source, query := "source", "read"
			node := runlineage.Node{Kind: "query", Snapshot: &api.PipelineRunStepSnapshot{Input: api.PipelineRunStepInputSnapshot{DataSourceSlug: &source, OperationRef: &query}}}
			if tc.name == "failure before snapshot" {
				node.Snapshot = nil
				node.Failure = &api.PipelineRunFailureContext{DataSourceSlug: &source}
				node.Planned.OperationRef = query
			}
			app := fiber.New()
			app.Get("/", func(c fiber.Ctx) error {
				return c.JSON(router.loadRunStepActions(c, "acme", "dev", node, views.NewOrganizationPermissions(tc.permissions...)))
			})
			resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
			require.NoError(t, err)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			var actions views.RunStepActions
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&actions))
			require.Equal(t, tc.wantSource, actions.DataSourceHref != "")
			require.Equal(t, tc.wantQuery, actions.QueryHref != "")
			require.Contains(t, actions.Message, tc.message)
			if tc.name == "restricted" {
				require.Zero(t, requests)
			}
		})
	}
}
