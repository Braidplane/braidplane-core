package frontend

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestPipelineExecutionControlRendersEvidenceAndProtectsResume(t *testing.T) {
	const path = "/orgs/acme-hq/projects/ops/pipelines/orders/environments/dev/execution-control"
	for _, scenario := range []string{"read", "resume", "revision conflict", "read only"} {
		t.Run(scenario, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/execution-control") {
					assert.Equal(t, "/api/organizations/acme-hq/projects/ops/environments/dev/pipelines/orders/execution-control", r.URL.Path)
					if r.Method == http.MethodPut {
						assert.NotEqual(t, "read only", scenario)
						var body api.PipelineExecutionControlUpdate
						assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
						assert.EqualValues(t, 4, body.ExpectedRevision)
						assert.False(t, body.Paused)
						if scenario == "revision conflict" {
							w.WriteHeader(http.StatusConflict)
							_, _ = io.WriteString(w, `{"error":{"code":"CONFLICT","message":"Reload status"}}`)
							return
						}
					}
					_, _ = io.WriteString(w, `{"paused":true,"revision":5,"drained":true,"queued_count":1,"active_count":0,"finished_count":1,"uncertain_count":1,"attempts":[{"run_slug":"held-orders","phase":"queued","status":"queued","source_type":"schedule","started_at":"2026-11-27T14:00:00Z"},{"run_slug":"recent-orders","phase":"finished","status":"failed","source_type":"schedule","started_at":"2026-11-27T14:00:00Z","outcome_uncertain":true,"known_completed_write":true}]}`)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/authorization/permissions") && r.URL.Query().Get("target_kind") == "deployment_targets" {
					assert.Equal(t, "orders", r.URL.Query().Get("target_slug"))
					assert.Equal(t, "dev", r.URL.Query().Get("environment_slug"))
					if scenario == "read only" {
						_, _ = io.WriteString(w, `{"permissions":[{"resource":"pipelines","action":"read"}]}`)
						return
					}
				}
				if !handleAlertsCommonTestAPI(w, r, "admin") {
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer backend.Close()
			store := session.NewStore()
			router, err := newHandlerTestRouter(backend.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			method := http.MethodGet
			var body io.Reader
			if scenario == "resume" || scenario == "revision conflict" {
				method = http.MethodPost
				body = strings.NewReader(url.Values{"expected_revision": {"4"}, "paused": {"false"}}.Encode())
			}
			req := httptest.NewRequestWithContext(t.Context(), method, path, body)
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if method == http.MethodPost {
				addCSRFToken(t, app, req)
			}
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			if scenario == "resume" {
				require.Equal(t, http.StatusSeeOther, resp.StatusCode)
				require.Equal(t, path, resp.Header.Get("Location"))
				return
			}
			html, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			if scenario == "revision conflict" {
				require.Equal(t, http.StatusConflict, resp.StatusCode)
				require.Contains(t, string(html), "Execution state changed")
				require.Contains(t, string(html), `name="expected_revision" value="5"`)
			} else {
				require.Equal(t, http.StatusOK, resp.StatusCode)
			}
			for _, text := range []string{"active attempts drained", "Held", "held-orders", "Write outcome uncertain", "Completed write recorded"} {
				require.Contains(t, string(html), text)
			}
			if scenario == "read only" {
				require.NotContains(t, string(html), "Resume execution")
			}
		})
	}
}
