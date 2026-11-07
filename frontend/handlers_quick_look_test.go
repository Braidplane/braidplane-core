package frontend

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"
)

func TestQuickLookLoadsOnlySelectedResource(t *testing.T) {
	for _, entity := range []struct{ name, path, target, resource, body, title string }{
		{"project", "/organizations/acme/projects?project_slug=ops&environment_slug=dev", "project-quick-look", "/api/organizations/acme/projects/ops", `{"slug":"ops","name":"Operations"}`, "Operations"},
		{"source", "/orgs/acme/data-sources?selected=warehouse&filter=with_queries&environment_slug=dev", "data-source-side-panel", "/api/organizations/acme/data-sources/warehouse", `{"slug":"warehouse","name":"Warehouse","type":"postgres","config":{},"is_active":true}`, "Warehouse"},
		{"pipeline", "/orgs/acme/projects/ops/pipelines?pipeline_slug=daily&environment_slug=dev&q=daily", "pipeline-detail-panel-slot", "/api/organizations/acme/projects/ops/pipelines/daily", `{"slug":"daily","name":"Daily sync","sync_status":"synced","has_draft":false,"pipeline_spec":{"metadata":{"slug":"daily","name":"Daily sync"},"steps":[]}}`, "Daily sync"},
	} {
		t.Run(entity.name, func(t *testing.T) {
			for _, denied := range []bool{false, true} {
				t.Run(map[bool]string{false: "allowed", true: "denied"}[denied], func(t *testing.T) {
					var resourceCalls atomic.Int32
					apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						switch r.URL.Path {
						case entity.resource:
							resourceCalls.Add(1)
							if denied {
								w.WriteHeader(http.StatusForbidden)
								_, _ = io.WriteString(w, `{"error":{"code":"FORBIDDEN","message":"Permission required"}}`)
								return
							}
							_, _ = io.WriteString(w, entity.body)
						case "/api/organizations/acme/projects/ops/environments":
							_, _ = io.WriteString(w, `{"project_environments":[{"environment_slug":"dev","environment_name":"Development","is_default":true}]}`)
						case "/api/organizations/acme/projects/ops/environments/dev/pipeline-deployments":
							_, _ = io.WriteString(w, `{"deployments":[]}`)
						case "/api/organizations/acme/projects/ops/environments/dev/runs":
							if r.URL.Query().Get("pipeline_slug") != "daily" {
								t.Errorf("quick look requested unrelated pipeline runs")
							}
							_, _ = io.WriteString(w, `{"runs":[],"has_more":false}`)
						case "/api/organizations/acme/projects/ops/environments/dev/pipeline-validation-results":
							_, _ = io.WriteString(w, `{"validation_results":[],"has_more":false}`)
						case "/api/organizations":

							_, _ = io.WriteString(w, `{"organizations":[{"slug":"acme","name":"Acme","member_role":"admin"}]}`)
						case "/api/organizations/acme/roles/admin":
							_, _ = io.WriteString(w, `{"slug":"admin","permissions":[]}`)
						default:
							t.Errorf("quick look loaded unrelated API %s", r.URL.Path)
							w.WriteHeader(http.StatusNotFound)
						}
					}))
					defer apiServer.Close()
					store := session.NewStore()
					router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
					require.NoError(t, err)
					app := fiber.New()
					registerTestSessionRoute(app, store)
					router.RegisterRoutes(app, CommunityPresentation{})
					req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, entity.path, nil)
					req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
					req.Header.Set("HX-Request", "true")
					req.Header.Set("HX-Target", entity.target)
					resp, err := app.Test(req)
					require.NoError(t, err)
					defer func() { _ = resp.Body.Close() }()
					body, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, resp.StatusCode)
					require.Equal(t, "HX-Request, HX-Target, HX-History-Restore-Request", resp.Header.Get("Vary"))
					require.Contains(t, string(body), `id="`+entity.target+`"`)
					require.NotContains(t, string(body), `id="main-content"`)
					require.EqualValues(t, 1, resourceCalls.Load())
					if denied {
						require.NotContains(t, string(body), entity.title)
					} else {
						require.Contains(t, string(body), entity.title)
						require.Contains(t, string(body), "Open full page")
						require.Contains(t, string(body), "environment_slug=dev")
						require.Contains(t, string(body), "list_return=")
					}
				})
			}
		})
	}
}
