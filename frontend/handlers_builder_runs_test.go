package frontend

import (
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuilderRunEvidence(t *testing.T) {
	t.Parallel()
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name, query, message string
		status               int
	}{
		{"recorded", "&run_slug=run-one", "0 rows", http.StatusOK},
		{"missing-checks", "&run_slug=run-one", "Missing evidence does not imply a pass", http.StatusOK},
		{"absent-edge", "&run_slug=run-one", "not present in the run's executed version", http.StatusOK},
		{"version-unavailable", "&run_slug=run-one", "could not be loaded", http.StatusOK},
		{"version-mismatch", "&run_slug=run-one", "does not match", http.StatusOK},
		{"empty", "", "No runs on this page", http.StatusOK},
		{"pagination", "&cursor=older-page", "Newest runs", http.StatusOK},
		{"wrong-list-scope", "", "", http.StatusBadGateway},
		{"wrong-pipeline", "&run_slug=run-one", "", http.StatusBadGateway},
		{"wrong-environment", "&run_slug=run-one", "", http.StatusBadGateway},
		{"denied", "&run_slug=run-one", "", http.StatusForbidden},
		{"invalid-slug", "&run_slug=bad%20slug", "", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				assert.Equal(t, "session=fixture", req.Header.Get("Cookie"))
				assert.NotContains(t, req.URL.Path, "latest")
				w.Header().Set("Content-Type", "application/json")
				metadata := map[string]any{"run_slug": "run-one", "pipeline_slug": "daily-sync", "environment_slug": "dev", "pipeline_version_spec_hash": hash, "status": "failed"}
				const runs = "/api/organizations/acme/projects/ops/environments/dev/runs"
				var response any
				switch req.URL.Path {
				case runs:
					assert.Equal(t, "daily-sync", req.URL.Query().Get("pipeline_slug"))
					assert.Equal(t, "20", req.URL.Query().Get("limit"))
					if tc.name == "pagination" {
						assert.Equal(t, "older-page", req.URL.Query().Get("cursor"))
					}
					if tc.name == "wrong-list-scope" {
						metadata["environment_slug"] = "prod"
					}
					if tc.name == "denied" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					list := []any{metadata}
					if tc.name == "empty" {
						list = nil
					}
					response = map[string]any{"runs": list, "next_cursor": "older-page"}
				case runs + "/run-one":
					if tc.name == "wrong-pipeline" {
						metadata["pipeline_slug"] = "other-pipeline"
					}
					if tc.name == "wrong-environment" {
						metadata["environment_slug"] = "prod"
					}
					timeline := map[string]any{
						"events":    []any{map[string]any{"step": "fetch", "type": "step_succeeded", "status": "succeeded"}, map[string]any{"step": "store", "type": "step_failed", "status": "failed"}},
						"snapshots": []any{map[string]any{"step": "fetch", "input": map[string]any{"parameter_values": map[string]any{"password": "never-render-this"}}, "output": map[string]any{"result_type": "rows", "details": map[string]any{"kind": "rows", "row_count": 0, "rows": []any{"never-render-this"}}}}},
					}
					if tc.name != "missing-checks" {
						timeline["structural_checks"] = []any{map[string]any{"step": "store", "phase": "pre_sink", "status": "failed", "reason_code": "type_mismatch"}, map[string]any{"step": "unrelated-step", "phase": "unrelated-check", "status": "passed"}}
					}
					response = map[string]any{"metadata": metadata, "timeline": timeline}
				case "/api/organizations/acme/projects/ops/pipelines/daily-sync/versions/" + hash:
					if tc.name == "version-unavailable" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					versionHash := hash
					if tc.name == "version-mismatch" {
						versionHash = strings.Repeat("b", 64)
					}
					input := "fetch"
					if tc.name == "absent-edge" {
						input = "other"
					}
					response = map[string]any{"spec_hash": versionHash, "spec": map[string]any{"metadata": map[string]any{"slug": "daily-sync", "name": "Daily sync"}, "steps": []any{
						map[string]any{"kind": "query", "slug": "fetch", "query": map[string]any{"data_source_ref": "source", "operation_type": "sql", "operation_ref": "read"}},
						map[string]any{"kind": "query", "slug": "other", "query": map[string]any{"data_source_ref": "source", "operation_type": "sql", "operation_ref": "read"}},
						map[string]any{"kind": "sink", "slug": "store", "sink": map[string]any{"input": input, "data_source_ref": "source", "operation_type": "sql", "operation_ref": "write"}},
					}}}
				default:
					t.Errorf("unexpected API path %s", req.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				assert.NoError(t, json.NewEncoder(w).Encode(response))
			}))
			defer backend.Close()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			app.Get("/builder/run-evidence", router.handleBuilderRunEvidence)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/builder/run-evidence?org_slug=acme&project_slug=ops&environment_slug=dev&pipeline_slug=daily-sync&from_step=fetch&to_step=store"+tc.query, nil)
			req.Header.Set("Cookie", "session=fixture")
			response, err := app.Test(req)
			require.NoError(t, err)
			require.Equal(t, tc.status, response.StatusCode)
			require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			if tc.message != "" {
				require.Contains(t, html.UnescapeString(string(body)), tc.message)
			}
			require.NotContains(t, string(body), "never-render-this")
			require.NotContains(t, string(body), "unrelated-check")
			if tc.name == "recorded" {
				require.Contains(t, string(body), hash)
				require.Contains(t, string(body), "type_mismatch")
				require.Contains(t, string(body), "Failed")
			}
			if strings.HasPrefix(tc.name, "version-") || tc.name == "absent-edge" {
				require.NotContains(t, string(body), "0 rows")
			}
		})
	}
}
