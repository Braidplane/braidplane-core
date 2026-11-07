package frontend

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipelineRunPreviewIdentityAndAvailability(t *testing.T) {
	for _, tt := range []struct {
		name, query, environment, pipeline, recordRun, recordEnvironment, recordPipeline, version, message, runStatus string
		listStatus, recordStatus, versionStatus                                                                       int
		empty, mismatch                                                                                               bool
	}{
		{name: "latest success", environment: "dev", pipeline: "orders"},
		{name: "active observations", environment: "dev", pipeline: "orders", runStatus: "running"},
		{name: "failed run", environment: "dev", pipeline: "orders", runStatus: "failed"},
		{name: "historical selection", query: "?run_slug=older-run", environment: "test", pipeline: "invoices", recordRun: "older-run"},
		{name: "no target", pipeline: "orders"},
		{name: "no runs", environment: "dev", pipeline: "orders", empty: true},
		{name: "history denied", environment: "dev", pipeline: "orders", listStatus: 403},
		{name: "record denied", environment: "dev", pipeline: "orders", recordStatus: 403, message: "Run evidence is unavailable"},
		{name: "record retained away", environment: "dev", pipeline: "orders", recordStatus: 404, message: "Run evidence is unavailable"},
		{name: "wrong pipeline record", environment: "dev", pipeline: "orders", recordPipeline: "invoices", message: "does not match"},
		{name: "wrong environment record", environment: "dev", pipeline: "orders", recordEnvironment: "test", message: "does not match"},
		{name: "wrong run record", environment: "dev", pipeline: "orders", recordRun: "other-run", message: "does not match"},
		{name: "wrong list scope", environment: "dev", pipeline: "orders", mismatch: true, message: "does not match"},
		{name: "invalid selected slug", query: "?run_slug=bad%2Fslug", environment: "dev", pipeline: "orders", message: "Selected run is invalid"},
		{name: "version retained away", environment: "dev", pipeline: "orders", versionStatus: 404},
		{name: "version mismatch", environment: "dev", pipeline: "orders", version: "today-hash"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			versionReads, recordReads, listReads := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				base := "/api/organizations/acme/projects/finance"
				expectedRun := "latest-run"
				if tt.query == "?run_slug=older-run" {
					expectedRun = "older-run"
				}
				metadata := api.PipelineRunMetadata{RunSlug: "latest-run", PipelineSlug: tt.pipeline, EnvironmentSlug: tt.environment, PipelineVersionSpecHash: "executed-hash", Status: api.PipelineRunMetadataStatusSucceeded}
				if tt.runStatus != "" {
					metadata.Status = api.PipelineRunMetadataStatus(tt.runStatus)
				}
				switch r.URL.Path {
				case base + "/environments/" + tt.environment + "/runs":
					listReads++
					assert.Equal(t, tt.pipeline, r.URL.Query().Get("pipeline_slug"))
					assert.Equal(t, "10", r.URL.Query().Get("limit"))
					assert.Equal(t, "desc", r.URL.Query().Get("order"))
					if tt.listStatus != 0 {
						w.WriteHeader(tt.listStatus)
						return
					}
					runs := []api.PipelineRunMetadata{metadata}
					if tt.empty {
						runs = nil
					}
					if tt.mismatch {
						runs[0].EnvironmentSlug = "unrelated"
					}
					assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineRunListResponse{Runs: runs}))
				case base + "/environments/" + tt.environment + "/runs/" + expectedRun:
					recordReads++
					if tt.recordStatus != 0 {
						w.WriteHeader(tt.recordStatus)
						return
					}
					metadata.RunSlug = expectedRun
					if tt.recordRun != "" {
						metadata.RunSlug = tt.recordRun
					}
					if tt.recordPipeline != "" {
						metadata.PipelineSlug = tt.recordPipeline
					}
					if tt.recordEnvironment != "" {
						metadata.EnvironmentSlug = tt.recordEnvironment
					}
					assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineRunRecord{Metadata: metadata}))
				case base + "/pipelines/" + tt.pipeline + "/versions/executed-hash":
					versionReads++
					if tt.versionStatus != 0 {
						w.WriteHeader(tt.versionStatus)
						return
					}
					hash := "executed-hash"
					if tt.version != "" {
						hash = tt.version
					}
					assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineVersion{SpecHash: hash}))
				default:
					t.Errorf("unexpected API lookup: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			var result views.PipelineRunPreview
			app := fiber.New()
			app.Get("/preview", func(c fiber.Ctx) error {
				result = router.loadPipelineRunPreview(c, "acme", "finance", tt.environment, tt.pipeline)
				return c.SendStatus(200)
			})
			response, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/preview"+tt.query, nil))
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			if tt.message != "" {
				require.Contains(t, result.Message, tt.message)
				require.Nil(t, result.Run)
				require.Zero(t, versionReads)
				return
			}
			if tt.environment == "" {
				require.Zero(t, listReads)
				return
			}
			if tt.empty || tt.listStatus != 0 {
				require.Nil(t, result.Run)
				require.Zero(t, recordReads)
				require.Equal(t, tt.listStatus != 0, result.HistoryUnavailable)
				return
			}
			require.NotNil(t, result.Run)
			require.Equal(t, "executed-hash", result.Run.Metadata.PipelineVersionSpecHash)
			if tt.runStatus != "" {
				require.Equal(t, tt.runStatus, string(result.Run.Metadata.Status))
			}
			require.Equal(t, 1, versionReads)
			if tt.versionStatus != 0 || tt.version != "" {
				require.False(t, result.Lineage.Available)
			} else {
				require.True(t, result.Lineage.Available)
			}
			if tt.recordRun != "" {
				require.Equal(t, tt.recordRun, result.SelectedSlug)
			} else {
				require.Equal(t, result.LatestSlug, result.SelectedSlug)
			}
		})
	}
}
