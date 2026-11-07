package frontend

import (
	"context"
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
)

func TestRecencyUIRequestsExactScopeAndPreservesFailures(t *testing.T) {
	for _, operation := range []string{"read", "save", "conflict", "denied-read", "denied-write", "unavailable"} {
		t.Run(operation, func(t *testing.T) {
			mutations := 0
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/successful-run-recency") {
					assert.Equal(t, "/api/organizations/acme-hq/projects/ops/environments/dev/pipelines/orders/successful-run-recency", r.URL.Path)
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodPut {
						mutations++
						var body map[string]any
						if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						assert.InDelta(t, 4, body["expected_revision"], 0)
						assert.InDelta(t, 7200, body["maximum_age_seconds"], 0)
						if operation == "conflict" {
							w.WriteHeader(409)
							_, _ = io.WriteString(w, `{ "error": {"code":"CONFLICT","message":"Reload"}}`)
							return
						}
						if operation == "denied-write" {
							w.WriteHeader(403)
							_, _ = io.WriteString(w, `{ "error": {"code":"FORBIDDEN","message":"Forbidden"}}`)
							return
						}
					}
					if operation == "denied-read" {
						w.WriteHeader(403)
						_, _ = io.WriteString(w, `{ "error": {"code":"FORBIDDEN","message":"Forbidden"}}`)
						return
					}
					capability := "true"
					if operation == "unavailable" {
						capability = "false"
					}
					_, _ = io.WriteString(w, `{"capability_enabled":`+capability+`,"configured":true,"revision":5,"maximum_age_seconds":7200,"enabled":true,"deployment_revision":7,"version_spec_hash":"exact-current-version","success_run_slug":"retained-success","evaluated_at":"2026-10-22T12:00:00Z","outcome":"healthy","episodes":[]}`)
					return
				}
				if handleAlertsCommonTestAPI(w, r, "admin") {
					return
				}
				w.WriteHeader(404)
			}))
			defer backend.Close()
			store := session.NewStore()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			cookie := getAuthenticatedSessionCookie(t, app)
			method := http.MethodGet
			var body io.Reader
			if operation == "save" || operation == "conflict" || operation == "denied-write" {
				method = http.MethodPost
				body = strings.NewReader(url.Values{"expected_revision": {"4"}, "maximum_age_seconds": {"7200"}, "enabled": {"true"}}.Encode())
			}
			req := httptest.NewRequestWithContext(context.Background(), method, "/orgs/acme-hq/projects/ops/pipelines/orders/environments/dev/recency", body)
			req.Header.Set("Cookie", cookie)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if method == http.MethodPost {
				addCSRFToken(t, app, req)
			}
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			html, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			switch operation {
			case "save":
				require.Equal(t, 303, resp.StatusCode)
				require.Equal(t, "/orgs/acme-hq/projects/ops/pipelines/orders/environments/dev/recency", resp.Header.Get("Location"))
				require.Equal(t, 1, mutations)
			case "conflict":
				require.Equal(t, 409, resp.StatusCode)
				require.Contains(t, string(html), "Review the current revision")
				require.Contains(t, string(html), `name="expected_revision" value="5"`)
			case "denied-read", "denied-write":
				require.Equal(t, 403, resp.StatusCode)
			default:
				require.Equal(t, 200, resp.StatusCode)
				require.Contains(t, string(html), "exact-current-version")
				require.Contains(t, string(html), "retained-success")
				if operation == "unavailable" {
					require.NotContains(t, string(html), "Save expectation")
					require.NotContains(t, string(html), "Configure recency alerts")
				}
			}
		})
	}
}
