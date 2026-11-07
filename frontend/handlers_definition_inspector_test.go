package frontend

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefinitionAuthoritiesNeverReplaceRequestedContent(t *testing.T) {
	oldHash, newHash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var oldSpec, newSpec api.PipelineSpec
	require.NoError(t, json.Unmarshal([]byte(`{"metadata":{"slug":"daily-sync","name":"Executed V1"},"steps":[]}`), &oldSpec))
	require.NoError(t, json.Unmarshal([]byte(`{"metadata":{"slug":"daily-sync","name":"Current V2"},"steps":[]}`), &newSpec))
	for _, tc := range []struct {
		name, query, want, absent    string
		run, mismatch, noCurrentHash bool
		status                       int
	}{
		{name: "current", query: "definition=current", want: "Current V2", absent: "Executed V1"},
		{name: "current hash missing", query: "definition=current&spec_hash=" + oldHash, want: "Current materialization changed", absent: "Current V2", noCurrentHash: true},
		{name: "current changed", query: "definition=current&spec_hash=" + oldHash, want: "Current materialization changed", absent: "Current V2"},
		{name: "draft changed", query: "definition=draft&spec_hash=" + oldHash, want: "Member draft changed", absent: "Current V2"},
		{name: "exact version", query: "definition=version&spec_hash=" + oldHash, want: "Executed V1", absent: "Current V2"},
		{name: "deployed", query: "definition=deployed&environment_slug=dev", want: "Executed V1", absent: "Current V2"},
		{name: "run ignores URL override", query: "definition=current&spec_hash=" + newHash, want: "Executed V1", absent: "Current V2", run: true},
		{name: "missing", query: "definition=version&spec_hash=" + oldHash, want: "missing", absent: "Current V2", status: 404},
		{name: "run version mismatch", query: "spec_hash=" + newHash, want: "does not match", absent: "Current V2", run: true, mismatch: true},
		{name: "run restricted", query: "spec_hash=" + newHash, want: "restricted", absent: "Current V2", run: true, status: 403},
		{name: "restricted", query: "definition=version&spec_hash=" + oldHash, want: "restricted", absent: "Current V2", status: 403},
		{name: "expired", query: "definition=version&spec_hash=" + oldHash, want: "expired", absent: "Current V2", status: 410},
		{name: "oversized", query: "definition=version&spec_hash=" + oldHash, want: "inspection limit", absent: "Current V2", status: 413},
		{name: "unsupported", query: "definition=version&spec_hash=" + oldHash, want: "unsupported", absent: "Current V2", status: 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/deployment") {
					assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineDeployment{CurrentVersionSpecHash: oldHash}))
					return
				}
				assert.True(t, strings.HasSuffix(r.URL.Path, "/versions/"+oldHash), r.URL.Path)
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"message":"sensitive provider error"}`))
					return
				}
				if tc.mismatch {
					assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineVersion{SpecHash: newHash, Spec: newSpec}))
					return
				}
				assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineVersion{SpecHash: oldHash, Spec: oldSpec}))
			}))
			defer apiServer.Close()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			app.Get("/inspect", func(c fiber.Ctx) error {
				if tc.run {
					_, inspection := router.loadPipelineRunLineage(c.Context(), "", "acme", "ops", api.PipelineRunRecord{Metadata: api.PipelineRunMetadata{PipelineSlug: "daily-sync", PipelineVersionSpecHash: oldHash}}, "", c.OriginalURL())
					c.SetContext(views.WithDefinitionInspection(c.Context(), inspection))
				} else {
					pipeline := &api.Pipeline{Slug: "daily-sync", Spec: newSpec, CurrentVersionSpecHash: &newHash, Draft: &api.PipelineDraft{Spec: newSpec, DraftSpecHash: newHash}}
					if tc.noCurrentHash {
						pipeline.CurrentVersionSpecHash = nil
					}
					router.loadDefinitionInspection(c, "acme", "ops", pipeline)
				}
				c.SetContext(views.WithEntityPageURL(c.Context(), "/orgs/acme/projects/ops/pipelines/daily-sync?"+tc.query+"&definition_view=serialized"))
				return views.DefinitionInspector("acme", "ops").Render(c.Context(), c.Response().BodyWriter())
			})
			resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/inspect?"+tc.query, nil))
			require.NoError(t, err)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Contains(t, string(body), tc.want)
			require.NotContains(t, string(body), tc.absent)
			require.NotContains(t, string(body), "sensitive provider error")
			if !tc.run {
				require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
			}
		})
	}
}
